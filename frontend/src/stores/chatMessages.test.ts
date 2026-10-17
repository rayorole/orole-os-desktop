import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { BotChange, Message, MessageDelta, MessagesRequest, RunState, SendRequest } from "@/mygo";

const bridge = vi.hoisted(() => ({
  messages: vi.fn(), send: vi.fn(), cancel: vi.fn(), regenerate: vi.fn(), clear: vi.fn(), botInfo: vi.fn(),
  run: null as ((s: RunState) => void) | null,
  message: null as ((m: Message) => void) | null,
  delta: null as ((d: MessageDelta) => void) | null,
  bot: null as ((c: BotChange) => void) | null,
}));

vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/mygo", () => ({
  Chat: { messages: bridge.messages, send: bridge.send, cancel: bridge.cancel, regenerate: bridge.regenerate, clear: bridge.clear, bot: bridge.botInfo },
  Daemon: { health: vi.fn() },
  events: {
    chatRun: { on: (f: (s: RunState) => void) => { bridge.run = f; return () => { bridge.run = null; }; } },
    chatMessage: { on: (f: (m: Message) => void) => { bridge.message = f; return () => { bridge.message = null; }; } },
    chatMessageDelta: { on: (f: (d: MessageDelta) => void) => { bridge.delta = f; return () => { bridge.delta = null; }; } },
    chatBot: { on: (f: (c: BotChange) => void) => { bridge.bot = f; return () => { bridge.bot = null; }; } },
  },
}));

import { FOCUS_MIN_INTERVAL_MS } from "@/lib/focus";
import { MAX_IN_MEMORY, applyDelta, mergeLatest, upsert, useBotRunning, useChatMessages } from "@/stores/chatMessages";
import { useChat } from "@/stores/chat";
import { useChatDrafts } from "@/stores/chatDrafts";
import { useDaemon } from "@/stores/daemon";

const msg = (seq: number, over: Partial<Message> = {}): Message => ({
  id: `m${seq}`, botId: "a", seq, role: seq % 2 ? "user" : "assistant", author: seq % 2 ? "user" : "bot:a",
  parts: [{ type: "text", text: `t${seq}` }], status: "complete", createdAt: seq * 10, ...over,
});
const page = (from: number, to: number) => Array.from({ length: to - from + 1 }, (_, i) => msg(from + i));
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
};
const settle = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };

let stop: (() => void) | undefined;
const reset = () => { useChatMessages.setState({ byBot: {}, runs: {}, problems: {} }); useChat.setState({ running: {} }); useChatDrafts.setState({ drafts: {} }); };

beforeEach(() => {
  Object.values(bridge).forEach((v) => { if (typeof v === "function" && "mockReset" in v) v.mockReset(); });
  useDaemon.setState({ link: "connected" });
  useChat.setState({ selectedBotId: "a" });
  reset();
  stop = useChatMessages.getState().startSync();
});
afterEach(() => { stop?.(); stop = undefined; });

