import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Bot, Message, Session } from "@/mygo";

const api = vi.hoisted(() => ({ get: vi.fn(), sendMessage: vi.fn(), options: vi.fn(), cancelQueuedPrompt: vi.fn(), resumeQueue: vi.fn(), setPermissionMode: vi.fn() }));
// Every daemon event is silent here: tasks change through the store.
vi.mock("@/mygo", () => ({ Agents: api, events: new Proxy({}, { get: () => ({ on: () => () => {} }) }) }));
vi.mock("@/components/ascii/AsciiMark", () => ({ SPACE_PIECES: [], AsciiMark: () => null }));
vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/stores/llm", () => ({ useHasProvider: () => true }));
vi.mock("@/stores/daemon", () => ({ useDaemon: Object.assign((select: (state: { link: string }) => unknown) => select({ link: "connected" }), { subscribe: () => () => {} }) }));
vi.mock("@/features/agents/shared", () => ({ ErrorNote: ({ error }: { error: string | null }) => error ? <p role="alert">{error}</p> : null }));
// The live feed is the Tasks view's own (tested there); here it only has to show which task it follows. The composer is the real one,
// loaded inside the factory because vi.mock factories run before this file's static imports.
vi.mock("@/features/tasks/TaskView", async () => {
  const { Composer } = await import("@/features/tasks/Composer");
  return { TaskView: ({ session }: { session: Session }) => <div><div data-testid="task-feed">{session.id}</div><Composer session={session} /></div> };
});
// The conversation is one reply that started three tasks and wrote a document.
const reply = vi.hoisted((): Message => ({ id: "m1", botId: "a", seq: 1, role: "assistant", author: "bot:a", createdAt: 1, status: "complete", parts: [
  { type: "text", text: "Starting three tasks." },
  { type: "task", id: "t1", title: "Fix login", name: "claude" },
  { type: "task", id: "t2", title: "Add tests", name: "codex" },
  { type: "task", id: "t3", title: "Write docs", name: "claude" },
  { type: "ui", id: "write_document", name: "document", spec: JSON.stringify({ title: "Rollout plan", content: "# Rollout\n\nShip **Friday**.", version: 2 }) },
] }));
vi.mock("@/features/chat/Conversation", async () => {
  const { MessageParts } = await import("@/features/chat/parts/MessageParts");
  return { Conversation: () => <div data-testid="conversation"><MessageParts message={reply} /></div> };
});

import { ChatView } from "./ChatView";
import { useChat } from "@/stores/chat";
import { useChatMessages } from "@/stores/chatMessages";
import { applySessionDelta, useAgents } from "@/stores/agents";
import { useHarnessOptionsCache } from "@/stores/harnessOptions";

const bot = (id: string): Bot => ({
  id, name: id, avatar: "", color: "blue", description: "", instructions: "", provider: "", model: "", reasoning: "",
  config: { tools: [], mcpServers: [], skills: [], memory: [], limits: { maxSteps: 0, maxToolCalls: 0 } },
  createdAt: 1, updatedAt: 1, running: false,
});
const session = (id: string, patch: Partial<Session>): Session => ({ id, title: "", task: "", kind: "claude", mode: "structured", machine: "local", live: true, status: "working", pending: [], questions: [], queue: [], updatedAt: "2026-10-12T10:00:00Z", error: "", ...patch }) as Session;

beforeEach(() => {
  vi.clearAllMocks();
  api.options.mockResolvedValue({ harnesses: [] });
  api.sendMessage.mockImplementation(async (id: string) => useAgents.getState().sessions[id]);
  useHarnessOptionsCache.setState({ machines: {} });
  useChat.setState({ bots: [bot("a"), bot("b")], selectedBotId: "a", running: {}, builder: { open: false, botId: null }, settingsOpen: false, pane: null,
    available: true, ready: true, loading: false, error: null, startSync: vi.fn(() => () => {}), load: vi.fn(async () => {}) });
  useAgents.setState({ loaded: true, available: true, sessions: {
    t1: session("t1", { title: "Fix the login bug", kind: "claude", status: "working" }),
    t2: session("t2", { title: "Add tests", kind: "codex", status: "working" }),
    t3: session("t3", { title: "Write docs", kind: "claude", status: "needs-you" }),
  } });
});
afterEach(cleanup);

