package tui

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
)

// The tree layout (tree_depth 1 or 2, key l): the listed issues under one
// label per subgroup of the group, down to the chosen depth, with child
// items (tasks, child issues) nested under their parent issue. Group
// labels come from the issues' project paths; the parent links from one
// GraphQL query per load (gl.WorkItemParents), made only in tree mode.
// Without them the tree still works, just without nesting.

// listRow is one row of the list: an issue or, in the tree, a group label.
type listRow struct {
	issue  int    // index into Model.issues; -1 for a group label
	level  int    // indentation level
	group  string // label: group path relative to the base group; "" = the base's own projects
	counts groupCounts
	kids   int  // issue: child items nested under it, all levels
	folded bool // label or parent issue: its rows are hidden
}

func (r listRow) isLabel() bool { return r.issue < 0 }

// groupCounts are the counts shown on a group label, for every issue
// under it.
type groupCounts struct{ open, closed, overdue, soon int }

func (c *groupCounts) add(is gl.Issue, now time.Time, soonDays int) {
	if is.State == "closed" {
		c.closed++
		return
	}
	c.open++
	if d, ok := is.DaysLeft(now); ok {
		if d < 0 {
			c.overdue++
		} else if d <= soonDays {
			c.soon++
		}
	}
}

// String is "12 open · 3 overdue · 2 due soon"; parts that are zero are
// left out, except the open count when nothing is closed.
func (c groupCounts) String() string {
	var parts []string
	if c.open > 0 || c.closed == 0 {
		parts = append(parts, fmt.Sprintf("%d open", c.open))
	}
	if c.closed > 0 {
		parts = append(parts, fmt.Sprintf("%d closed", c.closed))
	}
	if c.overdue > 0 {
		parts = append(parts, fmt.Sprintf("%d overdue", c.overdue))
	}
	if c.soon > 0 {
		parts = append(parts, fmt.Sprintf("%d due soon", c.soon))
	}
	return strings.Join(parts, " · ")
}

// Fold keys: "g:<path>" for a group label ("g:" for the base's own
// projects), "i:<id>" for a parent issue. They also name the cursor's row
// across rebuilds.
func groupKey(path string) string { return "g:" + path }
func issueKey(id int) string      { return fmt.Sprintf("i:%d", id) }

// keyIssueID returns the issue ID of an issueKey.
func keyIssueID(k string) (int, bool) {
	var id int
	if _, err := fmt.Sscanf(k, "i:%d", &id); err != nil || issueKey(id) != k {
		return 0, false
	}
	return id, true
}

// reveal unfolds every label and parent above the issue with this ID, so
// its row is shown. It reports whether the issue passes the filters at
// all (only then can it have a row).
func (m *Model) reveal(id int) bool {
	if m.tree == 0 {
		return false // the flat list hides nothing
	}
	all := buildRows(m.issues, m.view, m.group, m.tree, m.parents, nil, true, time.Now(), m.opts.DueSoonDays)
	at := slices.IndexFunc(all, func(r listRow) bool { return !r.isLabel() && m.issues[r.issue].ID == id })
	if at < 0 {
		return false
	}
	for k, level := at-1, all[at].level; k >= 0 && level > 0; k-- {
		if all[k].level < level { // the next row up, one level out: an ancestor
			m.opened[m.rowKey(all[k])] = true
			level = all[k].level
		}
	}
	return true
}

// rowKey names a row independently of indices.
func (m Model) rowKey(r listRow) string {
	if r.isLabel() {
		return groupKey(r.group)
	}
	return issueKey(m.issues[r.issue].ID)
}

