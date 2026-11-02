package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
	"gl-issues/internal/state"
)

// TestMain keeps every test away from the real state directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "gl-issues-state-")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", dir)
	os.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache")) // editor temp files
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// drain runs cmd and everything it leads to, feeding each message back
// into the model. Timers must be off (Options.Refresh = 0): they sleep.
func drain(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 1000 {
			t.Fatal("commands never stop")
		}
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case spinner.TickMsg, tea.QuitMsg, nil:
		default:
			nm, next := m.Update(msg)
			m = nm.(Model)
			queue = append(queue, next)
		}
	}
	return m
}

var t0 = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

const seenHost, seenGroup = "gitlab.example.edu", "acme"

// si is issue id of acme/platform, updated min minutes after t0.
func si(id, min, notes int) gl.Issue {
	is := gl.Issue{ID: id, IID: id, Title: "t", State: "opened", Notes: notes,
		CreatedAt: t0.Add(-time.Hour), UpdatedAt: t0.Add(time.Duration(min) * time.Minute)}
	is.Author = gl.User{ID: 2, Username: "alice"}
	is.References.Full = fmt.Sprintf("acme/platform#%d", id)
	return is
}

// seenSetup gives the test its own state directory and returns a model
// (refresh timers off, you are user 1) and its seen file.
func seenSetup(t *testing.T, o Options) (Model, string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	o.Refresh = 0
	m := New(gl.Client{Host: seenHost}, seenGroup, o)
	m.me = gl.User{ID: 1, Username: "me"}
	m.width, m.height = 100, 30
	return m, state.SeenFile(seenHost, seenGroup)
}

func writeSeen(t *testing.T, name string, issues ...gl.Issue) {
	t.Helper()
	s := state.Seen{Covered: []string{"opened"}, Issues: map[int]state.SeenIssue{}}
	for _, is := range issues {
		s.Issues[is.ID] = seenEntry(is)
	}
	if err := state.SaveSeen(name, s); err != nil {
		t.Fatal(err)
	}
}

func readSeen(t *testing.T, name string) state.Seen {
	t.Helper()
	s, existed, err := state.LoadSeen(name)
	if err != nil || !existed {
		t.Fatalf("seen file: existed %v, %v", existed, err)
	}
	return s
}

// start delivers the first list, then the seen file, as at startup.
func start(t *testing.T, m Model, list ...gl.Issue) Model {
	t.Helper()
	nm, cmd := m.Update(issuesMsg{state: states[m.state], gen: m.loadGen, issues: list})
	m = drain(t, nm.(Model), cmd)
	return drain(t, m, m.loadSeen())
}

// refresh delivers a background reload of the same view.
func refresh(t *testing.T, m Model, list ...gl.Issue) Model {
	t.Helper()
	nm, cmd := m.Update(issuesMsg{state: states[m.state], gen: m.loadGen, background: true, issues: list})
	return drain(t, nm.(Model), cmd)
}

// noteGlab fakes glab: GET user is user 1 ("me"), and the notes of
// issue N are notes[N] (JSON objects, newest first), or a failure when missing.
func noteGlab(t *testing.T, notes map[int]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`case "$*" in *" user") echo '{"id":1,"username":"me"}';;` + "\n")
	for id, n := range notes {
		fmt.Fprintf(&b, "*/issues/%d/notes*) echo '[%s]';;\n", id, n)
	}
	b.WriteString("*) echo 'HTTP 500' >&2; exit 1;;\nesac")
	return fakeGlab(t, b.String())
}

func note(author int, at time.Time) string {
	return fmt.Sprintf(`{"id":9,"system":true,"body":"changed the due date","author":{"id":%d},"updated_at":%q}`,
		author, at.Format(time.RFC3339Nano))
}

func countCalls(t *testing.T, log, part string) int {
	return strings.Count(calls(t, log), part)
}

