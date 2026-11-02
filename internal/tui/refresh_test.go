package tui

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
)

func issue(id int, updated time.Time) gl.Issue {
	is := gl.Issue{ID: id, IID: id, Title: "t", State: "opened", UpdatedAt: updated}
	is.References.Full = "team/proj#" + string(rune('0'+id))
	return is
}

func TestDiffIssues(t *testing.T) {
	t0 := time.Now()
	old := []gl.Issue{issue(1, t0), issue(2, t0), issue(3, t0)}
	cur := []gl.Issue{issue(1, t0), issue(2, t0.Add(time.Minute)), issue(4, t0)}
	c := diffIssues(old, cur)
	if len(c.added) != 1 || c.added[0] != 4 || len(c.updated) != 1 || c.updated[0] != 2 || c.gone != 1 {
		t.Errorf("diff = %+v", c)
	}
	if got := c.String(); got != "1 new issue · 1 updated · 1 closed or removed" {
		t.Errorf("summary = %q", got)
	}
	if !diffIssues(old, old).empty() {
		t.Error("identical lists should have no changes")
	}
}

func loaded(t0 time.Time) Model {
	m := New(gl.Client{}, "team", flatOptions())
	nm, _ := m.Update(issuesMsg{state: "opened", issues: []gl.Issue{issue(1, t0), issue(2, t0), issue(3, t0)}})
	m = nm.(Model)
	m.width, m.height = 100, 30
	return m
}

func TestBackgroundRefreshMerges(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	noteGlab(t, map[int]string{2: note(2, t0.Add(time.Minute))}) // changed by someone else
	m := loaded(t0)
	m.opts.Refresh = 0 // no timers: drain runs every command
	m.me = gl.User{ID: 1, Username: "me"}
	m = drain(t, m, m.loadSeen()) // first run: the baseline
	m = press(m, "j", " ")        // cursor on issue 2, select it -> cursor moves to 3
	m.selected[3] = true
	curID := m.current().ID

	nm, cmd := m.Update(issuesMsg{state: "opened", background: true,
		issues: []gl.Issue{issue(1, t0), issue(2, t0.Add(time.Minute)), issue(4, t0)}})
	m = drain(t, nm.(Model), cmd)

	if !strings.Contains(m.flash, "1 new issue") || !strings.Contains(m.flash, "1 updated") {
		t.Errorf("flash = %q", m.flash)
	}
	if m.fresh[4] != "new" || m.fresh[2] != "updated" {
		t.Errorf("fresh tags = %v", m.fresh)
	}
	if m.selected[3] || !m.selected[2] {
		t.Errorf("selection should drop vanished issues only: %v", m.selected)
	}
	if curID == 3 && m.current() == nil {
		t.Error("cursor lost")
	}
	if m.lastLoad.IsZero() || m.refreshing {
		t.Error("load time / refreshing flag not updated")
	}
	// opening an issue clears its tag
	for i, vi := range m.view {
		if m.issues[vi].ID == 4 {
			m.cursor = i
		}
	}
	if m = press(m, "enter"); m.fresh[4] != "" {
		t.Error("opening an issue should clear its tag")
	}
}

func TestRefreshKeepsCursorOnSameIssue(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	m := press(loaded(t0), "j", "j") // issue 3
	want := m.current().ID
	nm, _ := m.Update(issuesMsg{state: "opened", background: true,
		issues: []gl.Issue{issue(5, t0), issue(1, t0), issue(2, t0), issue(3, t0)}})
	if got := nm.(Model).current().ID; got != want {
		t.Errorf("cursor moved from issue %d to %d", want, got)
	}
}

func TestRefreshWaitsForPopups(t *testing.T) {
	m := press(loaded(time.Now()), "c") // comment pop-up open
	gen := m.tickGen
	nm, cmd := m.Update(refreshTickMsg{gen: gen})
	if nm.(Model).refreshing || cmd == nil {
		t.Error("with a pop-up open the refresh should be retried later, not started")
	}
	m = press(m, "esc")
	nm, _ = m.Update(refreshTickMsg{gen: m.tickGen})
	if !nm.(Model).refreshing {
		t.Error("tick should start a refresh when idle")
	}
	nm, cmd = m.Update(refreshTickMsg{gen: m.tickGen - 1})
	if nm.(Model).refreshing || cmd != nil {
		t.Error("an outdated timer must be ignored")
	}
}

func TestRefreshOff(t *testing.T) {
	o := flatOptions()
	o.Refresh = 0
	m := New(gl.Client{}, "team", o)
	if cmd := m.scheduleRefresh(time.Minute); cmd != nil {
		t.Error("refresh off must not schedule anything")
	}
	m.lastLoad = time.Now()
	if !strings.Contains(m.freshness(time.Now()), "auto-refresh off") {
		t.Errorf("title should say auto-refresh is off: %q", m.freshness(time.Now()))
	}
}

