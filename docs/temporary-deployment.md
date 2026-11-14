# Temporary AWS deployments: up, verify, destroy, audit

Decision (2026-11-13): RyuCloud runs on AWS **only for sessions** (testing, demos, portfolio evidence). It is **not** left running 24/7.
A permanent public deployment is planned for when active job applications start; this page is the procedure until then.
Nothing here runs automatically. Every `apply`/`destroy` is typed by the owner. Costs are list-price estimates, not measurements
([aws-costs.md](aws-costs.md)); us-west-2 prices were not confirmed against the AWS Price List.

## 1. What is kept and what is destroyed

| Resource | Lifetime | Why |
| --- | --- | --- |
| GitHub repository, Terraform code, `.terraform.lock.hcl`, docs, [verification.md](verification.md), [deployment-log.md](deployment-log.md) | **Kept** (Git) | This is the portfolio and the way to rebuild. |
| Terraform **state bucket** (S3, versioned, `us-west-2`) | **Kept** | Created by hand, outside the stack, so `destroy` cannot delete it. After a destroy the state is empty but remembers the project. Cents per month. |
| **Route 53 hosted zone** and the **domain** | **Kept** | Referenced by `hosted_zone_id`, not managed by Terraform. Recreating a zone changes its name servers, which would have to be re-delegated at the registrar. $0.50/month. |
| **CloudFront private key** `~/.ryucloud/cloudfront_private_key.pem` (+ public key in `infra/terraform/`, git-ignored) | **Kept** locally, never in Git | The same key pair is reused every session. Back it up (password manager or encrypted drive). |
| `backend.hcl`, `terraform.tfvars` (git-ignored) | **Kept** locally | Back them up too; they contain no secrets, but you need them to redeploy. |
| Account-level budget and billing alerts (made by hand) | **Kept** | The budget inside Terraform is destroyed with the stack. |
| IAM user `ryucloud-admin` (MFA, no keys) | **Kept** | Free. |
| VPC, subnets, security groups, ALB, ECS cluster/services/task definitions, ECR repositories **and images**, RDS, S3 media bucket, SQS queues, CloudFront distribution, ACM certificates and validation records, Secrets Manager secrets, CloudWatch logs/alarms/dashboard, SNS topic, IAM roles, GitHub OIDC provider and deploy role, stack budget | **Destroyed** each session | Recreated by `terraform apply` from the code. |

With `protect_data = false` (set before the **first** apply) the code already makes `destroy` complete in one step: RDS skips the final snapshot,
the media bucket and ECR repositories force-delete their contents, and the secret has no recovery window (`rds.tf`, `s3.tf`, `ecr.tf`, `secrets.tf`).

## 2. How each kind of data is handled on destroy

| Data | On destroy | If you want to keep it |
| --- | --- | --- |
| **RDS** (users, videos, jobs) | Deleted, no snapshot. | Not worth keeping: restoring from a snapshot is not implemented. Re-create demo data each session. For a one-off, take a manual snapshot first (`aws rds create-db-snapshot`); it bills about $0.095/GB-month until you delete it, and the audit script reports it. |
| **S3 media** (originals, HLS renditions) | Deleted (`force_destroy`). | Keep the *source* clips on your VM; re-upload them next session. Do not rely on S3 as an archive. |
| **Terraform state** | The bucket stays; resources are removed from state. | Keep. Never delete the bucket or its versions while a stack exists. |
| **Secrets** | The CloudFront-key secret and the database password secret are deleted immediately (recovery window 0), so names can be reused next session. | The real key stays on your VM; re-store it after every apply (§4, step 4). The DB password is generated fresh each time. |
| **CloudFront / ACM certificates** | Distribution, public key and both certificates are destroyed and re-issued next session (validation takes a few minutes if the zone is delegated). ACM public certificates are free. | Nothing to keep. A destroyed distribution takes 10–20 min. |
| **ECR images** | Deleted with the repositories. | Rebuild and push each session (§4, step 5). Keeping ECR outside the stack would save the rebuild for about $0.05/month, but needs a second Terraform root: a separate architecture decision, not made here. |
| **CloudWatch logs, dashboard, alarms** | Deleted. | Capture evidence **before** destroy (§5, step 1). |
| **SNS email subscription** | Deleted. | A new confirmation email arrives after each apply; alarms stay silent until you click it. |

## 3. One-time setup (before the first session)

