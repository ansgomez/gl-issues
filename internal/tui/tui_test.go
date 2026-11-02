package tui

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"gl-issues/internal/gl"
)

func TestApplyAssign(t *testing.T) {
	cur := []string{"alice", "bob"}
	for spec, want := range map[string][]string{
		"carol":              {"carol"},
		"carol, @erin":       {"carol", "erin"},
		"+carol":             {"alice", "bob", "carol"},
		"-bob":               {"alice"},
		"!alice, +carol":     {"bob", "carol"},
		"+alice":             {"alice", "bob"},
		"":                   nil,
		"carol, +erin, -bob": {"carol", "erin"},
		"Alice, alice":       {"Alice"},
	} {
		if got := ApplyAssign(cur, spec); !reflect.DeepEqual(got, want) {
			t.Errorf("ApplyAssign(%q) = %v, want %v", spec, got, want)
		}
	}
}

func TestLessDue(t *testing.T) {
	now := time.Now()
	a := gl.Issue{DueDate: "2026-01-01"}
	b := gl.Issue{DueDate: "2026-02-01"}
	none := gl.Issue{UpdatedAt: now}
	if !less(a, b, "due") || less(b, a, "due") {
		t.Error("earlier due date should sort first")
	}
	if !less(b, none, "due") || less(none, a, "due") {
		t.Error("issues without due date should sort last")
	}
}

