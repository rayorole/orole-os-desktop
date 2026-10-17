import { useState } from "react";
import { Check } from "lucide-react";
import { PreviewCard, PreviewCardPopup, PreviewCardTrigger } from "@/components/ui/preview-card";
import { MCP, type Discovery, type ServerState } from "@/mygo";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogDescription, DialogFooter, DialogHeader, DialogPanel, DialogPopup, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field";
import { Fieldset } from "@/components/ui/fieldset";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { BrandLogo } from "@/features/integrations/BrandLogo";
import type { Connector } from "@/features/integrations/directory";
import { errorText, Failure, JsonData, Loading, useRemote } from "./shared";

export const STATUS: Record<ServerState["status"], { label: string; variant: "success" | "outline" | "warning" }> = {
  connected: { label: "Connected", variant: "success" },
  connecting: { label: "Connecting", variant: "outline" },
  auth_required: { label: "Needs sign-in", variant: "warning" },
  backoff: { label: "Retrying", variant: "warning" },
  disabled: { label: "Off", variant: "outline" },
};

const COLLAPSED_TOOLS = 12;

/**
 * One connected server: who it is, sign-in and disconnect, which tools are on, and its URL.
 * Everything technical (OAuth client, per-tool permission levels, discovery, diagnostics) sits
 * under Advanced, closed by default: most servers sign in with nothing configured.
 */
