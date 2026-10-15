import { CheckIcon, CircleXIcon, LoaderCircleIcon, MonitorIcon, TriangleAlertIcon } from "lucide-react";
import { Bubble } from "@/features/chat/parts/Bubble";
import { cn } from "@/lib/utils";
import type { MsgPart } from "@/mygo";
import { useChat } from "@/stores/chat";

const STATUS_LABEL: Record<string, string> = { running: "Working", done: "Done", failed: "Failed", waiting: "Waiting for you" };

function StatusGlyph({ status }: { status: string }) {
  const base = "size-3.5 shrink-0";
  switch (status) {
    case "done":
      return <CheckIcon aria-hidden className={cn(base, "text-success")} />;
    case "failed":
      return <CircleXIcon aria-hidden className={cn(base, "text-destructive")} />;
    case "waiting":
      return <TriangleAlertIcon aria-hidden className={cn(base, "text-warning")} />;
    default:
      return <LoaderCircleIcon aria-hidden className={cn(base, "animate-spin text-info motion-reduce:animate-none")} />;
  }
}

/**
 * The bot used its computer in this reply: one compact card with what it is doing now. Clicking it opens the
 * Computer tab beside the conversation, where the screen and terminal are. The text is plain (already sanitized
 * by the daemon) and is never drawn as HTML or markdown.
 */
export function ComputerCard({ part }: { part: MsgPart }) {
  const selected = useChat((s) => s.selectedBotId);
  const botId = part.botId || selected;
  const open = useChat((s) => s.pane?.kind === "computer" && s.pane.id === botId);
  const status = part.status && Object.hasOwn(STATUS_LABEL, part.status) ? part.status : "running";
  const waiting = status === "waiting";
  return (
    <Bubble className={cn("p-0", waiting ? "ring-1 ring-warning/50" : open && "ring-1 ring-ring/60")}>
      <button type="button" data-slot="computer-card" data-status={status} aria-pressed={open} disabled={!botId}
        onClick={() => { if (botId) useChat.getState().openComputerPane(botId); }}
        className="flex w-full min-w-56 items-center gap-3 rounded-[inherit] px-3 py-1.5 text-start outline-none transition-colors hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none motion-reduce:transition-none">
        <MonitorIcon aria-hidden className="size-4 shrink-0 text-muted-foreground" />
        <span className="flex min-w-0 flex-1 flex-col">
          <span className="font-medium">Using the computer</span>
          {part.text && <span className="line-clamp-2 break-words text-muted-foreground">{part.text}</span>}
          {part.summary && part.summary !== part.text && <span data-slot="computer-card-summary" className="line-clamp-2 break-words text-muted-foreground">{part.summary}</span>}
        </span>
        <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted-foreground">
          <StatusGlyph status={status} />
          {STATUS_LABEL[status]}
          {waiting && <span data-slot="computer-card-action" className="ms-1 border-s ps-2 font-medium text-foreground">Take over</span>}
        </span>
      </button>
    </Bubble>
  );
}
