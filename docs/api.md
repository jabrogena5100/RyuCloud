# API reference (v1)

Base path: `/api/v1`. The interactive Swagger UI is at `/api/v1/docs`, and the
OpenAPI 3.1 spec at `/api/v1/openapi.yaml` (source:
`backend/internal/server/openapi.yaml`). The spec is maintained by hand, so update
it together with this page whenever a route changes. Set `ENABLE_API_DOCS=false`
to turn both off.

**Auth** column: `none` = public, `optional` = response depends on who is signed in,
`required` = needs a valid session cookie (`sf_session`).

## Health

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| GET | `/health` | none | Liveness. `{"status": "ok", "version": "0.3.0"}` |
| GET | `/health/ready` | none | Readiness: PostgreSQL answers **and** the applied migration version is at least the newest one embedded in this build (`schema_version` in the response). `503 database_unavailable` if the DB is down; `503 migrations_pending` until `api migrate up` has run. Compose's own health check uses the plain `/health` so a container is not restarted while you migrate |

## Auth

| Method | Path | Auth | Body | Success |
| --- | --- | --- | --- | --- |
| POST | `/auth/register` | none | `{email, password (8-128), display_name (1-100)}` | `201` user + session cookie |
| POST | `/auth/login` | none | `{email, password}` | `200` user + session cookie |
| POST | `/auth/logout` | optional | none | `204`, session revoked, cookie cleared |
| GET | `/auth/me` | required | none | `200` user |

User object: `{id, email, display_name, created_at}`. Password hashes are never returned.

Errors: `409 email_taken`, `401 invalid_credentials` (same response for unknown
email and wrong password), `401 not_authenticated`.

## Channels

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| POST | `/channels` | required | Create `{handle, name, description?}`. `409 handle_taken` |
| GET | `/channels/mine` | required | Channels owned by the caller |
| GET | `/channels/{handle}` | none | Channel by handle (case-insensitive). `404 channel_not_found` |
| PATCH | `/channels/{handle}` | required (owner) | Update `{name?, description?}`. `403` if not owner |
| GET | `/channels/{handle}/videos?limit&offset` | optional | Public+ready videos; the owner sees all |

Handles: 3-30 chars, `[a-z0-9][a-z0-9_-]*`, stored lowercase, immutable. Reserved:
`mine, new, admin, api, settings, studio`.

Channel object: `{id, owner_id, handle, name, description, created_at}`.

## Videos

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| GET | `/videos?limit=24&offset=0` | none | Home feed: `public` + `ready`, newest first. `limit` 1-50, `offset` 0-10000 |
| POST | `/videos` | required (channel owner) | Create draft metadata `{channel_id, title (1-100), description?, visibility?}` |
| GET | `/videos/{id}` | optional | `404 video_not_found` unless viewable (see below) |
| PATCH | `/videos/{id}` | required (owner) | Update `{title?, description?, visibility?}` |
| DELETE | `/videos/{id}` | required (owner) | `204`. Also deletes the stored upload and all generated HLS/thumbnail objects |
| GET | `/videos/{id}/hls/{path}` | optional | HLS file of a **ready** video, streamed from private storage. `path` is `master.m3u8`, `{N}p/index.m3u8` or `{N}p/seg_NNNNN.ts`; anything else is `404` |
| GET | `/videos/{id}/thumbnail` | optional | JPEG poster of a ready video |

Viewable means the caller owns it, or it is `ready` and `public`/`unlisted`. A
non-owner mutating a viewable video gets `403`; one they can't see gets `404`.

The two media routes apply the same rule (and return `404 media_not_found` for a video that is not ready or has
no thumbnail). They exist so storage can stay private in local development; use `playback_url` /
`thumbnail_url` instead of building the paths, because a later phase serves them from a CDN. Responses carry
`Content-Type: application/vnd.apple.mpegurl` (playlists), `video/mp2t` (segments) or `image/jpeg`, plus
`X-Content-Type-Options: nosniff`. The browser's player sends the session cookie automatically.

Video object:

```json
{
  "id": "uuid",
  "channel": {"id": "uuid", "handle": "alice-tv", "name": "Alice TV"},
  "title": "Intro to S3",
  "description": "",
  "status": "draft",            // draft | uploading | queued | processing | ready | failed
  "visibility": "private",      // public | unlisted | private
  "duration_seconds": null,
  "view_count": 0,
  "created_at": "2026-11-04T12:00:00Z",
  "published_at": null,
  "thumbnail_url": null,        // set once ready: /api/v1/videos/{id}/thumbnail
  "playback_url": null,         // set once ready: /api/v1/videos/{id}/hls/master.m3u8
  "processing_progress": null,  // 0-100 while queued/processing, otherwise null
  "processing_error": null      // owner-facing reason when status is "failed"
}
```

