import { vi } from "vitest";
import type { Channel, Video } from "@/lib/types";

export const USER = {
  id: "user-1",
  email: "alice@example.com",
  display_name: "Alice",
  created_at: "2026-11-01T00:00:00Z",
};

/** A complete value for the mocked useAuth() hook. */
export function authValue(signedIn: boolean) {
  return {
    user: signedIn ? USER : null,
    status: signedIn ? ("authenticated" as const) : ("anonymous" as const),
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
  };
}

export const CHANNEL: Channel = {
  id: "chan-1",
  owner_id: "user-1",
  handle: "alice-tv",
  name: "Alice TV",
  description: "",
  created_at: "2026-11-01T00:00:00Z",
};

export function makeVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "vid-1",
    channel: { id: CHANNEL.id, handle: CHANNEL.handle, name: CHANNEL.name },
    title: "My video",
    description: "",
    status: "processing",
    visibility: "private",
    duration_seconds: null,
    view_count: 0,
    created_at: "2026-11-05T10:00:00Z",
    published_at: null,
    thumbnail_url: null,
    playback_url: null,
    ...overrides,
  };
}
