package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"gl-issues/internal/gl"
)

// Rendering of the list, the detail pane, the title line and the key
// footer.

// View renders the whole screen: help, or the list/detail with any
// pop-up drawn over it.
func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.showHelp {
		lines := strings.Split(m.titleLine()+"\n"+helpText(m.opts.DueSoonDays), "\n")
		lines = lines[:min(len(lines), m.height)]
		for i := range lines {
			lines[i] = clip(lines[i], m.width)
		}
		return strings.Join(lines, "\n")
	}
	out := m.baseView()
	if m.form != nil {
		out = overlay(out, m.renderForm(), m.width, m.height)
	}
	return out
}

func (m Model) baseView() string {
	if m.mode == modeDetail {
		return m.titleLine() + "\n" + m.vp.View() + "\n" + m.bottomLines()
	}

	var b strings.Builder
	b.WriteString(m.titleLine())
	b.WriteByte('\n')

	const assignW, dueW, updW, notesW = 18, 12, 4, 4
	refW := m.stats.refW // counted when the view was built, not per frame
	titleW := max(m.width-(3+refW+1+1+assignW+1+dueW+1+notesW+1+updW), 10)

	if m.compact {
		b.WriteString(styleHeader.Render(truncate("   "+fit("ISSUE", refW)+" "+fit("TITLE", titleW)+" "+
			fit("ASSIGNEE", assignW)+" "+fit("DUE", dueW)+" "+fit("CMT", notesW)+fit("UPD", updW), m.width)))
	}
	b.WriteByte('\n')

	rowLines := 3 // two-line card plus a blank separator
	if m.compact {
		rowLines = 1
	}
	h := m.listHeight()
	if len(m.view) == 0 && !m.loading {
		h = 0 // the message below fills the list area instead
	}
	now := time.Now()
	for row := 0; row < h; row++ {
		ri := m.offset + row
		if ri < len(m.rows) {
			switch {
			case m.rows[ri].isLabel():
				b.WriteString(m.renderLabel(ri))
			case m.compact:
				row := m.renderRow(ri, now, refW, titleW, assignW, dueW, notesW, updW)
				if ri == m.cursor {
					b.WriteString(highlight(row, m.width))
				} else {
					b.WriteString(clip(row, m.width))
				}
			default:
				b.WriteString(m.renderCard(ri, now) + "\n")
			}
		} else {
			b.WriteString(strings.Repeat("\n", rowLines-1))
		}
		b.WriteByte('\n')
	}
	// pad odd leftover line so the footer stays at the bottom
	var empty []string
	if h == 0 {
		empty = m.emptyListLines()
	}
	for i := h * rowLines; i < max(m.height-m.chromeLines(), 1); i++ {
		if i < len(empty) {
			b.WriteString(empty[i])
		}
		b.WriteByte('\n')
	}

	b.WriteString(m.preview())
	b.WriteByte('\n')
	b.WriteString(m.bottomLines())
	return b.String()
}

// emptyListLines is what the list area shows when no issue is listed. A
// load error stays here, in full, until a load succeeds: the flash line
// clears on the next key.
func (m Model) emptyListLines() []string {
	const indent = "   "
	switch {
	case m.loadErr != nil && len(m.issues) == 0:
		wrapped := lipgloss.NewStyle().Width(max(m.width-len(indent), 10)).Render("could not load issues: " + m.loadErr.Error())
		var out []string
		for _, l := range strings.Split(wrapped, "\n") {
			out = append(out, clip(styleBad.Render(indent+strings.TrimRight(l, " ")), m.width))
		}
		return append(out, clip(styleDim.Render(indent+"r to retry · run gl-issues doctor to check the setup"), m.width))
	case len(m.issues) == 0:
		return []string{clip(styleDim.Render(indent+"no "+states[m.state]+" issues in "+m.group), m.width)}
	}
	return []string{clip(styleDim.Render(indent+"no issues match the filters · esc to clear"), m.width)}
}

// bottomLines renders the input/confirm/flash line and the help line.
func (m Model) bottomLines() string {
	var line string
	switch {
	case m.inputKind == inputSearch:
		line = m.input.View()
	case m.flash != "":
		if m.flashBad {
			line = styleBad.Render(truncate(m.flash, m.width))
		} else {
			line = styleGood.Render(truncate(m.flash, m.width))
		}
	}
	return clip(line, m.width) + "\n" + strings.Join(renderFooter(m.footerGroups(), m.width), "\n")
}

