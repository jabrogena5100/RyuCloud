# Phase 5: AWS deployment, cloud verification and production readiness

Phase 4 (local implementation and verification) is complete; see [verification.md](verification.md). Phase 5 is
everything that needs a real AWS account. **Status: preparation only. Nothing exists in AWS, no domain is registered,
no Identity Center or Organizations has been enabled, no account upgrade has been made, no access keys exist.**

## 1. Decisions (recorded 2026-11-06)

| Topic | Decision |
| --- | --- |
| Region | `us-west-2` (Oregon). The CloudFront certificate stays in `us-east-1` (a CloudFront requirement, handled by the second provider). |
| Domain | Buy one eventually, **not yet**. The stack cannot be applied without one (blocker B2). |
| Alert email | A personal address (value stays out of the repository). |
| GitHub | Public repository, GitHub Free plan. |
| AWS account | New; on the AWS **Free plan**. Root MFA is on. |
| Terraform | 1.10.5 in Docker (`scripts/aws-terraform.sh`), the same version as CI. |
| Development model | **Hybrid** (below). |
| Deployment strategy (2026-11-13) | **Temporary sessions only**, no 24/7 until job-search time. Procedure, retain/destroy table, costs for 8 h / 24 h / 7 d, domain advice and the post-destroy audit are in [temporary-deployment.md](temporary-deployment.md); session evidence goes in [deployment-log.md](deployment-log.md). |
| Hard constraints | No Organizations, no Identity Center activation, no account upgrade, no access keys, no domain purchase, no resources, no `apply`/deploy without a separate explicit instruction. |

## 2. Long-term strategy: hybrid development

| Environment | Purpose | Cost |
| --- | --- | --- |
| **Ubuntu VM + Docker Compose** (primary, daily) | All feature work, unit/E2E tests, `scripts/*-test.sh`. MinIO and Redis stand in for S3 and the queue. | $0 |
| **Temporary AWS environment** (`protect_data = false`) | Infrastructure changes, CI/CD, networking, security, monitoring, cloud verification. Created for a working session, then `terraform destroy`. | Cents to a few dollars per session ([aws-costs.md](aws-costs.md)) |
| **Long-running public deployment** (`protect_data = true`) | Only when the recurring cost is acceptable. | About $90–117/month as designed; **not** $10–25 |

Rules: develop and test locally first; use AWS to prove things only AWS can prove (IAM, ALB, CloudFront signing,
SQS, RDS, ECS). Every temporary session ends with the teardown checklist (§8, step 9) and a check that nothing is left running.

## 3. Blockers

| # | Blocker | Status / how it clears |
| --- | --- | --- |
| B1 | **IAM Identity Center is unavailable on the Free plan.** The console says: "IAM Identity Center is not available to your project. To access this service, upgrade your account first, and then activate advanced features." (reported by the owner from the console) | **Open, by design not worked around.** Enabling Identity Center as an organization instance would also create an AWS Organization, which you ruled out (and AWS documents that joining/creating an Organization from a Free-plan account upgrades it and ends credit eligibility). The plan avoids Identity Center: see §4. |
| B2 | **No domain.** ACM certificates, the ALB's HTTPS listener, the CloudFront alias and the signed cookies all need a name in a Route 53 hosted zone. | Open. Needs: a domain, a hosted zone, delegation. A first `apply` cannot succeed without it. A throw-away `plan` with a fake zone ID can be read for review but proves little. |
| B3 | **Free-plan service limits are not known to me.** AWS says some services are unavailable on the Free plan and prompt an upgrade; I could not find a list of which, and this stack uses about 20 service types. A partial failure part-way through `apply` is messy. | Open. Decide on the upgrade (§5) **before** the first `apply`; you said not to upgrade now, so it is only prepared. |
| B4 | **No keyless CLI login configured.** The CLI is installed (2.39.5) but not authenticated. | Open: needs an IAM user (free) and `aws login` (§4). |
| B5 | State bucket, CloudFront key pair, `backend.hcl`, `terraform.tfvars`, GitHub `production` environment | Open; each is a numbered step in §8. |

## 4. Authentication without root, access keys or Identity Center

| Option | Long-lived secret? | Verdict |
| --- | --- | --- |
| Root user for the CLI/Terraform | Root password + MFA | **No.** Unrestricted, no guardrails, and AWS advises against it. `aws login` technically allows root; do not. |
| IAM user + **access keys** | Yes (static keys on disk) | **No.** The thing to avoid. Do not create keys. |
| IAM Identity Center (`aws configure sso`) | No | Blocked (B1). Possible later after an upgrade, if you accept an Organization. |
| **IAM user + `aws login` (console credentials)** | **No.** Console password + MFA; the CLI receives short-lived credentials that it refreshes. | **Recommended.** Needs CLI ≥ 2.32 (yours is 2.39.5; `aws login` is present). IAM is free and available on every plan. |
| AWS CloudShell | No (uses the console session) | Good fallback for one-off commands. Storage is limited and the Docker-pinned Terraform workflow here would need re-checking there. |
| GitHub Actions → OIDC role | No | Already in `github_oidc.tf`; for CI only, and only after audit finding 1 is closed. |

