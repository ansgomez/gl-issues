package gl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fakeGlab puts a fake `glab` first in PATH. It records its environment
// and arguments, prints out on stdout and errOut on stderr, and exits with
// code. It never touches the network.
func fakeGlab(t *testing.T, out, errOut string, code int) (envFile, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	envFile, argsFile = filepath.Join(dir, "env"), filepath.Join(dir, "args")
	outFile, errFile := filepath.Join(dir, "out"), filepath.Join(dir, "err")
	for f, s := range map[string]string{outFile: out, errFile: errOut} {
		if err := os.WriteFile(f, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"env > '" + envFile + "'\n" +
		"printf '%s\\n' \"$@\" > '" + argsFile + "'\n" +
		"cat '" + outFile + "'\n" +
		"cat '" + errFile + "' >&2\n" +
		"exit " + string(rune('0'+code)) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return envFile, argsFile
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPISanitizesIssues(t *testing.T) {
	fakeGlab(t, `[{"id":1,"iid":2,"title":"evil\u001b]52;c;aGk=\u0007 title\u001b[2J","description":"a\u001b[31mred\u001b[0m\r\nline\tTab\u009b6n",`+
		`"labels":["bug\u0007"],"assignees":[{"username":"ali\u0007ce","name":"Alice\u0000 Example"}],"author":{"username":"bob"},`+
		`"web_url":"https://gitlab.example.edu/x\u001b[1m","references":{"full":"acme/x#2"}}]`, "", 0)
	is, err := Client{Host: "gitlab.example.edu"}.GroupIssues(context.Background(), "acme", "opened")
	if err != nil {
		t.Fatal(err)
	}
	got := is[0]
	for name, pair := range map[string][2]string{
		"title":    {got.Title, "evil title"},
		"desc":     {got.Description, "ared\nline\tTab"},
		"label":    {got.Labels[0], "bug"},
		"assignee": {got.Assignees[0].Username + "|" + got.Assignees[0].Name, "alice|Alice Example"},
		"url":      {got.WebURL, "https://gitlab.example.edu/x"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}
}

func TestAPIErrorText(t *testing.T) {
	fakeGlab(t, "", "ERROR\n  404 Not Found: project acme/x/-/issues/401 \x1b]0;pwned\x07gone\n", 1)
	_, err := Client{Host: "gitlab.example.edu"}.GetIssue(context.Background(), "acme/x", 401)
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\x1b\x07") {
		t.Errorf("control characters in error: %q", msg)
	}
	if strings.Contains(msg, "glab auth status") {
		t.Errorf("a 404 mentioning 401 must not get the login hint: %q", msg)
	}

	if !strings.HasPrefix(msg, "404 Not Found") || !strings.HasSuffix(msg, "(GET projects/acme%2Fx/issues/401)") {
		t.Errorf("want the reason first and the request last: %q", msg)
	}

	fakeGlab(t, "", "glab: 401 Unauthorized (HTTP 401)\n", 1)
	_, err = Client{Host: "gitlab.example.edu"}.Me(context.Background())
	if err == nil || !strings.Contains(err.Error(), "glab auth status") {
		t.Errorf("401 should get the login hint: %v", err)
	}
}

// gl-issues holds no token: inherited token variables never reach glab,
// and none is added, so glab uses only its stored login.
func TestAPITokenEnv(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "other-host-token")
	t.Setenv("GITLAB_ACCESS_TOKEN", "x")
	t.Setenv("OAUTH_TOKEN", "x")
	t.Setenv("CI_JOB_TOKEN", "x")
	envFile, _ := fakeGlab(t, `{"id":1,"username":"me"}`, "", 0)
	if _, err := (Client{Host: "gitlab.example.edu"}).Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	env := readFile(t, envFile)
	for _, v := range tokenVars {
		if strings.Contains(env, v+"=") {
			t.Errorf("%s must not reach glab", v)
		}
	}
	if !strings.Contains(env, "GITLAB_HOST=gitlab.example.edu") {
		t.Error("GITLAB_HOST not set")
	}
}

func TestPathIDEscapesColon(t *testing.T) {
	if got := PathID("acme/a:id"); got != "acme%2Fa%3Aid" {
		t.Errorf("PathID = %q", got)
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{
		"plain text, ünïcödé 日本": "plain text, ünïcödé 日本",
		"a\nb\tc":                "a\nb\tc",
		"a\r\nb":                 "a\nb",
		"\x1b[31mred\x1b[0m":     "red",
		"\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\": "link",
		"x\x1b]0;title\x07y":                       "xy",
		"\x1bPdcs\x1b\\z":                          "z",
		"\x1b(Bok":                                 "ok",
		"\x1bcreset":                               "reset",
		"del\x7f":                                  "del",
		"c1\u0085\u009b31m!":                       "c1!",
		"bell\x07\x00":                             "bell",
		"trailing\x1b":                             "trailing",
		"\x1b[":                                    "",
		"unterminated\x1b]0;x":                     "unterminated",
		// a lone ESC (nothing that can follow it in a sequence) goes alone:
		// it never eats the next character or joins two lines
		"é\x1bé":          "éé",
		"a\x1b\nb":        "a\nb",
		"a\x1b\x1b[1mb":   "ab",
		"a\x1b7b":         "ab",  // ESC 7: save cursor
		"a\x1b#8b":        "ab",  // ESC # 8: an intermediate, then the final byte
		"a\x1b(é":         "a(é", // intermediates without a final byte: only the ESC goes
		"ok\x1b\x7fz":     "okz",
		"\x1b\u200eright": "right",
	} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Zero width joiners are kept, but not without limit: a title chaining
// fifty emoji with them is one grapheme cluster that the width counting
// takes for two cells while the terminal draws a hundred. After maxJoins
// in a cluster, a joiner is dropped. Every real emoji sequence is shorter.
func TestSanitizeCapsJoiners(t *testing.T) {
	const zwj = "\u200d"
	family := "\U0001F468" + zwj + "\U0001F469" + zwj + "\U0001F467" + zwj + "\U0001F466"
	kiss := "\U0001F469\U0001F3FB" + zwj + "❤\ufe0f" + zwj + "\U0001F48B" + zwj + "\U0001F468\U0001F3FC"
	for _, keep := range []string{
		family, kiss, family + " and " + kiss, family + family + kiss, // adjacent clusters: a new count each
		"\U0001F3F3\ufe0f" + zwj + "\U0001F308", "क" + zwj + "्" + zwj + "त", // flag, Devanagari
		strings.Repeat("\U0001F600"+zwj, maxJoins) + "\U0001F600",
	} {
		if got := Sanitize(keep); got != keep {
			t.Errorf("Sanitize(%q) = %q, want it kept", keep, got)
		}
	}
	for _, in := range []string{
		strings.Repeat("\U0001F600"+zwj, 50),
		strings.Repeat("\U0001F600\ufe0f"+zwj, 50), // a variation selector does not end the cluster
		strings.Repeat(zwj, 50) + "x",
	} {
		got := Sanitize(in)
		if n := strings.Count(got, zwj); n != maxJoins {
			t.Errorf("Sanitize(%q) kept %d joiners, want %d", in, n, maxJoins)
		}
		if strings.ReplaceAll(got, zwj, "") != strings.ReplaceAll(in, zwj, "") {
			t.Errorf("Sanitize(%q) = %q changed more than joiners", in, got)
		}
	}
}

func TestSanitizeInvalidUTF8AndFormatChars(t *testing.T) {
	for in, want := range map[string]string{
		"raw\x9b31mC1":                    "raw\uFFFD31mC1", // a raw 8-bit CSI byte is not passed through
		"a\xffb":                          "a\uFFFDb",
		"admin\u202Etxt.exe":              "admintxt.exe", // RLO
		"x\u2066y\u2069z":                 "xyz",          // isolates
		"zero\u200Bwidth\u200Fmark":       "zerowidthmark",
		"\uFEFFbom":                       "bom",
		"left\u202Aright\u202Cpop":        "leftrightpop",
		"word\u2060joiner\u2061\u2064ops": "wordjoinerops",
		"arabic\u061Cmark\u200Elrm":       "arabicmarklrm",
		// the joiners emoji sequences and some scripts need, and the soft
		// hyphen, stay
		"family \U0001F468\u200d\U0001F469\u200d\U0001F467 flag \U0001F3F3\ufe0f\u200d\U0001F308": "family \U0001F468\u200d\U0001F469\u200d\U0001F467 flag \U0001F3F3\ufe0f\u200d\U0001F308",
		"\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645":                                        "\u0645\u06CC\u200C\u062E\u0648\u0627\u0647\u0645",
		"soft\u00ADhyphen":              "soft\u00ADhyphen",
		"\u200D\u202Ejoin and override": "\u200Djoin and override",
	} {
		got := Sanitize(in)
		if got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "\x9b") || !utf8.ValidString(got) {
			t.Errorf("Sanitize(%q) = %q still has a C1 byte or invalid UTF-8", in, got)
		}
	}
}

// Single-line fields lose newlines and tabs (a title could otherwise
// forge extra rows); the description and note bodies keep them.
func TestSanitizeLineFields(t *testing.T) {
	fakeGlab(t, `[{"id":1,"iid":2,"title":"one\ntwo\r\nacme/fake#9\tx","description":"keep\nlines\tand tabs",`+
		`"labels":["a\nb"],"assignees":[{"username":"al\nice","name":"Alice\tExample"}],"author":{"username":"bob\r"},`+
		`"state":"opened\n","due_date":"2026-10-01\n","references":{"full":"acme/x#2\nacme/y#3"}}]`, "", 0)
	is, err := Client{Host: "gitlab.example.edu"}.GroupIssues(context.Background(), "acme", "opened")
	if err != nil {
		t.Fatal(err)
	}
	got := is[0]
	for name, pair := range map[string][2]string{
		"title":    {got.Title, "one two acme/fake#9 x"},
		"desc":     {got.Description, "keep\nlines\tand tabs"},
		"label":    {got.Labels[0], "a b"},
		"assignee": {got.Assignees[0].Username + "|" + got.Assignees[0].Name, "al ice|Alice Example"},
		"author":   {got.Author.Username, "bob"},
		"state":    {got.State, "opened "},
		"due":      {got.DueDate, "2026-10-01 "},
		"ref":      {got.Ref(), "acme/x#2 acme/y#3"},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", name, pair[0], pair[1])
		}
	}
	var n Note
	if err := decodeOne([]byte(`{"body":"a\nb","author":{"name":"x\ny"}}`), &n); err != nil {
		t.Fatal(err)
	}
	if n.Body != "a\nb" || n.Author.Name != "x y" {
		t.Errorf("note %q by %q", n.Body, n.Author.Name)
	}
	var td Todo
	if err := decodeOne([]byte(`{"action_name":"a\nb","project":{"path_with_namespace":"p\tq"}}`), &td); err != nil {
		t.Fatal(err)
	}
	if td.ActionName != "a b" || td.Project.Path != "p q" {
		t.Errorf("todo %q %q", td.ActionName, td.Project.Path)
	}
}

func TestSanitizeKeepsRawDescription(t *testing.T) {
	var is Issue
	if err := decodeOne([]byte(`{"description":"a\u202Eb"}`), &is); err != nil {
		t.Fatal(err)
	}
	if is.Description != "ab" || is.RawDescription != "a\u202Eb" {
		t.Errorf("description %q, raw %q", is.Description, is.RawDescription)
	}
}

func TestGlabEnvDropsHostAndDebugVars(t *testing.T) {
	base := []string{
		"PATH=/bin", "HOME=/home/alice",
		"GITLAB_TOKEN=x", "GITLAB_ACCESS_TOKEN=x", "OAUTH_TOKEN=x", "CI_JOB_TOKEN=x",
		"GITLAB_HOST=evil.example", "GITLAB_URI=https://evil.example", "GITLAB_URL=https://evil.example",
		"GL_HOST=evil.example", "GLAB_ENABLE_CI_AUTOLOGIN=true",
		"GITLAB_API_HOST=evil.example", "GITLAB_API_PROTOCOL=http",
		"GLAB_DEBUG=1", "GLAB_DEBUG_HTTP=1", "DEBUG=true",
		"GLAB_SEND_TELEMETRY=true", "GLAB_SHOW_WHATS_NEW=true", "GLAB_CHECK_UPDATE=true",
	}
	env := strings.Join(glabEnv(base, "gitlab.example.edu"), "\n") + "\n"
	for _, k := range []string{"GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "OAUTH_TOKEN", "CI_JOB_TOKEN",
		"GITLAB_URI", "GITLAB_URL", "GL_HOST", "GITLAB_API_HOST", "GITLAB_API_PROTOCOL", "GLAB_ENABLE_CI_AUTOLOGIN", "GLAB_DEBUG", "GLAB_DEBUG_HTTP", "DEBUG"} {
		if strings.Contains("\n"+env, "\n"+k+"=") {
			t.Errorf("%s must not reach glab", k)
		}
	}
	if strings.Contains(env, "evil.example") || !strings.Contains(env, "GITLAB_HOST=gitlab.example.edu\n") {
		t.Errorf("only the configured host may reach glab:\n%s", env)
	}
	for _, k := range []string{"PATH=/bin", "HOME=/home/alice", "NO_COLOR=1", "GLAB_NO_PROMPT=1",
		"GLAB_CHECK_UPDATE=false", "GLAB_SEND_TELEMETRY=false", "GLAB_SHOW_WHATS_NEW=false"} {
		if !strings.Contains(env, k+"\n") {
			t.Errorf("%s missing", k)
		}
	}
	for _, k := range []string{"GLAB_SEND_TELEMETRY=true", "GLAB_SHOW_WHATS_NEW=true", "GLAB_CHECK_UPDATE=true"} {
		if strings.Contains(env, k) {
			t.Errorf("inherited %s must be replaced", k)
		}
	}
}

// Programs other than glab get the environment without any token
// variable, and nothing else changed.
func TestWithoutTokens(t *testing.T) {
	base := []string{"PATH=/bin", "GITLAB_TOKEN=x", "HOME=/home/alice", "GITLAB_ACCESS_TOKEN=x",
		"OAUTH_TOKEN=x", "CI_JOB_TOKEN=x", "GITLAB_HOST=gitlab.example.edu"}
	got := strings.Join(WithoutTokens(base), " ")
	if want := "PATH=/bin HOME=/home/alice GITLAB_HOST=gitlab.example.edu"; got != want {
		t.Errorf("WithoutTokens = %q, want %q", got, want)
	}
}

func TestAPITimeoutSaysTimedOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Client{Host: "gitlab.example.edu"}.Me(ctx)
	if err == nil || !strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "killed") {
		t.Errorf("want a timeout error, got %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded: %v", err)
	}
}

