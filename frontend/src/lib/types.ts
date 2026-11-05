// Mirrors the backend response schemas (see docs/api.md).

export type User = {
  id: string;
  email: string;
  display_name: string;
  created_at: string;
};

export type ChannelSummary = {
  id: string;
  handle: string;
  name: string;
};

export type Channel = {
  id: string;
  owner_id: string;
  handle: string;
  name: string;
  description: string;
  created_at: string;
};

export type VideoStatus = "draft" | "uploading" | "processing" | "ready" | "failed";
export type VideoVisibility = "public" | "unlisted" | "private";

export type Video = {
  id: string;
  channel: ChannelSummary;
  title: string;
  description: string;
  status: VideoStatus;
  visibility: VideoVisibility;
  duration_seconds: number | null;
  view_count: number;
  created_at: string;
  published_at: string | null;
  thumbnail_url: string | null;
  playback_url: string | null;
};

export type VideoPage = {
  items: Video[];
  limit: number;
  offset: number;
};

export type UploadStatus = "pending" | "uploaded" | "failed" | "cancelled" | "expired";

export type Upload = {
  id: string;
  video_id: string;
  status: UploadStatus;
  original_filename: string;
  content_type: string;
  size_bytes: number;
  failure_reason: string | null;
  expires_at: string;
  completed_at: string | null;
  created_at: string;
};

/** Where and how the browser sends the file: straight to object storage. */
export type UploadTarget = {
  method: string;
  url: string;
  headers: Record<string, string>;
  expires_at: string;
};

export type InitiatedUpload = {
  upload: Upload;
  video: { id: string; title: string; status: VideoStatus; visibility: VideoVisibility };
  target: UploadTarget;
};

export type UploadLimits = {
  max_bytes: number;
  allowed_content_types: string[];
  allowed_extensions: string[];
  url_ttl_seconds: number;
};

export type ApiErrorDetail = {
  loc: (string | number)[];
  msg: string;
  type: string;
};

/** Display model for a video card, shared by real API videos and placeholders. */
export type VideoCardData = {
  id: string;
  title: string;
  channelName: string;
  views: number;
  publishedAt: string | null;
  durationSeconds: number | null;
  thumbnailUrl: string | null;
};
