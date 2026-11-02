package tui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
)

// Editing text in $EDITOR and opening issue URLs in a browser: the only
// places where the dashboard runs programs other than glab. Neither gets
// the token variables of the environment (gl.WithoutTokens).

// runEditor suspends the TUI, edits text in $VISUAL/$EDITOR and passes
// the result and the temp file's name to done. With keep set the file is
// left for the caller to remove once the text is safe; otherwise it is
// removed right away.
func (m Model) runEditor(text string, keep bool, done func(text, file string, err error) tea.Msg) tea.Cmd {
	name, err := writeTemp(text)
	if err != nil {
		return func() tea.Msg { return done("", "", fmt.Errorf("temp file: %w", err)) }
	}
	return tea.ExecProcess(editorCmd(m.opts.Editor, name), func(err error) tea.Msg {
		return editorDone(name, text, keep, err, done)
	})
}

// editorCmd is the command running the editor (editorArgs) on file.
func editorCmd(override, file string) *exec.Cmd {
	parts := editorArgs(override)
	cmd := exec.Command(parts[0], append(parts[1:], file)...)
	cmd.Env = gl.WithoutTokens(os.Environ())
	return cmd
}

// editorDone passes the edited text to done once the editor has exited
// with err. A failed editor's file is kept, and its name added to the
// error, if it holds a draft that differs from text; so is a file that
// cannot be read back when keep is set.
func editorDone(name, text string, keep bool, err error, done func(text, file string, err error) tea.Msg) tea.Msg {
	if err != nil {
		if b, rerr := os.ReadFile(name); rerr == nil && string(b) != text {
			return done("", "", fmt.Errorf("%w (draft kept in %s)", err, name))
		}
		os.Remove(name)
		return done("", "", err)
	}
	b, err := os.ReadFile(name)
	if err != nil && keep {
		return done("", "", fmt.Errorf("%w (draft kept in %s)", err, name))
	}
	if !keep {
		os.Remove(name)
		name = ""
	}
	return done(strings.TrimRight(string(b), "\n"), name, err)
}

// writeTemp writes text to a new private temp file in editDir and returns
// its name. On any error the file is removed.
func writeTemp(text string) (string, error) {
	dir, err := editDir()
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "edit-*.md")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(text)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// editDir returns $XDG_CACHE_HOME/gl-issues (when that is an absolute
// path, else ~/.cache/gl-issues), created if needed and made 0700, for the
// texts being edited: not the shared system temp directory, where other
// users could see that a file exists or race for its name. A symlink
// there is refused.
func editDir() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", errors.New("no cache directory: HOME is not set")
		}
		base = filepath.Join(home, ".cache")
	}
	dir := filepath.Join(base, "gl-issues")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (m Model) finishEditor(msg editorMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setFlash("editor: "+msg.err.Error(), true)
		return m, nil
	}
	if msg.text == strings.TrimRight(msg.origRaw, "\n") { // what the editor was given
		os.Remove(msg.file)
		m.setFlash(msg.kind+" unchanged", false)
		return m, nil
	}
	// Save by project and number even if the issue has left the list
	// while the editor was open.
	is := msg.issue
	if m.busy[is.ID] != "" {
		m.setFlash("description not saved, your text is in "+msg.file+": another action on this issue is still running", true)
		return m, nil
	}
	m.busy[is.ID] = "description"
	return m, saveDescription(m.client, is, msg.origRaw, msg.text, msg.file)
}

// saveDescription PUTs an edited description unless it changed on GitLab
// since the editor opened (then the other edit would be lost). origRaw is
// the unsanitized description the edit started from; it is compared with
// the unsanitized server copy, so no change can hide behind sanitizing.
// The temp file is removed only once the text is saved; otherwise the
// message tells the user where it is.
func saveDescription(c gl.Client, is gl.Issue, origRaw, text, file string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		fail := func(why string) tea.Msg {
			if file == "" {
				return updatedMsg{id: is.ID, what: "description", err: errors.New(why)}
			}
			return updatedMsg{id: is.ID, what: "description", err: errors.New("not saved, your text is in " + file + ": " + why)}
		}
		cur, err := c.GetIssue(ctx, is.Project(), is.IID)
		if err != nil {
			return fail("could not check for other changes: " + err.Error())
		}
		if cur.RawDescription != origRaw {
			return fail("the description was changed on GitLab while you were editing; reload, then edit again")
		}
		up, err := c.Update(ctx, is.Project(), is.IID, map[string]any{"description": text})
		if err != nil {
			return fail(err.Error())
		}
		if file != "" {
			os.Remove(file)
		}
		return updatedMsg{id: is.ID, what: "description", issue: up}
	}
}

