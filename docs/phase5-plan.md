# Phase 5: AWS deployment, cloud verification and production readiness

Phase 4 (local implementation and verification) is complete; see [verification.md](verification.md). Phase 5 is
everything that needs a real AWS account. **Status (2026-11-13): preparation only. No application resources exist in AWS (read-only checks found none).
The domain has been purchased (user-reported) but is not registered in this account's Route 53 and has no hosted zone yet. Keyless login works. No access keys exist.
The account plan could not be verified from the CLI and no budgets exist yet. The AWS Organization found on 2026-11-13 is explained by the owner (§3, B6).**

## 1. Decisions (recorded 2026-11-06)

| Topic | Decision |
| --- | --- |
| Region | `us-west-2` (Oregon). The CloudFront certificate stays in `us-east-1` (a CloudFront requirement, handled by the second provider). |
| Domain | **Purchased 2026-11-13** (user-reported; name and registrar are not recorded in the repository). Registration is only the first part of blocker B2: hosted zone, DNS delegation and ACM validation are still open. |
| Alert email | A personal address (value stays out of the repository). |
| GitHub | Public repository, GitHub Free plan. |
| AWS account | The account this profile uses is the **Organization management account** ("Dreamin Management Account"); the owner upgraded the plan to enable IAM Identity Center (user-reported 2026-11-13; plan not verifiable from the CLI, confirm in Billing). Root MFA was reported on, but `iam get-account-summary` returned `AccountMFAEnabled: 0`: check in the console (step 2). |
| Terraform | 1.10.5 in Docker (`scripts/aws-terraform.sh`), the same version as CI. |
| Development model | **Hybrid** (below). |
| Deployment strategy (2026-11-13) | **Temporary sessions only**, no 24/7 until job-search time. Procedure, retain/destroy table, costs for 8 h / 24 h / 7 d, domain advice and the post-destroy audit are in [temporary-deployment.md](temporary-deployment.md); session evidence goes in [deployment-log.md](deployment-log.md). |
| Hard constraints | Do not change the existing Organization, its other accounts or Identity Center; no access keys; no resources, `apply` or deploy without a separate explicit instruction. (The earlier "no Organizations / no upgrade" constraints were superseded: the owner created the Organization and upgraded the account.) |

## 2. Long-term strategy: hybrid development

| Environment | Purpose | Cost |
| --- | --- | --- |
| **Ubuntu VM + Docker Compose** (primary, daily) | All feature work, unit/E2E tests, `scripts/*-test.sh`. MinIO and Redis stand in for S3 and the queue. | $0 |
| **Temporary AWS environment** (`protect_data = false`) | Infrastructure changes, CI/CD, networking, security, monitoring, cloud verification. Created for a working session, then `terraform destroy`. | Cents to a few dollars per session ([aws-costs.md](aws-costs.md)) |
| **Long-running public deployment** (`protect_data = true`) | Only when the recurring cost is acceptable. | About $90–117/month as designed; **not** $10–25 |

Rules: develop and test locally first; use AWS to prove things only AWS can prove (IAM, ALB, CloudFront signing,
SQS, RDS, ECS). Every temporary session ends with the teardown checklist (§8, step 10) and a check that nothing is left running.

## 3. Blockers

| # | Blocker | Status / how it clears |
| --- | --- | --- |
| B1 | **IAM Identity Center was unavailable on the Free plan** (reported 2026-11-06). **Cleared 2026-11-13:** after the upgrade an instance exists in `us-east-1` (read-only check).** The console says: "IAM Identity Center is not available to your project. To access this service, upgrade your account first, and then activate advanced features." (reported by the owner from the console) | **Open, by design not worked around.** Enabling Identity Center as an organization instance would also create an AWS Organization, which you ruled out (and AWS documents that joining/creating an Organization from a Free-plan account upgrades it and ends credit eligibility). The plan avoids Identity Center: see §4. |
| B2 | **Domain, hosted zone, delegation, certificates.** ACM certificates, the ALB's HTTPS listener, the CloudFront alias and the signed cookies all need a name in a Route 53 hosted zone. | **Partly cleared.** Domain **purchased** (user-reported). Still open: the public hosted zone (none exists), delegation of its four name servers at the registrar, then ACM validation, which Terraform performs during `apply`. A first `apply` cannot succeed until the zone is delegated. |
| B3 | **Free-plan service limits are not known to me.** AWS says some services are unavailable on the Free plan and prompt an upgrade; I could not find a list of which, and this stack uses about 20 service types. A partial failure part-way through `apply` is messy. | Open. Decide on the upgrade (§5) **before** the first `apply`; you said not to upgrade now, so it is only prepared. |
| B4 | Keyless CLI login. | **Cleared** (2026-11-10 user output; session re-verified valid 2026-11-13). Sessions are short-lived: re-run `aws login --profile ryucloud --remote` when it expires. |
| B6 | **An AWS Organization exists** (found 2026-11-13, read-only). **Explained by the owner (user-reported):** while signing up, two "projects" were created that turned out to be separate accounts; to use IAM Identity Center the account was upgraded and an Organization was created with a second Google account as its manager. The root user of the management account ("Dreamin Management Account") hosts the IAM user `ryucloud-admin`. Verified read-only: feature set `ALL`; four accounts (management, `Dreamin Identity Delegated Admin`, `Big Ideas Live Here`, and the original `RyuCloud` account, now closed); an Identity Center instance exists in **us-east-1** with the delegated-admin account; root-level SCPs include a managed-account policy and a region policy that allows only us-east-1, us-east-2, us-west-2 and, in `us-west-2`, a 21-service allow-list **without** ECS, RDS, ELB, ECR, SQS or S3. | **Understood; one design consequence.** SCPs do not apply to the management account, so deploy RyuCloud **in this account** (a member account such as `Big Ideas Live Here` would be blocked by that allow-list). Unproven until a plan/apply. Do not modify the Organization, SCPs or the other accounts. Still to check in the console: Billing → Account plan, and root MFA. |
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

