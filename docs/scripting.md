# Scripting with gl-issues

Use gl-issues from scripts and the shell. Back to the
[README](../README.md).

## Subcommands

The issue actions are also subcommands (opening the browser, `w`, is
not). `<ref>` is `group/project#12` (the ISSUE column of `gl-issues
list`; the dashboard's compact view drops the group prefix) or an issue
URL. A subcommand's flags may come before or
after its arguments, `--` ends them, and `gl-issues <command> -h` lists
them.

```sh
gl-issues init                         # create the config file from the template
gl-issues doctor                       # check config, glab, login, token storage, group, token scopes
gl-issues migrate-token                # hand a token left in the config file to glab's login
gl-issues list                         # open issues in the group, as a table
gl-issues list -mine -overdue
gl-issues list -state closed
gl-issues show     acme/tools/ground-tools#4
gl-issues comment  acme/tools/ground-tools#4 "Any update on this?"
gl-issues comment  acme/tools/ground-tools#4 - < notes.md
gl-issues assign   acme/tools/ground-tools#4 alice,+bob
gl-issues assign   acme/tools/ground-tools#4 +me          # "me" is you; -me removes you
gl-issues assign   acme/tools/ground-tools#4 none         # remove every assignee
gl-issues due      acme/tools/ground-tools#4 +2w
gl-issues describe acme/tools/ground-tools#4 - < description.md
gl-issues describe acme/tools/ground-tools#4 -clear    # an empty text alone is refused
gl-issues close    acme/tools/ground-tools#4
gl-issues reopen   acme/tools/ground-tools#4
gl-issues new      acme/tools/ground-tools "Title"
gl-issues new      people/bob/notes "Title" -due fri -assign me,alice -d "Details"
gl-issues new      weather-station "Title" -d - < description.md
```

## Arguments and limits

- Assignee specs: plain names replace the assignees, `+name` adds and
  `-name` removes. `none` removes every assignee; an empty spec is
  refused.
- `list -state` takes `opened` (default), `closed` or `all`.
- Due dates (`due`, `new -due`) take the formats listed in
  [Due dates](dashboard.md#due-dates).
- Each group of requests times out after 2 minutes ("timed out").
- Text read from standard input (`-`) may be at most 1 MB, about what
  GitLab accepts.

## The project for `new`

`new` takes the project's full path, its path below the group, or its
name (the project's display name or the last part of its path) if only
one project has that name. A fuzzy search like `ground` is
only used after you confirm it on a terminal (`[y/N]`); in scripts it is
an error, so an issue is never filed in a guessed project.

## Example

Example: a weekly nudge on every overdue issue.

```sh
gl-issues list -overdue | tail -n +2 | awk '{print $1}' |
  while read -r ref; do gl-issues comment "$ref" "Friendly reminder: this is past its due date."; done
```