// editorArgs returns the editor command split into program and
// arguments: override (the editor setting), else $VISUAL, $EDITOR, or the
// first of vi, nano found in PATH (minimal Linux installs may lack vi).
// Never empty.
func editorArgs(override string) []string {
	if parts := splitCommand(editorCommand(override)); len(parts) > 0 {
		return parts
	}
	return []string{"vi"}
}

func editorCommand(override string) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v
		}
	}
	for _, name := range []string{"vi", "nano"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	return "vi"
}

// splitCommand splits a command line into words like a simple shell:
// whitespace separates words, single quotes keep text literally, double
// quotes keep spaces, and a backslash outside single quotes escapes the
// next character. So "'/Applications/My Editor' -w" is two words. An
// unterminated quote runs to the end.
func splitCommand(s string) []string {
	var (
		words  []string
		cur    strings.Builder
		inWord bool
		quote  rune
		esc    bool
	)
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, inWord = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// browserCommands lists URL openers to try, in order: override (the
// browser setting), else `open` on macOS, else $BROWSER, xdg-open
// (desktop Linux), wslview (WSL) and sensible-browser.
func browserCommands(override string) []string {
	if v := strings.TrimSpace(override); v != "" {
		return []string{v}
	}
	if runtime.GOOS == "darwin" {
		return []string{"open"}
	}
	return []string{os.Getenv("BROWSER"), "xdg-open", "wslview", "sensible-browser"}
}

type browserMsg struct {
	url string
	err error
}

// checkWebURL accepts only http(s) URLs on the configured GitLab host,
// so a crafted web_url can never make the opener run something else or
// read as an option.
func checkWebURL(raw, host string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil || strings.HasPrefix(raw, "-"):
		return errors.New("not opening an invalid URL")
	case u.Scheme != "https" && u.Scheme != "http":
		return errors.New("not opening a non-http(s) URL")
	case host == "" || u.User != nil || !gl.SameHost(u, host):
		return errors.New("not opening a URL outside " + host)
	}
	return nil
}

// openerWait is how long openBrowser waits for an opener to fail. One
// still running by then (a browser started directly) has opened the URL.
var openerWait = 2 * time.Second

// openBrowser runs the first opener found; one that cannot start or exits
// with an error (e.g. xdg-open without a desktop) is skipped for the next,
// and the error comes back with the URL when none works. The command runs
// in the background, so waiting here never blocks the dashboard.
func openBrowser(u, host, override string) tea.Cmd {
	return func() tea.Msg {
		if err := checkWebURL(u, host); err != nil {
			return browserMsg{url: u, err: err}
		}
		var last error
		for _, name := range browserCommands(override) {
			parts := splitCommand(name) // may carry flags, e.g. "firefox --new-window"
			if len(parts) == 0 {
				continue
			}
			path, err := exec.LookPath(parts[0])
			if err != nil {
				continue
			}
			cmd := exec.Command(path, append(parts[1:], u)...)
			cmd.Env = gl.WithoutTokens(os.Environ())
			if err := cmd.Start(); err != nil {
				last = err // names the program already
				continue
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }() // reaps the child even after the timeout
			select {
			case err := <-done:
				if err != nil {
					last = fmt.Errorf("%s: %w", parts[0], err)
					continue
				}
			case <-time.After(openerWait):
			}
			return browserMsg{url: u}
		}
		if last == nil {
			last = errors.New("no browser opener found")
		}
		return browserMsg{url: u, err: last}
	}
}
