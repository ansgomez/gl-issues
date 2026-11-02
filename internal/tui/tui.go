// Package tui implements the interactive issue dashboard.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gl-issues/internal/gl"
	"gl-issues/internal/state"
)

const (
	loadTimeout   = 2 * time.Minute
	actionTimeout = 30 * time.Second
	actionWorkers = 4 // glab processes a bulk action runs at a time
)

var (
	styleTitle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleHeader      = lipgloss.NewStyle().Bold(true).Underline(true)
	styleDim         = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleGood        = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleWarn        = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	styleBad         = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleCursor      = lipgloss.NewStyle().Bold(true)
	styleLabel       = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	styleKey         = lipgloss.NewStyle().Bold(true)
	styleFooterLabel = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleProject     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4"))

	// styleCursorRow is the faint band behind the list row under the
	// cursor (see highlight): a grey just off the terminal background.
	// AdaptiveColor needs the background known before Bubble Tea owns
	// the terminal; see DetectBackground.
	styleCursorRow = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "252", Dark: "239"})
)

type mode int

const (
	modeList mode = iota
	modeDetail
)

type inputKind int

const (
	inputNone   inputKind = iota
	inputSearch           // live search on the bottom line; everything else uses pop-ups
)

// helpText is the full key reference shown by h or ?.
func helpText(soonDays int) string {
	return fmt.Sprintf(`
  List                                   Act on the issue (* or on all selected issues)
  ↑ ↓  j k        up / down              n      new issue (choose project, then fill in)
  pgup pgdn       page up / down         c      comment
  ctrl+u ctrl+d   page up / down         a      assign*   (type a name, enter adds it)
  g  home  end    first / last           i      assign me* ("I'll take it")
  enter tab →     open details           d      due date* (2026-11-01, tomorrow, fri,
  esc             clear filters, sel.                       +3d, +2w, +1m, none)
  q               quit                   e      edit description in $EDITOR
                                         x      close / reopen* (asks first)
  Filter and view                        w      open in browser
  m   assigned to me                     Selecting: space toggles, v all shown;
  u   unassigned                         a i d x then apply to every selected issue
  o   overdue or due within %d days
  /   search title, project, people      In details
  s   sort: due / updated / project      ↑ ↓ j k  pgup pgdn  ctrl+u ctrl+d  g home end
  t   show open / closed / all                          scroll
  z   two-line / compact rows            ← → [ ]        previous / next issue
  l   layout: flat / tree of 1 / 2       esc q enter backspace   back to the list
      group levels, child items nested
  f   fold / unfold the group or parent  In pop-up windows
      (enter on a group label too)       tab shift+tab   next / previous field
  b   unfold all / fold all (tree)       enter  save · ctrl+s send / create
  r   reload now                         ctrl+e editor · esc cancel
  h ? this help                          ↑ ↓ ctrl+p ctrl+n   choose a project

  press any key to close
`, soonDays)
}

var sortNames = []string{"due", "updated", "project"}

var states = gl.IssueStates

// --- messages ---

type issuesMsg struct {
	issues     []gl.Issue
	state      string // which list was loaded
	background bool   // automatic refresh, not a user action
	gen        int    // loadGen when the load started; older replies are dropped
	err        error
}

type metaMsg struct {
	me      gl.User
	members []gl.User
	gen     int // metaGen when the load started; older replies are dropped
	err     error
}

type updatedMsg struct {
	id    int // issue ID the action ran on
	what  string
	issue gl.Issue
	err   error
	warn  string // the action worked, but issue is not fresh (e.g. a refetch failed)
	form  *form  // the pop-up that sent the request, if any (see form.sending)

	// "create", once the issue exists: the recent projects with this one
	// remembered (the state file is read and written by the command, not
	// in Update) and why remembering it failed, if it did.
	recent    []string
	recentErr error
}

type notesMsg struct {
	id    int
	notes []gl.Note
	gen   int // notesGen[id] when the load started; older replies are dropped
	err   error
}

type editorMsg struct {
	issue   gl.Issue
	kind    string // "description"
	text    string
	origRaw string // the unsanitized text the editor started with
	file    string // the edited temp file, kept until the text is saved
	err     error
}

