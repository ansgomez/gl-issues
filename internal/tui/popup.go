package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gl-issues/internal/gl"
	"gl-issues/internal/state"
)

// Pop-up windows for everything that asks for input: comment, assign,
// due date, close/reopen confirmation and the two-step new-issue flow
// (pick a project, then fill in a form).

type formKind int

const (
	formComment formKind = iota
	formAssign
	formDue
	formConfirm
	formPickProject
	formNewIssue
)

const pickerRows = 8 // projects shown at once in the picker

type field struct {
	label string
	in    lineInput
	rows  int // >1 for a multi-line box

	people bool     // assignee picker (see people.go)
	chips  []string // usernames picked so far
	pick   int      // highlighted match
}

type form struct {
	kind    formKind
	title   string
	sub     string // e.g. the issue reference
	fields  []field
	focus   int
	targets []gl.Issue // copies of the issues the form applies to
	err     string
	run     func(*Model) tea.Cmd // formConfirm: action to run on "y"
	enterNo bool                 // formConfirm: enter means no (the question says y/N)

	fromSelection bool // acts on the selected issues: clear them once done

	// sending is set while a comment or new issue is being sent. The
	// pop-up stays (or, if hidden with esc, comes back) until the request
	// succeeds, so a failed request never loses the typed text.
	sending bool

	// project picker
	query   lineInput
	matches []gl.Project
	pick    int
	project gl.Project // chosen project (formNewIssue)
}

type projectsMsg struct {
	projects []gl.Project
	gen      int // projectsGen when the load started; older replies are dropped
	err      error
}

var (
	stylePopup      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(0, 1)
	stylePopupTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleFocusLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleFieldBox   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("8"))
	styleFieldFocus = styleFieldBox.BorderForeground(lipgloss.Color("6"))
	// Only the terminal's own 16 colours: they follow its light/dark theme.
	// Fixed 256-colour backgrounds looked wrong when the theme was guessed.
	styleFieldBar        = styleDim
	styleFieldBarFocused = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
)

// popupWidth is the inner width of pop-up windows.
func (m Model) popupWidth() int { return max(min(76, m.width-6), 20) }

// --- opening forms ---

func (m Model) openComment() (tea.Model, tea.Cmd) {
	is := m.current()
	f := &form{kind: formComment, title: "New comment", sub: is.Ref() + " · " + is.Title, targets: []gl.Issue{*is}}
	f.fields = []field{{label: "Comment", rows: 6, in: lineInput{Multiline: true, Placeholder: "Markdown is fine; enter adds a new line"}}}
	m.form = f
	return m, nil
}

func (m Model) openAssign(targets []gl.Issue) Model {
	fd := field{label: "Assignees", rows: 1, people: true}
	title, sub := "Assign", m.describeTargets(targets)
	if len(targets) == 1 {
		fd.chips = targets[0].AssigneeNames()
	} else {
		title = "Add assignees"
		sub += " · the people you pick are added to each"
	}
	m.form = &form{kind: formAssign, title: title, sub: sub, targets: targets, fields: []field{fd}}
	return m
}

func (m Model) openDue(targets []gl.Issue) Model {
	in := lineInput{Placeholder: "2026-11-01 · today · tomorrow · fri · +3d · +2w · +1m · none"}
	if len(targets) == 1 {
		in.SetValue(targets[0].DueDate)
	}
	m.form = &form{kind: formDue, title: "Due date", sub: m.describeTargets(targets), targets: targets,
		fields: []field{{label: "Due", rows: 1, in: in}}}
	return m
}

func (m Model) openConfirm(title, question string, run func(*Model) tea.Cmd) Model {
	m.form = &form{kind: formConfirm, title: title, sub: question, run: run}
	return m
}

