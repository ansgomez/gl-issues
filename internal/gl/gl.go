// Package gl talks to the GitLab REST API by shelling out to `glab api`.
// gl-issues holds no token: glab uses its own stored login for the host
// (`glab auth login`, kept in the OS keyring by default).
package gl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// User is a GitLab user as embedded in issues and member lists.
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// Issue holds the fields gl-issues shows and edits.
type Issue struct {
	ID          int       `json:"id"`
	IID         int       `json:"iid"`
	ProjectID   int       `json:"project_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	State       string    `json:"state"` // "opened" or "closed"
	DueDate     string    `json:"due_date"`
	Labels      []string  `json:"labels"`
	Assignees   []User    `json:"assignees"`
	Author      User      `json:"author"`
	WebURL      string    `json:"web_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Notes       int       `json:"user_notes_count"`
	References  struct {
		Full string `json:"full"` // "group/project#12"
	} `json:"references"`

	// RawDescription is Description exactly as the server sent it, before
	// sanitizing. Use it to detect changes (sanitizing can map different
	// texts to the same string), never to print.
	RawDescription string `json:"-"`
}

// Note is an issue comment.
type Note struct {
	ID        int       `json:"id"`
	Body      string    `json:"body"`
	Author    User      `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	System    bool      `json:"system"` // recorded by GitLab for a change (labels, due date, …)
}

// Ref returns the full reference, e.g. "group/project#12".
func (i Issue) Ref() string { return i.References.Full }

// Project returns the project path part of the reference.
func (i Issue) Project() string {
	p, _, _ := strings.Cut(i.References.Full, "#")
	return p
}

// AssigneeNames returns the assignees' usernames.
func (i Issue) AssigneeNames() []string {
	names := make([]string, len(i.Assignees))
	for k, u := range i.Assignees {
		names[k] = u.Username
	}
	return names
}

// Due parses DueDate; ok is false when there is none.
func (i Issue) Due() (t time.Time, ok bool) {
	if i.DueDate == "" {
		return t, false
	}
	t, err := time.ParseInLocation(time.DateOnly, i.DueDate, time.Local)
	return t, err == nil
}

// DaysLeft returns whole days from today until the due date (negative when
// overdue). ok is false when there is no due date.
func (i Issue) DaysLeft(now time.Time) (int, bool) {
	d, ok := i.Due()
	if !ok {
		return 0, false
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	return int(math.Round(d.Sub(today).Hours() / 24)), true // round: DST days are 23h/25h
}

// Client runs glab against one host, with glab's stored login for it.
type Client struct {
	Host string
}

// ErrNoGlab is returned when the glab binary is not installed.
var ErrNoGlab = errors.New("glab not found in PATH (install: https://gitlab.com/gitlab-org/cli)")

// waitDelay bounds how long API waits, after glab has exited or been
// killed, for its output pipes to close. A helper glab started (a pager,
// a browser) could otherwise inherit them and hold a call open forever.
var waitDelay = 5 * time.Second

// MaxOutput caps what one glab call may write on stdout or on stderr.
// Real answers stay far below it: a group's whole issue list (every page
// of 100 with --paginate) is a few KB per issue, so this holds tens of
// thousands of issues. Past it glab is killed and the call fails with
// ErrTooLarge, rather than buffering whatever a server sends.
var MaxOutput int64 = 128 << 20

// ErrTooLarge is returned when glab wrote more than MaxOutput bytes.
var ErrTooLarge = errors.New("response too large")

// addPage adds one page of a fetch that pages by itself to total, and
// fails once the pages together pass MaxOutput: the same cap a single
// --paginate call has, rather than one per page.
func addPage(total *int64, page []byte, request string) error {
	*total += int64(len(page))
	if *total > MaxOutput {
		return fmt.Errorf("%w (over %d MB in all; %s)", ErrTooLarge, MaxOutput>>20, request)
	}
	return nil
}

// cappedWriter buffers at most limit bytes. The first write past that
// calls kill (glab is stopped, not read forever) and fails.
type cappedWriter struct {
	buf   bytes.Buffer
	limit int64
	kill  func()
	over  bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if int64(w.buf.Len())+int64(len(p)) > w.limit {
		w.over = true
		w.kill()
		return 0, ErrTooLarge
	}
	return w.buf.Write(p)
}

// API performs one request and returns the raw response body. body, if not
// nil, is sent as JSON.
func (c Client) API(ctx context.Context, method, path string, body any, paginate bool) ([]byte, error) {
	args := []string{"api", "--method", method}
	if c.Host != "" && HostName(c.Host) == c.Host {
		// glab refuses a port in --hostname ("invalid hostname"); for
		// host:port, GITLAB_HOST (set by glabEnv) alone selects the host.
		args = append(args, "--hostname", c.Host)
	}
	if paginate {
		args = append(args, "--paginate")
	}
	var stdin io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		stdin = bytes.NewReader(b)
		args = append(args, "--header", "Content-Type: application/json", "--input", "-")
	}
	args = append(args, path)

	// Too much output kills glab through its own context, so the caller's
	// ctx still tells a timeout from an oversized answer.
	kctx, kill := context.WithCancel(ctx)
	defer kill()
	cmd := exec.CommandContext(kctx, "glab", args...)
	cmd.Stdin = stdin
	cmd.Env = glabEnv(os.Environ(), c.Host)
	cmd.WaitDelay = waitDelay
	out := &cappedWriter{limit: MaxOutput, kill: kill}
	errb := &cappedWriter{limit: MaxOutput, kill: kill}
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		// glab itself exited fine; only a child it left behind still held
		// the pipes. What glab wrote is complete.
		err = nil
	}
	if out.over || errb.over {
		return nil, fmt.Errorf("%w (over %d MB; %s %s)", ErrTooLarge, MaxOutput>>20, method, path)
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrNoGlab
		}
		// glab was killed because the context ended: say so, rather than
		// "signal: killed".
		if cerr := ctx.Err(); cerr != nil {
			if errors.Is(cerr, context.DeadlineExceeded) {
				return nil, fmt.Errorf("timed out (%s %s): %w", method, path, cerr)
			}
			return nil, fmt.Errorf("%w (%s %s)", cerr, method, path)
		}
		e := c.explain(method, path, errb.buf.String(), out.buf.String(), err)
		return nil, &e
	}
	return out.buf.Bytes(), nil
}

// APIError is a failed glab call. Its text is the reason first, then any
// hint, then the request: "401 Unauthorized (check …) (GET user)".
type APIError struct {
	Reason       string // what went wrong, cleaned for the terminal
	Hint         string // what to do about it; "" when there is nothing specific
	Request      string // e.g. "GET user"
	Unauthorized bool   // HTTP 401: no valid login or token for the host
}

func (e *APIError) Error() string {
	s := e.Reason
	if e.Hint != "" {
		s += " (" + e.Hint + ")"
	}
	return s + " (" + e.Request + ")"
}

// explain turns a failed glab call into a reason a person can act on.
func (c Client) explain(method, path, stderr, stdout string, err error) APIError {
	msg := cleanErr(stderr)
	if msg == "" {
		msg = cleanErr(stdout)
	}
	if msg == "" {
		msg = err.Error()
	}
	e := APIError{Reason: msg, Request: method + " " + path}
	switch {
	case insufficientScope(stderr, stdout):
		e.Reason = "the token needs the api scope: run " + LoginCommand(c.Host) + " with an api-scope (legacy) token"
	case unauthorized.MatchString(msg):
		e.Unauthorized = true
		e.Hint = "check `glab auth status --hostname " + c.Host + "`"
	case method == "GET" && notFound.MatchString(msg) && strings.HasPrefix(path, "groups/"):
		group, _, _ := strings.Cut(strings.TrimPrefix(path, "groups/"), "/")
		if g, uerr := url.PathUnescape(group); uerr == nil {
			group = g
		}
		e.Reason = fmt.Sprintf("group %q not found, or you have no access", group)
	}
	return e
}

// HostName returns host without its port: the name glab's --hostname
// takes (it refuses a port there).
func HostName(host string) string {
	name, _, _ := strings.Cut(host, ":")
	return name
}

// LoginCommand is the glab command that logs in to host. host is passed
// unchanged, port included: glab auth login keys the stored login by
// --hostname, and gl-issues selects it by GITLAB_HOST=host (with the port).
// A login made with the bare name and --api-host host:port would be
// stored under the bare name, which gl-issues never looks up.
func LoginCommand(host string) string {
	return "glab auth login --hostname " + host
}

// SameHost reports whether u points at host, given with or without a
// port: a URL with a port matches only that host:port, or host given
// without one.
func SameHost(u *url.URL, host string) bool {
	return strings.EqualFold(u.Host, host) || strings.EqualFold(u.Hostname(), host)
}

// tokenVars are the environment variables glab reads a token from. None
// reaches any program gl-issues runs: see glabEnv and WithoutTokens.
var tokenVars = []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN", "CI_JOB_TOKEN"}

// droppedVars are other inherited variables that change which host glab
// talks to, how it logs in, or make it print debug output (which can
// include request headers, and so the token, in the error text gl-issues
// shows).
var droppedVars = []string{
	"GITLAB_HOST", "GITLAB_URI", "GITLAB_URL", "GL_HOST",
	"GITLAB_API_HOST", "GITLAB_API_PROTOCOL", // would redirect the API, or downgrade it to http
	"GLAB_ENABLE_CI_AUTOLOGIN", "DEBUG",
}

// forcedVars are set for every glab run, replacing any inherited value:
// no colour, no prompts, and no update check, telemetry or "what's new"
// banner (each of those would be extra traffic or output nobody asked for).
var forcedVars = []string{
	"NO_COLOR=1", "GLAB_NO_PROMPT=1", "GLAB_CHECK_UPDATE=false",
	"GLAB_SEND_TELEMETRY=false", "GLAB_SHOW_WHATS_NEW=false",
}

// tokenVar reports whether k is one of tokenVars.
func tokenVar(k string) bool {
	for _, t := range tokenVars {
		if k == t {
			return true
		}
	}
	return false
}

// WithoutTokens returns env without the token variables (tokenVars): the
// environment for the programs gl-issues runs besides glab, the editor
// and the browser opener, which have no use for a token.
func WithoutTokens(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); !tokenVar(k) {
			out = append(out, kv)
		}
	}
	return out
}

// dropVar reports whether an inherited variable must not reach glab.
func dropVar(k string) bool {
	if tokenVar(k) || strings.HasPrefix(k, "GLAB_DEBUG") { // GLAB_DEBUG, GLAB_DEBUG_HTTP, ...
		return true
	}
	for _, f := range forcedVars {
		if name, _, _ := strings.Cut(f, "="); k == name {
			return true
		}
	}
	for _, t := range droppedVars {
		if k == t {
			return true
		}
	}
	return false
}

// glabEnv builds the environment for glab from base. Inherited token
// variables are always dropped, so glab uses only its own stored login
// for host, never a token meant for another host. Host overrides and
// debug switches (see droppedVars) are dropped too, and forcedVars always
// set.
func glabEnv(base []string, host string) []string {
	env := make([]string, 0, len(base)+len(forcedVars)+1)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !dropVar(k) {
			env = append(env, kv)
		}
	}
	env = append(env, forcedVars...)
	if host != "" {
		env = append(env, "GITLAB_HOST="+host)
	}
	return env
}

// unauthorized matches glab's report of an HTTP 401 response, not any
// "401" in the text (an issue number, a path).
var unauthorized = regexp.MustCompile(`\bHTTP 401\b|\b401 Unauthorized\b|\bUnauthenticated\b`)

// notFound matches glab's report of an HTTP 404 response.
var notFound = regexp.MustCompile(`\bHTTP 404\b|\b404 Not Found\b|\b404 Group Not Found\b`)

// scopeLine matches glab's own report of a 403 for a token without the
// scope ("glab: HTTP 403: insufficient_scope"), never the word elsewhere.
var scopeLine = regexp.MustCompile(`(?m)^glab: (?:HTTP 403\b[^\n]*\b)?insufficient_scope\b`)

// insufficientScope reports whether a failed call was refused for a token
// without the needed scope. GitLab answers with the JSON body
// {"error":"insufficient_scope",…}, which glab prints on stdout, saying
// only "HTTP 403" (or the error) on stderr. Only those shapes count: the
// words of an issue or a note in a response body do not.
func insufficientScope(stderr, stdout string) bool {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(stdout), &body) == nil && body.Error == "insufficient_scope" {
		return true
	}
	return scopeLine.MatchString(stderr)
}

// cleanErr squeezes glab's boxed, padded error output into one line and
// strips terminal control sequences from it.
func cleanErr(s string) string {
	var parts []string
	for _, l := range strings.Split(Sanitize(s), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && l != "ERROR" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// MaxItems caps the items of one list response: MaxOutput alone would
// let tiny objects (`{},{},…`) decode into gigabytes of structs.
var MaxItems = 100000

// decodeList decodes a response that is a JSON array, or several arrays
// back to back as produced by `glab api --paginate`, one item at a time
// up to MaxItems. Every string from the server is sanitized for terminal
// output.
func decodeList[T any, P interface {
	*T
	sanitizer
}](b []byte) ([]T, error) {
	var all []T
	dec := json.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			for i := range all {
				P(&all[i]).sanitize()
			}
			return all, nil
		}
		if err != nil {
			return nil, err
		}
		if tok == nil { // null: an empty page
			continue
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			// A delimiter, or else the token's type only: a string from
			// the server would go into the error unsanitized.
			if ok {
				return nil, fmt.Errorf("want a JSON array, got %q", string(d))
			}
			return nil, fmt.Errorf("want a JSON array, got %T", tok)
		}
		for dec.More() {
			if len(all) == MaxItems {
				return nil, fmt.Errorf("more than %d items: %w", MaxItems, ErrTooLarge)
			}
			var v T
			if err := dec.Decode(&v); err != nil {
				return nil, err
			}
			all = append(all, v)
		}
		if _, err := dec.Token(); err != nil { // the closing ]
			return nil, err
		}
	}
}

// decodeOne decodes a single JSON object and sanitizes its strings.
func decodeOne(b []byte, v sanitizer) error {
	if err := json.Unmarshal(b, v); err != nil {
		return err
	}
	v.sanitize()
	return nil
}

// PathID URL-encodes a project or group path for use as an API :id.
// ":" is escaped too, so glab never expands it as a placeholder
// (":id", ":fullpath", ...).
func PathID(path string) string { return strings.ReplaceAll(url.PathEscape(path), ":", "%3A") }

// IssueStates are the issue lists GroupIssues can fetch, in the order the
// dashboard cycles through them.
var IssueStates = []string{"opened", "closed", "all"}

// GroupIssues lists the issues of a group and its subgroups. state is one
// of IssueStates.
func (c Client) GroupIssues(ctx context.Context, group, state string) ([]Issue, error) {
	q := url.Values{"state": {state}, "per_page": {"100"}, "order_by": {"updated_at"}}
	b, err := c.API(ctx, "GET", "groups/"+PathID(group)+"/issues?"+q.Encode(), nil, true)
	if err != nil {
		return nil, err
	}
	return decodeList[Issue](b)
}

// Members lists everyone with access to the group, including inherited
// members.
func (c Client) Members(ctx context.Context, group string) ([]User, error) {
	b, err := c.API(ctx, "GET", "groups/"+PathID(group)+"/members/all?per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	return decodeList[User](b)
}

// Me returns the authenticated user.
func (c Client) Me(ctx context.Context) (User, error) {
	var u User
	b, err := c.API(ctx, "GET", "user", nil, false)
	if err != nil {
		return u, err
	}
	return u, decodeOne(b, &u)
}

// UserByName looks up a user by exact username.
func (c Client) UserByName(ctx context.Context, username string) (User, error) {
	b, err := c.API(ctx, "GET", "users?username="+url.QueryEscape(username), nil, false)
	if err != nil {
		return User{}, err
	}
	us, err := decodeList[User](b)
	if err != nil {
		return User{}, err
	}
	if len(us) == 0 {
		return User{}, fmt.Errorf("no user %q", username)
	}
	return us[0], nil
}

func issuePath(project string, iid int) string {
	return fmt.Sprintf("projects/%s/issues/%d", PathID(project), iid)
}

// GetIssue fetches one issue by project path and iid.
func (c Client) GetIssue(ctx context.Context, project string, iid int) (Issue, error) {
	var is Issue
	b, err := c.API(ctx, "GET", issuePath(project, iid), nil, false)
	if err != nil {
		return is, err
	}
	return is, decodeOne(b, &is)
}

// Notes returns the human (non-system) comments on an issue, oldest first.
func (c Client) Notes(ctx context.Context, project string, iid int) ([]Note, error) {
	b, err := c.API(ctx, "GET", issuePath(project, iid)+"/notes?sort=asc&order_by=created_at&per_page=100", nil, true)
	if err != nil {
		return nil, err
	}
	all, err := decodeList[Note](b)
	if err != nil {
		return nil, err
	}
	notes := all[:0]
	for _, n := range all {
		if !n.System {
			notes = append(notes, n)
		}
	}
	return notes, nil
}

// RecentNotesPage is how many notes RecentNotes fetches.
const RecentNotesPage = 20

// RecentNotes returns the most recently updated notes on an issue, newest
// first, system notes included (GitLab records label, due date,
// description and assignee changes as system notes by whoever made them):
// one page of RecentNotesPage, so a full page may not be all of them.
// Needs only read_api.
func (c Client) RecentNotes(ctx context.Context, project string, iid int) ([]Note, error) {
	b, err := c.API(ctx, "GET", fmt.Sprintf("%s/notes?sort=desc&order_by=updated_at&per_page=%d", issuePath(project, iid), RecentNotesPage), nil, false)
	if err != nil {
		return nil, err
	}
	return decodeList[Note](b)
}

// Comment adds a note to an issue.
func (c Client) Comment(ctx context.Context, project string, iid int, body string) error {
	_, err := c.API(ctx, "POST", issuePath(project, iid)+"/notes", map[string]string{"body": body}, false)
	return err
}

// Update changes issue fields. Keys follow the GitLab API, e.g.
// "description", "due_date", "assignee_ids", "state_event", "title".
func (c Client) Update(ctx context.Context, project string, iid int, fields map[string]any) (Issue, error) {
	var is Issue
	b, err := c.API(ctx, "PUT", issuePath(project, iid), fields, false)
	if err != nil {
		return is, err
	}
	return is, decodeOne(b, &is)
}

// Create opens a new issue in project.
func (c Client) Create(ctx context.Context, project string, fields map[string]any) (Issue, error) {
	var is Issue
	b, err := c.API(ctx, "POST", "projects/"+PathID(project)+"/issues", fields, false)
	if err != nil {
		return is, err
	}
	return is, decodeOne(b, &is)
}

// AssigneeIDs resolves usernames to user IDs, consulting known users
// first and the API for the rest.
func (c Client) AssigneeIDs(ctx context.Context, usernames []string, known []User) ([]int, error) {
	ids := make([]int, 0, len(usernames))
next:
	for _, name := range usernames {
		name = strings.TrimPrefix(strings.TrimSpace(name), "@")
		if name == "" {
			continue
		}
		for _, u := range known {
			if strings.EqualFold(u.Username, name) {
				ids = append(ids, u.ID)
				continue next
			}
		}
		u, err := c.UserByName(ctx, name)
		if err != nil {
			return nil, err
		}
		ids = append(ids, u.ID)
	}
	return ids, nil
}

// ParseRef splits "group/project#12" or an issue's web URL into project
// path and iid. host is the configured GitLab host: a URL on any other
// host is refused rather than acted on there.
func ParseRef(ref, host string) (project string, iid int, err error) {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "/-/issues/"); i >= 0 { // web URL
		// The scheme left off a URL on our host: a URL, not a project
		// named after it. The name without our port counts too, so that
		// SameHost refuses the port mismatch rather than a 404 confuse.
		if seg, _, _ := strings.Cut(ref, "/"); host != "" && (strings.EqualFold(seg, host) || strings.EqualFold(seg, HostName(host))) {
			ref = "https://" + ref
		}
		u, perr := url.Parse(ref)
		if perr == nil && u.Host != "" {
			if u.User != nil {
				return "", 0, fmt.Errorf("bad issue URL %q", RedactUserinfo(ref))
			}
			if host != "" && !SameHost(u, host) {
				return "", 0, fmt.Errorf("the URL is on %s, but host is %s", u.Host, host)
			}
			ref = strings.TrimLeft(u.Path, "/")
			i = strings.Index(ref, "/-/issues/")
		}
		if i < 0 { // "/-/issues/" was in the query or fragment, not the path
			return "", 0, fmt.Errorf("bad issue reference %q (want group/project#12 or an issue URL)", RedactUserinfo(ref))
		}
		ref = ref[:i] + "#" + ref[i+len("/-/issues/"):]
	}
	p, n, ok := strings.Cut(ref, "#")
	if !ok || p == "" || strings.Contains(p, ":") {
		return "", 0, fmt.Errorf("bad issue reference %q (want group/project#12 or an issue URL)", RedactUserinfo(ref))
	}
	iid, err = strconv.Atoi(n)
	if err != nil || iid <= 0 || strings.Trim(n, "0123456789") != "" { // no sign: "#+7"
		return "", 0, fmt.Errorf("bad issue number in %q", RedactUserinfo(ref))
	}
	return p, iid, nil
}

// RedactUserinfo hides the user and password a URL-shaped value may carry
// ("https://user:pass@host" is echoed as "https://***@host"). It works on
// the text, so it also covers a URL that does not parse: everything
// between the scheme and the last @ goes, a /, ? or # in the password
// included. Only a rejected value is echoed, so hiding too much is free.
func RedactUserinfo(v string) string {
	start := 0
	if i := strings.Index(v, "://"); i >= 0 {
		start = i + len("://")
	}
	if at := strings.LastIndex(v[start:], "@"); at >= 0 {
		return v[:start] + "***" + v[start+at:]
	}
	return v
}