func TestAPIReturnsWhenChildHoldsPipes(t *testing.T) {
	// glab exits at once but leaves a background child that inherited
	// stdout and stderr. The call must not wait for that child.
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 20 &\nprintf '{\"id\":1,\"username\":\"me\"}'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := waitDelay
	waitDelay = 200 * time.Millisecond
	t.Cleanup(func() { waitDelay = old })

	start := time.Now()
	u, err := Client{Host: "gitlab.example.edu"}.Me(context.Background())
	if err != nil {
		t.Fatalf("a call whose glab exited 0 must succeed: %v", err)
	}
	if u.Username != "me" {
		t.Errorf("username = %q, want me", u.Username)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("call took %s; it must not wait for glab's child", took)
	}
}

func TestAPIErrorHints(t *testing.T) {
	fakeGlab(t, "", "ERROR\n  404 Not Found\n", 1)
	_, err := Client{Host: "gitlab.example.edu"}.GroupIssues(context.Background(), "acme/sub", "opened")
	if err == nil || !strings.HasPrefix(err.Error(), `group "acme/sub" not found, or you have no access`) {
		t.Errorf("a missing group should say so: %v", err)
	}

	// insufficient_scope may come in the response body on stdout or in
	// glab's own line on stderr; the word in a response body, or in
	// another message, is not it.
	for _, tc := range []struct {
		out, errOut string
		scope       bool
	}{
		{`{"error":"insufficient_scope","error_description":"The request requires higher privileges than provided by the access token.","scope":"api"}`, "glab: HTTP 403\n", true},
		{"", "glab: HTTP 403: insufficient_scope\n", true},
		{"", "glab: insufficient_scope\n", true},
		{`{"id":1,"iid":1,"title":"insufficient_scope in the title","references":{"full":"acme/x#1"}}`, "glab: HTTP 403\n", false},
		{`{"message":"403 Forbidden"}`, "glab: 403 Forbidden\n  insufficient_scope\n", false},
		{`{"message":"insufficient_scope"}`, "glab: HTTP 403\n", false},
	} {
		fakeGlab(t, tc.out, tc.errOut, 1)
		err := Client{Host: "gitlab.example.edu"}.Comment(context.Background(), "acme/x", 1, "hi")
		hint := err != nil && strings.HasPrefix(err.Error(), "the token needs the api scope: run glab auth login --hostname gitlab.example.edu")
		if hint != tc.scope {
			t.Errorf("stdout %q, stderr %q: scope hint %v, got %v", tc.out, tc.errOut, tc.scope, err)
		}
	}
}

