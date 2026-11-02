# Troubleshooting

What to check when gl-issues does not start, cannot log in or shows an
error. Back to the [README](../README.md). Linux-specific problems are in
[linux.md](linux.md#troubleshooting).

## Run doctor first

Run `gl-issues doctor` first. It checks, read-only and one line each
(`✓` ok, `!` warning, `✗` problem, each with a fix), the config file,
host and group, glab and its version, the login (user name), where glab
keeps the token (OS keyring, or a warning for plain text in glab's config
file) and whether API calls use https, that the group exists and is
visible to you, and the token's scopes. Only those facts are taken from
`glab auth status`; no token text is ever printed. It also warns about a
`token` left in the config file, `GL_ISSUES_TOKEN`/`GITISSUE_TOKEN` still
set, and `GITISSUE_…` variables and directories left over from the old
name. It
exits 1 if anything is `✗`. Then the commands that print plain errors
instead of a status line:

```sh
gl-issues doctor                                 # the whole setup, with fixes
gl-issues config                                 # host, group and the rest, from where?
glab auth status --hostname gitlab.example.com   # glab logged in to that host?
gl-issues list                                   # the full error of the first request
```

## Common errors

- **`no GitLab host set` / `no group set`.** Set `host` and `group` in the
  config file (`gl-issues config` shows which file is read), or pass
  `-host` and `-g`. No config file yet? `gl-issues init` creates one.
- **`host = "…": want a bare host name`.** Remove the path, user name or
  query: `gitlab.example.com` or `gitlab.example.com:8443`, not
  `https://gitlab.example.com/acme`.
- **`401 Unauthorized`, `HTTP 401` or `Unauthenticated.`** (doctor:
  `not logged in`). glab has no valid login for exactly this host. Run
  `glab auth status --hostname <host>`; if it is not logged in, run
  `glab auth login --hostname <host>` (for a host with a port, see
  [Log in](configuration.md#log-in-and-token-scopes)). Logged in to `gitlab.com` or
  another host does not count: the host string must match `host`. If
  your config file still has a `token` line from an older version,
  `gl-issues migrate-token` logs glab in with it.
- **`the token needs the api scope` (`insufficient_scope`).** Viewing works
  but a comment, assignment, due date, description, close/reopen or new
  issue fails: the token has only `read_api` (or `read_user`, …). Create a
  legacy personal access token with the `api` scope (see
  [Log in](configuration.md#log-in-and-token-scopes)) and log glab in again with it
  (`glab auth login --hostname <host>`).
- **`group "…" not found, or you have no access` (or
  `404 Project Not Found`).** The path is
  wrong, or you have no access: GitLab answers 404, not 403, for groups
  and projects you cannot see. Use the full path as in the group's URL
  (`acme/platform`, not its display name), and check that the account
  glab is logged in as can open it in the browser.
- **`403 Forbidden` on a write.** The token is fine, but your role in that
  project does not allow the change.
- **`no such host`, `connection refused`, `i/o timeout`, or `timed out`.**
  gl-issues cannot reach the host. Check the spelling of `host`, and
  whether your GitLab is only reachable from an internal network or over
  a VPN, or a firewall or proxy blocks HTTPS to it; connect first and try
  `glab auth status --hostname <host>` again. Each group of requests gives
  up after 2 minutes.
- **`glab not found in PATH`.** Install glab, or add its directory to
  `PATH`.
- **Config file is "readable by other users".** Run
  `chmod 600 ~/.config/gl-issues/config.toml`. Until the file loads,
  doctor does not check host, group or the login.
- **`-g goes before the command`.** Global flags (`-g`, `-host`,
  `-config`, …) come before the subcommand: `gl-issues -g acme/other
  list`, not `gl-issues list -g acme/other`.
- **The dashboard is empty.** The group has no issues in the current
  list: press `t` to show closed or all issues, `esc` to clear filters,
  or check `filter` and `state` in `gl-issues config`.
- **`moved your settings from … (gitissue was renamed)`,
  `ignoring …: using …`, `GITISSUE_… is deprecated: use GL_ISSUES_…`.**
  Left over from the old name; see
  [Upgrading from gitissue](configuration.md#upgrading-from-gitissue).
- **Slow start on big groups.** All matching issues are fetched (100 per
  request). Use a narrower group with `-g`.
- **`response too large (over 128 MB; …)`.** glab wrote more than
  gl-issues reads from one call (far beyond any real page or issue list);
  the call is stopped instead of buffering it. Check the host, or use a
  narrower group with `-g`.
