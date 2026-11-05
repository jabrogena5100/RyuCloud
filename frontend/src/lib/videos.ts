import type { Video, VideoCardData } from "./types";

export function toVideoCardData(video: Video): VideoCardData {
  return {
    id: video.id,
    title: video.title,
    channelName: video.channel.name,
    views: video.view_count,
    publishedAt: video.published_at,
    durationSeconds: video.duration_seconds,
    thumbnailUrl: video.thumbnail_url,
  };
}

/** Shown on the home page until real uploads exist (later phase). */
export const PLACEHOLDER_VIDEOS: VideoCardData[] = [
  ["Deploying a Go API behind an AWS Application Load Balancer", "Cloud Native Notes", 128_400, 1_385],
  ["Terraform state, explained with real-world mistakes", "Infra Diaries", 48_210, 942],
  ["HLS adaptive streaming from scratch with FFmpeg", "Pixel Pipeline", 302_000, 2_210],
  ["Designing an SQS worker that never loses a job", "Queue Theory", 19_870, 1_104],
  ["Postgres indexing for feeds: a practical guide", "Data Plumbing", 76_530, 1_756],
  ["CloudFront signed URLs in 10 minutes", "Edge Cases", 9_412, 611],
  ["GitHub Actions CI for a Go + Next.js monorepo", "Ship It Weekly", 54_003, 1_322],
  ["Presigned S3 uploads: keep bytes out of your API", "Cloud Native Notes", 88_765, 845],
  ["Docker Compose for local dev that matches prod", "Infra Diaries", 33_190, 1_037],
  ["Argon2id vs bcrypt: hashing passwords in 2026", "Secure by Default", 41_287, 728],
  ["Building a YouTube-style layout with Tailwind", "Frontend Forge", 67_002, 1_498],
  ["Observability basics: structured logs and request IDs", "Ship It Weekly", 12_654, 903],
].map(([title, channelName, views, durationSeconds], i) => ({
  id: `placeholder-${i + 1}`,
  title: title as string,
  channelName: channelName as string,
  views: views as number,
  publishedAt: new Date(Date.UTC(2026, 9, 30 - i * 3)).toISOString(),
  durationSeconds: durationSeconds as number,
  thumbnailUrl: null,
}));
