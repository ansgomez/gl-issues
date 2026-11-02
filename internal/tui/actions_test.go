package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
)

// fakeGlab puts a shell script named glab first in PATH. It never
// touches the network. Every call appends its arguments to the returned
// log file.
func fakeGlab(t *testing.T, body string) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + log + "'\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "glab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// calls returns the logged glab calls ("" if there were none).
func calls(t *testing.T, log string) string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func testIssue(id int, ref, desc string) gl.Issue {
	is := gl.Issue{ID: id, IID: id, Title: "t", State: "opened", Description: desc, RawDescription: desc}
	is.References.Full = ref
	return is
}

// A posted comment is a success even if reloading the issue fails.
func TestCommentSucceedsWhenRefetchFails(t *testing.T) {
	fakeGlab(t, `case "$*" in *POST*) echo '{}';; *) echo 'HTTP 500' >&2; exit 1;; esac`)
	m := sampleModel(100, 30)
	is := *m.current()
	msg := m.comment(is, "hello")().(updatedMsg)
	if msg.err != nil {
		t.Fatalf("comment reported as failed: %v", msg.err)
	}
	nm, _ := m.Update(msg)
	m = nm.(Model)
	if strings.Contains(m.flash, "failed") || !strings.Contains(m.flash, "comment ✓") {
		t.Errorf("flash = %q, want a success note", m.flash)
	}
	if len(m.issues) != 3 {
		t.Errorf("no issue may be added: %d", len(m.issues))
	}
}

// A comment on an issue with another action still running is refused
// in the pop-up (the text kept): the two replies would otherwise clear
// each other's busy mark.
func TestCommentRefusedWhileIssueBusy(t *testing.T) {
	log := fakeGlab(t, "echo '{}'")
	m := sampleModel(100, 30)
	id := m.current().ID
	m.busy[id] = "assign"
	m = press(m, "c")
	m.form.fields[0].in.SetValue("hello")
	nm, cmd := m.handleFormKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = nm.(Model)
	if cmd != nil || m.form == nil || m.form.sending || !strings.Contains(m.form.err, "another action on this issue is still running") {
		t.Fatalf("cmd %v, form %+v", cmd, m.form)
	}
	if m.busy[id] != "assign" || m.form.fields[0].in.Value() != "hello" || calls(t, log) != "" {
		t.Errorf("busy %v, text %q, calls %q", m.busy, m.form.fields[0].in.Value(), calls(t, log))
	}
}

// A comment or description is sent with its leading indentation (Markdown:
// a code block, a nested list); only the end is trimmed.
func TestCommentKeepsIndentation(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin")
	fakeGlab(t, "cat >> '"+stdin+"'; echo '{}'")
	m := sampleModel(100, 30)
	m = press(m, "c")
	m.form.fields[0].in.SetValue("    code\n\n")
	nm, cmd := m.handleFormKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m = nm.(Model); cmd == nil || m.form == nil || !m.form.sending {
		t.Fatalf("not sent: form %+v", m.form)
	}
	if msg := cmd().(updatedMsg); msg.err != nil {
		t.Fatal(msg.err)
	}
	if b, _ := os.ReadFile(stdin); !strings.Contains(string(b), `"body":"    code"`) {
		t.Errorf("sent %s", b)
	}
	m = press(sampleModel(100, 30), "c")
	m.form.fields[0].in.SetValue("  \n\t\n")
	if nm, _ := m.handleFormKey(tea.KeyMsg{Type: tea.KeyCtrlS}); nm.(Model).form.err != "the comment is empty" {
		t.Errorf("blank comment: %q", nm.(Model).form.err)
	}
}

// An edited description is saved even if the issue left the list.
func TestDescriptionSavedAfterIssueLeftList(t *testing.T) {
	m := sampleModel(100, 30)
	gone := testIssue(42, "team/proj#42", "old")
	nm, cmd := m.Update(editorMsg{issue: gone, kind: "description", text: "new", origRaw: "old"})
	if cmd == nil || nm.(Model).busy[42] != "description" {
		t.Fatal("edit of an issue no longer in the list must still be saved")
	}
}

