# Video uploads (Phase 2)

Uploads go **browser → object storage directly**. The Go API only issues a
time-limited, single-object permission (a presigned URL) and later verifies what
arrived. Video bytes never pass through the API or the Next.js proxy.

```
Browser                    Next.js :3000 ─proxy─► Go API :8000        MinIO :9000        PostgreSQL
  │  POST /api/v1/uploads ──────────────────────►  validate, one tx:
  │                                                 videos(status=uploading) + video_uploads(pending)
  │  ◄── 201 {upload, video, target{url,headers}} ── presign (local computation)
  │
  │  PUT <presigned url> ───────────────────────────────────────────────►  stores the object
  │  ◄── 200 ───────────────────────────────────────────────────────────── (signature checks key, type, size, expiry)
  │
  │  POST /api/v1/uploads/{id}/complete ─────────►  HeadObject ────────────►  real size + content type
  │                                                 compare with declared; one tx:
  │                                                 video_uploads=uploaded, videos=processing, source_object_key set
  │  ◄── 200 {status: "uploaded"} ───────────────
```

## State machine

`video_uploads.status` (enforced by a CHECK constraint). Only `pending` has outgoing
transitions, and every transition is a conditional `UPDATE ... WHERE status = 'pending'`,
which is what makes repeated, concurrent and racing requests safe.

| Upload state | Meaning | Video status set |
| --- | --- | --- |
| `pending` | Presigned URL issued; file not confirmed | `uploading` |
| `uploaded` | API verified the stored object matches what was declared | `processing` ("processing pending": waits for the Phase 3 worker) |
| `failed` | Stored object had the wrong size or type; it was deleted | `failed` |
| `cancelled` | Owner cancelled; any stored object was deleted | `draft` |
| `expired` | Never confirmed within the window; any stored object was deleted | `failed` |

**A video is never playable or publicly listed after Phase 2.** Public visibility
requires `status = 'ready'`, which only the future transcoding worker will set.
`processing` therefore means "verified source file stored, waiting to be transcoded".

### Idempotency and races

- `complete` on an `uploaded` upload returns the same `200` and does not change anything.
- Concurrent `complete` calls: one conditional `UPDATE` wins; the others re-read the row and return the winner's result.
- `complete` vs `cancel`: whichever runs its conditional `UPDATE` first wins; the other gets a clear `409`.
- `cancel` on an already cancelled/failed/expired upload is a no-op `204`.
- A storage outage during `complete` (`502`) changes nothing, so the client can retry.
  The frontend retries confirmation up to 3 times for network errors and 5xx.

### Expiry

`expires_at` is when the **presigned URL** stops working (`UPLOAD_URL_TTL_MINUTES`,
default 30, max 60). Because a large file that started just before that moment may
legitimately finish later, the API still accepts `complete` for a **2-hour grace
period** after `expires_at`. After that the upload is marked `expired` (and its video
`failed`) lazily, the next time its owner lists, reads, completes or cancels any upload.
There is no background sweeper in Phase 2.

## What is validated, and where

| Rule | Where it is enforced |
| --- | --- |
| Signed in | `auth.Service.Required` on every upload route except `GET /uploads/limits` |
| Channel ownership | `POST /uploads` checks `channels.owner_id` inside the transaction (`403`; unknown channel `404`) |
| Upload ownership | Every query filters on `user_id`; someone else's upload is `404 upload_not_found`, indistinguishable from a missing one |
| Content type allow-list | API: `video/mp4`, `video/quicktime`, `video/webm`, `video/x-matroska` (`415`). Parameters (`; codecs=…`) are stripped, case is ignored |
| Extension matches the type | API (`415`): `.mp4/.m4v`, `.mov`, `.webm`, `.mkv` |
| Filename safe | API (`422`): no path separators or control characters. The filename is stored for display only and is **never** part of the object key |
| Size | API: `1 ≤ size_bytes ≤ UPLOAD_MAX_BYTES` (`413 file_too_large`). The size is also **signed into the URL**, so storage rejects any other body length |
| Content type of the bytes | Signed into the URL; storage rejects a different `Content-Type`. Re-checked on `complete` against what storage reports |
| Real size and type | `complete` calls `HeadObject`; a mismatch deletes the object and marks the upload `failed` (`422 upload_rejected`) |
| Limited lifetime | Presigned URL expiry (default 30 min); completion window as above |
| Limited concurrency | At most 10 pending uploads per user (`429 too_many_uploads`), serialised with a per-user advisory lock |
| Unique keys | `uploads/{videoID}/{uploadID}/source`, both server-generated UUIDs; `object_key` is also `UNIQUE` in the database |
| Private bucket | `mc anonymous set none`; the app user can only touch that one bucket |
| Database invariants | CHECK constraints: valid status; `uploaded` requires `verified_size_bytes = declared_size_bytes` and `completed_at`; one `pending` upload per video (partial unique index) |

