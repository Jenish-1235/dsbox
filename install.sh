#!/usr/bin/env bash
# install.sh — set up devbox for the current user. Safe to rerun.
#
# Clones dsbox into ~/.devbox/src (or uses the clone already there),
# builds ds and dev into ~/.devbox/bin, and applies every component.
set -euo pipefail

root="${DEVBOX_HOME:-$HOME/.devbox}"
repo="${DEVBOX_REPO:-https://github.com/Jenish-1235/dsbox.git}"
src="$root/src"

say() { printf '\033[1m%s\033[0m\n' "$*"; }
die() { printf 'install: %s\n' "$*" >&2; exit 1; }

command -v git >/dev/null || die "git is required"
if command -v go >/dev/null; then
  go_cmd=(go)
elif [ -x "$HOME/.local/bin/mise" ]; then
  go_cmd=("$HOME/.local/bin/mise" exec -- go)
else
  die "Go is required: mise use -g go@latest"
fi

mkdir -p "$root"/{bin,state,logs,backups,templates}

if [ -d "$src/.git" ]; then
  say "Using the existing clone in $src"
else
  say "Cloning dsbox into $src"
  git clone --quiet "$repo" "$src"
fi

say "Building ds and dev"
(cd "$src" && "${go_cmd[@]}" build -trimpath -o "$root/bin/" ./cmd/...)

"$root/bin/ds" apply

echo
say "Done. Open a new terminal (or run: exec zsh), then: ds doctor"
