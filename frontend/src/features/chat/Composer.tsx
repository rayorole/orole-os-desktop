import { ComposerPrimitive, useAui, useAuiState } from "@assistant-ui/react";
import { ArrowUpIcon, AtSignIcon, FlaskConicalIcon, PaperclipIcon, PlusIcon, ScrollTextIcon, SlashIcon, SquareIcon, XIcon } from "lucide-react";
import { memo, useEffect, useId, useRef, useState, type ComponentProps, type KeyboardEvent, type ReactNode } from "react";
import { ComposerAttachmentChip, ComposerAttachments } from "@/components/assistant-ui/elements/composer-attachments";
import { composerFrame } from "@/components/assistant-ui/elements/surfaces";
import { BotAvatar } from "@/components/shell/BotAvatar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Menu, MenuCheckboxItem, MenuItem, MenuPopup, MenuTrigger } from "@/components/ui/menu";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { ServerLogo } from "@/features/integrations/ServerLogo";
import { ComposerVoice } from "@/features/tasks/ComposerVoice";
import { cn } from "@/lib/utils";
import type { Bot } from "@/mygo";
import { useChatDrafts } from "@/stores/chatDrafts";
import { useChatMessages } from "@/stores/chatMessages";
import { ComposerMenu, matchItems, menuQuery, type MenuOption } from "./ComposerMenu";
import { chipOf, useCanSend } from "./composerState";
import { MentionHighlight } from "./MentionHighlight";
import { mentionDeletion, useMentionTargets, type MentionTarget } from "./mentions";
import { MAX_ATTACHMENTS } from "./runtime/attachments";

/** Opens the system file picker and hands each file to the composer, which stages it on the daemon. */
function pickFiles(add: (file: File) => Promise<void>) {
  const input = document.createElement("input");
  input.type = "file";
  input.multiple = true;
  input.onchange = () => { for (const file of Array.from(input.files ?? [])) void add(file).catch(() => {}); };
  input.click();
}

/** The title of each kind's group in the menu, in the order the groups are listed. */
const GROUPS: Record<MentionTarget["type"], string> = { bot: "Bots", integration: "Integrations", skill: "Skills" };

/** An icon button in the composer's footer, named by its tooltip. */
function FooterButton({ label, children, className, ...props }: { label: string; children: ReactNode } & ComponentProps<typeof Button>) {
  return <Tooltip>
    <TooltipTrigger render={<Button type="button" size="icon-sm" aria-label={label} className={cn("size-7 sm:size-7", className)} {...props} />}>{children}</TooltipTrigger>
    <TooltipPopup>{label}</TooltipPopup>
  </Tooltip>;
}

