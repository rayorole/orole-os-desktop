import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { lazy, Suspense, useEffect, useRef, useState, type KeyboardEvent, type PointerEvent, type RefObject } from "react";
import { useChat, type ChatPane } from "@/stores/chat";
import { CallPane } from "@/features/jarvis/CallPane";
import { DocumentPane } from "./DocumentPane";

// Split out like the builder: the task feed and composer load only when a task is opened beside the conversation. ChatView
// warms the chunk on idle so the first open never waits on the network.
const TaskPane = lazy(() => import("./TaskPane").then((m) => ({ default: m.TaskPane })));
// noVNC and the Computer pane load only when a bot's computer is opened.
const ComputerTab = lazy(() => import("@/features/computer/ComputerTab").then((m) => ({ default: m.ComputerTab })));

/** The pane's share of the chat width until the user drags it to a width of their own. */
const SHARE = 0.45;
/** The narrowest the pane goes, and the room the conversation keeps (or 40% of the chat width when that is less). */
const MIN_PANE_REM = 20;
const MIN_CHAT_REM = 20;
/** How far one arrow key press moves the divider. */
const KEY_STEP = 32;
const OPEN_MS = 220;
const WIDTH_KEY = "orole.chat.paneWidth";
const paneKey = (pane: ChatPane) => pane.kind === "document" ? `document:${pane.messageId}:${pane.partIndex}` : `${pane.kind}:${pane.id}`;

const readWidth = (): number | null => {
  try {
    const value = Number(localStorage.getItem(WIDTH_KEY));
    return Number.isFinite(value) && value > 0 ? value : null;
  } catch { return null; }
};
const writeWidth = (width: number | null) => {
  try {
    if (width === null) localStorage.removeItem(WIDTH_KEY); else localStorage.setItem(WIDTH_KEY, String(Math.round(width)));
  } catch {
    // The width then lasts for the session only.
  }
};

/**
 * The chat container's width, so the pane knows how wide it may grow. Measured in a passive effect: a child's layout
 * effect runs before the parent's ref is attached, so it would never see the container.
 */
function useContainerWidth(container: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const node = container.current;
    if (!node) return;
    setWidth(node.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => { if (entry) setWidth(entry.contentRect.width); });
    observer.observe(node);
    return () => observer.disconnect();
  }, [container]);
  return width;
}

/**
 * What sits beside the conversation: a code task, a phone call's transcript, a bot's computer or a document. Like the settings panel it always takes its own column
 * and the conversation narrows to make room; it never lies over the thread. The divider on its left edge resizes it
 * (drag or arrow keys, double-click to reset) and the width is remembered. One host that stays mounted, so opening
 * widens it with an exit on close, and switching between panes crossfades the content instead of remounting the shell.
 * The heavy task body waits for the open animation (`ready`); focus moves to the pane's heading once it has settled and
 * returns to the opener on close.
 */
export function SidePane({ botId, container }: { botId: string; container: RefObject<HTMLDivElement | null> }) {
  const pane = useChat((s) => s.pane);
  const reduced = useReducedMotion();
  const containerWidth = useContainerWidth(container);
  const rem = typeof document === "undefined" ? 16 : parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
  const [preferred, setPreferred] = useState(readWidth);
  const [drag, setDrag] = useState<{ x: number; width: number } | null>(null);
  const max = Math.max(0, containerWidth - Math.min(MIN_CHAT_REM * rem, containerWidth * 0.4));
  const min = Math.min(MIN_PANE_REM * rem, max);
  const clamp = (value: number) => Math.round(Math.min(Math.max(value, min), max));
  const width = clamp(preferred ?? Math.max(24 * rem, containerWidth * SHARE));
  const [ready, setReady] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const open = pane !== null;
  // The opener is a row in the thread; it stays put, so focus moves to the pane once it is in place and back on close.
  useEffect(() => {
    if (open) { opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null; return; }
    setReady(false);
    opener.current?.focus();
    opener.current = null;
  }, [open]);
  // The animation's own completion is the signal; the timer covers an environment that never animates.
  useEffect(() => {
    if (!open || ready) return;
    const timer = window.setTimeout(() => setReady(true), reduced ? 0 : OPEN_MS + 40);
    return () => window.clearTimeout(timer);
  }, [open, ready, reduced]);
  useEffect(() => { if (ready) root.current?.querySelector<HTMLElement>("h2[tabindex]")?.focus({ preventScroll: true }); }, [ready]);
  const close = () => useChat.getState().closePane();

  const startDrag = (event: PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture(event.pointerId);
    setDrag({ x: event.clientX, width });
  };
  // The divider is on the pane's left edge: dragging left widens the pane.
  const follow = (event: PointerEvent<HTMLDivElement>) => { if (drag) setPreferred(clamp(drag.width + drag.x - event.clientX)); };
  const endDrag = () => {
    if (!drag) return;
    setDrag(null);
    writeWidth(width);
  };
  const onKey = (event: KeyboardEvent<HTMLDivElement>) => {
    const step = event.key === "ArrowLeft" ? KEY_STEP : event.key === "ArrowRight" ? -KEY_STEP : 0;
    const next = step ? clamp(width + step) : event.key === "Home" ? min : event.key === "End" ? max : null;
    if (next === null) return;
    event.preventDefault();
    setPreferred(next);
    writeWidth(next);
  };
  const reset = () => { setPreferred(null); writeWidth(null); };

  const transition = reduced || drag ? { duration: 0 } : { duration: OPEN_MS / 1000, ease: "easeOut" as const };
  return <AnimatePresence initial={false}>
    {pane && <motion.div key="side-pane" ref={root} data-slot="side-pane"
      className="relative z-10 shrink-0 overflow-hidden border-l bg-background"
      initial={{ width: 0 }}
      animate={{ width }}
      exit={{ width: 0 }}
      transition={transition}
      onAnimationComplete={() => { if (useChat.getState().pane) setReady(true); }}>
      <div role="separator" aria-orientation="vertical" aria-label="Resize side pane" tabIndex={0}
        aria-valuenow={width} aria-valuemin={Math.round(min)} aria-valuemax={Math.round(max)}
        data-slot="side-pane-resizer" data-dragging={drag ? "" : undefined}
        className="absolute inset-y-0 left-0 z-20 w-1.5 cursor-col-resize touch-none outline-none transition-colors hover:bg-ring/40 focus-visible:bg-ring/60 data-dragging:bg-ring/60 motion-reduce:transition-none"
        onPointerDown={startDrag} onPointerMove={follow} onPointerUp={endDrag} onPointerCancel={endDrag} onLostPointerCapture={endDrag}
        onKeyDown={onKey} onDoubleClick={reset} />
      <div className="h-full" style={{ width }}>
        <AnimatePresence mode="wait" initial={false}>
          <motion.div key={paneKey(pane)} className="h-full" initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={reduced ? { duration: 0 } : { duration: 0.12 }}>
            {pane.kind === "task"
              ? <Suspense fallback={null}><TaskPane taskId={pane.id} ready={ready} onClose={close} /></Suspense>
              : pane.kind === "call"
                ? <CallPane callId={pane.id} botId={botId} onClose={close} />
                : pane.kind === "computer"
                  ? <Suspense fallback={null}><ComputerTab botId={pane.id} onClose={close} /></Suspense>
                  : <DocumentPane botId={botId} messageId={pane.messageId} partIndex={pane.partIndex} onClose={close} />}
          </motion.div>
        </AnimatePresence>
      </div>
    </motion.div>}
  </AnimatePresence>;
}
