import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { LogoMark } from "./icons";

describe("LogoMark", () => {
  it("renders the dragon as a decorative svg with a default size", () => {
    const { container } = render(<LogoMark />);
    const svg = container.querySelector("svg");
    expect(svg).toHaveAttribute("aria-hidden", "true");
    expect(svg).toHaveAttribute("viewBox", "0 0 64 64");
    expect(svg).toHaveClass("h-8", "w-8");
    expect(svg?.querySelectorAll("ellipse").length).toBeGreaterThanOrEqual(3); // head, snout and eyes
  });

  it("accepts a custom size", () => {
    const { container } = render(<LogoMark className="h-12 w-12" />);
    expect(container.querySelector("svg")).toHaveClass("h-12", "w-12");
  });
});
