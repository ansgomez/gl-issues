package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"gl-issues/internal/config"
	"gl-issues/internal/gl"
)

// `gl-issues doctor`: a read-only check of the setup, one line per check.

// doctorTimeout bounds each check.
const doctorTimeout = 20 * time.Second

// doctorClient is what doctor asks GitLab (via glab); gl.Client has it.
type doctorClient interface {
	Version(ctx context.Context) (string, error)
	Me(ctx context.Context) (gl.User, error)
	Group(ctx context.Context, path string) (gl.Group, error)
	AccessLevel(ctx context.Context, group string, userID int) (int, error)
	TokenSelf(ctx context.Context) (gl.TokenInfo, error)
	AuthStatus(ctx context.Context) (gl.AuthStatus, error)
}

type doctor struct {
	out      io.Writer
	notes    []renameNote // left over from the rename of gitissue
	c        doctorClient
	timeout  time.Duration
	problems int
}

const (
	markOK      = "✓"
	markWarn    = "!"
	markProblem = "✗"
)

// report prints one check, and for a warning or problem its fix. Text
// can hold server data, so it is sanitized.
func (d *doctor) report(mark, text, hint string) {
	fmt.Fprintf(d.out, "%s %s\n", mark, gl.Sanitize(text))
	if mark != markOK && hint != "" {
		fmt.Fprintf(d.out, "  → %s\n", gl.Sanitize(hint))
	}
	if mark == markProblem {
		d.problems++
	}
}

func (d *doctor) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d.timeout)
}

// networkErr matches errors that mean the host could not be reached.
var networkErr = regexp.MustCompile(`(?i)timed out|timeout|no such host|connection refused|connection reset|` +
	`network is unreachable|no route to host|dial tcp|lookup |tls handshake|certificate`)

const networkHint = "check the host name, and whether the host is only reachable over a VPN or " +
	"from an internal network, or a firewall, proxy or DNS blocks it"

// runDoctor checks the setup and reports whether there was no problem
// (warnings are fine). cfg is the loaded config (with flags applied),
// path the config file it was loaded from or would be, loadErr any error
// loading it. notes are warnings about the old name (renameNotes).
func runDoctor(out io.Writer, c doctorClient, cfg config.Config, path string, loadErr error, notes []renameNote) bool {
	d := &doctor{out: out, c: c, timeout: doctorTimeout, notes: notes}
	d.run(cfg, path, loadErr)
	return d.problems == 0
}

func (d *doctor) run(cfg config.Config, path string, loadErr error) {
	for _, n := range d.notes {
		d.report(markWarn, n.text, n.hint)
	}
	switch {
	case loadErr != nil:
		// `gl-issues config` would fail the same way, so no hint at it.
		d.report(markProblem, "config: "+loadErr.Error(), "fix what it names")
		d.report(markWarn, "host, group and login: not checked until the config error is fixed", "")
		d.checkGlab()
		return
	case cfg.Path == "":
		d.report(markWarn, "config: no file at "+path+"; using defaults, environment and flags",
			"create it with `gl-issues init`")
	default:
		d.report(markOK, "config: "+cfg.Path, "")
	}
	if cfg.FileToken {
		d.report(markWarn, "config: the token in "+cfg.Path+" is ignored (gl-issues uses glab's login)",
			"hand it to glab and remove it from the file: gl-issues migrate-token")
	}
	if cfg.Host == "" {
		d.report(markProblem, "host: not set", "set host in "+path+" (bare host name, optional :port), or pass -host")
	} else {
		d.report(markOK, "host: "+cfg.Host, "")
	}
	if cfg.Group == "" {
		d.report(markProblem, "group: not set", "set group in "+path+" (its full path, as in the group's URL), or pass -g")
	}

	if !d.checkGlab() || cfg.Host == "" {
		return // without a host glab would fall back to gitlab.com
	}

	loginHint := "run " + gl.LoginCommand(cfg.Host)
	if cfg.FileToken {
		loginHint = "run gl-issues migrate-token (hands the token in " + cfg.Path + " to glab), or " + gl.LoginCommand(cfg.Host)
	}
	ctx, cancel := d.ctx()
	me, err := d.c.Me(ctx)
	cancel()
	var apiErr *gl.APIError
	switch {
	case err != nil && networkErr.MatchString(err.Error()):
		d.report(markProblem, "cannot reach "+cfg.Host+": "+err.Error(), networkHint)
		return
	case errors.As(err, &apiErr) && apiErr.Unauthorized:
		// The reason without gl's own hint: loginHint says what to do.
		d.report(markProblem, "not logged in to "+cfg.Host+": "+apiErr.Reason+" ("+apiErr.Request+")", loginHint)
		return
	case err != nil:
		d.report(markProblem, "cannot check the login on "+cfg.Host+": "+err.Error(), "")
		return
	}
	d.report(markOK, "logged in to "+cfg.Host+" as @"+me.Username, "")
	d.checkTokenStorage(cfg.Host)

	if cfg.Group != "" {
		d.checkGroup(cfg.Host, cfg.Group, me)
	}
	d.checkScopes()
}

