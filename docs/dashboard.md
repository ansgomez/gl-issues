# Using the dashboard

This page covers the dashboard: its rows, keys, tree layout, pop-up
windows, the assignee picker, new issues and update tags. Back to the
[README](../README.md).

## Start

```sh
gl-issues                       # group from config
gl-issues -g acme/platform      # another group for this run
```

At start the issues are grouped by subgroup, every label folded with its
counts (see [Tree layout](#tree-layout); a group with a single label
starts with it unfolded):

```
gl-issues  acme @ gitlab.example.com  64 opened · 9 mine · 21 unassigned · 3 overdue · 4 due ≤7d  sort:due

▌ ▹ acme (own projects)
▌   3 open

  ▹ people
    12 open · 1 due soon

  ▹ platform
    40 open · 3 overdue · 2 due soon

  ▹ tools
    9 open · 1 due soon
```

`f` or `enter` unfolds a label, `b` unfolds everything, `l` cycles the
layout: two group levels, then the flat list, then one level again. Each issue takes two lines (example with made-up issues):

```

▌  Calibrate the rain gauge                                         2026-09-28 · 5d overdue
▌  #14  weather-station  platform/weather · @alice · 4 comments · updated 2d ago · hardware

   Update the onboarding checklist                                  2026-10-07 · in 4d
   #3  notes  people/bob · unassigned · updated 5h ago
```

- **Line 1** is the title and the due date. The due date is red when
  overdue, yellow when due within `due_soon_days` (7 by default), and
  green otherwise.
- **Line 2** is the issue number, the **project name** (highlighted), its
  parent path, assignees (`unassigned` in yellow), comment count, last
  update and labels. The group prefix is dropped, and deep paths are
  shortened in the middle: `platform/weather/sensors/logger` becomes
  `platform/…/logger`.
- `▌` marks the issue under the cursor. The line above the footer
  previews its author, labels and description.
- Press `z` for the compact view with one line per issue and columns,
  which fits more issues on screen.
- **To-Dos**: with each reload gl-issues also fetches your pending GitLab
  To-Dos on issues of the group (read-only requests of 100 each, at most
  10 pages and one more To-Do past them; `read_api` is enough, and
  nothing is ever marked done). The title shows `To-Do 3` (highlighted)
  while you have any, `To-Do 1000+` when
  there are more than were fetched, or `To-Do ?` if they could not be
  fetched; the list never waits for them. A listed issue you have a To-Do
  on is tagged, e.g. `todo: mentioned` or `todo: assigned`, and its
  details show the reasons. Turn it off with `todos = false`.
- Text size is set by your terminal, not by gl-issues: `⌘ +` / `⌘ −` in
  Terminal and iTerm2, `Ctrl +` / `Ctrl −` in most Linux terminals.

## Keys

Issues are sorted by due date by default (soonest first, no date last).
List and issue shortcuts are lower-case and none needs `Shift`. The
footer always lists them in groups. **List** holds general controls:
moving, searching, filters, sorting and reload. **Issue** holds actions
on the issue under the cursor. When issues are selected, a **Selected**
group lists the actions that apply to all of them (`a`, `i`, `d`, `x`),
and **Issue** keeps the ones that still act on the cursor only (`c`,
`e`, `w`). Inside an issue, its actions come first and navigation
(**View**) goes below. `h` or `?` shows the full help.

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k` | Move |
| `pgup`/`pgdn`, `ctrl+u`/`ctrl+d` | Page up / down |
| `g` or `home` / `end` | First / last issue |
| `enter`, `tab` or `→` | Open details: description and all comments |
| `n` | **New issue**: pick the project, then fill in title, assignees, due date and description (see below) |
| `c` | Comment: a pop-up with a multi-line text box |
| `a` | Assign people (see below). With several issues selected, the people you pick are added to each |
| `i` | Assign me ("I'll take it"). It asks first with several issues selected, when a selected issue is hidden in a folded group, or when a refresh moved the cursor to this issue |
| `d` | Due date (formats below). The pop-up shows the resulting date while you type |
| `e` | Edit the description in `$EDITOR`. If someone changed it on GitLab meanwhile, saving fails, or the editor exits with an error, nothing is overwritten and your text stays in the temp file whose path is shown (in the private `~/.cache/gl-issues/`, or `$XDG_CACHE_HOME/gl-issues/`). The check is a read just before the save, so an edit made on GitLab in that instant is still overwritten. A description holding terminal control or bidi characters (an escape sequence, a right-to-left override) is not opened: edit it on GitLab (`w`) |
| `x` | Close or reopen (asks first). With a mixed selection it closes the open issues and leaves the closed ones alone. A closed issue stays in the open list (and a reopened one in the closed list) until the next reload, so you can undo at once; the title counts only the issues in the list's state |
| `w` | Open in the browser (only http(s) links on your GitLab host) |
| `space` / `v` | Select an issue / select all visible issues (in the tree, not those in folded groups; a second `v` clears the selection). `a`, `i`, `d` and `x` then act on every selected issue, and the selection is cleared once the action runs (cancelling a pop-up keeps it). Selected issues a filter or search hides are not counted or acted on until it is lifted. Issues with another action still running are skipped, and the status line says how many |
| `m` / `u` / `o` | Filter: assigned to me / unassigned / overdue or due within `due_soon_days` |
| `/` | Search title, project, assignee and labels |
| `s` | Sort by due date, last update or project |
| `t` | Show open, closed or all issues |
| `z` | Two-line or compact (one line per issue) view |
| `l` | Layout: flat list → tree of 1 group level → 2 levels → flat (see [Tree layout](#tree-layout)) |
| `f` | In the tree: fold or unfold the group label or parent issue under the cursor; on another issue, the label or parent it is under. `enter` on a group label does the same |
| `b` | In the tree: unfold every label and parent, or, when nothing is folded, fold them all |
| `r` | Reload from GitLab |
| `h` or `?` | Help (any key closes it) |
| `esc` | Clear filters and selection |
| `q` | Quit (asks first while changes are still being sent) |

### In the details

Inside an issue's details:

| Key | Action |
| --- | --- |
| `↑`/`↓`, `j`/`k`, `pgup`/`pgdn`, `ctrl+u`/`ctrl+d`, `g`/`home`/`end`, `space` | Scroll |
| `←`/`→` or `[`/`]` | Previous / next issue |
| `esc`, `q`, `enter` or `backspace` | Back to the list |
| `n`, `c`, `a`, `i`, `d`, `e`, `x`, `w` | As in the list, on this issue |

## Due dates

**Due dates** (pop-up, `due` and `new -due`): `2026-11-01`, `today`,
`tomorrow`, a weekday (`fri`, `friday`: the next one after today), `+3`
or `+3d` days, `+2w` weeks, `+1m` months, or `none` to clear.

## Tree layout

By default (`tree_depth = 1`) the issues show as a tree of the group's
subgroups, with every label and parent folded at start; `f` unfolds one,
`b` all. `l` cycles flat → 1 → 2 levels (also `GL_ISSUES_TREE_DEPTH`,
`-tree 0|1|2`; `0` is the flat list). Unfolded:

```
  ▾ acme/platform (own projects)  1 open · 1 overdue
       api#1                    Rotate the deploy keys
  ▾ web  4 open · 1 due soon
       ▾ web/site#2             Redesign the landing page
           ▾ web/frontend/app#6   Split the header component
               web/frontend/app#7   Update the snapshots
    ▾ web/frontend  1 open
           web/frontend/app#3   Drop the old polyfills
  ▹ mobile  2 open
```

- **Levels.** Depth 1 has one label per direct subgroup of `group`;
  depth 2 adds their subgroups, indented. Issues of deeper subgroups go
  under their ancestor at the chosen depth. Issues of the projects
  directly in a group come first, before its subgroups; those of the
  group itself under "*group* (own projects)". Labels show the path
  below `group` and counts for everything under them (open, closed,
  overdue, due soon). Projects are not a level: each row keeps its
  project. Labels come from the listed issues, so a subgroup without
  listed issues has none.
- **Child items.** Tasks and child issues are nested under their parent
  issue, with their own state, assignees and due date, also when they are
  in another project or subgroup. A child whose parent is not listed
  (closed, filtered out, outside the group) stays in its own group.
  Tasks are issues to the REST API, so without the tree they are listed
  like any other issue; in the tree they show only once, nested.
- **Folding.** Everything starts folded: only the top labels with their
  counts (`▹` folded, `▾` open). If there is only one top label (a group
  without subgroups, or with one), it starts unfolded. While a search or
  filter is on, matches show unfolded and `f` and `b` are off (`esc`
  clears it). The cursor also stops on labels.
  `f` (or `enter` on a label) folds and unfolds; `b` unfolds everything,
  or folds everything when nothing is folded. Selected issues folded out
  of sight stay selected; pop-ups say how many are hidden. A folded label keeps its
  counts and a folded parent says how many child items it hides. Folds last for the session,
  across reloads. Issue keys (`c`, `a`, `i`, `d`, `e`, `x`, `w`, `space`)
  on a label only say "not an issue"; in the details `←`/`→` skip
  labels.
- Sorting, search and filters work inside each group: matching issues
  are shown under their labels.
- **Requests.** Only in the tree: one paginated GraphQL query per load
  (`group.workItems` with `includeDescendants` and the hierarchy widget,
  one request per page of 100 work items, at most 50), `read_api` is
  enough. If the server cannot answer it (an older GitLab, or an error),
  the tree works without child items and says so once; background
  refreshes then skip it for 15 minutes, twice as long after each further
  failure (at most 4 hours); `r` tries again at once.

## Pop-up windows

Comments, assignments, due dates, closing and new issues open a small
window over the list.

| Key | In a pop-up |
| --- | --- |
| `tab` / `shift+tab`, `↑`/`↓` | Next / previous field |
| `enter` | Save (one-line fields); new line in a text box |
| `ctrl+s` | Send the comment / create the issue |
| `ctrl+e` | Edit the text box in `$EDITOR` |
| `esc` | Cancel |

A comment or new issue stays in its pop-up until GitLab has it. If
sending fails, the pop-up shows why and keeps your text, so you can try
again. `esc` while it is sending hides the pop-up; it comes back if the
request fails.

## Picking assignees

Used by `a` and by the new-issue form:

```
Assignees   ▎ @alice car
            ▸ carol  Carol Example
              ↑↓ choose · enter add
```

- The people already assigned are shown as `@name` tags.
- Type part of a username or of a first or last name. Matching group
  members appear below with one highlighted (`▸`). You appear as
  "(you)" and also match `me`.
- `↑`/`↓` changes the highlighted person. `enter` or `tab` adds them.
  gl-issues never assigns the text you typed, only a person from the list.
- `backspace` in an empty box removes the last tag.
- `enter` in an empty box saves. In the new-issue form it moves to the
  next field instead.
- No tags means nobody is assigned. The assignees are saved as shown in
  the form: someone assigned on the server while it was open is replaced
  by them (the next refresh shows the result).

## Creating an issue

Press `n`. First choose the project. Type a few letters of its name or
path (`weather`, `bob notes`), move with `↑`/`↓` and press `enter`. Before
you type, the list starts with the project of the issue under the cursor,
then projects you used recently, then projects with the most open issues.

Then fill in the form. Only the title is required. Press `ctrl+s` to
create the issue; the cursor then moves to it in the list.

The project list of the group is fetched in the background at startup.
Recently used projects are remembered in
`~/.local/state/gl-issues/state.json` (or `$XDG_STATE_HOME/gl-issues/`;
set `XDG_STATE_HOME` to an absolute path to move all state, as the tests
do). That file holds no secrets.

The editor is `$VISUAL`, then `$EDITOR`, then `vi` (or `nano`). For an editor that
returns immediately, add its wait flag, e.g. `EDITOR="code -w"`.


## Update tags

With `notify` on (the default), gl-issues remembers what you last saw of
each issue, so tags survive restarts. A tagged issue stands out across
its row: title, number and update time in bold yellow (`updated`) or
bold green (`new`), until you open it.

- **Seen file.** Per issue: its `updated_at` and comment count when you
  last saw it. One private file (mode 0600) per host and group, in
  `~/.local/state/gl-issues/seen-<hash>.json` (or
  `$XDG_STATE_HOME/gl-issues/`), written atomically in the background.
  Issues that leave the list (closed, moved out, deleted) are dropped from
  it. It holds no secrets.
- **First run** (no file yet): nothing is tagged; the current list becomes
  the baseline.
- **Restart and reloads:** an issue whose `updated_at` or comment count
  differs from the file is tagged `updated`; one with no entry is tagged
  `new`. A note says what changed, e.g. "since last time: 1 new issue ·
  3 updated". Only GitLab's own timestamps are compared, never your
  clock. Switching between open, closed and all compares only issues seen
  before in a list of that state, so nothing is tagged `new` just because
  you switched.
- **Seen:** opening an issue's details marks it seen and clears its tag.
  So does any change you make with gl-issues (comment, assign, due date,
  description, close/reopen, new issue).
- **Your own changes elsewhere** (web UI, other tools) are not tagged:
  for each changed issue, gl-issues fetches its 20 most recently updated
  notes, system notes included (GitLab records label, due date,
  description and assignee changes as system notes by whoever made them).
  If every note updated since the version you saw is yours, and the
  newest is no more than a few seconds older than the issue's last
  change, the issue is marked seen silently. If any of them is someone
  else's, none is newer than the change, all 20 are newer than what you
  saw (there may be more), or the request fails, the issue is tagged.
  A change GitLab records without a note (for example a title edit) is
  not visible to this check: if you change the issue afterwards, it is
  marked seen with yours.
  An issue stays tagged until you open it, even if you change it later.
  A new issue you opened yourself elsewhere, untouched since, is not
  tagged either.
- **Requests:** one `GET user` per session if your user is not yet
  known, and one note request per changed issue, at most 4 at a time and
  at most 50 per reload (the 50 most recently updated; the rest are tagged
  without checking). `read_api` is enough. Tags appear together once the
  checks finish.
- **Corrupt or unreadable file:** a quiet note, and a fresh baseline.
- `notify = false`: no tags, no notes, no seen file and no note requests.
