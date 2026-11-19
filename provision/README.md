# provision

Machine-level setup for the devbox, as code. `provision.sh` runs as root and
sets up the machine: data disk tuning, swap,
sysctl, packages, GitHub CLI, the developer's account (`--user`; on a machine that's already set up it defaults to the one account with `~/.devbox`),
the memory guard (earlyoom), Docker, Tailscale, lingering, `/etc/devbox/` and
project names (dnsmasq + Caddy, http and https). `user.sh` installs their own tools (mise and the
tools it manages, `just`, Rust, Claude Code); `provision.sh` runs it as the developer.

Both are idempotent: each step checks first, then prints `✓ step: already done`
or `→ step: changed (…)`. On a machine that's already set up they change nothing.

```sh
sudo provision/provision.sh --check        # what would change; exit 1 if anything would
sudo provision/provision.sh                # apply
sudo provision/provision.sh --only config  # one step (--help lists them)
sudo provision/provision.sh --upgrade      # also apt full-upgrade
```

`apt full-upgrade` only runs with `--upgrade` (or `--only upgrade`). Without
it, the upgrade step just reports how many packages are upgradable and doesn't
count as a change. A machine always has something to upgrade sooner or later,
and unattended-upgrades already applies security fixes every day.

From the operator's machine, `just provision-check <name>` runs `--check` on the VM without
changing it.

## Fresh VM

1. **cloud-init** (from `infra/`) sets the hostname and mounts the data disk
   (`/data`, with `/home`, `/var/lib/docker` and `/var/lib/containerd` bound
   onto it).
2. **provision.sh**, as root (from `just vm-shell <name>`, then `sudo -i`):
   ```sh
   git clone https://github.com/Jenish-1235/dsbox.git /root/dsbox
   /root/dsbox/provision/provision.sh --user <user> --upgrade
   ```
3. **Tailscale login** (interactive, prints a link): `tailscale up --ssh --hostname=devbox`,
   then `/root/dsbox/provision/provision.sh --only names`, which needs the
   Tailscale address.
4. **Their password** (interactive): `passwd <user>`. Provision never sets one.
5. **Clone** as the developer: `git clone https://github.com/Jenish-1235/dsbox.git ~/.devbox/src`
6. **install.sh** as the developer: `~/.devbox/src/install.sh`

After that, rerun `sudo ~/.devbox/src/provision/provision.sh` whenever the
machine setup changes, e.g. `--only config` after editing `config/devbox.env`.
The full procedure, including Tailscale's admin console, is in
[docs/operator-guide.md](../docs/operator-guide.md).

## What a person has to do

provision.sh prints these under "Needs a person" instead of doing them:

- `tailscale up`, which needs a browser login.
- `passwd <user>`, because only the developer should choose their password.
- Restarting Docker after its log settings change, when containers are running.
- Adding the devbox as the tailnet's nameserver for the apps domain (split DNS),
  until `tailscale dns status` lists it.

The CloudWatch agent is deliberately left out, and so is Codex.
