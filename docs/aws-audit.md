# Terraform audit (Phase 4)

A read-only review of `infra/terraform` and the deployment workflow. **Static review only:** `terraform validate`
passes (provider 6.72.0), but nothing has been planned or applied, so anything labelled "unverified" is reasoning, not
an observed result.

## Findings

| # | Severity | Finding | Status |
| --- | --- | --- | --- |
| 1 | **High** (when CI deploy is enabled) | The deploy role trusts the OIDC subject `repo:OWNER/REPO:environment:production`. The "only on main" check lives in the workflow file, which a branch can edit. Unless the GitHub environment itself limits which branches may deploy, any branch's workflow could assume the role, and a role that can register task definitions and update services can run arbitrary images with the tasks' IAM permissions. | **Open until you do it on GitHub.** Runbook step 10 now has the UI path and `gh api` commands (branch policy `main`, required reviewer, and read-back commands that prove it). It cannot be enforced from Terraform here, so keep `github_repository` empty (no deploy role is created) until the read-back shows it. Environment rules need a public repository or a paid plan. |
| 2 | Medium | The media bucket has no versioning and no access logging. A deleted or overwritten video is gone; there is no audit trail of object access. | Accepted for cost; documented. Add versioning with a noncurrent-version expiry if the content matters. |
| 3 | Medium | `DB_SSLMODE=require` encrypts but does not verify the RDS certificate (a network attacker inside the VPC path could impersonate the DB). | Documented in aws-security.md. Fix: bake the RDS CA bundle into the image and use `verify-full`. |
| 4 | Medium | Tasks have public IPs (no NAT). Inbound is limited to the ALB security group, but egress is HTTPS to anywhere. | Documented trade-off (saves ~$33/month per AZ). |
| 5 | Medium | One task per service and a single-AZ database: any task replacement or AZ problem is visible downtime. | Documented; matches the cost goal. |
| 6 | Low | Alarms only reach you after you confirm the SNS email subscription; Budgets emails are separate. | In the runbook (step 5). |
| 7 | Low | `final_snapshot_identifier` is a fixed name. Destroying and re-creating the stack twice with `protect_data = true` fails the second time because a snapshot with that name already exists. | Known; delete or rename the old snapshot first. |
| 8 | Low | `scripts/aws-roll-service.sh` copies a described task definition back into `register-task-definition`. If a newer AWS CLI returns a field the register call rejects, the deploy step fails (nothing is changed; the old revision stays active). | Unverified; first manual run will show. |
| 9 | Low | `readonlyRootFilesystem = true` on the API container is unverified in ECS. A path the process writes to would crash the task at startup (the circuit breaker would then roll back). | Check the first deployment's logs. |
| 10 | Info | The API's local media routes (`/videos/{id}/hls/...`) stay registered in AWS. They apply the same visibility checks but would stream bytes through the API. | Documented. |
| 11 | Info | No WAF, no CloudFront/ALB access logs, no CloudTrail configuration in this stack. | Out of scope; listed as upgrades. |
| 12 | Medium | `.gitignore` only excluded `*.pem`/`*.key` under `infra/terraform/`. A private key saved anywhere else in the repository would have been committable. (Phase 5 review; found by a `git check-ignore` test.) | **Fixed:** `*.pem`, `*.key`, `.ryucloud/`, `infra/terraform/tfplan*` are ignored repository-wide. |
| 13 | Medium | The host has Terraform 1.17.1; CI and validation use 1.10.5. Running `plan`/`apply` with the newer version rewrites the remote state's version marker, after which 1.10.5 refuses to read it. | **Mitigated:** all state-touching Terraform runs through `scripts/aws-terraform.sh` (Docker, 1.10.5); runbook updated. Not yet exercised against AWS. |
| 14 | Low | Deploy re-runs may fail: tags are immutable in ECR and the workflow tags with the commit SHA, so a re-run of a partly failed deployment may be rejected at the image push. | **Unverified.** If it happens, push a new commit (new SHA) and deploy again. |
| 15 | Low | The Deploy workflow does not require CI to have passed for the commit. | Open. Enable branch protection with required status checks on `main` (CI job names: Backend, Frontend, Terraform, Shell scripts, Build images), or deploy only from commits you have seen pass. |
| 16 | Info | The runbook used a 12-character SHA tag while the workflow uses the full SHA. | **Fixed:** the runbook now uses the full SHA. |
| 17 | Medium | The published idle estimate ("80 to 105") was lower than the sum of its own rows (about 90 to 117), and the zero-task baseline (about 50) was not stated. | **Fixed** in [aws-costs.md](aws-costs.md), which now also compares temporary and always-on use. |
| 18 | **Blocker** | The stack requires a domain and a Route 53 hosted zone (ACM, ALB HTTPS, CloudFront alias, signed cookies). None exists. | Open: [phase5-plan.md](phase5-plan.md) B2. |
| 19 | Info | New AWS accounts start on the Free plan. AWS documents that some services are unavailable on it, and that creating or joining an Organization upgrades the account and ends credit eligibility. Which services this stack would hit is unknown. | Open: decide the upgrade before the first apply ([phase5-plan.md](phase5-plan.md) §5). |
| 20 | Medium | The runbook assumed IAM Identity Center. It is unavailable on this account (Free plan), and long-lived access keys must be avoided. | **Fixed (docs):** IAM user with MFA + `aws login` (short-lived credentials); see [phase5-plan.md](phase5-plan.md) §4. Unverified until run. |
| 21 | Low | The repository is public: environment variables (including the role ARN, hence the account ID) are visible; fork workflow runs and secret leaks need settings. | Open: GitHub settings in [phase5-plan.md](phase5-plan.md) step 0. Not credentials; account ID exposure is low risk. |
| 22 | Low | `aws_region` defaulted to `us-east-1` while the decision is `us-west-2`; a missing `aws_region` in tfvars would silently deploy to the wrong region (while `backend.hcl` says otherwise). | **Fixed:** default is now `us-west-2`; templates updated. `terraform validate` must be re-run (below). |
| 23 | Info | Temporary environments need `protect_data = false` from the first apply (ECR `force_delete`, S3 `force_destroy`, RDS no final snapshot, ALB without deletion protection, secrets deleted immediately). Data is lost on destroy; there is no snapshot-restore path. | Documented; `terraform.tfvars.example` sets it. Changing it later costs an extra apply. |

