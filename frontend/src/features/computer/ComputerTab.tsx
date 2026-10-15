import { useEffect, useState, type FormEvent } from "react";
import { ChevronLeftIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { TerminalPane } from "@/features/terminals/TerminalPane";
import { Sandbox } from "@/mygo";
import { useSettings } from "@/stores/settings";
import { computerView, errorText, runtimeProgress, SECURITY_NOTE, setupCopy, setupError, type ComputerView } from "./computerState";
import { LiveScreen } from "./LiveScreen";
import { useComputer, useViewerLease } from "./useComputer";

/** The longest note Done sends to the bot (the daemon's limit). */
const NOTE_MAX = 500;

type Computer = ReturnType<typeof useComputer>;

/**
 * A bot's computer beside its conversation: the setup screen until this PC can run one, otherwise its state, and
 * while it runs the live screen (view-only until Take over), Take over / Done, and a terminal in the computer.
 * Nothing else by design: no start, stop or reset, and the screen and terminal open only while it runs; the computer
 * starts when the bot uses it. The daemon decides what a takeover allows; this only asks.
 */
export function ComputerTab({ botId, onClose }: { botId: string; onClose: () => void }) {
  const computer = useComputer(botId);
  const { availability, status, handoff, error, refresh } = computer;
  useViewerLease(botId);
  const view = computerView(availability, status, handoff);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  // Closing the pane never releases a takeover: Done would tell a waiting bot the user finished. The daemon ends an
  // abandoned takeover itself (viewer lease expiry, or the bot's own hold timeout when it asked).

  const runAction = async (call: () => Promise<void>) => {
    setBusy(true);
    setActionError(null);
    try {
      await call();
      await refresh();
    } catch (e) {
      setActionError(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const takeOver = () => void runAction(() => Sandbox.takeOver(botId));
  const done = (note: string) => void runAction(() => Sandbox.release(botId, note));

  return <section aria-labelledby="side-pane-title" data-slot="computer-pane" className="flex h-full min-w-0 flex-col">
    <header className="flex h-10 shrink-0 items-center gap-2 border-b px-3">
      <Button type="button" variant="ghost" size="icon-xs" aria-label="Close computer" onClick={onClose}><ChevronLeftIcon aria-hidden /></Button>
      <h2 id="side-pane-title" tabIndex={-1} className="truncate text-sm font-medium outline-none">Computer</h2>
      {view.kind === "running" && !view.takenOver && !view.waitingForUser
        && <Button type="button" size="xs" variant="outline" className="ml-auto" disabled={busy} onClick={takeOver}>Take over</Button>}
    </header>
    <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-auto p-2">
      {actionError && <p role="alert" className="selectable px-1 text-xs text-destructive-foreground">{actionError}</p>}
      {view.kind !== "loading" && view.kind !== "setup" && error && <p role="alert" className="selectable px-1 text-xs text-destructive-foreground">{error}</p>}
      {view.kind === "loading"
        ? error
          ? <div className="flex flex-col items-start gap-2 p-2"><p role="alert" className="selectable text-sm text-destructive-foreground">{error}</p><Button type="button" size="xs" variant="outline" onClick={() => void refresh()}>Try again</Button></div>
          : <p role="status" className="p-2 text-sm text-muted-foreground">Loading…</p>
        : view.kind === "setup"
          ? <Setup reason={view.reason} detail={view.detail} computer={computer} />
          : view.kind === "running"
            ? <>
              {view.waitingForUser && <div role="status" data-slot="computer-handoff" className="flex items-start gap-2 rounded-lg border border-warning/40 bg-warning/8 p-2.5 text-sm">
                <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <p className="font-medium">The bot needs you</p>
                  <p className="selectable text-muted-foreground">{view.handoffReason || "The bot asked you to take over its computer."}</p>
                </div>
                <Button type="button" size="xs" disabled={busy} onClick={takeOver}>Take over</Button>
              </div>}
              {view.takenOver && <Takeover pending={handoff?.pending === true} reason={view.handoffReason} busy={busy} onDone={done} />}
              <LiveScreen botId={botId} interactive={view.takenOver} />
              <ComputerTerminal botId={botId} />
            </>
            : <StateNote view={view} />}
    </div>
    <p data-slot="computer-security-note" className="shrink-0 border-t px-3 py-1.5 text-xs text-muted-foreground">{SECURITY_NOTE}</p>
  </section>;
}

/** While the user has the computer: what that means, the bot's reason if it asked, and Done with an optional note. */
function Takeover({ pending, reason, busy, onDone }: { pending: boolean; reason?: string; busy: boolean; onDone: (note: string) => void }) {
  const [note, setNote] = useState("");
  const submit = (event: FormEvent) => {
    event.preventDefault();
    onDone(note.trim().slice(0, NOTE_MAX));
  };
  return <form data-slot="computer-takeover" onSubmit={submit} className="flex flex-col gap-1.5 rounded-lg border p-2 text-xs">
    <p role="status" className="text-muted-foreground">
      {pending ? "You have the mouse and keyboard. The bot waits until you click Done." : "You have the mouse and keyboard. The bot can't use the computer until you click Done. If you leave this tab or minimise the window for about 2½ minutes, the bot gets it back automatically."}
    </p>
    {reason && <p className="selectable"><span className="text-muted-foreground">The bot asked: </span>{reason}</p>}
    <div className="flex items-center gap-2">
      <Input size="sm" aria-label="Note for the bot" placeholder="Note for the bot (optional)" maxLength={NOTE_MAX}
        value={note} onChange={(e) => setNote(e.currentTarget.value)} />
      <Button type="submit" size="xs" disabled={busy}>Done</Button>
    </div>
  </form>;
}

function StateNote({ view }: { view: Extract<ComputerView, { kind: "off" | "starting" | "stopping" | "failed" }> }) {
  if (view.kind === "failed") {
    return <div role="status" className="flex flex-col gap-1 p-2 text-sm">
      <p className="selectable text-destructive-foreground">{view.error}</p>
      <p className="text-muted-foreground">It starts again the next time the bot uses it.</p>
    </div>;
  }
  const text = view.kind === "starting" ? "Starting computer…"
    : view.kind === "stopping" ? "The computer is shutting down…"
      : "The computer is off. It starts when the bot uses it.";
  return <p role="status" className="p-2 text-sm text-muted-foreground">{text}</p>;
}

/**
 * Why this PC can't run the computer yet and the one step that fixes it. Enable and Download return at once; their
 * progress and errors arrive as events through useComputer.
 */
function Setup({ reason, detail, computer }: { reason: string; detail: string; computer: Computer }) {
  const { runtime, setup, error, enable, install, refresh } = computer;
  const copy = setupCopy(reason, detail);
  const [busy, setBusy] = useState(false);
  const progress = copy.action === "install" ? runtimeProgress(runtime) : null;
  const failure = setupError(runtime, setup);
  const shown = failure ? errorText(failure) : error;
  const working = busy || (copy.action === "install" && !!runtime?.installing) || (copy.action === "enable" && !!setup?.running);
  const run = async () => {
    setBusy(true);
    try {
      if (copy.action === "enable") await enable();
      else if (copy.action === "install") await install();
      // Enabling may end in a restart step; installing reports through events.
      if (copy.action !== "install") await refresh();
    } finally {
      setBusy(false);
    }
  };
  const label = working
    ? copy.action === "install" ? "Downloading…" : copy.action === "enable" ? "Enabling…" : "Checking…"
    : copy.actionLabel;
  return <div className="flex max-w-md flex-col items-start gap-2 p-2">
    <h3 className="text-sm font-medium">{copy.title}</h3>
    <p className="selectable text-sm text-muted-foreground">{copy.body}</p>
    {copy.action === "enable" && setup?.running && <p className="text-xs text-muted-foreground">Approve the Windows prompt to continue.</p>}
    {progress && <div className="flex w-full flex-col gap-1 text-xs text-muted-foreground">
      <div className="flex justify-between gap-2"><span>{progress.label}</span>{progress.percent !== null && <span className="tabular-nums">{progress.percent}%</span>}</div>
      <Progress value={progress.percent} aria-label={progress.label} />
    </div>}
    {shown && <p role="alert" className="selectable text-xs text-destructive-foreground">{shown}</p>}
    {copy.action && <Button type="button" size="sm" disabled={working} onClick={() => void run()}>{label}</Button>}
  </div>;
}

/** The terminal inside the computer, a daemon terminal like any other: it stays open when the pane closes and ends with the computer. */
function ComputerTerminal({ botId }: { botId: string }) {
  const fontSize = useSettings((s) => s.terminal.fontSize);
  const [id, setId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [active, setActive] = useState(false);
  useEffect(() => {
    let live = true;
    setId(null);
    setError(null);
    Sandbox.openTerminal(botId, 100, 24).then(
      (info) => { if (live) setId(info.id); },
      (e: unknown) => { if (live) setError(errorText(e)); },
    );
    return () => { live = false; };
  }, [botId, attempt]);
  // Highlighted while it has the keyboard, like the Terminals view; clicking the screen moves focus away.
  return <div data-slot="computer-terminal" className="flex min-h-40 flex-[2] flex-col"
    onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setActive(false); }}>
    {id
      ? <TerminalPane id={id} active={active} onFocus={() => setActive(true)} fontSize={fontSize} />
      : error
        ? <div className="flex items-center gap-2 p-2 text-sm"><p role="alert" className="selectable text-destructive-foreground">The terminal could not open: {error}</p><Button type="button" size="xs" variant="outline" onClick={() => setAttempt((a) => a + 1)}>Retry</Button></div>
        : <p role="status" className="p-2 text-sm text-muted-foreground">Opening the terminal…</p>}
  </div>;
}