describe("loading and paging", () => {
  it("loads the latest page and records whether older messages exist", async () => {
    bridge.messages.mockResolvedValue({ messages: page(51, 100), hasMore: true });
    await useChatMessages.getState().load("a");
    expect(bridge.messages).toHaveBeenCalledWith({ botId: "a", beforeSeq: 0, limit: 50 } satisfies MessagesRequest);
    const state = useChatMessages.getState().byBot.a!;
    expect(state.loaded).toBe(true);
    expect(state.hasMore).toBe(true);
    expect(state.messages).toHaveLength(50);
  });

  it("pages older messages before the first seq, de-duplicating, and keeps the window", async () => {
    bridge.messages.mockResolvedValueOnce({ messages: page(51, 100), hasMore: true });
    await useChatMessages.getState().load("a");
    bridge.messages.mockResolvedValueOnce({ messages: page(1, 51), hasMore: false });
    await useChatMessages.getState().loadOlder("a");
    expect(bridge.messages).toHaveBeenLastCalledWith({ botId: "a", beforeSeq: 51, limit: 50 });
    const state = useChatMessages.getState().byBot.a!;
    expect(state.messages.map((m) => m.seq)).toEqual(Array.from({ length: 100 }, (_, i) => i + 1));
    expect(state.hasMore).toBe(false);
    expect(state.browsing).toBe(true);
  });

  it("does not page when nothing older exists or a page is already loading", async () => {
    bridge.messages.mockResolvedValue({ messages: page(1, 3), hasMore: false });
    await useChatMessages.getState().load("a");
    await useChatMessages.getState().loadOlder("a");
    expect(bridge.messages).toHaveBeenCalledTimes(1);
  });

  it("reports a load failure without the daemon's machine prefix", async () => {
    bridge.messages.mockRejectedValue(new Error("internal: boom"));
    await useChatMessages.getState().load("a");
    expect(useChatMessages.getState().byBot.a).toMatchObject({ loaded: false, loading: false, error: "boom" });
  });

  it("drops a stale response: only the newest request lands", async () => {
    const first = deferred<{ messages: Message[]; hasMore: boolean }>();
    const second = deferred<{ messages: Message[]; hasMore: boolean }>();
    bridge.messages.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    const a = useChatMessages.getState().load("a");
    const b = useChatMessages.getState().load("a");
    second.resolve({ messages: [msg(2)], hasMore: false });
    await b;
    first.resolve({ messages: [msg(1)], hasMore: false });
    await a;
    expect(useChatMessages.getState().byBot.a!.messages.map((m) => m.id)).toEqual(["m2"]);
  });

  it("never loads a draft: conversation", async () => {
    await useChatMessages.getState().load("draft:preview");
    expect(bridge.messages).not.toHaveBeenCalled();
  });
});

