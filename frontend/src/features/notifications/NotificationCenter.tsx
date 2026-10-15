import { useEffect, useRef, useState, type ComponentType, type CSSProperties } from "react";
import { Bell, Check, Hand, MessageCircle, ShieldQuestion, SquareTerminal, TriangleAlert, Workflow, X } from "lucide-react";
import { useNotifications, type NotificationItem, type NotificationTab } from "@/stores/notifications";
import { useDaemon } from "@/stores/daemon";
import { useChat } from "@/stores/chat";
import { useUI } from "@/stores/ui";
import { inDesktop } from "@/lib/backend";
import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Popover, PopoverDescription, PopoverPopup, PopoverTitle, PopoverTrigger } from "@/components/ui/popover";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTab } from "@/components/ui/tabs";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { BotAvatar } from "@/components/shell/BotAvatar";
import { dayGroup, shortTime } from "./format";
import { openTarget, parseTarget } from "./targets";

type Icon = ComponentType<{ className?: string }>;

/** Per kind: the corner badge, its tone, and how a screen reader hears it. */
const KINDS: Record<string, { label: string; badge: Icon; tone: string }> = {
  approval: { label: "Needs you", badge: Hand, tone: "var(--warning)" },
  failed: { label: "Failed", badge: X, tone: "var(--destructive)" },
  finished: { label: "Finished", badge: Check, tone: "var(--success)" },
  reply: { label: "Replied", badge: MessageCircle, tone: "var(--info)" },
  message: { label: "Message", badge: Bell, tone: "var(--muted-foreground)" },
};
const kindOf = (item: NotificationItem) => KINDS[item.kind] ?? KINDS.message!;

/** A message's tone follows its level; other kinds have their own. */
function messageTone(item: NotificationItem) {
  if (item.level === "error") return "var(--destructive)";
  if (item.level === "warning") return "var(--warning)";
  if (item.level === "success") return "var(--success)";
  return "var(--muted-foreground)";
}

function sourceIcon(item: NotificationItem): Icon {
  if (item.source === "task") return SquareTerminal;
  if (item.source.startsWith("automation/")) return Workflow;
  if (item.source === "policy") return ShieldQuestion;
  return Bell;
}

/** The thing that reported, with the kind as a small badge on its corner. Messages show a tinted tile instead. */
function Mark({ item }: { item: NotificationItem }) {
  const target = parseTarget(item.target);
  const bot = useChat((s) => target?.type === "bot" || target?.type === "computer" ? s.bots.find((b) => b.id === target.id) : undefined);
  const kind = kindOf(item);
  if (item.kind === "message") {
    const Glyph = item.level === "warning" || item.level === "error" ? TriangleAlert : sourceIcon(item);
    return <span aria-hidden="true" style={{ "--tone": messageTone(item) } as CSSProperties}
      className="inline-flex size-6 flex-none items-center justify-center rounded-md bg-[color-mix(in_oklab,var(--tone)_14%,var(--card))] text-(--tone) dark:bg-[color-mix(in_oklab,var(--tone)_22%,var(--card))]">
      <Glyph className="size-3.5" />
    </span>;
  }
  const Glyph = sourceIcon(item);
  const KindBadge = kind.badge;
  return <span aria-hidden="true" className="relative inline-flex size-6 flex-none" style={{ "--tone": kind.tone } as CSSProperties}>
    {bot ? <BotAvatar name={bot.name} avatar={bot.avatar} className="size-6" />
      : <span className="inline-flex size-full items-center justify-center rounded-md bg-muted text-muted-foreground shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--foreground)_6%,transparent)] dark:bg-background">
        <Glyph className="size-3.5" />
      </span>}
    <span className="absolute -right-1 -bottom-1 inline-flex size-3.5 items-center justify-center rounded-full bg-(--tone) text-card shadow-[0_0_0_2px_var(--card)]">
      <KindBadge className="size-2.5" />
    </span>
  </span>;
}

