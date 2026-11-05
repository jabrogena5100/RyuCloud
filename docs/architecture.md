# Architecture

## Overview

```
Browser ──► Next.js (frontend, :3000) ──/api/v1/* rewrite──► Go API (backend, :8000) ──► PostgreSQL
   │          pages, UI, AuthProvider                          modular monolith           users, sessions, channels,
   │                                                                │                      videos, video_uploads
   │                                                                │ presign / HeadObject / Delete
   └── PUT file (presigned URL, no cookies) ───────────────────────►└──────────► MinIO (S3 API, :9000)
                                                                       │            private bucket, local volume
                                                                       │ job id            ▲ get source / put HLS
                                                                       ▼                   │
                                                         Redis (queue) ──► Go worker (cmd/worker) + FFmpeg
                                                                              └─ job state, leases, retries ─► PostgreSQL
```

The browser talks to the Next.js origin for everything except the file itself.
Next.js forwards `/api/v1/*` to the API, so the session cookie is first-party and
the API needs no CORS configuration. **Video bytes bypass both Next.js and the
API**: the browser PUTs them straight to object storage using a presigned URL
(see [uploads.md](uploads.md)). That is the one cross-origin request, and storage
CORS is configured for it. The API also serves its own Swagger UI on `:8000`.

## Backend: Go modular monolith

One deployable binary (`backend/cmd/api`), split into domain packages under
`backend/internal/`:

| Package | Responsibility |
| --- | --- |
| `auth` | Accounts, Argon2id hashing, sessions, register/login/logout/me, and the `Required`/`Optional` handler wrappers other modules use |
| `channels` | Channel creation, lookup by handle, owner-only updates |
| `videos` | Video metadata, lifecycle status, visibility rules, home feed, channel video lists; deletes a video's stored objects |
| `uploads` | Direct-to-storage upload lifecycle: validation, presigned targets, verification, cancel/expiry, upload state machine; creates and publishes the processing job on completion |
| `jobs` | PostgreSQL job store: claim with a lease, heartbeat, progress, succeed/retry/fail/release, sweeping. Shared by the API and the worker |
| `transcode` | ffprobe/ffmpeg wrappers (argument lists, never a shell), rendition planning, HLS playlist helpers |
| `worker` | The processing engine: queue consumer, concurrency, retries with backoff, crash recovery, graceful shutdown |
| `health` | Liveness (`/health`) and readiness (`/health/ready`, pings the DB) |
| `integrations` | Interfaces for storage, queue and playback; the S3/MinIO adapter (AWS SDK v2), an in-memory fake and a disabled stand-in |
| `httpx` | Error envelope, JSON body validation, request-ID/origin-check/logging/recovery middleware |
| `database` | `pgx` connection pool, embedded `goose` migrations |
| `config` | Environment-variable configuration |
| `server` | Wires every module onto one `http.ServeMux`; embeds the OpenAPI spec |

Dependencies point one way: `server → videos / uploads → channels → auth → httpx/database`, and handlers reach external services only through `integrations` interfaces. The worker (`cmd/worker`) is a second binary in the same module: `worker → jobs / transcode / integrations`; it never imports the HTTP packages beyond `httpx` helpers.
`internal/` keeps these packages private to this module.

### Why Go, and why these libraries

- **Go** gives a single static binary, low memory use, fast startup and simple
  concurrency. It is also the natural language for the Phase 3 video worker, so
  the API and worker can share packages (`database`, `integrations`, `config`).
- **Standard-library routing** (Go 1.22+ method and wildcard patterns, e.g.
  `GET /api/v1/videos/{id}`) covers everything this API needs without a framework.
- **pgx** is the de-facto PostgreSQL driver. Queries are hand-written SQL with no
  ORM, which keeps them visible and reviewable.
- **goose** runs plain-SQL migrations that are embedded in the binary, so the prod
  image can migrate without extra files (`api migrate up`).
- **golang.org/x/crypto/argon2** provides Argon2id.

Trade-off vs. the original FastAPI plan: request validation and OpenAPI docs are
no longer generated from types. Validation is explicit (`httpx.Fields`), and
`internal/server/openapi.yaml` is maintained by hand and must be updated with
route changes.

### Why a monolith plus a worker (not microservices)

A single team and a single database don't benefit from the operational cost of
service boundaries. The one planned split is the **video worker**
(`backend/cmd/worker`, Phase 3). It is separate because transcoding is CPU-heavy,
slow and asynchronous, so it must not run inside API request handling. It will
live in the same Go module and reuse the internal packages, but run as its own
process and container.

## Data model

```
users 1──* auth_sessions
users 1──* channels 1──* videos 1──* video_uploads *──1 users
                                 videos 1──1 processing_jobs
                                 videos 1──* video_renditions
```

