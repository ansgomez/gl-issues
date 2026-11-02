package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gl-issues/internal/gl"
)

const todoJSON = `[
 {"id":1,"action_name":"mentioned","target_type":"Issue","project":{"path_with_namespace":"team/proj"},"target":{"iid":1}},
 {"id":2,"action_name":"directly_addressed","target_type":"Issue","project":{"path_with_namespace":"team/proj"},"target":{"iid":3}},
 {"id":3,"action_name":"assi\u001b[31mgned","target_type":"Issue","project":{"path_with_namespace":"team/gone"},"target":{"iid":8}},
 {"id":4,"action_name":"assigned","target_type":"Issue","project":{"path_with_namespace":"other/x"},"target":{"iid":1}}
]`

func todoGlab(t *testing.T) string {
	return fakeGlab(t, `case "$*" in *todos*) echo '`+todoJSON+`';; *groups/team) echo '{"id":42,"full_path":"team"}';; esac`)
}

func TestTodosCountAndTags(t *testing.T) {
	log := todoGlab(t)
	m := sampleModel(120, 30)
	msg := m.loadTodos()().(todosMsg)
	if msg.err != nil || msg.groupID != 42 {
		t.Fatalf("fetch: %+v", msg)
	}
	got := calls(t, log)
	if !strings.Contains(got, "todos?group_id=42&page=1&per_page=100&state=pending&type=Issue") || strings.Contains(got, "POST") {
		t.Errorf("want one read-only To-Do request for group 42:\n%s", got)
	}
	nm, _ := m.Update(msg)
	m = nm.(Model)
	if m.todoCount != 3 { // other/x is outside the group
		t.Errorf("count = %d, want 3", m.todoCount)
	}
	if got := m.todos["team/gone#8"]; len(got) != 1 || got[0] != "assigned" {
		t.Errorf("server text must be sanitized: %q", got)
	}
	if title := m.titleLine(); !strings.Contains(title, "To-Do 3") {
		t.Errorf("title lacks the count: %q", title)
	}
	out := m.View()
	if !strings.Contains(out, "todo: mentioned") || !strings.Contains(out, "todo: directly_addressed") {
		t.Errorf("listed issues not tagged:\n%s", out)
	}
	if out := press(m, "z").View(); !strings.Contains(out, "todo") {
		t.Errorf("compact rows not tagged:\n%s", out)
	}
	if d := press(m, "enter"); !strings.Contains(d.vp.View(), "to-do") || !strings.Contains(d.vp.View(), "mentioned") {
		t.Errorf("details lack the To-Do:\n%s", d.vp.View())
	}

	// The group ID is cached: the next fetch is a single request.
	log = todoGlab(t)
	m.loadTodos()()
	if got := calls(t, log); strings.Count(got, "\n") != 1 || !strings.Contains(got, "todos?") {
		t.Errorf("second fetch should be one request:\n%s", got)
	}
	// Reloads fetch To-Dos too.
	if press(m, "r").loadGen == m.loadGen {
		t.Error("r should reload")
	}

	// The title never overflows at narrow widths.
	for _, w := range []int{30, 40, 60} {
		nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		for _, v := range []Model{nm.(Model), press(nm.(Model), "z")} {
			for _, l := range strings.Split(v.View(), "\n") {
				if lw := lipgloss.Width(l); lw > w {
					t.Errorf("width %d: line is %d wide: %q", w, lw, l)
				}
			}
		}
	}
}

func TestTodosFailureAndOff(t *testing.T) {
	fakeGlab(t, `echo 'HTTP 500' >&2; exit 1`)
	m := sampleModel(120, 30)
	nm, _ := m.Update(m.loadTodos()())
	m = nm.(Model)
	if !strings.Contains(m.titleLine(), "To-Do ?") || len(m.issues) != 3 {
		t.Errorf("a failed fetch shows ? and leaves the list: %q", m.titleLine())
	}

	off := flatOptions()
	off.Todos = false
	m = New(m.client, "team", off)
	if m.loadTodos() != nil {
		t.Error("todos = false must not fetch")
	}
	m.todoErr = nil
	if strings.Contains(m.titleLine(), "To-Do") {
		t.Error("todos = false shows no count")
	}
	// Zero To-Dos show nothing.
	m = sampleModel(120, 30)
	nm, _ = m.Update(todosMsg{groupID: 1})
	if strings.Contains(nm.(Model).titleLine(), "To-Do") {
		t.Error("no To-Dos: nothing in the title")
	}
}