func TestRecentNotes(t *testing.T) {
	_, argsFile := fakeGlab(t, `[{"id":7,"body":"added \u001b[31mlabel","system":true,"author":{"id":3,"username":"carol"},"updated_at":"2026-05-04T10:00:00.000Z"},{"id":6,"body":"hi","author":{"id":1}}]`, "", 0)
	ns, err := Client{Host: "gitlab.example.edu"}.RecentNotes(context.Background(), "acme/platform", 4)
	if err != nil || len(ns) != 2 {
		t.Fatalf("%d notes, err %v", len(ns), err)
	}
	if n := ns[0]; n.Author.ID != 3 || !n.System || n.Body != "added label" || n.UpdatedAt.IsZero() {
		t.Errorf("note = %+v", n)
	}
	if args := readFile(t, argsFile); !strings.Contains(args, "projects/acme%2Fplatform/issues/4/notes?sort=desc&order_by=updated_at&per_page=20") ||
		strings.Contains(args, "--paginate") {
		t.Errorf("request = %q", args)
	}

	fakeGlab(t, `[]`, "", 0)
	if ns, err := (Client{}).RecentNotes(context.Background(), "acme/platform", 4); len(ns) != 0 || err != nil {
		t.Errorf("no notes: %v, err %v", ns, err)
	}
	fakeGlab(t, "", "HTTP 500", 1)
	if _, err := (Client{}).RecentNotes(context.Background(), "acme/platform", 4); err == nil {
		t.Error("failure: no error")
	}
}

