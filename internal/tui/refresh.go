package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
)

// Options are the user's preferences from the config file (see
// internal/config); zero values mean "off" or "none".
type Options struct {
	Refresh      time.Duration // reload the issue list this often; 0 = never
	RefreshLists time.Duration // reload members and projects this often; 0 = never
	Notify       bool          // tag and report changes since you last saw them (seen.go)
	Todos        bool          // fetch pending To-Dos with each list load
	Sort         string        // "due", "updated" or "project"
	State        string        // "opened", "closed" or "all"
	View         string        // "cards" or "compact"
	Filter       string        // "", "mine", "unassigned" or "due-soon"
	DueSoonDays  int
	Editor       string // editor command; "" = $VISUAL/$EDITOR
	Browser      string // URL opener command; "" = system default
	TreeDepth    int    // 0 = flat list; 1 or 2 = tree of that many subgroup levels (tree.go)
}

// DefaultOptions are the config defaults (internal/config, without
// importing it); main's TestDefaultOptionsMatchConfig keeps them equal.
// Used by tests.
func DefaultOptions() Options {
	return Options{Refresh: 5 * time.Minute, RefreshLists: time.Hour, Notify: true, Todos: true,
		Sort: "due", State: "opened", View: "cards", DueSoonDays: 7, TreeDepth: 1}
}

const refreshRetry = 15 * time.Second // when a pop-up or search blocks a refresh

// After a failed parent-link fetch, background refreshes skip it for
// hierBackoff, doubling with each failure in a row up to hierBackoffMax
// (a server without the GraphQL fields fails every time).
const (
	hierBackoff    = 15 * time.Minute
	hierBackoffMax = 4 * time.Hour
)

type refreshTickMsg struct{ gen int }
type listsTickMsg struct{}

// scheduleRefresh starts a new refresh timer and invalidates older ones,
// so manual reloads never create a second timer chain.
func (m *Model) scheduleRefresh(after time.Duration) tea.Cmd {
	if m.opts.Refresh == 0 {
		return nil
	}
	m.tickGen++
	gen := m.tickGen
	return tea.Tick(after, func(time.Time) tea.Msg { return refreshTickMsg{gen} })
}

func (m Model) scheduleLists() tea.Cmd {
	if m.opts.RefreshLists == 0 {
		return nil
	}
	return tea.Tick(m.opts.RefreshLists, func(time.Time) tea.Msg { return listsTickMsg{} })
}

// onRefreshTick starts a background reload unless the user is busy
// typing in a pop-up or search, in which case it tries again shortly.
func (m Model) onRefreshTick(msg refreshTickMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.tickGen {
		return m, nil // superseded by a newer timer
	}
	if m.form != nil || m.inputKind != inputNone || m.loading || m.refreshing {
		return m, m.scheduleRefresh(refreshRetry)
	}
	m.refreshing = true
	m.loadGen++
	var hier tea.Cmd
	if m.hierErr == nil || !time.Now().Before(m.hierRetry) { // after a failure, back off
		hier = m.loadHierarchy()
	} else {
		m.hierLoaded = false
	}
	return m, tea.Batch(m.loadIssuesBackground(), m.loadTodos(), hier)
}

func (m Model) loadIssuesBackground() tea.Cmd {
	cmd := m.loadIssues()
	return func() tea.Msg {
		msg := cmd().(issuesMsg)
		msg.background = true
		return msg
	}
}

// changes summarises what a reload found compared to the previous list.
type changes struct {
	added, updated []int // issue IDs
	gone           int   // no longer in the list (closed, moved, deleted)
}

func (c changes) empty() bool { return len(c.added)+len(c.updated)+c.gone == 0 }

