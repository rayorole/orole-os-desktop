import { useEffect, useState, type ReactNode } from "react";
import { ArrowLeft, Plus, Search } from "lucide-react";
import { MCP, events, type ServerState } from "@/mygo";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { GitHubSettings } from "@/features/github/GitHubSettings";
import { useGitHub } from "@/features/github/store";
import { ServerSettings, STATUS } from "@/features/mcp/ServerDetail";
import { Failure, Loading, useRemote } from "@/features/mcp/shared";
import { inDesktop } from "@/lib/backend";
import { useUI } from "@/stores/ui";
import { ApiKeyConnectorDialog } from "./ApiKeyConnectorDialog";
import { BrandLogo } from "./BrandLogo";
import { ClientConnectorDialog } from "./ClientConnectorDialog";
import { CustomConnectorDialog } from "./CustomConnectorDialog";
import { CATEGORIES, DIRECTORY, serverFor, type Connector } from "./directory";
import { connectServer, useConnect } from "./useConnect";

function Row({ icon, name, description, badge, children }: { icon: ReactNode; name: string; description: string; badge?: ReactNode; children: ReactNode }) {
  return (
    <li className="flex min-w-0 items-center gap-3 py-2">
      <span className="flex size-10 shrink-0 items-center justify-center rounded-xl border bg-card">{icon}</span>
      <div className="min-w-0 flex-1">
        <span className="flex items-center gap-2"><span className="truncate font-medium">{name}</span>{badge}</span>
        <span className="block truncate text-xs text-muted-foreground">{description}</span>
      </div>
      <div className="flex shrink-0 gap-2">{children}</div>
    </li>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="mb-6">
      <h2 className="mb-1 text-sm font-medium">{title}</h2>
      <ul className="grid grid-cols-1 gap-x-8 xl:grid-cols-2">{children}</ul>
    </section>
  );
}

const pill = "rounded-full";

/**
 * Settings > Integrations: every service Orole connects to, whether built in (GitHub) or an
 * MCP server. One click adds a directory entry and starts sign-in; "Custom connector" takes any
 * MCP server by name and URL. Tools from connected servers reach every harness via the gateway.
 */
