import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { VideoStatus } from "@/lib/types";
import { StatusBadge, statusHint } from "./StatusBadge";

describe("StatusBadge", () => {
  it.each<[VideoStatus, string]>([
    ["draft", "Draft"],
    ["uploading", "Uploading"],
    ["processing", "Processing pending"],
    ["ready", "Ready"],
    ["failed", "Failed"],
  ])("labels %s as %s", (status, label) => {
    render(<StatusBadge status={status} />);
    expect(screen.getByText(label)).toBeInTheDocument();
  });

  it("explains that a verified upload cannot be played yet", () => {
    expect(statusHint("processing")).toMatch(/transcoding isn't available yet/i);
    render(<StatusBadge status="processing" />);
    expect(screen.getByText("Processing pending")).toHaveAttribute("title", expect.stringMatching(/can't be played/));
  });
});
