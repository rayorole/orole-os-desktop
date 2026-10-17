/*
 * Orole's own agent run tree. Its layout follows the public demo of the AI SDK Agents "Agent run tree" component
 * (https://www.aisdkagents.com/ai-components/); none of that (paid) source was copied.
 */
import { CheckIcon, ChevronDownIcon, ChevronRightIcon, CircleMinusIcon, ShieldAlertIcon, XIcon } from "lucide-react";
import { useId, useState, type ComponentProps, type ReactNode } from "react";
import { Collapsible, CollapsiblePanel, CollapsibleTrigger } from "@/components/ui/collapsible";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { surfaces } from "./surfaces";
import { Elapsed } from "./working-status";

/** `waiting`: the agent needs the user. `idle`: reported but not started, or with no reported state. */
export type AgentState = "running" | "done" | "failed" | "waiting" | "cancelled" | "idle";

const STATE_LABEL: Record<AgentState, string> = { running: "Running", done: "Done", failed: "Failed", waiting: "Needs you", cancelled: "Stopped", idle: "Not started" };
const TOKENS = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

/** A dashed ring that turns while running (still when idle), a check when done, a cross when failed, a shield when it needs you. */
export function AgentStateGlyph({ state, className }: { state: AgentState; className?: string }) {
  const base = cn("size-4 shrink-0", className);
  switch (state) {
    case "running":
    case "idle": return <svg aria-hidden viewBox="0 0 16 16" className={cn(base, "text-muted-foreground", state === "running" ? "animate-spin [animation-duration:2.4s] motion-reduce:animate-none" : "opacity-60")}>
      <circle cx="8" cy="8" r="6" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeDasharray="2.2 2.5" />
    </svg>;
    case "done": return <CheckIcon aria-hidden strokeWidth={2.25} className={cn(base, "text-success")} />;
    case "failed": return <XIcon aria-hidden strokeWidth={2.25} className={cn(base, "text-destructive-foreground")} />;
    case "waiting": return <ShieldAlertIcon aria-hidden className={cn(base, "text-warning-foreground")} />;
    case "cancelled": return <CircleMinusIcon aria-hidden className={cn(base, "text-muted-foreground")} />;
  }
}

/** Siblings hanging off the glyph above them: each draws an elbow to its row, and the line runs on to the next sibling. */
const BRANCH = "relative min-w-0 ps-5 before:pointer-events-none before:absolute before:start-0 before:top-0 before:h-3.5 before:w-3 before:rounded-es-md before:border-s before:border-b before:border-border after:pointer-events-none after:absolute after:start-0 after:top-0 after:bottom-0 after:border-s after:border-border last:after:hidden";

/** The list of `AgentNode`s under a run, a node or a tool call. */
export function AgentNodes({ className, ...props }: ComponentProps<"ul">) {
  return <ul data-slot="agent-nodes" className={cn("m-0 flex min-w-0 list-none flex-col p-0", className)} {...props} />;
}

/**
 * One agent of the tree: state glyph, bold name, what it is doing now or its one-line result, elapsed time and, when reported,
 * tokens. It opens into its task, its steps (`children`), its result in a muted box and its child agents (`nodes`). With
 * `collapsible={false}` it is always open and its row is not a control. Render inside `AgentNodes`.
 */
