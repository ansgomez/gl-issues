package tui

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"gl-issues/internal/gl"
	"gl-issues/internal/state"
)

// Update tags that survive restarts. The seen file (internal/state) holds,
// per issue ID, the updated_at and notes count the user last saw. An issue
// is tagged "updated" when it differs from that, and "new" when it has no
// entry although its list was compared before. Opening an issue, or a
// write by gl-issues, marks it seen. Changes whose notes since the seen
// version (system notes included) are all your own are marked seen
// silently. Only the server's
// updated_at values are compared, never local time. All of it is off with
// notify = false.

const (
	ownSlack     = 5 * time.Second // a note this much older than the issue's change still counts as that change
	maxChecks    = 50              // note requests per comparison; the rest are tagged unchecked
	checkWorkers = 4               // note requests at a time
)

type seenLoadedMsg struct {
	seen    state.Seen
	existed bool
	err     error
}

type seenSavedMsg struct{ err error }

// checkMsg carries the result of the own-change checks for one comparison.
type checkMsg struct {
	gen   int
	me    gl.User      // the logged-in user, if the check had to fetch it
	own   map[int]bool // issue ID -> the change was your own
	cands []gl.Issue   // the changed issues, as they were checked
	added []int        // new issues, tagged together with the changes
	note  string       // flash prefix, e.g. "refreshed: "
	quiet bool         // don't replace a flash already shown
	gone  int          // closed or removed since the previous list
}

// seenWriter serialises writes of the seen file and drops snapshots older
// than one already written (a save and the flush on quit can overlap).
type seenWriter struct {
	mu   sync.Mutex
	last int
}

func (w *seenWriter) write(name string, seq int, s state.Seen) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if seq <= w.last {
		return nil
	}
	s.WrittenAt = time.Now()
	if err := state.SaveSeen(name, s); err != nil {
		return err
	}
	w.last = seq
	return nil
}

func seenEntry(is gl.Issue) state.SeenIssue {
	return state.SeenIssue{UpdatedAt: is.UpdatedAt, Notes: is.Notes, State: is.State}
}

// unchanged reports whether is still looks as it did when seen.
func unchanged(e state.SeenIssue, is gl.Issue) bool {
	return e.UpdatedAt.Equal(is.UpdatedAt) && e.Notes == is.Notes
}

// viewStates are the issue states a list view shows.
func viewStates(view string) []string {
	switch view {
	case "opened", "closed":
		return []string{view}
	}
	return []string{"opened", "closed"}
}

// loadSeen reads the seen file; nil with notify off.
func (m Model) loadSeen() tea.Cmd {
	if !m.opts.Notify {
		return nil
	}
	name := m.seenFile
	return func() tea.Msg {
		s, existed, err := state.LoadSeen(name)
		return seenLoadedMsg{seen: s, existed: existed, err: err}
	}
}

// onSeenLoaded installs the seen file and, once the list is there too,
// compares them. A corrupt or unreadable file starts a fresh baseline.
func (m *Model) onSeenLoaded(msg seenLoadedMsg) tea.Cmd {
	s, existed := msg.seen, msg.existed
	if msg.err != nil {
		if m.seenFile != "" {
			m.setFlash("could not read the list of seen issues; starting afresh", false)
		}
		s, existed = state.Seen{}, false
	}
	if s.Issues == nil {
		s.Issues = map[int]state.SeenIssue{}
	}
	s.Host, s.Group = m.client.Host, m.group
	maps.Copy(s.Issues, m.seenEarly) // opened before the file was read
	m.seenEarly = nil
	m.seen, m.seenExisted = &s, existed
	if m.lastLoad.IsZero() {
		return nil // compared when the list arrives
	}
	return m.compareSeen("", false, 0)
}

