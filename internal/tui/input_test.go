package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestLineInputMultiline(t *testing.T) {
	l := lineInput{Multiline: true}
	l.SetValue("first line\nsecond")
	l.Update(tea.KeyMsg{Type: tea.KeyUp})
	if l.pos != 6 {
		t.Errorf("up: pos = %d, want 6 (same column on line 1)", l.pos)
	}
	l.Update(tea.KeyMsg{Type: tea.KeyEnd})
	l.Insert('!')
	if got := l.Value(); got != "first line!\nsecond" {
		t.Errorf("value = %q", got)
	}
	l.Update(tea.KeyMsg{Type: tea.KeyDown})
	l.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := l.Value(); got != "first line!\n" {
		t.Errorf("ctrl+u on line 2: %q", got)
	}
}

func TestRenderBox(t *testing.T) {
	l := lineInput{Multiline: true}
	l.SetValue("abcdefghij\nxy")
	box := l.renderBox(5, 4, false)
	want := []string{"abcd", "efgh", "ij", "xy"}
	for i := range want {
		if box[i] != want[i] {
			t.Errorf("line %d = %q, want %q (box %q)", i, box[i], want[i], box)
		}
	}
	// cursor at the end must stay visible when the box is short
	box = l.renderBox(5, 2, true)
	if !strings.HasPrefix(ansi.Strip(box[1]), "xy") {
		t.Errorf("cursor line not visible: %q", box)
	}
	for _, line := range l.renderBox(5, 3, true) {
		if w := ansi.StringWidth(line); w > 5 {
			t.Errorf("line %q is %d wide", line, w)
		}
	}
}

// Pasted text never reaches the screen raw: line endings are normalized,
// escape sequences and control characters dropped, and a single-line
// field gets spaces instead of line breaks.
func TestPasteIsCleaned(t *testing.T) {
	paste := func(l lineInput, s string) string {
		l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Paste: true})
		return l.Value()
	}
	in := "one\r\ntwo\rthree\x1b[2J\x07\tend\u202e"
	if got, want := paste(lineInput{Multiline: true}, in), "one\ntwo\nthree    end"; got != want {
		t.Errorf("multi-line paste = %q, want %q", got, want)
	}
	if got, want := paste(lineInput{}, in), "one two three end"; got != want {
		t.Errorf("single-line paste = %q, want %q", got, want)
	}
}

// A single-line field scrolls by terminal cells, not runes: wide
// characters never push it past its width, and the cursor stays visible.
func TestRenderWideRunes(t *testing.T) {
	l := lineInput{}
	l.SetValue("日本語のテキストです")
	for w := 3; w <= 12; w++ {
		for pos := 0; pos <= len(l.value); pos++ {
			l.pos = pos
			got := ansi.Strip(l.render(w, true))
			if cw := ansi.StringWidth(got); cw > w {
				t.Errorf("w %d pos %d: %q is %d cells", w, pos, got, cw)
			}
			if pos < len(l.value) && !strings.ContainsRune(got, l.value[pos]) {
				t.Errorf("w %d pos %d: cursor rune %q not shown in %q", w, pos, l.value[pos], got)
			}
		}
	}
	l.SetValue("abcdefghij")
	if got := ansi.Strip(l.render(5, true)); got != "ghij " {
		t.Errorf("ASCII at the end: %q", got)
	}
	l.pos = 0
	if got := ansi.Strip(l.render(5, true)); got != "abcde" {
		t.Errorf("ASCII at the start: %q", got)
	}
}

// A text box wraps by terminal cells, not runes: a line of wide
// characters is split where it fills the width, and the cursor stays on
// its rune.
func TestRenderBoxWideRunes(t *testing.T) {
	l := lineInput{Multiline: true}
	l.SetValue("日本語のテキスト\nab")
	want := []string{"日本", "語の", "テキ", "スト", "ab"}
	for i, line := range l.renderBox(5, 5, false) {
		if line != want[i] {
			t.Errorf("line %d = %q, want %q", i, line, want[i])
		}
	}
	for pos := 0; pos <= len(l.value); pos++ {
		l.pos = pos
		shown := false
		for _, line := range l.renderBox(5, 5, true) {
			if w := ansi.StringWidth(line); w > 5 {
				t.Errorf("pos %d: %q is %d cells", pos, line, w)
			}
			shown = shown || (pos < len(l.value) && strings.ContainsRune(ansi.Strip(line), l.value[pos]))
		}
		if pos < len(l.value) && l.value[pos] != '\n' && !shown {
			t.Errorf("pos %d: cursor rune %q not shown", pos, l.value[pos])
		}
	}
}