**Setup (console, free; you do this, I do not):**
1. IAM → **User groups** → create `ryucloud-admins`; attach the managed policies **AdministratorAccess** and **SignInLocalDevelopmentAccess**.
   (Terraform creates IAM roles, so a narrow policy does not work yet; narrowing it is a later hardening task. The safety comes from MFA, no access keys, and short sessions.)
2. IAM → **Users** → create `ryucloud-admin` with console access, add it to the group, and register an **MFA device** on that user.
   **Do not create an access key.** (IAM → user → Security credentials should list zero access keys.)
3. Sign out of root. Sign in as `ryucloud-admin` for everything from now on. Keep root only for billing/account tasks.

**On the VM:**
```bash
aws login --profile ryucloud           # add --remote if the VM has no browser; paste the code it asks for
export AWS_PROFILE=ryucloud
aws sts get-caller-identity            # ARN should end in user/ryucloud-admin
# The Terraform wrapper depends on this working: print only the variable NAMES, never the values.
aws configure export-credentials --format env-no-export | cut -d= -f1
```
Expect `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_CREDENTIAL_EXPIRATION` and `AWS_SESSION_TOKEN`. (Unverified until you run it. AWS notes
that static keys in `~/.aws/credentials` override login sessions; there should be none.) Sign out with `aws logout`.

## 5. Free plan vs Paid plan

What AWS documents (sources: AWS Billing docs, "Choosing an AWS Free Tier plan"; AWS Free Tier FAQs; AWS Free Tier terms):
- A new account's Free plan gives credits (up to $200) for a limited period, then must be upgraded to Paid.
- Some services are unavailable on the Free plan and prompt an upgrade; the Paid plan removes those limits.
- Unused credits carry over after an upgrade and remain usable for 12 months from sign-up.
- **Creating or joining an AWS Organization (or Control Tower) from a Free-plan account upgrades it and ends credit eligibility.** Upgrade deliberately *first* if you ever want one.

What this means for RyuCloud (my assessment; not verified against your account):
- The stack uses ALB, ECS/Fargate, RDS, CloudFront, ACM, Route 53, S3, SQS, Secrets Manager, CloudWatch, Budgets, ECR and IAM.
  I cannot tell from here which of these the Free plan blocks. **Treat the upgrade as a prerequisite to the first `apply`**, not an option, so
  that you do not discover a restriction half-way through an apply.
- The upgrade needs a payment method and stops the Free-plan spending cap. Credits still pay for usage until they run out.
- Upgrading does not enable Identity Center or Organizations by itself; those remain separate, explicit choices that this plan does not make.
- Safe way to find out more before committing: Billing console → the account plan page (read-only), and `aws sts get-caller-identity`. Neither costs anything.

Before upgrading: create an account-level budget by hand (Billing → Budgets, the first two are free) with email alerts at, say, $20 and $50 actual, and turn on billing alerts.

## 6. Cost summary

Details and arithmetic are in [aws-costs.md](aws-costs.md). Corrected figures (estimates from list prices; us-west-2 not yet checked against the Price List):

| Mode | Estimate |
| --- | --- |
| Everything running 24/7 | **≈ $90–117 / month** (the earlier "80–105" understated it) |
| Infrastructure up, `deploy_services = false` (no tasks) | **≈ $50 / month** |
| Temporary session, whole stack up | **≈ $0.11–0.15 per hour** (≈ $0.45–0.60 for 4 h) plus about 30–60 min of apply/destroy time |
| Torn down | Route 53 zone $0.50/month, domain registration yearly, state bucket cents |

A $10–25/month budget is **not achievable with this architecture running continuously**: the floor with zero tasks is already about $50.
It is achievable only through temporary sessions, or by a different design (for example one small EC2 instance running Compose without an ALB or
RDS, which would be a separate architecture decision, not costed here).

## 7. Security and deployment audit summary

Full list: [aws-audit.md](aws-audit.md). New in Phase 5:
- **GitHub (public, Free):** environment branch restrictions and required reviewers are available for public repositories. Audit #1 stays open until
  you configure and read back the `production` environment (runbook step 10). Also enable secret scanning + push protection, require approval for all
  outside-collaborator workflow runs, read-only default `GITHUB_TOKEN`, and branch protection with required CI checks on `main`.
  Environment variables (role ARN, cluster, subnet IDs) are readable by anyone on a public repo; none is a credential, but the role ARN contains your account ID.
- **OIDC:** trust is pinned to one repository's `production` environment with `aud = sts.amazonaws.com`. The role cannot read secrets or data; it can register task
  definitions and update three services. Keep `github_repository` empty until the environment is verified.
- **Network (read from `network.tf`, `alb.tf`, `rds.tf`):** the ALB accepts 80/443 from the internet and can send only to the app ports; the tasks accept only the ALB's
  security group and send only HTTPS (anywhere) and PostgreSQL (to the DB group); the database accepts only the tasks, sits in route-less subnets and is not publicly
  accessible; the default security group is emptied; the S3 gateway endpoint is free. No NAT, so tasks hold public IPs. No WAF.
