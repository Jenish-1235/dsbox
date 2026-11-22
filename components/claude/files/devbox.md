# This machine: devbox

You're running on the developer's remote dev machine (Ubuntu 24.04, Docker). They work from a laptop over a private network (Tailscale): they read your terminal output, open apps in the browser at the URL `ds up` prints (`https://<project>.devbox`; `http://` works too), and point Postman and database tools at `devbox:<port>`.

## Running apps

- Start a project's dev server with `ds up` in the project folder. It runs in the background (tmux), keeps a stable port per project, waits until the app answers, and prints the URL. Finish your reply with that URL so they can click it.
- `ds ls` lists projects and their URLs, `ds logs <name>` shows output, `ds stop <name>` stops one. Don't stop servers you didn't start.
- Never run a dev server in the foreground; it blocks you. Never bind to localhost only: servers must listen on 0.0.0.0 so the laptop can reach them. That's safe here, because only the developer's private network can reach this machine.
- Local services (databases, queues, observability tools) go in the project's Docker Compose file, with host ports from variables (`"${DB_PORT:-5432}:5432"`) so projects don't clash, and `restart: unless-stopped` so they come back after a reboot (dev servers started with `ds up` do too). Tell them the address to use from their laptop, e.g. `devbox:5433` (http://dbs.devbox lists running databases with connection strings).

## Projects

- Projects live in `~/projects`. Start new ones with `ds new <template> <name>` (`ds templates` lists them) instead of hand-rolling a setup.
- Projects made from templates follow ADLC. Read `AGENTS.md` (it points to `.adlc/AGENTS.md`) and follow its policies, language standards and skills. Use the project's `just` recipes (`just --list`): `setup`, `dev`, `test`, `lint`, `up`, `down`.
- You're not done until `just lint` and `just test` pass.

## Artifacts

- Finished artifacts meant for reading (design docs, reports, dashboards, diagrams) are built as one self-contained HTML file: inline CSS and JS, or CDN links pinned to an exact version.
- Submit with `ds artifact submit <file.html> --title "…"` (in the project folder; `--slug` names it, default the file name). Re-submitting the same name replaces it. `https://artifacts.devbox` lists every project's artifacts.
- Finish your reply with the URL it prints.
- Keep source files (e.g. Markdown) in the project repo as usual; the HTML is what the developer reads.

## Screenshots and notifications

- Screenshots the developer sends from the laptop land in `~/.devbox/inbox/` (`shot-<date>-<time>.png`, kept 7 days). They usually paste the full path; when they say "my screenshot" or "the screenshot" without one, read the newest file there (`ds inbox` lists them, newest first).
- A hook rings their terminal bell when you finish or need them, and pushes to their phone if they ran `ds notify setup`. Don't add your own notification hooks or edit devbox's in `~/.claude/settings.json`.

## Machine rules

- Ask before using `sudo`.
- Don't edit anything in `~/.devbox`: `ds` manages it and overwrites changes. If the machine itself needs changing, say so instead of working around it.
- `ds doctor` checks the machine's health.
- Secrets never go in git or the shell environment. Use the project's gitignored `.env`.