- **users**: `email` (unique, lowercase enforced by a CHECK), Argon2id `password_hash`, `display_name`, `is_active`.
- **auth_sessions**: `token_hash` (SHA-256 of the cookie token, unique), `expires_at`. Cascade-deleted with the user.
- **channels**: `handle` (unique, immutable, `[a-z0-9][a-z0-9_-]{2,29}`), `name`, `description`.
- **videos**: `title`, `description`, `status`, `visibility`, `duration_seconds`, `view_count`,
  `published_at`, `processing_error`, and the object-storage keys `source_object_key`
  (the original), `hls_manifest_key` and `thumbnail_object_key` (set by the worker). `status` and `visibility` are strings guarded
  by CHECK constraints (simpler to evolve than native Postgres enums). The `ix_videos_feed`
  index covers the home-feed filter.

- **video_uploads** (Phase 2): one row per upload attempt. `object_key` (unique, server-generated),
  `original_filename` (display only), `content_type`, `declared_size_bytes`, `verified_size_bytes`, `etag`,
  `status` (`pending|uploaded|failed|cancelled|expired`), `expires_at`, `completed_at`. CHECK constraints
  guarantee an `uploaded` row carries a verified size equal to the declared size, and a partial unique index
  allows only one `pending` upload per video. See [uploads.md](uploads.md).
- **processing_jobs** (Phase 3): one row per video: `state`, `stage`, `progress`, `attempts`/`max_attempts`,
  `next_run_at` (retry schedule), `locked_by` + `lease_expires_at` (which worker owns it), `last_error`. PostgreSQL,
  not Redis, is the source of truth for job state. CHECK constraints keep it consistent (a `processing` job always
  has an owner and lease; `attempts <= max_attempts`). See [processing.md](processing.md).
- **video_renditions** (Phase 3): one row per generated HLS rendition (`360p`, `720p`, …) with size, measured
  bandwidth, segment count and playlist key.

Primary keys are UUIDs generated by PostgreSQL (`gen_random_uuid()`). Constraints
have explicit, predictable names (`uq_users_email`, `fk_videos_channel_id_channels`, …),
and the code maps unique violations to API errors by constraint name.

Video lifecycle: `draft → uploading → queued → processing → ready | failed` (PENDING_UPLOAD, UPLOADING, QUEUED,
PROCESSING, READY, FAILED). Metadata-only creation makes a `draft`; starting an upload makes it `uploading`; a
verified upload makes it `queued` and creates its job; the worker moves it to `processing` and finally `ready` (or
`failed`). **Visibility rules**: the owner can always see their videos;
everyone else sees only `ready` videos that are `public` (listed in feeds) or
`unlisted` (reachable by ID). Videos a caller can't see return **404**, so private
IDs don't leak. Mutating a visible video you don't own returns **403**.

Migrations live in `backend/internal/database/migrations/` as numbered goose SQL
files with `Up` and `Down` sections. The integration test suite runs them up, all
the way down, and up again on every run.

## Authentication and sessions

- **Passwords**: Argon2id (OWASP parameters: 19 MiB, 2 passes, 1 lane), stored in PHC
  string format. Hashes with outdated parameters are upgraded on the next login.
  Unknown-email logins still run a dummy hash check, so response time doesn't reveal
  whether an account exists. Both failure cases return the same `401 invalid_credentials`.
- **Sessions**: on login/registration the API creates a random 256-bit token, stores
  only its SHA-256 hash, and sets it as a cookie: `HttpOnly; SameSite=Lax; Path=/;
  Max-Age=<SESSION_TTL_HOURS>` (default 7 days), plus `Secure` unless `COOKIE_SECURE=false`.
  JavaScript never sees the token, and nothing goes in `localStorage`.
- **Why server-side sessions instead of JWTs**: logout and revocation are immediate
  (delete the row), there is no signing key to manage, and the browser-only client
  doesn't need stateless tokens. The cost is one indexed lookup per authenticated request.
- **CSRF**: `SameSite=Lax` blocks cookies on cross-site POSTs in modern browsers. As
  defence in depth, middleware rejects `POST/PUT/PATCH/DELETE` whose `Origin` is neither
  in `TRUSTED_ORIGINS` nor the API's own origin (`403 untrusted_origin`). Requests with no
  `Origin` (curl, server-to-server) are allowed unless `Sec-Fetch-Site: cross-site`.
