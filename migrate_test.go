package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gl-issues/internal/config"
)

// TestMain keeps every test in this package away from the real config
// and state directories and from the environment's GL_ISSUES_… and
// GITISSUE_… variables.
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
		if strings.HasPrefix(name, config.EnvPrefix) || strings.HasPrefix(name, config.LegacyEnvPrefix) {
			os.Unsetenv(name)
		}
	}
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// sandbox points HOME and the XDG directories at a fresh temp dir and
// returns it.
func sandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	return home
}

// writeOld creates the gitissue config and state directories as an
// older version left them: private modes, a config with a host and a
// state file.
func writeOld(t *testing.T, home string) (cfgDir, stateDir string) {
	t.Helper()
	cfgDir = filepath.Join(home, ".config", "gitissue")
	stateDir = filepath.Join(home, ".local", "state", "gitissue")
	for _, d := range []string{cfgDir, stateDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Chmod(d, 0o700)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("host = \"gitlab.example.edu\"\ngroup = \"acme\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stateDir, "state.json"), []byte(`{"recent_projects":["acme/platform"]}`), 0o600)
	os.WriteFile(filepath.Join(stateDir, "seen-abc.json"), []byte(`{}`), 0o600)
	return cfgDir, stateDir
}

func TestMigrateMovesOldDirs(t *testing.T) {
	home := sandbox(t)
	oldCfg, oldState := writeOld(t, home)
	newCfg := filepath.Join(home, ".config", "gl-issues")
	newState := filepath.Join(home, ".local", "state", "gl-issues")

	var errOut bytes.Buffer
	migrateLegacy(&errOut, legacyDirs(false))
	want := "gl-issues: moved your settings from " + oldCfg + " to " + newCfg + " (gitissue was renamed)\n" +
		"gl-issues: moved your state from " + oldState + " to " + newState + " (gitissue was renamed)\n"
	if errOut.String() != want {
		t.Errorf("messages:\n%s\nwant:\n%s", errOut.String(), want)
	}
	for _, old := range []string{oldCfg, oldState} {
		if _, err := os.Lstat(old); !os.IsNotExist(err) {
			t.Errorf("%s still exists: %v", old, err)
		}
	}
	if _, err := os.Stat(filepath.Join(newState, "seen-abc.json")); err != nil {
		t.Errorf("seen file not moved: %v", err)
	}

	// The rename keeps the private modes, and the config loads from the
	// new default path.
	if fi, err := os.Stat(newCfg); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("config dir mode: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(newCfg, "config.toml")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("config file mode: %v %v", fi, err)
	}
	cfg, err := config.Load("")
	if err != nil || cfg.Host != "gitlab.example.edu" || cfg.Path != filepath.Join(newCfg, "config.toml") {
		t.Errorf("load after move: %+v %v", cfg, err)
	}

	// A second run has nothing to say.
	errOut.Reset()
	migrateLegacy(&errOut, legacyDirs(false))
	if errOut.Len() != 0 {
		t.Errorf("second run: %q", errOut.String())
	}
	if n := renameNotes(); len(n) != 0 {
		t.Errorf("doctor notes after a clean move: %v", n)
	}
}

// doctor is read-only: it leaves the old directories where they are and
// reports them; any other command moves them.
func TestDoctorDoesNotMigrate(t *testing.T) {
	home := sandbox(t)
	cfgDir, stateDir := writeOld(t, home)
	var errOut bytes.Buffer
	migrateLegacyFor("doctor", &errOut, legacyDirs(false))
	for _, d := range []string{cfgDir, stateDir} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("doctor moved %s", d)
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("doctor wrote %q", errOut.String())
	}
	var old []string
	for _, n := range renameNotes() {
		if strings.Contains(n.text, "directory") {
			old = append(old, n.text)
		}
	}
	if len(old) != 2 {
		t.Errorf("doctor should report both old directories, got %q", old)
	}

	migrateLegacyFor("", &errOut, legacyDirs(false))
	if _, err := os.Stat(cfgDir); err == nil {
		t.Error("the dashboard did not move the old config directory")
	}
	if !strings.Contains(errOut.String(), "moved your settings") {
		t.Errorf("output %q", errOut.String())
	}
}

// A world-readable config is still refused after the move.
func TestMigrateKeepsPermissionCheck(t *testing.T) {
	home := sandbox(t)
	oldCfg, _ := writeOld(t, home)
	os.Chmod(filepath.Join(oldCfg, "config.toml"), 0o644)
	migrateLegacy(&bytes.Buffer{}, legacyDirs(false))
	_, err := config.Load("")
	if err == nil || !strings.Contains(err.Error(), "readable by other users") {
		t.Errorf("want the permission error, got %v", err)
	}
}

func TestMigrateBothExist(t *testing.T) {
	home := sandbox(t)
	oldCfg, oldState := writeOld(t, home)
	newCfg := filepath.Join(home, ".config", "gl-issues")
	os.MkdirAll(newCfg, 0o700)
	os.WriteFile(filepath.Join(newCfg, "config.toml"), []byte("host = \"new.example.edu\"\n"), 0o600)

	var errOut bytes.Buffer
	migrateLegacy(&errOut, legacyDirs(false))
	lines := strings.Split(strings.TrimSpace(errOut.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "gl-issues: ignoring "+oldCfg+": using "+newCfg) ||
		!strings.HasPrefix(lines[1], "gl-issues: moved your state from "+oldState) {
		t.Errorf("messages:\n%s", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(oldCfg, "config.toml")); err != nil {
		t.Errorf("old config must stay: %v", err)
	}
	if cfg, err := config.Load(""); err != nil || cfg.Host != "new.example.edu" {
		t.Errorf("must use the new config: %+v %v", cfg, err)
	}
	notes := renameNotes()
	if len(notes) != 1 || !strings.Contains(notes[0].text, "old settings directory "+oldCfg+" is still there") {
		t.Errorf("doctor notes: %v", notes)
	}
}

func TestMigrateSymlinkNotMoved(t *testing.T) {
	home := sandbox(t)
	real := filepath.Join(home, "dotfiles", "gitissue")
	os.MkdirAll(real, 0o700)
	os.MkdirAll(filepath.Join(home, ".config"), 0o700)
	oldCfg := filepath.Join(home, ".config", "gitissue")
	if err := os.Symlink(real, oldCfg); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	migrateLegacy(&errOut, legacyDirs(false))
	if got := errOut.String(); !strings.Contains(got, oldCfg+" is a symlink, not moved") || strings.Count(got, "\n") != 1 {
		t.Errorf("messages: %q", got)
	}
	if fi, err := os.Lstat(oldCfg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink touched: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".config", "gl-issues")); !os.IsNotExist(err) {
		t.Errorf("new dir created: %v", err)
	}
}

// With -config, $GL_ISSUES_CONFIG or $GITISSUE_CONFIG the config
// directory is left alone; the state directory still moves.
func TestMigrateExplicitConfig(t *testing.T) {
	for _, env := range []string{"", "GL_ISSUES_CONFIG", "GITISSUE_CONFIG"} {
		home := sandbox(t)
		oldCfg, oldState := writeOld(t, home)
		flagPath := ""
		if env == "" {
			flagPath = filepath.Join(home, "other.toml")
		} else {
			t.Setenv(env, filepath.Join(home, "other.toml"))
		}
		_, explicit, err := config.ResolvePath(flagPath)
		if err != nil || !explicit {
			t.Fatalf("%s: explicit %v, %v", env, explicit, err)
		}
		var errOut bytes.Buffer
		migrateLegacy(&errOut, legacyDirs(explicit))
		if _, err := os.Stat(oldCfg); err != nil {
			t.Errorf("%s: config dir moved: %v", env, err)
		}
		if _, err := os.Stat(oldState); !os.IsNotExist(err) {
			t.Errorf("%s: state dir not moved: %v", env, err)
		}
		if strings.Contains(errOut.String(), "settings") {
			t.Errorf("%s: %q", env, errOut.String())
		}
		if env != "" {
			os.Unsetenv(env)
		}
	}
}

// doctor names deprecated variables, never their values.
func TestRenameNotesEnv(t *testing.T) {
	sandbox(t)
	t.Setenv("GITISSUE_TOKEN", "glpat-secret-value")
	t.Setenv("GITISSUE_HOST", "old.example.edu")
	t.Setenv("GL_ISSUES_HOST", "gitlab.example.edu")
	var out bytes.Buffer
	runDoctor(&out, goodDoctor(), config.Config{Host: "gitlab.example.edu", Group: "acme"}, "/x/config.toml", nil, renameNotes())
	got := out.String()
	for _, want := range []string{
		"! GITISSUE_HOST is deprecated: use GL_ISSUES_HOST (ignored: GL_ISSUES_HOST is set)",
		"! GITISSUE_TOKEN is no longer read (ignored): gl-issues uses glab's login; remove it\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("doctor lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "glpat-secret-value") || strings.Contains(got, "old.example.edu") {
		t.Errorf("a value was printed:\n%s", got)
	}
}

// migrateGlab puts a fake glab first in PATH that saves its stdin and
// arguments in dir and exits with $GLAB_EXIT (0 if unset), echoing what
// it read on stderr when it fails.
func migrateGlab(t *testing.T) (stdin, args string) {
	t.Helper()
	dir := t.TempDir()
	stdin, args = filepath.Join(dir, "stdin"), filepath.Join(dir, "args")
	script := "#!/bin/sh\ncat > '" + stdin + "'\nprintf '%s\\n' \"$@\" > '" + args + "'\n" +
		"[ \"${GLAB_EXIT:-0}\" = 0 ] || { echo \"error: rejected $(cat '" + stdin + "')\" >&2; exit 1; }\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return stdin, args
}

// migrate-token hands the file's token to glab on stdin, then removes
// only the token line, keeping the file private; the token is never shown.
func TestMigrateToken(t *testing.T) {
	stdin, args := migrateGlab(t)
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	const secret = "glpat-SECRETvalue"
	body := "# mine\nhost = \"gitlab.example.edu\"\n\n# old login\ntoken = \"" + secret + "\"  # keep safe\ngroup = \"acme\"\n"
	os.WriteFile(cfgFile, []byte(body), 0o600)

	var out bytes.Buffer
	if err := setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(stdin); string(got) != secret+"\n" {
		t.Errorf("glab's stdin %q", got)
	}
	if got, _ := os.ReadFile(args); string(got) != "auth\nlogin\n--hostname\ngitlab.example.edu\n--stdin\n" {
		t.Errorf("glab's arguments %q", got)
	}
	want := "# mine\nhost = \"gitlab.example.edu\"\n\n# old login\ngroup = \"acme\"\n"
	if got, _ := os.ReadFile(cfgFile); string(got) != want {
		t.Errorf("file now:\n%s", got)
	}
	if fi, _ := os.Stat(cfgFile); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", fi.Mode().Perm())
	}
	if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "removed the token line from "+cfgFile) {
		t.Errorf("output:\n%s", out.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}

	// Nothing left to migrate: glab is not run.
	os.Remove(stdin)
	out.Reset()
	if err := setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out); err != nil || !strings.Contains(out.String(), "nothing to migrate") {
		t.Errorf("second run: %v %q", err, out.String())
	}
	if _, err := os.Stat(stdin); err == nil {
		t.Error("glab run without a token")
	}
}

