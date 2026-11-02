// Package config loads gl-issues settings from a small TOML-style file.
//
// Only flat `key = "value"` lines (basic or 'literal' strings, or bare
// words such as true and 7) and `#` comments are understood; that is all
// gl-issues needs and it keeps the tool free of a TOML dependency.
// Anything else is an error, never silently skipped.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gl-issues/internal/gl"
)

// Config holds connection settings and preferences. It holds no token:
// glab uses its own stored login for Host (`glab auth login`).
type Config struct {
	Host  string // e.g. "gitlab.com" or "git.example.org"
	Group string // full group path, e.g. "team/sub"
	Path  string // file the config was read from; "" if none

	Refresh      time.Duration // issue list reload interval; 0 = off
	RefreshLists time.Duration // members and projects reload interval; 0 = off
	Notify       bool          // tag and report changes since you last saw them
	Todos        bool          // fetch your pending GitLab To-Dos with each list load
	Sort         string        // "due", "updated" or "project"
	State        string        // "opened", "closed" or "all"
	View         string        // "cards" or "compact"
	Filter       string        // "", "mine", "unassigned" or "due-soon"
	DueSoonDays  int           // due within this many days counts as soon
	Editor       string        // overrides $VISUAL/$EDITOR when set
	Browser      string        // overrides the URL opener when set
	TreeDepth    int           // 0 = flat list; 1 or 2 = issues grouped by that many subgroup levels
	Theme        string        // "auto", "light" or "dark"

	// Source records where each setting's value came from: "default",
	// the config file path, "env GL_ISSUES_…" or "flag -…".
	Source map[string]string

	groupHost string // host of a group given as a URL, checked against Host
}

// MinRefresh is the shortest allowed refresh interval, to be kind to the
// GitLab server.
const MinRefresh = 30 * time.Second

type setting struct {
	key, def, help string
	apply          func(c *Config, v string) error
}

// Settings lists every key the config file understands, in the order
// `gl-issues config` prints them. Each can also be set with the
// environment variable GL_ISSUES_<KEY> (upper case; see lookupEnv).
var settings = []setting{
	{key: "host", help: "bare host name, optional :port", apply: func(c *Config, v string) error {
		h, err := parseHost(v)
		if err != nil {
			return err
		}
		if h != "" && c.groupHost != "" && !strings.EqualFold(h, c.groupHost) {
			return fmt.Errorf("the group was given as a URL on %s, not on this host", c.groupHost)
		}
		c.Host = h
		return nil
	}},
	{key: "group", help: "group whose issues are shown, subgroups included", apply: (*Config).setGroup},
	{key: "refresh", def: "5m", help: "reload the issue list this often (30s, 2m, 1h …) or off",
		apply: func(c *Config, v string) (err error) { c.Refresh, err = parseInterval(v); return }},
	{key: "refresh_lists", def: "1h", help: "reload group members and projects this often, or off",
		apply: func(c *Config, v string) (err error) { c.RefreshLists, err = parseInterval(v); return }},
	{key: "notify", def: "true", help: "tag new/updated issues since you last saw them (kept across restarts) and say what changed",
		apply: func(c *Config, v string) (err error) { c.Notify, err = parseBool(v); return }},
	{key: "todos", def: "true", help: "show your pending GitLab To-Dos on issues (count and tags; read-only)",
		apply: func(c *Config, v string) (err error) { c.Todos, err = parseBool(v); return }},
	{key: "sort", def: "due", help: "starting sort: due, updated or project",
		apply: func(c *Config, v string) error { return oneOf(&c.Sort, v, "due", "updated", "project") }},
	{key: "state", def: "opened", help: "starting list: opened, closed or all",
		apply: func(c *Config, v string) error { return oneOf(&c.State, v, gl.IssueStates...) }},
	{key: "view", def: "cards", help: "cards (two lines per issue) or compact (one line)",
		apply: func(c *Config, v string) error { return oneOf(&c.View, v, "cards", "compact") }},
	{key: "filter", def: "", help: "starting filter: mine, unassigned, due-soon, or empty for none",
		apply: func(c *Config, v string) error { return oneOf(&c.Filter, v, "", "mine", "unassigned", "due-soon") }},
	{key: "due_soon_days", def: "7", help: "issues due within this many days count as due soon",
		apply: func(c *Config, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || n > 365 {
				return fmt.Errorf("want a number of days from 0 to 365")
			}
			c.DueSoonDays = n
			return nil
		}},
	{key: "editor", def: "", help: "editor command for descriptions and long comments; empty = $VISUAL/$EDITOR",
		apply: func(c *Config, v string) error { c.Editor = v; return nil }},
	{key: "browser", def: "", help: "command that opens issue URLs; empty = system default",
		apply: func(c *Config, v string) error { c.Browser = v; return nil }},
	{key: "tree_depth", def: "1", help: "starting layout: 0 flat, 1 or 2 = tree of subgroup levels with child items (starts folded)",
		apply: func(c *Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 || n > 2 {
				return fmt.Errorf("want 0 (flat), 1 or 2")
			}
			c.TreeDepth = n
			return nil
		}},
	{key: "theme", def: "auto", help: "terminal background: auto (ask the terminal), light or dark",
		apply: func(c *Config, v string) error { return oneOf(&c.Theme, v, "auto", "light", "dark") }},
}

