package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/config"
	"gl-issues/internal/gl"
	"gl-issues/internal/tui"
)

// TestMain keeps every test in this package away from the real config
// and state directories and from the environment's GL_ISSUES_… variables.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gl-issues-test-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, config.EnvPrefix) {
			os.Unsetenv(name)
		}
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

var projects = []gl.Project{
	{Name: "weather-station", Path: "acme/platform/weather/weather-station"},
	{Name: "notes", Path: "acme/people/alice/notes"},
	{Name: "notes", Path: "acme/people/bob/notes"},
	{Name: "ground-tools", Path: "acme/tools/ground-tools"},
	{Name: "Rain Gauge", Path: "acme/hardware/rg-firmware"},
}

func TestMatchProject(t *testing.T) {
	for _, tc := range []struct {
		query, path string
		exact, err  bool
	}{
		{"acme/tools/ground-tools", "acme/tools/ground-tools", true, false},
		{"tools/ground-tools", "acme/tools/ground-tools", true, false},
		{"weather-station", "acme/platform/weather/weather-station", true, false},
		{"Ground-Tools", "acme/tools/ground-tools", true, false},
		{"ground", "acme/tools/ground-tools", false, false}, // fuzzy: needs confirmation
		{"bob notes", "acme/people/bob/notes", false, false},
		{"rain gauge", "acme/hardware/rg-firmware", true, false}, // display name
		{"rg-firmware", "acme/hardware/rg-firmware", true, false},
		{"gauge", "acme/hardware/rg-firmware", false, false},
		{"notes", "", false, true}, // two projects named notes
		{"nothing", "", false, true},
	} {
		path, exact, err := matchProject(projects, "acme", tc.query)
		if path != tc.path || exact != tc.exact || (err != nil) != tc.err {
			t.Errorf("matchProject(%q) = %q, %v, %v", tc.query, path, exact, err)
		}
	}
}

// A fuzzy match is never used without a yes.
func TestResolveProjectFuzzyNeedsConfirmation(t *testing.T) {
	dir := t.TempDir()
	out := `[{"name":"ground-tools","path_with_namespace":"acme/tools/ground-tools"}]`
	script := "#!/bin/sh\necho '" + out + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := gl.Client{Host: "gitlab.example.edu"}

	if _, err := resolveProject(c, "acme", "ground", nil); err == nil || !strings.Contains(err.Error(), "did you mean acme/tools/ground-tools") {
		t.Errorf("fuzzy match without confirmation: %v", err)
	}
	if _, err := resolveProject(c, "acme", "ground", func(string) bool { return false }); err == nil {
		t.Error("declined fuzzy match must fail")
	}
	asked := ""
	p, err := resolveProject(c, "acme", "ground", func(p string) bool { asked = p; return true })
	if err != nil || p != "acme/tools/ground-tools" || asked != p {
		t.Errorf("confirmed: %q, %v (asked %q)", p, err, asked)
	}
	if _, err := resolveProject(c, "acme", "ground-tools", func(string) bool { t.Error("exact match must not ask"); return false }); err != nil {
		t.Error(err)
	}
	if _, err := resolveProject(c, "", "acme/:id", nil); err == nil {
		t.Error("':' in a project path must be refused")
	}
}

// An empty description is only accepted with -clear.
func TestDescribeText(t *testing.T) {
	if _, err := describeText("-", false, strings.NewReader("")); err == nil {
		t.Error("empty stdin must be refused")
	}
	if _, err := describeText("  \n", false, nil); err == nil {
		t.Error("blank text must be refused")
	}
	if d, err := describeText("", true, nil); err != nil || d != "" {
		t.Errorf("-clear: %q, %v", d, err)
	}
	if d, err := describeText("-", false, strings.NewReader("new text\n")); err != nil || d != "new text" {
		t.Errorf("stdin: %q, %v", d, err)
	}
}

