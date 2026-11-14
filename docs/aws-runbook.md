# AWS deployment runbook

**Nothing here runs automatically and nothing has been deployed.** Each step is a command for you to run. Steps marked
**BILLS** start charges ([aws-costs.md](aws-costs.md)); steps marked **DESTRUCTIVE** delete data.

Where a command runs: **VM** = your Ubuntu terminal in the repository, **AWS CLI** = the same terminal with AWS
credentials configured for the target account (`aws sts get-caller-identity` should show the account you expect).
Placeholders look like `REPLACE_ME` and are explained where they appear.

## 0. Prerequisites (once)

- An AWS account with billing alerts enabled and an MFA-protected IAM user (logged in with `aws login`, no access keys; IAM Identity Center is not available on this account and an Organization already exists, see [phase5-plan.md](phase5-plan.md)) that can create IAM roles,
  VPCs, RDS, S3, CloudFront, ECS, ECR, SQS, ACM, Route 53 records, Secrets Manager and Budgets.
- A **domain you control with a public Route 53 hosted zone** whose name servers are already delegated at the
  registrar. (Status 2026-11-13: domain purchased, hosted zone and delegation not yet done; see [phase5-plan.md](phase5-plan.md) step 1.) Pick a host name for the app, e.g. `ryucloud.example.com`; media will be `media.ryucloud.example.com`.
  (Signed cookies cannot work on `*.cloudfront.net`/`*.amazonaws.com` host names.)
- Installed on the VM: Docker, AWS CLI v2, `jq`, `openssl`. Region, domain choices and AWS CLI / IAM Identity Center
  setup are in [aws-setup.md](aws-setup.md).
- **Terraform runs in Docker, pinned to 1.10.5** (the version CI uses). Do not run `plan`/`apply` with a newer host
  Terraform: it rewrites the state's version marker and 1.10.5 can then no longer read the state. In this runbook,
  every `terraform ...` command means the wrapper below (it forwards your AWS credentials to the container for that one command):

```bash
cd ~/projects/RyuCloud
tf() { ~/projects/RyuCloud/scripts/aws-terraform.sh "$@"; }     # paste once per shell session
tf version                                                       # must print Terraform v1.10.5
export TERRAFORM=~/projects/RyuCloud/scripts/aws-terraform.sh    # lets scripts/aws-run-migrations.sh read outputs the same way
```

```bash
tf version && aws --version && jq --version && openssl version
aws sts get-caller-identity          # confirm account and identity
aws route53 list-hosted-zones-by-name --dns-name example.com --query 'HostedZones[0].Id'   # your zone ID is the part after /hostedzone/
```

## 1. Validate locally (free, no AWS resources)

```bash
cd ~/projects/RyuCloud
# Backend: formatting, vet, tests (the tests need the local database; see README "Running tests")
docker compose up -d db
docker compose run --rm --no-deps backend gofmt -l .
docker compose run --rm --no-deps backend go vet ./...
docker compose run --rm backend go test ./... -count=1
# Frontend
docker compose run --rm --no-deps frontend npm test
docker compose run --rm --no-deps frontend npm run typecheck
# Terraform: formatting + validation, no credentials needed
tf -chdir=infra/terraform fmt -recursive
scripts/validate-terraform.sh      # offline only, touches no state; runs Terraform 1.10.5 in Docker if the host version differs
```

## 2. Create the Terraform state bucket (once, ≈ $0)

State holds resource IDs and attributes, so it gets its own private, versioned, encrypted bucket. Bucket names are
global: replace `REPLACE_STATE_BUCKET` with something unique (e.g. `ryucloud-tfstate-<account-id>`).

```bash
export AWS_REGION=us-west-2
STATE_BUCKET=REPLACE_STATE_BUCKET
aws s3api create-bucket --bucket "$STATE_BUCKET" --region "$AWS_REGION" \
  --create-bucket-configuration LocationConstraint="$AWS_REGION"     # required outside us-east-1
aws s3api put-bucket-versioning --bucket "$STATE_BUCKET" --versioning-configuration Status=Enabled
aws s3api put-bucket-encryption --bucket "$STATE_BUCKET" --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
aws s3api put-public-access-block --bucket "$STATE_BUCKET" --public-access-block-configuration \
  BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
```

