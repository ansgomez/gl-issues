package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gl-issues/internal/config"
	"gl-issues/internal/gl"
)

func TestInitConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "gl-issues", "config.toml")
	var out bytes.Buffer
	if err := initConfig(path, &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(b, configTemplate) || len(b) == 0 {
		t.Fatalf("file does not hold the template: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %o, want 600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %o, want 700", fi.Mode().Perm())
	}
	for _, want := range []string{path, "host and group", "${EDITOR:-nano} " + path, "glab auth login", "gl-issues doctor"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("next steps lack %q:\n%s", want, out.String())
		}
	}
	// The written template loads (and is private enough to be read).
	if _, err := config.Load(path); err != nil {
		t.Errorf("template does not load: %v", err)
	}

	os.WriteFile(path, []byte("mine"), 0o600)
	err = initConfig(path, &out)
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), path) {
		t.Errorf("must refuse to overwrite, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "mine" {
		t.Error("existing file was changed")
	}
}

// init writes to the -config path, else $GL_ISSUES_CONFIG, else the default.
func TestInitPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("GL_ISSUES_CONFIG", "")
	if p, _, _ := config.ResolvePath(""); p != filepath.Join(dir, "gl-issues", "config.toml") {
		t.Errorf("default path %s", p)
	}
	t.Setenv("GL_ISSUES_CONFIG", filepath.Join(dir, "env.toml"))
	if p, _, _ := config.ResolvePath(""); p != filepath.Join(dir, "env.toml") {
		t.Errorf("env path %s", p)
	}
	if p, _, _ := config.ResolvePath("flag.toml"); p != "flag.toml" {
		t.Errorf("flag path %s", p)
	}
	var out bytes.Buffer
	if err := setupCommand("init", nil, "", filepath.Join(dir, "env.toml"), nil, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "env.toml")); err != nil {
		t.Error(err)
	}
}

func TestMissingSetting(t *testing.T) {
	err := missingSetting(config.Config{}, "/x/config.toml", "host", "-host")
	if !strings.Contains(err.Error(), "no GitLab host set") || !strings.Contains(err.Error(), "gl-issues init") {
		t.Errorf("without a file, point at init: %v", err)
	}
	err = missingSetting(config.Config{Path: "/x/config.toml"}, "/x/config.toml", "group", "-g group/path")
	if !strings.Contains(err.Error(), "set group in /x/config.toml") || strings.Contains(err.Error(), "init") {
		t.Errorf("with a file, point at it: %v", err)
	}
}

// fakeDoctor answers doctor's questions from fields.
type fakeDoctor struct {
	version          string
	versionErr       error
	me               gl.User
	meErr            error
	group            gl.Group
	groupErr         error
	level            int
	token            gl.TokenInfo
	tokenErr         error
	auth             gl.AuthStatus
	authErr          error
	groupAsked, toks bool
}

func (f *fakeDoctor) Version(context.Context) (string, error) { return f.version, f.versionErr }
func (f *fakeDoctor) Me(context.Context) (gl.User, error)     { return f.me, f.meErr }
func (f *fakeDoctor) Group(context.Context, string) (gl.Group, error) {
	f.groupAsked = true
	return f.group, f.groupErr
}
func (f *fakeDoctor) AccessLevel(context.Context, string, int) (int, error) { return f.level, nil }
func (f *fakeDoctor) TokenSelf(context.Context) (gl.TokenInfo, error) {
	f.toks = true
	return f.token, f.tokenErr
}

func (f *fakeDoctor) AuthStatus(context.Context) (gl.AuthStatus, error) { return f.auth, f.authErr }

func goodDoctor() *fakeDoctor {
	return &fakeDoctor{version: "glab 1.50.0", me: gl.User{ID: 5, Username: "alice"},
		group: gl.Group{ID: 9, FullPath: "acme/platform"}, level: 30, token: gl.TokenInfo{Scopes: []string{"api"}},
		auth: gl.AuthStatus{Storage: gl.StorageKeyring, Protocol: "https"}}
}

func doctorCfg() config.Config {
	cfg := config.Defaults()
	cfg.Host, cfg.Group, cfg.Path = "gitlab.example.edu", "acme/platform", "/x/config.toml"
	return cfg
}

