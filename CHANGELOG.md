# Changelog

## Unreleased

- First public release of dsbox, under the MIT licence.
- `infra/`: OpenTofu for one devbox per workspace, with per-devbox values and
  state in a private ops folder (`$DEVBOX_OPS`, default `.ops/`).
- `just state-backup <env>` (also run by `just infra-apply`): copies the state
  to a private, versioned S3 bucket in the devbox's account, as Glacier
  Instant Retrieval.
- `provision/`: idempotent machine setup with `--check`; `--user` names the
  developer's account on a fresh machine.
- `ds`: projects from templates with stable ports and `<project>.devbox` links
  (HTTP and HTTPS), a Docker view (`ds d`), publishing to GitHub or GitLab
  (`DEVBOX_FORGE`, per developer in `~/.devbox/config.env`), update, rollback,
  doctor, status, config and cheatsheet.
- Restore of dev servers after a reboot, a memory guard (earlyoom), a daily
  update check that only prints a notice, and nightly data disk snapshots.
- Claude Code support: terminal bell and ntfy phone pushes when it finishes or
  waits, and screenshot paste helpers for Windows and macOS.
- Documentation: user, laptop, operator and internals guides.
