package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gl-issues/internal/gl"
)

// lineInput is a minimal text field with readline-style keys. With
// Multiline set it holds several lines (the caller inserts "\n" on enter)
// and up/down move between lines. bubbles/textinput would pull in a
// clipboard dependency we do not need.
type lineInput struct {
	Prompt      string
	Placeholder string
	Multiline   bool
	value       []rune
	pos         int
}

func (l *lineInput) SetValue(s string) { l.value, l.pos = []rune(s), len([]rune(s)) }
func (l lineInput) Value() string      { return string(l.value) }

// Insert adds runes at the cursor.
func (l *lineInput) Insert(r ...rune) {
	tail := append([]rune{}, l.value[l.pos:]...)
	l.value = append(append(l.value[:l.pos], r...), tail...)
	l.pos += len(r)
}

func (l *lineInput) Update(msg tea.KeyMsg) {
	switch msg.Type {
	case tea.KeyRunes: // typed or pasted
		l.Insert(l.clean(msg.Runes)...)
	case tea.KeySpace:
		l.Insert(' ')
	case tea.KeyBackspace:
		if l.pos > 0 {
			l.value = append(l.value[:l.pos-1], l.value[l.pos:]...)
			l.pos--
		}
	case tea.KeyDelete, tea.KeyCtrlD:
		if l.pos < len(l.value) {
			l.value = append(l.value[:l.pos], l.value[l.pos+1:]...)
		}
	case tea.KeyLeft, tea.KeyCtrlB:
		l.pos = max(l.pos-1, 0)
	case tea.KeyRight, tea.KeyCtrlF:
		l.pos = min(l.pos+1, len(l.value))
	case tea.KeyHome, tea.KeyCtrlA:
		l.pos = l.lineStart(l.pos)
	case tea.KeyEnd:
		l.pos = l.lineEnd(l.pos)
	case tea.KeyUp:
		if l.Multiline {
			l.moveLine(-1)
		}
	case tea.KeyDown:
		if l.Multiline {
			l.moveLine(1)
		}
	case tea.KeyCtrlU:
		s := l.lineStart(l.pos)
		l.value, l.pos = append(l.value[:s], l.value[l.pos:]...), s
	case tea.KeyCtrlK:
		e := l.lineEnd(l.pos)
		l.value = append(l.value[:l.pos], l.value[e:]...)
	case tea.KeyCtrlW:
		i := l.pos
		for i > 0 && l.value[i-1] == ' ' {
			i--
		}
		for i > 0 && l.value[i-1] != ' ' && l.value[i-1] != '\n' {
			i--
		}
		l.value, l.pos = append(l.value[:i], l.value[l.pos:]...), i
	}
}

// clean makes typed or pasted text safe to hold and draw: CR LF and CR
// become LF, escape sequences and other control characters are dropped
// (gl.Sanitize), a tab becomes spaces, and a single-line field gets
// spaces instead of line breaks.
func (l lineInput) clean(r []rune) []rune {
	s := string(r)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = gl.Sanitize(s)
	if l.Multiline {
		s = strings.ReplaceAll(s, "\t", "    ")
	} else {
		s = strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
	}
	return []rune(s)
}

func (l lineInput) lineStart(p int) int {
	for p > 0 && l.value[p-1] != '\n' {
		p--
	}
	return p
}

func (l lineInput) lineEnd(p int) int {
	for p < len(l.value) && l.value[p] != '\n' {
		p++
	}
	return p
}

// moveLine moves the cursor to the previous (-1) or next (+1) line,
// keeping the column where possible.
func (l *lineInput) moveLine(dir int) {
	start := l.lineStart(l.pos)
	col := l.pos - start
	if dir < 0 {
		if start == 0 {
			l.pos = 0
			return
		}
		prev := l.lineStart(start - 1)
		l.pos = min(prev+col, start-1)
		return
	}
	end := l.lineEnd(l.pos)
	if end == len(l.value) {
		l.pos = end
		return
	}
	next := end + 1
	l.pos = min(next+col, l.lineEnd(next))
}

var styleInputCursor = lipgloss.NewStyle().Reverse(true)

// View renders the prompt and value on one line (used for search).
func (l lineInput) View() string {
	return styleTitle.Render(l.Prompt) + l.render(0, true)
}

// render draws a single-line field in at most w cells (0 = unlimited),
// scrolling horizontally to keep the cursor visible.
func (l lineInput) render(w int, focused bool) string {
	if len(l.value) == 0 {
		cur := ""
		if focused {
			cur = styleInputCursor.Render(" ")
		}
		ph := l.Placeholder
		if w > 0 {
			ph = fit(ph, max(w-1, 0))
		}
		return cur + styleDim.Render(ph)
	}
	val, pos := l.value, l.pos
	if w > 0 && runesWidth(val)+1 > w { // keep the cursor in view
		start, end, used := pos, pos, 1 // the cursor cell past the end
		if pos < len(val) {
			end, used = pos+1, max(runeWidth(val[pos]), 1)
		}
		for start > 0 && used+runeWidth(val[start-1]) <= w {
			start--
			used += runeWidth(val[start])
		}
		for end < len(val) && used+runeWidth(val[end]) <= w {
			used += runeWidth(val[end])
			end++
		}
		val, pos = val[start:end], pos-start
	}
	return drawCursor(val, pos, focused)
}

// runeWidth is the number of terminal cells r takes (2 for wide CJK, 0
// for combining marks).
func runeWidth(r rune) int { return ansi.StringWidth(string(r)) }

func runesWidth(rs []rune) int { return ansi.StringWidth(string(rs)) }

func drawCursor(val []rune, pos int, focused bool) string {
	if !focused {
		return string(val)
	}
	cur := " "
	if pos < len(val) {
		cur = string(val[pos])
	}
	s := string(val[:pos]) + styleInputCursor.Render(cur)
	if pos < len(val) {
		s += string(val[pos+1:])
	}
	return s
}

// renderBox draws a multi-line field as exactly h lines of at most w
// cells, soft-wrapping long lines and scrolling to keep the cursor
// visible.
func (l lineInput) renderBox(w, h int, focused bool) []string {
	w = max(w, 2)
	type vline struct {
		runes []rune
		start int // index of runes[0] in value
	}
	var lines []vline
	curLine, curCol := 0, 0
	for start := 0; ; {
		end := l.lineEnd(start)
		for s := start; ; { // split one logical line into chunks of w-1 cells
			e, used := s, 0 // at least one rune per chunk, however wide
			for e < end && (e == s || used+runeWidth(l.value[e]) <= w-1) {
				used += runeWidth(l.value[e])
				e++
			}
			lines = append(lines, vline{l.value[s:e], s})
			if l.pos >= s && (l.pos < e || l.pos == end) {
				curLine, curCol = len(lines)-1, l.pos-s
			}
			if e >= end {
				break
			}
			s = e
		}
		if end >= len(l.value) {
			break
		}
		start = end + 1
	}
	if len(l.value) == 0 {
		ph := ""
		if focused {
			ph = styleInputCursor.Render(" ")
		}
		out := []string{ph + styleDim.Render(fit(l.Placeholder, w-1))}
		for len(out) < h {
			out = append(out, "")
		}
		return out
	}
	top := max(curLine-h+1, 0)
	var out []string
	for i := top; i < len(lines) && len(out) < h; i++ {
		if i == curLine {
			out = append(out, drawCursor(lines[i].runes, curCol, focused))
		} else {
			out = append(out, string(lines[i].runes))
		}
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}
