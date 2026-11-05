import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "./api";
import { putToStorage, StorageUploadError, UploadAbortedError } from "./upload";
import { runUpload } from "./uploadFlow";
import type { InitiatedUpload, Upload } from "./types";

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return {
    ...actual,
    api: { initiateUpload: vi.fn(), completeUpload: vi.fn(), cancelUpload: vi.fn() },
  };
});
vi.mock("./upload", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./upload")>();
  return { ...actual, putToStorage: vi.fn() };
});

const SESSION: InitiatedUpload = {
  upload: {
    id: "up-1",
    video_id: "vid-1",
    status: "pending",
    original_filename: "clip.mp4",
    content_type: "video/mp4",
    size_bytes: 3,
    failure_reason: null,
    expires_at: "2026-11-05T12:00:00Z",
    completed_at: null,
    created_at: "2026-11-05T11:30:00Z",
  },
  video: { id: "vid-1", title: "Clip", status: "uploading", visibility: "private" },
  target: { method: "PUT", url: "http://storage.test/x", headers: { "Content-Type": "video/mp4" }, expires_at: "2026-11-05T12:00:00Z" },
};
const DONE: Upload = { ...SESSION.upload, status: "uploaded", completed_at: "2026-11-05T11:31:00Z" };

const INPUT = {
  channelId: "chan-1",
  title: "Clip",
  description: "desc",
  visibility: "private" as const,
  file: new File(["abc"], "clip.mp4", { type: "video/mp4" }),
  contentType: "video/mp4",
};

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.cancelUpload).mockResolvedValue(undefined);
});

describe("runUpload", () => {
  it("initiates, uploads directly to storage, then completes, in that order", async () => {
    const calls: string[] = [];
    vi.mocked(api.initiateUpload).mockImplementation(async () => (calls.push("initiate"), SESSION));
    vi.mocked(putToStorage).mockImplementation(async () => void calls.push("put"));
    vi.mocked(api.completeUpload).mockImplementation(async () => (calls.push("complete"), DONE));
    const stages: string[] = [];

    const result = await runUpload(INPUT, { onStage: (s) => stages.push(s), retryDelayMs: 0 });

    expect(calls).toEqual(["initiate", "put", "complete"]);
    expect(stages).toEqual(["starting", "uploading", "finalizing"]);
    expect(result.upload.status).toBe("uploaded");
    expect(api.initiateUpload).toHaveBeenCalledWith({
      channel_id: "chan-1",
      title: "Clip",
      description: "desc",
      visibility: "private",
      filename: "clip.mp4",
      content_type: "video/mp4",
      size_bytes: 3,
    });
    expect(putToStorage).toHaveBeenCalledWith(SESSION.target, INPUT.file, expect.any(Object));
    expect(api.cancelUpload).not.toHaveBeenCalled();
  });

  it("does not upload anything when the API refuses the session", async () => {
    vi.mocked(api.initiateUpload).mockRejectedValue(new ApiError(413, "file_too_large", "Too big."));
    await expect(runUpload(INPUT, { retryDelayMs: 0 })).rejects.toMatchObject({ code: "file_too_large" });
    expect(putToStorage).not.toHaveBeenCalled();
    expect(api.cancelUpload).not.toHaveBeenCalled();
  });

  it("cancels the session when the transfer to storage fails", async () => {
    vi.mocked(api.initiateUpload).mockResolvedValue(SESSION);
    vi.mocked(putToStorage).mockRejectedValue(new StorageUploadError("Network error", null));

    await expect(runUpload(INPUT, { retryDelayMs: 0 })).rejects.toBeInstanceOf(StorageUploadError);
    expect(api.cancelUpload).toHaveBeenCalledWith("up-1");
    expect(api.completeUpload).not.toHaveBeenCalled();
  });

  it("cancels the session when the user aborts", async () => {
    vi.mocked(api.initiateUpload).mockResolvedValue(SESSION);
    vi.mocked(putToStorage).mockRejectedValue(new UploadAbortedError());

    await expect(runUpload(INPUT, { retryDelayMs: 0 })).rejects.toBeInstanceOf(UploadAbortedError);
    expect(api.cancelUpload).toHaveBeenCalledWith("up-1");
  });

  it("still reports the original error if cancelling also fails", async () => {
    vi.mocked(api.initiateUpload).mockResolvedValue(SESSION);
    vi.mocked(putToStorage).mockRejectedValue(new StorageUploadError("Network error", null));
    vi.mocked(api.cancelUpload).mockRejectedValue(new Error("offline"));
    await expect(runUpload(INPUT, { retryDelayMs: 0 })).rejects.toBeInstanceOf(StorageUploadError);
  });

  it("retries confirming the upload after a transient failure", async () => {
    vi.mocked(api.initiateUpload).mockResolvedValue(SESSION);
    vi.mocked(putToStorage).mockResolvedValue(undefined);
    vi.mocked(api.completeUpload)
      .mockRejectedValueOnce(new ApiError(502, "storage_unavailable", "Storage down."))
      .mockRejectedValueOnce(new TypeError("Failed to fetch"))
      .mockResolvedValueOnce(DONE);

    await expect(runUpload(INPUT, { retryDelayMs: 0 })).resolves.toMatchObject({ upload: { status: "uploaded" } });
    expect(api.completeUpload).toHaveBeenCalledTimes(3);
    expect(api.cancelUpload).not.toHaveBeenCalled();
  });

  it("gives up after three attempts but leaves the session for the dashboard", async () => {
    vi.mocked(api.initiateUpload).mockResolvedValue(SESSION);
    vi.mocked(putToStorage).mockResolvedValue(undefined);
    vi.mocked(api.completeUpload).mockRejectedValue(new ApiError(502, "storage_unavailable", "Storage down."));

    await expect(runUpload(INPUT, { retryDelayMs: 0 })).rejects.toMatchObject({ status: 502 });
    expect(api.completeUpload).toHaveBeenCalledTimes(3);
    expect(api.cancelUpload).not.toHaveBeenCalled();
  });

  it("does not retry client errors", async () => {
    vi.mocked(api.initiateUpload).mockResolvedValue(SESSION);
    vi.mocked(putToStorage).mockResolvedValue(undefined);
    vi.mocked(api.completeUpload).mockRejectedValue(new ApiError(422, "upload_rejected", "Mismatch."));

    await expect(runUpload(INPUT, { retryDelayMs: 0 })).rejects.toMatchObject({ code: "upload_rejected" });
    expect(api.completeUpload).toHaveBeenCalledTimes(1);
  });
});
