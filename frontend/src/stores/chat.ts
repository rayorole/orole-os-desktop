import { create } from "zustand";
import { useStoreWithEqualityFn } from "zustand/traditional";
import { Chat, events, type Bot, type Message, type MessagePreview } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { onFocusRefresh } from "@/lib/focus";
import { useDaemon } from "@/stores/daemon";
import { useUI } from "@/stores/ui";

const SELECTED_KEY = "orole.chat.selected";
const PREVIEW_MAX = 160;
/** Longest a streaming reply's rail preview may lag behind it. */
const PREVIEW_INTERVAL = 1000;

interface BuilderState {
  open: boolean;
  /** The bot being edited; null while creating a new one. */
  botId: string | null;
}

export interface ChatState {
  /** Newest conversation first, then by name. */
  bots: Bot[];
  selectedBotId: string | null;
  running: Record<string, boolean>;
  builder: BuilderState;
  /** False in the browser preview, where there is no daemon. */
  available: boolean;
  ready: boolean;
  loading: boolean;
  error: string | null;
  load: () => Promise<void>;
  startSync: () => () => void;
  select: (botId: string | null) => void;
  /** Selects the bot and shows the chat view (palette and rail entry point). */
  open: (botId: string) => void;
  openBuilder: (botId: string | "new") => void;
  closeBuilder: () => void;
  /** The slim settings panel beside the conversation (name, description, instructions, model). */
  settingsOpen: boolean;
  setSettingsOpen: (open: boolean) => void;
  /** What the side pane beside the conversation shows: a code task, a phone call's transcript, a bot's computer or a document a reply wrote; it and the settings panel are never open together. */
  pane: ChatPane | null;
  openTaskPane: (taskId: string) => void;
  /** Opens a phone call's transcript (its spoken turns are not in the thread). */
  openCallPane: (callId: string) => void;
  openDocument: (messageId: string, partIndex: number) => void;
  /** Opens the bot's Computer pane: its setup or state, live screen and terminal (id = the bot). */
  openComputerPane: (botId: string) => void;
  closePane: () => void;
}

export type ChatPane = { kind: "task"; id: string } | { kind: "call"; id: string } | { kind: "computer"; id: string } | { kind: "document"; messageId: string; partIndex: number };

const readSelected = (): string | null => {
  try { return localStorage.getItem(SELECTED_KEY) || null; } catch { return null; }
};
const writeSelected = (id: string | null) => {
  try {
    if (id) localStorage.setItem(SELECTED_KEY, id); else localStorage.removeItem(SELECTED_KEY);
  } catch {
    // Selection then lasts for the session only.
  }
};

/** Latest activity first; bots nobody has talked to follow, alphabetically. */
export function sortBots(bots: Bot[]): Bot[] {
  return bots.toSorted((a, b) => (b.lastMessage?.at ?? 0) - (a.lastMessage?.at ?? 0) || a.name.localeCompare(b.name));
}

/** One-line text for the rail from a message snapshot, or null when it has nothing worth showing yet (e.g. a tool-only step). */
export function previewOf(message: Message): MessagePreview | null {
  const parts = message.parts ?? [];
  let text = parts.filter((p) => p.type === "text").map((p) => p.text ?? "").join(" ");
  if (!text.trim()) text = parts.find((p) => p.type === "report")?.text ?? "";
  if (!text.trim() && parts.some((p) => p.type === "attachment")) text = "Attachment";
  if (!text.trim() && message.status === "error") text = message.error ?? "";
  text = text.replace(/\s+/g, " ").trim();
  if (!text) return null;
  return { role: message.role, text: text.length > PREVIEW_MAX ? `${text.slice(0, PREVIEW_MAX - 1)}…` : text, at: message.createdAt };
}

let sequence = 0;
let subscribers = 0;
let stop: (() => void) | undefined;
/** Bots whose live state arrived by event while a list request was in flight; the (older) response must not overwrite them. */
const touched = new Set<string>();

/** Newest preview per bot not yet applied; a streaming reply sends a snapshot every ~50 ms but the rail only needs one a second. */
const pending = new Map<string, MessagePreview>();
let previewTimer: ReturnType<typeof setTimeout> | undefined;

/** Applies every pending preview in one update that keeps untouched bots (and the array, if nothing changed) by reference. */
function flushPreviews() {
  if (pending.size === 0) return;
  const batch = [...pending];
  pending.clear();
  useChat.setState((state) => {
    let changed = false;
    const bots = state.bots.map((bot) => {
      const preview = batch.find(([id]) => id === bot.id)?.[1];
      const shown = bot.lastMessage;
      if (!preview || (shown && (shown.at > preview.at || (shown.at === preview.at && shown.role === preview.role && shown.text === preview.text)))) return bot;
      changed = true;
      return { ...bot, lastMessage: preview };
    });
    return changed ? { bots: sortBots(bots) } : state;
  });
}

/** A settled message applies at once; streaming snapshots apply immediately when idle, then at most once per `PREVIEW_INTERVAL`. */
function queuePreview(botId: string, preview: MessagePreview, settled: boolean) {
  pending.set(botId, preview);
  if (settled) { flushPreviews(); return; }
  if (previewTimer !== undefined) return;
  flushPreviews();
  const tick = () => {
    if (pending.size === 0) { previewTimer = undefined; return; }
    flushPreviews();
    previewTimer = setTimeout(tick, PREVIEW_INTERVAL);
  };
  previewTimer = setTimeout(tick, PREVIEW_INTERVAL);
}

function dropPreviews() {
  clearTimeout(previewTimer);
  previewTimer = undefined;
  pending.clear();
}

