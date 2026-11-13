#!/usr/bin/env bash
# Read-only post-destroy audit: lists anything the RyuCloud stack may have left behind that could still bill.
# It only calls Describe/List APIs (free). It changes nothing and deletes nothing.
#
#   export AWS_PROFILE=ryucloud
#   scripts/aws-audit.sh            # prefix ryucloud-prod, region us-west-2
#   PREFIX=ryucloud-dev AWS_REGION=us-west-2 scripts/aws-audit.sh
#
# Exit status: 0 = nothing left, 1 = something is left or a query failed.
# The output can contain account IDs inside ARNs/bucket names: redact them before pasting into the repository.
# Expected to remain by design (not reported here): the Terraform state bucket (name contains "tfstate"),
# the Route 53 hosted zone and registered domain, and the account-level budget made by hand.
set -uo pipefail

REGION="${AWS_REGION:-us-west-2}"
PREFIX="${PREFIX:-ryucloud-prod}"
PROJECT_TAG="${PROJECT_TAG:-ryucloud}"
left=0

if ! aws sts get-caller-identity --query UserId --output text >/dev/null 2>&1; then
  echo "error: no valid AWS session. Run: aws login --profile ryucloud --remote; export AWS_PROFILE=ryucloud" >&2
  exit 2
fi

section() { printf '\n== %s ==\n' "$1"; }

# check LABEL aws-args...   (use --output text and a --query that yields names; empty or "None" means clean)
check() {
  local label="$1"; shift
  local out
  if ! out="$("$@" 2>&1)"; then
    echo "  ??   $label: query failed: $(printf '%s' "$out" | head -n 1)"
    left=1
    return
  fi
  out="$(printf '%s' "$out" | tr '\t' '\n' | sed '/^$/d;/^None$/d')"
  if [ -z "$out" ]; then
    echo "  ok   $label: none"
  else
    echo "  LEFT $label:"
    printf '%s\n' "$out" | sed 's/^/         /'
    left=1
  fi
}

section "Tagged resources (Project=$PROJECT_TAG)"
for r in "$REGION" us-east-1; do
  check "tag search in $r" aws resourcegroupstaggingapi get-resources --region "$r" \
    --tag-filters "Key=Project,Values=$PROJECT_TAG" --query 'ResourceTagMappingList[].ResourceARN' --output text
done

section "Compute and network ($REGION)"
check "ECS clusters"        aws ecs list-clusters --region "$REGION" --query 'clusterArns[]' --output text
check "Load balancers"      aws elbv2 describe-load-balancers --region "$REGION" --query 'LoadBalancers[].LoadBalancerName' --output text
check "Target groups"       aws elbv2 describe-target-groups --region "$REGION" --query 'TargetGroups[].TargetGroupName' --output text
check "NAT gateways (live)" aws ec2 describe-nat-gateways --region "$REGION" --filter Name=state,Values=pending,available \
  --query 'NatGateways[].NatGatewayId' --output text
check "Elastic IPs"         aws ec2 describe-addresses --region "$REGION" --query 'Addresses[].PublicIp' --output text
check "EBS volumes"         aws ec2 describe-volumes --region "$REGION" --query 'Volumes[].VolumeId' --output text
check "Project VPCs"        aws ec2 describe-vpcs --region "$REGION" --filters "Name=tag:Project,Values=$PROJECT_TAG" \
  --query 'Vpcs[].VpcId' --output text
check "VPC endpoints"       aws ec2 describe-vpc-endpoints --region "$REGION" --filters "Name=tag:Project,Values=$PROJECT_TAG" \
  --query 'VpcEndpoints[].VpcEndpointId' --output text

section "Data ($REGION)"
check "RDS instances"       aws rds describe-db-instances --region "$REGION" --query 'DBInstances[].DBInstanceIdentifier' --output text
check "RDS manual/final snapshots" aws rds describe-db-snapshots --region "$REGION" --snapshot-type manual \
  --query 'DBSnapshots[].DBSnapshotIdentifier' --output text
check "S3 buckets (excluding tfstate)" aws s3api list-buckets \
  --query "Buckets[?starts_with(Name, '${PROJECT_TAG}') && !contains(Name, 'tfstate')].Name" --output text
check "SQS queues"          aws sqs list-queues --region "$REGION" --queue-name-prefix "$PREFIX" --query 'QueueUrls[]' --output text
check "ECR repositories"    aws ecr describe-repositories --region "$REGION" \
  --query "repositories[?starts_with(repositoryName, '${PREFIX}/')].repositoryName" --output text
check "Secrets (incl. scheduled deletion)" aws secretsmanager list-secrets --region "$REGION" --include-planned-deletion \
  --query "SecretList[?starts_with(Name, '${PREFIX}/')].Name" --output text

section "Edge and certificates"
check "CloudFront distributions" aws cloudfront list-distributions --query 'DistributionList.Items[].Id' --output text
check "ACM certificates ($REGION)"  aws acm list-certificates --region "$REGION" --query 'CertificateSummaryList[].DomainName' --output text
check "ACM certificates (us-east-1)" aws acm list-certificates --region us-east-1 --query 'CertificateSummaryList[].DomainName' --output text
check "CloudFront public keys"   aws cloudfront list-public-keys --query 'PublicKeyList.Items[].Name' --output text

section "Observability, messaging, identity ($REGION)"
check "CloudWatch log groups" aws logs describe-log-groups --region "$REGION" --log-group-name-prefix "/ecs/$PREFIX" \
  --query 'logGroups[].logGroupName' --output text
check "CloudWatch alarms"     aws cloudwatch describe-alarms --region "$REGION" --alarm-name-prefix "$PREFIX" \
  --query 'MetricAlarms[].AlarmName' --output text
check "CloudWatch dashboards" aws cloudwatch list-dashboards --region "$REGION" --dashboard-name-prefix "$PREFIX" \
  --query 'DashboardEntries[].DashboardName' --output text
check "SNS topics"            aws sns list-topics --region "$REGION" --query "Topics[?contains(TopicArn, '${PREFIX}')].TopicArn" --output text
check "IAM roles"             aws iam list-roles --query "Roles[?starts_with(RoleName, '${PREFIX}')].RoleName" --output text
check "GitHub OIDC provider"  aws iam list-open-id-connect-providers --query 'OpenIDConnectProviderList[].Arn' --output text

echo
if [ "$left" -eq 0 ]; then
  echo "RESULT: nothing left that this audit can see. Still check Billing -> Cost Explorer tomorrow (daily cost should be near zero)."
else
  echo "RESULT: items listed above remain (or a query failed). Resolve each, re-run, and do not walk away until this is clean."
fi
exit "$left"
