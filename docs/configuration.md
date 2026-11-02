# Configuring gl-issues

This page covers the config file, logging glab in, every setting and
the background refresh. Back to the
[README](../README.md).

gl-issues needs two settings, `host` and `group`, and glab logged in to
that host. gl-issues holds no token itself: glab keeps it, in the OS
keyring by default from glab 1.123 on (`gl-issues doctor` says where).

## Config file

```sh
gl-issues init       # creates ~/.config/gl-issues/config.toml (mode 600), never overwrites
${EDITOR:-nano} ~/.config/gl-issues/config.toml
gl-issues doctor     # checks it
```

`gl-issues init` writes the commented template (`config.example.toml`,
built into the binary) to the file gl-issues reads: `-config` if given,
else `$GL_ISSUES_CONFIG`, else the default path. It creates the directory
with mode 700 and refuses to replace an existing file. From a clone,
`make config` does the same. The template is also in the release archive
and, for the packages, in `/usr/share/doc/gl-issues/config.example.toml`.
It prints the file it created. The template ships with `host` and
`group` empty (examples in its comments), so `gl-issues doctor` reports
them until you set them.

Edit the config file with gl-issues closed. The dashboard reads it once,
at start, so changes apply the next time it starts.

The file is `$XDG_CONFIG_HOME/gl-issues/config.toml` if `XDG_CONFIG_HOME`
is set to an absolute path (on macOS too), else
`~/.config/gl-issues/config.toml`; `gl-issues config` prints the path it
uses. Without a home directory gl-issues needs `-config` or
`$GL_ISSUES_CONFIG`; it never reads a config from the current directory. Set at least:

```toml
host  = "gitlab.example.com"
group = "acme/platform"
```

- **`host`** is a bare host name, optionally with a port:
  `gitlab.example.com` or `gitlab.example.com:8443` (port 1-65535). A
  leading `https://` and a trailing `/` are dropped; `http://`, a path
  (`gitlab.example.com/gitlab`), user name, query or spaces are rejected,
  so a GitLab served below a path is not supported.
- **`group`** is the full path of a group, as in its URL:
  `acme` or `acme/platform`. Its subgroups are included. A user
  namespace (`alice`) is not a group. The group's URL pasted from the
  browser (`https://gitlab.example.com/acme/platform`) works too, if its
  host is `host`; spaces and `:` are rejected.

## Log in and token scopes

gl-issues never asks for a password and holds no token: every request
is `glab api --hostname <host>` (for a host with a port only
`GITLAB_HOST=<host>`, since glab refuses a port in `--hostname`),
authenticated by glab's own login for that host:

```sh
glab auth login --hostname gitlab.example.com
glab auth status --hostname gitlab.example.com
```

Use exactly the same host string as in `host`, port included:
`glab auth login --hostname gitlab.example.com:8443` (not the bare name
with `--api-host`: glab would store that login under the bare name,
which gl-issues does not look up). `gl-issues doctor` prints the command
for your `host`. On a self-managed GitLab, the simplest choice in glab's
prompt is to paste a personal access token;
`glab auth login --hostname <host> --stdin < token.txt` does the same
without prompts (delete `token.txt` afterwards).

glab (1.123 and later) stores the token in the OS keyring (Secret Service
on Linux, the Keychain on macOS) unless it is told `--insecure-storage`,
or no keyring is available; then it is kept in plain text in glab's
config file. `gl-issues doctor` says which one glab uses for your host.

The token is a personal access token, created at
`https://<host>/-/user_settings/personal_access_tokens`. If your GitLab
offers a choice between fine-grained and legacy tokens, pick the legacy
(scope-based) kind. The scope decides what works:

| Scope | Works | Fails with `insufficient_scope` |
| --- | --- | --- |
| `read_api` | Dashboard, `list`, `show`: viewing issues, details and comments | Every write (below) |
| `api` | Everything | |

glab keeps one login per host, so gl-issues and its sibling gl-repos
share the same token. For both tools, tick only `api` in GitLab's
*Select scopes* list; it includes everything `read_api` and `read_user`
allow. The full list, scope by scope:

