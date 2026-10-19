import { act, cleanup, createEvent, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Bot, BotChange, Message, RunState, SendRequest } from "@/mygo";

const bridge = vi.hoisted(() => ({
  messages: vi.fn(), send: vi.fn(), cancel: vi.fn(), regenerate: vi.fn(), clear: vi.fn(), exportBot: vi.fn(), botInfo: vi.fn(),
  pauseQueue: vi.fn(), resumeQueue: vi.fn(), cancelQueued: vi.fn(),
  begin: vi.fn(), chunk: vi.fn(), commit: vi.fn(), discard: vi.fn(),
  run: null as ((s: RunState) => void) | null,
  message: null as ((m: Message) => void) | null,
}));

vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/mygo", () => ({
  Chat: {
    messages: bridge.messages, send: bridge.send, cancel: bridge.cancel, regenerate: bridge.regenerate, clear: bridge.clear, exportBot: bridge.exportBot, bot: bridge.botInfo,
    pauseQueue: bridge.pauseQueue, resumeQueue: bridge.resumeQueue, cancelQueued: bridge.cancelQueued,
    beginAttachment: bridge.begin, attachmentChunk: bridge.chunk, commitAttachment: bridge.commit, discardAttachment: bridge.discard,
  },
  LLM: { models: vi.fn() },
  Skills: { list: () => Promise.resolve([{ name: "pdf", description: "Read PDFs", enabled: true }, { name: "skill-creator", description: "Make skills", enabled: true, source: "builtin" }]) },
  MCP: { list: () => Promise.resolve([{ id: "srv-canvas", name: "Canvas LMS", url: "https://canvas.test/mcp", enabled: true, status: "connected", toolCount: 12 }, { id: "srv-other", name: "Other", url: "https://other.test", enabled: true, status: "connected", toolCount: 1 }]) },
  Daemon: { health: vi.fn() },
  Policy: { pending: () => Promise.resolve([]), resolve: vi.fn(), addRule: vi.fn() },
  Shell: { openExternal: vi.fn() },
  events: {
    chatRun: { on: (f: (s: RunState) => void) => { bridge.run = f; return () => { bridge.run = null; }; } },
    chatMessage: { on: (f: (m: Message) => void) => { bridge.message = f; return () => { bridge.message = null; }; } },
    chatMessageDelta: { on: () => () => {} },
    chatBot: { on: (_f: (c: BotChange) => void) => () => {} },
    policyPending: { on: () => () => {} },
    daemonState: { on: () => () => {} },
  },
}));

// The real control records audio; its own tests live in tasks/ComposerVoice.test.tsx. This one says "hello there" when Stop is pressed.
vi.mock("@/features/tasks/ComposerVoice", () => {
  return {
    ComposerVoice: ({ onTranscript, onActiveChange }: { onTranscript: (text: string) => void; onActiveChange: (active: boolean) => void }) => {
      const [recording, setRecording] = useState(false);
      return recording
        ? <button type="button" onClick={() => { setRecording(false); onTranscript("hello there"); onActiveChange(false); }}>Stop dictation</button>
        : <button type="button" onClick={() => { setRecording(true); onActiveChange(true); }}>Dictate a message</button>;
    },
  };
});

import { BotActions } from "@/features/chat/BotHeader";
import { Conversation } from "@/features/chat/Conversation";
import { useChat } from "@/stores/chat";
import { useChatDrafts } from "@/stores/chatDrafts";
import { useChatMessages } from "@/stores/chatMessages";
import { useDaemon } from "@/stores/daemon";
import { useLLM } from "@/stores/llm";

const bot = (over: Partial<Bot> = {}): Bot => ({
  id: "a", name: "Atlas", avatar: "", color: "blue", description: "Plans trips", instructions: "", provider: "openrouter", model: "claude", reasoning: "",
  config: { tools: [], mcpServers: [], skills: [], memory: [], limits: { maxSteps: 0, maxToolCalls: 0 } },
  createdAt: 1, updatedAt: 1, running: false, ...over,
});
const withTools = (...ids: string[]) => bot({ config: { ...bot().config, tools: ids.map((id) => ({ id })) } });
const msg = (seq: number, over: Partial<Message> = {}): Message => ({
  id: `m${seq}`, botId: "a", seq, role: seq % 2 ? "user" : "assistant", author: seq % 2 ? "user" : "bot:a",
  parts: [{ type: "text", text: `${seq % 2 ? "question" : "answer"} ${seq}` }], status: "complete", createdAt: seq * 10, ...over,
});
const caps = { tools: true, vision: false, pdf: true, reasoning: false, hostedTools: [], deepResearch: false, contextWindow: 0 };

