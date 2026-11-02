#!/bin/sh
# Builds the sandbox that docs/demo/demo.tape records: a fake HOME with a
# config for the made-up group acme on gitlab.example.com, and a fake glab
# (fake_glab.py) that answers like GitLab from a JSON file of made-up
# issues. Nothing from this machine or any real server is used.
#
# Usage: docs/demo/setup.sh <dir> <gl-issues binary> [dark|light]
# Then:  . <dir>/env.sh   (sets HOME, PATH and XDG dirs for the recording)
set -eu
D=$1 BIN=$2 THEME=${3:-dark}
case $THEME in dark|light) ;; *) echo "theme must be dark or light" >&2; exit 2 ;; esac
here=$(cd "$(dirname "$0")" && pwd)
# never wipe a directory this script did not make
if [ -e "$D" ] && [ ! -e "$D/.gl-issues-demo" ]; then
  echo "$D exists and is not a gl-issues demo sandbox; pick another directory" >&2; exit 1
fi
rm -rf "$D"; mkdir -p "$D/bin" "$D/home/.config/gl-issues" "$D/state"
D=$(cd "$D" && pwd); touch "$D/.gl-issues-demo"
cp "$BIN" "$D/bin/gl-issues"
cp "$here/fake_glab.py" "$D/bin/fake_glab.py"
printf '#!/bin/sh\nexec %s %s "$@"\n' "$(command -v python3)" "$D/bin/fake_glab.py" > "$D/bin/glab"
chmod 755 "$D/bin/glab"

cat > "$D/home/.config/gl-issues/config.toml" <<CFG
host = "gitlab.example.com"
group = "acme"
refresh = "off"
editor = "true"
browser = "true"
theme = "$THEME"
CFG
chmod 700 "$D/home/.config" "$D/home/.config/gl-issues"; chmod 600 "$D/home/.config/gl-issues/config.toml"

cat > "$D/env.sh" <<ENV
export HOME="$D/home" PATH="$D/bin:/usr/bin:/bin" XDG_CONFIG_HOME="$D/home/.config"
export XDG_STATE_HOME="$D/home/.local/state" FAKE_STATE="$D/state" TERM=xterm-256color
# every GL_ISSUES_* override gl-issues reads (the config file
# and each setting): the recording must use the sandbox's config only
unset GL_ISSUES_CONFIG GL_ISSUES_HOST GL_ISSUES_GROUP GL_ISSUES_REFRESH GL_ISSUES_REFRESH_LISTS GL_ISSUES_NOTIFY GL_ISSUES_TODOS GL_ISSUES_SORT GL_ISSUES_STATE GL_ISSUES_VIEW GL_ISSUES_FILTER GL_ISSUES_DUE_SOON_DAYS GL_ISSUES_EDITOR GL_ISSUES_BROWSER GL_ISSUES_TREE_DEPTH GL_ISSUES_THEME
unset GITLAB_TOKEN GITLAB_HOST GLAB_CONFIG_DIR
cd "\$HOME"
PS1='\$ '
clear
ENV
echo "sandbox ready: . $D/env.sh"
