import { create } from "zustand";
import { useShallow } from "zustand/react/shallow";
import { Agents, events, type Detection, type Run, type Session, type SessionDelta } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { columnOf, onDashboard } from "@/features/agents/columns";
import type { LaunchDraft, PendingLaunch } from "@/features/tasks/pendingLaunch";

export type SessionMap = Record<string, Session>;

/** Sidebar / status bar numbers. `needsYou` includes failed sessions, like the dashboard column. */
export interface AgentCounts {
  needsYou: number;
  working: number;
  done: number;
  idle: number;
  /** Sessions on the dashboard (discarded ones excluded). */
  total: number;
  /** Finished work (done or failed) nobody has approved or discarded yet. */
  reviewable: number;
}

const ts = (s: { updatedAt: string }) => Date.parse(s.updatedAt) || 0;

/**
 * `updatedAt` of the newest full session each id was last set from. Deltas advance a held
 * session's `updatedAt` but not this, so a full update that was published before a delta yet
 * arrives after it (the daemon publishes outside its lock) still lands: it carries status
 * changes a delta cannot, and only the delta fields keep the newer values.
 */
const fullAt = new Map<string, number>();

/** The session to hold after a full update `next` for `cur` (undefined: `next` is stale, drop it). */
function foldFull(cur: Session | undefined, next: Session): Session | undefined {
  if (!cur) { fullAt.set(next.id, ts(next)); return next; }
  const at = ts(next);
  if (at < (fullAt.get(next.id) ?? ts(cur))) return undefined;
  fullAt.set(next.id, at);
  if (ts(cur) <= at) return next;
  // The fields `SessionDelta` carries keep the newer values the deltas already merged.
  const { updatedAt, activity, tail, lastMessage, agentSessionId, model, transcriptRef, files, additions, deletions, usage, toolCalls, failedTools, tokens, billed } = cur;
  return { ...next, updatedAt, activity, tail, lastMessage, agentSessionId, model, transcriptRef, files, additions, deletions, usage, toolCalls, failedTools, tokens, billed };
}

/** Upserts a session; an update older than what is held is dropped (events can arrive out of order). */
export function applySessionUpdate(sessions: SessionMap, next: Session): SessionMap {
  const merged = foldFull(sessions[next.id], next);
  return merged ? { ...sessions, [next.id]: merged } : sessions;
}

/** Merges a hot-path delta into the held session; a delta for an unheld or newer session is ignored. */
export function applySessionDelta(sessions: SessionMap, delta: SessionDelta): SessionMap {
  const cur = sessions[delta.id];
  if (!cur || ts(cur) > ts(delta)) return sessions;
  const { id: _id, ...fields } = delta;
  return { ...sessions, [delta.id]: { ...cur, ...fields } };
}

function omitLaunch(launches: Record<string, PendingLaunch>, id: string): Record<string, PendingLaunch> {
  const { [id]: _gone, ...rest } = launches;
  return rest;
}

export function removeSession(sessions: SessionMap, id: string): SessionMap {
  fullAt.delete(id);
  if (!(id in sessions)) return sessions;
  const { [id]: _gone, ...rest } = sessions;
  return rest;
}

/**
 * Replaces the held sessions by a fresh List result. Sessions an event delivered while the list
 * was in flight (`live`) are kept, and a held session newer than the listed one wins.
 */
export function mergeList(held: SessionMap, listed: Session[], live: ReadonlySet<string> = new Set()): SessionMap {
  const out: SessionMap = {};
  for (const s of listed) out[s.id] = foldFull(held[s.id], s) ?? held[s.id];
  for (const id of live) {
    if (held[id]) out[id] = held[id];
    else delete out[id];
  }
  for (const id of fullAt.keys()) if (!(id in out)) fullAt.delete(id);
  return out;
}

const runTs = (r: Run) => Date.parse(r.updatedAt) || 0;

/** Upserts a run, newest first. */
export function applyRunUpdate(runs: Run[], next: Run): Run[] {
  const cur = runs.find((r) => r.id === next.id);
  if (cur && runTs(cur) > runTs(next)) return runs;
  return [next, ...runs.filter((r) => r.id !== next.id)].sort((a, b) => runTs(b) - runTs(a));
}

export const isReviewable = (s: Session) => (s.status === "done" || s.status === "failed") && s.review === "";

/** Every count in one pass over the sessions: no grouping, no sorting. */
export function selectCounts(sessions: SessionMap): AgentCounts {
  const counts: AgentCounts = { needsYou: 0, working: 0, done: 0, idle: 0, total: 0, reviewable: 0 };
  for (const id in sessions) {
    const s = sessions[id]!;
    if (isReviewable(s)) counts.reviewable++;
    if (!onDashboard(s)) continue;
    counts.total++;
    switch (columnOf(s.status)) {
      case "needs-you": counts.needsYou++; break;
      case "working": counts.working++; break;
      case "done": counts.done++; break;
      default: counts.idle++;
    }
  }
  return counts;
}

const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

