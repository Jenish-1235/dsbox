#!/usr/bin/env bash
# provision.sh — set up the devbox machine: disk, swap, packages, the user,
# Docker, Tailscale and /etc/devbox. Run as root. Safe to rerun: each step
# checks first and only acts when something is missing or different.
#
#   sudo provision/provision.sh              # apply every step
#   sudo provision/provision.sh --check      # show what would change; exit 1 if anything would
#   sudo provision/provision.sh --only config
#   sudo provision/provision.sh --upgrade    # also apt full-upgrade (fresh VMs)
#
# The developer's own tools (mise, Rust, Claude Code) are installed by
# provision/user.sh, which the user-tools step runs as the developer.
set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage: provision.sh [--check] [--only <step>] [--user <name>] [--upgrade]

  --check         show what would change and exit 1 if anything would; change nothing
  --only <step>   run one step (see below)
  --user <name>   the developer's account (default: the one account with ~/.devbox;
                  required on a fresh machine)
  --upgrade       also run apt full-upgrade (always do this on a fresh VM)

Steps, in order:
  disk         reserve only 1% of the data disk for root
  autoupdates  unattended-upgrades on, for daily security updates
  upgrade      apt full-upgrade; only with --upgrade or --only upgrade
  swap         16G /swapfile, on now and at boot
  sysctl       swappiness and file-watcher limits for dev servers
  packages     build tools and CLIs from Ubuntu, plus the fd link
  gh           GitHub CLI from GitHub's apt repo
  user         the developer's account, in sudo, with zsh
  memguard     earlyoom stops a runaway dev server before memory runs out
  docker      Docker from Docker's apt repo, log rotation, docker group
  tailscale    Tailscale installed (logging in is up to you)
  linger       user services keep running without a login session
  config       /etc/devbox/devbox.env from the repo
  datadirs     containerd's image store on the data disk; services wait for /data
  names        <project>.<apps domain> links: dnsmasq on Tailscale + Caddy on :80 and :443
  user-tools   provision/user.sh as the developer: mise and tools, Rust, Claude Code
EOF
}

steps=(disk autoupdates upgrade swap sysctl packages gh user memguard docker datadirs tailscale linger config names user-tools)

die() { printf 'provision: %s\n' "$*" >&2; exit 2; }

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(dirname "$here")

check=0
only=""
user=
upgrade=0
while (($#)); do
  case $1 in
    --check) check=1 ;;
    --only) only=${2:?--only needs a step name}; shift ;;
    --only=*) only=${1#*=} ;;
    --user) user=${2:?--user needs a name}; shift ;;
    --user=*) user=${1#*=} ;;
    --upgrade) upgrade=1 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown option $1 (see --help)" ;;
  esac
  shift
done
if [[ -n $only ]]; then
  [[ " ${steps[*]} " == *" $only "* ]] || die "unknown step '$only'; steps: ${steps[*]}"
  [[ $only != upgrade ]] || upgrade=1
