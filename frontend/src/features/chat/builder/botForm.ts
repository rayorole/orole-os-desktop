import type { Bot, BotConfig, BotExport, BotInput, BotJarvis, BotTaskDefaults, BotTool, ScopeAccess } from "@/mygo";

/** Limits mirrored from the daemon (internal/chat/api.go, store.go). The daemon validates again. */
export const LIMITS = {
  name: 80,
  jarvisPolicy: 2000,
  description: 500,
  instructions: 32 * 1024,
  steps: { min: 1, max: 30, default: 8 },
  toolCalls: { min: 1, max: 60, default: 20 },
  mcpServers: 50,
  skills: 50,
} as const;

export const TOOL_IDS = { webSearch: "web_search", webFetch: "web_fetch", datetime: "datetime", research: "research" } as const;

export interface FormState {
  name: string;
  avatar: string;
  color: string;
  description: string;
  instructions: string;
  /** "<provider>/<model>"; empty = use the defaults. */
  model: string;
  reasoning: string;
  /** Ids of the enabled built-in tools. */
  tools: string[];
  mcpServers: string[];
  skills: string[];
  /** Grants beyond the bot's own memory, which every bot can always read and write. Kept as saved; not edited here. */
  memory: ScopeAccess[];
  maxSteps: string;
  maxToolCalls: string;
  /** An executive assistant may ask every other bot. */
  executive: boolean;
  /** May start, watch and steer code tasks (coding agents on this PC). */
  codeTasks: boolean;
  /** Has its own local Linux computer (terminal and browser) the user can watch and take over. */
  computer: boolean;
  /** Overrides the global "Bot tools" level (ToolAccess); "" uses the global setting. */
  toolAccess: string;
  /** Code-task overrides: "" uses the global setting, "auto" lets the bot choose (not for the permission mode). */
  taskHarness: string;
  taskModel: string;
  taskReasoning: string;
  taskPermissionMode: string;
  /** Jarvis mode: the bot may phone the owner. Only valid with full permissions; the daemon checks on save. */
  jarvisEnabled: boolean;
  /** "When to call me", free text read by the bot. */
  jarvisPolicy: string;
  /** ElevenLabs voice id; "" uses the global default. */
  jarvisVoice: string;
  /** ElevenLabs TTS model id; "" uses the global default. */
  jarvisModel: string;
}

/** A new bot starts with every built-in tool on; each can be switched off in its settings. */
export const initialForm = (): FormState => ({
  name: "", avatar: "", color: "", description: "", instructions: "", model: "", reasoning: "",
  tools: Object.values(TOOL_IDS), mcpServers: [], skills: [], memory: [], maxSteps: "", maxToolCalls: "", executive: false, codeTasks: false, computer: false,
  toolAccess: "", taskHarness: "", taskModel: "", taskReasoning: "", taskPermissionMode: "",
  jarvisEnabled: false, jarvisPolicy: "", jarvisVoice: "", jarvisModel: "",
});

export type Action =
  | { type: "patch"; patch: Partial<FormState> }
  | { type: "load"; state: FormState }
  | { type: "tool"; id: string; enabled: boolean };

export function formReducer(state: FormState, action: Action): FormState {
  switch (action.type) {
    case "patch": return { ...state, ...action.patch };
    case "load": return action.state;
    case "tool": return { ...state, tools: action.enabled ? [...state.tools.filter((id) => id !== action.id), action.id] : state.tools.filter((id) => id !== action.id) };
  }
}

/* ------------------------------------------------------------------ provider/model ids */

/** Splits "provider/model"; the model id may itself contain slashes. */
export function splitModel(value: string): { provider: string; model: string } {
  const i = value.indexOf("/");
  return i < 0 ? { provider: "", model: "" } : { provider: value.slice(0, i), model: value.slice(i + 1) };
}

/* ------------------------------------------------------------------ conversion */

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === "string") : [];
}

