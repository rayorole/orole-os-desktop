import { describe, expect, it } from "vitest";
import { LIMITS, TOOL_IDS, copyName, fingerprint, formFromBot, initialForm, parseBotExport, toInput, validate, type FormState } from "./botForm";
import { bot, config } from "./testing";

const valid = (over: Partial<FormState> = {}): FormState => ({ ...initialForm(), name: "Planner", ...over });

describe("validate", () => {
  it("accepts a minimal bot", () => {
    expect(validate(valid())).toEqual({});
  });

  it("requires a name and limits it to 80 characters", () => {
    expect(validate(valid({ name: "   " })).name).toMatch(/name/i);
    expect(validate(valid({ name: "x".repeat(LIMITS.name) })).name).toBeUndefined();
    expect(validate(valid({ name: "x".repeat(LIMITS.name + 1) })).name).toMatch(/80/);
  });

  it("limits description to 500 and instructions to 32 KiB characters", () => {
    expect(validate(valid({ description: "d".repeat(501) })).description).toMatch(/500/);
    expect(validate(valid({ instructions: "i".repeat(32 * 1024) })).instructions).toBeUndefined();
    expect(validate(valid({ instructions: "i".repeat(32 * 1024 + 1) })).instructions).toMatch(/32,768/);
  });

  it("checks the turn limits (steps 1-30, tool calls 1-60, empty = default)", () => {
    expect(validate(valid({ maxSteps: "", maxToolCalls: "" }))).toEqual({});
    expect(validate(valid({ maxSteps: "30", maxToolCalls: "60" }))).toEqual({});
    expect(validate(valid({ maxSteps: "0" })).maxSteps).toBeDefined();
    expect(validate(valid({ maxSteps: "31" })).maxSteps).toBeDefined();
    expect(validate(valid({ maxToolCalls: "61" })).maxToolCalls).toBeDefined();
    expect(validate(valid({ maxToolCalls: "2.5" })).maxToolCalls).toBeDefined();
  });

  it("limits integrations and skills to 50 each", () => {
    const ids = (n: number) => Array.from({ length: n }, (_, i) => `id${i}`);
    expect(validate(valid({ mcpServers: ids(LIMITS.mcpServers), skills: ids(LIMITS.skills) }))).toEqual({});
    expect(validate(valid({ mcpServers: ids(LIMITS.mcpServers + 1) })).mcpServers).toBeDefined();
    expect(validate(valid({ skills: ids(LIMITS.skills + 1) })).skills).toBeDefined();
  });

  it("limits the Jarvis call policy to 2000 characters", () => {
    expect(validate(valid({ jarvisPolicy: "p".repeat(2000) })).jarvisPolicy).toBeUndefined();
    expect(validate(valid({ jarvisPolicy: "p".repeat(2001) })).jarvisPolicy).toBe("Keep \"When to call me\" under 2000 characters.");
  });
});

describe("toInput", () => {
  it("creates with an empty id and sends the enabled tools without params", () => {
    const form = valid({ model: "openrouter/gpt-x", tools: [TOOL_IDS.datetime] });
    const input = toInput(form, "");
    expect(input).toMatchObject({ id: "", name: "Planner", provider: "openrouter", model: "gpt-x" });
    expect(input.config.tools).toEqual([{ id: "datetime" }]);
    expect(input.config.limits).toEqual({ maxSteps: 0, maxToolCalls: 0 });
  });

  it("keeps slashes of the model id and maps an empty model to the defaults", () => {
    expect(toInput(valid({ model: "openrouter/anthropic/claude" }), "b1")).toMatchObject({ id: "b1", provider: "openrouter", model: "anthropic/claude" });
    expect(toInput(valid({ model: "" }), "b1")).toMatchObject({ provider: "", model: "" });
  });

  it("starts a new bot with every built-in tool on", () => {
    expect(initialForm().tools).toEqual(["web_search", "web_fetch", "datetime", "research"]);
  });

  it("drops tool params saved by older versions and the retired knowledge_search entry", () => {
    const saved = bot("b1", { config: config({ tools: [{ id: "web_search", params: { max_results: 3, allowed_domains: ["a.com"] } }, { id: "knowledge_search" }, { id: "web_search" }] }) });
    const form = formFromBot(saved);
    expect(form.tools).toEqual(["web_search"]);
    expect(toInput(form, "b1").config.tools).toEqual([{ id: "web_search" }]);
  });

  it("round-trips a saved bot", () => {
    const saved = bot("b1", {
      name: "Researcher", provider: "openrouter", model: "gpt-x", reasoning: "low",
      config: config({
        tools: [{ id: "web_search" }], mcpServers: ["srv"], skills: ["s"],
        memory: [{ scope: "user", read: true, write: false }], limits: { maxSteps: 5, maxToolCalls: 12 }, executive: true,
      }),
    });
    const input = toInput(formFromBot(saved), "b1");
    expect(input).toMatchObject({ id: "b1", reasoning: "low", config: { ...saved.config } });
  });

  it("round-trips tool access and code-task overrides, sending only the overrides", () => {
    const saved = bot("b1", { config: config({ codeTasks: true, toolAccess: "read", tasks: { harness: "codex", model: "auto", permissionMode: "auto" } }) });
    const form = formFromBot(saved);
    expect(form).toMatchObject({ toolAccess: "read", taskHarness: "codex", taskModel: "auto", taskReasoning: "", taskPermissionMode: "auto" });
    expect(toInput(form, "b1").config).toMatchObject({ toolAccess: "read", tasks: { harness: "codex", model: "auto", permissionMode: "auto" } });
    expect(toInput(form, "b1").config.tasks).not.toHaveProperty("reasoning");
    const inherited = toInput({ ...form, toolAccess: "", taskHarness: "", taskModel: "", taskPermissionMode: "" }, "b1").config;
    expect(inherited).not.toHaveProperty("toolAccess");
    expect(inherited).not.toHaveProperty("tasks");
  });

  it("round-trips Jarvis settings and omits them when empty", () => {
    const saved = bot("b1", { config: config({ jarvis: { enabled: true, policy: "Only failures", voiceId: "v1", ttsModel: "eleven_flash_v2_5" } }) });
    const form = formFromBot(saved);
    expect(form).toMatchObject({ jarvisEnabled: true, jarvisPolicy: "Only failures", jarvisVoice: "v1", jarvisModel: "eleven_flash_v2_5" });
    expect(toInput(form, "b1").config.jarvis).toEqual({ enabled: true, policy: "Only failures", voiceId: "v1", ttsModel: "eleven_flash_v2_5" });
    expect(toInput({ ...form, jarvisEnabled: false, jarvisVoice: "" }, "b1").config.jarvis).toEqual({ policy: "Only failures", ttsModel: "eleven_flash_v2_5" });
    expect(toInput(initialForm(), "b1").config).not.toHaveProperty("jarvis");
    expect(fingerprint(valid())).not.toBe(fingerprint(valid({ jarvisEnabled: true })));
  });

  it("round-trips the computer switch and omits it when off", () => {
    const saved = bot("b1", { config: config({ computer: true }) });
    const form = formFromBot(saved);
    expect(form.computer).toBe(true);
    expect(toInput(form, "b1").config.computer).toBe(true);
    expect(toInput({ ...form, computer: false }, "b1").config).not.toHaveProperty("computer");
    expect(initialForm().computer).toBe(false);
  });
});