func TestSeenFirstRunBaseline(t *testing.T) {
	log := noteGlab(t, nil)
	m, name := seenSetup(t, flatOptions())
	m = start(t, m, si(1, 0, 0), si(2, 5, 1))
	if len(m.fresh) != 0 || m.flash != "" {
		t.Errorf("first run must tag nothing: %v %q", m.fresh, m.flash)
	}
	s := readSeen(t, name)
	if len(s.Issues) != 2 || !s.Issues[2].UpdatedAt.Equal(si(2, 5, 1).UpdatedAt) || s.Issues[2].Notes != 1 ||
		s.Group != seenGroup || s.Host != seenHost || s.WrittenAt.IsZero() {
		t.Errorf("baseline = %+v", s)
	}
	if calls(t, log) != "" {
		t.Errorf("no requests expected: %s", calls(t, log))
	}
	st, _ := os.Stat(name)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
}

// Changes by others since the last run are tagged; an issue that moved
// project keeps its ID and is not; closed or removed issues leave the file.
func TestSeenRestartWithOthersChanges(t *testing.T) {
	log := noteGlab(t, map[int]string{2: note(2, t0.Add(10*time.Minute)), 3: note(3, t0.Add(20*time.Minute))})
	m, name := seenSetup(t, flatOptions())
	moved := si(5, 0, 0)
	writeSeen(t, name, si(1, 0, 0), si(2, 0, 0), si(3, 0, 1), moved, si(6, 0, 0))
	moved.References.Full = "acme/other#1"
	m = start(t, m, si(1, 0, 0), si(2, 10, 0), si(3, 20, 2), si(4, 0, 0), moved)

	if m.fresh[2] != "updated" || m.fresh[3] != "updated" || m.fresh[4] != "new" || m.fresh[1] != "" || m.fresh[5] != "" {
		t.Errorf("tags = %v", m.fresh)
	}
	if m.flash != "since last time: 1 new issue · 2 updated" {
		t.Errorf("flash = %q", m.flash)
	}
	s := readSeen(t, name)
	if _, ok := s.Issues[6]; ok {
		t.Error("an issue no longer listed must be pruned")
	}
	if _, ok := s.Issues[4]; ok {
		t.Error("a new issue stays out of the file until seen")
	}
	if e := s.Issues[2]; !e.Tagged || !e.UpdatedAt.Equal(t0) {
		t.Errorf("a tagged issue keeps what you last saw: %+v", e)
	}
	if n := countCalls(t, log, "/notes?sort=desc&order_by=updated_at&per_page=20"); n != 2 {
		t.Errorf("%d note requests, want 2", n)
	}

	// The tag survives another restart without asking again.
	before := countCalls(t, log, "/notes")
	m2, _ := seenSetup2(t, name)
	m2 = start(t, m2, si(1, 0, 0), si(2, 10, 0), si(3, 20, 2), si(4, 0, 0), moved)
	if m2.fresh[2] != "updated" || m2.fresh[4] != "new" {
		t.Errorf("tags after restart = %v", m2.fresh)
	}
	if countCalls(t, log, "/notes") != before {
		t.Error("a tag that survived must not be checked again")
	}
}

// seenSetup2 opens a second session on the same state directory.
func seenSetup2(t *testing.T, name string) (Model, string) {
	o := flatOptions()
	o.Refresh = 0
	m := New(gl.Client{Host: seenHost}, seenGroup, o)
	m.me = gl.User{ID: 1, Username: "me"}
	m.width, m.height = 100, 30
	if m.seenFile != name {
		t.Fatalf("seen file %q, want %q", m.seenFile, name)
	}
	return m, name
}