func TestDoctor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*fakeDoctor, *config.Config)
		ok     bool
		want   []string
	}{
		{"all good", func(*fakeDoctor, *config.Config) {}, true,
			[]string{"✓ config: /x/config.toml", "✓ glab: glab 1.50.0", "✓ logged in to gitlab.example.edu as @alice\n",
				"✓ glab keeps the token for gitlab.example.edu in the OS keyring",
				"✓ group acme/platform is visible, you are developer", "✓ token scopes: api"}},
		{"token left in config", func(_ *fakeDoctor, c *config.Config) { c.FileToken = true }, true,
			[]string{"! config: the token in /x/config.toml is ignored (gl-issues uses glab's login)",
				"→ hand it to glab and remove it from the file: gl-issues migrate-token"}},
		{"token left in config, not logged in", func(f *fakeDoctor, c *config.Config) {
			c.FileToken = true
			f.meErr = &gl.APIError{Reason: "401 Unauthorized", Request: "GET user", Unauthorized: true}
		}, false, []string{"→ run gl-issues migrate-token (hands the token in /x/config.toml to glab), or glab auth login --hostname gitlab.example.edu\n"}},
		{"plaintext token", func(f *fakeDoctor, _ *config.Config) { f.auth.Storage = gl.StoragePlaintext }, true,
			[]string{"! glab keeps the token for gitlab.example.edu in its config file, in plain text",
				"→ with glab 1.123+ and an unlocked OS keyring, log in again to move it there: glab auth login --hostname gitlab.example.edu\n"}},
		{"api over http", func(f *fakeDoctor, _ *config.Config) { f.auth.Protocol = "http" }, true,
			[]string{"! glab makes API calls to gitlab.example.edu over http, not https",
				"→ glab config set api_protocol https --host gitlab.example.edu"}},
		{"storage unknown", func(f *fakeDoctor, _ *config.Config) { f.auth = gl.AuthStatus{} }, true,
			[]string{"! token storage unknown: glab auth status does not say", "→ see glab auth status --hostname gitlab.example.edu"}},
		{"auth status fails", func(f *fakeDoctor, _ *config.Config) { f.authErr = errors.New("glab auth status: exit status 1") }, true,
			[]string{"! token storage unknown: glab auth status: exit status 1"}},
		{"read_api only", func(f *fakeDoctor, _ *config.Config) { f.token.Scopes = []string{"read_api"} }, true,
			[]string{"! token scopes: read_api (read-only)", "→ browsing works; comment/close/assign/describe/new need the api scope"}},
		{"scopes unknown", func(f *fakeDoctor, _ *config.Config) { f.tokenErr = errors.New("404 Not Found") }, true,
			[]string{"! token scopes unknown"}},
		{"no file", func(_ *fakeDoctor, c *config.Config) { c.Path = "" }, true,
			[]string{"! config: no file at /x/config.toml", "→ create it with `gl-issues init`"}},
		{"no host", func(_ *fakeDoctor, c *config.Config) { c.Host = "" }, false,
			[]string{"✗ host: not set", "→ set host in /x/config.toml"}},
		{"not logged in", func(f *fakeDoctor, _ *config.Config) {
			f.meErr = &gl.APIError{Reason: "401 Unauthorized", Hint: "check glab auth status", Request: "GET user", Unauthorized: true}
		}, false, []string{"✗ not logged in to gitlab.example.edu: 401 Unauthorized (GET user)\n",
			"→ run glab auth login --hostname gitlab.example.edu\n"}},
		{"not logged in, port", func(f *fakeDoctor, c *config.Config) {
			c.Host = "gitlab.example.edu:8443"
			f.meErr = &gl.APIError{Reason: "Unauthenticated.", Request: "GET user", Unauthorized: true}
		}, false, []string{"→ run glab auth login --hostname gitlab.example.edu:8443\n"}},
		{"other login error", func(f *fakeDoctor, _ *config.Config) {
			f.meErr = &gl.APIError{Reason: "HTTP 500", Request: "GET user"}
		}, false, []string{"✗ cannot check the login on gitlab.example.edu: HTTP 500 (GET user)"}},
		{"unreachable", func(f *fakeDoctor, _ *config.Config) {
			f.meErr = errors.New("dial tcp: lookup gitlab.example.edu: no such host (GET user)")
		}, false, []string{"✗ cannot reach gitlab.example.edu", "VPN"}},
		{"group missing", func(f *fakeDoctor, _ *config.Config) {
			f.groupErr = errors.New(`group "acme/platform" not found, or you have no access (GET groups/acme%2Fplatform)`)
		}, false, []string{`✗ group "acme/platform" not found, or you have no access`, "→ set group to the path from the group's URL (what follows https://gitlab.example.edu/)"}},
		{"glab missing", func(f *fakeDoctor, _ *config.Config) { f.versionErr = gl.ErrNoGlab }, false,
			[]string{"✗ glab: not found in PATH", "→ install glab"}},
		{"evil server text", func(f *fakeDoctor, _ *config.Config) { f.me.Username = "ali\x1b]0;x\x07ce" }, true,
			[]string{"as @alice"}},
	} {
		f, cfg := goodDoctor(), doctorCfg()
		tc.change(f, &cfg)
		var out bytes.Buffer
		ok := runDoctor(&out, f, cfg, "/x/config.toml", nil, nil)
		if ok != tc.ok {
			t.Errorf("%s: ok = %v, want %v\n%s", tc.name, ok, tc.ok, out.String())
		}
		if strings.Count(out.String(), "glab auth login") > 1 {
			t.Errorf("%s: the fix is shown twice:\n%s", tc.name, out.String())
		}
		for _, w := range tc.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: output lacks %q:\n%s", tc.name, w, out.String())
			}
		}
		if strings.ContainsAny(out.String(), "\x1b\x07") {
			t.Errorf("%s: control characters printed", tc.name)
		}
	}
}

