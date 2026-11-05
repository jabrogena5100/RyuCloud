"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useEffect, useState } from "react";
import { StatusBadge, isInProgress } from "@/components/StatusBadge";
import { VideoPlayer } from "@/components/VideoPlayer";
import { ApiError, api, describeError } from "@/lib/api";
import { formatRelativeTime, formatViews } from "@/lib/format";
import type { Video } from "@/lib/types";

const POLL_MS = 3000;

type State =
  | { kind: "loading" }
  | { kind: "missing" }
  | { kind: "error"; message: string }
  | { kind: "loaded"; video: Video };

/** Watch page. A video that is still being processed shows live status instead of a player. */
export default function WatchPage() {
  const { id } = useParams<{ id: string }>();
  const [state, setState] = useState<State>({ kind: "loading" });

  const status = state.kind === "loaded" ? state.video.status : null;
  const waiting = status !== null && isInProgress(status);

  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .getVideo(id)
        .then((video) => !cancelled && setState({ kind: "loaded", video }))
        .catch((error: unknown) => {
          if (cancelled) {
            return;
          }
          if (error instanceof ApiError && error.status === 404) {
            setState({ kind: "missing" });
          } else {
            setState((current) => (current.kind === "loaded" ? current : { kind: "error", message: describeError(error) }));
          }
        });

    void load();
    const timer = waiting ? setInterval(() => void load(), POLL_MS) : undefined;
    return () => {
      cancelled = true;
      if (timer) {
        clearInterval(timer);
      }
    };
  }, [id, waiting]);

  if (state.kind === "loading") {
    return <div className="mx-auto aspect-video w-full max-w-4xl animate-pulse rounded-xl bg-neutral-200 dark:bg-neutral-800" />;
  }
  if (state.kind === "missing" || state.kind === "error") {
    return (
      <div className="mx-auto mt-6 max-w-md text-center">
        <h1 className="mb-2 text-xl font-semibold">{state.kind === "missing" ? "Video not found" : "Couldn't load this video"}</h1>
        <p className="mb-4 text-sm text-neutral-600 dark:text-neutral-400">
          {state.kind === "missing" ? "It may be private, still processing, or deleted." : state.message}
        </p>
        <Link href="/" className="font-medium text-blue-600 hover:underline dark:text-blue-400">
          Back to home
        </Link>
      </div>
    );
  }

  const { video } = state;
  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-4">
      {video.status === "ready" && video.playback_url ? (
        <VideoPlayer src={video.playback_url} poster={video.thumbnail_url} title={video.title} />
      ) : (
        <div className="flex aspect-video w-full flex-col items-center justify-center gap-3 rounded-xl bg-neutral-100 p-6 text-center dark:bg-neutral-900">
          <StatusBadge status={video.status} progress={video.processing_progress} />
          {video.status === "failed" ? (
            <p role="alert" className="max-w-md text-sm text-red-700 dark:text-red-300">
              {video.processing_error ?? "This video couldn't be processed."}
            </p>
          ) : (
            <p className="max-w-md text-sm text-neutral-600 dark:text-neutral-400">
              {video.status === "queued" && "Your video is in the queue. This page updates automatically."}
              {video.status === "processing" && "Your video is being prepared for streaming. This page updates automatically."}
              {(video.status === "draft" || video.status === "uploading") && "This video hasn't finished uploading."}
            </p>
          )}
          {typeof video.processing_progress === "number" && (video.status === "queued" || video.status === "processing") && (
            <div
              role="progressbar"
              aria-label="Processing progress"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={video.processing_progress}
              className="h-2 w-64 max-w-full overflow-hidden rounded-full bg-neutral-300 dark:bg-neutral-700"
            >
              <div className="h-full bg-blue-600 transition-[width]" style={{ width: `${video.processing_progress}%` }} />
            </div>
          )}
        </div>
      )}

      <div>
        <h1 className="text-xl font-semibold">{video.title}</h1>
        <p className="mt-1 text-sm text-neutral-600 dark:text-neutral-400">
          {video.channel.name} · {formatViews(video.view_count)}
          {video.published_at && <> · {formatRelativeTime(video.published_at)}</>}
          {video.visibility !== "public" && <> · {video.visibility}</>}
        </p>
        {video.description && <p className="mt-4 whitespace-pre-wrap text-sm">{video.description}</p>}
      </div>
    </div>
  );
}