// parseHost checks a host setting: glab takes a bare host name,
// optionally with a port, and no path, credentials or query. A leading
// https:// and a trailing / are dropped. Empty means unset.
func parseHost(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if hasPrefixFold(v, "http://") {
		return "", errors.New("drop http://: give the bare host name, optional :port " +
			"(glab uses https unless its api_protocol setting says http)")
	}
	if hasPrefixFold(v, "https://") {
		v = v[len("https://"):]
	}
	v = strings.TrimSuffix(v, "/")
	if strings.ContainsAny(v, "/ \t@?#\"'") {
		return "", errors.New("want a bare host name like gitlab.example.com or gitlab.example.com:8443, without a path")
	}
	if hasHidden(v) {
		return "", errors.New("the host name has control or invisible characters")
	}
	name, port, hasPort := strings.Cut(v, ":")
	if name == "" {
		return "", errors.New("want a host name like gitlab.example.com, optionally with :port")
	}
	if hasPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strings.Trim(port, "0123456789") != "" {
			return "", fmt.Errorf("bad port %q: want a number from 1 to 65535", port)
		}
	}
	return v, nil
}

// setGroup sets the group path. A group URL pasted from the browser
// (https://<host>/acme/platform, also .../groups/acme/-/issues) is
// reduced to its path, as long as its host is the configured one.
func (c *Config) setGroup(v string) error {
	v = strings.TrimSpace(v)
	c.groupHost = ""
	if hasPrefixFold(v, "https://") || hasPrefixFold(v, "http://") {
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || u.User != nil {
			return errors.New("want a group path like acme/platform, or the group's URL")
		}
		if c.Host != "" && !strings.EqualFold(u.Host, c.Host) {
			return fmt.Errorf("the URL is on %s, but host is %s", u.Host, c.Host)
		}
		p, _, _ := strings.Cut(strings.Trim(u.Path, "/")+"/", "/-/") // drop /-/issues and the like
		v, c.groupHost = strings.TrimPrefix(p, "groups/"), u.Host
		if strings.Trim(v, "/") == "" {
			return errors.New("the URL has no group path; want e.g. https://" + u.Host + "/acme/platform")
		}
	}
	v = strings.Trim(v, "/")
	if strings.ContainsAny(v, " \t\r\n:?#") {
		return errors.New("want a group path like acme/platform, without spaces or ':'")
	}
	if hasHidden(v) {
		return errors.New("the group path has control or invisible characters")
	}
	c.Group = v
	return nil
}

// hasHidden reports whether v has a control or Unicode format character
// (zero-width, direction overrides): host and group are shown as they
// are in the title line, where such a character would hide or reorder
// what follows.
func hasHidden(v string) bool {
	return strings.ContainsFunc(v, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) })
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func parseInterval(v string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "never", "false", "no":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("want a duration like 30s, 5m or 1h, or off")
	}
	if d < MinRefresh {
		return 0, fmt.Errorf("shortest allowed is %s (use off to disable)", MinRefresh)
	}
	return d, nil
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true, nil
	case "false", "no", "off", "0":
		return false, nil
	}
	return false, fmt.Errorf("want true or false")
}

func oneOf(dst *string, v string, allowed ...string) error {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, a := range allowed {
		if v == a {
			*dst = v
			return nil
		}
	}
	var shown []string
	for _, a := range allowed {
		if a != "" {
			shown = append(shown, a)
		}
	}
	return fmt.Errorf("want one of %s", strings.Join(shown, ", "))
}