// groupPath returns the subgroup path, relative to base, of the project
// an issue is in: "" for a project directly under base.
func groupPath(is gl.Issue, base string) string {
	p := relPath(is.Project(), base) // case-insensitive, like GitLab paths
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// groupAt cuts a relative group path to its first depth segments.
func groupAt(path string, depth int) string {
	if path == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	return strings.Join(segs[:min(depth, len(segs))], "/")
}

// treeNode is a group label being built.
type treeNode struct {
	path string
	own  []int // top-level issue indices, in view order
	subs map[string]*treeNode
}

// buildRows lays out the visible issues (view: indices into issues,
// filtered and sorted). depth 0 is the flat list. Otherwise issues go
// under group labels depth levels deep, issues of deeper groups rolled
// up into their ancestor at that depth, and each issue whose parent
// (parents: child ID -> parent ID) is visible too is nested under it.
// Labels and parents start folded unless opened (or expandAll, while a
// filter is on); folded ones keep their counts but hide their rows.
func buildRows(issues []gl.Issue, view []int, base string, depth int, parents map[int]int,
	opened map[string]bool, expandAll bool, now time.Time, soonDays int) []listRow {
	if depth <= 0 {
		rows := make([]listRow, len(view))
		for k, i := range view {
			rows[k] = listRow{issue: i}
		}
		return rows
	}

	byID := make(map[int]int, len(view)) // issue ID -> index, visible issues only
	for _, i := range view {
		byID[issues[i].ID] = i
	}
	// chainEnds reports whether the parent chain from an issue ID reaches
	// an issue without a parent, rather than looping. The answer is kept
	// per ID, so a long chain is walked once, not once per issue on it
	// (the links can number thousands and rows are rebuilt on every key).
	const (
		unknown = iota
		walking // on the chain being walked: met again, it loops
		ends
		loops
	)
	state := map[int]int{}
	chainEnds := func(id int) bool {
		var path []int
		result := ends
		for state[id] != ends && state[id] != loops {
			if state[id] == walking {
				result = loops
				break
			}
			state[id] = walking
			path = append(path, id)
			next, ok := parents[id]
			if !ok {
				break
			}
			id = next
		}
		if state[id] == loops {
			result = loops
		}
		for _, id := range path {
			state[id] = result
		}
		return result == ends
	}
	// nestedUnder returns the visible parent an issue nests under, if any.
	// A parent chain that loops back is cut, so every issue is shown once.
	nestedUnder := func(i int) (int, bool) {
		pid, ok := parents[issues[i].ID]
		p, visible := byID[pid]
		if !ok || !visible || !chainEnds(issues[i].ID) {
			return 0, false
		}
		return p, true
	}
	children := map[int][]int{} // parent index -> child indices, in view order
	root := &treeNode{subs: map[string]*treeNode{}}
	var rootOwn []int
	for _, i := range view {
		if p, ok := nestedUnder(i); ok {
			children[p] = append(children[p], i)
			continue
		}
		g := groupAt(groupPath(issues[i], base), depth)
		if g == "" {
			rootOwn = append(rootOwn, i)
			continue
		}
		n := root
		segs := strings.Split(g, "/")
		for k := range segs {
			path := strings.Join(segs[:k+1], "/")
			sub := n.subs[path]
			if sub == nil {
				sub = &treeNode{path: path, subs: map[string]*treeNode{}}
				n.subs[path] = sub
			}
			n = sub
		}
		n.own = append(n.own, i)
	}

	var rows []listRow
	// issueRows appends an issue and, unless it is folded, its child
	// items, and returns how many issues that covers and their counts.
	var issueRows func(i, level int, hidden bool, c *groupCounts) int
	issueRows = func(i, level int, hidden bool, c *groupCounts) int {
		c.add(issues[i], now, soonDays)
		at := len(rows)
		isFolded := !expandAll && !opened[issueKey(issues[i].ID)] && len(children[i]) > 0
		if !hidden {
			rows = append(rows, listRow{issue: i, level: level, folded: isFolded})
		}
		kids := 0
		for _, ch := range children[i] {
			kids += issueRows(ch, level+1, hidden || isFolded, c)
		}
		if !hidden {
			rows[at].kids = kids
		}
		return kids + 1
	}
	// groupRows appends a label, its own issues, then its subgroups.
	var groupRows func(n *treeNode, level int, hidden bool) groupCounts
	groupRows = func(n *treeNode, level int, hidden bool) groupCounts {
		at := len(rows)
		isFolded := !expandAll && !opened[groupKey(n.path)]
		if !hidden {
			rows = append(rows, listRow{issue: -1, level: level, group: n.path, folded: isFolded})
		}
		var c groupCounts
		for _, i := range n.own {
			issueRows(i, level+1, hidden || isFolded, &c)
		}
		for _, sub := range sortedSubs(n) {
			sc := groupRows(sub, level+1, hidden || isFolded)
			c.open, c.closed, c.overdue, c.soon = c.open+sc.open, c.closed+sc.closed, c.overdue+sc.overdue, c.soon+sc.soon
		}
		if !hidden {
			rows[at].counts = c
		}
		return c
	}
	if len(rootOwn) > 0 {
		groupRows(&treeNode{own: rootOwn}, 0, false)
	}
	for _, n := range sortedSubs(root) {
		groupRows(n, 0, false)
	}
	return rows
}

func sortedSubs(n *treeNode) []*treeNode {
	out := make([]*treeNode, 0, len(n.subs))
	for _, s := range n.subs {
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].path < out[b].path })
	return out
}

