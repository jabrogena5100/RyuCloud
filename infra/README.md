# infra/

Infrastructure-as-code lives here. **Phase 1 contains no cloud resources**: local
development runs entirely on Docker Compose (`../docker-compose.yml`) and nothing
in this repository talks to AWS.

Planned for later phases (each will be introduced in its own reviewable change,
with cost notes before anything is applied):

| Area | Planned service | Backend seam that already exists |
| --- | --- | --- |
| Raw uploads, renditions, thumbnails | S3 (private buckets, presigned uploads) | `backend/internal/integrations/storage.go` (`ObjectStorage`) |
| Transcode jobs | SQS + a separate Go/FFmpeg worker (`backend/cmd/worker`) | `backend/internal/integrations/queue.go` (`JobQueue`) |
| Video delivery | CloudFront in front of S3 | `backend/internal/integrations/playback.go` (`PlaybackURLProvider`) |
| Provisioning | Terraform (remote state, least-privilege IAM) | n/a |
| CI/CD | GitHub Actions | n/a |

Expected layout once Terraform is added:

```
infra/
  terraform/
    modules/        # reusable pieces (bucket, queue, cdn, iam)
    envs/dev/       # thin root module per environment
```