let stubs: Array<() => void> = [];
beforeEach(() => {
  Object.values(bridge).forEach((v) => { if (typeof v === "function" && "mockReset" in v) v.mockReset(); });
  stubs.push(() => vi.unstubAllGlobals());
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  useDaemon.setState({ link: "connected" });
  useChat.setState({ bots: [bot()], selectedBotId: "a", running: {}, builder: { open: false, botId: null }, settingsOpen: false, available: true, ready: true, loading: false, error: null });
  useChatMessages.setState({ byBot: {}, runs: {}, paused: {}, problems: {} });
  useChatDrafts.setState({ drafts: {} });
  Element.prototype.scrollTo = () => {};
  Element.prototype.scrollIntoView = () => {};
  useLLM.setState({
    providers: [{ id: "openrouter", name: "OpenRouter", kind: "builtin", connected: true }], ready: true, defaults: { chatModel: "openrouter/claude", memoryModel: "" },
    models: { openrouter: { models: [{ id: "claude", name: "Claude Sonnet", caps, reasoning: [] }], loading: false, error: null } },
  });
  bridge.messages.mockResolvedValue({ messages: [], hasMore: false });
  bridge.cancel.mockResolvedValue(undefined);
  bridge.regenerate.mockResolvedValue(undefined);
});
afterEach(() => { cleanup(); stubs.forEach((f) => f()); stubs = []; });

const open = async (messages: Message[] = [], b: Bot = bot(), hasMore = false) => {
  bridge.messages.mockResolvedValue({ messages, hasMore });
  const view = render(<><BotActions /><Conversation key={b.id} bot={b} /></>);
  if (messages.length) await screen.findAllByText(/answer|question/);
  else await screen.findByText(/Say hello/);
  return view;
};
const box = () => screen.getByRole("combobox", { name: "Message Atlas" }) as HTMLTextAreaElement;
const type = (text: string) => fireEvent.change(box(), { target: { value: text } });

describe("conversation rendering", () => {
  it("shows the bot and a plain prompt when there are no messages", async () => {
    await open();
    const empty = document.querySelector("[data-slot=conversation-empty]") as HTMLElement;
    expect(within(empty).getByText("Atlas")).toBeInTheDocument();
    expect(within(empty).queryByText("Plans trips")).toBeNull();
    expect(within(empty).getByText("Say hello to start the conversation.")).toBeInTheDocument();
  });

  it("does not draw a code task's report, only the bot's reply to it", async () => {
    await open([msg(1), msg(2), msg(3, { author: "task:t-42", parts: [{ type: "text", text: "Task \"Fix login\" finished." }] }), msg(4, { parts: [{ type: "text", text: "The login fix is done." }] })]);
    expect(screen.getByText("The login fix is done.")).toBeInTheDocument();
    expect(screen.queryByText(/Fix login/)).toBeNull();
  });

  it("renders the stored conversation with accessible message labels", async () => {
    await open([msg(1), msg(2)]);
    expect(screen.getByRole("region", { name: "Conversation with Atlas" })).toBeInTheDocument();
    expect(screen.getByText("question 1")).toBeInTheDocument();
    expect(screen.getByText("answer 2")).toBeInTheDocument();
    expect(screen.getByText("You said:")).toBeInTheDocument();
    expect(screen.getByText("Atlas said:")).toBeInTheDocument();
  });

  it("lists a waiting message in the queue above the composer instead of the thread", async () => {
    await open([msg(1), msg(2), msg(3, { status: "pending" })]);
    const queue = screen.getByRole("region", { name: "Queued messages" });
    expect(within(queue).getByText("question 3")).toBeInTheDocument();
    expect(screen.getAllByText("question 3")).toHaveLength(1);
    expect(screen.getByText("question 1")).toBeInTheDocument();
  });

  it("cancels one waiting message, and pauses and resumes the queue", async () => {
    bridge.cancelQueued.mockResolvedValue(undefined);
    bridge.pauseQueue.mockResolvedValue(undefined);
    bridge.resumeQueue.mockResolvedValue(undefined);
    await open([msg(1), msg(2), msg(3, { status: "pending" }), msg(5, { status: "pending", parts: [{ type: "text", text: "question 5" }] })]);
    const queue = screen.getByRole("region", { name: "Queued messages" });
    fireEvent.click(within(queue).getByRole("button", { name: "Cancel queued message: question 5" }));
    await waitFor(() => expect(bridge.cancelQueued).toHaveBeenCalledWith({ botId: "a", messageId: "m5" }));

    fireEvent.click(within(queue).getByRole("button", { name: "Pause" }));
    await waitFor(() => expect(bridge.pauseQueue).toHaveBeenCalledWith("a"));
    act(() => bridge.run!({ botId: "a", running: false, paused: true }));
    expect(await within(queue).findByText(/Paused/)).toBeInTheDocument();
    fireEvent.click(within(queue).getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(bridge.resumeQueue).toHaveBeenCalledWith("a"));
  });

  it("replaces a streaming reply in place as snapshots arrive", async () => {
    await open([msg(1), msg(2, { status: "running", parts: [{ type: "text", text: "Hel" }] })]);
    act(() => bridge.message!(msg(2, { status: "running", parts: [{ type: "text", text: "Hello wor" }] })));
    expect(await screen.findByText("Hello wor")).toBeInTheDocument();
    act(() => bridge.message!(msg(2, { status: "complete", parts: [{ type: "text", text: "Hello world" }] })));
    expect(await screen.findByText("Hello world")).toBeInTheDocument();
    expect(screen.queryByText("Hello wor")).toBeNull();
    expect(screen.getAllByText(/Atlas said:/)).toHaveLength(1);
  });

  it("announces Responding and Done through a polite live region", async () => {
    await open([msg(1), msg(2)]);
    const live = screen.getAllByRole("status").find((el) => el.getAttribute("aria-live") === "polite")!;
    expect(live).toHaveTextContent("");
    act(() => bridge.run!({ botId: "a", running: true }));
    await waitFor(() => expect(live).toHaveTextContent("Responding"));
    act(() => bridge.run!({ botId: "a", running: false }));
    await waitFor(() => expect(live).toHaveTextContent("Done"));
  });

  it("offers Load earlier messages when older history exists and pages by the first seq", async () => {
    await open([msg(3), msg(4)], bot(), true);
    bridge.messages.mockResolvedValueOnce({ messages: [msg(1), msg(2)], hasMore: false });
    fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
    expect(await screen.findByText("question 1")).toBeInTheDocument();
    expect(bridge.messages).toHaveBeenLastCalledWith({ botId: "a", beforeSeq: 3, limit: 50 });
    expect(screen.queryByRole("button", { name: "Load earlier messages" })).toBeNull();
  });

  it("shows a load failure with Retry", async () => {
    bridge.messages.mockRejectedValueOnce(new Error("internal: daemon down"));
    render(<Conversation bot={bot()} />);
    expect(await screen.findByText(/Could not load this conversation: daemon down/)).toBeInTheDocument();
    bridge.messages.mockResolvedValueOnce({ messages: [msg(1), msg(2)], hasMore: false });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("answer 2")).toBeInTheDocument();
  });
});

