# AWS architecture (Phase 4)

Everything described here is **defined but not deployed**. `infra/terraform` creates it only when you run
`terraform apply` (see [aws-runbook.md](aws-runbook.md)). Local development is unchanged and still runs on
Docker Compose with MinIO and Redis.

```
                         Route 53  (app.example.com, media.app.example.com)
                              │                              │
             ┌────────────────▼───────────────┐   ┌─────────▼──────────────────────┐
 Browser ───►│ ALB  (HTTPS, ACM cert)         │   │ CloudFront  (ACM cert, us-east-1)│◄── signed cookies / URLs
             │  /api/*  → api   ·  else → web │   │  trusted key group + OAC         │
             └───────┬───────────────┬────────┘   └─────────┬──────────────────────┘
                     │               │                      │ SigV4 (Origin Access Control)
              ┌──────▼─────┐  ┌──────▼─────┐         ┌──────▼───────────────────────┐
              │ api (ECS)  │  │ web (ECS)  │         │ S3 media bucket (private)    │
              │ Go, :8000  │  │ Next, :3000│         │  uploads/…  originals        │
              └──┬───┬───┬─┘  └────────────┘         │  videos/…   HLS + thumbnails │
   presign/Head  │   │   │ SendMessage                └──────▲────────────▲──────────┘
   ──────────────┘   │   └──────────┐                        │ PUT (presigned, browser)
                     │              ▼                        │            │ get source / put HLS
                     │        ┌───────────┐  receive/delete  │     ┌──────┴──────────┐
                     │        │ SQS jobs  ├──────────────────┼────►│ worker (ECS)    │
                     │        │  └─► DLQ  │                  │     │ Go + FFmpeg     │
                     │        └───────────┘                  │     │ Fargate Spot    │
                     ▼                                       │     └──────┬──────────┘
              ┌───────────────┐◄─────────────────────────────┘            │ job state
              │ RDS PostgreSQL│◄───────────────────────────────────────────┘
              │ private subnet│
              └───────────────┘
```

## Local vs AWS

| Concern | Local (Docker Compose) | AWS (Terraform) | Switch |
| --- | --- | --- | --- |
| Object storage | MinIO | S3 (private bucket, SSE-S3) | `S3_ENDPOINT` empty → AWS; IAM role replaces access keys |
| Queue | Redis lists | SQS standard queue + dead-letter queue | `QUEUE_BACKEND=sqs` |
| Media delivery | API streams from storage | CloudFront, private origin, signed access | `PLAYBACK_BACKEND=cloudfront` |
| Database | `postgres:17-alpine` container | RDS PostgreSQL 17 | `DB_HOST`/`DB_PASSWORD` (or `DATABASE_URL`) |
| Secrets | `.env` file (git-ignored) | Secrets Manager → injected as env by ECS | `secrets` in task definition |
| Logs | `docker compose logs` | CloudWatch Logs | same JSON lines, `awslogs` driver |

The seams already existed (`ObjectStorage`, `Queue`, `PlaybackURLProvider`); Phase 4 adds one adapter behind each
and no handler changed its contract. The only behavior change in a handler is that `GET /videos/{id}` sets the
playback cookies when CloudFront delivery is on.

## Request flows

**Upload.** Browser → API (`POST /uploads`) → API signs an S3 presigned `PUT` with its task role → browser `PUT`s
straight to S3 (CORS allows only the app origin) → API verifies with `HeadObject`, creates the job row and sends the
job ID to SQS. Video bytes never touch the ALB, API or Next.js.

**Processing.** The worker long-polls SQS, claims the job in PostgreSQL (lease + heartbeat), downloads the source,
runs FFmpeg, uploads HLS renditions and a thumbnail to `videos/{id}/`, marks the video ready in PostgreSQL, then
deletes the SQS message. PostgreSQL stays the source of truth; SQS only says "look at job X".

**Playback.**
1. Browser calls `GET /api/v1/videos/{id}`. The API applies the normal visibility rules (public / unlisted / private
   owner). If the viewer may watch it, the response contains `playback_url`
   (`https://media.<domain>/videos/{id}/hls/master.m3u8`) and sets three `CloudFront-*` cookies.
