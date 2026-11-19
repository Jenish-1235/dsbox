# Contributing

## Setup

```sh
just setup     # Go modules; reports missing golangci-lint, shellcheck, gitleaks
just check     # lint and test: run before every push
```

`just` lists every task. CI runs the same checks, plus `tofu fmt -check` and
`tofu validate` on `infra/`.

To try a change on a real devbox, push a branch and run
`just vm-update <name> <branch>` (see [docs/operator-guide.md](docs/operator-guide.md)).

## Changes

- Commit messages: a short, plain summary of what changed and why, in the
  present tense. One topic per commit.
- Every user-visible change gets a line in `CHANGELOG.md` under
  `## Unreleased`. The file is append-only.
- A new feature the developer should know about gets a line in
  `components/welcome/files/tips.txt`; new terminals show it once, marked
  `new:`. Keep `components/welcome/files/cheatsheet.txt` and `ds help`
  current.
- Machine changes go in `provision/` and must stay idempotent: a second run on
  a set-up machine changes nothing, and `--check` reports without changing.
- No secrets, account IDs or personal values in the repository; per-devbox
  values belong in the ops folder.
- Shell scripts must pass shellcheck (`scripts/lint-shell.sh` lists them).
