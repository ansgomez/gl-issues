package gl

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseRef(t *testing.T) {
	for _, tc := range []struct {
		in   string
		proj string
		iid  int
	}{
		{"team/sub/proj#12", "team/sub/proj", 12},
		{"https://git.example.org/team/proj/-/issues/7", "team/proj", 7},
		{"https://git.example.org//team/proj/-/issues/7", "team/proj", 7},
		{"team/proj/-/issues/3", "team/proj", 3},
		// the scheme left off a URL on our host: a URL, not a project
		// named after the host (which would be a confusing 404)
		{"git.example.org/team/proj/-/issues/3", "team/proj", 3},
		{"GIT.Example.org/team/proj/-/issues/3", "team/proj", 3},
	} {
		p, n, err := ParseRef(tc.in, "git.example.org")
		if err != nil || p != tc.proj || n != tc.iid {
			t.Errorf("ParseRef(%q) = %q, %d, %v", tc.in, p, n, err)
		}
	}
	// Only the first segment equal to the configured host is a URL: with
	// another host, or none, the text is a project path as before.
	for _, tc := range []struct{ in, host, proj string }{
		{"git.example.org/team/proj/-/issues/3", "other.example.org", "git.example.org/team/proj"},
		{"git.example.org/team/proj/-/issues/3", "", "git.example.org/team/proj"},
		{"git.example.org:8443/team/proj/-/issues/3", "git.example.org:8443", "team/proj"},
	} {
		if p, n, err := ParseRef(tc.in, tc.host); err != nil || p != tc.proj || n != 3 {
			t.Errorf("ParseRef(%q, %q) = %q, %d, %v", tc.in, tc.host, p, n, err)
		}
	}
	for _, bad := range []string{"proj", "#3", "proj#x", "proj#0", "team/proj#+7", "team/proj#-7", "foo#12abc", "foo#12 3", "a:id/proj#3", "https://h/x?q=/-/issues/3"} {
		if _, _, err := ParseRef(bad, "git.example.org"); err == nil {
			t.Errorf("ParseRef(%q): want error", bad)
		}
	}
	// A URL on another host is refused, not acted on at the configured one.
	for _, tc := range []struct{ in, host string }{
		{"https://other.example.org/team/proj/-/issues/7", "git.example.org"},
		{"https://git.example.org:8443/team/proj/-/issues/7", "git.example.org:9443"},
		{"https://user@git.example.org/team/proj/-/issues/7", "git.example.org"},
	} {
		_, _, err := ParseRef(tc.in, tc.host)
		if err == nil {
			t.Errorf("ParseRef(%q, %q): want error", tc.in, tc.host)
		}
	}
	if _, _, err := ParseRef("https://other.example.org/team/proj/-/issues/7", "git.example.org"); err == nil ||
		err.Error() != "the URL is on other.example.org, but host is git.example.org" {
		t.Errorf("host mismatch: %v", err)
	}
	// A URL with credentials is refused without echoing them.
	if _, _, err := ParseRef("https://user:SECRET@git.example.org/team/proj/-/issues/7", "git.example.org"); err == nil ||
		strings.Contains(err.Error(), "SECRET") || !strings.Contains(err.Error(), `"https://***@git.example.org/team/proj/-/issues/7"`) {
		t.Errorf("credentials: %v", err)
	}
	// Also when the URL does not parse, or has no scheme: the error
	// echoes the input, redacted.
	for in, want := range map[string]string{
		"https://user:SE%zzCRET@git.example.org/team/proj/-/issues/7": `"https://***@git.example.org/team/proj#7"`,
		"user:SECRET@git.example.org/team/proj/-/issues/7":            `"***@git.example.org/team/proj#7"`,
		"https://user:SECRET@git.example.org/x?q=/-/issues/3":         `"https://***@git.example.org/x?q=/-/issues/3"`,
		"https://user:SE%zzCRET@git.example.org/team/proj/-/issues/x": `"https://***@git.example.org/team/proj#x"`,
		// a raw /, ? or # in the password: the authority does not end there
		"https://user:SE/CRET@git.example.org/team/proj/-/issues/7": `"https://***@git.example.org/team/proj#7"`,
		"user:SE?CRET@git.example.org/team/proj/-/issues/7":         `"***@git.example.org/team/proj#7"`,
		"https://user:SE#CRET@git.example.org/team/proj/-/issues/7": `"https://***@git.example.org/team/proj#7"`,
	} {
		_, _, err := ParseRef(in, "git.example.org")
		if err == nil || strings.Contains(err.Error(), "SE") || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseRef(%q): %v, want error quoting %s", in, err, want)
		}
	}
	// A scheme-less URL on our host without its port is still a URL on
	// our host, and the port mismatch is what is refused.
	if _, _, err := ParseRef("gitlab.example.edu/acme/x/-/issues/1", "gitlab.example.edu:8443"); err == nil ||
		err.Error() != "the URL is on gitlab.example.edu, but host is gitlab.example.edu:8443" {
		t.Errorf("port left off: %v", err)
	}
	for _, tc := range []struct{ in, host string }{
		{"https://GIT.example.org/team/proj/-/issues/7", "git.example.org"},
		{"https://git.example.org:8443/team/proj/-/issues/7", "git.example.org:8443"},
		{"https://git.example.org:8443/team/proj/-/issues/7", "git.example.org"},
	} {
		if p, n, err := ParseRef(tc.in, tc.host); err != nil || p != "team/proj" || n != 7 {
			t.Errorf("ParseRef(%q, %q) = %q, %d, %v", tc.in, tc.host, p, n, err)
		}
	}
}