1. **Billing (free, root user):** create two budgets, $20 and $50 actual spend, email alerts (**none exist as of 2026-11-13**); turn on billing alert preferences; upgrade to the Paid plan (Billing → Account plan; credits carry over). Root MFA needs no action (Google-federated root; see aws-setup.md §1). Also review the Organization (phase5-plan B6). [phase5-plan.md §5](phase5-plan.md).
2. **Login (done; user-reported, session re-checked valid 2026-11-13):** `aws login --profile ryucloud --remote`, `export AWS_PROFILE=ryucloud`, then `scripts/aws-terraform.sh version` prints `Terraform v1.10.5`.
3. **Domain (purchased 2026-11-13, user-reported) and hosted zone (not created; $0.50/month):** see §8. Registration, zone creation, delegation and certificate validation are separate steps. Exit test: `dig +short NS <your domain>` lists `awsdns-` servers.
4. **State bucket, key pair, config files:** [aws-runbook.md](aws-runbook.md) steps 2–4. In `terraform.tfvars` keep `protect_data = false`, `deploy_services = false`, `monthly_budget_usd = 60`.
5. **GitHub settings** ([phase5-plan.md](phase5-plan.md) step 3; secret scanning and push protection are already on, the rest is open). Leave `github_repository` empty until audit finding 1 is closed.
6. `terraform plan` reviewed once with the owner (phase5-plan step 6). No apply before explicit approval.

## 4. Starting a session (billing starts at apply)

Set an alarm for the end time before you start. Billing: the ALB and RDS start the moment the first apply creates them
(about $0.07/hour with zero tasks), tasks add about $0.05–0.08/hour. Plan on 30–60 minutes of apply time.

```bash
cd ~/projects/RyuCloud
export AWS_PROFILE=ryucloud AWS_REGION=us-west-2
aws sts get-caller-identity --query Arn --output text          # if expired: aws login --profile ryucloud --remote
tf() { ~/projects/RyuCloud/scripts/aws-terraform.sh "$@"; }
export TERRAFORM=~/projects/RyuCloud/scripts/aws-terraform.sh
cd infra/terraform
tf init -backend-config=backend.hcl
tf plan -out=tfplan        # READ: expect only creates on a fresh session; any destroy line means stop
tf apply tfplan            # BILLS (ALB, RDS, IPv4 ...)
```

Then, in order (details: [aws-runbook.md](aws-runbook.md) steps 5–8):

1. Click the SNS confirmation email.
2. Store the CloudFront private key in the new secret (runbook step 5).
3. Build and push images, using the VM workaround `docker build --network host` (runbook step 6; the **committed** tree, clean `git status`).
4. Run migrations, point the services at the images (runbook step 7), set `deploy_services = true`, `tf plan -out=tfplan`, `tf apply tfplan` (**BILLS**: tasks).
5. Verify (runbook step 8): health, readiness, frontend 200, register, upload, process, play, unsigned media 403 on CloudFront and S3, `CloudFront-*` cookies.

## 5. Ending a session (always, even if something failed)

1. **Capture evidence first** (nothing survives the destroy). Paste real output into [verification.md](verification.md) and a row into [deployment-log.md](deployment-log.md);
   save screenshots (the site, playback, CloudWatch dashboard, `terraform output`) under `docs/evidence/YYYY-MM-DD-*.png`. Redact account IDs, ARNs, the
   alert email and any signed URL/cookie value before committing; check images before `git add`.
2. Confirm `protect_data = false` in `terraform.tfvars` (the example sets it). If it was `true`, change it and run `tf apply` first.
3. **DESTRUCTIVE** (deletes the database and every video):

```bash
cd ~/projects/RyuCloud/infra/terraform
tf plan -destroy -out=destroy.tfplan      # read it: only ryucloud resources; no hosted zone, no state bucket
tf apply destroy.tfplan                    # 10–20 min (CloudFront is the slow part)
```

**Staged destroy (recommended; untested).** The 15-minute credential limit ([aws-runbook.md](aws-runbook.md) step 5) can cut a single
destroy off during the CloudFront or RDS deletion. Run these in order, each with fresh credentials, then the full plan for the rest. If one
fails with `ExpiredToken`, use the recovery in the runbook and re-run it:

```bash
tf apply -destroy -target=aws_cloudfront_distribution.media     # slowest
tf apply -destroy -target=aws_db_instance.main
tf plan -destroy -out=destroy.tfplan && tf apply destroy.tfplan  # everything else
```

4. **Audit** (read-only, free):

```bash
cd ~/projects/RyuCloud
scripts/aws-audit.sh                       # must end with "nothing left"; exit status 0
```

5. Console double-check (free): EC2 → Load Balancers, RDS → Databases and Snapshots, VPC → NAT gateways and Elastic IPs, CloudFront → Distributions: all empty.
6. **Next day:** Billing → Cost Explorer → daily view: cost for the day after the destroy should be close to zero (data can lag up to ~24 h). Billing → Bills lists anything still accruing by service.
7. Commit the evidence and docs (commands given in the handoff). `aws logout` when you finish for the day.

If `destroy` fails part-way: do not delete things by hand first. Re-run `tf plan -destroy` and read the error (usual causes: a non-empty bucket
because `protect_data` was `true`, or a dependency still being deleted); fix and re-apply. Run the audit; delete leftovers one at a time only after confirming them.

## 6. Safeguards against unexpected charges

