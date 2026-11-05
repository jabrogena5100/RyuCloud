"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { StatusBadge, isInProgress, statusHint } from "@/components/StatusBadge";
import { api, describeError } from "@/lib/api";
import { useAuth } from "@/lib/auth-context";
import { formatDuration, formatRelativeTime } from "@/lib/format";
import type { Video } from "@/lib/types";

type Row = { video: Video; pendingUploadId: string | null };

/** How often to refresh while any video is still moving through the pipeline. (Page files may not export extras.) */
const POLL_INTERVAL_MS = 3000;

const BUTTON =
  "rounded-full border border-neutral-300 px-3 py-1 text-sm font-medium hover:bg-neutral-100 disabled:opacity-60 dark:border-neutral-700 dark:hover:bg-neutral-800";

function Thumbnail({ video }: { video: Video }) {
  return (
    <div className="relative aspect-video w-32 shrink-0 overflow-hidden rounded-lg bg-neutral-200 dark:bg-neutral-800 sm:w-40">
      {video.thumbnail_url ? (
        // Plain <img>: served by the API with the owner's session cookie.
        <img src={video.thumbnail_url} alt="" loading="lazy" className="h-full w-full object-cover" />
      ) : (
        <div aria-hidden="true" className="flex h-full w-full items-center justify-center text-xs text-neutral-500">
          {video.status === "ready" ? "No thumbnail" : "Processing"}
        </div>
      )}
      {video.duration_seconds !== null && (
        <span className="absolute bottom-1 right-1 rounded bg-black/80 px-1 py-0.5 text-[10px] font-medium text-white">
          {formatDuration(video.duration_seconds)}
        </span>
      )}
    </div>
  );
}

