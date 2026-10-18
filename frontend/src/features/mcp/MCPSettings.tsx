import { useState } from "react";
import { MCP, type ServerState } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Fieldset } from "@/components/ui/fieldset";
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { useVisibleServers } from "./disconnect";
import { errorText, Failure, Loading, NoResults, useRemote } from "./shared";
import { ServerSettings } from "./ServerDetail";

export const QUICK_SERVERS = {
  mail: { name: "Orole Mail", url: "https://mail.orole.be/api/mcp" },
  drive: { name: "Orole Drive", url: "https://drive.orole.be/api/mcp" },
};

const EMPTY: ServerState[] = [];

export function MCPSettings({ adapter }: { adapter: "mail" | "drive" }) {
  const servers = useRemote(() => MCP.list(), "servers", true, "mcp-servers");
  const [adding, setAdding] = useState(false);
  const preset = adapter;
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const list = useVisibleServers(servers.data ?? EMPTY);
  const selected = list.filter((server) => server.adapter === adapter);
  async function add(form: HTMLFormElement) {
    const data = new FormData(form);
    setBusy(true); setError(null);
    try {
      await MCP.add({ name: String(data.get("name")).trim(), url: String(data.get("url")).trim(), adapter: preset ?? "", authKind: "oauth", oauthClientId: String(data.get("clientId")).trim(), scopes: String(data.get("scopes")).trim() });
      setAdding(false); servers.reload();
    } catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  }
  return <div className="flex min-w-0 flex-col gap-4">
    <p className="max-w-prose text-muted-foreground">Connect a Streamable HTTP MCP server. Tools and capabilities appear only after the server has been connected and discovered.</p>
    {!inDesktop && <NoResults title="Open the desktop app">MCP connections and credentials are managed by the local daemon, not this browser preview.</NoResults>}
    <div className="flex flex-wrap gap-2">
      <Button variant="outline" disabled={!inDesktop || list.some((server) => server.adapter === adapter)} onClick={() => { setError(null); setAdding(true); }}>Add {adapter === "mail" ? "Mail" : "Drive"}</Button>
      <Button variant="ghost" disabled={!inDesktop || servers.loading} onClick={servers.reload}>Refresh connections</Button>
    </div>
    <Failure error={servers.error} retry={servers.reload} />
    {servers.loading && !servers.data && <Loading>Loading connections…</Loading>}
    {inDesktop && servers.data && !selected.length && <NoResults title="No server connected">Add {adapter} to get started. Authorization happens in your browser.</NoResults>}
    {selected.map((server) => <ServerSettings key={server.id} server={server} changed={servers.reload} />)}
    <Dialog open={adding} onOpenChange={(open) => { if (!busy) setAdding(open); }}>
      <DialogPopup anchored showCloseButton={!busy}>
        <DialogHeader><DialogTitle>Add {preset ? QUICK_SERVERS[preset].name : "MCP server"}</DialogTitle><DialogDescription>Use an OAuth client registered for this server, or a supported client metadata URL.{preset && " Mail and Drive disable dynamic client registration."}</DialogDescription></DialogHeader>
        <form className="contents" onSubmit={(event) => { event.preventDefault(); void add(event.currentTarget); }}>
          <DialogPanel><Fieldset className="flex flex-col gap-4" disabled={busy}>
            <Field><FieldLabel htmlFor="mcp-name">Name</FieldLabel><Input id="mcp-name" name="name" required defaultValue={preset ? QUICK_SERVERS[preset].name : ""} /></Field>
            <Field><FieldLabel htmlFor="mcp-url">Server URL</FieldLabel><Input id="mcp-url" name="url" type="url" required defaultValue={preset ? QUICK_SERVERS[preset].url : ""} /></Field>
            <Field><FieldLabel htmlFor="mcp-client">Client ID or CIMD URL</FieldLabel><Input id="mcp-client" name="clientId" autoComplete="off" /><FieldDescription>A public OAuth client ID or HTTPS client ID metadata document URL. Blank uses Orole’s own client metadata document, or dynamic registration, when the server supports it.</FieldDescription></Field>
            <Field><FieldLabel htmlFor="mcp-scopes">Scopes (optional)</FieldLabel><Input id="mcp-scopes" name="scopes" /><FieldDescription>Space-separated. Blank uses the server’s requested scopes.</FieldDescription></Field>
            <Failure error={error} />
          </Fieldset></DialogPanel>
          <DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={() => setAdding(false)}>Cancel</Button><Button type="submit" disabled={busy}>{busy ? "Adding…" : "Add server"}</Button></DialogFooter>
        </form>
      </DialogPopup>
    </Dialog>
  </div>;
}
