# Installing gl-issues

This page covers every way to install gl-issues and how to check a
release download before you run it. Back to the [README](../README.md).

## Requirements

Linux and macOS (Intel and ARM). Needs [`glab`](https://gitlab.com/gitlab-org/cli)
at run time (`brew install glab` on macOS; Linux: see
[linux.md](linux.md)). gl-issues is tested with glab 1.123
(which keeps its login in the OS keyring; older glab stores it in plain
text); it relies on `glab api` with `--paginate`, GraphQL (`glab api graphql`, for
the tree's child items) and `GLAB_NO_PROMPT`, so use a recent glab
(`gl-issues doctor` prints the version in use). Building from source also
needs Go 1.26+, `git` and `make`.

## Release downloads

No Go needed. Archives, `.deb` and `.rpm` packages are on the project's
[Releases page](https://github.com/ansgomez/gl-issues/releases). Each
release has a `checksums.txt`, signed with
[Sigstore](https://www.sigstore.dev) (keyless), and an SBOM per archive.

### Verify a download

Check the checksum, then the signature of `checksums.txt`:

```sh
sha256sum -c checksums.txt --ignore-missing          # Linux
shasum -a 256 -c checksums.txt --ignore-missing      # macOS
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/ansgomez/gl-issues/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
```

Every file listed in `checksums.txt` also has a GitHub build provenance
attestation. With the GitHub CLI (`gh`) you can check one file, for
example:

```sh
gh attestation verify gl-issues_0.1.0_linux_amd64.tar.gz --repo ansgomez/gl-issues
```

### macOS: Gatekeeper

On macOS the binary is not notarized, so Gatekeeper blocks a
downloaded one ("cannot be opened because the developer cannot be
verified"). After checking the checksum, clear the quarantine flag:
`xattr -d com.apple.quarantine gl-issues`.

## From source

Go 1.26+, the version in `go.mod`; a newer `go`
command fetches the pinned toolchain itself. Clone the repository,
then `make install` (puts `gl-issues` in `$(go env GOPATH)/bin`) or
`make build` (for `./gl-issues`):

```sh
git clone https://github.com/ansgomez/gl-issues.git gl-issues && cd gl-issues
make install                      # into $(go env GOPATH)/bin; add that to PATH
```

## Linux

See [linux.md](linux.md) for installing glab, the
prebuilt binaries, where the config and binary live, and the
editor/browser differences.

## Next steps

Log glab in and create the config file: see
[Configuration](configuration.md).
