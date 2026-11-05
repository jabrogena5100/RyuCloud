# Asynchronous video processing (Phase 3)

An uploaded video is converted to adaptive-bitrate HLS and given a thumbnail by a
separate **Go worker** (`backend/cmd/worker`). The worker runs independently of the HTTP
API; the two share only PostgreSQL, the job queue and object storage.

```
Browser ─PUT file─► MinIO (uploads/…/source)
   │
   └─POST /uploads/{id}/complete─► API: verify object, then ONE transaction:
                                        video_uploads=uploaded, videos=queued, INSERT processing_jobs
                                   API: publish job id ─► Redis list  (best effort; the row is already durable)

Worker:  receive job id ─► claim in PostgreSQL (lease) ─► download source ─► ffprobe + validate
         ─► ffmpeg per rendition (360p / 720p) ─► ffmpeg thumbnail ─► upload to MinIO (videos/{id}/…)
         ─► ONE transaction: job=succeeded, videos=ready (+keys, duration), video_renditions rows
         ─► only now: acknowledge the queue message

Browser: polls video status ─► READY ─► hls.js plays /api/v1/videos/{id}/hls/master.m3u8
```

## Video lifecycle

The API keeps lower-case status values (existing contract). The product states map as:

| API `status` | Product state | Meaning |
| --- | --- | --- |
| `draft` | PENDING_UPLOAD | Metadata only; no file yet |
| `uploading` | UPLOADING | Presigned URL issued; file not confirmed |
| `queued` | QUEUED | File verified; job waiting for a worker (also: waiting to retry) |
| `processing` | PROCESSING | A worker holds the job; `processing_progress` is 0-100 |
| `ready` | READY | HLS + thumbnail stored; playable |
| `failed` | FAILED | Upload expired/cancelled or processing failed for good; `processing_error` says why |

Only `ready` videos are playable, and only `ready` + `public`/`unlisted` videos are visible to
anyone but the owner.

## Where state lives

**PostgreSQL is the source of truth.** The queue carries only a job ID, so a lost, duplicated,
reordered or stale message can never corrupt anything.

`processing_jobs` (one row per video): `state` (`queued|processing|retry|succeeded|failed`),
`stage`, `progress`, `attempts`, `max_attempts`, `next_run_at`, `locked_by`, `lease_expires_at`,
`last_published_at`, `last_error`, `started_at`, `finished_at`. CHECK constraints guarantee, for
example, that a `processing` job always has an owner and a lease, and that `attempts <= max_attempts`.

`video_renditions`: one row per rendition (`name`, size, `bandwidth_bps`, `segment_count`,
`playlist_key`). The master playlist and thumbnail keys live on `videos`
(`hls_manifest_key`, `thumbnail_object_key`).

## Job state machine

```
queued ──claim──► processing ──success──────────────────────► succeeded
  ▲                  │   │
  │  release         │   ├─ permanent error ──────────────────► failed
  └──────────────────┤   ├─ transient error, attempts left ──► retry ──(backoff elapsed, claim)──► processing
                     │   └─ transient error, no attempts left ► failed
                     └─ worker died: lease expires ──► (sweeper re-sends; next claim = new attempt)
```

Every transition is one conditional `UPDATE` (or a short transaction that also updates the video):

- **Claim**: `… WHERE id = $1 AND attempts < max_attempts AND ((state IN ('queued','retry') AND next_run_at <= now()) OR (state = 'processing' AND lease_expires_at < now()))`.
  Of any number of concurrent workers, exactly one matches; the rest get zero rows and drop the message.
- **Heartbeat / progress / succeed / retry / fail / release** all require `locked_by = <this worker> AND state = 'processing'`.
  A worker that lost its lease gets `ErrLeaseLost` and abandons its work without touching the job.

## Idempotency

- **Duplicate messages** (retries, republishing, double delivery): the second claim finds nothing to claim.
- **Duplicate completion** of an upload: `INSERT … ON CONFLICT (video_id) DO NOTHING` creates at most one job, and only the request that made the transition publishes.
- **Duplicate outputs**: output keys depend only on the video ID (`videos/{id}/hls/…`, `videos/{id}/thumbnail.jpg`).
  Before uploading, the worker deletes everything under `videos/{id}/` (leftovers of a crashed attempt), and the
  renditions rows are replaced, not appended. Reprocessing therefore overwrites; it never accumulates.