const chips = () => within(screen.getByRole("list", { name: "Code tasks" })).getAllByRole("button");
const pane = () => useChat.getState().pane;

describe("task rows in a bot reply", () => {
  it("shows one row per started task with its live status, title, activity and harness", () => {
    render(<ChatView />);
    const all = chips();
    expect(all).toHaveLength(3);
    expect(all.map((c) => c.getAttribute("data-status"))).toEqual(["running", "running", "needs-you"]);
    // The live title wins over the one the bot gave at start.
    expect(all[0]).toHaveTextContent("Fix the login bug");
    expect(all[0]).toHaveTextContent("Claude Code");
    expect(all[1]).toHaveTextContent("Codex");
    act(() => useAgents.setState((s) => ({ sessions: { ...s.sessions, t1: { ...s.sessions.t1!, subagents: 2 } } })));
    expect(chips()[0]).toHaveTextContent("2 background agents running");
    act(() => useAgents.setState((s) => ({ sessions: { ...s.sessions, t1: { ...s.sessions.t1!, status: "done", subagents: 0 } } })));
    expect(chips()[0]).toHaveAttribute("data-status", "done");
  });

  it("draws each task as a chat bubble", () => {
    render(<ChatView />);
    const bubble = chips()[0]!.closest("[data-slot=chat-bubble]");
    expect(bubble).not.toBeNull();
    expect(bubble!.className).toContain("bg-secondary");
  });

  it("meters model, elapsed time and tokens live while the task works, and freezes them when it stops", () => {
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date("2026-10-12T10:00:10Z"));
      useAgents.setState((s) => ({ sessions: { ...s.sessions, t1: { ...s.sessions.t1!, model: "claude-opus-5-5", startedAt: "2026-10-12T10:00:00Z", tokens: 1250 } } }));
      render(<ChatView />);
      const meter = () => within(chips()[0]!).getByText((_, el) => el?.getAttribute("data-slot") === "task-meter");
      expect(meter()).toHaveTextContent("Claude Opus 5.5");
      expect(meter()).toHaveTextContent("10s");
      expect(meter()).toHaveTextContent("1.3K");
      act(() => { vi.advanceTimersByTime(5000); });
      expect(meter()).toHaveTextContent("15s");
      // A token reading arrives as a hot-path delta while the task works.
      act(() => useAgents.setState((s) => ({ sessions: applySessionDelta(s.sessions, { id: "t1", updatedAt: "2026-10-12T10:00:16Z", tokens: 1800 }) })));
      expect(meter()).toHaveTextContent("1.8K");
      act(() => useAgents.setState((s) => ({ sessions: { ...s.sessions, t1: { ...s.sessions.t1!, status: "done", endedAt: "2026-10-12T10:00:20Z", tokens: 2500 } } })));
      act(() => { vi.advanceTimersByTime(60_000); });
      expect(meter()).toHaveTextContent("20s");
      expect(meter()).toHaveTextContent("2.5K");
    } finally { vi.useRealTimers(); }
  });

  it("shows a dash for a model, start time or token count the daemon has not reported", () => {
    render(<ChatView />);
    const meter = within(chips()[1]!).getByText((_, el) => el?.getAttribute("data-slot") === "task-meter");
    expect(meter.textContent?.match(/—/g)).toHaveLength(3);
  });

  it("meters tokens without cache reads and breaks the billed tokens down on hover", async () => {
    // The task from #207: 1,199,955 billed tokens, 1,099,170 of them cache reads.
    const billed = { input: 3_285, cacheWrite: 61_300, cacheRead: 1_099_170, output: 36_200, reasoning: 0, total: 1_199_955, work: 100_785, costUsd: 4.12, unpriced: [] };
    useAgents.setState((s) => ({ sessions: { ...s.sessions, t1: { ...s.sessions.t1!, tokens: 102_785, billed } } }));
    render(<ChatView />);
    const tokens = within(chips()[0]!).getByText((_, el) => el?.getAttribute("data-slot") === "task-tokens");
    expect(tokens).toHaveTextContent("102.8K");
    expect(tokens).not.toHaveTextContent("1.2M");
    fireEvent.pointerEnter(tokens, { pointerType: "mouse" });
    fireEvent.pointerMove(tokens, { pointerType: "mouse" });
    fireEvent.mouseEnter(tokens);
    fireEvent.mouseMove(tokens);
    const breakdown = await screen.findByText((_, el) => el?.getAttribute("data-slot") === "token-breakdown", {}, { timeout: 3000 });
    const rows = [...breakdown.querySelectorAll("dt")].map((dt) => `${dt.textContent} ${dt.nextElementSibling?.textContent}`);
    expect(rows).toEqual(["Input 3,285", "Cache write 61,300", "Cache read 1,099,170", "Output 36,200", "Cost $4.12"]);
    // The running turn's estimate is in the headline but not billed yet.
    expect(breakdown).toHaveTextContent("+2,000 estimated for the running turn");
    expect(breakdown).toHaveTextContent("Cache reads are left out: they are the context re-sent on each request.");
  });

  it("names a model alias after the model the harness's catalog says it runs, before the agent reports its id", async () => {
    api.options.mockResolvedValue({ harnesses: [{ id: "claude", models: [{ id: "opus", name: "Opus (claude-opus-5-5)" }] }] });
    useAgents.setState((s) => ({ sessions: { ...s.sessions, t1: { ...s.sessions.t1!, model: "opus" } } }));
    render(<ChatView />);
    const meter = () => within(chips()[0]!).getByText((_, el) => el?.getAttribute("data-slot") === "task-meter");
    await waitFor(() => expect(meter()).toHaveTextContent("Claude Opus 5.5"));
    expect(meter()).not.toHaveTextContent("opus");
  });

  it("opens the task beside the conversation with a composer that talks to that task, and closes back to the chat", async () => {
    render(<ChatView />);
    fireEvent.click(chips()[1]!);
    expect(pane()).toEqual({ kind: "task", id: "t2" });
    // The pane chunk is lazy and its body waits for the open animation: allow for a loaded test runner.
    const region = await screen.findByRole("region", { name: "Add tests" }, { timeout: 4000 });
    expect(await within(region).findByTestId("task-feed", {}, { timeout: 4000 })).toHaveTextContent("t2");
    expect(screen.getByTestId("conversation")).toBeInTheDocument();
    expect(chips()[1]).toHaveAttribute("aria-pressed", "true");

    const input = within(region).getByLabelText("Message the agent");
    fireEvent.change(input, { target: { value: "Also cover the edge cases" } });
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(api.sendMessage).toHaveBeenCalledTimes(1));
    expect(api.sendMessage.mock.calls[0]![0]).toBe("t2");
    expect(api.sendMessage.mock.calls[0]![1]).toBe("Also cover the edge cases");

    expect(within(region).queryByText("Open in Tasks")).toBeNull();
    expect(region.querySelector("[data-slot=harness-badge]")).toBeNull();
    expect(within(region).getByRole("button", { name: "Open task page" })).toBeInTheDocument();

    fireEvent.click(within(region).getByRole("button", { name: "Close task pane" }));
    expect(pane()).toBeNull();
    await waitFor(() => expect(screen.queryByRole("region", { name: "Add tests" })).toBeNull());
    expect(screen.getByTestId("conversation")).toBeInTheDocument();
  });

  it("fetches a task the agents list does not hold", async () => {
    useAgents.setState({ sessions: {} });
    api.get.mockImplementation(async (id: string) => session(id, { title: `Task ${id}`, kind: "codex", status: "done" }));
    render(<ChatView />);
    await waitFor(() => expect(chips()[0]).toHaveTextContent("Task t1"));
    expect(api.get.mock.calls.map(([id]) => id).toSorted()).toEqual(["t1", "t2", "t3"]);
  });

  it("opens a written document beside the conversation as markdown, copies it, closes it and switches panes without a remount", async () => {
    const writeText = vi.fn(async () => {});
    Object.assign(navigator, { clipboard: { writeText } });
    useChatMessages.setState({ byBot: { a: { messages: [reply], hasMore: false, loaded: true, loading: false, loadingOlder: false, error: null, browsing: false } } });
    render(<ChatView />);
    fireEvent.click(chips()[0]!);
    const shell = await screen.findByRole("region", { name: "Fix the login bug" });
    const host = shell.closest("[data-slot=side-pane]");
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(pane()).toEqual({ kind: "document", messageId: "m1", partIndex: 4 });
    const region = await screen.findByRole("region", { name: "Rollout plan" });
    expect(region.closest("[data-slot=side-pane]")).toBe(host);
    expect(within(region).getByRole("heading", { name: "Rollout" })).toBeInTheDocument();
    expect(within(region).getByText("Friday").tagName).toBe("STRONG");
    expect(within(region).getByText("v2")).toBeInTheDocument();
    expect(within(region).getByText("Saved")).toBeInTheDocument();
    fireEvent.click(within(region).getByRole("button", { name: "Copy document" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("# Rollout\n\nShip **Friday**."));
    fireEvent.click(within(region).getByRole("button", { name: "Close document" }));
    expect(pane()).toBeNull();
    await waitFor(() => expect(screen.queryByRole("region", { name: "Rollout plan" })).toBeNull());
  });
});

