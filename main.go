// Command gl-issues is a terminal dashboard for the issues of a GitLab group:
// see what is open, overdue or unassigned, then comment, assign, set due
// dates and edit descriptions. It talks to GitLab through `glab api`.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/config"
	"gl-issues/internal/gl"
	"gl-issues/internal/state"
	"gl-issues/internal/tui"
)

var version = "dev"

const usage = `Usage:
  gl-issues [flags]                          interactive dashboard
  gl-issues [flags] init                     create the config file from the template
  gl-issues [flags] doctor                   check config, glab, login, token storage, group and token scopes
  gl-issues [flags] config                   show all settings and where they come from
  gl-issues [flags] list     [-mine] [-overdue] [-state opened|closed|all]
  gl-issues [flags] show     <ref>
  gl-issues [flags] comment  <ref> <text|->    ("-" reads stdin)
  gl-issues [flags] assign   <ref> <user,+user,-user,...|none>   ("me" is you)
  gl-issues [flags] due      <ref> <date>
  gl-issues [flags] describe <ref> <text|->    (empty text is refused)
  gl-issues [flags] describe <ref> -clear      remove the description
  gl-issues [flags] close    <ref>
  gl-issues [flags] reopen   <ref>
  gl-issues [flags] new      <project> <title> [-due date] [-assign users] [-d text|-]

<ref>      group/project#12 or an issue URL
<project>  full path, path below the group, or project name ("weather-station")
<date>     YYYY-MM-DD, today, tomorrow, fri, +3d, +2w, +1m, or none
Subcommand flags may come before or after the arguments; "--" ends them.
"gl-issues <command> -h" shows the flags of one command.
gl-issues holds no token: it uses glab's login for the host, which glab
1.123+ keeps in the OS keyring (glab auth login --hostname <host>;
gl-issues doctor says where it is).

Flags:
`

// cliTimeout bounds each group of GitLab requests of a subcommand. It
// never covers time spent waiting for the user (the [y/N] question).
const cliTimeout = 2 * time.Minute

func main() {
	var (
		cfgPath     string
		group       string
		host        string
		refresh     string
		view        string
		tree        string
		showVersion bool
	)
	defPath, err := config.DefaultPath()
	if err != nil {
		defPath = "~/.config/gl-issues/config.toml"
	}
	flag.StringVar(&cfgPath, "config", "", "config file (default $GL_ISSUES_CONFIG or "+defPath+")")
	flag.StringVar(&group, "g", "", "group path, overrides the config file")
	flag.StringVar(&group, "group", "", "same as -g")
	flag.StringVar(&host, "host", "", "GitLab host, overrides the config file")
	flag.StringVar(&refresh, "refresh", "", "auto-refresh interval (30s, 5m, 1h) or off, overrides the config file")
	flag.StringVar(&view, "view", "", "cards or compact, overrides the config file")
	flag.StringVar(&tree, "tree", "", "0 flat, 1 or 2 subgroup levels as a tree, overrides the config file")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage)
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Println("gl-issues", version)
		return
	}

	path, _, err := config.ResolvePath(cfgPath)
	if err != nil {
		fatal(err)
	}
	flags := map[string]string{"group": group, "host": host, "refresh": refresh, "view": view, "tree_depth": tree}
	switch flag.Arg(0) {
	case "init", "doctor", "config": // these work without a (valid) config, or show it
		if err := setupCommand(flag.Arg(0), flag.Args()[1:], cfgPath, path, flags, os.Stdout); err != nil {
			fatal(err)
		}
		return
	}

	cfg, err := loadConfig(cfgPath, flags)
	if err != nil {
		fatal(err)
	}
	if hostMissing(cfg.Host, flag.Args()) {
		// Without a host glab silently falls back to gitlab.com.
		fatal(missingSetting(cfg, path, "host", "-host"))
	}
	client := newClient(cfg)

	if flag.NArg() == 0 {
		if cfg.Group == "" {
			fatal(missingSetting(cfg, path, "group", "-g group/path"))
		}
		tui.DetectBackground(cfg.Theme)
		p := tea.NewProgram(tui.New(client, cfg.Group, tuiOptions(cfg)), tea.WithAltScreen())
		if err := runTUI(p); err != nil {
			fatal(err)
		}
		return
	}

	app := cli{c: client, cfg: cfg, out: os.Stdout, errOut: os.Stderr, in: os.Stdin, confirm: confirmOnTTY}
	if err := app.run(flag.Arg(0), flag.Args()[1:]); err != nil {
		fatal(err)
	}
}

