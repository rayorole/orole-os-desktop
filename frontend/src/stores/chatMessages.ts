import { create } from "zustand";
import { Chat, events, type Message, type MessageDelta, type SendRequest } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { onFocusRefresh } from "@/lib/focus";
import { useChatDrafts } from "@/stores/chatDrafts";
import { useChat } from "@/stores/chat";
import { useDaemon } from "@/stores/daemon";

/** Messages kept in memory per bot; older ones stay reachable through `loadOlder`. */
export const MAX_IN_MEMORY = 300;
const PAGE = 50;
const DRAFT_PREFIX = "draft:";

/** One bot's loaded window of its single, continuous conversation (ascending by seq). */
export interface Conversation {
  messages: Message[];
  /** Older messages exist on the daemon. */
  hasMore: boolean;
  loaded: boolean;
  loading: boolean;
  loadingOlder: boolean;
  error: string | null;
  /** The reader has paged back, so trimming the front on new messages would pull history out from under them. */
  browsing: boolean;
}

/** Something the user should see about the last action; a failed send carries its request so Retry is idempotent. */
export interface Problem { message: string; request?: SendRequest }

export interface SendInput { text: string; attachmentIds?: string[]; mode?: string; mentions?: string[]; skills?: string[]; integrations?: string[] }

interface State {
  byBot: Record<string, Conversation>;
  /**
   * Run state from chat.run events while this store is subscribed (absent otherwise: events that arrive while nobody
   * listens, or while the daemon link is down, are lost, so the value is dropped rather than kept stale).
   */
  runs: Record<string, boolean>;
  /** Bots whose queue is on hold (chat.run events): waiting messages start only after `resumeQueue`. */
  paused: Record<string, boolean>;
  problems: Record<string, Problem>;
  load: (botId: string) => Promise<void>;
  loadOlder: (botId: string) => Promise<void>;
  /** Re-fetches the latest page of the open conversation and merges it into what live events already delivered; other conversations reload when opened. */
  refresh: () => void;
  send: (botId: string, input: SendInput) => Promise<boolean>;
  retrySend: (botId: string) => Promise<boolean>;
  cancel: (botId: string) => Promise<void>;
  pauseQueue: (botId: string) => Promise<void>;
  resumeQueue: (botId: string) => Promise<void>;
  /** Drops one message that is still waiting in the bot's queue. */
  cancelQueued: (botId: string, messageId: string) => Promise<void>;
  regenerate: (botId: string, messageId: string) => Promise<void>;
  clear: (botId: string) => Promise<boolean>;
  dismissProblem: (botId: string) => void;
  startSync: () => () => void;
}

const EMPTY: Conversation = { messages: [], hasMore: false, loaded: false, loading: false, loadingOlder: false, error: null, browsing: false };

/** Daemon errors carry a machine prefix ("bad_request: ..."); the user needs the sentence. */
const describe = (cause: unknown) => (cause instanceof Error ? cause.message : String(cause)).replace(/^(bad_request|not_found|canceled|timeout|internal|denied): /, "");

const bySeq = (a: Message, b: Message) => a.seq - b.seq || a.createdAt - b.createdAt;

/** Inserts or replaces by id; a replacement keeps its slot so a streaming reply updates in place. */
export function upsert(list: readonly Message[], message: Message): Message[] {
  const at = list.findIndex((m) => m.id === message.id);
  if (at >= 0) {
    const next = list.slice();
    next[at] = message;
    return next[at - 1] && bySeq(next[at - 1]!, message) > 0 || next[at + 1] && bySeq(message, next[at + 1]!) > 0 ? next.sort(bySeq) : next;
  }
  const last = list[list.length - 1];
  return !last || bySeq(last, message) <= 0 ? [...list, message] : [...list, message].sort(bySeq);
}

/**
 * Appends a streamed delta to its message part. Returns null (message unchanged) unless the message and
 * part exist, the part type matches and the part's text is exactly `from` UTF-16 units long: a delta
 * that does not line up means this window missed an event, and the next full snapshot repairs it.
 */
