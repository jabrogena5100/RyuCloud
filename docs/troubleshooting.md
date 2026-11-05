# Troubleshooting and lessons learned

Things that broke while getting the Phase 3 stack (Compose, MinIO, Redis, worker, FFmpeg) running, what
actually caused each one, and what we did about it. Blunt on purpose: if you hit the same wall, start here.

Status as of 2026-11-05: `scripts/e2e-processing-test.sh` passed all 35 checks (upload, queue, worker, FFmpeg,
HLS playback, corrupt-file failure path). Not yet confirmed: the `CRASH_TEST=1` step, the frontend Vitest suite
and the Go test suite after the readiness change. Each is called out below.

---

## 1. Correct startup order (read this first)

The stack being "up" does **not** mean it works. The database starts empty and the API does **not** migrate it
for you. The order that works:

```bash
cp .env.example .env            # then set real passwords (see .env.example); never commit .env
docker compose up -d db         # 1. database first
docker compose run --rm backend go run ./cmd/api migrate up    # 2. schema
docker compose up -d --build    # 3. everything else
docker compose run --rm backend go run ./cmd/api migrate status   # sanity check: expect 00001-00003 applied
bash scripts/e2e-processing-test.sh                            # 4. only now run the tests
```

`docker compose run --rm backend ...` also starts what the backend depends on (db, redis, minio, and
`minio-init`), so it is fine to run it before `up -d`.

---

## 2. Registration returned HTTP 500: `relation "users" does not exist`

**Symptom:** the e2e test failed at registration with `Expected HTTP 201, received HTTP 500`. The API log had
`unhandled error ... relation "users" does not exist (SQLSTATE 42P01)`.

**Cause:** the test ran before `migrate up`. Not a code bug. The migration timestamps in `migrate status` were
later than the error in the log.

**Why we didn't notice:** `/api/v1/health/ready` only did a database ping, so it said "ready" against an empty
database.

**Fix:**
- Run migrations before anything else (section 1). Existing rows and volumes are untouched by `migrate up`.
- `/health/ready` now compares the applied migration version with the newest migration embedded in the binary
  (`database.ExpectedSchemaVersion` / `AppliedSchemaVersion`). Behind = `503 migrations_pending`. Ahead = still
  ready (migrations here are additive, so older code works on a newer schema).
- Compose's own container health check still uses the plain `/health` on purpose, so a container isn't restarted
  while you migrate.

**How to find the real error for any 500:** the API logs the cause and returns only a generic message.

```bash
docker compose logs --no-color --tail=200 backend | grep -i "unhandled error"
```

---

## 3. `docker compose up` failed: MinIO images "pull access denied"

**Symptom:** `minio/minio:RELEASE.2025-04-22T22-12-26Z` pull access denied; `minio/mc:...` not found. Redis never
pulled because the MinIO failure aborted the whole pull.

**Cause:** MinIO, Inc. removed its `minio/minio` and `minio/mc` repositories from Docker Hub (the Hub API returns
404), and quay.io now answers 401 for anonymous pulls. Pinned tags don't help when the repository is gone.

**Fix:** switched to **Silo** (`pgsty/minio` and `pgsty/mc`), a community fork of the MinIO server and client. It
is not MinIO, Inc., it is AGPLv3, and it is only the local stand-in for S3. Production uses AWS S3.
Both images are pinned by release tag **and** digest, so a re-pushed tag can't change what runs. Same S3 API,
same `MINIO_*` variables, same credentials, same ports.

**Don't:** replace tags with `latest`. Do check what the image contains before assuming (see next two items).

---

## 4. `minio-init` would have failed: the `mc` image has no `grep`

The init script used `grep`. The `pgsty/mc` image doesn't ship it. Rewrote the check in plain shell. Also changed
the MinIO health check from `mc ready local` (needs an `mc` alias to exist) to
`curl -fsS http://localhost:9000/minio/health/ready` (the server image ships `curl`; no credentials needed).

---

## 5. Direct upload to MinIO failed: the app user had no permissions

**Symptom:** everything passed up to `FAILED: direct PUT to MinIO failed`.

**Cause:** `infra/minio/init.sh` decided whether to attach the bucket policy by looking for `ryucloud-app` in the
output of `mc admin user info`. The default access key is also named `ryucloud-app`, and that output always
contains an `AccessKey: ryucloud-app` line. So the check always said "already attached", the policy was never
attached, and MinIO answered 403 `AccessDenied` to the presigned PUT. The original `grep` version had the same
bug; it was invisible until the names collided.

**Fix:** match the `PolicyName:` line only (with `--no-color`), attach if missing, then re-check and exit non-zero
if it still isn't attached. Re-running `docker compose run --rm minio-init` is safe and repairs an existing user.
It touches no volumes.

