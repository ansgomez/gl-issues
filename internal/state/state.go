// Package state keeps small, non-secret per-user data between runs, such
// as recently used projects and the issues last seen on each dashboard
// (seen.go). It lives under $XDG_STATE_HOME/gl-issues (or
// ~/.local/state/gl-issues), separate from the config file.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const maxRecent = 10

// ErrNoDir is returned when there is no usable state directory (no
// $XDG_STATE_HOME and no home directory).
var ErrNoDir = errors.New("no state directory: set $HOME or $XDG_STATE_HOME")

// Dir returns the state directory, or "" if there is none. A relative
// $XDG_STATE_HOME is ignored, as the XDG spec requires.
func Dir() string { return dirFor("gl-issues") }

// LegacyDir returns the state directory of gitissue, the tool's old name,
// by the same rules as Dir; main moves it to Dir once.
func LegacyDir() string { return dirFor("gitissue") }

func dirFor(name string) string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, name)
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(home, ".local", "state", name)
}

type file struct {
	RecentProjects []string `json:"recent_projects"`
}

// RecentProjects returns recently used project paths, most recent first.
// Errors are ignored: missing state is not a problem.
func RecentProjects() []string {
	dir := Dir()
	if dir == "" {
		return nil // never fall back to ./state.json
	}
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil
	}
	var f file
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return f.RecentProjects
}

// AddRecentProject moves path to the front of the recent list.
func AddRecentProject(path string) error {
	dir := Dir()
	if dir == "" {
		return ErrNoDir
	}
	list := []string{path}
	for _, p := range RecentProjects() {
		if p != path && len(list) < maxRecent {
			list = append(list, p)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(file{RecentProjects: list}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, "state.json"), b)
}

// writeAtomic replaces name with data via a private temp file and a
// rename, so a crash or a second gl-issues never leaves a half-written file.
func writeAtomic(name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".state-*.tmp") // mode 0600
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
