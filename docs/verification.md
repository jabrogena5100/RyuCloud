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

## Re-verification, 2026-11-10 (Claude Code session; every row below was executed in that session and its output seen)

Environment: Ubuntu VM, Docker 29.9.3, Compose 5.7.0, Node 22.22.1. Go is not installed on the host: Go commands ran in the
Compose `backend`/`worker` containers. The stack had been stopped (VM reboot); `docker compose up -d --build` restarted it, no volume was touched.

| Check | Result |
| --- | --- |
| `go build ./...`, `gofmt -l .`, `go vet ./...` (Go 1.25.14 image) | **Run: OK** (build ok, gofmt printed nothing, vet ok) |
| `go test ./... -count=1` | **Run: OK** (all 9 test packages `ok`; `internal/server` 20 s) |
| `worker go test ./internal/transcode -v` (real FFmpeg) | **Run: OK** (all tests PASS, 11.9 s) |
| `S3_INTEGRATION_TEST=1 go test ./internal/integrations -run S3` (real MinIO) | **Run: OK** (3 tests PASS; was "Not run" before) |
| Frontend `npm run typecheck` / `npm test` | **Run: OK** (exit 0; 14 files, 106/106 tests) |
| `terraform fmt -check`, `init -backend=false`, `validate` with Terraform 1.10.5 in Docker | **Run: OK** ("configuration is valid"); also after the `aws_region` default change |
| `/health`, `/health/ready` | **Run: OK** (`0.4.0`, `schema_version: 3`) |
| `scripts/smoke-test.sh` | **Run: OK** |
| `scripts/e2e-upload-test.sh` | **Run: OK** (23 checks) |
| `scripts/e2e-processing-test.sh` | **Run: OK** (35 checks) |
| `CRASH_TEST=1 scripts/e2e-processing-test.sh` | **Run: OK** (43 checks, worker SIGKILL recovered) |
| `actionlint` 1.7.7 on `.github/workflows` | **Run: OK** (no findings) |
| `shellcheck` 0.10.0 on the files CI checks (`aws-*.sh`, `validate-terraform.sh`) | **Run: OK** |
| `shellcheck` on `scripts/e2e-*.sh` (not checked by CI) | **Run: info/style findings only** (SC2015, SC2034, SC2001); not fixed, they are test helpers |
| `gitleaks` 8.21.2 over all 15 commits | **Run: OK** ("no leaks found"). `.env`, `*.tfvars`, `backend.hcl` are untracked and ignored. |
| `govulncheck` v1.1.4 (after fix: Go 1.26.10, pgx 5.9.2, x/text 0.41.0, x/sync 0.22.0) | **Run: OK, "Your code is affected by 0 vulnerabilities"**. Before the fix: 13 reachable vulnerabilities (Go 1.25.14 stdlib: net/http, crypto/tls, crypto/x509, mime/multipart; `x/text` 0.26.0; `pgx` 5.7.5).  |
| `npm audit --omit=dev` | **Run: 2 findings** (1 high, 1 moderate): `next` bundles a vulnerable `postcss` (source-map file read; build-time CSS processing, not reachable from request handling). The only offered fix is `next@16`, a breaking major upgrade. **Accepted for now**, see [project-status.md](project-status.md). |

| Production image builds (`docker build --target prod` for api) | **Run: OK** for `api`. **Not verified:** the `worker` production image (its `apk add ffmpeg` step stalled for 24 min and was stopped; the worker-dev image, which also installs FFmpeg, built fine) and the `frontend` prod image (not started). CI's `images` job covers all three. |

Defects found and fixed in this session:

1. `docs/aws-runbook.md` used `tf -chdir=infra/tf output ...` (no such directory) in 5 commands; corrected to `infra/terraform`.
2. `scripts/validate-terraform.sh` ran whatever Terraform was on the host (1.17.1 here), contradicting the 1.10.5 pin; it now uses Docker 1.10.5 when the host version differs.
4. Fixed the 13 govulncheck findings by moving the toolchain image to Go 1.26.10 and bumping pgx, x/text and x/sync; after that `go vet`, `go test ./...` (all 9 packages), the transcode tests, smoke, upload (23) and `CRASH_TEST=1` processing (43 checks) were rerun on the new code and passed.
3. No limit on failed sign-ins: added a per-email limiter (10 failures / 15 min, 429 `too_many_login_attempts`, `Retry-After`), with unit and HTTP tests (`TestRepeatedFailedLoginsAreThrottled` PASS).

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