func TestFit(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "abc…"},
		{"日本語です", 5, "日本…"},
		{"a日本", 3, "a… "}, // 本 does not fit in the cell left before the mark
		{"abc", 1, "…"},
		{strings.Repeat("日", 5000) + "x", 8, "日日日… "}, // one pass, not one per removed rune
	} {
		if got := fit(tc.in, tc.w); got != tc.want {
			t.Errorf("fit(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
	for _, tc := range []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 5, "abc  "},
		{"abcdef", 4, "…def"},
		{"abcdef", 1, "f"},
		{"日本語", 4, "…語 "}, // 本 does not fit in the 3 cells left
		{"x日本語", 5, "…本語"},
		{"abc", 0, ""}, // no room: nothing, no panic (like fit)
		{"abc", -1, ""},
		{"", -3, ""},
	} {
		if got := fitLeft(tc.in, tc.w); got != tc.want {
			t.Errorf("fitLeft(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}

// Emoji joined with U+200D (family, flag) and a heart with its variation
// selector are one 2-cell cluster each; cut rune by rune they would be
// miscounted, shifting the columns after them or overflowing the width.
func TestFitGraphemeClusters(t *testing.T) {
	family, flag, heart := "\U0001F468\u200d\U0001F469\u200d\U0001F467", "\U0001F3F3\ufe0f\u200d\U0001F308", "\u2764\ufe0f"
	title := "Fix " + family + " family " + flag + " flag " + heart + " emoji " + family
	for w := 1; w <= lipgloss.Width(title)+2; w++ {
		for name, f := range map[string]func(string, int) string{"fit": fit, "fitLeft": fitLeft} {
			if got := f(title, w); lipgloss.Width(got) != w {
				t.Errorf("%s(title, %d) = %q: %d cells", name, w, got, lipgloss.Width(got))
			}
		}
	}
	for _, tc := range []struct {
		f    func(string, int) string
		in   string
		w    int
		want string
	}{
		{fit, family + " ok", 4, family + " …"},
		{fit, heart + heart + heart, 3, heart + "…"}, // not "❤\ufe0f❤\ufe0f…" (5 cells)
		{fit, "ab" + family, 3, "ab…"},               // the family does not fit in the cell left
		{fitLeft, "ok " + family, 3, "…" + family},
		{fitLeft, family + "ab", 3, "…ab"},
	} {
		if got := tc.f(tc.in, tc.w); got != tc.want {
			t.Errorf("(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}

// Text back from ctrl+e's editor is cleaned like a paste: CR LF becomes
// LF, escape sequences and control characters go, a tab becomes spaces.
func TestFieldEditorTextIsCleaned(t *testing.T) {
	m := press(sampleModel(100, 30), "c")
	if m.form == nil || m.form.fields[m.form.focus].rows <= 1 {
		t.Fatal("c should open the comment pop-up on its text field")
	}
	nm, _ := m.Update(fieldEditorMsg{text: "one\r\ntwo\rthree\x1b[2J\x07\tend\u202e"})
	m = nm.(Model)
	if got, want := m.form.fields[m.form.focus].in.Value(), "one\ntwo\nthree    end"; got != want {
		t.Errorf("field = %q, want %q", got, want)
	}
	nm, _ = m.Update(fieldEditorMsg{err: errors.New("exit status 1")})
	if m = nm.(Model); !strings.Contains(m.flash, "editor: exit status 1") || m.form.fields[m.form.focus].in.Value() != "one\ntwo\nthree    end" {
		t.Errorf("a failed editor must keep the text and say so: flash %q", m.flash)
	}
}

// A filter or search that hides selected issues takes them out of the
// count in the title and footer and out of the bulk actions, which then
// act on the selected issues still shown, or on the cursor's issue when
// none is; lifting the filter brings them back.
func TestSelectionHiddenByFilter(t *testing.T) {
	ids := func(is []gl.Issue) []int {
		var out []int
		for _, i := range is {
			out = append(out, i.ID)
		}
		return out
	}
	sel := press(sampleModel(200, 20), " ", " ") // issues 1 (mine) and 2 (unassigned); wide: the title is clipped
	if got := ids(sel.targets()); !reflect.DeepEqual(got, []int{1, 2}) || !strings.Contains(sel.titleLine(), "2 selected") {
		t.Fatalf("setup: targets %v, title %q", got, sel.titleLine())
	}
	m := press(sel, "u") // only issue 2 is shown
	if got := ids(m.targets()); !reflect.DeepEqual(got, []int{2}) {
		t.Errorf("unassigned filter: targets %v, want [2]", got)
	}
	if !strings.Contains(m.titleLine(), "1 selected") || !m.usingSelection() {
		t.Errorf("title %q should count the 1 selected issue shown", m.titleLine())
	}
	m = press(m, "u") // filter lifted: both again
	if got := ids(m.targets()); !reflect.DeepEqual(got, []int{1, 2}) || !strings.Contains(m.titleLine(), "2 selected") {
		t.Errorf("filter lifted: targets %v, title %q", got, m.titleLine())
	}
	m = press(sel, "/", "No", " ", "due", "enter") // only issue 3 is shown: no selection in sight
	if got := ids(m.targets()); !reflect.DeepEqual(got, []int{3}) {
		t.Errorf("search hiding the selection: targets %v, want the cursor's [3]", got)
	}
	if strings.Contains(m.titleLine(), "selected") || m.usingSelection() {
		t.Errorf("title %q counts hidden selected issues", m.titleLine())
	}
	for _, g := range m.footerGroups() {
		if g.label == "Selected" {
			t.Error("footer offers bulk actions with no selected issue shown")
		}
	}
	if len(m.selected) != 2 {
		t.Errorf("the hidden selection must survive the filter: %v", m.selected)
	}
	if m = press(m, "esc"); len(m.selected) != 0 || m.search != "" {
		t.Errorf("esc should clear the search and the selection: %v %q", m.selected, m.search)
	}
}

// A new issue joins the list only when the view shows its state: the
// closed list must not gain an open one.
func TestCreatedIssueOnlyInMatchingView(t *testing.T) {
	closed := issue(1, time.Now())
	closed.State = "closed"
	for _, tc := range []struct {
		view string
		want int
	}{{"closed", 1}, {"all", 2}, {"opened", 2}} {
		m := New(gl.Client{}, "team", flatOptions())
		m.state = slices.Index(states, tc.view)
		nm, _ := m.Update(issuesMsg{state: tc.view, issues: []gl.Issue{closed}})
		m = nm.(Model)
		m.replace(issue(2, time.Now()), true)
		if len(m.issues) != tc.want {
			t.Errorf("%s view: %d issues after creating an open one, want %d", tc.view, len(m.issues), tc.want)
		}
	}
}

// The project picker counts open issues only, also in the all view.
func TestPickerCountsOpenIssues(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := New(gl.Client{}, "team", flatOptions())
	m.state = slices.Index(states, "all")
	closed := issue(2, time.Now())
	closed.State = "closed"
	nm, _ := m.Update(issuesMsg{state: "all", issues: []gl.Issue{issue(1, time.Now()), closed}})
	m = press(nm.(Model), "n")
	nm, _ = m.Update(projectsMsg{projects: []gl.Project{{ID: 1, Name: "proj", Path: "team/proj"}}})
	m = nm.(Model)
	if open := m.openByProject(); open["team/proj"] != 1 {
		t.Errorf("open per project = %v", open)
	}
	if out := ansi.Strip(strings.Join(m.renderPicker(80), "\n")); !strings.Contains(out, "1 open") || strings.Contains(out, "2 open") {
		t.Errorf("picker:\n%s", out)
	}
}

// Once the day turns, the due counts in the title and on the group rows
// are made again by Update, once, not on every frame.
func TestTitleStatsRecountCached(t *testing.T) {
	m := loaded(time.Now())
	m.stats.day = time.Now().AddDate(0, 0, -1)
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = nm.(Model)
	if today := time.Now().Format(time.DateOnly); m.stats.day.Format(time.DateOnly) != today {
		t.Errorf("stats counted for %v, want today", m.stats.day)
	}

	// Issue 2 (web, due soon) turns overdue: its label says so only once
	// the day turns, as a message that changes nothing arrives.
	m = treeModel(t, 1, 120, 40)
	for i := range m.issues {
		if m.issues[i].ID == 2 {
			m.issues[i].DueDate = time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
		}
	}
	webCounts := func(m Model) string {
		for _, r := range m.rows {
			if r.isLabel() && r.group == "web" {
				return r.counts.String()
			}
		}
		return "no web label"
	}
	nm, _ = m.Update(refreshTickMsg{gen: -1})
	if got := webCounts(nm.(Model)); !strings.Contains(got, "due soon") {
		t.Errorf("same day: web counts %q, want them kept", got)
	}
	m.stats.day = time.Now().AddDate(0, 0, -1)
	nm, _ = m.Update(refreshTickMsg{gen: -1})
	if got := webCounts(nm.(Model)); !strings.Contains(got, "1 overdue") || strings.Contains(got, "due soon") {
		t.Errorf("new day: web counts %q, want 1 overdue", got)
	}
}

func sampleModel(w, h int) Model {
	m := New(gl.Client{Host: "git.example.org"}, "team", flatOptions())
	m.me = gl.User{ID: 1, Username: "me"}
	m.members = []gl.User{{ID: 1, Username: "me"}, {ID: 2, Username: "alice", Name: "Alice A"}}
	mk := func(id int, ref, title, due string, who ...string) gl.Issue {
		is := gl.Issue{ID: id, IID: id, Title: title, DueDate: due, State: "opened", UpdatedAt: time.Now().Add(-time.Duration(id) * time.Hour)}
		is.References.Full = ref
		for _, u := range who {
			is.Assignees = append(is.Assignees, gl.User{Username: u})
		}
		return is
	}
	nm, _ := m.Update(issuesMsg{issues: []gl.Issue{
		mk(1, "team/proj#1", "Fix the thing with a rather long title that will need truncation", "2020-01-01", "me"),
		mk(2, "team/sub/other#2", "Write docs", time.Now().Format(time.DateOnly)),
		mk(3, "team/proj#3", "No due date", "", "alice", "me"),
	}})
	nm, _ = nm.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return nm.(Model)
}

func press(m Model, keys ...string) Model {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case " ":
			msg = tea.KeyMsg{Type: tea.KeySpace}
		case "ctrl+s":
			msg = tea.KeyMsg{Type: tea.KeyCtrlS}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		nm, _ := m.Update(msg)
		m = nm.(Model)
	}
	return m
}

func TestViewSmoke(t *testing.T) {
	for _, w := range []int{40, 80, 160} {
		m := sampleModel(w, 20)
		for _, v := range []Model{m, press(m, "z"), press(m, "enter"), press(m, "a", "al"), press(m, "d", "fri"),
			press(m, "x"), press(m, "c", "hello"), press(m, "n"), press(m, "h"), press(m, "j", "j", "j")} {
			out := v.View()
			lines := strings.Split(out, "\n")
			if len(lines) > 20 {
				t.Errorf("width %d: %d lines, want ≤ 20", w, len(lines))
			}
			for _, l := range lines {
				if lw := lipgloss.Width(l); lw > w {
					t.Errorf("width %d: line is %d wide: %q", w, lw, l)
				}
			}
		}
	}
}

// The row under the cursor gets a background band across the full width,
// in both views, kept alive past the resets of its inner styles; no other
// row does.
func TestCursorRowHighlight(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	on, _, _ := strings.Cut(styleCursorRow.Render("x"), "x")
	if on == "" {
		t.Fatal("no background sequence rendered; test setup broken")
	}
	const w = 90
	for name, keys := range map[string][]string{
		"cards": {"j"}, "compact": {"z", "j"},
	} {
		m := press(sampleModel(w, 30), keys...)
		want := 2 // both card lines
		if m.compact {
			want = 1
		}
		var got []string
		for _, l := range strings.Split(m.View(), "\n") {
			if strings.Contains(l, on) {
				got = append(got, l)
			}
		}
		if len(got) != want {
			t.Fatalf("%s: %d highlighted lines, want %d", name, len(got), want)
		}
		title := m.issues[m.view[m.cursor]].Title
		if !strings.Contains(got[0], title[:10]) {
			t.Errorf("%s: highlight is not on the cursor row: %q", name, got[0])
		}
		for _, l := range got {
			if lw := lipgloss.Width(l); lw != w {
				t.Errorf("%s: highlighted line is %d wide, want %d", name, lw, w)
			}
			body := strings.TrimSuffix(l, "\x1b[0m")
			for _, part := range strings.Split(body, "\x1b[0m")[1:] {
				if !strings.HasPrefix(part, on) {
					t.Errorf("%s: background lost after a reset: %q", name, l)
					break
				}
			}
		}
	}
}

func TestFiltersAndSort(t *testing.T) {
	m := sampleModel(100, 20)
	if got := m.issues[m.view[0]].ID; got != 1 {
		t.Errorf("due sort: first = %d, want overdue issue 1", got)
	}
	if n := len(press(m, "o").view); n != 2 {
		t.Errorf("due-soon filter: %d issues, want 2", n)
	}
	if n := len(press(m, "m").view); n != 2 {
		t.Errorf("mine filter: %d issues, want 2", n)
	}
	if n := len(press(m, "u").view); n != 1 {
		t.Errorf("unassigned filter: %d issues, want 1", n)
	}
	if n := len(press(m, "/", "d", "o", "c", "s", "enter").view); n != 1 {
		t.Errorf("search: %d issues, want 1", n)
	}
	sel := press(m, " ", " ")
	if len(sel.targets()) != 2 {
		t.Errorf("selection targets = %v", sel.targets())
	}
}

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nano -w")
	if got := editorCommand(""); got != "nano -w" {
		t.Errorf("editorCommand = %q, want $EDITOR", got)
	}
	t.Setenv("VISUAL", "code -w")
	if got := editorCommand(""); got != "code -w" {
		t.Errorf("editorCommand = %q, want $VISUAL", got)
	}
	if got := editorCommand("hx"); got != "hx" {
		t.Errorf("editorCommand = %q, want the editor setting", got)
	}
}

func TestEmptyListMessages(t *testing.T) {
	m := New(gl.Client{}, "team", flatOptions())
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 12}) // two list lines left under the footer
	nm, _ = nm.Update(issuesMsg{err: errors.New(`group "acme" not found, or you have no access (GET groups/acme/issues)`)})
	if out := nm.View(); !strings.Contains(out, "could not load issues: group \"acme\" not found") {
		t.Errorf("load error not shown:\n%s", out)
	}
	// The reason stays in the list area after the flash line clears.
	nm, _ = nm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	out := nm.View()
	if !strings.Contains(out, "no access") || !strings.Contains(out, "r to retry · run gl-issues doctor") || nm.(Model).flash != "" {
		t.Errorf("load error must stay visible:\n%s", out)
	}
	if n := len(strings.Split(out, "\n")); n != 12 {
		t.Errorf("view is %d lines, want 12", n)
	}
	// A narrow screen wraps it instead of cutting it off.
	nm, _ = nm.Update(tea.WindowSizeMsg{Width: 40, Height: 24})
	if out := nm.View(); !strings.Contains(out, "no access") {
		t.Errorf("load error cut off at width 40:\n%s", out)
	}
	if out := press(sampleModel(100, 10), "/", "z", "z", "z").View(); !strings.Contains(out, "no issues match the filters") {
		t.Errorf("filter message not shown:\n%s", out)
	}
}

func TestShortPath(t *testing.T) {
	for _, tc := range []struct {
		in   string
		w    int
		want string
	}{
		{"people/bob", 20, "people/bob"},
		{"platform/weather/sensors/logger", 22, "platform/…/logger"},
		{"platform/weather/sensors/logger", 26, "platform/…/sensors/logger"},
		{"platform/weather/sensors/logger", 10, "…/logger"},
		{"averyveryverylongsegment", 8, "…segment"},
	} {
		if got := shortPath(tc.in, tc.w); got != tc.want {
			t.Errorf("shortPath(%q, %d) = %q, want %q", tc.in, tc.w, got, tc.want)
		}
	}
}

func TestNewIssueFlow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := press(sampleModel(100, 30), "n")
	if m.form == nil || m.form.kind != formPickProject {
		t.Fatal("n should open the project picker")
	}
	nm, _ := m.Update(projectsMsg{projects: []gl.Project{
		{ID: 1, Name: "proj", Path: "team/proj"},
		{ID: 2, Name: "other", Path: "team/sub/other"},
		{ID: 3, Name: "tools", Path: "team/tools"},
	}})
	m = nm.(Model)
	if got := m.form.matches[0].Path; got != "team/proj" {
		t.Errorf("current issue's project should come first, got %s", got)
	}
	m = press(m, "o", "t", "h")
	if len(m.form.matches) != 1 || m.form.matches[0].Path != "team/sub/other" {
		t.Errorf("search 'oth': %v", m.form.matches)
	}
	m = press(m, "enter")
	if m.form.kind != formNewIssue || m.form.project.Path != "team/sub/other" {
		t.Fatalf("enter should open the form for team/sub/other, got %+v", m.form)
	}
	if out := m.View(); !strings.Contains(out, "New issue") || !strings.Contains(out, "Title") {
		t.Errorf("form not rendered:\n%s", out)
	}
	m = press(m, "ctrl+s")
	if m.form == nil || !strings.Contains(m.form.err, "title") {
		t.Fatal("empty title must be refused")
	}
	m = press(m, "Fix", " ", "it", "enter", "me", "enter", "enter", "fri")
	if m.form.focus != 2 {
		t.Errorf("enter should move to the next field, focus = %d", m.form.focus)
	}
	m = press(m, "ctrl+s")
	if m.form == nil || !m.form.sending {
		t.Errorf("ctrl+s should submit and keep the pop-up while sending")
	}
	if len(m.recent) != 0 {
		t.Errorf("project must not be remembered before the issue exists, recent = %v", m.recent)
	}
	nm, _ = m.Update(updatedMsg{what: "create", err: errors.New("HTTP 403")})
	if r := nm.(Model).recent; len(r) != 0 {
		t.Errorf("failed create must not be remembered, recent = %v", r)
	}
	created := gl.Issue{ID: 99, IID: 5, Title: "Fix it", State: "opened"}
	created.References.Full = "team/sub/other#5"
	nm, _ = m.Update(updatedMsg{id: 99, what: "create", issue: created, recent: []string{"team/sub/other"}})
	m = nm.(Model)
	if r := m.recent; len(r) == 0 || r[0] != "team/sub/other" {
		t.Errorf("project should be remembered, recent = %v", r)
	}
	if is := m.current(); is == nil || is.ID != 99 {
		t.Errorf("cursor should move to the new issue")
	}
}

// An issue created from details is added to the list behind them, but
// details (and the cursor) stay on the issue shown: the next action
// targets what the pane shows. The cursor only follows a new issue in
// the list.
func TestCreateFromDetailKeepsShownIssue(t *testing.T) {
	m := press(flatLoaded(), "j", "enter")
	shown := *m.current()
	created := gl.Issue{ID: 99, IID: 5, Title: "Brand new issue", State: "opened"}
	created.References.Full = "team/sub/other#5"
	nm, _ := m.Update(updatedMsg{id: 99, what: "create", issue: created})
	m = nm.(Model)
	if m.mode != modeDetail || m.current() == nil || m.current().ID != shown.ID {
		t.Fatalf("details moved: mode %v on %s", m.mode, m.cursorKey())
	}
	if out := m.vp.View(); !strings.Contains(out, shown.Title) || strings.Contains(out, created.Title) {
		t.Errorf("pane shows:\n%s", out)
	}
	if c := press(m, "c"); c.form == nil || c.form.targets[0].ID != shown.ID {
		t.Errorf("c should comment on %s, form = %+v", shown.Ref(), c.form)
	}
	if !slices.ContainsFunc(m.issues, func(is gl.Issue) bool { return is.ID == 99 }) {
		t.Error("the new issue is not in the list")
	}
	if l := press(m, "esc"); l.mode != modeList || l.current().ID != shown.ID {
		t.Errorf("after esc the cursor is on %s", l.cursorKey())
	}
}

// A rebuild the user did not ask for (the day turned: due groups and
// counts change) can take the issue shown in details out of view, e.g.
// into a folded group. Details then stay on it (unfolded) or close; they
// never keep showing one issue while the cursor is on another.
func TestDayChangeInDetailKeepsOrClosesIssue(t *testing.T) {
	m := flatLoaded()
	m.cursor = rowOf(m, "i:3") // no due date
	m = press(m, "enter")
	shown := *m.current()
	m.attention = true // its issue no longer matches, as if a due date passed overnight
	m.stats.day = time.Now().AddDate(0, 0, -1)
	nm, _ := m.Update(refreshTickMsg{gen: -1})
	m = nm.(Model)
	if m.mode == modeDetail && (m.current() == nil || m.current().ID != shown.ID) {
		t.Errorf("details still open, cursor on %s", m.cursorKey())
	}
	if m.mode == modeDetail && !strings.Contains(m.vp.View(), shown.Title) {
		t.Errorf("pane shows:\n%s", m.vp.View())
	}
	if m.mode == modeList {
		if !strings.Contains(m.flash, "details closed") {
			t.Errorf("flash %q", m.flash)
		}
		if a := press(m, "i"); a.form == nil || len(a.busy) != 0 {
			t.Error("i after details closed by the day change must ask")
		}
	}
}

func TestPopupsCloseAndCancel(t *testing.T) {
	m := sampleModel(100, 30)
	if c := press(m, "x"); c.form == nil || c.form.kind != formConfirm {
		t.Fatal("x should ask for confirmation")
	}
	if c := press(m, "x", "n"); c.form != nil || len(c.busy) != 0 {
		t.Error("n should cancel without starting anything")
	}
	if c := press(m, "x", "y"); c.form != nil || len(c.busy) != 1 {
		t.Errorf("y should start the close, busy = %v", c.busy)
	}
	c := press(m, "c", "line one", "enter", "line two")
	if got := c.form.fields[0].in.Value(); got != "line one\nline two" {
		t.Errorf("enter in a comment should add a line, got %q", got)
	}
	if c := press(m, "d", "nonsense", "enter"); c.form == nil || c.form.err == "" {
		t.Error("invalid due date should keep the pop-up open with an error")
	}
}

// Every pop-up re-renders (and parses) on each keystroke; typing odd
// input one character at a time must never panic.
func TestPopupsTypingNeverPanics(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	inputs := []string{"+", "++2w", "+-", "-", ",", ",,+,-,@", "me,", "@", "  ", "日本", "a/b#c", "+2w", "2026-1", "fri"}
	base := sampleModel(60, 24)
	nm, _ := base.Update(projectsMsg{projects: []gl.Project{{ID: 1, Name: "proj", Path: "team/proj"}}})
	base = nm.(Model)
	openers := map[string][]string{
		"assign":  {"a"},
		"due":     {"d"},
		"comment": {"c"},
		"picker":  {"n"},
		"new":     {"n", "enter"},
		"search":  {"/"},
	}
	for name, keys := range openers {
		for _, in := range inputs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s: typing %q panicked: %v", name, in, r)
					}
				}()
				m := press(base, keys...)
				for _, field := range []int{0, 1, 2, 3} {
					for _, r := range in {
						m = press(m, string(r))
						_ = m.View()
					}
					m = press(m, "tab")
					_ = field
				}
				for _, k := range []string{"backspace", "up", "down", "enter"} {
					var msg tea.KeyMsg
					switch k {
					case "backspace":
						msg = tea.KeyMsg{Type: tea.KeyBackspace}
					case "up":
						msg = tea.KeyMsg{Type: tea.KeyUp}
					case "down":
						msg = tea.KeyMsg{Type: tea.KeyDown}
					case "enter":
						msg = tea.KeyMsg{Type: tea.KeyEnter}
					}
					nm, _ := m.Update(msg)
					m = nm.(Model)
					_ = m.View()
				}
			}()
		}
	}
}