// When glab refuses the token the file is left exactly as it was, and
// the error does not show the token even if glab echoes it.
func TestMigrateTokenGlabFails(t *testing.T) {
	migrateGlab(t)
	t.Setenv("GLAB_EXIT", "1")
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	const secret = "glpat-SECRETvalue"
	body := "host = \"gitlab.example.edu\"\ntoken = '" + secret + "'\n"
	os.WriteFile(cfgFile, []byte(body), 0o600)
	var out bytes.Buffer
	err := setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out)
	if err == nil || strings.Contains(err.Error()+out.String(), secret) || !strings.Contains(err.Error(), cfgFile+" is unchanged") {
		t.Errorf("err %v, output %q", err, out.String())
	}
	if got, _ := os.ReadFile(cfgFile); string(got) != body {
		t.Errorf("file changed:\n%s", got)
	}
}

// An empty token line is just removed; a symlinked config file keeps its
// symlink; without a host nothing happens.
func TestMigrateTokenEdgeCases(t *testing.T) {
	stdin, _ := migrateGlab(t)
	dir := t.TempDir()
	real := filepath.Join(dir, "real.toml")
	link := filepath.Join(dir, "config.toml")
	os.WriteFile(real, []byte("host = \"gitlab.example.edu\"\ntoken = \"\"\n"), 0o600)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := setupCommand("migrate-token", nil, link, link, nil, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stdin); err == nil {
		t.Error("glab run for an empty token")
	}
	if got, _ := os.ReadFile(real); string(got) != "host = \"gitlab.example.edu\"\n" {
		t.Errorf("file now %q", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced")
	}

	noHost := filepath.Join(dir, "nohost.toml")
	os.WriteFile(noHost, []byte("token = \"glpat-x\"\n"), 0o600)
	if err := setupCommand("migrate-token", nil, noHost, noHost, nil, &out); err == nil || !strings.Contains(err.Error(), "no GitLab host set") {
		t.Errorf("no host: %v", err)
	}
	if got, _ := os.ReadFile(noHost); string(got) != "token = \"glpat-x\"\n" {
		t.Error("file changed without a host")
	}
}

// An edit made to the config file while glab logs in is never lost: the
// file is left as the user changed it, and the message says glab has the
// token and the token line is still there.
func TestMigrateTokenFileChangedDuringLogin(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	const secret = "glpat-SECRETvalue"
	os.WriteFile(cfgFile, []byte("host = \"gitlab.example.edu\"\ntoken = \""+secret+"\"\n"), 0o600)
	bin := t.TempDir()
	stdin := filepath.Join(bin, "stdin")
	script := "#!/bin/sh\ncat > '" + stdin + "'\nprintf 'view = \"compact\"\\n' >> '" + cfgFile + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var out bytes.Buffer
	err := setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "glab has the token now") ||
		!strings.Contains(err.Error(), "delete its token line yourself") || strings.Contains(err.Error()+out.String(), secret) {
		t.Errorf("err %v, output %q", err, out.String())
	}
	if got, _ := os.ReadFile(stdin); string(got) != secret+"\n" {
		t.Errorf("glab's stdin %q", got)
	}
	want := "host = \"gitlab.example.edu\"\ntoken = \"" + secret + "\"\nview = \"compact\"\n"
	if got, _ := os.ReadFile(cfgFile); string(got) != want {
		t.Errorf("the edit made during the login was lost:\n%s", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("temp file left behind: %v", entries)
	}
}

// Every read and the rewrite use the file the symlink pointed to at the
// start: retargeting the symlink during the login changes nothing about
// which file is checked and rewritten, and the other file is untouched.
func TestMigrateTokenSymlinkRetargeted(t *testing.T) {
	dir := t.TempDir()
	first, other, link := filepath.Join(dir, "first.toml"), filepath.Join(dir, "other.toml"), filepath.Join(dir, "config.toml")
	os.WriteFile(first, []byte("host = \"gitlab.example.edu\"\ntoken = \"glpat-x\"\n"), 0o600)
	otherBody := "host = \"evil.example\"\ntoken = \"glpat-y\"\n"
	os.WriteFile(other, []byte(otherBody), 0o600)
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	args := filepath.Join(bin, "args")
	script := "#!/bin/sh\ncat > /dev/null\nprintf '%s\\n' \"$@\" > '" + args + "'\nln -sfn '" + other + "' '" + link + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := setupCommand("migrate-token", nil, link, link, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(args); !strings.Contains(string(got), "gitlab.example.edu") {
		t.Errorf("login for the wrong host: %q", got)
	}
	if got, _ := os.ReadFile(first); string(got) != "host = \"gitlab.example.edu\"\n" {
		t.Errorf("first file %q", got)
	}
	if got, _ := os.ReadFile(other); string(got) != otherBody {
		t.Errorf("the new target was changed: %q", got)
	}
}

// Two migrate-token runs never overlap: while one holds the config
// directory's lock, another changes nothing and says why.
func TestMigrateTokenLocked(t *testing.T) {
	stdin, _ := migrateGlab(t)
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	body := "host = \"gitlab.example.edu\"\ntoken = \"glpat-SECRETvalue\"\n"
	os.WriteFile(cfgFile, []byte(body), 0o600)
	unlock, err := lockDir(filepath.Dir(cfgFile))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "another gl-issues migrate-token") {
		t.Errorf("err %v", err)
	}
	if got, _ := os.ReadFile(cfgFile); string(got) != body {
		t.Errorf("the file changed while locked:\n%s", got)
	}
	if _, err := os.Stat(stdin); err == nil {
		t.Error("glab was called while locked")
	}
	unlock()
	if err := setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out); err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
}
