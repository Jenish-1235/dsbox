# dsbox

dsbox sets up and maintains remote Linux development machines ("devboxes") on
AWS. Docker, builds, dev servers and coding agents such as Claude Code run on
the VM. The laptop runs a terminal, an editor connected over Remote-SSH
(Cursor or VS Code) and a browser. It is for developers who want a large,
always-on machine that is not reachable from the internet, and for the
operator who creates and maintains those machines (often the same person).

The project is called dsbox; on the machine everything is still called devbox
(`~/.devbox`, `DEVBOX_*` settings, the default hostname `devbox`, links such as
`http://shop.devbox`), and the command is `ds`.

## How it works

```
 laptop                                   devbox (EC2, Ubuntu 24.04)
 ------------------------------           -------------------------------------------
 terminal       ssh devbox  ----+         zsh, tmux, ds
 Cursor/VS Code Remote-SSH  ----+         dev servers, one tmux window each
 browser        shop.devbox ----+         Caddy :80/:443 -> each project's port
 DB tools       devbox:5432 ----+-- Tailscale --> dnsmasq (*.devbox, tailnet only)
 phone          shop.devbox ----+         Docker and Compose, databases
                                          Claude Code and other agents
                                          data disk: /home, Docker, containerd
```

- Every connection goes over Tailscale. The security group allows no inbound
  traffic except Tailscale's WireGuard port (UDP 41641), which drops
  unauthenticated packets.
- `<project>.devbox` resolves through Tailscale split DNS to dnsmasq on the
  devbox, which answers with the devbox's Tailscale address. Caddy forwards
  each name to the project's port.
- `/home`, Docker and containerd live on a separate encrypted data disk, so the
  instance can be replaced without losing work.
- AWS Systems Manager (SSM) gives the operator a shell without SSH or
  Tailscale.

## Features

- **Infrastructure** (`infra/`, OpenTofu): one EC2 instance per devbox, a
  persistent encrypted gp3 data disk, daily snapshots, a monthly budget alert,
  SSM access, IMDSv2 only. Any AWS region.
- **Provisioning** (`provision/`): idempotent machine setup as root, with
  `--check` to show drift, and the developer's tools (mise, Node, Python, Go,
  Rust, just, Claude Code and more) installed as the developer.
- **Projects** (`ds`): create a project from a template, run it as a
  background dev server on a stable port, follow its logs, attach to it, and
  open it at `http://<project>.devbox`, optionally over HTTPS signed by the
  devbox's own certificate authority.
- **Docker view** (`ds d`): containers grouped by project, with logs, a shell,
  restart, ports, database connection strings and disk use.
- **Publishing** (`ds publish`): push a project to a new private repository on
  GitHub or GitLab.
- **Upkeep**: `ds update`, `ds rollback`, `ds doctor`, `ds status`,
  `ds config`, `ds cheat`, and a daily update check that only prints a notice.
- **Resilience**: dev servers come back after a reboot (`ds restore`), a
  memory guard (earlyoom) stops a runaway process before the machine stalls,
  and the data disk is snapshotted every night.
- **Agent support**: Claude Code knows the machine, rings the terminal bell
  when it finishes or needs input, can push to a phone through ntfy, and reads
  screenshots sent from the laptop.

## Requirements

On the operator's machine:

