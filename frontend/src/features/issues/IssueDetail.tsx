import { useEffect, useMemo, useRef, useState } from "react";
import { Bot, CircleCheck, CircleDot, ExternalLink, MessageSquare } from "lucide-react";
import { GitHub, type Comment, type Issue, type IssueDetail as IssueDetailData } from "@/mygo";
import { openExternal } from "@/lib/openExternal";
import { AlertDialog, AlertDialogClose, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogPopup, AlertDialogTitle } from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Bubble, BubbleContent } from "@/components/ui/bubble";
import { Button } from "@/components/ui/button";
import { Kbd } from "@/components/ui/kbd";
import { Message, MessageAvatar, MessageContent, MessageGroup, MessageHeader } from "@/components/ui/message";
import { Tooltip, TooltipPopup, TooltipTrigger } from "@/components/ui/tooltip";
import { IssueAgentLauncher, IssueAgentRuns, useIssueAgent } from "@/features/github/IssueAgentWorkflow";
import { Markdown } from "@/features/github/Markdown";
import { ErrorBox, LabelChip, Loading, RefreshButton, agoIso, useAsync } from "@/features/github/shared";
import { errMsg, useGitHub } from "@/features/github/store";
import { MentionTextarea } from "./MentionTextarea";
import { PersonAvatar } from "./PersonAvatar";
import { isShortcutTarget } from "./shortcuts";
import type { IssueRef } from "./mentions";

const ROLES: Record<string, string> = { OWNER: "Owner", MEMBER: "Member", COLLABORATOR: "Collaborator" };

export function IssueDetail({ repo, number, related, onChanged }: { repo: string; number: number; /** Other loaded issues in this repository, offered after `#`. */ related: IssueRef[]; /** Called after the issue's state changed on GitHub so the list can refetch. */ onChanged: () => void }) {
  const detail = useAsync(() => GitHub.issue(repo, number), [repo, number]);
  const last = useRef<IssueDetailData | null>(null);
  useEffect(() => { if (detail.data) last.current = detail.data; }, [detail.data]);
  const data = detail.data ?? last.current;
  if (!data) return detail.error ? <ErrorBox error={detail.error} onRetry={detail.reload} /> : <Loading what="issue" />;
  return <IssueBody repo={repo} issue={data.issue} comments={data.comments} related={related} refreshing={detail.loading} error={detail.error} reload={detail.reload} onChanged={onChanged} />;
}