- **Account:** root has MFA; day-to-day access is an MFA-protected IAM user with no access keys; add CloudTrail once you go long-running.

## 8. Phase 5 checklist

Each step has an exit test. Stop at any failed test. Steps marked **BILLS** cost money; **GATE** means wait for explicit approval.

**Step 0: Local preparation (free)**
- [ ] Commit the preparation work (commands are in the handoff of the session that wrote this).
- [ ] Optional: MinIO S3 integration test (see README "Running tests").
- [ ] GitHub repository settings (browser): secret scanning + push protection on; Actions → General → "Require approval for all outside collaborators"; workflow
  permissions "Read repository contents"; ruleset/branch protection on `main` requiring the CI checks; create the `production` environment restricted to `main`
  (runbook step 10 commands). Exit: the read-back commands in step 10 show `main` only and a required reviewer.

**Step 1: Account and authentication (free)**
- [ ] Billing: create the account-level budget and alerts (§5).
- [ ] Create the IAM group/user with MFA and no access keys (§4); sign out of root.
- [~] `aws login --profile ryucloud`; `aws sts get-caller-identity` shows `user/ryucloud-admin` (**done 2026-11-10**, owner's output, account ID redacted); the `export-credentials` name check in §4 is **not yet run**.
- [ ] `scripts/aws-terraform.sh version` prints `Terraform v1.10.5` (first real test of the wrapper).

**Step 2: Upgrade decision (GATE, BILLS potentially)**
- [ ] Read the Billing account-plan page. Decide whether to upgrade to Paid (recommended before apply; credits carry over). Do not enable Organizations.

**Step 3: Domain (GATE, BILLS)**
- [ ] Choose and buy the domain (Route 53 registrar or another; yearly fee). Create the public hosted zone for the app name (about $0.50/month).
- [ ] If the domain is registered elsewhere, add the zone's four NS records there. Exit: `dig +short NS <your name>` lists the `awsdns-` servers.

**Step 4: Bootstrap (runbook steps 2–4)**
- [ ] State bucket in `us-west-2` (GATE; about $0). Needs `--create-bucket-configuration LocationConstraint=us-west-2`.
- [ ] CloudFront key pair under `~/.ryucloud/` (local only). Private key never committed (`*.pem` is ignored everywhere).
- [ ] `backend.hcl` and `terraform.tfvars` from the templates; `protect_data = false` for a temporary environment.

**Step 5: First plan, no apply (GATE: you approve each plan run)**
- [ ] `tf init -backend-config=backend.hcl`, then `tf plan -out=tfplan`. Together review: nothing destroyed; resource count; and the audit's unverified items:
  RDS `engine_version = "17"` accepted, CloudFront response-headers policy, ACM timing, ECS role-assumption condition, region is `us-west-2` on every regional resource.
- [ ] Exit: plan reviewed and the estimated cost still matches §6.

**Step 6: First apply (GATE, BILLS ≈ $0.11/h plus fixed items)**
- [ ] `deploy_services = false`; apply; confirm the SNS email; store the signing key in Secrets Manager (runbook step 5).

**Step 7: Images, migrations, services (GATE, BILLS)**
- [ ] Runbook steps 6–7; then `deploy_services = true` and apply.

**Step 8: Cloud verification (runbook step 8)**
- [ ] Health, readiness, frontend 200; register/upload/process/play; unsigned media request returns 403 both on CloudFront and directly on S3; `CloudFront-*` cookies; alarms and dashboard visible.
- [ ] Record outputs in [verification.md](verification.md) (paste real output; update the Phase 5 table).
- [ ] Optional: failure drills (kill a worker task; send a bad job to the DLQ) and a CI/CD run through the Deploy workflow.

**Step 9: Teardown of a temporary environment (DESTRUCTIVE: deletes the database and all videos)**
- [ ] `tf plan -destroy -out=destroy.tfplan`; read it; apply it only when you are sure. With `protect_data = false` from the start no extra apply is needed.
- [ ] Check nothing is left: `aws resourcegroupstaggingapi get-resources --tag-filters Key=Project,Values=ryucloud --region us-west-2` (read-only), plus the console for the
  load balancer, RDS and NAT/EIP pages. The state bucket, hosted zone, domain and the manual budget remain by design.
- [ ] Run `scripts/aws-audit.sh` (read-only); it must report nothing left. See [temporary-deployment.md](temporary-deployment.md) §5.
- [ ] Next day: Billing → Cost Explorer; confirm daily cost is near zero.

**Step 10: Production readiness (only for a long-running deployment)**
- [ ] `protect_data = true`; restore test from an RDS snapshot; alarm drill; RDS CA bundle + `verify-full` (audit #3); CloudTrail; decide on Multi-AZ, WAF, S3 versioning (audit #2);
  one week of cost data compared with §6; decide whether to keep it running.
