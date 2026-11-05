import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  contentTypeFor,
  describeUploadError,
  putToStorage,
  StorageUploadError,
  titleFromFilename,
  UploadAbortedError,
  validateFile,
} from "./upload";
import { ApiError } from "./api";
import type { UploadLimits, UploadTarget } from "./types";

const LIMITS: UploadLimits = {
  max_bytes: 10 * 1024 * 1024,
  allowed_content_types: ["video/mp4", "video/quicktime", "video/webm", "video/x-matroska"],
  allowed_extensions: [".m4v", ".mkv", ".mov", ".mp4", ".webm"],
  url_ttl_seconds: 900,
};

const file = (name: string, type: string, size: number) => ({ name, type, size });

describe("contentTypeFor", () => {
  it("prefers the type reported by the browser, without parameters", () => {
    expect(contentTypeFor({ name: "a.mp4", type: "Video/MP4; codecs=avc1" })).toBe("video/mp4");
  });

  it("falls back to the extension when the browser reports nothing", () => {
    expect(contentTypeFor({ name: "movie.MKV", type: "" })).toBe("video/x-matroska");
    expect(contentTypeFor({ name: "clip.mov", type: "" })).toBe("video/quicktime");
  });

  it("returns an empty string for unknown files", () => {
    expect(contentTypeFor({ name: "notes.txt", type: "" })).toBe("");
  });
});

describe("validateFile", () => {
  it("accepts a supported file within the size limit", () => {
    expect(validateFile(file("clip.mp4", "video/mp4", 1024), LIMITS)).toBeNull();
    expect(validateFile(file("movie.mkv", "", 1024), LIMITS)).toBeNull();
  });

  it("rejects empty files", () => {
    expect(validateFile(file("clip.mp4", "video/mp4", 0), LIMITS)).toMatch(/empty/i);
  });

  it("rejects unsupported types", () => {
    expect(validateFile(file("doc.pdf", "application/pdf", 1024), LIMITS)).toMatch(/unsupported file type/i);
    expect(validateFile(file("page.html", "text/html", 1024), LIMITS)).toMatch(/unsupported file type/i);
  });

  it("rejects a supported type with a mismatched extension", () => {
    expect(validateFile(file("malware.exe", "video/mp4", 1024), LIMITS)).toMatch(/extension/i);
  });

  it("rejects oversized files and says how large the limit is", () => {
    const message = validateFile(file("big.mp4", "video/mp4", LIMITS.max_bytes + 1), LIMITS);
    expect(message).toMatch(/maximum size is 10 MB/);
  });
});

describe("titleFromFilename", () => {
  it("drops the extension and tidies separators", () => {
    expect(titleFromFilename("my_holiday-clip.final.mp4")).toBe("my holiday clip.final");
    expect(titleFromFilename("plain")).toBe("plain");
  });
});

describe("describeUploadError", () => {
  it("passes storage and abort messages through", () => {
    expect(describeUploadError(new StorageUploadError("Network error", null))).toBe("Network error");
    expect(describeUploadError(new UploadAbortedError())).toBe("Upload cancelled.");
  });

  it("describes API errors and falls back for unknown errors", () => {
    expect(describeUploadError(new ApiError(413, "file_too_large", "Too big."))).toBe("Too big.");
    expect(describeUploadError(new Error("boom"))).toBe("Something went wrong. Please try again.");
  });
});

class FakeXHR {
  static instances: FakeXHR[] = [];
  method = "";
  url = "";
  headers: Record<string, string> = {};
  withCredentials = true;
  status = 0;
  sentBody: unknown = undefined;
  upload: { onprogress: ((event: { lengthComputable: boolean; loaded: number; total: number }) => void) | null } = {
    onprogress: null,
  };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;

  constructor() {
    FakeXHR.instances.push(this);
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(name: string, value: string) {
    this.headers[name] = value;
  }
  send(body: unknown) {
    this.sentBody = body;
  }
  abort() {
    this.onabort?.();
  }
  finish(status: number) {
    this.status = status;
    this.onload?.();
  }
}

const TARGET: UploadTarget = {
  method: "PUT",
  url: "http://localhost:9000/ryucloud-media/uploads/v/u/source?X-Amz-Signature=abc",
  headers: { "Content-Type": "video/mp4" },
  expires_at: "2026-11-05T12:00:00Z",
};
const BLOB = new Blob(["0123456789"], { type: "video/mp4" });

describe("putToStorage", () => {
  beforeEach(() => {
    FakeXHR.instances = [];
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("sends the file straight to the presigned URL with the signed headers and no cookies", async () => {
    const onProgress = vi.fn();
    const promise = putToStorage(TARGET, BLOB, { onProgress });
    const xhr = FakeXHR.instances[0];

    expect(xhr.method).toBe("PUT");
    expect(xhr.url).toBe(TARGET.url);
    expect(xhr.headers).toEqual({ "Content-Type": "video/mp4" });
    expect(xhr.withCredentials).toBe(false);
    expect(xhr.sentBody).toBe(BLOB);

    xhr.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 });
    expect(onProgress).toHaveBeenLastCalledWith(5, 10);
    xhr.finish(200);
    await expect(promise).resolves.toBeUndefined();
    expect(onProgress).toHaveBeenLastCalledWith(10, 10);
  });

  it("explains a rejected or expired link (403)", async () => {
    const promise = putToStorage(TARGET, BLOB);
    FakeXHR.instances[0].finish(403);
    await expect(promise).rejects.toMatchObject({ name: "StorageUploadError", status: 403, message: expect.stringMatching(/expired/) });
  });

  it("reports other storage errors with their status", async () => {
    const promise = putToStorage(TARGET, BLOB);
    FakeXHR.instances[0].finish(500);
    await expect(promise).rejects.toMatchObject({ status: 500, message: expect.stringContaining("500") });
  });

  it("reports network failures", async () => {
    const promise = putToStorage(TARGET, BLOB);
    FakeXHR.instances[0].onerror?.();
    await expect(promise).rejects.toMatchObject({ status: null, message: expect.stringMatching(/network/i) });
  });

  it("aborts when the signal fires", async () => {
    const controller = new AbortController();
    const promise = putToStorage(TARGET, BLOB, { signal: controller.signal });
    controller.abort();
    await expect(promise).rejects.toBeInstanceOf(UploadAbortedError);
  });

  it("does not send anything if already aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    await expect(putToStorage(TARGET, BLOB, { signal: controller.signal })).rejects.toBeInstanceOf(UploadAbortedError);
    expect(FakeXHR.instances).toHaveLength(0);
  });
});