- **Authorization**: `auth.Service.Required` and `Optional` wrap handlers and pass in
  the resolved user. Ownership checks live next to the resources (channel owner, the
  video's channel owner).
- **Logout** deletes the session row and clears the cookie. Expired sessions are
  rejected, and a user's expired rows are purged on their next login.

## Errors, validation and logging

- JSON bodies are capped at 1 MiB and parsed with `httpx.Fields`. It collects every
  invalid field into one response and tells missing fields apart from explicit
  `null`s (PATCH rejects nulls). Query and path parameters are validated too.
- Every error uses one envelope (see [api.md](api.md)) with a machine-readable `code`
  and the `request_id`. Validation errors list `loc`/`msg`/`type` but never echo
  submitted values. Unknown routes and wrong methods also get JSON (`404`/`405`).
  Unhandled errors and panics become a generic `500 internal_error` and are logged.
- Each request gets an `X-Request-ID` (an incoming header is accepted if well-formed).
  Logs are JSON lines (`log/slog`) with request ID, method, path, status and duration.
  Query strings aren't logged.
- The server sets read/write/idle timeouts and shuts down gracefully on SIGTERM.

## Video pipeline

```
1. POST /uploads                → videos(uploading) + video_uploads(pending) + presigned PUT   (Phase 2)
2. Browser PUTs bytes to MinIO/S3 directly                                                      (Phase 2)
3. POST /uploads/{id}/complete  → HeadObject check → ONE tx: upload=uploaded, video=queued,
                                  INSERT processing_jobs; then publish the job id to the queue   (Phase 3)
4. Go worker (cmd/worker): claim (lease) → ffprobe → ffmpeg 360p/720p HLS + thumbnail →
                           upload to storage → ONE tx: job=succeeded, video=ready, renditions     (Phase 3)
5. Browser polls status, then plays /videos/{id}/hls/master.m3u8 with hls.js                     (Phase 3, API-served)
6. CloudFront serves the same keys from private S3 via signed URLs/cookies                       (Phase 4)
```

Details, retry/recovery behaviour and the AWS mapping are in [processing.md](processing.md).

| Interface | File | Implementations |
| --- | --- | --- |
| `ObjectStorage` | `internal/integrations/storage.go` | `S3Storage` (MinIO now, AWS S3 later; `STORAGE_BACKEND=s3`), `DisabledStorage`, `MemoryStorage` (tests) |
| `Queue` | `internal/integrations/queue.go` | `RedisQueue` (`QUEUE_BACKEND=redis`; SQS later), `MemoryQueue` (tests), `DisabledQueue` |
| `PlaybackURLProvider` | `internal/integrations/playback.go` | `APIPlayback` (URLs of the API's authorized media routes; CloudFront in Phase 4), `NullPlayback` |

Handlers depend only on the interfaces, so swapping MinIO for S3, Redis for SQS, or the API media route for
CloudFront does not change handler code.

## Frontend

- App Router pages: `/` (home feed), `/login`, `/register`, `/upload` (drag-and-drop upload with
  progress and cancel), `/studio` (creator dashboard: thumbnails, queued/processing progress, failure reasons,
  watch/cancel/delete), `/watch/[id]` (HLS player, or live status while the video is not ready), plus a 404 page.
- Status updates use **polling** (every 3 s, only while a video is uploading, queued or processing), not WebSockets:
  the API is stateless, processing takes seconds to minutes, and polling needs no extra infrastructure. If it ever
  matters, server-sent events could replace it without changing the API's data model.
- `components/VideoPlayer.tsx` plays HLS natively (Safari) or through `hls.js` (loaded on demand).
- `AppShell` = sticky `Navbar` (menu toggle, logo, search placeholder, account) +
  `Sidebar`. The sidebar is a collapsible rail on ≥1024px and an overlay drawer below that.
- `AuthProvider` keeps the current user in memory, populated from `GET /auth/me` on load.
- `lib/upload.ts` (client-side validation, XHR transfer with progress) and `lib/uploadFlow.ts`
  (initiate → direct PUT → complete, with cancel and retry) implement the browser side of uploads.
  The PUT to storage is the one request that does not go through `lib/api.ts`.
- `lib/api.ts` is the only place that calls `fetch` for API requests. It uses relative `/api/v1` URLs
  and turns the error envelope into an `ApiError`.
- The home feed calls `GET /videos` (public + ready videos). With none, or an unreachable API, it shows
  placeholder cards and a notice saying so.

The frontend is language-agnostic about the backend: it depends only on the HTTP
contract in [api.md](api.md).

## Local environment

`docker-compose.yml` runs `db` (postgres:17-alpine, named volume `pgdata`),
`minio` (S3-compatible storage, named volume `miniodata`, API on `:9000`, console on `:9001`),
`minio-init` (one-shot: creates the private bucket and a least-privilege app user),
`redis` (queue transport, named volume `redisdata`),
`worker` (the processing worker with FFmpeg, `go run ./cmd/worker`, CPU/memory limited, scratch space in the
named volume `workertmp`),
`backend` (`go run`, source bind-mounted, module/build caches in named volumes) and
`frontend` (`next dev`, source bind-mounted, `node_modules`/`.next` in container
volumes). Host ports bind to `127.0.0.1` only. `GET /health/ready` is 200 only when PostgreSQL answers and the applied
migration version is at least the newest one embedded in the binary (otherwise `503 migrations_pending`). The Dockerfiles also have `prod`
targets for later deployment work: a static Go binary on distroless/nonroot, and the
Next.js standalone output. They aren't used by Compose yet.

## Known limitations

Upload limitations (single-part uploads, lazy expiry) are listed in [uploads.md](uploads.md) and
processing limitations (CPU-only x264, no resume, delete-while-processing leftovers) in
[processing.md](processing.md). Carried over from Phase 1:

- No rate limiting or account lockout on login/registration yet.
- Registration reveals whether an email is already registered (`409 email_taken`).
  This is a deliberate usability trade-off for now.
- No email verification or password reset.
- No search, comments, subscriptions or view counting.
- The backend dev container doesn't hot-reload (restart it after Go changes).
- No performance or scalability measurements have been made.
