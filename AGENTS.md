# AGENTS.md

Guidelines for agents working on gl-issues.

## Layout

- `main.go` — flags, config loading, subcommands (`list`, `comment`, …,
  run by the `cli` type with injectable I/O), starts the TUI when no
  subcommand is given. Each subcommand is a `command` in the `commands`
  table: usage, argument count and a handler that defines its flags;
  `cli.run` parses and counts the arguments for all of them. Subcommand
  flags are parsed anywhere among the arguments (`parseArgs`).
- `setup.go` — `init` (writes the embedded `config.example.toml`, never
  overwrites) and the config loading shared with `doctor`, which run
  before the config must be valid. `doctor.go` — read-only setup check
  behind the `doctorClient` interface (tests use a fake; `gl.Client` is
  the real one, calls in `internal/gl/setup.go`).
- `internal/config` — reads `~/.config/gl-issues/config.toml` (flat
  `key = value` subset of TOML, no dependency; anything else is an
  error), refuses group/world-readable files. It holds no token; an
  unknown key such as `token` is an error that never shows the value.
  Every setting is one entry in the `settings` table (default,
  validation, help); env `GL_ISSUES_<KEY>` and flags go through
  `Config.Set`, which records the source for `gl-issues config`. Tests in
  the main and config packages sandbox HOME/XDG and clear the
  `GL_ISSUES_` variables in `TestMain`.
- `internal/gl` — GitLab REST calls through `glab api` (`Client.API`;
  stdout and stderr each capped at `MaxOutput`, past which glab is
  killed); issue/note/user types; `ParseRef`. `project.go`: project listing and `RankProjects`.
  `date.go`: `ParseDue` (due-date input).
  `sanitize.go`: `Sanitize`, applied to every string decoded from the
  server (escape sequences, control and Unicode format characters);
  `SanitizeLine` (also newlines and tabs to spaces) for every
  single-line field, `Sanitize` only for description and note bodies.
- `internal/state` — non-secret per-user state (recent projects) in
  `$XDG_STATE_HOME/gl-issues/state.json`, kept apart from the config.
  `seen.go`: the seen file, one per host + group (`seen-<hash>.json`):
  issue ID → last seen `updated_at`, notes count, state, tagged flag.
  Tests set `XDG_STATE_HOME` to a temp dir (`TestMain` in internal/tui).
- `internal/tui` — Bubble Tea model (v1 API), lipgloss styling.
  `tui.go`: model, messages, keys, filters and the GitLab commands
  (bulk ones through `limited`, `actionWorkers` glab processes at a
  time); what a frame or a search keystroke would otherwise compute over
  every issue is kept in `listStats` and one `issueCache` per issue,
  both refreshed by `rebuildViewKeep`.
  `view.go`: list, cards, detail pane, title and footer rendering.
  `text.go`: cell-width helpers (`fit`, `clip`, `shortPath`, …).
  `editor.go`: `$EDITOR` round trip (temp files in a 0700
  `$XDG_CACHE_HOME/gl-issues/`, `editDir`) and the browser opener (the
  only programs besides glab). `input.go`: small single/multi-line
  `lineInput` instead of bubbles/textinput. `popup.go`: pop-up forms
  (comment, assign, due, confirm, project picker, new issue) drawn with
  `overlay`. `people.go`: assignee picker. `refresh.go`: `Options`,
  background refresh timer (generation-counted), change detection and
  merge. `seen.go`: update tags against the seen file (read and written
  only in commands, writes coalesced, flushed on quit), the own-change
  check (`gl.RecentNotes`, one page of 20: every note since the seen
  version must be yours; ≤4 at a time, ≤50 per comparison, fail open)
  and the stale-check generation. `tree.go`: the tree layout (`l`,
  `tree_depth`): `buildRows` turns the filtered, sorted issues into
  `listRow`s (group labels and issues; flat mode is one row per issue),
  folding by key (`g:<path>`, `i:<id>`), and the parent links from
  `gl.WorkItemParents` (`gl/hierarchy.go`, one paginated GraphQL query,
  only in tree mode). The cursor indexes `rows`, not `view`;
  `current()` is nil on a label. `todo.go`: your pending To-Dos (read-only, fetched with each
  load; `gl/todo.go` makes the request), shown in the title, tags and
  details, page by page up to `gl.MaxTodoPages` (`To-Do N+` when there
  are more).
- `README.md` — the short overview (pitch, quick start, install,
  minimal config, docs index). The details live in `docs/`:
  `install.md` (install, release verification), `configuration.md`
  (config file, login and token scopes, settings, background refresh),
  `dashboard.md` (keys, tree layout,
  pop-ups, assignees, new issues, update tags), `scripting.md`
  (subcommands), `troubleshooting.md` and `linux.md`. Keep them in step
  with behaviour changes; new docs files also go in
  `.goreleaser.yaml` `archives.files`.
- `docs/demo/` — the README screencasts (`docs/demo-dark.gif`,
  `docs/demo-light.gif`): `setup.sh`
  builds a sandbox HOME, `fake_glab.py` answers like GitLab from a JSON
  file of made-up issues, `demo.tape` is the VHS script. `make demo`
  records it again; do that when the dashboard's look changes.