export function ServerSettings({ server, connector, changed, onRemoved }: {
  server: ServerState;
  connector?: Connector;
  changed: () => void;
  onRemoved?: () => void;
}) {
  const [remove, setRemove] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [authStarted, setAuthStarted] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [discovery, setDiscovery] = useState<Discovery | null>(null);
  const catalog = useRemote(() => MCP.catalog(server.id), server.id, server.status === "connected");
  const diagnostics = useRemote(() => MCP.diagnostics(server.id), server.id, advanced);
  async function run(action: () => Promise<unknown>) {
    setBusy(true); setError(null);
    try { await action(); changed(); catalog.reload(); diagnostics.reload(); }
    catch (cause) { setError(errorText(cause)); }
    finally { setBusy(false); }
  }

  const status = STATUS[server.status];
  const tools = catalog.data?.tools ?? [];
  const disabled = tools.filter((tool) => tool.permission === "deny").length;
  const shown = showAll ? tools : tools.slice(0, COLLAPSED_TOOLS);
  const signedIn = server.status === "connected";
  const waiting = authStarted && !signedIn;
  const bearer = server.authKind === "bearer";
  const keyHeader = bearer ? server.tokenHeader : "";

  return (
    <section aria-label={server.name} className="flex max-w-2xl min-w-0 flex-col gap-5">
      <header className="flex flex-wrap items-center gap-3">
        <span className="flex size-12 shrink-0 items-center justify-center rounded-xl border bg-card"><BrandLogo logo={connector?.logo} className="size-6" /></span>
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-base font-medium">{server.name}</h2>
          <div className="flex items-center gap-2 text-muted-foreground">{connector?.category && <span>{connector.category}</span>}<Badge variant={status.variant}>{status.label}</Badge></div>
        </div>
        <div className="flex flex-wrap gap-2">
          {bearer ? null : waiting
            ? <Button variant="outline" className="rounded-full" disabled={busy} onClick={() => void run(async () => { await MCP.cancelAuth(server.id); setAuthStarted(false); })}>Cancel sign-in</Button>
            : <Button variant={signedIn ? "outline" : "default"} className="rounded-full" disabled={busy || !server.enabled} onClick={() => void run(async () => { await MCP.startAuth(server.id); setAuthStarted(true); })}>{signedIn ? "Reauthenticate" : "Sign in"}</Button>}
          <Button variant="outline" className="rounded-full" disabled={busy} onClick={() => setRemove(true)}>Disconnect</Button>
        </div>
      </header>

      {waiting && <p role="status" className="text-muted-foreground">Finish signing in in your browser.</p>}
      <Failure error={error ?? server.lastError ?? null} />

      {connector?.about && <p className="max-w-prose text-base text-muted-foreground">{connector.about}</p>}
      {bearer && <form key={server.id} onSubmit={(event) => {
        event.preventDefault();
        const form = event.currentTarget;
        const token = String(new FormData(form).get("token")).trim();
        void run(async () => { await MCP.update(server.id, { bearerToken: token }); form.reset(); });
      }}>
        <Fieldset disabled={busy} className="flex flex-col gap-3">
          <Field>
            <FieldLabel htmlFor={`token-${server.id}`}>{keyHeader ? "API key" : "Access token"}</FieldLabel>
            <Input id={`token-${server.id}`} name="token" type="password" required autoComplete="off" />
            <FieldDescription>{keyHeader
              ? <>This server uses an API key, sent in the <code>{keyHeader}</code> header, instead of browser sign-in. The stored key is never shown; enter a new one to replace it.</>
              : "This server uses a fixed bearer token instead of browser sign-in. The stored token is never shown; enter a new one to replace it."}</FieldDescription>
          </Field>
          <Button type="submit" size="sm" variant={signedIn ? "outline" : "default"} className="self-start">{keyHeader ? "Replace key" : "Replace token"}</Button>
        </Fieldset>
      </form>}

      {signedIn && <div className="flex flex-col gap-3">
        <Failure error={catalog.error} retry={catalog.reload} />
        {catalog.loading && !catalog.data && <Loading>Loading tools…</Loading>}
        {catalog.data && !tools.length && <p className="text-muted-foreground">This server offers no tools.</p>}
        {tools.length > 0 && <>
          <p className="text-muted-foreground">{disabled === 0 ? "All tools enabled" : `${tools.length - disabled} of ${tools.length} tools enabled`}</p>
          <ul className="flex flex-wrap gap-2" aria-label="Tools">
            {shown.map((tool) => {
              const on = tool.permission !== "deny";
              return <li key={tool.name}>
                <PreviewCard>
                  <PreviewCardTrigger delay={250} render={<button type="button" aria-pressed={on} disabled={busy}
                    className={`inline-flex items-center gap-1.5 rounded-full bg-muted px-3 py-1 text-sm hover:bg-accent disabled:opacity-60 ${on ? "" : "text-muted-foreground line-through"}`}
                    onClick={() => void run(() => MCP.setToolPermission(server.id, tool.name, on ? "deny" : "ask"))} />}>
                    {on && <Check aria-hidden className="size-3.5" />}{tool.name}
                  </PreviewCardTrigger>
                  <PreviewCardPopup align="start" className="w-80 flex-col gap-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <code className="break-all text-xs font-medium">{tool.name}</code>
                      <Badge variant="outline">{tool.effect || "unclassified"}</Badge>
                    </div>
                    {/* Server-supplied text: sanitised by the daemon and shown as plain text only. */}
                    <p className="line-clamp-8 whitespace-pre-wrap break-words text-muted-foreground">{tool.description || "This server gives no description for the tool."}</p>
                    <p className="text-xs text-muted-foreground">{on ? `Enabled · ${tool.permission === "allow" ? "runs without asking" : "asks before it runs"}` : "Turned off"}. Click to {on ? "turn off" : "turn on"}.</p>
                  </PreviewCardPopup>
                </PreviewCard>
              </li>;
            })}
          </ul>
          {tools.length > COLLAPSED_TOOLS && <Button size="sm" variant="secondary" className="self-start rounded-full" onClick={() => setShowAll(!showAll)}>{showAll ? "See less" : "See more"}</Button>}
        </>}
      </div>}

      <div>
        <p className="text-muted-foreground">Server URL</p>
        <p className="selectable break-all text-base">{server.url}</p>
      </div>

      <details className="rounded-lg border" onToggle={(event) => setAdvanced(event.currentTarget.open)}>
        <summary className="cursor-pointer px-3 py-2 font-medium">Advanced</summary>
        <div className="flex flex-col gap-5 border-t p-3">
          <label className="flex items-center justify-between gap-3"><span>Enabled</span><Switch aria-label={`Enable ${server.name}`} checked={server.enabled} disabled={busy} onCheckedChange={(enabled) => void run(() => MCP.enable(server.id, enabled))} /></label>

          {!bearer && <form key={`${server.id}:${server.oauthClientId}:${server.scopes}`} onSubmit={(event) => { event.preventDefault(); const data = new FormData(event.currentTarget); void run(() => MCP.update(server.id, { oauthClientId: String(data.get("clientId")).trim(), scopes: String(data.get("scopes")).trim() })); }}>
            <Fieldset disabled={busy} className="flex flex-col gap-3">
              <p className="font-medium">OAuth client</p>
              <p className="text-muted-foreground">Only needed for the few servers that do not register clients automatically. Leave empty otherwise.</p>
              <Field><FieldLabel htmlFor={`client-${server.id}`}>Client ID or CIMD URL</FieldLabel><Input id={`client-${server.id}`} name="clientId" defaultValue={server.oauthClientId} autoComplete="off" /><FieldDescription>Public client configuration only. Access and refresh tokens never appear here.</FieldDescription></Field>
              <Field><FieldLabel htmlFor={`scopes-${server.id}`}>Scopes</FieldLabel><Input id={`scopes-${server.id}`} name="scopes" defaultValue={server.scopes} /><FieldDescription>Space-separated. Blank uses what the server asks for.</FieldDescription></Field>
              <div className="flex flex-wrap gap-2">
                <Button type="submit" size="sm" variant="outline">Save client</Button>
                <Button type="button" size="sm" variant="ghost" onClick={() => void run(async () => setDiscovery(await MCP.discoverAuth(server.id)))}>Discover authorization</Button>
                {server.authorized && <Button type="button" size="sm" variant="ghost" onClick={() => void run(() => MCP.signOut(server.id))}>Sign out</Button>}
              </div>
            </Fieldset>
          </form>}
          {discovery && <JsonData value={discovery} />}

          {tools.length > 0 && <div className="flex flex-col gap-2">
            <p className="font-medium">Tool permissions</p>
            <p className="text-xs text-muted-foreground">Names, descriptions and annotations come from the server and are untrusted. Annotations are hints, not permission grants.</p>
            <ul className="flex flex-col divide-y rounded-md border">
              {tools.map((tool) => <li key={tool.name} className="flex items-center gap-3 px-3 py-2">
                <div className="min-w-0 flex-1"><code className="block truncate text-xs">{tool.name}</code><span className="block truncate text-xs text-muted-foreground">{tool.description}</span></div>
                <Badge variant="outline">{tool.effect || "unclassified"}</Badge>
                <select aria-label={`Permission for ${tool.name}`} className="rounded-md border bg-background p-1.5" disabled={busy} value={tool.permission || "ask"} onChange={(event) => void run(() => MCP.setToolPermission(server.id, tool.name, event.target.value))}><option value="ask">Ask</option><option value="allow">Allow</option><option value="deny">Deny</option></select>
              </li>)}
            </ul>
          </div>}

          <div className="flex flex-col gap-2">
            <div className="flex items-center justify-between"><p className="font-medium">Diagnostics</p><Button size="sm" variant="outline" disabled={busy || !signedIn} onClick={() => void run(() => MCP.refreshCatalog(server.id))}>Refresh tools</Button></div>
            <Failure error={diagnostics.error} retry={diagnostics.reload} />
            {diagnostics.loading && !diagnostics.data ? <Loading>Loading diagnostics…</Loading> : <JsonData value={diagnostics.data} />}
          </div>
        </div>
      </details>

      <Dialog open={remove} onOpenChange={(open) => { if (!busy) setRemove(open); }}>
        <DialogPopup>
          <DialogHeader><DialogTitle>Disconnect {server.name}?</DialogTitle><DialogDescription>This removes the connection and its stored credentials. It does not delete anything on the service itself.</DialogDescription></DialogHeader>
          <DialogPanel><Failure error={error} /></DialogPanel>
          <DialogFooter>
            <Button variant="outline" disabled={busy} onClick={() => setRemove(false)}>Keep connected</Button>
            <Button variant="destructive" disabled={busy} onClick={() => void run(async () => { await MCP.remove(server.id); setRemove(false); onRemoved?.(); })}>Disconnect</Button>
          </DialogFooter>
        </DialogPopup>
      </Dialog>
    </section>
  );
}