| Scope | gl-issues | gl-repos | Tick it? |
| --- | --- | --- | --- |
| `api` | every change (below) | create projects and subgroups (`n`, `u`) | **Yes** |
| `read_api` | viewing only | browsing and search only | Only for a read-only token instead of `api` |
| `read_user` | not used (`api`/`read_api` cover `glab auth status`) | not used | No |
| `read_repository`, `write_repository` | not used | not used: git clones over ssh | Only if git uses this token over https (glab as git's credential helper): `write_repository` to push |
| `self_rotate` | not used | not used | Optional: lets the token rotate itself |
| `read_registry`, `write_registry`, `read_virtual_registry`, `write_virtual_registry`, `ai_features`, `create_runner`, `manage_runner`, `k8s_proxy` | not used | not used | No |
| `sudo`, `admin_mode` (administrators only) | not used | not used | No |

The writes are: comment (`c`, `comment`), assign and assign me (`a`, `i`,
`assign`), due date (`d`, `due`), edit the description (`e`,
`describe`), close/reopen (`x`, `close`, `reopen`) and new issues (`n`,
`new`). gl-issues does not change labels, titles or milestones. Your
role in the project must also allow the change; otherwise GitLab answers
`403 Forbidden`.

## Keeping the token safe

- The config holds no secrets and lives in `~/.config/gl-issues/`,
  outside any repository.
- `config.toml`, `*.local.toml`, `.env*` and `*.token` are in `.gitignore`,
  so a copy inside this folder is not committed either. Only
  `config.example.toml`, which holds no secrets, is tracked.
- gl-issues refuses to start if the config file can be read by other users
  (`chmod 600` fixes it).
- gl-issues never stores or passes a token: glab uses its own login,
  kept in the OS keyring by glab 1.123+. `gl-issues doctor` warns when
  glab keeps it in plain text in its config file (fix: with glab 1.123+
  and an unlocked keyring, log in again,
  `glab auth login --hostname <host>`) and when glab makes API calls over
  `http` instead of `https`.
- gl-issues removes `GITLAB_TOKEN`, `GITLAB_ACCESS_TOKEN`, `OAUTH_TOKEN`
  and `CI_JOB_TOKEN` from glab's environment, so glab uses only its stored
  login, and a token meant for another host is never sent. The editor and
  the browser opener, the only other programs it runs, do not get them
  either.
- Inherited host overrides (`GITLAB_HOST`, `GITLAB_URI`, `GITLAB_URL`,
  `GL_HOST`, `GITLAB_API_HOST`, `GITLAB_API_PROTOCOL`),
  `GLAB_ENABLE_CI_AUTOLOGIN` and debug switches (`GLAB_DEBUG*`, `DEBUG`)
  never reach glab either, so it only talks to the configured host, over
  https, and never prints debug output (which could contain the token)
  into gl-issues's error messages.
- Text from GitLab (titles, descriptions, comments, names, errors) is
  cleaned before it is shown: terminal escape sequences, control
  characters and invisible Unicode formatting characters (such as
  right-to-left overrides) are removed. The zero-width joiners that
  emoji sequences and some scripts need, and the soft hyphen, are kept.

## All settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `host` | | GitLab host name, optionally `:port`; no `https://`, no path |
| `group` | | Full group path (`acme/platform`) whose issues are shown, subgroups included |
| `refresh` | `5m` | Reload the issue list in the background this often: `30s`, `2m`, `1h` … (minimum 30s), or `off` |
| `refresh_lists` | `1h` | Reload group members and projects this often, or `off` |
| `notify` | `true` | Tag issues `new`/`updated` since you last saw them, across restarts, ignoring your own changes (see [Update tags](dashboard.md#update-tags)); `false` = no tags, no seen file, no extra requests |
| `todos` | `true` | Show your pending GitLab To-Dos on issues of the group (count in the title, `todo` tags); `false` = no request |
| `sort` | `due` | Starting sort: `due`, `updated`, `project` |
| `state` | `opened` | Starting list: `opened`, `closed`, `all` |
| `view` | `cards` | `cards` (two lines per issue) or `compact` (one line) |
| `tree_depth` | `1` | Starting layout: `0` flat list; `1` or `2` = [tree](dashboard.md#tree-layout) of that many subgroup levels, with child items nested |
| `filter` | | Starting filter: `mine`, `unassigned`, `due-soon` |
| `due_soon_days` | `7` | Issues due within this many days count as due soon |
| `editor` | | Editor command; empty = `$VISUAL`, `$EDITOR`, `vi`, `nano` |
| `browser` | | Command that opens issue URLs; empty = `open` on macOS; `$BROWSER`, `xdg-open`, `wslview`, `sensible-browser` on Linux |
| `theme` | `auto` | Terminal background for the cursor-row band: `auto` (ask the terminal), `light` or `dark`; set it when a multiplexer such as Zellij reports the wrong colour |

`config.example.toml` has every setting with comments. The file is a
small subset of TOML: one `key = value` per line, where the value is a
`"string"`, a `'literal string'`, or a bare `true`/`false`/number, plus `#`
comments. A misspelt setting, an invalid value, a setting given twice,
a `[section]` or text after a value stops gl-issues with a message that
names the problem and its line, so typos are never silently ignored. The
message never quotes the config text, so a mistyped secret is never
shown.

**Where values come from**, later ones winning:

1. the defaults above;
2. the config file: `-config <file>`, else `$GL_ISSUES_CONFIG`, else
   `$XDG_CONFIG_HOME/gl-issues/config.toml` or
   `~/.config/gl-issues/config.toml` (a missing default file is fine, a
   missing `-config` or `$GL_ISSUES_CONFIG` file is an error);
3. environment variables `GL_ISSUES_<SETTING>`, e.g. `GL_ISSUES_REFRESH=2m`
   or `GL_ISSUES_VIEW=compact` (an empty one is ignored);
4. flags: `-host`, `-g` (or `-group`), `-refresh`, `-view`, `-tree`.

**Flags** (before any subcommand: `gl-issues -g acme/other list`; after
it, gl-issues says to move them):

| Flag | Meaning |
| --- | --- |
| `-config <file>` | Config file to read instead of the default |
| `-g <path>`, `-group <path>` | Group for this run |
| `-host <host>` | GitLab host for this run |
| `-refresh <interval>` | Background refresh interval (`30s`, `5m`, `1h`) or `off` |
| `-view cards\|compact` | Starting view |
| `-tree 0\|1\|2` | Starting layout: flat, or a tree of 1 or 2 subgroup levels |
| `-version` | Print the version and exit |
| `-h` | Help; `gl-issues <command> -h` shows a subcommand's flags |

`gl-issues config` prints every setting, its value and where it came
from (`file` for the config file named above the table, `env …`,
`flag …` or `default`). A value that does not validate names its
source: the file, `GL_ISSUES_…` or `flag -…`.

## Background refresh

The issue list reloads every `refresh` interval, 5 minutes by default.
The refresh is quiet:

- Few requests per reload: the issue list (one request per page of 100
  issues), your pending To-Dos with `todos` on, and in the tree layout
  one GraphQL request per 100 work items for child items (at most 50);
  with `notify` on, also one note request per changed issue (see [Update
  tags](dashboard.md#update-tags)). Members and projects reload only every `refresh_lists`.
- Your cursor, filters, search and selection stay as they are. If the
  issue under the cursor left the list (closed or moved elsewhere), a note
  says so and where the cursor went; open details close rather than show
  another issue, and the next `i` asks before assigning you to the issue
  now under the cursor (moving the cursor ends that). An issue that new
  child-item links fold away is unfolded.
- It never interrupts you. While a pop-up or the search line is open it
  waits and tries again 15 seconds later. A list that arrives while a
  pop-up is open (e.g. after `r`) is shown once the pop-up closes, so the
  pop-up always acts on the issues it was opened for.
- With `notify` on, a short note says what changed, e.g. "refreshed:
  2 new issues · 1 updated", and those issues carry a `new` or `updated`
  tag until you open them (see [Update tags](dashboard.md#update-tags)).
- The title bar shows how fresh the list is ("updated 3m ago"), or
  "refresh failed" while GitLab is unreachable (the last list stays on
  screen).
- `r` reloads at once and restarts the timer.