interface State {
  /** False in the browser preview: no daemon, so no agents. */
  available: boolean;
  sessions: SessionMap;
  runs: Run[];
  detections: Detection[] | null;
  detecting: boolean;
  detectError: string | null;
  loaded: boolean;
  loading: boolean;
  error: string | null;
  /** The last List failed: the daemon could not be reached. */
  offline: boolean;
  /** Project id the dashboard is filtered to; null shows every project. */
  projectFilter: string | null;
  /** Session shown in the dashboard's detail panel. */
  openId: string | null;
  setProjectFilter: (id: string | null) => void;
  setOpenId: (id: string | null) => void;
  /** Tasks being started: sent, but `Agents.start` has not returned a session (or failed). The id is also the `openId` of the pending task page. */
  launches: Record<string, PendingLaunch>;
  /** The launcher form a failed launch is returned to; the next launcher of that kind to open takes it. */
  launchDraft: LaunchDraft | null;
  addLaunch: (launch: PendingLaunch) => void;
  patchLaunch: (id: string, patch: Partial<Pick<PendingLaunch, "status" | "error">>) => void;
  removeLaunch: (id: string) => void;
  setLaunchDraft: (draft: LaunchDraft | null) => void;
  /** Swaps a started launch for its session; `open` also opens the session where the pending page was. */
  resolveLaunch: (id: string, session: Session, open: boolean) => void;
  load: () => Promise<void>;
  refresh: () => Promise<void>;
  detect: (refresh: boolean) => Promise<void>;
  /** Applies a session returned by a call without waiting for its event. */
  upsert: (s: Session) => void;
  /** Drops a deleted session now, without waiting for its removal event; an open task page falls back to the board. */
  drop: (id: string) => void;
  /** Subscribes to daemon events once (reference counted). Returns the disposer. */
  init: () => () => void;
}

let reloadQueued = false;
/** Ids that arrived by event while a List was in flight. */
let liveDuringLoad: Set<string> | null = null;
let subscribers = 0;
let unsubscribe: (() => void) | null = null;

export const useAgents = create<State>((set, get) => ({
  available: inDesktop,
  sessions: {},
  runs: [],
  detections: null,
  detecting: false,
  detectError: null,
  loaded: !inDesktop,
  loading: false,
  error: null,
  offline: false,
  projectFilter: null,
  openId: null,
  setProjectFilter: (projectFilter) => set({ projectFilter }),
  setOpenId: (openId) => set({ openId }),
  drop: (id) => set((s) => ({ sessions: removeSession(s.sessions, id), openId: s.openId === id ? null : s.openId })),
  launches: {},
  launchDraft: null,
  addLaunch: (launch) => set((s) => ({ launches: { ...s.launches, [launch.id]: launch }, launchDraft: null })),
  patchLaunch: (id, patch) => set((s) => s.launches[id] ? { launches: { ...s.launches, [id]: { ...s.launches[id], ...patch } } } : s),
  removeLaunch: (id) => set((s) => ({ launches: omitLaunch(s.launches, id), openId: s.openId === id ? null : s.openId })),
  setLaunchDraft: (launchDraft) => set({ launchDraft }),
  resolveLaunch: (id, session, open) => {
    get().upsert(session);
    set((s) => ({ launches: omitLaunch(s.launches, id), openId: s.openId === id ? (open ? session.id : null) : s.openId }));
  },

  async load() {
    if (!inDesktop) return;
    if (get().loading) { reloadQueued = true; return; }
    set({ loading: true });
    liveDuringLoad = new Set();
    try {
      const [list, runs] = await Promise.all([Agents.list(), Agents.runs()]);
      set((s) => ({ sessions: mergeList(s.sessions, list, liveDuringLoad ?? undefined), runs: runs.reduce(applyRunUpdate, s.runs), loaded: true, loading: false, error: null, offline: false }));
    } catch (e) {
      set({ loaded: true, loading: false, error: msg(e), offline: true });
    }
    liveDuringLoad = null;
    if (reloadQueued) { reloadQueued = false; await get().load(); }
  },

  refresh: () => get().load(),

  async detect(refresh) {
    if (!inDesktop) return;
    set({ detecting: true });
    try {
      set({ detections: await Agents.detect(refresh), detecting: false, detectError: null });
    } catch (e) {
      set({ detecting: false, detectError: msg(e) });
    }
  },

  upsert: (s) => { liveDuringLoad?.add(s.id); set((st) => ({ sessions: applySessionUpdate(st.sessions, s) })); },

  init() {
    if (!inDesktop) return () => {};
    if (subscribers++ === 0) {
      const offs = [
        events.agentsUpdated.on((s) => { liveDuringLoad?.add(s.id); set((st) => ({ sessions: applySessionUpdate(st.sessions, s) })); }),
        events.agentsDelta.on((d) => set((st) => ({ sessions: applySessionDelta(st.sessions, d) }))),
        events.agentsRemoved.on((r) => { liveDuringLoad?.add(r.id); set((st) => ({ sessions: removeSession(st.sessions, r.id), openId: st.openId === r.id ? null : st.openId })); }),
        events.agentsRun.on((r) => set((st) => ({ runs: applyRunUpdate(st.runs, r) }))),
        events.daemonState.on((l) => { if (l.connected) void get().load(); else set({ offline: true }); }),
      ];
      unsubscribe = () => offs.forEach((o) => o());
      void get().load();
    }
    let disposed = false;
    return () => {
      if (disposed) return;
      disposed = true;
      if (--subscribers === 0) { unsubscribe?.(); unsubscribe = null; }
    };
  },
}));

/** Counts for the sidebar badges and the status bar. The selector result is compared by value, so a session update that moves no count renders nothing. */
export function useAgentCounts(): AgentCounts {
  return useAgents(useShallow((s) => selectCounts(s.sessions)));
}