function IssueBody({ repo, issue, comments, related, refreshing, error, reload, onChanged }: { repo: string; issue: Issue; comments: Comment[]; related: IssueRef[]; refreshing: boolean; error: string | null; reload: () => void; onChanged: () => void }) {
  const status = useGitHub((state) => state.status);
  const agent = useIssueAgent(repo, issue);
  const open = issue.state === "open";
  const me = status?.user?.login ?? "";

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (!isShortcutTarget(event)) return;
      if (event.key === "s") { event.preventDefault(); agent.start(); }
      if (event.key === "g") { event.preventDefault(); void openExternal(issue.html_url); }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [agent, issue.html_url]);

  const people = useMemo(() => [...new Set([issue.user.login, ...issue.assignees.map((user) => user.login), ...comments.map((comment) => comment.user.login), ...(me ? [me] : [])])], [issue, comments, me]);
  const role = (login: string, association: string) => login === issue.user.login ? "Author" : ROLES[association];
  const startButton = <Button size="sm" disabled={!agent.canStart} onClick={agent.start}><Bot data-icon="inline-start" />{agent.saved.data ? "Review saved launch" : "Start an agent"}<Kbd>S</Kbd></Button>;

  return <article aria-label={`${repo}#${issue.number}`} className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-5 p-5">
    <header className="flex flex-col gap-3">
      <div className="flex items-start gap-2">
        <h2 className="min-w-0 flex-1 break-words font-semibold text-lg leading-snug">{issue.title}</h2>
        <div className="flex shrink-0 items-center gap-0.5">
          <Tooltip><TooltipTrigger render={<Button size="icon-xs" variant="ghost" aria-label="Open on GitHub" onClick={() => void openExternal(issue.html_url)} />}><ExternalLink /></TooltipTrigger><TooltipPopup><span className="flex items-center gap-2">Open on GitHub <Kbd>G</Kbd></span></TooltipPopup></Tooltip>
          <RefreshButton a={{ data: null, error, loading: refreshing, reload }} />
        </div>
      </div>
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground text-xs">
        <Badge variant={open ? "success" : "info"}>{open ? "Open" : issue.state_reason === "not_planned" ? "Closed (not planned)" : "Closed"}</Badge>
        <span className="font-mono">{repo}#{issue.number}</span>
        <span>opened {agoIso(issue.created_at)} by <strong className="font-medium text-foreground">{issue.user.login}</strong></span>
      </p>
      {issue.labels.length > 0 && <div className="flex flex-wrap gap-1">{issue.labels.map((label) => <LabelChip key={label.id} label={label} />)}</div>}
      <div className="flex flex-wrap items-center gap-2">
        {issue.pull_request ? <Tooltip><TooltipTrigger render={<span className="inline-flex" />}>{startButton}</TooltipTrigger><TooltipPopup>Pull requests cannot be assigned to an agent</TooltipPopup></Tooltip> : startButton}
        {!issue.pull_request && <StateButton repo={repo} issue={issue} onChanged={() => { reload(); onChanged(); }} />}
      </div>
    </header>
    {error && <ErrorBox error={error} onRetry={reload} />}
    <IssueAgentRuns agent={agent} quiet />
    <MessageGroup className="gap-4">
      <Entry login={issue.user.login} when={issue.created_at} role={role(issue.user.login, "")} body={issue.body} repo={repo} />
      {comments.map((comment) => <Entry key={comment.id} login={comment.user.login} when={comment.created_at} role={role(comment.user.login, comment.author_association)} you={comment.user.login === me} body={comment.body} repo={repo} />)}
      <Composer repo={repo} number={issue.number} me={me} source={status?.source ?? ""} people={people} related={related} onPosted={reload} />
    </MessageGroup>
    <IssueAgentLauncher agent={agent} repo={repo} issue={issue} />
  </article>;
}

function Entry({ login, when, role, you, body, repo }: { login: string; when: string; role?: string; you?: boolean; body: string; repo: string }) {
  return <Message>
    <MessageAvatar className="self-start"><PersonAvatar login={login} className="size-8 text-xs" /></MessageAvatar>
    <MessageContent>
      <div className="w-full min-w-0 overflow-hidden rounded-lg border bg-card">
        <MessageHeader className="justify-between gap-2 border-b px-3 py-1.5 text-xs group-has-data-[variant=ghost]/message:px-3">
          <span className="truncate"><strong className="font-medium text-foreground">{login}</strong>{you && <span className="text-muted-foreground"> (you)</span>} <span className="text-muted-foreground">· {agoIso(when)}</span></span>
          {role && <span className="shrink-0 text-[11px] text-muted-foreground">{role}</span>}
        </MessageHeader>
        <Bubble variant="ghost" className="w-full max-w-full p-3"><BubbleContent><Markdown source={body} repo={repo} /></BubbleContent></Bubble>
      </div>
    </MessageContent>
  </Message>;
}