/** The daemon owns bots and conversations. This is a disposable, event-synchronized snapshot for the rail. */
export const useChat = create<ChatState>((set, get) => ({
  bots: [], selectedBotId: readSelected(), running: {}, builder: { open: false, botId: null }, settingsOpen: false, pane: null,
  available: inDesktop, ready: false, loading: false, error: null,
  async load() {
    if (!get().available) return;
    const request = ++sequence;
    touched.clear();
    set({ loading: true, error: null });
    try {
      const fetched = await Chat.bots() ?? [];
      if (request !== sequence) return;
      set((state) => {
        const current = new Map(state.bots.map((b) => [b.id, b]));
        const bots = fetched.map((b) => {
          if (!touched.has(b.id)) return b;
          const live = current.get(b.id);
          return live ? { ...b, running: state.running[b.id] ?? b.running, lastMessage: live.lastMessage } : b;
        });
        const running = Object.fromEntries(bots.map((b) => [b.id, touched.has(b.id) ? state.running[b.id] ?? b.running : b.running]));
        const selectedBotId = state.selectedBotId && bots.some((b) => b.id === state.selectedBotId) ? state.selectedBotId : null;
        if (selectedBotId !== state.selectedBotId) writeSelected(null);
        return { bots: sortBots(bots), running, selectedBotId, ready: true, loading: false, error: null };
      });
    } catch (cause) {
      if (request === sequence) set({ ready: false, loading: false, error: cause instanceof Error ? cause.message : String(cause) });
    }
  },
  startSync() {
    if (!get().available) return () => {};
    if (++subscribers === 1) {
      const refresh = () => { void get().load(); };
      // Subscribe first so nothing that happens during the first list is lost.
      const offRun = events.chatRun.on(({ botId, running }) => {
        touched.add(botId);
        // Kept even for a bot the first list has not delivered yet; the list merge consults it.
        set((state) => ({ running: { ...state.running, [botId]: running } }));
      });
      const offMessage = events.chatMessage.on((message) => {
        touched.add(message.botId);
        const preview = previewOf(message);
        if (preview) queuePreview(message.botId, preview, message.status !== "running");
      });
      const offBot = events.chatBot.on(({ id, action }) => {
        if (action === "archived") {
          pending.delete(id);
          set((state) => {
            if (state.selectedBotId === id) writeSelected(null);
            const running = { ...state.running };
            delete running[id];
            return {
              bots: state.bots.filter((b) => b.id !== id), running,
              selectedBotId: state.selectedBotId === id ? null : state.selectedBotId,
              pane: state.selectedBotId === id ? null : state.pane,
              builder: state.builder.botId === id ? { open: false, botId: null } : state.builder,
            };
          });
        } else if (action === "cleared") {
          pending.delete(id);
          set((state) => ({ bots: sortBots(state.bots.map((b) => b.id === id ? { ...b, lastMessage: undefined } : b)) }));
        }
        refresh();
      });
      const offDaemon = useDaemon.subscribe((state, previous) => {
        if (state.link === "connected" && previous.link !== "connected") refresh();
        if (state.link === "offline" && previous.link !== "offline") {
          ++sequence;
          set({ ready: false, loading: false, error: "The daemon is offline. Reconnect to chat with your bots." });
        }
      });
      const offFocus = onFocusRefresh(refresh);
      refresh();
      stop = () => { offRun(); offMessage(); offBot(); offDaemon(); offFocus(); dropPreviews(); };
    }
    let stopped = false;
    return () => {
      if (stopped) return;
      stopped = true;
      if (--subscribers === 0) {
        ++sequence;
        stop?.();
        stop = undefined;
        set({ ready: false, loading: false });
      }
    };
  },
  select(botId) {
    writeSelected(botId);
    // A task pane belongs to the conversation it was opened from.
    set((state) => ({ selectedBotId: botId, pane: state.selectedBotId === botId ? state.pane : null }));
  },
  open(botId) {
    get().select(botId);
    useUI.getState().setView("chat");
  },
  openBuilder: (botId) => set({ builder: { open: true, botId: botId === "new" ? null : botId } }),
  closeBuilder: () => set({ builder: { open: false, botId: null } }),
  setSettingsOpen: (settingsOpen) => set((state) => ({ settingsOpen, pane: settingsOpen ? null : state.pane })),
  openTaskPane: (id) => set({ pane: { kind: "task", id }, settingsOpen: false }),
  openCallPane: (id) => set({ pane: { kind: "call", id }, settingsOpen: false }),
  openDocument: (messageId, partIndex) => set({ pane: { kind: "document", messageId, partIndex }, settingsOpen: false }),
  openComputerPane: (botId) => set({ pane: { kind: "computer", id: botId }, settingsOpen: false }),
  closePane: () => set({ pane: null }),
}));

/** Fields a conversation depends on; `running` and the rail preview are derived and change while a reply streams. */
const sameBot = (a: Bot | undefined, b: Bot | undefined): boolean => {
  if (a === b) return true;
  if (!a || !b) return false;
  const { running: _ar, lastMessage: _al, ...restA } = a;
  const { running: _br, lastMessage: _bl, ...restB } = b;
  return (Object.keys(restA) as (keyof typeof restA)[]).every((key) => restA[key] === restB[key]);
};

/** The bot whose conversation is open, if it still exists. Stable while only its rail preview or running flag changes. */
export function useSelectedBot(): Bot | undefined {
  return useStoreWithEqualityFn(useChat, (state) => state.bots.find((b) => b.id === state.selectedBotId), sameBot);
}