export function AgentNode({ name, state, summary, start, end, tokens, task, result, nodes, children, collapsible = true, defaultOpen = false, className }: {
  name: ReactNode;
  state: AgentState;
  /** The current step while running, the one-line result once done. */
  summary?: ReactNode;
  /** When it started and ended (Unix ms); a running node ticks from `start`. */
  start?: number | null;
  end?: number | null;
  /** Only when the harness reports them for this agent. */
  tokens?: number;
  task?: ReactNode;
  result?: ReactNode;
  /** Child agents: `AgentNode` elements. */
  nodes?: ReactNode;
  /** The agent's steps, in order. */
  children?: ReactNode;
  collapsible?: boolean;
  defaultOpen?: boolean;
  className?: string;
}) {
  const running = state === "running";
  const line = <>
    <AgentStateGlyph state={state} />
    <span className="sr-only">{STATE_LABEL[state]}: </span>
    <span className="shrink-0 font-medium text-foreground">{name}</span>
    {summary ? <span className={cn("min-w-0 flex-1 truncate text-muted-foreground", running && "cv-shimmer")}>{summary}</span> : <span className="flex-1" />}
    <Elapsed start={start} end={end} live={running} className="text-xs text-muted-foreground" />
    {tokens !== undefined && <span className="shrink-0 text-xs tabular-nums text-muted-foreground">{TOKENS.format(tokens)}</span>}
  </>;
  const hasBody = Boolean(task || children || result);
  const body = <>
    {hasBody && <div className={cn("ms-2 flex min-w-0 flex-col gap-2 pb-1.5 ps-4 pt-0.5", nodes ? "border-s border-border" : undefined)}>
      {task && <p className="selectable m-0 whitespace-pre-wrap break-words text-[13px] text-muted-foreground">{task}</p>}
      {children}
      {result && <div data-slot="agent-result" className="selectable min-w-0 rounded-lg bg-muted/60 px-3 py-2 text-[13px]">{result}</div>}
    </div>}
    {nodes && <AgentNodes className="ms-2">{nodes}</AgentNodes>}
  </>;
  const rowClass = "flex w-full min-w-0 items-center gap-2 py-1 text-left text-sm";
  return <li data-slot="agent-node" data-state={state} className={cn(BRANCH, className)}>
    {collapsible && (hasBody || nodes) ? <Collapsible defaultOpen={defaultOpen}>
      <CollapsibleTrigger className={cn(rowClass, "group/node -mx-1 w-[calc(100%+0.5rem)] rounded-md px-1 outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring")}>
        {line}
        <ChevronRightIcon aria-hidden className="size-3.5 shrink-0 text-muted-foreground transition-transform duration-150 group-data-[panel-open]/node:rotate-90 motion-reduce:transition-none" />
      </CollapsibleTrigger>
      <CollapsiblePanel>{body}</CollapsiblePanel>
    </Collapsible> : <>
      <div className={rowClass}>{line}</div>
      {body}
    </>}
  </li>;
}

/**
 * One step inside an agent: a small mark and a label, opening into `details` when given; `children` show under (or, without a
 * label, beside) the mark at all times, such as a message the agent wrote while working.
 */
export function AgentStep({ icon, label, details, live = false, children, className }: {
  icon: ReactNode;
  label?: ReactNode;
  details?: ReactNode;
  /** In flight: the label shimmers. */
  live?: boolean;
  children?: ReactNode;
  className?: string;
}) {
  const mark = <span aria-hidden className="grid size-4 shrink-0 place-items-center text-muted-foreground/80 [&>svg]:size-3.5">{icon}</span>;
  if (label === undefined) return <div data-slot="agent-step" className={cn("flex min-w-0 items-start gap-2 py-0.5", className)}>
    <span className="mt-0.5">{mark}</span>
    <div className="min-w-0 flex-1">{children}</div>
  </div>;
  const text = <span className={cn("min-w-0 truncate text-foreground/85", live && "cv-shimmer")}>{label}</span>;
  const row = "flex w-full min-w-0 items-center gap-2 rounded-md py-0.5 text-left text-[13px]";
  return <div data-slot="agent-step" className={cn("min-w-0", className)}>
    {details ? <Collapsible>
      <CollapsibleTrigger className={cn(row, "group/step -mx-1 w-[calc(100%+0.5rem)] px-1 outline-none hover:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring")}>
        {mark}{text}
        <ChevronRightIcon aria-hidden className="size-3.5 shrink-0 text-muted-foreground/70 transition-transform duration-150 group-data-[panel-open]/step:rotate-90 motion-reduce:transition-none" />
      </CollapsibleTrigger>
      <CollapsiblePanel><div className="mb-1.5 ms-6 mt-1 min-w-0">{details}</div></CollapsiblePanel>
    </Collapsible> : <div className={row}>{mark}{text}</div>}
    {children && <div className="ms-6 min-w-0">{children}</div>}
  </div>;
}