- An AWS account, and an AWS CLI profile for it
- The AWS CLI and its Session Manager plugin
- [OpenTofu](https://opentofu.org) 1.8 or later
- [just](https://just.systems) and jq

Also a Tailscale account: the devbox joins the developer's tailnet.

On the developer's laptop: Tailscale, an SSH client, and optionally Cursor or
VS Code. See [docs/laptop-setup.md](docs/laptop-setup.md).

## Quick start (operator)

The full procedure, with rebuilds, updates and costs, is in
[docs/operator-guide.md](docs/operator-guide.md). The examples use a devbox
named `alex` for a developer whose Linux user name is also `alex`.

With [Claude Code](https://claude.com/claude-code), run `claude` in this
repository and ask it to set up a devbox: the `dsbox-setup` skill
(`.claude/skills/dsbox-setup/`) walks through these steps, runs the checks,
and stops for anything that costs money or needs a login.

1. **Describe the devbox.** Per-devbox values live in a private ops folder,
   `$DEVBOX_OPS`, set in the environment or in a `.env` file in the repo root.
   The default is `.ops/` in the repo, which is gitignored.

   ```sh
   mkdir -p .ops/envs
   cp infra/envs/example.tfvars .ops/envs/alex.tfvars   # then edit it
   ```

   The file name is the devbox name and its OpenTofu workspace. `owner` is
   the developer's Linux user name.

2. **Create it.**

   ```sh
   just infra-init          # once per checkout
   just infra-plan alex     # review the plan
   just infra-apply alex
   ```

3. **Provision it** from an SSM shell (`just vm-shell alex`):

   ```sh
   sudo -i
   git clone https://github.com/Jenish-1235/dsbox.git /root/dsbox
   /root/dsbox/provision/provision.sh --user alex --upgrade
   tailscale up --ssh --hostname=devbox     # log in to the developer's tailnet
   /root/dsbox/provision/provision.sh --only names
   passwd alex                              # the developer chooses it
   ```

4. **Configure Tailscale.** In the admin console, add the devbox's Tailscale
   address as a nameserver restricted to the apps domain (`devbox`), so
   `<project>.devbox` resolves on the developer's devices. `provision.sh`
   prints the exact values under "Needs a person".

5. **Install `ds`** as the developer (`sudo -iu alex`):

   ```sh
   git clone https://github.com/Jenish-1235/dsbox.git ~/.devbox/src
   ~/.devbox/src/install.sh
   ```

6. **Set up the laptop**: [docs/laptop-setup.md](docs/laptop-setup.md).

## Daily use

The developer's guide is [docs/user-guide.md](docs/user-guide.md). On the
devbox, `ds cheat` (or `http://devbox/` in a browser) shows the cheatsheet, and
`ds help` lists every command.

```sh
ds                          # projects and their links
ds new nextjs shop          # create from a template and start it
ds up | stop | logs -f | attach | url [name]
ds d                        # Docker by project
ds doctor                   # check the machine, with fixes
ds update / ds rollback     # get or undo devbox improvements
```

### Publishing

`ds publish` puts the current project on the developer's code forge, chosen by
`DEVBOX_FORGE`: each developer can set their own in `~/.devbox/config.env`,
otherwise the machine's `config/devbox.env` applies. With `github` (the
default) it creates a private GitHub repository with `gh` and pushes; nothing
is stored anywhere. With `gitlab` it creates a private GitLab project and can
also set up a GitLab push mirror to GitHub. The mirror stores the developer's
`gh` token on GitLab, and that token reaches every repository the login can; a
fine-grained token limited to Contents write on the mirror repository is the
safer choice. Details: [docs/internals.md](docs/internals.md#publishing-github-or-gitlab).

## Security model

- **No inbound ports.** The security group allows only Tailscale's WireGuard
  port. SSH, project links and databases are reachable only over the tailnet.
- **Tailscale decides who gets in.** The devbox joins the developer's tailnet
  with Tailscale SSH. The recommended SSH policy lets a member's own devices
  (`autogroup:self`) log in as non-root users only. dnsmasq answers on the
  Tailscale interface only.
- **SSM is the break-glass path.** The instance role has
  `AmazonSSMManagedInstanceCore`, so the operator can open a shell with their
  AWS credentials when Tailscale is down.
- **Containers can't use the instance role.** IMDSv2 is required with a hop
  limit of 1, so processes in Docker containers can't fetch the role's
  credentials.
- **`instance_admin` is off by default.** Turning it on gives the instance role
  `AdministratorAccess` on the account: anything running on the VM outside a
  container, agents included, can then manage the account.
- **Guard rails.** `expected_account_id` stops OpenTofu from running against
  the wrong account. The data disk has `prevent_destroy`, the instance has
  termination protection, and both disks are encrypted.
- **Secrets stay out of the repo.** tfvars and OpenTofu state live in the ops
  folder. `config/devbox.env` is public and world-readable on the machine, so
  it holds no secrets. CI runs gitleaks over the whole history.
- **Logins belong to the developer.** `gh`, `glab` and Claude Code use the
  developer's own accounts, stored in their home directory on the devbox.
- **Phone pushes carry no code.** A push is the project name and a fixed text.
  The ntfy topic is a random name kept in `~/.config/devbox/ntfy-topic`; anyone
  who knows it can read the pushes.
- **A GitHub mirror stores a token on GitLab.** With `DEVBOX_FORGE=gitlab` and
  a mirror, GitLab keeps the developer's `gh` token to push to GitHub. That
  token can reach every repository the `gh` login can; a fine-grained token
  limited to Contents write on the mirror repository is the safer choice.
- **The developer has sudo.** Agents run as the developer. Claude Code is told
  to ask before using `sudo`; that is an instruction, not a boundary.

To report a vulnerability, see [SECURITY.md](SECURITY.md).

## On the devbox

dsbox keeps the developer's side of the machine in `~/.devbox` (don't delete
it):

| Path | What |
|---|---|
| `src/` | This repository; `ds update` moves it |
| `bin/` | `ds` and helper scripts, on PATH |
| `state/` | What was applied, the previous `ds` for rollback (`*.prev`), the last update check, project ports (`ports.json`), running projects (`running.json`), Caddy routes |
| `backups/` | Copies of files an update replaced, one folder per run |
| `logs/` | Dev server output (`<project>.log`) and the notify log |
| `templates/` | The developer's own project templates for `ds new` |
| `adlc/` | A clone of [ADLC](https://github.com/Jenish-1235/adlc); its templates come first in `ds new` |
| `share/` | The cheatsheet and tips |
| `inbox/` | Screenshots sent from the laptop, kept 7 days |

Machine settings are in `/etc/devbox/devbox.env`, installed from
[config/devbox.env](config/devbox.env). How updates, components, project
detection, project links and notifications work is in
[docs/internals.md](docs/internals.md).

## Repository layout

| Path | What |
|---|---|
| `cmd/ds/` | The `ds` command |
| `internal/` | Packages behind `ds`: projects, Docker view, updates, doctor, web pages, notifications, publishing |
| `components/` | What `ds` installs and checks for the developer: `shell`, `tmux`, `toolchain`, `updates`, `web`, `restore`, `welcome`, `claude` |
| `config/devbox.env` | Non-secret machine settings |
| `provision/` | `provision.sh` (root) and `user.sh` (the developer's tools); see [provision/README.md](provision/README.md) |
| `infra/` | OpenTofu, one workspace per devbox; `envs/example.tfvars` is the template |
| `laptop/` | Screenshot paste helpers for Windows and macOS |
| `scripts/` | `vm-run.sh` (run a command on a devbox over SSM) and the shell lint list |
| `install.sh` | Clones the repo into `~/.devbox/src`, builds `ds` and applies the components |
| `docs/` | User, laptop, operator and internals guides |

## Development

```sh
just setup        # Go modules; reports missing golangci-lint, shellcheck, gitleaks
just check        # lint (gofmt, go vet, golangci-lint, shellcheck, gitleaks) and test
just test         # go test -race ./...
just build        # build into ./bin
just run doctor   # run ds from source against your own ~/.devbox
```

CI (`.github/workflows/ci.yml`) runs the same checks on every push to `main`
and on pull requests, plus `tofu fmt -check` and `tofu validate` on `infra/`.
See [CONTRIBUTING.md](CONTRIBUTING.md).

## Licence

MIT. See [LICENSE](LICENSE).