// compareSeen compares the installed list with the seen file: unchanged
// issues need nothing, issues without an entry are new (if their list was
// compared before) or join the baseline, and changed ones are checked for
// your own changes in a command whose reply applies all tags at once.
// Entries the list should have shown but did not are pruned.
func (m *Model) compareSeen(note string, quiet bool, gone int) tea.Cmd {
	s := m.seen
	if !m.seenStarted {
		note, quiet = "since last time: ", false
	}
	baseline := !m.seenExisted && !m.seenStarted // the very first run: tag nothing
	m.seenStarted = true
	m.checkGen++

	covered := map[string]bool{}
	for _, st := range s.Covered {
		covered[st] = true
	}
	var added []int
	var check, sticky []gl.Issue
	seenAt := map[int]time.Time{}
	listed := make(map[int]bool, len(m.issues))
	for _, is := range m.issues {
		listed[is.ID] = true
		e, ok := s.Issues[is.ID]
		switch {
		case !ok && (baseline || !covered[is.State] || m.ownNew(is)):
			s.Issues[is.ID] = seenEntry(is)
		case !ok:
			if m.fresh[is.ID] != "new" {
				added = append(added, is.ID)
			}
		case unchanged(e, is):
			e.State = is.State
			s.Issues[is.ID] = e
		case e.Tagged || m.fresh[is.ID] == "updated":
			// Still unseen: a later change of yours must not hide an
			// earlier one by someone else, so it is not checked again.
			e.Tagged = true
			s.Issues[is.ID] = e
			sticky = append(sticky, is)
		default:
			check = append(check, is)
			seenAt[is.ID] = e.UpdatedAt
		}
	}
	view := viewStates(m.loadedState)
	for id, e := range s.Issues {
		if !listed[id] && (e.State == "" || slices.Contains(view, e.State)) {
			delete(s.Issues, id) // closed, moved out or removed
		}
	}
	for _, st := range view {
		if !covered[st] {
			s.Covered = append(s.Covered, st)
		}
	}
	slices.Sort(s.Covered)

	save := m.saveSeen()
	msg := checkMsg{gen: m.checkGen, cands: append(check, sticky...), added: added, note: note, quiet: quiet,
		gone: gone, own: map[int]bool{}}
	if len(check) == 0 {
		return tea.Batch(save, m.applyCheck(msg))
	}
	c, me, gen := m.client, m.me, m.checkGen
	return tea.Batch(save, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg.own, msg.me = ownChanges(ctx, c, me, check, seenAt)
		msg.gen = gen
		return msg
	})
}

// ownNew reports whether a new issue is one you opened yourself and
// nobody has touched since.
func (m Model) ownNew(is gl.Issue) bool {
	return m.me.ID != 0 && is.Author.ID == m.me.ID && is.Notes == 0 &&
		is.UpdatedAt.Sub(is.CreatedAt) <= ownSlack
}