// Model is the Bubble Tea model for the dashboard.
type Model struct {
	client  gl.Client
	group   string
	state   int // index into states
	issues  []gl.Issue
	view    []int     // indices into issues that pass the filters, sorted
	rows    []listRow // what the list shows: view as a flat list or a tree (tree.go)
	cursor  int       // index into rows
	offset  int
	width   int
	height  int
	loading bool
	compact bool  // one line per issue instead of two
	loadErr error // last issue load failure, shown instead of an empty list

	me       gl.User
	members  []gl.User
	selected map[int]bool   // by issue ID
	busy     map[int]string // by issue ID
	actions  chan struct{}  // slots for the glab processes of bulk actions (actionWorkers)

	mine, unassigned, attention bool
	search                      string
	sortBy                      int

	mode      mode
	vp        viewport.Model
	notes     map[int][]gl.Note
	notesErr  map[int]error
	notesGen  map[int]int // issue ID -> current notes load; replies to older ones are dropped
	inputKind inputKind
	input     lineInput
	form      *form // open pop-up window, if any
	showHelp  bool
	flash     string
	flashBad  bool
	spin      spinner.Model

	failedForms []*form // pop-ups whose request failed behind another one: shown again, in order, as it closes

	projects        []gl.Project // loaded on first "new issue"
	projectsLoading bool
	projectsErr     error
	recent          []string // recently used project paths

	opts        Options
	pendingLoad *issuesMsg     // a loaded list held back while a pop-up is open
	tickGen     int            // current refresh timer; older ticks are ignored
	loadGen     int            // current issue load; replies to older ones are dropped
	metaGen     int            // current user and member load, likewise
	projectsGen int            // current project load, likewise
	refreshing  bool           // a background reload is running
	refreshErr  error          // last background reload failure
	lastLoad    time.Time      // when the issue list was last loaded
	loadedState string         // state of the loaded list, for change detection
	fresh       map[int]string // issue ID -> "new"/"updated": differs from the seen file (seen.go)
	creating    int            // new issues being created (not in busy: no ID yet)
	ticking     bool           // the spinner's tick chain is running (see spinning)

	footerN map[footerKey]int // footer line counts, by what decides them
	stats   listStats         // the title's counts and the list's ref column width, per rebuild
	cache   []issueCache      // per issue, same index as issues: what the search and the columns reuse

	todos       map[string][]string // gl.IssueKey -> your pending To-Do actions on it
	todoCount   int                 // pending To-Dos on issues of the group
	todoMore    bool                // more exist than were fetched (gl.MaxTodoPages): the title says N+
	todoErr     error               // last To-Do fetch failed: the count shows "?"
	todoGroupID int                 // the group's ID, resolved once for the To-Do request

	// The seen file (seen.go); all unused with notify off.
	seenFile     string                  // "" without a state directory: tags last for the session
	seen         *state.Seen             // nil until read
	seenEarly    map[int]state.SeenIssue // issues opened before the file was read
	seenExisted  bool                    // the file was there (and readable) at start
	seenStarted  bool                    // the first comparison is done
	seenDirty    bool                    // changed since the last write started
	seenSaving   bool                    // a write is running
	seenSeq      int                     // numbers the writes, see seenWriter
	seenWriter   *seenWriter             // shared by the model's copies
	seenErrShown bool                    // a failed write was reported
	checkGen     int                     // current comparison; older own-change checks are dropped

	// The tree layout (tree.go).
	tree       int             // 0 flat, else group levels shown
	opened     map[string]bool // labels and parents unfolded by the user (all start folded), for the session
	foldsSet   bool            // the first tree was laid out, or b used: no more automatic unfolding
	parents    map[int]int     // child issue ID -> parent issue ID
	hierLoaded bool            // parents match the current list
	hierGen    int             // loadGen of the last parent fetch started; -1 none
	hierErr    error           // the last parent fetch failed: background refreshes back off (hierRetry)
	hierFails  int             // parent fetches failed in a row
	hierRetry  time.Time       // after a failure, background refreshes fetch the links again from then on
	hierNoted  bool            // the one note about missing child items was shown

	// cursorMoved names the row (an issue ref or a group) that left the
	// list under the cursor in a load or refresh, so the cursor landed on
	// another row by itself; "" once the user moves it. While set, the
	// single-issue i asks first (see relayout).
	cursorMoved string
}

// DetectBackground settles whether the terminal background is dark, for
// styleCursorRow: theme "light" or "dark" says so, "auto" asks the
// terminal (multiplexers such as Zellij may answer with their own
// colour). Call it before the Bubble Tea program starts: once the
// program reads stdin, the terminal's reply would arrive as keys.
func DetectBackground(theme string) {
	switch theme {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	default:
		lipgloss.HasDarkBackground()
	}
}

// New builds a dashboard for group.
func New(client gl.Client, group string, opts Options) Model {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	m := Model{
		client:   client,
		group:    group,
		loading:  true,
		selected: map[int]bool{},
		busy:     map[int]string{},
		actions:  make(chan struct{}, actionWorkers),
		notes:    map[int][]gl.Note{},
		notesErr: map[int]error{},
		notesGen: map[int]int{},
		spin:     sp,
		recent:   state.RecentProjects(),
		stats:    listStats{refW: 8}, // the ref column before the first list

		projectsLoading: true, // fetched by Init

		opts:    opts,
		fresh:   map[int]string{},
		compact: opts.View == "compact",
		ticking: true, // Init starts the spinner for the first load
		footerN: map[footerKey]int{},

		seenFile:   state.SeenFile(client.Host, group),
		seenWriter: &seenWriter{},

		tree:    min(max(opts.TreeDepth, 0), 2),
		opened:  map[string]bool{},
		hierGen: -1,
	}
	if m.tree > 0 {
		m.hierGen = 0 // Init fetches the parent links with the first load
	}
	for i, n := range sortNames {
		if n == opts.Sort {
			m.sortBy = i
		}
	}
	for i, n := range states {
		if n == opts.State {
			m.state = i
		}
	}
	switch opts.Filter {
	case "mine":
		m.mine = true
	case "unassigned":
		m.unassigned = true
	case "due-soon":
		m.attention = true
	}
	return m
}

// Init starts the spinner and the first loads.
func (m Model) Init() tea.Cmd {
	// Projects are only needed for "new issue"; fetch them in the
	// background so the picker opens instantly.
	// The issue refresh timer starts when the first load finishes.
	return tea.Batch(m.spin.Tick, m.loadIssues(), m.loadTodos(), m.loadMeta(), m.fetchProjects(), m.scheduleLists(),
		m.loadSeen(), m.loadHierarchy())
}

// Update handles one message. A list that finished loading while a pop-up
// was open is installed as soon as the pop-up closes, and a pop-up whose
// request failed behind it comes back then. The spinner ticks only while
// something it shows is running.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	tm, cmd := m.update(msg)
	nm := tm.(Model)
	if !nm.sameDay(time.Now()) {
		// A new day's recount, once: the title and the group rows. An
		// issue may move into a folded group or out of the due-soon
		// filter: like a reload, keep the cursor on it or say it is gone.
		nm.relayout(nm.cursorKey(), nm.cursorName(), true)
		nm.refreshDetail()
	}
	if nm.form == nil && nm.pendingLoad != nil {
		pending := *nm.pendingLoad
		nm.pendingLoad = nil
		cmd = tea.Batch(cmd, nm.installIssues(pending))
	}
	if nm.form == nil && len(nm.failedForms) > 0 {
		nm.form, nm.failedForms = nm.failedForms[0], nm.failedForms[1:]
	}
	if !nm.ticking && nm.spinning() {
		nm.ticking = true
		cmd = tea.Batch(cmd, nm.spin.Tick)
	}
	return nm, cmd
}

