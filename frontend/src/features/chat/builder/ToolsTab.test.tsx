import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useReducer } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import type { ServerState, Skill } from "@/mygo";
import { useUI } from "@/stores/ui";
import { formReducer, initialForm, type FormState, type Problems } from "./botForm";
import { bridge, resetBridge } from "./testing";
import { ToolsPage, ToolsTab } from "./ToolsTab";

const server = (id: string, over: Partial<ServerState> = {}): ServerState => ({
  id, name: `Server ${id}`, url: `https://example.com/${id}`, transport: "http", adapter: "", enabled: true, authKind: "oauth", oauthClientId: "", scopes: "", tokenHeader: "",
  createdAt: 1, status: "connected", authorized: true, toolCount: 3, ...over,
});
const skill = (name: string, over: Partial<Skill> = {}): Skill => ({
  name, description: `${name} description`, body: "", files: [], enabled: true, source: "orole", addedAt: 1, updatedAt: 1, ...over,
});

let latest: FormState;
function Harness({ initial, problems = {} }: { initial?: Partial<FormState>; problems?: Problems }) {
  const [form, dispatch] = useReducer(formReducer, { ...initialForm(), tools: [], ...initial });
  latest = form;
  return <ToolsTab form={form} dispatch={dispatch} problems={problems} />;
}

beforeEach(() => {
  resetBridge();
  useUI.setState({ view: "chat", settingsSection: "" });
});
afterEach(cleanup);

describe("integrations", () => {
  it("lists each server with its logo and name, and toggles the whole server", async () => {
    bridge.mcp.list.mockResolvedValue([
      server("s1", { name: "Vercel", url: "https://mcp.vercel.com" }),
      server("s2", { name: "Internal" }),
    ]);
    render(<Harness />);
    const list = await screen.findByRole("list", { name: "Integrations" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(within(list).getByRole("img", { name: "Vercel" })).toBeInTheDocument();
    expect(within(list).getByText("Internal")).toBeInTheDocument();
    expect(bridge.mcp.list).toHaveBeenCalledTimes(1);

    const vercel = screen.getByRole("switch", { name: "Vercel" });
    expect(vercel).not.toBeChecked();
    fireEvent.click(vercel);
    expect(latest.mcpServers).toEqual(["s1"]);
    expect(screen.getByRole("switch", { name: "Vercel" })).toBeChecked();
    fireEvent.click(screen.getByRole("switch", { name: "Vercel" }));
    expect(latest.mcpServers).toEqual([]);
  });

  it("never lists tools", async () => {
    bridge.mcp.list.mockResolvedValue([server("s1", { name: "Vercel", toolCount: 9 })]);
    render(<Harness />);
    await screen.findByText("Vercel");
    expect(screen.queryByText(/tool/i, { selector: "li *" })).toBeNull();
  });

  it("shows a badge when a server is not usable now, and still allows the toggle", async () => {
    bridge.mcp.list.mockResolvedValue([
      server("a", { name: "Auth", status: "auth_required" }),
      server("b", { name: "Paused", enabled: false, status: "disabled" }),
      server("c", { name: "Flaky", status: "backoff" }),
      server("d", { name: "Fine" }),
    ]);
    render(<Harness />);
    await screen.findByText("Auth");
    expect(screen.getByText("Needs sign-in")).toBeInTheDocument();
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(screen.getByText("Retrying")).toBeInTheDocument();
    expect(screen.queryAllByText(/Needs sign-in|Off|Retrying/)).toHaveLength(3);
    fireEvent.click(screen.getByRole("switch", { name: "Paused" }));
    expect(latest.mcpServers).toEqual(["b"]);
  });

  it("offers to remove integrations that no longer exist", async () => {
    bridge.mcp.list.mockResolvedValue([server("s1", { name: "Vercel" })]);
    render(<Harness initial={{ mcpServers: ["s1", "gone"] }} />);
    await screen.findByText("Vercel");
    expect(screen.getByRole("switch", { name: "Vercel" })).toBeChecked();
    expect(screen.getByText("Removed integration")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(latest.mcpServers).toEqual(["s1"]);
    expect(screen.queryByText("Removed integration")).toBeNull();
  });

  it("points to Integrations when none are connected", async () => {
    render(<Harness />);
    expect(await screen.findByText("No integrations connected yet.")).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Integrations" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Open Integrations" }));
    expect(bridge.desktop.openSettings).toHaveBeenCalledWith("integrations");
  });

  it("shows a load error and a validation problem", async () => {
    bridge.mcp.list.mockRejectedValue(new Error("boom"));
    render(<Harness problems={{ mcpServers: "At most 50 integrations can be enabled." }} />);
    expect(await screen.findByText("boom")).toBeInTheDocument();
    expect(screen.getByText("At most 50 integrations can be enabled.")).toBeInTheDocument();
    expect(screen.queryByText("No integrations connected yet.")).toBeNull();
  });
});

