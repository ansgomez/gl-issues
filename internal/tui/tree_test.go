package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gl-issues/internal/gl"
)

// treeIssue is issue id in project acme/platform/<project>, due in
// dueDays days (none if dueDays is 99), updated id hours ago.
func treeIssue(id int, project string, dueDays int) gl.Issue {
	is := gl.Issue{ID: id, IID: id, Title: fmt.Sprintf("Issue %d", id), State: "opened",
		UpdatedAt: time.Now().Add(-time.Duration(id) * time.Hour)}
	is.References.Full = fmt.Sprintf("acme/platform/%s#%d", project, id)
	if dueDays != 99 {
		is.DueDate = time.Now().AddDate(0, 0, dueDays).Format(time.DateOnly)
	}
	return is
}

// treeIssues: own projects of the base, web (own project and a
// subgroup), mobile two levels deeper, infra three levels deeper.
func treeIssues() []gl.Issue {
	return []gl.Issue{
		treeIssue(1, "api", -2),                     // base's own project, overdue
		treeIssue(2, "web/site", 3),                 // web's own project, due soon
		treeIssue(3, "web/frontend/app", 99),        // web/frontend
		treeIssue(4, "mobile/ios/app", 30),          // mobile/ios
		treeIssue(5, "infra/k8s/cluster/ops", -1),   // rolls up to infra (1) or infra/k8s (2)
		treeIssue(6, "web/frontend/app", 99),        // child of 2, other project
		treeIssue(7, "web/frontend/app", 99),        // child of 6: a task
		treeIssue(8, "mobile/ios/app", 99),          // orphan: its parent 99 is not listed
		treeIssue(9, "infra/k8s/cluster/ops", 99),   // in a cycle with 10
		treeIssue(10, "infra/k8s/cluster/ops", 99),  // in a cycle with 9
		treeIssue(11, "infra/k8s/cluster/ops", 200), // child of 5
	}
}

var treeParents = map[int]int{6: 2, 7: 6, 8: 99, 9: 10, 10: 9, 11: 5}

func treeOpts(depth int) Options {
	o := DefaultOptions()
	o.Refresh, o.Notify, o.Todos, o.TreeDepth, o.Sort = 0, false, false, depth, "project"
	return o
}

func treeModel(t *testing.T, depth, w, h int) Model {
	t.Helper()
	m := New(gl.Client{Host: "gitlab.example.edu"}, "acme/platform", treeOpts(depth))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	nm, _ = nm.Update(issuesMsg{state: "opened", issues: treeIssues()})
	nm, _ = nm.Update(hierarchyMsg{gen: nm.(Model).loadGen, parents: treeParents})
	m = nm.(Model)
	unfoldAll(&m) // the tree starts folded (TestTreeStartsFolded); most tests look inside
	return m
}

// unfoldAll opens every label and parent, as if f was pressed on each.
func unfoldAll(m *Model) {
	for changed := true; changed; {
		changed = false
		for _, r := range m.rows {
			if r.folded && !m.opened[m.rowKey(r)] {
				m.opened[m.rowKey(r)], changed = true, true
			}
		}
		m.rebuildView()
	}
}