export function applyDelta(message: Message, delta: MessageDelta): Message | null {
  const part = message.parts[delta.part];
  if (!part || part.type !== delta.type || (part.text ?? "").length !== delta.from) return null;
  const parts = message.parts.slice();
  parts[delta.part] = { ...part, text: (part.text ?? "") + delta.text };
  return { ...message, parts };
}

let subscribers = 0;
let stop: (() => void) | undefined;
/** Latest request token per bot, for both pages: a response only lands while its token is still current. */
const latest = new Map<string, number>();
/** Counts the chat.run events seen per bot; a run-state fetch that raced one of them is stale and dropped. */
const runEvents = new Map<string, number>();
const older = new Map<string, number>();
/** Ids an event delivered since the bot's latest-page request started; the (older) response must not overwrite them. */
const live = new Map<string, Map<string, Message>>();
let token = 0;

const patch = (botId: string, change: Partial<Conversation>) =>
  useChatMessages.setState((state) => ({ byBot: { ...state.byBot, [botId]: { ...(state.byBot[botId] ?? EMPTY), ...change } } }));

/** Merges a fetched latest page with the current window; see the store notes on stale responses. */
export function mergeLatest(current: Conversation, page: readonly Message[], pageHasMore: boolean, touched: ReadonlyMap<string, Message>): Pick<Conversation, "messages" | "hasMore"> {
  const floor = page[0]?.seq;
  const keep = page.length && pageHasMore && floor !== undefined ? current.messages.filter((m) => m.seq < floor && !touched.has(m.id)) : [];
  const byId = new Map<string, Message>();
  for (const m of page) byId.set(m.id, touched.get(m.id) ?? m);
  for (const [id, m] of touched) if (!byId.has(id) && (floor === undefined || m.seq >= floor)) byId.set(id, m);
  const messages = [...keep, ...[...byId.values()].toSorted(bySeq)];
  return { messages, hasMore: keep.length ? current.hasMore : pageHasMore };
}

function trim(conversation: Conversation, messages: Message[]): Pick<Conversation, "messages" | "hasMore"> {
  if (conversation.browsing || messages.length <= MAX_IN_MEMORY) return { messages, hasMore: conversation.hasMore };
  return { messages: messages.slice(messages.length - MAX_IN_MEMORY), hasMore: true };
}