func TestDecodeList(t *testing.T) {
	got, err := decodeList[User]([]byte(`[{"id":1,"username":"a"}][{"id":2,"username":"b"}]` + "\n[]"))
	if err != nil || len(got) != 2 || got[1].Username != "b" {
		t.Fatalf("got %v, %v", got, err)
	}
	defer func(n int) { MaxItems = n }(MaxItems)
	MaxItems = 3
	if _, err := decodeList[User]([]byte(`[{},{}][{},{}]`)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("4 items over a cap of 3: got %v, want ErrTooLarge", err)
	}
	if got, err := decodeList[User]([]byte(`[{},{}]null[{}]`)); err != nil || len(got) != 3 {
		t.Errorf("3 items at the cap: got %d, %v", len(got), err)
	}
	if _, err := decodeList[User]([]byte(`{"id":1}`)); err == nil {
		t.Error("an object is not a list")
	}
}

// A decode error never carries a terminal control sequence from the
// server: the CLI and the TUI print errors as they are. A top-level
// token that is not a list is not echoed at all (decoded, it would be
// raw); the other paths (encoding/json, time) quote what they echo.
func TestDecodeErrorsEchoNoControls(t *testing.T) {
	esc := `\u001b]52;c;SECRET\u0007` // an OSC 52 sequence, JSON-escaped
	for name, body := range map[string]string{
		"string instead of a list":  `"` + esc + `"`,
		"string after a list":       `[]"` + esc + `"`,
		"bad time in a list":        `[{"created_at":"` + esc + `"}]`,
		"bad time in an object":     `{"created_at":"` + esc + `"}`,
		"raw C1 control in a time":  "[{\"created_at\":\"\u009b52;SECRET\"}]",
		"wrong type for a field":    `[{"id":"` + esc + `"}]`,
		"syntax error after a list": `[]` + "\x1b]52;c;SECRET\x07",
	} {
		var err error
		if strings.HasPrefix(body, "{") {
			err = decodeOne([]byte(body), &Issue{})
		} else {
			_, err = decodeList[Issue]([]byte(body))
		}
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if HasControl(err.Error()) {
			t.Errorf("%s: error carries a control character: %q", name, err.Error())
		}
		if strings.HasPrefix(name, "string") && strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the token is echoed: %q", name, err.Error())
		}
	}
}

