import {
  Bell, Blocks, Bot, CalendarClock, Code, Eye, FlaskConical, GitPullRequest, Hand, Hourglass, MessageSquareText, Plug, Radio, ShieldCheck, Split, SquareTerminal, Timer, Webhook, Workflow,
  type LucideIcon,
} from "lucide-react";

const ICONS: Record<string, LucideIcon> = {
  "bot.tell": Bot, "task.start": Code, "task.prompt": MessageSquareText, "task.wait": Hourglass, notify: Bell, "http.request": Webhook,
  "github.query": GitPullRequest, "mcp.tool": Plug, condition: Split, delay: Timer, approval: ShieldCheck, "command.run": SquareTerminal, "test.run": FlaskConical,
};

export function BlockIcon({ type, className }: { type: string; className?: string }) {
  const Icon = ICONS[type] ?? Blocks;
  return <Icon aria-hidden className={className} />;
}

const TRIGGER_ICONS: Record<string, LucideIcon> = { manual: Hand, schedule: CalendarClock, event: Radio, watch: Eye, webhook: Webhook };

/** What starts a flow (Trigger.type), as an icon. */
export function TriggerIcon({ type, className }: { type: string; className?: string }) {
  const Icon = TRIGGER_ICONS[type] ?? Workflow;
  return <Icon aria-hidden className={className} />;
}