func TestOptionsApplied(t *testing.T) {
	o := flatOptions()
	o.View, o.Sort, o.State, o.Filter, o.DueSoonDays = "compact", "project", "all", "mine", 14
	m := New(gl.Client{}, "team", o)
	if !m.compact || sortNames[m.sortBy] != "project" || states[m.state] != "all" || !m.mine || m.opts.DueSoonDays != 14 {
		t.Errorf("options not applied: compact=%v sort=%s state=%s mine=%v soon=%d",
			m.compact, sortNames[m.sortBy], states[m.state], m.mine, m.opts.DueSoonDays)
	}
	if !strings.Contains(helpText(m.opts.DueSoonDays), "due within 14 days") {
		t.Error("help must use due_soon_days")
	}
}

func TestNotifyOffNoTags(t *testing.T) {
	o := flatOptions()
	o.Notify = false
	m := New(gl.Client{}, "team", o)
	t0 := time.Now()
	nm, _ := m.Update(issuesMsg{state: "opened", issues: []gl.Issue{issue(1, t0)}})
	nm, _ = nm.Update(issuesMsg{state: "opened", background: true, issues: []gl.Issue{issue(1, t0), issue(2, t0)}})
	if m := nm.(Model); len(m.fresh) != 0 || m.flash != "" {
		t.Errorf("notify off: tags %v, flash %q", m.fresh, m.flash)
	}
}

// Only the reply to the newest load counts: an older one that arrives
// late (t pressed twice, a refresh overtaken by r) is dropped, and r does
// nothing while a load runs.
func TestStaleLoadRepliesDropped(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	m := loaded(t0)
	m = press(m, "t") // closed
	first := m.loadGen
	m = press(m, "t") // all
	if !m.loading || m.loadGen == first {
		t.Fatalf("t should start a new load (gen %d -> %d)", first, m.loadGen)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}); cmd != nil {
		t.Error("r must be ignored while a load is running")
	}

	nm, _ := m.Update(issuesMsg{state: "closed", gen: first, issues: []gl.Issue{issue(9, t0)}})
	m = nm.(Model)
	if !m.loading || len(m.issues) != 3 {
		t.Fatalf("a stale reply must be dropped: loading %v, %d issues", m.loading, len(m.issues))
	}
	nm, _ = m.Update(issuesMsg{state: "all", gen: m.loadGen, issues: []gl.Issue{issue(1, t0), issue(9, t0)}})
	m = nm.(Model)
	if m.loading || len(m.issues) != 2 || m.loadedState != "all" {
		t.Errorf("the newest reply should be installed: loading %v, %d issues, state %s", m.loading, len(m.issues), m.loadedState)
	}

	// A background refresh overtaken by a manual reload.
	nm, _ = m.Update(refreshTickMsg{gen: m.tickGen})
	m = nm.(Model)
	bg := m.loadGen
	m = press(m, "r")
	if m.refreshing || m.loadGen == bg {
		t.Fatal("r should supersede the background refresh")
	}
	nm, _ = m.Update(issuesMsg{state: "all", background: true, gen: bg})
	if got := nm.(Model); len(got.issues) != 2 || got.refreshing {
		t.Errorf("an overtaken refresh must be dropped: %d issues", len(got.issues))
	}
}

// The spinner ticks only while something it shows is running.
func TestSpinnerTicksOnlyWhenBusy(t *testing.T) {
	m := loaded(time.Now())
	m.projectsLoading = false
	nm, cmd := m.Update(m.spin.Tick())
	m = nm.(Model)
	if cmd != nil || m.ticking {
		t.Fatal("an idle dashboard must stop ticking")
	}
	m.busy[1] = "assign" // an action starts
	nm, cmd = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = nm.(Model)
	if cmd == nil || !m.ticking {
		t.Fatal("the spinner should restart once something runs")
	}
	if _, cmd = m.Update(m.spin.Tick()); cmd == nil {
		t.Error("the spinner should keep ticking while busy")
	}
}

// without returns the model's issues minus the one with this ID.
func without(m Model, id int) []gl.Issue {
	var rest []gl.Issue
	for _, is := range m.issues {
		if is.ID != id {
			rest = append(rest, is)
		}
	}
	return rest
}

// backgroundLoad delivers a background refresh of the current list.
func backgroundLoad(m Model, issues []gl.Issue) Model {
	nm, _ := m.Update(issuesMsg{gen: m.loadGen, state: states[m.state], background: true, issues: issues})
	return nm.(Model)
}

