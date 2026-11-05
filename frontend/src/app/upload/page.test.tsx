import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "@/lib/api";
import { useAuth } from "@/lib/auth-context";
import type { UploadLimits } from "@/lib/types";
import { UploadAbortedError } from "@/lib/upload";
import { runUpload } from "@/lib/uploadFlow";
import { authValue, CHANNEL } from "@/test/fixtures";
import UploadPage from "./page";

vi.mock("@/lib/auth-context", () => ({ useAuth: vi.fn() }));
vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>();
  return { ...actual, api: { uploadLimits: vi.fn(), myChannels: vi.fn(), createChannel: vi.fn() } };
});
vi.mock("@/lib/uploadFlow", () => ({ runUpload: vi.fn() }));

const LIMITS: UploadLimits = {
  max_bytes: 10 * 1024 * 1024,
  allowed_content_types: ["video/mp4", "video/quicktime", "video/webm", "video/x-matroska"],
  allowed_extensions: [".m4v", ".mkv", ".mov", ".mp4", ".webm"],
  url_ttl_seconds: 900,
};
const RESULT = { upload: {} as never, session: {} as never };
const clip = () => new File(["abc"], "my_first-clip.mp4", { type: "video/mp4" });

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(useAuth).mockReturnValue(authValue(true));
  vi.mocked(api.uploadLimits).mockResolvedValue(LIMITS);
  vi.mocked(api.myChannels).mockResolvedValue([CHANNEL]);
});