## Conventions

- Go, `gofmt`; keep deps minimal (the charmbracelet stack and termenv only).
- All GitLab access goes through `gl.Client.API`, which shells out to
  `glab api` non-interactively. Never let glab prompt: the TUI owns the
  terminal.
- Secrets: gl-issues holds no token; glab uses its own stored login (OS
  keyring by default). Inherited token, host-override and debug variables
  are dropped from glab's environment (`glabEnv`) and no token is ever
  added. doctor reads only the storage and protocol from
  `glab auth status` (`gl.AuthStatus`), never prints its output. Never commit `config.toml`, `.env*` or
  tokens; only `config.example.toml` (no secrets) is tracked.
- Writes to GitLab happen only on an explicit keypress or subcommand.
  In the TUI, `x` (close/reopen) always asks first; bulk `i` (assign me)
  asks first; bulk `a` and `d` open a pop-up whose save is the
  confirmation. Single-issue `i` acts at once, unless the cursor landed
  on the issue by itself (its row left the list in a reload:
  `cursorMoved`, see `relayout`) or the selected issue is hidden under a
  fold; then it asks too.
- Pop-ups hold copies of their target issues (`form.targets`), never
  indices into `m.issues`; a list loaded while a pop-up is open is held in
  `pendingLoad` and installed when it closes, and a pop-up whose request
  failed behind another one waits in `failedForms` and comes back then.
- Use API fields by ID (`assignee_ids`), resolving usernames via group
  members first, then `users?username=`. "me" means the logged-in user.
- Must run on macOS and Linux (no Windows). Keep OS-specific code
  (browser opener, editor fallback) in small helpers; `make vet`
  also runs `go vet` for Linux; check builds with `make snapshot`. Linux notes:
  `docs/linux.md`.

- Action shortcuts are lower-case letters only (no Shift; `shift+tab`
  moves between pop-up fields), and every one must appear in
  the footer key list (`listGeneral`/`treeKeys`/`issueActions`/`bulkActions`/
  `cursorActions`/`detailNav`) and in `helpText`.
- Anything that asks for input opens a pop-up `form`; only `/` search
  stays on the bottom line so the list filters live.

- New settings: add an entry to `settings` in internal/config, a field to
  `tui.Options` if the TUI needs it (no package globals), and document it
  in `config.example.toml` and the settings table in
  `docs/configuration.md` ("All settings"). Never add a token setting.
- Fixtures and docs use fictional names only: `gitlab.example.com` in
  docs, `gitlab.example.edu` in test fixtures, `acme/…` (`acme/platform`
  in examples), alice, bob, carol. The one exception: the README badges
  link the project's real GitHub Actions runs and releases.

## CI, releases, tooling

- GitHub Actions (`.github/workflows/`) runs only `make` targets: add a
  check to the Makefile first, then call it from the workflow. Every
  action is pinned to a full commit SHA with the version as a trailing
  comment; Dependabot (`.github/dependabot.yml`) updates the pins.
- Tools (golangci-lint, govulncheck, gitleaks, goreleaser, …) are pinned
  in the Makefile and run via `go run`; bump versions there only.
  `.golangci.yml` configures lint; keep `make lint` at 0 issues.
- Releases: push a `v*` tag; GoReleaser (`.goreleaser.yaml`) builds
  Linux/macOS × amd64/arm64, deb/rpm, SBOMs, and signs checksums with
  cosign (keyless). Validate config changes with `make release-check
  snapshot`. Update CHANGELOG.md before tagging.
- `go.mod` pins a patched `toolchain`; keep govulncheck clean by bumping
  it (and `golang.org/x/*`) when advisories appear.
- Dependabot opens update pull requests; CI must
  pass before merging them.

## Commits and publishing

- Commit only as the GitHub noreply address
  (`<id>+<user>@users.noreply.github.com`), set as `user.email` in this
  repository; never a work, institutional or personal address.
- Run `make publish-check` before every commit and every push, and do
  not commit or push while it fails. It checks `user.email` and every
  commit not yet on a remote for the noreply address, runs gitleaks, and
  greps the files and unpushed commits (messages and diffs) for the patterns in the untracked
  `.git/info/private-patterns` (one extended regexp per line).
- Nothing may reveal the author's employer, institution, colleagues,
  private servers, hostnames, projects, paths or issue numbers: not in
  code, tests, fixtures, docs, file names, images or commit messages. Use
  the placeholders above. Keep the patterns themselves out of the
  repository: naming them would leak them.

## Checks

```sh
make ci        # fmt-check vet lint actionlint release-check race vuln secrets build
```

Add tests for parsing and pure logic (refs, dates, assign specs, config,
CLI arguments, filters/sort). Tests that need GitLab use a fake `glab`
script first in `PATH` and never touch the network. `TestViewSmoke`
renders the TUI with fake data at several widths; the live GitLab
behaviour is verified manually.