describe("skills", () => {
  it("shows only the skill name and toggles it on the bot", async () => {
    bridge.skills.list.mockResolvedValue([skill("planner"), skill("writer")]);
    render(<Harness />);
    const list = await screen.findByRole("list", { name: "Skills" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByText("planner description")).toBeNull();
    expect(screen.queryByText("writer description")).toBeNull();

    fireEvent.click(screen.getByRole("switch", { name: "writer" }));
    expect(latest.skills).toEqual(["writer"]);
    fireEvent.click(screen.getByRole("switch", { name: "planner" }));
    expect(latest.skills).toEqual(["writer", "planner"]);
    fireEvent.click(screen.getByRole("switch", { name: "writer" }));
    expect(latest.skills).toEqual(["planner"]);
  });

  it("shows built-in skills as on and locked, without storing them", async () => {
    bridge.skills.list.mockResolvedValue([skill("core", { source: "builtin" }), skill("mine")]);
    render(<Harness />);
    const core = await screen.findByRole("switch", { name: "core" });
    expect(core).toBeChecked();
    expect(core).toHaveAttribute("aria-disabled", "true");
    expect(screen.getByText("Built in")).toBeInTheDocument();
    fireEvent.click(core);
    expect(latest.skills).toEqual([]);
    fireEvent.click(screen.getByRole("switch", { name: "mine" }));
    expect(latest.skills).toEqual(["mine"]);
  });

  it("flags disabled skills with an Off badge", async () => {
    bridge.skills.list.mockResolvedValue([skill("quiet", { enabled: false }), skill("loud")]);
    render(<Harness />);
    const row = (await screen.findByText("quiet")).closest("li")!;
    expect(within(row).getByText("Off")).toBeInTheDocument();
    expect(within(screen.getByText("loud").closest("li")!).queryByText("Off")).toBeNull();
  });

  it("offers to remove skills that are not in the registry", async () => {
    bridge.skills.list.mockResolvedValue([skill("planner")]);
    render(<Harness initial={{ skills: ["planner", "stale"] }} />);
    await screen.findByText("planner");
    expect(screen.getByText("stale")).toBeInTheDocument();
    expect(screen.getByText("Removed skill")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(latest.skills).toEqual(["planner"]);
  });

  it("says when there are no skills", async () => {
    render(<Harness />);
    expect(await screen.findByText("No skills yet.")).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Skills" })).toBeNull();
  });

  it("shows a load error", async () => {
    bridge.skills.list.mockRejectedValue(new Error("no registry"));
    render(<Harness />);
    expect(await screen.findByText("no registry")).toBeInTheDocument();
  });
});

function PageHarness({ initial }: { initial?: Partial<FormState> }) {
  const [form, dispatch] = useReducer(formReducer, { ...initialForm(), tools: [], ...initial });
  latest = form;
  return <ToolsPage form={form} dispatch={dispatch} />;
}

describe("tools page", () => {
  it("switches the bot's own computer on and off with one switch", async () => {
    render(<PageHarness />);
    const toggle = await screen.findByRole("switch", { name: "Has its own computer" });
    expect(toggle).not.toBeChecked();
    fireEvent.click(toggle);
    expect(latest.computer).toBe(true);
    fireEvent.click(screen.getByRole("switch", { name: "Has its own computer" }));
    expect(latest.computer).toBe(false);
    expect(screen.getAllByRole("switch", { name: /computer/i })).toHaveLength(1);
  });
});
