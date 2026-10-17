import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { useUI } from "@/stores/ui";
import { cn } from "@/lib/utils";
import { api, errorText, type Topic, type Trigger, type Watch } from "./api";
import { TriggerIcon } from "./BlockIcon";
import { ChoiceField, JsonField, LinesField, NumberField, TextField } from "./fields";
import { DEPLOYMENT_PRESETS, describeSchedule, eventFields, triggerSummary } from "./flow";
import { useMcpServers } from "./pickers";

const TYPES = [
  { value: "manual", label: "Manual" }, { value: "schedule", label: "Schedule" }, { value: "event", label: "App event" },
  { value: "watch", label: "Watch" }, { value: "webhook", label: "Webhook" },
];
const GITHUB_KINDS = [
  { value: "push", label: "Push to a branch" }, { value: "pr_opened", label: "Pull request opened" }, { value: "pr_merged", label: "Pull request merged" },
  { value: "workflow_failed", label: "Workflow failed" }, { value: "issue_opened", label: "Issue opened" },
];

export function TriggerCard({ trigger, onChange, topics, issue, offline, invalid }: { trigger: Trigger; onChange: (trigger: Trigger) => void; topics: Topic[]; issue: (path: string) => string | undefined; offline: boolean; invalid: (key: string, message: string | null) => void }) {
  const patch = (value: Partial<Trigger>) => onChange({ ...trigger, ...value });
  const fields = eventFields(trigger, topics);
  const typeIssue = issue("trigger.type");
  return <section aria-label="Trigger" className="flex flex-col gap-3 rounded-lg border bg-card p-3 shadow-xs/5">
    <header className="flex min-w-0 flex-col gap-0.5">
      <h2 className="text-xs font-medium text-muted-foreground uppercase tracking-wide">Trigger</h2>
      <p className="truncate text-sm font-medium">{triggerSummary(trigger, topics)}</p>
    </header>
    <div role="radiogroup" aria-label="Start this flow" className="flex flex-wrap gap-1">
      {TYPES.map(({ value, label }) => <button key={value} type="button" role="radio" aria-checked={trigger.type === value}
        onClick={() => patch(value === "watch" && !trigger.watch ? { type: value, watch: { source: "github", kind: "push", repo: "", branch: "" } } : { type: value })}
        className={cn("inline-flex h-7 items-center gap-1.5 rounded-md border px-2 text-xs outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring",
          trigger.type === value ? "border-primary bg-primary/8 text-foreground" : "text-muted-foreground hover:bg-accent hover:text-foreground")}>
        <TriggerIcon type={value} className="size-3.5" />{label}
      </button>)}
    </div>
    {typeIssue && <p role="alert" className="text-xs text-destructive-foreground">{typeIssue}</p>}
    {trigger.type === "manual" && <p className="text-xs text-muted-foreground">Runs only when you choose Run now.</p>}
    {trigger.type === "schedule" && <ScheduleFields trigger={trigger} patch={patch} issue={issue} offline={offline} />}
    {trigger.type === "event" && <ChoiceField label="Event" value={trigger.event} issue={issue("trigger.event")} placeholder="Choose an event…"
      options={[...topics.map((t) => ({ value: t.topic, label: t.label, group: t.group })), ...(trigger.event && !topics.some((t) => t.topic === trigger.event) ? [{ value: trigger.event, label: trigger.event, group: "Other" }] : [])]}
      onChange={(event) => patch({ event })} />}
    {trigger.type === "watch" && <WatchFields watch={trigger.watch ?? { source: "github", kind: "push" }} onChange={(watch) => patch({ watch })} issue={issue} invalid={invalid} />}
    {trigger.type === "webhook" && <div className="flex flex-col items-start gap-2 text-xs text-muted-foreground">
      <p>Apps on this PC start the flow by sending a signed request to the local webhook listener. Turn the listener on and manage its secret in Settings; the secret never appears here.</p>
      <Button size="xs" variant="outline" onClick={() => useUI.getState().openSettings("notifications", "automations")}>Open webhook settings</Button>
    </div>}
    {(trigger.type === "event" || trigger.type === "watch" || trigger.type === "webhook") && <div className="flex flex-col gap-1.5">
      <TextField label="Only when (optional)" mono value={trigger.filter} onChange={(filter) => patch({ filter })} issue={issue("trigger.filter")} placeholder="event.data.status == 'done'" hint="An expression; the flow starts only when it is true. Event data is untrusted text." />
      {fields.length > 0 && <div className="flex flex-wrap gap-1" aria-label="Event fields">
        {fields.map((f) => <button key={f.name} type="button" onClick={() => patch({ filter: `${trigger.filter ? `${trigger.filter.trimEnd()} ` : ""}event.data.${f.name}` })}
          className="rounded-sm border bg-muted/40 px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">{f.name}</button>)}
      </div>}
    </div>}
  </section>;
}

