"use client";

import { useEffect, useRef, useState } from "react";

type VideoPlayerProps = {
  /** URL of the HLS master playlist (served by the API with the viewer's session cookie). */
  src: string;
  poster?: string | null;
  title: string;
};

/**
 * HLS player. Safari/iOS play HLS natively; other browsers get hls.js (loaded only
 * when needed, so it stays out of every other page's bundle). The playlist and
 * segment requests are same-origin, so the session cookie authorizes private media.
 */
export function VideoPlayer({ src, poster, title }: VideoPlayerProps) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    setError(null);

    if (video.canPlayType("application/vnd.apple.mpegurl")) {
      video.src = src;
      return () => {
        video.removeAttribute("src");
        video.load();
      };
    }

    let cancelled = false;
    let destroy: (() => void) | undefined;
    import("hls.js")
      .then(({ default: Hls }) => {
        if (cancelled) {
          return;
        }
        if (!Hls.isSupported()) {
          setError("This browser can't play streaming video.");
          return;
        }
        const hls = new Hls();
        destroy = () => hls.destroy();
        hls.on(Hls.Events.ERROR, (_event, data) => {
          if (data.fatal) {
            setError("The video couldn't be loaded. Try refreshing the page.");
          }
        });
        hls.loadSource(src);
        hls.attachMedia(video);
      })
      .catch(() => {
        if (!cancelled) {
          setError("The video player couldn't be loaded.");
        }
      });

    return () => {
      cancelled = true;
      destroy?.();
    };
  }, [src]);

  return (
    <div className="flex flex-col gap-2">
      <video
        ref={videoRef}
        controls
        playsInline
        preload="metadata"
        poster={poster ?? undefined}
        aria-label={title}
        className="aspect-video w-full rounded-xl bg-black"
      />
      {error && (
        <p role="alert" className="rounded-lg bg-red-50 px-3 py-2 text-sm text-red-700 dark:bg-red-950 dark:text-red-300">
          {error}
        </p>
      )}
    </div>
  );
}