// glab refuses a port in --hostname, so a host:port goes only in
// GITLAB_HOST. The fake glab fails the way the real one does.
func TestAPIHostWithPort(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "env")
	script := "#!/bin/sh\n" +
		"env > '" + envFile + "'\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = --hostname ]; then case \"$2\" in *:*) echo 'invalid hostname' >&2; exit 1;; esac; fi\n" +
		"  shift\n" +
		"done\n" +
		"echo '{\"id\":1,\"username\":\"alice\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, host := range []string{"gitlab.example.edu:8443", "gitlab.example.edu"} {
		u, err := Client{Host: host}.Me(context.Background())
		if err != nil || u.Username != "alice" {
			t.Errorf("%s: %+v, %v", host, u, err)
		}
		if env := readFile(t, envFile); !strings.Contains(env, "GITLAB_HOST="+host+"\n") {
			t.Errorf("%s: GITLAB_HOST not set", host)
		}
	}
	// glab keys the login by --hostname; gl-issues looks it up by
	// GITLAB_HOST=host:port, so the port must stay in --hostname.
	if got := LoginCommand("gitlab.example.edu:8443"); got != "glab auth login --hostname gitlab.example.edu:8443" {
		t.Errorf("LoginCommand = %q", got)
	}
	if got := LoginCommand("gitlab.example.edu"); got != "glab auth login --hostname gitlab.example.edu" {
		t.Errorf("LoginCommand = %q", got)
	}
}