// flatLoaded is sampleModel with its list loaded as "opened", like a real
// first load, so a later refresh is the same list.
func flatLoaded() Model {
	m := sampleModel(100, 30)
	nm, _ := m.Update(issuesMsg{gen: m.loadGen, state: states[m.state], issues: m.issues})
	return nm.(Model)
}

// When the issue under the cursor leaves the list in a refresh, the
// cursor lands on another issue: that is said, and the single-issue i
// asks before assigning you to it. Moving the cursor ends that.
func TestRefreshCursorIssueLeftList(t *testing.T) {
	m := press(flatLoaded(), "j")
	gone := m.current().Ref()
	m = backgroundLoad(m, without(m, m.current().ID))
	other := m.current()
	if other == nil || other.Ref() == gone {
		t.Fatalf("cursor on %v", other)
	}
	if !strings.Contains(m.flash, gone+" left the list") || !strings.Contains(m.flash, "cursor now on "+other.Ref()) {
		t.Errorf("flash %q", m.flash)
	}
	a := press(m, "i")
	if a.form == nil || a.form.kind != formConfirm || len(a.busy) != 0 {
		t.Fatalf("i right after the cursor moved by itself must ask (busy %v)", a.busy)
	}
	if !strings.Contains(a.form.sub, gone+" left the list") || !strings.Contains(a.form.sub, other.Ref()) {
		t.Errorf("question %q", a.form.sub)
	}
	if a = press(a, "y"); len(a.busy) != 1 || a.busy[other.ID] == "" {
		t.Errorf("y assigns the shown issue: busy %v", a.busy)
	}
	// after a cursor move, i acts at once again
	if b := press(m, "k", "j", "i"); b.form != nil || len(b.busy) != 1 {
		t.Errorf("i after moving the cursor: form %v busy %v", b.form, b.busy)
	}
	// a refresh that keeps the issue changes nothing
	k := press(flatLoaded(), "j")
	id := k.current().ID
	k = backgroundLoad(k, k.issues)
	if k.current().ID != id || k.cursorMoved != "" || k.flash != "" {
		t.Errorf("same list: cursor %d, moved %q, flash %q", k.current().ID, k.cursorMoved, k.flash)
	}
}

// Details stay on the opened issue across a refresh, or close (with a
// note) when it left the list, never silently showing another issue.
func TestRefreshDetailKeepsOrClosesIssue(t *testing.T) {
	m := press(flatLoaded(), "j", "enter")
	opened := m.current().ID
	// reordered list: details stay on the same issue
	rev := slices.Clone(m.issues)
	slices.Reverse(rev)
	if k := backgroundLoad(m, rev); k.mode != modeDetail || k.current().ID != opened {
		t.Errorf("reordered: mode %v on %d", k.mode, k.current().ID)
	}
	m = backgroundLoad(m, without(m, opened))
	if m.mode != modeList {
		t.Errorf("details still open on %s after their issue left", m.current().Ref())
	}
	if !strings.Contains(m.flash, "left the list, details closed") {
		t.Errorf("flash %q", m.flash)
	}
	if a := press(m, "i"); a.form == nil || len(a.busy) != 0 {
		t.Error("i after details closed by a refresh must ask")
	}
}

// The same in the tree, where the cursor indexes rows with labels.
func TestRefreshCursorIssueLeftTree(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.me = gl.User{ID: 1, Username: "me"}
	m.cursor = rowOf(m, "i:3")
	m = backgroundLoad(m, without(m, 3))
	if !strings.Contains(m.flash, "acme/platform/web/frontend/app#3 left the list") {
		t.Errorf("flash %q", m.flash)
	}
	if cur := m.current(); cur != nil {
		if a := press(m, "i"); a.form == nil || len(a.busy) != 0 {
			t.Errorf("i on %s must ask", cur.Ref())
		}
	}
	// details on an issue that a new parent link folds away: it is
	// unfolded, and details stay on it
	m = treeModel(t, 1, 120, 40)
	m.cursor = rowOf(m, "i:4")
	m = press(m, "enter")
	m.opened = map[string]bool{"g:web": true, "g:mobile": true} // parent 2 (web/site) folded
	m.rebuildView()
	parents := maps.Clone(treeParents)
	parents[4] = 2
	nm, _ := m.Update(hierarchyMsg{gen: m.loadGen, parents: parents})
	m = nm.(Model)
	if m.mode != modeDetail || m.current() == nil || m.current().ID != 4 || m.cursorMoved != "" {
		t.Errorf("details moved: mode %v, %s", m.mode, m.cursorKey())
	}
	if !m.opened["i:2"] {
		t.Error("the new parent was not unfolded")
	}
}

