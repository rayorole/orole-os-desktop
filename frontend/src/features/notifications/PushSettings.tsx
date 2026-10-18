import { useCallback, useEffect, useState, type ReactNode } from "react";
import { TriangleAlert } from "lucide-react";
import { Notifications } from "@/mygo";
import type { NtfyUpdate, NtfyView } from "@/mygo";
import { useDaemon } from "@/stores/daemon";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { ConfirmDialog } from "@/features/projects/dialogs";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);
/** Shown in the token field while one is stored: the token itself never reaches the window. */
const MASK = "••••••••••••";

function Row({ label, labelId, hint, children }: { label: string; labelId: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5 px-4 py-3">
      <div id={labelId} className="font-medium">{label}</div>
      {hint && <div className="text-muted-foreground text-xs">{hint}</div>}
      <div className="flex items-center gap-2">{children}</div>
    </div>
  );
}

/** ntfy configuration. Which kinds push is the Push column of the table above; this is where and whether. */
export function PushSettings() {
  const link = useDaemon((state) => state.link);
  const offline = link === "offline";
  const [view, setView] = useState<NtfyView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [server, setServer] = useState("");
  const [token, setToken] = useState("");
  const [copied, setCopied] = useState(false);
  const [testing, setTesting] = useState(false);
  const [test, setTest] = useState<{ ok: boolean; text: string } | null>(null);
  const [regenerating, setRegenerating] = useState(false);

  const adopt = useCallback((next: NtfyView) => { setView(next); setServer(next.server); setToken(""); }, []);
  useEffect(() => {
    let live = true;
    Notifications.ntfy().then((next) => { if (live) { adopt(next); setError(null); } }, (e) => { if (live) setError(message(e)); });
    return () => { live = false; };
  }, [adopt, link]);

  const save = async (update: Partial<NtfyUpdate>) => {
    if (!view) return;
    setSaving(true);
    setTest(null);
    try {
      adopt(await Notifications.setNtfy({ enabled: view.enabled, server: view.server, token: null, regenerateTopic: false, ...update }));
      setError(null);
    } catch (e) { setError(message(e)); } finally { setSaving(false); }
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      const result = await Notifications.testNtfy();
      setTest(result.ok ? { ok: true, text: "Sent. It should arrive in your ntfy app within a few seconds." } : { ok: false, text: result.error || "The push was not sent." });
    } catch (e) { setTest({ ok: false, text: message(e) }); } finally { setTesting(false); }
  };

  const copy = (text: string) => {
    void navigator.clipboard?.writeText(text).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }, () => setCopied(false));
  };

  if (!view) {
    return error
      ? <Alert variant="error"><AlertTitle>Push settings could not be loaded</AlertTitle><AlertDescription>{error}</AlertDescription></Alert>
      : <div role="status" aria-label="Loading push settings"><Skeleton className="h-40 w-full" /></div>;
  }
  const disabled = offline || saving;
  const serverDirty = server.trim() !== view.server;
  const tokenDirty = token.trim() !== "";
  const dirty = serverDirty || tokenDirty;

  return (
    <section aria-label="Push notifications" className="divide-y rounded-xl border bg-card">
      <div className="flex items-center gap-4 px-4 py-3">
        <div className="min-w-0 flex-1">
          <p id="push-master" className="font-medium">Push notifications</p>
          <p className="text-muted-foreground text-xs">Send notifications to the ntfy app on this PC or your phone. Pick the kinds in the Push column above. Everything starts off.</p>
        </div>
        <Switch aria-labelledby="push-master" checked={view.enabled} disabled={disabled} onCheckedChange={(enabled) => void save({ enabled })} />
      </div>
      {error && <div className="px-4 py-3"><Alert variant="error"><AlertTitle>Push settings could not be saved</AlertTitle><AlertDescription>{error}</AlertDescription></Alert></div>}
      <Row label="Server" labelId="push-server" hint={<>The default is the public server at ntfy.sh. Use your own (<code>ntfy serve</code>) to keep notifications on your network.</>}>
        <Input aria-labelledby="push-server" value={server} disabled={disabled} spellCheck={false} placeholder="https://ntfy.sh" onChange={(e) => setServer(e.target.value)} />
      </Row>
      <Row label="Topic" labelId="push-topic" hint="Subscribe to this topic in the ntfy app.">
        {view.topic
          ? <>
            <Input aria-labelledby="push-topic" readOnly value={view.topic} className="font-mono" />
            <Button size="sm" variant="outline" onClick={() => copy(view.topic)}>{copied ? "Copied" : "Copy"}</Button>
            <Button size="sm" variant="outline" disabled={disabled} onClick={() => setRegenerating(true)}>Regenerate</Button>
          </>
          : <p className="text-muted-foreground text-xs">Created when you switch push on.</p>}
      </Row>
      <div className="px-4 py-3">
        <Alert variant="warning">
          <TriangleAlert aria-hidden="true" />
          <AlertTitle>The topic works like a password</AlertTitle>
          <AlertDescription>On a public server such as ntfy.sh, anyone who knows the topic can read what Orole sends and post to it. Keep it private. On ntfy.sh, messages pass through their server and are cached for about 12 hours. Notification titles and text, which can come from mail, issues and bot replies, are sent as they are.</AlertDescription>
        </Alert>
      </div>
      <Row label="Access token" labelId="push-token" hint="Optional. Needed only when your server restricts topics. Stored in Windows Credential Manager.">
        <Input aria-labelledby="push-token" type="password" autoComplete="off" value={token} disabled={disabled} placeholder={view.tokenSet ? MASK : "tk_…"} onChange={(e) => setToken(e.target.value)} />
        {view.tokenSet && <Button size="sm" variant="outline" disabled={disabled} onClick={() => void save({ token: "" })}>Remove</Button>}
      </Row>
      <div className="flex flex-col gap-2 px-4 py-3">
        <div className="flex items-center gap-2">
          <Button size="sm" disabled={disabled || !dirty} onClick={() => void save({ server: server.trim(), token: tokenDirty ? token.trim() : null })}>Save changes</Button>
          <Button size="sm" variant="outline" loading={testing} disabled={disabled || testing || !view.topic || dirty} onClick={() => void runTest()}>Send test</Button>
          {dirty && <span className="text-muted-foreground text-xs">Save before testing.</span>}
          {!view.topic && <span className="text-muted-foreground text-xs">Switch push on to create a topic.</span>}
        </div>
        {test && <p role={test.ok ? "status" : "alert"} className={test.ok ? "text-success-foreground text-xs" : "selectable break-words text-destructive-foreground text-xs"}>{test.text}</p>}
      </div>
      <ConfirmDialog open={regenerating} onOpenChange={setRegenerating} title="Regenerate the topic?" confirmLabel="Regenerate"
        onConfirm={async () => { await Notifications.setNtfy({ enabled: view.enabled, server: view.server, token: null, regenerateTopic: true }).then(adopt); setTest(null); }}>
        <p>Orole switches to a new random topic. Subscribe to it in the ntfy app; the old topic stops receiving pushes.</p>
      </ConfirmDialog>
    </section>
  );
}