**Also:** a bare "direct PUT failed" told us nothing. `put_to_storage` in `scripts/e2e-processing-test.sh` now
prints the HTTP status, curl's error, the S3 error code/message and the names of the signed headers. It never
prints the full URL, signature, credentials or headers.

**Presigned URL rules that matter** (all already true in the code, written down because they bite):
- The host is part of the signature. URLs are signed against `S3_PUBLIC_ENDPOINT` (`http://localhost:9000`), while
  the API itself talks to `S3_ENDPOINT` (`http://minio:9000`). Never rewrite a presigned URL after it's signed.
- Content-Type and Content-Length are signed. The client must send exactly what the API validated.
- The SDK is set to compute checksums only "when required"; otherwise a presigned PUT demands extra checksum
  headers the browser doesn't send.

---

## 6. Browser on the VM host can't reach MinIO (not yet tested)

Presigned URLs point at `localhost:9000`, so the machine running the **browser** must reach MinIO as
`localhost:9000`. Compose publishes ports on `127.0.0.1` of the Docker host. A browser inside the VM works. A
browser on another machine (for example the host of a VirtualBox VM) does not, and a VirtualBox NAT port-forward
doesn't help because it can't reach a port bound to the guest's loopback. Use an SSH tunnel:

```bash
ssh -L 3000:localhost:3000 -L 9000:localhost:9000 user@vm
```

Don't "fix" this by binding services to `0.0.0.0`: that exposes Postgres and MinIO to your network.

---

## 7. Backend container "exited with code 2" (never diagnosed)

We never saw those logs. It came from an earlier run, and `docker compose up` recreates the container and
erases them. Config errors in this codebase exit 1, so exit 2 points to a Go panic or a `go run` failure.
**Lesson:** if a container died, capture its logs *before* running `up` again:

```bash
docker compose ps -a
docker compose logs --no-color --tail=200 backend > ~/backend-exit.log 2>&1
```

The backend has run fine since, so we treat it as a stale-state problem, not a known bug.

---

## 8. Go dependencies are pinned for a reason

The toolchain in the image is fixed (`GOTOOLCHAIN=local`, Go 1.25.x) and will not download a newer one.
`go mod tidy` pulls the *latest* release of any module you haven't pinned, and fails if that module needs a newer
Go. So every direct dependency is pinned in `backend/go.mod` (for example `go-redis` v9.22.0, because v9.23.0
needs Go 1.26). Don't run `go get -u`. To upgrade one module: `go get module@version` and run
`python3 scripts/check-go-deps.py` first.

---

## 9. Running frontend tests on the host fails; run them in the container

`node_modules` on the host was installed for another platform (missing the native rollup binary), so
`vitest` can't start there. Run tests where the dependencies are correct:

```bash
docker compose run --rm --no-deps frontend npm test
docker compose run --rm --no-deps frontend npm run typecheck
```

Three Vitest tests were reported failing. We had not captured their output when this was written. To see them:

```bash
docker compose run --rm --no-deps frontend npx vitest run --reporter=verbose 2>&1 | tail -150
```

Update this section once they are fixed.

---

## 10. Crash recovery check

The Go suite tests worker crashes in-process (`internal/server/worker_test.go`). For the real stack:

```bash
CRASH_TEST=1 bash scripts/e2e-processing-test.sh
```

It SIGKILLs the worker mid-transcode. The job's lease (`WORKER_LEASE_SECONDS`, default 120s) has to expire before
another attempt picks it up, so expect several minutes. Passing means `succeeded|2|ready` (second attempt).
If it reports the job "finished before it could be killed", raise `CRASH_SECONDS`.
**Result not yet recorded.**

---

## Quick reference

| Symptom | Likely cause | Look at |
| --- | --- | --- |
| HTTP 500 on any API call | Unmigrated or wrong database | `docker compose logs backend \| grep -i "unhandled error"`, then `migrate status` |
| `/health/ready` 503 `migrations_pending` | Migrations not applied | `docker compose run --rm backend go run ./cmd/api migrate up` |
| Direct PUT to storage: 403 `AccessDenied` | App user missing its policy | `docker compose run --rm minio-init` |
| `SignatureDoesNotMatch` on PUT | Signed host or headers differ from what was sent | `S3_PUBLIC_ENDPOINT` vs. the host you're hitting |
| Pull access denied for `minio/*` | Images removed from Docker Hub | Use the pinned `pgsty/*` images in `docker-compose.yml` |
| Container exited, logs gone | `up` recreated it | Capture logs before `up` |
| Upload works on the VM but not from your laptop | Presigned URL points at the VM's `localhost` | SSH tunnel (section 6) |