// checkGlab reports glab's version; false if glab cannot run.
func (d *doctor) checkGlab() bool {
	ctx, cancel := d.ctx()
	v, err := d.c.Version(ctx)
	cancel()
	switch {
	case errors.Is(err, gl.ErrNoGlab):
		d.report(markProblem, "glab: not found in PATH", "install glab (https://gitlab.com/gitlab-org/cli; Linux: docs/linux.md), or add its directory to PATH")
		return false
	case err != nil:
		d.report(markProblem, "glab: "+err.Error(), "reinstall glab")
		return false
	}
	d.report(markOK, "glab: "+v, "")
	return true
}

// checkTokenStorage reports where glab keeps the token for host (the OS
// keyring, or its config file in plain text) and warns when API calls go
// over http. It reads `glab auth status`, of which only those two facts
// are used: no token text is ever printed.
func (d *doctor) checkTokenStorage(host string) {
	ctx, cancel := d.ctx()
	st, err := d.c.AuthStatus(ctx)
	cancel()
	const check = "see glab auth status --hostname "
	switch {
	case err != nil:
		d.report(markWarn, "token storage unknown: "+err.Error(), check+host)
	case st.Storage == gl.StorageKeyring:
		d.report(markOK, "glab keeps the token for "+host+" in the OS keyring", "")
	case st.Storage == gl.StoragePlaintext:
		d.report(markWarn, "glab keeps the token for "+host+" in its config file, in plain text",
			"with glab 1.123+ and an unlocked OS keyring, log in again to move it there: "+gl.LoginCommand(host))
	default:
		d.report(markWarn, "token storage unknown: glab auth status does not say", check+host)
	}
	if st.Protocol == "http" {
		d.report(markWarn, "glab makes API calls to "+host+" over http, not https: the token is sent unencrypted",
			"glab config set api_protocol https --host "+host)
	}
}

func (d *doctor) checkGroup(host, group string, me gl.User) {
	ctx, cancel := d.ctx()
	defer cancel()
	g, err := d.c.Group(ctx, group)
	switch {
	case err != nil && networkErr.MatchString(err.Error()):
		d.report(markProblem, "group "+group+": "+err.Error(), networkHint)
		return
	case err != nil:
		d.report(markProblem, err.Error(), "set group to the path from the group's URL (what follows https://"+
			host+"/), not its display name, and check that @"+me.Username+" can open it in the browser")
		return
	}
	text := "group " + g.FullPath + " is visible"
	if level, err := d.c.AccessLevel(ctx, group, me.ID); err == nil && level > 0 {
		text += ", you are " + gl.AccessLevelName(level)
	}
	d.report(markOK, text, "")
}

const scopeHint = "browsing works; comment/close/assign/describe/new need the api scope: create a legacy " +
	"personal access token with api, then glab auth login"

func (d *doctor) checkScopes() {
	ctx, cancel := d.ctx()
	defer cancel()
	t, err := d.c.TokenSelf(ctx)
	if err != nil {
		d.report(markWarn, "token scopes unknown: "+err.Error(),
			"browsing needs read_api and changes need api; check the token's scopes in GitLab (Access tokens)")
		return
	}
	scopes := strings.Join(t.Scopes, ", ")
	switch {
	case slices.Contains(t.Scopes, "api"):
		d.report(markOK, "token scopes: "+scopes, "")
	case slices.Contains(t.Scopes, "read_api"):
		d.report(markWarn, "token scopes: "+scopes+" (read-only)", scopeHint)
	default:
		d.report(markProblem, "token scopes: "+scopes+" (neither api nor read_api)",
			"create a legacy personal access token with api (or read_api to only look), then glab auth login")
	}
}
