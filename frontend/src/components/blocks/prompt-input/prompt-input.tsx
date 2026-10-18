/*
 * Adapted from the HextaUI Prompt Input block (agent harness layout), https://hextaui.com/blocks/prompt-input,
 * registry item https://hextaui.com/r/pro/prompt-input.json. Used under the HextaUI Pro License
 * (https://hextaui.com/legal/license). See docs/research/upstream-attribution.md for what changed.
 */
import { useImperativeHandle, useRef, useSyncExternalStore, type ComponentProps, type ReactNode } from "react";
import { ArrowUpIcon, PaperclipIcon, PlusIcon } from "lucide-react";
import { composerFrame } from "@/components/assistant-ui/elements/surfaces";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import { PromptInputContext, prefersReducedMotion, usePromptInput, type PromptInputContextValue } from "./context";
import { pastedFiles } from "@/lib/attachmentFiles";
import { useFileDrop } from "@/lib/useFileDrop";

function subscribeCoarse(callback: () => void) {
  if (typeof window.matchMedia !== "function") return () => {};
  const query = window.matchMedia("(pointer: coarse)");
  query.addEventListener("change", callback);
  return () => query.removeEventListener("change", callback);
}
const readCoarse = () => typeof window.matchMedia === "function" && window.matchMedia("(pointer: coarse)").matches;

function shake(element: HTMLElement | null) {
  if (!element || prefersReducedMotion()) return;
  element.removeAttribute("data-shake");
  void element.offsetWidth;
  element.setAttribute("data-shake", "");
  setTimeout(() => element.removeAttribute("data-shake"), 400);
}

type PromptInputProps = Omit<ComponentProps<"form">, "onSubmit"> & {
  value: string;
  onValueChange: (value: string) => void;
  onSubmit: () => void;
  /** False shakes the composer instead of submitting. */
  canSubmit: boolean;
  disabled?: boolean;
  /** Receives picked, pasted and dropped files. Omit to accept no files. */
  onFiles?: (files: File[]) => void;
  /** Whether more files can be added right now. */
  filesEnabled?: boolean;
  fileInputLabel?: string;
};

/**
 * The form and shared state for a controlled composer: the caller owns the text, files and submit rules
 * (the daemon decides what can launch), the block owns keyboard, drop and layout behaviour.
 */
export function PromptInput({ value, onValueChange, onSubmit, canSubmit, disabled = false, onFiles, filesEnabled = true, fileInputLabel = "Attach files", className, children, ...props }: PromptInputProps) {
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const acceptsFiles = Boolean(onFiles) && filesEnabled && !disabled;
  const addFiles = onFiles && ((files: File[]) => { if (files.length && acceptsFiles) onFiles(files); });
  // Pasted files go to the caller even when the composer is full or busy-but-enabled, so it can say why they were refused.
  const receiveFiles = onFiles && !disabled ? (files: File[]) => { if (files.length) onFiles(files); } : undefined;
  const { dragging, handlers: dropHandlers } = useFileDrop({ enabled: Boolean(onFiles), accepting: acceptsFiles, onFiles: (files) => onFiles?.(files), afterDrop: () => textareaRef.current?.focus() });

  const context: PromptInputContextValue = {
    value, setValue: onValueChange, textareaRef, disabled, canSubmit, dragging, addFiles: acceptsFiles ? addFiles : undefined, receiveFiles,
    openFileDialog: () => inputRef.current?.click(),
  };

  function submit() {
    if (!canSubmit) { shake(formRef.current?.querySelector<HTMLElement>("[data-slot=prompt-input-body]") ?? null); textareaRef.current?.focus(); return; }
    onSubmit();
  }

  return <PromptInputContext.Provider value={context}>
    <form ref={formRef} data-slot="prompt-input"
      className={cn("relative flex min-w-0 flex-col gap-2 [--composer-radius:1.25rem] [--composer-radius-outer:calc(var(--composer-radius)+--spacing(1.5))]", className)}
      onSubmit={(event) => { event.preventDefault(); submit(); }}
      {...dropHandlers}
      {...props}>
      {children}
      {onFiles && <input ref={inputRef} type="file" multiple hidden tabIndex={-1} aria-label={fileInputLabel} disabled={!acceptsFiles}
        onChange={(event) => { addFiles?.(Array.from(event.currentTarget.files ?? [])); event.currentTarget.value = ""; textareaRef.current?.focus(); }} />}
    </form>
  </PromptInputContext.Provider>;
}