// Pop-ups must only use the terminal's 16 theme colours for text, and no
// background colours, so they look right in both light and dark themes.
func TestPopupsUseThemeColorsOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	base := sampleModel(100, 30)
	nm, _ := base.Update(projectsMsg{projects: []gl.Project{{ID: 1, Name: "proj", Path: "team/proj"}}})
	base = nm.(Model)
	for name, keys := range map[string][]string{
		"new":     {"n", "enter", "Title", "tab", "a", "tab", "+2w"},
		"picker":  {"n", "pr"},
		"assign":  {"a"},
		"due":     {"d"},
		"comment": {"c", "hi"},
		"close":   {"x"},
	} {
		out := press(base, keys...).renderForm()
		if !strings.Contains(out, "\x1b[") {
			t.Fatalf("%s: no colour codes rendered; test setup broken", name)
		}
		for _, bad := range []string{"[48;", ";48;", "[38;5;", ";38;5;"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s pop-up uses a fixed colour (%q)", name, bad)
			}
		}
	}
}

func peopleModel() Model {
	m := sampleModel(100, 30)
	m.members = []gl.User{
		{ID: 1, Username: "me", Name: "Me Myself"},
		{ID: 2, Username: "alice", Name: "Alice A"},
		{ID: 3, Username: "alicia", Name: "Alicia B"},
		{ID: 4, Username: "carol", Name: "Carol Lumen"},
	}
	return m
}

