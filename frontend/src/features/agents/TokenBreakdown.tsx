import type { ReactElement, ReactNode } from "react";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { costLabel } from "@/features/home/usageFormat";
import type { TokenTotals } from "@/mygo";

const COUNT = new Intl.NumberFormat("en-US");

/** What every token headline counts, and why cache reads are not in it. */
const HEADLINE_NOTE = "Input + cache write + output. Cache reads are left out: they are the context re-sent on each request.";

/**
 * The billed tokens behind a headline, kind by kind, with their cost. `pending` is what the headline holds beyond them: the
 * running turn's estimate, which the harness bills when the turn ends.
 */
export function TokenBreakdown({ totals, pending = 0 }: { totals: TokenTotals; pending?: number }) {
  const rows: [string, string][] = [
    ["Input", COUNT.format(totals.input)],
    ["Cache write", COUNT.format(totals.cacheWrite)],
    ["Cache read", COUNT.format(totals.cacheRead)],
    ["Output", COUNT.format(totals.output)],
  ];
  if (totals.costUsd > 0 || totals.unpriced.length > 0) rows.push(["Cost", costLabel(totals)]);
  return <div data-slot="token-breakdown" className="flex max-w-64 flex-col gap-1.5 py-0.5">
    <dl className="grid grid-cols-[auto_auto] gap-x-4 gap-y-0.5 tabular-nums">
      {rows.map(([label, value]) => <div key={label} className="contents">
        <dt className="text-muted-foreground">{label}</dt><dd className="text-right">{value}</dd>
      </div>)}
    </dl>
    {pending > 0 && <p className="text-muted-foreground">+{COUNT.format(pending)} estimated for the running turn, billed when it ends.</p>}
    {totals.unpriced.length > 0 && <p className="text-muted-foreground">No price for {totals.unpriced.join(", ")}, so the cost excludes their tokens.</p>}
    <p className="text-muted-foreground">{HEADLINE_NOTE}</p>
  </div>;
}

/** `render` (an element without its own interaction) showing `children`, with the token breakdown on hover once anything is billed. */
export function WithTokenBreakdown({ totals, pending, render, children }: { totals?: TokenTotals; pending?: number; render: ReactElement; children: ReactNode }) {
  return <Tooltip disabled={!totals}>
    <TooltipTrigger render={render}>{children}</TooltipTrigger>
    {totals && <TooltipPopup><TokenBreakdown totals={totals} pending={pending} /></TooltipPopup>}
  </Tooltip>;
}