- **Upload order**: segments, then media playlists, then the master playlist last, so anything that can see the
  master can rely on everything it references.

## Retries

| Failure | Handling |
| --- | --- |
| Corrupt/unreadable file, no video track, too long, too large, unsupported (ffprobe or ffmpeg says the *input* is bad) | **Permanent**: job `failed` immediately (attempt 1), video `failed` with an owner-facing message. Retrying cannot help. |
| Source object missing | Permanent. |
| Storage/database/ffmpeg crash, out of memory, timeout | **Transient**: `retry` with backoff, up to `WORKER_MAX_ATTEMPTS` (default 4) in total |
| Attempts exhausted | Job and video `failed` ("Processing failed after several attempts…") |
| Thumbnail failure | Not fatal: tried at the chosen time, then at the first frame; the video becomes `ready` without a poster |

**Backoff**: `delay = min(WORKER_RETRY_BASE_SECONDS × 2^(attempt−1), WORKER_RETRY_MAX_SECONDS)`, spread ±20 % by
random jitter so retries do not synchronise. Defaults: 30 s, 60 s, 120 s (cap 15 min). During backoff the video shows
`queued`. The job is not on the queue while it waits; the sweeper re-sends it when `next_run_at` passes.

## Recovery from crashes and lost messages

A worker **claims with a lease** (`WORKER_LEASE_SECONDS`, default 120 s) and renews it every third of that while
working. The **sweeper** (inside every worker, every `WORKER_SWEEP_SECONDS`, default 15 s) does two things:

1. Fails jobs whose worker died on their last allowed attempt (lease expired, `attempts >= max_attempts`).
2. Re-sends to the queue every job that should be running but has no live owner: retries whose backoff elapsed,
   `queued` jobs whose publish was lost (Redis down when the API tried), and `processing` jobs whose **lease expired**
   (their worker crashed). `FOR UPDATE SKIP LOCKED` keeps concurrent sweepers from picking the same jobs, and a job is
   re-sent at most once per `WORKER_REPUBLISH_SECONDS` (default 300 s) unless it is a retry or crash recovery.

Consequences:

- **Worker killed mid-job** → lease expires (≤ 2 min) → sweeper re-sends → another worker claims it (attempt +1) and
  redoes the work from scratch. The crashed attempt's leftovers are removed before the new upload.
- **Redis restarts or loses its data** → nothing is lost: sweepers re-publish from PostgreSQL.
- **API cannot reach Redis when an upload completes** → the upload still succeeds (HTTP 200); the sweeper publishes the job.
- **A message sits "in flight" in Redis after its worker died** → `Requeue` removes the stale in-flight copy and re-adds the id.
- **PostgreSQL unreachable when a job finishes** → the outcome cannot be stored, so the message is **not acknowledged**;
  the lease expires and the job is retried. Acknowledging happens only after the result transaction commits.

## Concurrency and shutdown

- `WORKER_CONCURRENCY` (default **1**) jobs run at once; each uses `FFMPEG_THREADS` (default 2) FFmpeg threads.
  Compose also limits the container (`WORKER_CPUS`, default 2; `WORKER_MEMORY`, default 2g). Scale out by running more
  worker containers (`docker compose up -d --scale worker=2`); the claim logic makes that safe.
- **Graceful shutdown** (SIGTERM, `docker compose stop`): the worker stops taking jobs, lets running ones finish for up to
  `WORKER_SHUTDOWN_GRACE_SECONDS` (default 60; Compose waits 90 s), and then interrupts and **releases** them back to
  `queued` *without consuming an attempt*.
- Each job has a hard time limit (`WORKER_JOB_TIMEOUT_SECONDS`, default 30 min) and sources longer than
  `WORKER_MAX_DURATION_MINUTES` (default 120) are rejected before any transcoding.

## FFmpeg details

- ffprobe validates the source (video track, sane dimensions and duration, rotation metadata) before anything is encoded.
- Per rendition: H.264 (libx264, main profile, `yuv420p`, CRF 23 with a `maxrate` cap) + AAC 128 kb/s stereo,
  keyframes every 2 s, 6-second MPEG-TS segments, VOD playlists.