func TestAssignPicker(t *testing.T) {
	m := peopleModel()
	a := press(m, "j", "a") // issue 2: nobody assigned
	fd := func(a Model) field { return a.form.fields[0] }
	if len(fd(a).chips) != 0 {
		t.Fatalf("issue without assignees should start empty: %v", fd(a).chips)
	}
	a = press(a, "ali", "enter")
	if got := fd(a).chips; len(got) != 1 || got[0] != "alice" {
		t.Errorf("enter must add the highlighted member, not the typed text: %v", got)
	}
	a = press(a, "ali", "down", "enter")
	if got := fd(a).chips; len(got) != 2 || got[1] != "alicia" {
		t.Errorf("down should pick the second match: %v", got)
	}
	a = press(a, "zzz", "enter")
	if a.form.err == "" || len(fd(a).chips) != 2 {
		t.Errorf("unknown name must be refused, chips = %v, err = %q", fd(a).chips, a.form.err)
	}
	if out := a.renderForm(); !strings.Contains(out, "no group member matches") {
		t.Error("no-match message not shown")
	}
	a = press(a, "backspace", "backspace", "backspace", "backspace") // clear "zzz", then remove @alicia
	if got := fd(a).chips; len(got) != 1 || got[0] != "alice" {
		t.Errorf("backspace on empty box should remove the last tag: %v", got)
	}
	a = press(a, "me", "enter")
	if got := fd(a).chips; len(got) != 2 || got[1] != "me" {
		t.Errorf(`"me" should pick you: %v`, got)
	}
	a = press(a, "enter") // empty box: save
	if a.form != nil {
		t.Fatalf("enter on an empty box should save, err = %q", a.form.err)
	}
	if len(a.busy) != 1 {
		t.Errorf("save should start the assignment, busy = %v", a.busy)
	}
}

