import type { ReactNode } from "react";
import { ArrowUpRight, ChartBar, ChevronDown, CircleAlert, Coins, Info, Layers, Rows3, Sigma, Wallet } from "lucide-react";
import { Menu, MenuPopup, MenuRadioGroup, MenuRadioItem, MenuTrigger } from "@/components/ui/menu";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { Widget } from "@/components/widgets/Widget";
import type { WidgetProps } from "@/components/widgets/registry";
import { ago, baseName, kindLabel, useNow } from "@/features/agents/format";
import { ProviderLogo } from "@/features/agents/ProviderLogo";
import { WithTokenBreakdown } from "@/features/agents/TokenBreakdown";
import { openTask } from "@/features/projects/tasks";
import { cn } from "@/lib/utils";
import type { DailyUsage, SessionUsage, TokenTotals, UsageStats } from "@/mygo";
import { useUI } from "@/stores/ui";
import { compactNumber, costLabel, sparkSeries, usd } from "./usageFormat";
import { useUsageStats, type UsageRange, type UsageResult } from "./useUsageStats";

const RANGES: Record<UsageRange, { label: string; title: string; spark: string; window: "day" | "week" | "all" }> = {
  "24h": { label: "24h", title: "24h", spark: "last 7 days", window: "day" },
  "7d": { label: "7 days", title: "7 days", spark: "last 7 days", window: "week" },
  all: { label: "All time", title: "all time", spark: "last 12 months", window: "all" },
};
const COUNT = new Intl.NumberFormat("en-US");
const PRICES_DATE = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

/** Harness colors for the By agent bars; an unknown harness uses the muted foreground. */
const ACCENT: Record<string, string> = {
  claude: "#d97757",
  codex: "#8b9cf7",
  gemini: "#4f8df5",
  opencode: "#a1a1aa",
  omp: "#c084fc",
};

const rangeOf = (config: WidgetProps["config"]): UsageRange => (config?.range === "7d" || config?.range === "all" ? config.range : "24h");
const plural = (n: number, one: string) => `${COUNT.format(n)} ${n === 1 ? one : `${one}s`}`;
const headerButton = "inline-flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring";

function RangeMenu({ range, onRange }: { range: UsageRange; onRange: (range: UsageRange) => void }) {
  return (
    <Menu>
      <MenuTrigger render={<button type="button" aria-label={`Range: ${RANGES[range].label}`} className="inline-flex h-6 items-center gap-1 rounded-full border bg-card px-2.5 text-[11px] text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring data-popup-open:bg-accent" />}>
        {RANGES[range].label}<ChevronDown className="size-3 opacity-70" />
      </MenuTrigger>
      <MenuPopup align="end" className="min-w-32">
        <MenuRadioGroup value={range} onValueChange={(value: UsageRange) => onRange(value)}>
          {(Object.keys(RANGES) as UsageRange[]).map((r) => <MenuRadioItem key={r} value={r} closeOnClick>{RANGES[r].label}</MenuRadioItem>)}
        </MenuRadioGroup>
      </MenuPopup>
    </Menu>
  );
}

function Chip({ tone = "muted", hint, children }: { tone?: "muted" | "warning"; hint?: string; children: ReactNode }) {
  const className = cn("inline-flex h-6 max-w-full min-w-0 items-center rounded-md px-2 text-xs tabular-nums", tone === "warning" ? "bg-amber-500/15 text-amber-300" : "bg-foreground/[0.07] text-foreground/80");
  if (!hint) return <span className={className}><span className="truncate">{children}</span></span>;
  return (
    <Tooltip>
      <TooltipTrigger render={<span className={className} />}><span className="truncate">{children}</span></TooltipTrigger>
      <TooltipPopup className="max-w-xs">{hint}</TooltipPopup>
    </Tooltip>
  );
}

/** Plain SVG line over the series' own scale that narrows to leave the figure room; a flat or empty series draws along the bottom. */
function Sparkline({ values }: { values: number[] }) {
  const width = 112, height = 34, max = Math.max(0, ...values);
  const step = values.length > 1 ? width / (values.length - 1) : 0;
  const path = values.map((value, i) => `${i ? "L" : "M"}${(i * step).toFixed(1)},${(max > 0 ? height - 1 - (value / max) * (height - 2) : height - 1).toFixed(1)}`).join("");
  return (
    <svg aria-hidden viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="h-[34px] w-28 min-w-10 shrink overflow-visible text-foreground/70">
      <path d={path} fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinejoin="round" strokeLinecap="round" vectorEffect="non-scaling-stroke" />
    </svg>
  );
}