/** The daemon owns conversations. This is a disposable, event-synchronized window onto each one that has been opened. */
export const useChatMessages = create<State>((set, get) => ({
  byBot: {}, runs: {}, paused: {}, problems: {},
  async load(botId) {
    if (!inDesktop || botId.startsWith(DRAFT_PREFIX)) return;
    const request = ++token;
    latest.set(botId, request);
    live.set(botId, new Map());
    const before = get().byBot[botId];
    patch(botId, { loading: true, error: null, ...(before ? {} : { loaded: false }) });
    try {
      const page = await Chat.messages({ botId, beforeSeq: 0, limit: PAGE });
      if (latest.get(botId) !== request) return;
      const merged = mergeLatest(get().byBot[botId] ?? EMPTY, page?.messages ?? [], page?.hasMore ?? false, live.get(botId) ?? new Map());
      patch(botId, { ...merged, loaded: true, loading: false, error: null });
    } catch (cause) {
      if (latest.get(botId) === request) patch(botId, { loading: false, error: describe(cause) });
    }
  },
  async loadOlder(botId) {
    const current = get().byBot[botId];
    if (!current || !current.hasMore || current.loadingOlder || !current.messages.length) return;
    const request = ++token;
    older.set(botId, request);
    patch(botId, { loadingOlder: true });
    try {
      const page = await Chat.messages({ botId, beforeSeq: current.messages[0]!.seq, limit: PAGE });
      if (older.get(botId) !== request) return;
      const state = get().byBot[botId] ?? EMPTY;
      const have = new Set(state.messages.map((m) => m.id));
      const fresh = (page?.messages ?? []).filter((m) => !have.has(m.id));
      patch(botId, { messages: [...fresh, ...state.messages], hasMore: page?.hasMore ?? false, loadingOlder: false, browsing: true });
    } catch (cause) {
      if (older.get(botId) === request) patch(botId, { loadingOlder: false, error: describe(cause) });
    }
  },
  refresh() {
    const botId = useChat.getState().selectedBotId;
    const conversation = botId ? get().byBot[botId] : undefined;
    if (botId && (conversation?.loaded || conversation?.error)) void get().load(botId);
  },
  async send(botId, input) {
    const request: SendRequest = { botId, text: input.text, attachmentIds: input.attachmentIds ?? [], mode: input.mode ?? "", clientNonce: crypto.randomUUID(), ...(input.mentions?.length ? { mentions: input.mentions } : {}), ...(input.skills?.length ? { skills: input.skills } : {}), ...(input.integrations?.length ? { integrations: input.integrations } : {}) };
    return deliver(botId, request);
  },
  async retrySend(botId) {
    const request = get().problems[botId]?.request;
    return request ? deliver(botId, request) : false;
  },
  async cancel(botId) {
    try { await Chat.cancel(botId); } catch (cause) { setProblem(botId, { message: `Could not stop the reply: ${describe(cause)}` }); }
    // Stopping a run the daemon no longer has changes nothing and emits no event: ask it what is true so a wrong "running" ends here.
    await reconcileRun(botId);
  },
  async pauseQueue(botId) {
    try { await Chat.pauseQueue(botId); } catch (cause) { setProblem(botId, { message: `Could not pause the queue: ${describe(cause)}` }); }
  },
  async resumeQueue(botId) {
    try { await Chat.resumeQueue(botId); } catch (cause) { setProblem(botId, { message: `Could not resume the queue: ${describe(cause)}` }); }
  },
  async cancelQueued(botId, messageId) {
    try { await Chat.cancelQueued({ botId, messageId }); } catch (cause) { setProblem(botId, { message: `Could not cancel the message: ${describe(cause)}` }); }
  },
  async regenerate(botId, messageId) {
    try { await Chat.regenerate({ botId, messageId }); } catch (cause) { setProblem(botId, { message: `Could not regenerate the reply: ${describe(cause)}` }); }
  },
  async clear(botId) {
    try {
      await Chat.clear(botId);
      latest.delete(botId); older.delete(botId); live.delete(botId);
      set((state) => ({ byBot: { ...state.byBot, [botId]: { ...EMPTY, loaded: true } }, problems: omit(state.problems, botId) }));
      return true;
    } catch (cause) {
      setProblem(botId, { message: `Could not clear the conversation: ${describe(cause)}` });
      return false;
    }
  },
  dismissProblem: (botId) => set((state) => ({ problems: omit(state.problems, botId) })),
  startSync() {
    if (!inDesktop) return () => {};
    if (++subscribers === 1) {
      const refresh = () => { get().refresh(); };
      // Subscribe before anything is fetched so no event between a request and its response is lost.
      const offMessage = events.chatMessage.on((message) => {
        if (message.botId.startsWith(DRAFT_PREFIX)) return;
        const conversation = get().byBot[message.botId];
        if (!conversation) return;
        live.get(message.botId)?.set(message.id, message);
        set((state) => {
          const current = state.byBot[message.botId];
          if (!current) return state;
          const messages = upsert(current.messages, message);
          const isNew = messages.length > current.messages.length;
          return { byBot: { ...state.byBot, [message.botId]: { ...current, ...(isNew ? trim(current, messages) : { messages }) } } };
        });
      });
      // Streamed text arrives as deltas between full snapshots; one that does not line up is dropped and the next snapshot repairs it.
      const offDelta = events.chatMessageDelta.on((delta) => {
        if (delta.botId.startsWith(DRAFT_PREFIX)) return;
        const current = get().byBot[delta.botId];
        const at = current ? current.messages.findLastIndex((m) => m.id === delta.messageId) : -1;
        const next = current && at >= 0 ? applyDelta(current.messages[at]!, delta) : null;
        if (!current || !next) return;
        live.get(delta.botId)?.set(next.id, next);
        const messages = current.messages.slice();
        messages[at] = next;
        set((state) => ({ byBot: { ...state.byBot, [delta.botId]: { ...current, messages } } }));
      });
      const offRun = events.chatRun.on(({ botId, running, paused }) => {
        runEvents.set(botId, (runEvents.get(botId) ?? 0) + 1);
        set((state) => ({ runs: { ...state.runs, [botId]: running }, paused: { ...state.paused, [botId]: paused === true } }));
      });
      const offBot = events.chatBot.on(({ id, action }) => {
        if (action === "archived") {
          latest.delete(id); older.delete(id); live.delete(id);
          set((state) => ({ byBot: omit(state.byBot, id), runs: omit(state.runs, id), paused: omit(state.paused, id), problems: omit(state.problems, id) }));
          useChatDrafts.getState().dropDraft(id);
        } else if (action === "cleared") {
          latest.delete(id); older.delete(id); live.delete(id);
          set((state) => state.byBot[id] ? { byBot: { ...state.byBot, [id]: { ...EMPTY, loaded: true } } } : state);
        }
      });
      const offDaemon = useDaemon.subscribe((state, previous) => {
        if (state.link === "connected" && previous.link !== "connected") refresh();
        // Events sent while the link is down never arrive: forget what they last said and let the daemon's list speak after reconnecting.
        if (state.link === "offline" && previous.link !== "offline") set({ runs: {} });
      });
      const offFocus = onFocusRefresh(refresh);
      stop = () => { offMessage(); offDelta(); offRun(); offBot(); offDaemon(); offFocus(); };
    }
    let stopped = false;
    return () => {
      if (stopped) return;
      stopped = true;
      if (--subscribers === 0) { stop?.(); stop = undefined; set({ runs: {} }); }
    };
  },
}));

