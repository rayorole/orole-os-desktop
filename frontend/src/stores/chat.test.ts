import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Bot, BotChange, Message, RunState } from "@/mygo";

const bridge = vi.hoisted(() => ({
  bots: vi.fn(),
  run: null as ((s: RunState) => void) | null,
  message: null as ((m: Message) => void) | null,
  bot: null as ((c: BotChange) => void) | null,
}));

vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/mygo", () => ({
  Chat: { bots: bridge.bots },
  Daemon: { health: vi.fn() },
  events: {
    chatRun: { on: (f: (s: RunState) => void) => { bridge.run = f; return () => { bridge.run = null; }; } },
    chatMessage: { on: (f: (m: Message) => void) => { bridge.message = f; return () => { bridge.message = null; }; } },
    chatMessageDelta: { on: () => () => {} },
    chatBot: { on: (f: (c: BotChange) => void) => { bridge.bot = f; return () => { bridge.bot = null; }; } },
  },
}));

import { useChat, previewOf, sortBots, useSelectedBot } from "@/stores/chat";
import { useDaemon } from "@/stores/daemon";
import { useUI } from "@/stores/ui";
import { act, renderHook } from "@testing-library/react";

const bot = (id: string, over: Partial<Bot> = {}): Bot => ({
  id, name: id, avatar: "", color: "blue", description: "", instructions: "", provider: "", model: "", reasoning: "",
  config: { tools: [], mcpServers: [], skills: [], memory: [], limits: { maxSteps: 0, maxToolCalls: 0 } },
  createdAt: 1, updatedAt: 1, running: false, ...over,
});
const message = (botId: string, text: string, over: Partial<Message> = {}): Message => ({
  id: `m-${botId}-${text}`, botId, seq: 1, role: "assistant", author: `bot:${botId}`, parts: [{ type: "text", text }], status: "complete", createdAt: 100, ...over,
});

let stop: (() => void) | undefined;
/** Lets already-resolved bridge promises run their continuations (no wall-clock waiting). */
const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };

beforeEach(() => {
  stop?.();
  stop = undefined;
  localStorage.clear();
  bridge.bots.mockReset();
  useDaemon.setState({ link: "connected" });
  useChat.setState({ bots: [], selectedBotId: null, running: {}, builder: { open: false, botId: null }, available: true, ready: false, loading: false, error: null });
});

describe("chat store ordering and previews", () => {
  it("sorts by last activity, newest first, then by name", () => {
    const sorted = sortBots([
      bot("quiet-b", { name: "Bravo" }), bot("quiet-a", { name: "Alpha" }),
      bot("old", { name: "Old", lastMessage: { role: "user", text: "x", at: 10 } }),
      bot("new", { name: "New", lastMessage: { role: "user", text: "y", at: 20 } }),
    ]);
    expect(sorted.map((b) => b.id)).toEqual(["new", "old", "quiet-a", "quiet-b"]);
  });

  it("builds one-line previews and ignores messages that show nothing yet", () => {
    expect(previewOf(message("a", "Hello\n\n  there"))?.text).toBe("Hello there");
    expect(previewOf(message("a", "x".repeat(400)))?.text).toHaveLength(160);
    expect(previewOf(message("a", "", { parts: [{ type: "tool", name: "web_search" }] }))).toBeNull();
    expect(previewOf(message("a", "", { role: "user", parts: [{ type: "attachment", attachmentId: "f" }] }))?.text).toBe("Attachment");
    expect(previewOf(message("a", "", { parts: [], status: "error", error: "Provider rejected the key" }))?.text).toBe("Provider rejected the key");
  });
});

