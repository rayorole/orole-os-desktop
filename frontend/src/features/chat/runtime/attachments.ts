import type { AttachmentAdapter, PendingAttachment } from "@assistant-ui/react";
import { Chat, type Caps } from "@/mygo";
import { portableFileName } from "@/lib/attachmentFiles";

/** Mirrors internal/chat/api.go for UX only; the daemon validates for real. */
export const MAX_ATTACHMENT_BYTES = 20 << 20;
export const MAX_ATTACHMENTS = 10;
export const CHUNK_BYTES = 1 << 20;

/** The name of the `data` part a sent attachment carries; its `id` is the daemon's committed attachment id. */
export const ATTACHMENT_PART = "orole-attachment";

const IMAGE_TYPES: Record<string, true> = { "image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true };
const TEXT_APPLICATION = /^application\/(json|xml|x-yaml|yaml|toml|x-sh|x-httpd-php|javascript|typescript|sql|x-ndjson|x-ipynb\+json|ld\+json)$|\+(json|xml|yaml)$/;

export type Support = Pick<Caps, "vision" | "pdf">;

/** Why a file cannot be attached, or null when it can (an unknown type is left for the daemon to sniff). */
export function problemWith(file: Pick<File, "name" | "size" | "type">, count: number, support: Support | null): string | null {
  if (count >= MAX_ATTACHMENTS) return `At most ${MAX_ATTACHMENTS} files can be attached to one message.`;
  if (file.size === 0) return `${file.name} is empty.`;
  if (file.size > MAX_ATTACHMENT_BYTES) return `${file.name} is larger than ${MAX_ATTACHMENT_BYTES >> 20} MB.`;
  const type = file.type.toLowerCase();
  if (IMAGE_TYPES[type]) return support && !support.vision ? "This model cannot read images. Choose a model with vision support." : null;
  if (type === "application/pdf") return support && !support.pdf ? "This model cannot read PDFs. Choose a model with PDF support or attach the text instead." : null;
  if (!type || type === "application/octet-stream" || type.startsWith("text/") || TEXT_APPLICATION.test(type)) return null;
  return `${file.name} is not a supported file type. Attach images (PNG, JPEG, GIF, WebP), PDFs or text files.`;
}

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

const errorText = (cause: unknown) => (cause instanceof Error ? cause.message : String(cause)).replace(/^(bad_request|not_found|canceled|timeout|internal|denied): /, "");

/** Uploads one file in 1 MiB chunks and commits it; a failed or aborted upload is discarded on the daemon. Resolves with the committed attachment id. */
export async function uploadFile(file: File, signal?: AbortSignal): Promise<string> {
  const { id } = await Chat.beginAttachment({ name: portableFileName(file.name), size: file.size });
  try {
    let seq = 0;
    for (let offset = 0; offset < file.size; offset += CHUNK_BYTES, seq++) {
      signal?.throwIfAborted();
      const bytes = new Uint8Array(await file.slice(offset, offset + CHUNK_BYTES).arrayBuffer());
      await Chat.attachmentChunk({ id, seq, dataBase64: toBase64(bytes) });
    }
    signal?.throwIfAborted();
    return (await Chat.commitAttachment(id)).id;
  } catch (cause) {
    await Chat.discardAttachment(id).catch(() => {});
    throw cause;
  }
}

interface Entry { controller: AbortController; committed?: string }

/**
 * Stages files on the daemon as soon as they are added, so Send only has to reference them. `remove` discards
 * the staged file (or cancels the upload still in flight). Validation here is advisory; `setSupport` tells it
 * what the bot's model is known to read (null while unknown).
 */
export type BotAttachmentAdapter = AttachmentAdapter & { setSupport(next: Support | null): void };

export function createAttachmentAdapter(): BotAttachmentAdapter {
  const entries = new Map<string, Entry>();
  let support: Support | null = null;
  return {
    accept: "*",
    setSupport(next) { support = next; },
    async *add({ file }) {
      const id = crypto.randomUUID();
      const base = { id, type: file.type.startsWith("image/") ? "image" : "file", name: portableFileName(file.name), contentType: file.type, file } satisfies Omit<PendingAttachment, "status">;
      const problem = problemWith(file, entries.size, support);
      if (problem) {
        yield { ...base, status: { type: "incomplete", reason: "error", message: problem } };
        return;
      }
      const entry: Entry = { controller: new AbortController() };
      entries.set(id, entry);
      yield { ...base, status: { type: "running", reason: "uploading", progress: 0 } };
      try {
        entry.committed = await uploadFile(file, entry.controller.signal);
        yield { ...base, status: { type: "requires-action", reason: "composer-send" } };
      } catch (cause) {
        entries.delete(id);
        // A removal aborts the upload; nothing is left to show for it.
        if (!entry.controller.signal.aborted) yield { ...base, status: { type: "incomplete", reason: "error", message: errorText(cause) } };
      }
    },
    async remove(attachment) {
      const entry = entries.get(attachment.id);
      if (!entry) return;
      entries.delete(attachment.id);
      entry.controller.abort();
      if (entry.committed) await Chat.discardAttachment(entry.committed).catch(() => {});
    },
    async send(attachment) {
      const entry = entries.get(attachment.id);
      if (!entry?.committed) throw new Error(`${attachment.name} has not finished uploading.`);
      entries.delete(attachment.id);
      return { ...attachment, status: { type: "complete" }, content: [{ type: "data", name: ATTACHMENT_PART, data: { id: entry.committed } }] };
    },
  };
}
