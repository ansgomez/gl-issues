# Changelog

All notable changes to this project are documented here. The format
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the
project uses [Semantic Versioning](https://semver.org/).

## [0.1.0] - 2026-11-01

First public release.

- Terminal dashboard for the open, overdue, due-soon and unassigned
  issues of a GitLab group and its subgroups: two-line or compact rows,
  filters (`m` mine, `u` unassigned, `o` due soon), search and sorting;
  details with description and comments.
- Tree layout (`l`, `tree_depth`): issues under one label per subgroup,
  one or two levels deep, with tasks and child issues nested under their
  parent; `f` and `b` fold and unfold.
- Act on an issue, or on a selection: comment, assign (member picker,
  "assign me"), due dates (`tomorrow`, `fri`, `+2w`, `none`), edit the
  description in `$EDITOR` with conflict detection, close and reopen;
  new issues with a project picker. Bulk actions run at most 4 glab
  processes at a time, and a request that fails keeps its text.
- Quiet background refresh that tags new and updated issues; the tags
  survive restarts through a private seen file per host and group (your
  own changes alone are not tagged). `notify = false` turns it off.
- Your pending GitLab To-Dos on the group's issues: a count in the title,
  a tag on the rows and the reasons in the details (`todos = false`).
- The same actions as subcommands for scripts: `list`, `show`,
  `comment`, `assign`, `due`, `describe`, `close`, `reopen`, `new`, and
  `config` to show every setting and its source.
- Configuration in `~/.config/gl-issues/config.toml`, environment
  variables (`GL_ISSUES_<SETTING>`) and flags; `gl-issues init` writes
  the commented template; `theme` for terminals that report the wrong
  background.
- All GitLab access through `glab api` with glab's own login (kept in
  the OS keyring by glab 1.123+); gl-issues never holds a token.
  `gl-issues doctor` checks the setup read-only (config, host, glab and
  its version, where the token is kept, group access, token scopes) and
  `gl-issues migrate-token` hands a token left in the config file to
  glab's login, never printing it.
- Text from the server is sanitized before it is shown; every glab
  call's output is capped; glab runs without telemetry or prompts.
- Release builds for Linux and macOS (amd64, arm64), `.deb` and `.rpm`
  packages, SBOMs and keyless-signed checksums.

[0.1.0]: https://github.com/ansgomez/gl-issues/releases/tag/v0.1.0
