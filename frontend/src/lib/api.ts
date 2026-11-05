import type {
  ApiErrorDetail,
  Channel,
  InitiatedUpload,
  Upload,
  UploadLimits,
  UploadStatus,
  User,
  Video,
  VideoPage,
  VideoVisibility,
} from "./types";

const API_BASE = "/api/v1";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly details: ApiErrorDetail[] = [],
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/**
 * Fetch wrapper for the same-origin API proxy. Authentication rides on the
 * HttpOnly session cookie, which JavaScript never reads or stores.
 */
export async function apiFetch<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body !== undefined && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const response = await fetch(`${API_BASE}${path}`, { ...init, headers, credentials: "same-origin" });
  if (response.status === 204) {
    return undefined as T;
  }

  const body: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const error = (body as { error?: { code?: string; message?: string; details?: ApiErrorDetail[] } } | null)
      ?.error;
    throw new ApiError(
      response.status,
      error?.code ?? "http_error",
      error?.message ?? `Request failed with status ${response.status}`,
      error?.details ?? [],
    );
  }
  return body as T;
}

/** Human-readable message for any error thrown by an API call. */
export function describeError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.code === "validation_error" && error.details.length > 0) {
      return error.details
        .map((detail) => {
          const field = String(detail.loc[detail.loc.length - 1] ?? "").replace(/_/g, " ");
          return field ? `${field}: ${detail.msg}` : detail.msg;
        })
        .join("; ");
    }
    return error.message;
  }
  return "Something went wrong. Please try again.";
}

export const api = {
  me: () => apiFetch<User>("/auth/me"),
  login: (input: { email: string; password: string }) =>
    apiFetch<User>("/auth/login", { method: "POST", body: JSON.stringify(input) }),
  register: (input: { email: string; password: string; display_name: string }) =>
    apiFetch<User>("/auth/register", { method: "POST", body: JSON.stringify(input) }),
  logout: () => apiFetch<void>("/auth/logout", { method: "POST" }),
  listVideos: ({ limit = 24, offset = 0 } = {}) =>
    apiFetch<VideoPage>(`/videos?limit=${limit}&offset=${offset}`),

  myChannels: () => apiFetch<Channel[]>("/channels/mine"),
  createChannel: (input: { handle: string; name: string }) =>
    apiFetch<Channel>("/channels", { method: "POST", body: JSON.stringify(input) }),
  channelVideos: (handle: string, { limit = 50, offset = 0 } = {}) =>
    apiFetch<VideoPage>(`/channels/${encodeURIComponent(handle)}/videos?limit=${limit}&offset=${offset}`),
  getVideo: (id: string) => apiFetch<Video>(`/videos/${encodeURIComponent(id)}`),
  deleteVideo: (id: string) => apiFetch<void>(`/videos/${id}`, { method: "DELETE" }),

  uploadLimits: () => apiFetch<UploadLimits>("/uploads/limits"),
  initiateUpload: (input: {
    channel_id: string;
    title: string;
    description: string;
    visibility: VideoVisibility;
    filename: string;
    content_type: string;
    size_bytes: number;
  }) => apiFetch<InitiatedUpload>("/uploads", { method: "POST", body: JSON.stringify(input) }),
  completeUpload: (id: string) => apiFetch<Upload>(`/uploads/${id}/complete`, { method: "POST" }),
  getUpload: (id: string) => apiFetch<Upload>(`/uploads/${id}`),
  cancelUpload: (id: string) => apiFetch<void>(`/uploads/${id}`, { method: "DELETE" }),
  listUploads: ({ status, limit = 100 }: { status?: UploadStatus; limit?: number } = {}) =>
    apiFetch<{ items: Upload[] }>(`/uploads?limit=${limit}${status ? `&status=${status}` : ""}`),
};