// A 401 is marked, so doctor can say "not logged in" without the hint.
func TestAPIErrorUnauthorized(t *testing.T) {
	fakeGlab(t, "", "ERROR\n  Unauthenticated.\n", 1)
	_, err := Client{Host: "gitlab.example.edu:8443"}.Me(context.Background())
	var e *APIError
	if !errors.As(err, &e) || !e.Unauthorized || e.Reason != "Unauthenticated." || e.Request != "GET user" {
		t.Fatalf("got %#v", err)
	}
	if !strings.Contains(err.Error(), "glab auth status --hostname gitlab.example.edu:8443`") {
		t.Errorf("hint should name the host with its port: %v", err)
	}
	fakeGlab(t, "", "HTTP 500\n", 1)
	if _, err = (Client{Host: "gitlab.example.edu"}).Me(context.Background()); !errors.As(err, &e) || e.Unauthorized {
		t.Errorf("a 500 is not a login problem: %#v", err)
	}
}

// Version reports glab's stderr when it fails, not what it wrote to stdout.
func TestVersionPrefersStderr(t *testing.T) {
	fakeGlab(t, "glab 1.123.0\n", "", 0)
	if v, err := (Client{}).Version(context.Background()); err != nil || v != "glab 1.123.0" {
		t.Errorf("version %q, %v", v, err)
	}
	fakeGlab(t, "some banner\n", "config: bad \x1b[2Jfile\n", 1)
	_, err := Client{}.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "config: bad file") || strings.Contains(err.Error(), "banner") {
		t.Errorf("want glab's stderr only: %v", err)
	}
}