// openNewIssue starts the new-issue flow with the project picker,
// loading the group's projects on first use.
func (m Model) openNewIssue() (tea.Model, tea.Cmd) {
	f := &form{kind: formPickProject, title: "New issue · choose a project"}
	f.query.Placeholder = "type to search, e.g. weather or bob notes"
	m.form = f
	m.rankPicker()
	if m.projects == nil && !m.projectsLoading { // e.g. the startup fetch failed
		return m, m.loadProjects()
	}
	return m, nil
}

// loadProjects marks projects as loading and fetches them; replies to
// earlier fetches are dropped.
func (m *Model) loadProjects() tea.Cmd {
	m.projectsLoading, m.projectsErr = true, nil
	m.projectsGen++
	return m.fetchProjects()
}

// fetchProjects loads the projects, tagged with the current projectsGen.
func (m Model) fetchProjects() tea.Cmd {
	c, group, gen := m.client, m.group, m.projectsGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		ps, err := c.GroupProjects(ctx, group)
		return projectsMsg{projects: ps, gen: gen, err: err}
	}
}

func (m Model) describeTargets(targets []gl.Issue) string {
	hidden := ""
	if n := m.hiddenTargets(targets); n > 0 && m.mode == modeList {
		hidden = fmt.Sprintf(" (%d hidden in folded groups)", n)
		if len(targets) == 1 {
			hidden = " (hidden in a folded group)"
		}
	}
	if len(targets) == 1 {
		return targets[0].Ref() + " · " + targets[0].Title + hidden
	}
	return fmt.Sprintf("%d selected issues", len(targets)) + hidden
}

// rankPicker refreshes the picker matches for the current query. Without
// a query the current issue's project comes first, then recently used
// projects, then projects with many open issues in the list.
func (m *Model) rankPicker() {
	f := m.form
	if f == nil || f.kind != formPickProject {
		return
	}
	boost := m.openByProject()
	for i, p := range m.recent {
		boost[p] += 10000 - i*100
	}
	if is := m.current(); is != nil {
		boost[is.Project()] += 100000
	}
	f.matches = gl.RankProjects(m.projects, f.query.Value(), func(p gl.Project) int { return boost[p.Path] })
	f.pick = min(f.pick, max(len(f.matches)-1, 0))
}

// openByProject counts the open issues in the list per project (the
// closed and all views hold closed ones too).
func (m Model) openByProject() map[string]int {
	open := map[string]int{}
	for _, is := range m.issues {
		if is.State == "opened" {
			open[is.Project()]++
		}
	}
	return open
}

func (m Model) startNewIssueForm(p gl.Project) (tea.Model, tea.Cmd) {
	f := &form{kind: formNewIssue, title: "New issue", project: p, sub: relPath(p.Path, m.group)}
	f.fields = []field{
		{label: "Title", rows: 1, in: lineInput{Placeholder: "required"}},
		{label: "Assignees", rows: 1, people: true},
		{label: "Due", rows: 1, in: lineInput{Placeholder: "optional · 2026-11-01 · fri · +2w"}},
		{label: "Description", rows: 5, in: lineInput{Multiline: true, Placeholder: "optional · Markdown · ctrl+e opens your editor"}},
	}
	m.form = f
	return m, nil
}

// --- keys ---

