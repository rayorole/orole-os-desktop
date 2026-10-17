import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { CircleAlert, CircleCheck, CircleDot, CircleSlash, GitMerge, GitPullRequest, GitPullRequestClosed, GitPullRequestDraft, RefreshCw } from "lucide-react";
import type { Issue, Label, PullRequest } from "@/mygo";
import { ago } from "@/lib/projects";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner } from "@/components/ui/spinner";
import { errMsg, useGitHub } from "./store";

export const agoIso = (iso: string | null | undefined) => (iso ? ago(Date.parse(iso) / 1000) : "never");

export interface Async<T> { data: T | null; error: string | null; loading: boolean; reload: () => void }

/** Runs `fn` on mount and when deps / daemon data revision change. Stale results are dropped. */
export function useAsync<T>(fn: () => Promise<T>, deps: unknown[], enabled = true): Async<T> {
  const rev = useGitHub((s) => s.rev);
  const [state, setState] = useState<{ data: T | null; error: string | null; loading: boolean }>({ data: null, error: null, loading: true });
  const [tick, setTick] = useState(0);
  const fnRef = useRef(fn);
  fnRef.current = fn;
  useEffect(() => {
    if (!enabled) { setState({ data: null, error: null, loading: false }); return; }
    let live = true;
    setState({ data: null, loading: true, error: null });
    fnRef.current().then(
      (data) => live && setState({ data, error: null, loading: false }),
      (e) => live && setState({ data: null, error: errMsg(e), loading: false }),
    );
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, rev, tick, enabled]);
  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { ...state, reload };
}

export function Loading({ what }: { what: string }) {
  return <div role="status" className="flex items-center justify-center gap-2 p-6 text-muted-foreground text-sm"><Spinner className="size-4" />Loading {what}…</div>;
}

export function ErrorBox({ error, onRetry }: { error: string; onRetry?: () => void }) {
  return (
    <div role="alert" className="m-3 flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/8 p-3 text-sm">
      <CircleAlert className="mt-0.5 size-4 shrink-0 text-destructive-foreground" />
      <div className="min-w-0 flex-1"><p className="break-words text-destructive-foreground">{error}</p><p className="text-muted-foreground text-xs">Showing nothing rather than stale or sample data.</p></div>
      {onRetry && <Button size="xs" variant="outline" onClick={onRetry}>Retry</Button>}
    </div>
  );
}

export function EmptyLine({ children }: { children: ReactNode }) {
  return <p className="p-6 text-center text-muted-foreground text-sm">{children}</p>;
}

export function RefreshButton({ a }: { a: Async<unknown> }) {
  return <Button size="icon-xs" variant="ghost" aria-label="Refresh" onClick={a.reload} disabled={a.loading}><RefreshCw className={a.loading ? "animate-spin" : ""} /></Button>;
}

const HEX = /^[0-9a-fA-F]{6}$/;

/** A label's colour as CSS. It comes from GitHub, so it is validated before it reaches a style attribute. */
export const labelColor = (label: Pick<Label, "color">) => (HEX.test(label.color) ? `#${label.color}` : "#888888");

export function LabelChip({ label }: { label: Label }) {
  const c = labelColor(label);
  return (
    <Tooltip><TooltipTrigger render={<span tabIndex={0} className="inline-flex h-4.5 max-w-40 items-center gap-1 rounded-full border px-1.5 text-[11px] leading-none" style={{ borderColor: `${c}88`, background: `${c}22` }} />}><span className="size-1.5 shrink-0 rounded-full" style={{ background: c }} /><span className="truncate">{label.name}</span></TooltipTrigger><TooltipPopup><span className="max-w-sm whitespace-pre-wrap break-words">{label.description || label.name}</span></TooltipPopup></Tooltip>
  );
}

export function IssueStateIcon({ issue }: { issue: Pick<Issue, "state" | "state_reason"> }) {
  if (issue.state === "open") return <CircleDot className="size-4 shrink-0 text-success-foreground" aria-label="open" />;
  if (issue.state_reason === "not_planned") return <CircleSlash className="size-4 shrink-0 text-muted-foreground" aria-label="closed as not planned" />;
  return <CircleCheck className="size-4 shrink-0 text-info-foreground" aria-label="closed" />;
}

export function PullStateIcon({ pr }: { pr: Pick<PullRequest, "state" | "draft" | "merged" | "merged_at"> }) {
  if (pr.merged || pr.merged_at) return <GitMerge className="size-4 shrink-0 text-info-foreground" aria-label="merged" />;
  if (pr.state === "closed") return <GitPullRequestClosed className="size-4 shrink-0 text-destructive-foreground" aria-label="closed" />;
  if (pr.draft) return <GitPullRequestDraft className="size-4 shrink-0 text-muted-foreground" aria-label="draft" />;
  return <GitPullRequest className="size-4 shrink-0 text-success-foreground" aria-label="open" />;
}

export function ReviewBadge({ state }: { state: string }) {
  if (state === "approved") return <Badge variant="success">Approved</Badge>;
  if (state === "changes_requested") return <Badge variant="error">Changes requested</Badge>;
  if (state === "commented") return <Badge variant="info">Reviewed</Badge>;
  return <Badge variant="outline">No reviews</Badge>;
}

/** CI conclusion for a workflow run or check. */
export function CiBadge({ status, conclusion }: { status: string; conclusion: string }) {
  if (status !== "completed") return <Badge variant="warning"><Spinner size={12} />{status.replace("_", " ")}</Badge>;
  if (conclusion === "success") return <Badge variant="success">passed</Badge>;
  if (conclusion === "skipped" || conclusion === "neutral" || conclusion === "cancelled") return <Badge variant="outline">{conclusion}</Badge>;
  return <Badge variant="error">{conclusion.replace("_", " ") || "failed"}</Badge>;
}

/** Splits an API issue/notification URL such as .../repos/o/r/issues/5 into parts. */
export function parseSubjectUrl(url: string): { repo: string; kind: "issue" | "pr"; number: number } | null {
  const m = /\/repos\/([^/]+\/[^/]+)\/(issues|pulls)\/(\d+)$/.exec(url);
  return m ? { repo: m[1]!, kind: m[2] === "pulls" ? "pr" : "issue", number: +m[3]! } : null;
}

export function Header({ title, count, children }: { title: string; count?: ReactNode; children?: ReactNode }) {
  return (
    <div className="flex h-9 shrink-0 items-center gap-2 border-b px-3">
      <h2 className="font-medium text-[13px]">{title}</h2>{count}
      <span className="flex-1" />{children}
    </div>
  );
}