func TestAuthStatus(t *testing.T) {
	const secret = "glpat-SECRETvalue"
	for _, tc := range []struct {
		name, out string
		code      int
		want      AuthStatus
	}{
		{"keyring", "gitlab.example.edu\n  ✓ Logged in to gitlab.example.edu as alice\n" +
			"  ✓ API calls for gitlab.example.edu are made over https protocol.\n" +
			"  ✓ Token found in operating system keyring: **************************\n", 0,
			AuthStatus{StorageKeyring, "https"}},
		{"plaintext over http", "  ✓ API calls for gitlab.example.edu are made over http protocol.\n" +
			"  ✓ Token found in configuration file (plaintext): " + secret + "\n", 0,
			AuthStatus{StoragePlaintext, "http"}},
		{"other host's protocol", "  ✓ API calls for gitlab.example.com are made over http protocol.\n", 0,
			AuthStatus{}},
		{"other host's token", "gitlab.example.com\n  ✓ Logged in to gitlab.example.com as alice\n" +
			"  ✓ Token found in configuration file (plaintext): " + secret + "\n", 0, AuthStatus{}},
		{"token line before any host", "  ✓ Token found in configuration file (plaintext): " + secret + "\n", 0,
			AuthStatus{}},
		{"two hosts, ours second", "gitlab.example.com\n  ✓ Logged in to gitlab.example.com as alice\n" +
			"  ✓ API calls for gitlab.example.com are made over https protocol.\n" +
			"  ✓ Token found in configuration file (plaintext): " + secret + "\n\n" +
			"gitlab.example.edu\n  ✓ Logged in to gitlab.example.edu as alice\n" +
			"  ✓ API calls for gitlab.example.edu are made over https protocol.\n" +
			"  ✓ Token found in operating system keyring: ****\n", 0, AuthStatus{StorageKeyring, "https"}},
		{"two hosts, ours first", "GITLAB.example.edu\n  ✓ Logged in to gitlab.example.edu as alice\n" +
			"  ✓ API calls for gitlab.example.edu are made over http protocol.\n" +
			"  ✓ Token found in configuration file (plaintext): " + secret + "\n\n" +
			"gitlab.example.com\n  ✓ Logged in to gitlab.example.com as alice\n" +
			"  ✓ Token found in operating system keyring: ****\n", 0, AuthStatus{StoragePlaintext, "http"}},
		{"not logged in", "  x gitlab.example.edu: API call failed: 401 " + secret + "\n", 1, AuthStatus{}},
	} {
		_, argsFile := fakeGlab(t, "", tc.out, tc.code)
		st, err := Client{Host: "gitlab.example.edu"}.AuthStatus(context.Background())
		if st != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, st, tc.want)
		}
		if (err != nil) != (tc.code != 0 && tc.want == AuthStatus{}) {
			t.Errorf("%s: err %v", tc.name, err)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("%s: output in the error: %v", tc.name, err)
		}
		if got := readFile(t, argsFile); got != "auth\nstatus\n--hostname\ngitlab.example.edu\n" {
			t.Errorf("%s: args %q", tc.name, got)
		}
	}
}

