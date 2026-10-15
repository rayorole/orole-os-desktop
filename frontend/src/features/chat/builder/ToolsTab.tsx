import { useId, type Dispatch, type ReactNode } from "react";
import { Globe, MonitorIcon, ScrollText, SquareTerminalIcon } from "lucide-react";
import { Chat, MCP, Skills, type ChatToolInfo, type ServerState, type Skill } from "@/mygo";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { ServerLogo } from "@/features/integrations/ServerLogo";
import { useUI } from "@/stores/ui";
import { SwitchRow } from "../panel/rows";
import { splitModel, type Action, type FormState, type Problems } from "./botForm";
import { Section } from "./controls";
import { useLoad, type Loaded } from "./useLoad";

/** Why an integration cannot be used right now, if it cannot. */
function serverNotice(server: ServerState): string | undefined {
  if (!server.enabled || server.status === "disabled") return "Off";
  if (server.status === "auth_required") return "Needs sign-in";
  if (server.status === "backoff") return "Retrying";
  return undefined;
}

function BuiltinTool({ info, form, dispatch }: { info: ChatToolInfo; form: FormState; dispatch: Dispatch<Action> }) {
  const heading = useId();
  const enabled = form.tools.includes(info.id);
  return (
    <li className="flex min-w-0 flex-col gap-2 py-2">
      <div className="flex items-center justify-between gap-3">
        <span className="flex min-w-0 items-center gap-1.5">
          <span id={heading} className="truncate text-sm">{info.name}</span>
          {info.egress && <Globe className="size-3 shrink-0 text-muted-foreground" aria-label={`Sends data off this computer: ${info.egress}`} />}
        </span>
        <Switch
          checked={enabled} disabled={!info.available && !enabled} aria-labelledby={heading}
          onCheckedChange={(on) => dispatch({ type: "tool", id: info.id, enabled: on })}
        />
      </div>
      {!info.available && <p role="status" className="text-muted-foreground text-xs">{info.reason || "The selected model cannot run this tool."}</p>}
    </li>
  );
}

/** One switch row: a lead (icon and name), quiet trailing text or badges, and the switch labelled by the name. */
function ToggleRow({ lead, name, badge, quiet, checked, disabled, onChange }: {
  lead: ReactNode; name: string; badge?: string; quiet?: string; checked: boolean; disabled?: boolean; onChange: (on: boolean) => void;
}) {
  const heading = useId();
  return (
    <li className="flex items-center justify-between gap-3 py-2">
      <span className="flex min-w-0 items-center gap-2">
        {lead}
        <span id={heading} className="truncate text-sm">{name}</span>
        {badge && <Badge size="sm" variant="warning">{badge}</Badge>}
      </span>
      <span className="flex shrink-0 items-center gap-2">
        {quiet && <span className="text-muted-foreground text-xs">{quiet}</span>}
        <Switch checked={checked} disabled={disabled} aria-labelledby={heading} onCheckedChange={onChange} />
      </span>
    </li>
  );
}

/** A saved id that no longer exists, with a way to drop it. */
function RemovedRow({ label, name, onRemove }: { label: string; name?: string; onRemove: () => void }) {
  const heading = useId();
  return (
    <li className="flex items-center justify-between gap-3 py-2">
      <span id={heading} className="min-w-0 truncate text-muted-foreground text-sm">{name && <span className="text-foreground">{name} </span>}{label}</span>
      <Button size="xs" variant="outline" aria-describedby={heading} onClick={onRemove}>Remove</Button>
    </li>
  );
}

