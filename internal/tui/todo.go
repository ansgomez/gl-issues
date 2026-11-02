package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
)

// Your pending GitLab To-Dos on issues of the group: a count in the title,
// a tag on listed issues and the reasons in the details. Read-only: they
// are fetched with each list load and never marked done. A failure never
// holds up the issue list.

type todosMsg struct {
	gen       int // loadGen when the fetch started; older replies are dropped
	groupID   int // resolved group ID, cached for the next fetch
	todos     []gl.Todo
	truncated bool // more than gl.MaxTodoPages pages: todos holds the first ones
	err       error
}

// loadTodos fetches the pending To-Dos, resolving the group's ID on first
// use. It is nil when To-Dos are turned off. The fetch goes with each
// issue load and is tagged with its loadGen.
func (m Model) loadTodos() tea.Cmd {
	if !m.opts.Todos {
		return nil
	}
	c, group, id, gen := m.client, m.group, m.todoGroupID, m.loadGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		if id == 0 {
			g, err := c.Group(ctx, group)
			if err != nil {
				return todosMsg{gen: gen, err: err}
			}
			id = g.ID
		}
		todos, truncated, err := c.PendingIssueTodos(ctx, id, group)
		return todosMsg{gen: gen, groupID: id, todos: todos, truncated: truncated, err: err}
	}
}

// applyTodos installs a fetched To-Do list. On failure the count shows
// "?" but the tags from the last good fetch stay.
func (m *Model) applyTodos(msg todosMsg) {
	if msg.groupID != 0 {
		m.todoGroupID = msg.groupID
	}
	m.todoErr = msg.err
	if msg.err != nil {
		return
	}
	m.todos = map[string][]string{}
	for _, t := range msg.todos {
		k := t.Key()
		m.todos[k] = append(m.todos[k], t.ActionName)
	}
	m.todoCount, m.todoMore = len(msg.todos), msg.truncated
	m.refreshDetail()
}

// issueTodos returns the To-Do actions you have on an issue.
func (m Model) issueTodos(is gl.Issue) []string {
	return m.todos[gl.IssueKey(is.Project(), is.IID)]
}

// todoTitle is the To-Do part of the title line: "To-Do N" while there
// are any ("To-Do N+" when only the first N were fetched), "To-Do ?" when
// they could not be fetched, else nothing.
func (m Model) todoTitle() string {
	switch {
	case !m.opts.Todos:
		return ""
	case m.todoErr != nil:
		return styleDim.Render("To-Do ?")
	case m.todoMore:
		return styleWarn.Bold(true).Render(fmt.Sprintf("To-Do %d+", m.todoCount))
	case m.todoCount > 0:
		return styleWarn.Bold(true).Render(fmt.Sprintf("To-Do %d", m.todoCount))
	}
	return ""
}

// todoTag marks a listed issue you have a To-Do on: "todo: mentioned"
// when the reason is short, else "todo".
func todoTag(actions []string) string {
	if len(actions) == 0 {
		return ""
	}
	s := "todo"
	if a := strings.TrimSpace(actions[0]); a != "" && len(a) <= 20 {
		s += ": " + a
	}
	return styleWarn.Render(s) + " "
}
