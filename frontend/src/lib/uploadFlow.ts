import { api, ApiError } from "./api";
import { putToStorage, UploadAbortedError } from "./upload";
import type { InitiatedUpload, Upload, VideoVisibility } from "./types";

export type UploadStage = "starting" | "uploading" | "finalizing";

export type UploadInput = {
  channelId: string;
  title: string;
  description: string;
  visibility: VideoVisibility;
  file: File;
  contentType: string;
};

export type UploadCallbacks = {
  onStage?: (stage: UploadStage) => void;
  onProgress?: (loaded: number, total: number) => void;
  signal?: AbortSignal;
  /** Pause between attempts to confirm a finished upload. Tests set 0. */
  retryDelayMs?: number;
};

const COMPLETE_ATTEMPTS = 3;

/** Network errors and server-side (5xx) failures are worth retrying; client errors are not. */
function isTransient(error: unknown): boolean {
  return !(error instanceof ApiError) || error.status >= 500;
}

async function completeWithRetry(uploadId: string, delayMs: number): Promise<Upload> {
  for (let attempt = 1; ; attempt += 1) {
    try {
      return await api.completeUpload(uploadId);
    } catch (error) {
      if (attempt >= COMPLETE_ATTEMPTS || !isTransient(error)) {
        throw error;
      }
      await new Promise((resolve) => setTimeout(resolve, delayMs * attempt));
    }
  }
}

/**
 * The full browser-side upload lifecycle:
 *   1. ask the API for an upload session (validates metadata, returns a presigned URL)
 *   2. PUT the file directly to object storage (never through the API)
 *   3. tell the API it is done; the API verifies the stored object
 * If step 2 fails or is aborted, the session is cancelled best-effort so no dangling
 * "uploading" video is left behind. If step 3 keeps failing the file may still be fully
 * stored, so the session is left pending (visible, with a cancel button, in the dashboard)
 * rather than discarded. Errors are rethrown as-is.
 */
export async function runUpload(
  input: UploadInput,
  { onStage, onProgress, signal, retryDelayMs = 1000 }: UploadCallbacks = {},
): Promise<{ upload: Upload; session: InitiatedUpload }> {
  onStage?.("starting");
  const session = await api.initiateUpload({
    channel_id: input.channelId,
    title: input.title,
    description: input.description,
    visibility: input.visibility,
    filename: input.file.name,
    content_type: input.contentType,
    size_bytes: input.file.size,
  });

  onStage?.("uploading");
  try {
    await putToStorage(session.target, input.file, { onProgress, signal });
  } catch (error) {
    await api.cancelUpload(session.upload.id).catch(() => undefined);
    throw error;
  }

  if (signal?.aborted) {
    await api.cancelUpload(session.upload.id).catch(() => undefined);
    throw new UploadAbortedError();
  }
  onStage?.("finalizing");
  const upload = await completeWithRetry(session.upload.id, retryDelayMs);
  return { upload, session };
}