function Integrations({ form, dispatch, servers, error, bare }: { form: FormState; dispatch: Dispatch<Action>; servers: Loaded<ServerState[]>; error?: string; bare?: boolean }) {
  const list = servers.data ?? [];
  const known = new Set(list.map((s) => s.id));
  const removed = servers.data ? form.mcpServers.filter((id) => !known.has(id)) : [];
  const set = (mcpServers: string[]) => dispatch({ type: "patch", patch: { mcpServers } });
  return (
    <Group title={bare ? undefined : "Integrations / MCP"}>
      {servers.error && <p role="alert" className="selectable break-words text-destructive-foreground text-xs">{servers.error}</p>}
      {servers.loading && !servers.data && !servers.error && <p role="status" className="text-muted-foreground text-xs">Loading…</p>}
      {servers.data && list.length === 0 && removed.length === 0 && (
        <div className="flex items-center justify-between gap-2">
          <p className="text-muted-foreground text-xs">No integrations connected yet.</p>
          <Button size="xs" variant="outline" onClick={() => useUI.getState().openSettings("integrations")}>Open Integrations</Button>
        </div>
      )}
      {(list.length > 0 || removed.length > 0) && (
        <ul aria-label="Integrations" className="flex flex-col divide-y">
          {list.map((server) => (
            <ToggleRow
              key={server.id} lead={<ServerLogo server={server} className="size-4 shrink-0" />} name={server.name} badge={serverNotice(server)}
              checked={form.mcpServers.includes(server.id)}
              onChange={(on) => set(on ? [...form.mcpServers, server.id] : form.mcpServers.filter((id) => id !== server.id))}
            />
          ))}
          {removed.map((id) => <RemovedRow key={id} label="Removed integration" onRemove={() => set(form.mcpServers.filter((other) => other !== id))} />)}
        </ul>
      )}
      {error && <p role="alert" className="text-destructive-foreground text-xs">{error}</p>}
    </Group>
  );
}

function SkillList({ form, dispatch, skills, error, bare }: { form: FormState; dispatch: Dispatch<Action>; skills: Loaded<Skill[]>; error?: string; bare?: boolean }) {
  const list = skills.data ?? [];
  const known = new Set(list.map((s) => s.name));
  const removed = skills.data ? form.skills.filter((name) => !known.has(name)) : [];
  const set = (next: string[]) => dispatch({ type: "patch", patch: { skills: next } });
  return (
    <Group title={bare ? undefined : "Skills"}>
      {skills.error && <p role="alert" className="selectable break-words text-destructive-foreground text-xs">{skills.error}</p>}
      {skills.loading && !skills.data && !skills.error && <p role="status" className="text-muted-foreground text-xs">Loading…</p>}
      {skills.data && list.length === 0 && removed.length === 0 && <p className="text-muted-foreground text-xs">No skills yet.</p>}
      {(list.length > 0 || removed.length > 0) && (
        <ul aria-label="Skills" className="flex flex-col divide-y">
          {list.map((skill) => {
            const lead = <ScrollText aria-hidden className="size-4 shrink-0 text-muted-foreground" />;
            // Built-in skills reach every bot on their own; they are never stored on the bot.
            if (skill.source === "builtin") return <ToggleRow key={skill.name} lead={lead} name={skill.name} quiet="Built in" checked disabled onChange={() => {}} />;
            return (
              <ToggleRow
                key={skill.name} lead={lead} name={skill.name} badge={skill.enabled ? undefined : "Off"}
                checked={form.skills.includes(skill.name)}
                onChange={(on) => set(on ? [...form.skills, skill.name] : form.skills.filter((name) => name !== skill.name))}
              />
            );
          })}
          {removed.map((name) => <RemovedRow key={name} name={name} label="Removed skill" onRemove={() => set(form.skills.filter((other) => other !== name))} />)}
        </ul>
      )}
      {error && <p role="alert" className="text-destructive-foreground text-xs">{error}</p>}
    </Group>
  );
}

