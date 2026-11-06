# RyuCloud

A cloud-native, YouTube-style video platform built as a software-engineering and
AWS portfolio project.

**Status: Phase 4 (AWS infrastructure, prepared but not deployed).** Accounts, channels, direct-to-storage
uploads and background HLS transcoding still run locally on Docker Compose exactly as before (MinIO, Redis, the Go
worker). Phase 4 adds the code and configuration to run the same app on AWS: Terraform for the VPC, ALB, ECS Fargate
services, RDS PostgreSQL, private S3, CloudFront (Origin Access Control + signed cookies/URLs), SQS with a dead-letter
queue, IAM, Secrets Manager, CloudWatch alarms/dashboard and a budget; an SQS queue adapter and CloudFront playback
provider selected by environment variables; and GitHub Actions for CI and a manual, approval-gated deploy.
**Nothing has been applied or deployed and nothing here costs money until you run `terraform apply`.**
See [docs/aws-architecture.md](docs/aws-architecture.md), [docs/aws-costs.md](docs/aws-costs.md),
[docs/aws-security.md](docs/aws-security.md) and [docs/aws-runbook.md](docs/aws-runbook.md). Local behavior:
[docs/uploads.md](docs/uploads.md), [docs/processing.md](docs/processing.md).

| Layer | Tech |
| --- | --- |
| Frontend | Next.js (App Router), React, TypeScript, Tailwind CSS · Vitest + Testing Library |
| Backend | Go 1.25, standard-library `net/http` routing · `go test` |
| Data | PostgreSQL 17 via `pgx`, SQL migrations with `goose` (embedded in the binary) |
| Object storage | MinIO locally, AWS SDK for Go v2 behind a storage interface (AWS S3 later by configuration) |
| Auth | Argon2id password hashing, server-side sessions in an HttpOnly cookie |
| Queue | Redis locally, behind a `Queue` interface (AWS SQS later). PostgreSQL holds the durable job state |
| Worker | Go (`backend/cmd/worker`) sharing the backend's internal packages, running `ffprobe`/`ffmpeg` |
| Local env | Docker Compose |

## Repository layout

```
frontend/                 Next.js app (UI, same-origin proxy to the API)
backend/                  Go API (modular monolith)
  cmd/api/                entry point: `api serve` / `api migrate up|down|reset|status`
  cmd/worker/             video-processing worker (queue consumer, FFmpeg)
  internal/auth/          users, password hashing, sessions, login/registration
  internal/channels/      channels
  internal/videos/        video metadata, visibility rules, feeds
  internal/uploads/       direct-to-storage upload lifecycle (presign, verify, cancel, expire)
  internal/jobs/          PostgreSQL job store (claim/lease, retry, fail, sweep)
  internal/transcode/     ffprobe/ffmpeg wrappers, rendition planning, HLS playlists
  internal/worker/        the processing engine (concurrency, backoff, recovery, shutdown)
  internal/health/        liveness/readiness
  internal/integrations/  storage (S3/MinIO), queue (Redis, SQS) and playback (API, CloudFront) adapters
  internal/httpx/         error envelope, validation, middleware
  internal/database/      pgx pool, goose runner, SQL migrations
  internal/config/        environment configuration
  internal/server/        route wiring, OpenAPI spec, API integration tests
infra/                    MinIO bucket init script (infra/minio); AWS Terraform (infra/terraform)
.github/workflows/        CI (ci.yml) and manual deploy (deploy.yml)
docs/                     architecture and API docs
scripts/                  helper scripts (smoke tests, end-to-end tests, AWS migration/roll-out, Terraform validation)
docker-compose.yml        local development stack
```

## Prerequisites

- Docker Engine with the Compose plugin (`docker compose version`)
- `openssl` (to generate a local database password)
- Go, Node.js and Python on the host are **not** required; everything runs in containers.

## Quick start

