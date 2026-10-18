/**
 * What every composer does with files that arrive by paste, drop or the attach button. Pure helpers: each composer
 * owns its limits and its upload, these only decide *which* files a clipboard carries and make their names safe.
 * The daemon validates again; nothing here is a security boundary.
 */

type Clipboard = Pick<DataTransfer, "files" | "items" | "getData">;

/**
 * The files a paste should attach, or `[]` when it is a text paste (which then runs as the browser always did).
 * Text wins whenever the clipboard carries any: a spreadsheet cell or a web selection comes with a rendered image
 * of itself, and pasting that image instead of the text would be wrong. The one exception is a file manager that
 * adds the file's own name as text (Finder does), which is not text anybody copied.
 */
export function pastedFiles(data: Clipboard | null | undefined): File[] {
  if (!data) return [];
  let files = Array.from(data.files ?? []);
  if (files.length === 0) {
    // Some sources hand an image over only as a clipboard item.
    files = Array.from(data.items ?? []).flatMap((item) => {
      const file = item.kind === "file" ? item.getAsFile() : null;
      return file ? [file] : [];
    });
  }
  if (files.length === 0) return [];
  const text = data.getData("text/plain").trim();
  if (text && !text.split(/\r?\n/).every((line) => files.some((file) => file.name === line.trim()))) return [];
  return files;
}

/** Whether a drag carries files (and not a selection of text or a link), so only a file drag is taken over. */
export function dragHasFiles(data: Pick<DataTransfer, "types"> | null | undefined): boolean {
  return Array.from(data?.types ?? []).includes("Files");
}

const RESERVED = /^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/i;
const MAX_NAME_BYTES = 255;
const encoder = new TextEncoder();

/**
 * A file name the daemon accepts: a plain, portable name. A pasted file's name is whatever the source app chose
 * (or the OS invented), so it is made safe instead of refusing the file. The shown name is never used as a path:
 * the daemon stores attachments under ids and the agent harnesses get opaque file names.
 */
export function portableFileName(name: string): string {
  let clean = name.replace(/[\p{Cc}\p{Cf}\\/:<>"|?*]/gu, "_").replace(/\p{Cs}/gu, "_").trim().replace(/[. ]+$/, "");
  if (RESERVED.test(clean.split(".", 1)[0] ?? "")) clean = `_${clean}`;
  if (encoder.encode(clean).length > MAX_NAME_BYTES) {
    const dot = clean.lastIndexOf(".");
    const ext = dot > 0 && clean.length - dot <= 16 ? clean.slice(dot) : "";
    let stem = clean.slice(0, ext ? dot : undefined);
    while (encoder.encode(stem + ext).length > MAX_NAME_BYTES) stem = stem.slice(0, -1);
    clean = stem + ext;
  }
  return clean || "pasted-file";
}