## 3. Generate the CloudFront signing key pair (once)

The **private key never goes into Git or Terraform**. Keep it in `~/.ryucloud/` for now.

```bash
mkdir -p ~/.ryucloud && chmod 700 ~/.ryucloud
openssl genrsa -out ~/.ryucloud/cloudfront_private_key.pem 2048
chmod 600 ~/.ryucloud/cloudfront_private_key.pem
openssl rsa -pubout -in ~/.ryucloud/cloudfront_private_key.pem -out ~/projects/RyuCloud/infra/terraform/cloudfront_public_key.pem
```

## 4. Configure Terraform

```bash
cd ~/projects/RyuCloud/infra/terraform
cp backend.hcl.example backend.hcl
cp terraform.tfvars.example terraform.tfvars
vi backend.hcl          # set bucket = your STATE_BUCKET, region
vi terraform.tfvars     # set domain_name, hosted_zone_id, alert_email; keep deploy_services = false
```

Both files are git-ignored. If you want the GitHub deploy role, also set `github_repository = "OWNER/REPO"` (your
GitHub login and repository name).

## 5. Create the infrastructure (BILLS: ALB, RDS, IPv4, Secrets Manager, CloudWatch start immediately)

`deploy_services = false` means **no ECS tasks run yet**, but the load balancer, database and other fixed-price
resources are billed from this point (≈ $45–55/month before any task runs).

```bash
cd ~/projects/RyuCloud/infra/terraform
tf init -backend-config=backend.hcl
tf plan -out=tfplan        # READ IT: nothing should be destroyed on a first run
tf apply tfplan
```

Expect roughly 15–30 minutes: RDS and the CloudFront distribution are slow, and ACM waits for DNS validation.
If certificate validation hangs, the hosted zone is not delegated to Route 53 yet: check with
`dig +norec NS <domain> @ns01.trs-dns.com` that the registry lists the name servers of **your** zone (a domain registered through
Route 53 gets its own default zone, which is not the one Terraform fills). Commit the generated
`.terraform.lock.hcl` (it pins provider versions).

**Credential limit:** `tf` passes fixed credentials into the container and `aws login` credentials last about 15 minutes. A single
`apply` that runs longer fails with `ExpiredToken`, Terraform writes `errored.tfstate`, and a lock is left behind. If that happens:
read the lock ID from the `.tflock` object in the state bucket, `tf force-unlock -force <ID>` (only when no Terraform is running),
`tf state push errored.tfstate`, and check for tainted resources with `tf plan` (`tf untaint <address>` if the real resource is fine).
To avoid it, apply in stages: `-target=aws_acm_certificate_validation.app -target=aws_acm_certificate_validation.media`, then
`-target=aws_cloudfront_distribution.media`, then the full plan. (Verified 2026-11-14; see [verification.md](verification.md).)

Then:

1. **Confirm the SNS email** AWS sent to `alert_email` (and the Budgets email, if any). Alarms are silent until you do.
2. **Store the signing key** (the API cannot start without it):

```bash
cd ~/projects/RyuCloud/infra/terraform
aws secretsmanager put-secret-value \
  --secret-id "$(tf output -raw cloudfront_private_key_secret)" \
  --secret-string "file://$HOME/.ryucloud/cloudfront_private_key.pem"
```

## 6. Build and push the first images (BILLS: small ECR storage)

Tags are immutable, so use the full commit SHA (the same tag the Deploy workflow uses). Images are built from the
**committed** tree: commit first, and check `git status` is clean. Steps 6 and 7 use the shell variables set here, so
run both in the same terminal session.

```bash
cd ~/projects/RyuCloud
export AWS_REGION=us-west-2
TAG="$(git rev-parse HEAD)"
PREFIX=ryucloud-prod            # <project>-<environment>
REGISTRY="$(aws sts get-caller-identity --query Account --output text).dkr.ecr.${AWS_REGION}.amazonaws.com"
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$REGISTRY"
docker build --target prod   -t "$REGISTRY/$PREFIX/api:$TAG"      backend
docker build --target worker -t "$REGISTRY/$PREFIX/worker:$TAG"   backend
docker build --target prod   -t "$REGISTRY/$PREFIX/frontend:$TAG" frontend
docker push "$REGISTRY/$PREFIX/api:$TAG"
docker push "$REGISTRY/$PREFIX/worker:$TAG"
docker push "$REGISTRY/$PREFIX/frontend:$TAG"
```

