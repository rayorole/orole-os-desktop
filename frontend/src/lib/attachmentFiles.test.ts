import { describe, expect, it } from "vitest";
import { dragHasFiles, pastedFiles, portableFileName } from "./attachmentFiles";

const file = (name: string, type = "") => new File(["x"], name, { type });

/** What a paste event's clipboardData holds, as far as the helper reads it. */
function clipboard({ files = [], text = "", items }: { files?: File[]; text?: string; items?: Array<{ kind: string; getAsFile: () => File | null }> }) {
  return { files: files as unknown as FileList, items: (items ?? []) as unknown as DataTransferItemList, getData: (type: string) => (type === "text/plain" ? text : "") };
}

describe("pastedFiles", () => {
  it("takes a screenshot and files copied from the file explorer", () => {
    const shot = file("image.png", "image/png");
    expect(pastedFiles(clipboard({ files: [shot] }))).toEqual([shot]);
    const two = [file("a.txt", "text/plain"), file("b.pdf", "application/pdf")];
    expect(pastedFiles(clipboard({ files: two }))).toEqual(two);
  });

  it("reads an image the clipboard offers only as an item", () => {
    const shot = file("image.png", "image/png");
    const got = pastedFiles(clipboard({ items: [{ kind: "string", getAsFile: () => null }, { kind: "file", getAsFile: () => shot }] }));
    expect(got).toEqual([shot]);
  });

  it("leaves a text paste to the browser", () => {
    expect(pastedFiles(clipboard({ text: "hello" }))).toEqual([]);
    expect(pastedFiles(null)).toEqual([]);
  });

  it("keeps the text when the clipboard also carries a file, as a copied spreadsheet cell or web selection does", () => {
    expect(pastedFiles(clipboard({ text: "1\t2", files: [file("image.png", "image/png")] }))).toEqual([]);
  });

  it("still attaches files whose only text is their own names, which is what some file managers add", () => {
    const files = [file("report.pdf", "application/pdf"), file("notes.txt", "text/plain")];
    expect(pastedFiles(clipboard({ text: "report.pdf\r\nnotes.txt", files }))).toEqual(files);
    expect(pastedFiles(clipboard({ text: "report.pdf and my notes", files }))).toEqual([]);
  });
});

describe("dragHasFiles", () => {
  it("is true only for a drag of files, not of text or a link", () => {
    expect(dragHasFiles({ types: ["Files"] })).toBe(true);
    expect(dragHasFiles({ types: ["text/plain", "text/uri-list"] })).toBe(false);
    expect(dragHasFiles(null)).toBe(false);
  });
});

describe("portableFileName", () => {
  it("keeps an ordinary name", () => {
    expect(portableFileName("Screenshot 2026-10-18 at 10.31.png")).toBe("Screenshot 2026-10-18 at 10.31.png");
    expect(portableFileName("zażółć.txt")).toBe("zażółć.txt");
  });

  it("replaces what the daemon refuses: path separators, reserved characters, controls and invisible formatting", () => {
    expect(portableFileName("a/b\\c:d<e>f\"g|h?i*.png")).toBe("a_b_c_d_e_f_g_h_i_.png");
    expect(portableFileName("../../etc/passwd")).toBe(".._.._etc_passwd");
    expect(portableFileName("line\nbreak\u202egnp.exe")).toBe("line_break_gnp.exe");
  });

  it("drops trailing dots and spaces and avoids Windows device names", () => {
    expect(portableFileName("  report. . ")).toBe("report");
    expect(portableFileName("CON")).toBe("_CON");
    expect(portableFileName("nul.txt")).toBe("_nul.txt");
    expect(portableFileName("console.log")).toBe("console.log");
  });

  it("falls back to a name when nothing is left, and cuts a long name to 255 bytes keeping its extension", () => {
    expect(portableFileName("...")).toBe("pasted-file");
    expect(portableFileName("")).toBe("pasted-file");
    const long = portableFileName(`${"é".repeat(300)}.png`);
    expect(new TextEncoder().encode(long).length).toBeLessThanOrEqual(255);
    expect(long.endsWith(".png")).toBe(true);
  });
});