export function Integrations() {
  const requested = useUI((s) => s.settingsTab);
  const servers = useRemote(() => MCP.list(), "servers", true, "mcp-servers");
  const { status: github, load, startSync } = useGitHub();
  const [open, setOpen] = useState<string | null>(requested === "github" ? "github" : null);
  const [query, setQuery] = useState("");
  const [custom, setCustom] = useState(false);
  const [clientFor, setClientFor] = useState<Connector | null>(null);
  const [keyFor, setKeyFor] = useState<Connector | null>(null);
  const [waiting, setWaiting] = useState<ReadonlySet<string>>(new Set());
  const connect = useConnect(servers.reload);
  useEffect(() => { if (!inDesktop) return; void load(); return startSync(); }, [load, startSync]);
  useEffect(() => {
    if (!inDesktop) return;
    return events.mcpAuthDone.on(({ serverId }) => setWaiting((current) => { const next = new Set(current); next.delete(serverId); return next; }));
  }, []);

  const list = servers.data ?? [];
  const selected = list.find((server) => server.id === open);
  if (open === "github") {
    return (
      <div className="flex flex-col gap-3">
        <Button size="sm" variant="ghost" className="self-start" onClick={() => setOpen(null)}><ArrowLeft />All integrations</Button>
        <GitHubSettings />
      </div>
    );
  }
  if (selected) {
    return (
      <div className="flex flex-col gap-3">
        <Button size="sm" variant="ghost" className="self-start" onClick={() => setOpen(null)}><ArrowLeft />All integrations</Button>
        <ServerSettings server={selected} connector={DIRECTORY.find((c) => serverFor(c, [selected]))} changed={servers.reload} onRemoved={() => setOpen(null)} />
      </div>
    );
  }

  const add = (c: Connector) => {
    if (c.native === "github") { setOpen("github"); return; }
    if (c.client) { connect.clearError(); setClientFor(c); return; }
    if (c.apiKey) { connect.clearError(); setKeyFor(c); return; }
    void connect.run(c.id, async () => {
      const { id, signingIn } = await connectServer(c.name, c.url!, { local: c.local });
      if (signingIn) setWaiting((current) => new Set(current).add(id));
    });
  };
  const signIn = (server: ServerState) => void connect.run(server.id, async () => {
    await MCP.startAuth(server.id);
    setWaiting((current) => new Set(current).add(server.id));
  });

  const needle = query.trim().toLowerCase();
  const matches = (name: string, description: string) => !needle || `${name} ${description}`.toLowerCase().includes(needle);
  const githubConnected = github?.connected === true;
  const isConnected = (c: Connector) => c.native === "github" ? githubConnected : !!serverFor(c, list);
  const available = DIRECTORY.filter((c) => !isConnected(c) && matches(c.name, c.description));
  const featured = available.filter((c) => c.featured);
  const desktopHint = !inDesktop && <p className="mb-4 text-muted-foreground">Open the desktop app to add integrations: connections and credentials are managed by the local daemon, not this browser preview.</p>;

  const connectedRows: ReactNode[] = [];
  if (github?.connected && matches("GitHub", "Search repos and PRs")) {
    connectedRows.push(
      <Row key="github" icon={<BrandLogo logo={DIRECTORY[0]!.logo} />} name="GitHub" description={`Signed in as ${github.user?.login ?? "your account"}`} badge={<Badge variant="success">Connected</Badge>}>
        <Button size="sm" variant="secondary" className={pill} onClick={() => setOpen("github")}>Manage</Button>
      </Row>,
    );
  }
  for (const server of list) {
    const entry = DIRECTORY.find((c) => serverFor(c, [server]));
    if (!matches(server.name, entry?.description ?? server.url)) continue;
    const state = STATUS[server.status];
    const pending = waiting.has(server.id) && server.status === "auth_required";
    connectedRows.push(
      <Row key={server.id} icon={<BrandLogo logo={entry?.logo} />} name={server.name}
        description={pending ? "Finish signing in in your browser…" : server.lastError || entry?.description || new URL(server.url).host}
        badge={<Badge variant={state.variant}>{state.label}</Badge>}>
        {server.status === "auth_required" && server.authKind !== "bearer" && !pending && <Button size="sm" className={pill} disabled={connect.busy === server.id} onClick={() => signIn(server)}>Sign in</Button>}
        {pending && <Button size="sm" variant="outline" className={pill} onClick={() => void connect.run(server.id, () => MCP.cancelAuth(server.id))}>Cancel</Button>}
        <Button size="sm" variant="secondary" className={pill} onClick={() => setOpen(server.id)}>Manage</Button>
      </Row>,
    );
  }

  const addRow = (c: Connector) => (
    <Row key={c.id} icon={<BrandLogo logo={c.logo} />} name={c.name} description={c.description}>
      <Button size="sm" variant="secondary" className={pill} disabled={!inDesktop || connect.busy !== null} aria-label={`Add ${c.name}`} onClick={() => add(c)}>
        {connect.busy === c.id ? "Adding…" : "Add"}
      </Button>
    </Row>
  );

  return (
    <div className="flex min-w-0 flex-col">
      <div className="mb-5 flex flex-wrap items-center gap-2">
        <div className="relative min-w-48 flex-1">
          <Search aria-hidden className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground" />
          <Input aria-label="Search integrations" placeholder="Search integrations" className="pl-8" value={query} onChange={(event) => setQuery(event.target.value)} />
        </div>
        <Button variant="outline" disabled={!inDesktop} onClick={() => { connect.clearError(); setCustom(true); }}><Plus />Custom connector</Button>
      </div>
      {desktopHint}
      <Failure error={servers.error ?? (custom || clientFor || keyFor ? null : connect.error)} retry={servers.error ? servers.reload : undefined} />
      {servers.loading && !servers.data && <Loading>Loading connections…</Loading>}
      {connectedRows.length > 0 && <Section title="Connected">{connectedRows}</Section>}
      {featured.length > 0 && <Section title="Featured">{featured.map(addRow)}</Section>}
      {CATEGORIES.map((category) => {
        const items = available.filter((c) => !c.featured && c.category === category);
        return items.length > 0 && <Section key={category} title={category}>{items.map(addRow)}</Section>;
      })}
      {needle && !connectedRows.length && !available.length && <p className="text-muted-foreground">No integration matches “{query}”. Use Custom connector to add any MCP server by URL.</p>}
      <CustomConnectorDialog open={custom} busy={connect.busy === "custom"} error={connect.error} onClose={() => setCustom(false)}
        onAdd={(name, url, token) => void connect.run("custom", async () => {
          const { id, signingIn } = await connectServer(name, url, token ? { token } : {});
          if (signingIn) setWaiting((current) => new Set(current).add(id));
          setCustom(false);
        })} />
      <ClientConnectorDialog connector={clientFor} busy={connect.busy === clientFor?.id} error={connect.error} onClose={() => setClientFor(null)}
        onAdd={(c, client) => void connect.run(c.id, async () => {
          const { id, signingIn } = await connectServer(c.name, c.url!, { client });
          if (signingIn) setWaiting((current) => new Set(current).add(id));
          setClientFor(null);
        })} />
      <ApiKeyConnectorDialog connector={keyFor} busy={connect.busy === keyFor?.id} error={connect.error} onClose={() => setKeyFor(null)}
        onAdd={(c, key) => void connect.run(c.id, async () => {
          await connectServer(c.name, c.url!, { token: key, tokenHeader: c.apiKey!.header });
          setKeyFor(null);
        })} />
    </div>
  );
}