describe("message actions", () => {
  it("offers Regenerate only on the last reply and wires it to the daemon", async () => {
    await open([msg(1), msg(2), msg(3), msg(4)]);
    const regenerate = screen.getAllByRole("button", { name: "Regenerate response" });
    expect(regenerate).toHaveLength(1);
    fireEvent.click(regenerate[0]!);
    await waitFor(() => expect(bridge.regenerate).toHaveBeenCalledWith({ botId: "a", messageId: "m4" }));
  });

  it("shows a failed reply with its error and a Retry that regenerates it", async () => {
    await open([msg(1), msg(2, { status: "error", error: "Invalid API key", parts: [] })]);
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid API key");
    fireEvent.click(within(screen.getByRole("alert")).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(bridge.regenerate).toHaveBeenCalledWith({ botId: "a", messageId: "m2" }));
  });

  it("offers no actions on a phone call card, even as the last message", async () => {
    await open([msg(1), msg(2), msg(4, { parts: [{ type: "call", id: "c1", name: "outbound", status: "completed" }] })]);
    expect(await screen.findByRole("button", { name: /Called you/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Regenerate response" })).toBeNull();
    // Only the typed reply before it can be copied.
    expect(screen.getAllByRole("button", { name: "Copy response" })).toHaveLength(1);
  });

  it("hides actions on a reply that is still streaming", async () => {
    await open([msg(1), msg(2, { status: "running", parts: [{ type: "text", text: "so far" }] })]);
    expect(screen.queryByRole("button", { name: "Copy response" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Regenerate response" })).toBeNull();
  });
});

describe("composer", () => {
  it("sends with Enter, keeps a newline for Shift+Enter, and clears afterwards", async () => {
    bridge.send.mockImplementation(async (r: SendRequest) => msg(3, { parts: [{ type: "text", text: r.text }] }));
    await open([msg(1), msg(2)]);
    expect(screen.queryByRole("button", { name: "Send message" })).toBeNull();
    type("hello");
    const send = screen.getByRole("button", { name: "Send message" });
    expect(send).toBeEnabled();
    fireEvent.keyDown(box(), { key: "Enter", shiftKey: true });
    expect(bridge.send).not.toHaveBeenCalled();
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ botId: "a", text: "hello", attachmentIds: [], mode: "" });
    expect(bridge.send.mock.calls[0]![0].clientNonce).toMatch(/\S/);
    await waitFor(() => expect(box()).toHaveValue(""));
    expect(await screen.findByText("hello")).toBeInTheDocument();
    expect(useChatDrafts.getState().drafts.a?.text).toBe("");
  });

  it("keeps Dictate beside Send whatever the draft holds, and never sends whitespace", async () => {
    await open([msg(1), msg(2)]);
    expect(screen.getByRole("button", { name: "Dictate a message" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send message" })).toBeNull();
    type("   ");
    fireEvent.keyDown(box(), { key: "Enter" });
    expect(bridge.send).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Dictate a message" })).toBeInTheDocument();
    type("hi");
    expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Dictate a message" })).toBeInTheDocument();
  });

  it("adds dictated text to what is already typed instead of replacing it, with Send held back while recording", async () => {
    await open([msg(1), msg(2)]);
    type("Hello");
    fireEvent.click(screen.getByRole("button", { name: "Dictate a message" }));
    expect(screen.queryByRole("button", { name: "Send message" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Stop dictation" }));
    await waitFor(() => expect(box()).toHaveValue("Hello hello there"));
    expect(useChatDrafts.getState().drafts.a?.text).toBe("Hello hello there");
    expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Dictate a message" })).toBeInTheDocument();
  });

  it("keeps a recording that is under way when a reply starts, and offers Queue once it ends", async () => {
    await open([msg(1), msg(2)]);
    type("Hello");
    fireEvent.click(screen.getByRole("button", { name: "Dictate a message" }));
    act(() => bridge.run!({ botId: "a", running: true }));
    expect(await screen.findByRole("button", { name: "Stop" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop dictation" }));
    await waitFor(() => expect(box()).toHaveValue("Hello hello there"));
    expect(screen.getByRole("button", { name: "Queue message" })).toBeEnabled();
  });

  it("does not submit while an IME composition is active", async () => {
    await open([msg(1), msg(2)]);
    type("こんにちは");
    fireEvent.keyDown(box(), { key: "Enter", isComposing: true });
    expect(bridge.send).not.toHaveBeenCalled();
  });

  it("offers Stop and Queue while a reply runs, and Enter queues a message", async () => {
    bridge.send.mockResolvedValue(msg(3, { status: "pending" }));
    await open([msg(1), msg(2, { status: "running", parts: [{ type: "text", text: "thinking" }] })]);
    act(() => bridge.run!({ botId: "a", running: true }));
    expect(await screen.findByRole("button", { name: "Stop" })).toBeInTheDocument();
    type("one more thing");
    expect(await screen.findByRole("button", { name: "Queue message" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Dictate a message" })).toBeNull();
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(within(await screen.findByRole("region", { name: "Queued messages" })).getByText("question 3")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    await waitFor(() => expect(bridge.cancel).toHaveBeenCalledWith("a"));
  });

  it("does not show a reply that finished while the conversation was closed as still running", async () => {
    const view = await open([msg(1), msg(2)]);
    act(() => bridge.run!({ botId: "a", running: true }));
    useChat.setState({ running: { a: true } });
    expect(await screen.findByRole("button", { name: "Stop" })).toBeInTheDocument();
    view.unmount();
    // The finish arrives while nothing listens to this conversation; only the always-synced rail hears it.
    useChat.setState({ running: { a: false } });
    await open([msg(1), msg(2)]);
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    type("next");
    expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
  });

  it("recovers from a wrong running state when Stop finds the daemon idle", async () => {
    await open([msg(1), msg(2)]);
    act(() => bridge.run!({ botId: "a", running: true }));
    bridge.botInfo.mockResolvedValue(bot({ running: false }));
    fireEvent.click(await screen.findByRole("button", { name: "Stop" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Stop" })).toBeNull());
    expect(bridge.cancel).toHaveBeenCalledWith("a");
    type("hello");
    expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
  });

  it("shows text put in the draft from elsewhere in the message box", async () => {
    await open([msg(1), msg(2)]);
    act(() => useChatDrafts.getState().setDraft("a", { text: "> quoted\n\n" }));
    await waitFor(() => expect(box().value).toBe("> quoted\n\n"));
  });

  it("keeps each bot's draft while another is open", async () => {
    const first = await open([msg(1), msg(2)]);
    type("half-written");
    first.unmount();
    useChat.setState({ bots: [bot(), bot({ id: "b", name: "Beta" })] });
    bridge.messages.mockResolvedValue({ messages: [], hasMore: false });
    const other = render(<Conversation key="b" bot={bot({ id: "b", name: "Beta" })} />);
    await screen.findByText(/Say hello/);
    expect(screen.getByRole("combobox", { name: "Message Beta" })).toHaveValue("");
    other.unmount();
    render(<Conversation key="a" bot={bot()} />);
    await waitFor(() => expect(box()).toHaveValue("half-written"));
  });

  it("offers Research mode only when the research tool is on, sends mode research once and shows a pill", async () => {
    bridge.send.mockImplementation(async (r: SendRequest) => msg(3, { parts: [{ type: "text", text: r.text }] }));
    const view = await open([msg(1), msg(2)], withTools("web_search"));
    fireEvent.click(screen.getByRole("button", { name: "Add files and research" }));
    expect(screen.queryByRole("menuitemcheckbox", { name: "Research mode" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Web search" })).toBeNull();
    view.unmount();
    cleanup();

    await open([msg(1), msg(2)], withTools("research"));
    fireEvent.click(screen.getByRole("button", { name: "Add files and research" }));
    fireEvent.click(await screen.findByRole("menuitemcheckbox", { name: "Research mode" }));
    expect(await screen.findByRole("button", { name: /Research mode on/ })).toBeInTheDocument();
    type("why is the sky blue");
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ mode: "research", text: "why is the sky blue" });
    await waitFor(() => expect(screen.queryByRole("button", { name: /Research mode on/ })).toBeNull());
  });

  describe("attachments", () => {
    const pick = async (...files: File[]) => {
      const click = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(() => {});
      fireEvent.click(screen.getByRole("button", { name: "Add files and research" }));
      fireEvent.click(await screen.findByRole("menuitem", { name: "Attach files" }));
      await waitFor(() => expect(click).toHaveBeenCalled());
      const input = click.mock.contexts[0] as HTMLInputElement;
      Object.defineProperty(input, "files", { value: files });
      act(() => { input.onchange?.(new Event("change")); });
      click.mockRestore();
    };
    beforeEach(() => {
      bridge.begin.mockResolvedValue({ id: "up1" });
      bridge.chunk.mockResolvedValue(undefined);
      bridge.commit.mockResolvedValue({ id: "att1", name: "notes.txt", mime: "text/plain", size: 5 });
      bridge.discard.mockResolvedValue(undefined);
    });

    it("stages the file, shows it as done, sends its id, and removes it from the composer", async () => {
      bridge.send.mockResolvedValue(msg(3));
      await open([msg(1), msg(2)]);
      await pick(new File(["hello"], "notes.txt", { type: "text/plain" }));
      const chip = await screen.findByText("notes.txt");
      await waitFor(() => expect(chip.closest("[data-slot=composer-attachment]")).toHaveAttribute("data-state", "done"));
      expect(bridge.chunk).toHaveBeenCalledWith({ id: "up1", seq: 0, dataBase64: btoa("hello") });
      expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
      fireEvent.click(screen.getByRole("button", { name: "Send message" }));
      await waitFor(() => expect(bridge.send).toHaveBeenCalled());
      expect(bridge.send.mock.calls[0]![0]).toMatchObject({ text: "", attachmentIds: ["att1"] });
      await waitFor(() => expect(screen.queryByText("notes.txt")).toBeNull());
    });

    it("shows an error chip for a file the daemon rejects, blocks sending, and discards on remove", async () => {
      bridge.commit.mockRejectedValue(new Error("bad_request: not a supported file type"));
      await open([msg(1), msg(2)]);
      type("see attached");
      await pick(new File(["x"], "weird.bin", { type: "" }));
      const chip = (await screen.findByText("weird.bin")).closest("[data-slot=composer-attachment]") as HTMLElement;
      await waitFor(() => expect(chip).toHaveAttribute("data-state", "error"));
      expect(within(chip).getByRole("alert")).toHaveTextContent("not a supported file type");
      expect(screen.getByRole("button", { name: "Send message" })).toBeDisabled();
      fireEvent.click(within(chip).getByRole("button", { name: "Remove weird.bin" }));
      await waitFor(() => expect(screen.queryByText("weird.bin")).toBeNull());
      expect(screen.getByRole("button", { name: "Send message" })).toBeEnabled();
    });

    it("explains up front when the model cannot read images", async () => {
      await open([msg(1), msg(2)]);
      await pick(new File(["png"], "shot.png", { type: "image/png" }));
      const chip = (await screen.findByText("shot.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
      expect(within(chip).getByRole("alert")).toHaveTextContent("cannot read images");
      expect(bridge.begin).not.toHaveBeenCalled();
    });

    describe("pasting", () => {
      const withVision = () => useLLM.setState({ models: { openrouter: { models: [{ id: "claude", name: "Claude Sonnet", caps: { ...caps, vision: true }, reasoning: [] }], loading: false, error: null } } });
      /** Fires a paste carrying `clipboardData`; defaultPrevented means the composer took it over. */
      const paste = (files: File[], text = "") => {
        const event = createEvent.paste(box(), { clipboardData: { files, items: [], types: [], getData: (type: string) => (type === "text/plain" ? text : "") } });
        fireEvent(box(), event);
        return event;
      };
      const shot = () => new File(["png-bytes"], "image.png", { type: "image/png" });

      it("attaches a pasted screenshot for a vision model, stages it and sends its id", async () => {
        withVision();
        bridge.send.mockResolvedValue(msg(3));
        await open([msg(1), msg(2)]);
        expect(paste([shot()]).defaultPrevented).toBe(true);
        const chip = (await screen.findByText("image.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
        await waitFor(() => expect(chip).toHaveAttribute("data-state", "done"));
        expect(bridge.begin).toHaveBeenCalledWith({ name: "image.png", size: 9 });
        expect(bridge.chunk).toHaveBeenCalledWith({ id: "up1", seq: 0, dataBase64: btoa("png-bytes") });
        fireEvent.click(screen.getByRole("button", { name: "Send message" }));
        await waitFor(() => expect(bridge.send).toHaveBeenCalled());
        expect(bridge.send.mock.calls[0]![0]).toMatchObject({ attachmentIds: ["att1"] });
      });

      it("attaches files pasted from the file explorer", async () => {
        await open([msg(1), msg(2)]);
        paste([new File(["hello"], "notes.txt", { type: "text/plain" }), new File(["%PDF"], "doc.pdf", { type: "application/pdf" })]);
        expect(await screen.findByText("notes.txt")).toBeInTheDocument();
        expect(await screen.findByText("doc.pdf")).toBeInTheDocument();
        await waitFor(() => expect(bridge.begin).toHaveBeenCalledTimes(2));
      });

      it("leaves a text paste alone, even when the clipboard also has an image", async () => {
        withVision();
        await open([msg(1), msg(2)]);
        expect(paste([], "just words").defaultPrevented).toBe(false);
        expect(paste([shot()], "A1\tB1").defaultPrevented).toBe(false);
        await act(async () => { await Promise.resolve(); });
        expect(bridge.begin).not.toHaveBeenCalled();
        expect(document.querySelector("[data-slot=composer-attachment]")).toBeNull();
      });

      it("refuses a pasted image up front when the model cannot read images, and says so on the chip", async () => {
        await open([msg(1), msg(2)]);
        paste([shot()]);
        const chip = (await screen.findByText("image.png")).closest("[data-slot=composer-attachment]") as HTMLElement;
        expect(within(chip).getByRole("alert")).toHaveTextContent("This model cannot read images. Choose a model with vision support.");
        expect(bridge.begin).not.toHaveBeenCalled();
      });

      it("keeps ten files and puts the limit on the chips of the rest", async () => {
        await open([msg(1), msg(2)]);
        paste(Array.from({ length: 12 }, (_, i) => new File([`f${i}`], `f${i}.txt`, { type: "text/plain" })));
        await waitFor(() => expect(document.querySelectorAll("[data-slot=composer-attachment]")).toHaveLength(12));
        await waitFor(() => expect(bridge.begin).toHaveBeenCalledTimes(10));
        const refused = Array.from(document.querySelectorAll("[data-slot=composer-attachment][data-state=error]"));
        expect(refused).toHaveLength(2);
        expect(refused[0]).toHaveTextContent("At most 10 files can be attached to one message.");
      });

      it("stores a safe name for a pasted file whose name the daemon would refuse", async () => {
        await open([msg(1), msg(2)]);
        paste([new File(["x"], "a:b?.txt", { type: "text/plain" })]);
        expect(await screen.findByText("a_b_.txt")).toBeInTheDocument();
        await waitFor(() => expect(bridge.begin).toHaveBeenCalledWith({ name: "a_b_.txt", size: 1 }));
      });

      it("discards a pasted file's staged copy when its chip is removed", async () => {
        await open([msg(1), msg(2)]);
        paste([new File(["hello"], "notes.txt", { type: "text/plain" })]);
        const chip = (await screen.findByText("notes.txt")).closest("[data-slot=composer-attachment]") as HTMLElement;
        await waitFor(() => expect(chip).toHaveAttribute("data-state", "done"));
        fireEvent.click(within(chip).getByRole("button", { name: "Remove notes.txt" }));
        await waitFor(() => expect(bridge.discard).toHaveBeenCalledWith("att1"));
        expect(screen.queryByText("notes.txt")).toBeNull();
      });

      it("takes dropped files through the same adapter", async () => {
        await open([msg(1), msg(2)]);
        const dropzone = document.querySelector("[data-slot=composer] > div")!.firstElementChild as HTMLElement;
        fireEvent.drop(dropzone, { dataTransfer: { types: ["Files"], files: [new File(["hello"], "notes.txt", { type: "text/plain" })] } });
        expect(await screen.findByText("notes.txt")).toBeInTheDocument();
        await waitFor(() => expect(bridge.begin).toHaveBeenCalledWith({ name: "notes.txt", size: 5 }));
      });

      it("takes no files in Research mode and says so", async () => {
        await open([msg(1), msg(2)], withTools("research"));
        fireEvent.click(screen.getByRole("button", { name: "Add files and research" }));
        fireEvent.click(await screen.findByRole("menuitemcheckbox", { name: "Research mode" }));
        await screen.findByRole("button", { name: /Research mode on/ });
        expect(paste([new File(["hello"], "notes.txt", { type: "text/plain" })]).defaultPrevented).toBe(true);
        expect(await screen.findByText("Research mode takes text only. Turn it off to attach files.")).toBeInTheDocument();
        expect(bridge.begin).not.toHaveBeenCalled();
      });
    });
  });
});

describe("bot title bar actions", () => {
  it("never shows the model in the conversation or the composer", async () => {
    await open([msg(1), msg(2)], withTools("web_search"));
    expect(screen.queryByText(/Claude Sonnet/)).toBeNull();
    expect(screen.queryByText("claude")).toBeNull();
  });

  it("has a settings toggle and no overflow menu", async () => {
    await open([msg(1), msg(2)]);
    expect(screen.queryByRole("button", { name: /More actions/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Show bot settings" }));
    expect(useChat.getState().settingsOpen).toBe(true);
    expect(screen.getByRole("button", { name: "Hide bot settings" })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("questions from the bot", () => {
  const asked = () => msg(2, { parts: [{ type: "question", text: "What will I be used for?", choices: [{ label: "Writing", description: "Prose and editing" }, { label: "Coding" }] }] });

  it("sends a chosen suggestion as the user's next message", async () => {
    bridge.send.mockResolvedValue(msg(3));
    await open([msg(1), asked()]);
    expect(screen.getByText("What will I be used for?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Coding" }));
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ botId: "a", text: "Coding" });
  });

  it("sends the user's own answer", async () => {
    bridge.send.mockResolvedValue(msg(3));
    await open([msg(1), asked()]);
    fireEvent.change(screen.getByRole("textbox", { name: "Type your own answer" }), { target: { value: "  Legal research " } });
    fireEvent.submit(screen.getByRole("textbox", { name: "Type your own answer" }));
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ text: "Legal research" });
  });

  it("is read-only once the conversation has moved on", async () => {
    await open([msg(1), asked(), msg(3)]);
    expect(screen.getByRole("button", { name: "Coding" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Type your own answer" })).toBeDisabled();
  });
});

describe("mentions, structured output and search", () => {
  const coach = bot({ id: "b", name: "Coach" });
  const skilled = () => bot({ config: { ...bot().config, skills: ["pdf"] } });
  const withCoach = () => useChat.setState({ bots: [bot({ config: { ...bot().config, skills: ["pdf"] } }), coach] });

  it("offers the other bots and this bot's skills after @ and sends the mentioned ids", async () => {
    withCoach();
    bridge.send.mockResolvedValue(msg(3));
    await open();
    box().focus();
    fireEvent.change(box(), { target: { value: "@", selectionStart: 1, selectionEnd: 1 } });
    fireEvent.select(box(), { target: { selectionStart: 1, selectionEnd: 1 } });
    fireEvent.click(await screen.findByRole("option", { name: /Coach/ }));
    await waitFor(() => expect(box().value).toContain("@Coach"));
    expect(screen.queryByRole("option", { name: /Atlas/ })).toBeNull();
    expect(screen.queryByRole("option", { name: /pdf/ })).toBeNull();
    type(`${box().value.trim()} what do you think?`);
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ botId: "a", text: "@Coach what do you think?", mentions: ["b"] });
    expect(bridge.send.mock.calls[0]![0]).not.toHaveProperty("skills");
  });

  it("opens the skills picker on / and writes /skill", async () => {
    withCoach();
    await open([], skilled());
    box().focus();
    fireEvent.change(box(), { target: { value: "/", selectionStart: 1, selectionEnd: 1 } });
    fireEvent.select(box(), { target: { selectionStart: 1, selectionEnd: 1 } });
    expect(screen.queryByRole("option", { name: /Coach/ })).toBeNull();
    fireEvent.click(await screen.findByRole("option", { name: /pdf/ }));
    await waitFor(() => expect(box().value).toContain("/pdf"));
  });

  it("moves through the menu with the arrow keys, picks with Enter without sending, and closes on Escape", async () => {
    withCoach();
    await open([], skilled());
    box().focus();
    fireEvent.change(box(), { target: { value: "/", selectionStart: 1, selectionEnd: 1 } });
    fireEvent.select(box(), { target: { selectionStart: 1, selectionEnd: 1 } });
    await screen.findByRole("option", { name: /skill-creator/ });
    const selected = () => screen.getAllByRole("option").map((o) => o.getAttribute("aria-selected"));
    expect(selected()).toEqual(["true", "false"]);
    fireEvent.keyDown(box(), { key: "ArrowDown" });
    expect(selected()).toEqual(["false", "true"]);
    expect(box()).toHaveAttribute("aria-activedescendant", screen.getAllByRole("option")[1]!.id);
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(box().value).toBe("/skill-creator "));
    expect(bridge.send).not.toHaveBeenCalled();
    fireEvent.change(box(), { target: { value: "/skill-creator /", selectionStart: 16, selectionEnd: 16 } });
    fireEvent.select(box(), { target: { selectionStart: 16, selectionEnd: 16 } });
    await screen.findByRole("listbox");
    fireEvent.keyDown(box(), { key: "Escape" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(box().value).toBe("/skill-creator /");
  });

  it("offers this bot's integrations after @ and sends the mentioned integration's id", async () => {
    const canvas = bot({ config: { ...bot().config, mcpServers: ["srv-canvas"] } });
    useChat.setState({ bots: [canvas] });
    bridge.send.mockResolvedValue(msg(3));
    await open([], canvas);
    box().focus();
    fireEvent.change(box(), { target: { value: "use @can", selectionStart: 8, selectionEnd: 8 } });
    fireEvent.select(box(), { target: { selectionStart: 8, selectionEnd: 8 } });
    const option = await screen.findByRole("option", { name: /Canvas LMS/ });
    expect(option).toHaveTextContent("12 tools");
    expect(screen.queryByRole("option", { name: /Other/ })).toBeNull();
    fireEvent.click(option);
    await waitFor(() => expect(box().value).toBe("use @Canvas LMS "));
    type(`${box().value}for my deadlines`);
    // Picking puts the caret after the mention on the next animation frame. Until then the composer still reads
    // the caret inside "@can", keeps the menu open, and Enter would pick again instead of sending.
    await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ text: "use @Canvas LMS for my deadlines", integrations: ["srv-canvas"] });
  });

  it("keeps a picked mention in the draft after leaving the conversation", async () => {
    withCoach();
    const first = await open();
    box().focus();
    fireEvent.change(box(), { target: { value: "@", selectionStart: 1, selectionEnd: 1 } });
    fireEvent.select(box(), { target: { selectionStart: 1, selectionEnd: 1 } });
    fireEvent.click(await screen.findByRole("option", { name: /Coach/ }));
    await waitFor(() => expect(box().value).toContain("@Coach"));
    first.unmount();
    render(<Conversation key="a" bot={bot()} />);
    await waitFor(() => expect(box().value).toContain("@Coach"));
  });

  it("tints typed mentions and deletes a whole mention with one Backspace", async () => {
    withCoach();
    await open([], skilled());
    type("ask @Coach to use /pdf now");
    const marks = () => Array.from(document.querySelectorAll("[data-slot=mention-highlight] mark")).map((m) => m.textContent);
    expect(marks()).toEqual(["@Coach", "/pdf"]);
    box().setSelectionRange(9, 9);
    fireEvent.keyDown(box(), { key: "Backspace" });
    await waitFor(() => expect(box().value).toBe("ask  to use /pdf now"));
    expect(marks()).toEqual(["/pdf"]);
    box().setSelectionRange(13, 13);
    fireEvent.keyDown(box(), { key: "Delete" });
    await waitFor(() => expect(box().value).toBe("ask  to use  now"));
    type("plain text");
    box().setSelectionRange(10, 10);
    const untouched = fireEvent.keyDown(box(), { key: "Backspace" });
    expect(untouched).toBe(true);
  });

  it("sends a typed /skill as a skill mention and keeps research messages free of mentions", async () => {
    withCoach();
    bridge.send.mockResolvedValue(msg(3));
    await open();
    type("/pdf summarise this");
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ text: "/pdf summarise this", skills: ["pdf"] });
    expect(bridge.send.mock.calls[0]![0]).not.toHaveProperty("mentions");
  });

  it("offers the built-in skill-creator to a bot that was given no skills", async () => {
    withCoach();
    bridge.send.mockResolvedValue(msg(3));
    await open();
    await act(async () => {}); // let the registry list arrive: built-in skills come from it, not from the bot
    type("/skill-creator make me a skill");
    fireEvent.keyDown(box(), { key: "Enter" });
    await waitFor(() => expect(bridge.send).toHaveBeenCalledTimes(1));
    expect(bridge.send.mock.calls[0]![0]).toMatchObject({ skills: ["skill-creator"] });
  });

  it("draws mentions in a sent message as chips with the bot's avatar and no raw trigger", async () => {
    withCoach();
    await open([msg(1, { parts: [{ type: "text", text: "question @Coach and /pdf please" }] })], skilled());
    const chips = Array.from(document.querySelectorAll("[data-slot=directive-text-chip]")) as HTMLElement[];
    expect(chips.map((c) => [c.dataset.directiveType, c.dataset.directiveId, c.textContent])).toEqual([["bot", "b", "Coach"], ["skill", "pdf", "pdf"]]);
    expect(chips[0]!.querySelector("img")).not.toBeNull();
  });

  it("draws a reply to an @mention as the mentioned bot", async () => {
    withCoach();
    await open([msg(1), msg(2, { author: "bot:b", parts: [{ type: "text", text: "Coach here" }] })]);
    expect(screen.getByText("Coach here")).toBeInTheDocument();
    expect(screen.getByText("Coach said:")).toBeInTheDocument();
    expect(screen.queryByText("Atlas said:")).toBeNull();
  });

  it("renders the structured components an assistant message carries", async () => {
    const table = JSON.stringify({ columns: [{ key: "n", label: "Name", priority: "primary" }, { key: "v", label: "Score", format: { kind: "number" } }], rows: [{ n: "Ada", v: 9 }] });
    const chart = JSON.stringify({ label: "Runs", value: "92", points: [1, 5, 9] });
    const timeline = JSON.stringify({ events: [{ id: "1", when: "now", time: "09:00", title: "Kickoff" }] });
    await open([msg(1), msg(2, { parts: [
      { type: "ui", name: "table", spec: table }, { type: "ui", name: "chart", spec: chart }, { type: "ui", name: "timeline", spec: timeline },
      { type: "ui", name: "chart", spec: "{not json" }, { type: "ui", name: "bogus", spec: "{}" },
    ] })]);
    expect(document.querySelector("[data-slot=data-table]")).not.toBeNull();
    expect(screen.getAllByText("Ada").length).toBeGreaterThan(0);
    expect(document.querySelector("[data-slot=chart]")).not.toBeNull();
    expect(screen.getByText("Kickoff")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-slot=chat-structured]")).toHaveLength(3);
  });

  it("opens find-in-conversation on Ctrl+F, counts matches and closes on Escape", async () => {
    await open([msg(1, { parts: [{ type: "text", text: "question: Where is Paris?" }] }), msg(2, { parts: [{ type: "text", text: "answer: Paris is in France; paris again" }] })]);
    const scrolled: string[] = [];
    Element.prototype.scrollIntoView = function (this: Element) { scrolled.push(this.getAttribute("data-message-id") ?? ""); };
    vi.stubGlobal("matchMedia", () => ({ matches: false }));
    fireEvent.keyDown(document.body, { key: "f", ctrlKey: true });
    const input = await screen.findByRole("textbox", { name: "Find in conversation" });
    fireEvent.change(input, { target: { value: "paris" } });
    expect(screen.getByText("1/3")).toBeInTheDocument();
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByText("2/3")).toBeInTheDocument();
    fireEvent.keyDown(input, { key: "Enter", shiftKey: true });
    expect(screen.getByText("1/3")).toBeInTheDocument();
    expect(scrolled).toEqual(["m1", "m2", "m1"]);
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("textbox", { name: "Find in conversation" })).toBeNull();
  });
});
