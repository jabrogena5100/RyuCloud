#!/usr/bin/env bash
# Offline Terraform checks: formatting and validation. Needs no AWS credentials and creates
# nothing. `init -backend=false` downloads the AWS provider into infra/terraform/.terraform.
set -euo pipefail
cd "$(dirname "$0")/../infra/terraform"
terraform fmt -check -recursive -diff
terraform init -backend=false -input=false
terraform validate