(Alternative once the GitHub role exists: run the **Deploy** workflow, which does steps 6–7 for you. See step 10.)

## 7. Migrate the database, point the services at the images, then start them

```bash
cd ~/projects/RyuCloud
CLUSTER="$(tf -chdir=infra/terraform output -raw ecs_cluster)"

# Register a migrate task revision with the new API image, then run it (BILLS: ~1 minute of Fargate)
scripts/aws-roll-service.sh "$CLUSTER" - "$PREFIX-migrate" migrate "$REGISTRY/$PREFIX/api:$TAG"
scripts/aws-run-migrations.sh

# Point each (currently zero-task) service at the new images
scripts/aws-roll-service.sh "$CLUSTER" api      "$PREFIX-api"      api      "$REGISTRY/$PREFIX/api:$TAG"
scripts/aws-roll-service.sh "$CLUSTER" worker   "$PREFIX-worker"   worker   "$REGISTRY/$PREFIX/worker:$TAG"
scripts/aws-roll-service.sh "$CLUSTER" frontend "$PREFIX-frontend" frontend "$REGISTRY/$PREFIX/frontend:$TAG"
```

Now start them (BILLS: Fargate tasks and task public IPs, ≈ $35–70/month):

```bash
cd ~/projects/RyuCloud/infra/terraform
vi terraform.tfvars        # change to: deploy_services = true
tf plan -out=tfplan
tf apply tfplan
```

## 8. Verify

```bash
APP=$(tf -chdir=infra/terraform output -raw app_url)
curl -fsS "$APP/api/v1/health"          # {"status":"ok","version":"0.4.0"}
curl -fsS "$APP/api/v1/health/ready"    # includes schema_version
curl -sI "$APP/" | head -1              # HTTP/2 200
aws ecs describe-services --cluster "$CLUSTER" --services api frontend worker \
  --query 'services[].{name:serviceName,running:runningCount,desired:desiredCount}'
aws logs tail "/ecs/$PREFIX/api" --since 10m
aws logs tail "/ecs/$PREFIX/worker" --since 10m
```

In the browser (`$APP`): register, upload a short clip, watch the status go `processing` → `ready`, play it.
Then prove media is private:

```bash
MEDIA=$(tf -chdir=infra/terraform output -raw media_url)
curl -sI "$MEDIA/videos/anything/hls/master.m3u8" | head -1       # HTTP/2 403 (no signature)
BUCKET=$(tf -chdir=infra/terraform output -raw media_bucket)
curl -sI "https://$BUCKET.s3.amazonaws.com/videos/anything/hls/master.m3u8" | head -1   # 403 (bucket is private)
```

In the browser dev tools (Application → Cookies) you should see three `CloudFront-*` cookies for the video, and the
`.m3u8`/`.ts` requests going to `media.<domain>` with status 200. The CloudWatch dashboard URL is
`tf output dashboard_url`.

Not yet verified by anyone: Safari native playback, behavior after the 2-hour cookie expiry, and a failover test.

## 9. Day-two operations

**Logs:** `aws logs tail "/ecs/$PREFIX/<api|worker|frontend>" --follow`. Lines are JSON (`level`, `msg`, `request_id`, `job_id`).

**Dead-letter queue** (alarm `…-dlq-not-empty`): the messages name job IDs. Read one, fix the cause, check the job row, then
return the messages to the main queue:

```bash
DLQ_URL=$(tf -chdir=infra/terraform output -raw jobs_dlq_url)
aws sqs receive-message --queue-url "$DLQ_URL" --max-number-of-messages 5 --visibility-timeout 30
DLQ_ARN=$(aws sqs get-queue-attributes --queue-url "$DLQ_URL" --attribute-names QueueArn --query Attributes.QueueArn --output text)
aws sqs start-message-move-task --source-arn "$DLQ_ARN"        # moves them back to the original queue
```