// The edited text is only PUT when the server copy is unchanged,
// and the temp file is kept whenever it was not saved.
func TestSaveDescription(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	get := write("get.json", `{"id":1,"iid":1,"description":"changed by someone else","references":{"full":"team/proj#1"}}`)
	put := write("put.json", `{"id":1,"iid":1,"description":"mine","references":{"full":"team/proj#1"}}`)
	body := `case "$*" in *"--method GET"*) cat '` + get + `';; *"--method PUT"*) cat '` + put + `';; esac`
	is := testIssue(1, "team/proj#1", "original")

	log := fakeGlab(t, body)
	file := write("edit.md", "mine")
	msg := saveDescription(gl.Client{Host: "h"}, is, "original", "mine", file)().(updatedMsg)
	if msg.err == nil || !strings.Contains(msg.err.Error(), "changed on GitLab") || !strings.Contains(msg.err.Error(), file) {
		t.Errorf("conflict should be reported with the file name, got %v", msg.err)
	}
	if strings.Contains(calls(t, log), "PUT") {
		t.Error("must not overwrite a description changed on the server")
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("temp file must be kept when not saved")
	}

	log = fakeGlab(t, `case "$*" in *"--method GET"*) exit 1;; esac`)
	msg = saveDescription(gl.Client{Host: "h"}, is, "original", "mine", file)().(updatedMsg)
	if msg.err == nil || strings.Contains(calls(t, log), "PUT") {
		t.Error("a failed check must not save")
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("temp file must be kept when the save fails")
	}

	write("get.json", `{"id":1,"iid":1,"description":"original","references":{"full":"team/proj#1"}}`)
	log = fakeGlab(t, body)
	msg = saveDescription(gl.Client{Host: "h"}, is, "original", "mine", file)().(updatedMsg)
	if msg.err != nil || !strings.Contains(calls(t, log), "PUT") {
		t.Fatalf("unchanged server copy should be saved: %v", msg.err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("temp file should be removed once saved")
	}
}

// A temp file that cannot be written aborts before the editor starts.
func TestRunEditorTempFileError(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notDir, nil, 0o600)
	t.Setenv("XDG_CACHE_HOME", notDir)
	m := sampleModel(100, 30)
	msg := m.runEditor("x", true, func(text, file string, err error) tea.Msg {
		return editorMsg{text: text, file: file, err: err}
	})().(editorMsg)
	if msg.err == nil {
		t.Error("want an error when the temp file cannot be created")
	}
}

