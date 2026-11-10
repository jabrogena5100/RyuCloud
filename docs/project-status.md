# Project status (handoff)

Last updated 2026-11-10. Evidence for every claim is in [verification.md](verification.md); this file says where the project stands.

## Maturity levels

| Level | Status |
| --- | --- |
| **Locally complete** | **Yes.** API, auth, direct-to-storage uploads, Redis queue, FFmpeg/HLS worker, crash recovery, retries, health/readiness, Next.js frontend and Compose stack all work; tests and E2E scripts pass (re-run 2026-11-10). |
| **Cloud-ready** | **Mostly.** Terraform formats and validates offline (1.10.5), workflows and scripts pass static checks, runbook commands corrected. Not proven by a real `plan`. |
| **Cloud-verified** | **No.** Nothing has been planned, applied or deployed. |
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

1. Domain purchase + Route 53 zone (B2). 2. AWS account upgrade decision (B3). 3. IAM admin user + `aws login` (B4). 4. State bucket, signing key, `backend.hcl`, `terraform.tfvars`. 5. GitHub settings (secret scanning, branch protection, `production` environment). 6. Every `plan`/`apply`/deploy.

## Next action

Commit the local work (commands in the session handoff), then start [phase5-plan.md](phase5-plan.md) step 1 (account/authentication, free).
