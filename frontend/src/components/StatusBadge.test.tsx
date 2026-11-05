import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { VideoStatus } from "@/lib/types";
import { StatusBadge, isInProgress, statusHint } from "./StatusBadge";

describe("StatusBadge", () => {
  it.each<[VideoStatus, string]>([
    ["draft", "Draft"],
    ["uploading", "Uploading"],
    ["queued", "Queued"],
    ["processing", "Processing"],
    ["ready", "Ready"],
    ["failed", "Failed"],
  ])("labels %s as %s", (status, label) => {
    render(<StatusBadge status={status} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it("shows the percentage only while processing", () => {
    const { rerender } = render(<StatusBadge status="processing" progress={42} />);
    expect(screen.getByText("Processing 42%")).toBeInTheDocument();

    rerender(<StatusBadge status="queued" progress={0} />);
    expect(screen.getByText("Queued")).toBeInTheDocument();

    rerender(<StatusBadge status="processing" progress={null} />);
    expect(screen.getByText("Processing")).toBeInTheDocument();
  });

  it("explains each stage of the pipeline", () => {
    expect(statusHint("queued")).toMatch(/waiting for a free processing worker/i);
    expect(statusHint("processing")).toMatch(/streaming formats/i);
    expect(statusHint("ready")).toMatch(/ready to watch/i);
    expect(statusHint("failed")).toMatch(/upload it again/i);
  });

  it("knows which statuses are still moving", () => {
    expect(["uploading", "queued", "processing"].every((s) => isInProgress(s as VideoStatus))).toBe(true);
    expect(["draft", "ready", "failed"].some((s) => isInProgress(s as VideoStatus))).toBe(false);
  });
});