// Changes you made elsewhere (notes since seen all yours) are marked seen silently.
// The logged-in user is fetched once if not yet known.
func TestSeenRestartOwnChangesSilent(t *testing.T) {
	log := noteGlab(t, map[int]string{2: note(1, t0.Add(10*time.Minute-2*time.Second)), 3: note(1, t0.Add(30*time.Minute))})
	m, name := seenSetup(t, flatOptions())
	m.me = gl.User{}
	writeSeen(t, name, si(1, 0, 0), si(2, 0, 0), si(3, 0, 0))
	m = start(t, m, si(1, 0, 0), si(2, 10, 0), si(3, 20, 1))
	if len(m.fresh) != 0 || m.flash != "" {
		t.Errorf("own changes must be silent: %v %q", m.fresh, m.flash)
	}
	if e := readSeen(t, name).Issues[3]; !e.UpdatedAt.Equal(si(3, 20, 1).UpdatedAt) || e.Notes != 1 || e.Tagged {
		t.Errorf("own change not recorded as seen: %+v", e)
	}
	if n := countCalls(t, log, " user\n"); n != 1 || m.me.ID != 1 {
		t.Errorf("GET user %d times, me = %+v", n, m.me)
	}
}

// Your comment followed by someone else's change (newest note older than
// the issue's change) is tagged, and so is an issue whose check fails.
func TestSeenOwnNoteOlderOrCheckFailsIsTagged(t *testing.T) {
	noteGlab(t, map[int]string{2: note(1, t0.Add(time.Minute))}) // issue 3: request fails
	m, name := seenSetup(t, flatOptions())
	writeSeen(t, name, si(2, 0, 0), si(3, 0, 0))
	m = start(t, m, si(2, 10, 1), si(3, 10, 0))
	if m.fresh[2] != "updated" || m.fresh[3] != "updated" {
		t.Errorf("tags = %v", m.fresh)
	}
}

// More than maxChecks changed: the newest are checked, checkWorkers at a
// time, and the rest are tagged unchecked.
func TestSeenManyChanged(t *testing.T) {
	dir := t.TempDir()
	log := fakeGlab(t, `case "$*" in *"/notes"*)
  touch '`+dir+`/run.'$$; ls '`+dir+`' | grep -c '^run\.' >> '`+dir+`/conc'; sleep 0.05; rm -f '`+dir+`/run.'$$
  echo '[`+note(1, t0.Add(2*time.Hour))+`]';;
*) exit 1;; esac`)
	m, name := seenSetup(t, flatOptions())
	var old, cur []gl.Issue
	for id := 1; id <= 60; id++ {
		old = append(old, si(id, 0, 0))
		cur = append(cur, si(id, id, 0))
	}
	writeSeen(t, name, old...)
	m = start(t, m, cur...)
	if n := countCalls(t, log, "/notes"); n != maxChecks {
		t.Errorf("%d note requests, want %d", n, maxChecks)
	}
	for id := 1; id <= 60; id++ {
		if want := id <= 60-maxChecks; (m.fresh[id] == "updated") != want {
			t.Errorf("issue %d: tag %q", id, m.fresh[id])
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "conc"))
	if n := len(strings.Fields(string(b))); n != maxChecks {
		t.Errorf("%d requests counted, want %d", n, maxChecks)
	}
	for _, l := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(l); err != nil || n > checkWorkers {
			t.Fatalf("%s note requests at once, limit %d", l, checkWorkers)
		}
	}
}

