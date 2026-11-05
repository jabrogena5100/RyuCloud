"use client";

import { useEffect, useState } from "react";
import { VideoGrid, VideoGridSkeleton } from "@/components/VideoGrid";
import { api } from "@/lib/api";
import type { VideoCardData } from "@/lib/types";
import { PLACEHOLDER_VIDEOS, toVideoCardData } from "@/lib/videos";

const CATEGORIES = ["All", "Cloud", "DevOps", "Streaming", "Databases", "Security", "Frontend"];

type FeedState =
  | { status: "loading" }
  | { status: "ready"; videos: VideoCardData[] }
  | { status: "error" };

export default function HomePage() {
  const [feed, setFeed] = useState<FeedState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    api
      .listVideos({ limit: 24 })
      .then((page) => !cancelled && setFeed({ status: "ready", videos: page.items.map(toVideoCardData) }))
      .catch(() => !cancelled && setFeed({ status: "error" }));
    return () => {
      cancelled = true;
    };
  }, []);

  const realVideos = feed.status === "ready" ? feed.videos : [];
  const showPlaceholders = feed.status === "error" || (feed.status === "ready" && realVideos.length === 0);

  return (
    <div className="flex flex-col gap-6">
      {/* Category chips are decorative until search/filtering exists. */}
      <div className="flex gap-3 overflow-x-auto pb-1">
        {CATEGORIES.map((category, i) => (
          <span
            key={category}
            className={`shrink-0 rounded-lg px-3 py-1.5 text-sm font-medium ${
              i === 0
                ? "bg-neutral-900 text-white dark:bg-white dark:text-neutral-900"
                : "bg-neutral-100 dark:bg-neutral-800"
            }`}
          >
            {category}
          </span>
        ))}
      </div>

      {showPlaceholders && (
        <p
          role="status"
          className="rounded-lg border border-amber-300 bg-amber-50 px-4 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200"
        >
          {feed.status === "error"
            ? "Couldn't reach the API, so sample videos are shown."
            : "No published videos yet. These are sample cards: uploaded videos appear here once processing (a later phase) publishes them."}
        </p>
      )}

      {feed.status === "loading" ? (
        <VideoGridSkeleton />
      ) : (
        <VideoGrid videos={showPlaceholders ? PLACEHOLDER_VIDEOS : realVideos} />
      )}
    </div>
  );
}
