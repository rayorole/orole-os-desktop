import { useState } from "react";
import { ChevronDown, ChevronRight, PlusIcon } from "lucide-react";
import { MCP, type McpStatus, type ServerState, type ToolInfo } from "@/mygo";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { inDesktop } from "@/lib/backend";
import { cn } from "@/lib/utils";
import { useUI } from "@/stores/ui";
import { useAction } from "@/features/agents/format";
import { ServerLogo } from "@/features/integrations/ServerLogo";
import { useRemote } from "@/features/mcp/shared";
import { RailSection } from "./RailSection";

const COLLAPSED_SERVERS = 4;

const STATUS_DOT: Record<McpStatus, string> = {
  connected: "bg-success",
  connecting: "bg-info animate-pulse motion-reduce:animate-none",
  auth_required: "bg-warning",
  backoff: "bg-destructive",
  disabled: "",
};

// Servers that work first, then the ones that need the person, then the rest; the daemon's own order within each.
const STATUS_ORDER: Record<McpStatus, number> = { connected: 0, auth_required: 1, backoff: 1, connecting: 2, disabled: 3 };

const smallSwitch = "[--thumb-size:--spacing(3.5)] sm:[--thumb-size:--spacing(3.5)]";

/**
 * The connectors (MCP servers) every task's agent reaches through the Orole gateway: one row per server with its status,
 * a switch, and Sign in or Reconnect when it dropped; a connected server opens to switch single tools. Changes apply to
 * every task and bot, as on Settings › Integrations, and Go enforces them on each call.
 */
export function ConnectorsSection() {
  const list = useRemote(() => MCP.list(), "servers", inDesktop);
  const [showAll, setShowAll] = useState(false);
  const servers = (list.data ?? []).toSorted((a, b) => STATUS_ORDER[a.status] - STATUS_ORDER[b.status]);
  const attention = servers.filter((server) => server.enabled && (server.status === "auth_required" || server.status === "backoff")).length;
  const connected = servers.filter((server) => server.status === "connected").length;
  const shown = showAll ? servers : servers.slice(0, COLLAPSED_SERVERS);
  const summary = attention ? `${attention} to reconnect` : servers.length ? `${connected} connected` : undefined;
  return <RailSection id="connectors" title="Connectors" summary={summary}>
    {!inDesktop ? <p className="text-xs text-muted-foreground">Connectors are available in the desktop app.</p>
      : list.error ? <div role="alert" className="flex flex-col items-start gap-1.5 text-xs text-destructive-foreground">{list.error}<Button size="xs" variant="outline" onClick={list.reload}>Try again</Button></div>
        : !list.data ? <p role="status" className="text-xs text-muted-foreground">Loading connectors…</p>
          : !servers.length ? <p className="text-xs text-muted-foreground">No connectors yet. Add one to give tasks tools like GitHub, Linear or Notion.</p>
            : <ul aria-label="Connectors" className="flex flex-col">{shown.map((server) => <ServerRow key={server.id} server={server} changed={list.reload} />)}</ul>}
    {servers.length > COLLAPSED_SERVERS && <button type="button" onClick={() => setShowAll(!showAll)} className="self-start rounded text-xs text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
      {showAll ? "Show less" : `Show ${servers.length - COLLAPSED_SERVERS} more`}
    </button>}
    {inDesktop && <div className="flex items-center justify-between gap-2 border-t pt-2 text-xs text-muted-foreground">
      <span className="tabular-nums">{list.data ? `${connected} of ${servers.length} connected` : ""}</span>
      <Button size="xs" variant="secondary" onClick={() => useUI.getState().openSettings("integrations")}><PlusIcon />Add connector</Button>
    </div>}
  </RailSection>;
}

