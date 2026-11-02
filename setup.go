package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gl-issues/internal/config"
	"gl-issues/internal/gl"
)

// configTemplate is the commented config template, built into the binary
// so `gl-issues init` works for release and package installs too.
//
//go:embed config.example.toml
var configTemplate []byte

// initConfig writes the config template to path: the directory is created
// private (0700), the file 0600, and an existing file is never replaced.
func initConfig(path string, out io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s already exists; edit it, or remove it to start over", path)
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(configTemplate)
	if err := errors.Join(werr, f.Close()); err != nil {
		os.Remove(path)
		return err
	}
	fmt.Fprintf(out, `created %s (mode 600). Next:
  1. set host and group in it: ${EDITOR:-nano} %s
  2. log glab in (glab 1.123+ keeps the token in the OS keyring; doctor says where): glab auth login --hostname <host>
  3. check the setup: gl-issues doctor
`, path, shellQuote(path))
	return nil
}

// shellQuote quotes s for a shell command line when it needs it.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+:@%,=") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// missingSetting explains a missing host or group, pointing at the file
// to set it in and at `gl-issues init` when that file does not exist yet.
func missingSetting(cfg config.Config, path, setting, flag string) error {
	where := "set " + setting + " in " + path
	if cfg.Path == "" {
		where = "run `gl-issues init` to create " + path + ", then set " + setting + " there"
	}
	what := map[string]string{"host": "no GitLab host set", "group": "no group set"}[setting]
	return fmt.Errorf("%s: %s (or pass %s)", what, where, flag)
}

// loadConfig loads the config file and applies the global flags.
func loadConfig(cfgPath string, flags map[string]string) (config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return cfg, err
	}
	return applyFlags(cfg, flags)
}

// applyFlags applies the global flags to a loaded config.
func applyFlags(cfg config.Config, flags map[string]string) (config.Config, error) {
	// host first: a group URL is checked against it
	for _, f := range []struct{ key, flag string }{
		{"host", "host"}, {"group", "group"}, {"refresh", "refresh"}, {"view", "view"}, {"tree_depth", "tree"},
	} {
		if v := flags[f.key]; v != "" {
			if err := cfg.Set(f.key, v, "flag -"+f.flag); err != nil {
				return cfg, fmt.Errorf("flag -%s: %w", f.flag, err)
			}
		}
	}
	return cfg, nil
}

func newClient(cfg config.Config) gl.Client {
	return gl.Client{Host: cfg.Host}
}

// tokenWarning is the one-line warning about a token line left in the
// config file by older versions, or "" when there is none.
func tokenWarning(cfg config.Config) string {
	if !cfg.FileToken {
		return ""
	}
	return "ignoring the token in " + cfg.Path + " (gl-issues uses glab's login now); " +
		"move it to glab's login with: gl-issues migrate-token"
}

// errDoctorFound makes doctor exit 1 once it has printed its report.
var errDoctorFound = errors.New("doctor found problems (✗ above)")

// setupCommand runs init or doctor, which must work before the config is
// complete or even valid, config, which shows it, or migrate-token. path
// is the config file gl-issues uses.
func setupCommand(name string, args []string, cfgPath, path string, flags map[string]string, out io.Writer) error {
	if wantsHelp(name, args) {
		fmt.Fprintln(out, map[string]string{
			"init":   "Usage: gl-issues [-config file] init\nWrites the commented config template to " + path + " (never overwrites).",
			"doctor": "Usage: gl-issues [flags] doctor\nChecks the config, glab, the login and where glab keeps the token, the group and the token scopes (read-only).",
			"config": "Usage: gl-issues [flags] config\nShows every setting, its value and where it comes from (" + path + ", environment, flags).",
			"migrate-token": "Usage: gl-issues [flags] migrate-token\nHands the token older versions kept in " + path + " to glab's login for the host\n" +
				"(glab auth login --hostname <host> --stdin; glab 1.123+ keeps it in the OS keyring, doctor says where),\nthen removes the token line from the file. " +
				"If glab fails, the file is left unchanged.",
		}[name])
		return nil
	}
	if err := noArgs(name, args); err != nil {
		return err
	}
	switch name {
	case "init":
		return initConfig(path, out)
	case "migrate-token":
		return migrateToken(path, flags, out)
	case "config":
		cfg, err := loadConfig(cfgPath, flags)
		if err != nil {
			return err
		}
		printConfig(out, cfg, path)
		return nil
	}
	cfg, err := loadConfig(cfgPath, flags)
	if !runDoctor(out, newClient(cfg), cfg, path, err, renameNotes()) {
		return errDoctorFound
	}
	return nil
}