func lookup(key string) *setting {
	for i := range settings {
		if settings[i].key == key {
			return &settings[i]
		}
	}
	return nil
}

// Set validates and applies one setting, recording its source.
func (c *Config) Set(key, value, source string) error {
	s := lookup(key)
	if s == nil {
		return unknownSetting(key)
	}
	if err := s.apply(c, value); err != nil {
		return fmt.Errorf("%s = %q: %w", key, gl.RedactUserinfo(value), err)
	}
	if c.Source == nil {
		c.Source = map[string]string{}
	}
	c.Source[key] = source
	return nil
}

// unknownSetting is the error for a key that is not a setting. It never
// includes a value.
func unknownSetting(key string) error {
	var keys []string
	for _, s := range settings {
		keys = append(keys, s.key)
	}
	return fmt.Errorf("unknown setting %q (known: %s)", key, strings.Join(keys, ", "))
}

// Defaults returns a Config with every default applied.
func Defaults() Config {
	var c Config
	for _, s := range settings {
		if err := c.Set(s.key, s.def, "default"); err != nil {
			panic("bad default: " + err.Error())
		}
	}
	return c
}

// Entry describes one setting for display.
type Entry struct {
	Key, Value, Source, Help string
}

// Entries lists every setting with its current value and source.
func (c Config) Entries() []Entry {
	value := map[string]string{
		"host": c.Host, "group": c.Group,
		"refresh": interval(c.Refresh), "refresh_lists": interval(c.RefreshLists),
		"notify": strconv.FormatBool(c.Notify), "todos": strconv.FormatBool(c.Todos), "sort": c.Sort, "state": c.State,
		"view": c.View, "filter": c.Filter, "due_soon_days": strconv.Itoa(c.DueSoonDays),
		"editor": c.Editor, "browser": c.Browser, "tree_depth": strconv.Itoa(c.TreeDepth), "theme": c.Theme,
	}
	var out []Entry
	for _, s := range settings {
		out = append(out, Entry{s.key, value[s.key], c.Source[s.key], s.help})
	}
	return out
}

func interval(d time.Duration) string {
	if d == 0 {
		return "off"
	}
	s := d.String() // "5m0s", "1h0m0s", "1m30s"
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// ErrNoConfigDir is returned by DefaultPath when there is no home
// directory and no absolute XDG_CONFIG_HOME: gl-issues never falls back to
// a config.toml in the current directory.
var ErrNoConfigDir = errors.New("no config directory: HOME is not set and XDG_CONFIG_HOME is not an absolute path; " +
	"pass -config or set GL_ISSUES_CONFIG")

// Name is the config directory name; EnvPrefix starts the environment
// overrides (GL_ISSUES_<SETTING>).
const (
	Name      = "gl-issues"
	EnvPrefix = "GL_ISSUES_"
)

// DefaultPath returns ~/.config/gl-issues/config.toml, or
// $XDG_CONFIG_HOME/gl-issues/config.toml when that is an absolute path (a
// relative one is ignored, as the XDG spec says).
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", ErrNoConfigDir
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, Name, "config.toml"), nil
}

// lookupEnv reads the environment variable GL_ISSUES_<key>. name is the
// variable that was read.
func lookupEnv(key string) (name, value string) {
	name = EnvPrefix + strings.ToUpper(key)
	return name, os.Getenv(name)
}

// ResolvePath returns the config file gl-issues uses: path (the -config
// flag) if set, else $GL_ISSUES_CONFIG, else DefaultPath. explicit is
// false for the default path, which may be missing.
func ResolvePath(path string) (resolved string, explicit bool, err error) {
	if path != "" {
		return path, true, nil
	}
	if _, p := lookupEnv("config"); p != "" {
		return p, true, nil
	}
	resolved, err = DefaultPath()
	return resolved, false, err
}

// Load reads the config from path. If path is empty it uses
// $GL_ISSUES_CONFIG, then DefaultPath. A missing file is not an error when
// path was not given explicitly. Environment variables GL_ISSUES_<KEY>
// override file values; unknown keys and invalid values are errors. An
// empty variable is ignored.
func Load(path string) (Config, error) {
	c := Defaults()
	path, explicit, err := ResolvePath(path)
	if err != nil {
		return c, err
	}
	f, err := os.Open(path)
	switch {
	case err == nil:
		defer f.Close()
		if err := checkPerms(f, path); err != nil {
			return c, err
		}
		if err := c.loadFile(f, path); err != nil {
			return c, err
		}
	case os.IsNotExist(err) && !explicit:
	default:
		return c, err
	}
	return c, c.applyEnv()
}