// The tree starts with every label and parent folded: only the top-level
// labels show, with their counts. A filter shows its matches unfolded.
func TestTreeStartsFolded(t *testing.T) {
	m := New(gl.Client{Host: "gitlab.example.edu"}, "acme/platform", treeOpts(1))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	nm, _ = nm.Update(issuesMsg{state: "opened", issues: treeIssues()})
	nm, _ = nm.Update(hierarchyMsg{gen: nm.(Model).loadGen, parents: treeParents})
	m = nm.(Model)
	if len(m.rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range m.rows {
		if !r.isLabel() || !r.folded || r.level != 0 {
			t.Fatalf("not everything folded at start:\n%s", layout(m))
		}
	}
	m.cursor = 0
	m = press(m, "f")
	if !strings.Contains(layout(m), "#") {
		t.Errorf("f did not unfold the first label:\n%s", layout(m))
	}
	m.search = treeIssues()[0].Title
	m.rebuildView()
	if !strings.Contains(layout(m), fmt.Sprintf("#%d", treeIssues()[0].ID)) {
		t.Errorf("search match hidden in a folded label:\n%s", layout(m))
	}
}

// layout describes the rows: "[group]" for labels, "#id" for issues,
// indented two spaces per level, folded rows marked with "+".
func layout(m Model) string {
	var b strings.Builder
	for _, r := range m.rows {
		b.WriteString(strings.Repeat("  ", r.level))
		if r.isLabel() {
			b.WriteString("[" + r.group + "]")
		} else {
			fmt.Fprintf(&b, "#%d", m.issues[r.issue].ID)
		}
		if r.folded {
			b.WriteString("+")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestTreeDepth1(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	// Sorted by project, then newest number first, inside each group.
	want := `[]
  #1
[infra]
  #10
  #9
  #5
    #11
[mobile]
  #8
  #4
[web]
  #3
  #2
    #6
      #7
`
	if got := layout(m); got != want {
		t.Errorf("depth 1:\n%s\nwant:\n%s", got, want)
	}
}

func TestTreeDepth2(t *testing.T) {
	m := treeModel(t, 2, 120, 40)
	// web's own project (#2, with its children from web/frontend) comes
	// before web's subgroup; infra/k8s/cluster rolls up into infra/k8s;
	// #8's parent is not listed, so it stays in its own group.
	want := `[]
  #1
[infra]
  [infra/k8s]
    #10
    #9
    #5
      #11
[mobile]
  [mobile/ios]
    #8
    #4
[web]
  #2
    #6
      #7
  [web/frontend]
    #3
`
	if got := layout(m); got != want {
		t.Errorf("depth 2:\n%s\nwant:\n%s", got, want)
	}
	if m.labelName(m.rows[0]) != "acme/platform (own projects)" {
		t.Errorf("own label = %q", m.labelName(m.rows[0]))
	}
}

func TestTreeOwnProjectsBeforeSubgroups(t *testing.T) {
	m := New(gl.Client{}, "acme/platform", treeOpts(2))
	nm, _ := m.Update(issuesMsg{state: "opened", issues: []gl.Issue{
		treeIssue(1, "web/frontend/app", 99), treeIssue(2, "web/site", 99), treeIssue(3, "docs", 99),
	}})
	want := "[]\n  #3\n[web]\n  #2\n  [web/frontend]\n    #1\n"
	m = nm.(Model)
	unfoldAll(&m)
	if got := layout(m); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTreeCounts(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	got := map[string]string{}
	for _, r := range m.rows {
		if r.isLabel() {
			got[r.group] = r.counts.String()
		}
	}
	for g, want := range map[string]string{
		"":       "1 open · 1 overdue",
		"infra":  "4 open · 1 overdue",
		"mobile": "2 open",
		"web":    "4 open · 1 due soon",
	} {
		if got[g] != want {
			t.Errorf("%q counts = %q, want %q", g, got[g], want)
		}
	}
	closed := groupCounts{open: 0, closed: 2}
	if closed.String() != "2 closed" {
		t.Errorf("closed only: %q", closed.String())
	}
}

// Without parent links (flat mode or a failed fetch) nothing nests and
// every issue shows once.
func TestFlatAndNoParents(t *testing.T) {
	m := treeModel(t, 0, 120, 40)
	if len(m.rows) != len(m.issues) {
		t.Errorf("flat: %d rows for %d issues", len(m.rows), len(m.issues))
	}
	for _, r := range m.rows {
		if r.isLabel() || r.level != 0 {
			t.Fatalf("flat layout has a label or indentation:\n%s", layout(m))
		}
	}
	m = press(m, "l")
	unfoldAll(&m)
	if m.tree != 1 || !strings.Contains(layout(m), "    #11") {
		t.Errorf("l should switch to the tree:\n%s", layout(m))
	}
	if m = press(m, "l", "l"); m.tree != 0 {
		t.Errorf("l cycles back to flat: tree = %d", m.tree)
	}
}

// A failed GraphQL request leaves a working tree without nesting, with
// one quiet note, and no retry on background refreshes.
func TestTreeHierarchyFailure(t *testing.T) {
	log := fakeGlab(t, `echo '{"errors":[{"message":"Field workItems does not exist"}]}'`)
	m := New(gl.Client{Host: "gitlab.example.edu"}, "acme/platform", treeOpts(1))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = nm.(Model)
	m.loadGen = 1
	nm, _ = m.Update(issuesMsg{state: "opened", gen: 1, issues: treeIssues()})
	m = drain(t, nm.(Model), m.loadHierarchy())
	unfoldAll(&m)
	if !strings.Contains(m.flash, "child items unavailable") || m.flashBad {
		t.Errorf("flash = %q (bad %v)", m.flash, m.flashBad)
	}
	issues := 0
	for _, r := range m.rows {
		if !r.isLabel() {
			issues++
			if r.kids > 0 {
				t.Error("nested without parent links")
			}
		}
	}
	if issues != len(m.issues) {
		t.Errorf("%d issue rows, want %d", issues, len(m.issues))
	}
	if n := countCalls(t, log, "graphql"); n != 1 {
		t.Errorf("%d GraphQL calls, want 1", n)
	}
	// the note is shown once; background refreshes do not retry
	m.flash = ""
	m.loadGen++
	m = drain(t, m, m.loadHierarchy())
	if m.flash != "" {
		t.Errorf("second note: %q", m.flash)
	}
	tm, cmd := m.onRefreshTick(refreshTickMsg{gen: m.tickGen})
	m = drain(t, tm.(Model), cmd)
	if n := countCalls(t, log, "graphql"); n != 2 {
		t.Errorf("background refresh retried the failed query: %d GraphQL calls", n)
	}
}

// A failed refetch drops the links of the previous list: the tree says it
// is shown without child items, so none may stay nested on stale links.
func TestHierarchyFailureDropsStaleLinks(t *testing.T) {
	m := treeModel(t, 1, 90, 30)
	unfoldAll(&m)
	nested := 0
	for _, r := range m.rows {
		if r.kids > 0 {
			nested++
		}
	}
	if nested == 0 {
		t.Fatal("test needs a tree with parent links")
	}
	m.loadGen++ // a reload, whose parent fetch fails
	nm, _ := m.Update(hierarchyMsg{gen: m.loadGen, err: errors.New("boom")})
	m = nm.(Model)
	unfoldAll(&m)
	if len(m.parents) != 0 || m.hierLoaded {
		t.Errorf("stale parent links kept: %v (loaded %v)", m.parents, m.hierLoaded)
	}
	issues := 0
	for _, r := range m.rows {
		if r.kids > 0 {
			t.Errorf("row %q still nests children after the links failed", m.rowKey(r))
		}
		if !r.isLabel() {
			issues++
		}
	}
	if issues != len(m.issues) {
		t.Errorf("%d issue rows, want %d", issues, len(m.issues))
	}
	if !strings.Contains(m.flash, "tree shown without them") {
		t.Errorf("flash = %q", m.flash)
	}
}

// Tree mode issues one GraphQL request per load; flat mode none.
func TestHierarchyOnlyInTree(t *testing.T) {
	m := treeModel(t, 0, 90, 30)
	if cmd := m.loadHierarchy(); cmd != nil {
		t.Error("flat mode fetches parent links")
	}
	m.hierLoaded = false
	m.loadGen++
	tm, cmd := m.cycleTree()
	if cmd == nil {
		t.Fatal("switching to the tree should fetch the parent links")
	}
	m = tm.(Model)
	if _, cmd = m.cycleTree(); cmd != nil {
		t.Error("depth 2 fetched again for the same list")
	}
}

func TestTreeFolding(t *testing.T) {
	m := treeModel(t, 1, 120, 60)
	// fold the "web" label with enter: its rows go, its counts stay
	at := rowOf(m, "g:web")
	m.cursor = at
	m = press(m, "enter")
	if m.mode != modeList || !m.rows[at].folded || m.rows[at].counts.open != 4 || strings.Contains(layout(m), "#2") {
		t.Fatalf("fold label:\n%s", layout(m))
	}
	if strings.Contains(m.View(), "Issue 2") || !strings.Contains(m.View(), "4 open · 1 due soon") {
		t.Error("folded label: rows still shown or counts lost")
	}
	m = press(m, "f") // unfold
	if m.rows[at].folded || !strings.Contains(layout(m), "#7") {
		t.Fatalf("unfold label:\n%s", layout(m))
	}
	// fold parent #2 with f: "2 child items"
	m.cursor = rowOf(m, "i:2")
	m = press(m, "f")
	r := m.rows[m.cursor]
	if !r.folded || r.kids != 2 || strings.Contains(layout(m), "#6") {
		t.Fatalf("fold parent:\n%s", layout(m))
	}
	for _, compact := range []bool{false, true} {
		m.compact = compact
		if !strings.Contains(m.View(), "2 child items") {
			t.Errorf("compact %v: folded parent does not say how many child items", compact)
		}
	}
	// enter on a parent still opens it
	if d := press(m, "enter"); d.mode != modeDetail || d.current().ID != 2 {
		t.Error("enter on a parent issue should open details")
	}
	// f on a child folds its parent, cursor moves there
	m = press(m, "f") // unfold #2
	m.cursor = rowOf(m, "i:7")
	m = press(m, "f")
	if m.cursorKey() != "i:6" || !m.rows[m.cursor].folded {
		t.Errorf("f on a child: cursor %s\n%s", m.cursorKey(), layout(m))
	}
	// f in flat mode only explains
	if flat := press(treeModel(t, 0, 90, 30), "f"); !strings.Contains(flat.flash, "tree") {
		t.Errorf("f in flat mode: %q", flat.flash)
	}
}

func rowOf(m Model, key string) int {
	for i, r := range m.rows {
		if m.rowKey(r) == key {
			return i
		}
	}
	return -1
}

func TestKeysOnLabels(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.cursor = rowOf(m, "g:web")
	for _, k := range []string{"c", "a", "i", "d", "e", "x", "w", " "} {
		got := press(m, k)
		if got.form != nil || got.mode != modeList || len(got.selected) != 0 || len(got.busy) != 0 {
			t.Errorf("%q on a label acted", k)
		}
		if got.flash != notAnIssue {
			t.Errorf("%q on a label: flash %q", k, got.flash)
		}
	}
	if m.current() != nil || len(m.targets()) != 0 {
		t.Error("a label is not an issue")
	}
	// details: ←/→ skip labels
	m.cursor = rowOf(m, "i:1")
	d := press(m, "enter", "right")
	if d.mode != modeDetail || d.current() == nil || d.current().ID != 10 {
		t.Errorf("→ in details should skip the infra label, at %s", d.cursorKey())
	}
	if d = press(d, "left"); d.current() == nil || d.current().ID != 1 {
		t.Errorf("← in details: at %s", d.cursorKey())
	}
	// selection works on issues in the tree, and v selects them all
	m.cursor = rowOf(m, "i:11")
	if s := press(m, " "); !s.selected[11] {
		t.Error("space did not select in the tree")
	}
	if s := press(m, "v"); len(s.selected) != len(m.issues) {
		t.Errorf("v selected %d", len(s.selected))
	}
}

// Search shows matching issues under their ancestor labels; a child
// whose parent does not match moves up to its own group.
func TestTreeSearch(t *testing.T) {
	m := treeModel(t, 2, 120, 40)
	m = press(m, "/", "Issue 7", "enter")
	want := "[web]\n  [web/frontend]\n    #7\n"
	if got := layout(m); got != want {
		t.Errorf("search:\n%s\nwant:\n%s", got, want)
	}
	m = press(m, "esc")
	if !strings.Contains(layout(m), "      #7") {
		t.Errorf("esc should restore the tree:\n%s", layout(m))
	}
}

// A background refresh keeps fold state and the cursor's row, also when
// that row is a label.
func TestTreeRefreshKeepsFoldsAndCursor(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.cursor = rowOf(m, "i:2")
	m = press(m, "f")
	m.cursor = rowOf(m, "g:mobile")
	m = press(m, "f")
	m.cursor = rowOf(m, "g:infra")
	issues := append(treeIssues(), treeIssue(12, "aaa", 99)) // a new row above
	nm, _ := m.Update(issuesMsg{state: "opened", background: true, gen: m.loadGen, issues: issues})
	m = nm.(Model)
	if m.cursorKey() != "g:infra" {
		t.Errorf("cursor on %s, want the infra label", m.cursorKey())
	}
	if m.opened["i:2"] || m.opened["g:mobile"] || strings.Contains(layout(m), "#6") || strings.Contains(layout(m), "#4") {
		t.Errorf("folds lost:\n%s", layout(m))
	}
	m.cursor = rowOf(m, "i:5")
	nm, _ = m.Update(hierarchyMsg{gen: m.loadGen, parents: treeParents})
	if m = nm.(Model); m.cursorKey() != "i:5" {
		t.Errorf("new parent links moved the cursor to %s", m.cursorKey())
	}
	// fresh tags still show in the tree
	m.fresh[11] = "new"
	if !strings.Contains(m.View(), "new") {
		t.Error("fresh tag missing in the tree")
	}
}

func TestTreeWidths(t *testing.T) {
	for _, w := range []int{40, 60, 90, 120} {
		for _, depth := range []int{1, 2} {
			for _, compact := range []bool{false, true} {
				m := treeModel(t, depth, w, 30)
				m.compact = compact
				for ri := range m.rows {
					m.cursor = ri
					m.clamp()
					for _, l := range strings.Split(m.View(), "\n") {
						if lw := lipgloss.Width(l); lw > w {
							t.Fatalf("width %d depth %d compact %v: line %d wide: %q", w, depth, compact, lw, l)
						}
					}
				}
				m.cursor = rowOf(m, "g:web")
				m = press(m, "f")
				if lines := strings.Split(m.View(), "\n"); len(lines) > 30 {
					t.Errorf("width %d: %d lines", w, len(lines))
				}
			}
		}
	}
}

// The counts and labels never break on an error-free empty list.
func TestTreeEmpty(t *testing.T) {
	m := New(gl.Client{}, "acme/platform", treeOpts(2))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	nm, _ = nm.Update(issuesMsg{state: "opened"})
	m = nm.(Model)
	if len(m.rows) != 0 || m.current() != nil {
		t.Error("rows in an empty list")
	}
	_ = press(m, "f", "enter", "c", "j", "end")
	nm, _ = m.Update(hierarchyMsg{gen: m.loadGen, err: errors.New("boom")})
	if !strings.Contains(nm.(Model).flash, "boom") {
		t.Error("no note on failure")
	}
}

// A group whose issues all fall under one top-level label (no
// subgroups, or one) starts with that label unfolded, so the first screen
// shows issues rather than a single folded line. Once only: folding it
// again sticks across reloads.
func TestTreeLoneLabelStartsOpen(t *testing.T) {
	for _, tc := range []struct {
		name     string
		projects []string
		want     string
	}{
		{"no subgroups", []string{"api", "docs"}, "[]\n  #1\n  #2\n"},
		{"one subgroup", []string{"web/site", "web/frontend/app"}, "[web]\n  #2\n  #1\n"},
	} {
		var issues []gl.Issue
		for i, p := range tc.projects {
			issues = append(issues, treeIssue(i+1, p, 99))
		}
		m := New(gl.Client{Host: "gitlab.example.edu"}, "acme/platform", treeOpts(1))
		nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		nm, _ = nm.Update(issuesMsg{state: "opened", issues: issues})
		m = nm.(Model)
		if got := layout(m); got != tc.want {
			t.Errorf("%s: first screen\n%s\nwant\n%s", tc.name, got, tc.want)
		}
		if !strings.Contains(m.View(), "Issue 1") {
			t.Errorf("%s: issue not on the first screen:\n%s", tc.name, m.View())
		}
		m.cursor = 0
		m = press(m, "f")
		nm, _ = m.Update(issuesMsg{state: "opened", background: true, gen: m.loadGen, issues: issues})
		if m = nm.(Model); !m.rows[0].folded || len(m.rows) != 1 {
			t.Errorf("%s: a folded lone label reopened on reload:\n%s", tc.name, layout(m))
		}
	}
}

// b unfolds everything (labels and parents, hidden ones too), then folds
// it all again, keeping the cursor on its top-level label.
func TestTreeFoldAll(t *testing.T) {
	m := New(gl.Client{Host: "gitlab.example.edu"}, "acme/platform", treeOpts(2))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	nm, _ = nm.Update(issuesMsg{state: "opened", issues: treeIssues()})
	nm, _ = nm.Update(hierarchyMsg{gen: nm.(Model).loadGen, parents: treeParents})
	m = nm.(Model)
	folded := layout(m)
	want := treeModel(t, 2, 120, 60) // unfoldAll
	m = press(m, "b")
	if got := layout(m); got != layout(want) {
		t.Errorf("b should unfold everything:\n%s\nwant\n%s", got, layout(want))
	}
	m.cursor = rowOf(m, "i:7") // under web
	m = press(m, "b")
	if got := layout(m); got != folded || m.cursorKey() != "g:web" {
		t.Errorf("b again should fold everything, cursor on web (at %s):\n%s\nwant\n%s", m.cursorKey(), got, folded)
	}
	if flat := press(treeModel(t, 0, 90, 30), "b"); !strings.Contains(flat.flash, "tree") {
		t.Errorf("b in flat mode: %q", flat.flash)
	}
	if s := press(m, "m", "b"); !strings.Contains(s.flash, "filter") {
		t.Errorf("b while filtering: %q", s.flash)
	}
	if !strings.Contains(helpText(7), "b   unfold all / fold all") || !strings.Contains(strings.Join(treeKeys, " "), "b fold all") {
		t.Error("b missing from the help or the footer")
	}
}

// A group typed in another case than GitLab's canonical path still
// groups by subgroup: "Acme/Platform" matches "acme/platform/web/…".
func TestTreeGroupCaseInsensitive(t *testing.T) {
	want := layout(treeModel(t, 1, 120, 40))
	m := New(gl.Client{Host: "gitlab.example.edu"}, "Acme/Platform", treeOpts(1))
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	nm, _ = nm.Update(issuesMsg{state: "opened", issues: treeIssues()})
	nm, _ = nm.Update(hierarchyMsg{gen: nm.(Model).loadGen, parents: treeParents})
	m = nm.(Model)
	unfoldAll(&m)
	if got := layout(m); got != want {
		t.Errorf("Acme/Platform:\n%s\nwant as for acme/platform:\n%s", got, want)
	}
	if p, n := m.splitProject(treeIssues()[1]); p != "web" || n != "site" {
		t.Errorf("splitProject = %q, %q", p, n)
	}
}

// A selection folded out of sight still counts, but never silently: i
// asks first and every pop-up says how many targets are hidden. v selects
// only the issues shown.
func TestHiddenSelection(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.me = gl.User{ID: 1, Username: "me"}
	m.cursor = rowOf(m, "i:3")
	m = press(m, " ")
	m.cursor = rowOf(m, "g:web")
	m = press(m, "f") // folds web, hiding the selected #3
	m.cursor = rowOf(m, "i:1")
	if got := m.targets(); len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("targets %v", got)
	}
	a := press(m, "i")
	if a.form == nil || a.form.kind != formConfirm || len(a.busy) != 0 {
		t.Fatal("i on a hidden selection must ask")
	}
	if !strings.Contains(a.form.sub, "#3") || !strings.Contains(a.form.sub, "hidden in a folded group") {
		t.Errorf("question %q", a.form.sub)
	}
	for _, k := range []string{"a", "d", "x"} {
		if p := press(m, k); p.form == nil || !strings.Contains(p.form.sub+p.form.title, "hidden in a folded group") {
			t.Errorf("%s pop-up does not say the target is hidden", k)
		}
	}

	// v: only what is shown
	m = treeModel(t, 1, 120, 40)
	m.cursor = rowOf(m, "g:web")
	m = press(m, "f")
	shown := 0
	for _, r := range m.rows {
		if !r.isLabel() {
			shown++
		}
	}
	v := press(m, "v")
	if len(v.selected) != shown || v.selected[3] || !strings.Contains(v.flash, "not") {
		t.Errorf("v selected %d of %d shown (#3 %v), flash %q", len(v.selected), shown, v.selected[3], v.flash)
	}
	if v = press(v, "v"); len(v.selected) != 0 {
		t.Errorf("second v should clear, %d left", len(v.selected))
	}
}

// f and enter on a label do nothing while a search or filter shows
// everything unfolded: the fold would change out of sight.
func TestFoldRefusedWhileFiltering(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.cursor = rowOf(m, "g:web")
	m = press(m, "f") // fold web
	m = press(m, "/", "Issue", "enter")
	m.cursor = rowOf(m, "g:web")
	for _, k := range []string{"f", "enter"} {
		got := press(m, k)
		if !strings.Contains(got.flash, "esc clears it") || got.opened["g:web"] {
			t.Errorf("%s while searching: flash %q, web opened %v", k, got.flash, got.opened["g:web"])
		}
	}
	if m = press(m, "/", "esc"); m.rows[rowOf(m, "g:web")].folded != true {
		t.Error("web should still be folded after the search")
	}
}

// A failed parent-link fetch is retried by background refreshes after a
// back-off that doubles with each failure, not never again.
func TestHierarchyRetryBacksOff(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.opts.Refresh = time.Minute
	fail := func(m Model) Model {
		nm, _ := m.Update(hierarchyMsg{gen: m.loadGen, err: errors.New("boom")})
		return nm.(Model)
	}
	tick := func(m Model) Model { // a background refresh starting
		nm, _ := m.onRefreshTick(refreshTickMsg{gen: m.tickGen})
		return nm.(Model)
	}
	m = fail(m)
	if d := time.Until(m.hierRetry); d < hierBackoff-time.Minute || d > hierBackoff {
		t.Errorf("first back-off %v", d)
	}
	if r := tick(m); r.hierGen == r.loadGen {
		t.Error("a refresh within the back-off fetched the links")
	}
	m.hierRetry = time.Now().Add(-time.Second) // the back-off is over
	m = tick(m)
	if m.hierGen != m.loadGen {
		t.Error("a refresh after the back-off did not fetch the links")
	}
	m.refreshing = false
	m = fail(m)
	if d := time.Until(m.hierRetry); d < 2*hierBackoff-time.Minute {
		t.Errorf("second back-off %v, want doubled", d)
	}
	for range 10 {
		m = fail(m)
	}
	if d := time.Until(m.hierRetry); d > hierBackoffMax {
		t.Errorf("back-off %v beyond the cap", d)
	}
	nm, _ := m.Update(hierarchyMsg{gen: m.loadGen, parents: treeParents})
	if m = nm.(Model); m.hierFails != 0 || m.hierErr != nil {
		t.Error("success resets the back-off")
	}
}

// Two issues that name each other as parent (a cycle GitLab should not
// allow, but the data could hold) are both shown once, at the top level
// of their group, neither nested under the other. A child of one of them
// (whose parent chain never ends) is shown once at the top level too.
func TestTreeTwoNodeCycle(t *testing.T) {
	issues := []gl.Issue{treeIssue(1, "web/site", 99), treeIssue(2, "web/site", 99), treeIssue(3, "web/site", 99)}
	parents := map[int]int{1: 2, 2: 1, 3: 1}
	rows := buildRows(issues, []int{0, 1, 2}, "acme/platform", 1, parents, nil, true, time.Now(), 7)
	seen := map[int]int{}
	var b strings.Builder
	for _, r := range rows {
		if r.isLabel() {
			fmt.Fprintf(&b, "%*s[%s]\n", 2*r.level, "", r.group)
			continue
		}
		seen[issues[r.issue].ID]++
		fmt.Fprintf(&b, "%*s#%d\n", 2*r.level, "", issues[r.issue].ID)
	}
	if want := "[web]\n  #1\n  #2\n  #3\n"; b.String() != want {
		t.Errorf("layout:\n%s\nwant:\n%s", b.String(), want)
	}
	for id := 1; id <= 3; id++ {
		if seen[id] != 1 {
			t.Errorf("#%d shown %d times", id, seen[id])
		}
	}
}

// A chain that runs into a loop further up (#4 under #3 under #2, and #2
// and #1 name each other) is cut at every link: all of them are shown
// once, at the top level. A chain that ends (#7 under #6 under #5) nests
// all the way, and a second child of #6 (#8) reuses the answer for #6.
func TestTreeChainIntoCycle(t *testing.T) {
	var issues []gl.Issue
	for id := 1; id <= 8; id++ {
		issues = append(issues, treeIssue(id, "web/site", 99))
	}
	parents := map[int]int{1: 2, 2: 1, 3: 2, 4: 3, 6: 5, 7: 6, 8: 6}
	rows := buildRows(issues, []int{0, 1, 2, 3, 4, 5, 6, 7}, "acme/platform", 1, parents, nil, true, time.Now(), 7)
	var b strings.Builder
	for _, r := range rows {
		if r.isLabel() {
			fmt.Fprintf(&b, "%*s[%s]\n", 2*r.level, "", r.group)
			continue
		}
		fmt.Fprintf(&b, "%*s#%d\n", 2*r.level, "", issues[r.issue].ID)
	}
	if want := "[web]\n  #1\n  #2\n  #3\n  #4\n  #5\n    #6\n      #7\n      #8\n"; b.String() != want {
		t.Errorf("layout:\n%s\nwant:\n%s", b.String(), want)
	}
}

// One chain of n issues, each the child of the one before: every rebuild
// walks it once, not once per issue.
func BenchmarkBuildRowsChain(b *testing.B) {
	const n = 3000
	issues := make([]gl.Issue, n)
	view := make([]int, n)
	parents := map[int]int{}
	for i := range issues {
		issues[i] = treeIssue(i+1, "web/site", 99)
		view[i] = i
		if i > 0 {
			parents[i+1] = i
		}
	}
	for b.Loop() {
		buildRows(issues, view, "acme/platform", 1, parents, nil, true, time.Now(), 7)
	}
}

// A new issue under a folded label or parent is revealed, with the cursor
// on it (as docs/dashboard.md, "Creating an issue", says), like an issue the cursor was on across a
// refresh.
func TestNewIssueRevealedUnderFold(t *testing.T) {
	m := treeModel(t, 1, 120, 40)
	m.cursor = rowOf(m, "g:web")
	m = press(m, "f")
	if strings.Contains(layout(m), "#2") {
		t.Fatalf("setup: web not folded:\n%s", layout(m))
	}
	nm, _ := m.Update(updatedMsg{what: "create", issue: treeIssue(12, "web/site", 99)})
	m = nm.(Model)
	if m.cursorKey() != "i:12" || !m.opened["g:web"] {
		t.Errorf("cursor on %s, web opened %v:\n%s", m.cursorKey(), m.opened["g:web"], layout(m))
	}
	m.cursor = rowOf(m, "i:2")
	m = press(m, "f") // fold issue 2's children
	if strings.Contains(layout(m), "#6") {
		t.Fatalf("setup: issue 2 not folded:\n%s", layout(m))
	}
	m.parents[13] = 2
	nm, _ = m.Update(updatedMsg{what: "create", issue: treeIssue(13, "web/frontend/app", 99)})
	m = nm.(Model)
	if m.cursorKey() != "i:13" || !m.opened["i:2"] {
		t.Errorf("cursor on %s, issue 2 opened %v:\n%s", m.cursorKey(), m.opened["i:2"], layout(m))
	}
}