// labelName is what a group label says: the path relative to the base,
// or "<base> (own projects)".
func (m Model) labelName(r listRow) string {
	if r.group == "" {
		return m.group + " (own projects)"
	}
	return r.group
}

// indent is the indentation of a row, capped so narrow terminals keep
// room for the text.
func (m Model) indent(r listRow) string {
	return strings.Repeat(" ", min(2*r.level, max(m.width/5, 2)))
}

// foldMark is the fold state of a label or parent issue; other issues
// in the tree get the same room, so titles line up.
func (m Model) foldMark(r listRow) string {
	switch {
	case r.isLabel() || r.kids > 0:
		if r.folded {
			return "▹ "
		}
		return "▾ "
	case m.tree > 0:
		return "  "
	}
	return ""
}

// --- keys ---

// cycleTree switches flat -> 1 level -> 2 levels -> flat (key l), and
// fetches the parent links if the tree needs them.
func (m Model) cycleTree() (tea.Model, tea.Cmd) {
	m.tree = (m.tree + 1) % 3
	m.rebuildView()
	if m.tree == 0 {
		m.setFlash("layout: flat list", false)
		return m, nil
	}
	m.setFlash(fmt.Sprintf("layout: tree, %s · f folds", plural(m.tree, "group level", "group levels")), false)
	if !m.hierLoaded && m.hierGen != m.loadGen && len(m.issues) > 0 {
		return m, m.loadHierarchy()
	}
	return m, nil
}

// toggleFold folds or unfolds the label or parent issue under the
// cursor; on another issue, the label or parent it is under (the cursor
// moves there).
func (m *Model) toggleFold() {
	if m.tree == 0 {
		m.setFlash("f folds groups in the tree layout (l)", false)
		return
	}
	if m.filtering() {
		m.setFlash("a search or filter shows everything unfolded · esc clears it", false)
		return
	}
	if m.cursor >= len(m.rows) {
		return
	}
	at := m.cursor
	if r := m.rows[at]; !r.isLabel() && r.kids == 0 {
		at--
		for at >= 0 && m.rows[at].level >= r.level {
			at--
		}
		if at < 0 || r.level == 0 {
			m.setFlash("nothing to fold here", false)
			return
		}
	}
	k := m.rowKey(m.rows[at])
	if m.rows[at].folded {
		m.opened[k] = true
	} else {
		delete(m.opened, k)
	}
	m.rebuildViewKeep(k)
}

