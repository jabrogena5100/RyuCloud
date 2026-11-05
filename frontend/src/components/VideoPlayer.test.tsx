import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { VideoPlayer } from "./VideoPlayer";

// hls.js needs a real browser; replace it with a recording fake.
const hlsInstance = {
  on: vi.fn(),
  loadSource: vi.fn(),
  attachMedia: vi.fn(),
  destroy: vi.fn(),
};
const HlsMock = Object.assign(
  vi.fn(() => hlsInstance),
  { isSupported: vi.fn(() => true), Events: { ERROR: "hlsError" } },
);
vi.mock("hls.js", () => ({ default: HlsMock }));

beforeEach(() => {
  vi.clearAllMocks();
  HlsMock.isSupported.mockReturnValue(true);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("VideoPlayer", () => {
  it("plays the master playlist through hls.js where HLS is not native", async () => {
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    const { unmount } = render(<VideoPlayer src="/api/v1/videos/v1/hls/master.m3u8" poster="/api/v1/videos/v1/thumbnail" title="Demo" />);

    await waitFor(() => expect(hlsInstance.loadSource).toHaveBeenCalledWith("/api/v1/videos/v1/hls/master.m3u8"));
    expect(hlsInstance.attachMedia).toHaveBeenCalledWith(screen.getByLabelText("Demo"));
    expect(screen.getByLabelText("Demo")).toHaveAttribute("poster", "/api/v1/videos/v1/thumbnail");

    unmount();
    expect(hlsInstance.destroy).toHaveBeenCalled();
  });

  it("uses native HLS (Safari) without loading hls.js", () => {
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("maybe");
    render(<VideoPlayer src="/api/v1/videos/v1/hls/master.m3u8" title="Native" />);

    expect(screen.getByLabelText("Native")).toHaveAttribute("src", "/api/v1/videos/v1/hls/master.m3u8");
    expect(HlsMock).not.toHaveBeenCalled();
  });

  it("explains when the browser cannot play streaming video", async () => {
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    HlsMock.isSupported.mockReturnValue(false);
    render(<VideoPlayer src="/x.m3u8" title="Old browser" />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/can't play streaming video/i);
  });

  it("reports fatal playback errors", async () => {
    vi.spyOn(HTMLMediaElement.prototype, "canPlayType").mockReturnValue("");
    render(<VideoPlayer src="/x.m3u8" title="Broken" />);
    await waitFor(() => expect(hlsInstance.on).toHaveBeenCalled());

    const onError = hlsInstance.on.mock.calls.find(([event]) => event === "hlsError")?.[1] as (e: string, d: { fatal: boolean }) => void;
    onError("hlsError", { fatal: false });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    onError("hlsError", { fatal: true });
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't be loaded/i);
  });
});