/** The editable form of a bot (or an import) with missing parts defaulted. */
export function formFromInput(bot: Pick<BotInput, "name" | "avatar" | "color" | "description" | "instructions" | "provider" | "model" | "reasoning"> & { config?: Partial<BotConfig> }): FormState {
  const config = bot.config ?? {};
  const limit = (n: number | undefined) => (n && n > 0 ? String(n) : "");
  return {
    name: bot.name, avatar: bot.avatar, color: bot.color, description: bot.description, instructions: bot.instructions,
    model: bot.provider && bot.model ? `${bot.provider}/${bot.model}` : "",
    reasoning: bot.reasoning,
    // knowledge_search is retired as a setting: every bot always has it, so a saved entry is dropped.
    tools: [...new Set((config.tools ?? []).map((t) => t.id).filter((id) => id !== "knowledge_search"))],
    mcpServers: [...(config.mcpServers ?? [])], skills: [...(config.skills ?? [])],
    memory: (config.memory ?? []).map((a) => ({ ...a })),
    maxSteps: limit(config.limits?.maxSteps), maxToolCalls: limit(config.limits?.maxToolCalls),
    executive: config.executive ?? false,
    codeTasks: config.codeTasks ?? false,
    computer: config.computer ?? false,
    toolAccess: config.toolAccess ?? "",
    taskHarness: config.tasks?.harness ?? "", taskModel: config.tasks?.model ?? "", taskReasoning: config.tasks?.reasoning ?? "", taskPermissionMode: config.tasks?.permissionMode ?? "",
    jarvisEnabled: config.jarvis?.enabled ?? false, jarvisPolicy: config.jarvis?.policy ?? "", jarvisVoice: config.jarvis?.voiceId ?? "", jarvisModel: config.jarvis?.ttsModel ?? "",
  };
}

export const formFromBot = (bot: Bot): FormState => formFromInput(bot);

const integer = (text: string): number => (/^\d+$/.test(text.trim()) ? Number(text.trim()) : 0);

/** The payload for Chat.saveBot / Chat.draftSend. Pass an empty id to create. */
export function toInput(form: FormState, id: string): BotInput {
  const { provider, model } = splitModel(form.model);
  // Empty fields inherit the global defaults, so only the overrides are sent.
  const overrides = Object.entries({ harness: form.taskHarness, model: form.taskModel, reasoning: form.taskReasoning, permissionMode: form.taskPermissionMode }).filter(([, value]) => value);
  const tasks: BotTaskDefaults | undefined = overrides.length > 0 ? Object.fromEntries(overrides) : undefined;
  const jarvis: BotJarvis = {
    ...(form.jarvisEnabled ? { enabled: true } : {}),
    ...(form.jarvisPolicy ? { policy: form.jarvisPolicy } : {}),
    ...(form.jarvisVoice ? { voiceId: form.jarvisVoice } : {}),
    ...(form.jarvisModel ? { ttsModel: form.jarvisModel } : {}),
  };
  return {
    id,
    name: form.name.trim(),
    avatar: form.avatar.trim(),
    color: form.color,
    description: form.description.trim(),
    instructions: form.instructions,
    provider, model,
    reasoning: form.reasoning,
    config: {
      tools: form.tools.map((toolId) => ({ id: toolId })),
      mcpServers: form.mcpServers,
      skills: form.skills,
      memory: form.memory,
      limits: { maxSteps: integer(form.maxSteps), maxToolCalls: integer(form.maxToolCalls) },
      ...(form.executive ? { executive: true } : {}),
      ...(form.codeTasks ? { codeTasks: true } : {}),
      ...(form.computer ? { computer: true } : {}),
      ...(form.toolAccess ? { toolAccess: form.toolAccess } : {}),
      ...(tasks ? { tasks } : {}),
      ...(Object.keys(jarvis).length > 0 ? { jarvis } : {}),
    },
  };
}

/** A stable text of the form for unsaved-change detection (independent of toggle order). */
export function fingerprint(form: FormState): string {
  return JSON.stringify({
    ...form,
    tools: form.tools.toSorted(),
    memory: form.memory.toSorted((a, b) => a.scope.localeCompare(b.scope)),
    mcpServers: form.mcpServers.toSorted(), skills: form.skills.toSorted(),
  });
}

/* ------------------------------------------------------------------ validation */

/** Problems by field key. */
export type Problems = Partial<Record<string, string>>;

const charCount = (text: string): number => Array.from(text).length;

function limit(text: string, label: string, range: { min: number; max: number }): string | undefined {
  const t = text.trim();
  if (!t) return undefined;
  if (!/^\d+$/.test(t) || Number(t) < range.min || Number(t) > range.max) return `${label} must be a whole number from ${range.min} to ${range.max}, or empty for the default.`;
  return undefined;
}

