#!/usr/bin/env bash
# vm-run.sh — run a command on a devbox VM (via SSM) and print its output.
#
#   DEVBOX_ENV=alex scripts/vm-run.sh 'ds doctor'                    # as the developer, in a login zsh
#   DEVBOX_ENV=alex scripts/vm-run.sh --root 'provision.sh --check'  # as root, in bash
#
# Env: DEVBOX_ENV (required: the OpenTofu workspace, as in just infra-plan
# <env>). The instance, AWS profile, region and developer come from that
# workspace's outputs; DEVBOX_INSTANCE, AWS_PROFILE, AWS_REGION and DEVBOX_USER
# override them.
set -euo pipefail

as_root=0
if [ "${1:-}" = --root ]; then
  as_root=1
  shift
fi
cmd=${1:?usage: DEVBOX_ENV=<env> vm-run.sh [--root] '<command>'}
env=${DEVBOX_ENV:?set DEVBOX_ENV to the devbox (its OpenTofu workspace)}
here=$(cd "$(dirname "$0")" && pwd)
outputs=$(cd "$here/../infra" && TF_WORKSPACE=$env tofu output -json)
output() { jq -r --arg k "$1" '.[$k].value // empty' <<<"$outputs"; }
export AWS_PROFILE=${AWS_PROFILE:-$(output aws_profile)} AWS_REGION=${AWS_REGION:-$(output region)}
instance=${DEVBOX_INSTANCE:-$(output instance_id)}
user=${DEVBOX_USER:-$(output owner)}
[ -n "$instance" ] || { echo "vm-run: no instance_id output in workspace $env" >&2; exit 1; }

# Ship the command base64-encoded so quotes survive. Each run gets its own temp
# file, so runs that overlap can't overwrite or delete each other's script.
encoded=$(printf '%s\n' "$cmd" | base64 | tr -d '\n')
if [ "$as_root" = 1 ]; then
  runner="bash \$f"
else
  runner="sudo -iu $user zsh \$f"
fi
remote="f=\$(mktemp /tmp/vm-run.XXXXXX) || exit 1; echo $encoded | base64 -d > \$f && chmod 644 \$f && $runner; rc=\$?; rm -f \$f; exit \$rc"
params=$(jq -n --arg c "$remote" '{commands: [$c]}')

id=$(aws ssm send-command --instance-ids "$instance" --document-name AWS-RunShellScript \
  --parameters "$params" --query Command.CommandId --output text)

while :; do
  status=$(aws ssm get-command-invocation --command-id "$id" --instance-id "$instance" \
    --query Status --output text 2>/dev/null || echo Pending)
  case $status in
    Pending | InProgress | Delayed) sleep 2 ;;
    *) break ;;
  esac
done

result=$(aws ssm get-command-invocation --command-id "$id" --instance-id "$instance" --output json)
jq -r '.StandardOutputContent' <<<"$result"
err=$(jq -r '.StandardErrorContent' <<<"$result")
[ -z "$err" ] || printf '%s\n' "$err" >&2
exit "$(jq -r '.ResponseCode' <<<"$result")"
