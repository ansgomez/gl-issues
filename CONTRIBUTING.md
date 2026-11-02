# Contributing

Thanks for helping! Bug reports, ideas and pull requests are welcome.

## Before you start

- For larger changes, open an issue first so we can agree on the approach.
- Read [AGENTS.md](AGENTS.md): it describes the layout and the rules the
  code follows (all GitLab access through non-interactive `glab api`, no
  token held by gl-issues (glab's own login), writes only on an explicit
  key or subcommand, sanitized server text).

## Development

```sh
make build        # ./gl-issues
make ci           # the CI checks (gofmt, vet, lint, actionlint, goreleaser check, race tests, govulncheck, gitleaks, build)
make cover        # tests with coverage (coverage.out, coverage.xml)
make snapshot     # release build into dist/ without publishing
```

Requirements: Go (the version in `go.mod`; the `go` command fetches it
automatically) and git. The tests never talk to GitLab: they use a fake
`glab` script, so glab need not be installed to run them.

`make ci` runs the checks CI runs: gofmt, `go vet` (also for Linux),
golangci-lint, actionlint, the GoReleaser config check, tests with the
race detector, govulncheck, gitleaks over the git history, and a build.
CI additionally runs `make cover` (the tests with a coverage report),
`make snapshot` (a release build into `dist/` without publishing) and
CodeQL. Tools run at pinned versions via `go run`, so nothing needs to
be installed. Other targets: `make config` (runs `gl-issues init`). The
tests never contact GitLab: they run against a fake `glab` script.

## CI and releases

GitHub Actions (`.github/workflows`) runs the Makefile targets: lint,
tests on Linux and macOS, coverage, govulncheck, gitleaks, and a
release snapshot, plus CodeQL code scanning. Dependencies are kept
current by Dependabot.

Pushing a `v*` tag (e.g. `git tag v0.1.0 && git push origin v0.1.0`)
publishes a GitHub release with GoReleaser: binaries for
Linux/macOS × amd64/arm64, `.deb`/`.rpm`, checksums signed with cosign
(keyless), SBOMs, and a build provenance attestation. A
Homebrew tap (macOS and Linux) can be enabled in `.goreleaser.yaml`.
[Installing gl-issues](docs/install.md#verify-a-download) shows how to
verify a release.

See also [SECURITY.md](SECURITY.md).

## Pull requests

- Keep changes focused; one topic per request.
- Add tests: unit tests for parsing and logic (refs, dates, assign specs,
  config, filters), and keep `TestViewSmoke` passing for new screens.
- Update the docs and CHANGELOG.md (under an "Unreleased" heading above
  the last release) when behaviour changes. The README is the short
  overview; the details live in `docs/`: `install.md`,
  `configuration.md` (settings, login, refresh), `dashboard.md` (keys,
  tree, pop-ups, update tags), `scripting.md` (subcommands),
  `troubleshooting.md` and `linux.md`. A new setting goes in
  `config.example.toml` and the settings table in
  `docs/configuration.md`.
- Never put real hostnames, group/project paths, usernames or tokens in
  code, tests or docs; use `gitlab.example.com` (docs; tests also use
  `gitlab.example.edu`), `acme/…`, alice, bob, carol and the like. The
  README badges are the one exception: they link the project's own
  GitHub Actions runs and releases.
- Commit messages: a short imperative subject ("Add …", "Fix …"), body
  explaining why.

## Releases (maintainers)

Update CHANGELOG.md, then tag: `git tag v0.1.0 && git push origin v0.1.0`.
CI builds, signs and publishes the release (see
[CI and releases](#ci-and-releases)).