func TestMemberMatchesByWordPrefix(t *testing.T) {
	m := peopleModel()
	names := func(us []gl.User) (s []string) {
		for _, u := range us {
			s = append(s, u.Username)
		}
		return
	}
	if got := names(m.memberMatches("me", nil)); len(got) != 1 || got[0] != "me" {
		t.Errorf(`"me" should match only you, not Lu-me-n: %v`, got)
	}
	if got := names(m.memberMatches("lum", nil)); len(got) != 1 || got[0] != "carol" {
		t.Errorf("surname prefix should match: %v", got)
	}
	if got := names(m.memberMatches("al", []string{"alice"})); len(got) != 1 || got[0] != "alicia" {
		t.Errorf("already picked people should be left out: %v", got)
	}
}

func TestHalfTypedNameBlocksSave(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := peopleModel()
	nm, _ := press(m, "n").Update(projectsMsg{projects: []gl.Project{{ID: 1, Name: "proj", Path: "team/proj"}}})
	a := press(nm.(Model), "enter", "Title", "tab", "ali", "ctrl+s")
	if a.form == nil || !strings.Contains(a.form.err, "pick") || a.form.focus != 1 {
		t.Errorf("ctrl+s with a half-typed name must not guess; form = %+v", a.form)
	}
	a = press(a, "enter", "ctrl+s")
	if a.form == nil || !a.form.sending {
		t.Errorf("after picking, ctrl+s should create")
	}
}

