---
name: dsbox-setup
description: Set up a dsbox devbox end to end, or one part of it. Use when someone wants to create a devbox on AWS, provision it, install ds for a developer, connect a laptop (Tailscale, SSH, editor, certificate, screenshots, alerts), or check why one of those steps isn't working. Run from a checkout of the dsbox repository.
---

# Set up a devbox

Walk the person through creating and connecting a devbox. The docs are the
source of truth; this skill says what to run, what to check, and where to
stop for the person. Read the doc section for a step before running it:

- `docs/operator-guide.md`: requirements, the ops folder, tfvars, apply,
  provision, Tailscale admin, installing ds, costs.
- `docs/laptop-setup.md`: the developer's laptop and logins.
- `docs/user-guide.md`: daily use, for questions after setup.

## Ground rules

- **Never put real values in this repository.** Account IDs, profiles,
  emails, names and state go in the ops folder (`$DEVBOX_OPS`, set in `.env`
  at the repo root; default `.ops/`, gitignored). Run `git status` before
  finishing and make sure nothing private is staged or untracked outside it.
- **Stop for the person** before anything that costs money, changes access,
  or needs their eyes:
  - `just infra-apply` (creates billable resources; see Costs in the operator
    guide). Show the plan summary and wait for a clear yes.
  - Logins and links: `tailscale up`, `gh auth login`, `glab auth login`,
    `claude` login. Give the exact command; the person opens the link.
  - `passwd <user>`: the developer chooses their own password.
  - The Tailscale admin console (key expiry, split DNS, the SSH rule).
  - Trusting the devbox certificate on a laptop.
- **Check before writing.** Read an existing `~/.ssh/config`, tfvars or
  `.env` before adding to it, and never overwrite one.
- Use the `just` recipes; they read the instance, profile, region and user
  from the workspace's OpenTofu outputs. Commands below use `<name>` for the
  devbox (its tfvars file name and OpenTofu workspace) and `<user>` for the
  developer's Linux user name.

## First, find out what's needed

Ask, if it isn't clear: are they the **operator** (creating a devbox), the
**developer** (connecting a laptop to a devbox someone set up), or both? Then
check where things stand and skip what's done:

```sh
cat .env 2>/dev/null; ls "${DEVBOX_OPS:-.ops}/envs" 2>/dev/null   # existing devboxes
just vm <name> 'ds doctor'                                         # an existing devbox's health
```

## Operator

### 1. Requirements

```sh
aws --version && session-manager-plugin --version && tofu version && just --version && jq --version
aws sts get-caller-identity --profile <profile>
```

Missing tools on macOS: `brew install awscli session-manager-plugin opentofu just jq`.
OpenTofu must be 1.8 or later. The account from `get-caller-identity` is the
`expected_account_id`. One AWS account per devbox is recommended.

### 2. Ops folder and tfvars

If there's no `.env`, ask where the private ops folder should live (a private
git repository is a good home; state must be backed up), then write
`DEVBOX_OPS=<path>` to `.env`. Copy `infra/envs/example.tfvars` to
`$DEVBOX_OPS/envs/<name>.tfvars` and fill it with the person's answers: region,
profile, account ID (from step 1), the developer's Linux user name (`owner`),
the budget alert email, timezone. Mention the instance size choice
(`r7i.xlarge` 32 GiB default, `m7i.xlarge` 16 GiB) and its cost.

### 3. Apply

```sh
just infra-init           # once per checkout
just infra-plan <name>
```

Summarise the plan (what is created; it should be only additions for a new
devbox) and wait for approval. Then `just infra-apply <name>`. It also backs up
the state to S3 in the devbox's account (`just state-backup <name>`).

### 4. Provision

Wait until the instance registers with SSM (a few minutes after apply):

```sh
cd infra && tofu workspace select <name> && tofu output -json | jq -r '.instance_id.value, .aws_profile.value, .region.value'
aws ssm describe-instance-information --profile <profile> --region <region> \
  --filters Key=InstanceIds,Values=<instance id> --query 'InstanceInformationList[].PingStatus' --output text   # Online
```

Run provisioning as root over SSM. It takes several minutes, and SSM keeps
only the start of long output, so log to a file and print the end:

```sh
DEVBOX_ENV=<name> scripts/vm-run.sh --root 'git clone --quiet https://github.com/Jenish-1235/dsbox.git /root/dsbox 2>/dev/null || git -C /root/dsbox pull --quiet; /root/dsbox/provision/provision.sh --user <user> --upgrade >/var/log/devbox-provision.log 2>&1; rc=$?; tail -40 /var/log/devbox-provision.log; exit $rc'
```

If the devboxes should follow a fork, clone the fork's URL here and in step 6
instead (see "Machine settings and forks" in the operator guide).

### 5. Person steps on the devbox

`tailscale up` waits for a browser login, so it can't run over `vm-run.sh`.
Give the person these to run in `just vm-shell <name>`:

```sh
sudo -i
tailscale up --ssh --hostname=devbox      # open the link with the developer's Tailscale account
/root/dsbox/provision/provision.sh --only names
passwd <user>                             # the developer chooses it
```

Then the admin console steps from "4. Tailscale" in the operator guide. Get
the split DNS address for them with
`DEVBOX_ENV=<name> scripts/vm-run.sh --root 'tailscale ip -4'`, and confirm
afterwards with `DEVBOX_ENV=<name> scripts/vm-run.sh --root 'tailscale status; tailscale dns status'`.

### 6. Install ds for the developer

```sh
just vm <name> 'git clone --quiet https://github.com/Jenish-1235/dsbox.git ~/.devbox/src && ~/.devbox/src/install.sh'
just vm <name> 'ds doctor'
```

`ds publish` creates repositories on GitHub by default. For GitLab:
`just vm <name> 'echo DEVBOX_FORGE=gitlab >> ~/.devbox/config.env'`.
Doctor will still list the developer's logins; those are theirs to make.

Hand over to the developer: the tailnet machine name (`devbox`), their Linux
user name, the apps domain (`devbox`), and `docs/laptop-setup.md`.

## Developer laptop

Follow `docs/laptop-setup.md` in order, on the laptop. Run the checks
yourself; leave logins and admin dialogs to the person.

| Step | Check it worked |
|---|---|
| Tailscale | `ping devbox` replies |
| Project names | macOS `dscacheutil -q host -a name x.devbox`, Windows `Resolve-DnsName x.devbox`, Linux `getent hosts x.devbox`: the devbox's `100.x` address. If not, the split DNS entry is missing |
| SSH | `ssh devbox 'whoami; hostname'` prints the user and `devbox` |
| Certificate | the person opens `https://devbox/` and sees no warning (restart the browser first) |
| Logins on the devbox | `ssh devbox 'ds doctor'` lists any still missing |
| First project | `ssh devbox 'ds new nextjs hello'`, then open `https://hello.devbox` |

## When something fails

- `ds doctor` on the devbox names the failing part and the fix.
- `just provision-check <name>` shows what `provision.sh` would change.
- No SSH or Tailscale: `just vm-shell <name>` still works over SSM.
- Doctor's notes on fixing each part, and break-glass access, are in the
  operator guide.