export const Composer = memo(function Composer({ bot, running }: { bot: Bot; running: boolean }) {
  const aui = useAui();
  const attachments = useAuiState((s) => s.composer.attachments);
  const text = useAuiState((s) => s.composer.text);
  const canSend = useCanSend();
  const [dictating, setDictating] = useState(false);
  const hasResearch = bot.config.tools.some((tool) => tool.id === "research");
  const research = useChatDrafts((s) => s.drafts[bot.id]?.research ?? false) && hasResearch;
  const failed = attachments.some((attachment) => attachment.status.type === "incomplete");
  const hasContent = text.trim() !== "" || attachments.length > 0;
  const targets = useMentionTargets(bot);
  const mirror = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const menuId = useId();

  // The menu follows the caret: an @ or / it is typing after opens it, Escape closes it for that trigger.
  const [caret, setCaret] = useState(0);
  const [focused, setFocused] = useState(false);
  const [dismissed, setDismissed] = useState<number | null>(null);
  const [highlight, setHighlight] = useState({ key: "", index: 0 });
  // Research takes no mentions (the daemon refuses them), so the menu stays shut.
  const query = focused && !research ? menuQuery(text, Math.min(caret, text.length)) : null;
  const matches = query ? (query.trigger === "/" ? ["skill"] as const : ["bot", "integration"] as const).flatMap((kind) => matchItems(targets.filter((t) => t.type === kind), query.query)) : [];
  const options: MenuOption[] = matches.map((target) => ({
    key: `${target.type}:${target.id}`, group: GROUPS[target.type], label: target.label, detail: target.detail,
    icon: target.type === "bot" ? <BotAvatar name={target.label} avatar={target.avatar} className="size-[18px] text-[9px]" />
      : target.server ? <ServerLogo server={target.server} className="size-4" /> : <ScrollTextIcon />,
  }));
  // A query with a space that names nothing is ordinary text ("@ the team"), not a lookup.
  const open = query !== null && dismissed !== query.start && (options.length > 0 || !/\s/.test(query.query));
  const menuKey = query ? `${query.start}:${query.query}` : "";
  const active = highlight.key === menuKey ? Math.min(highlight.index, Math.max(0, options.length - 1)) : 0;
  const setActive = (index: number) => setHighlight({ key: menuKey, index });

  // The composer lives and dies with the bot's conversation; its text is kept per bot in the store. Both directions
  // are subscribed: text put in the draft from elsewhere (Add to chat on a selected passage) shows up in the box, and
  // every change to the box (typing, a picked mention, dictation, input history) lands in the draft, so leaving the
  // view and coming back keeps it. Subscribed outside render: every keystroke changes the draft.
  useEffect(() => {
    const sync = (text: string) => { if (text !== aui.composer.getState().text) aui.composer.setText(text); };
    sync(useChatDrafts.getState().drafts[bot.id]?.text ?? "");
    const offDraft = useChatDrafts.subscribe((state, previous) => {
      const text = state.drafts[bot.id]?.text ?? "";
      if (text !== (previous.drafts[bot.id]?.text ?? "")) sync(text);
    });
    const offComposer = aui.subscribe(() => {
      const text = aui.composer.getState().text;
      if (text !== (useChatDrafts.getState().drafts[bot.id]?.text ?? "")) useChatDrafts.getState().setDraft(bot.id, { text });
    });
    return () => { offDraft(); offComposer(); };
  }, [aui, bot.id]);

  const send = () => { if (canSend) aui.composer.send(); };
  const setResearch = (on: boolean) => useChatDrafts.getState().setDraft(bot.id, { research: on });
  const addDictation = (value: string) => {
    const current = aui.composer.getState().text;
    aui.composer.setText(current ? `${current}${/\s$/.test(current) ? "" : " "}${value}` : value);
  };
  /** Puts `next` in the box with the caret at `at`, once the box shows it. */
  const replace = (next: string, at: number) => {
    aui.composer.setText(next);
    setDismissed(null);
    requestAnimationFrame(() => {
      input.current?.focus();
      input.current?.setSelectionRange(at, at);
      setCaret(at);
    });
  };
  /** Swaps the typed `@que` for the picked name and a space, so typing carries on after it. */
  const pick = (index: number) => {
    const option = options[index];
    if (!query || !option) return;
    const mention = `${query.trigger}${option.label} `;
    replace(text.slice(0, query.start) + mention + text.slice(caret), query.start + mention.length);
  };
  /** The @ and / buttons type their trigger at the caret, opening the menu as if typed. */
  const typeTrigger = (trigger: "@" | "/") => {
    const el = input.current;
    const from = el?.selectionStart ?? text.length;
    const before = text.slice(0, from);
    const lead = before && !/\s$/.test(before) ? " " : "";
    replace(`${before}${lead}${trigger}${text.slice(el?.selectionEnd ?? from)}`, from + lead.length + 1);
  };
  /** Backspace/Delete next to a mention removes all of it, as one unit; returns whether it did. */
  const removeMention = (event: KeyboardEvent<HTMLTextAreaElement>): boolean => {
    const el = event.currentTarget;
    if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey || event.nativeEvent.isComposing || el.selectionStart !== el.selectionEnd) return false;
    const cut = mentionDeletion(el.value, targets, el.selectionStart, event.key === "Delete");
    if (!cut) return false;
    event.preventDefault();
    replace(el.value.slice(0, cut.from) + el.value.slice(cut.to), cut.from);
    return true;
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.nativeEvent.isComposing) return;
    if (open) {
      if ((event.key === "ArrowDown" || event.key === "ArrowUp") && options.length > 0) {
        event.preventDefault();
        setActive((active + (event.key === "ArrowDown" ? 1 : options.length - 1)) % options.length);
        return;
      }
      if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey && options.length > 0) {
        event.preventDefault();
        pick(active);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setDismissed(query.start);
        return;
      }
    }
    if ((event.key === "Backspace" || event.key === "Delete") && removeMention(event)) return;
    if (event.key !== "Enter" || event.shiftKey) return;
    event.preventDefault();
    send();
  };

  return <ComposerPrimitive.Root data-slot="composer" aria-label="Message composer" className="relative mx-auto flex w-full max-w-3xl shrink-0 flex-col gap-2 px-4 pb-4">
    <div className="relative">
      {open && <ComposerMenu id={menuId} label={query.trigger === "/" ? "Skills" : "Bots and integrations"} options={options} active={active}
        trigger={query.trigger === "/" ? "/" : ""} columns={query.trigger === "/"} onActive={setActive} onPick={pick}
        empty={query.trigger === "/" ? "No skill matches" : "No bot or integration matches"} />}
      <ComposerPrimitive.AttachmentDropzone disabled={research}
        className={cn(composerFrame, "flex flex-col gap-1.5 p-2 data-[dragging=true]:shadow-[0_0_0_2px_var(--ring)]")}>
        {attachments.length > 0 && <ComposerAttachments aria-label="Attachments" className="px-1 pt-0.5">
          {attachments.map((attachment, index) => <ComposerAttachmentChip key={attachment.id} attachment={chipOf(attachment)} onRemove={() => void aui.composer.attachment({ index }).remove()} />)}
        </ComposerAttachments>}
        <div className="relative min-w-0">
          <MentionHighlight text={text} targets={targets} mirrorRef={mirror} />
          <ComposerPrimitive.Input
            ref={input}
            aria-label={`Message ${bot.name}`}
            placeholder={research ? "What should I research?" : running ? `Queue a message for ${bot.name}` : `Message ${bot.name}, @ to mention, / for skills`}
            submitMode="none"
            rows={1}
            maxRows={8}
            role="combobox"
            aria-autocomplete="list"
            aria-expanded={open}
            aria-controls={open ? menuId : undefined}
            aria-activedescendant={open && options.length > 0 ? `${menuId}-option-${active}` : undefined}
            className="relative block max-h-48 min-h-[44px] w-full resize-none overflow-y-auto bg-transparent px-1.5 pt-1 pb-0.5 text-sm leading-[22px] text-transparent caret-foreground outline-none selection:bg-primary/25 selection:text-transparent placeholder:text-muted-foreground"
            onScroll={(event) => { if (mirror.current) mirror.current.scrollTop = event.currentTarget.scrollTop; }}
            onSelect={(event) => setCaret(event.currentTarget.selectionStart)}
            onFocus={() => setFocused(true)}
            onBlur={() => setFocused(false)}
            onKeyDown={onKeyDown}
          />
        </div>
        <div className="flex h-7 min-w-0 items-center gap-1">
          <Menu>
            <Tooltip>
              <TooltipTrigger render={<MenuTrigger render={<Button type="button" variant="ghost" size="icon-sm" className="size-7 text-muted-foreground sm:size-7" aria-label="Add files and research" />} />}>
                <PlusIcon aria-hidden />
              </TooltipTrigger>
              <TooltipPopup>Add files and research</TooltipPopup>
            </Tooltip>
            <MenuPopup align="start" side="top" className="min-w-56">
              <MenuItem disabled={research || attachments.length >= MAX_ATTACHMENTS} onClick={() => pickFiles((file) => aui.composer.addAttachment(file))}><PaperclipIcon />Attach files</MenuItem>
              {hasResearch && <MenuCheckboxItem checked={research} disabled={attachments.length > 0} onCheckedChange={setResearch}>Research mode</MenuCheckboxItem>}
            </MenuPopup>
          </Menu>
          <FooterButton label="Mention a bot or integration" variant="ghost" className="text-muted-foreground" disabled={research} onClick={() => typeTrigger("@")}><AtSignIcon aria-hidden /></FooterButton>
          <FooterButton label="Use a skill" variant="ghost" className="text-muted-foreground" disabled={research} onClick={() => typeTrigger("/")}><SlashIcon aria-hidden /></FooterButton>
          {research && <Badge variant="info" size="lg" render={<button type="button" aria-label="Research mode on. Turn off" onClick={() => setResearch(false)} />}>
            <FlaskConicalIcon aria-hidden />Research<XIcon aria-hidden />
          </Badge>}
          <div className="ms-auto flex min-w-0 items-center gap-1">
            {running && <FooterButton label="Stop" variant={hasContent ? "ghost" : "secondary"} className="rounded-full" onClick={() => void useChatMessages.getState().cancel(bot.id)}><SquareIcon aria-hidden className="fill-current" /></FooterButton>}
            {/* Dictation stays offered next to Send whatever the draft holds (a transcript is appended to it). It is hidden while a reply runs, except for a recording that is already going: unmounting that would lose it. */}
            {(!running || dictating) && <ComposerVoice compact quiet disabled={false} onActiveChange={setDictating} onTranscript={addDictation} />}
            {hasContent && !dictating && <FooterButton label={running ? "Queue message" : "Send message"} className="rounded-full" disabled={!canSend} onClick={send}><ArrowUpIcon aria-hidden /></FooterButton>}
          </div>
        </div>
      </ComposerPrimitive.AttachmentDropzone>
    </div>
    {failed && <p role="status" className="px-1 text-xs text-muted-foreground">Remove files that could not be attached to send your message.</p>}
  </ComposerPrimitive.Root>;
});