func TestBulkAssignAdds(t *testing.T) {
	m := peopleModel()
	a := press(m, " ", " ", "a")
	if a.form.title != "Add assignees" {
		t.Errorf("bulk title = %q", a.form.title)
	}
	if a = press(a, "enter"); a.form == nil || a.form.err == "" {
		t.Error("bulk assign with nobody picked should ask for a person")
	}
}

func TestPickerFooterMatchesEnter(t *testing.T) {
	a := press(peopleModel(), "a")
	if out := a.renderForm(); !strings.Contains(out, "enter save") {
		t.Errorf("empty box: footer should say enter saves")
	}
	a = press(a, "ali")
	if out := a.renderForm(); strings.Contains(out, "enter save") || !strings.Contains(out, "adds the highlighted") {
		t.Errorf("while typing a name the footer must say enter adds, not saves:\n%s", out)
	}
}

func TestFooterGroups(t *testing.T) {
	m := sampleModel(100, 30)
	lines := renderFooter(m.footerGroups(), 100)
	var listPart, issuePart string
	inIssue := false
	for _, l := range lines {
		if strings.HasPrefix(l, "Issue") {
			inIssue = true
		}
		if inIssue {
			issuePart += l
		} else {
			listPart += l
		}
	}
	if !strings.HasPrefix(lines[0], "List") || issuePart == "" {
		t.Fatalf("footer should have List then Issue groups:\n%s", strings.Join(lines, "\n"))
	}
	for _, k := range []string{"c comment", "a assign", "x close", "d due"} {
		if strings.Contains(listPart, k) {
			t.Errorf("%q belongs on the Issue line, not the List line", k)
		}
	}
	for _, k := range []string{"s sort", "/ search", "m mine"} {
		if strings.Contains(issuePart, k) {
			t.Errorf("%q belongs on the List line", k)
		}
	}
	if sel := renderFooter(press(m, " ").footerGroups(), 100); !strings.Contains(strings.Join(sel, "\n"), "Selected") {
		t.Error("with a selection the issue group should be labelled Selected")
	}
	for _, g := range press(m, " ").footerGroups() {
		if g.label != "Selected" {
			continue
		}
		for _, k := range g.items {
			if k == "c comment" || k == "e edit" || k == "w web" {
				t.Errorf("%q acts on the cursor only; it must not be listed under Selected", k)
			}
		}
	}
	if d := renderFooter(press(m, "enter").footerGroups(), 100); !strings.HasPrefix(d[0], "Issue") {
		t.Errorf("details view should start with the Issue group: %q", d[0])
	}
}

// Every shortcut is in the footer: the paging keys too, in the list and
// in the details (the aliases ctrl+u/d, home and j/k are in the help).
func TestFooterListsPagingKeys(t *testing.T) {
	m := sampleModel(100, 30)
	for name, groups := range map[string][]keyGroup{"list": m.footerGroups(), "details": press(m, "enter").footerGroups()} {
		var items []string
		for _, g := range groups {
			items = append(items, g.items...)
		}
		for _, key := range []string{"pgup/pgdn", "g/end"} {
			if !slices.ContainsFunc(items, func(it string) bool { return strings.HasPrefix(it, key+" ") }) {
				t.Errorf("%s footer lacks %s: %v", name, key, items)
			}
		}
	}
}

// esc clears filters and the search: the footer says so while one is on.
func TestFooterEscClear(t *testing.T) {
	m := sampleModel(100, 30)
	has := func(m Model) bool {
		for _, g := range m.footerGroups() {
			if g.label == "List" && slices.Contains(g.items, "esc clear") {
				return true
			}
		}
		return false
	}
	if has(m) {
		t.Error("esc clear shown with nothing to clear")
	}
	before := m.footerLines()
	m = press(m, "m")
	if !has(m) {
		t.Error("esc clear missing while a filter is on")
	}
	m.width = 40 // narrow: the extra key may wrap, and the cached line count must follow
	if n := len(renderFooter(m.footerGroups(), 40)); m.footerLines() != n {
		t.Errorf("footerLines %d, rendered %d", m.footerLines(), n)
	}
	m.width = 100
	if m = press(m, "esc"); has(m) || m.footerLines() != before {
		t.Error("esc clear still shown after esc")
	}
	m.search = "x"
	if !has(m) {
		t.Error("esc clear missing during a search")
	}
}

