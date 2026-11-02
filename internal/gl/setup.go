package gl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Calls used by `gl-issues doctor` to check the setup. All are read-only.

// Group is a GitLab group.
type Group struct {
	ID       int    `json:"id"`
	FullPath string `json:"full_path"`
}

func (g *Group) sanitize() { g.FullPath = SanitizeLine(g.FullPath) }

// TokenInfo describes the token a request was made with.
type TokenInfo struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
}

func (t *TokenInfo) sanitize() {
	t.Name, t.ExpiresAt = SanitizeLine(t.Name), SanitizeLine(t.ExpiresAt)
	for i := range t.Scopes {
		t.Scopes[i] = SanitizeLine(t.Scopes[i])
	}
}

// The calls below cap glab's output as API does: past MaxOutput glab is
// killed through its own context (kill) and the call fails with
// ErrTooLarge.

// Version returns the first line of `glab --version`, sanitized. On
// failure the error is what glab wrote to stderr, else to stdout.
func (c Client) Version(ctx context.Context) (string, error) {
	kctx, kill := context.WithCancel(ctx)
	defer kill()
	cmd := exec.CommandContext(kctx, "glab", "--version")
	cmd.Env = glabEnv(os.Environ(), c.Host)
	cmd.WaitDelay = waitDelay
	out, errb := &cappedWriter{limit: MaxOutput, kill: kill}, &cappedWriter{limit: MaxOutput, kill: kill}
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	if out.over || errb.over {
		return "", fmt.Errorf("glab --version: %w", ErrTooLarge)
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrNoGlab
		}
		msg := cleanErr(errb.buf.String())
		if msg == "" {
			msg = cleanErr(out.buf.String())
		}
		return "", fmt.Errorf("glab --version: %s", strings.TrimSpace(msg+" "+err.Error()))
	}
	line, _, _ := strings.Cut(strings.TrimSpace(out.buf.String()), "\n")
	return Sanitize(strings.TrimSpace(line)), nil
}

// AuthStatus is what `glab auth status` says about the login for a host:
// where the token is kept and the protocol of API calls. Never the token.
type AuthStatus struct {
	Storage  string // StorageKeyring, StoragePlaintext, or "" if glab did not say
	Protocol string // "https", "http", or "" if glab did not say
}

// Where glab keeps a token.
const (
	StorageKeyring   = "keyring"   // the OS keyring (Secret Service, macOS Keychain)
	StoragePlaintext = "plaintext" // glab's config file, in plain text
)

var (
	// "✓ Token found in operating system keyring: ****" or "… in
	// configuration file (plaintext): ****"
	tokenFoundLine = regexp.MustCompile(`(?i)\btoken found in ([^:]+):`)
	// "✓ API calls for gitlab.example.com are made over https protocol."
	apiProtocolLine = regexp.MustCompile(`(?i)\bAPI calls for (\S+) are made over (\w+) protocol`)
	// "✓ Logged in to gitlab.example.com as alice"
	loggedInLine = regexp.MustCompile(`(?i)\bLogged in to (\S+) as\b`)
)

// AuthStatus runs `glab auth status --hostname <host>` and reads where
// glab keeps the token and which protocol API calls use. Only those two
// facts are taken from the output; nothing of it is returned or printed,
// so no token text can leak. glab exits non-zero when the login does not
// work; what it printed is still read.
func (c Client) AuthStatus(ctx context.Context) (AuthStatus, error) {
	var st AuthStatus
	kctx, kill := context.WithCancel(ctx)
	defer kill()
	cmd := exec.CommandContext(kctx, "glab", "auth", "status", "--hostname", c.Host)
	cmd.Env = glabEnv(os.Environ(), c.Host)
	cmd.WaitDelay = waitDelay
	out := &cappedWriter{limit: MaxOutput, kill: kill}
	cmd.Stdout, cmd.Stderr = out, out // glab writes the status to stderr
	err := cmd.Run()
	switch {
	case out.over:
		return st, fmt.Errorf("glab auth status: %w", ErrTooLarge)
	case errors.Is(err, exec.ErrNotFound):
		return st, ErrNoGlab
	case ctx.Err() != nil:
		return st, fmt.Errorf("glab auth status: %w", ctx.Err())
	}
	st = parseAuthStatus(out.buf.String(), c.Host)
	if st == (AuthStatus{}) && err != nil {
		return st, fmt.Errorf("glab auth status: %w", err) // the exit status only, never the output
	}
	return st, nil
}

// parseAuthStatus reads the token storage and API protocol for host from
// `glab auth status` output. The output is one section per host, headed
// by the host name alone; with --hostname only that host's, but should
// glab ever print more, the token line (which names no host) counts only
// inside the requested host's section: one entered by a line naming the
// host (the header, "Logged in to <host>", "API calls for <host>") and
// left by one naming another.
func parseAuthStatus(out, host string) AuthStatus {
	var st AuthStatus
	inHost := false
	for _, line := range strings.Split(out, "\n") {
		named := ""
		switch t := strings.TrimSpace(line); t {
		case "":
			continue
		case line: // not indented: a section header
			named = t
		default:
			if m := loggedInLine.FindStringSubmatch(line); m != nil {
				named = m[1]
			} else if m := apiProtocolLine.FindStringSubmatch(line); m != nil {
				named = m[1]
			}
		}
		if named != "" {
			inHost = strings.EqualFold(named, host)
		}
		if m := tokenFoundLine.FindStringSubmatch(line); m != nil && inHost && st.Storage == "" {
			switch where := strings.ToLower(m[1]); {
			case strings.Contains(where, "keyring") || strings.Contains(where, "keychain"):
				st.Storage = StorageKeyring
			case strings.Contains(where, "plaintext") || strings.Contains(where, "config"):
				st.Storage = StoragePlaintext
			}
		}
		if m := apiProtocolLine.FindStringSubmatch(line); m != nil && strings.EqualFold(m[1], host) {
			st.Protocol = strings.ToLower(m[2])
		}
	}
	return st
}

// Group fetches a group by its full path.
func (c Client) Group(ctx context.Context, path string) (Group, error) {
	var g Group
	b, err := c.API(ctx, "GET", "groups/"+PathID(path), nil, false)
	if err != nil {
		return g, err
	}
	return g, decodeOne(b, &g)
}

// AccessLevel returns a user's access level in a group, inherited
// membership included (10 guest … 50 owner).
func (c Client) AccessLevel(ctx context.Context, group string, userID int) (int, error) {
	var m struct {
		AccessLevel int `json:"access_level"`
	}
	b, err := c.API(ctx, "GET", fmt.Sprintf("groups/%s/members/all/%d", PathID(group), userID), nil, false)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(b, &m); err != nil { // a number only: nothing to sanitize
		return 0, err
	}
	return m.AccessLevel, nil
}

// TokenSelf describes the token in use (GET personal_access_tokens/self).
// GitLab answers only for personal, project and group access tokens.
func (c Client) TokenSelf(ctx context.Context) (TokenInfo, error) {
	var t TokenInfo
	b, err := c.API(ctx, "GET", "personal_access_tokens/self", nil, false)
	if err != nil {
		return t, err
	}
	return t, decodeOne(b, &t)
}

// AccessLevelName names a GitLab access level.
func AccessLevelName(level int) string {
	switch level {
	case 5:
		return "minimal access"
	case 10:
		return "guest"
	case 15:
		return "planner"
	case 20:
		return "reporter"
	case 30:
		return "developer"
	case 40:
		return "maintainer"
	case 50:
		return "owner"
	}
	return fmt.Sprintf("access level %d", level)
}