fi
if [[ -z $user ]]; then
  found=()
  for d in /home/*/.devbox; do [[ -d $d ]] && found+=("$(basename "$(dirname "$d")")"); done
  ((${#found[@]} == 1)) || die "pass --user <name>: the developer's account (found ${#found[@]} accounts with ~/.devbox)"
  user=${found[0]}
fi
[[ $user =~ ^[a-z_][a-z0-9_-]*$ ]] || die "'$user' isn't a valid user name"
[[ $EUID -eq 0 ]] || die "run as root: sudo $0"
# shellcheck source=/dev/null
. /etc/os-release
[[ ${ID:-} == ubuntu ]] || die "this provisions Ubuntu (24.04); found ${PRETTY_NAME:-unknown}"

export DEBIAN_FRONTEND=noninteractive
backup_dir=/var/backups/devbox/$(date +%Y%m%d-%H%M%S)
arch=$(dpkg --print-architecture)
host=$(sed -n 's/^DEVBOX_HOST=//p' "$repo/config/devbox.env" 2>/dev/null || true)
host=${host:-devbox}

# --- Bookkeeping --------------------------------------------------------------

current=""  # step being run, for the failure message
pending=()  # what the current step changed (or, with --check, would change)
info=""     # a note about the current step that isn't a change
notes=()    # things only a person can do, printed at the end
changed=0   # steps that changed (or would change)

trap 'printf "✗ %s: failed (see the output above)\n" "${current:-provision}" >&2; exit 2' ERR

# todo "what" cmd args...: record a change and, unless --check, make it.
todo() {
  pending+=("$1")
  shift
  if ((check)); then return 0; fi
  "$@"
}

run_step() {
  current=$1
  pending=()
  info=""
  "step_${1//-/_}"
  if ((${#pending[@]} == 0)); then
    if [[ -n $info ]]; then
      printf '· %s: %s\n' "$1" "$info"
    else
      printf '✓ %s: already done\n' "$1"
    fi
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

# --- Helpers ------------------------------------------------------------------

# shellcheck disable=SC2016 # ${Status} is dpkg-query's format, not a shell variable
installed() { [[ $(dpkg-query -W -f='${Status}' "$1" 2>/dev/null || true) == "install ok installed" ]]; }

missing_packages() {
  local p
  for p in "$@"; do installed "$p" || printf '%s\n' "$p"; done
}

apt_updated=0
apt_install() {
  if ((!apt_updated)); then
    apt-get update -q
    apt_updated=1
  fi
  apt-get install -y -q "$@"
}

in_group() { [[ " $(id -nG "$1" 2>/dev/null || true) " == *" $2 "* ]]; }

# same_content path content: path holds exactly content plus a newline.
same_content() { [[ -f $1 ]] && cmp -s "$1" <(printf '%s\n' "$2"); }

# save_old path: copy path into this run's backup folder before replacing it.
save_old() {
  [[ -e $1 ]] || return 0
  mkdir -p "$backup_dir$(dirname "$1")"
  cp -a "$1" "$backup_dir$1"
  printf '  saved the old %s to %s\n' "$1" "$backup_dir$1"
}

# write_file path mode content: replace path atomically (root-owned), keeping a backup.
write_file() {
  local path=$1 mode=$2 content=$3 tmp
  save_old "$path"
  install -d -m 0755 "$(dirname "$path")"
  tmp=$(mktemp "$path.XXXXXX")
  printf '%s\n' "$content" >"$tmp"
  chown root:root "$tmp"
  chmod "$mode" "$tmp"
  mv -f "$tmp" "$path"
}

# copy_file src dest mode: like write_file, from a file.
copy_file() {
  save_old "$2"
  install -d -m 0755 "$(dirname "$2")"
  install -m "$3" -o root -g root "$1" "$2.new"
  mv -f "$2.new" "$2"
}

fetch_key() {
  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL "$1" -o "$2.new"
  chmod 0644 "$2.new"
  mv -f "$2.new" "$2"
}

add_apt_source() {
  write_file "$1" 0644 "$2"
  apt_updated=0 # the next install must see the new repo
}

owner_mode() { stat -c '%U:%G %a' "$1"; }

set_owner_mode() {
  chown root:root "$1"
  chmod "$2" "$1"
}

add_fstab_line() {
  save_old /etc/fstab
  printf '%s\n' "$1" >>/etc/fstab
}

load_sysctl() { sysctl --system >/dev/null; }

# --- Steps ----------------------------------------------------------------------

step_disk() {
  local dev=/dev/disk/by-label/devdata blocks reserved
  if [[ ! -e $dev ]]; then
    notes+=("No data disk labelled devdata. cloud-init (infra/cloud-init.yaml.tftpl) creates and mounts it.")
    info="no data disk, skipped"
    return
  fi
  blocks=$(tune2fs -l "$dev" | awk -F: '/^Block count/ {gsub(/ /, "", $2); print $2}')
  reserved=$(tune2fs -l "$dev" | awk -F: '/^Reserved block count/ {gsub(/ /, "", $2); print $2}')
  if ((reserved > blocks / 100 + 1)); then
    todo "reserve 1% of the data disk for root, not $((reserved * 100 / blocks))%" tune2fs -m 1 "$dev"
  fi
}

step_autoupdates() {
  installed unattended-upgrades || todo "install unattended-upgrades" apt_install unattended-upgrades
  local lists="" uu=""
  eval "$(apt-config shell lists APT::Periodic::Update-Package-Lists uu APT::Periodic::Unattended-Upgrade)"
  if [[ $lists != 1 || $uu != 1 ]]; then
    todo "turn on daily security updates" write_file /etc/apt/apt.conf.d/20auto-upgrades 0644 \
      $'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";'
  fi
}

# Upgrading isn't drift: a provisioned machine always has something to upgrade
# eventually, and unattended-upgrades already applies security fixes daily. So
# the full upgrade only runs when asked for, and otherwise is just reported.
step_upgrade() {
  local n
  n=$(apt list --upgradable 2>/dev/null | grep -c 'upgradable from' || true)
  if ((!upgrade)); then
    if ((n > 0)); then
      info="$n packages can be upgraded (as of the last apt update) → provision.sh --upgrade"
    else
      info="nothing to upgrade as of the last apt update"
    fi
    return
  fi
  if ((check)); then
    ((n == 0)) || todo "upgrade $n packages (as of the last apt update)" true
    return
  fi
  apt-get update -q
  apt_updated=1
  n=$(apt list --upgradable 2>/dev/null | grep -c 'upgradable from' || true)
  ((n == 0)) || todo "upgrade $n packages" apt-get full-upgrade -y -q
}

make_swapfile() {
  fallocate -l 16G /swapfile
  chmod 600 /swapfile
  mkswap /swapfile >/dev/null
}

step_swap() {
  local f=/swapfile active
  if [[ ! -e $f ]]; then
    todo "create a 16G $f" make_swapfile
  else
    [[ $(stat -c %a "$f") == 600 ]] || todo "make $f private (600)" chmod 600 "$f"
    active=$'\n'$(swapon --show=NAME --noheadings)$'\n'
    if [[ $active != *$'\n'$f$'\n'* && $(blkid -o value -s TYPE "$f" || true) != swap ]]; then
      todo "format $f as swap" mkswap "$f"
    fi
  fi
  active=$'\n'$(swapon --show=NAME --noheadings)$'\n'
  [[ $active == *$'\n'$f$'\n'* ]] || todo "turn on $f" swapon "$f"
  grep -Eq "^${f}[[:space:]]" /etc/fstab || todo "add $f to /etc/fstab" add_fstab_line "$f none swap sw 0 0"
}

step_sysctl() {
  local file=/etc/sysctl.d/90-devbox.conf
  local want=$'vm.swappiness = 10\nfs.inotify.max_user_watches = 1048576\nfs.inotify.max_user_instances = 1024'
  same_content "$file" "$want" || todo "write $file" write_file "$file" 0644 "$want"
  if [[ ${#pending[@]} -gt 0 || $(sysctl -n vm.swappiness) != 10 ||
    $(sysctl -n fs.inotify.max_user_watches) != 1048576 ||
    $(sysctl -n fs.inotify.max_user_instances) != 1024 ]]; then
    todo "load the settings" load_sysctl
  fi
}

step_packages() {
  local pkgs=(build-essential pkg-config libssl-dev curl wget git unzip zip jq ripgrep fd-find fzf
    tmux zsh htop btop ncdu tree ca-certificates gnupg zsh-autosuggestions zsh-syntax-highlighting)
  local missing
  mapfile -t missing < <(missing_packages "${pkgs[@]}")
  ((${#missing[@]} == 0)) || todo "install ${missing[*]}" apt_install "${missing[@]}"
  [[ $(readlink /usr/local/bin/fd || true) == /usr/bin/fdfind ]] ||
    todo "link fd to fdfind" ln -sf /usr/bin/fdfind /usr/local/bin/fd
}

step_gh() {
  local key=/etc/apt/keyrings/githubcli-archive-keyring.gpg list=/etc/apt/sources.list.d/github-cli.list
  local line="deb [arch=$arch signed-by=$key] https://cli.github.com/packages stable main"
  [[ -s $key ]] || todo "add GitHub's apt key" fetch_key https://cli.github.com/packages/githubcli-archive-keyring.gpg "$key"
  same_content "$list" "$line" || todo "add GitHub's apt repo" add_apt_source "$list" "$line"
  installed gh || todo "install gh" apt_install gh
}

step_user() {
  if ! id -u "$user" >/dev/null 2>&1; then
    todo "create $user" adduser --disabled-password --gecos "${user^}" "$user"
  fi
  in_group "$user" sudo || todo "add $user to sudo" usermod -aG sudo "$user"
  [[ $(getent passwd "$user" | cut -d: -f7) == /usr/bin/zsh ]] ||
    todo "make zsh $user's shell" chsh -s /usr/bin/zsh "$user"
  # A password is needed for sudo, and only a person should choose it.
  if [[ $(passwd -S "$user" 2>/dev/null | awk '{print $2}') != P ]]; then
    notes+=("$user has no password yet, and sudo needs one → passwd $user")
  fi
}

# --- memguard: earlyoom ------------------------------------------------------------
# The kernel only kills a process once memory and swap are exhausted, after
# minutes of thrashing. earlyoom acts first, at 4% free memory and 10% free
# swap: it stops the process with the highest score, preferring dev servers and
# compilers, and avoiding the services the machine (and the developer's session) need.
# The regexes match the process name (/proc/<pid>/comm, 15 characters). They
# have no spaces or quotes: systemd splits $EARLYOOM_ARGS at spaces.

restart_earlyoom() {
  systemctl enable --quiet earlyoom
  systemctl restart earlyoom
}

step_memguard() {
  local conf=/etc/default/earlyoom
  local prefer='^(node|next-server.*|vite|esbuild|tsc|go|cargo|rustc|python.*|java)$'
  local avoid='^(init|systemd.*|sshd|tailscaled|dockerd|containerd.*|caddy|dnsmasq|tmux.*|ds|earlyoom)$'
  local want="# devbox (provision.sh --only memguard): act at 4% free memory and 10% free swap.
EARLYOOM_ARGS=\"-m 4 -s 10 -r 3600 --prefer $prefer --avoid $avoid\""
  local restart=0
  installed earlyoom || todo "install earlyoom" apt_install earlyoom
  if ! same_content "$conf" "$want"; then
    todo "act at 4% free memory, prefer dev servers ($conf)" write_file "$conf" 0644 "$want"
    restart=1
  fi
  if ((restart)) || ! systemctl is-enabled --quiet earlyoom 2>/dev/null || ! systemctl is-active --quiet earlyoom; then
    todo "start earlyoom, now and at boot" restart_earlyoom
  fi
  # ds doctor reads earlyoom's log to say what it stopped.
  in_group "$user" systemd-journal || todo "let $user read system logs (systemd-journal)" usermod -aG systemd-journal "$user"
}

write_daemon_json() {
  write_file /etc/docker/daemon.json 0644 "$1"
  systemctl is-active --quiet docker || return 0
  if [[ -z $(docker ps -q) ]]; then
    systemctl restart docker
  else
    notes+=("Docker's new log settings apply after a restart; when no containers need to run: systemctl restart docker")
  fi
}

step_docker() {
  local key=/etc/apt/keyrings/docker.asc list=/etc/apt/sources.list.d/docker.list
  local line="deb [arch=$arch signed-by=$key] https://download.docker.com/linux/ubuntu ${UBUNTU_CODENAME:-$VERSION_CODENAME} stable"
  [[ -s $key ]] || todo "add Docker's apt key" fetch_key https://download.docker.com/linux/ubuntu/gpg "$key"
  same_content "$list" "$line" || todo "add Docker's apt repo" add_apt_source "$list" "$line"
  local missing
  mapfile -t missing < <(missing_packages docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin)
  ((${#missing[@]} == 0)) || todo "install ${missing[*]}" apt_install "${missing[@]}"

  local conf=/etc/docker/daemon.json
  local want='{ "log-driver": "json-file", "log-opts": { "max-size": "20m", "max-file": "5" } }'
  if ! same_content "$conf" "$want" &&
    ! { [[ -f $conf ]] && command -v jq >/dev/null &&
      [[ $(jq -cS . "$conf" 2>/dev/null) == "$(jq -cS . <<<"$want")" ]]; }; then
    todo "rotate container logs (20m x 5) in $conf" write_daemon_json "$want"
  fi
  systemctl is-enabled --quiet docker 2>/dev/null || todo "start Docker at boot" systemctl enable --now docker
  in_group "$user" docker || todo "add $user to docker" usermod -aG docker "$user"
}

# --- datadirs: everything big lives on the data disk ------------------------------
# Docker 29 keeps images and layers in containerd's store (/var/lib/containerd),
# so bind it onto the data disk like /var/lib/docker. The bind mounts are nofail
# (a missing data disk must not stop the boot), so services that write there
# wait for them explicitly instead.

move_containerd() {
  systemctl stop docker.socket docker containerd
  install -d -m 0711 /data/containerd
  if [[ -d /var/lib/containerd ]] && ! mountpoint -q /var/lib/containerd; then
    cp -a /var/lib/containerd/. /data/containerd/
    rm -rf /var/lib/containerd
  fi
  install -d -m 0711 /var/lib/containerd
  grep -Eq "^/data/containerd[[:space:]]" /etc/fstab ||
    add_fstab_line "/data/containerd /var/lib/containerd none bind,nofail,x-systemd.requires-mounts-for=/data 0 0"
  systemctl daemon-reload
  mount /var/lib/containerd
  systemctl start containerd docker
}

step_datadirs() {
  if ! grep -Eq '^/data/containerd[[:space:]]' /etc/fstab || ! mountpoint -q /var/lib/containerd; then
    if [[ -n $(docker ps -q 2>/dev/null) ]] && ((!check)); then
      notes+=("containerd's images are still on the OS disk: stop your containers, then provision.sh --only datadirs")
    else
      todo "move containerd's image store to the data disk (stops Docker briefly)" move_containerd
    fi
  fi
  local unit want changed=0
  for unit in docker containerd; do
    want="[Unit]
# devbox: start only once the data disk's bind mounts are in place.
RequiresMountsFor=/var/lib/docker /var/lib/containerd"
    if ! same_content "/etc/systemd/system/$unit.service.d/devbox-data.conf" "$want"; then
      todo "$unit waits for the data disk" write_file "/etc/systemd/system/$unit.service.d/devbox-data.conf" 0644 "$want"
      changed=1
    fi
  done
  want="[Unit]
# devbox: user services (dev servers, pages) need /home on the data disk.
RequiresMountsFor=/home"
  if ! same_content /etc/systemd/system/user@.service.d/devbox-data.conf "$want"; then
    todo "user services wait for /home" write_file /etc/systemd/system/user@.service.d/devbox-data.conf 0644 "$want"
    changed=1
  fi
  ((changed == 0)) || todo "reload systemd" systemctl daemon-reload
}

install_tailscale() { curl -fsSL https://tailscale.com/install.sh | sh; }

step_tailscale() {
  command -v tailscale >/dev/null || todo "install Tailscale" install_tailscale
  # Logging in prints a link for a person to open, so provision only reminds.
  local status
  status=$(tailscale status --json 2>/dev/null || true)
  [[ $status =~ \"BackendState\":[[:space:]]*\"Running\" ]] ||
    notes+=("Tailscale isn't logged in → tailscale up --ssh --hostname=$host (open the link it prints)")
}

step_linger() {
  [[ -e /var/lib/systemd/linger/$user ]] ||
    todo "let $user's services, like update checks, run without a login" loginctl enable-linger "$user"
}

step_config() {
  local dir=/etc/devbox src=$repo/config/devbox.env
  [[ -r $src ]] || die "can't read $src"
  local env=$dir/devbox.env
  [[ -d $dir ]] || todo "create $dir" install -d -m 0755 -o root -g root "$dir"
  if ! cmp -s "$src" "$env"; then
    todo "install $env from the repo" copy_file "$src" "$env" 0644
  elif [[ $(owner_mode "$env") != "root:root 644" ]]; then
    todo "make $env root:root 644" set_owner_mode "$env" 0644
  fi
}

# --- names: <project>.<apps domain> in the developer's browser --------------------------------
# A small DNS server (dnsmasq, Tailscale interface only) answers
# *.<apps domain> with this machine; Tailscale's split DNS sends the developer's laptop's
# lookups for that domain here. Caddy on ports 80 and 443, running as the developer,
# forwards each name to its project's port using the routes file ds writes, and
# reloads when that file changes. With DEVBOX_APPS_TLS=internal, Caddy's own CA
# (in ~/.local/share/caddy, where ds cert finds it) signs the https certificates.

names_reload() {
  systemctl daemon-reload
  systemctl enable --quiet devbox-dns devbox-web
  systemctl restart devbox-dns devbox-web
}

# split_dns_set domain ip: Tailscale already sends lookups under domain to ip
# (the tailnet's split DNS, as tailscale dns status shows it; Tailscale 1.74+).
split_dns_set() {
  tailscale dns status 2>/dev/null |
    awk -v d="$1" -v ip="$2" '$1 == "-" && ($2 == d || $2 == d ".") && $3 == "->" && $4 == ip { found = 1 } END { exit !found }'
}

first_routes() {
  # Create the folders as the developer, so ~/.devbox and ~/.devbox/state are
  # theirs too (install -d -o only owns the last one).
  sudo -u "$user" mkdir -p "$(dirname "$1")"
  printf '{\n\tauto_https off\n}\n\nhttp://%s {\n\trespond "No projects yet: ds new <template> <name>"\n}\n' "$2" >"$1"
  chown "$user:$user" "$1"
}

step_names() {
  local domain
  domain=$(sed -n 's/^DEVBOX_APPS_DOMAIN=//p' "$repo/config/devbox.env" | tail -1)
  [[ -n $domain ]] || return 0

  local missing
  mapfile -t missing < <(missing_packages caddy dnsmasq-base)
  ((${#missing[@]} == 0)) || todo "install ${missing[*]}" apt_install "${missing[@]}"
  # Ubuntu's own caddy service serves a placeholder page on :80; devbox-web replaces it.
  if systemctl is-enabled --quiet caddy 2>/dev/null || systemctl is-active --quiet caddy 2>/dev/null; then
    todo "turn off Ubuntu's default caddy service" systemctl disable --now caddy
  fi

  local ip
  ip=$(tailscale ip -4 2>/dev/null | head -1 || true)
  if [[ -z $ip ]]; then
    notes+=("Tailscale has no address yet, so *.$domain isn't set up → log in to Tailscale, then: provision.sh --only names")
    return 0
  fi
  local home routes changed=0
  home=$(getent passwd "$user" | cut -d: -f6)
  routes=$home/.devbox/state/caddy/Caddyfile

  local dns_conf=/etc/devbox/dnsmasq.conf
  local dns_want="# devbox: answer *.$domain with this machine's Tailscale address, on the
# Tailscale interface only. The machine's own DNS is untouched.
interface=tailscale0
bind-dynamic
no-resolv
no-hosts
user=nobody
group=nogroup
address=/$domain/$ip"
  if ! same_content "$dns_conf" "$dns_want"; then
    todo "answer *.$domain with $ip" write_file "$dns_conf" 0644 "$dns_want"
    changed=1
  fi

  local dns_unit=/etc/systemd/system/devbox-dns.service
  local dns_unit_want="[Unit]
Description=devbox: *.$domain names for project links
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
ExecStart=/usr/sbin/dnsmasq --keep-in-foreground --conf-file=$dns_conf
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target"
  if ! same_content "$dns_unit" "$dns_unit_want"; then
    todo "add the devbox-dns service" write_file "$dns_unit" 0644 "$dns_unit_want"
    changed=1
  fi

  [[ -s $routes ]] || todo "write the first routes file $routes" first_routes "$routes" "$domain"

  local web_unit=/etc/systemd/system/devbox-web.service
  local web_unit_want="[Unit]
Description=devbox: forward <project>.$domain to each project's port (routes written by ds)
After=network-online.target
Wants=network-online.target

[Service]
User=$user
Environment=HOME=$home XDG_DATA_HOME=$home/.local/share
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/usr/bin/caddy run --config $routes --adapter caddyfile --watch
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target"
  if ! same_content "$web_unit" "$web_unit_want"; then
    todo "add the devbox-web service" write_file "$web_unit" 0644 "$web_unit_want"
    changed=1
  fi

  if ((changed)) || ! systemctl is-active --quiet devbox-dns || ! systemctl is-active --quiet devbox-web; then
    todo "start devbox-dns and devbox-web" names_reload
  fi
  # Only while it's outstanding, or when this run changed the names setup.
  if ((changed)) || ! split_dns_set "$domain" "$ip"; then
    notes+=("Tailscale admin console (the account devbox is on) → DNS → Add nameserver → Custom: $ip, Restrict to domain: $domain (once)")
  fi
}

step_user_tools() {
  if ! id -u "$user" >/dev/null 2>&1; then
    todo "install mise and its tools, Rust and Claude Code for $user" true
    return
  fi
  local args=() log rc=0
  ((!check)) || args+=(--check)
  log=$(mktemp)
  # Piped in on stdin so it works wherever this repo is, even in /root.
  # shellcheck disable=SC2024 # root reads user.sh and feeds it to the user's bash on purpose
  sudo -iu "$user" bash -s -- "${args[@]}" <"$here/user.sh" 2>&1 | sed -u 's/^/  /' | tee "$log" || rc=$?
  if grep -q '^  →' "$log"; then
    pending+=("as $user, see above")
  fi
  rm -f "$log"
  # user.sh exits 1 for "would change" under --check; anything else is a failure.
  if ((rc != 0)) && ! ((check && rc == 1)); then
    printf '✗ user-tools: user.sh failed as %s\n' "$user" >&2
    exit 2
  fi
}

# --- Main -----------------------------------------------------------------------

if ((check)); then
  printf 'Checking what provision.sh would change (nothing is changed)\n'
fi
if [[ -n $only ]]; then
  run_step "$only"
else
  for s in "${steps[@]}"; do run_step "$s"; done
fi
current=""

if ((${#notes[@]} > 0)); then
  printf '\nNeeds a person:\n'
  for n in "${notes[@]}"; do printf '! %s\n' "$n"; done
fi
echo
if ((check)); then
  if ((changed > 0)); then
    printf '%d step(s) would change. Run without --check to apply.\n' "$changed"
    exit 1
  fi
  printf '✓ Nothing to change\n'
elif ((changed > 0)); then
  printf '✓ Done: %d step(s) changed\n' "$changed"
else
  printf '✓ Already provisioned, nothing changed\n'
fi