function omit<T>(record: Record<string, T>, key: string): Record<string, T> {
  if (!(key in record)) return record;
  const { [key]: _removed, ...rest } = record;
  return rest;
}

function setProblem(botId: string, problem: Problem) {
  useChatMessages.setState((state) => ({ problems: { ...state.problems, [botId]: problem } }));
}

/** Sends once; the daemon returns the stored user message (the reply arrives by event). A failure keeps the exact request for an idempotent retry. */
async function deliver(botId: string, request: SendRequest): Promise<boolean> {
  try {
    const stored = await Chat.send(request);
    useChatMessages.setState((state) => {
      const current = state.byBot[botId] ?? EMPTY;
      return { byBot: { ...state.byBot, [botId]: { ...current, messages: upsert(current.messages, stored) } }, problems: omit(state.problems, botId) };
    });
    live.get(botId)?.set(stored.id, stored);
    return true;
  } catch (cause) {
    setProblem(botId, { message: `Message not sent: ${describe(cause)}`, request });
    return false;
  }
}

/** The conversation window for a bot (an empty, unloaded one before `load`). */
export const selectConversation = (botId: string) => (state: State): Conversation => state.byBot[botId] ?? EMPTY;

/** Asks the daemon whether the bot is running and corrects both run-state copies, unless a chat.run event arrived meanwhile. */
async function reconcileRun(botId: string) {
  if (!inDesktop || botId.startsWith(DRAFT_PREFIX)) return;
  const seen = runEvents.get(botId) ?? 0;
  try {
    const { running } = await Chat.bot(botId);
    if ((runEvents.get(botId) ?? 0) !== seen) return;
    useChatMessages.setState((state) => ({ runs: { ...state.runs, [botId]: running } }));
    useChat.setState((state) => ({ running: { ...state.running, [botId]: running } }));
  } catch {
    // The daemon cannot be asked (offline, bot archived): the link and bot events already cover those.
  }
}

/**
 * Whether the bot is producing a reply. The daemon's run event wins, then the rail's snapshot (kept in step with the
 * daemon for the app's lifetime). A streaming message only decides before either has said anything: a stale
 * "running" snapshot must never override the daemon reporting the bot idle.
 */
export function useBotRunning(botId: string): boolean {
  const own = useChatMessages((s) => s.runs[botId]);
  const rail = useChat((s) => s.running[botId]);
  const streaming = useChatMessages((s) => s.byBot[botId]?.messages.some((m) => m.status === "running") ?? false);
  return own ?? rail ?? streaming;
}
