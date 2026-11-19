#!/usr/bin/env bash
# devbox-paste.sh — send a screenshot to the devbox and copy its path, so you
# can paste it into Claude Code running there (Cmd+V in the terminal).
#
#   devbox-paste.sh             the image on the clipboard (e.g. Cmd+Ctrl+Shift+4)
#   devbox-paste.sh shot.png    an image file instead (any format; sent as PNG)
#
# It uploads to devbox:~/.devbox/inbox/shot-<time>.png (kept 7 days; ds inbox
# lists them) and puts that file's full path on the clipboard.
# Needs `ssh devbox` to work. Uses pngpaste when installed (brew install
# pngpaste), otherwise macOS's own osascript.
# Env: DEVBOX_HOST (default devbox).
set -euo pipefail

# Shortcuts and Automator run with a bare PATH.
export PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
host=${DEVBOX_HOST:-devbox}

# Feedback goes to the terminal, or to Notification Center when there's none.
say() {
  if [[ -t 2 ]]; then
    printf '%s\n' "$1" >&2
  else
    osascript -e 'on run argv' -e 'display notification (item 2 of argv) with title (item 1 of argv)' -e 'end run' \
      "devbox paste" "$1" >/dev/null 2>&1 || true
  fi
}
fail() {
  say "$1"
  exit 1
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
name="shot-$(date +%Y%m%d-%H%M%S).png"
png="$work/$name"

if [[ $# -gt 0 ]]; then
  [[ -f $1 ]] || fail "No such file: $1"
  # Converted, so the upload really is the PNG its name says (JPEG, HEIC, TIFF...).
  sips -s format png "$1" --out "$png" >/dev/null 2>&1 || fail "Not an image macOS can read: $1"
elif command -v pngpaste >/dev/null 2>&1; then
  pngpaste "$png" >/dev/null 2>&1 || fail "No image on the clipboard. Take a screenshot with Cmd+Ctrl+Shift+4 first."
else
  # The clipboard as PNG; screenshots also offer TIFF, which sips converts.
  if ! osascript -e 'on run argv' \
    -e 'set f to open for access (POSIX file (item 1 of argv)) with write permission' \
    -e 'try' -e 'write (the clipboard as «class PNGf») to f' \
    -e 'on error' -e 'close access f' -e 'error number -1' -e 'end try' \
    -e 'close access f' -e 'end run' "$png" >/dev/null 2>&1; then
    tiff="$work/clip.tiff"
    if ! { osascript -e 'on run argv' \
      -e 'set f to open for access (POSIX file (item 1 of argv)) with write permission' \
      -e 'write (the clipboard as TIFF picture) to f' -e 'close access f' -e 'end run' "$tiff" >/dev/null 2>&1 &&
      sips -s format png "$tiff" --out "$png" >/dev/null 2>&1; }; then
      fail "No image on the clipboard. Take a screenshot with Cmd+Ctrl+Shift+4 first."
    fi
  fi
fi
[[ -s $png ]] || fail "No image on the clipboard. Take a screenshot with Cmd+Ctrl+Shift+4 first."

# Make the inbox and learn its full path (Claude needs an absolute path).
ssh_opts=(-o BatchMode=yes -o ConnectTimeout=10)
dir=$(ssh "${ssh_opts[@]}" "$host" 'mkdir -p ~/.devbox/inbox && cd ~/.devbox/inbox && pwd' 2>"$work/err") ||
  fail "Couldn't reach $host: $(head -n1 "$work/err")"
scp -q "${ssh_opts[@]}" "$png" "$host:$dir/$name" 2>"$work/err" ||
  fail "Upload to $host failed: $(head -n1 "$work/err")"

printf '%s' "$dir/$name" | pbcopy
say "Path copied: $dir/$name. Paste it into Claude."
