package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	kv, err := parse(strings.NewReader(`
# comment
host  = "git.example.org"   # trailing comment
Group = "team/sub"
token = "glpat-a\"b#c"   # ignored, see TestTokenIgnored
bare = value # note
editor = 'C:\no\escapes "here"' # literal string
empty = ""
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"host": "git.example.org", "group": "team/sub", "bare": "value",
		"editor": `C:\no\escapes "here"`, "empty": ""}
	for k, v := range want {
		if kv.values[k] != v {
			t.Errorf("%s = %q, want %q", k, kv.values[k], v)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{
		"novalue",
		`k = "open`,
		`k = 'open`,
		"[section]",
		"  [gl-issues]",
		`host = "a" "b"`,
		`host = "a" junk`,
		`host = 'a'b`,
		`host = two words`,
		`host = `,
		`host = """multi"""`,
		`"quoted key" = "x"`,
		`a b = "x"`,
		"host = \"a\"\nhost = \"b\"",
		"\uFEFF\uFEFFhost = \"a\"",
	} {
		if _, err := parse(strings.NewReader(in)); err == nil {
			t.Errorf("parse(%q): want error", in)
		}
	}
}

func TestLoadPermsAndEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(`host = "https://h.example/"`+"\ngroup = \"g\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("want permission error, got %v", err)
	}
	os.Chmod(p, 0o600)
	t.Setenv("GL_ISSUES_GROUP", "other")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "h.example" || c.Group != "other" {
		t.Errorf("got %+v", c)
	}
}

func TestLoadMissingDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GL_ISSUES_CONFIG", "")
	if _, err := Load(""); err != nil {
		t.Errorf("missing default config should not fail: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("missing explicit config should fail")
	}
}

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Refresh != 5*time.Minute || c.RefreshLists != time.Hour || !c.Notify ||
		c.Sort != "due" || c.State != "opened" || c.View != "cards" || c.Filter != "" || c.DueSoonDays != 7 ||
		c.TreeDepth != 1 || c.Theme != "auto" {
		t.Errorf("defaults: %+v", c)
	}
	if c.Source["refresh"] != "default" {
		t.Errorf("source = %q", c.Source["refresh"])
	}
}

func TestSettingsFromFileAndEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("refresh = \"2m\"\nview = \"compact\"\nnotify = false\ndue_soon_days = 3\n"), 0o600)
	t.Setenv("GL_ISSUES_REFRESH", "off")
	t.Setenv("GL_ISSUES_SORT", "updated")
	t.Setenv("GL_ISSUES_FILTER", "")
	t.Setenv("GL_ISSUES_THEME", "light")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Refresh != 0 || c.Source["refresh"] != "env GL_ISSUES_REFRESH" {
		t.Errorf("env should override file: %v from %q", c.Refresh, c.Source["refresh"])
	}
	if c.View != "compact" || c.Notify || c.DueSoonDays != 3 || c.Source["view"] != p {
		t.Errorf("file values: %+v", c)
	}
	if c.Sort != "updated" {
		t.Errorf("sort = %q", c.Sort)
	}
	if c.Theme != "light" {
		t.Errorf("theme = %q", c.Theme)
	}
}

// tree_depth: the file sets it, the environment wins over the file.
func TestTreeDepthSetting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("tree_depth = 1\n"), 0o600)
	c, err := Load(p)
	if err != nil || c.TreeDepth != 1 || c.Source["tree_depth"] != p {
		t.Fatalf("file: %d from %q, %v", c.TreeDepth, c.Source["tree_depth"], err)
	}
	t.Setenv("GL_ISSUES_TREE_DEPTH", "2")
	if c, err = Load(p); err != nil || c.TreeDepth != 2 || c.Source["tree_depth"] != "env GL_ISSUES_TREE_DEPTH" {
		t.Errorf("env: %d from %q, %v", c.TreeDepth, c.Source["tree_depth"], err)
	}
	for _, bad := range []string{"3", "-1", "deep"} {
		if err := c.Set("tree_depth", bad, "test"); err == nil {
			t.Errorf("tree_depth = %q accepted", bad)
		}
	}
}