```bash
cd ~/projects/RyuCloud                     # adjust to where you cloned the repo

# 1. Local configuration (never commit .env)
cp .env.example .env
sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=$(openssl rand -hex 16)/" .env
sed -i "s/^MINIO_ROOT_PASSWORD=.*/MINIO_ROOT_PASSWORD=$(openssl rand -hex 16)/" .env
sed -i "s/^S3_SECRET_ACCESS_KEY=.*/S3_SECRET_ACCESS_KEY=$(openssl rand -hex 16)/" .env

# 2. Build images and pin Go dependencies (first time only: creates backend/go.sum)
docker compose build
docker compose run --rm --no-deps backend go mod tidy
docker compose build backend

# 3. Start the database and apply migrations
docker compose up -d db
docker compose run --rm backend go run ./cmd/api migrate up

# 4. Start everything (also starts MinIO, Redis and the video worker)
docker compose up -d
docker compose ps
```

### Upgrading an existing Phase 1 checkout

Your `.env` predates the storage settings, and Compose refuses to run any command until they exist.
This appends them **only if missing** and never overwrites existing values (it generates two
random secrets and prints nothing):

```bash
cd ~/projects/RyuCloud
grep -q '^MINIO_ROOT_PASSWORD=' .env || cat >> .env <<ENVBLOCK

# --- Object storage (MinIO for local development) ---
MINIO_ROOT_USER=ryucloud-minio-root
MINIO_ROOT_PASSWORD=$(openssl rand -hex 16)
S3_ACCESS_KEY_ID=ryucloud-app
S3_SECRET_ACCESS_KEY=$(openssl rand -hex 16)
S3_BUCKET=ryucloud-media
MINIO_API_HOST_PORT=9000
MINIO_CONSOLE_HOST_PORT=9001
UPLOAD_MAX_BYTES=1073741824
UPLOAD_URL_TTL_MINUTES=30
ENVBLOCK
```

Then pin the new Go dependencies (the AWS SDK, Redis client) and apply the new migrations. They only
add tables and columns; existing data is untouched, and any video a previous version left waiting
("processing" with no worker) is given a job automatically:

```bash
docker compose run --rm --no-deps backend go mod tidy
docker compose build backend
docker compose up -d db
docker compose run --rm backend go run ./cmd/api migrate up
docker compose up -d
```

Then open:

- App: http://localhost:3000
- Upload: http://localhost:3000/upload · Your videos: http://localhost:3000/studio
- MinIO console: http://localhost:9001 (sign in with `MINIO_ROOT_USER` / `MINIO_ROOT_PASSWORD` from `.env`)
- API docs (Swagger UI): http://localhost:8000/api/v1/docs (spec: `/api/v1/openapi.yaml`)
- Health: http://localhost:8000/api/v1/health and `/api/v1/health/ready`

Use `localhost`, not another hostname: the API only accepts state-changing
browser requests from origins listed in `TRUSTED_ORIGINS`.

The backend container runs `go run`, which doesn't hot-reload. After editing Go
code, restart it with `docker compose restart backend`.

### Upgrading to Phase 3 (video processing)

Phase 3 adds Redis, the `worker` service, new settings (all have defaults) and one frontend
dependency (`hls.js`, the HLS player). Run these in order:

```bash
cd ~/projects/RyuCloud

# 1. New settings (optional: every value has a default; this just documents them in your .env)
grep -q '^WORKER_CONCURRENCY=' .env || cat >> .env <<ENVBLOCK

# --- Video processing worker ---
WORKER_CPUS=2
WORKER_MEMORY=2g
WORKER_CONCURRENCY=1
FFMPEG_THREADS=2
WORKER_MAX_ATTEMPTS=4
ENVBLOCK
docker compose config --quiet && echo CONFIG_OK

# 2. Go dependencies (adds the Redis client; go.mod/go.sum are updated in ./backend)
docker compose run --rm --no-deps backend go mod tidy

# 3. Build the images (the frontend build installs the new hls.js dependency), then refresh
#    frontend/package-lock.json so `npm ci` is reproducible again, and rebuild once more
docker compose build
docker compose run --rm --no-deps frontend npm install
docker compose build frontend

# 4. Database: apply migration 00003 (job + rendition tables, new "queued" status)
docker compose up -d db
docker compose run --rm backend go run ./cmd/api migrate up
docker compose run --rm backend go run ./cmd/api migrate status

# 5. Start everything; recreate the frontend's node_modules volume so hls.js is present
docker compose up -d --renew-anon-volumes frontend
docker compose up -d
docker compose ps
```

