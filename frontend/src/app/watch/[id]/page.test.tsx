import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "@/lib/api";
import { makeVideo } from "@/test/fixtures";
import WatchPage from "./page";

vi.mock("next/navigation", () => ({ useParams: () => ({ id: "vid-1" }) }));
vi.mock("@/components/VideoPlayer", () => ({
  VideoPlayer: ({ src, title }: { src: string; title: string }) => <div data-testid="player" data-src={src} aria-label={title} />,
}));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return { ...actual, api: { getVideo: vi.fn() } };
});

beforeEach(() => {
  vi.resetAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("WatchPage", () => {
  it("plays a ready video from its HLS URL", async () => {
    vi.mocked(api.getVideo).mockResolvedValue(
      makeVideo({
        status: "ready",
        title: "Ready video",
        description: "About it",
        playback_url: "/api/v1/videos/vid-1/hls/master.m3u8",
        thumbnail_url: "/api/v1/videos/vid-1/thumbnail",
      }),
    );
    render(<WatchPage />);

    const player = await screen.findByTestId("player");
    expect(player).toHaveAttribute("data-src", "/api/v1/videos/vid-1/hls/master.m3u8");
    expect(screen.getByRole("heading", { name: "Ready video" })).toBeInTheDocument();
    expect(screen.getByText("About it")).toBeInTheDocument();
  });

  it("shows queued and processing states with progress instead of a player", async () => {
    vi.mocked(api.getVideo).mockResolvedValue(makeVideo({ status: "processing", processing_progress: 55 }));
    render(<WatchPage />);

    expect(await screen.findByText("Processing 55%")).toBeInTheDocument();
    expect(screen.getByRole("progressbar", { name: "Processing progress" })).toHaveAttribute("aria-valuenow", "55");
    expect(screen.queryByTestId("player")).not.toBeInTheDocument();
  });

  it("polls until processing finishes, then shows the player", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    // Two "queued" answers, not one: when the first response arrives the page starts polling, which
    // re-runs its effect and fetches once more straight away. With a single "queued" answer that second
    // fetch returned "ready" immediately, so "Queued" could vanish before the assertion ran (flaky under load).
    vi.mocked(api.getVideo)
      .mockResolvedValueOnce(makeVideo({ status: "queued", processing_progress: 0 }))
      .mockResolvedValueOnce(makeVideo({ status: "queued", processing_progress: 0 }))
      .mockResolvedValue(makeVideo({ status: "ready", playback_url: "/api/v1/videos/vid-1/hls/master.m3u8" }));
    render(<WatchPage />);

    expect(await screen.findByText("Queued")).toBeInTheDocument();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3100);
    });
    expect(await screen.findByTestId("player")).toBeInTheDocument();

    // Once ready, polling stops.
    const calls = vi.mocked(api.getVideo).mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(vi.mocked(api.getVideo).mock.calls.length).toBe(calls);
  });

  it("shows why processing failed", async () => {
    vi.mocked(api.getVideo).mockResolvedValue(
      makeVideo({ status: "failed", processing_error: "This video couldn't be processed: the file does not contain a video track." }),
    );
    render(<WatchPage />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/does not contain a video track/);
    expect(screen.queryByTestId("player")).not.toBeInTheDocument();
  });

  it("handles videos that are missing or private", async () => {
    vi.mocked(api.getVideo).mockRejectedValue(new ApiError(404, "video_not_found", "Video not found."));
    render(<WatchPage />);
    expect(await screen.findByRole("heading", { name: "Video not found" })).toBeInTheDocument();
  });

  it("handles API errors", async () => {
    vi.mocked(api.getVideo).mockRejectedValue(new ApiError(502, "storage_unavailable", "Object storage is unavailable."));
    render(<WatchPage />);
    expect(await screen.findByRole("heading", { name: "Couldn't load this video" })).toBeInTheDocument();
    expect(screen.getByText("Object storage is unavailable.")).toBeInTheDocument();
  });
});