// ownChanges asks, for each changed issue, whether every note (system
// notes included) updated since the version you saw (seenAt, less
// ownSlack) is yours, and the newest no older than the change (less
// ownSlack). It fetches the logged-in user if me is unknown, checks at
// most maxChecks issues (the most recently updated), checkWorkers at a
// time, and fails open: an issue whose check fails, or whose page of
// notes is all newer (there may be more), is not yours.
func ownChanges(ctx context.Context, c gl.Client, me gl.User, cands []gl.Issue, seenAt map[int]time.Time) (map[int]bool, gl.User) {
	own := map[int]bool{}
	if me.ID == 0 {
		u, err := c.Me(ctx)
		if err != nil || u.ID == 0 {
			return own, me
		}
		me = u
	}
	check := slices.Clone(cands)
	slices.SortStableFunc(check, func(a, b gl.Issue) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	check = check[:min(len(check), maxChecks)]

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, checkWorkers)
	for _, is := range check {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			ns, err := c.RecentNotes(ctx, is.Project(), is.IID)
			if err == nil && allOwn(ns, me, seenAt[is.ID].Add(-ownSlack), is.UpdatedAt.Add(-ownSlack)) {
				mu.Lock()
				own[is.ID] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return own, me
}

// allOwn reports whether notes (newest first, one page) show only your
// changes since since: at least one note after since, all of those
// yours, the newest not before change, and not a full page of them.
func allOwn(notes []gl.Note, me gl.User, since, change time.Time) bool {
	newer := 0
	for _, n := range notes {
		if !n.UpdatedAt.After(since) {
			break
		}
		if n.Author.ID != me.ID {
			return false
		}
		newer++
	}
	return newer > 0 && newer < gl.RecentNotesPage && !notes[0].UpdatedAt.Before(change)
}

// applyCheck applies the tags of one comparison at once and reports them.
// Issues opened or changed again since the check started are left alone.
func (m *Model) applyCheck(msg checkMsg) tea.Cmd {
	if msg.gen != m.checkGen || m.seen == nil {
		return nil // a newer comparison is running
	}
	if m.me.ID == 0 && msg.me.ID != 0 {
		m.me = msg.me
		m.rebuildView() // the "mine" filter and count depend on it
	}
	cur := m.latestByID()
	s := m.seen
	ch := changes{gone: msg.gone}
	for _, id := range msg.added {
		if _, ok := cur[id]; !ok {
			continue
		}
		if _, ok := s.Issues[id]; !ok && m.fresh[id] != "new" {
			m.fresh[id] = "new"
			ch.added = append(ch.added, id)
		}
	}
	changed := false
	for _, was := range msg.cands {
		is, ok := cur[was.ID]
		e, in := s.Issues[was.ID]
		if !ok || !in || unchanged(e, is) || !unchanged(seenEntry(was), is) {
			continue
		}
		if msg.own[is.ID] {
			s.Issues[is.ID] = seenEntry(is)
			delete(m.fresh, is.ID)
			changed = true
			continue
		}
		if !e.Tagged {
			e.Tagged = true
			s.Issues[is.ID] = e
			changed = true
		}
		if m.fresh[is.ID] != "updated" {
			m.fresh[is.ID] = "updated"
			ch.updated = append(ch.updated, is.ID)
		}
	}
	// A quiet refresh does not replace another message, except the note
	// about the cursor's row leaving the list that it starts with.
	if !ch.empty() && (!msg.quiet || m.flash == "" || strings.HasPrefix(msg.note, m.flash)) {
		m.setFlash(msg.note+ch.String(), false)
	}
	if !changed {
		return nil
	}
	return m.saveSeen()
}

// markSeen records is as seen (opened, or written by gl-issues) and clears
// its tag.
func (m *Model) markSeen(is gl.Issue) tea.Cmd {
	delete(m.fresh, is.ID)
	if !m.opts.Notify {
		return nil
	}
	if m.seen == nil {
		if m.seenEarly == nil {
			m.seenEarly = map[int]state.SeenIssue{}
		}
		m.seenEarly[is.ID] = seenEntry(is)
		return nil
	}
	m.seen.Issues[is.ID] = seenEntry(is)
	return m.saveSeen()
}

// saveSeen writes the seen file from a command. Writes coalesce: while
// one runs, further changes only mark the file dirty, and one more write
// follows when it is done.
func (m *Model) saveSeen() tea.Cmd {
	if m.seen == nil || m.seenFile == "" {
		return nil
	}
	m.seenDirty = true
	if m.seenSaving {
		return nil
	}
	return m.writeSeen()
}

func (m *Model) writeSeen() tea.Cmd {
	m.seenDirty, m.seenSaving = false, true
	m.seenSeq++
	snap := *m.seen
	snap.Issues = maps.Clone(m.seen.Issues)
	snap.Covered = slices.Clone(m.seen.Covered)
	w, name, seq := m.seenWriter, m.seenFile, m.seenSeq
	return func() tea.Msg { return seenSavedMsg{err: w.write(name, seq, snap)} }
}

func (m *Model) onSeenSaved(msg seenSavedMsg) tea.Cmd {
	m.seenSaving = false
	if msg.err != nil {
		if !m.seenErrShown {
			m.seenErrShown = true // once per session: it would only repeat
			m.setFlash("could not save the list of seen issues: "+msg.err.Error(), true)
		}
		if !m.seenDirty {
			// Still unsaved: the next change, or quitting (flushSeen),
			// writes again. Not now: a lasting error would loop.
			m.seenDirty = true
			return nil
		}
	}
	if m.seenDirty {
		return m.writeSeen()
	}
	return nil
}

// flushSeen writes unsaved seen state before quitting; nil if none. A
// write still running is written over once it is done (the writer's
// lock), so quitting does not cut it short.
func (m *Model) flushSeen() tea.Cmd {
	if m.seen == nil || m.seenFile == "" || (!m.seenDirty && !m.seenSaving) {
		return nil
	}
	return m.writeSeen()
}