### Frontend lockfile (first run)

`frontend/package-lock.json` isn't committed yet. Generate it once inside the
container, then rebuild so future builds use `npm ci`:

```bash
docker compose run --rm --no-deps frontend npm install
docker compose build frontend
```

## Running tests

```bash
# Backend: unit tests plus API/DB integration tests against a separate
# <POSTGRES_DB>_test database (created automatically, wiped each run).
# Upload tests use an in-memory object store, so they don't need MinIO.
docker compose up -d db
docker compose run --rm backend go test ./...
docker compose run --rm --no-deps backend go vet ./...
docker compose run --rm --no-deps backend gofmt -l .     # prints nothing when formatted

# FFmpeg-backed tests (probe, real HLS transcode, thumbnail, cancellation). They need the ffmpeg
# binaries, which exist in the worker image, and generate their own synthetic test video:
docker compose run --rm --no-deps worker go test ./internal/transcode -v

# Frontend
docker compose run --rm --no-deps frontend npm test
docker compose run --rm --no-deps frontend npm run typecheck
```

The backend integration tests run every migration up, all the way down, and up
again before exercising the API over real HTTP.

Tests against a **real MinIO** (presigned PUT round trip, tampered uploads rejected by storage, URL
expiry). Opt-in because they need the storage service:

```bash
docker compose up -d minio minio-init
docker compose run --rm -e S3_INTEGRATION_TEST=1 backend go test ./internal/integrations -run S3 -v
```

The worker/queue/recovery tests (`internal/server/worker_test.go`) run the real worker, job store, schema and API
against PostgreSQL with in-memory storage, an in-memory queue and a fake FFmpeg, so every failure path (crashes,
retries, exhaustion, storage errors, lease loss, duplicate and concurrent deliveries, graceful shutdown) is
deterministic. They run as part of `go test ./...`.

End-to-end tests against the running stack:

```bash
bash scripts/smoke-test.sh            # auth only
bash scripts/e2e-upload-test.sh       # browser-equivalent upload: API -> MinIO -> PostgreSQL
bash scripts/e2e-processing-test.sh   # the whole pipeline: upload -> Redis -> worker -> FFmpeg -> MinIO -> READY -> HLS fetched
CRASH_TEST=1 bash scripts/e2e-processing-test.sh   # same, plus SIGKILL the worker mid-job and check the job is recovered (adds several minutes)
```

Run the migrations **before** these scripts: `/api/v1/health/ready` answers 503 `migrations_pending` until the
database is at the schema version this build expects, and the tests fail on missing tables if you skip it.
If a direct upload to storage fails, the processing script prints the HTTP status and S3 error code (never the
URL, signature or credentials).

## Verify processing end to end (manual)

Prove the whole pipeline in the browser: upload → MinIO → queue → worker → FFmpeg → MinIO → PostgreSQL `ready` → HLS playback.
Prerequisites: `docker compose ps` shows `db`, `redis`, `minio`, `backend`, `worker` and `frontend` running (`minio-init`
runs once and exits 0), and migrations are applied.

You need a small video. If you don't have one, generate a 10-second test clip (no downloads, no licensing issues):

```bash
cd ~/projects/RyuCloud
docker compose run --rm --no-deps -T --entrypoint ffmpeg worker \
  -hide_banner -loglevel error -f lavfi -i testsrc2=size=1280x720:rate=24 -f lavfi -i sine=frequency=440 \
  -t 10 -c:v libx264 -pix_fmt yuv420p -c:a aac -movflags frag_keyframe+empty_moov -f mp4 pipe:1 > ~/demo.mp4
```

1. Follow the worker in a second terminal: `docker compose logs -f worker`.
2. Open http://localhost:3000, register (or sign in), then open http://localhost:3000/upload and create a channel if asked.
3. Drag `~/demo.mp4` onto the page, enter a title, choose **Public**, and click **Upload**. The browser's Network tab
   shows the `PUT` going to `localhost:9000` (MinIO), not `/api/v1`. You'll see "Upload complete ... queued for processing".