func TestSelectionClearedAfterBulkAction(t *testing.T) {
	sel := func() Model { return press(peopleModel(), " ", " ") } // issues 1 and 2 selected
	if n := len(sel().selected); n != 2 {
		t.Fatalf("setup: %d selected", n)
	}
	for name, tc := range map[string]struct {
		keys  []string
		clear bool
	}{
		"close confirmed":    {[]string{"x", "y"}, true},
		"close via enter":    {[]string{"x", "enter"}, true},
		"close declined":     {[]string{"x", "n"}, false},
		"close cancelled":    {[]string{"x", "esc"}, false},
		"due set":            {[]string{"d", "fri", "enter"}, true},
		"due invalid":        {[]string{"d", "nonsense", "enter"}, false},
		"due cancelled":      {[]string{"d", "fri", "esc"}, false},
		"assign":             {[]string{"a", "ali", "enter", "enter"}, true},
		"assign cancelled":   {[]string{"a", "ali", "enter", "esc"}, false},
		"assign me":          {[]string{"i", "y"}, true},
		"assign me declined": {[]string{"i", "n"}, false},
		"comment (not bulk)": {[]string{"c", "hi", "esc"}, false},
	} {
		m := press(sel(), tc.keys...)
		if cleared := len(m.selected) == 0; cleared != tc.clear {
			t.Errorf("%s: selection cleared = %v, want %v (selected %v)", name, cleared, tc.clear, m.selected)
		}
	}
	// acting from inside an issue does not touch the list selection
	m := press(sel(), "enter", "x", "y")
	if len(m.selected) != 2 {
		t.Errorf("action in details cleared the list selection: %v", m.selected)
	}
}

// g / home and end jump to the top and bottom of the details.
func TestDetailTopBottom(t *testing.T) {
	m := sampleModel(80, 12)
	m.issues[m.view[0]].Description = strings.Repeat("line\n", 50)
	m = press(m, "enter")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = nm.(Model)
	if !m.vp.AtBottom() || m.vp.YOffset == 0 {
		t.Fatalf("end should scroll to the bottom (offset %d)", m.vp.YOffset)
	}
	if m = press(m, "g"); m.vp.YOffset != 0 {
		t.Errorf("g should scroll to the top (offset %d)", m.vp.YOffset)
	}
	nm, _ = press(m, "j", "j").Update(tea.KeyMsg{Type: tea.KeyHome})
	if off := nm.(Model).vp.YOffset; off != 0 {
		t.Errorf("home should scroll to the top (offset %d)", off)
	}
}

// A new or updated issue stands out across its row, in both views: the
// title (and number) bold in the tag's colour; other rows stay plain.
func TestFreshRowsStandOut(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	for name, keys := range map[string][]string{"cards": nil, "compact": {"z"}} {
		m := press(sampleModel(120, 40), keys...)
		m.cursor = len(m.view) - 1 // keep the cursor's own styling off the rows checked
		upd, plain := m.issues[m.view[0]], m.issues[m.view[1]]
		m.fresh = map[int]string{upd.ID: "updated"}
		out := m.View()
		line := ""
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, upd.Title[:10]) {
				line = l
				break
			}
		}
		bold := styleWarn.Bold(true).Render("x")
		on, _, _ := strings.Cut(bold, "x")
		if on == "" || !strings.Contains(line, on+upd.Title[:10]) {
			t.Errorf("%s: updated title not bold yellow: %q", name, line)
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, plain.Title[:10]) && strings.Contains(l, on+plain.Title[:10]) {
				t.Errorf("%s: plain row styled as updated: %q", name, l)
			}
		}
	}
}

// An explicit theme overrides what the terminal reports, so the cursor
// band uses the light or dark grey regardless of the multiplexer.
func TestDetectBackgroundTheme(t *testing.T) {
	defer lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
	DetectBackground("light")
	if lipgloss.HasDarkBackground() {
		t.Error("light: background reported dark")
	}
	DetectBackground("dark")
	if !lipgloss.HasDarkBackground() {
		t.Error("dark: background reported light")
	}
}

// Titles and labels with newlines or tabs from the server (decoded by gl,
// as on a real load) cannot forge rows: the view keeps its height and no
// line runs past the width, in two-line and compact rows.
func TestServerNewlinesKeepLayout(t *testing.T) {
	fakeGlab(t, `printf '%s\n' '[{"id":1,"iid":1,"state":"opened","title":"line one\nacme/fake#999   FORGED ROW\tx\ty",`+
		`"labels":["a\nb"],"references":{"full":"acme/x#1"}},`+
		`{"id":2,"iid":2,"state":"opened","title":"two\r\nrows","references":{"full":"acme/x#2"}}]'`)
	issues, err := gl.Client{Host: "gitlab.example.edu"}.GroupIssues(context.Background(), "acme", "opened")
	if err != nil {
		t.Fatal(err)
	}
	m := sampleModel(80, 20)
	nm, _ := m.Update(issuesMsg{gen: m.loadGen, state: states[m.state], issues: issues})
	m = nm.(Model)
	for _, v := range []Model{m, press(m, "z")} {
		lines := strings.Split(v.View(), "\n")
		if len(lines) != 20 {
			t.Errorf("view has %d lines, want 20:\n%s", len(lines), v.View())
		}
		for _, l := range lines {
			if lipgloss.Width(l) > 80 || strings.Contains(l, "\t") {
				t.Errorf("line too wide or with a tab: %q", l)
			}
		}
	}
}