// fakeCLI returns a cli whose glab is a script answering like GitLab for
// the fictional group acme. Every call is logged with its JSON body; the
// log is returned by the second result.
func fakeCLI(t *testing.T, stdin string) (*cli, func() string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	issue := `{"id":40,"iid":4,"state":"opened","title":"t","assignees":[{"id":7,"username":"carol"},{"id":6,"username":"bob"}],"references":{"full":"acme/tools/ground-tools#4"}}`
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + log + "'\n" +
		"case \"$*\" in *--input*) cat >> '" + log + "'; echo >> '" + log + "';; esac\n" +
		"case \"$*\" in\n" +
		"  *' user') echo '{\"id\":5,\"username\":\"alice\"}';;\n" +
		"  *'users?username=bob') echo '[{\"id\":6,\"username\":\"bob\"}]';;\n" +
		"  *'users?username=alice') echo '[{\"id\":5,\"username\":\"alice\"}]';;\n" +
		"  *'groups/acme/projects'*) echo '[{\"name\":\"ground-tools\",\"path_with_namespace\":\"acme/tools/ground-tools\"}]';;\n" +
		"  *'POST'*'/notes') echo '{}';;\n" +
		"  *'POST'*'/issues') echo '" + issue + "';;\n" +
		"  *) echo '" + issue + "';;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	cfg := config.Defaults()
	cfg.Group = "acme"
	a := &cli{c: gl.Client{Host: "gitlab.example.edu"}, cfg: cfg, out: &out, errOut: &out,
		in: strings.NewReader(stdin), confirm: func(string) bool { return false }}
	return a, func() string { b, _ := os.ReadFile(log); return string(b) }, &out
}

const ref = "acme/tools/ground-tools#4"

func TestCLIFlagsAfterArguments(t *testing.T) {
	for _, args := range [][]string{{ref, "-clear"}, {"-clear", ref}, {ref, "--clear"}, {ref, "-clear=true"}} {
		a, calls, _ := fakeCLI(t, "")
		if err := a.run("describe", args); err != nil {
			t.Fatalf("describe %q: %v", args, err)
		}
		if got := calls(); !strings.Contains(got, `{"description":""}`) || strings.Contains(got, "-clear") {
			t.Errorf("describe %q should clear the description:\n%s", args, got)
		}
	}
	a, calls, _ := fakeCLI(t, "")
	err := a.run("new", []string{"ground-tools", "Title", "-due", "2026-11-01", "-d", "Details"})
	if err != nil {
		t.Fatal(err)
	}
	if got := calls(); !strings.Contains(got, `"due_date":"2026-11-01"`) || !strings.Contains(got, `"description":"Details"`) ||
		!strings.Contains(got, `"title":"Title"`) {
		t.Errorf("new with flags after the arguments:\n%s", got)
	}
	a, calls, _ = fakeCLI(t, "")
	if err := a.run("comment", []string{ref, "--", "-clear is a flag"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls(), `"body":"-clear is a flag"`) {
		t.Errorf("-- must end the flags:\n%s", calls())
	}
}

func TestCLIDescribeFromStdin(t *testing.T) {
	a, calls, _ := fakeCLI(t, "new text\n")
	if err := a.run("describe", []string{ref, "-"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(calls(), `{"description":"new text"}`) {
		t.Errorf("calls:\n%s", calls())
	}
}

func TestCLIAssignMe(t *testing.T) {
	for spec, want := range map[string]string{
		"+me":         `{"assignee_ids":[7,6,5]}`,
		"me":          `{"assignee_ids":[5]}`,
		"-bob,+@ME":   `{"assignee_ids":[7,5]}`, // "-bob" is a spec, not a flag
		"-bob,-carol": `{"assignee_ids":[]}`,
		"none":        `{"assignee_ids":[]}`,
	} {
		a, calls, _ := fakeCLI(t, "")
		if err := a.run("assign", []string{ref, spec}); err != nil {
			t.Fatalf("assign %q: %v", spec, err)
		}
		got := calls()
		if !strings.Contains(got, want) {
			t.Errorf("assign %q: want %s in\n%s", spec, want, got)
		}
		if strings.Contains(got, "username=me") {
			t.Errorf("assign %q looked up a user named me", spec)
		}
	}
	a, calls, _ := fakeCLI(t, "")
	if err := a.run("new", []string{"-assign", "me", "ground-tools", "T"}); err != nil {
		t.Fatal(err)
	}
	if got := calls(); !strings.Contains(got, `"assignee_ids":[5]`) || strings.Contains(got, "username=me") {
		t.Errorf("new -assign me:\n%s", got)
	}
}

func TestCLIRejectsBadArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"new", []string{"ground-tools", "Title", "Old third argument"}, "unexpected argument"},
		{"new", []string{"ground-tools"}, "usage"},
		{"list", []string{"-state", "bogus"}, "bad -state"},
		{"list", []string{"-bogus"}, "unknown flag"},
		{"list", []string{"extra"}, "unexpected argument"},
		{"describe", []string{ref, "-clear", "text"}, "usage"},
		{"describe", []string{ref, ""}, "empty description"},
		{"assign", []string{ref, ""}, "empty assignee list"},
		{"assign", []string{ref, " , "}, "empty assignee list"},
		{"assign", []string{ref, "@"}, "empty assignee list"},
		{"assign", []string{ref, "+, -@"}, "empty assignee list"},
		{"comment", []string{ref}, "usage"},
		{"due", []string{ref, "someday"}, "bad date"},
		{"frobnicate", nil, "unknown command"},
		{"close", []string{"https://other.example.edu/acme/tools/ground-tools/-/issues/4"},
			"the URL is on other.example.edu, but host is gitlab.example.edu"},
	} {
		a, calls, _ := fakeCLI(t, "")
		err := a.run(tc.name, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: error %v, want %q", tc.name, tc.args, err, tc.want)
		}
		if got := calls(); strings.Contains(got, "PUT") || strings.Contains(got, "POST") {
			t.Errorf("%s %q must not write:\n%s", tc.name, tc.args, got)
		}
	}
}

