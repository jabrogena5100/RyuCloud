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

describe("VideoCard links", () => {
  const base = {
    id: "v1",
    title: "Watchable",
    channelName: "Chan",
    views: 0,
    publishedAt: null,
    durationSeconds: null,
    thumbnailUrl: "/api/v1/videos/v1/thumbnail",
  };

  it("links to the watch page when the video has an href", () => {
    render(<VideoCard video={{ ...base, href: "/watch/v1" }} />);
    expect(screen.getByRole("link")).toHaveAttribute("href", "/watch/v1");
  });

  it("is not a link for placeholders", () => {
    render(<VideoCard video={base} />);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("shows the generated thumbnail", () => {
    const { container } = render(<VideoCard video={base} />);
    expect(container.querySelector("img")).toHaveAttribute("src", "/api/v1/videos/v1/thumbnail");
  });
});
