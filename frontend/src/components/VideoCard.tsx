import { formatDuration, formatRelativeTime, formatViews } from "@/lib/format";
import type { VideoCardData } from "@/lib/types";

/** Deterministic gradient so each placeholder thumbnail looks distinct. */
function placeholderGradient(seed: string): string {
  let hash = 0;
  for (const char of seed) {
    hash = (hash * 31 + char.charCodeAt(0)) % 360;
  }
  return `linear-gradient(135deg, hsl(${hash} 70% 55%), hsl(${(hash + 50) % 360} 65% 30%))`;
}

export function VideoCard({ video }: { video: VideoCardData }) {
  return (
    <article className="flex flex-col gap-3">
      <div className="relative aspect-video overflow-hidden rounded-xl bg-neutral-200 dark:bg-neutral-800">
        {video.thumbnailUrl ? (
          // Plain <img>: thumbnails will come from a CDN domain configured in a later phase.
          <img src={video.thumbnailUrl} alt="" loading="lazy" className="h-full w-full object-cover" />
        ) : (
          <div aria-hidden="true" className="h-full w-full" style={{ background: placeholderGradient(video.id) }} />
        )}
        {video.durationSeconds !== null && (
          <span className="absolute bottom-1.5 right-1.5 rounded bg-black/80 px-1.5 py-0.5 text-xs font-medium text-white">
            {formatDuration(video.durationSeconds)}
          </span>
        )}
      </div>
      <div className="flex gap-3">
        <div
          aria-hidden="true"
          className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-neutral-300 text-sm font-semibold dark:bg-neutral-700"
        >
          {video.channelName.charAt(0).toUpperCase()}
        </div>
        <div className="min-w-0">
          <h3 className="line-clamp-2 text-sm font-semibold leading-snug">{video.title}</h3>
          <p className="mt-1 text-sm text-neutral-600 dark:text-neutral-400">{video.channelName}</p>
          <p className="text-sm text-neutral-600 dark:text-neutral-400">
            {formatViews(video.views)}
            {video.publishedAt && <> · {formatRelativeTime(video.publishedAt)}</>}
          </p>
        </div>
      </div>
    </article>
  );
}