describe("formReducer", () => {
  it("detects unsaved changes independent of toggle order", () => {
    const a = valid({ skills: ["x", "y"] });
    expect(fingerprint(a)).toBe(fingerprint(valid({ skills: ["y", "x"] })));
    expect(fingerprint(a)).not.toBe(fingerprint(valid({ skills: ["x"] })));
    expect(fingerprint(a)).not.toBe(fingerprint({ ...a, toolAccess: "all" }));
    expect(fingerprint(a)).not.toBe(fingerprint({ ...a, taskPermissionMode: "manual" }));
  });
});

describe("parseBotExport", () => {
  const doc = (over: Record<string, unknown> = {}) => JSON.stringify({ version: 1, bot: { name: "Imported", instructions: "Be brief", color: "red", config: { tools: [{ id: "datetime" }], memory: [{ scope: "user", read: true }] } }, ...over });

  it("accepts version 1 and fills defaults", () => {
    const parsed = parseBotExport(doc());
    expect(parsed).toMatchObject({ ok: true, value: { version: 1, bot: { id: "", name: "Imported", color: "red", instructions: "Be brief" } } });
    if (parsed.ok) {
      expect(parsed.value.bot.config.tools).toEqual([{ id: "datetime" }]);
      expect(parsed.value.bot.config.memory).toEqual([{ scope: "user", read: true, write: false }]);
      expect(parsed.value.bot.config.limits).toEqual({ maxSteps: 0, maxToolCalls: 0 });
    }
  });

  it("reads integrations and skills as plain id lists and skips anything else", () => {
    const parsed = parseBotExport(doc({ bot: { name: "Imported", config: { mcpServers: ["srv", 3, "other"], skills: ["s", null] } } }));
    if (!parsed.ok) throw new Error(parsed.error);
    expect(parsed.value.bot.config.mcpServers).toEqual(["srv", "other"]);
    expect(parsed.value.bot.config.skills).toEqual(["s"]);
  });

  it("never takes tool access or code-task settings from a file", () => {
    const parsed = parseBotExport(doc({ bot: { name: "Imported", config: { codeTasks: true, toolAccess: "all", tasks: { permissionMode: "yolo" } } } }));
    if (!parsed.ok) throw new Error(parsed.error);
    expect(parsed.value.bot.config).not.toHaveProperty("toolAccess");
    expect(parsed.value.bot.config).not.toHaveProperty("tasks");
  });

  it("rejects other versions, missing names and malformed input", () => {
    expect(parseBotExport(doc({ version: 2 }))).toMatchObject({ ok: false, error: expect.stringContaining("version 2") });
    expect(parseBotExport(JSON.stringify({ bot: { name: "x" } }))).toMatchObject({ ok: false, error: expect.stringContaining("no version") });
    expect(parseBotExport(JSON.stringify({ version: 1, bot: { name: " " } }))).toMatchObject({ ok: false });
    expect(parseBotExport(JSON.stringify({ version: 1 }))).toMatchObject({ ok: false });
    expect(parseBotExport("not json")).toMatchObject({ ok: false, error: expect.stringContaining("JSON") });
    expect(parseBotExport("[]")).toMatchObject({ ok: false });
    expect(parseBotExport(JSON.stringify({ version: 1, bot: { name: "x", instructions: 3 } }))).toMatchObject({ ok: false });
  });
});

describe("copyName", () => {
  it("suffixes and stays within the name limit", () => {
    expect(copyName("Planner")).toBe("Planner (copy)");
    expect(Array.from(copyName("x".repeat(80))).length).toBe(80);
    expect(copyName("x".repeat(80)).endsWith(" (copy)")).toBe(true);
  });
});