function Row({ item, now, onOpen }: { item: NotificationItem; now: number; onOpen: (item: NotificationItem) => void }) {
  const kind = kindOf(item);
  const time = shortTime(item.at, now);
  const date = new Date(item.at * 1000);
  return <li>
    <button type="button" data-kind={item.kind} data-read={item.read || undefined} onClick={() => onOpen(item)}
      aria-label={[item.title, item.needsYou ? "Needs your approval" : kind.label, item.body, time, item.read ? "" : "unread"].filter(Boolean).join(", ")}
      className="flex h-10 w-full min-w-0 cursor-pointer items-center gap-2.5 rounded-lg px-2 text-left outline-none transition-colors duration-150 hover:bg-foreground/4 focus-visible:ring-2 focus-visible:ring-ring/50 active:bg-foreground/6 motion-reduce:transition-none">
      <Mark item={item} />
      <span className="flex min-w-0 flex-1 items-baseline gap-1.5 text-[13px] leading-5">
        <span className={cn("max-w-[70%] min-w-0 flex-none truncate", item.read ? "text-card-foreground/80" : "font-medium text-card-foreground")}>{item.title}</span>
        {item.body && <span className="min-w-0 flex-1 truncate text-muted-foreground">{item.body}</span>}
      </span>
      <time dateTime={date.toISOString()} className="flex-none text-xs leading-4 tabular-nums text-muted-foreground">{time}</time>
      <span aria-hidden="true" className={cn("size-1.5 flex-none rounded-full bg-primary transition-[opacity,scale] duration-200 motion-reduce:transition-none", item.read ? "scale-50 opacity-0" : "scale-100 opacity-100")} />
    </button>
  </li>;
}

/** The time rows are aged against: reset when the panel opens, then ticking once a minute while it is open. */
function useMinuteClock(enabled: boolean) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!enabled) return;
    const timer = window.setInterval(() => setNow(Date.now()), 60_000);
    return () => window.clearInterval(timer);
  }, [enabled]);
  return [now, () => setNow(Date.now())] as const;
}