/**
 * The big figure over its chips, with a graphic beside the figure and a caption beside the chips.
 * The figure never truncates: the graphic narrows and the chips truncate instead.
 */
function Stat({ value, label, chips, graphic, caption, breakdown }: { value: string; label: string; chips: ReactNode; graphic?: ReactNode; caption?: ReactNode; /** shown on hover over the figure */ breakdown?: TokenTotals }) {
  return (
    <div className="flex h-full min-h-24 flex-col justify-center gap-2.5 px-1">
      <div className="flex items-end justify-between gap-3">
        <WithTokenBreakdown totals={breakdown} render={<p aria-label={label} className="shrink-0 whitespace-nowrap font-semibold text-[32px] tabular-nums leading-none tracking-tight" />}>{value}</WithTokenBreakdown>
        {graphic}
      </div>
      <div className="flex min-h-6 items-center justify-between gap-3">
        <div className="flex min-w-0 gap-1.5">{chips}</div>
        {caption && <div className="shrink-0 text-[11px] text-muted-foreground">{caption}</div>}
      </div>
    </div>
  );
}

function UsageCard({ title, icon, usage, onRemove, actions, children }: { title: string; icon: ReactNode; usage: UsageResult; onRemove: () => void; actions?: ReactNode; children: (stats: UsageStats) => ReactNode }) {
  return (
    <Widget title={title} icon={icon} status={usage.status} message={usage.message} onRemove={onRemove} actions={actions}
      action={usage.retry ? { label: "Retry", onClick: usage.retry } : undefined}>
      {usage.stats && children(usage.stats)}
    </Widget>
  );
}

function LogoTile({ kind }: { kind: string }) {
  return (
    <span className="inline-flex size-8 shrink-0 items-center justify-center rounded-lg border bg-card [&_svg]:size-4">
      <ProviderLogo kind={kind} />
    </span>
  );
}

/** Sessions started in the last 24 hours and the harnesses that ran them. */
export function SessionsWidget({ onRemove }: WidgetProps) {
  const usage = useUsageStats();
  return (
    <UsageCard title="Sessions · 24h" icon={<Layers />} usage={usage} onRemove={onRemove}>
      {({ day }) => (
        <Stat value={COUNT.format(day.sessions)} label={`${plural(day.sessions, "session")} in the last 24 hours`}
          chips={<Chip>{plural(day.agents.length, "agent")}</Chip>}
          caption={day.agents.length > 0 && (
            <ul className="flex shrink-0 gap-1.5" aria-label="Agents">
              {day.agents.slice(0, 4).map((kind) => <li key={kind}><LogoTile kind={kind} /><span className="sr-only">{kindLabel(kind)}</span></li>)}
            </ul>
          )} />
      )}
    </UsageCard>
  );
}

/** Tokens (cache reads excluded, like every token headline) in a range (24h, 7 days, all time), the average per day with usage, and a trend line. */
export function TokensWidget({ config, onConfig, onRemove }: WidgetProps) {
  const range = rangeOf(config);
  const usage = useUsageStats();
  return (
    <UsageCard title={`Tokens · ${RANGES[range].title}`} icon={<Coins />} usage={usage} onRemove={onRemove}
      actions={<RangeMenu range={range} onRange={(next) => onConfig({ range: next })} />}>
      {(stats) => {
        const { tokens, activeDays } = stats[RANGES[range].window];
        return (
          <Stat value={compactNumber(tokens.work)} label={`${COUNT.format(tokens.work)} tokens excluding cache reads, ${RANGES[range].title}`} breakdown={tokens}
            chips={activeDays > 0 && <Chip>{compactNumber(tokens.work / activeDays)} per active day</Chip>}
            graphic={<Sparkline values={sparkSeries(stats.daily, range === "all", (d: DailyUsage) => d.tokens)} />} caption={RANGES[range].spark} />
        );
      }}
    </UsageCard>
  );
}

