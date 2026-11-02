# gl-issues on Linux

gl-issues works the same on Linux as on macOS. This page covers only what
is different. Everything else is in the [README](../README.md) and the
other pages in this folder.

## 1. Install glab

gl-issues calls `glab` for every GitLab request, so install it first.

| Distribution | Command |
| --- | --- |
| Fedora | `sudo dnf install glab` |
| Arch | `sudo pacman -S glab` |
| Debian / Ubuntu / others | Download the `.deb`, `.rpm` or `.tar.gz` from the [glab releases](https://gitlab.com/gitlab-org/cli/-/releases) page |

Check it: `glab --version`.

## 2. Install gl-issues

**Option A: prebuilt binary (no Go needed).** Download the archive,
`.deb` or `.rpm` for your machine (`uname -m`: `x86_64` → `amd64`,
`aarch64` → `arm64`) from the project's Releases page and check it as
shown in [Verify a download](install.md#verify-a-download). The `.deb`/`.rpm` install
`/usr/bin/gl-issues`; from the archive, put the binary in your `PATH`:

```sh
tar xzf gl-issues_*_linux_amd64.tar.gz gl-issues   # arm64: *_linux_arm64.tar.gz
mkdir -p ~/.local/bin
install -m 755 gl-issues ~/.local/bin/gl-issues
export PATH="$HOME/.local/bin:$PATH"          # if that is not on PATH yet
```

To build the same files yourself on any machine with Go 1.26+, run
`make snapshot` (output in `dist/`) and copy the binary over, e.g. with
`scp`. The binaries are static, so they need no libraries and run on any
distribution.

**Option B: build on the Linux machine.** This needs `git` and Go 1.26
or newer (a Go 1.21+ `go` command downloads the toolchain `go.mod` asks
for by itself, unless `go env GOTOOLCHAIN` says `local`). Distro Go packages are often older; check with `go version`,
and if needed install Go from https://go.dev/dl/.

```sh
git clone https://github.com/ansgomez/gl-issues.git gl-issues
cd gl-issues && make install    # into $(go env GOPATH)/bin, usually ~/go/bin; add that to PATH
```

## 3. Where things live on Linux

| What | Location |
| --- | --- |
| gl-issues config (host, group; no token) | `~/.config/gl-issues/config.toml`, or `$XDG_CONFIG_HOME/gl-issues/config.toml` if that variable is an absolute path |
| Config template | built in (`gl-issues init`); also `config.example.toml` in the archive, `/usr/share/doc/gl-issues/` for `.deb`/`.rpm` |
| glab login (the token) | the OS keyring (Secret Service) by default from glab 1.123 on; `~/.config/glab-cli/config.yml`, in plain text, with older glab, with `glab auth login --insecure-storage`, or when no keyring is available (`gl-issues doctor` says which) |
| gl-issues binary | `/usr/bin` (`.deb`/`.rpm`), wherever you put it (archive, e.g. `~/.local/bin`) or `~/go/bin` (Option B) |
| Recently used projects (no secrets) | `~/.local/state/gl-issues/state.json`, or `$XDG_STATE_HOME/gl-issues/` |
| Temporary files while editing | `~/.cache/gl-issues/` (or `$XDG_CACHE_HOME/gl-issues/` if that is an absolute path), mode 700, deleted after saving |

gl-issues used to be called gitissue: on the first run it moves
`~/.config/gitissue` and `~/.local/state/gitissue` (or their
`$XDG_…` counterparts) to the new places above and says so; see
[Upgrading from gitissue](configuration.md#upgrading-from-gitissue).

Create the config the same way as on macOS:

```sh
gl-issues init     # writes the template, mode 600, never overwrites
```

Then set `host` and `group`, log glab in to the same host, as in the
README's [Quick start](../README.md#quick-start) and
[Log in and token scopes](configuration.md#log-in-and-token-scopes), and run
`gl-issues doctor` to check the setup.

gl-issues refuses a config file that other users can read. On shared
servers this matters even more, so keep it `chmod 600`.

## 4. Network

gl-issues and glab talk to GitLab over HTTPS by default (port 443 unless
`host` names another port; glab's `api_protocol` setting can make it
http, which `gl-issues doctor` warns about). If your GitLab is only reachable from an internal
network or over a VPN, connect first; a firewall or proxy must let HTTPS
to the host through. `glab auth status --hostname <host>` is a quick way
to check that the host is reachable and that you are logged in.

## 5. Differences from macOS

- **Editor (`e`, `ctrl+e` in pop-ups).** gl-issues uses `$VISUAL`, then `$EDITOR`, then
  `vi`, then `nano`, whichever is installed. Set your favourite in
  `~/.bashrc`: `export EDITOR=nano`.
- **Open in browser (`w`).** gl-issues tries `$BROWSER`, then `xdg-open`,
  `wslview` (WSL) and `sensible-browser`. Over SSH or on a server without
  a desktop, none of them works. gl-issues then shows the issue URL at the
  bottom of the screen so you can copy it.
- **Colours.** Use a terminal with 256 colours (`TERM=xterm-256color`).
  Inside `tmux` or `screen`, set `default-terminal "tmux-256color"`.

## Troubleshooting

[Troubleshooting](troubleshooting.md) covers login,
token scopes, group paths and network errors. Linux-specific:

- **`glab not found in PATH`.** Install glab (step 1) or add its directory
  to `PATH`.
- **`gl-issues: command not found`.** Add `~/.local/bin` or `~/go/bin` to
  `PATH` in `~/.bashrc` or `~/.zshrc`.
- **`lookup <host>: no such host`.** The host name does not resolve; check
  the `host` setting, your network (or VPN, if your GitLab needs one) and
  `/etc/resolv.conf` (or `resolvectl status` on systemd distros).
- **`go: go.mod requires go >= 1.26.0`.** Your Go is too old to fetch
  the toolchain itself, or toolchain downloads are off
  (`GOTOOLCHAIN=local`, common in distro packages; try
  `GOTOOLCHAIN=auto make install`). Otherwise use Option A or install a
  newer Go.