// A subcommand that needs arguments and gets none is a usage error, not
// a panic.
func TestCLINoArguments(t *testing.T) {
	for name, cmd := range commands {
		if cmd.nargs == 0 {
			continue
		}
		a, calls, _ := fakeCLI(t, "")
		err := a.run(name, nil)
		if err == nil || !strings.Contains(err.Error(), "usage: gl-issues "+name) {
			t.Errorf("%s with no arguments: %v", name, err)
		}
		if calls() != "" {
			t.Errorf("%s with no arguments called glab:\n%s", name, calls())
		}
	}
}

func TestCLIHelp(t *testing.T) {
	for name := range commands {
		for _, h := range []string{"-h", "--help"} {
			a, calls, out := fakeCLI(t, "")
			if err := a.run(name, []string{h}); err != nil {
				t.Errorf("%s %s: %v", name, h, err)
			}
			if !strings.Contains(out.String(), "Usage: gl-issues "+name) || calls() != "" {
				t.Errorf("%s %s: output %q, calls %q", name, h, out.String(), calls())
			}
		}
	}
}

// Help needs no host, but only a real request for it: "-h" as the value
// of a flag (`new … -d -h`) is a description, and without a host the
// command must stop at the empty-host check, before any glab call.
func TestWantsHelpParsesFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"new", []string{"acme/project", "Title", "-d", "-h"}, false},
		{"new", []string{"acme/project", "Title", "-due", "--help"}, false},
		{"comment", []string{ref, "--", "-h"}, false},
		{"new", []string{"acme/project", "Title", "-d", "x", "-h"}, true},
		{"new", []string{"-help"}, true},
		{"list", []string{"--help"}, true},
		{"config", []string{"-h"}, true},
		{"", nil, false},
	} {
		if got := wantsHelp(tc.name, tc.args); got != tc.want {
			t.Errorf("wantsHelp(%q, %q) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

// Global flags go before the subcommand; after it they get a clear error,
// not "unknown flag". A word like "-host" that fits the command stays an
// argument (here: an assignee spec removing user "host").
func TestGlobalFlagAfterCommand(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"list", []string{"-g", "acme/other"}, "-g goes before the command: gl-issues -g … list"},
		{"list", []string{"-mine", "--group=acme/other"}, "-group goes before the command"},
		{"show", []string{ref, "-host", "gitlab.example.edu"}, "-host goes before the command: gl-issues -host … show"},
		{"new", []string{"ground-tools", "T", "-tree", "2"}, "-tree goes before the command"},
		{"list", []string{"-config"}, "-config goes before the command"},
	} {
		a, calls, _ := fakeCLI(t, "")
		err := a.run(tc.name, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %q: error %v, want %q", tc.name, tc.args, err, tc.want)
		}
		if got := calls(); got != "" {
			t.Errorf("%s %q made requests:\n%s", tc.name, tc.args, got)
		}
	}
	a, calls, _ := fakeCLI(t, "")
	if err := a.run("assign", []string{ref, "-host"}); err != nil || !strings.Contains(calls(), "PUT") {
		t.Errorf("assign spec -host: %v\n%s", err, calls())
	}
	for _, name := range []string{"doctor", "init", "config"} {
		err := noArgs(name, []string{"-host", "gitlab.example.edu"})
		if err == nil || err.Error() != "-host goes before the command: gl-issues -host … "+name {
			t.Errorf("%s -host: %v", name, err)
		}
	}
	var out bytes.Buffer
	err := setupCommand("doctor", []string{"-g", "acme"}, "", "/x/config.toml", nil, &out)
	if err == nil || !strings.Contains(err.Error(), "-g goes before the command") || out.Len() != 0 {
		t.Errorf("doctor -g: %v\n%s", err, out.String())
	}
	if err := noArgs("config", []string{"extra"}); err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Errorf("config extra: %v", err)
	}
}