func TestBadSettings(t *testing.T) {
	for _, line := range []string{
		`refesh = "5m"`,                            // typo
		`refresh = "10s"`,                          // too short
		`refresh = "soon"`,                         // not a duration
		`view = "grid"`,                            // not allowed
		`notify = "maybe"`,                         // not a bool
		`due_soon_days = "-1"`,                     // out of range
		`filter = "everything"`,                    // not allowed
		`theme = "sepia"`,                          // not allowed
		`host = "https://user@gitlab.example.edu"`, // credentials in the host
		`host = "gitlab example"`,                  // not a host name
		`host = "gitlab.example.edu/gitlab"`,       // a path
		`host = "https://gitlab.example.edu/-/x"`,  // a URL with a path
	} {
		p := filepath.Join(t.TempDir(), "c.toml")
		os.WriteFile(p, []byte(line+"\n"), 0o600)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: want error", line)
		}
	}
}

func TestInterval(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "off", 5 * time.Minute: "5m", time.Hour: "1h", 90 * time.Second: "1m30s", 30 * time.Second: "30s"} {
		if got := interval(d); got != want {
			t.Errorf("interval(%v) = %q, want %q", d, got, want)
		}
	}
}

// The shipped template must always load.
func TestExampleConfigParses(t *testing.T) {
	f, err := os.Open("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	kv, err := parse(f)
	if err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	for _, k := range kv.order {
		if err := c.Set(k, kv.values[k], "example"); err != nil {
			t.Error(err)
		}
	}
	if len(kv.order) != len(settings) {
		t.Errorf("config.example.toml has %d settings, want all %d", len(kv.order), len(settings))
	}
	// host and group ship empty, so an unedited file is reported as such
	// instead of pointing at the example host.
	if c.Host != "" || c.Group != "" {
		t.Errorf("template sets host %q, group %q; want both empty", c.Host, c.Group)
	}
}

// A token line left from older versions loads: the value is ignored,
// never kept or shown, and FileToken says it is there (an empty one is
// left alone). The old token variables are not read, only warned about.
func TestTokenIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("token = \"glpat-file\"\nview = \"compact\"\n"), 0o600)
	t.Setenv("GL_ISSUES_TOKEN", "glpat-env")
	t.Setenv("GITISSUE_TOKEN", "glpat-old")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !c.FileToken || c.View != "compact" {
		t.Errorf("FileToken %v, view %q", c.FileToken, c.View)
	}
	for _, e := range c.Entries() {
		if e.Key == TokenKey || strings.Contains(e.Value, "glpat") {
			t.Errorf("token in %+v", e)
		}
	}
	if fmt.Sprintf("%+v", c) != strings.NewReplacer("glpat-file", "", "glpat-env", "", "glpat-old", "").Replace(fmt.Sprintf("%+v", c)) {
		t.Error("the token is kept in Config")
	}
	want := []string{
		"GL_ISSUES_TOKEN is no longer read (ignored): gl-issues uses glab's login; remove it",
		"GITISSUE_TOKEN is no longer read (ignored): gl-issues uses glab's login; remove it",
	}
	if got := RemovedEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("RemovedEnv = %q", got)
	}

	os.WriteFile(p, []byte("token = \"\"\n"), 0o600)
	if c, err = Load(p); err != nil || c.FileToken {
		t.Errorf("empty token line: FileToken %v, %v", c.FileToken, err)
	}
	if err := c.Set(TokenKey, "x", "flag"); err == nil {
		t.Error("token is no longer a setting")
	}
}

