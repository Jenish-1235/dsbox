# Using a devbox

A devbox is a Linux machine in AWS that does the heavy work: code, Docker,
builds and agents run there. The laptop only shows things: the terminal, the
editor and the browser. Closing the laptop doesn't stop anything running on the
devbox.

Quick reference: `ds cheat` in the terminal, or `http://devbox/` in the
browser. Setting up a laptop for the first time: [laptop-setup.md](laptop-setup.md).

## The machine

The operator chooses the size. The defaults:

| | |
|---|---|
| Size | 4 vCPU, 32 GiB RAM (`r7i.xlarge`, x86); `m7i.xlarge` has 16 GiB |
| Disk | 500 GB for home and Docker, snapshotted every night (7 kept), plus a 50 GB OS disk |
| OS | Ubuntu 24.04, security updates installed automatically every day |
| Reachable | Only from the developer's own Tailscale devices (laptop, phone). Nothing is open to the internet |

## Connecting

- **Terminal:** `ssh devbox`, or a terminal profile that runs
  `ssh -t devbox "tmux new -A -s main"` to open the tmux workspace directly.
- **Cursor or VS Code:** Remote-SSH to `devbox`. Files and processes stay on
  the devbox; the editor feels local.
- **Browser, Postman, database tools and phone:** project links (below).
  Tailscale has to be on.

## Projects: `ds`

| Command | |
|---|---|
| `ds` | projects and their links |
| `ds new` | pick a template and a name; the project is created and started |
| `ds new <template> <name>` | the same, without the questions: template copied, git repo, `just setup`, started, link printed |
| `ds templates` | the templates, for example `nextjs`, `react-vite`, `ddd-golang`, and `empty` |
| `ds up [name]` | start it (inside the project folder, no name needed) and print its link |
| `ds stop [name]` | stop it, and keep it stopped after a reboot |
| `ds logs [name] -f` | follow its output |
| `ds attach [name]` | open its window, e.g. to use Vite's shortcuts; `Ctrl-b d` leaves it running |
| `ds url [name]` | print its link |
| `ds restore` | start every project that was running before a reboot (runs by itself at boot) |
| `ds publish [name]` | put the project on GitHub or GitLab as a new private repository |

- Projects live in `~/projects`. Servers run in the background (tmux session
  `dev`) and keep running when the laptop is closed.
- Each project keeps the same port, so its link never changes.
- A project that didn't come from a template still works. `ds up` detects
  Node (pnpm, npm, bun or yarn), Vite, Next.js, Docker Compose, FastAPI,
  Django, Go and Rust. For anything else, put the start command in a `.devrc`
  file in the project, for example `pnpm dev --port $PORT --host 0.0.0.0`.
  The server must listen on `$PORT` and `0.0.0.0`.
- Projects started with `ds up` start again by themselves after a reboot;
  `ds stop` keeps one stopped.

## Links: no ports to remember

| Link | What |
|---|---|
| `https://<project>.devbox` | the app, in the browser, Postman or on a phone (`http://` works too) |
| `http://devbox/` | every project, running or stopped, plus the cheatsheet |
| `http://dbs.devbox/` | running databases, grouped by project, with connection strings to copy |
| `devbox:<port>` | databases and other Docker services, e.g. `devbox:5432` in DBeaver or TablePlus |

- Type the trailing `/` the first time (`shop.devbox/`) so the browser doesn't
  search for it.
- A project that isn't running answers with how to start it. An unknown name
  points to `http://devbox/`.
- HTTPS links need the devbox's certificate trusted once per laptop: `ds cert`
  shows how. Phones can keep using `http://`.

## Docker: `ds d`

`ds d` (or `ds docker`) shows containers grouped by project.

| Key | |
|---|---|
| `enter` | logs |
| `s` | a shell in the container |
| `r` / `x` / `u` | restart / stop / start |
| `c` | copy a database connection string, for DBeaver or `.env` |
| `P` | actions for the whole project |
| `/` | filter |
| `2` | disk use, with pruning |
| `?` | every key |

`lzd` (lazydocker) is the raw view of every container, with logs and stats.

## Templates and ADLC