function ServerRow({ server, changed }: { server: ServerState; changed: () => void }) {
  const [open, setOpen] = useState(false);
  const action = useAction();
  const connected = server.status === "connected";
  const expanded = open && connected;
  const catalog = useRemote(() => MCP.catalog(server.id), server.id, expanded);
  const tools = catalog.data?.tools ?? [];
  const on = tools.filter((tool) => tool.permission !== "deny").length;
  const run = (work: () => Promise<unknown>) => void action.run(async () => { await work(); changed(); catalog.reload(); });

  const line = !server.enabled ? "Off"
    : server.status === "auth_required" ? "Sign in to connect"
      : server.status === "backoff" ? server.lastError || "The server did not answer."
        : server.status === "connecting" ? "Connecting…"
          : catalog.data ? `${on} of ${tools.length} tools` : `${server.toolCount} ${server.toolCount === 1 ? "tool" : "tools"}`;

  const control = server.enabled && server.status === "auth_required"
    ? server.authKind === "bearer"
      ? <Button size="xs" variant="secondary" onClick={() => useUI.getState().openSettings("integrations")}>{server.tokenHeader ? "Replace key" : "Replace token"}</Button>
      : <Button size="xs" variant="secondary" loading={action.busy} onClick={() => run(() => MCP.startAuth(server.id))}>Sign in</Button>
    : server.enabled && server.status === "backoff"
      ? <Button size="xs" variant="secondary" loading={action.busy} onClick={() => run(() => MCP.connect(server.id))}>Reconnect</Button>
      : <Switch className={smallSwitch} aria-label={`${server.name} on`} checked={server.enabled} disabled={action.busy} onCheckedChange={(enabled) => run(() => MCP.enable(server.id, enabled))} />;

  return <li className="flex min-w-0 flex-col">
    <div className="flex min-h-10 min-w-0 items-center gap-2.5 py-1">
      <span className={cn("relative flex size-7 shrink-0 items-center justify-center rounded-md border bg-background", !server.enabled && "opacity-50 grayscale")}>
        <ServerLogo server={server} className="size-4" />
        {STATUS_DOT[server.status] && <span aria-hidden className={cn("absolute -right-0.5 -bottom-0.5 size-2 rounded-full ring-2 ring-card", STATUS_DOT[server.status])} />}
      </span>
      <div className="flex min-w-0 flex-1 flex-col">
        {connected
          ? <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex min-w-0 items-center gap-1 self-start rounded text-left text-[13px] font-medium outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring">
            <span className="truncate">{server.name}</span>
            {open ? <ChevronDown aria-hidden className="size-3 shrink-0 text-muted-foreground" /> : <ChevronRight aria-hidden className="size-3 shrink-0 text-muted-foreground" />}
          </button>
          : <span className={cn("truncate text-[13px] font-medium", !server.enabled && "text-muted-foreground")}>{server.name}</span>}
        <span className={cn("break-words text-xs text-muted-foreground", server.enabled && server.status === "backoff" && "text-destructive-foreground")}>{line}</span>
      </div>
      <span className="shrink-0">{control}</span>
    </div>
    {action.error && <p role="alert" className="pb-1 text-xs break-words text-destructive-foreground">{action.error}</p>}
    {expanded && <div className="mb-2 ml-3.5 flex min-w-0 flex-col gap-1.5 border-l pl-4">
      <div className="flex min-w-0 items-center justify-between gap-2 text-xs text-muted-foreground">
        <span className="min-w-0 truncate">{hostOf(server.url)}</span>
        {tools.length > 0 && <button type="button" disabled={action.busy} className="shrink-0 rounded font-medium text-foreground outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60"
          onClick={() => run(() => Promise.all(tools.filter((tool) => (tool.permission === "deny") === (on === 0)).map((tool) => MCP.setToolPermission(server.id, tool.name, on === 0 ? "ask" : "deny"))))}>
          {on === 0 ? "Turn all on" : "Turn all off"}
        </button>}
      </div>
      {catalog.error && <p role="alert" className="text-xs text-destructive-foreground">{catalog.error}</p>}
      {catalog.loading && !catalog.data && <p role="status" className="text-xs text-muted-foreground">Loading tools…</p>}
      {catalog.data && !tools.length && <p className="text-xs text-muted-foreground">This server offers no tools.</p>}
      <ToolGroup label="Can read" tools={tools.filter((tool) => tool.effect === "read")} busy={action.busy} onToggle={(tool, enabled) => run(() => MCP.setToolPermission(server.id, tool.name, enabled ? "ask" : "deny"))} />
      <ToolGroup label="Can change" tools={tools.filter((tool) => tool.effect !== "read")} busy={action.busy} onToggle={(tool, enabled) => run(() => MCP.setToolPermission(server.id, tool.name, enabled ? "ask" : "deny"))} />
    </div>}
  </li>;
}

/** Tools the server classifies as reading, or (anything else, including unclassified) as able to change things. */
function ToolGroup({ label, tools, busy, onToggle }: { label: string; tools: ToolInfo[]; busy: boolean; onToggle: (tool: ToolInfo, enabled: boolean) => void }) {
  if (!tools.length) return null;
  return <div className="flex min-w-0 flex-col">
    <p className="py-0.5 text-xs text-muted-foreground">{label}</p>
    <ul aria-label={label} className="flex min-w-0 flex-col">
      {tools.map((tool) => {
        const enabled = tool.permission !== "deny";
        return <li key={tool.name} className="flex min-h-6 min-w-0 items-center gap-2">
          <code className={cn("min-w-0 flex-1 truncate font-mono text-xs", !enabled && "text-muted-foreground")}>{tool.name}</code>
          <Switch className={smallSwitch} aria-label={tool.name} checked={enabled} disabled={busy} onCheckedChange={(next) => onToggle(tool, next)} />
        </li>;
      })}
    </ul>
  </div>;
}

function hostOf(url: string): string {
  try { return new URL(url).host || url; } catch { return url; }
}
