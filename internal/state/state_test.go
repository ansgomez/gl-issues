package state

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRecentProjects(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := RecentProjects(); got != nil {
		t.Fatalf("empty state: %v", got)
	}
	for _, p := range []string{"a", "b", "a", "c"} {
		if err := AddRecentProject(p); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := RecentProjects(), []string{"c", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for i := 0; i < 20; i++ {
		AddRecentProject(string(rune('a' + i)))
	}
	if n := len(RecentProjects()); n != maxRecent {
		t.Errorf("kept %d, want %d", n, maxRecent)
	}
}

func TestStateFileIsPrivateAndComplete(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if err := AddRecentProject("acme/tools/ground-tools"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(Dir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("state.json mode = %v, want 0600", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(Dir())
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestNoStateDirNeverUsesWorkingDir(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_STATE_HOME", "relative/dir")
	t.Setenv("HOME", "")
	if d := Dir(); d != "" {
		t.Skipf("this system still has a home directory (%s)", d)
	}
	if err := AddRecentProject("acme/x"); !errors.Is(err, ErrNoDir) {
		t.Errorf("want ErrNoDir, got %v", err)
	}
	if _, err := os.Stat("state.json"); !os.IsNotExist(err) {
		t.Error("state.json must not be written to the working directory")
	}
	if got := RecentProjects(); got != nil {
		t.Errorf("RecentProjects = %v", got)
	}
}

func TestSeenFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	a, b := SeenFile("gitlab.example.edu", "acme"), SeenFile("gitlab.example.edu", "acme/platform")
	if a == "" || a == b || a != SeenFile("gitlab.example.edu", "acme") || filepath.Dir(a) != Dir() {
		t.Fatalf("seen files: %q %q", a, b)
	}
	if _, existed, err := LoadSeen(a); existed || err != nil {
		t.Fatalf("missing file: existed %v, err %v", existed, err)
	}
	t0 := time.Date(2026, 5, 4, 10, 0, 0, 123e6, time.UTC)
	want := Seen{Host: "gitlab.example.edu", Group: "acme", Covered: []string{"opened"},
		Issues: map[int]SeenIssue{7: {UpdatedAt: t0, Notes: 2, State: "opened", Tagged: true}}}
	if err := SaveSeen(a, want); err != nil {
		t.Fatal(err)
	}
	got, existed, err := LoadSeen(a)
	if err != nil || !existed || !reflect.DeepEqual(got.Issues, want.Issues) || got.Group != "acme" {
		t.Fatalf("round trip: %+v, %v, %v", got, existed, err)
	}
	if !got.Issues[7].UpdatedAt.Equal(t0) {
		t.Error("updated_at must survive exactly")
	}
	st, err := os.Stat(a)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, %v", st.Mode().Perm(), err)
	}

	for _, bad := range []string{"{not json", "{}", "null"} {
		if err := os.WriteFile(a, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, existed, err := LoadSeen(a); !existed || err == nil {
			t.Errorf("corrupt %q: existed %v, err %v", bad, existed, err)
		}
	}
}

func TestDir(t *testing.T) {
	d := t.TempDir()
	t.Setenv("XDG_STATE_HOME", d)
	if Dir() != filepath.Join(d, "gl-issues") {
		t.Errorf("%s", Dir())
	}
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", d)
	if Dir() != filepath.Join(d, ".local", "state", "gl-issues") {
		t.Errorf("%s", Dir())
	}
}
