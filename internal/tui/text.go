package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"gl-issues/internal/gl"
)

// Small helpers for fitting text into terminal cells.

// clip cuts a possibly styled single line to w cells.
func clip(s string, w int) string {
	return lipgloss.NewStyle().MaxWidth(w).Render(s)
}

// collapseSpace is strings.Join(strings.Fields(s), " ") stopped after
// about n bytes: a one-line preview of a long description needs only
// its start, not the whole text split and joined on every frame.
func collapseSpace(s string, n int) string {
	var b strings.Builder
	space := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsSpace(r) {
			space = b.Len() > 0
		} else {
			if b.Len() >= n {
				break
			}
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// highlight clips a possibly styled single line to w cells, pads it to
// exactly w and paints styleCursorRow's background behind all of it. The
// background is re-applied after every reset of the inner styles, so it
// spans the whole row, not just the unstyled gaps. Without a colour
// profile it only pads.
func highlight(s string, w int) string {
	s = clip(s, w)
	s += strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
	on, _, _ := strings.Cut(styleCursorRow.Render("x"), "x")
	if on == "" {
		return s
	}
	for _, reset := range []string{"\x1b[0m", "\x1b[m"} {
		s = strings.ReplaceAll(s, reset, reset+on)
	}
	return on + s + "\x1b[0m"
}

// ago formats a duration compactly: "5m", "3h", "2d", "6w", "1y".
func ago(t, now time.Time) string {
	d := max(now.Sub(t), 0) // a server clock ahead of ours
	switch {
	case t.IsZero():
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dw", int(d.Hours()/24/7))
	}
	return fmt.Sprintf("%dy", int(d.Hours()/24/365))
}

// fit pads or truncates s, one line, to exactly w cells, truncating on
// the right.
// It cuts between grapheme clusters, as lipgloss.Width counts them: an
// emoji joined with U+200D or a heart with its variation selector is one
// cluster, and would be miscounted rune by rune.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := lipgloss.Width(s)
	if sw <= w {
		return s + strings.Repeat(" ", w-sw)
	}
	// keep the clusters at the start that fit in w-1 cells, before the "…"
	s = ansi.Truncate(s, w, "…")
	return s + strings.Repeat(" ", max(w-lipgloss.Width(s), 0))
}

// fitLeft is like fit but truncates on the left, keeping the end. Like
// fit it counts cells, so a wide (CJK) name takes no more than w columns.
func fitLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := lipgloss.Width(s)
	if sw > w {
		mark, room := "…", w-1
		if w <= 1 {
			mark, room = "", w
		}
		// keep the clusters at the end that fit in room cells
		cl := clusters(s)
		i, n := len(cl), 0
		for ; i > 0; i-- {
			cw := lipgloss.Width(cl[i-1])
			if n+cw > room {
				break
			}
			n += cw
		}
		s, sw = mark+strings.Join(cl[i:], ""), lipgloss.Width(mark)+n
	}
	return s + strings.Repeat(" ", w-sw)
}

// clusters splits s into its grapheme clusters, each one or more runes
// that draw as one unit.
func clusters(s string) []string {
	var cl []string
	for s != "" {
		c, _ := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		if c == "" { // cannot happen for a non-empty string; never loop forever
			c = s
		}
		cl = append(cl, c)
		s = s[len(c):]
	}
	return cl
}

// truncate shortens s to at most w runes. Only safe for unstyled text or
// with a generous w.
func truncate(s string, w int) string {
	r := []rune(s)
	if w <= 0 || len(r) <= w {
		return s
	}
	return string(r[:w])
}

// shortPath fits a slash-separated path into w cells by replacing middle
// segments with "…", keeping the first and as many trailing segments as
// fit: "platform/weather/sensors/logger/fw" -> "platform/…/logger/fw".
func shortPath(p string, w int) string {
	if lipgloss.Width(p) <= w {
		return p
	}
	segs := strings.Split(p, "/")
	for keep := len(segs) - 2; keep >= 1; keep-- {
		s := segs[0] + "/…/" + strings.Join(segs[len(segs)-keep:], "/")
		if lipgloss.Width(s) <= w {
			return s
		}
	}
	if s := "…/" + segs[len(segs)-1]; lipgloss.Width(s) <= w {
		return s
	}
	return fitLeft(p, w)
}

// relPath is path relative to group, or path itself outside it.
func relPath(path, group string) string {
	rel, _ := gl.RelPath(path, group)
	return rel
}