function ScheduleFields({ trigger, patch, issue, offline }: { trigger: Trigger; patch: (value: Partial<Trigger>) => void; issue: (path: string) => string | undefined; offline: boolean }) {
  // The daemon computes the next runs; a result only counts for the schedule it was asked for.
  const [result, setResult] = useState<{ schedule: string; dates: string[] | null; error: string | null } | null>(null);
  const schedule = trigger.schedule.trim();
  const preview = result?.schedule === schedule && !offline ? result : { dates: null, error: null };
  useEffect(() => {
    if (!schedule || offline) return;
    let active = true;
    const timer = window.setTimeout(() => {
      api.preview(schedule).then((dates) => { if (active) setResult({ schedule, dates: dates ?? [], error: null }); }, (e) => { if (active) setResult({ schedule, dates: null, error: errorText(e) }); });
    }, 400);
    return () => { active = false; window.clearTimeout(timer); };
  }, [schedule, offline]);
  return <>
    <TextField label="Schedule" mono value={trigger.schedule} onChange={(value) => patch({ schedule: value })} issue={issue("trigger.schedule")} placeholder="0 9 * * 1-5 or every 15m"
      hint={schedule ? describeSchedule(schedule) : "Cron (minute hour day month weekday) or every 15m, 2h, 1d."} />
    <div aria-live="polite" className="text-xs">
      {preview.error && <p className="text-destructive-foreground">{preview.error}</p>}
      {preview.dates && (preview.dates.length
        ? <><p className="mb-1 text-muted-foreground">Next runs</p><ol className="flex flex-col gap-0.5 tabular-nums">{preview.dates.map((date, i) => <li key={`${date}:${i}`}><time dateTime={date}>{new Date(date).toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</time></li>)}</ol></>
        : <p className="text-muted-foreground">This schedule has no upcoming runs.</p>)}
      {offline && schedule && <p className="text-muted-foreground">Reconnect the daemon to preview the next runs.</p>}
    </div>
    <ChoiceField label="If the PC was off" value={trigger.catchUp} issue={issue("trigger.catchUp")} onChange={(catchUp) => patch({ catchUp })}
      options={[{ value: "skip", label: "Skip missed runs" }, { value: "run-once", label: "Run once to catch up" }]} />
  </>;
}

function WatchFields({ watch, onChange, issue, invalid }: { watch: Watch; onChange: (watch: Watch) => void; issue: (path: string) => string | undefined; invalid: (key: string, message: string | null) => void }) {
  const patch = (value: Partial<Watch>) => onChange({ ...watch, ...value });
  const mcp = watch.source === "mcp";
  const { servers, error } = useMcpServers(mcp);
  const preset = DEPLOYMENT_PRESETS.find((p) => p.tool === watch.tool)?.id ?? "custom";
  const interval = <NumberField label="Check every (seconds)" value={watch.intervalSec ?? 0} min={0} max={86400} onChange={(intervalSec) => patch({ intervalSec })} issue={issue("trigger.watch.intervalSec")} hint={`0 uses the default (${mcp ? 120 : 60} s); minimum 30.`} />;
  return <div className="flex flex-col gap-3">
    <ChoiceField label="Watch" value={watch.source} issue={issue("trigger.watch.source") ?? issue("trigger.watch")} onChange={(source) => onChange(source === "mcp" ? { source, kind: "deployment", server: "", tool: DEPLOYMENT_PRESETS[0].tool, args: { ...DEPLOYMENT_PRESETS[0].args }, states: [] } : { source, kind: "push", repo: "", branch: "" })}
      options={[{ value: "github", label: "GitHub repository" }, { value: "mcp", label: "Integration deployments" }]} />
    {!mcp && <>
      <ChoiceField label="When" value={watch.kind} options={GITHUB_KINDS} issue={issue("trigger.watch.kind")} onChange={(kind) => patch({ kind })} />
      <TextField label="Repository" mono value={watch.repo ?? ""} placeholder="owner/name" issue={issue("trigger.watch.repo")} onChange={(repo) => patch({ repo })} />
      {(watch.kind === "push" || watch.kind === "workflow_failed") && <TextField label="Branch (optional)" mono value={watch.branch ?? ""} placeholder={watch.kind === "push" ? "Default branch" : "Any branch"} issue={issue("trigger.watch.branch")} onChange={(branch) => patch({ branch })} />}
      {interval}
    </>}
    {mcp && <>
      <ChoiceField label="Integration" value={watch.server ?? ""} issue={issue("trigger.watch.server") ?? (error ? `Could not list integrations: ${error}` : undefined)} placeholder={servers ? "Choose a connected integration…" : "Loading integrations…"}
        options={[...(servers ?? []).map((s) => ({ value: s.id, label: s.status === "connected" ? s.name : `${s.name} (${s.status})` })), ...(watch.server && servers && !servers.some((s) => s.id === watch.server) ? [{ value: watch.server, label: `${watch.server} (not found)` }] : [])]}
        hint={servers && !servers.length ? "Connect Vercel, Railway or another integration in Settings → Integrations first." : undefined}
        onChange={(server) => patch({ server })} />
      <ChoiceField label="Deployments from" value={preset} onChange={(id) => { const p = DEPLOYMENT_PRESETS.find((x) => x.id === id); if (p) patch({ tool: p.tool, args: { ...p.args, ...watch.args } }); else patch({ tool: "" }); }}
        options={[...DEPLOYMENT_PRESETS.map((p) => ({ value: p.id, label: `${p.label} (${p.tool})` })), { value: "custom", label: "Another tool" }]} />
      {preset === "custom" && <TextField label="Tool" mono value={watch.tool ?? ""} issue={issue("trigger.watch.tool")} onChange={(tool) => patch({ tool })} hint="A tool of the integration that lists deployments." />}
      <JsonField key={`${preset}:${watch.server ?? ""}`} label="Tool arguments" value={watch.args ?? {}} issue={issue("trigger.watch.args")} onChange={(args) => patch({ args })} onInvalid={(message) => invalid("trigger.watch.args", message)}
        hint={preset === "railway" ? "Railway needs projectId, environmentId and serviceId." : preset === "vercel" ? "Vercel needs projectId." : "Passed to the tool as is."} />
      <LinesField label="Only these states (optional)" mono value={watch.states ?? []} issue={issue("trigger.watch.states")} hint="One per line, like READY or ERROR. Empty starts the flow on every state change."
        onChange={(states) => patch({ states })} />
      {interval}
    </>}
  </div>;
}
