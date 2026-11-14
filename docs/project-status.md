# Project status (handoff)

Last updated 2026-11-14. Evidence for every claim is in [verification.md](verification.md); this file says where the project stands.

## Maturity levels

| Level | Status |
| --- | --- |
| **Locally complete** | **Yes.** API, auth, direct-to-storage uploads, Redis queue, FFmpeg/HLS worker, crash recovery, retries, health/readiness, Next.js frontend and Compose stack all work; tests and E2E scripts pass (re-run 2026-11-10). |
| **Cloud-ready** | **Mostly.** Terraform formats and validates offline (1.10.5), workflows and scripts pass static checks, runbook commands corrected. Not proven by a real `plan`. |
| **Cloud-verified** | **Yes for one session (2026-11-14).** Applied, migrated, services running, upload, processing and playback work, unsigned media is 403 on CloudFront and S3. One application bug (signed-cookie wildcard) was found by the cloud test and fixed. Teardown and the audit script have **not** been run yet; cookie expiry, Safari, failure drills and a restore test are untested. |
| **Production-ready** | **No.** Needs the live checks in [phase5-plan.md](phase5-plan.md) §8 (restore test, alarm drill, one week of cost data). |

## Done this session

- Re-ran the whole local suite (see verification.md); started the stopped stack without touching volumes.
- Security: per-email login throttling (429); 13 reachable Go vulnerabilities cleared (Go 1.26.10, pgx 5.9.2, x/text 0.41.0); secrets scan clean.
- Fixed runbook paths (`infra/tf` -> `infra/terraform`) and made `validate-terraform.sh` honour the Terraform 1.10.5 pin.

## Known issues and accepted risks

| Item | Decision |
| --- | --- |
| `npm audit`: `next` bundles a vulnerable `postcss` (source-map file read during CSS processing, build time only). Fix requires `next@16` (major). | Accepted for now; revisit with a planned Next 16 upgrade and full frontend test run. |
| Login limiter is in memory, per API process, keyed by e-mail | Accepted; one API task today. A WAF or shared store would be the upgrade. |
| `shellcheck` style findings in `scripts/e2e-*.sh` (not in CI scope) | Accepted. |
| Audit findings 1-23 in [aws-audit.md](aws-audit.md) | Unchanged; finding 1 (GitHub `production` environment) needs your GitHub settings. |
| Docker builds stall on the default bridge network in this VM (about 5 KB/s vs 13 MB/s on the host network; see verification.md diagnosis) | Not a repo defect; build with `docker build --network host`. Lockfile verified in sync. |

## Blocked on you (not done, not authorised)

Done (2026-11-14, see verification.md): domain registered in this account, delegation corrected, certificates issued, state bucket, key pair and config files, reviewed plan, first apply with `deploy_services = false`. Then the signing key, images, migrations, services and the end-to-end test (see verification.md). **The stack is running and billing until destroyed.** Remaining, in order ([phase5-plan.md](phase5-plan.md) §8; items 1, 3-5 below are historical):
1. Hosted zone, delegation, ACM (done). 2. Account readiness: (root MFA resolved: Google-federated root, no AWS device needed) confirm account plan in Billing, budgets (the Organization is explained, B6). 3. GitHub `production` environment, branch protection, fork-PR approval. 4. State bucket, signing key. 5. `backend.hcl`, `terraform.tfvars`. 6. Every `plan`/`apply`/deploy.

## Next action

Capture screenshots, then destroy in stages and run the audit ([temporary-deployment.md](temporary-deployment.md) §5), record the result, and commit. Then: failure drills, a restore test, and the cookie-expiry and Safari checks if a longer session is ever run.
