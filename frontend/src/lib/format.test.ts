import { describe, expect, it } from "vitest";
import { formatBytes, formatDuration, formatRelativeTime, formatViews } from "./format";

describe("formatDuration", () => {
  it.each([
    [0, "0:00"],
    [65, "1:05"],
    [3600, "1:00:00"],
    [3725, "1:02:05"],
  ])("%i seconds -> %s", (seconds, expected) => {
    expect(formatDuration(seconds)).toBe(expected);
  });
});

describe("formatViews", () => {
  it.each([
    [1, "1 view"],
    [999, "999 views"],
    [1200, "1.2K views"],
    [1_000_000, "1M views"],
  ])("%i -> %s", (count, expected) => {
    expect(formatViews(count)).toBe(expected);
  });
});

describe("formatRelativeTime", () => {
  const now = new Date("2026-11-04T12:00:00Z");

  it.each([
    ["2026-11-04T11:59:30Z", "just now"],
    ["2026-11-04T11:00:00Z", "1 hour ago"],
    ["2026-11-01T12:00:00Z", "3 days ago"],
    ["2025-11-01T12:00:00Z", "1 year ago"],
  ])("%s -> %s", (iso, expected) => {
    expect(formatRelativeTime(iso, now)).toBe(expected);
  });
});

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [512, "512 B"],
    [1536, "1.5 KB"],
    [5 * 1024 * 1024, "5.0 MB"],
    [1024 ** 3, "1.0 GB"],
    [250 * 1024 ** 2, "250 MB"],
    [-1, "0 B"],
    [Number.NaN, "0 B"],
  ])("%d bytes -> %s", (bytes, expected) => {
    expect(formatBytes(bytes)).toBe(expected);
  });
});