// flatOptions are the config defaults with the flat list instead of the
// tree, which most tests look at.
func flatOptions() Options {
	o := DefaultOptions()
	o.TreeDepth = 0
	return o
}

// Comments stay only for issues still in the list: a reload without an
// issue drops its notes, error and load generation, and a late reply for
// it is ignored.
func TestRefreshPrunesComments(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	m := loaded(t0)
	for _, is := range m.issues {
		m.notesGen[is.ID]++
		m.notes[is.ID] = []gl.Note{{Body: "hi"}}
	}
	m.notesErr[3] = errors.New("boom")
	nm, _ := m.Update(issuesMsg{state: "opened", background: true, issues: []gl.Issue{issue(1, t0)}})
	m = nm.(Model)
	for _, id := range []int{2, 3} {
		if _, ok := m.notes[id]; ok || m.notesErr[id] != nil || m.notesGen[id] != 0 {
			t.Errorf("issue %d left the list but its comments stayed: %v %v %v", id, m.notes[id], m.notesErr[id], m.notesGen[id])
		}
	}
	if len(m.notes[1]) != 1 || m.notesGen[1] != 1 {
		t.Errorf("issue 1 is still listed; comments = %v gen %d", m.notes[1], m.notesGen[1])
	}
	nm, _ = m.Update(notesMsg{id: 2, gen: 1, notes: []gl.Note{{Body: "late"}}})
	if m = nm.(Model); len(m.notes) != 1 {
		t.Errorf("a late reply for a gone issue was kept: %v", m.notes)
	}
}

// A refresh that finds the shown issue updated drops its comments and any
// failed load of them, and loads them again, rather than leaving the
// details at "loading…" or on an old error.
func TestRefreshReloadsShownComments(t *testing.T) {
	log := fakeGlab(t, `case "$*" in */notes*) echo '[]';; *) echo 'HTTP 500' >&2; exit 1;; esac`)
	t0 := time.Now().Add(-time.Hour)
	m := loaded(t0)
	m.opts.Refresh = 0                                           // no timers: drain runs every command
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30}) // sizes the details pane
	m = nm.(Model)
	m.cursor = rowOf(m, "i:2")
	m = press(m, "enter")
	if m.mode != modeDetail || m.current().ID != 2 {
		t.Fatalf("setup: mode %v, cursor on %s", m.mode, m.cursorKey())
	}
	m.notes[2] = []gl.Note{{Body: "old"}}
	m.notesErr[2] = errors.New("boom")
	m.notesErr[3] = errors.New("boom") // not shown: cleared, not reloaded
	gen := m.notesGen[2]
	var cmd tea.Cmd
	nm, cmd = m.Update(issuesMsg{state: "opened", background: true,
		issues: []gl.Issue{issue(1, t0), issue(2, t0.Add(time.Minute)), issue(3, t0.Add(time.Minute))}})
	m = nm.(Model)
	if _, ok := m.notes[2]; ok || m.notesErr[2] != nil || m.notesErr[3] != nil {
		t.Errorf("stale comments or errors kept: %v %v", m.notes, m.notesErr)
	}
	if m.notesGen[2] != gen+1 || cmd == nil {
		t.Fatalf("the shown comments are not reloaded (gen %d, was %d)", m.notesGen[2], gen)
	}
	if !strings.Contains(m.vp.View(), "loading…") {
		t.Errorf("details should say the comments are loading:\n%s", m.vp.View())
	}
	m = drain(t, m, cmd)
	if n, ok := m.notes[2]; !ok || len(n) != 0 {
		t.Errorf("comments not reloaded: %v", m.notes)
	}
	if got := calls(t, log); strings.Count(got, "/issues/2/notes") != 1 || strings.Contains(got, "/issues/3/notes") {
		t.Errorf("want one load of issue 2's comments only:\n%s", got)
	}
}

// A failed r stops showing once a background refresh brings fresh data.
func TestBackgroundRefreshClearsReloadError(t *testing.T) {
	t0 := time.Now().Add(-time.Hour)
	m := loaded(t0)
	nm, _ := m.Update(issuesMsg{state: "opened", gen: m.loadGen, err: errors.New("boom")})
	if f := nm.(Model).freshness(time.Now()); !strings.Contains(f, "reload failed") {
		t.Fatalf("after a failed reload: %q", f)
	}
	nm, _ = nm.Update(issuesMsg{state: "opened", gen: nm.(Model).loadGen, background: true, issues: []gl.Issue{issue(1, t0)}})
	if f := nm.(Model).freshness(time.Now()); strings.Contains(f, "failed") {
		t.Errorf("after a fresh background refresh: %q", f)
	}
}
