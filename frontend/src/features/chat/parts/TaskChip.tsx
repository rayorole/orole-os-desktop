import { CheckIcon, CircleIcon, CircleXIcon, CoinsIcon, CpuIcon, LoaderCircleIcon, TimerIcon, TriangleAlertIcon } from "lucide-react";
import { useEffect, useState } from "react";
import { formatElapsed } from "@/components/assistant-ui/elements/working-status";
import type { AgentsStatus, MsgPart, Session } from "@/mygo";
import { ProviderLogo } from "@/features/agents/ProviderLogo";
import { kindLabel, sessionTitle } from "@/features/agents/format";
import { WithTokenBreakdown } from "@/features/agents/TokenBreakdown";
import { useHarnessOptions } from "@/features/agents/useHarnessOptions";
import { cn } from "@/lib/utils";
import { useChat } from "@/stores/chat";
import { TASK_STATUS_LABEL, useTaskSessions } from "../taskSession";
import { modelLogo } from "../botLogos";
import { modelLabel } from "@/features/home/model-options";
import { formatTokens, isTaskLive, NO_VALUE, taskElapsedSeconds } from "../taskMeter";
import { Bubble } from "./Bubble";

/** Spinner while the agent works, a check when its turn is done, a warning when it needs you, a cross when it failed. */
export function TaskStatusGlyph({ status, className }: { /** absent while the task is still loading */ status?: AgentsStatus; className?: string }) {
  const base = cn("size-3.5 shrink-0", className);
  switch (status) {
    case undefined:
    case "working":
      return <LoaderCircleIcon aria-hidden className={cn(base, "animate-spin motion-reduce:animate-none", status ? "text-info" : "text-muted-foreground")} />;
    case "done":
      return <CheckIcon aria-hidden className={cn(base, "text-success")} />;
    case "needs-you":
      return <TriangleAlertIcon aria-hidden className={cn(base, "text-warning")} />;
    case "failed":
      return <CircleXIcon aria-hidden className={cn(base, "text-destructive")} />;
    default:
      return <CircleIcon aria-hidden className={cn(base, "text-muted-foreground")} />;
  }
}

/** The coding agent a task runs on, as a small logo-and-name badge. */
export function HarnessBadge({ kind }: { kind: string }) {
  return <span data-slot="harness-badge" className="inline-flex shrink-0 items-center gap-1 rounded-md bg-muted px-1.5 py-0.5 text-[11px] leading-none text-muted-foreground [&>span]:size-3 [&>svg]:size-3">
    <ProviderLogo kind={kind} />{kindLabel(kind)}
  </span>;
}

type RowStatus = "running" | "needs-you" | "done" | "failed";
const ROW_STATUS: Record<AgentsStatus, RowStatus> = { idle: "running", working: "running", "needs-you": "needs-you", done: "done", failed: "failed", stopped: "failed", lost: "failed" };

/** Re-renders once a second while `live`, so an elapsed time counts like a meter; returns the current time (Unix ms). */
function useNow(live: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!live) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [live]);
  return now;
}

/**
 * Model, elapsed time and tokens of a task as a compact live meter; a reading the daemon has not reported shows a dash. Elapsed and
 * tokens freeze once the task stops. Tokens leave cache reads out; hovering them shows the billed breakdown.
 */
function TaskMeter({ session }: { session: Session }) {
  const now = useNow(isTaskLive(session));
  const seconds = taskElapsedSeconds(session, now);
  const tokens = session.tokens;
  // The harness's catalog names an alias ("opus") after the model it runs, so the label is right before the agent reports its model id.
  const { options } = useHarnessOptions(session.machine || "local");
  const model = options?.harnesses.find((h) => h.id === session.kind)?.models.find((m) => m.id === session.model) ?? { id: session.model, name: "" };
  const item = "inline-flex min-w-0 items-center gap-1";
  return <div data-slot="task-meter" className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-0.5 pl-5.5 text-[11px] leading-4 text-muted-foreground tabular-nums">
    <span data-slot="task-model" className={item}>
      {(session.model && modelLogo(session.kind, model, "size-3")) || <CpuIcon aria-hidden className="size-3 shrink-0" />}
      <span className="sr-only">Model: </span><span className="truncate">{session.model ? modelLabel(model) : NO_VALUE}</span>
    </span>
    <span data-slot="task-elapsed" className={item}>
      <TimerIcon aria-hidden className="size-3 shrink-0" /><span className="sr-only">Elapsed: </span>{seconds === undefined ? NO_VALUE : formatElapsed(seconds)}
    </span>
    <WithTokenBreakdown totals={session.billed} pending={(tokens ?? 0) - (session.billed?.work ?? 0)} render={<span data-slot="task-tokens" className={item} />}>
      <CoinsIcon aria-hidden className="size-3 shrink-0" /><span className="sr-only">Tokens: </span>{tokens ? formatTokens(tokens) : NO_VALUE}
    </WithTokenBreakdown>
  </div>;
}

/** What a task's row says under its title: the agent's current activity, or how many background agents a settled turn still waits for. */
export function taskActivity(session: Session | undefined, missing: boolean): string | undefined {
  if (missing) return "This task is no longer available";
  if (!session) return "Loading…";
  if (session.status === "working" && session.subagents) return `${session.subagents} background ${session.subagents === 1 ? "agent" : "agents"} running`;
  if (session.status === "working") return session.activity || undefined;
  if (session.status === "needs-you") return session.activity || TASK_STATUS_LABEL[session.status];
  return session.status === "done" ? undefined : TASK_STATUS_LABEL[session.status];
}

/**
 * The tasks one reply started, one chat bubble each (live status, title, activity, harness, model, elapsed time, tokens);
 * clicking a bubble opens that task beside the conversation.
 */
export function TaskChips({ parts }: { parts: readonly MsgPart[] }) {
  const ids = parts.map((part) => part.id ?? "");
  const { sessions, missing } = useTaskSessions(ids);
  const openId = useChat((s) => s.pane?.kind === "task" ? s.pane.id : null);
  return <ul aria-label="Code tasks" data-slot="task-list" className="flex w-full min-w-0 flex-col items-start gap-1.5">
    {parts.map((part, i) => {
      const session = sessions[i];
      const gone = !session && missing[ids[i]!] === true;
      const kind = session?.kind || part.name || "";
      const status = gone ? "failed" : session ? ROW_STATUS[session.status] : "running";
      const activity = taskActivity(session, gone);
      const title = session ? sessionTitle(session) : part.title || "Code task";
      const selected = ids[i] !== "" && ids[i] === openId;
      return <li key={i} className="flex max-w-full min-w-0">
        <Bubble className={cn("p-0", selected && "ring-1 ring-ring/60")}>
          <button type="button" data-slot="subagent-row" data-status={status} aria-pressed={selected} onClick={() => { if (ids[i]) useChat.getState().openTaskPane(ids[i]!); }}
            className="flex min-w-56 w-full flex-col gap-1 rounded-[inherit] px-3 py-1.5 text-start outline-none transition-colors hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring motion-reduce:transition-none">
            <span className="flex min-w-0 items-center gap-2">
              <TaskStatusGlyph status={session?.status ?? (gone ? "failed" : undefined)} />
              <span className="min-w-0 flex-1 truncate text-sm leading-5">{title}</span>
              {kind && <HarnessBadge kind={kind} />}
            </span>
            {activity && <span className={cn("block truncate pl-5.5 text-xs leading-4 text-muted-foreground", status === "running" && "cv-shimmer")}>{activity}</span>}
            {session && <TaskMeter session={session} />}
          </button>
        </Bubble>
      </li>;
    })}
  </ul>;
}