4. Open http://localhost:3000/studio. The video appears as **Queued**, then **Processing NN%** with a progress bar
   (the page refreshes itself every 3 seconds), then **Ready** with a thumbnail and a **Watch** button. In the worker
   log you'll see `job claimed`, `source probed`, `job succeeded`.
5. Click **Watch** (http://localhost:3000/watch/<video-id>). The video plays; in the Network tab you'll see
   `master.m3u8`, then `360p/…` or `720p/…` playlists and `seg_NNNNN.ts` segments, all from `/api/v1/videos/<id>/hls/…`.
6. Open http://localhost:3000: the public video is now on the home feed with its generated thumbnail.
7. Check **MinIO** has the generated files (renditions, playlists, segments, thumbnail) and the original:

   ```bash
   docker compose run --rm --no-deps --entrypoint sh minio-init -c \
     'mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && mc ls --recursive local/$S3_BUCKET'
   ```

   Expect `uploads/<id>/<upload-id>/source` plus `videos/<id>/hls/master.m3u8`, `videos/<id>/hls/360p/…`,
   `videos/<id>/hls/720p/…` and `videos/<id>/thumbnail.jpg`. (The console at http://localhost:9001 shows the same.)
8. Check **PostgreSQL** (video `ready`, job `succeeded` after 1 attempt, two renditions):

   ```bash
   docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "
     SELECT v.title, v.status, j.state AS job, j.attempts, j.progress, v.duration_seconds,
            (SELECT string_agg(name, chr(44) ORDER BY height) FROM video_renditions r WHERE r.video_id = v.id) AS renditions
     FROM videos v JOIN processing_jobs j ON j.video_id = v.id ORDER BY v.created_at DESC LIMIT 5;"'
   ```
9. **Failure path**: upload any non-video file renamed to `.mp4` (e.g. `head -c 20000 /dev/urandom > ~/bad.mp4`). It goes
   Queued → Processing → **Failed**, with the reason shown on the dashboard and watch page, after a single attempt.
10. **Crash recovery** (optional): upload a larger video, and while it is **Processing** run
    `docker compose kill worker`, then `docker compose up -d worker`. After the lease expires (up to 2 minutes) the worker
    log shows `job re-sent to queue` and the video finishes. The job's `attempts` will be 2.

The same pipeline without a browser: `bash scripts/e2e-processing-test.sh`.

## Operating the worker

```bash
docker compose logs -f worker                         # JSON logs: job_id, video_id, attempt, stage results
docker compose stop worker                            # graceful: running jobs finish (≤ 60s), then are handed back
docker compose up -d --scale worker=2                 # a second worker; safe, jobs are claimed with leases
docker compose exec redis redis-cli llen ryucloud:jobs:ready      # messages waiting
docker compose exec redis redis-cli llen ryucloud:jobs:inflight   # messages taken but not yet acknowledged

# Jobs by state (queued / processing / retry / succeeded / failed)
docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT state, count(*) FROM processing_jobs GROUP BY state;"'
# Details of unfinished or failed jobs (stage, attempts, next retry, last error)
docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "SELECT video_id, state, stage, progress, attempts, max_attempts, next_run_at, locked_by, left(last_error, 80) AS last_error FROM processing_jobs WHERE state <> '"'"'succeeded'"'"' ORDER BY updated_at DESC LIMIT 20;"'
```

**Recovering a stuck or failed video.** Stuck jobs recover by themselves (leases and the sweeper; see
[docs/processing.md](docs/processing.md)). To re-run a job that failed for a transient reason (for example storage was down
past the retry limit), after fixing the cause:

```bash
docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "
  UPDATE processing_jobs SET state = '"'"'queued'"'"', attempts = 0, next_run_at = now(), last_error = NULL, finished_at = NULL, last_published_at = NULL WHERE video_id = '"'"'<VIDEO_ID>'"'"';
  UPDATE videos SET status = '"'"'queued'"'"', processing_error = NULL WHERE id = '"'"'<VIDEO_ID>'"'"';"'
```

Replace `<VIDEO_ID>` with the video's UUID (shown in the dashboard URL of the watch page). The worker's sweeper picks it up within
about 15 seconds. Corrupt files will simply fail again; delete those videos from the dashboard instead.

## Common tasks

```bash
docker compose logs -f backend              # follow API logs (JSON lines)
docker compose logs -f frontend
docker compose exec db psql -U ryucloud -d ryucloud   # SQL shell (default user/db names)

# Migrations live in backend/internal/database/migrations (plain SQL, goose format).
# Add a file named 00002_describe_change.sql with "-- +goose Up" / "-- +goose Down" sections, then:
docker compose run --rm backend go run ./cmd/api migrate up
docker compose run --rm backend go run ./cmd/api migrate status

# After changing Go dependencies (see "Go toolchain and dependency pins" below before upgrading anything)
docker compose run --rm --no-deps backend go mod tidy
python3 scripts/check-go-deps.py        # host-side check that every pin fits the Go version in go.mod

# After changing frontend/package.json: rebuild and recreate the node_modules volume
docker compose build frontend
docker compose up -d --renew-anon-volumes frontend

docker compose logs -f minio minio-init     # object storage logs / bucket setup output
docker compose logs -f worker redis         # video processing / queue logs

docker compose down                         # stop (keeps data)
docker compose down -v                      # stop AND delete the database, uploaded/processed files, queue and cache volumes
```

## Go toolchain and dependency pins

All Go code builds with one toolchain: **Go 1.25** (`go 1.25.0` in `backend/go.mod`; the image tag is
`ARG GO_VERSION=1.25.14` in `backend/Dockerfile`, shared by the dev, worker-dev and build stages; the runtime
stages contain no Go). Containers set `GOTOOLCHAIN=local`, so Go never downloads a different toolchain.

Consequence: a dependency whose own `go` line is newer than 1.25 makes `go mod tidy` fail with
`requires go >= 1.26`. And `go mod tidy` resolves any module that is **not already pinned** in `go.mod` to its *latest*
release, which can be too new. So every directly imported module is pinned in `go.mod` (with a comment explaining why),
and you should not run `go get -u`. To change a dependency:

```bash
python3 scripts/check-go-deps.py --suggest github.com/redis/go-redis/v9   # newest release that fits Go 1.25
docker compose run --rm --no-deps backend go get github.com/redis/go-redis/v9@v9.22.0
docker compose run --rm --no-deps backend go mod tidy
python3 scripts/check-go-deps.py                                          # verify the whole graph still fits
```

To move to a newer Go, change `go` in `backend/go.mod` and `GO_VERSION` in `backend/Dockerfile` together, rebuild, then
re-run the tests.

## Configuration

| File | Purpose |
| --- | --- |
| `.env.example` → `.env` | Compose settings: Postgres and MinIO credentials, host ports, trusted origins, upload limits, worker limits (CPU, memory, concurrency, retries) |
| `backend/.env.example` | Only for running the API outside Docker (needs Go installed) |
| `frontend/.env.example` | Only for running Next.js outside Docker |

Real `.env` files are git-ignored. `COOKIE_SECURE` defaults to `true` in code; the
Compose file sets it to `false` because local development is plain HTTP.

## Documentation

- [docs/architecture.md](docs/architecture.md): structure, decisions, auth design, future AWS seams
- [docs/uploads.md](docs/uploads.md): upload lifecycle, state machine, security rules, S3 migration notes
- [docs/processing.md](docs/processing.md): worker, queue, retries, crash recovery, FFmpeg settings, AWS (SQS) migration
- [docs/api.md](docs/api.md): endpoint reference and error format
- [docs/troubleshooting.md](docs/troubleshooting.md): what broke while building Phase 3, why, and the startup order that works

## Roadmap

1. **Foundation**: layout, auth, data model, local environment, tests (done)
2. **Uploads to S3-compatible storage via presigned URLs, upload state tracking** (done)
3. **Redis queue + Go/FFmpeg worker (`backend/cmd/worker`) producing HLS renditions and thumbnails, HLS watch page** (implemented; its end-to-end verification result is not recorded in the repository)
4. **AWS: Terraform, ECS Fargate, RDS, private S3 + CloudFront signed access, SQS + DLQ, observability, GitHub Actions CI/CD** (this phase: written, not deployed)
5. First real deployment, then hardening: private subnets/NAT or VPC endpoints, WAF, auto scaling, Multi-AZ