export function NotificationCenter() {
  const [open, setOpen] = useState(false);
  const cornerAnchor = useRef<HTMLSpanElement>(null);
  const n = useNotifications();
  const link = useDaemon((state) => state.link);
  useEffect(() => n.startSync(), [n.startSync]);
  const [now, resetClock] = useMinuteClock(open);
  const offline = link === "offline";
  const unavailable = !inDesktop || offline;
  const countKnown = n.loaded && !n.error && !unavailable;

  const choose = (item: NotificationItem) => {
    if (!item.read) void n.markRead([item.id]);
    if (openTarget(item.target)) setOpen(false);
  };
  const groups: { label: string; items: NotificationItem[] }[] = [];
  for (const item of n.items) {
    const label = dayGroup(item.at, now);
    const last = groups[groups.length - 1];
    if (last?.label === label) last.items.push(item); else groups.push({ label, items: [item] });
  }
  const needsYouTab = n.tab === "needs-you";

  return (
    <Popover open={open} onOpenChange={(value) => { setOpen(value); if (value) { resetClock(); void n.load(); } }}>
      <span ref={cornerAnchor} aria-hidden="true" className="pointer-events-none fixed right-3 top-[var(--titlebar-h)] size-px" />
      <Tooltip><TooltipTrigger render={<PopoverTrigger render={<Button variant="ghost" size="icon-xs" className="relative" />} />} aria-label={countKnown ? `Notifications, ${n.unread} unread` : "Notifications, unread count unavailable"}>
        <Bell />
        {countKnown && n.unread > 0 && <Badge size="sm" className="absolute -right-1 -top-1" aria-hidden="true">{n.unread > 99 ? "99+" : n.unread}</Badge>}
      </TooltipTrigger><TooltipPopup>Notifications</TooltipPopup></Tooltip>
      {/* Always below the titlebar: flipping (to the anchor's side when a short window cannot fit 480px) would put the
          popup over the native minimise/maximise/close buttons. Its height follows --available-height instead. */}
      <PopoverPopup anchor={cornerAnchor} align="end" sideOffset={8} collisionAvoidance={{ side: "none", fallbackAxisSide: "none" }}
        className="w-[360px] max-w-[calc(100vw-16px)] rounded-2xl bg-card text-card-foreground [&>[data-slot=popover-viewport]]:p-0">
        <PopoverTitle className="sr-only">Notifications</PopoverTitle>
        <PopoverDescription className="sr-only">{countKnown ? `${n.unread} unread, ${n.needsYou} waiting for you` : "Activity from your tasks, bots and flows."}</PopoverDescription>
        <div className="flex max-h-[min(480px,var(--available-height))] flex-col text-sm">
          <div className="flex h-11 flex-none items-center gap-2 px-2">
            <Tabs value={n.tab} onValueChange={(value) => n.setTab(value as NotificationTab)}>
              <TabsList aria-label="Filter" className="h-7 rounded-full bg-foreground/6 p-0.5 [&>[data-slot=tab-indicator]]:rounded-full">
                <TabsTab value="all" size="sm" className="h-6 rounded-full px-2.5 text-xs sm:h-6 sm:text-xs">All</TabsTab>
                <TabsTab value="needs-you" size="sm" className="h-6 gap-1 rounded-full px-2.5 text-xs sm:h-6 sm:text-xs">
                  Needs you
                  {countKnown && n.needsYou > 0 && <span aria-label={`${n.needsYou} waiting`} className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-[color-mix(in_oklab,var(--warning)_18%,transparent)] px-1 text-[10px] leading-none tabular-nums text-warning-foreground">{n.needsYou}</span>}
                </TabsTab>
              </TabsList>
            </Tabs>
            <span className="flex-1" />
            {inDesktop && <Button size="xs" variant="ghost" className="mr-0.5 text-muted-foreground" disabled={unavailable || n.busy || n.unread === 0} onClick={() => void n.markRead([], true)}>Mark all read</Button>}
          </div>
          <div className="min-h-0 overflow-y-auto overscroll-contain" aria-busy={n.loading || n.busy}>
            {!inDesktop ? (
              <Notice title="Open the desktop app">Notifications are kept by the Orole daemon, which this browser preview does not run.</Notice>
            ) : (
              <>
                {(offline || n.error) && <p role="alert" className="mx-3.5 mb-2 rounded-lg bg-warning/8 px-2.5 py-2 text-xs text-warning-foreground">{offline ? "The daemon is offline. Showing the last loaded notifications." : `Could not load notifications: ${n.error}`}</p>}
                {n.actionError && <p role="alert" className="mx-3.5 mb-2 rounded-lg bg-destructive/8 px-2.5 py-2 text-xs text-destructive-foreground">Not marked read: {n.actionError}</p>}
                {n.loading && !n.loaded && <div role="status" aria-label="Loading notifications" className="flex flex-col gap-1 px-3.5 pb-3 pt-2"><Skeleton className="h-8 w-full" /><Skeleton className="h-8 w-full" /><Skeleton className="h-8 w-full" /></div>}
                {n.loaded && !n.error && n.items.length === 0 && (needsYouTab
                  ? <Notice title="Nothing needs you">Approvals and questions from tasks, bots and flows wait here until you answer them.</Notice>
                  : <Notice title="No notifications yet">Tasks, bots and flows report here when they finish, fail or need you.</Notice>)}
                {groups.length > 0 && <ul aria-label={needsYouTab ? "Waiting for you" : "Notifications"} className="flex flex-col px-1.5 pb-1.5">
                  {groups.map((group) => <li key={group.label}>
                    <div className="sticky top-0 z-1 bg-card px-2 pb-1 pt-2 text-xs leading-4 text-muted-foreground">{group.label}</div>
                    <ul className="flex flex-col">{group.items.map((item) => <Row key={item.id} item={item} now={now} onOpen={choose} />)}</ul>
                  </li>)}
                </ul>}
              </>
            )}
          </div>
          <div className="flex h-10 flex-none items-center justify-between gap-2 px-2 shadow-[inset_0_1px_0_0_var(--border)]">
            <Button size="xs" variant="ghost" className="text-muted-foreground" onClick={() => { setOpen(false); useUI.getState().openSettings("notifications", "notifications"); }}>Settings</Button>
          </div>
        </div>
      </PopoverPopup>
    </Popover>
  );
}

function Notice({ title, children }: { title: string; children: string }) {
  return <div className="flex flex-col items-center gap-1 px-6 py-8 text-center">
    <p className="font-medium text-card-foreground">{title}</p>
    <p className="text-xs text-muted-foreground">{children}</p>
  </div>;
}