// spinning reports whether anything that shows the spinner is running.
func (m Model) spinning() bool {
	return m.loading || m.refreshing || len(m.busy) > 0 || m.creating > 0 ||
		(m.projectsLoading && m.form != nil && m.form.kind == formPickProject)
}

// installIssues puts a successfully loaded list on screen and compares
// it with the seen file (once that is read), which tags and reports what
// changed.
func (m *Model) installIssues(msg issuesMsg) tea.Cmd {
	ch, moved, reload := m.applyIssues(msg)
	if !m.opts.Notify || m.seen == nil {
		return reload
	}
	if moved != "" {
		moved += " · " // keep it in the flash with what changed
	}
	if msg.background {
		return tea.Batch(reload, m.compareSeen(moved+"refreshed: ", true, ch.gone))
	}
	return tea.Batch(reload, m.compareSeen(moved+"reloaded: ", false, ch.gone))
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width, m.vp.Height = m.width, m.detailHeight()
		m.refreshDetail()
		m.clamp()
	case spinner.TickMsg:
		if !m.spinning() {
			m.ticking = false // stop the chain; Update restarts it when needed
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case issuesMsg:
		if msg.gen != m.loadGen {
			return m, nil // a newer load is running (e.g. t pressed twice): its reply counts
		}
		next := m.scheduleRefresh(m.opts.Refresh) // the timer counts from the last load
		if msg.background {
			m.refreshing = false
			m.refreshErr = msg.err
			if msg.err != nil || msg.state != states[m.state] {
				return m, next // keep showing what we have
			}
			m.loadErr = nil // fresh data: a failed r no longer applies
		} else {
			m.loading = false
			m.loadErr = msg.err
			if msg.err != nil {
				m.setFlash(msg.err.Error(), true)
				return m, next
			}
			m.refreshErr = nil
		}
		if m.form != nil {
			// The open pop-up shows (and may act on) the issues as they
			// were: swap the list in once it closes.
			m.pendingLoad = &msg
			return m, next
		}
		return m, tea.Batch(next, m.installIssues(msg))
	case refreshTickMsg:
		return m.onRefreshTick(msg)
	case todosMsg:
		if msg.gen == m.loadGen {
			m.applyTodos(msg)
		}
	case hierarchyMsg:
		m.applyHierarchy(msg)
	case seenLoadedMsg:
		return m, m.onSeenLoaded(msg)
	case seenSavedMsg:
		return m, m.onSeenSaved(msg)
	case checkMsg:
		return m, m.applyCheck(msg)
	case listsTickMsg:
		m.metaGen++ // replies to loads still running are dropped
		m.projectsGen++
		return m, tea.Batch(m.loadMeta(), m.fetchProjects(), m.scheduleLists())
	case metaMsg:
		if msg.gen != m.metaGen {
			return m, nil // a newer load is running: its reply counts
		}
		if msg.err != nil {
			m.setFlash(msg.err.Error(), true)
			if m.members != nil || msg.me.Username == "" { // keep the user and lists we have
				return m, nil
			}
		}
		was := m.me.Username
		m.me, m.members = msg.me, msg.members
		if m.me.Username != was { // the "mine" filter and count depend on it
			m.rebuildView()
		}
	case updatedMsg:
		delete(m.busy, msg.id)
		if msg.what == "create" {
			m.creating = max(m.creating-1, 0)
		}
		if msg.form != nil && m.finishSending(msg) {
			return m, nil
		}
		if msg.err != nil {
			m.setFlash(msg.what+" failed: "+msg.err.Error(), true)
			return m, nil
		}
		fresh := msg.warn == "" // msg.issue is the server's copy after the action
		if msg.what == "create" {
			m.recent = msg.recent
			if msg.recentErr != nil {
				msg.warn = strings.TrimPrefix(msg.warn+" · could not remember the project: "+msg.recentErr.Error(), " · ")
			}
		}
		if msg.warn != "" {
			m.setFlash(msg.what+" ✓ "+msg.issue.Ref()+" · "+msg.warn, true)
		} else {
			m.setFlash(msg.what+" ✓ "+msg.issue.Ref(), false)
		}
		var seen tea.Cmd
		if fresh {
			m.replace(msg.issue, msg.what == "create")
			seen = m.markSeen(msg.issue) // your own change: never tagged
		}
		if msg.what == "comment" {
			return m, tea.Batch(seen, m.loadNotes(msg.issue))
		}
		return m, seen
	case notesMsg:
		if msg.gen != m.notesGen[msg.id] {
			return m, nil // a newer load of these comments is running: its reply counts
		}
		m.notes[msg.id], m.notesErr[msg.id] = msg.notes, msg.err
		m.refreshDetail()
	case editorMsg:
		return m.finishEditor(msg)
	case fieldEditorMsg:
		if msg.err != nil {
			m.setFlash("editor: "+msg.err.Error(), true)
		} else if f := m.form; f != nil && f.fields[f.focus].rows > 1 {
			in := &f.fields[f.focus].in
			in.SetValue(string(in.clean([]rune(msg.text)))) // like a paste: the file may hold anything
		}
	case projectsMsg:
		if msg.gen != m.projectsGen {
			return m, nil // a newer load is running: its reply counts
		}
		m.projectsLoading = false
		if msg.err != nil && m.projects != nil {
			return m, nil // a background reload failed: keep the old list
		}
		m.projects, m.projectsErr = msg.projects, msg.err
		m.rankPicker()
	case browserMsg:
		if msg.err != nil {
			// e.g. over SSH or on a headless box: show the URL to copy instead
			m.setFlash(msg.err.Error()+": "+msg.url, true)
		}
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		if m.mode == modeDetail {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

// quit ends the program, asking first while writes to GitLab are still
// running: quitting would drop their results unseen.
func (m Model) quit() (tea.Model, tea.Cmd) {
	n := len(m.busy) + m.creating
	if n == 0 {
		return m, m.quitCmd()
	}
	question := plural(n, "action", "actions") + " still running — quit anyway? y/N"
	m = m.openConfirm("Quit", question, func(m *Model) tea.Cmd { return m.quitCmd() })
	m.form.enterNo = true
	return m, nil
}

// quitCmd ends the program after writing unsaved seen state.
func (m *Model) quitCmd() tea.Cmd {
	if flush := m.flushSeen(); flush != nil {
		return tea.Sequence(flush, tea.Quit)
	}
	return tea.Quit
}

func (m *Model) setFlash(s string, bad bool) { m.flash, m.flashBad = s, bad }

// --- keys ---

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.form != nil {
		return m.handleFormKey(msg)
	}
	if m.inputKind != inputNone {
		return m.handleInputKey(msg)
	}
	m.flash = ""
	if m.showHelp {
		m.showHelp = false // any key closes help
		if msg.String() == "ctrl+c" {
			return m.quit()
		}
		return m, nil
	}
	if msg.String() == "h" || msg.String() == "?" {
		m.showHelp = true
		return m, nil
	}

	if m.mode == modeDetail {
		switch msg.String() {
		case "esc", "enter", "q", "backspace":
			m.mode = modeList
			m.clamp() // the list is taller than under the details
			return m, nil
		case "ctrl+c":
			return m.quit()
		case "home", "g": // the viewport has no keys for these
			m.vp.GotoTop()
			return m, nil
		case "end":
			m.vp.GotoBottom()
			return m, nil
		case "up", "k", "down", "j", "pgup", "pgdown", "ctrl+u", "ctrl+d", " ":
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case "right", "left", "]", "[": // next / previous issue without leaving details
			step := -1
			if msg.String() == "right" || msg.String() == "]" {
				step = 1
			}
			m.stepIssue(step)
			m.cursorMoved = ""
			var seen tea.Cmd
			if is := m.current(); is != nil {
				seen = m.markSeen(*is)
			}
			m.vp.GotoTop()
			m.refreshDetail()
			return m, tea.Batch(seen, m.loadNotesCurrent())
		}
		return m.handleAction(msg)
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m.quit()
	case "esc":
		if m.filtering() || len(m.selected) > 0 {
			m.search, m.mine, m.unassigned, m.attention = "", false, false, false
			m.selected = map[int]bool{}
			m.rebuildView()
			m.setFlash("filters and selection cleared", false)
		}
	case "up", "k":
		m.cursor--
		m.cursorMoved = ""
	case "down", "j":
		m.cursor++
		m.cursorMoved = ""
	case "pgup", "ctrl+u":
		m.cursor -= m.listHeight()
		m.cursorMoved = ""
	case "pgdown", "ctrl+d":
		m.cursor += m.listHeight()
		m.cursorMoved = ""
	case "home", "g":
		m.cursor = 0
		m.cursorMoved = ""
	case "end":
		m.cursor = len(m.rows) - 1
		m.cursorMoved = ""
	case " ":
		m.cursorMoved = ""
		if is := m.current(); is != nil {
			m.selected[is.ID] = !m.selected[is.ID]
			if !m.selected[is.ID] {
				delete(m.selected, is.ID)
			}
			m.cursor++
		} else if m.onLabel() {
			m.setFlash(notAnIssue, false)
		}
	case "v":
		m.toggleAll()
	case "enter", "tab", "right":
		if m.onLabel() {
			m.toggleFold()
			return m, nil
		}
		if is := m.current(); is != nil {
			seen := m.markSeen(*is)
			m.cursorMoved = "" // opened: the user sees which issue it is
			m.mode = modeDetail
			m.vp.GotoTop()
			m.refreshDetail()
			return m, tea.Batch(seen, m.loadNotesCurrent())
		}
	case "n":
		return m.openNewIssue()
	case "z":
		m.compact = !m.compact
	case "l":
		return m.cycleTree()
	case "f":
		m.toggleFold()
		return m, nil
	case "b":
		m.toggleAllFolds()
		return m, nil
	case "m":
		m.mine = !m.mine
		m.rebuildView()
	case "u":
		m.unassigned = !m.unassigned
		m.rebuildView()
	case "o":
		m.attention = !m.attention
		m.rebuildView()
	case "s":
		m.sortBy = (m.sortBy + 1) % len(sortNames)
		m.rebuildView()
		m.setFlash("sort: "+sortNames[m.sortBy], false)
	case "t":
		m.state = (m.state + 1) % len(states)
		m.selected = map[int]bool{}
		m.setFlash("showing "+states[m.state]+" issues", false)
		return m, m.startLoad()
	case "/":
		return m.startSearch()
	case "r":
		if m.loading {
			m.setFlash("still loading", false)
			return m, nil
		}
		m.metaGen++
		return m, tea.Batch(m.startLoad(), m.loadMeta())
	default:
		return m.handleAction(msg)
	}
	m.clamp()
	return m, nil
}

// handleAction handles the issue actions shared by list and detail mode.
func (m Model) handleAction(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "n" {
		return m.openNewIssue()
	}
	if m.current() == nil {
		switch msg.String() {
		case "c", "a", "i", "d", "e", "x", "w":
			if m.onLabel() {
				m.setFlash(notAnIssue, false)
			}
		}
		return m, nil
	}
	targets := m.targets()
	fromSel := m.usingSelection()
	switch msg.String() {
	case "c":
		return m.openComment()
	case "e":
		// The editor gets the description exactly as GitLab has it: the
		// whole text is sent back, so a sanitized copy would silently drop
		// characters (zero-width joiners, soft hyphens, CR) on save.
		is := *m.current()
		if gl.HasControl(is.RawDescription) {
			// The editor would show them to the terminal as they are.
			m.setFlash("the description holds control or bidi characters; edit it on GitLab (w)", true)
			return m, nil
		}
		return m, m.runEditor(is.RawDescription, true, func(text, file string, err error) tea.Msg {
			return editorMsg{issue: is, kind: "description", text: text, origRaw: is.RawDescription, file: file, err: err}
		})
	case "a":
		return m.openAssign(targets).markSelection(fromSel), nil
	case "i":
		if m.me.Username == "" {
			m.setFlash("still loading your user; try again", true)
			return m, nil
		}
		spec := "+" + m.me.Username
		var why string
		switch {
		case len(targets) > 1: // a bulk action: ask first, like x
		case fromSel && m.hiddenTargets(targets) > 0:
			why = "The selected issue is hidden in a folded group."
		case !fromSel && m.cursorMoved != "":
			// The cursor landed on this issue by itself: never write to it
			// unasked.
			why = m.cursorMoved + " left the list; the cursor moved to this issue."
		}
		if len(targets) > 1 || why != "" {
			question := fmt.Sprintf("Assign yourself (@%s) to these %d issues?", m.me.Username, len(targets))
			if len(targets) == 1 {
				question = fmt.Sprintf("Assign yourself (@%s) to this issue?", m.me.Username)
			}
			if why != "" {
				question = why + "\n" + question
			}
			m.cursorMoved = ""
			return m.openConfirm("Assign me", m.describeTargets(targets)+"\n\n"+question,
				func(m *Model) tea.Cmd { return m.assign(targets, spec) }).markSelection(fromSel), nil
		}
		cmd := m.assign(targets, spec)
		if fromSel {
			m.clearSelection()
		}
		return m, cmd
	case "d":
		return m.openDue(targets).markSelection(fromSel), nil
	case "x":
		event, verb, apply, skip := closeOrReopen(targets)
		question := verb + " this issue?"
		if len(apply) > 1 {
			question = fmt.Sprintf("%s these %d issues?", verb, len(apply))
		}
		question += skippedNote(event, skip)
		return m.openConfirm(verb+" issue", m.describeTargets(apply)+"\n\n"+question, func(m *Model) tea.Cmd {
			return m.updateMany(apply, event, func(gl.Issue) map[string]any {
				return map[string]any{"state_event": event}
			})
		}).markSelection(fromSel), nil
	case "w":
		return m, openBrowser(m.current().WebURL, m.client.Host, m.opts.Browser)
	}
	return m, nil
}

// closeOrReopen decides what x does for the target issues: close the open
// ones if there are any, else reopen the closed ones. It returns the
// issues the action applies to and how many targets it leaves alone.
func closeOrReopen(targets []gl.Issue) (event, verb string, apply []gl.Issue, skip int) {
	event, verb, want := "reopen", "Reopen", "closed"
	for _, is := range targets {
		if is.State != "closed" {
			event, verb, want = "close", "Close", ""
			break
		}
	}
	for _, is := range targets {
		if (is.State == "closed") == (want == "closed") {
			apply = append(apply, is)
		} else {
			skip++
		}
	}
	return event, verb, apply, skip
}

// skippedNote explains which selected issues x leaves alone.
func skippedNote(event string, skip int) string {
	already := "closed"
	if event == "reopen" {
		already = "open"
	}
	switch skip {
	case 0:
		return ""
	case 1:
		return "\n1 selected issue is already " + already + " and stays as it is."
	}
	return fmt.Sprintf("\n%d selected issues are already %s and stay as they are.", skip, already)
}

// usingSelection reports whether actions apply to the selected issues
// (list view with a visible selection) rather than the one under the
// cursor.
func (m Model) usingSelection() bool { return m.mode == modeList && m.selectedShown() > 0 }

// selectedShown counts the selected issues in the view: what the title
// and footer announce and the bulk actions act on (targets). Selected
// issues a filter or search hides are neither, until it is lifted.
func (m Model) selectedShown() int {
	n := 0
	for _, i := range m.view {
		if m.selected[m.issues[i].ID] {
			n++
		}
	}
	return n
}

func (m *Model) clearSelection() { m.selected = map[int]bool{} }

// markSelection records on a just-opened pop-up whether it acts on the
// selection, so the selection is cleared once the action is carried out
// (but kept if the pop-up is cancelled).
func (m Model) markSelection(fromSel bool) Model {
	if m.form != nil {
		m.form.fromSelection = fromSel
	}
	return m
}

func (m Model) startSearch() (tea.Model, tea.Cmd) {
	m.inputKind = inputSearch
	m.input = lineInput{Prompt: "/", Placeholder: "search title, project, people, labels · enter keep · esc cancel"}
	m.input.SetValue(m.search)
	return m, nil
}

func (m Model) handleInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.inputKind = inputNone
		m.search = ""
		m.rebuildView()
		return m, nil
	case "enter":
		m.inputKind = inputNone
		m.search = strings.TrimSpace(m.input.Value())
		m.rebuildView()
		return m, nil
	}
	m.input.Update(msg)
	m.search = m.input.Value()
	m.rebuildView()
	return m, nil
}