// A full page is followed by the next, up to gl.MaxTodoPages; past that
// the title says the count is only the first pages'.
func TestTodosPaginated(t *testing.T) {
	var page strings.Builder
	page.WriteString("[")
	for i := range 100 {
		if i > 0 {
			page.WriteString(",")
		}
		fmt.Fprintf(&page, `{"id":%d,"action_name":"mentioned","target_type":"Issue","project":{"path_with_namespace":"team/proj"},"target":{"iid":%d}}`, i+1, i+1)
	}
	page.WriteString("]")
	log := fakeGlab(t, `case "$*" in *todos*) echo '`+page.String()+`';; *groups/team) echo '{"id":42,"full_path":"team"}';; esac`)
	m := sampleModel(120, 30)
	msg := m.loadTodos()().(todosMsg)
	if msg.err != nil || !msg.truncated || len(msg.todos) != 100*gl.MaxTodoPages {
		t.Fatalf("err %v, truncated %v, %d To-Dos; want the first %d pages", msg.err, msg.truncated, len(msg.todos), gl.MaxTodoPages)
	}
	got := calls(t, log)
	if n := strings.Count(got, "todos?"); n != gl.MaxTodoPages+1 { // the pages, then one To-Do past them
		t.Errorf("%d To-Do requests, want %d:\n%s", n, gl.MaxTodoPages+1, got)
	}
	if !strings.Contains(got, "page=2&") || strings.Contains(got, fmt.Sprintf("page=%d&", gl.MaxTodoPages+1)) ||
		!strings.Contains(got, fmt.Sprintf("page=%d&per_page=1&", gl.MaxTodoPages*100+1)) {
		t.Errorf("pages requested:\n%s", got)
	}
	nm, _ := m.Update(msg)
	m = nm.(Model)
	if title := m.titleLine(); !strings.Contains(title, fmt.Sprintf("To-Do %d+", 100*gl.MaxTodoPages)) {
		t.Errorf("title must mark the count as a lower bound: %q", title)
	}
	// A complete fetch clears the mark.
	nm, _ = m.Update(todosMsg{groupID: 42, todos: msg.todos[:3]})
	if title := nm.(Model).titleLine(); !strings.Contains(title, "To-Do 3") || strings.Contains(title, "+") {
		t.Errorf("title after a complete fetch: %q", title)
	}
}

// A reply to an older fetch never overwrites a newer one's: To-Dos go
// with each issue load and carry its loadGen.
func TestTodosStaleReplyDropped(t *testing.T) {
	todoGlab(t)
	m := sampleModel(120, 30)
	if msg := m.loadTodos()().(todosMsg); msg.gen != m.loadGen {
		t.Errorf("fetch tagged %d, loadGen is %d", msg.gen, m.loadGen)
	}
	var td gl.Todo
	td.ActionName, td.Project.Path, td.Target.IID = "assigned", "team/proj", 1
	nm, _ := m.Update(todosMsg{gen: m.loadGen, groupID: 42, todos: []gl.Todo{td}})
	m = nm.(Model)
	m.loadGen++ // a reload started since
	nm, _ = m.Update(todosMsg{gen: m.loadGen - 1, groupID: 42, err: errors.New("old")})
	if m = nm.(Model); m.todoCount != 1 || m.todoErr != nil || len(m.todos["team/proj#1"]) != 1 {
		t.Errorf("a stale reply changed the To-Dos: count %d, err %v", m.todoCount, m.todoErr)
	}
}
