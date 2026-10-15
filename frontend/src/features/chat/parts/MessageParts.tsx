import { CircleAlertIcon, CircleSlashIcon, RotateCcwIcon } from "lucide-react";
import type { ReactNode } from "react";
import { Markdown } from "@/components/Markdown";
import { Button } from "@/components/ui/button";
import type { Message, MsgPart } from "@/mygo";
import { sourcesOf } from "./format";
import { NoticeLine } from "./Steps";
import { ThinkingPanel, type BotRef } from "./ThinkingPanel";
import { MemoryChips } from "@/components/assistant-ui/elements/memory-chips";
import { AttachmentPill, ReportCard, SourcesButton } from "./Sources";
import { QuestionCard } from "./Question";
import { StructuredPart } from "./StructuredPart";
import { parseDocument } from "./DocumentCard";
import { MentionText } from "../MentionText";
import type { MentionTarget } from "../mentions";
import { splitQuote } from "../quote";
import { splitBubbles } from "./bubbles";
import { Bubble } from "./Bubble";
import { TaskChips } from "./TaskChip";
import { CallCard } from "@/features/jarvis/CallCard";
import { ComputerCard } from "@/features/computer/ComputerCard";

/** Whether a user message has anything to draw (the setup kickoff is text the user never sees). */
export const hasVisibleParts = (parts: readonly MsgPart[]): boolean => parts.some((p) => p.type === "attachment" || (p.type === "text" && !p.hidden));

/** The sent text and files of a user message; plain text, never markdown. */
export function UserParts({ parts, targets }: { parts: readonly MsgPart[]; /** bots and skills to draw as chips; absent = plain text */ targets?: readonly MentionTarget[] }) {
  const files = parts.filter((p) => p.type === "attachment");
  const text = parts.filter((p) => p.type === "text" && !p.hidden).map((p) => p.text ?? "").join("\n\n");
  const { quote, rest } = splitQuote(text);
  return <>
    {files.length > 0 && <div className="mb-1 flex flex-wrap gap-1.5">{files.map((p, i) => <AttachmentPill key={p.attachmentId || i} name={p.name || p.title} mime={p.mime} size={p.size} />)}</div>}
    {quote && <blockquote data-slot="chat-quote" className="selectable mb-1.5 border-s-2 border-primary/60 ps-2.5 text-muted-foreground text-xs whitespace-pre-wrap break-words">{quote}</blockquote>}
    {rest && <p className="selectable whitespace-pre-wrap break-words">{targets?.length ? <MentionText text={rest} targets={targets} /> : rest}</p>}
  </>;
}

export { Bubble };

/**
 * Renders what an assistant message holds: what the bot did as one live thinking panel (its thinking, tool steps, the
 * pages it read, research outline and handoffs), then the answer in order (text, report, questions, notices, the code tasks
 * it started), then the memory it wrote as chips. Sources ride with the search that found them, and for a report in its
 * "N sources" popover; they are never listed in the answer.
 * Pure over its props (task chips read live task state from the agents store), so the builder preview can use it without a runtime.
 */
