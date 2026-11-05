# RyuCloud

A cloud-native, YouTube-style video platform built as a software-engineering and
AWS portfolio project.

**Status: Phase 2 (Video uploads and object storage).** Accounts, sessions, channels,
video metadata, and **direct-to-storage video uploads** work locally: the browser uploads
to MinIO (S3-compatible) with a presigned URL, the API verifies the object, and PostgreSQL
records it as ready for processing. Transcoding, queues, HLS and CDN delivery are **not**
implemented yet, so uploaded videos stop at "processing pending" and cannot be played.
Nothing here uses AWS or costs money. See [docs/uploads.md](docs/uploads.md).

| Layer | Tech |
| --- | --- |
| Frontend | Next.js (App Router), React, TypeScript, Tailwind CSS · Vitest + Testing Library |
| Backend | Go 1.25, standard-library `net/http` routing · `go test` |
| Data | PostgreSQL 17 via `pgx`, SQL migrations with `goose` (embedded in the binary) |
| Object storage | MinIO locally, AWS SDK for Go v2 behind a storage interface (AWS S3 later by configuration) |
| Auth | Argon2id password hashing, server-side sessions in an HttpOnly cookie |
| Local env | Docker Compose |
| Planned worker (Phase 3) | Go, sharing the backend's internal packages |

## Repository layout

```
frontend/                 Next.js app (UI, same-origin proxy to the API)
backend/                  Go API (modular monolith)
  cmd/api/                entry point: `api serve` / `api migrate up|down|reset|status`
  internal/auth/          users, password hashing, sessions, login/registration
  internal/channels/      channels
  internal/videos/        video metadata, visibility rules, feeds
  internal/uploads/       direct-to-storage upload lifecycle (presign, verify, cancel, expire)
  internal/health/        liveness/readiness
  internal/integrations/  storage (S3/MinIO adapter), queue and playback interfaces (SQS, CloudFront later)
  internal/httpx/         error envelope, validation, middleware
  internal/database/      pgx pool, goose runner, SQL migrations
  internal/config/        environment configuration
  internal/server/        route wiring, OpenAPI spec, API integration tests
infra/                    MinIO bucket init script (infra/minio); Terraform arrives in a later phase
docs/                     architecture and API docs
scripts/                  helper scripts (smoke test, end-to-end upload test)
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

# 4. Start everything (also starts MinIO and creates the private bucket)
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

Then pin the new Go dependencies (the AWS SDK) and apply the new migration. The migration only
adds the `video_uploads` table; existing data is untouched:

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

End-to-end tests against the running stack:

```bash
bash scripts/smoke-test.sh            # auth only
bash scripts/e2e-upload-test.sh       # browser-equivalent upload: API -> MinIO -> PostgreSQL
```

## Verify uploads end to end (manual)

Prove that a file uploaded in the browser lands in MinIO **and** is recorded in PostgreSQL.
Prerequisites: the stack is up (`docker compose ps` shows `db`, `minio`, `backend`, `frontend` running;
`minio-init` runs once and exits 0), and migrations are applied.

1. Open http://localhost:3000 and register (or sign in).
2. Open http://localhost:3000/upload. If you have no channel yet, create one.
3. Drag a small `.mp4`, `.mov`, `.webm` or `.mkv` file onto the page (or use **Select video file**).
   Keep the browser's Network tab open: the `PUT` goes to `localhost:9000` (MinIO), not `/api/v1`.
4. Enter a title, description and visibility, then click **Upload**. You'll see progress, then
   "Upload complete ... processing pending".
5. Open http://localhost:3000/studio. The video shows the **Processing pending** badge. It is not
   playable and not on the home feed, by design.
6. Check **MinIO** has the object (you'll see `uploads/<video-id>/<upload-id>/source` and its size):

   ```bash
   cd ~/projects/RyuCloud
   docker compose run --rm --no-deps --entrypoint sh minio-init -c \
     'mc alias set local http://minio:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null && mc ls --recursive --summarize local/$S3_BUCKET'
   ```

   Or browse http://localhost:9001 → Object Browser → `ryucloud-media`.
7. Check **PostgreSQL** recorded it (upload `uploaded`, video `processing`, sizes equal, and the
   video's `source_object_key` equals the upload's `object_key`):

   ```bash
   docker compose exec db sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "
     SELECT v.title, v.status AS video_status, u.status AS upload_status, u.original_filename,
            u.declared_size_bytes, u.verified_size_bytes, u.object_key = v.source_object_key AS key_linked
     FROM video_uploads u JOIN videos v ON v.id = u.video_id ORDER BY u.created_at DESC LIMIT 5;"'
   ```
8. Try the failure paths: pick a `.txt` file (rejected in the browser), cancel mid-upload with
   **Cancel** (the upload row becomes `cancelled`, the video returns to `draft`, and no object remains),
   and delete a video in the dashboard (its object disappears from MinIO).

The same flow without a browser: `bash scripts/e2e-upload-test.sh`.

## Common tasks

```bash
docker compose logs -f backend              # follow API logs (JSON lines)
docker compose logs -f frontend
docker compose exec db psql -U ryucloud -d ryucloud   # SQL shell (default user/db names)

# Migrations live in backend/internal/database/migrations (plain SQL, goose format).
# Add a file named 00002_describe_change.sql with "-- +goose Up" / "-- +goose Down" sections, then:
docker compose run --rm backend go run ./cmd/api migrate up
docker compose run --rm backend go run ./cmd/api migrate status

# After changing Go dependencies
docker compose run --rm --no-deps backend go mod tidy

# After changing frontend/package.json: rebuild and recreate the node_modules volume
docker compose build frontend
docker compose up -d --renew-anon-volumes frontend

docker compose logs -f minio minio-init     # object storage logs / bucket setup output

docker compose down                         # stop (keeps data)
docker compose down -v                      # stop AND delete the database, uploaded files and cache volumes
```

## Configuration

| File | Purpose |
| --- | --- |
| `.env.example` → `.env` | Compose settings: Postgres and MinIO credentials, host ports, trusted origins, upload limits |
| `backend/.env.example` | Only for running the API outside Docker (needs Go installed) |
| `frontend/.env.example` | Only for running Next.js outside Docker |

Real `.env` files are git-ignored. `COOKIE_SECURE` defaults to `true` in code; the
Compose file sets it to `false` because local development is plain HTTP.

## Documentation

- [docs/architecture.md](docs/architecture.md): structure, decisions, auth design, future AWS seams
- [docs/uploads.md](docs/uploads.md): upload lifecycle, state machine, security rules, S3 migration notes
- [docs/api.md](docs/api.md): endpoint reference and error format

## Roadmap

1. **Foundation**: layout, auth, data model, local environment, tests (done)
2. **Uploads to S3-compatible storage via presigned URLs, upload state tracking** (this phase)
3. SQS + Go/FFmpeg worker (`backend/cmd/worker`) producing HLS renditions and thumbnails
4. CloudFront delivery and a watch page with a video player
5. Terraform-managed AWS environment and GitHub Actions CI/CD