// One colour rule for the due date wherever it is shown: the compact
// column, the card badge and the details, including for a closed issue.
func TestDueDateOneColourRule(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	now := time.Now()
	day := func(d int) string { return now.AddDate(0, 0, d).Format(time.DateOnly) }
	colour := func(st lipgloss.Style) string { on, _, _ := strings.Cut(st.Render("x"), "x"); return on }
	for _, tc := range []struct {
		name   string
		is     gl.Issue
		want   lipgloss.Style
		badge  string
		detail string
	}{
		{"none", gl.Issue{State: "opened"}, styleDim, "no due date", "none"},
		{"overdue", gl.Issue{State: "opened", DueDate: day(-3)}, styleBad, day(-3) + " · 3d overdue", day(-3) + " (3 days overdue)"},
		{"today", gl.Issue{State: "opened", DueDate: day(0)}, styleWarn, day(0) + " · today", day(0) + " (today)"},
		{"tomorrow", gl.Issue{State: "opened", DueDate: day(1)}, styleWarn, day(1) + " · tomorrow", day(1) + " (tomorrow)"},
		{"soon", gl.Issue{State: "opened", DueDate: day(7)}, styleWarn, day(7) + " · in 7d", day(7) + " (in 7 days)"},
		{"later", gl.Issue{State: "opened", DueDate: day(8)}, styleGood, day(8) + " · in 8d", day(8) + " (in 8 days)"},
		{"closed", gl.Issue{State: "closed", DueDate: day(-3)}, styleDim, day(-3) + " · 3d overdue", day(-3) + " (3 days overdue)"},
	} {
		for form, got := range map[string]string{
			"column":  dueCell(tc.is, now, 12, 7),
			"badge":   dueBadge(tc.is, now, 7),
			"details": dueDetail(tc.is, now, 7),
		} {
			if on := colour(tc.want); !strings.HasPrefix(got, on) {
				t.Errorf("%s, %s: %q not in the colour of %q", tc.name, form, got, tc.want.Render("x"))
			}
		}
		if s := ansi.Strip(dueBadge(tc.is, now, 7)); s != tc.badge {
			t.Errorf("%s: badge %q, want %q", tc.name, s, tc.badge)
		}
		if s := ansi.Strip(dueDetail(tc.is, now, 7)); s != tc.detail {
			t.Errorf("%s: details %q, want %q", tc.name, s, tc.detail)
		}
	}
	if s := ansi.Strip(dueCell(gl.Issue{State: "opened"}, now, 5, 7)); s != "—    " {
		t.Errorf("column without a due date %q", s)
	}
}

// The preview reads only the start of a description: the same one line
// as before for an ordinary description, and no pass over a multi-MB one
// on every frame.
func TestPreviewReadsOnlyTheStart(t *testing.T) {
	for _, in := range []string{"", "  ", "one", " a\n\n b\t c  ", "é  ü x", strings.Repeat("word ", 100)} {
		if got, want := collapseSpace(in, previewBytes), strings.Join(strings.Fields(in), " "); got != want {
			t.Errorf("collapseSpace(%q) = %q, want %q", in, got, want)
		}
	}
	m := sampleModel(100, 30)
	m.issues[0].Description = strings.Repeat("lorem ipsum\n\n", 400000) // ~5 MB
	m.cursor = 0
	got := collapseSpace(m.issues[0].Description, previewBytes)
	if len(got) > previewBytes+utf8.UTFMax || !strings.HasPrefix(got, "lorem ipsum lorem ipsum ") || strings.HasSuffix(got, " ") {
		t.Errorf("collapseSpace on 5 MB: %d bytes, %q…", len(got), got[:min(len(got), 40)])
	}
	short := m
	short.issues = slices.Clone(m.issues)
	short.issues[0].Description = m.issues[0].Description[:previewBytes*2]
	if a, b := ansi.Strip(m.preview()), ansi.Strip(short.preview()); a != b || !strings.Contains(a, "lorem ipsum lorem") {
		t.Errorf("preview differs from the one of the start alone:\n%q\n%q", a, b)
	}
}

// A reply to an older load of an issue's comments (the issue opened twice
// quickly, or a comment sent while the first load ran) never overwrites
// the newer one's.
func TestStaleNotesRepliesDropped(t *testing.T) {
	m := sampleModel(100, 30)
	is := *m.current()
	m.loadNotes(is) // gen 1: its reply comes late
	m.loadNotes(is) // gen 2
	nm, _ := m.Update(notesMsg{id: is.ID, gen: 1, notes: []gl.Note{{Body: "old"}}})
	m = nm.(Model)
	if m.notes[is.ID] != nil {
		t.Fatalf("stale reply applied: %+v", m.notes[is.ID])
	}
	nm, _ = m.Update(notesMsg{id: is.ID, gen: 2, notes: []gl.Note{{Body: "new"}}})
	m = nm.(Model)
	if n := m.notes[is.ID]; len(n) != 1 || n[0].Body != "new" {
		t.Errorf("current reply not applied: %+v", n)
	}
}

// A failed user lookup keeps the user we have: the "mine" filter and
// assign-me depend on it, also before the members ever loaded.
func TestMetaErrorKeepsUser(t *testing.T) {
	m := loaded(time.Now())
	m.me = gl.User{ID: 2, Username: "alice"}
	nm, _ := m.Update(metaMsg{err: errors.New("glab: timeout")})
	if got := nm.(Model); got.me.Username != "alice" || got.members != nil {
		t.Errorf("after a failed lookup: me %q, members %v", got.me.Username, got.members)
	}
}

// Member and project reloads are tagged like issue loads: a reply to a
// load that another one overtook is dropped, and only the current reply
// ends the projects' loading state.
func TestStaleListRepliesDropped(t *testing.T) {
	m := loaded(time.Now())
	m.me = gl.User{ID: 2, Username: "alice"}
	nm, _ := m.Update(listsTickMsg{}) // the loads started by Init are now stale
	m = nm.(Model)
	nm, _ = m.Update(metaMsg{me: gl.User{ID: 3, Username: "bob"}, gen: 0})
	m = nm.(Model)
	if m.me.Username != "alice" {
		t.Fatalf("stale user reply applied: %q", m.me.Username)
	}
	nm, _ = m.Update(projectsMsg{projects: []gl.Project{{ID: 1, Name: "old", Path: "team/old"}}, gen: 0})
	m = nm.(Model)
	if m.projects != nil || !m.projectsLoading {
		t.Fatalf("stale project reply applied: %v, loading %v", m.projects, m.projectsLoading)
	}
	nm, _ = m.Update(metaMsg{me: gl.User{ID: 3, Username: "bob"}, gen: m.metaGen})
	m = nm.(Model)
	nm, _ = m.Update(projectsMsg{projects: []gl.Project{{ID: 2, Name: "new", Path: "team/new"}}, gen: m.projectsGen})
	m = nm.(Model)
	if m.me.Username != "bob" || len(m.projects) != 1 || m.projects[0].Name != "new" || m.projectsLoading {
		t.Errorf("current replies not applied: me %q, projects %v, loading %v", m.me.Username, m.projects, m.projectsLoading)
	}
}
