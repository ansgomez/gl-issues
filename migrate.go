package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gl-issues/internal/config"
	"gl-issues/internal/gl"
	"gl-issues/internal/state"
)

// gl-issues was called gitissue. For one release its default directories
// are moved over once, and its GITISSUE_… variables still work with a
// warning (config.DeprecatedEnv). Remove that part after that release;
// the token migration at the end stays longer.

const renamedNote = "(gitissue was renamed)"

// legacyDir is one directory to move from the old name to the new one.
type legacyDir struct {
	what     string // "settings" or "state", for the messages
	old, new string
}

// legacyDirs returns the default directories to move: the state directory
// always, the config directory only when the config file is the default
// one (no -config, $GL_ISSUES_CONFIG or $GITISSUE_CONFIG).
func legacyDirs(explicitConfig bool) []legacyDir {
	var dirs []legacyDir
	if !explicitConfig {
		oldDir, err1 := config.LegacyDir()
		newPath, err2 := config.DefaultPath()
		if err1 == nil && err2 == nil {
			dirs = append(dirs, legacyDir{"settings", oldDir, filepath.Dir(newPath)})
		}
	}
	if oldDir, newDir := state.LegacyDir(), state.Dir(); oldDir != "" && newDir != "" {
		dirs = append(dirs, legacyDir{"state", oldDir, newDir})
	}
	return dirs
}

// migrateLegacy moves each old default directory to its new place when
// only the old one exists, and says so on errOut in one line. A symlink
// is never moved (only reported), and when both exist the new one is used
// and the old one reported as ignored. The rename keeps the private
// modes, so the config file's permission check still holds.
func migrateLegacy(errOut io.Writer, dirs []legacyDir) {
	for _, d := range dirs {
		if msg := moveLegacyDir(d); msg != "" {
			fmt.Fprintln(errOut, "gl-issues:", msg)
		}
	}
}

// migrateLegacyFor runs migrateLegacy for the subcommand cmd ("" for the
// dashboard), except for doctor: it is read-only and reports old
// directories instead (renameNotes).
func migrateLegacyFor(cmd string, errOut io.Writer, dirs []legacyDir) {
	if cmd == "doctor" {
		return
	}
	migrateLegacy(errOut, dirs)
}

// moveLegacyDir moves one directory and returns what to tell the user, or
// "" when there is nothing to say.
func moveLegacyDir(d legacyDir) string {
	oldInfo, err := os.Lstat(d.old)
	if err != nil {
		return "" // nothing left over (or nothing we can see)
	}
	if _, err := os.Lstat(d.new); err == nil {
		return fmt.Sprintf("ignoring %s: using %s %s; remove the old one when you no longer need it",
			d.old, d.new, renamedNote)
	}
	switch {
	case oldInfo.Mode()&os.ModeSymlink != 0:
		return fmt.Sprintf("%s is a symlink, not moved: point it to %s yourself %s", d.old, d.new, renamedNote)
	case !oldInfo.IsDir():
		return fmt.Sprintf("%s is not a directory, not moved %s", d.old, renamedNote)
	}
	if err := os.Rename(d.old, d.new); err != nil {
		return fmt.Sprintf("could not move your %s from %s to %s %s: %v", d.what, d.old, d.new, renamedNote, err)
	}
	return fmt.Sprintf("moved your %s from %s to %s %s", d.what, d.old, d.new, renamedNote)
}

// leftoverLegacyDirs returns old directories that still exist, for doctor.
func leftoverLegacyDirs(dirs []legacyDir) []legacyDir {
	var left []legacyDir
	for _, d := range dirs {
		if _, err := os.Lstat(d.old); err == nil {
			left = append(left, d)
		}
	}
	return left
}

// renameNote is one doctor warning about the old name or the old token.
type renameNote struct{ text, hint string }

// renameNotes lists deprecated GITISSUE_… variables in use, old
// directories still left over and token variables no longer read, for
// doctor.
func renameNotes() []renameNote {
	var notes []renameNote
	for _, w := range config.RemovedEnv() {
		notes = append(notes, renameNote{w, "unset it where you set it (shell profile, scripts); log glab in instead: glab auth login --hostname <host>"})
	}
	for _, w := range config.DeprecatedEnv() {
		notes = append(notes, renameNote{w, "rename it where you set it (shell profile, scripts); the old names stop working in a later release"})
	}
	for _, d := range leftoverLegacyDirs(legacyDirs(false)) {
		notes = append(notes, renameNote{
			"old " + d.what + " directory " + d.old + " is still there " + renamedNote,
			"gl-issues uses " + d.new + "; move anything you still need there, then remove " + d.old,
		})
	}
	return notes
}