// targets returns copies of the selected visible issues, or of the one
// under the cursor if none are selected or we are in detail mode. They
// are copies, not indices, so a reload cannot change what an action that
// is still being set up applies to.
func (m Model) targets() []gl.Issue {
	if m.mode == modeList && len(m.selected) > 0 {
		var t []gl.Issue
		for _, i := range m.view {
			if m.selected[m.issues[i].ID] {
				t = append(t, m.issues[i])
			}
		}
		if len(t) > 0 {
			return t
		}
	}
	if is := m.current(); is != nil {
		return []gl.Issue{*is}
	}
	return nil
}

// latestByID is the list by issue ID, for the newest copy of each target
// (it may have been reloaded since the target was taken; one that left
// the list is missing) without a scan per target. A list held back behind
// a pop-up (pendingLoad) is newer than the one shown: an action from the
// pop-up starts from its copies, or it would undo what changed meanwhile
// (an assignee someone added, say).
func (m Model) latestByID() map[int]gl.Issue {
	byID := make(map[int]gl.Issue, len(m.issues))
	for _, is := range m.issues {
		byID[is.ID] = is
	}
	if p := m.pendingLoad; p != nil {
		for _, is := range p.issues {
			byID[is.ID] = is
		}
	}
	return byID
}

// current is the issue under the cursor; nil on a group label or in an
// empty list.
func (m Model) current() *gl.Issue {
	if m.cursor < 0 || m.cursor >= len(m.rows) || m.rows[m.cursor].isLabel() {
		return nil
	}
	return &m.issues[m.rows[m.cursor].issue]
}

