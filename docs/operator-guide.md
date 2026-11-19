# Operator guide

The operator creates devboxes, provisions them, and ships updates to them. This
guide covers the whole lifecycle. The developer's side is in
[user-guide.md](user-guide.md) and [laptop-setup.md](laptop-setup.md).

Examples use a devbox named `alex` for a developer whose Linux user name is
also `alex`.

## Requirements

- An AWS account, and an AWS CLI profile for it. One account per devbox keeps
  costs and permissions apart; the budget alert covers the whole account.
- The AWS CLI and its Session Manager plugin.
- OpenTofu 1.8 or later, just, and jq.
- Access to the Tailscale admin console of the developer's tailnet.

## The ops folder

Per-devbox values never go in this repository. They live in a private ops
folder, `$DEVBOX_OPS`, set in the environment or in a `.env` file in the repo
root (`just` loads it). The default is `.ops/` in the repo, which is
gitignored.

```
$DEVBOX_OPS/
  envs/<name>.tfvars    one per devbox (copy infra/envs/example.tfvars)
  state/                OpenTofu local state, one workspace per devbox
  <name>.tfplan         the plan saved by just infra-plan
```

The state is the only record of what OpenTofu created. Keep the folder
somewhere private and backed up, for example a private git repository.

## Creating a devbox

### 1. Write the tfvars

```sh
mkdir -p .ops/envs        # or "$DEVBOX_OPS/envs"
cp infra/envs/example.tfvars .ops/envs/alex.tfvars
```

The file name is the devbox name, which is also the OpenTofu workspace.