/** Spend at API list prices; a lower bound ("≥") when some models have no price. */
export function SpendWidget({ config, onConfig, onRemove }: WidgetProps) {
  const range = rangeOf(config);
  const usage = useUsageStats();
  const prices = usage.stats && Date.parse(usage.stats.pricesUpdatedAt) > 0 ? PRICES_DATE.format(new Date(usage.stats.pricesUpdatedAt)) : null;
  const note = `API list prices from models.dev${prices ? `, updated ${prices}` : ""}. Subscription plans bill differently.`;
  return (
    <UsageCard title={`Spend · ${RANGES[range].title}`} icon={<Wallet />} usage={usage} onRemove={onRemove}
      actions={<>
        <Tooltip>
          <TooltipTrigger render={<button type="button" aria-label="About these prices" className={headerButton} />}><Info className="size-3.5" /></TooltipTrigger>
          <TooltipPopup><span className="block max-w-64">{note}</span></TooltipPopup>
        </Tooltip>
        <RangeMenu range={range} onRange={(next) => onConfig({ range: next })} />
      </>}>
      {(stats) => {
        const { tokens, activeDays } = stats[RANGES[range].window];
        const unpriced = tokens.unpriced.join(", ");
        return (
          <Stat value={costLabel(tokens)} label={`${tokens.unpriced.length ? "At least " : ""}${usd(tokens.costUsd)} spent, ${RANGES[range].title}. ${note}`}
            chips={tokens.unpriced.length > 0
              ? <Chip tone="warning" hint={`No price for ${unpriced}, so the spend excludes their tokens.`}>Excludes {unpriced}</Chip>
              : activeDays > 0 && <Chip>{usd(tokens.costUsd / activeDays)} per active day</Chip>}
            graphic={<Sparkline values={sparkSeries(stats.daily, range === "all", (d: DailyUsage) => d.costUsd)} />} caption={RANGES[range].spark} />
        );
      }}
    </UsageCard>
  );
}

/** Tokens (cache reads excluded) and spend per harness for a range, as bars relative to the busiest harness. */
export function ByAgentWidget({ config, onConfig, onRemove }: WidgetProps) {
  const range = rangeOf(config);
  const usage = useUsageStats(range);
  return (
    <UsageCard title="By agent" icon={<ChartBar />} usage={usage} onRemove={onRemove}
      actions={<RangeMenu range={range} onRange={(next) => onConfig({ range: next })} />}>
      {({ byAgent }) => {
        if (byAgent.length === 0) return <p className="flex h-full min-h-28 items-center justify-center text-center text-muted-foreground text-xs">No agent usage {range === "all" ? "recorded yet" : `in the last ${RANGES[range].label}`}.</p>;
        const max = Math.max(0, ...byAgent.map((agent) => agent.tokens.work));
        return (
          <ul className="-mx-3 -my-1.5 flex flex-col divide-y divide-border/60">
            {byAgent.map((agent) => {
              const accent = ACCENT[agent.kind] ?? "var(--muted-foreground)";
              const share = max > 0 ? agent.tokens.work / max : 0;
              return (
                <li key={agent.kind} className="flex items-center gap-3 px-3 py-3.5">
                  <LogoTile kind={agent.kind} />
                  <span className="w-28 min-w-0 truncate text-[13px]">{kindLabel(agent.kind)}</span>
                  <span aria-hidden className="relative h-4 min-w-12 flex-1 overflow-hidden rounded-sm bg-foreground/[0.06]">
                    <span className="absolute inset-y-0 left-0 rounded-sm border-r-2"
                      style={{ width: `max(2px, ${(share * 100).toFixed(2)}%)`, borderColor: accent, backgroundImage: `radial-gradient(${accent} 0.9px, transparent 1.1px)`, backgroundSize: "3px 3px" }} />
                  </span>
                  <WithTokenBreakdown totals={agent.tokens} render={<span className="w-16 shrink-0 text-right text-[13px] tabular-nums" />}>{compactNumber(agent.tokens.work)}</WithTokenBreakdown>
                  <Tooltip disabled={!agent.tokens.unpriced.length}>
                    <TooltipTrigger render={<span className="w-20 shrink-0 text-right text-[13px] text-muted-foreground tabular-nums" />}>{costLabel(agent.tokens)}</TooltipTrigger>
                    <TooltipPopup className="max-w-xs">Excludes {agent.tokens.unpriced.join(", ")}</TooltipPopup>
                  </Tooltip>
                </li>
              );
            })}
          </ul>
        );
      }}
    </UsageCard>
  );
}

