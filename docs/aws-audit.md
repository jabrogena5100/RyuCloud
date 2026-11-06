# Terraform audit (Phase 4)

A read-only review of `infra/terraform` and the deployment workflow. **Static review only:** `terraform validate`
passes (provider 6.72.0), but nothing has been planned or applied, so anything labelled "unverified" is reasoning, not
an observed result.

## Findings

| # | Severity | Finding | Status |
| --- | --- | --- | --- |
| 1 | **High** (when CI deploy is enabled) | The deploy role trusts the OIDC subject `repo:OWNER/REPO:environment:production`. The "only on main" check lives in the workflow file, which a branch can edit. Unless the GitHub environment itself limits which branches may deploy, any branch's workflow could assume the role, and a role that can register task definitions and update services can run arbitrary images with the tasks' IAM permissions. | **Mitigation documented** in the runbook (restrict the `production` environment to `main` and require reviewers). It cannot be enforced from Terraform here. |
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

## Checked and found sound (by reading)

- **No public data paths.** S3 Block Public Access is on; the bucket policy admits only this distribution, only for `videos/*`; RDS is in route-less subnets with no public address; SQS and S3 deny non-TLS.
- **Least privilege.** The worker reads only `uploads/*` and writes only `videos/*`; the API cannot receive or delete queue messages; the frontend has no role; the execution role reads exactly two secrets; `iam:PassRole` is limited to three roles and to `ecs-tasks.amazonaws.com`.
- **Secrets.** The database password is created and rotated by RDS and never appears in Terraform variables or state-managed attributes the repo controls; the CloudFront private key is stored by hand and never enters state.
- **Dependency order.** The CloudFront distribution waits for the us-east-1 certificate validation; the bucket policy references the distribution but the distribution references only the bucket (no cycle); ECS services wait for their listener rules and the capacity providers; the first apply creates services with zero tasks so the missing images and the empty signing-key secret do not break it.
- **Frontend routing.** The frontend makes no server-side API calls, so `/api/*` routed at the ALB is sufficient and the Compose-era `API_PROXY_TARGET` is never used in AWS.
- **Spot interruption.** On SIGTERM the worker releases its job and acknowledges the message; the replacement task's sweeper re-sends the job. The stop timeout (120 s) exceeds the worker's grace (90 s).
- **Health checks.** The ALB checks API liveness, not readiness, so a database blip does not remove every API target.

## Cost review

No finding changes the estimate in [aws-costs.md](aws-costs.md) (about $80 to $105/month idle). Items most likely to surprise:

1. **Public IPv4 addresses**, billed per address per hour: about $18/month across 3 tasks and the ALB.
2. **The ALB** bills hourly even with zero tasks.
3. **RDS** bills while stopped for storage, and restarts itself after 7 days if you stop it.
4. **CloudFront egress** beyond the free tier is the one cost that grows with viewers.
5. **Always-on worker**: Spot makes it cheap, but it runs 24/7 even when the queue is empty.

## What `plan` and the first apply will still have to prove

Provider/API behaviours that only a real run shows: ACM validation timing, CloudFront creation time, whether RDS accepts `engine_version = "17"` as written, the CloudFront response-headers policy shape, and the ECS task-role assumption condition.
