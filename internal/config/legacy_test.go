package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMain keeps the tests away from the real config directory and from
// the environment's GL_ISSUES_… and GITISSUE_… variables.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gl-issues-config-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, EnvPrefix) || strings.HasPrefix(name, LegacyEnvPrefix) {
			os.Unsetenv(name)
		}
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// The old GITISSUE_<KEY> names work while the new ones are unset; the
// new one wins when both are set; the source names the variable read.
func TestLegacyEnv(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	os.WriteFile(p, []byte("host = \"gitlab.example.edu\"\nview = \"compact\"\n"), 0o600)
	t.Setenv("GITISSUE_GROUP", "old/group")
	t.Setenv("GITISSUE_SORT", "updated")
	t.Setenv("GL_ISSUES_SORT", "project")
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Group != "old/group" || c.Source["group"] != "env GITISSUE_GROUP" {
		t.Errorf("group %q from %q", c.Group, c.Source["group"])
	}
	if c.Sort != "project" || c.Source["sort"] != "env GL_ISSUES_SORT" {
		t.Errorf("sort %q from %q: the new name must win", c.Sort, c.Source["sort"])
	}
}

func TestLegacyConfigEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("GITISSUE_CONFIG", filepath.Join(dir, "old.toml"))
	if p, explicit, _ := ResolvePath(""); p != filepath.Join(dir, "old.toml") || !explicit {
		t.Errorf("GITISSUE_CONFIG: %s %v", p, explicit)
	}
	t.Setenv("GL_ISSUES_CONFIG", filepath.Join(dir, "new.toml"))
	if p, _, _ := ResolvePath(""); p != filepath.Join(dir, "new.toml") {
		t.Errorf("GL_ISSUES_CONFIG must win: %s", p)
	}
	if p, _, _ := ResolvePath("flag.toml"); p != "flag.toml" {
		t.Errorf("-config must win: %s", p)
	}
}

func TestDeprecatedEnv(t *testing.T) {
	if got := DeprecatedEnv(); got != nil {
		t.Errorf("clean environment: %v", got)
	}
	t.Setenv("GITISSUE_CONFIG", "/x/c.toml")
	t.Setenv("GITISSUE_TOKEN", "glpat-secret") // no longer read: RemovedEnv warns about it
	t.Setenv("GITISSUE_VIEW", "cards")
	t.Setenv("GL_ISSUES_VIEW", "compact")
	want := []string{
		"GITISSUE_CONFIG is deprecated: use GL_ISSUES_CONFIG",
		"GITISSUE_VIEW is deprecated: use GL_ISSUES_VIEW (ignored: GL_ISSUES_VIEW is set)",
	}
	if got := DeprecatedEnv(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestLegacyDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/abs/xdg")
	if d, err := LegacyDir(); err != nil || d != "/abs/xdg/gitissue" {
		t.Errorf("%s %v", d, err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if d, _ := LegacyDir(); d != filepath.Join(home, ".config", "gitissue") {
		t.Errorf("%s", d)
	}
}