// Footer key groups: "key label" pairs, all lower-case, no shift needed.
// Each group gets its own labelled line(s): general list controls apart
// from the actions on an issue.
type keyGroup struct {
	label string
	items []string
}

var (
	listGeneral = []string{
		"↑↓ move", "pgup/pgdn page", "g/end first/last", "enter open", "n new", "/ search", "m mine", "o due soon",
		"u unassigned", "s sort", "t open/closed", "z compact", "l tree", "r reload", "h help", "q quit",
	}
	treeKeys     = []string{"f fold", "b fold all"}
	issueActions = []string{
		"c comment", "a assign", "i assign me", "d due", "e edit", "x close", "w web",
	}
	// With a selection: what acts on every selected issue, and what still
	// acts on the issue under the cursor only.
	bulkActions   = []string{"a assign", "i assign me", "d due", "x close", "space select", "v select all", "esc clear"}
	cursorActions = []string{"c comment", "e edit", "w web"}
	detailNav     = []string{"↑↓ scroll", "pgup/pgdn page", "g/end top/bottom", "←→ prev/next issue", "n new", "h help", "esc back"}
)

func (m Model) footerGroups() []keyGroup {
	if m.mode == modeDetail {
		return []keyGroup{{"Issue", issueActions}, {"View", detailNav}}
	}
	list := listGeneral
	if m.tree > 0 {
		list = append(append([]string{}, listGeneral...), treeKeys...)
	}
	if m.selectedShown() > 0 { // bulkActions has esc clear
		return []keyGroup{{"List", list}, {"Selected", bulkActions}, {"Issue", cursorActions}}
	}
	if m.filtering() {
		list = append(append([]string{}, list...), "esc clear")
	}
	actions := append(append([]string{}, issueActions...), "space select", "v select all")
	return []keyGroup{{"List", list}, {"Issue", actions}}
}

const keyLabelW = 10 // width of the group label column

// renderFooter lays out the key groups, wrapping each to the width.
func renderFooter(groups []keyGroup, width int) []string {
	var lines []string
	for _, g := range groups {
		for i, l := range wrapKeys(g.items, max(width-keyLabelW, 10)) {
			label := strings.Repeat(" ", keyLabelW)
			if i == 0 {
				label = styleFooterLabel.Render(fit(g.label, keyLabelW))
			}
			lines = append(lines, clip(label+l, width))
		}
	}
	return lines
}