## 8. Phase 5 checklist (in execution order)

Legend: **[x]** done with evidence; **[u]** reported by the owner, not re-run by me; **[~]** partly done; **[ ]** not done. **GATE** = wait for explicit approval; **BILLS** = costs money.
"Read-only 2026-11-13" evidence was gathered with AWS CLI Describe/List calls and `gh api` GETs in the repository session; details are in [verification.md](verification.md).
Each step has an exit test. Stop at any failed test.

**Step 1: Domain and DNS (domain purchase BILLS yearly; zone BILLS $0.50/month)**
- [x] Domain purchased (**[u]**, 2026-11-13). The name and registrar are not recorded in the repository (the name is not secret; it goes in the git-ignored `terraform.tfvars`).
- [x] Read-only 2026-11-13: `route53domains list-domains` and `list-operations` are empty and `route53 list-hosted-zones` is empty. So the domain is **not registered through this account's Route 53** (registered elsewhere or in another account: unverified) and **no hosted zone exists**.
- [ ] Create the public hosted zone for the name you will put in `domain_name` (GATE, $0.50/month; console or `aws route53 create-hosted-zone`). Keep it between sessions.
- [ ] Delegation: put the zone's four `awsdns-` name servers at the registrar. Exit: `dig +short NS <domain>` lists them. (Registration is not delegation; it can take minutes to hours.)
- [ ] Certificates: nothing to create by hand. Terraform requests two ACM certificates (the app host in `us-west-2`, `media.<host>` in `us-east-1`) and their DNS validation records; they validate during `apply` only if delegation works. Read-only 2026-11-13: no ACM certificates exist in either region.
- Supported hostnames: `domain_name` may be the apex (`example.com`) or a subdomain (`ryucloud.example.com`). Media is `media.<domain_name>`. The signed-cookie domain is `domain_name` itself (`ecs.tf`, `config.go`).