The object key is not returned by the API (the presigned URL naturally contains it).

## Storage adapter

`integrations.ObjectStorage` has three methods: `PresignUpload`, `StatObject`,
`DeleteObject`. Implementations:

- `S3Storage` (`internal/integrations/s3.go`): AWS SDK for Go v2. The same code serves MinIO
  and AWS S3; only configuration changes.
- `MemoryStorage`: in-process fake used by the Go integration tests (can inject failures).
- `DisabledStorage`: used when `STORAGE_BACKEND=disabled`; upload routes answer `503 storage_not_configured`.

Two endpoints are configured on purpose:

| Variable | Used for | Local value |
| --- | --- | --- |
| `S3_ENDPOINT` | Calls the API makes itself (`HeadObject`, `DeleteObject`) | `http://minio:9000` (Docker network) |
| `S3_PUBLIC_ENDPOINT` | Signing presigned URLs that the **browser** uses | `http://localhost:9000` |

The host is part of an S3 signature, so a URL signed for `minio:9000` is both unreachable from
a browser and cannot be "fixed" by rewriting the host. Signing against the public endpoint is
what makes the URL work. On AWS, leave both endpoints empty and use an IAM role.

### Moving to AWS S3 later

1. Terraform: a private bucket with block-public-access, SSE, and a lifecycle rule that aborts
   incomplete multipart uploads and expires objects under `uploads/` that never became a video
   (this is the safety net for objects orphaned by abandoned uploads, which Phase 2 cleans up only lazily).
2. A bucket **CORS rule** (MinIO takes `MINIO_API_CORS_ALLOW_ORIGIN` instead): allow `PUT` from the
   site origin with header `Content-Type`.
3. Set `S3_BUCKET`, `S3_REGION`; leave `S3_ENDPOINT`, `S3_PUBLIC_ENDPOINT` and the keys empty; grant the
   API task role `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject` on `uploads/*`.

## CORS in local development

The page is served from `http://localhost:3000`, but the file is PUT to `http://localhost:9000`, so the
browser performs a CORS preflight. MinIO has no bucket-CORS API; its allowed origins come from the server
setting `MINIO_API_CORS_ALLOW_ORIGIN`, which Compose sets from `TRUSTED_ORIGINS`
(`http://localhost:3000,http://127.0.0.1:3000`). The upload request carries **no cookies**: the presigned
signature is its only credential.

## Cleanup behaviour

- Cancelling, failing or expiring an upload deletes its stored object (best effort, logged on failure).
- Deleting a video deletes its stored objects after the database rows are removed (best effort).
- Best effort means a storage outage never blocks the user, but may leave an orphaned object. Nothing
  references an orphan, and the bucket lifecycle rule above is the planned backstop.

## Manual end-to-end test

See the README ("Verify uploads end to end") or run `bash scripts/e2e-upload-test.sh`.

## Known limitations (Phase 2)

- **Single-part uploads only**: one `PUT` per file, up to `UPLOAD_MAX_BYTES` (default 1 GiB, hard maximum 5 GiB).
  There is no resume after a dropped connection; the user starts again. Multipart/resumable uploads are future work.
- Content is not inspected: a file with an allowed type, extension and size is accepted even if it is not a
  decodable video. Phase 3 (FFmpeg) is where invalid media will be detected and the video marked `failed`.
- Expiry is lazy (no background job) and orphaned objects rely on a future lifecycle rule.
- A user can have several uploads for different videos at once, but cannot re-upload into the same video: a failed
  or cancelled upload means deleting the video and starting a new one.
- Nothing is enqueued for processing yet (`QUEUE_BACKEND` is untouched); Phase 3 will enqueue when an upload becomes `uploaded`.