| Safeguard | Where | State |
| --- | --- | --- |
| $20 and $50 account budgets with email | Billing → Budgets (manual) | **To do** (step 3.1) |
| Stack budget at `monthly_budget_usd = 60`, alerts at 80% actual / 100% forecast | Terraform (`monitoring.tf`) | Built; destroyed with the stack |
| Billing alert preferences; Cost Anomaly Detection (optional, free) | Billing → Billing preferences / Cost Anomaly Detection | **To do** |
| End-time alarm on your phone for every session | You | Habit |
| Tag-based search plus a service-by-service audit, with a clean exit status | `scripts/aws-audit.sh` | Written; **not run yet** |
| Next-day Cost Explorer check | Console | Part of §5 |
| No NAT gateway, no Multi-AZ, no WAF, `deploy_services` gates the Fargate cost, RDS storage autoscaling capped | Terraform defaults | Built |
| Daily rule: never leave the stack overnight. If you must pause: `deploy_services = false` removes tasks only (≈ $50/month remains); destroy instead. | Policy | |

What the audit cannot see: charges in other regions or other services you may start by hand, support plans, marketplace items. The Billing → Bills page
shows the whole account by service and is the final word.

## 7. Cost estimates (us-west-2, list prices, unmeasured)

Hourly cost with everything running: about **$0.12–0.16** (ALB, five public IPv4 addresses, RDS `db.t4g.micro`, two small tasks, a worker on Spot or on-demand,
plus prorated storage, secrets and alarms). Apply and destroy add roughly an hour at the infrastructure-only rate (≈ $0.07/h), so about **+$0.10** per session.

| Deployment | Running cost | Persistent items in that period | Notes |
| --- | --- | --- | --- |
| **8 hours** | ≈ **$1.1–1.4** | Route 53 zone $0.50 per month it exists; state bucket under $0.05; domain yearly | Realistic demo session |
| **24 hours** | ≈ **$3.0–3.9** | same | Overnight is where forgetting hurts |
| **7 days** (168 h) | ≈ **$20–27** | same | Matches the earlier ≈ $90–117/month for 24/7 (that figure ÷ 730 × 168) |

Persistent costs outside sessions: zone $6/year, state bucket under $1/year, domain **registrar price per year (not checked; roughly $12–15 for a .com, less for some TLDs)**.
So domain + zone ≈ **$18–21/year before any session**. Usage costs (S3, CloudFront) for a few demo clips are cents (CloudFront's first 1 TB/month is free).
Rough yearly total for five 8-hour sessions: about $6–7 of sessions plus about $18–21 fixed, i.e. **just over $20**. A cheaper TLD moves it under; a
seven-day run adds about $20–27. Credits (Free plan, up to $200) pay for usage while they last; verify the balance in Billing.

## 8. Buy the domain now or later?

**Decision: bought on 2026-11-13 (user-reported).** The reasoning for buying before the first session, kept for the record: **buy it now.** Deferring does not make a session cheaper, it makes a *full* session impossible: ACM certificates, the ALB's HTTPS listener,
the CloudFront alias and the signed cookies (which cannot work on `*.cloudfront.net` or the load balancer's name) all need a domain in a Route 53 zone
([phase5-plan.md](phase5-plan.md) blocker B2). Without it you could run only a partial test, which does not prove the architecture. Deferring saves
the yearly fee only for as long as you also forgo AWS verification.

- Buy the name you intend to keep for the permanent deployment, with a cheap TLD, so you do not pay for two. Compare **renewal** price with the first-year price.
- **Easiest:** register in Route 53 (it creates the hosted zone and delegates it for you). **Possibly cheaper:** another registrar, then create the zone and enter its four NS records there.
- Use `ryucloud.<tld>`; the app is the apex name in `domain_name` and media is `media.ryucloud.<tld>`. Tell me the final name and I will give you the exact `terraform.tfvars` lines.
- Registration is a yearly charge that does not stop when the stack is destroyed.

## 9. Where to pay

| What | Where | Frequency |
| --- | --- | --- |
| AWS usage, Route 53 zone, state bucket | Billed to the card on the AWS account: Billing and Cost Management → **Payment preferences** (add or change the card); invoices in **Bills** | Monthly in arrears |
| Paid plan upgrade (no charge by itself) | Billing and Cost Management → **Account plan** | Once |
| Domain (Route 53) | Route 53 → **Registered domains → Register domains**, paid with the same card | Yearly |
| Domain (other registrar) | That registrar's checkout | Yearly |
| Terraform, GitHub (Free plan, public repo), Docker, ACM certificates | Nothing to pay | |

## 10. Redeploy later: what is already repeatable and what is not

Repeatable from Git: all infrastructure (`tf apply`), the images (`docker build`, runbook step 6), migrations, the GitHub Deploy workflow (after audit finding 1).

Manual per session, by design (cost: about 20 minutes of your time): confirm the SNS email, store the CloudFront key in Secrets Manager,
build/push images, create demo data (there is no seed script: register a user and upload a short clip). A seed or "session up/down" wrapper script
is a reasonable next step once the first session has run, and not before, so it matches what actually happened.

Untested (do not rely on until a session proves it): that a second apply after a destroy is clean (secret and certificate names reused),
destroy completing in one pass, and the audit script's queries on a live account.
