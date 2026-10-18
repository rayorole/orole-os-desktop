import { useLayoutEffect, useRef, useState, type ClipboardEvent, type KeyboardEvent } from "react";
import { ArrowUpIcon, FolderIcon, GitBranchIcon, PaperclipIcon } from "lucide-react";
import { Agents, type Session } from "@/mygo";
import { composerFrame } from "@/components/assistant-ui/elements/surfaces";
import { pastedFiles } from "@/lib/attachmentFiles";
import { useFileDrop } from "@/lib/useFileDrop";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { ErrorNote } from "@/features/agents/shared";
import { useAction } from "@/features/agents/format";
import { useAgents } from "@/stores/agents";
import { useTaskDrafts } from "@/stores/taskDrafts";
import { cn } from "@/lib/utils";
import { composerState } from "./composerState";
import { ComposerContext } from "./ComposerContext";
import { ComposerVoice } from "./ComposerVoice";
import { MessageQueue } from "./MessageQueue";
import { PermissionModeSelect } from "./PermissionModeSelect";
import type { PermissionMode } from "./permissionModes";
import { useTaskMessage } from "./useTaskMessage";
import { TaskAddMenu, TaskAttachmentPreviews, TaskModelPicker } from "./TaskMessageControls";
const MAX_INPUT_PX = 240;

/** Follow-up box for a Code task: one surface, the draft on top, one toolbar underneath. Enter sends; Ctrl+Enter inserts an explicit line. */
export function Composer({ session }: { session: Session }) {
  return <SessionComposer key={session.id} session={session} />;
}

