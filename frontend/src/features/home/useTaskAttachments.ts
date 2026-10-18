import { useEffect, useRef, useState } from "react";
import { Agents } from "@/mygo";
import { errText, toastError } from "@/features/projects/dialogs";
import { portableFileName } from "@/lib/attachmentFiles";
import type { ComposerAttachment } from "@/components/assistant-ui/elements/composer-attachments";

export const MAX_FILES = 8;
export const MAX_BYTES = 2 * 1024 * 1024;
export type TaskAttachment = ComposerAttachment & { key: string; uploadId?: string };

/** Why a file cannot be attached to a task, or null. The daemon enforces the same limits on what it receives. */
export function problemWith(file: Pick<File, "name" | "size">): string | null {
  if (file.size === 0) return `${portableFileName(file.name)} is empty.`;
  if (file.size > MAX_BYTES) return `${portableFileName(file.name)} is ${sizeLabel(file.size)}; files must be ${MAX_BYTES >> 20} MB or smaller.`;
  return null;
}

function fileData(file: File): Promise<string> {
  const { promise, resolve, reject } = Promise.withResolvers<string>();
  const reader = new FileReader();
  reader.onload = () => resolve(String(reader.result).split(",", 2)[1] ?? "");
  reader.onerror = () => reject(reader.error ?? new Error("Could not read this file."));
  reader.onabort = () => reject(new Error("File reading was cancelled."));
  reader.readAsDataURL(file);
  return promise;
}
function sizeLabel(bytes: number) {
  return bytes < 1024 ? `${bytes} B` : bytes < 1024 * 1024 ? `${Math.ceil(bytes / 1024)} KB` : `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
function discard(id: string) {
  // Abandoned staging files also expire in the daemon. Never turn a successful
  // launch into a failure because disposal raced a daemon disconnect.
  void Agents.discardAttachment(id).catch(() => {});
}

/**
 * `initial`: uploads a failed launch hands back; they are already staged in the daemon. `unavailable`: why files
 * cannot be added right now (shown when someone pastes or drops one anyway); empty keeps a busy composer quiet.
 */
export function useTaskAttachments(enabled: boolean, initial: TaskAttachment[] = [], unavailable = "") {
  const [files, setFiles] = useState<TaskAttachment[]>(initial);
  const current = useRef<TaskAttachment[]>(initial);
  const mounted = useRef(true);
  function update(next: TaskAttachment[]) { current.current = next; if (mounted.current) setFiles(next); }
  function clear() {
    const previous = current.current;
    update([]);
    for (const file of previous) if (file.uploadId) discard(file.uploadId);
  }
  /** Hands the files to a launch: they leave this composer without being discarded, the launch now owns them. */
  function release() {
    const previous = current.current;
    update([]);
    return previous;
  }
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      const previous = current.current;
      current.current = [];
      for (const file of previous) if (file.uploadId) discard(file.uploadId);
    };
  }, []);
  function remove(key: string) {
    const file = current.current.find((item) => item.key === key);
    update(current.current.filter((item) => item.key !== key));
    if (file?.uploadId) discard(file.uploadId);
  }
  async function add(incoming: File[]) {
    if (!mounted.current) return;
    if (!enabled) {
      // Say why instead of ignoring a paste or a drop; a busy composer (no reason given) stays quiet.
      if (unavailable) toastError("Files cannot be attached", new Error(unavailable));
      return;
    }
    const room = Math.max(MAX_FILES - current.current.length, 0);
    if (incoming.length > room) toastError("Too many attachments", new Error(`Attach up to ${MAX_FILES} files per task; ${incoming.length - room} not added.`));
    const entries = incoming.slice(0, room).map((file) => ({ file, entry: {
      key: crypto.randomUUID(), name: portableFileName(file.name), meta: "Uploading…", state: "uploading" as const, file,
      kind: file.type.startsWith("image/") ? "image" as const : /\.(zip|gz|tar|7z|rar)$/i.test(file.name) ? "archive" as const : "text" as const,
    } }));
    update([...current.current, ...entries.map(({ entry }) => entry)]);
    for (const { file, entry } of entries) {
      if (!current.current.some((item) => item.key === entry.key)) continue;
      try {
        const problem = problemWith(file);
        if (problem) throw new Error(problem);
        const dataBase64 = await fileData(file);
        if (!current.current.some((item) => item.key === entry.key)) continue;
        const uploaded = await Agents.uploadAttachment({ name: entry.name, dataBase64 });
        if (!mounted.current || !current.current.some((item) => item.key === entry.key)) { discard(uploaded.id); continue; }
        update(current.current.map((item) => item.key === entry.key ? { ...item, uploadId: uploaded.id, state: "done", meta: sizeLabel(uploaded.size) } : item));
      } catch (error) {
        if (!mounted.current || !current.current.some((item) => item.key === entry.key)) continue;
        update(current.current.map((item) => item.key === entry.key ? { ...item, state: "error", meta: errText(error) } : item));
        toastError(`Could not attach ${entry.name}`, error);
      }
    }
  }
  return { files, add, remove, clear, release, ids: files.flatMap((file) => file.uploadId ? [file.uploadId] : []), ready: files.every((file) => file.state === "done"), canAdd: enabled && files.length < MAX_FILES };
}