// notAnIssue answers an issue key pressed on a group label.
const notAnIssue = "not an issue: enter or f folds the group"

// onLabel reports whether the cursor is on a group label.
func (m Model) onLabel() bool {
	return m.cursor >= 0 && m.cursor < len(m.rows) && m.rows[m.cursor].isLabel()
}

// cursorKey names the row under the cursor ("" in an empty list), so it
// can be found again after the rows are rebuilt.
func (m Model) cursorKey() string {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return ""
	}
	return m.rowKey(m.rows[m.cursor])
}

// cursorName names the row under the cursor for a message: the issue's
// reference or "group <path>"; "" in an empty list.
func (m Model) cursorName() string {
	if is := m.current(); is != nil {
		return is.Ref()
	}
	if m.onLabel() {
		return "group " + m.labelName(m.rows[m.cursor])
	}
	return ""
}

// stepIssue moves the cursor to the next (step 1) or previous (-1)
// issue row, past group labels; it stays put if there is none.
func (m *Model) stepIssue(step int) {
	for c := m.cursor + step; c >= 0 && c < len(m.rows); c += step {
		if !m.rows[c].isLabel() {
			m.cursor = c
			break
		}
	}
	m.clamp()
}

// toggleAll selects every issue shown, or clears the selection if all
// of them are selected already (key v). Issues hidden under a fold in the
// tree are left as they are: v never selects what you cannot see.
func (m *Model) toggleAll() {
	var shown []int // issue IDs with a row
	for _, r := range m.rows {
		if !r.isLabel() {
			shown = append(shown, m.issues[r.issue].ID)
		}
	}
	all := true
	for _, id := range shown {
		if !m.selected[id] {
			all = false
			break
		}
	}
	if all {
		m.clearSelection()
		return
	}
	for _, id := range shown {
		m.selected[id] = true
	}
	if hidden := len(m.view) - len(shown); hidden > 0 {
		m.setFlash(fmt.Sprintf("selected the %d issues shown; %d in folded groups are not (b unfolds all)", len(shown), hidden), false)
	}
}