// loadFile sets the values of the config file read from r.
func (c *Config) loadFile(r io.Reader, path string) error {
	kv, err := parse(r)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range kv.order {
		if err := c.Set(k, kv.values[k], path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	c.Path = path
	return nil
}

// applyEnv applies the GL_ISSUES_<KEY> environment overrides.
func (c *Config) applyEnv() error {
	for _, s := range settings {
		env, v := lookupEnv(s.key)
		if v != "" {
			if err := c.Set(s.key, v, "env "+env); err != nil {
				return fmt.Errorf("%s: %w", env, err)
			}
		}
	}
	return nil
}

// checkPerms refuses a config file that others can read.
func checkPerms(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is readable by other users (mode %04o); run: chmod 600 %s",
			path, fi.Mode().Perm(), path)
	}
	return nil
}

type keyValues struct {
	values map[string]string
	order  []string // keys in file order
}

// parse reads the TOML subset gl-issues understands: blank lines, `#`
// comments and one `key = value` per line, where value is a "basic
// string", a 'literal string' or a bare word (true, 7). Anything else,
// such as a [section] header, a multi-line string or text after the
// closing quote, is an error: a typo must never be silently ignored.
func parse(r io.Reader) (keyValues, error) {
	kv := keyValues{values: map[string]string{}}
	sc := bufio.NewScanner(r)
	seen := map[string]int{} // key → line it was set on
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 {
			line = strings.TrimPrefix(line, "\uFEFF") // UTF-8 byte order mark
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			return kv, fmt.Errorf("line %d: sections ([…]) are not supported; put every setting at the top level", n)
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return kv, fmt.Errorf("line %d: expected key = \"value\"", n)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !validKey(k) {
			return kv, fmt.Errorf("line %d: bad setting name", n)
		}
		k = strings.ToLower(k)
		if lookup(k) == nil { // before the value, so its text is never in the error
			return kv, fmt.Errorf("line %d: %w", n, unknownSetting(k))
		}
		v, err := parseValue(v)
		if err != nil {
			return kv, fmt.Errorf("line %d: %s: %w", n, k, err)
		}
		if first, dup := seen[k]; dup {
			return kv, fmt.Errorf("line %d: %s is already set on line %d; keep one", n, k, first)
		}
		seen[k] = n
		kv.order = append(kv.order, k)
		kv.values[k] = v
	}
	return kv, sc.Err()
}

// validKey accepts TOML bare keys: letters, digits, '_' and '-'.
func validKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		switch {
		case r == '_', r == '-', r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

// parseValue decodes the right-hand side of a line, including an optional
// trailing comment.
func parseValue(v string) (string, error) {
	var val, rest string
	switch {
	case strings.HasPrefix(v, `"""`) || strings.HasPrefix(v, "'''"):
		return "", fmt.Errorf("multi-line strings are not supported")
	case strings.HasPrefix(v, `"`):
		end := closingQuote(v)
		if end < 0 {
			return "", fmt.Errorf("unterminated string")
		}
		s, err := strconv.Unquote(v[:end+1])
		if err != nil {
			return "", errors.New("bad escape in string (use 'single quotes' for backslashes)")
		}
		val, rest = s, v[end+1:]
	case strings.HasPrefix(v, "'"): // literal string: no escapes
		end := strings.IndexByte(v[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated string")
		}
		val, rest = v[1:end+1], v[end+2:]
	default: // bare value such as true or 7
		val, rest, _ = strings.Cut(v, "#")
		val = strings.TrimSpace(val)
		if val == "" {
			return "", fmt.Errorf("missing value (use \"\" for an empty one)")
		}
		if rest != "" {
			rest = "#" + rest
		}
		if strings.ContainsAny(val, "\"'") || strings.ContainsAny(val, " \t") {
			return "", errors.New("bad value (quote text: \"…\")")
		}
	}
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
		return "", errors.New("unexpected text after the value")
	}
	return val, nil
}

// closingQuote returns the index of the quote that ends the string
// starting at s[0], or -1.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}
