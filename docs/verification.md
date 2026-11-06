# Verification record

This file separates **evidence that exists** from **checks nobody has independently run**. Update it as results come in;
paste real command output or a link, never a summary from memory.

Legend: **Reported** = stated by the repository owner, raw output not stored in the repo. **Run** = executed and the
output seen by whoever recorded it. **Not run** = no evidence.

## Phase 3 (local processing pipeline): historical evidence

| Check | Status | Source | Notes |
| --- | --- | --- | --- |
| End-to-end test script reported 35 successful checks | **Reported** | Repository owner, earlier session | Which script (`e2e-upload-test.sh`, `e2e-processing-test.sh`, or both combined), the date and the commit it ran against were not recorded. The scripts print `passed (N checks)` on success; the owner's original terminal output, if kept, should be pasted here. |

This result is preserved as history. It describes the code as it was then, **before any Phase 4 change**, and it was
not re-observed in the session that wrote Phase 4.

## Phase 4: current state

"Run" results below were produced by the repository owner on the local VM and pasted into the session; they are
recorded as reported, with the command used.

| Area | Status | Evidence / notes |
| --- | --- | --- |
| Go dependency pin (`service/sqs@v1.52.10`) | **Run: OK** | `scripts/check-go-deps.py`: 693 modules, nothing needs Go newer than 1.25.0. Side effect: `aws-sdk-go-v2` 1.47.6→1.47.7, `internal/configsources` 1.5.9→1.5.10, `internal/endpoints/v2` 2.8.9→2.8.10. |
| `go build ./...`, `gofmt -l .`, `go vet ./...` | **Run: OK** | All exit 0, `gofmt` printed nothing. |
| `go test ./... -count=1` | **Run: OK** | All 9 test packages `ok`, exit 0. One pre-existing test (`TestNewQueueSelectsBackend`) used `sqs` as its "unknown backend" example and was updated now that SQS exists. |
| FFmpeg tests (`worker go test ./internal/transcode`) | **Run: OK** | Real probe/transcode/thumbnail/cancel tests pass. |
| `tsc --noEmit` | **Run: OK** | exit 0. |
| Frontend `npm test` | **Run: OK after fix (3 consecutive full-suite runs: 106/106)** | `watch/[id]/page.test.tsx` "polls until processing finishes" was racy (not caused by Phase 4): the page re-fetches right after its first "queued" response, so a test that queued only one "queued" answer could lose the element. Fixed by queueing two (commit `dc74eb1`); the full suite was then re-run 3 times with 106/106 passing. |
| `terraform fmt -check`, `init -backend=false`, `validate` (Terraform 1.10.5 in Docker, AWS provider 6.72.0) | **Run: OK** | fmt exit 0; "Success! The configuration is valid." `validate` checks syntax and provider schemas only; it does not prove a `plan` would succeed. |
| Local Docker Compose rebuild (`backend`, `worker`, `frontend`) and restart | **Run: OK** | All services Up; `GET /api/v1/health` → `0.4.0`; `/health/ready` → `schema_version: 3`; `migrate status` shows migrations 1–3 applied, none new. Volumes untouched. |
| `scripts/smoke-test.sh` | **Run: OK** | "Smoke test passed." |
| `scripts/e2e-upload-test.sh` | **Run: OK** | "End-to-end upload test passed (23 checks)." |
| `scripts/e2e-processing-test.sh` | **Run: OK** | "End-to-end processing test passed (35 checks)." Same count as the Phase 3 baseline above, now observed on the Phase 4 code. |
| Startup log lines showing local defaults | **Run: OK** | `object storage: s3`, `job queue: redis`, `media delivery: api`. |
| Crash-recovery E2E (`CRASH_TEST=1 bash scripts/e2e-processing-test.sh`) | **Run: OK** | "passed (43 checks)": the 35 baseline checks plus 8 crash-recovery checks (worker SIGKILLed mid-job, lease expired, second attempt completed). |
| MinIO S3 integration tests (`S3_INTEGRATION_TEST=1`) | **Not run** | Optional. |
| Terraform security/cost/dependency audit | **Done (read-only review)** | See [aws-audit.md](aws-audit.md). Findings are unverified by `plan`. |
| `terraform plan` / `apply`, AWS deployment | **Not done, not authorised** | |

## Phase 5: AWS deployment, cloud verification and production readiness

Phase 4 (local implementation and verification) is complete. Phase 5 is everything that needs a real AWS account; its plan,
decisions, blockers and checklist are in [phase5-plan.md](phase5-plan.md). Each item stays **Not done** until output is pasted here.

| Step | Status |
| --- | --- |
| Pre-plan review of runbook, workflow, IAM, network and cost (repository only; no AWS access) | **Done**: [aws-audit.md](aws-audit.md) findings 1–23 |
| AWS CLI v2 installed (2.39.5; `aws login` and `export-credentials --format env-no-export` present) | **Run: OK** (owner's terminal output) |
| Account plan: Free; root MFA on | **Reported** by the owner |
| IAM Identity Center | **Blocked**: "not available to your project" on the Free plan (reported); not worked around, Organizations deliberately not enabled |
| Keyless login (IAM user + MFA, `aws login`; no access keys) | Not done |
| Account upgrade to Paid plan | Not done, not authorised |
| Region chosen (`us-west-2`); domain | Region decided; **domain not purchased** (blocker) |
| Terraform state bucket created | Not done, not authorised |
| `scripts/aws-terraform.sh` (Terraform 1.10.5 in Docker) exercised | Not done |
| `terraform validate` after the `aws_region` default change | Not done: run `scripts/validate-terraform.sh` |
| `terraform plan` reviewed (including the audit's unverified items) | Not done |
| `terraform apply` (first, `deploy_services = false`) | Not done, not authorised |
| Images pushed, migrations run, services started | Not done, not authorised |
| Cloud checks: runbook step 8 (health, playback, private-media 403s, alarms, SNS confirmation) | Not done |
| GitHub `production` environment restricted to `main` and verified (audit finding 1) | Not done |
| Deploy workflow run once end to end | Not done |
| Teardown rehearsal (`destroy`, then a tag search shows nothing left) | Not done |
| Production readiness: backups/restore test, alarms fired once, cost check after one week | Not done |

## Git repository incident (2026-11-05)

An interrupted commit left `refs/heads/main` pointing at a zero-byte commit object (`3c4eafd…`) and the index cache-tree
pointing at a zero-byte tree (`4eddbbd…`). The last intact commit is `cdf38f2` ("Replace logo with cartoon dragon mascot and
add favicon"), identical to `origin/main`. See the recovery procedure given in the session; update this section when it
is complete.