function SessionRow({ session: s, now }: { session: SessionUsage; now: number }) {
  const project = s.project ? baseName(s.project) : "";
  return (
    <li>
      <button type="button" onClick={() => openTask(s.id)}
        className="flex w-full items-center gap-3 px-3 py-3 text-left outline-none hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset">
        <LogoTile kind={s.kind} />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate text-[13px]">{s.title || kindLabel(s.kind)}</span>
          <span className="flex min-w-0 items-center gap-1.5 text-muted-foreground text-xs">
            {project && <span className="min-w-0 truncate">{project}</span>}
            {project && s.toolCalls > 0 && <span aria-hidden>·</span>}
            {s.toolCalls > 0 && <span className="shrink-0 tabular-nums">{plural(s.toolCalls, "tool call")}</span>}
            {s.failedTools > 0 && <span className="inline-flex shrink-0 items-center gap-1 text-red-400 tabular-nums"><CircleAlert className="size-3" />{COUNT.format(s.failedTools)} failed</span>}
          </span>
        </span>
        <WithTokenBreakdown totals={s.tokens} render={<span className="w-16 shrink-0 text-right text-[13px] tabular-nums" />}>{compactNumber(s.tokens.work)}</WithTokenBreakdown>
        <span className="w-14 shrink-0 text-right text-muted-foreground text-xs tabular-nums">{ago(s.updatedAt, now)}</span>
      </button>
    </li>
  );
}

/** The most recently active sessions with their tool calls, failures and tokens (cache reads excluded); a row opens the task. */
export function LatestSessionsWidget({ onRemove }: WidgetProps) {
  const usage = useUsageStats();
  const now = useNow(usage.stats !== null && usage.stats.latest.length > 0);
  return (
    <UsageCard title="Latest sessions" icon={<Rows3 />} usage={usage} onRemove={onRemove}
      actions={<Tooltip>
        <TooltipTrigger render={<button type="button" aria-label="Open Tasks" className={headerButton} onClick={() => useUI.getState().setView("agents")} />}><ArrowUpRight className="size-3.5" /></TooltipTrigger>
        <TooltipPopup>Open Tasks</TooltipPopup>
      </Tooltip>}>
      {({ latest }) => latest.length === 0
        ? <p className="flex h-full min-h-28 items-center justify-center text-center text-muted-foreground text-xs">No sessions yet. Start a task above.</p>
        : <ul className="-mx-3 -my-1.5 flex flex-col divide-y divide-border/60">{latest.map((s) => <SessionRow key={s.id} session={s} now={now} />)}</ul>}
    </UsageCard>
  );
}

/** Everything recorded: tokens (cache reads excluded), spend, sessions and days with usage. */
export function UsageTotalsWidget({ onRemove }: WidgetProps) {
  const usage = useUsageStats();
  return (
    <UsageCard title="All time" icon={<Sigma />} usage={usage} onRemove={onRemove}>
      {({ all }) => (
        <dl className="grid h-full grid-cols-2 content-center gap-x-4 gap-y-3 px-1">
          {([
            ["Tokens", compactNumber(all.tokens.work)],
            ["Spend", costLabel(all.tokens)],
            ["Sessions", COUNT.format(all.sessions)],
            ["Active days", COUNT.format(all.activeDays)],
          ] as const).map(([label, value]) => (
            <div key={label} className="flex min-w-0 flex-col gap-0.5">
              <dt className="text-muted-foreground text-xs">{label}</dt>
              {label === "Tokens"
                ? <WithTokenBreakdown totals={all.tokens} render={<dd className="truncate font-semibold text-lg tabular-nums tracking-tight" />}>{value}</WithTokenBreakdown>
                : <Tooltip disabled={!(label === "Spend" && all.tokens.unpriced.length)}>
                  <TooltipTrigger render={<dd className="truncate font-semibold text-lg tabular-nums tracking-tight" />}>{value}</TooltipTrigger>
                  <TooltipPopup className="max-w-xs">Excludes {all.tokens.unpriced.join(", ")}</TooltipPopup>
                </Tooltip>}
            </div>
          ))}
        </dl>
      )}
    </UsageCard>
  );
}