describe("task pane state", () => {
  it("shares the side with the settings panel and belongs to the open conversation", () => {
    const chat = useChat.getState();
    chat.openTaskPane("t1");
    chat.setSettingsOpen(true);
    expect(useChat.getState()).toMatchObject({ settingsOpen: true, pane: null });
    chat.openTaskPane("t1");
    expect(useChat.getState()).toMatchObject({ settingsOpen: false, pane: { kind: "task", id: "t1" } });
    chat.openDocument("m1", 2);
    expect(pane()).toEqual({ kind: "document", messageId: "m1", partIndex: 2 });
    chat.select("a");
    expect(pane()).toEqual({ kind: "document", messageId: "m1", partIndex: 2 });
    chat.select("b");
    expect(pane()).toBeNull();
  });
});

describe("side pane size", () => {
  // jsdom has no layout: give every element a 1000px width so the chat container has room to split.
  beforeEach(() => {
    localStorage.removeItem("orole.chat.paneWidth");
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1000);
  });
  afterEach(() => vi.restoreAllMocks());
  const divider = () => screen.getByRole("separator", { name: "Resize side pane" });
  const paneWidth = () => divider().nextElementSibling!.getAttribute("style");

  it("opens a task in a column of its own sized from the chat width", async () => {
    render(<ChatView />);
    fireEvent.click(chips()[0]!);
    await waitFor(() => expect(divider()).toHaveAttribute("aria-valuenow", "450"));
    expect(paneWidth()).toContain("width: 450px");
    expect(divider()).toHaveAttribute("aria-valuemin", "320");
    expect(divider()).toHaveAttribute("aria-valuemax", "680");
  });

  it("resizes within bounds, remembers the width and resets on double-click", async () => {
    const view = render(<ChatView />);
    fireEvent.click(chips()[0]!);
    await waitFor(() => expect(divider()).toHaveAttribute("aria-valuenow", "450"));
    fireEvent.keyDown(divider(), { key: "ArrowLeft" });
    expect(divider()).toHaveAttribute("aria-valuenow", "482");
    fireEvent.keyDown(divider(), { key: "End" });
    fireEvent.keyDown(divider(), { key: "ArrowLeft" });
    expect(divider()).toHaveAttribute("aria-valuenow", "680");
    expect(localStorage.getItem("orole.chat.paneWidth")).toBe("680");
    view.unmount();
    render(<ChatView />);
    await waitFor(() => expect(divider()).toHaveAttribute("aria-valuenow", "680"));
    fireEvent.doubleClick(divider());
    expect(divider()).toHaveAttribute("aria-valuenow", "450");
    expect(localStorage.getItem("orole.chat.paneWidth")).toBeNull();
  });
});