// A glab that never stops writing is killed once it passes MaxOutput, and
// the call fails saying so; it neither buffers on nor blocks. Output
// under the cap is read as before.
func TestAPIRefusesOversizedOutput(t *testing.T) {
	old := MaxOutput
	MaxOutput = 64 << 10
	t.Cleanup(func() { MaxOutput = old })

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte("#!/bin/sh\nexec yes '[{\"id\":1}]'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	start := time.Now()
	_, err := Client{Host: "gitlab.example.edu"}.GroupIssues(context.Background(), "acme", "opened")
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "GET groups/acme/issues") {
		t.Fatalf("want ErrTooLarge naming the request, got %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "killed") {
		t.Errorf("an oversized answer must not read as a timeout or a signal: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("call took %s; glab must be killed, not read on", took)
	}

	// Just under the cap is fine.
	fakeGlab(t, `{"id":1,"username":"me","name":"`+strings.Repeat("a", int(MaxOutput)-100)+`"}`, "", 0)
	if u, err := (Client{Host: "gitlab.example.edu"}).Me(context.Background()); err != nil || u.Username != "me" {
		t.Errorf("output under the cap: %v %+v", err, u)
	}
}

// The setup calls (Version and AuthStatus) cap glab's output as API
// does: a glab that writes without bound is killed and the call fails
// with ErrTooLarge instead of buffering on.
func TestSetupCallsRefuseOversizedOutput(t *testing.T) {
	old := MaxOutput
	MaxOutput = 64 << 10
	t.Cleanup(func() { MaxOutput = old })
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte("#!/bin/sh\nexec yes 'glab 1.0.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := Client{Host: "gitlab.example.edu"}
	ctx := context.Background()
	start := time.Now()
	if _, err := c.Version(ctx); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Version: want ErrTooLarge, got %v", err)
	}
	if _, err := c.AuthStatus(ctx); !errors.Is(err, ErrTooLarge) {
		t.Errorf("AuthStatus: want ErrTooLarge, got %v", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("calls took %s; glab must be killed, not read on", took)
	}
}