**Changing task configuration** (CPU, env vars, secrets): edit the `.tf` file, `tf apply`, then roll the service
onto the newest revision of its family (CI/the script copy the *latest* revision, so this keeps your change):

```bash
aws ecs update-service --cluster "$CLUSTER" --service api \
  --task-definition "$PREFIX-api" --force-new-deployment      # family without a revision = latest
```

**Roll back an image:** run `scripts/aws-roll-service.sh` with the previous SHA tag (images stay in ECR). ECS also rolls
back by itself when a new deployment fails its health checks (circuit breaker).

**Pause to save money:** set `deploy_services = false` and apply (services go to zero tasks; the ALB, database and fixed
items still bill). Stop the database too with `aws rds stop-db-instance --db-instance-identifier "$PREFIX"` (AWS restarts it
after 7 days).

## 10. GitHub Actions deployment (after the first manual deploy works)

1. Set `github_repository = "OWNER/REPO"` in `terraform.tfvars`, `tf apply`. Copy `tf output github_deploy_role_arn`.
2. In GitHub: **Settings → Environments → New environment → `production`**. Add yourself as a required reviewer **and set "Deployment branches and tags" to "Selected branches" → `main`**. The AWS role trusts the environment, so this branch restriction is what stops other branches from deploying (see [aws-audit.md](aws-audit.md), finding 1). **Do not enable the AWS role (step 1) until this is done and checked.**
   Required reviewers and branch rules on environments need a public repository or a paid GitHub plan; on a free private repository, leave `github_repository` empty and deploy by hand (steps 6–7).
   The same thing from the terminal with the GitHub CLI (`gh auth login` first; replace `OWNER/REPO` and `YOUR_NUMERIC_USER_ID`, which is `gh api user --jq .id`):

```bash
gh api -X PUT repos/OWNER/REPO/environments/production --input - <<'JSON'
{"reviewers":[{"type":"User","id":YOUR_NUMERIC_USER_ID}],
 "deployment_branch_policy":{"protected_branches":false,"custom_branch_policies":true}}
JSON
gh api -X POST repos/OWNER/REPO/environments/production/deployment-branch-policies -f name=main -f type=branch
# Verify: expect custom_branch_policies true, one policy named main, and a required_reviewers rule
gh api repos/OWNER/REPO/environments/production --jq '{policy: .deployment_branch_policy, rules: [.protection_rules[].type]}'
gh api repos/OWNER/REPO/environments/production/deployment-branch-policies --jq '.branch_policies[].name'
```
3. Add these **environment variables** (not secrets; none are sensitive):

| Variable | Value |
| --- | --- |
| `AWS_DEPLOY_ROLE_ARN` | `tf output github_deploy_role_arn` |
| `AWS_REGION` | `us-west-2` |
| `ECR_PREFIX` | `ryucloud-prod` |
| `ECS_CLUSTER` | `tf output ecs_cluster` |
| `MIGRATE_SUBNETS` | `tf output -json task_subnets \| jq -r 'join(",")'` |
| `MIGRATE_SECURITY_GROUP` | `tf output task_security_group` |

4. **Actions → Deploy → Run workflow**, type `deploy`, approve in the environment gate. It builds, pushes, migrates and rolls
   the services. CI (`ci.yml`) runs automatically on pushes and pull requests and never touches AWS.

## 11. Tear down (DESTRUCTIVE: deletes the database and all videos)

Only for a throwaway environment. `protect_data = true` (the default) blocks this on purpose.

```bash
cd ~/projects/RyuCloud/infra/terraform
# 1. In terraform.tfvars set:  protect_data = false   then apply so deletion protection is lifted
tf apply
# 2. Review the destroy plan carefully, then:
tf plan -destroy -out=destroy.tfplan
tf apply destroy.tfplan
```

The state bucket from step 2 is not removed by this. After the destroy run `scripts/aws-audit.sh` (read-only). The full session procedure, evidence capture and cost checks are in [temporary-deployment.md](temporary-deployment.md).