// Opening an issue clears its tag and records it; so does a write by
// gl-issues. Neither is tagged again by the next refresh.
func TestSeenOpenAndOwnWriteMarkSeen(t *testing.T) {
	log := noteGlab(t, map[int]string{2: note(2, t0.Add(10*time.Minute))})
	m, name := seenSetup(t, flatOptions())
	writeSeen(t, name, si(1, 0, 0), si(2, 0, 0))
	m = start(t, m, si(1, 0, 0), si(2, 10, 0))
	if m.fresh[2] != "updated" {
		t.Fatalf("tags = %v", m.fresh)
	}
	for m.current().ID != 2 {
		m = press(m, "j")
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, nm.(Model), cmd)
	if m.fresh[2] != "" {
		t.Error("opening must clear the tag")
	}
	if e := readSeen(t, name).Issues[2]; !e.UpdatedAt.Equal(si(2, 10, 0).UpdatedAt) || e.Tagged {
		t.Errorf("opened issue not saved as seen: %+v", e)
	}
	m = press(m, "esc")

	// A due date set by gl-issues on issue 1.
	up := si(1, 30, 0)
	up.DueDate = "2026-06-01"
	nm, cmd = m.Update(updatedMsg{id: 1, what: "due", issue: up})
	m = drain(t, nm.(Model), cmd)
	if e := readSeen(t, name).Issues[1]; !e.UpdatedAt.Equal(up.UpdatedAt) {
		t.Errorf("own write not saved as seen: %+v", e)
	}
	before := countCalls(t, log, "/notes")
	m = refresh(t, m, up, si(2, 10, 0))
	if len(m.fresh) != 0 || countCalls(t, log, "/notes") != before {
		t.Errorf("after open and own write: tags %v, %d new note requests", m.fresh, countCalls(t, log, "/notes")-before)
	}
}

func TestSeenCorruptFile(t *testing.T) {
	noteGlab(t, nil)
	m, name := seenSetup(t, flatOptions())
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = start(t, m, si(1, 0, 0), si(2, 0, 0))
	if len(m.fresh) != 0 || !strings.Contains(m.flash, "starting afresh") || m.flashBad {
		t.Errorf("corrupt file: tags %v, flash %q (bad %v)", m.fresh, m.flash, m.flashBad)
	}
	if s := readSeen(t, name); len(s.Issues) != 2 {
		t.Errorf("fresh baseline not written: %+v", s)
	}
}

// With notify off there are no tags, no seen file and no note requests.
func TestSeenNotifyOff(t *testing.T) {
	log := noteGlab(t, nil)
	o := flatOptions()
	o.Notify = false
	m, name := seenSetup(t, o)
	writeSeen(t, name, si(1, 0, 0))
	before, _ := os.ReadFile(name)
	if m.loadSeen() != nil {
		t.Fatal("notify off must not read the seen file")
	}
	m = start(t, m, si(1, 5, 0), si(2, 0, 0))
	m = refresh(t, m, si(1, 9, 0), si(2, 0, 0), si(3, 0, 0))
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		cmd() // the notes of the opened issue only
	}
	m = nm.(Model)
	after, _ := os.ReadFile(name)
	if len(m.fresh) != 0 || m.flash != "" || string(after) != string(before) || strings.Contains(calls(t, log), "sort=desc") {
		t.Errorf("notify off: tags %v, flash %q, file changed %v, calls %q", m.fresh, m.flash, string(after) != string(before), calls(t, log))
	}
}

// Switching between open, closed and all compares only issues in both
// lists: nothing is tagged new, and the other view's entries are kept.
func TestSeenViewSwitchDoesNotMassTag(t *testing.T) {
	noteGlab(t, nil)
	m, name := seenSetup(t, flatOptions())
	open := []gl.Issue{si(1, 0, 0), si(2, 0, 0)}
	closed := []gl.Issue{si(10, 0, 0), si(11, 0, 0)}
	for i := range closed {
		closed[i].State = "closed"
	}
	m = start(t, m, open...)

	load := func(m Model, list ...gl.Issue) Model {
		m = press(m, "t")
		nm, cmd := m.Update(issuesMsg{state: states[m.state], gen: m.loadGen, issues: list})
		return drain(t, nm.(Model), cmd)
	}
	m = load(m, closed...) // closed
	if len(m.fresh) != 0 {
		t.Errorf("closed view: tags %v", m.fresh)
	}
	m = load(m, append(append([]gl.Issue{}, open...), closed...)...) // all
	if len(m.fresh) != 0 {
		t.Errorf("all view: tags %v", m.fresh)
	}
	m = load(m, open...) // opened again
	if len(m.fresh) != 0 {
		t.Errorf("opened view: tags %v", m.fresh)
	}
	s := readSeen(t, name)
	if len(s.Issues) != 4 || strings.Join(s.Covered, ",") != "closed,opened" {
		t.Errorf("seen file after switching: %+v", s)
	}
	// A new closed issue shows up as new once the closed list is known.
	m = load(m, append(closed, func() gl.Issue { is := si(12, 0, 0); is.State = "closed"; return is }())...)
	if m.fresh[12] != "new" || len(m.fresh) != 1 {
		t.Errorf("new closed issue: tags %v", m.fresh)
	}
}

