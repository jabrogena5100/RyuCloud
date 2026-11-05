"use client";

import { useState, type ChangeEvent, type DragEvent } from "react";
import { formatBytes } from "@/lib/format";
import { UploadIcon } from "./icons";

type DropzoneProps = {
  file: File | null;
  accept: string;
  disabled?: boolean;
  onFile: (file: File) => void;
};

/** Drag-and-drop target with a keyboard-accessible "select file" button. Validation is the caller's job. */
export function Dropzone({ file, accept, disabled = false, onFile }: DropzoneProps) {
  const [dragging, setDragging] = useState(false);

  function handleDragOver(event: DragEvent<HTMLDivElement>) {
    event.preventDefault(); // required for the drop event to fire
    if (!disabled) {
      setDragging(true);
    }
  }

  function handleDrop(event: DragEvent<HTMLDivElement>) {
    event.preventDefault();
    setDragging(false);
    const dropped = event.dataTransfer?.files?.[0];
    if (dropped && !disabled) {
      onFile(dropped);
    }
  }

  function handleChange(event: ChangeEvent<HTMLInputElement>) {
    const selected = event.target.files?.[0];
    if (selected) {
      onFile(selected);
    }
    event.target.value = ""; // allow choosing the same file again after an error
  }

  return (
    <div
      data-testid="dropzone"
      onDragOver={handleDragOver}
      onDragLeave={() => setDragging(false)}
      onDrop={handleDrop}
      className={`flex flex-col items-center gap-2 rounded-2xl border-2 border-dashed px-6 py-10 text-center transition-colors ${
        dragging
          ? "border-blue-500 bg-blue-50 dark:bg-blue-950/40"
          : "border-neutral-300 dark:border-neutral-700"
      } ${disabled ? "opacity-60" : ""}`}
    >
      <UploadIcon className="h-10 w-10 text-neutral-500" />
      {file ? (
        <>
          <p className="max-w-full break-all font-medium">{file.name}</p>
          <p className="text-sm text-neutral-600 dark:text-neutral-400">{formatBytes(file.size)}</p>
        </>
      ) : (
        <p className="text-neutral-600 dark:text-neutral-400">Drag and drop a video file here, or</p>
      )}
      <label
        className={`mt-1 rounded-full bg-blue-600 px-4 py-2 text-sm font-medium text-white focus-within:ring-2 focus-within:ring-blue-500/50 ${
          disabled ? "cursor-not-allowed" : "cursor-pointer hover:bg-blue-700"
        }`}
      >
        {file ? "Choose a different file" : "Select video file"}
        <input type="file" accept={accept} disabled={disabled} onChange={handleChange} className="sr-only" />
      </label>
    </div>
  );
}