// Older versions kept the token in the config file and passed it to glab
// as GITLAB_TOKEN. Now glab's own login is used (kept in the OS keyring),
// and `gl-issues migrate-token` moves a token left in the file there.
// Keep this as long as old config files may be around.

// migrateToken hands the token in the config file to glab's login for the
// configured host, token on glab's stdin only, and once glab has it,
// rewrites the file without the token line (atomically, same mode). If
// glab fails, the file is left unchanged. The token is never printed.
// Host and token come from one snapshot of the file: path's symlinks are
// resolved once, and that one file is read, loaded as the config, read
// again (it must not change in between), checked and replaced. It is
// rewritten only if it still holds exactly that snapshot right before the
// rename, so an edit made during the login is kept. Two migrate-token
// runs never overlap (lockDir); an editor does not take that lock, so one
// saving in the instant between the last check and the rename would still
// lose its edit: edit the config with gl-issues closed. path is the
// config file to migrate.
func migrateToken(path string, flags map[string]string, out io.Writer) error {
	file, err := filepath.EvalSymlinks(path) // every read and the rewrite use this file, not the symlink
	if os.IsNotExist(err) {
		return fmt.Errorf("no config file at %s: nothing to migrate", path)
	}
	if err != nil {
		return err
	}
	unlock, err := lockDir(filepath.Dir(file))
	if errors.Is(err, errLocked) {
		return fmt.Errorf("another gl-issues migrate-token is working on %s: nothing done, run it again when that one is done", path)
	}
	if err != nil {
		return fmt.Errorf("could not lock %s: %w; nothing done", filepath.Dir(path), err)
	}
	defer unlock()
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if _, err := loadConfig(file, flags); err != nil { // the permission check and any config error
		return err
	}
	// The host comes from the same bytes as the token, never from a
	// second read of the file.
	cfg, err := config.LoadData(file, data)
	if err == nil {
		cfg, err = applyFlags(cfg, flags)
	}
	if err != nil {
		return err
	}
	cfg.Path = path // for the messages: the name the user knows
	if !sameContent(file, data) {
		return fmt.Errorf("%s changed while gl-issues read it; nothing done, run gl-issues migrate-token again", path)
	}
	if cfg.Host == "" {
		return missingSetting(cfg, path, "host", "-host")
	}
	rest, token, found, err := config.StripToken(data)
	if err != nil {
		return fmt.Errorf("%s: %w\nfix or delete the token line; the file is unchanged", cfg.Path, err)
	}
	if !found {
		fmt.Fprintf(out, "no token in %s: nothing to migrate (gl-issues uses glab's login)\n", cfg.Path)
		return nil
	}
	if token = strings.TrimSpace(token); token != "" {
		ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
		err := gl.Client{Host: cfg.Host}.Login(ctx, token)
		cancel()
		if err != nil {
			return fmt.Errorf("%w\n%s is unchanged", err, cfg.Path)
		}
		fmt.Fprintf(out, "glab now has the token for %s (check where it keeps it: glab auth status --hostname %s)\n", cfg.Host, cfg.Host)
	}
	if !sameContent(file, data) { // edited during the login: keep that edit
		has := ""
		if token != "" {
			has = "glab has the token now, but "
		}
		return fmt.Errorf("%s%s changed meanwhile, so gl-issues did not rewrite it: delete its token line yourself, "+
			"or run gl-issues migrate-token again", has, cfg.Path)
	}
	// An editor saving in the instant between this check and the rename
	// would still lose its edit: editors do not take lockDir's lock.
	if err := replaceFile(file, rest); err != nil {
		return fmt.Errorf("could not remove the token line from %s: %w; delete that line yourself", cfg.Path, err)
	}
	fmt.Fprintf(out, "removed the token line from %s\n", cfg.Path)
	return nil
}

// errLocked: another gl-issues holds lockDir's lock.
var errLocked = errors.New("locked by another gl-issues")

// sameContent reports whether name still holds exactly data.
func sameContent(name string, data []byte) bool {
	now, err := os.ReadFile(name)
	return err == nil && bytes.Equal(now, data)
}

// replaceFile replaces name with data atomically: a temp file in the same
// directory, private from the start, given name's permissions (at most
// 0600) and renamed over it. On error name is unchanged.
func replaceFile(name string, data []byte) error {
	fi, err := os.Stat(name)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	err = errors.Join(werr, f.Chmod(fi.Mode().Perm()&0o600), f.Sync(), f.Close())
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}
