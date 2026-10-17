import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { Event as AgentEvent, ServerState, Session } from "@/mygo";

const api = vi.hoisted(() => ({
  agents: { publish: vi.fn() },
  mcp: { list: vi.fn(), catalog: vi.fn(), setToolPermission: vi.fn(), enable: vi.fn(), startAuth: vi.fn(), connect: vi.fn() },
}));
vi.mock("@/mygo", () => ({ Agents: api.agents, MCP: api.mcp, Projects: {}, Machines: { list: vi.fn(async () => []) }, Shell: {}, events: { mcpStatus: { on: () => () => {} }, mcpAuthDone: { on: () => () => {} }, daemonState: { on: () => () => {} } } }));
vi.mock("@/lib/backend", () => ({ inDesktop: true }));
import { useAgents } from "@/stores/agents";
import { TooltipProvider } from "@/components/ui/tooltip";
import { backgroundTasks, BackgroundTasksSection } from "./BackgroundTasksSection";
import { ChangesSection } from "./ChangesSection";
import { ConnectorsSection } from "./ConnectorsSection";
import { SessionRail } from "./SessionRail";

let seq = 0;
const subagent = (at: string, data: Record<string, string>): AgentEvent => ({ id: ++seq, sessionId: "s", at, kind: "subagent", text: "", data });
const session = (patch: Partial<Session> = {}) => ({ id: "s", title: "Fix the parser", task: "", kind: "claude", machine: "local", project: "C:\\repo", dir: "C:\\repo", live: false, status: "done", branch: "fix/parser", files: 3, additions: 120, deletions: 4, prUrl: "", prNumber: 0, updatedAt: "2026-10-15T10:00:00Z", ...patch }) as Session;
const server = (patch: Partial<ServerState>): ServerState => ({ id: "gh", name: "GitHub", url: "https://mcp.example.com/mcp", transport: "", adapter: "", enabled: true, authKind: "oauth", oauthClientId: "", scopes: "", tokenHeader: "", createdAt: 0, status: "connected", authorized: true, toolCount: 3, ...patch });

beforeEach(() => { vi.clearAllMocks(); localStorage.clear(); });
afterEach(cleanup);

it("folds a background agent's reports: first report starts it, a report without text keeps the earlier text", () => {
  const [task] = backgroundTasks([
    subagent("2026-10-15T10:00:00Z", { id: "a", name: "Research", status: "running", task: "Find the flaky test" }),
    subagent("2026-10-15T10:05:00Z", { id: "a", status: "completed", message: "Found it" }),
  ]);
  expect(task).toMatchObject({ name: "Research", task: "Find the flaky test", message: "Found it", state: "done", startedAt: Date.parse("2026-10-15T10:00:00Z"), updatedAt: Date.parse("2026-10-15T10:05:00Z") });
});

it("lifts failed agents above running ones and folds finished ones behind one row", () => {
  render(<TooltipProvider><BackgroundTasksSection events={[
    subagent("2026-10-15T10:00:00Z", { id: "run", name: "Implement task 5", status: "running" }),
    subagent("2026-10-15T10:01:00Z", { id: "ok", name: "Review task 3", status: "completed" }),
    subagent("2026-10-15T10:02:00Z", { id: "bad", name: "Review task 4", status: "failed", message: "Timed out" }),
  ]} /></TooltipProvider>);
  const list = screen.getByRole("list", { name: "Background tasks" });
  const names = () => within(list).queryAllByText(/^(Implement|Review) task/).map((node) => node.textContent);
  expect(names()).toEqual(["Review task 4", "Implement task 5"]);
  expect(within(list).getByText("Failed: Timed out")).toBeInTheDocument();
  fireEvent.click(within(list).getByRole("button", { name: /1 finished/ }));
  expect(names()).toEqual(["Review task 4", "Implement task 5", "Review task 3"]);
});

it("creates the pull request only after the dialog confirms, with push, PR and the confirmation set", async () => {
  const published = session({ prUrl: "https://github.com/acme/app/pull/7", prNumber: 7 });
  api.agents.publish.mockResolvedValue(published);
  render(<TooltipProvider><ChangesSection session={session()} /></TooltipProvider>);
  fireEvent.click(screen.getByRole("button", { name: "Create PR" }));
  fireEvent.click(await screen.findByRole("checkbox"));
  expect(api.agents.publish).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "Create pull request" }));
  await waitFor(() => expect(api.agents.publish).toHaveBeenCalledWith("s", { message: "", push: true, pr: true, title: "Fix the parser", body: "", base: "", draft: true, confirmed: true }));
  await waitFor(() => expect(useAgents.getState().sessions.s?.prNumber).toBe(7));
});

it("does not offer Create PR while the agent works or when nothing changed", () => {
  const { rerender } = render(<TooltipProvider><ChangesSection session={session({ live: true, status: "working" })} /></TooltipProvider>);
  expect(screen.getByRole("button", { name: "Create PR" })).toBeDisabled();
  rerender(<TooltipProvider><ChangesSection session={session({ files: 0, additions: 0, deletions: 0 })} /></TooltipProvider>);
  expect(screen.getByRole("button", { name: "Create PR" })).toBeDisabled();
});

it("offers the fix for each dropped connector and turns off only the tools that are on", async () => {
  api.mcp.list.mockResolvedValue([server({}), server({ id: "li", name: "Linear", status: "auth_required" }), server({ id: "se", name: "Sentry", status: "backoff", lastError: "No answer within 10 seconds" })]);
  api.mcp.catalog.mockResolvedValue({ tools: [
    { name: "search_code", effect: "read", permission: "allow" },
    { name: "create_issue", effect: "write", permission: "" },
    { name: "merge_pull_request", effect: "destructive", permission: "deny" },
  ] });
  render(<TooltipProvider><ConnectorsSection /></TooltipProvider>);
  const list = await screen.findByRole("list", { name: "Connectors" });
  expect(within(list).getByRole("button", { name: "Sign in" })).toBeInTheDocument();
  expect(within(list).getByRole("button", { name: "Reconnect" })).toBeInTheDocument();
  expect(within(list).getByText("No answer within 10 seconds")).toBeInTheDocument();
  fireEvent.click(within(list).getByRole("button", { name: "GitHub" }));
  expect(await within(list).findByText("2 of 3 tools")).toBeInTheDocument();
  expect(within(list).getByRole("switch", { name: "merge_pull_request" })).not.toBeChecked();
  fireEvent.click(within(list).getByRole("button", { name: "Turn all off" }));
  await waitFor(() => expect(api.mcp.setToolPermission).toHaveBeenCalledTimes(2));
  expect(api.mcp.setToolPermission.mock.calls.map((call) => call.slice(1))).toEqual([["search_code", "deny"], ["create_issue", "deny"]]);
});

it("lays the sections out in the order the person dragged them into, appending sections that order does not name", () => {
  api.mcp.list.mockResolvedValue([]);
  localStorage.setItem("orole.taskRail.order", JSON.stringify(["changes", "gone", "context", "changes"]));
  render(<TooltipProvider><SessionRail session={session()} events={[]} /></TooltipProvider>);
  const titles = within(screen.getByRole("complementary", { name: "Session rail" })).getAllByRole("region").map((section) => section.getAttribute("aria-label"));
  expect(titles).toEqual(["Changes", "Context", "Environment", "Connectors", "Background tasks"]);
});