function Progress({ video }: { video: Video }) {
  if (video.status !== "queued" && video.status !== "processing") {
    return null;
  }
  const percent = video.processing_progress ?? 0;
  return (
    <div className="mt-2 flex items-center gap-2">
      <div
        role="progressbar"
        aria-label={`Processing progress for ${video.title}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={percent}
        className="h-1.5 w-40 overflow-hidden rounded-full bg-neutral-200 dark:bg-neutral-800"
      >
        <div className="h-full bg-blue-600 transition-[width]" style={{ width: `${percent}%` }} />
      </div>
      <span className="text-xs text-neutral-500">{video.status === "queued" ? "Waiting in queue" : `${percent}%`}</span>
    </div>
  );
}

/** Creator dashboard: every video the signed-in user owns, with its live upload/processing state. */
export default function StudioPage() {
  const { user, status } = useAuth();
  const userId = user?.id;
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const channels = await api.myChannels();
      const [pages, pending] = await Promise.all([
        Promise.all(channels.map((channel) => api.channelVideos(channel.handle, { limit: 50 }))),
        api.listUploads({ status: "pending" }),
      ]);
      const pendingByVideo = new Map(pending.items.map((upload) => [upload.video_id, upload.id]));
      const videos = pages
        .flatMap((page) => page.items)
        .sort((a, b) => b.created_at.localeCompare(a.created_at));
      setRows(videos.map((video) => ({ video, pendingUploadId: pendingByVideo.get(video.id) ?? null })));
      setError(null);
    } catch (err) {
      setError(describeError(err));
    }
  }, []);

  useEffect(() => {
    if (userId) {
      void load();
    }
  }, [userId, load]);

  // Status polling: refresh while anything is uploading, queued or processing, then stop.
  const pipelineActive = rows?.some(({ video }) => isInProgress(video.status)) ?? false;
  useEffect(() => {
    if (!userId || !pipelineActive) {
      return;
    }
    const timer = setInterval(() => void load(), POLL_INTERVAL_MS);
    return () => clearInterval(timer);
  }, [userId, pipelineActive, load]);

  async function act(videoId: string, action: () => Promise<unknown>) {
    setBusyId(videoId);
    try {
      await action();
      await load();
    } catch (err) {
      setError(describeError(err));
    } finally {
      setBusyId(null);
    }
  }

  if (status === "loading") {
    return <div className="h-64 animate-pulse rounded-2xl bg-neutral-100 dark:bg-neutral-900" />;
  }
  if (!user) {
    return (
      <div className="mx-auto mt-6 max-w-md rounded-2xl border border-neutral-200 p-6 text-center dark:border-neutral-800">
        <h1 className="mb-2 text-xl font-semibold">Sign in to see your videos</h1>
        <Link href="/login" className="inline-block rounded-full bg-blue-600 px-5 py-2 font-medium text-white hover:bg-blue-700">
          Sign in
        </Link>
      </div>
    );
  }

  return (
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-6">
      <div className="flex items-center justify-between gap-3">
        <h1 className="text-2xl font-semibold">Your videos</h1>
        <div className="flex gap-2">
          <button type="button" onClick={() => void load()} className={BUTTON}>
            Refresh
          </button>
          <Link href="/upload" className="rounded-full bg-blue-600 px-4 py-1 text-sm font-medium text-white hover:bg-blue-700">
            Upload
          </Link>
        </div>
      </div>

      {error && (
        <p role="alert" className="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950 dark:text-red-300">
          {error}
        </p>
      )}

      {rows === null ? (
        !error && <div className="h-40 animate-pulse rounded-2xl bg-neutral-100 dark:bg-neutral-900" />
      ) : rows.length === 0 ? (
        <p className="rounded-2xl border border-dashed border-neutral-300 p-8 text-center text-neutral-600 dark:border-neutral-700 dark:text-neutral-400">
          You haven&apos;t uploaded anything yet.{" "}
          <Link href="/upload" className="font-medium text-blue-600 hover:underline dark:text-blue-400">
            Upload your first video
          </Link>
        </p>
      ) : (
        <ul className="flex flex-col divide-y divide-neutral-200 rounded-2xl border border-neutral-200 dark:divide-neutral-800 dark:border-neutral-800">
          {rows.map(({ video, pendingUploadId }) => (
            <li key={video.id} className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center">
              <Thumbnail video={video} />
              <div className="min-w-0 flex-1">
                <h2 className="truncate font-medium">
                  {video.status === "ready" ? (
                    <Link href={`/watch/${video.id}`} className="hover:underline">
                      {video.title}
                    </Link>
                  ) : (
                    video.title
                  )}
                </h2>
                <p className="text-sm text-neutral-600 dark:text-neutral-400">
                  {video.channel.name} · {video.visibility} · {formatRelativeTime(video.created_at)}
                </p>
                {video.status === "failed" && video.processing_error ? (
                  <p className="mt-1 text-sm text-red-700 dark:text-red-300">{video.processing_error}</p>
                ) : (
                  <p className="mt-1 text-xs text-neutral-500">{statusHint(video.status)}</p>
                )}
                <Progress video={video} />
              </div>
              <div className="flex shrink-0 flex-wrap items-center gap-2">
                <StatusBadge status={video.status} progress={video.processing_progress} />
                {video.status === "ready" && (
                  <Link href={`/watch/${video.id}`} className={BUTTON}>
                    Watch
                  </Link>
                )}
                {pendingUploadId && (
                  <button
                    type="button"
                    disabled={busyId === video.id}
                    onClick={() => void act(video.id, () => api.cancelUpload(pendingUploadId))}
                    className={BUTTON}
                  >
                    Cancel upload
                  </button>
                )}
                <button
                  type="button"
                  disabled={busyId === video.id}
                  onClick={() => {
                    if (window.confirm(`Delete “${video.title}”? This also deletes the uploaded file and all generated streams.`)) {
                      void act(video.id, () => api.deleteVideo(video.id));
                    }
                  }}
                  className={BUTTON}
                >
                  Delete
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