// doctor through a fake glab, as the CLI runs it.
func TestDoctorCLI(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgFile, []byte("host = \"gitlab.example.edu\"\ngroup = \"acme\"\n"), 0o600)
	user := `echo '{"id":5,"username":"alice"}'`
	group := `echo '{"id":9,"full_path":"acme"}'`
	member := `echo '{"access_level":40}'`
	tokens := `echo '{"name":"t","scopes":["api","read_user"]}'`
	status := `echo '  ✓ API calls for gitlab.example.edu are made over https protocol.' >&2; ` +
		`echo '  ✓ Token found in configuration file (plaintext): glpat-SECRET' >&2`
	glab := func(user, group, tokens string) string {
		return "case \"$*\" in\n" +
			"  --version) echo 'glab 1.50.0 (2026-01-01)';;\n" +
			"  'auth status --hostname gitlab.example.edu') " + status + ";;\n" +
			"  *' user') " + user + ";;\n" +
			"  *'groups/acme/members/all/5') " + member + ";;\n" +
			"  *'groups/acme') " + group + ";;\n" +
			"  *'personal_access_tokens/self') " + tokens + ";;\n" +
			"esac"
	}
	fail := func(msg string) string { return "echo '" + msg + "' >&2; exit 1" }
	for _, tc := range []struct {
		name, script string
		ok           bool
		want         string
	}{
		{"ok", glab(user, group, tokens), true, "✓ group acme is visible, you are maintainer"},
		{"not logged in", glab(fail("glab: 401 Unauthorized (HTTP 401)"), group, tokens), false, "✗ not logged in to gitlab.example.edu"},
		{"read_api only", glab(user, group, `echo '{"scopes":["read_api"]}'`), true, "! token scopes: read_api (read-only)"},
		{"group 404", glab(user, fail("ERROR 404 Not Found"), tokens), false, `✗ group "acme" not found, or you have no access`},
		{"plaintext", glab(user, group, tokens), true, "! glab keeps the token for gitlab.example.edu in its config file, in plain text"},
		{"glab missing", "", false, "✗ glab: not found in PATH"},
	} {
		bin := t.TempDir()
		path := bin
		if tc.script != "" {
			os.WriteFile(filepath.Join(bin, "glab"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700)
			path += string(os.PathListSeparator) + os.Getenv("PATH")
		}
		t.Setenv("PATH", path)
		var out bytes.Buffer
		err := setupCommand("doctor", nil, cfgFile, cfgFile, nil, &out)
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok %v\n%s", tc.name, err, tc.ok, out.String())
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: output lacks %q:\n%s", tc.name, tc.want, out.String())
		}
		if strings.Contains(out.String(), "SECRET") {
			t.Errorf("%s: token text printed:\n%s", tc.name, out.String())
		}
	}
}

