import { describeError } from "./api";
import { formatBytes } from "./format";
import type { UploadLimits, UploadTarget } from "./types";

/** Media types by extension, used when the browser doesn't report `file.type` (common for .mkv). */
const TYPE_BY_EXTENSION: Record<string, string> = {
  ".mp4": "video/mp4",
  ".m4v": "video/mp4",
  ".mov": "video/quicktime",
  ".webm": "video/webm",
  ".mkv": "video/x-matroska",
};

function extensionOf(filename: string): string {
  const dot = filename.lastIndexOf(".");
  return dot === -1 ? "" : filename.slice(dot).toLowerCase();
}

/** The media type to declare for a file: the browser's value, else a guess from the extension. */
export function contentTypeFor(file: Pick<File, "name" | "type">): string {
  const reported = file.type.split(";")[0].trim().toLowerCase();
  return reported || TYPE_BY_EXTENSION[extensionOf(file.name)] || "";
}

/**
 * Client-side pre-check so users get instant feedback. The API enforces the same
 * rules authoritatively; this only saves a round trip.
 */
export function validateFile(file: Pick<File, "name" | "type" | "size">, limits: UploadLimits): string | null {
  if (file.size <= 0) {
    return "This file is empty.";
  }
  const type = contentTypeFor(file);
  if (!limits.allowed_content_types.includes(type)) {
    return `Unsupported file type. Allowed extensions: ${limits.allowed_extensions.join(", ")}.`;
  }
  if (!limits.allowed_extensions.includes(extensionOf(file.name))) {
    return `Unsupported file extension. Allowed extensions: ${limits.allowed_extensions.join(", ")}.`;
  }
  if (file.size > limits.max_bytes) {
    return `This file is ${formatBytes(file.size)}. The maximum size is ${formatBytes(limits.max_bytes)}.`;
  }
  return null;
}

/** Strips the extension so the filename can pre-fill the title field. */
export function titleFromFilename(filename: string): string {
  const dot = filename.lastIndexOf(".");
  const base = dot > 0 ? filename.slice(0, dot) : filename;
  return base.replace(/[_-]+/g, " ").trim().slice(0, 100);
}

export class UploadAbortedError extends Error {
  constructor() {
    super("Upload cancelled.");
    this.name = "UploadAbortedError";
  }
}

export class StorageUploadError extends Error {
  constructor(
    message: string,
    readonly status: number | null,
  ) {
    super(message);
    this.name = "StorageUploadError";
  }
}

/**
 * Sends the file straight to object storage with the presigned request. This uses
 * XMLHttpRequest because `fetch` cannot report upload progress. The request is
 * deliberately NOT sent to the Go API and carries no cookies: the presigned
 * signature is the only credential.
 */
export function putToStorage(
  target: UploadTarget,
  file: Blob,
  options: { onProgress?: (loaded: number, total: number) => void; signal?: AbortSignal } = {},
): Promise<void> {
  const { onProgress, signal } = options;
  return new Promise<void>((resolve, reject) => {
    if (signal?.aborted) {
      reject(new UploadAbortedError());
      return;
    }

    const xhr = new XMLHttpRequest();
    xhr.open(target.method, target.url);
    xhr.withCredentials = false;
    for (const [name, value] of Object.entries(target.headers)) {
      xhr.setRequestHeader(name, value);
    }

    const onAbort = () => xhr.abort();
    signal?.addEventListener("abort", onAbort, { once: true });
    const cleanup = () => signal?.removeEventListener("abort", onAbort);

    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) {
        onProgress?.(event.loaded, event.total);
      }
    };
    xhr.onload = () => {
      cleanup();
      if (xhr.status >= 200 && xhr.status < 300) {
        onProgress?.(file.size, file.size);
        resolve();
      } else if (xhr.status === 403) {
        reject(new StorageUploadError("The upload link was rejected or has expired. Please start again.", 403));
      } else {
        reject(new StorageUploadError(`Storage rejected the upload (HTTP ${xhr.status}).`, xhr.status));
      }
    };
    xhr.onerror = () => {
      cleanup();
      reject(new StorageUploadError("Network error while uploading. Check your connection and try again.", null));
    };
    xhr.onabort = () => {
      cleanup();
      reject(new UploadAbortedError());
    };
    xhr.send(file);
  });
}

/** Message for anything that can go wrong during an upload, including storage-side failures. */
export function describeUploadError(error: unknown): string {
  if (error instanceof StorageUploadError || error instanceof UploadAbortedError) {
    return error.message;
  }
  return describeError(error);
}
