package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gl-issues/internal/gl"
)

// The assignee picker: people already chosen are shown as @tags; typing
// lists matching group members, ↑/↓ highlights one and enter or tab adds
// the highlighted member (never the raw text). Backspace on an empty box
// removes the last tag.

const maxMatches = 5

var stylePerson = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))

// memberMatches returns group members matching q, best first, leaving
// out the usernames in exclude. You are included and match "me" too.
func (m Model) memberMatches(q string, exclude []string) []gl.User {
	q = strings.ToLower(strings.TrimLeft(strings.TrimSpace(q), "@"))
	if q == "" {
		return nil
	}
	type scored struct {
		u     gl.User
		score int
	}
	var out []scored
	for _, u := range m.members {
		if containsFold(exclude, u.Username) {
			continue
		}
		user := strings.ToLower(u.Username)
		score := -1
		switch {
		case u.Username == m.me.Username && len(q) <= 2 && "me"[:len(q)] == q: // q starts "me"
			score = 0
		case strings.HasPrefix(user, q):
			score = 1
		case nameWordHasPrefix(u.Name, q):
			score = 2
		case strings.Contains(user, q):
			score = 3
		}
		if score >= 0 {
			out = append(out, scored{u, score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score < out[j].score
		}
		return out[i].u.Username < out[j].u.Username
	})
	users := make([]gl.User, len(out))
	for i, s := range out {
		users[i] = s.u
	}
	return users
}

// nameWordHasPrefix reports whether any word of a display name starts
// with q, so "lu" finds "Carol Lumen" but "me" does not.
func nameWordHasPrefix(name, q string) bool {
	for _, w := range strings.Fields(strings.ToLower(name)) {
		if strings.HasPrefix(w, q) {
			return true
		}
	}
	return false
}

// handlePeopleKey handles the keys that mean something special in an
// assignee field. It reports whether the key was consumed; unconsumed
// keys (typing, enter on an empty box, ...) take the normal path.
func (m Model) handlePeopleKey(f *form, fd *field, msg tea.KeyMsg) bool {
	q := strings.TrimSpace(fd.in.Value())
	matches := m.memberMatches(q, fd.chips)
	n := min(len(matches), maxMatches)
	switch msg.String() {
	case "up":
		if q != "" {
			fd.pick = max(fd.pick-1, 0)
			return true
		}
	case "down":
		if q != "" {
			fd.pick = min(fd.pick+1, max(n-1, 0))
			return true
		}
	case "enter", "tab", ",":
		if q == "" {
			return msg.String() == "," // nothing typed: enter/tab move on
		}
		if n == 0 {
			f.err = "no group member matches “" + q + "”"
			return true
		}
		fd.chips = append(fd.chips, matches[min(fd.pick, n-1)].Username)
		fd.in.SetValue("")
		fd.pick = 0
		return true
	case "backspace":
		if fd.in.Value() == "" && len(fd.chips) > 0 {
			fd.chips = fd.chips[:len(fd.chips)-1]
			return true
		}
	}
	return false
}

// renderPeople draws an assignee field: tags and input on one line, then
// (when focused) the matching members and a hint.
func (m Model) renderPeople(f *form, fd field, focused bool, label string, w int) []string {
	const labelW = 12
	bar := styleFieldBar.Render("▏")
	if focused {
		bar = styleFieldBarFocused.Render("▎")
	}
	var tags []string
	for _, c := range fd.chips {
		tags = append(tags, stylePerson.Render("@"+c))
	}
	line := label + bar + " "
	if len(tags) > 0 {
		line += strings.Join(tags, " ") + " "
	} else if !focused || fd.in.Value() == "" {
		line += styleWarn.Render("nobody") + " "
	}
	rest := max(w-lipgloss.Width(line)-1, 8)
	in := fd.in
	if len(fd.chips) > 0 || focused {
		in.Placeholder = ""
	}
	line += in.render(rest, focused)
	out := []string{line}
	if !focused {
		return out
	}

	indent := strings.Repeat(" ", labelW+2)
	q := strings.TrimSpace(fd.in.Value())
	switch {
	case q == "":
		next := "enter save"
		if f.kind == formNewIssue {
			next = "enter next field"
		}
		hint := "type a name to add"
		if len(fd.chips) > 0 {
			hint += " · backspace removes the last"
		}
		out = append(out, indent+styleDim.Render(hint+" · "+next))
	case len(m.members) == 0:
		out = append(out, indent+styleDim.Render(m.spin.View()+" loading group members …"))
	default:
		matches := m.memberMatches(q, fd.chips)
		if len(matches) == 0 {
			out = append(out, indent+styleWarn.Render("no group member matches “"+q+"”"))
			break
		}
		pick := min(fd.pick, min(len(matches), maxMatches)-1)
		for i, u := range matches[:min(len(matches), maxMatches)] {
			name := u.Name
			if u.Username == m.me.Username {
				name += " (you)"
			}
			if i == pick {
				out = append(out, indent[:len(indent)-2]+styleFieldBarFocused.Render("▸ ")+
					styleFocusLabel.Render(u.Username)+"  "+styleDim.Render(name))
			} else {
				out = append(out, indent+u.Username+"  "+styleDim.Render(name))
			}
		}
		more := ""
		if len(matches) > maxMatches {
			more = " · " + fmt.Sprint(len(matches)-maxMatches) + " more, keep typing"
		}
		out = append(out, indent+styleDim.Render("↑↓ choose · enter add"+more))
	}
	return out
}