// hiddenTargets counts the targets with no row: hidden in a folded group
// or under a folded parent in the tree.
func (m Model) hiddenTargets(targets []gl.Issue) int {
	shown := map[int]bool{}
	for _, r := range m.rows {
		if !r.isLabel() {
			shown[m.issues[r.issue].ID] = true
		}
	}
	n := 0
	for _, is := range targets {
		if !shown[is.ID] {
			n++
		}
	}
	return n
}

// replace swaps in an updated copy of an issue. A new issue (add) is
// appended and the cursor moves to it, unfolding the label or parent it
// is under, if the list shows its state (the closed list does not gain an
// open issue); an updated issue that is no
// longer in the list (e.g. it left it while an editor was open) is not
// re-added. Open details stay on the issue they show (as across a reload):
// the next action targets what the pane shows, not the new issue.
func (m *Model) replace(is gl.Issue, add bool) {
	if p := m.pendingLoad; p != nil {
		// The list held back behind a pop-up is installed over this one
		// as soon as the pop-up closes: give it the fresh copy too, or it
		// would undo the change, or drop the new issue, until the next load.
		p.issues = replaceIssue(p.issues, is, add && m.inState(is))
	}
	for i := range m.issues {
		if m.issues[i].ID == is.ID {
			m.issues[i] = is
			if i < len(m.cache) {
				m.cache[i] = m.cacheIssue(is) // even if the server kept updated_at
			}
			m.rebuildView()
			m.refreshDetail()
			return
		}
	}
	if !add || !m.inState(is) {
		return
	}
	m.issues = append(m.issues, is)
	if m.mode == modeDetail {
		m.rebuildView()
		return
	}
	key := issueKey(is.ID)
	m.rebuildViewKeep(key)
	if m.cursorKey() != key && m.reveal(is.ID) { // under a folded label or parent
		m.rebuildViewKeep(key)
	}
}

// replaceIssue swaps is into list, or appends it (add) when it is not there.
func replaceIssue(list []gl.Issue, is gl.Issue, add bool) []gl.Issue {
	for i := range list {
		if list[i].ID == is.ID {
			list[i] = is
			return list
		}
	}
	if add {
		list = append(list, is)
	}
	return list
}

// inState reports whether an issue is in the state the list shows. An
// issue closed or reopened from the list keeps its row (so it can be
// reopened at once) until the next reload, but is not counted.
func (m Model) inState(is gl.Issue) bool {
	return states[m.state] == "all" || is.State == states[m.state]
}

// --- filtering and sorting ---

