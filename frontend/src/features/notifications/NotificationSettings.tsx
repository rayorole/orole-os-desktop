import { useEffect, type ComponentType, type CSSProperties } from "react";
import { Bell, Check, Hand, Inbox, Lock, MessageCircle, Play, Smartphone, Volume2, VolumeOff, X } from "lucide-react";
import type { KindPref } from "@/mygo";
import { inDesktop } from "@/lib/backend";
import { playSound } from "@/lib/audio";
import { cn } from "@/lib/utils";
import { useDaemon } from "@/stores/daemon";
import { useUI } from "@/stores/ui";
import { useNotifications, type NotificationKind } from "@/stores/notifications";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { PushSettings } from "@/features/notifications/PushSettings";

type Icon = ComponentType<{ className?: string }>;

/** Every notification kind the daemon reports, in the order of the center's badges. */
const ROWS: { kind: NotificationKind; title: string; description: string; icon: Icon; tone: string; locked?: string }[] = [
  { kind: "approval", title: "Needs you", description: "A task, bot or flow waits for your yes or no, or for an answer.", icon: Hand, tone: "var(--warning)", locked: "Always shown, so nothing waits on you unseen." },
  { kind: "failed", title: "Failed", description: "A task, bot reply or flow run stopped with an error.", icon: X, tone: "var(--destructive)" },
  { kind: "finished", title: "Finished", description: "A task is done, or a flow run you started finished.", icon: Check, tone: "var(--success)" },
  { kind: "reply", title: "Bot replies", description: "A bot answered. Read at once when you are already in that chat.", icon: MessageCircle, tone: "var(--info)" },
  { kind: "message", title: "Flow messages", description: "A Notify step in one of your flows.", icon: Bell, tone: "var(--muted-foreground)" },
];

/** A channel toggle. `locked` is on and cannot be changed, so it keeps its on look instead of dimming. */
function Channel({ on, disabled, locked, label, onToggle, iconOn: On, iconOff: Off }: { on: boolean; disabled: boolean; locked?: boolean; label: string; onToggle: () => void; iconOn: Icon; iconOff: Icon }) {
  const Glyph = on ? On : Off;
  return <Button variant="outline" size="sm" aria-label={label} aria-pressed={on} disabled={disabled || locked} onClick={onToggle} data-ui-sound="none"
    className={cn("h-8 w-12 rounded-full",
      on ? "border-info/40 bg-info/14 text-info-foreground hover:bg-info/20" : "text-muted-foreground/60",
      locked && "disabled:cursor-default disabled:opacity-100")}>
    <Glyph className="size-4" />
  </Button>;
}

export function NotificationSettings() {
  const n = useNotifications();
  const link = useDaemon((state) => state.link);
  useEffect(() => n.startSync(), [n.startSync]);
  const offline = link === "offline";

  if (!inDesktop) return <Empty><EmptyHeader><EmptyTitle>Open the desktop app</EmptyTitle><EmptyDescription>Notification preferences are saved by the Orole daemon, not in the browser preview.</EmptyDescription></EmptyHeader></Empty>;
  const disabled = offline || n.prefsSaving || n.prefsLoading || !n.prefs;
  const pref = (kind: NotificationKind): KindPref => n.prefs?.kinds?.[kind] ?? { inApp: true, sound: true, push: false };
  return (
    <div className="flex max-w-3xl flex-col gap-4">
      <p className="text-muted-foreground">Notifications appear in the bell at the top right, each kind with its own sound, and can also be pushed to your phone or PC through ntfy. Changes save automatically.</p>
      {offline && <Alert variant="warning"><AlertTitle>Daemon offline</AlertTitle><AlertDescription>Reconnect to load or save notification preferences.</AlertDescription></Alert>}
      {n.prefsError && <Alert variant="error"><AlertTitle>Preferences could not be loaded or saved</AlertTitle><AlertDescription>{n.prefsError}</AlertDescription></Alert>}
      {(n.prefsError || offline) && <Button size="sm" variant="outline" className="self-start" disabled={n.prefsLoading || n.prefsSaving} onClick={() => { void useDaemon.getState().refresh(); void n.loadPrefs(); }}>Retry connection</Button>}
      {n.prefsLoading && !n.prefs && <div role="status" aria-label="Loading notification preferences"><Skeleton className="h-72 w-full" /></div>}
      {n.prefs && <section aria-label="Notification preferences" className="rounded-xl border bg-card">
        <div className="flex items-end gap-4 border-b px-4 py-2.5 text-xs text-muted-foreground">
          <span className="flex-1">Event</span>
          <span className="w-12 text-center">In app</span>
          <span className="w-[5.5rem] text-center">Sound</span>
          <span className="w-12 text-center">Push</span>
        </div>
        <ul className="divide-y">
          {ROWS.map((row) => {
            const p = pref(row.kind);
            const Glyph = row.icon;
            return <li key={row.kind} className="flex items-center gap-4 px-4 py-3">
              <span aria-hidden="true" style={{ "--tone": row.tone } as CSSProperties} className="inline-flex size-7 flex-none items-center justify-center self-start rounded-lg bg-[color-mix(in_oklab,var(--tone)_14%,var(--card))] text-(--tone)">
                <Glyph className="size-4" />
              </span>
              <div className="min-w-0 flex-1">
                <p className="font-medium">{row.title}</p>
                <p className="text-muted-foreground text-xs">{row.description}</p>
                {row.locked && <p className="mt-0.5 flex items-center gap-1 text-muted-foreground text-xs"><Lock aria-hidden="true" className="size-3" />{row.locked}</p>}
              </div>
              <Channel on={p.inApp} disabled={disabled} locked={!!row.locked} label={`Show ${row.title} in app`} iconOn={Inbox} iconOff={Inbox}
                onToggle={() => void n.setKindPref(row.kind, { ...p, inApp: !p.inApp })} />
              <div className="flex items-center gap-1">
                <Channel on={p.inApp && p.sound} disabled={disabled || !p.inApp} label={`Play a sound for ${row.title}`} iconOn={Volume2} iconOff={VolumeOff}
                  onToggle={() => {
                    const sound = !p.sound;
                    void n.setKindPref(row.kind, { ...p, sound });
                    if (sound) void playSound(row.kind, "notification");
                  }} />
                <Tooltip><TooltipTrigger render={<Button variant="ghost" size="icon-xs" aria-label={`Play the ${row.title} sound`} data-ui-sound="none" onClick={() => void playSound(row.kind, "notification")} />}><Play /></TooltipTrigger><TooltipPopup>Play sound</TooltipPopup></Tooltip>
              </div>
              <Channel on={p.push} disabled={disabled} label={`Send ${row.title} as a push notification`} iconOn={Smartphone} iconOff={Smartphone}
                onToggle={() => void n.setKindPref(row.kind, { ...p, push: !p.push })} />
            </li>;
          })}
        </ul>
        <div className="flex items-center gap-3 border-t px-4 py-2.5">
          <p role="status" className="flex-1 text-xs text-muted-foreground">{n.prefsSaving ? "Saving…" : ""}</p>
          <Button size="xs" variant="ghost" className="text-muted-foreground" onClick={() => useUI.getState().openSettings("customization", "sound")}>Volume</Button>
        </div>
      </section>}
      {n.prefs && <PushSettings />}
    </div>
  );
}
