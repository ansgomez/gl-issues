package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"gl-issues/internal/gl"
)

// What the list caches between frames and keystrokes (the title's
// counts, the ref column width, the search text of each issue) must
// follow every change to the list. The benchmarks show the per-frame and
// per-keystroke cost on a large group.

// bigIssues makes n open issues over a few subgroups and projects, with
// mixed due dates and assignees.
func bigIssues(n int) []gl.Issue {
	issues := make([]gl.Issue, n)
	now := time.Now()
	for i := range issues {
		is := gl.Issue{ID: i + 1, IID: i + 1, State: "opened", Title: fmt.Sprintf("Issue %d about thing %d", i+1, i%17),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute), Labels: []string{"kind::" + []string{"bug", "feature", "chore"}[i%3]}}
		is.References.Full = fmt.Sprintf("acme/platform/%s/project-%d#%d", []string{"web", "api", "ops/tools"}[i%3], i%7, i+1)
		if i%4 != 0 {
			is.DueDate = now.AddDate(0, 0, i%30-10).Format(time.DateOnly)
		}
		if i%5 != 0 {
			is.Assignees = []gl.User{{Username: []string{"alice", "bob", "carol", "me"}[i%4]}}
		}
		issues[i] = is
	}
	return issues
}

// bigModel is a dashboard of n issues, w wide and h high, in the flat
// list, as after the first load.
func bigModel(n, w, h int) Model {
	m := New(gl.Client{Host: "gitlab.example.edu"}, "acme/platform", flatOptions())
	m.me = gl.User{ID: 1, Username: "me"}
	nm, _ := m.Update(issuesMsg{issues: bigIssues(n), state: "opened"})
	nm, _ = nm.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return nm.(Model)
}

// The title's counts and the ref column width are counted when the view
// is built, not on every frame, and must follow an updated issue, a
// reload, the logged-in user and the day.
func TestTitleStatsFollowTheList(t *testing.T) {
	m := sampleModel(100, 30)
	title := func(m Model) string { return ansi.Strip(m.titleLine()) }
	if s := title(m); !strings.Contains(s, "2 mine") || !strings.Contains(s, "1 overdue") || !strings.Contains(s, "1 due ≤7d") {
		t.Fatalf("title %q", s)
	}

	// An action's reply replaces #1 (overdue, mine) without due date or assignee.
	is := m.issues[0]
	is.DueDate, is.Assignees = "", nil
	m.replace(is, false)
	if s := title(m); strings.Contains(s, "overdue") || !strings.Contains(s, "1 mine") || !strings.Contains(s, "2 unassigned") {
		t.Errorf("after the update: title %q", s)
	}

	// A reload with a deeper project path widens the ref column.
	before := m.stats.refW
	wide := is
	wide.ID, wide.References.Full = 4, "team/sub/deeper/still/project-with-a-long-name#4"
	nm, _ := m.Update(issuesMsg{issues: append(m.issues, wide), state: states[m.state]})
	m = nm.(Model)
	if m.stats.refW <= before {
		t.Errorf("ref column %d wide after the reload, was %d", m.stats.refW, before)
	}

	// The user arriving after the list changes the "mine" count, and a
	// "mine" filter chosen in the config shows the issues only then.
	m.me = gl.User{}
	m.mine = true
	m.rebuildView()
	if len(m.view) != 0 {
		t.Fatalf("%d issues match mine without a user", len(m.view))
	}
	nm, _ = m.Update(metaMsg{me: gl.User{ID: 2, Username: "alice"}})
	m = nm.(Model)
	if s := title(m); !strings.Contains(s, "1 mine") || len(m.view) != 1 {
		t.Errorf("after the user arrived: title %q, %d issues shown", s, len(m.view))
	}

	// Tomorrow the issue due today is overdue: the counts are made again
	// for a new day, and reused within it.
	if st := m.titleStats(time.Now()); st != m.stats {
		t.Error("the counts were made again on the same day")
	}
	if st := m.titleStats(time.Now().AddDate(0, 0, 1)); st.overdue != 1 {
		t.Errorf("tomorrow %d overdue, want 1", st.overdue)
	}
}

