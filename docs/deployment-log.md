# Deployment log

One row per temporary AWS session ([temporary-deployment.md](temporary-deployment.md)). Fill it in at the end of each session from real
output. Redact account IDs and ARNs. Screenshots go in `docs/evidence/` (redacted).

| Date | Git commit | Hours up | Apply OK? | Verified (health, playback, 403s) | Destroy OK? | `aws-audit.sh` clean? | Next-day cost (Cost Explorer) | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-11-14 (in progress) | `296c2bf` plus uncommitted docs | not recorded | **Yes, after recovery**: first apply failed twice on expired credentials; final apply `22 added, 1 changed, 0 destroyed`; `deploy_services = false` | Partly: certificates issued, CloudFront deployed, RDS available, ALB active, SNS confirmed (read-only). Not yet: signing key, images, migrations, health, playback, 403s | Not yet | Not yet | Not yet | Delegation had to be corrected first (registration created a second zone); see verification.md. Infrastructure is still running and billing (about $0.07/hour) |

## Before the first session (state on 2026-11-13)

No session had run. Nothing existed in AWS for this project (read-only checks, [verification.md](verification.md)). Domain purchased (user-reported);
no hosted zone, delegation, budgets, state bucket, `backend.hcl` or `terraform.tfvars` yet. See the checklist in [phase5-plan.md](phase5-plan.md) §8.