func (m Model) handleFormKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	f := m.form
	key := msg.String()
	if f.sending { // wait for the request; esc hides the pop-up meanwhile
		if key == "esc" || key == "ctrl+c" {
			m.form = nil
		}
		return m, nil
	}
	f.err = ""

	if key == "esc" || key == "ctrl+c" {
		m.form = nil
		return m, nil
	}

	switch f.kind {
	case formConfirm:
		if key == "enter" && f.enterNo {
			key = "n"
		}
		switch key {
		case "y", "enter":
			m.form = nil
			if f.fromSelection {
				m.clearSelection()
			}
			cmd := f.run(&m)
			return m, cmd
		case "n":
			m.form = nil
		}
		return m, nil

	case formPickProject:
		switch key {
		case "up", "ctrl+p":
			f.pick = max(f.pick-1, 0)
		case "down", "ctrl+n":
			f.pick = min(f.pick+1, max(len(f.matches)-1, 0))
		case "pgup":
			f.pick = max(f.pick-pickerRows, 0)
		case "pgdown":
			f.pick = min(f.pick+pickerRows, max(len(f.matches)-1, 0))
		case "enter", "tab":
			if len(f.matches) == 0 {
				return m, nil
			}
			return m.startNewIssueForm(f.matches[f.pick])
		default:
			f.query.Update(msg)
			f.pick = 0
			m.rankPicker()
		}
		return m, nil
	}

	fd := &f.fields[f.focus]
	if fd.people && m.handlePeopleKey(f, fd, msg) {
		return m, nil
	}
	switch key {
	case "ctrl+s":
		return m.submit()
	case "tab":
		f.focus = (f.focus + 1) % len(f.fields)
		return m, nil
	case "shift+tab":
		f.focus = (f.focus - 1 + len(f.fields)) % len(f.fields)
		return m, nil
	case "ctrl+e":
		if fd.rows > 1 {
			return m, m.openFieldEditor(fd.in.Value())
		}
	case "enter":
		if fd.rows > 1 {
			fd.in.Insert('\n')
			return m, nil
		}
		if f.focus < len(f.fields)-1 && f.kind == formNewIssue {
			f.focus++
			return m, nil
		}
		return m.submit()
	case "up":
		if fd.rows == 1 && f.focus > 0 {
			f.focus--
			return m, nil
		}
	case "down":
		if fd.rows == 1 && f.focus < len(f.fields)-1 {
			f.focus++
			return m, nil
		}
	}
	fd.in.Update(msg)
	fd.pick = 0 // the query changed: highlight the best match again
	return m, nil
}

// submit carries out the form; once it has started (the pop-up closed),
// a selection the form acted on is cleared.
func (m Model) submit() (tea.Model, tea.Cmd) {
	f := m.form
	tm, cmd := m.submitForm()
	if nm := tm.(Model); nm.form == nil && f.fromSelection {
		nm.clearSelection()
		return nm, cmd
	}
	return tm, cmd
}

