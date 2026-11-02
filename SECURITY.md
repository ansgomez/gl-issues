# Security policy

## Supported versions

Only the latest release receives fixes.

## Reporting a vulnerability

Please do **not** open a public issue. Report privately instead:

- GitHub: *Security → Report a vulnerability* (private advisory)

Include what you found, how to reproduce it, and the impact. You'll get an
answer within a week; fixes are released as soon as possible and credited
unless you prefer otherwise.

## Scope

gl-issues runs `glab api`, which uses glab's own login (kept in the OS
keyring by glab 1.123+ by default; `gl-issues doctor` reports where), and
prints text written by other GitLab users in your
terminal. gl-issues holds no token. Particularly relevant:

- a token reaching anything but glab (command lines, environment, output,
  logs, files), or being sent to another host,
- `gl-issues doctor` printing anything of `glab auth status` beyond where
  the token is kept and the API protocol,
- text from GitLab that could reach the terminal unsanitized (escape
  sequences, control or bidi characters),
- anything that writes to GitLab without an explicit key, confirmation or
  subcommand, or acts on a different issue than the one shown,
- URLs from the server being opened outside the configured GitLab host.

See [Keeping the token safe](docs/configuration.md#keeping-the-token-safe)
for the design.