// matches reports whether an issue passes the filters and the search
// (words, all of which must occur in hay, its search text).
func (m Model) matches(is gl.Issue, hay string, words []string, now time.Time) bool {
	if m.mine && !hasAssignee(is, m.me.Username) {
		return false
	}
	if m.unassigned && len(is.Assignees) > 0 {
		return false
	}
	if m.attention {
		d, ok := is.DaysLeft(now)
		if !ok || d > m.opts.DueSoonDays {
			return false
		}
	}
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func hasAssignee(is gl.Issue, username string) bool {
	for _, u := range is.Assignees {
		if u.Username == username {
			return true
		}
	}
	return false
}

// issueCache is what the list keeps per issue rather than computing again
// on every keystroke or frame: the text the search looks in (ref, title,
// assignees and labels, lower-cased and joined) and the width of the
// short ref for the compact columns. Building both for every issue on
// every search keystroke is what made the search slow on a large group.
// An entry is kept while the issue's ID and update time are the same
// (GitLab bumps updated_at with every change).
type issueCache struct {
	id      int
	updated time.Time
	hay     string
	refW    int
}

func (m Model) cacheIssue(is gl.Issue) issueCache {
	hay := strings.ToLower(is.Ref() + " " + is.Title + " " + strings.Join(is.AssigneeNames(), " ") +
		" " + strings.Join(is.Labels, " "))
	return issueCache{id: is.ID, updated: is.UpdatedAt, hay: hay, refW: lipgloss.Width(m.shortRef(is))}
}

// refreshCache brings cache in step with issues: an entry is made for
// every issue that has none or whose entry belongs to another or an
// older copy (after a reload that reordered the list, or an action's
// reply).
func (m *Model) refreshCache() {
	if len(m.cache) > len(m.issues) {
		m.cache = m.cache[:len(m.issues)]
	}
	for i, is := range m.issues {
		if i == len(m.cache) {
			m.cache = append(m.cache, m.cacheIssue(is))
		} else if c := m.cache[i]; c.id != is.ID || !c.updated.Equal(is.UpdatedAt) {
			m.cache[i] = m.cacheIssue(is)
		}
	}
}

// rebuildView refilters, resorts and lays out the list, keeping the
// cursor on its row.
func (m *Model) rebuildView() { m.rebuildViewKeep(m.cursorKey()) }

// rebuildViewKeep is rebuildView with the cursor going to the row named
// keep (see rowKey), if it is still shown; else the cursor index stays.
func (m *Model) rebuildViewKeep(keep string) {
	now := time.Now()
	words := strings.Fields(strings.ToLower(m.search))
	m.refreshCache()
	m.view = make([]int, 0, len(m.issues))
	for i, is := range m.issues {
		if m.matches(is, m.cache[i].hay, words, now) {
			m.view = append(m.view, i)
		}
	}
	sort.SliceStable(m.view, func(a, b int) bool {
		return less(m.issues[m.view[a]], m.issues[m.view[b]], sortNames[m.sortBy])
	})
	// a filter shows its matches unfolded; otherwise everything starts folded
	filtering := m.filtering()
	m.rows = buildRows(m.issues, m.view, m.group, m.tree, m.parents, m.opened, filtering, now, m.opts.DueSoonDays)
	if !filtering && m.openLoneLabel() {
		m.rows = buildRows(m.issues, m.view, m.group, m.tree, m.parents, m.opened, false, now, m.opts.DueSoonDays)
	}
	for ri, r := range m.rows {
		if m.rowKey(r) == keep {
			m.cursor = ri
			break
		}
	}
	m.stats = m.countIssues(now)
	m.clamp()
}

// listStats is what the title line and the list header count over every
// issue: computed by rebuildViewKeep, once per load, change, filter or
// sort, rather than on every frame (the spinner redraws several times a
// second). The due counts depend on the day, so Update rebuilds the view
// once it changes (see sameDay).
type listStats struct {
	day                             time.Time // local day of the due counts
	refW                            int       // width of the widest short ref shown (compact columns)
	listed, shown                   int       // issues in the list's state: all, and those passing the filters
	overdue, soon, mine, unassigned int       // open issues, in the title
}

// countIssues computes the listStats for the current list and view.
func (m Model) countIssues(now time.Time) listStats {
	st := listStats{day: now, refW: 8}
	for _, i := range m.view {
		st.refW = max(st.refW, m.cache[i].refW)
		if m.inState(m.issues[i]) {
			st.shown++
		}
	}
	st.refW = min(st.refW, 32)
	for _, is := range m.issues {
		if !m.inState(is) {
			continue
		}
		st.listed++
		if is.State != "opened" {
			continue
		}
		if d, ok := is.DaysLeft(now); ok {
			if d < 0 {
				st.overdue++
			} else if d <= m.opts.DueSoonDays {
				st.soon++
			}
		}
		if hasAssignee(is, m.me.Username) {
			st.mine++
		}
		if len(is.Assignees) == 0 {
			st.unassigned++
		}
	}
	return st
}

// sameDay reports whether the counts (in the title and on the group
// rows) were made on now's day; an issue due yesterday is overdue today.
func (m Model) sameDay(now time.Time) bool {
	y1, m1, d1 := m.stats.day.Date()
	y2, m2, d2 := now.Date()
	return y1 == y2 && m1 == m2 && d1 == d2
}

// titleStats returns the cached stats, recounted if the day has turned
// since. View cannot keep the recount, so Update rebuilds the view (the
// group rows count due dates too) before the next frame.
func (m Model) titleStats(now time.Time) listStats {
	if !m.sameDay(now) {
		return m.countIssues(now)
	}
	return m.stats
}

// less orders issues for the given sort key. Ties fall back to most
// recently updated first.
func less(a, b gl.Issue, by string) bool {
	switch by {
	case "due":
		switch {
		case a.DueDate == "" && b.DueDate != "":
			return false
		case a.DueDate != "" && b.DueDate == "":
			return true
		case a.DueDate != b.DueDate:
			return a.DueDate < b.DueDate
		}
	case "project":
		if a.Project() != b.Project() {
			return a.Project() < b.Project()
		}
		return a.IID > b.IID
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

func (m *Model) clamp() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

// listHeight is the number of issues that fit on screen.
func (m Model) listHeight() int {
	lines := max(m.height-m.chromeLines(), 1)
	if m.compact {
		return lines
	}
	return max(lines/3, 1) // two lines per issue plus a blank line between
}

// --- commands ---

// startLoad starts a user-requested load of the issue list. Replies to
// any earlier load, including a background refresh, are dropped.
func (m *Model) startLoad() tea.Cmd {
	m.loadGen++
	m.loading, m.refreshing = true, false
	m.hierErr = nil // a manual reload tries the child items again at once
	return tea.Batch(m.loadIssues(), m.loadTodos(), m.loadHierarchy())
}

// loadIssues loads the current list, tagged with the current loadGen.
func (m Model) loadIssues() tea.Cmd {
	c, group, which, gen := m.client, m.group, states[m.state], m.loadGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		is, err := c.GroupIssues(ctx, group, which)
		return issuesMsg{issues: is, state: which, gen: gen, err: err}
	}
}

// loadMeta loads the user and the members, tagged with the current metaGen.
func (m Model) loadMeta() tea.Cmd {
	c, group, gen := m.client, m.group, m.metaGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		me, err := c.Me(ctx)
		if err != nil {
			return metaMsg{gen: gen, err: err}
		}
		members, err := c.Members(ctx, group)
		return metaMsg{me: me, members: members, gen: gen, err: err}
	}
}

func (m Model) loadNotesCurrent() tea.Cmd {
	if is := m.current(); is != nil {
		return m.loadNotes(*is)
	}
	return nil
}

// loadNotes loads the comments of an issue, tagged with its notesGen (the
// map is shared by the model's copies).
func (m Model) loadNotes(is gl.Issue) tea.Cmd {
	m.notesGen[is.ID]++
	c, gen := m.client, m.notesGen[is.ID]
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		notes, err := c.Notes(ctx, is.Project(), is.IID)
		return notesMsg{id: is.ID, notes: notes, gen: gen, err: err}
	}
}