// tree_depth: config file < GL_ISSUES_TREE_DEPTH < -tree.
func TestTreeDepthPrecedence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("tree_depth = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GL_ISSUES_TREE_DEPTH", "")
	for _, tc := range []struct {
		env, flag string
		want      int
		source    string
	}{
		{"", "", 1, p},
		{"2", "", 2, "env GL_ISSUES_TREE_DEPTH"},
		{"2", "0", 0, "flag -tree"},
	} {
		t.Setenv("GL_ISSUES_TREE_DEPTH", tc.env)
		cfg, err := loadConfig(p, map[string]string{"tree_depth": tc.flag})
		if err != nil || cfg.TreeDepth != tc.want || cfg.Source["tree_depth"] != tc.source {
			t.Errorf("env %q flag %q: %d from %q, %v", tc.env, tc.flag, cfg.TreeDepth, cfg.Source["tree_depth"], err)
		}
	}
	if _, err := loadConfig(p, map[string]string{"tree_depth": "5"}); err == nil {
		t.Error("-tree 5 accepted")
	}
}

// A config that does not load (here: readable by others) is the one
// problem reported; host and group are not called missing, and the hint
// does not send you to `gl-issues config`, which fails the same way.
func TestDoctorConfigError(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(cfgFile, []byte("host = \"gitlab.example.edu\"\ngroup = \"acme\"\n"), 0o600)
	if err := os.Chmod(cfgFile, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(cfgFile, nil)
	if err == nil {
		t.Fatal("a 0644 config must not load")
	}
	f := goodDoctor()
	var out bytes.Buffer
	if runDoctor(&out, f, cfg, cfgFile, err, nil) {
		t.Error("doctor passed")
	}
	got := out.String()
	for _, w := range []string{"✗ config: " + cfgFile + " is readable by other users", "chmod 600",
		"! host, group and login: not checked until the config error is fixed", "✓ glab: glab 1.50.0"} {
		if !strings.Contains(got, w) {
			t.Errorf("output lacks %q:\n%s", w, got)
		}
	}
	for _, bad := range []string{"not set", "gl-issues config", "logged in"} {
		if strings.Contains(got, bad) {
			t.Errorf("output has %q:\n%s", bad, got)
		}
	}
}

// The template as `gl-issues init` writes it, unedited: doctor says host
// and group are not set and where to set them.
func TestDoctorAfterInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("GL_ISSUES_HOST", "")
	t.Setenv("GL_ISSUES_GROUP", "")
	if err := initConfig(path, io.Discard); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if runDoctor(&out, goodDoctor(), cfg, path, nil, nil) {
		t.Errorf("doctor passed an unedited template:\n%s", out.String())
	}
	for _, w := range []string{"✗ host: not set", "✗ group: not set", "→ set host in " + path} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("output lacks %q:\n%s", w, out.String())
		}
	}
	if err := missingSetting(cfg, path, "host", "-host"); !strings.Contains(err.Error(), "set host in "+path) {
		t.Errorf("start without a host: %v", err)
	}
}

// Flag values that fail say which flag they came from.
func TestFlagErrorNamesFlag(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, nil, 0o600)
	for flag, v := range map[string]string{"host": "http://gitlab.example.edu", "tree_depth": "5", "refresh": "1s"} {
		_, err := loadConfig(p, map[string]string{flag: v})
		name := map[string]string{"tree_depth": "tree"}[flag]
		if name == "" {
			name = flag
		}
		if err == nil || !strings.HasPrefix(err.Error(), "flag -"+name+": ") {
			t.Errorf("-%s %s: %v", name, v, err)
		}
	}
}