func TestParseDue(t *testing.T) {
	now := time.Date(2026, 10, 3, 15, 0, 0, 0, time.Local) // Saturday
	for in, want := range map[string]string{
		"":           "",
		"none":       "",
		"today":      "2026-10-03",
		"tomorrow":   "2026-10-04",
		"2026-12-01": "2026-12-01",
		"+3":         "2026-10-06",
		"+3d":        "2026-10-06",
		"+2w":        "2026-10-17",
		"+1m":        "2026-11-03",
		"fri":        "2026-10-09",
		"Monday":     "2026-10-05",
		"sat":        "2026-10-10",
	} {
		got, err := ParseDue(in, now)
		if err != nil || got != want {
			t.Errorf("ParseDue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"soon", "+x", "2026-13-01", "mo", "++3", "+-0", "++3d"} {
		if _, err := ParseDue(bad, now); err == nil {
			t.Errorf("ParseDue(%q): want error", bad)
		}
	}
	// Offsets are bounded (about ten years): "+9999999999m" is a typo,
	// not a due date in the year 833335360.
	for in, want := range map[string]string{"+3650d": "2036-09-30", "+3650": "2036-09-30", "+520w": "2036-09-20", "+120m": "2036-10-03"} {
		if got, err := ParseDue(in, now); err != nil || got != want {
			t.Errorf("ParseDue(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"+3651d", "+3651", "+521w", "+121m", "+9999999999m", "+99999999999999999999d"} {
		if _, err := ParseDue(bad, now); err == nil || !strings.Contains(err.Error(), "bad offset") {
			t.Errorf("ParseDue(%q) = %v, want a bad offset error", bad, err)
		}
	}
}

func TestDaysLeft(t *testing.T) {
	now := time.Date(2026, 10, 3, 23, 0, 0, 0, time.Local)
	is := Issue{DueDate: "2026-10-01"}
	if d, ok := is.DaysLeft(now); !ok || d != -2 {
		t.Errorf("DaysLeft = %d, %v", d, ok)
	}
	if _, ok := (Issue{}).DaysLeft(now); ok {
		t.Error("no due date should report ok=false")
	}
}

func TestRankProjects(t *testing.T) {
	ps := []Project{
		{Path: "g/platform/weather/sensors/logger"},
		{Path: "g/platform/weather/sensors/logger-plugins"},
		{Path: "g/people/bob/notes"},
		{Path: "g/people/carol/notes"},
		{Path: "g/tools/logger-notes"},
	}
	paths := func(ps []Project) []string {
		var s []string
		for _, p := range ps {
			s = append(s, p.Path)
		}
		return s
	}
	got := paths(RankProjects(ps, "logger", nil))
	if len(got) != 3 || got[0] != "g/platform/weather/sensors/logger" {
		t.Errorf("logger: %v", got)
	}
	if got := paths(RankProjects(ps, "bob notes", nil)); len(got) != 1 || got[0] != "g/people/bob/notes" {
		t.Errorf("bob notes: %v", got)
	}
	boost := func(p Project) int {
		if p.Path == "g/people/carol/notes" {
			return 1
		}
		return 0
	}
	if got := paths(RankProjects(ps, "notes", boost)); got[0] != "g/people/carol/notes" {
		t.Errorf("boost should win ties: %v", got)
	}
	named := slices.Concat(ps, []Project{{Name: "Rain Gauge", Path: "g/hw/rg-fw"}})
	if got := paths(RankProjects(named, "rain gauge", nil)); len(got) != 1 || got[0] != "g/hw/rg-fw" {
		t.Errorf("display name should match: %v", got)
	}
	if got := RankProjects(ps, "", nil); len(got) != len(ps) {
		t.Errorf("empty query should match all, got %d", len(got))
	}
}

// ParseDue runs on every keystroke for the live preview, so every
// partially typed input must return an error or a date, never panic.
func TestParseDuePartialInputs(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	for _, full := range []string{"+2w", "+10d", "+1m", "tomorrow", "friday", "2026-11-01", "none", "+", "++", "+d", "+-1", " + ", "-", "+w"} {
		for i := 0; i <= len(full); i++ {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("ParseDue(%q) panicked: %v", full[:i], r)
					}
				}()
				ParseDue(full[:i], now)
			}()
		}
	}
}

func TestRelPathAndInGroup(t *testing.T) {
	for _, tc := range []struct {
		path, group, rel string
		ok, in           bool
	}{
		{"acme/platform/web/site", "acme/platform", "web/site", true, true},
		{"acme/platform/web/site", "Acme/Platform", "web/site", true, true},
		{"acme/platform", "ACME/platform", "acme/platform", false, true},
		{"acme/platformer/x", "acme/platform", "acme/platformer/x", false, false},
		{"other/x", "acme", "other/x", false, false},
		{"acme/x", "", "acme/x", false, false},
	} {
		rel, ok := RelPath(tc.path, tc.group)
		if rel != tc.rel || ok != tc.ok || InGroup(tc.path, tc.group) != tc.in {
			t.Errorf("RelPath(%q, %q) = %q, %v; InGroup %v", tc.path, tc.group, rel, ok, InGroup(tc.path, tc.group))
		}
	}
}
