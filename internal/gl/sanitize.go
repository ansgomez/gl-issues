package gl

import (
	"strings"
	"unicode"
)

// Sanitize makes text from GitLab safe to print on a terminal. Invalid
// UTF-8 bytes (such as a raw 0x9b, an 8-bit CSI) become U+FFFD first. Then
// it removes ANSI escape sequences (CSI, OSC, DCS, ... and two-byte ESC
// sequences), the C0 control characters except newline and tab, DEL, the
// C1 control characters, and Unicode format characters (category Cf:
// bidi controls, BOM, word joiner and other invisible characters), which
// could make text look different from what it is. The joiners that
// emoji sequences and some scripts need (see keepFormat) are kept, up to
// maxJoins per cluster, as is everything else, including any other
// Unicode text.
func Sanitize(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if isClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	r := []rune(s)
	joins, bases := 0, 0 // joiners in the current cluster; base runes since the last one (see maxJoins)
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == 0x1b: // ESC
			i = skipEscape(r, i)
		case c == 0x9b: // C1 CSI
			i = skipCSI(r, i+1)
		case c == 0x90 || c == 0x98 || c == 0x9d || c == 0x9e || c == 0x9f: // C1 DCS, SOS, OSC, PM, APC
			i = skipString(r, i+1)
		case c == '\n' || c == '\t':
			bases++
			b.WriteRune(c)
		case c < 0x20 || c == 0x7f || (c >= 0x80 && c <= 0x9f) || isFormat(c):
			// drop other control and format characters
		case c == '\u200d': // ZWJ
			if bases > 1 { // a cluster continues only as base, marks, ZWJ: this starts a new one
				joins = 0
			}
			bases = 0
			joins++
			if joins <= maxJoins {
				b.WriteRune(c)
			}
		default:
			if !isExtend(c) {
				bases++
			}
			b.WriteRune(c)
		}
	}
	return b.String()
}

// SanitizeLine is Sanitize for a single-line field (title, label, name,
// path, ...): newlines, carriage returns and tabs become spaces, so text
// from the server can never forge extra rows or columns. Only multi-line
// text (descriptions, comment bodies) keeps its newlines.
func SanitizeLine(s string) string {
	s = Sanitize(s)
	if !strings.ContainsAny(s, "\n\r\t") {
		return s
	}
	return strings.Map(func(c rune) rune {
		if c == '\n' || c == '\r' || c == '\t' {
			return ' '
		}
		return c
	}, s)
}

func isClean(s string) bool {
	if strings.Count(s, "\u200d") > maxJoins {
		return false // maybe one cluster of too many joiners
	}
	for _, c := range s {
		if (c < 0x20 && c != '\n' && c != '\t') || (c >= 0x7f && c <= 0x9f) || isFormat(c) {
			return false
		}
	}
	return true
}

// keepFormat lists the format characters (category Cf) Sanitize keeps:
// the zero width joiner (emoji sequences such as family or flag emoji,
// and Indic scripts), the zero width non-joiner (Persian and other
// cursive scripts) and the soft hyphen. Each only shapes the text
// around it; none moves or hides other text.
const keepFormat = "\u200d\u200c\u00ad"

// isFormat reports whether c is a format character Sanitize drops.
func isFormat(c rune) bool {
	return unicode.Is(unicode.Cf, c) && !strings.ContainsRune(keepFormat, c)
}

// maxJoins is the most zero width joiners Sanitize keeps in one grapheme
// cluster. The longest emoji sequences (a family, a kiss with skin tones)
// have three; after four, a joiner is dropped. Otherwise a title could
// chain a hundred emoji into one cluster, which width counting takes for
// two or three cells while the terminal draws every emoji in it: the
// row overflows and the columns after it shift.
const maxJoins = 4

// isExtend reports whether c extends the grapheme cluster before it
// (for the joiner cap): a combining mark, a variation selector, an emoji
// skin tone modifier or the zero width non-joiner.
func isExtend(c rune) bool {
	return unicode.In(c, unicode.Mn, unicode.Me, unicode.Mc) || (c >= 0x1f3fb && c <= 0x1f3ff) || c == '\u200c'
}

// skipEscape skips the sequence starting with ESC at r[i] and returns the
// index of its last rune.
func skipEscape(r []rune, i int) int {
	if i+1 >= len(r) {
		return i
	}
	switch r[i+1] {
	case '[':
		return skipCSI(r, i+2)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC: run to the terminator
		return skipString(r, i+2)
	}
	// ESC, intermediate bytes (0x20-0x2f) and one final byte (0x30-0x7e,
	// ECMA-48). Anything else (text, a newline, the end) is not part of a
	// sequence: a lone ESC is dropped alone, and the text after it kept.
	j := i + 1
	for j < len(r) && r[j] >= 0x20 && r[j] <= 0x2f {
		j++
	}
	if j < len(r) && r[j] >= 0x30 && r[j] <= 0x7e {
		return j
	}
	return i
}

// skipCSI skips parameter and intermediate bytes from r[i] up to and
// including the final byte (0x40-0x7e), returning the final byte's index.
func skipCSI(r []rune, i int) int {
	for ; i < len(r); i++ {
		if r[i] >= 0x40 && r[i] <= 0x7e {
			return i
		}
		if r[i] < 0x20 || r[i] > 0x7e {
			return i // malformed: drop this rune too
		}
	}
	return len(r) - 1
}

// skipString skips a control string (OSC, DCS, ...) from r[i] to its
// terminator: BEL, ESC \ or C1 ST. Unterminated strings run to the end.
func skipString(r []rune, i int) int {
	for ; i < len(r); i++ {
		switch {
		case r[i] == 0x07 || r[i] == 0x9c:
			return i
		case r[i] == 0x1b && i+1 < len(r) && r[i+1] == '\\':
			return i + 1
		}
	}
	return len(r) - 1
}

// sanitizer is implemented by the API types: it cleans every string
// field that came from the server, with SanitizeLine for all but the
// multi-line description and note body.
type sanitizer interface{ sanitize() }

func (u *User) sanitize() {
	u.Username, u.Name = SanitizeLine(u.Username), SanitizeLine(u.Name)
}

func (n *Note) sanitize() {
	n.Body = Sanitize(n.Body)
	n.Author.sanitize()
}

func (p *Project) sanitize() {
	p.Name, p.Path = SanitizeLine(p.Name), SanitizeLine(p.Path)
}

func (i *Issue) sanitize() {
	i.Title = SanitizeLine(i.Title)
	i.RawDescription = i.Description
	i.Description = Sanitize(i.Description)
	i.State = SanitizeLine(i.State)
	i.DueDate = SanitizeLine(i.DueDate)
	i.WebURL = SanitizeLine(i.WebURL)
	i.References.Full = SanitizeLine(i.References.Full)
	for k := range i.Labels {
		i.Labels[k] = SanitizeLine(i.Labels[k])
	}
	for k := range i.Assignees {
		i.Assignees[k].sanitize()
	}
	i.Author.sanitize()
}

// HasControl reports whether s holds a character Sanitize would drop
// other than carriage return: C0 other than tab and newline, DEL, C1 (ESC
// among them) or a format character outside keepFormat (bidi controls).
func HasControl(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || (r >= 0x7f && r <= 0x9f) || isFormat(r) {
			return true
		}
	}
	return false
}