// tuiOptions are the dashboard's preferences from the config.
func tuiOptions(cfg config.Config) tui.Options {
	return tui.Options{
		Refresh: cfg.Refresh, RefreshLists: cfg.RefreshLists, Notify: cfg.Notify, Todos: cfg.Todos,
		Sort: cfg.Sort, State: cfg.State, View: cfg.View, Filter: cfg.Filter,
		DueSoonDays: cfg.DueSoonDays, Editor: cfg.Editor, Browser: cfg.Browser, TreeDepth: cfg.TreeDepth,
	}
}

// teaProgram is what runTUI needs of a *tea.Program; tests use a fake.
type teaProgram interface {
	Run() (tea.Model, error)
	ReleaseTerminal() error
}

// runTUI runs the dashboard. A panic that escapes Bubble Tea's own
// handler first gives the terminal back (ReleaseTerminal: normal screen,
// cursor, no raw mode), then comes back as the error, with its stack, so
// it is printed readably.
func runTUI(p teaProgram) (err error) {
	defer func() {
		if r := recover(); r != nil {
			_ = p.ReleaseTerminal()
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()
	_, err = p.Run()
	return err
}

// cli runs one subcommand. Its fields are the outside world, so tests can
// replace them.
type cli struct {
	c       gl.Client
	cfg     config.Config
	out     io.Writer // results
	errOut  io.Writer // warnings
	in      io.Reader // "-" arguments
	confirm func(path string) bool
}

// hostMissing reports whether a run needs the host the config lacks: the
// dashboard (no arguments) or a known subcommand other than its -h (an
// unknown one is reported as such by cli.run).
func hostMissing(host string, args []string) bool {
	if host != "" {
		return false
	}
	if len(args) == 0 {
		return true
	}
	_, known := commands[args[0]]
	return known && !wantsHelp(args[0], args[1:])
}

// wantsHelp reports whether subcommand name's arguments ask for its
// help, which needs no configuration. They are parsed as run parses them,
// so "-h" where a flag's value goes (`new … -d -h`) is a description, not
// a request for help. A setup command (setupCommand) has no flags.
func wantsHelp(name string, args []string) bool {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if cmd, ok := commands[name]; ok {
		cmd.setup(&cli{}, fs) // defines the flags; the handler is not run
	}
	_, err := parseArgs(fs, args)
	return errors.Is(err, flag.ErrHelp)
}

// globalFlags are the flags of gl-issues itself, which go before a
// subcommand (the flag package stops at the first argument).
var globalFlags = []string{"config", "g", "group", "host", "refresh", "view", "tree", "version"}

// misplacedGlobal explains a global flag given after the subcommand
// (`gl-issues list -g acme`), or returns nil. It is only asked once the
// arguments are known to be wrong, so a word such as an assignee spec
// "-host" stays an argument.
func misplacedGlobal(name string, args []string) error {
	for _, a := range args {
		if a == "--" {
			return nil
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		f, _, _ := strings.Cut(strings.TrimPrefix(a[1:], "-"), "=")
		if slices.Contains(globalFlags, f) {
			return fmt.Errorf("-%s goes before the command: gl-issues -%s … %s", f, f, name)
		}
	}
	return nil
}

// noArgs refuses arguments to a subcommand that takes none.
func noArgs(name string, args []string) error {
	if len(args) == 0 {
		return nil
	}
	if err := misplacedGlobal(name, args); err != nil {
		return err
	}
	return fmt.Errorf("unexpected argument %q\nusage: gl-issues %s", args[0], name)
}

// apiCtx bounds one group of GitLab requests.
func apiCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cliTimeout)
}

// command describes one subcommand: its usage line, how many positional
// arguments it takes, and setup, which defines the command's flags on fs
// and returns its handler. run parses and counts the arguments the same
// way for every command; the handler does the rest.
type command struct {
	args  string // usage after the name
	nargs int
	setup func(a *cli, fs *flag.FlagSet) handler
}

// handler is one subcommand with its flags read: run gets the positional
// arguments once they are counted. fix, if set, adjusts them before the
// count, for a flag that stands in for an argument (describe -clear).
type handler struct {
	run func(pos []string) error
	fix func(pos []string) ([]string, error)
}

var commands = map[string]command{
	"list":     {"[-mine] [-overdue] [-state opened|closed|all]", 0, (*cli).listCmd},
	"show":     {"<ref>", 1, (*cli).showCmd},
	"comment":  {"<ref> <text|->", 2, (*cli).commentCmd},
	"assign":   {"<ref> <user,+user,-user,...|none>", 2, (*cli).assignCmd},
	"due":      {"<ref> <date>", 2, (*cli).dueCmd},
	"describe": {"<ref> <text|-> | describe <ref> -clear", 2, (*cli).describeCmd},
	"close":    {"<ref>", 1, stateCmd("close")},
	"reopen":   {"<ref>", 1, stateCmd("reopen")},
	"new":      {"<project> <title> [-due date] [-assign users] [-d text|-]", 2, (*cli).newCmd},
}

// run parses the arguments of subcommand name and runs it.
func (a *cli) run(name string, args []string) error {
	cmd, ok := commands[name]
	if !ok {
		return fmt.Errorf("unknown command %q (see gl-issues -h)", name)
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // errors are returned; usage goes to a.out on -h
	h := cmd.setup(a, fs)
	pos, err := parseArgs(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(a.out, "Usage: gl-issues %s %s\n", name, cmd.args)
		fs.SetOutput(a.out)
		fs.PrintDefaults()
		return nil
	}
	if err != nil {
		if gerr := misplacedGlobal(name, args); gerr != nil {
			return gerr
		}
		return fmt.Errorf("%w\nusage: gl-issues %s %s", err, name, cmd.args)
	}
	if h.fix != nil {
		if pos, err = h.fix(pos); err != nil {
			return err
		}
	}
	if len(pos) != cmd.nargs {
		if err := misplacedGlobal(name, args); err != nil {
			return err
		}
		if len(pos) > cmd.nargs {
			extra := pos[cmd.nargs]
			what := "unexpected argument"
			if strings.HasPrefix(extra, "-") {
				what = "unknown flag or unexpected argument"
			}
			return fmt.Errorf("%s %q\nusage: gl-issues %s %s", what, extra, name, cmd.args)
		}
		return usageErr(name + " " + cmd.args)
	}
	return h.run(pos)
}

// The handlers. Each one that takes a <ref> parses it first, then reads
// any text from stdin, before the clock for the requests starts.

func (a *cli) listCmd(fs *flag.FlagSet) handler {
	var mine, overdue bool
	var which string
	fs.BoolVar(&mine, "mine", false, "only issues assigned to me")
	fs.BoolVar(&overdue, "overdue", false, "only overdue issues")
	fs.StringVar(&which, "state", "opened", "opened, closed or all")
	return handler{run: func([]string) error { return a.list(mine, overdue, which) }}
}

func (a *cli) showCmd(*flag.FlagSet) handler {
	return handler{run: func(pos []string) error {
		project, iid, err := gl.ParseRef(pos[0], a.c.Host)
		if err != nil {
			return err
		}
		return a.show(project, iid)
	}}
}

func (a *cli) commentCmd(*flag.FlagSet) handler {
	return handler{run: func(pos []string) error {
		project, iid, err := gl.ParseRef(pos[0], a.c.Host)
		if err != nil {
			return err
		}
		body, err := textFrom(pos[1], a.in)
		if err != nil {
			return err
		}
		if strings.TrimSpace(body) == "" {
			return errors.New("empty comment")
		}
		ctx, cancel := apiCtx()
		defer cancel()
		if err := a.c.Comment(ctx, project, iid, body); err != nil {
			return err
		}
		fmt.Fprintln(a.out, "commented on", pos[0])
		return nil
	}}
}

func (a *cli) assignCmd(*flag.FlagSet) handler {
	return handler{run: func(pos []string) error {
		project, iid, err := gl.ParseRef(pos[0], a.c.Host)
		if err != nil {
			return err
		}
		clearAll, err := checkAssignSpec(pos[1])
		if err != nil {
			return err
		}
		ids := []int{} // explicit empty list unassigns everyone
		if !clearAll {
			ctx, cancel := apiCtx()
			defer cancel()
			spec, err := a.expandMe(ctx, pos[1])
			if err != nil {
				return err
			}
			is, err := a.c.GetIssue(ctx, project, iid)
			if err != nil {
				return err
			}
			names := tui.ApplyAssign(is.AssigneeNames(), spec)
			if ids, err = a.c.AssigneeIDs(ctx, names, is.Assignees); err != nil {
				return err
			}
			if ids == nil {
				ids = []int{} // e.g. -alice removed the last one
			}
		}
		return a.update(project, iid, map[string]any{"assignee_ids": ids})
	}}
}

func (a *cli) dueCmd(*flag.FlagSet) handler {
	return handler{run: func(pos []string) error {
		project, iid, err := gl.ParseRef(pos[0], a.c.Host)
		if err != nil {
			return err
		}
		d, err := gl.ParseDue(pos[1], time.Now())
		if err != nil {
			return err
		}
		return a.update(project, iid, map[string]any{"due_date": d})
	}}
}

func (a *cli) describeCmd(fs *flag.FlagSet) handler {
	var clearDesc bool
	fs.BoolVar(&clearDesc, "clear", false, "remove the description")
	return handler{
		fix: func(pos []string) ([]string, error) { // -clear takes the text's place
			if !clearDesc {
				return pos, nil
			}
			if len(pos) != 1 {
				return nil, usageErr("describe <ref> -clear")
			}
			return append(pos, ""), nil
		},
		run: func(pos []string) error {
			project, iid, err := gl.ParseRef(pos[0], a.c.Host)
			if err != nil {
				return err
			}
			d, err := describeText(pos[1], clearDesc, a.in)
			if err != nil {
				return err
			}
			return a.update(project, iid, map[string]any{"description": d})
		},
	}
}

// stateCmd is the setup of close and reopen: a PUT of that state event.
func stateCmd(event string) func(*cli, *flag.FlagSet) handler {
	return func(a *cli, _ *flag.FlagSet) handler {
		return handler{run: func(pos []string) error {
			project, iid, err := gl.ParseRef(pos[0], a.c.Host)
			if err != nil {
				return err
			}
			return a.update(project, iid, map[string]any{"state_event": event})
		}}
	}
}

func (a *cli) newCmd(fs *flag.FlagSet) handler {
	var due, who, desc string
	fs.StringVar(&due, "due", "", "due date: YYYY-MM-DD, today, tomorrow, fri, +3d, +2w, +1m")
	fs.StringVar(&who, "assign", "", `comma-separated usernames; "me" is you`)
	fs.StringVar(&desc, "d", "", "description, or - to read it from stdin")
	return handler{run: func(pos []string) error { return a.newIssue(pos[0], pos[1], due, who, desc) }}
}

// update PUTs fields on an issue and prints its new state.
func (a *cli) update(project string, iid int, fields map[string]any) error {
	ctx, cancel := apiCtx()
	defer cancel()
	is, err := a.c.Update(ctx, project, iid, fields)
	if err != nil {
		return err
	}
	printIssueLine(a.out, is)
	return nil
}

// parseArgs parses flags anywhere among the arguments, so both
// `describe -clear <ref>` and `describe <ref> -clear` work. Only flags
// the subcommand defines (and -h/-help) count as flags; any other word,
// even one starting with "-" (an assign spec like "-bob", stdin "-"), is
// an argument. "--" ends the flags.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return append(pos, args[1:]...), nil
		}
		name, hasValue, ok := flagName(fs, arg)
		if !ok {
			pos = append(pos, arg)
			args = args[1:]
			continue
		}
		n := 1 // tokens this flag uses
		if f := fs.Lookup(name); f != nil && !hasValue && !isBoolFlag(f) {
			n = 2 // -name value
		}
		if err := fs.Parse(args[:min(n, len(args))]); err != nil {
			return nil, err
		}
		args = args[min(n, len(args)):]
	}
	return pos, nil
}