func TestStripToken(t *testing.T) {
	in := "# my settings\r\nhost = \"gitlab.example.edu\"\n  # token = \"commented\"\n" +
		"Token = 'glpat-a\"b' # old\ngroup = \"acme\"" // no newline at the end
	rest, tok, found, err := StripToken([]byte(in))
	if err != nil || !found || tok != `glpat-a"b` {
		t.Fatalf("token %q, found %v, %v", tok, found, err)
	}
	if want := "# my settings\r\nhost = \"gitlab.example.edu\"\n  # token = \"commented\"\ngroup = \"acme\""; string(rest) != want {
		t.Errorf("rest %q", rest)
	}
	if rest, _, found, err := StripToken([]byte("host = \"h\"\n")); found || err != nil || string(rest) != "host = \"h\"\n" {
		t.Errorf("no token: %q %v %v", rest, found, err)
	}
	for _, bad := range []string{"token = glpat-SECRET oops\n", "token = \"a\"\ntoken = \"glpat-SECRET\"\n"} {
		if _, _, _, err := StripToken([]byte(bad)); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestHostForms(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"gitlab.example.edu", "gitlab.example.edu"},
		{"https://gitlab.example.edu/", "gitlab.example.edu"},
		{"https://gitlab.example.edu:8443", "gitlab.example.edu:8443"},
		{"HTTPS://gitlab.example.edu", "gitlab.example.edu"},
		{" gitlab.example.edu ", "gitlab.example.edu"}, // surrounding space is trimmed
		{"", ""},
	} {
		in, want := tc.in, tc.want
		var c Config
		if err := c.Set("host", in, "test"); err != nil {
			t.Errorf("host %q: %v", in, err)
		} else if c.Host != want {
			t.Errorf("host %q = %q, want %q", in, c.Host, want)
		}
	}
	var c Config
	if err := c.Set("host", "gitlab.example.edu/sub", "test"); err == nil || !strings.Contains(err.Error(), "without a path") {
		t.Errorf("a path must be rejected with a clear message, got %v", err)
	}
}

func TestHostRejects(t *testing.T) {
	for in, want := range map[string]string{
		"http://gitlab.example.edu":     "drop http://",
		"https://":                      "want a host name",
		"/":                             "want a host name",
		":8443":                         "want a host name",
		"gitlab.example.edu:":           "bad port",
		"gitlab.example.edu:0":          "bad port",
		"gitlab.example.edu:65536":      "bad port",
		"gitlab.example.edu:+80":        "bad port",
		"gitlab.example.edu:https":      "bad port",
		"gitlab.example.edu:80:80":      "bad port",
		"https://gitlab.example.edu/x/": "without a path",
		"gitlab.example\x1b[2J.edu":     "control or invisible",
		"gitlab.exam\u200bple.edu":      "control or invisible", // zero width space
		"gitlab.exam\u202eple.edu":      "control or invisible", // right-to-left override
	} {
		var c Config
		if err := c.Set("host", in, "test"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("host %q: error %v, want %q", in, err, want)
		}
	}
}

// A rejected value is echoed without the password it may carry.
func TestRejectedValueRedactsCredentials(t *testing.T) {
	for _, key := range []string{"host", "group"} {
		var c Config
		err := c.Set(key, "https://user:SECRET@gitlab.example.edu", "test")
		if err == nil || strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), `"https://***@gitlab.example.edu"`) {
			t.Errorf("%s: %v", key, err)
		}
	}
	if err := (&Config{}).Set("host", "gitlab example", "test"); err == nil || !strings.Contains(err.Error(), `"gitlab example"`) {
		t.Errorf("a value without credentials is echoed as it is: %v", err)
	}
}

