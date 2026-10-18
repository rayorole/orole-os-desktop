import { act, cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Session } from "@/mygo";

const api = vi.hoisted(() => ({
  sendMessage: vi.fn(), options: vi.fn(), cancelQueuedPrompt: vi.fn(), resumeQueue: vi.fn(), setPermissionMode: vi.fn(),
  uploadAttachment: vi.fn(), discardAttachment: vi.fn(), upsert: vi.fn(), toast: vi.fn(),
}));
vi.mock("@/mygo", () => ({ Agents: api }));
vi.mock("@/stores/agents", () => ({ useAgents: { getState: () => ({ upsert: api.upsert }) } }));
vi.mock("@/features/agents/shared", () => ({ ErrorNote: ({ error }: { error: string | null }) => error ? <p role="alert">{error}</p> : null }));
vi.mock("@/features/projects/dialogs", async (original) => ({ ...(await original<object>()), toastError: api.toast }));
vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/stores/daemon", () => ({ useDaemon: (select: (state: { link: string }) => unknown) => select({ link: "connected" }) }));
import { useHarnessOptionsCache } from "@/stores/harnessOptions";
import { useTaskDrafts } from "@/stores/taskDrafts";
import { Composer } from "./Composer";

const session = (): Session => ({
  id: "task-a", kind: "claude", mode: "structured", machine: "local", live: true, status: "idle", pending: [], questions: [], queue: [],
  followup: { attachments: true, model: false, reasoning: false, reason: "", permissionMode: false, permissionModeRestarts: false, permissionModeReason: "" },
}) as unknown as Session;

const box = () => screen.getByLabelText("Message the agent");
const png = () => new File(["png-bytes"], "image.png", { type: "image/png" });
const uploaded = (name: string) => ({ id: `staged-${name}`, name, size: 9, mime: "application/octet-stream" });

/** Fires a paste carrying `clipboardData`; false means the composer took the paste over (default prevented). */
function paste(files: File[], text = "") {
  const event = createEvent.paste(box(), { clipboardData: { files, items: [], types: [], getData: (type: string) => (type === "text/plain" ? text : "") } });
  fireEvent(box(), event);
  return event;
}

beforeEach(() => {
  vi.clearAllMocks();
  useHarnessOptionsCache.setState({ machines: {} });
  useTaskDrafts.setState({ drafts: {} });
  api.options.mockResolvedValue({ harnesses: [] });
  api.sendMessage.mockResolvedValue(session());
  api.uploadAttachment.mockImplementation(async ({ name }: { name: string }) => uploaded(name));
  api.discardAttachment.mockResolvedValue(undefined);
});
afterEach(cleanup);

