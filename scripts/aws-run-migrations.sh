#!/usr/bin/env bash
# Runs database migrations on AWS as a one-off ECS Fargate task (the "migrate" task definition:
# `api migrate up` against RDS) and fails if the task does not exit 0.
#
# Reads its settings from the environment, or from `terraform output` in infra/terraform:
#   AWS_REGION  CLUSTER  TASK_FAMILY  SUBNETS (comma separated)  SECURITY_GROUP
# Needs: aws CLI v2 (credentials with ecs:RunTask/DescribeTasks), and jq.
#
# This starts a billed Fargate task for roughly a minute. It does not run by itself.
set -euo pipefail

TF_DIR="$(cd "$(dirname "$0")/../infra/terraform" && pwd)"
tf_out() { terraform -chdir="$TF_DIR" output -raw "$1"; }
tf_json() { terraform -chdir="$TF_DIR" output -json "$1"; }

: "${AWS_REGION:=$(tf_out aws_region)}"
: "${CLUSTER:=$(tf_out ecs_cluster)}"
: "${TASK_FAMILY:=$(tf_json task_families | jq -r .migrate)}"
: "${SUBNETS:=$(tf_json task_subnets | jq -r 'join(",")')}"
: "${SECURITY_GROUP:=$(tf_out task_security_group)}"
export AWS_REGION

echo "Running migrations: cluster=$CLUSTER task=$TASK_FAMILY region=$AWS_REGION"
task_arn="$(aws ecs run-task \
  --cluster "$CLUSTER" \
  --task-definition "$TASK_FAMILY" \
  --capacity-provider-strategy capacityProvider=FARGATE,weight=1 \
  --network-configuration "awsvpcConfiguration={subnets=[$SUBNETS],securityGroups=[$SECURITY_GROUP],assignPublicIp=ENABLED}" \
  --query 'tasks[0].taskArn' --output text)"

if [ -z "$task_arn" ] || [ "$task_arn" = "None" ]; then
  echo "error: ECS did not start the task (check capacity, subnets and the task definition)" >&2
  exit 1
fi
echo "Started $task_arn; waiting for it to stop..."
aws ecs wait tasks-stopped --cluster "$CLUSTER" --tasks "$task_arn"

exit_code="$(aws ecs describe-tasks --cluster "$CLUSTER" --tasks "$task_arn" \
  --query 'tasks[0].containers[0].exitCode' --output text)"
if [ "$exit_code" != "0" ]; then
  echo "error: migration task exited with code $exit_code" >&2
  echo "Logs: aws logs tail /ecs/${TASK_FAMILY%-migrate}/migrate --since 15m" >&2
  exit 1
fi
echo "Migrations applied."