func TestGroupForms(t *testing.T) {
	for _, tc := range []struct{ host, in, want string }{
		{"", "acme/platform", "acme/platform"},
		{"", "/acme/platform/", "acme/platform"},
		{"gitlab.example.edu", "https://gitlab.example.edu/acme/platform", "acme/platform"},
		{"gitlab.example.edu", "https://GitLab.example.edu/groups/acme/platform/-/issues", "acme/platform"},
		{"gitlab.example.edu:8443", "https://gitlab.example.edu:8443/acme/", "acme"},
	} {
		c := Config{Host: tc.host}
		if err := c.Set("group", tc.in, "test"); err != nil {
			t.Errorf("group %q: %v", tc.in, err)
		} else if c.Group != tc.want {
			t.Errorf("group %q = %q, want %q", tc.in, c.Group, tc.want)
		}
	}
	for _, tc := range []struct{ host, in, want string }{
		{"gitlab.example.edu", "https://other.example.edu/acme", "but host is gitlab.example.edu"},
		{"", "acme platform", "without spaces"},
		{"", "acme:platform", "without spaces or ':'"},
		{"", "https://gitlab.example.edu/", "no group path"},
		{"", "acme/plat\x1b[2Jform", "control or invisible"},
		{"", "acme/plat\u200bform", "control or invisible"},
		{"", "https://gitlab.example.edu/acme/plat\u202eform", "control or invisible"},
	} {
		c := Config{Host: tc.host}
		if err := c.Set("group", tc.in, "test"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("group %q: error %v, want %q", tc.in, err, tc.want)
		}
	}

	// The host may be set after the group URL (file order, flags): it must match.
	var c Config
	if err := c.Set("group", "https://gitlab.example.edu/acme", "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("host", "other.example.edu", "test"); err == nil {
		t.Error("a host other than the group URL's must be rejected")
	}
	if err := c.Set("host", "gitlab.example.edu", "test"); err != nil || c.Group != "acme" {
		t.Errorf("matching host: %v, group %q", err, c.Group)
	}
	if err := c.Set("group", "acme/other", "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("host", "other.example.edu", "test"); err != nil {
		t.Errorf("a plain group path does not tie the host: %v", err)
	}
}

// A duplicate key is an error naming both lines; a BOM on line 1 is
// dropped.
func TestParseDuplicateAndBOM(t *testing.T) {
	_, err := parse(strings.NewReader("host = \"a\"\n# c\nHOST = \"b\"\n"))
	if err == nil || err.Error() != "line 3: host is already set on line 1; keep one" {
		t.Errorf("duplicate: %v", err)
	}
	kv, err := parse(strings.NewReader("\uFEFFhost = \"a\"\ngroup = 'g'\n"))
	if err != nil || kv.values["host"] != "a" || kv.values["group"] != "g" {
		t.Errorf("BOM: %v %v", kv, err)
	}
}

// An old token line never stops loading, whatever is wrong with its
// value: it is ignored (FileToken), and its text appears nowhere.
// StripToken, for migrate-token, reports what is wrong without the text.
func TestSecretNeverInErrors(t *testing.T) {
	for _, line := range []string{
		`token = glpat-SECRETabc junk`,
		`token = glpat-SECRETabc"`,
		`token = "glpat-SECRETabc" trailing`,
		`token = 'glpat-SECRETabc' trailing`,
		`token = "glpat-SECRETabc\q"`,
		`token = "x"` + "\ntoken = \"glpat-SECRETabc\"",
	} {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := Load(p)
		if err != nil || !c.FileToken {
			t.Errorf("%q: must load with FileToken: %v, %v", line, c.FileToken, err)
		}
		if strings.Contains(fmt.Sprintf("%+v", c), "SECRET") {
			t.Errorf("%q: token kept in Config", line)
		}
		_, _, _, err = StripToken([]byte(line + "\n"))
		if err == nil {
			t.Errorf("%q: StripToken: want error", line)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), "token") {
			t.Errorf("%q: StripToken error: %v", line, err)
		}
	}
}

// Without a home directory, or with a relative XDG_CONFIG_HOME, the
// default path never falls back to the current directory.
func TestDefaultPathNeedsAbsoluteDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "rel/dir")
	if p, err := DefaultPath(); err != nil || p != filepath.Join(home, ".config", "gl-issues", "config.toml") {
		t.Errorf("relative XDG_CONFIG_HOME: %q, %v", p, err)
	}
	t.Setenv("HOME", "")
	t.Setenv("GL_ISSUES_CONFIG", "")
	if p, err := DefaultPath(); !errors.Is(err, ErrNoConfigDir) {
		t.Errorf("no home: %q, %v", p, err)
	}
	if _, err := Load(""); !errors.Is(err, ErrNoConfigDir) {
		t.Errorf("Load without home: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", "/abs/xdg")
	if p, err := DefaultPath(); err != nil || p != "/abs/xdg/gl-issues/config.toml" {
		t.Errorf("absolute XDG_CONFIG_HOME: %q, %v", p, err)
	}
}
