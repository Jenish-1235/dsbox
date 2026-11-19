#!/usr/bin/env bash
# lint-shell.sh — shellcheck every shell script in the repo. Used by `just lint` and CI,
# so the list lives in one place.
set -euo pipefail
cd "$(dirname "$0")/.."
shellcheck install.sh scripts/*.sh provision/*.sh laptop/mac/*.sh \
  components/welcome/files/devbox-welcome components/claude/files/devbox-claude-hooks