| Variable | Default | |
|---|---|---|
| `region` | required | Any AWS region |
| `aws_profile` | required | AWS CLI profile for the devbox's account |
| `expected_account_id` | required | OpenTofu refuses to run against any other account |
| `owner` | required | The developer's Linux user name (`provision.sh --user`); used in tags and resource names |
| `alert_email` | required | Where budget alerts go |
| `hostname` | `devbox` | Linux hostname, and the Tailscale machine name |
| `instance_type` | `r7i.xlarge` | 4 vCPU, 32 GiB. `m7i.xlarge` is 4 vCPU, 16 GiB. Changing it is a stop and start, with no data loss |
| `availability_zone` | the region's `a` zone | The instance and data disk must share it |
| `timezone` | `UTC` | Set on first boot |
| `root_volume_gb` | `50` | OS disk; disposable |
| `data_volume_gb` | `500` | `/home`, Docker and containerd; can be grown later |
| `snapshot_time_utc` | `20:30` | Daily data disk snapshot, `HH:MM` UTC; pick a time the developer is usually offline |
| `snapshot_retain_days` | `7` | Snapshots kept |
| `monthly_budget_usd` | `250` | Budget alert threshold for the account (80% actual, 100% forecast) |
| `instance_admin` | `false` | Gives the instance role `AdministratorAccess` on the account. See the security model in the [README](../README.md#security-model) |

### 2. Apply

```sh
just infra-init           # once per checkout: points OpenTofu at $DEVBOX_OPS/state
just infra-plan alex      # selects or creates the workspace, saves a plan
just infra-apply alex     # applies that saved plan
```

This creates the instance (latest Ubuntu 24.04 AMI, IMDSv2 only, termination
protection on), the encrypted data disk, its daily snapshot policy, the
security group (only Tailscale's UDP port 41641 inbound), the instance role
with SSM, and the budget. On first boot cloud-init sets the hostname and
mounts the data disk at `/data`, with `/home`, `/var/lib/docker` and
`/var/lib/containerd` bind-mounted onto it. It never formats a disk that
already has a filesystem.

New AMIs and changes to the cloud-init template don't replace or reboot an
existing instance; OpenTofu ignores them.

### 3. Provision

A few minutes after the apply, the instance registers with SSM. Open a shell
and become root:

```sh
just vm-shell alex
sudo -i
```

Then:

```sh
git clone https://github.com/Jenish-1235/dsbox.git /root/dsbox
/root/dsbox/provision/provision.sh --user alex --upgrade
```

`--user` is required on a fresh machine; later runs default to the one
account that has `~/.devbox`. The script creates the account, installs
packages, Docker, Tailscale, the memory guard and `/etc/devbox/devbox.env`,
and runs `provision/user.sh` as the developer (mise and its tools, Rust,
Claude Code, the ADLC clone). Steps and options are in
[provision/README.md](../provision/README.md); `--help` lists the steps.

Log the devbox in to the developer's tailnet, then set up the project names,
which need its Tailscale address:

```sh
tailscale up --ssh --hostname=devbox     # open the link it prints
/root/dsbox/provision/provision.sh --only names
```

The developer's account has no password, and sudo needs one. Have the
developer choose it:

```sh
passwd alex
```

`provision.sh` ends with a "Needs a person" list of anything still
outstanding.

### 4. Tailscale

In the Tailscale admin console of the tailnet the devbox joined:

- **Machines → devbox → Disable key expiry**, so the devbox doesn't drop off
  the tailnet after the key's lifetime.
- **DNS → Nameservers → Add nameserver → Custom:** the devbox's Tailscale
  address (`tailscale ip -4` on the devbox), with **Restrict to domain**
  turned on and the domain set to the apps domain (`devbox` by default). This
  is split DNS: only `*.devbox` lookups go to the devbox. `provision.sh`
  prints the exact values, and `tailscale dns status` on the devbox lists the
  entry once it is in place.
- **Access controls:** Tailscale SSH needs an `ssh` rule. This one lets each
  member log in to their own machines as a non-root user without a periodic
  browser re-check:

  ```json
  "ssh": [
    {
      "action": "accept",
      "src":    ["autogroup:member"],
      "dst":    ["autogroup:self"],
      "users":  ["autogroup:nonroot"]
    }
  ]
  ```

  `autogroup:self` only matches devices owned by the same user, so the
  devbox has to be logged in with the developer's Tailscale account. Use
  `"action": "check"` to require a browser re-check instead.

### 5. Install ds for the developer

As the developer (`sudo -iu alex` from the SSM shell):

```sh
git clone https://github.com/Jenish-1235/dsbox.git ~/.devbox/src
~/.devbox/src/install.sh
```

`install.sh` builds `ds` and applies every component. `ds doctor` then checks
the whole machine. Logins (`gh`, `glab`, Claude Code) are the developer's to
make; see [laptop-setup.md](laptop-setup.md).

## Working with a devbox

From the repo on the operator's machine. Each recipe reads the instance,
profile, region and developer from the workspace's OpenTofu outputs.

| Command | |
|---|---|
| `just vm alex '<cmd>'` | Run a command as the developer, in a login zsh, over SSM, and print its output |
| `just vm-shell alex` | Interactive shell over SSM |
| `just provision-check alex` | Show what `provision.sh` would change on the devbox, using this checkout's `provision/` and `config/` |
| `just vm-update alex [ref]` | Run `ds update [ref]` on the devbox, e.g. to try a branch |

After changing provisioning or `config/devbox.env`, rerun it on the devbox:
`sudo ~/.devbox/src/provision/provision.sh` (or `--only <step>`). Run
`--check` first to see the changes.

`systemctl --user` works from `sudo -iu <user>` and from SSM sessions: `ds`
fills in `XDG_RUNTIME_DIR` and the user bus address when they are missing.

## Machine settings and forks

`config/devbox.env` is installed as `/etc/devbox/devbox.env` and is shared by
every devbox that installs from the same repository. The settings:

| Setting | Default | |
|---|---|---|
| `DEVBOX_HOST` | `devbox` | Machine name on the tailnet |
| `DEVBOX_APPS_DOMAIN` | `devbox` | Project links are `<project>.<domain>` |
| `DEVBOX_APPS_TLS` | `internal` | `internal`: links also answer on HTTPS with the devbox's own CA; `off`: HTTP only |
| `DEVBOX_PORT_MIN`, `DEVBOX_PORT_MAX` | `4100`, `4999` | Ports handed out to dev servers |
| `DEVBOX_CHANNEL` | `main` | What `ds update` follows: `main` or `release` (newest `v*` tag) |
| `DEVBOX_FORGE` | `github` | Where `ds publish` creates repositories: `github` or `gitlab`. Each developer can override it in `~/.devbox/config.env` |

To run devboxes with different settings, fork the repository, change the file
in the fork, and install from it: clone the fork in the provisioning and
install steps, and set `DEVBOX_REPO` to its URL when running `install.sh`.
Devboxes then take updates from the fork.

## Shipping updates

Devboxes follow the public repository.

1. Push to `main` (or tag a release, for devboxes on the `release` channel).
2. Each devbox's daily check sees the change, and new terminals show a notice
   such as `↑ devbox update available (3 changes) → ds update`. Nothing
   installs on its own.
3. The developer runs `ds update`; `ds rollback` undoes it.

For each user-visible change:

- add a line to `CHANGELOG.md` under `## Unreleased`;
- for a new feature, add a line to `components/welcome/files/tips.txt`. The
  developer sees new tips once, marked `new:`;
- keep `components/welcome/files/cheatsheet.txt` current.

To cut a release, add a `## <version>` section to `CHANGELOG.md`, then
`just release <version>` tags it and prints the push command.

Changes to `provision/` don't reach a devbox through `ds update`; rerun
`provision.sh` on it.

## Resizing

- **Instance type:** change `instance_type` in the tfvars, then
  `just infra-plan` and `just infra-apply`. It is a stop and start; data stays.
- **Data disk:** raise `data_volume_gb` and apply; the volume grows online.
  Then grow the filesystem on the devbox:
  `sudo resize2fs /dev/disk/by-label/devdata`.

## Rebuilding a machine

Everything that matters is on the data disk: `/home` (projects, `~/.devbox`,
logins) and Docker's images and volumes. The OS disk is disposable. The data
disk has `prevent_destroy`, so OpenTofu won't delete it, and nightly snapshots
cover the rest.

To replace the instance:

1. Turn off termination protection:
   `aws ec2 modify-instance-attribute --instance-id <id> --no-disable-api-termination`
   (with the devbox's profile and region; `tofu output instance_id` in its
   workspace gives the ID).
2. Plan a replacement and apply it:

   ```sh
   ops=${DEVBOX_OPS:-$PWD/.ops}       # from the repo root; the ops folder just uses
   cd infra && tofu workspace select alex
   tofu plan -replace=aws_instance.devbox -var-file="$ops/envs/alex.tfvars" -out="$ops/alex.tfplan"
   cd .. && just infra-apply alex
   ```

   The new instance gets the latest Ubuntu AMI, and cloud-init mounts the
   existing data disk without formatting it.
3. Provision again as on a fresh machine: `provision.sh --user alex --upgrade`.
   The account, `/etc/devbox`, the system services and Tailscale's state lived
   on the old OS disk. Check that the files in `/home/alex` still belong to
   the account (`ls -ln /home`).
4. Log in to Tailscale again, remove the old machine in the admin console,
   disable key expiry, and update the split DNS nameserver if the Tailscale
   address changed (`provision.sh --only names` says so).
5. `passwd alex`, then, as the developer, `~/.devbox/src/install.sh` (it
   reuses the existing clone).

To restore from a snapshot instead, create a volume from it in the same
availability zone and swap it in for the data disk; that is outside what the
OpenTofu code automates.

## Break-glass access

When Tailscale or SSH is down, SSM still works with the operator's AWS
credentials:

```sh
just vm-shell alex
# or, from infra/ in the devbox's workspace:
tofu output -raw ssm_shell
```

The session starts as `ssm-user`, which has sudo. `sudo -iu alex` becomes the
developer. `just vm alex 'ds doctor'` runs a single command without a shell.

## Costs

The instance runs around the clock. Rough on-demand figures for us-east-1;
other regions cost more or less, so check the AWS pricing page for yours:

| Item | Roughly |
|---|---|
| `r7i.xlarge` (32 GiB) | about $190 per month |
| `m7i.xlarge` (16 GiB) | about $150 per month |
| gp3 storage, 550 GB (data and OS disks) | about $45 per month |
| Public IPv4 address | a few dollars per month |
| Snapshots | incremental; usually a small fraction of the disk cost |

After two or three weeks, once the usage pattern is clear, consider a 1-year
Compute Savings Plan for the instance's steady usage; it lowers the instance
cost substantially compared with on-demand, and it also covers a later change
of instance type. The budget alert (`monthly_budget_usd`) emails at 80% of
actual and 100% of forecast spend.