// wrapKeys lays out "key label" items over as many lines as needed.
func wrapKeys(items []string, width int) []string {
	var lines []string
	var cur string
	curW := 0
	for _, it := range items {
		k, label, _ := strings.Cut(it, " ")
		s := styleKey.Render(k) + " " + styleDim.Render(label)
		w := lipgloss.Width(k) + 1 + lipgloss.Width(label)
		if curW > 0 && curW+3+w > width {
			lines = append(lines, cur)
			cur, curW = "", 0
		}
		if curW > 0 {
			cur += styleDim.Render(" · ")
			curW += 3
		}
		cur += s
		curW += w
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// footerKey is what decides the footer's layout (see footerGroups).
type footerKey struct {
	width    int
	mode     mode
	selected bool
	tree     bool
	filtered bool
}

// footerLines is the number of lines of the key footer, cached per
// width and footer variant: listHeight needs it on every key and render.
func (m Model) footerLines() int {
	k := footerKey{max(m.width, 1), m.mode, m.selectedShown() > 0, m.tree > 0, m.filtering()}
	if n, ok := m.footerN[k]; ok {
		return n
	}
	n := len(renderFooter(m.footerGroups(), k.width))
	if m.footerN != nil {
		m.footerN[k] = n
	}
	return n
}

// detailHeight is the height of the scrolling detail pane: everything but
// the title, flash line and key footer.
func (m Model) detailHeight() int {
	d := m
	d.mode = modeDetail
	return max(m.height-2-d.footerLines(), 1)
}

// chromeLines counts the lines around the list: title, header/spacer,
// preview, flash/search line and the wrapped key footer.
func (m Model) chromeLines() int {
	return 4 + m.footerLines()
}

func (m Model) titleLine() string {
	now := time.Now()
	st := m.titleStats(now)
	summary := fmt.Sprintf("%d %s", st.listed, states[m.state])
	if m.loading {
		summary = m.spin.View() + " loading"
	} else {
		if st.shown != st.listed {
			summary = fmt.Sprintf("%d/%d %s", st.shown, st.listed, states[m.state])
		}
		summary += fmt.Sprintf(" · %d mine · %d unassigned", st.mine, st.unassigned)
		if st.overdue > 0 {
			summary += " · " + styleBad.Render(fmt.Sprintf("%d overdue", st.overdue))
		}
		if st.soon > 0 {
			summary += " · " + styleWarn.Render(fmt.Sprintf("%d due ≤%dd", st.soon, m.opts.DueSoonDays))
		}
	}
	if t := m.todoTitle(); t != "" {
		summary += " · " + t
	}
	if len(m.busy) > 0 {
		summary += fmt.Sprintf(" · %s %d running", m.spin.View(), len(m.busy))
	}
	var filters []string
	if m.mine {
		filters = append(filters, "mine")
	}
	if m.unassigned {
		filters = append(filters, "unassigned")
	}
	if m.attention {
		filters = append(filters, "due soon")
	}
	if m.search != "" {
		filters = append(filters, "/"+m.search)
	}
	if n := m.selectedShown(); n > 0 {
		filters = append(filters, fmt.Sprintf("%d selected", n))
	}
	if len(filters) > 0 {
		summary += styleWarn.Render("  [" + strings.Join(filters, " · ") + "]")
	}
	summary += styleDim.Render("  sort:" + sortNames[m.sortBy])
	if m.tree > 0 {
		summary += styleDim.Render(fmt.Sprintf(" tree:%d", m.tree))
	}
	if f := m.freshness(time.Now()); f != "" {
		summary += styleDim.Render("  ") + styleDim.Render(f)
	}
	return clip(styleTitle.Render("gl-issues")+"  "+m.group+styleDim.Render(" @ "+m.hostName())+"  "+summary, m.width)
}

func (m Model) hostName() string {
	if m.client.Host == "" {
		return "glab default host"
	}
	return m.client.Host
}

// shortRef drops the group prefix and shortens deep paths:
// "team/platform/a/b/proj#3" -> "platform/…/b/proj#3".
func (m Model) shortRef(is gl.Issue) string {
	parent, name := m.splitProject(is)
	tail := fmt.Sprintf("%s#%d", name, is.IID)
	if parent == "" {
		return tail
	}
	return shortPath(parent+"/"+tail, 32)
}

func (m Model) renderRow(vi int, now time.Time, refW, titleW, assignW, dueW, notesW, updW int) string {
	r := m.rows[vi]
	is := m.issues[r.issue]
	cur, sel := " ", " "
	if vi == m.cursor {
		cur = "▸"
	}
	switch {
	case m.selected[is.ID]:
		sel = "●"
	case m.fresh[is.ID] == "new":
		sel = styleGood.Render("+")
	case m.fresh[is.ID] == "updated":
		sel = styleWarn.Render("~")
	}

	ref := fitLeft(m.shortRef(is), refW)
	tag := todoTag(m.issueTodos(is))
	if lipgloss.Width(tag) > titleW/2 {
		tag = todoTag([]string{""}) // no room for the reason
	}
	// In the tree the row starts indented, with the fold mark of a
	// parent; the title column gives up that room so the columns after
	// it stay aligned. A folded parent says how many child items it hides.
	indent := m.indent(r) + m.foldMark(r)
	titleW = max(titleW-lipgloss.Width(indent), 4)
	var kids string
	if r.folded {
		kids = " " + plural(r.kids, "child item", "child items")
		if len(kids) > titleW/2 {
			kids = fmt.Sprintf(" +%d", r.kids)
		}
	}
	title := fit(is.Title, max(titleW-lipgloss.Width(tag)-len(kids), 1))
	fresh, isFresh := freshStyle(m.fresh[is.ID])
	isFresh = isFresh && is.State != "closed"
	switch {
	case is.State == "closed":
		title = styleDim.Render(title)
	case isFresh:
		title = fresh.Render(title)
	case vi == m.cursor:
		title = styleCursor.Render(title)
	}
	switch {
	case isFresh:
		ref = fresh.Render(ref)
	case vi == m.cursor:
		ref = styleCursor.Render(ref)
	default:
		ref = styleDim.Render(ref)
	}

	assignee := fit(strings.Join(is.AssigneeNames(), ","), assignW)
	if len(is.Assignees) == 0 {
		assignee = styleWarn.Render(fit("—", assignW))
	}

	var due string
	if busy := m.busy[is.ID]; busy != "" {
		due = styleDim.Render(fit(m.spin.View()+" "+busy, dueW))
	} else {
		due = dueCell(is, now, dueW, m.opts.DueSoonDays)
	}
	notes := styleDim.Render(fit(fmt.Sprintf("%d", is.Notes), notesW))
	if is.Notes == 0 {
		notes = styleDim.Render(fit("", notesW))
	}
	upd := styleDim.Render(fit(ago(is.UpdatedAt, now), updW))
	if isFresh {
		upd = fresh.Render(fit(ago(is.UpdatedAt, now), updW))
	}
	title += styleDim.Render(kids) // at the end of the title column
	return cur + sel + " " + styleDim.Render(indent) + ref + " " + tag + title + " " + assignee + " " + due + " " + notes + upd
}

// Due dates are shown in three places, in one colour rule (dueStyle):
// the compact column (dueCell), the card badge (dueBadge) and the
// details (dueDetail).

// dueStyle is the colour of a due date d days away (ok false: none):
// dim without one or once the issue is closed, red overdue, yellow
// within soonDays, green otherwise.
func dueStyle(is gl.Issue, d int, ok bool, soonDays int) lipgloss.Style {
	switch {
	case !ok, is.State == "closed":
		return styleDim
	case d < 0:
		return styleBad
	case d <= soonDays:
		return styleWarn
	}
	return styleGood
}

// dueRelative says how far off a due date d days away is: "today",
// "tomorrow", "in 9d", "3d overdue"; long spells the days out.
func dueRelative(d int, long bool) string {
	switch {
	case d == 0:
		return "today"
	case d == 1:
		return "tomorrow"
	case d < 0 && long:
		return fmt.Sprintf("%d days overdue", -d)
	case d < 0:
		return fmt.Sprintf("%dd overdue", -d)
	case long:
		return fmt.Sprintf("in %d days", d)
	}
	return fmt.Sprintf("in %dd", d)
}

// dueCell is the due column of a compact row: the date alone, w wide.
func dueCell(is gl.Issue, now time.Time, w, soonDays int) string {
	d, ok := is.DaysLeft(now)
	s := "—"
	if ok {
		s = is.DueDate
	}
	return dueStyle(is, d, ok, soonDays).Render(fit(s, w))
}

// dueBadge is the short due-date label at the end of a card:
// "2026-10-12 · in 9d".
func dueBadge(is gl.Issue, now time.Time, soonDays int) string {
	d, ok := is.DaysLeft(now)
	s := "no due date"
	if ok {
		s = is.DueDate + " · " + dueRelative(d, false)
	}
	return dueStyle(is, d, ok, soonDays).Render(s)
}

// dueDetail is the due field of the details: "2026-10-12 (in 9 days)".
func dueDetail(is gl.Issue, now time.Time, soonDays int) string {
	d, ok := is.DaysLeft(now)
	s := "none"
	if ok {
		s = is.DueDate + " (" + dueRelative(d, true) + ")"
	}
	return dueStyle(is, d, ok, soonDays).Render(s)
}

// previewBytes bounds how much of a description the preview reads.
const previewBytes = 4 << 10

// preview is a one-line summary of the issue under the cursor.
func (m Model) preview() string {
	is := m.current()
	if is == nil {
		return styleDim.Render(strings.Repeat("─", max(m.width, 1)))
	}
	parts := []string{"by " + is.Author.Username}
	if len(is.Labels) > 0 {
		parts = append(parts, styleLabel.Render(strings.Join(is.Labels, ", ")))
	}
	desc := collapseSpace(is.Description, previewBytes) // one row: a few KB is more than clip can show
	if desc == "" {
		desc = styleWarn.Render("(no description)")
	}
	parts = append(parts, desc)
	return clip(styleDim.Render("─ ")+strings.Join(parts, styleDim.Render(" · ")), m.width)
}

// refreshDetail re-renders the detail viewport for the current issue.
func (m *Model) refreshDetail() {
	if m.mode != modeDetail {
		return
	}
	is := m.current()
	if is == nil {
		m.mode = modeList
		m.clamp()
		return
	}
	w := max(m.width-2, 20)
	wrap := lipgloss.NewStyle().Width(w)
	now := time.Now()

	var b strings.Builder
	open := styleGood.Render("open")
	if is.State == "closed" {
		open = styleDim.Render("closed")
	}
	b.WriteString(styleCursor.Render(wrap.Render(is.Title)) + "\n")
	b.WriteString(styleDim.Render(is.Ref()) + "  " + open + "\n\n")
	assignees := strings.Join(is.AssigneeNames(), ", ")
	if assignees == "" {
		assignees = styleWarn.Render("unassigned")
	}
	field := func(k, v string) { b.WriteString(styleDim.Render(fit(k, 11)) + v + "\n") }
	field("assignees", assignees)
	field("due", dueDetail(*is, now, m.opts.DueSoonDays))
	if len(is.Labels) > 0 {
		field("labels", styleLabel.Render(strings.Join(is.Labels, ", ")))
	}
	if todos := m.issueTodos(*is); len(todos) > 0 {
		field("to-do", styleWarn.Render(strings.Join(todos, ", ")))
	}
	field("author", is.Author.Username)
	field("created", is.CreatedAt.Local().Format("2006-01-02 15:04"))
	field("updated", is.UpdatedAt.Local().Format("2006-01-02 15:04")+" ("+ago(is.UpdatedAt, now)+" ago)")
	field("url", is.WebURL)

	b.WriteString("\n" + styleHeader.Render("Description") + "\n")
	if strings.TrimSpace(is.Description) == "" {
		b.WriteString(styleWarn.Render("(none — press e to write one)") + "\n")
	} else {
		b.WriteString(wrap.Render(is.Description) + "\n")
	}

	notes, loaded := m.notes[is.ID]
	b.WriteString("\n" + styleHeader.Render(fmt.Sprintf("Comments (%d)", is.Notes)) + "\n")
	switch {
	case m.notesErr[is.ID] != nil:
		b.WriteString(styleBad.Render(m.notesErr[is.ID].Error()) + "\n")
	case !loaded:
		b.WriteString(styleDim.Render("loading…") + "\n")
	case len(notes) == 0:
		b.WriteString(styleDim.Render("(none — press c to comment)") + "\n")
	}
	for _, n := range notes {
		b.WriteString("\n" + styleTitle.Render(n.Author.Username) + styleDim.Render("  "+n.CreatedAt.Local().Format("2006-01-02 15:04")) + "\n")
		b.WriteString(wrap.Render(n.Body) + "\n")
	}
	m.vp.SetContent(b.String())
}

// splitProject splits an issue's project path, relative to the group,
// into its parent path and project name: "people/bob/notes" ->
// ("people/bob", "notes").
func (m Model) splitProject(is gl.Issue) (parent, name string) {
	p := relPath(is.Project(), m.group)
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[:i], p[i+1:]
	}
	return "", p
}

// renderCard renders one issue as two lines:
//
//	▌● Title of the issue                          2026-10-12 · in 9d
//	▌  #3  notes  people/bob · @alice, @bob · 2 comments · 3h ago · bug
func (m Model) renderCard(vi int, now time.Time) string {
	r := m.rows[vi]
	is := m.issues[r.issue]
	indent := m.indent(r)
	bar, sel := "  ", " "
	if vi == m.cursor {
		bar = styleTitle.Render("▌ ")
	}
	if m.selected[is.ID] {
		sel = styleTitle.Render("●")
	}

	badge := dueBadge(is, now, m.opts.DueSoonDays)
	if busy := m.busy[is.ID]; busy != "" {
		badge = styleDim.Render(m.spin.View() + " " + busy)
	}
	tag := styleDim.Render(m.foldMark(r)) + freshTag(m.fresh[is.ID]) + todoTag(m.issueTodos(is))
	titleW := max(m.width-3-2-len(indent)-lipgloss.Width(badge)-lipgloss.Width(tag), 10)
	title := fit(is.Title, titleW)
	fresh, isFresh := freshStyle(m.fresh[is.ID])
	isFresh = isFresh && is.State != "closed"
	switch {
	case is.State == "closed":
		title = styleDim.Render(title)
	case isFresh:
		title = fresh.Render(title)
	case vi == m.cursor:
		title = styleCursor.Render(title)
	}
	line1 := bar + sel + indent + tag + title + "  " + badge

	parent, name := m.splitProject(is)
	num := styleDim.Render(fmt.Sprintf("#%d", is.IID))
	if isFresh {
		num = fresh.Render(fmt.Sprintf("#%d", is.IID))
	}
	meta := num + "  " + styleProject.Render(name)
	if parent != "" {
		meta += "  " + styleDim.Render(shortPath(parent, max(m.width/3, 16)))
	}
	var who string
	if len(is.Assignees) == 0 {
		who = styleWarn.Render("unassigned")
	} else {
		names := is.AssigneeNames()
		for i := range names {
			names[i] = "@" + names[i]
		}
		who = strings.Join(names, ", ")
	}
	parts := []string{meta, who}
	if r.folded {
		parts = append(parts, styleWarn.Render(plural(r.kids, "child item", "child items")))
	}
	switch is.Notes {
	case 0:
	case 1:
		parts = append(parts, styleDim.Render("1 comment"))
	default:
		parts = append(parts, styleDim.Render(fmt.Sprintf("%d comments", is.Notes)))
	}
	updated := styleDim.Render("updated " + ago(is.UpdatedAt, now) + " ago")
	if isFresh {
		updated = fresh.Render("updated " + ago(is.UpdatedAt, now) + " ago")
	}
	parts = append(parts, updated)
	if len(is.Labels) > 0 {
		parts = append(parts, styleLabel.Render(strings.Join(is.Labels, ", ")))
	}
	line2 := bar + " " + indent + strings.Join(parts, styleDim.Render(" · "))
	if vi == m.cursor {
		return highlight(line1, m.width) + "\n" + highlight(line2, m.width)
	}
	return clip(line1, m.width) + "\n" + clip(line2, m.width)
}

// freshTag marks issues that a reload found new or changed, until you
// open them.
func freshTag(kind string) string {
	if st, ok := freshStyle(kind); ok {
		return st.Render(kind) + " "
	}
	return ""
}

// freshStyle is the bold colour of a new (green) or updated (yellow)
// issue: its tag and the rest of its row, so it stands out in the list.
func freshStyle(kind string) (lipgloss.Style, bool) {
	switch kind {
	case "new":
		return styleGood.Bold(true), true
	case "updated":
		return styleWarn.Bold(true), true
	}
	return lipgloss.Style{}, false
}

// renderLabel renders a group label of the tree: one line in the compact
// view, the name over its counts in cards.
//
//	▾ web  12 open · 3 overdue · 2 due soon
func (m Model) renderLabel(ri int) string {
	r := m.rows[ri]
	bar := "  "
	switch {
	case ri == m.cursor && m.compact:
		bar = "▸ "
	case ri == m.cursor:
		bar = styleTitle.Render("▌ ")
	}
	indent := m.indent(r)
	counts := r.counts.String()
	name := m.labelName(r)
	short := func(w int) string {
		if r.group == "" {
			return fit(name, min(w, lipgloss.Width(name)))
		}
		return shortPath(name, w)
	}
	lead := 2 + len(indent) + 2 // bar, indent, fold mark
	var lines []string
	if m.compact {
		nameW := max(m.width-lead-2-lipgloss.Width(counts), min(lipgloss.Width(name), 12))
		lines = []string{bar + indent + m.foldMark(r) + styleProject.Render(short(nameW)) + "  " + styleDim.Render(counts)}
	} else {
		lines = []string{
			bar + indent + m.foldMark(r) + styleProject.Render(short(max(m.width-lead, 8))),
			bar + indent + "  " + styleDim.Render(counts),
		}
	}
	for i, l := range lines {
		if ri == m.cursor {
			lines[i] = highlight(l, m.width)
		} else {
			lines[i] = clip(l, m.width)
		}
	}
	out := strings.Join(lines, "\n")
	if !m.compact {
		out += "\n"
	}
	return out
}