Lifecycle: `draft → uploading → queued → processing → ready | failed`. `queued` means the upload was
verified and a processing job exists (also while waiting to retry); `processing` means a worker is transcoding.
See [processing.md](processing.md).

List responses: `{"items": [Video...], "limit": 24, "offset": 0}`.

## Uploads

Direct-to-storage uploads; see [uploads.md](uploads.md) for the lifecycle and security model.
All routes need a session except `/uploads/limits`. Other users' uploads are always `404 upload_not_found`.

| Method | Path | Auth | Description |
| --- | --- | --- | --- |
| GET | `/uploads/limits` | none | `{max_bytes, allowed_content_types, allowed_extensions, url_ttl_seconds}` |
| POST | `/uploads` | required (channel owner) | Start an upload. Body `{channel_id, title (1-100), description?, visibility?, filename, content_type, size_bytes}`. `201 {upload, video, target}` |
| GET | `/uploads?status&limit&offset` | required | Your uploads, newest first. `status`: `pending\|uploaded\|failed\|cancelled\|expired` |
| GET | `/uploads/{id}` | required (owner) | Upload status |
| POST | `/uploads/{id}/complete` | required (owner) | Verify the stored object. Idempotent. `200` upload |
| DELETE | `/uploads/{id}` | required (owner) | Cancel a pending upload. `204` |

`POST /uploads` response:

```json
{
  "upload": {"id": "uuid", "video_id": "uuid", "status": "pending", "original_filename": "clip.mp4",
             "content_type": "video/mp4", "size_bytes": 5000000, "failure_reason": null,
             "expires_at": "2026-11-05T12:30:00Z", "completed_at": null, "created_at": "2026-11-05T12:00:00Z"},
  "video":  {"id": "uuid", "title": "My video", "status": "uploading", "visibility": "private"},
  "target": {"method": "PUT", "url": "http://localhost:9000/ryucloud-media/uploads/…?X-Amz-Signature=…",
             "headers": {"Content-Type": "video/mp4"}, "expires_at": "2026-11-05T12:30:00Z"}
}
```

Send the file with exactly `target.method` to `target.url`, with exactly `target.headers` and a body of exactly
`size_bytes` bytes (no cookies, not through `/api/v1`). Then call `complete`.

After a successful `complete` the video's `status` is `queued`: verified and stored, with a processing job
created in the same transaction and published to the worker queue. It is **not playable**, and invisible to
everyone but its owner, until the worker finishes (`ready`). Poll `GET /videos/{id}` (or the channel video list)
for `status` and `processing_progress`.

## Errors

Every non-2xx response uses this envelope, and every response carries an
`X-Request-ID` header:

```json
{
  "error": {
    "code": "validation_error",
    "message": "The request is invalid.",
    "request_id": "6f1c...",
    "details": [{"loc": ["body", "password"], "msg": "must be at least 8 characters", "type": "string_too_short"}]
  }
}
```

| Status | Codes |
| --- | --- |
| 401 | `not_authenticated`, `invalid_credentials` |
| 403 | `forbidden`, `untrusted_origin` (state-changing request from an origin not in `TRUSTED_ORIGINS`) |
| 404 | `not_found` (unknown route), `channel_not_found`, `video_not_found`, `upload_not_found`, `media_not_found` |
| 405 | `method_not_allowed` (with an `Allow` header) |
| 409 | `email_taken`, `handle_taken`, `upload_incomplete` (nothing stored yet; retry), `upload_cancelled`, `upload_failed`, `upload_already_completed` |
| 410 | `upload_expired` |
| 413 | `payload_too_large` (JSON bodies are capped at 1 MiB), `file_too_large` (`size_bytes` over the limit) |
| 415 | `unsupported_media_type` (content type not allowed, or extension does not match it) |
| 422 | `upload_rejected` (stored object did not match the declared size/type; it was discarded), `validation_error`: `details` lists every problem in the body (`loc: ["body", field]`), query (`["query", name]`) or path (`["path", name]`, e.g. a malformed UUID). Submitted values are never echoed. Explicit `null`s are rejected. |
| 429 | `too_many_uploads` (10 pending uploads per user) |
| 500 | `internal_error` |
| 502 | `storage_unavailable` (object storage failed; state unchanged, retry; also from the media routes) |
| 503 | `database_unavailable`, `storage_not_configured` |

## Example (curl)

```bash
curl -c jar.txt -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.com","password":"correct-horse-battery","display_name":"Alice"}' \
  http://localhost:8000/api/v1/auth/register
curl -b jar.txt http://localhost:8000/api/v1/auth/me
```