/** Content cut to its first lines under a fade while `foldable`, with a Show all / Show less toggle. */
export function FadeFold({ foldable, collapsedClassName = "max-h-44", children, className }: {
  foldable: boolean;
  /** The height of the folded content. */
  collapsedClassName?: string;
  children: ReactNode;
  className?: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const id = useId();
  const folded = foldable && !expanded;
  return <div data-slot="fade-fold" className={cn("min-w-0", className)}>
    <div id={id} className={cn("relative min-w-0", folded && cn("overflow-hidden", collapsedClassName))}>
      {children}
      {folded && <div aria-hidden className="pointer-events-none absolute inset-x-0 bottom-0 h-14 bg-linear-to-b from-transparent to-card" />}
    </div>
    {foldable && <button type="button" aria-expanded={expanded} aria-controls={id} onClick={() => setExpanded((value) => !value)}
      className="mt-1 rounded-md px-1 py-0.5 text-xs font-medium text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
      {expanded ? "Show less" : "Show all"}
    </button>}
  </div>;
}

/** A run's final answer: a card whose first lines show under a fade when it is long, the rest one click away. */
export function RunResult({ foldable, children, className, ...props }: ComponentProps<"div"> & { foldable: boolean }) {
  return <div data-slot="run-result" className={cn(surfaces.paper, "min-w-0 px-4 py-3", className)} {...props}>
    <FadeFold foldable={foldable} collapsedClassName="max-h-48">{children}</FadeFold>
  </div>;
}

/**
 * A run of one or more agents as a card. The header always says what is happening now (`title`), with the elapsed time and
 * tokens on the right; it is also the disclosure of the tree below (`children`: `AgentNode`s). The caller owns `open`, so a
 * running run can stay open and a settled one fold to its header line. Only `status` is announced, never a clock tick.
 */
export function AgentRunTree({ state, title, status, elapsed, tokens, tokensDetail, open, onOpenChange, children, className, ...props }: Omit<ComponentProps<"section">, "title" | "children"> & {
  state: AgentState;
  title: ReactNode;
  /** What a screen reader hears when the run changes step or settles. */
  status: string;
  elapsed?: ReactNode;
  /** Tokens to show next to the time; omitted when unknown. */
  tokens?: number;
  /** What the tokens are made of, shown on hover over them. */
  tokensDetail?: ReactNode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  const running = state === "running";
  return <section data-slot="agent-run-tree" data-state={state} aria-busy={running} className={cn(surfaces.paper, "min-w-0", className)} {...props}>
    <Collapsible open={open} onOpenChange={onOpenChange}>
      <CollapsibleTrigger className="group/run flex w-full min-w-0 items-center gap-2.5 rounded-xl px-3 py-2 text-left outline-none hover:bg-muted/30 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
        <span className="grid size-6 shrink-0 place-items-center rounded-md bg-muted"><AgentStateGlyph state={state} className="size-3.5" /></span>
        <span className={cn("min-w-0 flex-1 truncate text-sm font-medium", running && "cv-shimmer", state === "failed" && "text-destructive-foreground", state === "waiting" && "text-warning-foreground")}>{title}</span>
        {(elapsed || tokens !== undefined) && <span data-slot="run-meta" className="flex shrink-0 items-center gap-1 text-xs tabular-nums text-muted-foreground">
          {elapsed}
          {elapsed && tokens !== undefined && <span aria-hidden>·</span>}
          {tokens !== undefined && <Tooltip disabled={!tokensDetail}>
            <TooltipTrigger render={<span data-slot="run-tokens" />}>{TOKENS.format(tokens)} tokens</TooltipTrigger>
            {tokensDetail && <TooltipPopup>{tokensDetail}</TooltipPopup>}
          </Tooltip>}
        </span>}
        <ChevronDownIcon aria-hidden className="size-3.5 shrink-0 -rotate-90 text-muted-foreground transition-transform duration-150 group-data-[panel-open]/run:rotate-0 motion-reduce:transition-none" />
      </CollapsibleTrigger>
      <CollapsiblePanel>
        <div className="px-3 pb-3">
          <AgentNodes className="relative ms-3 before:absolute before:start-0 before:-top-2 before:h-2 before:border-s before:border-border">{children}</AgentNodes>
        </div>
      </CollapsiblePanel>
    </Collapsible>
    <span role="status" className="sr-only">{status}</span>
  </section>;
}
