# Deployment log

One row per temporary AWS session ([temporary-deployment.md](temporary-deployment.md)). Fill it in at the end of each session from real
output. Redact account IDs and ARNs. Screenshots go in `docs/evidence/` (redacted).

| Date | Git commit | Hours up | Apply OK? | Verified (health, playback, 403s) | Destroy OK? | `aws-audit.sh` clean? | Next-day cost (Cost Explorer) | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-11-14 | `296c2bf` plus uncommitted docs | not recorded | **Yes, after recovery**: first apply failed twice on expired credentials; final apply `22 added, 1 changed, 0 destroyed`; `deploy_services = false` | Yes: health, readiness (schema 3), frontend 200, upload, processing (2 renditions), playback (after a signed-cookie fix, commit `0dda14b`), unsigned media 403 on CloudFront and S3 | **Yes**, staged destroy, 94 destroyed | **Direct checks clean; tag search stale** (19 entries, all verified gone or inactive; script flaw) | Not yet | Delegation had to be corrected first (registration created a second zone); two `ExpiredToken` failures recovered with `state push`; see verification.md. Session ended with the stack destroyed (hours up not recorded). Only the state bucket, zones and domain remain |

## Before the first session (state on 2026-11-13)

No session had run. Nothing existed in AWS for this project (read-only checks, [verification.md](verification.md)). Domain purchased (user-reported);
no hosted zone, delegation, budgets, state bucket, `backend.hcl` or `terraform.tfvars` yet. See the checklist in [phase5-plan.md](phase5-plan.md) §8.