// An issue closed from the open list keeps its row until the next reload
// (so it can be reopened at once) but leaves the title's counts: it is
// no longer one of the opened issues, mine or overdue. The closed list
// does the same with a reopened one.
func TestClosedIssueStaysListedButUncounted(t *testing.T) {
	m := sampleModel(100, 30)
	title := func(m Model) string { return ansi.Strip(m.titleLine()) }
	if s := title(m); !strings.Contains(s, "  3 opened · 2 mine · 1 unassigned · 1 overdue · 1 due") {
		t.Fatalf("title %q", s)
	}
	is := m.issues[0] // #1: overdue, mine
	is.State = "closed"
	m.replace(is, false)
	if s := title(m); !strings.Contains(s, "  2 opened · 1 mine · 1 unassigned · 1 due") {
		t.Errorf("after closing #1: title %q", s)
	}
	if len(m.view) != 3 || m.hiddenTargets([]gl.Issue{is}) != 0 {
		t.Errorf("the closed issue left the list: %d shown", len(m.view))
	}
	m.search = "docs" // the filtered count skips it too
	m.rebuildView()
	if s := title(m); !strings.Contains(s, "  1/2 opened") {
		t.Errorf("filtered: title %q", s)
	}

	m.search, m.state = "", 1 // the closed list, with #1 reopened in it
	for i := range m.issues {
		m.issues[i].State = "closed"
	}
	m.rebuildView()
	is.State = "opened"
	m.replace(is, false)
	if s := title(m); !strings.Contains(s, "  2 closed · 0 mine · 0 unassigned  ") || len(m.view) != 3 {
		t.Errorf("after reopening #1 in the closed list: title %q, %d shown", s, len(m.view))
	}
}

// The search text and ref width of an issue are built once, not on every
// keystroke or frame, and
// must follow an action's reply (even one that kept updated_at), a
// reload that reorders or changes issues, and one that drops some.
func TestSearchTextFollowsTheList(t *testing.T) {
	m := sampleModel(100, 30)
	found := func(m Model, search string) []int {
		m.search = search
		m.rebuildView()
		var ids []int
		for _, i := range m.view {
			ids = append(ids, m.issues[i].ID)
		}
		return ids
	}
	if got := found(m, "docs"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("search docs: %v", got)
	}
	m = press(m, "/", "docs") // a search on: the entries exist now
	if len(m.cache) != 3 {
		t.Fatalf("%d cache entries for 3 issues", len(m.cache))
	}

	// An action's reply with a new title and the same update time.
	is := m.issues[1]
	is.Title = "Write code"
	m.replace(is, false)
	if got := found(m, "docs"); len(got) != 0 {
		t.Errorf("the old title still matches: %v", got)
	}
	if got := found(m, "code"); len(got) != 1 || got[0] != 2 {
		t.Errorf("the new title does not match: %v", got)
	}

	// A reload: reversed order, #3 assigned to carol since, #1 gone.
	later := m.issues[2]
	later.Assignees, later.UpdatedAt = []gl.User{{Username: "carol"}}, later.UpdatedAt.Add(time.Minute)
	nm, _ := m.Update(issuesMsg{issues: []gl.Issue{later, m.issues[1]}, state: states[m.state]})
	m = nm.(Model)
	if got := found(m, "carol"); len(got) != 1 || got[0] != 3 {
		t.Errorf("search carol after the reload: %v", got)
	}
	if got := found(m, "code"); len(got) != 1 || got[0] != 2 {
		t.Errorf("search code after the reload: %v", got)
	}
	if got := found(m, "fix"); len(got) != 0 || len(m.cache) != 2 {
		t.Errorf("the gone issue still matches: %v (%d entries)", got, len(m.cache))
	}
	for i, h := range m.cache {
		if h.id != m.issues[i].ID {
			t.Errorf("entry %d is for #%d, the issue is #%d", i, h.id, m.issues[i].ID)
		}
	}
}

func BenchmarkSearchKeystroke(b *testing.B) {
	m := press(bigModel(5000, 160, 50), "/", "thin")
	for b.Loop() {
		press(m, "g")
	}
}

func BenchmarkView(b *testing.B) {
	for _, view := range []string{"cards", "compact"} {
		m := bigModel(5000, 160, 50)
		if view == "compact" {
			m = press(m, "z")
		}
		b.Run(view, func(b *testing.B) {
			for b.Loop() {
				m.View()
			}
		})
	}
}