func (m Model) submitForm() (tea.Model, tea.Cmd) {
	f := m.form
	val := func(label string) string {
		for _, fd := range f.fields {
			if fd.label == label {
				return fd.in.Value()
			}
		}
		return ""
	}
	// A half-typed name is never guessed: it must be picked or cleared.
	for i, fd := range f.fields {
		if fd.people && strings.TrimSpace(fd.in.Value()) != "" {
			f.err = "pick “" + strings.TrimSpace(fd.in.Value()) + "” from the list with enter, or clear it"
			f.focus = i
			return m, nil
		}
	}
	chips := func() []string {
		for _, fd := range f.fields {
			if fd.people {
				return fd.chips
			}
		}
		return nil
	}
	switch f.kind {
	case formComment:
		// Only the end is trimmed: leading indentation is Markdown
		// (a code block, a nested list).
		body := strings.TrimRight(val("Comment"), " \t\r\n")
		if strings.TrimSpace(body) == "" {
			f.err = "the comment is empty"
			return m, nil
		}
		if m.busy[f.targets[0].ID] != "" {
			f.err = "another action on this issue is still running; try again shortly"
			return m, nil
		}
		f.sending = true
		return m, sentFrom(f, m.comment(f.targets[0], body))
	case formAssign:
		people := chips()
		if len(f.targets) == 1 { // the tags are the new assignee list
			m.form = nil
			cmd := m.assign(f.targets, strings.Join(people, ","))
			return m, cmd
		}
		if len(people) == 0 {
			f.err = "pick at least one person to add"
			return m, nil
		}
		add := make([]string, len(people))
		for i, p := range people {
			add[i] = "+" + p
		}
		m.form = nil
		cmd := m.assign(f.targets, strings.Join(add, ","))
		return m, cmd
	case formDue:
		due, err := gl.ParseDue(val("Due"), time.Now())
		if err != nil {
			f.err = err.Error()
			return m, nil
		}
		m.form = nil
		// Nothing is written to an issue that has this date already (enter
		// on the field as it opened): no change, no bumped updated_at.
		var changed []gl.Issue
		latest := m.latestByID()
		for _, is := range f.targets {
			if cur, ok := latest[is.ID]; ok {
				is = cur
			}
			if is.DueDate != due {
				changed = append(changed, is)
			}
		}
		if len(changed) == 0 {
			m.setFlash("due date unchanged", false)
			return m, nil
		}
		if same := len(f.targets) - len(changed); same > 0 { // a busy-skip note, if any, replaces this
			m.setFlash(fmt.Sprintf("due date: %s had it already", plural(same, "issue", "issues")), false)
		}
		cmd := m.updateMany(changed, "due date", func(gl.Issue) map[string]any {
			return map[string]any{"due_date": due} // "" clears it
		})
		return m, cmd
	case formNewIssue:
		title := strings.TrimSpace(val("Title"))
		if title == "" {
			f.err = "a title is required"
			f.focus = 0
			return m, nil
		}
		due, err := gl.ParseDue(val("Due"), time.Now())
		if err != nil {
			f.err = err.Error()
			f.focus = 2
			return m, nil
		}
		assignees := chips()
		desc := strings.TrimRight(val("Description"), " \t\r\n") // indentation is Markdown
		m.creating++
		f.sending = true // the project is remembered once the issue exists
		return m, sentFrom(f, m.createIssue(f.project.Path, title, desc, due, assignees))
	}
	return m, nil
}

// sentFrom tags the result of cmd with the pop-up that sent it, so the
// pop-up closes on success and shows the error (text kept) on failure.
func sentFrom(f *form, cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		msg, ok := cmd().(updatedMsg)
		if ok {
			msg.form = f
		}
		return msg
	}
}

// finishSending handles the reply to a pop-up's request (see
// form.sending). It reports whether the reply was a failure, which it has
// shown in the pop-up: on screen again at once, or, while another pop-up
// is open, as soon as that one closes (failedForms); the text is never
// lost.
func (m *Model) finishSending(msg updatedMsg) (failed bool) {
	f := msg.form
	f.sending = false
	if msg.err == nil {
		if m.form == f {
			m.form = nil
		}
		return false
	}
	f.err = msg.what + " failed: " + msg.err.Error()
	switch m.form {
	case f: // still on screen
	case nil: // hidden with esc: bring the text back
		m.form = f
	default: // another pop-up is open: do not replace it, follow it
		m.failedForms = append(m.failedForms, f)
		m.setFlash(f.err+"; the text comes back when this window closes", true)
	}
	return true
}

func (m Model) createIssue(project, title, desc, due string, assignees []string) tea.Cmd {
	c, members := m.client, m.members
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		fields := map[string]any{"title": title}
		if desc != "" {
			fields["description"] = desc
		}
		if due != "" {
			fields["due_date"] = due
		}
		if len(assignees) > 0 {
			ids, err := c.AssigneeIDs(ctx, assignees, members)
			if err != nil {
				return updatedMsg{what: "create", err: err}
			}
			fields["assignee_ids"] = ids
		}
		is, err := c.Create(ctx, project, fields)
		if err != nil {
			return updatedMsg{what: "create", err: err}
		}
		// Only now that the issue exists is the project worth remembering;
		// the state file is read and written here, off the UI goroutine.
		msg := updatedMsg{id: is.ID, what: "create", issue: is}
		msg.recentErr = state.AddRecentProject(project)
		msg.recent = state.RecentProjects()
		return msg
	}
}

// --- view ---