describe("event sync", () => {
  beforeEach(async () => {
    bridge.messages.mockResolvedValue({ messages: [msg(1), msg(2)], hasMore: false });
    await useChatMessages.getState().load("a");
  });

  it("appends a new message once and replaces a streaming snapshot in place", () => {
    bridge.message!(msg(3, { status: "running", parts: [{ type: "text", text: "Hel" }] }));
    bridge.message!(msg(3, { status: "running", parts: [{ type: "text", text: "Hello" }] }));
    bridge.message!(msg(3, { status: "complete", parts: [{ type: "text", text: "Hello there" }] }));
    const list = useChatMessages.getState().byBot.a!.messages;
    expect(list.map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
    expect(list[2]).toMatchObject({ status: "complete", parts: [{ text: "Hello there" }] });
  });

  it("appends streamed deltas to the running message, in order, and a snapshot replaces the accumulated text", () => {
    const delta = (from: number, text: string): MessageDelta => ({ botId: "a", messageId: "m3", part: 0, type: "text", from, text });
    bridge.message!(msg(3, { status: "running", parts: [{ type: "text", text: "Hel" }] }));
    bridge.delta!(delta(3, "lo"));
    bridge.delta!(delta(5, " there"));
    expect(useChatMessages.getState().byBot.a!.messages[2]!.parts[0]!.text).toBe("Hello there");
    bridge.message!(msg(3, { status: "complete", parts: [{ type: "text", text: "Hello there!" }] }));
    expect(useChatMessages.getState().byBot.a!.messages[2]).toMatchObject({ status: "complete", parts: [{ text: "Hello there!" }] });
  });

  it("drops a delta that does not line up and recovers on the next snapshot", () => {
    bridge.message!(msg(3, { status: "running", parts: [{ type: "text", text: "Hel" }] }));
    bridge.delta!({ botId: "a", messageId: "m3", part: 0, type: "text", from: 10, text: "lost the middle" }); // missed an event
    bridge.delta!({ botId: "a", messageId: "m3", part: 0, type: "text", from: 0, text: "dup" }); // replayed
    bridge.delta!({ botId: "a", messageId: "m3", part: 4, type: "text", from: 3, text: "no such part" });
    bridge.delta!({ botId: "a", messageId: "m3", part: 0, type: "reasoning", from: 3, text: "wrong kind" });
    bridge.delta!({ botId: "a", messageId: "ghost", part: 0, type: "text", from: 0, text: "unknown message" });
    expect(useChatMessages.getState().byBot.a!.messages[2]!.parts).toEqual([{ type: "text", text: "Hel" }]);
    bridge.message!(msg(3, { status: "running", parts: [{ type: "text", text: "Hello wor" }] }));
    bridge.delta!({ botId: "a", messageId: "m3", part: 0, type: "text", from: 9, text: "ld" });
    expect(useChatMessages.getState().byBot.a!.messages[2]!.parts[0]!.text).toBe("Hello world");
  });

  it("ignores deltas for bots never loaded and for draft: conversations", () => {
    bridge.delta!({ botId: "never", messageId: "m3", part: 0, type: "text", from: 0, text: "x" });
    bridge.delta!({ botId: "draft:1", messageId: "m3", part: 0, type: "text", from: 0, text: "x" });
    expect(Object.keys(useChatMessages.getState().byBot)).toEqual(["a"]);
  });

  it("counts the offset in UTF-16 units, as JavaScript strings do", () => {
    const base = msg(3, { status: "running", parts: [{ type: "text", text: "a😀" }] }); // 3 UTF-16 units, 5 UTF-8 bytes
    expect(applyDelta(base, { botId: "a", messageId: "m3", part: 0, type: "text", from: 3, text: "!" })?.parts[0]?.text).toBe("a😀!");
    expect(applyDelta(base, { botId: "a", messageId: "m3", part: 0, type: "text", from: 5, text: "!" })).toBeNull();
  });

  it("ignores events for bots never loaded and for draft: conversations", () => {
    bridge.message!(msg(1, { id: "x", botId: "never" }));
    bridge.message!(msg(1, { id: "d", botId: "draft:1" }));
    expect(Object.keys(useChatMessages.getState().byBot)).toEqual(["a"]);
  });

  it("tracks run state from chat.run events", () => {
    bridge.run!({ botId: "a", running: true });
    expect(useChatMessages.getState().runs.a).toBe(true);
    bridge.run!({ botId: "a", running: false });
    expect(useChatMessages.getState().runs.a).toBe(false);
  });

  it("empties the window when the conversation is cleared and forgets an archived bot and its draft", () => {
    useChatDrafts.getState().setDraft("a", { text: "unsent" });
    bridge.bot!({ id: "a", action: "cleared" });
    expect(useChatMessages.getState().byBot.a).toMatchObject({ messages: [], loaded: true });
    expect(useChatDrafts.getState().drafts.a?.text).toBe("unsent");
    bridge.bot!({ id: "a", action: "archived" });
    expect(useChatMessages.getState().byBot.a).toBeUndefined();
    expect(useChatDrafts.getState().drafts.a).toBeUndefined();
  });

  it("keeps at most MAX_IN_MEMORY messages while following the live end", () => {
    for (let seq = 3; seq < MAX_IN_MEMORY + 20; seq++) bridge.message!(msg(seq));
    const state = useChatMessages.getState().byBot.a!;
    expect(state.messages).toHaveLength(MAX_IN_MEMORY);
    expect(state.messages.at(-1)!.seq).toBe(MAX_IN_MEMORY + 19);
    expect(state.hasMore).toBe(true);
  });
});

describe("reconnect and focus refresh", () => {
  it("refetches the latest page when the daemon reconnects and keeps what events delivered meanwhile", async () => {
    bridge.messages.mockResolvedValueOnce({ messages: [msg(1), msg(2)], hasMore: false });
    await useChatMessages.getState().load("a");
    useDaemon.setState({ link: "offline" });
    const refetch = deferred<{ messages: Message[]; hasMore: boolean }>();
    bridge.messages.mockReturnValueOnce(refetch.promise);
    useDaemon.setState({ link: "connected" });
    // An event arrives while the (older) snapshot is still in flight.
    bridge.message!(msg(4, { parts: [{ type: "text", text: "live" }] }));
    refetch.resolve({ messages: [msg(1), msg(2), msg(3), msg(4, { parts: [{ type: "text", text: "old snapshot" }] })], hasMore: false });
    await settle();
    const list = useChatMessages.getState().byBot.a!.messages;
    expect(list.map((m) => m.id)).toEqual(["m1", "m2", "m3", "m4"]);
    expect(list[3]!.parts[0]!.text).toBe("live");
  });

  describe("window focus", () => {
    const refocus = () => { vi.useFakeTimers({ toFake: ["Date"] }); vi.setSystemTime(Date.now() + FOCUS_MIN_INTERVAL_MS); window.dispatchEvent(new Event("focus")); };
    afterEach(() => { vi.useRealTimers(); });

    it("refetches the open conversation once however many focus events arrive", async () => {
      bridge.messages.mockResolvedValue({ messages: [msg(1)], hasMore: false });
      await useChatMessages.getState().load("a");
      bridge.messages.mockClear();
      refocus();
      for (let i = 0; i < 9; i++) window.dispatchEvent(new Event("focus"));
      await settle();
      expect(bridge.messages).toHaveBeenCalledTimes(1);
      expect(bridge.messages).toHaveBeenCalledWith({ botId: "a", beforeSeq: 0, limit: 50 } satisfies MessagesRequest);
    });

    it("leaves every other loaded conversation alone", async () => {
      bridge.messages.mockImplementation(async ({ botId }: MessagesRequest) => ({ messages: [msg(1, { botId })], hasMore: false }));
      for (const botId of ["a", "b", "c", "d"]) await useChatMessages.getState().load(botId);
      bridge.messages.mockClear();
      refocus();
      await settle();
      expect(bridge.messages.mock.calls.map(([request]) => request.botId)).toEqual(["a"]);
      bridge.messages.mockClear();
      useChat.setState({ selectedBotId: "c" });
      vi.setSystemTime(Date.now() + FOCUS_MIN_INTERVAL_MS);
      window.dispatchEvent(new Event("focus"));
      await settle();
      expect(bridge.messages.mock.calls.map(([request]) => request.botId)).toEqual(["c"]);
    });

    it("fetches nothing when no conversation is open or the open one was never loaded", async () => {
      useChat.setState({ selectedBotId: null });
      refocus();
      await settle();
      expect(bridge.messages).not.toHaveBeenCalled();
      useChat.setState({ selectedBotId: "a" });
      vi.setSystemTime(Date.now() + FOCUS_MIN_INTERVAL_MS);
      window.dispatchEvent(new Event("focus"));
      await settle();
      expect(bridge.messages).not.toHaveBeenCalled();
    });
  });

  it("mergeLatest keeps older history the page does not cover", () => {
    const current = { messages: [msg(1), msg(2), msg(3)], hasMore: true, loaded: true, loading: false, loadingOlder: false, error: null, browsing: true };
    const merged = mergeLatest(current, [msg(3), msg(4)], true, new Map());
    expect(merged.messages.map((m) => m.seq)).toEqual([1, 2, 3, 4]);
  });
});

describe("sending, cancelling, regenerating", () => {
  beforeEach(async () => {
    bridge.messages.mockResolvedValue({ messages: [msg(1), msg(2)], hasMore: false });
    await useChatMessages.getState().load("a");
  });

  it("sends once with an idempotency nonce and upserts the stored user message, inventing nothing", async () => {
    bridge.send.mockImplementation(async (request: SendRequest) => msg(3, { parts: [{ type: "text", text: request.text }] }));
    expect(await useChatMessages.getState().send("a", { text: "hi", attachmentIds: ["f1"], mode: "research" })).toBe(true);
    const request: SendRequest = bridge.send.mock.calls[0]![0];
    expect(request).toMatchObject({ botId: "a", text: "hi", attachmentIds: ["f1"], mode: "research" });
    expect(request.clientNonce).toMatch(/\S/);
    expect(useChatMessages.getState().byBot.a!.messages.map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
  });

  it("keeps a failed send's exact request for an idempotent retry", async () => {
    bridge.send.mockRejectedValueOnce(new Error("bad_request: nope")).mockResolvedValueOnce(msg(3));
    expect(await useChatMessages.getState().send("a", { text: "hi" })).toBe(false);
    const problem = useChatMessages.getState().problems.a!;
    expect(problem.message).toBe("Message not sent: nope");
    expect(await useChatMessages.getState().retrySend("a")).toBe(true);
    expect(bridge.send.mock.calls[1]![0].clientNonce).toBe(problem.request!.clientNonce);
    expect(useChatMessages.getState().problems.a).toBeUndefined();
  });

  it("cancels and regenerates through the daemon and reports failures", async () => {
    await useChatMessages.getState().cancel("a");
    expect(bridge.cancel).toHaveBeenCalledWith("a");
    await useChatMessages.getState().regenerate("a", "m2");
    expect(bridge.regenerate).toHaveBeenCalledWith({ botId: "a", messageId: "m2" });
    bridge.regenerate.mockRejectedValueOnce(new Error("bad_request: not the last"));
    await useChatMessages.getState().regenerate("a", "m1");
    expect(useChatMessages.getState().problems.a!.message).toBe("Could not regenerate the reply: not the last");
  });

  it("asks the daemon after Stop, so a run it no longer has stops showing as running", async () => {
    bridge.run!({ botId: "a", running: true });
    useChat.setState({ running: { a: true } });
    bridge.botInfo.mockResolvedValue({ id: "a", running: false });
    await useChatMessages.getState().cancel("a");
    expect(bridge.botInfo).toHaveBeenCalledWith("a");
    expect(useChatMessages.getState().runs.a).toBe(false);
    expect(useChat.getState().running.a).toBe(false);
  });

  it("keeps a run event that arrives while the daemon is being asked after Stop", async () => {
    const answer = deferred<{ id: string; running: boolean }>();
    bridge.botInfo.mockReturnValue(answer.promise);
    const stopping = useChatMessages.getState().cancel("a");
    await settle();
    bridge.run!({ botId: "a", running: true });
    answer.resolve({ id: "a", running: false });
    await stopping;
    expect(useChatMessages.getState().runs.a).toBe(true);
  });

  it("survives the daemon not answering after Stop", async () => {
    bridge.run!({ botId: "a", running: true });
    bridge.botInfo.mockRejectedValue(new Error("daemon unavailable"));
    await expect(useChatMessages.getState().cancel("a")).resolves.toBeUndefined();
    expect(useChatMessages.getState().runs.a).toBe(true);
  });

  it("clears the window after the daemon clears the conversation", async () => {
    bridge.clear.mockResolvedValue(undefined);
    expect(await useChatMessages.getState().clear("a")).toBe(true);
    expect(useChatMessages.getState().byBot.a).toMatchObject({ messages: [], loaded: true });
  });
});

describe("upsert", () => {
  it("re-sorts only when a replacement moves", () => {
    const list = [msg(1), msg(2), msg(3)];
    expect(upsert(list, msg(2, { status: "running" })).map((m) => m.id)).toEqual(["m1", "m2", "m3"]);
    expect(upsert(list, msg(0, { id: "m9" })).map((m) => m.id)).toEqual(["m9", "m1", "m2", "m3"]);
  });
});

describe("stale run state", () => {
  it("forgets run events once nothing listens, so a run that ends meanwhile is not shown as running when the chat returns", () => {
    bridge.run!({ botId: "a", running: true });
    useChat.setState({ running: { a: true } });
    const { result } = renderHook(() => useBotRunning("a"));
    expect(result.current).toBe(true);
    stop!();
    stop = undefined;
    // The finish arrives while the conversation is not mounted: only the always-synced rail hears it.
    useChat.setState({ running: { a: false } });
    expect(useChatMessages.getState().runs).toEqual({});
    stop = useChatMessages.getState().startSync();
    expect(renderHook(() => useBotRunning("a")).result.current).toBe(false);
  });

  it("forgets run events when the daemon link drops, and keeps listening after it returns", () => {
    bridge.run!({ botId: "a", running: true });
    useDaemon.setState({ link: "offline" });
    expect(useChatMessages.getState().runs).toEqual({});
    useDaemon.setState({ link: "connected" });
    bridge.run!({ botId: "a", running: true });
    expect(useChatMessages.getState().runs.a).toBe(true);
  });
});

describe("useBotRunning", () => {
  it("prefers the daemon's run event, then the rail snapshot, then a streaming message", () => {
    useChat.setState({ running: { a: true } });
    const { result, rerender } = renderHook(() => useBotRunning("a"));
    expect(result.current).toBe(true);
    useChatMessages.setState({ runs: { a: false } });
    rerender();
    expect(result.current).toBe(false);
    useChatMessages.setState({ runs: {}, byBot: { a: { messages: [msg(2, { status: "running" })], hasMore: false, loaded: true, loading: false, loadingOlder: false, error: null, browsing: false } } });
    useChat.setState({ running: {} });
    rerender();
    expect(result.current).toBe(true);
  });

  it("does not let a stale streaming message override the daemon reporting the bot idle", () => {
    useChatMessages.setState({ runs: {}, byBot: { a: { messages: [msg(2, { status: "running" })], hasMore: false, loaded: true, loading: false, loadingOlder: false, error: null, browsing: false } } });
    useChat.setState({ running: { a: false } });
    expect(renderHook(() => useBotRunning("a")).result.current).toBe(false);
  });
});