// The seen file may arrive before the list, and an issue opened before
// either arrives is not tagged.
func TestSeenFileBeforeList(t *testing.T) {
	noteGlab(t, map[int]string{1: note(2, t0.Add(10*time.Minute))})
	m, name := seenSetup(t, flatOptions())
	writeSeen(t, name, si(1, 0, 0))
	m = drain(t, m, m.loadSeen())
	nm, cmd := m.Update(issuesMsg{state: "opened", gen: m.loadGen, issues: []gl.Issue{si(1, 10, 0), si(2, 0, 0)}})
	m = drain(t, nm.(Model), cmd)
	if m.fresh[1] != "updated" || m.fresh[2] != "new" || !strings.HasPrefix(m.flash, "since last time: ") {
		t.Errorf("tags %v, flash %q", m.fresh, m.flash)
	}
}

// A check that finishes after a newer comparison started is dropped.
func TestSeenStaleCheckDropped(t *testing.T) {
	m, _ := seenSetup(t, flatOptions())
	m = start(t, m, si(1, 0, 0))
	nm, _ := m.Update(checkMsg{gen: m.checkGen - 1, cands: []gl.Issue{si(1, 5, 0)}, own: map[int]bool{}})
	if len(nm.(Model).fresh) != 0 {
		t.Error("a stale check must be ignored")
	}
}

// The user may arrive with a check, when loading it failed before: the
// "mine" filter shows the user's issues then, not at the next list.
func TestSeenCheckBringsUser(t *testing.T) {
	m, _ := seenSetup(t, flatOptions())
	is := si(1, 0, 0)
	is.Assignees = []gl.User{{ID: 1, Username: "me"}}
	m.me = gl.User{}
	m = start(t, m, is)
	m.mine = true
	m.rebuildView()
	if len(m.view) != 0 {
		t.Fatalf("%d issues match mine without a user", len(m.view))
	}
	nm, _ := m.Update(checkMsg{gen: m.checkGen, me: gl.User{ID: 1, Username: "me"}, own: map[int]bool{}})
	if m = nm.(Model); len(m.view) != 1 {
		t.Errorf("after the check brought the user: %d issues shown", len(m.view))
	}
}

// Before the first list the compact header's ref column has its minimum
// width, as it has afterwards.
func TestRefColumnBeforeList(t *testing.T) {
	m := New(gl.Client{Host: seenHost}, seenGroup, flatOptions())
	if m.stats.refW != 8 {
		t.Errorf("ref column %d wide before the first list, want 8", m.stats.refW)
	}
}

// Quitting writes unsaved seen state first.
func TestSeenFlushedOnQuit(t *testing.T) {
	m, name := seenSetup(t, flatOptions())
	m = start(t, m, si(1, 0, 0))
	m.seen.Issues[1] = seenEntry(si(1, 7, 0))
	m.seenDirty = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("no quit command")
	}
	// tea.Sequence: run its commands in order.
	for _, c := range sequenceCmds(cmd) {
		if c != nil {
			c()
		}
	}
	if e := readSeen(t, name).Issues[1]; !e.UpdatedAt.Equal(si(1, 7, 0).UpdatedAt) {
		t.Errorf("not flushed on quit: %+v", e)
	}
}