// A failed editor keeps a changed draft and names it in the error; an
// unchanged file is removed.
func TestEditorFailureKeepsDraft(t *testing.T) {
	done := func(text, file string, err error) tea.Msg { return editorMsg{text: text, file: file, err: err} }
	fail := errors.New("exit status 1")
	name := filepath.Join(t.TempDir(), "draft.md")
	os.WriteFile(name, []byte("new text"), 0o600)
	msg := editorDone(name, "old", false, fail, done).(editorMsg)
	if !errors.Is(msg.err, fail) || !strings.Contains(msg.err.Error(), name) {
		t.Errorf("error %v should name the kept draft", msg.err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Error("changed draft should be kept")
	}
	os.WriteFile(name, []byte("old"), 0o600)
	msg = editorDone(name, "old", false, fail, done).(editorMsg)
	if !errors.Is(msg.err, fail) || strings.Contains(msg.err.Error(), name) {
		t.Errorf("unchanged file: error %v", msg.err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Error("unchanged file should be removed")
	}
}

// A file the editor left but that cannot be read back is kept, and named
// in the error, when the caller asked to keep it; otherwise it is removed.
func TestEditorUnreadableFileKept(t *testing.T) {
	done := func(text, file string, err error) tea.Msg { return editorMsg{text: text, file: file, err: err} }
	for _, keep := range []bool{true, false} {
		name := filepath.Join(t.TempDir(), "draft.md") // a directory: cannot be read as a file
		os.Mkdir(name, 0o700)
		msg := editorDone(name, "old", keep, nil, done).(editorMsg)
		if msg.err == nil || msg.file != "" || strings.Contains(msg.err.Error(), "draft kept") != keep {
			t.Errorf("keep %v: file %q, error %v", keep, msg.file, msg.err)
		}
		if _, err := os.Stat(name); (err == nil) != keep {
			t.Errorf("keep %v: stat %v", keep, err)
		}
	}
}

func TestEditorArgs(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nano")
	for set, want := range map[string][]string{
		"   ":                            {"nano"},
		` "/Applications/My Editor" -w `: {"/Applications/My Editor", "-w"},
		`code --wait`:                    {"code", "--wait"},
		`/opt/my\ ed 'a b' "c\"d"`:       {"/opt/my ed", "a b", `c"d`},
		`"unterminated`:                  {"unterminated"},
	} {
		if got := editorArgs(set); !reflect.DeepEqual(got, want) {
			t.Errorf("editor %q: args %q, want %q", set, got, want)
		}
	}
	for in, want := range map[string][]string{
		``:                      nil,
		" \t\n ":                nil,
		`''`:                    {""},
		`"" x`:                  {"", "x"},
		`"a b"c d`:              {"a bc", "d"},
		`'it'"'"'s'`:            {"it's"},
		`'a\"b' "a'b"`:          {`a\"b`, "a'b"},
		`a\ b\'c`:               {"a b'c"},
		"firefox\t--new-window": {"firefox", "--new-window"},
		`'/My Apps/ed' -w`:      {"/My Apps/ed", "-w"},
		`"unterminated x`:       {"unterminated x"},
		`trailing\`:             {"trailing"},
	} {
		if got := splitCommand(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckWebURL(t *testing.T) {
	const host = "gitlab.example.edu"
	for u, ok := range map[string]bool{
		"https://gitlab.example.edu/acme/x/-/issues/1": true,
		"http://GitLab.Example.edu/acme/x/-/issues/1":  true,
		"https://gitlab.example.edu:8443/acme/x":       true,
		"https://evil.example/acme/x":                  false,
		"https://gitlab.example.edu.evil.example/x":    false,
		"https://user@gitlab.example.edu/x":            false,
		"https://user:pw@gitlab.example.edu/x":         false,
		"https://gitlab.example.edu@evil.example/x":    false,
		"ssh://gitlab.example.edu/acme/x":              false,
		"mailto:alice@gitlab.example.edu":              false,
		"//gitlab.example.edu/acme/x":                  false,
		"gitlab.example.edu/acme/x":                    false,
		"-https://gitlab.example.edu/x":                false,
		"file:///etc/passwd":                           false,
		"javascript:alert(1)":                          false,
		"--help":                                       false,
		"-a Calculator":                                false,
		"":                                             false,
	} {
		if err := checkWebURL(u, host); (err == nil) != ok {
			t.Errorf("checkWebURL(%q) = %v, want ok=%v", u, err, ok)
		}
	}
	if err := checkWebURL("https://gitlab.example.edu/x", ""); err == nil {
		t.Error("no configured host: refuse")
	}
	msg := openBrowser("https://evil.example/x", host, "")().(browserMsg)
	if msg.err == nil {
		t.Error("openBrowser must refuse a foreign host")
	}
}

// x on a mixed selection only closes the open issues and says so.
func TestCloseMixedSelection(t *testing.T) {
	m := sampleModel(100, 30)
	m.issues[m.view[1]].State = "closed"
	m.issues[m.view[0]].Title = strings.Repeat("a long title ", 20) // wider than the pop-up
	m.rebuildView()
	m = press(m, " ", " ", "x") // first two in the list selected
	if m.form == nil {
		t.Fatal("x should ask first")
	}
	if !strings.Contains(m.form.sub, "Close this issue?") || !strings.Contains(m.form.sub, "1 selected issue is already closed") {
		t.Errorf("confirm text: %q", m.form.sub)
	}
	if v := m.View(); !strings.Contains(v, "Close this issue?") || !strings.Contains(v, "1 selected issue is already closed") {
		t.Errorf("a long title must not hide the question:\n%s", v)
	}
	m = press(m, "y")
	if len(m.busy) != 1 {
		t.Errorf("only the open issue should be closed, busy = %v", m.busy)
	}

	all := sampleModel(100, 30)
	for _, i := range all.view[:2] {
		all.issues[i].State = "closed"
	}
	all = press(all, " ", " ", "x")
	if !strings.Contains(all.form.sub, "Reopen these 2 issues?") {
		t.Errorf("all closed: %q", all.form.sub)
	}
}

// A reload that lands while a pop-up is open must not change what the
// pop-up acts on: the comment goes to the issue it was opened for.
func TestReloadDuringCommentFormKeepsTarget(t *testing.T) {
	log := fakeGlab(t, `case "$*" in *POST*) echo '{}';; *) echo '{"id":1,"iid":1,"references":{"full":"team/proj#1"}}';; esac`)
	m := sampleModel(100, 30)
	want := m.current()
	if want.Ref() != "team/proj#1" {
		t.Fatalf("setup: cursor on %s", want.Ref())
	}
	m = press(m, "c", "hello")

	// The list changes under the open pop-up: other issues, other order.
	other := testIssue(7, "team/other#7", "")
	nm, _ := m.Update(issuesMsg{state: "opened", issues: []gl.Issue{other}})
	m = nm.(Model)
	if m.pendingLoad == nil || len(m.issues) != 3 {
		t.Fatalf("the list must not be swapped while a pop-up is open (pending %v, %d issues)", m.pendingLoad != nil, len(m.issues))
	}

	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = nm.(Model)
	if m.form == nil || !m.form.sending || cmd == nil {
		t.Fatal("ctrl+s should send the comment, keeping the pop-up until it is sent")
	}
	msg := cmd().(updatedMsg)
	if msg.err != nil || msg.id != 1 {
		t.Fatalf("comment result: %+v", msg)
	}
	nm, _ = m.Update(msg)
	m = nm.(Model)
	if m.form != nil {
		t.Fatal("the pop-up should close once the comment is sent")
	}
	if len(m.issues) != 1 || m.issues[0].ID != 7 || m.pendingLoad != nil {
		t.Errorf("held-back list should be installed once the pop-up closed: %v", m.issues)
	}
	if got := calls(t, log); !strings.Contains(got, "projects/team%2Fproj/issues/1/notes") || strings.Contains(got, "other") {
		t.Errorf("comment went to the wrong issue:\n%s", got)
	}
}

// A list that shrinks under an open bulk pop-up must never make the
// action panic or hit other issues.
func TestShrinkingListUnderBulkPopup(t *testing.T) {
	for name, keys := range map[string][]string{
		"due":    {"d", "fri", "enter"},
		"close":  {"x", "y"},
		"assign": {"a", "ali", "enter", "enter"},
		"me":     {"i", "y"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panicked: %v", name, r)
				}
			}()
			m := press(peopleModel(), "v") // all three selected
			m = press(m, keys[0])
			if m.form == nil {
				t.Fatalf("%s: no pop-up", name)
			}
			nm, _ := m.Update(issuesMsg{state: "opened", background: true,
				issues: []gl.Issue{testIssue(9, "team/x#9", "")}})
			m = press(nm.(Model), keys[1:]...)
			if m.form != nil {
				t.Fatalf("%s: pop-up still open: %q", name, m.form.err)
			}
			for _, id := range []int{1, 2, 3} {
				if m.busy[id] == "" {
					t.Errorf("%s: issue %d not acted on (busy %v)", name, id, m.busy)
				}
			}
			if m.busy[9] != "" {
				t.Errorf("%s: acted on an issue that was not selected", name)
			}
		}()
	}
}

// Bulk actions say when they leave out issues that are still busy.
func TestBulkActionReportsBusySkips(t *testing.T) {
	m := press(peopleModel(), " ", " ") // issues 1 and 2
	first := m.issues[m.view[0]].ID
	m.busy[first] = "comment"
	m = press(m, "d", "fri", "enter")
	if !strings.Contains(m.flash, "skipped 1 issue") || !m.flashBad {
		t.Errorf("flash = %q", m.flash)
	}
	if m.busy[first] != "comment" {
		t.Error("the running action must not be replaced")
	}
}

// Assigning yourself to several selected issues asks first.
func TestBulkAssignMeConfirms(t *testing.T) {
	m := press(peopleModel(), " ", " ", "i")
	if m.form == nil || m.form.kind != formConfirm || len(m.busy) != 0 {
		t.Fatalf("bulk i must ask first (busy %v)", m.busy)
	}
	if !strings.Contains(m.form.sub, "these 2 issues") {
		t.Errorf("question: %q", m.form.sub)
	}
	if m = press(m, "y"); len(m.busy) != 2 {
		t.Errorf("y should assign both, busy = %v", m.busy)
	}
	if one := press(peopleModel(), "i"); one.form != nil || len(one.busy) != 1 {
		t.Error("i on the issue under the cursor needs no confirmation")
	}
}

// The editor starts from the description exactly as GitLab has it, so
// saving never drops characters the terminal display hides; and that raw
// text, saved unchanged, is not sent back.
func TestEditorGetsRawDescription(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	raw := "Zero\u200dwidth joiner, soft\u00adhyphen\r\nCRLF"
	is := testIssue(7, "acme/platform#7", gl.Sanitize(raw))
	is.RawDescription = raw
	m := sampleModel(100, 30)
	m.issues = []gl.Issue{is}
	m.rebuildView()
	m.opts.Editor = "true"
	m = press(m, "e")
	files, _ := filepath.Glob(filepath.Join(tmp, "gl-issues", "edit-*.md"))
	if len(files) != 1 {
		t.Fatalf("want one temp file, got %v", files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != raw {
		t.Errorf("editor got %q, want the raw description %q", b, raw)
	}

	nm, cmd := m.Update(editorMsg{issue: is, kind: "description", text: raw, origRaw: raw, file: files[0]})
	if cmd != nil || nm.(Model).busy[7] != "" || !strings.Contains(nm.(Model).flash, "unchanged") {
		t.Errorf("raw text saved unchanged must not be sent; flash %q", nm.(Model).flash)
	}
}

// A description with terminal control characters never reaches the
// editor, which would show them to the terminal as they are.
func TestEditorRefusesControlCharacters(t *testing.T) {
	for _, raw := range []string{"bad \x1b]52;c;aGk=\x07 text", "bidi \u202e txet"} {
		testEditorRefuses(t, raw)
	}
}

func testEditorRefuses(t *testing.T, raw string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmp)
	is := testIssue(7, "acme/platform#7", gl.Sanitize(raw))
	is.RawDescription = raw
	m := sampleModel(100, 30)
	m.issues = []gl.Issue{is}
	m.rebuildView()
	m.opts.Editor = "true"
	m = press(m, "e")
	if files, _ := filepath.Glob(filepath.Join(tmp, "gl-issues", "edit-*.md")); len(files) != 0 {
		t.Errorf("editor started on %v", files)
	}
	if !strings.Contains(m.flash, "control or bidi characters") {
		t.Errorf("flash %q", m.flash)
	}
}

// A comment or new issue whose request fails keeps its text: the pop-up
// shows the error, and comes back if it was hidden while sending.
func TestFailedSendKeepsText(t *testing.T) {
	fakeGlab(t, `echo 'HTTP 500 Internal Server Error' >&2; exit 1`)
	m := press(sampleModel(100, 30), "c", "hello")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = nm.(Model)
	if m.form == nil || !m.form.sending {
		t.Fatal("the pop-up should stay while sending")
	}
	if m = press(m, "x"); m.form.fields[0].in.Value() != "hello" {
		t.Error("keys must be ignored while sending")
	}
	nm, _ = m.Update(cmd())
	m = nm.(Model)
	if m.form == nil || m.form.sending || m.form.fields[0].in.Value() != "hello" ||
		!strings.Contains(m.form.err, "comment failed") || !strings.Contains(m.form.err, "HTTP 500") {
		t.Fatalf("failed comment must keep the text and show why; form = %+v", m.form)
	}
	if len(m.busy) != 0 {
		t.Errorf("busy not cleared: %v", m.busy)
	}

	// Hidden with esc while sending: the failure brings it back.
	nm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = press(nm.(Model), "esc")
	if m.form != nil {
		t.Fatal("esc should hide the pop-up while sending")
	}
	nm, _ = m.Update(cmd())
	m = nm.(Model)
	if m.form == nil || m.form.fields[0].in.Value() != "hello" || m.form.err == "" {
		t.Fatalf("a hidden pop-up must come back with its text on failure; form = %+v", m.form)
	}

	// New issue: the same.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m = sampleModel(100, 30)
	nm, _ = press(m, "n").Update(projectsMsg{projects: []gl.Project{{ID: 1, Name: "proj", Path: "team/proj"}}})
	m = press(nm.(Model), "enter", "Broken", "tab", "tab", "tab", "some text")
	nm, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	nm, _ = nm.Update(cmd())
	m = nm.(Model)
	if m.form == nil || m.form.kind != formNewIssue || m.form.fields[0].in.Value() != "Broken" ||
		m.form.fields[3].in.Value() != "some text" || !strings.Contains(m.form.err, "create failed") {
		t.Fatalf("failed create must keep the form; form = %+v", m.form)
	}
}

// Quitting while writes run asks first; y quits, enter and n stay.
func TestQuitAsksWhileBusy(t *testing.T) {
	isQuit := func(cmd tea.Cmd) bool {
		if cmd == nil {
			return false
		}
		_, ok := cmd().(tea.QuitMsg)
		return ok
	}
	m := sampleModel(100, 30)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); !isQuit(cmd) {
		t.Fatal("q with nothing running should quit at once")
	}
	m.busy[1], m.busy[2] = "assign", "due date"
	for _, k := range []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("q")}, {Type: tea.KeyCtrlC}} {
		nm, cmd := m.Update(k)
		got := nm.(Model)
		if isQuit(cmd) || got.form == nil || !strings.Contains(got.form.sub, "2 actions still running — quit anyway? y/N") {
			t.Fatalf("%s while busy should ask; form = %+v", k, got.form)
		}
		if a := press(got, "enter"); a.form != nil {
			t.Error("enter should answer no")
		}
		_, cmd = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
		if !isQuit(cmd) {
			t.Error("y should quit")
		}
	}
	// In details too.
	if d := press(m, "enter"); d.mode == modeDetail {
		if nm, cmd := d.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); isQuit(cmd) || nm.(Model).form == nil {
			t.Error("ctrl+c in details while busy should ask")
		}
	}
}

// The project of a created issue is remembered by the create command
// (the state file is never touched in Update), and Update takes the list
// from the reply. If the file cannot be written the issue is still added,
// with a warning.
func TestCreateRemembersProjectInCommand(t *testing.T) {
	fakeGlab(t, `echo '{"id":99,"iid":5,"state":"opened","title":"T","references":{"full":"team/sub/other#5"}}'`)
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	m := sampleModel(100, 30)
	msg := m.createIssue("team/sub/other", "T", "", "", nil)().(updatedMsg)
	if msg.err != nil || msg.recentErr != nil || len(msg.recent) != 1 || msg.recent[0] != "team/sub/other" {
		t.Fatalf("reply %+v", msg)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "gl-issues", "state.json")); err != nil {
		t.Errorf("the command should have written the state file: %v", err)
	}
	nm, _ := m.Update(msg)
	m = nm.(Model)
	if !reflect.DeepEqual(m.recent, msg.recent) || m.flashBad || !strings.Contains(m.flash, "create ✓") {
		t.Errorf("recent %v, flash %q", m.recent, m.flash)
	}

	t.Setenv("XDG_STATE_HOME", filepath.Join(stateDir, "gl-issues", "state.json")) // a file: no directory can be made
	msg = m.createIssue("team/sub/other", "T", "", "", nil)().(updatedMsg)
	if msg.err != nil || msg.recentErr == nil {
		t.Fatalf("reply %+v", msg)
	}
	nm, _ = m.Update(msg)
	m = nm.(Model)
	if !strings.Contains(m.flash, "could not remember the project") || !m.flashBad {
		t.Errorf("flash %q", m.flash)
	}
	if is := m.current(); is == nil || is.ID != 99 {
		t.Error("the issue must still be added to the list")
	}
}

// d followed by enter on the date as it opened (or an empty field on an
// issue without one) writes nothing; a changed date does, and a bulk d
// leaves out the issues that have the date already.
func TestDueDateUnchangedNotWritten(t *testing.T) {
	enter := tea.KeyMsg{Type: tea.KeyEnter}
	m := press(sampleModel(100, 30), "d") // #1, due 2020-01-01, in the field
	nm, cmd := m.Update(enter)
	m = nm.(Model)
	if cmd != nil || len(m.busy) != 0 || m.form != nil || m.flash != "due date unchanged" {
		t.Errorf("unchanged date written: busy %v, flash %q", m.busy, m.flash)
	}
	nm, cmd = press(m, "j", "j", "d").Update(enter) // #3 has no due date; the field is empty
	if m := nm.(Model); cmd != nil || len(m.busy) != 0 || m.flash != "due date unchanged" {
		t.Errorf("empty date written to an issue without one: busy %v, flash %q", m.busy, m.flash)
	}
	m = press(m, "d")
	m.form.fields[0].in.SetValue("none")
	nm, cmd = m.Update(enter)
	if m := nm.(Model); cmd == nil || m.busy[1] != "due date" {
		t.Errorf("clearing the date not written: busy %v", m.busy)
	}

	m = press(sampleModel(100, 30), " ", " ", "d") // #1 and #2 selected
	m.form.fields[0].in.SetValue("2020-01-01")
	nm, cmd = m.Update(enter)
	m = nm.(Model)
	if cmd == nil || m.busy[1] != "" || m.busy[2] != "due date" || m.flash != "due date: 1 issue had it already" {
		t.Errorf("bulk d: busy %v, flash %q", m.busy, m.flash)
	}
}

// A request that fails while another pop-up is open does not lose its
// text: the failed pop-up comes back, error shown, when that one closes.
func TestFailedSendBehindAnotherPopup(t *testing.T) {
	fakeGlab(t, `echo 'HTTP 500 Internal Server Error' >&2; exit 1`)
	m := press(sampleModel(100, 30), "c", "hello")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = press(nm.(Model), "esc", "d") // hidden while sending; the due pop-up opens over it
	if m.form == nil || m.form.kind != formDue {
		t.Fatal("test needs another pop-up open")
	}
	nm, _ = m.Update(cmd())
	m = nm.(Model)
	if m.form == nil || m.form.kind != formDue {
		t.Fatalf("the failure must not replace the open pop-up; form = %+v", m.form)
	}
	if !strings.Contains(m.flash, "comment failed") || !m.flashBad {
		t.Errorf("flash = %q", m.flash)
	}
	m = press(m, "esc")
	if m.form == nil || m.form.kind != formComment || m.form.fields[0].in.Value() != "hello" ||
		!strings.Contains(m.form.err, "comment failed") {
		t.Fatalf("the failed comment must come back with its text once the other pop-up closes; form = %+v", m.form)
	}
	if len(m.busy) != 0 {
		t.Errorf("busy not cleared: %v", m.busy)
	}
	if m = press(m, "esc"); m.form != nil {
		t.Error("esc must close the restored pop-up")
	}
}

// peakGlab is a fake glab that records how many of it run at once.
func peakGlab(t *testing.T) (peaks string) {
	t.Helper()
	dir := t.TempDir()
	running := filepath.Join(dir, "running")
	if err := os.Mkdir(running, 0o700); err != nil {
		t.Fatal(err)
	}
	peaks = filepath.Join(dir, "peaks")
	fakeGlab(t, `touch '`+running+`'/$$; ls '`+running+`' | wc -l >> '`+peaks+`'; sleep 0.3; rm '`+running+`'/$$
		echo '{"id":1,"iid":1,"state":"opened","references":{"full":"team/proj#1"}}'`)
	return peaks
}

// runAll runs the commands of a batch at once, as Bubble Tea would, and
// returns their messages.
func runAll(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatal("want a batch")
	}
	msgs := make([]tea.Msg, len(batch))
	var wg sync.WaitGroup
	for i, c := range batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs[i] = c()
		}()
	}
	wg.Wait()
	return msgs
}

// maxPeak returns the most glab processes a peakGlab saw at once.
func maxPeak(t *testing.T, peaks string) int {
	t.Helper()
	most := 0
	for _, l := range strings.Fields(calls(t, peaks)) {
		n, err := strconv.Atoi(l)
		if err != nil {
			t.Fatal(err)
		}
		most = max(most, n)
	}
	return most
}

// A bulk action over many issues runs at most actionWorkers glab
// processes at a time; every issue is still done.
func TestBulkActionsBounded(t *testing.T) {
	targets := make([]gl.Issue, 12)
	for i := range targets {
		targets[i] = testIssue(i+1, fmt.Sprintf("team/proj#%d", i+1), "")
	}
	for name, run := range map[string]func(m *Model) tea.Cmd{
		"updateMany": func(m *Model) tea.Cmd {
			return m.updateMany(targets, "close", func(gl.Issue) map[string]any { return map[string]any{"state_event": "close"} })
		},
		"assign": func(m *Model) tea.Cmd { return m.assign(targets, "+me") },
	} {
		peaks := peakGlab(t)
		m := sampleModel(100, 30)
		msgs := runAll(t, run(&m))
		if len(msgs) != len(targets) {
			t.Errorf("%s: %d replies, want %d", name, len(msgs), len(targets))
		}
		for _, msg := range msgs {
			if up, ok := msg.(updatedMsg); !ok || up.err != nil {
				t.Errorf("%s: reply %+v", name, msg)
			}
		}
		if most := maxPeak(t, peaks); most > actionWorkers || most < 2 {
			t.Errorf("%s: %d glab processes at once, want 2..%d", name, most, actionWorkers)
		}
	}
}

// A comment or new issue that succeeds while a reload is held back behind
// a pop-up must survive that list being installed when the pop-up closes:
// the fresh copy, not the held-back one, is what the list shows.
func TestReplySurvivesHeldBackList(t *testing.T) {
	fakeGlab(t, `case "$*" in *POST*) echo '{}';; *) echo '{"id":1,"iid":1,"title":"fresh","references":{"full":"team/proj#1"}}';; esac`)
	m := press(sampleModel(100, 30), "c", "hello")
	nm, _ := m.Update(issuesMsg{state: "opened", issues: slices.Clone(m.issues)}) // older copies
	m = nm.(Model)
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	m = nm.(Model)
	nm, _ = m.Update(cmd())
	m = nm.(Model)
	if m.form != nil || m.pendingLoad != nil {
		t.Fatal("the pop-up should close and the held-back list be installed")
	}
	if i := slices.IndexFunc(m.issues, func(is gl.Issue) bool { return is.ID == 1 }); i < 0 || m.issues[i].Title != "fresh" {
		t.Errorf("the held-back list undid the comment's refetch: %+v", m.issues)
	}

	// A new issue created while another pop-up holds a list back.
	fakeGlab(t, `echo '{"id":9,"iid":9,"state":"opened","references":{"full":"team/proj#9"}}'`)
	m = press(sampleModel(100, 30), "d")
	nm, _ = m.Update(issuesMsg{state: "opened", issues: slices.Clone(m.issues)})
	m = nm.(Model)
	nm, _ = m.Update(m.createIssue("team/proj", "new", "", "", nil)())
	m = press(nm.(Model), "esc")
	if m.pendingLoad != nil || !slices.ContainsFunc(m.issues, func(is gl.Issue) bool { return is.ID == 9 }) {
		t.Errorf("the held-back list dropped the new issue: %+v", m.issues)
	}
}

// Adding assignees from a bulk pop-up starts from the newest copy of each
// issue, the list held back behind the pop-up included: the full list
// sent keeps an assignee someone added meanwhile.
func TestAddAssigneesUsesHeldBackList(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin")
	fakeGlab(t, "cat >> '"+stdin+"'; echo '{}'")
	m := peopleModel()
	m.opts.Refresh = 0          // no timers: drain runs every command
	m = press(m, " ", " ", "a") // issues 1 and 2: "Add assignees"
	if m.form == nil || m.form.title != "Add assignees" {
		t.Fatalf("setup: form %+v", m.form)
	}
	fresh := slices.Clone(m.issues)
	for i := range fresh {
		if fresh[i].ID == 1 { // carol took it meanwhile
			fresh[i].Assignees = append(slices.Clone(fresh[i].Assignees), gl.User{ID: 4, Username: "carol"})
		}
	}
	nm, _ := m.Update(issuesMsg{state: "opened", background: true, issues: fresh})
	m = nm.(Model)
	if m.pendingLoad == nil {
		t.Fatal("the refresh should be held back behind the pop-up")
	}
	m = press(m, "ali", "enter")
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, nm.(Model), cmd)
	if m.form != nil || m.pendingLoad != nil {
		t.Fatal("the pop-up should close and the held-back list be installed")
	}
	b, _ := os.ReadFile(stdin)
	if sent := string(b); strings.Count(sent, `"assignee_ids"`) != 2 || !regexp.MustCompile(`\[(\d+,)*4(,\d+)*\]`).MatchString(sent) {
		t.Errorf("issue 1 should keep carol (id 4); sent %s", sent)
	}
}
