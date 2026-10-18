import { useAuiState, type Attachment } from "@assistant-ui/react";
import type { ComposerAttachment } from "@/components/assistant-ui/elements/composer-attachments";
import { formatSize } from "./parts/format";

/** How an assistant-ui attachment reads as a chip; a failed one carries the reason, an uploading one says so. */
export function chipOf(attachment: Attachment): ComposerAttachment {
  const kind = attachment.type === "image" ? "image" : "text";
  const file = attachment.file;
  switch (attachment.status.type) {
    case "running":
      return { name: attachment.name, kind, file, state: "uploading", meta: "Uploading…" };
    case "incomplete":
      return { name: attachment.name, kind, file, state: "error", meta: attachment.status.message || "Could not upload this file." };
    default:
      return { name: attachment.name, kind, file, state: "done", meta: file ? formatSize(file.size) : "Ready" };
  }
}

const staged = (attachment: Attachment) => attachment.status.type === "requires-action" || attachment.status.type === "complete";

/** Whether a message can go out now: text or files, every file staged, none failed. While a reply runs the daemon queues it. */
export function useCanSend(): boolean {
  return useAuiState((s) => s.composer.canSend && s.composer.attachments.every(staged));
}