export function ToolsTab({ form, dispatch, problems }: { form: FormState; dispatch: Dispatch<Action>; problems: Problems }) {
  const request = splitModel(form.model);
  const tools = useLoad(() => Chat.tools(request), form.model);
  const servers = useLoad(async () => (await MCP.list()) ?? [], "mcp");
  const skills = useLoad(async () => (await Skills.list()) ?? [], "skills");

  const offered = new Set((tools.data ?? []).map((t) => t.id));
  const unknown = tools.data ? form.tools.filter((id) => !offered.has(id)) : [];

  return (
    <div className="flex flex-col gap-4">
      {tools.error && (
        <Alert variant="error"><AlertDescription className="flex items-center justify-between gap-2"><span className="selectable break-words">{tools.error}</span><Button size="xs" variant="outline" onClick={tools.reload}>Retry</Button></AlertDescription></Alert>
      )}
      {!tools.data && !tools.error && <p role="status" className="text-muted-foreground text-xs">Loading…</p>}
      {tools.data && tools.data.length > 0 && <ul aria-label="Built-in tools" className="flex flex-col divide-y">{tools.data.map((info) => <BuiltinTool key={info.id} info={info} form={form} dispatch={dispatch} />)}</ul>}
      {unknown.map((id) => (
        <div key={id} className="flex items-center justify-between gap-2 text-xs">
          <span className="min-w-0 break-words text-muted-foreground">{id} is not offered here.</span>
          <Button size="xs" variant="outline" onClick={() => dispatch({ type: "tool", id, enabled: false })}>Remove {id}</Button>
        </div>
      ))}

      <Integrations form={form} dispatch={dispatch} servers={servers} error={problems.mcpServers} />
      <SkillList form={form} dispatch={dispatch} skills={skills} error={problems.skills} />
    </div>
  );
}

/** The built-in tools alone, for a settings page of their own. */
export function ToolsPage({ form, dispatch }: { form: FormState; dispatch: Dispatch<Action> }) {
  const tools = useLoad(() => Chat.tools(splitModel(form.model)), form.model);
  const offered = new Set((tools.data ?? []).map((t) => t.id));
  const unknown = tools.data ? form.tools.filter((id) => !offered.has(id)) : [];
  return (
    <div className="flex flex-col gap-3">
      <div className="overflow-hidden rounded-xl border bg-card/60">
        <SwitchRow icon={SquareTerminalIcon} label="Runs code tasks" hint="Starts coding agents in your projects, follows their progress and tells you when they finish or need you. Your settings decide whether a start waits for your approval, and which agent, model and permission mode it uses." checked={form.codeTasks} onChange={(codeTasks) => dispatch({ type: "patch", patch: { codeTasks } })} />
        <SwitchRow icon={MonitorIcon} label="Has its own computer" hint="A private Linux computer on this PC with a terminal and a browser you can watch and take over. It starts when the bot first uses it and can reach the internet and your home network." checked={form.computer} onChange={(computer) => dispatch({ type: "patch", patch: { computer } })} />
      </div>
      {tools.error && (
        <Alert variant="error"><AlertDescription className="flex items-center justify-between gap-2"><span className="selectable break-words">{tools.error}</span><Button size="xs" variant="outline" onClick={tools.reload}>Retry</Button></AlertDescription></Alert>
      )}
      {!tools.data && !tools.error && <p role="status" className="text-muted-foreground text-xs">Loading…</p>}
      {tools.data && tools.data.length > 0 && <ul aria-label="Built-in tools" className="flex flex-col divide-y">{tools.data.map((info) => <BuiltinTool key={info.id} info={info} form={form} dispatch={dispatch} />)}</ul>}
      {unknown.map((id) => (
        <div key={id} className="flex items-center justify-between gap-2 text-xs">
          <span className="min-w-0 break-words text-muted-foreground">{id} is not offered here.</span>
          <Button size="xs" variant="outline" onClick={() => dispatch({ type: "tool", id, enabled: false })}>Remove {id}</Button>
        </div>
      ))}
    </div>
  );
}

export function IntegrationsPage({ form, dispatch, problems }: { form: FormState; dispatch: Dispatch<Action>; problems: Problems }) {
  const servers = useLoad(async () => (await MCP.list()) ?? [], "mcp");
  return <Integrations form={form} dispatch={dispatch} servers={servers} error={problems.mcpServers} bare />;
}

export function SkillsPage({ form, dispatch, problems }: { form: FormState; dispatch: Dispatch<Action>; problems: Problems }) {
  const skills = useLoad(async () => (await Skills.list()) ?? [], "skills");
  return <SkillList form={form} dispatch={dispatch} skills={skills} error={problems.skills} bare />;
}

/** A heading and its content, or only the content on a page that has its own heading. */
function Group({ title, children }: { title?: string; children: ReactNode }) {
  return title ? <Section title={title}>{children}</Section> : <div className="flex flex-col gap-2">{children}</div>;
}