// renderForm draws the current pop-up window, shrinking multi-line
// fields if it would not fit the terminal.
func (m Model) renderForm() string {
	out := m.renderFormRows(0)
	for cut := 1; lipgloss.Height(out) > m.height-1 && cut <= 8; cut++ {
		out = m.renderFormRows(cut)
	}
	return out
}

// renderFormRows renders the pop-up with multi-line fields cut lines
// shorter (never below 2).
func (m Model) renderFormRows(cut int) string {
	f := m.form
	w := m.popupWidth()
	var lines []string
	lines = append(lines, stylePopupTitle.Render(f.title))
	if f.sub != "" {
		for _, l := range strings.Split(f.sub, "\n") { // fit takes one line
			lines = append(lines, styleDim.Render(fit(l, w)))
		}
	}
	lines = append(lines, "")

	var hint string
	switch {
	case f.sending:
		hint = m.spin.View() + " sending… · esc hides this window"
	case f.kind == formConfirm && f.enterNo:
		hint = "y yes · n / enter / esc no"
	case f.kind == formConfirm:
		hint = "y yes · n / esc no"
	case f.kind == formPickProject:
		lines = append(lines, m.renderPicker(w)...)
		hint = "↑↓ choose · enter select · esc cancel"
	default:
		for i, fd := range f.fields {
			if fd.rows > 1 {
				fd.rows = max(fd.rows-cut, 2)
			}
			lines = append(lines, m.renderField(f, i, fd, w)...)
		}
		fd := f.fields[f.focus]
		switch {
		case fd.people && strings.TrimSpace(fd.in.Value()) != "":
			hint = "enter or tab adds the highlighted person · esc cancel"
		case f.kind == formComment:
			hint = "ctrl+s send · ctrl+e editor · esc cancel"
		case f.kind == formNewIssue:
			hint = "tab next field · ctrl+s create · esc cancel"
		default:
			hint = "enter save · esc cancel"
		}
	}
	if f.err != "" {
		lines = append(lines, styleBad.Render(fit(f.err, w)))
	}
	lines = append(lines, "", styleDim.Render(hint))
	for i := range lines {
		lines[i] = clip(lines[i], w)
	}
	return stylePopup.Width(w + 2).Render(strings.Join(lines, "\n"))
}

func (m Model) renderField(f *form, i int, fd field, w int) []string {
	focused := i == f.focus
	label := styleDim.Render(fd.label)
	if focused {
		label = styleFocusLabel.Render(fd.label)
	}
	box := styleFieldBox
	if focused {
		box = styleFieldFocus
	}
	if fd.people {
		const labelW = 12
		l := styleDim.Render(fit(fd.label, labelW))
		if focused {
			l = styleFocusLabel.Render(fit(fd.label, labelW))
		}
		return m.renderPeople(f, fd, focused, l, w)
	}
	var out []string
	if fd.rows > 1 { // label above a bordered box
		inner := w - 2
		content := strings.Join(fd.in.renderBox(inner, fd.rows, focused), "\n")
		out = append(out, label)
		out = append(out, strings.Split(box.Width(inner).Render(content), "\n")...)
	} else { // label and value on one line, value on a shaded background
		const labelW = 12
		label = fit(fd.label, labelW)
		if focused {
			label = styleFocusLabel.Render(label)
		} else {
			label = styleDim.Render(label)
		}
		bar := styleFieldBar.Render("▏")
		if focused {
			bar = styleFieldBarFocused.Render("▎")
		}
		out = append(out, label+bar+" "+fd.in.render(w-labelW-3, focused))
	}

	if focused {
		switch fd.label {
		case "Due":
			if v := strings.TrimSpace(fd.in.Value()); v != "" {
				due, err := gl.ParseDue(v, time.Now())
				switch {
				case err != nil:
					out = append(out, styleWarn.Render("  "+err.Error()))
				case due == "":
					out = append(out, styleDim.Render("  → no due date"))
				default:
					out = append(out, styleGood.Render("  → "+describeDate(due, time.Now())))
				}
			}
		}
	}
	return out
}

