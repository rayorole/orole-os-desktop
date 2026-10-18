import { act, cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HarnessOption, LaunchOptions, Machine, Project } from "@/mygo";

const api = vi.hoisted(() => ({ options: vi.fn(), start: vi.fn(), upload: vi.fn(), discard: vi.fn(), toast: vi.fn(), settings: vi.fn(), setSetting: vi.fn() }));
vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/mygo", () => ({ Agents: { options: api.options, start: api.start, uploadAttachment: api.upload, discardAttachment: api.discard }, Settings: { all: api.settings, set: api.setSetting }, events: {} }));
vi.mock("@/features/projects/dialogs", async (original) => ({ ...(await original<object>()), toastError: api.toast }));
import { useAgents } from "@/stores/agents";
import { useBoxes } from "@/stores/boxes";
import { useDaemon } from "@/stores/daemon";
import { useHarnessOptionsCache } from "@/stores/harnessOptions";
import { useProjects } from "@/stores/projects";
import { useUI } from "@/stores/ui";
import { HomeAgentComposer } from "./HomeAgentComposer";

const project: Project = { id: "notes", name: "Research", machineId: "local", path: "", remoteUrl: "", lastOpened: 0, createdAt: 0, worktreeRoot: "", worktreeRootCustom: false, missing: false, isGit: false };
const harness: HarnessOption = { id: "codex", name: "Codex", available: true, executable: "codex", version: "1", reasoning: [], customModel: true, modes: ["structured"], structured: true, interactive: false, unavailableReason: "", permissionModes: [], models: [{ id: "model-a", name: "Model A", reasoning: [], default: true }] };
const options: LaunchOptions = { machine: "local", harnesses: [harness], projects: [], directories: [], worktree: true, unavailableReason: "" };

beforeEach(() => {
  useDaemon.setState({ link: "connected", connectEpoch: 0 });
  useHarnessOptionsCache.setState({ machines: {} });
  vi.clearAllMocks();
  localStorage.clear();
  api.options.mockResolvedValue(options);
  api.settings.mockResolvedValue({});
  api.setSetting.mockResolvedValue(undefined);
  api.start.mockResolvedValue({ id: "task", updatedAt: "2026-10-08T00:00:00Z" });
  api.upload.mockImplementation(async ({ name }: { name: string }) => ({ id: `staged-${name}`, name, size: 9, mime: "application/octet-stream" }));
  api.discard.mockResolvedValue(undefined);
  useAgents.setState({ sessions: {}, launches: {}, launchDraft: null, openId: null });
  useUI.setState({ view: "home" });
  useBoxes.setState({ machines: [{ id: "local", name: "devl" }] as Machine[], observations: {} });
  useProjects.setState({ projects: [project], worktrees: {}, selectedId: null, available: true, loaded: true, newProjectOpen: false });
});
afterEach(cleanup);

const box = () => screen.getByLabelText("Task for your agent");
const png = () => new File(["png-bytes"], "image.png", { type: "image/png" });
function paste(files: File[], text = "") {
  const event = createEvent.paste(box(), { clipboardData: { files, items: [], types: [], getData: (type: string) => (type === "text/plain" ? text : "") } });
  fireEvent(box(), event);
  return event;
}
const ready = async () => { await screen.findByRole("button", { name: "Start task" }); };

describe("pasting into the new task composer", () => {
  it("attaches a pasted screenshot and launches the task with its staged id", async () => {
    render(<HomeAgentComposer />);
    await ready();
    expect(paste([png()]).defaultPrevented).toBe(true);
    const chip = (await screen.findByText("image.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
    await waitFor(() => expect(chip).toHaveAttribute("data-state", "done"));
    expect(api.upload).toHaveBeenCalledWith({ name: "image.png", dataBase64: btoa("png-bytes") });
    fireEvent.change(box(), { target: { value: "Fix what the screenshot shows" } });
    fireEvent.click(screen.getByRole("button", { name: "Start task" }));
    await waitFor(() => expect(api.start).toHaveBeenCalledWith(expect.objectContaining({ prompt: "Fix what the screenshot shows", attachmentIds: ["staged-image.png"] })));
  });

  it("leaves text pastes alone, including a clipboard that has text and a file", async () => {
    render(<HomeAgentComposer />);
    await ready();
    expect(paste([], "just words").defaultPrevented).toBe(false);
    expect(paste([png()], "A1\tB1").defaultPrevented).toBe(false);
    await act(async () => { await Promise.resolve(); });
    expect(api.upload).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
  });

  it("stores a safe name for a pasted file", async () => {
    render(<HomeAgentComposer />);
    await ready();
    paste([new File(["x"], "report:final?.pdf", { type: "application/pdf" })]);
    await waitFor(() => expect(api.upload).toHaveBeenCalledWith(expect.objectContaining({ name: "report_final_.pdf" })));
  });

  it("names the 2 MB limit on the chip of a file that is too large and uploads nothing", async () => {
    render(<HomeAgentComposer />);
    await ready();
    paste([new File([new Uint8Array(2 * 1024 * 1024 + 1)], "big.png", { type: "image/png" })]);
    const chip = (await screen.findByText("big.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
    await waitFor(() => expect(chip).toHaveAttribute("data-state", "error"));
    expect(within(chip).getByRole("alert")).toHaveTextContent("files must be 2 MB or smaller");
    expect(api.upload).not.toHaveBeenCalled();
  });

  it("keeps eight files, says how many were left out, and removes one chip on request", async () => {
    render(<HomeAgentComposer />);
    await ready();
    paste(Array.from({ length: 9 }, (_, i) => new File([`f${i}`], `f${i}.txt`, { type: "text/plain" })));
    await waitFor(() => expect(api.upload).toHaveBeenCalledTimes(8));
    expect(api.toast).toHaveBeenCalledWith("Too many attachments", expect.objectContaining({ message: "Attach up to 8 files per task; 1 not added." }));
    // Full: another paste is explained, not ignored.
    api.toast.mockClear();
    paste([png()]);
    await waitFor(() => expect(api.toast).toHaveBeenCalledWith("Too many attachments", expect.anything()));
    expect(api.upload).toHaveBeenCalledTimes(8);
    fireEvent.click(screen.getByRole("button", { name: "Remove f0.txt" }));
    await waitFor(() => expect(api.discard).toHaveBeenCalledWith("staged-f0.txt"));
    expect(screen.queryByText("f0.txt")).toBeNull();
  });

  it("takes a drop through the same path", async () => {
    render(<HomeAgentComposer />);
    await ready();
    const form = box().closest("form")!;
    fireEvent.dragEnter(form, { dataTransfer: { types: ["Files"] } });
    expect(await screen.findByText("Drop files to attach")).toBeInTheDocument();
    fireEvent.drop(form, { dataTransfer: { types: ["Files"], files: [png()] } });
    await waitFor(() => expect(api.upload).toHaveBeenCalledWith(expect.objectContaining({ name: "image.png" })));
  });
});