// String summarises the changes for the flash line, e.g.
// "2 new issues · 1 updated".
func (c changes) String() string {
	var parts []string
	if n := len(c.added); n > 0 {
		parts = append(parts, plural(n, "new issue", "new issues"))
	}
	if n := len(c.updated); n > 0 {
		parts = append(parts, plural(n, "updated", "updated"))
	}
	if c.gone > 0 {
		parts = append(parts, plural(c.gone, "closed or removed", "closed or removed"))
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// diffIssues compares two issue lists by ID and last update.
func diffIssues(old, cur []gl.Issue) changes {
	before := make(map[int]gl.Issue, len(old))
	for _, is := range old {
		before[is.ID] = is
	}
	var c changes
	seen := map[int]bool{}
	for _, is := range cur {
		seen[is.ID] = true
		prev, ok := before[is.ID]
		switch {
		case !ok:
			c.added = append(c.added, is.ID)
		case !prev.UpdatedAt.Equal(is.UpdatedAt) || prev.Notes != is.Notes:
			c.updated = append(c.updated, is.ID)
		}
	}
	for id := range before {
		if !seen[id] {
			c.gone++
		}
	}
	return c
}

// applyIssues installs a freshly loaded list, keeping the cursor,
// selection and filters, and reports what changed since the previous
// list, and a note if the row under the cursor left it (see relayout).
// Tags come from the seen file instead (compareSeen). The comments of an
// updated issue are dropped; reload reloads them if details show it.
func (m *Model) applyIssues(msg issuesMsg) (ch changes, note string, reload tea.Cmd) {
	sameList := m.loadedState == msg.state && m.lastLoad != (time.Time{})
	keep := m.cursorKey() // row under the cursor, found again after the swap
	keepName := m.cursorName()
	m.view, m.rows = nil, nil // indices into the old list are meaningless now
	if sameList {
		ch = diffIssues(m.issues, msg.issues)
	}
	m.issues = msg.issues
	m.loadedState = msg.state
	m.lastLoad = time.Now()

	present := map[int]bool{}
	for _, is := range m.issues {
		present[is.ID] = true
	}
	for id := range m.selected {
		if !present[id] {
			delete(m.selected, id)
		}
	}
	for id := range m.fresh {
		if !present[id] {
			delete(m.fresh, id)
		}
	}
	updated := map[int]bool{}
	for _, id := range ch.updated {
		updated[id] = true
		delete(m.notes, id) // comments may have changed: reload on open
		delete(m.notesErr, id)
	}
	for id := range m.notesGen { // every comments load registers here first
		if !present[id] { // left the list: nothing shows its comments any more
			delete(m.notes, id)
			delete(m.notesErr, id)
			delete(m.notesGen, id)
		}
	}
	// Switching lists (t) may well drop the cursor's issue: no note then.
	note = m.relayout(keep, keepName, sameList || m.mode == modeDetail)
	if m.mode == modeDetail {
		// Open details would otherwise say "loading…" with nothing loading.
		if is := m.current(); is != nil && updated[is.ID] {
			reload = m.loadNotes(*is)
		}
	}
	m.refreshDetail()
	return ch, note, reload
}

// relayout rebuilds the rows after a change the user did not make (a
// load, a refresh, new parent links), keeping the cursor on the row named
// keep. An issue now hidden under a fold is unfolded. If the row has left
// the list, the cursor stays at its index, now on another row: details
// close (they would show that other issue), and with announce the next
// single-issue i asks first and the returned note (also flashed) says so.
func (m *Model) relayout(keep, name string, announce bool) string {
	m.rebuildViewKeep(keep)
	if keep == "" || m.cursorKey() == keep {
		return ""
	}
	if id, ok := keyIssueID(keep); ok && m.reveal(id) {
		m.rebuildViewKeep(keep)
		if m.cursorKey() == keep {
			return ""
		}
	}
	note := name + " left the list"
	if m.mode == modeDetail {
		m.mode = modeList
		m.clamp() // the list is taller than under the details
		note += ", details closed"
	}
	if !announce {
		return ""
	}
	m.cursorMoved = name
	if cur := m.current(); cur != nil {
		note += " · cursor now on " + cur.Ref()
	}
	m.setFlash(note, true)
	return note
}

// freshness returns the "updated 3m ago" part of the title line.
func (m Model) freshness(now time.Time) string {
	switch {
	case m.refreshing:
		return m.spin.View() + " refreshing"
	case m.refreshErr != nil && m.lastLoad.IsZero():
		return styleWarn.Render("refresh failed, no data yet")
	case m.refreshErr != nil:
		return styleWarn.Render("refresh failed, showing data from " + ago(m.lastLoad, now) + " ago")
	case m.loadErr != nil && !m.lastLoad.IsZero(): // a reload failed; its flash goes on the next key
		return styleWarn.Render("reload failed, showing data from " + ago(m.lastLoad, now) + " ago")
	case m.lastLoad.IsZero():
		return ""
	}
	s := "updated " + ago(m.lastLoad, now) + " ago"
	if now.Sub(m.lastLoad) < time.Minute {
		s = "updated just now"
	}
	if m.opts.Refresh == 0 {
		s += " · auto-refresh off"
	}
	return s
}