// flagName reports whether arg is -name, --name or -name=value for a flag
// fs defines, or -h/-help.
func flagName(fs *flag.FlagSet, arg string) (name string, hasValue, ok bool) {
	if len(arg) < 2 || arg[0] != '-' {
		return "", false, false
	}
	name = strings.TrimPrefix(arg[1:], "-")
	name, _, hasValue = strings.Cut(name, "=")
	if name == "h" || name == "help" {
		return name, hasValue, true
	}
	return name, hasValue, fs.Lookup(name) != nil
}

func isBoolFlag(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

// checkAssignSpec refuses an assignee spec without any name, which would
// silently remove every assignee (e.g. an empty shell variable, or a bare
// "@"). Removing all of them takes the explicit spec "none".
func checkAssignSpec(spec string) (clearAll bool, err error) {
	if strings.TrimSpace(spec) == "none" {
		return true, nil
	}
	for _, tok := range strings.Split(spec, ",") {
		if strings.TrimLeft(strings.TrimSpace(tok), "+-!@") != "" {
			return false, nil
		}
	}
	return false, errors.New("empty assignee list refused; to remove every assignee use: gl-issues assign <ref> none")
}

// expandMe replaces "me" in an assignee spec (as name, +me or -me) with
// your username, like the "me" of the assignee picker.
func (a *cli) expandMe(ctx context.Context, spec string) (string, error) {
	toks := strings.Split(spec, ",")
	var me string
	for i, tok := range toks {
		t := strings.TrimSpace(tok)
		prefix := ""
		if t != "" && strings.ContainsRune("+-!", rune(t[0])) {
			prefix, t = t[:1], t[1:]
		}
		if !strings.EqualFold(strings.TrimPrefix(t, "@"), "me") {
			continue
		}
		if me == "" {
			u, err := a.c.Me(ctx)
			if err != nil {
				return "", err
			}
			me = u.Username
		}
		toks[i] = prefix + me
	}
	return strings.Join(toks, ","), nil
}

func (a *cli) list(mine, overdue bool, which string) error {
	if !slices.Contains(gl.IssueStates, which) {
		return fmt.Errorf("bad -state %q: want opened, closed or all", which)
	}
	if a.cfg.Group == "" {
		return errors.New("no group set: pass -g group/path or set group in the config file")
	}
	ctx, cancel := apiCtx()
	defer cancel()
	issues, err := a.c.GroupIssues(ctx, a.cfg.Group, which)
	if err != nil {
		return err
	}
	var me gl.User
	if mine {
		if me, err = a.c.Me(ctx); err != nil {
			return err
		}
	}
	now := time.Now()
	w := tabwriter.NewWriter(a.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE\tDUE\tASSIGNEE\tTITLE")
	for _, is := range issues {
		if mine && !slices.Contains(is.AssigneeNames(), me.Username) {
			continue
		}
		if d, ok := is.DaysLeft(now); overdue && (!ok || d >= 0) {
			continue
		}
		due := is.DueDate
		if due == "" {
			due = "-"
		}
		assignees := strings.Join(is.AssigneeNames(), ",")
		if assignees == "" {
			assignees = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", is.Ref(), due, assignees, is.Title)
	}
	return w.Flush()
}

// printConfig shows every setting, its value and where it came from
// ("file" for the config file named above the table), and warns about a
// token left in the file (never printed). Values, sources and the path
// come from the environment and the file, so like server text they are
// sanitized before they reach the terminal.
func printConfig(out io.Writer, cfg config.Config, path string) {
	file := cfg.Path
	if file == "" {
		file = path + " (not found)"
	}
	fmt.Fprintln(out, "config file:", gl.SanitizeLine(file))
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SETTING\tVALUE\tFROM\tMEANING")
	for _, e := range cfg.Entries() {
		v := e.Value
		if v == "" {
			v = "-"
		}
		from := e.Source
		if from == cfg.Path && cfg.Path != "" {
			from = "file"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Key, gl.SanitizeLine(v), gl.SanitizeLine(from), e.Help)
	}
	_ = w.Flush()
	fmt.Fprintln(out, "\nSet any of these in the config file, or as GL_ISSUES_<SETTING> in the environment.")
	fmt.Fprintln(out, "The login is glab's: glab auth login --hostname <host> (glab 1.123+ keeps the token in the OS keyring; gl-issues doctor says where it is).")
}

func (a *cli) newIssue(projectArg, title, due, who, desc string) error {
	if strings.TrimSpace(title) == "" {
		return errors.New("empty title")
	}
	// Check and read everything local first.
	fields := map[string]any{"title": title}
	if desc != "" {
		d, err := textFrom(desc, a.in)
		if err != nil {
			return err
		}
		fields["description"] = d
	}
	if due != "" {
		dd, err := gl.ParseDue(due, time.Now())
		if err != nil {
			return err
		}
		fields["due_date"] = dd
	}
	project, err := resolveProject(a.c, a.cfg.Group, projectArg, a.confirm)
	if err != nil {
		return err
	}
	ctx, cancel := apiCtx() // starts after any [y/N] question
	defer cancel()
	if who != "" {
		spec, err := a.expandMe(ctx, who)
		if err != nil {
			return err
		}
		ids, err := a.c.AssigneeIDs(ctx, tui.ApplyAssign(nil, spec), nil)
		if err != nil {
			return err
		}
		fields["assignee_ids"] = ids
	}
	is, err := a.c.Create(ctx, project, fields)
	if err != nil {
		return err
	}
	if err := state.AddRecentProject(project); err != nil {
		fmt.Fprintln(a.errOut, "gl-issues: warning: could not remember the project:", err)
	}
	fmt.Fprintln(a.out, is.Ref(), is.WebURL)
	return nil
}

// resolveProject turns a project path or name into a project path. Only
// an exact match is used as is. A project found only by a fuzzy search is
// used after confirm says yes (on a terminal it asks); otherwise the
// command stops, so an issue is never created in a guessed project. The
// request timeout does not run while confirm waits for an answer.
func resolveProject(c gl.Client, group, query string, confirm func(path string) bool) (string, error) {
	if strings.Contains(query, ":") {
		return "", fmt.Errorf("bad project %q", query)
	}
	if group == "" {
		if strings.Contains(query, "/") {
			return query, nil
		}
		return "", errors.New("no group set: give the full project path or set a group")
	}
	ctx, cancel := apiCtx()
	projects, err := c.GroupProjects(ctx, group)
	cancel()
	if err != nil {
		return "", err
	}
	path, exact, err := matchProject(projects, group, query)
	if err != nil || exact {
		return path, err
	}
	if confirm != nil && confirm(path) {
		return path, nil
	}
	return "", fmt.Errorf("%q is not an exact project path or name (did you mean %s?); give the full path", query, path)
}

// matchProject finds the project query names. exact is true for a full
// path (with or without the group prefix), or a project whose name (its
// display name or the last part of its path) is unique; a single fuzzy
// match is returned with exact false.
func matchProject(projects []gl.Project, group, query string) (path string, exact bool, err error) {
	var named []gl.Project
	for _, p := range projects {
		if strings.EqualFold(p.Path, query) || strings.EqualFold(p.Path, group+"/"+query) {
			return p.Path, true, nil
		}
		if strings.EqualFold(p.Name, query) || strings.EqualFold(p.Base(), query) {
			named = append(named, p)
		}
	}
	matches := gl.RankProjects(projects, query, nil)
	switch {
	case len(named) == 1:
		return named[0].Path, true, nil
	case len(named) == 0 && len(matches) == 1:
		return matches[0].Path, false, nil
	case len(named) == 0 && len(matches) == 0:
		return "", false, fmt.Errorf("no project in %s matches %q", group, query)
	}
	if len(named) > 1 {
		matches = named
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d projects; use the full path:", query, len(matches))
	for _, p := range matches[:min(len(matches), 10)] {
		b.WriteString("\n  " + p.Path)
	}
	if len(matches) > 10 {
		fmt.Fprintf(&b, "\n  … and %d more", len(matches)-10)
	}
	return "", false, errors.New(b.String())
}

// confirmOnTTY asks on the terminal whether to use a fuzzy project match.
// Without a terminal (scripts, piped input) the answer is no.
func confirmOnTTY(path string) bool {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stderr) {
		return false
	}
	fmt.Fprintf(os.Stderr, "Create the issue in %s? [y/N] ", path)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func (a *cli) show(project string, iid int) error {
	ctx, cancel := apiCtx()
	defer cancel()
	is, err := a.c.GetIssue(ctx, project, iid)
	if err != nil {
		return err
	}
	notes, err := a.c.Notes(ctx, project, iid)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "%s  [%s]\n%s\n\n", is.Ref(), is.State, is.Title)
	printIssueLine(a.out, is)
	fmt.Fprintln(a.out, is.WebURL)
	if is.Description != "" {
		fmt.Fprintf(a.out, "\n%s\n", is.Description)
	}
	for _, n := range notes {
		fmt.Fprintf(a.out, "\n--- %s, %s\n%s\n", n.Author.Username, n.CreatedAt.Local().Format("2006-01-02 15:04"), n.Body)
	}
	return nil
}

func printIssueLine(w io.Writer, is gl.Issue) {
	assignees := strings.Join(is.AssigneeNames(), ", ")
	if assignees == "" {
		assignees = "nobody"
	}
	due := is.DueDate
	if due == "" {
		due = "none"
	}
	fmt.Fprintf(w, "%s  state=%s  assignees=%s  due=%s\n", is.Ref(), is.State, assignees, due)
}

// maxText caps what textFrom reads from stdin. GitLab refuses comments
// and descriptions of about 1 MB anyway, so nothing longer is buffered.
const maxText = 1 << 20

// textFrom returns s, or all of stdin (at most maxText) when s is "-".
func textFrom(s string, stdin io.Reader) (string, error) {
	if s != "-" {
		return s, nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, maxText+1))
	if err == nil && len(b) > maxText {
		err = fmt.Errorf("stdin is over %d MB, which GitLab would refuse", maxText>>20)
	}
	return strings.TrimRight(string(b), "\n"), err
}

// describeText returns the new description. An empty one (e.g. from an
// empty pipe) is refused unless clearDesc is set, so a broken pipeline
// never wipes a description.
func describeText(arg string, clearDesc bool, stdin io.Reader) (string, error) {
	if clearDesc {
		return "", nil
	}
	d, err := textFrom(arg, stdin)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(d) == "" {
		return "", errors.New("empty description refused; to remove it use: gl-issues describe <ref> -clear")
	}
	return d, nil
}

func usageErr(s string) error { return errors.New("usage: gl-issues " + s) }

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gl-issues:", err)
	os.Exit(1)
}
