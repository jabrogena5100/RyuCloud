import type { VideoStatus } from "@/lib/types";

const STATUS: Record<VideoStatus, { label: string; className: string; hint: string }> = {
  draft: {
    label: "Draft",
    className: "bg-neutral-100 text-neutral-700 dark:bg-neutral-800 dark:text-neutral-300",
    hint: "No video file has been uploaded.",
  },
  uploading: {
    label: "Uploading",
    className: "bg-blue-100 text-blue-800 dark:bg-blue-950 dark:text-blue-300",
    hint: "Waiting for the file to finish uploading.",
  },
  queued: {
    label: "Queued",
    className: "bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-200",
    hint: "Upload verified. Waiting for a free processing worker.",
  },
  processing: {
    label: "Processing",
    className: "bg-indigo-100 text-indigo-900 dark:bg-indigo-950 dark:text-indigo-200",
    hint: "Being converted to streaming formats and a thumbnail.",
  },
  ready: {
    label: "Ready",
    className: "bg-green-100 text-green-800 dark:bg-green-950 dark:text-green-300",
    hint: "Ready to watch.",
  },
  failed: {
    label: "Failed",
    className: "bg-red-100 text-red-800 dark:bg-red-950 dark:text-red-300",
    hint: "Upload or processing failed. Delete this video and upload it again.",
  },
};

export function statusHint(status: VideoStatus): string {
  return STATUS[status].hint;
}

/** Whether the video is still moving through the pipeline (worth polling for). */
export function isInProgress(status: VideoStatus): boolean {
  return status === "uploading" || status === "queued" || status === "processing";
}

export function StatusBadge({ status, progress }: { status: VideoStatus; progress?: number | null }) {
  const { label, className, hint } = STATUS[status];
  const showPercent = status === "processing" && typeof progress === "number";
  return (
    <span title={hint} className={`inline-block rounded-full px-2.5 py-0.5 text-xs font-medium ${className}`}>
      {label}
      {showPercent ? ` ${progress}%` : ""}
    </span>
  );
}
