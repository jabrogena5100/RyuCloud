#!/usr/bin/env bash
# Registers a new revision of an ECS task definition with a new image and rolls the service
# onto it. Used by the deploy workflow; also usable by hand.
#
#   scripts/aws-roll-service.sh <cluster> <service> <family> <container> <image>
#
# Pass "-" as <service> to only register the revision (used for the one-off migrate task).
# It copies the LATEST revision of <family> (so changes made with Terraform are kept) and only
# replaces the image of <container>. Needs: aws CLI v2 and jq.
set -euo pipefail

if [ "$#" -ne 5 ]; then
  echo "usage: $0 <cluster> <service> <family> <container> <image>" >&2
  exit 2
fi
cluster="$1" service="$2" family="$3" container="$4" image="$5"

current="$(aws ecs describe-task-definition --task-definition "$family" --query taskDefinition)"
new_def="$(jq --arg c "$container" --arg i "$image" '
  .containerDefinitions |= map(if .name == $c then .image = $i else . end)
  | del(.taskDefinitionArn, .revision, .status, .requiresAttributes, .compatibilities,
        .registeredAt, .registeredBy)' <<<"$current")"

new_arn="$(aws ecs register-task-definition --cli-input-json "$new_def" \
  --query taskDefinition.taskDefinitionArn --output text)"
echo "Registered $new_arn"

if [ "$service" = "-" ]; then
  exit 0
fi

aws ecs update-service --cluster "$cluster" --service "$service" --task-definition "$new_arn" >/dev/null
echo "Waiting for $service to become stable (the deployment circuit breaker rolls back on failure)..."
aws ecs wait services-stable --cluster "$cluster" --services "$service"
echo "$service is running $image"