describe("pasting into the task composer", () => {
  it("attaches a pasted screenshot as a chip, stages it on the daemon and sends its id", async () => {
    render(<Composer session={session()} />);
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
    expect(paste([png()]).defaultPrevented).toBe(true);
    const chip = (await screen.findByText("image.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
    await waitFor(() => expect(chip).toHaveAttribute("data-state", "done"));
    expect(api.uploadAttachment).toHaveBeenCalledWith({ name: "image.png", dataBase64: btoa("png-bytes") });
    expect(box()).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledWith("task-a", "", ["staged-image.png"], "", ""));
    await waitFor(() => expect(screen.queryByText("image.png")).toBeNull());
  });

  it("attaches files pasted from the file explorer, whatever their type", async () => {
    render(<Composer session={session()} />);
    paste([new File(["a"], "build.log", { type: "text/plain" }), new File(["b"], "data.zip", { type: "application/zip" })]);
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(2));
    expect(api.uploadAttachment.mock.calls.map(([spec]) => spec.name)).toEqual(["build.log", "data.zip"]);
  });

  it("leaves a plain text paste to the browser, and keeps the text when the clipboard also has a file", async () => {
    render(<Composer session={session()} />);
    expect(paste([], "just words").defaultPrevented).toBe(false);
    expect(paste([png()], "1\t2\n3\t4").defaultPrevented).toBe(false);
    await act(async () => { await Promise.resolve(); });
    expect(api.uploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
  });

  it("stores a safe name for a pasted file whose name the daemon would refuse", async () => {
    render(<Composer session={session()} />);
    paste([new File(["x"], "a:b?.png", { type: "image/png" })]);
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledWith(expect.objectContaining({ name: "a_b_.png" })));
    expect(await screen.findByText("a_b_.png")).toBeInTheDocument();
  });

  it("stops at eight files and says how many were left out", async () => {
    render(<Composer session={session()} />);
    paste(Array.from({ length: 10 }, (_, i) => new File([`f${i}`], `f${i}.txt`, { type: "text/plain" })));
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledTimes(8));
    expect(api.toast).toHaveBeenCalledWith("Too many attachments", expect.objectContaining({ message: "Attach up to 8 files per task; 2 not added." }));
    expect(screen.getAllByRole("button", { name: /^Remove / })).toHaveLength(8);
  });

  it("refuses a file over 2 MB without uploading it, names the limit on its chip and lets the user remove it", async () => {
    render(<Composer session={session()} />);
    paste([new File([new Uint8Array(2 * 1024 * 1024 + 1)], "big.png", { type: "image/png" })]);
    const chip = (await screen.findByText("big.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
    await waitFor(() => expect(chip).toHaveAttribute("data-state", "error"));
    expect(within(chip).getByRole("alert")).toHaveTextContent("files must be 2 MB or smaller");
    expect(api.uploadAttachment).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Send" })).toBeNull(); // no text and nothing staged
    fireEvent.change(box(), { target: { value: "look" } });
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    fireEvent.click(within(chip).getByRole("button", { name: "Remove big.png" }));
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("discards a staged file when its chip is removed", async () => {
    render(<Composer session={session()} />);
    paste([png()]);
    const chip = (await screen.findByText("image.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
    await waitFor(() => expect(chip).toHaveAttribute("data-state", "done"), { timeout: 5000 });
    fireEvent.click(within(chip).getByRole("button", { name: "Remove image.png" }));
    await waitFor(() => expect(api.discardAttachment).toHaveBeenCalledWith("staged-image.png"), { timeout: 5000 });
    expect(screen.queryByText("image.png")).toBeNull();
  });

  it("previews a pasted image on its chip and releases the preview when the chip goes", async () => {
    const create = vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:preview-1");
    const revoke = vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
    try {
      render(<Composer session={session()} />);
      paste([png(), new File(["<svg/>"], "vector.svg", { type: "image/svg+xml" })]);
      const chip = (await screen.findByText("image.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
      await waitFor(() => expect(chip.querySelector("img")).toHaveAttribute("src", "blob:preview-1"));
      expect(create).toHaveBeenCalledTimes(1); // only raster types are drawn: the SVG keeps its icon
      expect((await screen.findByText("vector.svg")).closest("[data-slot=composer-attachment]")!.querySelector("img")).toBeNull();
      fireEvent.click(within(chip).getByRole("button", { name: "Remove image.png" }));
      await waitFor(() => expect(revoke).toHaveBeenCalledWith("blob:preview-1"));
    } finally {
      create.mockRestore();
      revoke.mockRestore();
    }
  });

  it("takes dropped files through the same path, and ignores a drag of text", async () => {
    render(<Composer session={session()} />);
    const form = box().closest("form")!;
    const dropText = createEvent.drop(form, { dataTransfer: { types: ["text/plain"], files: [] } });
    fireEvent(form, dropText);
    expect(dropText.defaultPrevented).toBe(false);
    expect(api.uploadAttachment).not.toHaveBeenCalled();
    fireEvent.dragEnter(form, { dataTransfer: { types: ["Files"] } });
    expect(await screen.findByText("Drop files to attach")).toBeInTheDocument();
    fireEvent.drop(form, { dataTransfer: { types: ["Files"], files: [png()] } });
    await waitFor(() => expect(api.uploadAttachment).toHaveBeenCalledWith(expect.objectContaining({ name: "image.png" })));
    expect(screen.queryByText("Drop files to attach")).toBeNull();
  });
});
