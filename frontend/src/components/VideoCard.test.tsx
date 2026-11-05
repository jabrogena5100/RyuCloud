import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { VideoCard } from "./VideoCard";

describe("VideoCard", () => {
  it("renders title, channel, views and duration", () => {
    render(
      <VideoCard
        video={{
          id: "v1",
          title: "Intro to S3",
          channelName: "Cloud Native Notes",
          views: 1200,
          publishedAt: null,
          durationSeconds: 245,
          thumbnailUrl: null,
        }}
      />,
    );
    expect(screen.getByRole("heading", { name: "Intro to S3" })).toBeInTheDocument();
    expect(screen.getByText("Cloud Native Notes")).toBeInTheDocument();
    expect(screen.getByText("1.2K views")).toBeInTheDocument();
    expect(screen.getByText("4:05")).toBeInTheDocument();
  });
});