function SessionComposer({ session }: { session: Session }) {
  // The draft lives in a store keyed by session, so it survives leaving the task and coming back.
  const text = useTaskDrafts((s) => s.drafts[session.id] ?? "");
  const setText = (next: string | ((current: string) => string)) => useTaskDrafts.getState().setDraft(session.id, next);
  const [voiceActive, setVoiceActive] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  const action = useAction();
  const state = composerState(session);
  const message = useTaskMessage(session);
  const inactive = action.busy || voiceActive;
  const modeControl = session.followup;
  const where = session.worktreeOwned ? "Worktree" : session.machine && session.machine !== "local" ? session.machine : "Local checkout";

  const send = () => {
    const prompt = text.trim();
    if ((!prompt && !message.attachments.ids.length) || action.busy || voiceActive || !state.canSend || !message.attachments.ready) return;
    void action.run(async () => {
      await message.send(prompt);
      setText((current) => current.trim() === prompt ? "" : current);
    });
  };
  // The box grows with its content (wrapped lines included) up to a cap, then scrolls.
  useLayoutEffect(() => {
    const el = input.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, MAX_INPUT_PX)}px`;
  }, [text]);
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    if (e.ctrlKey) {
      // Browsers do not insert a line break on Ctrl+Enter; do it explicitly.
      e.preventDefault();
      const start = e.currentTarget.selectionStart;
      const end = e.currentTarget.selectionEnd;
      setText((value) => `${value.slice(0, start)}\n${value.slice(end)}`);
      requestAnimationFrame(() => input.current?.setSelectionRange(start + 1, start + 1));
    } else if (e.shiftKey || e.altKey) {
      // Shift/Alt+Enter keep the browser's own new line.
    } else if (!e.metaKey) {
      e.preventDefault();
      send();
    }
  };
  const insertText = (value: string) => {
    setText((current) => current ? `${current}${/\s$/.test(current) ? "" : " "}${value}` : value);
    requestAnimationFrame(() => input.current?.focus());
  };
  // The daemon decides whether the mode can change and when it takes effect; this only relays the choice.
  const changeMode = async (mode: PermissionMode, confirmYolo: boolean) => {
    useAgents.getState().upsert(await Agents.setPermissionMode(session.id, mode, confirmYolo));
  };

  const dropAccepted = message.attachments.canAdd && !inactive;
  const drop = useFileDrop({ enabled: true, accepting: dropAccepted, onFiles: (files) => void message.attachments.add(files), afterDrop: () => input.current?.focus() });
  // A paste of files and no text attaches them; any text paste is left to the browser, exactly as before.
  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = pastedFiles(e.clipboardData);
    if (!files.length) return;
    e.preventDefault();
    void message.attachments.add(files);
  };
  const hasContent = text.trim().length > 0 || message.attachments.ids.length > 0;
  return <div className="bg-background px-5 pt-2 pb-3">
    <div className="mx-auto flex max-w-4xl flex-col gap-1">
      <MessageQueue session={session} />
      {!state.canSend ? <>
        <div className={cn(composerFrame, "px-3.5 py-3 text-muted-foreground text-sm")}><textarea aria-label="Message the agent" disabled rows={1} placeholder="Messages are unavailable" className="w-full resize-none bg-transparent outline-none placeholder:text-muted-foreground" /></div>
        <p role="status" className="text-xs text-muted-foreground">{state.reason}</p>
      </> : <form className="flex flex-col gap-1.5" onSubmit={(e) => { e.preventDefault(); send(); }} {...drop.handlers}>
        {state.note && <p className="px-2 text-xs text-muted-foreground">{state.note}</p>}
        <div className={cn(composerFrame, "relative flex flex-col gap-1.5 p-2")}>
          {message.attachments.files.length > 0 && <TaskAttachmentPreviews message={message} disabled={inactive} />}
          <textarea ref={input} aria-label="Message the agent" aria-description="Enter to send. Shift+Enter for a new line." placeholder="Reply to the agent…" value={text} disabled={inactive} rows={1}
            onChange={(e) => setText(e.target.value)} onKeyDown={onKeyDown} onPaste={onPaste}
            className="min-h-[44px] w-full min-w-0 resize-none bg-transparent px-1.5 pt-1 text-sm leading-[22px] outline-none placeholder:text-muted-foreground disabled:opacity-70" />
          <div className="flex h-7 min-w-0 items-center gap-1">
            <TaskAddMenu message={message} disabled={inactive} onInsert={insertText} />
            <TaskModelPicker message={message} disabled={inactive} />
            <PermissionModeSelect value={session.permissionMode} onChange={changeMode} support={message.harness?.permissionModes}
              disabled={inactive || !modeControl?.permissionMode}
              disabledReason={modeControl?.permissionMode ? "" : modeControl?.permissionModeReason || "This task cannot change permission mode."} />
            <div className="ms-auto flex min-w-0 shrink-0 items-center gap-1">
              <ComposerContext usage={session.usage} />
              {hasContent && !voiceActive
                ? <Tooltip>
                  <TooltipTrigger render={<Button type="submit" aria-label={state.label} className="size-7 shrink-0 rounded-full sm:size-7" size="icon-sm" loading={action.busy} disabled={inactive || !message.attachments.ready}><ArrowUpIcon /></Button>} />
                  <TooltipPopup>{state.label}</TooltipPopup>
                </Tooltip>
                : <ComposerVoice compact quiet disabled={action.busy} onActiveChange={setVoiceActive} onTranscript={insertText} />}
            </div>
          </div>
          {drop.dragging && <div aria-hidden className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center gap-2 rounded-[inherit] border-2 border-dashed border-ring bg-background/90 text-sm text-muted-foreground">
            <PaperclipIcon className="size-4 text-foreground" />{dropAccepted ? "Drop files to attach" : "No more files can be attached"}
          </div>}
        </div>
        {(session.dir || session.branch) && <div className="flex min-w-0 items-center gap-3 px-2 text-xs text-muted-foreground">
          {session.dir && <Tooltip><TooltipTrigger render={<span className="flex min-w-0 items-center gap-1" />}><FolderIcon aria-hidden className="size-3.5 shrink-0" /><span className="max-w-40 truncate">{where}</span></TooltipTrigger><TooltipPopup className="max-w-sm break-all">{session.dir}</TooltipPopup></Tooltip>}
          {session.branch && <Tooltip><TooltipTrigger render={<span className="flex min-w-0 items-center gap-1" />}><GitBranchIcon aria-hidden className="size-3.5 shrink-0" /><span className="max-w-40 truncate">{session.branch}</span></TooltipTrigger><TooltipPopup className="max-w-sm break-all">{session.branch}</TooltipPopup></Tooltip>}
        </div>}
        <ErrorNote error={action.error} />
      </form>}
    </div>
  </div>;
}