**Step 2: AWS account and billing readiness (free; upgrade is a GATE)**
- [x] Read-only 2026-11-13: session valid; identity `user/ryucloud-admin` (non-root); profile region `us-west-2`; IAM user has an MFA device and **zero access keys**; group has `AdministratorAccess` and `SignInLocalDevelopmentAccess`.
- [u] Earlier checks reported by the owner: `export-credentials` returned the four variable names; `scripts/aws-terraform.sh version` printed `Terraform v1.10.5`; `scripts/validate-terraform.sh` printed "Success! The configuration is valid."
- [x] Read-only 2026-11-13: no application resources (no tagged resources in `us-west-2`; no S3 buckets, EC2 instances or Elastic IPs; no ACM certificates). The five tagged items in `us-east-1` are Organization policies and a payment instrument, not ours. Month-to-date cost reads $0 (shown as `-0`).
- [ ] **Root MFA:** `iam get-account-summary` reported `AccountMFAEnabled = 0` (root keys: 0). This contradicts the earlier report. Verify in the console (Security credentials, signed in as root, or the account's root-access management page) and enable it if missing.
- [~] **Account plan:** the owner upgraded to enable Identity Center (user-reported); not verifiable from the CLI (`freetier get-account-plan-state` returned "Missing data for account"). Read Billing and Cost Management → Account plan.
- [x] **Organization (B6):** explained by the owner (user-reported); instance and accounts verified read-only. Do not create/join/leave/modify anything.
- [ ] **Budgets:** none exist (read-only). Create $20 and $50 actual-spend budgets with email alerts (console or CLI, free), and enable billing alert preferences (not readable from the CLI). Cost Anomaly Detection has a default services monitor.
- [ ] Upgrade decision (GATE): recommended before the first apply (B3). The upgrade charges nothing by itself.

**Step 3: GitHub production-environment security (free)**
- [x] Read-only 2026-11-13: repository is public; secret scanning and push protection are **enabled**; default workflow token permission is `read`; local work pushed and CI green on `9eff787`.
- [ ] Fork-PR approval is `first_time_contributors`; set it to **require approval for all outside collaborators** (Settings → Actions → General).
- [x] Ruleset on the default branch created by the owner and **verified read-only 2026-11-13** (`gh api`): active; blocks deletion and non-fast-forward (force) pushes; requires the 7 CI job checks; bypass for the repository-admin role (always), so direct pushes to `main` still work.
- [ ] No `production` environment exists: create it, restrict to `main`, add yourself as required reviewer (runbook step 10 commands). Exit: the read-back commands there show `main` only and a reviewer.
- [ ] Keep `github_repository` empty in `terraform.tfvars` until the above is verified (audit finding 1). The Deploy workflow is not needed for the first temporary sessions.

**Step 4: Terraform remote-state bootstrap (GATE, about $0; runbook steps 2-3)**
- [ ] State bucket in `us-west-2` (private, versioned, encrypted, public access blocked; needs `--create-bucket-configuration LocationConstraint=us-west-2`). Created by hand so `destroy` can never delete it.
- [ ] CloudFront key pair under `~/.ryucloud/` (private key local only; `*.pem` is git-ignored everywhere); public key at `infra/terraform/cloudfront_public_key.pem`. Back the private key up.

**Step 5: Configure `backend.hcl` and `terraform.tfvars` (free; files are git-ignored)**
- [ ] `backend.hcl` from the template (bucket, `us-west-2`); `terraform.tfvars` from the template: `domain_name`, `hosted_zone_id`, `alert_email`, `deploy_services = false`, `protect_data = false`, `monthly_budget_usd = 60`. Neither file exists yet (checked 2026-11-13).

**Step 6: First reviewed plan (GATE: you approve each plan run; free, read-only against AWS)**
- [ ] `tf init -backend-config=backend.hcl`, then `tf plan -out=tfplan`. Review together: no destroys; resource count; the audit's unverified items (RDS `engine_version = "17"`, CloudFront response-headers policy, ACM timing, ECS role-assumption condition, `us-west-2` on every regional resource); estimated cost still matches §6.
- [x] Offline checks, **run 2026-11-13 by me, Terraform 1.10.5 in Docker**: `scripts/validate-terraform.sh` passed (`fmt -check`, `init -backend=false`, `validate`).

**Step 7: First temporary apply (GATE, BILLS about $0.07/h infrastructure only)**
- [ ] `deploy_services = false`; apply; confirm the SNS email; store the signing key in Secrets Manager (runbook step 5).

**Step 8: Images, migrations, services (GATE, BILLS about $0.12-0.16/h in total)**
- [ ] Runbook steps 6-7 (`docker build --network host`); then `deploy_services = true` and apply.

**Step 9: Real cloud end-to-end test (runbook step 8)**
- [ ] Health, readiness, frontend 200; register/upload/process/play; unsigned media returns 403 on CloudFront and directly on S3; `CloudFront-*` cookies; alarms and dashboard visible.
- [ ] Record real output in [verification.md](verification.md) and a row in [deployment-log.md](deployment-log.md); save redacted screenshots in `docs/evidence/`.
- [ ] Optional: failure drills (kill a worker task; send a bad job to the DLQ).

**Step 10: Teardown and residual-resource audit (DESTRUCTIVE: deletes the database and all videos)**
- [ ] `tf plan -destroy -out=destroy.tfplan`; read it; apply it only when sure. With `protect_data = false` from the start no extra apply is needed.
- [ ] `scripts/aws-audit.sh` reports nothing left (it is syntax-checked only; it has never been run against a live stack). Console double-check of load balancers, RDS, NAT/EIP, CloudFront. State bucket, hosted zone, domain and the manual budgets remain by design.
- [ ] Next day: Billing → Cost Explorer; confirm daily cost is near zero.

**Step 11: Final portfolio documentation**
- [ ] Update [project-status.md](project-status.md), the Phase 5 table in [verification.md](verification.md) and [deployment-log.md](deployment-log.md) from real output only; add redacted screenshots; a short README section linking the architecture, the runbook and the teardown evidence.

**Step 12: Production readiness (only for a long-running deployment)**
- [ ] `protect_data = true`; restore test from an RDS snapshot; alarm drill; RDS CA bundle + `verify-full` (audit #3); CloudTrail; decide on Multi-AZ, WAF, S3 versioning (audit #2); one week of cost data compared with §6; decide whether to keep it running.