// updateMany runs a PUT on each target issue with the fields returned by
// fields, actionWorkers at a time. Issues with another action still
// running are skipped, and the flash line says how many.
func (m *Model) updateMany(targets []gl.Issue, what string, fields func(gl.Issue) map[string]any) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(targets))
	c := m.client
	skipped := 0
	defer func() { m.reportSkipped(what, skipped) }()
	for _, is := range targets {
		if m.busy[is.ID] != "" {
			skipped++
			continue
		}
		m.busy[is.ID] = what
		f := fields(is)
		cmds = append(cmds, m.limited(func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			up, err := c.Update(ctx, is.Project(), is.IID, f)
			return updatedMsg{id: is.ID, what: what, issue: up, err: err}
		}))
	}
	return tea.Batch(cmds...)
}

// limited runs cmd once one of the actionWorkers slots is free, so a bulk
// action over many issues starts a few glab processes at a time rather
// than one per issue at once. The wait does not count against cmd's own
// timeout, which starts inside it.
func (m Model) limited(cmd tea.Cmd) tea.Cmd {
	slots := m.actions
	if slots == nil {
		return cmd
	}
	return func() tea.Msg {
		slots <- struct{}{}
		defer func() { <-slots }()
		return cmd()
	}
}

// reportSkipped tells the user that an action left some issues alone
// because another action on them was still running.
func (m *Model) reportSkipped(what string, skipped int) {
	if skipped == 0 {
		return
	}
	m.setFlash(fmt.Sprintf("%s: skipped %s with another action still running; try again shortly",
		what, plural(skipped, "issue", "issues")), true)
}

// assign applies an assignee spec (see ApplyAssign) to each target issue,
// starting from its newest known assignees, actionWorkers at a time.
func (m *Model) assign(targets []gl.Issue, spec string) tea.Cmd {
	c, members := m.client, m.members
	cmds := make([]tea.Cmd, 0, len(targets))
	latest := m.latestByID()
	skipped := 0
	defer func() { m.reportSkipped("assign", skipped) }()
	for _, is := range targets {
		if cur, ok := latest[is.ID]; ok {
			is = cur
		}
		if m.busy[is.ID] != "" {
			skipped++
			continue
		}
		m.busy[is.ID] = "assign"
		names := ApplyAssign(is.AssigneeNames(), spec)
		cmds = append(cmds, m.limited(func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			ids, err := c.AssigneeIDs(ctx, names, members)
			if err != nil {
				return updatedMsg{id: is.ID, what: "assign", err: err}
			}
			if ids == nil {
				ids = []int{} // explicit empty list unassigns everyone
			}
			up, err := c.Update(ctx, is.Project(), is.IID, map[string]any{"assignee_ids": ids})
			return updatedMsg{id: is.ID, what: "assign", issue: up, err: err}
		}))
	}
	return tea.Batch(cmds...)
}

// ApplyAssign computes the new assignee list. A spec whose tokens all start
// with + or - edits the current list; otherwise the spec replaces it.
// Mixed specs apply plain names as a replacement first, then the edits.
func ApplyAssign(current []string, spec string) []string {
	var set, add, del []string
	replace := false
	for _, tok := range strings.Split(spec, ",") {
		tok = strings.TrimPrefix(strings.TrimSpace(tok), "@")
		switch {
		case tok == "":
		case strings.HasPrefix(tok, "+"):
			add = append(add, strings.TrimPrefix(tok[1:], "@"))
		case strings.HasPrefix(tok, "-"), strings.HasPrefix(tok, "!"):
			del = append(del, strings.TrimPrefix(tok[1:], "@"))
		default:
			replace = true
			set = append(set, tok)
		}
	}
	base := current
	if replace || (len(add) == 0 && len(del) == 0) {
		base = set
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range append(append([]string{}, base...), add...) {
		k := strings.ToLower(n)
		if n == "" || seen[k] || containsFold(del, n) {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func (m Model) comment(is gl.Issue, body string) tea.Cmd {
	c := m.client
	m.busy[is.ID] = "comment"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		if err := c.Comment(ctx, is.Project(), is.IID, body); err != nil {
			return updatedMsg{id: is.ID, what: "comment", err: err}
		}
		// The comment is posted: a failed refetch must not look like a
		// failure, or people post the same comment twice.
		up, err := c.GetIssue(ctx, is.Project(), is.IID)
		if err != nil {
			return updatedMsg{id: is.ID, what: "comment", issue: is, warn: "could not reload the issue; press r"}
		}
		return updatedMsg{id: is.ID, what: "comment", issue: up}
	}
}
