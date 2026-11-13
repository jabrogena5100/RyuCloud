#!/usr/bin/env bash
# Runs Terraform 1.10.5 (the version CI uses) in Docker, so the host's Terraform version
# never touches the remote state. A newer Terraform that runs plan/apply rewrites the state
# file's version marker, after which 1.10.5 refuses to read it.
#
#   scripts/aws-terraform.sh -chdir=infra/terraform plan -out=tfplan
#   cd infra/terraform && ../../scripts/aws-terraform.sh plan -out=tfplan
#
# It forwards your current AWS credentials as environment variables for this one command.
# If AWS_ACCESS_KEY_ID is not set, it exports short-lived credentials from the AWS CLI
# profile in AWS_PROFILE (an IAM Identity Center login works: `aws sso login` first).
# Nothing is printed or stored. The repository is mounted at the same absolute path inside the
# container, so absolute paths in other scripts (TF_DIR) keep working.
#
# This script only runs the Terraform command you pass. `apply` and `destroy` are yours to type.
set -euo pipefail

TF_IMAGE="${TF_IMAGE:-hashicorp/terraform:1.10.5}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [ -z "${AWS_ACCESS_KEY_ID:-}" ]; then
  if ! command -v aws >/dev/null 2>&1; then
    echo "error: AWS CLI v2 not found and AWS_ACCESS_KEY_ID is not set (see docs/aws-setup.md)" >&2
    exit 1
  fi
  creds="$(aws configure export-credentials --format env-no-export)" || {
    echo "error: could not get credentials; run 'aws login --profile ryucloud' (or 'aws sso login') and set AWS_PROFILE" >&2
    exit 1
  }
  set -a
  eval "$creds"
  set +a
fi

tty_flags=(-i)
if [ -t 0 ] && [ -t 1 ]; then tty_flags=(-it); fi

exec docker run --rm "${tty_flags[@]}" \
  --user "$(id -u):$(id -g)" \
  --volume "$ROOT:$ROOT" \
  --workdir "$PWD" \
  --env HOME=/tmp \
  --env AWS_ACCESS_KEY_ID --env AWS_SECRET_ACCESS_KEY --env AWS_SESSION_TOKEN \
  --env AWS_REGION --env AWS_DEFAULT_REGION \
  "$TF_IMAGE" "$@"