2. The cookies are `Secure`, `HttpOnly`, scoped to `Domain=<app host>` and `Path=/videos/{id}/`, and signed (RSA-SHA1,
   the only algorithm CloudFront supports) for `https://media.<domain>/videos/{id}/*` for two hours by default.
3. hls.js requests the playlist and segments from the media host with credentials. CloudFront checks the signature
   against the trusted key group, then serves from cache or fetches from S3 through Origin Access Control.
4. Thumbnails in lists use a **signed URL per image** (one hour) so a feed of many videos does not need a cookie each.

Why cookies for video and URLs for thumbnails: an HLS playlist references segments by relative path, and a query-string
signature on the playlist URL is not inherited by those relative requests. A cookie is, so one signature covers the
playlist, every rendition and every segment.

## Key design decisions and trade-offs

| Decision | Why | Cost / trade-off |
| --- | --- | --- |
| ECS Fargate, no EC2 | No servers to patch; per-second billing; matches the "containers" goal | Higher unit price than EC2 at sustained load |
| Public subnets for tasks, **no NAT gateway** | A NAT gateway is ~$33/month per AZ, more than the API itself. Tasks accept inbound traffic only from the ALB security group | Tasks have public IPs (billed ~$3.65/month each). A private-subnet + NAT or VPC-endpoint layout is the hardening step |
| One ALB, path routing (`/api/*`) | Single origin → first-party session cookie, no CORS, one certificate | The ALB has a fixed hourly cost |
| RDS single-AZ `db.t4g.micro` | Cheapest managed PostgreSQL | An AZ outage means downtime until restore; `db_multi_az = true` doubles the price |
| Worker on Fargate Spot | ~70% cheaper; safe because jobs are leased, retried and handed back on SIGTERM | Interrupted jobs restart from scratch |
| Standard SQS, not FIFO | Duplicates are harmless by design (job state lives in PostgreSQL) | Possible duplicate deliveries (handled) |
| CloudFront signed cookies on a sibling subdomain | Only mechanism that covers relative HLS segment URLs without a Lambda@Edge/URL-rewriting layer | Requires a domain you control (cookies cannot cross `*.cloudfront.net`) |
| Signing implemented with the standard library | No new dependency to pin; CloudFront's format is a small, well-specified RSA-SHA1 policy; unit-tested by round-trip verification | We maintain ~100 lines instead of using the AWS SDK signer |
| Terraform owns task *shape*, CI owns *image* | CI can deploy a new image without a Terraform run; deploys are small and fast | After editing a task definition in Terraform you must roll it out (runbook) |
| Flat Terraform root module | Easy to read for a single environment; modules would be premature | Reuse across environments needs refactoring later |

## SQS semantics worth knowing

- `Requeue` sends a new message; the previous in-flight copy cannot be deleted by the sweeper (only the receiver
  holds its receipt handle). The stale copy reappears after the visibility timeout and is a harmless duplicate:
  the worker finds the job already claimed or finished and acknowledges it.
- Visibility timeout is 1860 s, longer than the worker's 1800 s job limit, so a healthy job is never redelivered.
- After 5 deliveries without acknowledgement a message moves to the dead-letter queue and an alarm fires. The job
  row in PostgreSQL is untouched, so the sweeper can still recover it; the DLQ message is for diagnosis.

## Known limitations (not hidden)

- **Playback cookie lifetime** is 2 hours (`CLOUDFRONT_PLAYBACK_TTL_MINUTES`). A viewer who pauses past that point
  needs to reload the page. Silent refresh is not implemented.
- **Safari native HLS with cross-origin cookies** is configured (`crossOrigin = "use-credentials"`) but unverified
  here; test it on a real Safari before relying on it. hls.js (Chrome/Firefox/Edge) is the primary path.
- **No WAF, no auto scaling, no Multi-AZ database, no VPC endpoints beyond S3.** These are listed in
  [aws-costs.md](aws-costs.md) as upgrade options with their price.
- The API's local `/videos/{id}/hls/...` routes remain registered in AWS. They apply the same visibility checks but
  would stream bytes through the API; nothing links to them when CloudFront delivery is on.
- Nothing in this phase has been run against real AWS. Every claim above is a design, not a measurement.