describe("UploadPage", () => {
  it("asks anonymous visitors to sign in", () => {
    vi.mocked(useAuth).mockReturnValue(authValue(false));
    render(<UploadPage />);
    expect(screen.getByRole("heading", { name: "Sign in to upload" })).toBeInTheDocument();
    expect(api.uploadLimits).not.toHaveBeenCalled();
  });

  it("offers to create a channel first when the user has none", async () => {
    vi.mocked(api.myChannels).mockResolvedValue([]);
    vi.mocked(api.createChannel).mockResolvedValue(CHANNEL);
    const user = userEvent.setup();
    render(<UploadPage />);

    await user.type(await screen.findByLabelText("Channel name"), "Alice TV");
    await user.type(screen.getByLabelText("Handle"), "Alice-TV");
    await user.click(screen.getByRole("button", { name: "Create channel" }));

    expect(api.createChannel).toHaveBeenCalledWith({ handle: "alice-tv", name: "Alice TV" });
    expect(await screen.findByLabelText("Select video file")).toBeInTheDocument();
  });

  it("uploads the chosen file with the entered metadata and explains the processing-pending state", async () => {
    vi.mocked(runUpload).mockResolvedValue(RESULT);
    const user = userEvent.setup();
    render(<UploadPage />);

    const file = clip();
    await user.upload(await screen.findByLabelText("Select video file"), file);
    expect(screen.getByLabelText("Title")).toHaveValue("my first clip"); // pre-filled from the filename

    await user.clear(screen.getByLabelText("Title"));
    await user.type(screen.getByLabelText("Title"), "Hello RyuCloud");
    await user.type(screen.getByLabelText("Description"), "First upload");
    await user.selectOptions(screen.getByLabelText("Visibility"), "unlisted");
    await user.click(screen.getByRole("button", { name: "Upload" }));

    expect(runUpload).toHaveBeenCalledWith(
      {
        channelId: "chan-1",
        title: "Hello RyuCloud",
        description: "First upload",
        visibility: "unlisted",
        file,
        contentType: "video/mp4",
      },
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(await screen.findByRole("heading", { name: "Upload complete" })).toBeInTheDocument();
    expect(screen.getByText(/processing pending/i)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "View your videos" })).toHaveAttribute("href", "/studio");
  });

  it("shows upload progress while the file is being sent", async () => {
    let finish!: () => void;
    vi.mocked(runUpload).mockImplementation(async (_input, callbacks) => {
      callbacks?.onStage?.("uploading");
      callbacks?.onProgress?.(50, 100);
      await new Promise<void>((resolve) => {
        finish = resolve;
      });
      return RESULT;
    });
    const user = userEvent.setup();
    render(<UploadPage />);

    await user.upload(await screen.findByLabelText("Select video file"), clip());
    await user.click(screen.getByRole("button", { name: "Upload" }));

    const bar = await screen.findByRole("progressbar", { name: "Upload progress" });
    expect(bar).toHaveAttribute("aria-valuenow", "50");
    expect(screen.getByRole("button", { name: "Uploading…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeInTheDocument();

    finish();
    expect(await screen.findByRole("heading", { name: "Upload complete" })).toBeInTheDocument();
  });

  it("lets the user cancel an upload in progress", async () => {
    vi.mocked(runUpload).mockImplementation(async (_input, callbacks) => {
      callbacks?.onStage?.("uploading");
      return new Promise<never>((_resolve, reject) => {
        callbacks?.signal?.addEventListener("abort", () => reject(new UploadAbortedError()));
      });
    });
    const user = userEvent.setup();
    render(<UploadPage />);

    await user.upload(await screen.findByLabelText("Select video file"), clip());
    await user.click(screen.getByRole("button", { name: "Upload" }));
    await user.click(await screen.findByRole("button", { name: "Cancel" }));

    expect(await screen.findByText(/upload cancelled/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload" })).toBeEnabled();
  });

  it("rejects unsupported files before contacting the API", async () => {
    const user = userEvent.setup({ applyAccept: false });
    render(<UploadPage />);

    await user.upload(await screen.findByLabelText("Select video file"), new File(["x"], "notes.txt", { type: "text/plain" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/unsupported file type/i);
    expect(screen.getByRole("button", { name: "Upload" })).toBeDisabled();
    expect(runUpload).not.toHaveBeenCalled();
  });

  it("rejects files over the size limit", async () => {
    const user = userEvent.setup();
    render(<UploadPage />);

    const big = new File(["x"], "big.mp4", { type: "video/mp4" });
    Object.defineProperty(big, "size", { value: LIMITS.max_bytes + 1 });
    await user.upload(await screen.findByLabelText("Select video file"), big);

    expect(await screen.findByRole("alert")).toHaveTextContent(/maximum size is 10 MB/i);
    expect(runUpload).not.toHaveBeenCalled();
  });

  it("shows the API's message when the upload fails, and allows trying again", async () => {
    vi.mocked(runUpload).mockRejectedValueOnce(new ApiError(502, "storage_unavailable", "Object storage is unavailable. Please try again."));
    vi.mocked(runUpload).mockResolvedValueOnce(RESULT);
    const user = userEvent.setup();
    render(<UploadPage />);

    await user.upload(await screen.findByLabelText("Select video file"), clip());
    await user.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Object storage is unavailable");

    await user.click(screen.getByRole("button", { name: "Upload" }));
    await waitFor(() => expect(runUpload).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("heading", { name: "Upload complete" })).toBeInTheDocument();
  });

  it("shows a channel picker when the user has several channels", async () => {
    vi.mocked(api.myChannels).mockResolvedValue([CHANNEL, { ...CHANNEL, id: "chan-2", handle: "second", name: "Second" }]);
    const user = userEvent.setup();
    render(<UploadPage />);

    const picker = await screen.findByLabelText("Channel");
    expect(within(picker).getAllByRole("option")).toHaveLength(2);
    vi.mocked(runUpload).mockResolvedValue(RESULT);
    await user.selectOptions(picker, "chan-2");
    await user.upload(screen.getByLabelText("Select video file"), clip());
    await user.click(screen.getByRole("button", { name: "Upload" }));
    expect(runUpload).toHaveBeenCalledWith(expect.objectContaining({ channelId: "chan-2" }), expect.anything());
  });
});
