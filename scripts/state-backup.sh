#!/usr/bin/env bash
# state-backup.sh — copy one devbox's OpenTofu state to S3 in that devbox's own
# AWS account, stored as Glacier Instant Retrieval.
#
#   DEVBOX_ENV=alex DEVBOX_OPS=~/ops scripts/state-backup.sh
#
# The bucket (devbox-state-<account id>, or DEVBOX_STATE_BUCKET) is created on
# first use: private, encrypted, TLS only, versioned. Each run uploads
# <env>/terraform.tfstate; older copies stay as versions (the newest 20 are
# kept, others are deleted after 90 days, Glacier Instant Retrieval's minimum).
#
# Restore: aws s3 cp s3://<bucket>/<env>/terraform.tfstate \
#            "$DEVBOX_OPS/state/terraform.tfstate.d/<env>/terraform.tfstate"
set -euo pipefail

env=${DEVBOX_ENV:?set DEVBOX_ENV to the devbox (its OpenTofu workspace)}
ops=${DEVBOX_OPS:?set DEVBOX_OPS to your ops folder}
state="$ops/state/terraform.tfstate.d/$env/terraform.tfstate"
[ -s "$state" ] || { echo "state-backup: no state at $state" >&2; exit 1; }

here=$(cd "$(dirname "$0")" && pwd)
outputs=$(cd "$here/../infra" && TF_WORKSPACE=$env tofu output -json)
output() { jq -r --arg k "$1" '.[$k].value // empty' <<<"$outputs"; }
export AWS_PROFILE=${AWS_PROFILE:-$(output aws_profile)} AWS_REGION=${AWS_REGION:-$(output region)}
[ -n "$AWS_PROFILE" ] && [ -n "$AWS_REGION" ] || { echo "state-backup: no aws_profile/region output in workspace $env" >&2; exit 1; }

account=$(aws sts get-caller-identity --query Account --output text)
bucket=${DEVBOX_STATE_BUCKET:-devbox-state-$account}

if ! aws s3api head-bucket --bucket "$bucket" --expected-bucket-owner "$account" 2>/dev/null; then
  echo "Creating s3://$bucket in $AWS_REGION"
  if [ "$AWS_REGION" = us-east-1 ]; then
    aws s3api create-bucket --bucket "$bucket" >/dev/null
  else
    aws s3api create-bucket --bucket "$bucket" \
      --create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null
  fi
  aws s3api wait bucket-exists --bucket "$bucket"
  aws s3api put-public-access-block --bucket "$bucket" --public-access-block-configuration \
    BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
  aws s3api put-bucket-encryption --bucket "$bucket" --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'
  aws s3api put-bucket-versioning --bucket "$bucket" --versioning-configuration Status=Enabled
  aws s3api put-bucket-policy --bucket "$bucket" --policy "$(jq -n --arg b "$bucket" '{
    Version: "2012-10-17",
    Statement: [{
      Sid: "TLSOnly", Effect: "Deny", Principal: "*", Action: "s3:*",
      Resource: ["arn:aws:s3:::\($b)", "arn:aws:s3:::\($b)/*"],
      Condition: {Bool: {"aws:SecureTransport": "false"}}
    }]}')"
  aws s3api put-bucket-lifecycle-configuration --bucket "$bucket" --lifecycle-configuration '{
    "Rules": [{
      "ID": "old-state-versions", "Status": "Enabled", "Filter": {},
      "NoncurrentVersionExpiration": {"NoncurrentDays": 90, "NewerNoncurrentVersions": 20},
      "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 7}
    }]}' >/dev/null
fi

aws s3 cp "$state" "s3://$bucket/$env/terraform.tfstate" \
  --storage-class GLACIER_IR --checksum-algorithm SHA256 --only-show-errors
echo "Backed up $env state to s3://$bucket/$env/terraform.tfstate (Glacier Instant Retrieval)"