// toggleAllFolds unfolds every label and parent if any is folded, else
// folds them all (key b). Folding keeps the cursor on its top-level
// label.
func (m *Model) toggleAllFolds() {
	if m.tree == 0 {
		m.setFlash("b folds groups in the tree layout (l)", false)
		return
	}
	if m.filtering() {
		m.setFlash("a search or filter shows everything unfolded · esc clears it", false)
		return
	}
	m.foldsSet = true
	if slices.ContainsFunc(m.rows, func(r listRow) bool { return r.folded }) {
		all := buildRows(m.issues, m.view, m.group, m.tree, m.parents, nil, true, time.Now(), m.opts.DueSoonDays)
		for _, r := range all {
			if r.isLabel() || r.kids > 0 {
				m.opened[m.rowKey(r)] = true
			}
		}
		m.setFlash("everything unfolded · b folds it all", false)
		m.rebuildView()
		return
	}
	top := m.cursorKey()
	for at := min(m.cursor, len(m.rows)-1); at >= 0; at-- {
		if m.rows[at].level == 0 {
			top = m.rowKey(m.rows[at])
			break
		}
	}
	m.opened = map[string]bool{}
	m.setFlash("everything folded · b unfolds it all", false)
	m.rebuildViewKeep(top)
}

// openLoneLabel unfolds the only top-level label the first time the tree
// is laid out with issues, so a group without subgroups (or with one)
// starts with its issues in view rather than one folded line. It reports
// whether it unfolded anything; after the first layout it does nothing.
func (m *Model) openLoneLabel() bool {
	if m.foldsSet || m.tree == 0 || len(m.rows) == 0 {
		return false
	}
	m.foldsSet = true
	var top []listRow
	for _, r := range m.rows {
		if r.level == 0 {
			top = append(top, r)
		}
	}
	if len(top) != 1 || !top[0].folded {
		return false
	}
	m.opened[m.rowKey(top[0])] = true
	return true
}

// filtering reports whether a search or filter is on; the tree then
// shows its matches unfolded.
func (m Model) filtering() bool { return m.search != "" || m.mine || m.unassigned || m.attention }

// --- hierarchy ---

type hierarchyMsg struct {
	gen       int // loadGen when the fetch started
	parents   map[int]int
	truncated bool
	err       error
}

// loadHierarchy fetches the parent links for the current list, only in
// tree mode.
func (m *Model) loadHierarchy() tea.Cmd {
	if m.tree == 0 {
		m.hierLoaded = false // the links will be stale; fetch again in the tree
		return nil
	}
	m.hierGen = m.loadGen
	c, group, which, gen := m.client, m.group, states[m.state], m.loadGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		parents, trunc, err := c.WorkItemParents(ctx, group, which)
		return hierarchyMsg{gen: gen, parents: parents, truncated: trunc, err: err}
	}
}

// applyHierarchy installs fetched parent links. A failure drops the links
// of the previous list (they may be stale), so the tree is shown without
// nesting, and says so once per session.
func (m *Model) applyHierarchy(msg hierarchyMsg) {
	if msg.gen != m.loadGen {
		return // a newer load fetches its own
	}
	m.hierErr = msg.err
	if msg.err != nil {
		m.hierFails++
		wait := hierBackoff << min(m.hierFails-1, 8)
		m.hierRetry = time.Now().Add(min(wait, hierBackoffMax))
		if !m.hierNoted {
			m.hierNoted = true
			m.setFlash("child items unavailable, tree shown without them: "+msg.err.Error(), false)
		}
		if m.parents != nil {
			m.parents, m.hierLoaded = nil, false
			m.relayout(m.cursorKey(), m.cursorName(), true)
			m.refreshDetail()
		}
		return
	}
	m.parents, m.hierLoaded, m.hierFails = msg.parents, true, 0
	if msg.truncated && !m.hierNoted {
		m.hierNoted = true
		m.setFlash(fmt.Sprintf("child items: only the first %d work items checked", gl.MaxParentPages*100), false)
	}
	m.relayout(m.cursorKey(), m.cursorName(), true)
	m.refreshDetail()
}