describe("chat store sync", () => {
  it("loads sorted bots and mirrors running state", async () => {
    bridge.bots.mockResolvedValue([bot("a", { running: true }), bot("b", { lastMessage: { role: "assistant", text: "hi", at: 5 } })]);
    stop = useChat.getState().startSync();
    await settle();
    const state = useChat.getState();
    expect(state.ready).toBe(true);
    expect(state.bots.map((b) => b.id)).toEqual(["b", "a"]);
    expect(state.running).toEqual({ a: true, b: false });
  });

  it("applies chat.run and chat.message events and re-sorts the rail", async () => {
    bridge.bots.mockResolvedValue([bot("a"), bot("b")]);
    stop = useChat.getState().startSync();
    await settle();
    bridge.run!({ botId: "b", running: true });
    expect(useChat.getState().running.b).toBe(true);
    bridge.message!(message("b", "Working on it", { createdAt: 200 }));
    expect(useChat.getState().bots.map((b) => b.id)).toEqual(["b", "a"]);
    expect(useChat.getState().bots[0]!.lastMessage).toEqual({ role: "assistant", text: "Working on it", at: 200 });
    // A streaming snapshot of a message with nothing to show yet keeps the last preview; builder drafts are ignored.
    bridge.message!(message("b", "", { parts: [{ type: "tool", name: "web_search" }], createdAt: 210 }));
    bridge.message!(message("draft:1", "preview", { createdAt: 300 }));
    expect(useChat.getState().bots[0]!.lastMessage!.text).toBe("Working on it");
    expect(useChat.getState().bots.map((b) => b.id)).not.toContain("draft:1");
    bridge.run!({ botId: "b", running: false });
    expect(useChat.getState().running.b).toBe(false);
  });

  it("keeps live event state when an older list response arrives afterwards", async () => {
    let finish!: (bots: Bot[]) => void;
    bridge.bots.mockImplementation(() => { const { promise, resolve } = Promise.withResolvers<Bot[]>(); finish = resolve; return promise; });
    stop = useChat.getState().startSync();
    bridge.run!({ botId: "a", running: true });
    finish([bot("a", { running: false })]);
    await settle();
    expect(useChat.getState().running.a).toBe(true);
  });

  it("ignores a stale list response after a newer request", async () => {
    const pending: ((bots: Bot[]) => void)[] = [];
    bridge.bots.mockImplementation(() => { const { promise, resolve } = Promise.withResolvers<Bot[]>(); pending.push(resolve); return promise; });
    stop = useChat.getState().startSync();
    void useChat.getState().load();
    pending[1]!([bot("fresh")]);
    await settle();
    pending[0]!([bot("stale")]);
    await settle();
    expect(useChat.getState().bots.map((b) => b.id)).toEqual(["fresh"]);
  });

  it("removes an archived bot at once, clears its selection and closes its builder, then refetches", async () => {
    bridge.bots.mockResolvedValue([bot("a"), bot("b")]);
    stop = useChat.getState().startSync();
    await settle();
    useChat.getState().select("a");
    useChat.getState().openBuilder("a");
    bridge.bots.mockResolvedValue([bot("b")]);
    bridge.bot!({ id: "a", action: "archived" });
    expect(useChat.getState().bots.map((b) => b.id)).toEqual(["b"]);
    expect(useChat.getState().selectedBotId).toBeNull();
    expect(useChat.getState().builder.open).toBe(false);
    expect(localStorage.getItem("orole.chat.selected")).toBeNull();
    await settle();
    expect(bridge.bots).toHaveBeenCalledTimes(2);
  });

  it("drops the preview when a conversation is cleared", async () => {
    bridge.bots.mockResolvedValue([bot("a", { lastMessage: { role: "user", text: "hi", at: 5 } })]);
    stop = useChat.getState().startSync();
    await settle();
    bridge.bots.mockResolvedValue([bot("a")]);
    bridge.bot!({ id: "a", action: "cleared" });
    expect(useChat.getState().bots[0]!.lastMessage).toBeUndefined();
  });

  it("is reference counted: one subscription, torn down with the last subscriber", async () => {
    bridge.bots.mockResolvedValue([]);
    const first = useChat.getState().startSync();
    const second = useChat.getState().startSync();
    await settle();
    expect(bridge.bots).toHaveBeenCalledTimes(1);
    first();
    first();
    expect(bridge.run).not.toBeNull();
    second();
    expect(bridge.run).toBeNull();
    expect(bridge.message).toBeNull();
    expect(bridge.bot).toBeNull();
  });

  it("reports an offline daemon and reloads on reconnect", async () => {
    bridge.bots.mockResolvedValue([bot("a")]);
    stop = useChat.getState().startSync();
    await settle();
    useDaemon.setState({ link: "offline" });
    expect(useChat.getState()).toMatchObject({ ready: false });
    expect(useChat.getState().error).toMatch(/offline/);
    useDaemon.setState({ link: "connected" });
    await settle();
    expect(useChat.getState()).toMatchObject({ ready: true, error: null });
  });

  it("surfaces a load failure", async () => {
    bridge.bots.mockRejectedValue(new Error("boom"));
    await useChat.getState().load();
    expect(useChat.getState()).toMatchObject({ ready: false, loading: false, error: "boom" });
  });

  it("does nothing without a daemon", async () => {
    useChat.setState({ available: false });
    const off = useChat.getState().startSync();
    await useChat.getState().load();
    off();
    expect(bridge.bots).not.toHaveBeenCalled();
    expect(bridge.run).toBeNull();
  });
});