// describeDate turns "2026-10-09" into "Fri 9 Oct 2026 · in 6 days".
func describeDate(date string, now time.Time) string {
	t, err := time.ParseInLocation(time.DateOnly, date, time.Local)
	if err != nil {
		return date
	}
	is := gl.Issue{DueDate: date}
	d, _ := is.DaysLeft(now)
	rel := fmt.Sprintf("in %d days", d)
	switch {
	case d == 0:
		rel = "today"
	case d == 1:
		rel = "tomorrow"
	case d < 0:
		rel = fmt.Sprintf("%d days ago", -d)
	}
	return t.Format("Mon 2 Jan 2006") + " · " + rel
}

func (m Model) renderPicker(w int) []string {
	f := m.form
	lines := []string{styleFocusLabel.Render(fit("Search", 12)) + styleFieldBarFocused.Render("▎") + " " + f.query.render(w-15, true), ""}
	switch {
	case m.projectsErr != nil:
		return append(lines, styleBad.Render("could not load projects: "+m.projectsErr.Error()))
	case m.projectsLoading && len(m.projects) == 0:
		return append(lines, styleDim.Render(m.spin.View()+" loading projects of "+m.group+" …"))
	case len(f.matches) == 0:
		return append(lines, styleDim.Render("no project matches"))
	}
	open := m.openByProject()
	top := max(min(f.pick-pickerRows/2, len(f.matches)-pickerRows), 0)
	for i := top; i < min(top+pickerRows, len(f.matches)); i++ {
		p := f.matches[i]
		rel := relPath(p.Path, m.group)
		parent, name := "", rel
		if j := strings.LastIndex(rel, "/"); j >= 0 {
			parent, name = rel[:j], rel[j+1:]
		}
		mark := "  "
		nameS := styleProject.Render(name)
		if i == f.pick {
			mark = styleTitle.Render("▌ ")
			nameS = styleProject.Reverse(true).Render(" " + name + " ")
		}
		extra := ""
		if n := open[p.Path]; n > 0 {
			extra = styleDim.Render(fmt.Sprintf("  %d open", n))
		}
		line := mark + nameS + "  " + styleDim.Render(shortPath(parent, max(w-lipgloss.Width(name)-16, 10))) + extra
		lines = append(lines, line)
	}
	lines = append(lines, styleDim.Render(fmt.Sprintf("%d of %d projects", len(f.matches), len(m.projects))))
	return lines
}

// overlay draws popup centred over base, dimming base.
func overlay(base, popup string, width, height int) string {
	bl := strings.Split(base, "\n")
	for len(bl) < height {
		bl = append(bl, "")
	}
	for i := range bl {
		bl[i] = styleDim.Render(ansi.Strip(bl[i]))
	}
	pl := strings.Split(popup, "\n")
	pw := lipgloss.Width(popup)
	x := max((width-pw)/2, 0)
	y := max((height-len(pl))/2, 0)
	for i, l := range pl {
		if y+i >= len(bl) {
			break
		}
		row := bl[y+i]
		left := ansi.Truncate(row, x, "")
		left += strings.Repeat(" ", max(x-lipgloss.Width(left), 0))
		right := ansi.TruncateLeft(row, x+pw, "")
		bl[y+i] = left + l + right
	}
	return strings.Join(bl[:min(len(bl), height)], "\n")
}

// --- editor for a pop-up field ---

type fieldEditorMsg struct {
	text string
	err  error
}

// openFieldEditor edits the focused multi-line field in $EDITOR.
func (m Model) openFieldEditor(text string) tea.Cmd {
	return m.runEditor(text, false, func(text, _ string, err error) tea.Msg {
		return fieldEditorMsg{text: text, err: err}
	})
}