- **Renditions**: 360p if the source is at least 360 px tall, 720p if at least 720 px. A source smaller than 360p gets a
  single rendition at its own size. Nothing is ever upscaled. Widths follow the source aspect ratio (always even).
- Audio is optional; sources without audio work.
- Thumbnail: one JPEG ≤ 640 px wide, taken at `min(duration/2, 2 s)`.
- The master playlist is generated by the worker from measured segment sizes (`BANDWIDTH` = peak segment bitrate).
- **Security**: ffmpeg/ffprobe run via `exec.Command` with an argument list, never a shell; every path is generated by
  the worker (a temp directory it creates), so no user-controlled text reaches a command line. Output keys contain only
  server-generated IDs and fixed names. Each job works in its own temp directory, removed afterwards (and stale ones are
  removed at worker start).

## Object storage layout (private bucket)

```
uploads/{videoID}/{uploadID}/source       original upload, kept, never served
videos/{videoID}/hls/master.m3u8
videos/{videoID}/hls/360p/index.m3u8
videos/{videoID}/hls/360p/seg_00000.ts …
videos/{videoID}/hls/720p/…
videos/{videoID}/thumbnail.jpg
```

Nothing is public. Deleting a video deletes its upload(s) and the whole `videos/{id}/` prefix.

## Playback and authorization (local development)

`GET /api/v1/videos/{id}/hls/{master.m3u8 | {N}p/index.m3u8 | {N}p/seg_NNNNN.ts}` and `GET /api/v1/videos/{id}/thumbnail`
stream objects from private storage after applying the same visibility rule as `GET /videos/{id}`. Only the exact file
names the worker produces are accepted (whitelisted by pattern), the response `Content-Type` is chosen by the API, and
`X-Content-Type-Options: nosniff` is set. hls.js (or native HLS in Safari) fetches same-origin, so the session cookie
authorizes private videos.

This puts media bytes through the API, acceptable for local development only. The `video.playback_url` /
`thumbnail_url` fields come from a `PlaybackURLProvider`, so **Phase 4** swaps in a CloudFront provider (signed URLs or
cookies, Origin Access Control to the private bucket) and the API stops carrying media; clients keep using the same fields.

## Moving to AWS

| Local | AWS | What changes |
| --- | --- | --- |
| Redis (`RedisQueue`) | SQS | Implement `integrations.Queue` with SQS (`SendMessage`, `ReceiveMessage` with a visibility timeout, `DeleteMessage` = `Ack`, `ChangeMessageVisibility`). Job state stays in PostgreSQL (RDS); a standard queue with a dead-letter queue is enough, because duplicates are harmless |
| Worker container (Compose) | ECS/Fargate task or EC2 (autoscale on queue depth) | Same image (`worker` Dockerfile target); IAM task role instead of static keys (`S3_ACCESS_KEY_ID` unset) |
| MinIO | S3 | Config only; `STORAGE_BACKEND=s3`, no endpoints |
| API media route | CloudFront | `PlaybackURLProvider` implementation (Phase 4) |
| `workertmp` volume | Ephemeral storage / EBS | Size for source + all renditions of the longest video |

## Known limitations

- Single-pass, CPU-only x264 encoding of renditions one after another; no GPU, no parallel renditions, no per-title encoding.
- No resume: a retry restarts the whole job. Progress is monotonic within an attempt and resets on retry.
- Redis is a delivery channel, not durable storage; correctness relies on PostgreSQL leases and the sweeper, so crash
  recovery latency is bounded by the lease (2 min by default), not instant.
- If a video is deleted while its job runs, the worker finds its job gone (lease lost) and stops recording, but objects it
  already uploaded under `videos/{id}/` can remain until a bucket lifecycle rule (Phase 4/5 infrastructure) removes them.
- Only MPEG-TS HLS (no fMP4/CMAF), no audio-only or subtitle tracks, no DRM, no seek thumbnails.
- No metrics or alerting yet; use `docker compose logs -f worker` (JSON logs with `job_id`, `video_id`, `attempt`).
