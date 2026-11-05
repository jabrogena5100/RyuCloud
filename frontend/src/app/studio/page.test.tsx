import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "@/lib/api";
import { useAuth } from "@/lib/auth-context";
import { authValue, CHANNEL, makeVideo } from "@/test/fixtures";
import StudioPage from "./page";

vi.mock("@/lib/auth-context", () => ({ useAuth: vi.fn() }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return {
    ...actual,
    api: { myChannels: vi.fn(), channelVideos: vi.fn(), listUploads: vi.fn(), cancelUpload: vi.fn(), deleteVideo: vi.fn() },
  };
});

const VIDEOS = [
  makeVideo({ id: "v-processing", title: "Verified upload", status: "processing", created_at: "2026-11-05T10:00:00Z" }),
  makeVideo({ id: "v-uploading", title: "Interrupted upload", status: "uploading", created_at: "2026-11-05T11:00:00Z" }),
  makeVideo({ id: "v-failed", title: "Expired upload", status: "failed", created_at: "2026-11-05T09:00:00Z" }),
];

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(useAuth).mockReturnValue(authValue(true));
  vi.mocked(api.myChannels).mockResolvedValue([CHANNEL]);
  vi.mocked(api.channelVideos).mockResolvedValue({ items: VIDEOS, limit: 50, offset: 0 });
  vi.mocked(api.listUploads).mockResolvedValue({ items: [{ id: "up-9", video_id: "v-uploading" } as never] });
  vi.mocked(api.cancelUpload).mockResolvedValue(undefined);
  vi.mocked(api.deleteVideo).mockResolvedValue(undefined);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("StudioPage", () => {
  it("lists the creator's videos, newest first, with clear processing states", async () => {
    render(<StudioPage />);

    const rows = await screen.findAllByRole("listitem");
    expect(rows.map((row) => within(row).getByRole("heading").textContent)).toEqual([
      "Interrupted upload",
      "Verified upload",
      "Expired upload",
    ]);
    expect(within(rows[0]).getByText("Uploading")).toBeInTheDocument();
    expect(within(rows[1]).getByText("Processing pending")).toBeInTheDocument();
    expect(within(rows[1]).getByText(/transcoding isn't available yet/i)).toBeInTheDocument();
    expect(within(rows[2]).getByText("Failed")).toBeInTheDocument();
    expect(api.listUploads).toHaveBeenCalledWith({ status: "pending" });
  });

  it("only offers 'Cancel upload' for videos with a pending upload", async () => {
    render(<StudioPage />);
    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]).getByRole("button", { name: "Cancel upload" })).toBeInTheDocument();
    expect(within(rows[1]).queryByRole("button", { name: "Cancel upload" })).not.toBeInTheDocument();
  });

  it("cancels a pending upload and reloads the list", async () => {
    const user = userEvent.setup();
    render(<StudioPage />);
    await user.click(await screen.findByRole("button", { name: "Cancel upload" }));

    expect(api.cancelUpload).toHaveBeenCalledWith("up-9");
    expect(api.channelVideos).toHaveBeenCalledTimes(2);
  });

  it("deletes a video only after confirmation", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm");
    render(<StudioPage />);
    const rows = await screen.findAllByRole("listitem");

    confirm.mockReturnValueOnce(false);
    await user.click(within(rows[1]).getByRole("button", { name: "Delete" }));
    expect(api.deleteVideo).not.toHaveBeenCalled();

    confirm.mockReturnValueOnce(true);
    await user.click(within(rows[1]).getByRole("button", { name: "Delete" }));
    expect(api.deleteVideo).toHaveBeenCalledWith("v-processing");
  });

  it("shows an empty state with a link to upload", async () => {
    vi.mocked(api.channelVideos).mockResolvedValue({ items: [], limit: 50, offset: 0 });
    vi.mocked(api.listUploads).mockResolvedValue({ items: [] });
    render(<StudioPage />);
    expect(await screen.findByRole("link", { name: "Upload your first video" })).toHaveAttribute("href", "/upload");
  });

  it("shows an error when the API fails", async () => {
    vi.mocked(api.myChannels).mockRejectedValue(new Error("offline"));
    render(<StudioPage />);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("asks anonymous visitors to sign in", () => {
    vi.mocked(useAuth).mockReturnValue(authValue(false));
    render(<StudioPage />);
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  });
});