describe("selection", () => {
  it("persists and is cleared when the bot has disappeared", async () => {
    useChat.getState().select("gone");
    expect(localStorage.getItem("orole.chat.selected")).toBe("gone");
    bridge.bots.mockResolvedValue([bot("a")]);
    await useChat.getState().load();
    expect(useChat.getState().selectedBotId).toBeNull();
    expect(localStorage.getItem("orole.chat.selected")).toBeNull();
  });

  it("keeps a selection that still exists and exposes it through useSelectedBot", async () => {
    useChat.getState().select("a");
    bridge.bots.mockResolvedValue([bot("a", { name: "Alpha" })]);
    await useChat.getState().load();
    const { result } = renderHook(() => useSelectedBot());
    expect(result.current?.name).toBe("Alpha");
  });

  it("opens a bot's Computer pane and drops it when another bot is selected", () => {
    useChat.setState({ selectedBotId: "b1", pane: null, settingsOpen: true });
    useChat.getState().openComputerPane("b1");
    expect(useChat.getState()).toMatchObject({ pane: { kind: "computer", id: "b1" }, settingsOpen: false });
    useChat.getState().select("b2");
    expect(useChat.getState().pane).toBeNull();
  });

  it("open() selects the bot and shows the chat view", () => {
    useChat.getState().open("a");
    expect(useChat.getState().selectedBotId).toBe("a");
    expect(useUI.getState()).toMatchObject({ view: "chat" });
  });

  it("opens the builder for a new or an existing bot", () => {
    useChat.getState().openBuilder("new");
    expect(useChat.getState().builder).toEqual({ open: true, botId: null });
    useChat.getState().openBuilder("a");
    expect(useChat.getState().builder).toEqual({ open: true, botId: "a" });
    useChat.getState().closeBuilder();
    expect(useChat.getState().builder).toEqual({ open: false, botId: null });
  });
});

describe("streaming previews", () => {
  const streaming = (botId: string, text: string, at = 100) => message(botId, text, { status: "running", createdAt: at });
  const syncWith = async (...bots: Bot[]) => {
    bridge.bots.mockResolvedValue(bots);
    stop = useChat.getState().startSync();
    await settle();
  };
  afterEach(() => vi.useRealTimers());

  it("applies a streaming snapshot at once when idle, then at most once per second with the newest text", async () => {
    await syncWith(bot("a"), bot("b"));
    vi.useFakeTimers();
    let changes = 0;
    const off = useChat.subscribe((state, previous) => { if (state.bots !== previous.bots) changes++; });
    for (let i = 1; i <= 60; i++) { bridge.message!(streaming("a", `word ${i}`)); vi.advanceTimersByTime(50); }
    expect(changes).toBeLessThanOrEqual(4);
    expect(changes).toBeGreaterThan(1);
    vi.advanceTimersByTime(1000);
    expect(useChat.getState().bots.find((b) => b.id === "a")!.lastMessage!.text).toBe("word 60");
    off();
  });

  it("applies a settled message immediately and keeps untouched bots by reference", async () => {
    await syncWith(bot("a"), bot("b"));
    vi.useFakeTimers();
    const before = useChat.getState().bots.find((b) => b.id === "b");
    bridge.message!(streaming("a", "partial"));
    bridge.message!(streaming("a", "partial more"));
    bridge.message!(message("a", "final answer", { id: "m-a-partial", createdAt: 100 }));
    expect(useChat.getState().bots.find((b) => b.id === "a")!.lastMessage!.text).toBe("final answer");
    expect(useChat.getState().bots.find((b) => b.id === "b")).toBe(before);
    vi.advanceTimersByTime(5000);
    expect(useChat.getState().bots.find((b) => b.id === "a")!.lastMessage!.text).toBe("final answer");
  });

  it("keeps the bots array when a snapshot shows what the rail already has", async () => {
    await syncWith(bot("a"));
    bridge.message!(message("a", "same"));
    const bots = useChat.getState().bots;
    bridge.message!(message("a", "same"));
    expect(useChat.getState().bots).toBe(bots);
  });

  it("drops a pending preview when the bot is archived or the sync stops", async () => {
    await syncWith(bot("a"), bot("b"));
    vi.useFakeTimers();
    bridge.message!(streaming("a", "first"));
    bridge.message!(streaming("a", "second"));
    bridge.bots.mockResolvedValue([bot("b")]);
    bridge.bot!({ id: "a", action: "archived" });
    vi.advanceTimersByTime(2000);
    expect(useChat.getState().bots.map((b) => b.id)).toEqual(["b"]);
  });

  it("useSelectedBot stays the same object while only the preview or run flag changes", async () => {
    await syncWith(bot("a"), bot("b"));
    useChat.getState().select("a");
    const { result } = renderHook(() => useSelectedBot());
    const first = result.current;
    bridge.message!(message("a", "hello"));
    bridge.run!({ botId: "a", running: true });
    expect(useChat.getState().bots.find((b) => b.id === "a")!.lastMessage!.text).toBe("hello");
    expect(result.current).toBe(first);
    bridge.bots.mockResolvedValue([bot("a", { name: "Renamed" }), bot("b")]);
    await act(async () => { await useChat.getState().load(); });
    expect(result.current?.name).toBe("Renamed");
  });
});
