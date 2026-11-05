import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Dropzone } from "./Dropzone";

const video = () => new File(["abc"], "clip.mp4", { type: "video/mp4" });

describe("Dropzone", () => {
  it("reports a file chosen with the file picker", async () => {
    const onFile = vi.fn();
    render(<Dropzone file={null} accept=".mp4" onFile={onFile} />);
    const file = video();
    await userEvent.setup().upload(screen.getByLabelText("Select video file"), file);
    expect(onFile).toHaveBeenCalledWith(file);
  });

  it("reports a file dropped onto the zone", () => {
    const onFile = vi.fn();
    render(<Dropzone file={null} accept=".mp4" onFile={onFile} />);
    const file = video();
    fireEvent.drop(screen.getByTestId("dropzone"), { dataTransfer: { files: [file] } });
    expect(onFile).toHaveBeenCalledWith(file);
  });

  it("ignores drops and picks while disabled", () => {
    const onFile = vi.fn();
    render(<Dropzone file={null} accept=".mp4" disabled onFile={onFile} />);
    fireEvent.drop(screen.getByTestId("dropzone"), { dataTransfer: { files: [video()] } });
    expect(onFile).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Select video file")).toBeDisabled();
  });

  it("shows the selected file's name and size", () => {
    render(<Dropzone file={new File(["abc"], "holiday.mp4", { type: "video/mp4" })} accept=".mp4" onFile={vi.fn()} />);
    expect(screen.getByText("holiday.mp4")).toBeInTheDocument();
    expect(screen.getByText("3 B")).toBeInTheDocument();
    expect(screen.getByLabelText("Choose a different file")).toBeInTheDocument();
  });
});
