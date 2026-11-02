package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Texts being edited go to a private directory in the user's cache, never
// the shared temp directory.
func TestEditDir(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	name, err := writeTemp("draft")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cache, "gl-issues")
	if filepath.Dir(name) != dir || !strings.HasSuffix(name, ".md") {
		t.Errorf("temp file %s, want one in %s", name, dir)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(name); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %o, want 600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(name); string(b) != "draft" {
		t.Errorf("content %q", b)
	}

	os.Chmod(dir, 0o755) // made too open by something else: tightened again
	if _, err := writeTemp("x"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o after reuse, want 700", fi.Mode().Perm())
	}

	// A relative XDG_CACHE_HOME is ignored (XDG spec): ~/.cache is used.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", "rel")
	if d, err := editDir(); err != nil || d != filepath.Join(home, ".cache", "gl-issues") {
		t.Errorf("fallback dir %s, %v", d, err)
	}

	// A symlink in place of the directory is refused.
	cache = t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if err := os.Symlink(t.TempDir(), filepath.Join(cache, "gl-issues")); err != nil {
		t.Fatal(err)
	}
	if _, err := editDir(); err == nil {
		t.Error("a symlinked edit directory must be refused")
	}
}

// The editor and the browser opener run without the token variables of
// the environment (only glab may see a token), but with the rest of it.
func TestChildEnvWithoutTokens(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "secret")
	t.Setenv("OAUTH_TOKEN", "secret")
	check := func(what string, env []string) {
		t.Helper()
		all := "\n" + strings.Join(env, "\n") + "\n"
		for _, k := range []string{"GITLAB_TOKEN", "OAUTH_TOKEN"} {
			if strings.Contains(all, "\n"+k+"=") {
				t.Errorf("%s gets %s", what, k)
			}
		}
		if !strings.Contains(all, "\nPATH=") {
			t.Errorf("%s does not get PATH", what)
		}
	}
	check("the editor", editorCmd("vi", "draft.md").Env)

	dir := t.TempDir()
	opener, marker := filepath.Join(dir, "opener"), filepath.Join(dir, "env")
	script := "#!/bin/sh\nenv > '" + marker + ".tmp' && mv '" + marker + ".tmp' '" + marker + "'\n"
	if err := os.WriteFile(opener, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if msg := openBrowser("https://gitlab.example.edu/x", "gitlab.example.edu", opener)().(browserMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(marker); err == nil {
			check("the browser opener", strings.Split(strings.TrimSpace(string(b)), "\n"))
			break
		}
		if time.Since(start) > 10*time.Second {
			t.Fatal("the opener did not run")
		}
	}
}

// openBrowser runs the browser setting, else the first of $BROWSER,
// xdg-open, wslview and sensible-browser found in PATH, with the URL as
// the last argument after any flags of its own, and says so when there
// is none. The openers are stubs that write their arguments to a file.
func TestOpenBrowser(t *testing.T) {
	const u = "https://gitlab.example.edu/acme/x/-/issues/1"
	stub := func(dir, name string) (path, marker string) {
		t.Helper()
		path, marker = filepath.Join(dir, name), filepath.Join(dir, name+".args")
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + marker + ".tmp' && mv '" + marker + ".tmp' '" + marker + "'\n"
		if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		return path, marker
	}
	args := func(marker string) string { // the opener runs on its own: wait for it
		t.Helper()
		for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(10 * time.Millisecond) {
			if b, err := os.ReadFile(marker); err == nil {
				os.Remove(marker)
				return string(b)
			}
		}
		t.Fatalf("%s: the opener did not run", filepath.Base(marker))
		return ""
	}
	open := func(override string) browserMsg { return openBrowser(u, "gitlab.example.edu", override)().(browserMsg) }

	// The browser setting, flags included.
	mine, mineArgs := stub(t.TempDir(), "my-browser")
	if msg := open(mine + " --new-window"); msg.err != nil || msg.url != u {
		t.Fatalf("browser setting: %+v", msg)
	}
	if got := args(mineArgs); got != "--new-window\n"+u+"\n" {
		t.Errorf("browser setting ran with %q", got)
	}
	if msg := open("no-such-browser-xyz"); msg.err == nil || !strings.Contains(msg.err.Error(), "no browser opener found") {
		t.Errorf("a browser setting that is not installed: %v", msg.err)
	}
	// An opener that fails (xdg-open without a desktop) is reported with
	// the URL to copy, not taken as success …
	failing := filepath.Join(t.TempDir(), "failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if msg := open(failing); msg.err == nil || !strings.Contains(msg.err.Error(), "exit status 3") || msg.url != u {
		t.Errorf("failing opener: %+v", msg)
	}
	// … and so is one that cannot start (found, but not a program) …
	notExec := filepath.Join(t.TempDir(), "not-exec")
	if err := os.WriteFile(notExec, []byte("not a program\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if msg := open(notExec); msg.err == nil || !strings.Contains(msg.err.Error(), "not-exec") || msg.url != u {
		t.Errorf("opener that cannot start: %+v", msg)
	}
	// … while one that keeps running (a browser started directly) has
	// opened the URL.
	defer func(d time.Duration) { openerWait = d }(openerWait)
	openerWait = 50 * time.Millisecond
	lingering := filepath.Join(t.TempDir(), "lingering")
	if err := os.WriteFile(lingering, []byte("#!/bin/sh\nsleep 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if msg := open(lingering); msg.err != nil {
		t.Errorf("lingering opener: %v", msg.err)
	}
	if runtime.GOOS == "darwin" {
		return // open(1) instead of the chain below
	}

	// Without the setting: $BROWSER when it is found …
	bin := t.TempDir()
	_, xdgArgs := stub(bin, "xdg-open")
	t.Setenv("PATH", bin+":/usr/bin:/bin") // the stub before any installed xdg-open; mv for the stubs
	t.Setenv("BROWSER", mine)
	if msg := open(""); msg.err != nil {
		t.Fatalf("$BROWSER: %v", msg.err)
	}
	if got := args(mineArgs); got != u+"\n" {
		t.Errorf("$BROWSER ran with %q", got)
	}
	// … else the next opener in PATH, also when $BROWSER fails or cannot start …
	for _, browser := range []string{"no-such-browser-xyz", failing, notExec} {
		t.Setenv("BROWSER", browser)
		if msg := open(""); msg.err != nil {
			t.Fatalf("xdg-open after BROWSER=%s: %v", filepath.Base(browser), msg.err)
		}
		if got := args(xdgArgs); got != u+"\n" {
			t.Errorf("xdg-open ran with %q", got)
		}
	}
	// … and with none, the URL to copy comes back with the error.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("BROWSER", "")
	if msg := open(""); msg.err == nil || !strings.Contains(msg.err.Error(), "no browser opener found") || msg.url != u {
		t.Errorf("no opener: %+v", msg)
	}
}
