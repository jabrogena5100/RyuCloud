"use client";

import { useEffect, useRef, useState } from "react";

type VideoPlayerProps = {
  /** URL of the HLS master playlist (served by the API with the viewer's session cookie). */
  src: string;
  poster?: string | null;
  title: string;
};

/** True when src is on another origin (the CDN host), where cookies need explicit opt-in. */
function isCrossOrigin(src: string): boolean {
  try {
    return new URL(src, window.location.href).origin !== window.location.origin;
  } catch {
    return false;
  }
}

/**
 * HLS player. Safari/iOS play HLS natively; other browsers get hls.js (loaded only
 * when needed, so it stays out of every other page's bundle). The playlist and
 * segment requests are same-origin locally, so the session cookie authorizes private media.
 * In AWS the playlist lives on the CDN host (a sibling subdomain); the API sets signed
 * CloudFront cookies when it returns the video, and requests to the CDN must send them.
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

    const crossOrigin = isCrossOrigin(src);

    if (video.canPlayType("application/vnd.apple.mpegurl")) {
      if (crossOrigin) {
        video.crossOrigin = "use-credentials";
      }
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
        const hls = new Hls(
          crossOrigin
            ? {
                xhrSetup: (xhr: XMLHttpRequest) => {
                  xhr.withCredentials = true;
                },
                fetchSetup: (context: { url: string }, init: RequestInit) =>
                  new Request(context.url, { ...init, credentials: "include" }),
              }
            : {},
        );
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