## Checked and found sound (by reading)

- **No public data paths.** S3 Block Public Access is on; the bucket policy admits only this distribution, only for `videos/*`; RDS is in route-less subnets with no public address; SQS and S3 deny non-TLS.
- **Least privilege.** The worker reads only `uploads/*` and writes only `videos/*`; the API cannot receive or delete queue messages; the frontend has no role; the execution role reads exactly two secrets; `iam:PassRole` is limited to three roles and to `ecs-tasks.amazonaws.com`.
- **Secrets.** The database password is created and rotated by RDS and never appears in Terraform variables or state-managed attributes the repo controls; the CloudFront private key is stored by hand and never enters state.
- **Dependency order.** The CloudFront distribution waits for the us-east-1 certificate validation; the bucket policy references the distribution but the distribution references only the bucket (no cycle); ECS services wait for their listener rules and the capacity providers; the first apply creates services with zero tasks so the missing images and the empty signing-key secret do not break it.
- **Frontend routing.** The frontend makes no server-side API calls, so `/api/*` routed at the ALB is sufficient and the Compose-era `API_PROXY_TARGET` is never used in AWS.
- **Spot interruption.** On SIGTERM the worker releases its job and acknowledges the message; the replacement task's sweeper re-sends the job. The stop timeout (120 s) exceeds the worker's grace (90 s).
- **Health checks.** The ALB checks API liveness, not readiness, so a database blip does not remove every API target.

## Cost review

See [aws-costs.md](aws-costs.md): about $90 to $117/month with all services running, about $50/month with zero tasks, about $0.11 to $0.15 per hour for a temporary session (unverified for us-west-2). Items most likely to surprise:

1. **Public IPv4 addresses**, billed per address per hour: about $18/month across 3 tasks and the ALB.
2. **The ALB** bills hourly even with zero tasks.
3. **RDS** bills while stopped for storage, and restarts itself after 7 days if you stop it.
4. **CloudFront egress** beyond the free tier is the one cost that grows with viewers.
5. **Always-on worker**: Spot makes it cheap, but it runs 24/7 even when the queue is empty.

## What `plan` and the first apply will still have to prove

Provider/API behaviours that only a real run shows: ACM validation timing, CloudFront creation time, whether RDS accepts `engine_version = "17"` as written, the CloudFront response-headers policy shape, and the ECS task-role assumption condition.