// gl-issues config names the file once, above the table, and says "file"
// in the FROM column.
func TestPrintConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("host = \"gitlab.example.edu\"\n"), 0o600)
	t.Setenv("GL_ISSUES_VIEW", "compact")
	t.Setenv("GL_ISSUES_EDITOR", "vim\x1b[31m\n--trick") // an escape sequence and a newline in a value
	cfg, err := config.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printConfig(&out, cfg, p)
	got := out.String()
	if strings.Count(got, p) != 1 || !strings.HasPrefix(got, "config file: "+p+"\n") {
		t.Errorf("the path should appear once, in the header:\n%s", got)
	}
	for _, re := range []string{`(?m)^host +gitlab\.example\.edu +file `, `(?m)^view +compact +env GL_ISSUES_VIEW `,
		`(?m)^sort +due +default `} {
		if !regexp.MustCompile(re).MatchString(got) {
			t.Errorf("no line matching %s:\n%s", re, got)
		}
	}
	if strings.Contains(got, "\x1b") || !regexp.MustCompile(`(?m)^editor +vim --trick +env GL_ISSUES_EDITOR `).MatchString(got) {
		t.Errorf("a value reached the terminal unsanitized:\n%s", got)
	}
}

// A title with a newline or tab from the server stays on its own row in
// `list`: it cannot forge another issue line.
func TestCLIListOneLinePerIssue(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' '[{\"id\":1,\"iid\":4,\"title\":\"real\\nacme/fake#999  -  -  FORGED\\tx\",\"labels\":[\"a\\nb\"],\"references\":{\"full\":\"acme/x#4\"}}]'\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var out bytes.Buffer
	cfg := config.Defaults()
	cfg.Group = "acme"
	a := &cli{c: gl.Client{Host: "gitlab.example.edu"}, cfg: cfg, out: &out, errOut: &out, in: strings.NewReader("")}
	if err := a.run("list", nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "real acme/fake#999") || strings.Contains(out.String(), "\t") {
		t.Errorf("want a header and one issue line:\n%s", out.String())
	}
}

// The dashboard's defaults are the config's defaults.
func TestDefaultOptionsMatchConfig(t *testing.T) {
	if got, want := tuiOptions(config.Defaults()), tui.DefaultOptions(); got != want {
		t.Errorf("tui.DefaultOptions() = %+v\nconfig defaults give %+v", want, got)
	}
}

// fakeProgram panics in Run and records what runTUI calls, in order.
type fakeProgram struct{ calls []string }

func (f *fakeProgram) Run() (tea.Model, error) {
	f.calls = append(f.calls, "run")
	panic("boom")
}

func (f *fakeProgram) ReleaseTerminal() error {
	f.calls = append(f.calls, "release")
	return nil
}

// A panic gives the terminal back (ReleaseTerminal, not RestoreTerminal,
// which would take it again) before it comes back as an error to print.
func TestRunTUIPanic(t *testing.T) {
	f := &fakeProgram{}
	err := runTUI(f)
	if err == nil || !strings.HasPrefix(err.Error(), "panic: boom\n") || !strings.Contains(err.Error(), "goroutine") {
		t.Errorf("err = %v", err)
	}
	if strings.Join(f.calls, ",") != "run,release" {
		t.Errorf("calls %v", f.calls)
	}
}

// Text from stdin is read up to maxText; more is an error, not a buffer.
func TestTextFromCapsStdin(t *testing.T) {
	if _, err := textFrom("-", strings.NewReader(strings.Repeat("a", maxText+1))); err == nil || !strings.Contains(err.Error(), "over 1 MB") {
		t.Errorf("oversized stdin: %v, want an error", err)
	}
	if d, err := textFrom("-", strings.NewReader(strings.Repeat("a", maxText))); err != nil || len(d) != maxText {
		t.Errorf("stdin at the cap: %d bytes, %v", len(d), err)
	}
}

// The dashboard without a host reports it (it used to slice an empty
// argument list); a subcommand's -h needs no host.
func TestHostMissing(t *testing.T) {
	for _, c := range []struct {
		host string
		args []string
		want bool
	}{
		{"", nil, true},
		{"", []string{"list"}, true},
		{"", []string{"list", "-h"}, false},
		{"", []string{"bogus"}, false}, // cli.run says "unknown command"

		{"gitlab.example.edu", nil, false},
	} {
		if got := hostMissing(c.host, c.args); got != c.want {
			t.Errorf("hostMissing(%q, %q) = %v, want %v", c.host, c.args, got, c.want)
		}
	}
}
