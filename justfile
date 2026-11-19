# dsbox — tasks for working on this repo. Run `just` to list them.

# DEVBOX_OPS (in the environment or a .env file here) is your private ops
# folder: envs/<name>.tfvars per devbox, and the OpenTofu state in state/.
set dotenv-load := true

ops := env("DEVBOX_OPS", justfile_directory() / ".ops")

_default:
    @just --list --unsorted

# Download Go modules and report missing dev tools
setup:
    go mod download
    @command -v golangci-lint >/dev/null || echo "missing golangci-lint: brew install golangci-lint"
    @command -v shellcheck >/dev/null || echo "missing shellcheck: brew install shellcheck"
    @command -v gitleaks >/dev/null || echo "missing gitleaks: brew install gitleaks"

# Build ds into ./bin
build:
    go build -trimpath -o bin/ ./cmd/...

# Run ds from source against your own ~/.devbox, e.g. just run doctor
run *args:
    go run ./cmd/ds {{ args }}

# Run tests with the race detector
test:
    go test -race ./...

# Check formatting, vet, Go lint, and shell scripts
lint:
    @test -z "$(gofmt -l $(git ls-files '*.go'))" || { gofmt -l $(git ls-files '*.go'); echo "not formatted: run just fmt"; exit 1; }
    go vet ./...
    golangci-lint run
    scripts/lint-shell.sh
    gitleaks git --redact --no-banner --log-opts=HEAD .

# Format Go code
fmt:
    gofmt -w .

# Everything to check before pushing
check: lint test

# Run a command on a devbox as its developer, e.g. just vm alex 'ds doctor'
vm env cmd:
    DEVBOX_ENV={{ env }} scripts/vm-run.sh {{ quote(cmd) }}

# Move a devbox to a branch, tag or commit, e.g. just vm-update alex my-branch
vm-update env ref="main":
    DEVBOX_ENV={{ env }} scripts/vm-run.sh {{ quote("ds update " + ref) }}

# Show what provision.sh would change on a devbox, without changing anything
provision-check env:
    #!/usr/bin/env bash
    set -euo pipefail
    payload=$(COPYFILE_DISABLE=1 tar --no-xattrs --no-mac-metadata -czf - provision config | base64 | tr -d '\n')
    DEVBOX_ENV={{ env }} scripts/vm-run.sh --root "d=\$(mktemp -d /tmp/provision-check.XXXXXX); echo $payload | base64 -d | tar -xzf - -C \$d; bash \$d/provision/provision.sh --check; rc=\$?; rm -rf \$d; exit \$rc"

# Open an interactive shell on a devbox (Session Manager): just vm-shell alex
vm-shell env:
    #!/usr/bin/env bash
    set -euo pipefail
    cd infra
    tofu workspace select {{ env }} >/dev/null
    out=$(tofu output -json)
    AWS_PROFILE=$(jq -r .aws_profile.value <<<"$out") AWS_REGION=$(jq -r .region.value <<<"$out") \
      aws ssm start-session --target "$(jq -r .instance_id.value <<<"$out")"

# Point OpenTofu at the state in the ops folder (once per checkout)
infra-init:
    mkdir -p "{{ ops }}/state" "{{ ops }}/envs"
    cd infra && tofu init -reconfigure -input=false \
      -backend-config="path={{ ops }}/state/terraform.tfstate" \
      -backend-config="workspace_dir={{ ops }}/state/terraform.tfstate.d"

# Preview infrastructure changes for one devbox: just infra-plan alex
infra-plan env:
    cd infra && tofu workspace select -or-create {{ env }} && tofu plan -var-file="{{ ops }}/envs/{{ env }}.tfvars" -out="{{ ops }}/{{ env }}.tfplan"

# Apply the plan saved by infra-plan (review it first): just infra-apply alex
infra-apply env:
    cd infra && tofu workspace select {{ env }} && tofu apply "{{ ops }}/{{ env }}.tfplan"

# Tag a release once CHANGELOG.md has a section for it
release version:
    #!/usr/bin/env bash
    set -euo pipefail
    [ -z "$(git status --porcelain)" ] || { echo "Commit or stash changes first."; exit 1; }
    grep -qx "## {{ version }}" CHANGELOG.md || { echo "Add a '## {{ version }}' section to CHANGELOG.md first."; exit 1; }
    git tag -a "{{ version }}" -m "dsbox {{ version }}"
    echo "Tagged {{ version }}. Push it with: git push origin {{ version }}"
