#!/usr/bin/env bash
# user.sh — install the developer's own tools: mise and the tools it manages,
# Rust (rustup) and Claude Code. Run as the developer, not root; provision.sh runs it for
# the developer with sudo -iu. Safe to rerun: it only installs what's missing and never
# changes versions the developer picked.
#
#   provision/user.sh            # install what's missing
#   provision/user.sh --check    # show what would change; exit 1 if anything would
set -Eeuo pipefail

die() { printf 'user.sh: %s\n' "$*" >&2; exit 2; }

check=0
case ${1:-} in
  --check) check=1 ;;
  "") ;;
  -h | --help)
    sed -n '2,9p' "$0" | sed 's/^# \{0,1\}//'
    exit 0
    ;;
  *) die "unknown option $1 (see --help)" ;;
esac
[[ $EUID -ne 0 ]] || die "run this as the developer, not root"

mise=$HOME/.local/bin/mise
mise_config=$HOME/.config/mise/config.toml
# Installed with mise use -g. A tool already in the developer's global config is left at
# whatever version the developer chose.
tools=(node@lts python@3.13 go@latest pnpm@latest bun@latest uv@latest lazygit@latest
  lazydocker@latest starship@latest eza@latest bat@latest zoxide@latest fzf@latest
  delta@latest just@latest glab@latest gitleaks@latest golangci-lint@latest
  aqua:fe3dback/go-arch-lint@latest npm:squawk-cli@latest)

current=""
pending=()
changed=0
trap 'printf "✗ %s: failed (see the output above)\n" "${current:-user.sh}" >&2; exit 2' ERR

todo() {
  pending+=("$1")
  shift
  if ((check)); then return 0; fi
  "$@"
}

run_step() {
  current=$1
  pending=()
  "step_${1//-/_}"
  if ((${#pending[@]} == 0)); then
    printf '✓ %s: already done\n' "$1"
    return
  fi
  changed=$((changed + 1))
  local what
  printf -v what '%s, ' "${pending[@]}"
  if ((check)); then
    printf '→ %s: would change (%s)\n' "$1" "${what%, }"
  else
    printf '→ %s: changed (%s)\n' "$1" "${what%, }"
  fi
}

# from_web url cmd args...: download an installer and pipe it to cmd.
from_web() {
  local url=$1
  shift
  curl --proto '=https' --tlsv1.2 -fsSL "$url" | "$@"
}

configured() { [[ -f $mise_config ]] && grep -Eq "^[[:space:]]*\"?$1\"?[[:space:]]*=" "$mise_config"; }

step_mise() {
  [[ -x $mise ]] || todo "install mise" from_web https://mise.run sh
}

step_mise_tools() {
  local spec missing=() absent=""
  for spec in "${tools[@]}"; do
    configured "${spec%@*}" || missing+=("$spec")
  done
  ((${#missing[@]} == 0)) || todo "add ${missing[*]%@*}" "$mise" use -g "${missing[@]}"
  # Tools in the developer's config whose versions aren't downloaded (e.g. after a rebuild).
  if [[ -x $mise ]]; then
    absent=$("$mise" ls --global --missing 2>/dev/null || true)
  fi
  [[ -z $absent ]] || todo "download missing tool versions" "$mise" install
}

step_rust() {
  # rustup also writes ~/.cargo/env, which the managed ~/.zshenv sources. It
  # adds a line to ~/.zshenv too; install.sh later replaces that file (with a
  # backup), so run this before install.sh on a fresh machine.
  [[ -x $HOME/.cargo/bin/rustup ]] ||
    todo "install Rust with rustup" from_web https://sh.rustup.rs sh -s -- -y
}

step_claude() {
  [[ -x $HOME/.local/bin/claude ]] || todo "install Claude Code" from_web https://claude.ai/install.sh bash
}

# ADLC: the agent rules, standards, skills and project templates that dev new
# uses. Kept as a clone in ~/.devbox/adlc and fast-forwarded on every run.
adlc_repo=${ADLC_REPO:-https://github.com/Jenish-1235/adlc.git}
adlc_ref=${ADLC_REF:-main}
adlc_dir=$HOME/.devbox/adlc

adlc_update() {
  git -C "$adlc_dir" fetch --quiet origin "$adlc_ref"
  git -C "$adlc_dir" checkout --quiet "$adlc_ref"
  git -C "$adlc_dir" merge --quiet --ff-only "origin/$adlc_ref"
}

step_adlc() {
  if [[ ! -d $adlc_dir/.git ]]; then
    todo "clone ADLC ($adlc_ref) into $adlc_dir" git clone --quiet --branch "$adlc_ref" "$adlc_repo" "$adlc_dir"
    return
  fi
  local here there=""
  here=$(git -C "$adlc_dir" rev-parse HEAD)
  if ((check)); then
    # Ask the remote without fetching, so --check writes nothing to the clone.
    there=$(git -C "$adlc_dir" ls-remote origin "refs/heads/$adlc_ref" 2>/dev/null | cut -f1) || true
    [[ -n $there ]] ||
      printf '! adlc: could not reach origin; compared with the last fetched %s\n' "$adlc_ref" >&2
  else
    git -C "$adlc_dir" fetch --quiet origin "$adlc_ref" 2>/dev/null || true
  fi
  [[ -n $there ]] || there=$(git -C "$adlc_dir" rev-parse "origin/$adlc_ref" 2>/dev/null || echo "$here")
  [[ $here == "$there" ]] || todo "update ADLC to the latest $adlc_ref" adlc_update
}

for s in mise mise-tools rust claude adlc; do run_step "$s"; done
current=""
if ((check && changed > 0)); then
  exit 1
fi