export function validate(form: FormState): Problems {
  const problems: Problems = {};
  const name = form.name.trim();
  if (!name) problems.name = "Give the bot a name.";
  else if (charCount(name) > LIMITS.name) problems.name = `Names are limited to ${LIMITS.name} characters.`;
  // eslint-disable-next-line no-control-regex
  else if (/[\u0000-\u001f\u007f]/.test(name)) problems.name = "The name contains invalid characters.";
  if (charCount(form.description.trim()) > LIMITS.description) problems.description = `The description is limited to ${LIMITS.description} characters.`;
  if (charCount(form.instructions) > LIMITS.instructions) problems.instructions = `Instructions are limited to ${LIMITS.instructions.toLocaleString("en-US")} characters.`;
  if (charCount(form.jarvisPolicy) > LIMITS.jarvisPolicy) problems.jarvisPolicy = `Keep "When to call me" under ${LIMITS.jarvisPolicy} characters.`;
  const steps = limit(form.maxSteps, "Max steps", LIMITS.steps);
  if (steps) problems.maxSteps = steps;
  const calls = limit(form.maxToolCalls, "Max tool calls", LIMITS.toolCalls);
  if (calls) problems.maxToolCalls = calls;
  if (form.mcpServers.length > LIMITS.mcpServers) problems.mcpServers = `At most ${LIMITS.mcpServers} integrations can be enabled.`;
  if (form.skills.length > LIMITS.skills) problems.skills = `At most ${LIMITS.skills} skills can be enabled.`;
  return problems;
}

/* ------------------------------------------------------------------ import */

export type ParsedExport = { ok: true; value: BotExport } | { ok: false; error: string };

/** Parses a pasted or selected bot export. Only version 1 is understood; the daemon validates the content again. */
export function parseBotExport(text: string): ParsedExport {
  let raw: unknown;
  try { raw = JSON.parse(text); } catch { return { ok: false, error: "This is not valid JSON." }; }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { ok: false, error: "Expected a bot export object." };
  const doc = raw as Record<string, unknown>;
  if (doc.version !== 1) return { ok: false, error: typeof doc.version === "number" ? `Export version ${doc.version} is not supported; only version 1 can be imported.` : "The export has no version number; only version 1 can be imported." };
  const bot = doc.bot;
  if (!bot || typeof bot !== "object" || Array.isArray(bot)) return { ok: false, error: "The export has no bot." };
  const b = bot as Record<string, unknown>;
  for (const key of ["name", "avatar", "color", "description", "instructions", "provider", "model", "reasoning"] as const) {
    if (b[key] !== undefined && typeof b[key] !== "string") return { ok: false, error: `“${key}” must be text.` };
  }
  if (typeof b.name !== "string" || !b.name.trim()) return { ok: false, error: "The exported bot has no name." };
  const text_ = (key: string) => (typeof b[key] === "string" ? (b[key] as string) : "");
  const config = (b.config && typeof b.config === "object" && !Array.isArray(b.config) ? b.config : {}) as Record<string, unknown>;
  const limits = (config.limits && typeof config.limits === "object" ? config.limits : {}) as Record<string, unknown>;
  const tools = Array.isArray(config.tools)
    ? config.tools.flatMap((t): BotTool[] => {
      if (!t || typeof t !== "object" || typeof (t as BotTool).id !== "string") return [];
      const { id, params } = t as BotTool;
      return [params && typeof params === "object" && !Array.isArray(params) ? { id, params } : { id }];
    })
    : [];
  const memory = Array.isArray(config.memory)
    ? config.memory.flatMap((a): ScopeAccess[] => a && typeof a === "object" && typeof (a as ScopeAccess).scope === "string" ? [{ scope: (a as ScopeAccess).scope, read: (a as ScopeAccess).read === true, write: (a as ScopeAccess).write === true }] : [])
    : [];
  return {
    ok: true,
    value: {
      version: 1,
      bot: {
        id: "", name: b.name, avatar: text_("avatar"), color: text_("color"), description: text_("description"), instructions: text_("instructions"),
        provider: text_("provider"), model: text_("model"), reasoning: text_("reasoning"),
        config: {
          tools, mcpServers: stringList(config.mcpServers), skills: stringList(config.skills), memory,
          limits: { maxSteps: typeof limits.maxSteps === "number" ? limits.maxSteps : 0, maxToolCalls: typeof limits.maxToolCalls === "number" ? limits.maxToolCalls : 0 },
        },
      },
    },
  };
}

/** Name for a "Save as copy": suffixed and trimmed to the name limit. */
export function copyName(name: string): string {
  const suffix = " (copy)";
  const base = Array.from(name.trim());
  return base.length + suffix.length > LIMITS.name ? `${base.slice(0, LIMITS.name - suffix.length).join("")}${suffix}` : `${base.join("")}${suffix}`;
}
