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

| Production image builds (2026-11-10, `docker build --network host --target <t>`, 20 min timeout) | **Run: OK, all three.** `api` (target `prod`) 157 s, 30.3 MB; `worker` (target `worker`, includes `apk add ffmpeg`) 49 s, 212 MB; `frontend` (target `prod`, `npm ci` strict) 466 s, 354 MB. Not run: the containers from these images were not started (the Compose stack uses the dev targets). buildx prints `fatal: bad object HEAD` after each build because the repo is damaged (see incident); it does not affect the build. |
| Full local suite re-run on final code (2026-11-10) | **Run: OK.** Stack started with `docker compose up -d` (no volumes touched). Readiness 200; gofmt/build/vet OK; `go test ./... -count=1` 9 packages `ok`, 0 FAIL; transcode (real FFmpeg) ok; S3/MinIO integration ok; frontend typecheck OK and 14 files / 106 tests pass; `validate-terraform.sh` "configuration is valid"; smoke passed; upload E2E 23 checks; processing E2E 35 checks; `CRASH_TEST=1` processing 43 checks. |

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
| Pre-plan review of runbook, workflow, IAM, network and cost (repository only; no AWS access) | **Done**: [aws-audit.md](aws-audit.md) findings 1-23 |
| AWS CLI v2 installed (2.39.5; `aws login` and `export-credentials --format env-no-export` present) | **Run: OK** (owner's terminal output) |
| Keyless login (IAM user + MFA, `aws login`; no access keys) | **Run: OK** (owner's output, 2026-11-10); session re-checked valid 2026-11-13 (read-only, below) |
| `export-credentials` returned the four credential variable names | **User-reported OK** (not re-run) |
| `scripts/aws-terraform.sh version` printed `Terraform v1.10.5` | **User-reported OK** (not re-run) |
| `scripts/validate-terraform.sh` -> "Success! The configuration is valid." | **User-reported OK**; **re-run by me 2026-11-13: OK** (`fmt -check`, `init -backend=false`, `validate`, Terraform 1.10.5 in Docker) |
| Domain purchased | **User-reported** 2026-11-13. Not verifiable from this account: not in Route 53 Domains (below) |
| Hosted zone created / DNS delegated / ACM certificates validated | **Not done** (no zone exists; no ACM certificates exist) |
| Account plan (Free vs Paid) | **Unverified**: not readable from the CLI; check Billing → Account plan |
| Root MFA on | **Reported on by the owner, but the IAM account summary says off (`AccountMFAEnabled: 0`)**: verify in the console |
| AWS Organization | **Found read-only; explained by the owner (user-reported)**: created when upgrading to use Identity Center; this account is the management account (below) |
| IAM Identity Center | **Instance exists in `us-east-1`** (read-only; none in us-east-2/us-west-2), delegated admin account present. The 2026-11-06 "not available" message predates the upgrade (user-reported) |
| Account-level budgets / billing alerts | **No budgets exist** (read-only); alert preferences unverifiable from the CLI |
| Account upgrade to Paid plan | Not done, not authorised |
| Terraform state bucket created; CloudFront key pair; `backend.hcl`; `terraform.tfvars` | Not done (files absent) |
| `terraform plan` reviewed (including the audit's unverified items) | Not done |
| `terraform apply` (first, `deploy_services = false`) | Not done, not authorised |
| Images pushed, migrations run, services started | Not done, not authorised |
| Cloud checks: runbook step 8 (health, playback, private-media 403s, alarms, SNS confirmation) | Not done |
| GitHub: secret scanning + push protection | **Run: enabled** (read-only 2026-11-13) |
| GitHub: branch ruleset on the default branch | **Run: OK 2026-11-13** (active; no deletion, no force-push; 7 required CI checks; admin bypass) |
| GitHub: `production` environment restricted to `main` (audit finding 1), fork-PR approval for all outside collaborators | Not done (no environment as of 2026-11-13; fork approval last seen as `first_time_contributors`) |
| Deploy workflow run once end to end | Not done |
| `scripts/aws-audit.sh` | Written and syntax-checked; never run against a live stack |
| Teardown rehearsal (`destroy`, then the audit shows nothing left) | Not done |
| Production readiness: backups/restore test, alarms fired once, cost check after one week | Not done |

### Pre-deployment readiness checks (2026-11-13, read-only, run by me with profile `ryucloud`)

Account IDs, ARNs and the account owner's email are redacted or omitted. Account structure per owner (user-reported): the management account's root user hosts `ryucloud-admin`; the Organization was created on upgrade for Identity Center.

| Check | Result |
| --- | --- |
| `sts get-caller-identity` | Valid session; ARN ends `user/ryucloud-admin` (not root). Profile region `us-west-2` |
| IAM user | MFA device registered; 0 access keys; group `ryucloud-admins` has `AdministratorAccess`, `SignInLocalDevelopmentAccess` |
| `iam get-account-summary` | `AccountMFAEnabled: 0`, `AccountAccessKeysPresent: 0` (root) |
| `route53domains list-domains` / `list-operations` (us-east-1) | Both empty |
| `route53 list-hosted-zones` | Empty |
| `acm list-certificates` (us-west-2, us-east-1) | 0 and 0 |
| Resource search | 0 tagged resources in `us-west-2`; 5 in `us-east-1`, all non-RyuCloud (4 Organization SCPs, 1 payment instrument); 0 S3 buckets, 0 EC2 instances, 0 Elastic IPs; 1 VPC (not inspected, presumably the default) |
| `budgets describe-budgets` | None |
| `ce get-cost-and-usage` (2026-11-01 to 11-13) | `-0` USD |
| `freetier get-account-plan-state` | Error "Missing data for account": plan not verifiable |
| `organizations describe-organization` | Exists, feature set `ALL`; caller is the management account. 4 accounts; the one named `RyuCloud` is `SUSPENDED`/`CLOSED` (joined 2026-11-06). Enabled service access includes `sso`, `iam`, `account`. 7 SCPs: root has the managed-account policy (denies `sso:CreateInstance`, leaving the Organization, role changes under `managed/`) and a region policy (us-east-1, us-east-2, us-west-2; in `us-west-2` it denies everything except a 21-service allow-list that does **not** include ECS, RDS, ELB, ECR, SQS, S3 and others). Four `BudgetsSpendLimitDeny*` SCPs are attached to nothing. SCPs do not affect the management account, which is the account this profile uses; this has **not** been proven by an apply. |
| `sso-admin list-instances` | 1 instance in `us-east-1`; 0 in `us-east-2` and `us-west-2` (the earlier us-west-2-only check had missed it) |
| GitHub (`gh api`) | Public repo; secret scanning and push protection enabled; default workflow permission `read`; fork approval `first_time_contributors`; no `production` environment; `main` has no protection or ruleset; CI success on `9eff787` |

## Git repository incident (2026-11-05)

An interrupted commit left `refs/heads/main` pointing at a zero-byte commit object (`3c4eafd…`) and the index cache-tree
pointing at a zero-byte tree (`4eddbbd…`). The last intact commit is `cdf38f2` ("Replace logo with cartoon dragon mascot and
add favicon"), identical to `origin/main`. See the recovery procedure given in the session; update this section when it
is complete.

## Docker build stall diagnosis (2026-11-10)

| Finding | Evidence |
| --- | --- |
| **Lockfile is in sync; CI's `npm ci` is not affected.** | `npm ci` on a scratch copy of `frontend/package.json` + `package-lock.json` (node:22-alpine, same npm 10.9.9) finished: "added 201 packages", exit 0; no EUSAGE/ERESOLVE/sync errors. |
| **Root cause: the Docker default bridge network on this VM is throttled, not a repository defect.** | The VM sits behind a phone-hotspot-style link (CLAT at 192.0.0.1, path MTU about 1400, IPv6 unreachable). The same 31 MB tarball downloads in 2.3 s with `--network host` (about 13 MB/s) but about 5 KB/s on the bridge; `next` alone took 384 s inside the bridge container. MTU 1400 did not fix it; TTL 65 helped partly (about 70-100 KB/s), so the carrier is likely throttling forwarded (NAT) traffic. Not fully proven. |
| Why the build "hung" | `npm ci` hit the slow path and failed; `|| npm install` then re-resolved the whole tree over the same slow path. |
| Fix in repo | `frontend/Dockerfile`: removed the `|| npm install` fallback (it would hide lockfile drift); `npm ci` is strict, with `fetch-retries 3` and `fetch-timeout 120000`. |
| Workaround on this VM | `docker build --network host ...` (all three builds above used it). `docker compose build` has no equivalent flag; use `build: network: host` locally only if needed. |

**Update 2026-11-10:** the damage recurred after the VM crash (VM boot 08:46; 25 zero-byte objects under `.git/objects`, all modified 07:27, including commit `2d0b2c7`). A bare clone of `origin` into scratch space passed `git fsck`, and all 25 objects exist on GitHub; `origin/main` is `2d0b2c7` ("Throttle failed logins, fix Go vulnerabilities…"), so the earlier local work is already pushed. Repair = remove the empty files and re-fetch (commands in the session handoff). Uncommitted at the time: `frontend/Dockerfile`, `docs/verification.md`, `docs/project-status.md`.