export function MessageParts({ message, onAnswer, botName = "This bot", botAvatar = "", bots = [], onOpenBot }: { message: Pick<Message, "parts" | "status"> & Partial<Pick<Message, "id" | "createdAt" | "finishedAt">>; /** Sends the user's answer to a question; absent = questions are read-only. */ onAnswer?: (answer: string) => void; /** Who wrote the message, named in its handoffs. */ botName?: string; botAvatar?: string; /** Every bot, to draw the avatars of the bots a handoff names. */ bots?: readonly BotRef[]; /** Opens another bot's conversation (a handoff's worker); absent = no link. */ onOpenBot?: (botId: string) => void }) {
  const hasReport = message.parts.some((p) => p.type === "report");
  const sources = sourcesOf(message.parts);
  const searched = message.parts.some((p) => p.type === "tool" && (p.name === "web_search" || p.name === "web_fetch"));
  const memory = message.parts.filter((p) => p.type === "memory");
  // Every task the reply started is listed together, where the first one was started.
  const tasks = message.parts.filter((p) => p.type === "task" && p.id);
  // Every use of the computer in a reply is one card where the first one was, showing the latest step.
  const computer = message.parts.filter((p) => p.type === "computer");
  // A document rewritten under the same title shows once, at its latest version.
  const latestDocument = new Map<string, number>();
  message.parts.forEach((p, i) => { if (p.type === "ui" && p.name === "document") latestDocument.set(parseDocument(p.spec)?.title ?? "", i); });
  const blocks: ReactNode[] = [];
  blocks.push(<ThinkingPanel key="thinking" message={message} sources={sources} botName={botName} botAvatar={botAvatar} bots={bots} onOpenBot={onOpenBot} />);
  message.parts.forEach((part, i) => {
    switch (part.type) {
      case "text":
        if (part.text) splitBubbles(part.text).forEach((chunk, j) => blocks.push(<Bubble key={`${i}.${j}`}><Markdown>{chunk}</Markdown></Bubble>));
        break;
      case "report":
        blocks.push(<ReportCard key={i} title={part.title} text={part.text ?? ""} sources={sources} />);
        break;
      case "question":
        if (part.text && part.choices?.length) blocks.push(<QuestionCard key={i} text={part.text} choices={part.choices} onAnswer={onAnswer} />);
        break;
      case "ui":
        if (part.name === "document" && latestDocument.get(parseDocument(part.spec)?.title ?? "") !== i) break;
        blocks.push(<StructuredPart key={i} part={part} at={message.id ? { messageId: message.id, partIndex: i } : undefined} />);
        break;
      case "notice":
        if (part.text) blocks.push(<NoticeLine key={i} text={part.text} />);
        break;
      case "call":
        blocks.push(<CallCard key={i} part={part} />);
        break;
      case "task":
        if (part === tasks[0]) blocks.push(<TaskChips key="tasks" parts={tasks} />);
        break;
      case "computer":
        if (part === computer[0]) blocks.push(<ComputerCard key="computer" part={computer[computer.length - 1]!} />);
        break;
      default:
        break; // work rows go to the timeline, memory writes to the chips below; citations ride along; attachments belong to user messages
    }
  });
  if (memory.length) blocks.push(<MemoryChips key="memory" chips={memory.map((part, i) => ({ id: part.id || `m${i}`, text: part.text || "Memory updated", change: "added" as const }))} />);
  if (!hasReport && sources.length && !searched) blocks.push(<SourcesButton key="sources" sources={sources} />);
  return <>{blocks}</>;
}

/** Why a reply stopped early, with Retry where the caller can honour it. Nothing is shown for a healthy message. */
export function MessageStatusNote({ status, error, onRetry }: { status?: string; error?: string; onRetry?: () => void }) {
  if (status === "error") {
    return <div role="alert" data-slot="chat-error" className="flex min-w-0 items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/8 px-2.5 py-1.5 text-xs text-destructive-foreground">
      <CircleAlertIcon aria-hidden className="mt-0.5 size-3.5 shrink-0 text-destructive" />
      <span className="selectable min-w-0 flex-1 break-words">{error || "The reply failed."}</span>
      {onRetry && <Button type="button" variant="outline" size="xs" onClick={onRetry}><RotateCcwIcon aria-hidden />Retry</Button>}
    </div>;
  }
  if (status === "canceled" || status === "incomplete") {
    return <p data-slot="chat-stopped" className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <CircleSlashIcon aria-hidden className="size-3.5 shrink-0" />
      {status === "canceled" ? "Stopped" : "This reply was interrupted"}
      {onRetry && <Button type="button" variant="ghost" size="xs" onClick={onRetry}><RotateCcwIcon aria-hidden />Retry</Button>}
    </p>;
  }
  return null;
}