function Composer({ repo, number, me, source, people, related, onPosted }: { repo: string; number: number; me: string; source: string; people: string[]; related: IssueRef[]; onPosted: () => void }) {
  const [text, setText] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const post = () => {
    setBusy(true); setError(null);
    GitHub.comment(repo, number, text, true).then(
      () => { setText(""); setConfirming(false); onPosted(); },
      (e: unknown) => setError(errMsg(e)),
    ).finally(() => setBusy(false));
  };
  return <Message>
    <MessageAvatar className="self-start"><PersonAvatar login={me || "me"} className="size-8 text-xs" /></MessageAvatar>
    <MessageContent>
      <MentionTextarea value={text} onChange={setText} onSubmit={() => { if (text.trim()) setConfirming(true); }} people={people} issues={related} aria-label="New comment" placeholder="Leave a comment (Markdown). @ mentions a person, # links an issue." />
      <div className="flex items-center justify-between gap-2 text-muted-foreground text-xs">
        <span>{me ? `Posts as @${me}${source === "gh" ? ", with gh" : source === "pat" ? ", with your token" : ""}.` : "Not connected to GitHub."}</span>
        <Button size="sm" variant="outline" disabled={!text.trim() || !me} onClick={() => setConfirming(true)}><MessageSquare data-icon="inline-start" />Comment</Button>
      </div>
    </MessageContent>
    <AlertDialog open={confirming} onOpenChange={(next) => { if (!next && !busy) { setConfirming(false); setError(null); } }}>
      <AlertDialogPopup>
        <AlertDialogHeader>
          <AlertDialogTitle>Post this comment?</AlertDialogTitle>
          <AlertDialogDescription render={<div />}>This acts on GitHub as you, on <span className="font-mono">{repo}#{number}</span>, and is visible to everyone with access to the repository.</AlertDialogDescription>
        </AlertDialogHeader>
        {error && <p role="alert" className="px-6 pb-4 text-destructive-foreground text-sm">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogClose render={<Button variant="outline" disabled={busy} />}>Cancel</AlertDialogClose>
          <Button disabled={busy} onClick={post}>{busy ? "Posting…" : "Post comment"}</Button>
        </AlertDialogFooter>
      </AlertDialogPopup>
    </AlertDialog>
  </Message>;
}

/** Close or reopen. Like every GitHub write it needs the confirmation dialog and sends `confirmed: true`. */
function StateButton({ repo, issue, onChanged }: { repo: string; issue: Issue; onChanged: () => void }) {
  const closing = issue.state === "open";
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const apply = () => {
    setBusy(true); setError(null);
    GitHub.updateIssue({ repo, number: issue.number, title: null, body: null, state: closing ? "closed" : "open", labels: [], setLabels: false, assignees: [], setAssignees: false, milestone: null, confirmed: true }).then(
      () => { setConfirming(false); onChanged(); },
      (e: unknown) => setError(errMsg(e)),
    ).finally(() => setBusy(false));
  };
  return <>
    <Button size="sm" variant="outline" onClick={() => setConfirming(true)}>{closing ? <CircleCheck data-icon="inline-start" /> : <CircleDot data-icon="inline-start" />}{closing ? "Close issue" : "Reopen issue"}</Button>
    <AlertDialog open={confirming} onOpenChange={(next) => { if (!next && !busy) { setConfirming(false); setError(null); } }}>
      <AlertDialogPopup>
        <AlertDialogHeader>
          <AlertDialogTitle>{closing ? "Close this issue?" : "Reopen this issue?"}</AlertDialogTitle>
          <AlertDialogDescription render={<div />}>This acts on GitHub as you, on <span className="font-mono">{repo}#{issue.number}</span>, and is visible to everyone with access to the repository.</AlertDialogDescription>
        </AlertDialogHeader>
        {error && <p role="alert" className="px-6 pb-4 text-destructive-foreground text-sm">{error}</p>}
        <AlertDialogFooter>
          <AlertDialogClose render={<Button variant="outline" disabled={busy} />}>Cancel</AlertDialogClose>
          <Button disabled={busy} onClick={apply}>{busy ? "Working…" : closing ? "Close issue" : "Reopen issue"}</Button>
        </AlertDialogFooter>
      </AlertDialogPopup>
    </AlertDialog>
  </>;
}
