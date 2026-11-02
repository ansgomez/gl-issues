package gl

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDue turns user input into a YYYY-MM-DD due date relative to now.
// Accepted: "" or "none" (clear), "today", "tomorrow", "YYYY-MM-DD",
// "+N"/"+Nd" days, "+Nw" weeks, "+Nm" months, and weekday names
// ("fri", "friday") meaning the next such day after today.
func ParseDue(s string, now time.Time) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch s {
	case "", "none", "-":
		return "", nil
	case "today":
		return today.Format(time.DateOnly), nil
	case "tomorrow":
		return today.AddDate(0, 0, 1).Format(time.DateOnly), nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t.Format(time.DateOnly), nil
	}
	if strings.HasPrefix(s, "+") {
		num, unit := s[1:], byte('d')
		if num == "" {
			return "", fmt.Errorf("bad offset %q (try +3d, +2w, +1m)", s)
		}
		if last := num[len(num)-1:]; last == "d" || last == "w" || last == "m" {
			num, unit = num[:len(num)-1], last[0]
		}
		n, err := strconv.Atoi(num)
		if err != nil || strings.Trim(num, "0123456789") != "" { // no sign: "++3", "+-0"
			return "", fmt.Errorf("bad offset %q (try +3d, +2w, +1m)", s)
		}
		limit := 3650 // about ten years in each unit: "+9999999999m" is a typo, not a date
		switch unit {
		case 'w':
			limit = 520
		case 'm':
			limit = 120
		}
		if n > limit {
			return "", fmt.Errorf("bad offset %q (at most +3650d, +520w or +120m)", s)
		}
		switch unit {
		case 'w':
			return today.AddDate(0, 0, 7*n).Format(time.DateOnly), nil
		case 'm':
			return today.AddDate(0, n, 0).Format(time.DateOnly), nil
		}
		return today.AddDate(0, 0, n).Format(time.DateOnly), nil
	}
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		name := strings.ToLower(wd.String())
		if len(s) >= 3 && strings.HasPrefix(name, s) {
			d := (int(wd) - int(today.Weekday()) + 7) % 7
			if d == 0 {
				d = 7
			}
			return today.AddDate(0, 0, d).Format(time.DateOnly), nil
		}
	}
	return "", fmt.Errorf("bad date %q (use YYYY-MM-DD, today, tomorrow, fri, +3d, +2w, or none)", s)
}