// Quitting while a write runs (nothing dirty) still waits for the file to
// hold the latest state.
func TestSeenQuitWaitsForRunningSave(t *testing.T) {
	m, name := seenSetup(t, flatOptions())
	m = start(t, m, si(1, 0, 0))
	m.seen.Issues[1] = seenEntry(si(1, 7, 0))
	m.seenDirty, m.seenSaving = false, true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	cmds := sequenceCmds(cmd)
	if len(cmds) == 0 {
		t.Fatal("quit did not wait for the running save")
	}
	for _, c := range cmds {
		if c != nil {
			c()
		}
	}
	if e := readSeen(t, name).Issues[1]; !e.UpdatedAt.Equal(si(1, 7, 0).UpdatedAt) {
		t.Errorf("not written on quit: %+v", e)
	}
}

// sequenceCmds returns the commands of a tea.Sequence (or cmd alone).
func sequenceCmds(cmd tea.Cmd) []tea.Cmd {
	msg := cmd()
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice {
		return nil
	}
	cmds := make([]tea.Cmd, v.Len())
	for i := range cmds {
		cmds[i], _ = v.Index(i).Interface().(tea.Cmd)
	}
	return cmds
}

// A write that fails leaves the state unsaved, so quitting tries again.
func TestSeenFailedSaveRetriedOnQuit(t *testing.T) {
	m, name := seenSetup(t, flatOptions())
	m = start(t, m, si(1, 0, 0))
	m.seenFile = filepath.Join(name, "seen.json") // under a file: every write fails
	m.seen.Issues[1] = seenEntry(si(1, 7, 0))
	nm, _ := m.Update(m.saveSeen()())
	m = nm.(Model)
	if !m.seenDirty || m.seenSaving || !strings.Contains(m.flash, "could not save") {
		t.Fatalf("after a failed write: dirty %v, saving %v, flash %q", m.seenDirty, m.seenSaving, m.flash)
	}
	m.seenFile = name
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	cmds := sequenceCmds(cmd)
	if len(cmds) == 0 {
		t.Fatal("quit did not write the unsaved state")
	}
	for _, c := range cmds {
		if c != nil {
			c()
		}
	}
	if e := readSeen(t, name).Issues[1]; !e.UpdatedAt.Equal(si(1, 7, 0).UpdatedAt) {
		t.Errorf("not written on quit: %+v", e)
	}
}

// notesPage joins note objects into one page, newest first.
func notesPage(ns ...string) string { return strings.Join(ns, ",") }

// Every note since the seen version counts, not only the newest: a
// collaborator's change followed by yours is tagged, yours alone is not,
// and so is a page full of newer notes (there could be more).
func TestSeenEveryNewerNoteChecked(t *testing.T) {
	full := make([]string, gl.RecentNotesPage)
	for i := range full {
		full[i] = note(1, t0.Add(10*time.Minute-time.Duration(i)*time.Second))
	}
	noteGlab(t, map[int]string{
		1: notesPage(note(1, t0.Add(10*time.Minute)), note(3, t0.Add(5*time.Minute))), // collaborator, then you
		2: notesPage(note(1, t0.Add(10*time.Minute)), note(1, t0.Add(5*time.Minute)),
			note(3, t0.Add(-time.Hour))), // only you since seen
		3: notesPage(note(3, t0.Add(10*time.Minute)), note(1, t0.Add(5*time.Minute))), // you, then collaborator
		4: notesPage(full...),
		// issue 5: request fails
	})
	m, name := seenSetup(t, flatOptions())
	writeSeen(t, name, si(1, 0, 0), si(2, 0, 0), si(3, 0, 0), si(4, 0, 0), si(5, 0, 0))
	m = start(t, m, si(1, 10, 2), si(2, 10, 2), si(3, 10, 2), si(4, 10, 20), si(5, 10, 1))
	for id, want := range map[int]string{1: "updated", 2: "", 3: "updated", 4: "updated", 5: "updated"} {
		if m.fresh[id] != want {
			t.Errorf("issue %d: tag %q, want %q", id, m.fresh[id], want)
		}
	}
}