/** A muted shell around the body that also holds the footer row, for agent harnesses. */
export function PromptInputInset({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="prompt-input-inset" className={cn("flex min-w-0 flex-col rounded-(--composer-radius-outer) bg-muted p-1.5 dark:bg-muted/60 forced-colors:border", className)} {...props} />;
}

/** The rounded surface: attachments, then the text, then tools on the left and actions on the right. */
export function PromptInputBody({ className, children, onMouseDown, ...props }: ComponentProps<"div">) {
  const context = usePromptInput("PromptInputBody");
  return <div data-slot="prompt-input-body" data-disabled={context.disabled ? "" : undefined} data-dragging={context.dragging ? "" : undefined}
    className={cn(composerFrame, "group/composer relative flex min-w-0 cursor-text flex-wrap items-center gap-1 rounded-(--composer-radius) p-2 data-shake:motion-safe:animate-toast-error-odd data-disabled:cursor-not-allowed forced-colors:border", className)}
    onMouseDown={(event) => {
      onMouseDown?.(event);
      const target = event.target as HTMLElement;
      if (event.defaultPrevented || target.closest("button, a, input, textarea, [role=button], [tabindex]")) return;
      event.preventDefault();
      context.textareaRef.current?.focus();
    }}
    {...props}>
    {children}
    {context.dragging && <div aria-hidden className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center gap-2 rounded-(--composer-radius) border-2 border-dashed border-ring bg-background/90 text-sm text-muted-foreground">
      <PaperclipIcon className="size-4 text-foreground" />{context.addFiles ? "Drop files to attach" : "No more files can be attached"}
    </div>}
  </div>;
}

export function PromptInputAttachments({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="prompt-input-attachments" className={cn("order-first flex min-w-0 basis-full flex-wrap gap-2 px-1 pt-1", className)} {...props} />;
}

/** The field. Enter sends, Shift+Enter adds a line, input methods are never interrupted; on touch screens Enter adds a line. */
export function PromptInputTextarea({ ref, className, onKeyDown, onPaste, ...props }: ComponentProps<"textarea">) {
  const context = usePromptInput("PromptInputTextarea");
  const coarse = useSyncExternalStore(subscribeCoarse, readCoarse, () => false);
  const { textareaRef } = context;
  // The caller's ref (the New task page focuses the field from its quick actions) sees the block's own textarea.
  useImperativeHandle(ref, () => textareaRef.current!, [textareaRef]);
  return <textarea ref={textareaRef} data-slot="prompt-input-textarea" value={context.value} disabled={context.disabled} dir="auto" rows={1}
    enterKeyHint={coarse ? "enter" : "send"} autoComplete="off" spellCheck
    className={cn("order-1 field-sizing-content max-h-64 min-h-11 min-w-0 basis-full resize-none bg-transparent px-1.5 pt-1 pb-1 text-[15px] leading-relaxed text-foreground outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed", className)}
    onChange={(event) => context.setValue(event.currentTarget.value)}
    onKeyDown={(event) => {
      onKeyDown?.(event);
      if (event.defaultPrevented) return;
      // keyCode 229: an input method is still composing (Safari reports isComposing late).
      if (event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) return;
      if (event.key === "Enter" && !event.shiftKey && !event.altKey && (event.metaKey || event.ctrlKey || !coarse)) {
        event.preventDefault();
        if (!event.repeat) event.currentTarget.form?.requestSubmit();
        return;
      }
      if (event.key === "Escape") event.currentTarget.blur();
    }}
    onPaste={(event) => {
      onPaste?.(event);
      if (event.defaultPrevented || !context.receiveFiles) return;
      // A text paste is left to the browser; only a paste that is files and no text is taken over.
      const files = pastedFiles(event.clipboardData);
      if (!files.length) return;
      event.preventDefault();
      context.receiveFiles(files);
    }}
    {...props} />;
}

export function PromptInputTools({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="prompt-input-tools" className={cn("order-2 flex min-w-0 flex-wrap items-center gap-0.5", className)} {...props} />;
}

export function PromptInputActions({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="prompt-input-actions" className={cn("order-3 ms-auto flex min-w-0 items-center gap-1", className)} {...props} />;
}

/** The + button: opens the file picker. */
export function PromptInputAddFiles({ label = "Add photos and files", hint }: { label?: string; hint?: ReactNode }) {
  const context = usePromptInput("PromptInputAddFiles");
  return <Tooltip>
    <TooltipTrigger render={<Button type="button" size="icon-sm" variant="ghost" className="rounded-full" aria-label={label} disabled={!context.addFiles} onClick={context.openFileDialog} />}>
      <PlusIcon />
    </TooltipTrigger>
    <TooltipPopup>{hint ?? label}</TooltipPopup>
  </Tooltip>;
}

export function PromptInputSubmit({ label = "Send", loading = false, className }: { label?: string; loading?: boolean; className?: string }) {
  const context = usePromptInput("PromptInputSubmit");
  return <Tooltip>
    <TooltipTrigger render={<Button type="submit" size="icon-sm" disabled={!context.canSubmit} loading={loading} aria-label={label} className={cn("shrink-0 rounded-full", className)} />}>
      <ArrowUpIcon />
    </TooltipTrigger>
    <TooltipPopup>{label} <Kbd>Enter</Kbd></TooltipPopup>
  </Tooltip>;
}

/** The row inside the inset shell for where the agent runs and what it works on. */
export function PromptInputFooter({ className, ...props }: ComponentProps<"div">) {
  return <div data-slot="prompt-input-footer" className={cn("flex min-h-9 min-w-0 flex-wrap items-center gap-0.5 px-1 pt-1.5 text-muted-foreground", className)} {...props} />;
}