Templates come from [ADLC](https://github.com/Jenish-1235/adlc), a set of
agent rules, language standards, skills and project templates. The devbox
keeps a copy in `~/.devbox/adlc`. Personal templates go in
`~/.devbox/templates/<name>/`; `ds new` copies them.

- Template projects have `just` recipes: `just setup`, `just dev`,
  `just test`, `just lint`, `just up` / `just down` (local databases).
  `just --list` shows them.
- Agents in a template project read `AGENTS.md`, which points to ADLC's
  policies, standards and skills, so they write code to the same standard.
- Database ports in compose files come from variables
  (`DB_PORT=5433 just up`), so projects don't clash.

## Publishing

`ds publish` in a project (or `ds publish <name>`) creates a private
repository and pushes the project to it. `DEVBOX_FORGE` decides where:
`github` (the default) uses the `gh` login and, if you're logged in to
GitLab, can also keep a copy there that every `git push` updates; `gitlab`
uses the `glab` login and can also mirror the project to GitHub. `ds mirrors`
shows whether the copies are in sync. To choose for yourself, put
`DEVBOX_FORGE=gitlab` (or `github`) in `~/.devbox/config.env`; `ds config`
shows the setting in effect. Log in to `gh` (and
`glab`, for GitLab) once first: see [laptop-setup.md](laptop-setup.md#7-logins-on-the-devbox).

## Claude Code

- `claude` knows this machine. It runs apps with `ds up` and replies with the
  link, keeps servers listening where the laptop can reach them, follows ADLC
  in template projects, and asks before using `sudo`.
- `~/.claude/CLAUDE.md` belongs to the developer. The devbox only adds one
  import line for its own guide.
- When Claude finishes or waits for input, the terminal bell rings, from any
  tmux window.
- `ds notify setup` adds a phone push through the ntfy app. A push contains the
  project name and a fixed text, never code or prompts, at most once per 30
  seconds per project. `ds notify off` stops pushes; the bell stays.
- Screenshots: a helper on the laptop uploads the clipboard image and copies
  its path, which Claude can read. `ds inbox` lists what arrived; files are
  kept 7 days. Set-up: [laptop-setup.md](laptop-setup.md#9-screenshots-for-claude-code).

## Terminal

| Keys | |
|---|---|
| `t` | the tmux workspace; `Ctrl-b d` leaves it running |
| `Ctrl-b \|` / `Ctrl-b -` | split the screen |
| `Ctrl-R` | search every command run before |
| type + `↑` | find earlier commands that start with what was typed |
| `z <folder>` | jump to a folder visited before |
| `p` | go to `~/projects` |
| `lg` | git (lazygit) |
| `lzd` | Docker (lazydocker): containers, logs, stats |
| `ports` | what's listening, and which program |
| `ll`, `lt`, `gs`, `gl`, `dc`, `dps` | file list, tree, git status, git log, docker compose, docker ps |

New terminals show one status line (RAM, disk, uptime, running apps with links)
and a tip. Tips about new features show once, marked `new:`.

Personal shell changes go in `~/.zshrc.local` and `~/.tmux.conf.local`.
Updates never touch those two files.

## Tools installed

Node (LTS) with pnpm and bun, Python 3.13 with uv, Go, Rust, Docker with
Compose and Buildx, just, gitleaks, golangci-lint, git, glab, gh, Claude Code,
lazygit, lazydocker, ripgrep, fd, fzf, jq, btop and tmux. Language versions
come from mise; a project can pin its own in `mise.toml`.

## Keeping it healthy

| Command | |
|---|---|
| `ds doctor` | checks everything (tools, files, disk, swap, Docker, Tailscale, logins, background jobs) and says how to fix each problem; `--fix` reapplies the devbox files |
| `ds update` | installs the latest devbox improvements. New terminals show a notice when one is ready |
| `ds rollback` | undoes the last update |
| `ds status` | version, updates and health in a few lines |
| `ds config` | the machine settings |
| `ds diff` | how the devbox-managed files differ from the shipped versions |

Running in the background:

- **daily update check:** a notice only; nothing installs on its own;
- **security updates** from Ubuntu, every day;
- **nightly disk snapshots;**
- **project restore** after a reboot;
- **memory guard:** when memory runs out, earlyoom stops the biggest dev server
  or compiler instead of letting the machine freeze; `ds doctor` reports it;
- **the project-link services:** `devbox-web` (Caddy) and `devbox-dns`;
- **Docker,** with container logs capped so they can't fill the disk.

## Where things live

| Path | |
|---|---|
| `~/projects` | projects |
| `~/.devbox` | everything that runs the devbox: `ds`, the ADLC copy, state, logs, backups. **Don't delete it.** |
| `~/.devbox/backups` | copies of any file an update replaced |
| `~/.devbox/templates` | personal project templates |
| `~/.devbox/inbox` | screenshots from the laptop |
| `/etc/devbox/devbox.env` | machine settings (`ds config` shows them) |
