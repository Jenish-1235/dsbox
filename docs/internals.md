# How dsbox works

Reference for people changing dsbox or debugging a devbox. The developer's
guide is [user-guide.md](user-guide.md); the operator's is
[operator-guide.md](operator-guide.md).

## Install, update and rollback

`install.sh` runs as the developer. It clones the repository into
`~/.devbox/src` (or uses the clone already there; `DEVBOX_REPO` overrides the
URL), builds `ds` into `~/.devbox/bin` with Go (from mise if Go isn't on PATH),
and runs `ds apply`, which installs every component. It is safe to rerun.

`ds update [ref]`:

1. fetches `~/.devbox/src`;
2. moves it to `ref` (a branch, tag or commit), or by default to what
   `DEVBOX_CHANNEL` points at: `main` (every change) or `release` (the newest
   `v*` tag);
3. rebuilds `ds` when the version changed, keeping the previous binary as
   `~/.devbox/state/*.prev`;
4. applies every component.

If the build or install fails, the source goes back to the commit the
installed binary was built from, so source and binary never disagree.
`ds update` refuses to run when files in `~/.devbox/src` were changed by hand;
commit or discard them first. `ds rollback` returns to the version before the
last update or rollback, reusing the kept binary.

From the operator's machine, `just vm-update <name> [ref]` runs `ds update` on
a devbox, which is the way to try a branch on a real machine.

## Update notices

The `updates` component installs a systemd user timer that runs once a day and
shortly after boot. Its service runs `ds check-updates`, which fetches the
repository and writes `~/.devbox/state/updates.json`; it never installs
anything. The same run deletes screenshots in `~/.devbox/inbox` older than
7 days.

Every interactive shell runs `ds notices`, which reads only local files and
prints at most two lines, for example:

```
↑ devbox update available (3 changes) → ds update
```

It is silent when there is nothing to say. `ds status` reuses the cached
result when it is less than a day old. `ds doctor` checks that the timer is on
and that its last run worked. The timer needs lingering
(`provision.sh --only linger`) to run while the developer is logged out. Logs:
`journalctl --user -u devbox-check-updates`.

## Components

Each folder in `components/` is one part of the developer's setup, described by
`component.yaml`:

```yaml
name: tmux
description: what it gives the developer
requires:                 # commands that must exist (ds doctor checks)
  - tmux
  - { cmd: just, install: mise use -g just@latest }
files:                    # installed by ds update; edits are backed up first
  - { src: files/tmux.conf, dest: ~/.tmux.conf }
checks:                   # bash that exits 0 when healthy
  - { name: ..., run: ..., fix: ... }
after_apply:              # bash run after any of its files changed, in order
  - systemctl --user daemon-reload
os: linux                 # optional: skip the component elsewhere (e.g. a Mac)
```

| Component | What it does |
|---|---|
| `shell` | zsh with the starship prompt, history search, completions and aliases |
| `tmux` | tmux with mouse support, splits in the current folder, and bells passed on to the laptop |
| `toolchain` | Checks the languages, build tools and CLIs every project expects |
| `updates` | The daily update check |
| `web` | The pages behind `http://devbox` (projects, cheatsheet) and `http://dbs.devbox` (databases) |
| `restore` | Starts the projects that were running before a reboot |
| `welcome` | The status line in new terminals, tips, and the cheatsheet |
| `claude` | Claude Code instructions for the machine, and its bell and phone hooks |

Before `ds` replaces a file, it copies the old one to
`~/.devbox/backups/<timestamp>/`, one folder per run; `ds diff` shows how the installed files differ from the managed
versions. A failed `after_apply` command is a warning, not a failed update: the
files stay in place, and the next `ds update` or `ds doctor --fix` runs the
commands again.

Managed files load a `.local` sibling (`~/.zshrc.local`, `~/.tmux.conf.local`)
for personal changes that updates never touch.

`systemctl --user` works from `sudo -iu <user>` and from SSM sessions too: `ds`
fills in `XDG_RUNTIME_DIR` and the user bus address when they are missing.

## Machine settings

`config/devbox.env` holds the non-secret machine settings: host name, apps
domain, HTTPS, the port range for dev servers, the update channel and the code
forge. `provision.sh --only config` installs it as `/etc/devbox/devbox.env`,
which `~/.zshenv` and the devbox services load. It is in git and readable by
everyone on the machine, so it holds no secrets.

The format is plain `KEY=value` with `#` comments: no quotes, spaces or `$`, so
systemd and the shell read it the same way. `ds config` shows the settings,
flags invalid ones, and says when `/etc` is behind the repository.

The file is the same for every devbox that installs from the same repository.
`DEVBOX_USER` and `DEVBOX_PROJECTS_DIR` can be overridden per machine in the
environment; the developer's account comes from `provision.sh --user` and
projects default to `~/projects`.

## Projects

`ds` runs projects as dev servers in the tmux session `dev`, one window per
project. Each project gets a stable port from `DEVBOX_PORT_MIN` to
`DEVBOX_PORT_MAX`, saved in `~/.devbox/state/ports.json`. Servers get `PORT`,
`HOST=0.0.0.0` and `DEV_ALLOWED_HOSTS` in their environment.

How a project starts, first match wins:

1. a `.devrc` file (its first non-comment line);
2. a `dev` recipe in the `justfile`;
3. a `dev` script in `package.json` (pnpm, bun, yarn or npm by lockfile; Vite
   gets `--port --host --strictPort`);
4. `compose.yaml`;
5. FastAPI (`uv run fastapi dev`);
6. `manage.py` (Django);
7. `go.mod`;
8. `Cargo.toml`.

`ds ls --brief` prints `<name>\t<url>` per running project and nothing
otherwise. It only asks tmux, so shells can call it at start.
`DEVBOX_PROJECTS_DIR` and `DEVBOX_HOME` in the environment override the machine
settings (the tests use this).

`ds new` lists templates from the ADLC clone (`~/.devbox/adlc/templates`)
first, then the developer's own in `~/.devbox/templates`; a name in both places
is the ADLC one. ADLC templates are created with ADLC's own `just new`; the
developer's are copied. `empty` creates a project with only git, a README and
ADLC's agent rules and skills. `ds new` then commits, runs `just setup`, and
starts the project.

## Project links, HTTP and HTTPS

With `DEVBOX_APPS_DOMAIN=devbox`, provision step `names` runs dnsmasq on the
Tailscale interface only (`devbox-dns`), answering `*.devbox` with the devbox's
Tailscale address, and Caddy on ports 80 and 443 as the developer
(`devbox-web`). `ds` writes the routes to `~/.devbox/state/caddy/Caddyfile`.

The tailnet must send lookups for the apps domain to the devbox (split DNS):
see [operator-guide.md](operator-guide.md#4-tailscale).

With `DEVBOX_APPS_TLS=internal`, every project, `devbox` and `dbs.devbox` also
answer on HTTPS, signed by Caddy's own CA (`tls internal`, kept in
`~/.local/share/caddy`). HTTP keeps working and is not redirected, for phones.
`ds cert` prints the root certificate's path and the one-time import for
Windows, macOS and Firefox; `http://devbox/root.crt` serves it. Websockets
(Vite and Next.js live reload) pass through Caddy on both.

## After a reboot

`ds up` records the project in `~/.devbox/state/running.json`; `ds stop`
removes it. The `restore` component's user service runs `ds restore` at boot,
after waiting up to 60 seconds for Docker: it starts every recorded project
that isn't running and prints nothing when there's nothing to do. Logs:
`journalctl --user -u devbox-restore`. Docker Compose services come back on
their own when they have `restart: unless-stopped`.

## Memory guard

Provision step `memguard` installs earlyoom (`/etc/default/earlyoom`). At 4%
free memory and 10% free swap it stops the process with the highest score,
preferring dev servers and compilers (node, next-server, vite, esbuild, tsc,
go, cargo, rustc, python, java) and avoiding system services, Docker, Caddy,
tmux and `ds`. `ds doctor` says when it stopped something in the last 24 hours.

## Claude Code: bell, phone pushes and screenshots

The `claude` component installs `~/.claude/devbox.md`, which tells Claude Code
how the machine works, and adds one import line for it to the developer's
`~/.claude/CLAUDE.md`. It also merges two hooks into `~/.claude/settings.json`,
for `Stop` and for `Notification` (permission and input prompts), with jq
(`devbox-claude-hooks`); the developer's other settings and hooks stay.

- `ds notify bell` returns `{"terminalSequence":"\u0007"}`, so Claude Code
  rings the bell itself (hooks have no terminal). tmux passes bells on from any
  window (`bell-action any`), so the laptop's terminal can flash or mark the
  tab.
- `ds notify hook` (asynchronous) pushes to the phone through ntfy, but only
  once `ds notify setup` has saved a private topic in
  `~/.config/devbox/ntfy-topic`. A push is the project name and a fixed text,
  never code or prompts; at most one per 30 seconds per project, with a
  5-second timeout. Failures go to `~/.devbox/logs/notify.log`.
  `DEVBOX_NTFY_SERVER` points at a self-hosted ntfy server instead of ntfy.sh.

Screenshots: Claude Code over SSH can't read the laptop's clipboard, but it
reads image paths. `laptop/windows/devbox-paste.ps1` (Ctrl+Alt+V, set up by
`laptop/windows/install.ps1`) and `laptop/mac/devbox-paste.sh` upload the
clipboard image to `~/.devbox/inbox/` with scp and copy its full path. `ds
inbox` lists them; the daily update check and `ds inbox` delete those older
than 7 days.

## Troubleshooting

| Symptom | Where to look |
|---|---|
| Anything | `ds doctor`, then `ds doctor --fix` to reapply components |
| `<project>.devbox` doesn't resolve | `tailscale dns status` on the devbox should list the apps domain; `systemctl status devbox-dns` |
| Link resolves but doesn't answer | `journalctl -u devbox-web -n 20`; `ds logs <project>` |
| `ds cert` says there is no certificate | Caddy creates it when it first serves HTTPS: `journalctl -u devbox-web -n 20` |
| Projects didn't come back after a reboot | `journalctl --user -u devbox-restore` |
| No update notices | `journalctl --user -u devbox-check-updates`; lingering must be on (`provision.sh --only linger`) |
| `ds update` refuses | Local changes in `~/.devbox/src`: commit or discard them |
| A process was killed | earlyoom; `ds doctor` reports it |
| Phone pushes don't arrive | `ds notify` shows whether they're on; `~/.devbox/logs/notify.log` |

## Publishing: GitHub or GitLab

`DEVBOX_FORGE` (the developer's own in `~/.devbox/config.env`, else the machine's in `config/devbox.env`) picks where `ds publish` creates a
project that has no `origin` yet: `github` (the default, also when unset) or
`gitlab`. Both use the developer's own logins (`gh`, `glab`); `ds doctor`
checks the ones the forge needs.

```sh
ds publish [name]          # push to origin; with none, create a private repo on the forge first
ds publish --github        # gitlab: mirror to GitHub without asking (--no-github: never)
ds publish --gitlab        # github: keep a copy on GitLab without asking (--no-gitlab: never)
ds mirrors                 # every GitHub mirror or GitLab copy, whether it's in sync, and a fix
```

`ds publish` works on the project you're in (or `name`). Whatever the forge,
an `origin` on github.com or on GitLab gets pushed to (every branch and tag),
as long as that forge's CLI is logged in; an `origin` anywhere else stops it
with how to move it.

With `github`:

- No `origin`: creates a private repo `<gh user>/<name>` (or reuses an empty
  one of that name; one with branches stops it), adds it as `origin` in the
  protocol `gh` is set to use, and pushes every branch and tag; `main`
  becomes the default branch. Then, if `glab` is logged in, it asks "Copy to
  GitLab? (y/N)". `--github` and `--no-github` are refused.
- Copy on GitLab: gitlab.com's free plan can't pull from GitHub, so the copy
  is kept by `git push`. `ds publish` creates a private project
  `<glab user>/<repo name>` described as `Copy of https://github.com/<repo>`
  (an existing one is used only if it's this repo's copy or is empty with no
  description), pushes every branch and tag to it, and adds it as a second
  push URL of `origin` (after GitHub's own). From then on every `git push`
  from that clone updates both, and `ds publish` pushes to both. Changes made
  on GitHub itself (a merged pull request, an edit on the website) reach GitLab
  with the next `git pull` and `git push`. No token is stored; it uses the
  `glab` login. To stop: `git remote set-url --delete --push origin <gitlab URL>`.

With `gitlab`, GitLab is the source of truth and GitHub an optional read-only
mirror:

- No `origin`: creates a private project in the developer's GitLab namespace (or reuses
  an empty one of that name; one with commits stops it), adds it as `origin`
  and pushes every branch and tag; `main` becomes the default branch. Then it
  asks "Mirror to GitHub? (y/N)" (not when `origin` is on GitHub already).
- Mirror: a private GitHub repo `<gh user>/<name>` (issues and wiki off,
  described as a mirror; an existing repo is used only if it's this
  project's mirror or has no branches), plus a GitLab push mirror of every
  branch with divergent refs overwritten. The mirror authenticates with the
  `gh` login's token (`gh auth token`), which only ever travels in the API
  request body. GitLab stores that token with the mirror, and it can reach
  every repo the developer's `gh` login can (scopes `repo`, `read:org` and often
  `workflow`); a fine-grained GitHub token limited to Contents write on the
  mirror repo is the safer choice. `ds publish` then syncs and waits up to 90s until GitHub has every local branch at
  its local commit (GitLab may run a requested sync minutes later, so the
  branches decide, not the mirror's timestamps).
- Reruns reuse the project, repo and mirror; a failed mirror is replaced
  with the current token, so `gh auth login` then `ds publish --github` fixes
  a rejected token. Without a terminal and without a flag it doesn't mirror.

gitlab.com allows a burst of 100 API requests per user, so `ds mirrors` doesn't
ask about every project: it lists the developer's GitHub repos (`gh`) and checks the GitLab
projects with a GitHub repo of the same name, a GitHub repo described as
`Mirror of <its GitLab URL>`, or a mirror seen before. A mirror to a renamed
repo, or on a group's project, is found once it's been seen (e.g. after
`ds publish --github`). With no GitHub repos (or no `gh` login) and no mirror
seen before, it doesn't ask GitLab at all.

The daily `devbox-check-updates` service also runs `ds check-mirrors`, which
writes `~/.devbox/state/mirrors.json`; `ds notices` then prints e.g.
`! mirror shop → GitHub failing since 2d → ds mirrors`. `ds publish` and
`ds mirrors` refresh the file too, so a fixed mirror stops the notice at once.
With `github`, `ds mirrors` and `ds check-mirrors` instead list the
developer's GitLab projects described as copies and compare their branches
with GitHub's (no `glab` login: nothing to check). A copy that differs gives
`! GitLab copy of shop differs from GitHub since 2d → ds mirrors`; `git pull`
then `git push` in the project fixes it. `ds doctor` checks the `glab` login
once a copy exists, and before that only says it's needed for copies.