// doctor through a fake glab for a host with a port: glab refuses
// --hostname host:port, so gl-issues must not pass it.
func TestDoctorCLIHostWithPort(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgFile, []byte("host = \"gitlab.example.edu:8443\"\ngroup = \"acme\"\n"), 0o600)
	bin := t.TempDir()
	// glab api refuses a port in --hostname; glab auth status takes the
	// host as its login is stored, port included, and reports it so.
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = api ]; then for a in \"$@\"; do case \"$a\" in *.edu:8443) [ \"$prev\" = --hostname ] && { echo 'invalid hostname' >&2; exit 1; };; esac; prev=$a; done; fi\n" +
		"[ \"$GITLAB_HOST\" = gitlab.example.edu:8443 ] || { echo 'wrong host' >&2; exit 1; }\n" +
		"case \"$*\" in\n" +
		"  --version) echo 'glab 1.123.0';;\n" +
		"  'auth status --hostname gitlab.example.edu:8443') {\n" +
		"    echo 'gitlab.example.edu:8443'\n" +
		"    echo '  ✓ Logged in to gitlab.example.edu:8443 as alice (keyring)'\n" +
		"    echo '  ✓ Git operations for gitlab.example.edu:8443 configured to use https protocol.'\n" +
		"    echo '  ✓ API calls for gitlab.example.edu:8443 are made over http protocol.'\n" +
		"    echo '  ✓ REST API Endpoint: http://gitlab.example.edu:8443/api/v4/'\n" +
		"    echo '  ✓ Token found in operating system keyring: **************************'\n" +
		"  } >&2;;\n" +
		"  'auth status'*) echo 'unexpected auth status arguments' >&2; exit 1;;\n" +
		"  *' user') echo '{\"id\":5,\"username\":\"alice\"}';;\n" +
		"  *'groups/acme/members/all/5') echo '{\"access_level\":30}';;\n" +
		"  *'groups/acme') echo '{\"id\":9,\"full_path\":\"acme\"}';;\n" +
		"  *'personal_access_tokens/self') echo '{\"scopes\":[\"api\"]}';;\n" +
		"esac\n"
	os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out bytes.Buffer
	if err := setupCommand("doctor", nil, cfgFile, cfgFile, nil, &out); err != nil {
		t.Errorf("doctor: %v\n%s", err, out.String())
	}
	for _, want := range []string{"✓ logged in to gitlab.example.edu:8443 as @alice",
		"✓ glab keeps the token for gitlab.example.edu:8443 in the OS keyring",
		"! glab makes API calls to gitlab.example.edu:8443 over http, not https",
		"→ glab config set api_protocol https --host gitlab.example.edu:8443"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "token storage unknown") {
		t.Errorf("auth status not understood:\n%s", out.String())
	}
}

// A config with a malformed token line still loads (the line is
// ignored); doctor points at migrate-token, never shows the token's text,
// and migrate-token reports the line without it, leaving the file as is.
func TestDoctorNeverShowsToken(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.toml")
	body := "host = \"gitlab.example.edu\"\ntoken = glpat-SECRETabc oops\ngroup = \"acme\"\n"
	os.WriteFile(cfgFile, []byte(body), 0o600)
	cfg, err := loadConfig(cfgFile, nil)
	if err != nil || cfg.Group != "acme" || !cfg.FileToken {
		t.Fatalf("a bad token line must not stop loading: %v", err)
	}
	var out bytes.Buffer
	runDoctor(&out, goodDoctor(), cfg, cfgFile, err, nil)
	if got := out.String(); strings.Contains(got, "SECRET") || !strings.Contains(got, "gl-issues migrate-token") {
		t.Errorf("doctor output:\n%s", got)
	}
	out.Reset()
	err = setupCommand("migrate-token", nil, cfgFile, cfgFile, nil, &out)
	if err == nil || strings.Contains(err.Error()+out.String(), "SECRET") || !strings.Contains(err.Error(), "line 2: token") {
		t.Errorf("migrate-token: %v %q", err, out.String())
	}
	if b, _ := os.ReadFile(cfgFile); string(b) != body {
		t.Error("file changed")
	}
}

// gl-issues config -h prints help, like init and doctor, without loading
// the config.
func TestConfigHelp(t *testing.T) {
	var out bytes.Buffer
	if err := setupCommand("config", []string{"-h"}, "/nonexistent/c.toml", "/nonexistent/c.toml", nil, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Usage: gl-issues [flags] config\n") {
		t.Errorf("got %q", out.String())
	}
	out.Reset()
	if err := setupCommand("config", []string{"extra"}, "", "", nil, &out); err == nil {
		t.Error("config takes no arguments")
	}
}
