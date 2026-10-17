import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Event as AgentEvent, Session } from "@/mygo";

const api = vi.hoisted(() => ({ artifact: vi.fn(), openExternal: vi.fn() }));
vi.mock("@/mygo", () => ({ Agents: { approve: vi.fn(), get: vi.fn(), artifact: api.artifact }, Shell: { openExternal: api.openExternal }, events: { agentsEvent: { on: () => () => {} } } }));
vi.mock("@/lib/backend", () => ({ inDesktop: true }));
vi.mock("@/stores/agents", () => ({ useAgents: { getState: () => ({ upsert: vi.fn() }) } }));
vi.mock("@/stores/boxes", () => ({ useBoxes: vi.fn() }));
import { Feed } from "./Feed";

beforeEach(() => { vi.clearAllMocks(); });
afterEach(() => { cleanup(); vi.useRealTimers(); });

let seq = 0;
const ev = (kind: string, text = "", data: unknown = null, sec = 0): AgentEvent => ({ id: ++seq, sessionId: "s1", at: new Date(Date.UTC(2026, 9, 8, 12, 0, sec)).toISOString(), kind, text, data });
/** The run tree's header: its own disclosure trigger, not a step's. */
const run = () => document.querySelector<HTMLElement>('[data-slot="agent-run-tree"] > [data-slot="collapsible"] > [data-slot="collapsible-trigger"]')!;
const tree = () => document.querySelector<HTMLElement>('[data-slot="agent-run-tree"]')!;
const session = (patch: Partial<Session> = {}): Session => ({ id: "s1", live: true, status: "idle", activity: "", pending: [], ...patch }) as Session;
const props = { loading: false, loadingMore: false, error: null, hasMore: false, onLoadMore: () => Promise.resolve(true) };
const show = (events: AgentEvent[], s = session()) => render(<Feed session={s} events={events} {...props} />);

describe("Feed", () => {
  it("shows tool calls as one card of plain-language rows and a shell command as a terminal result, with raw JSON one click away", () => {
    const { container } = show([
      ev("prompt", "look it up"),
      ev("tool", "", { id: "t1", name: "search", server: "docs", status: "running", input: { query: "hooks" } }),
      ev("tool_result", "", { id: "t1", name: "search", server: "docs", status: "ok", output: "3 results" }),
      ev("tool", "", { id: "t2", name: "web_search", status: "ok", input: { action: null, query: "" } }),
      ev("tool", "", { id: "t3", name: "shell", status: "ok", input: { command: `"C:\\Program Files\\PowerShell\\7\\pwsh.exe" -Command 'npm test'`, cwd: "C:\\w" } }),
      ev("message", "Found it."),
    ]);
    expect(screen.getByText("Found it.")).toBeInTheDocument();
    expect(screen.queryByText("3 results")).toBeNull();
    fireEvent.click(run());
    const group = container.querySelector<HTMLElement>('[data-slot="tool-call-group"]')!;
    expect(group).toHaveTextContent("2 tool calls");
    const rows = group.querySelectorAll<HTMLElement>('[data-slot="tool-call"]');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("Used");
    expect(rows[0]).toHaveTextContent("docs · Search");
    expect(rows[1]).toHaveTextContent("Searched the web");
    expect(rows[1]).not.toHaveTextContent("action");
    const terminal = container.querySelector('[data-slot="terminal-result"]');
    expect(terminal).toHaveTextContent(/\$\s*npm test/);
    expect(terminal).not.toHaveTextContent("pwsh");
    expect(container.querySelectorAll("time")).toHaveLength(container.querySelectorAll('[data-slot="day-separator"] time').length);
    // The technical record is one disclosure away.
    fireEvent.click(within(rows[0]!).getByRole("button"));
    const details = screen.getByTestId("tool-details");
    expect(details).toHaveAttribute("data-status", "ok");
    expect(details).toHaveTextContent("search");
    expect(screen.getByLabelText("Tool output")).toHaveTextContent("3 results");
    expect(screen.getByLabelText("Tool input")).toHaveTextContent('"query": "hooks"');
  });

  it("opens a failed shell command with its exit code and keeps a successful one closed", () => {
    const { container } = show([
      ev("prompt", "test it"),
      ev("tool", "", { id: "t1", name: "shell", status: "running", input: { command: "npm run lint" } }),
      ev("tool_result", "", { id: "t1", status: "ok", output: "lint clean" }),
      ev("tool", "", { id: "t2", name: "shell", status: "running", input: { command: "npm test" } }),
      ev("tool_result", "", { id: "t2", status: "error", output: "exit code 1\nnpm ERR! 2 failing tests" }),
      ev("message", "Tests fail."),
    ]);
    fireEvent.click(run());
    const [ok, failed] = Array.from(container.querySelectorAll<HTMLElement>('[data-slot="terminal-result"]'));
    expect(within(ok!).getByRole("button", { name: /npm run lint/ })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("lint clean")).toBeNull();
    expect(ok).not.toHaveTextContent("Exit");
    expect(failed).toHaveTextContent("Exit 1");
    expect(within(failed!).getByRole("button", { name: /npm test/ })).toHaveAttribute("aria-expanded", "true");
    expect(within(failed!).getByLabelText("Command output")).toHaveTextContent("npm ERR! 2 failing tests");
    expect(within(failed!).getByLabelText("Command output")).not.toHaveTextContent("exit code");
    expect(failed).not.toHaveAttribute("title");
  });

  it("puts thoughts in the run as steps labelled by their title, with the full text one click away", () => {
    show([ev("prompt", "plan"), ev("reasoning", "**Planning the change**\n\nThen run the tests carefully.", { id: "r1" }), ev("message", "Ready.")]);
    expect(screen.queryByText("Planning the change")).toBeNull();
    fireEvent.click(run());
    const step = screen.getByRole("button", { name: /Planning the change/ });
    expect(step).not.toHaveTextContent("Thought");
    expect(screen.queryByText("Then run the tests carefully.")).toBeNull();
    fireEvent.click(step);
    expect(screen.getByText("Then run the tests carefully.")).toBeInTheDocument();
  });

  it("keeps the final message outside the folded run and labels the run by its real duration", () => {
    show([
      ev("prompt", "go", null, 0),
      ev("tool", "", { id: "t1", name: "Read", status: "ok", input: { file_path: "a.go" } }, 10),
      ev("message", "Interim note.", null, 20),
      ev("tool", "", { id: "t2", name: "Read", status: "ok", input: { file_path: "b.go" } }, 30),
      ev("message", "All done.", null, 133),
      ev("turn_done", "All done.", { ok: true }, 133),
    ]);
    expect(run()).toHaveTextContent("Worked for");
    expect(run()).toHaveTextContent("2m 13s");
    expect(screen.getByText("All done.")).toBeInTheDocument();
    expect(screen.queryByText("Interim note.")).toBeNull();
    expect(screen.queryByRole("separator", { name: /Turn complete/ })).toBeNull();
    fireEvent.click(run());
    expect(screen.getAllByText("Interim note.").length).toBeGreaterThan(0);
  });

  it("omits the duration when the turn has no usable timestamps", () => {
    show([ev("prompt", "go"), ev("tool", "", { id: "t1", name: "Read", status: "ok", input: { file_path: "a.go" } }), ev("message", "ok"), ev("turn_done", "ok", { ok: true })]);
    expect(run()).toHaveTextContent("Completed");
    expect(run()).not.toHaveTextContent(/\d+s/);
  });

  it("keeps a running run open and live, folds it when it settles, and lets the reader's toggle win", () => {
    const working = session({ status: "working", activity: "Reading" });
    const events = [ev("prompt", "go"), ev("tool", "", { id: "t1", name: "Read", status: "ok", input: { file_path: "a.go" } })];
    const more = [...events, ev("tool", "", { id: "t2", name: "Read", status: "ok", input: { file_path: "b.go" } })];
    const done = [...events, ev("message", "Done."), ev("turn_done", "Done.", { ok: true })];
    const view = render(<Feed {...props} session={working} events={events} />);
    expect(run()).toHaveTextContent("Reading");
    expect(run()).toHaveAttribute("aria-expanded", "true");
    view.rerender(<Feed {...props} session={working} events={more} />);
    expect(document.querySelectorAll('[data-slot="tool-call"]')).toHaveLength(2);
    view.rerender(<Feed {...props} session={session()} events={done} />);
    expect(run()).toHaveTextContent("Completed");
    expect(run()).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByText("Done.")).toBeInTheDocument();
    fireEvent.click(run());
    view.rerender(<Feed {...props} session={session()} events={[...done]} />);
    expect(run()).toHaveAttribute("aria-expanded", "true");
  });

  it("keeps a running run closed once the reader closed it", () => {
    const working = session({ status: "working", activity: "Reading" });
    const events = [ev("prompt", "go"), ev("tool", "", { id: "t1", name: "Read", status: "ok", input: { file_path: "a.go" } })];
    const view = render(<Feed {...props} session={working} events={events} />);
    fireEvent.click(run());
    view.rerender(<Feed {...props} session={working} events={[...events, ev("tool", "", { id: "t2", name: "Read", status: "ok", input: { file_path: "b.go" } })]} />);
    expect(run()).toHaveAttribute("aria-expanded", "false");
    expect(document.querySelector('[data-slot="tool-call"]')).toBeNull();
  });

  it("says in the header of a running run what is happening now, with a ticking elapsed time and the session's tokens", () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date(Date.UTC(2026, 9, 8, 12, 0, 5)));
    show([ev("prompt", "test", null, 0), ev("tool", "", { id: "t1", name: "shell", status: "running", input: { command: "npm test" } }, 2)], session({ status: "working", activity: "Working…", tokens: 14_800 }));
    expect(run()).toHaveTextContent("Running npm test");
    const meta = run().querySelector('[data-slot="run-meta"]')!;
    expect(meta).toHaveTextContent("5s");
    expect(meta).toHaveTextContent("14.8K tokens");
    act(() => { vi.advanceTimersByTime(3000); });
    expect(meta).toHaveTextContent("8s");
  });

  it("counts the run header's tokens without cache reads and breaks them down on hover", async () => {
    const billed = { input: 3_285, cacheWrite: 61_300, cacheRead: 1_099_170, output: 36_200, reasoning: 0, total: 1_199_955, work: 100_785, costUsd: 0, unpriced: ["local/llama"] };
    show([ev("prompt", "test", null, 0), ev("tool", "", { id: "t1", name: "shell", status: "ok", input: { command: "npm test" } }, 1), ev("message", "Done.", null, 2)],
      session({ status: "done", tokens: 100_785, billed }));
    const tokens = run().querySelector<HTMLElement>('[data-slot="run-meta"]')!;
    expect(tokens).toHaveTextContent("100.8K tokens");
    expect(tokens).not.toHaveTextContent("1.2M");
    const trigger = within(tokens).getByText("100.8K tokens");
    fireEvent.pointerEnter(trigger, { pointerType: "mouse" });
    fireEvent.pointerMove(trigger, { pointerType: "mouse" });
    fireEvent.mouseEnter(trigger);
    fireEvent.mouseMove(trigger);
    const breakdown = await screen.findByText((_, el) => el?.getAttribute("data-slot") === "token-breakdown", {}, { timeout: 3000 });
    const rows = [...breakdown.querySelectorAll("dt")].map((dt) => `${dt.textContent} ${dt.nextElementSibling?.textContent}`);
    // An unpriced model makes the cost a lower bound; a settled run has no estimate left.
    expect(rows).toEqual(["Input 3,285", "Cache write 61,300", "Cache read 1,099,170", "Output 36,200", "Cost ≥ $0.00"]);
    expect(breakdown).toHaveTextContent("No price for local/llama");
    expect(breakdown).not.toHaveTextContent("estimated");
  });

  it("shows the messages the agent writes while working live inside the run", () => {
    const working = session({ status: "working", activity: "Working…" });
    const events = [ev("prompt", "go"), ev("message", "Let me check the config first."), ev("tool", "", { id: "t1", name: "Read", status: "running", input: { file_path: "a.go" } })];
    const view = render(<Feed {...props} session={working} events={events} />);
    expect(within(tree()).getByText("Let me check the config first.")).toBeInTheDocument();
    view.rerender(<Feed {...props} session={working} events={[...events, ev("message", "The config sets the port.")]} />);
    expect(within(tree()).getByText("The config sets the port.")).toBeInTheDocument();
    expect(document.querySelector('[data-slot="run-result"]')).toBeNull();
  });

  it("shows the final answer as a result card that folds long text behind Show all", () => {
    const report = Array.from({ length: 30 }, (_, i) => `Line ${i} of the report.`).join("\n\n");
    show([ev("prompt", "report"), ev("tool", "", { id: "t1", name: "Read", status: "ok", input: { file_path: "a.go" } }), ev("message", report), ev("turn_done", "", { ok: true })]);
    const card = document.querySelector<HTMLElement>('[data-slot="run-result"]')!;
    expect(card).toHaveTextContent("Line 0 of the report.");
    const toggle = within(card).getByRole("button", { name: "Show all" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(within(card).getByRole("button", { name: "Show less" })).toHaveAttribute("aria-expanded", "true");
  });

  it("nests a subagent under the tool call that started it, showing only the provider's id and status", () => {
    const { container } = show([
      ev("prompt", "review"),
      ev("tool", "", { id: "call-4", name: "Task", status: "running", input: { description: "Review the diff" } }),
      ev("subagent", "", { id: "provider-agent-42", name: "Research", status: "waiting-on-provider", task: "Read the spec", message: "", toolCallId: "call-4" }),
      ev("subagent", "", { id: "loose-7", name: "Helper", status: "started", task: "", message: "", toolCallId: "elsewhere" }),
    ], session({ status: "working", activity: "Working…" }));
    const call = container.querySelector<HTMLElement>('[data-slot="tool-call"]')!;
    expect(within(call).getByText("Research")).toBeInTheDocument();
    expect(within(call).queryByText("Helper")).toBeNull();
    expect(within(tree()).getByText("Helper")).toBeInTheDocument();
    fireEvent.click(within(call).getByRole("button", { name: /Research/ }));
    expect(screen.getByText("provider-agent-42")).toBeInTheDocument();
    expect(screen.getByText("waiting-on-provider")).toBeInTheDocument();
    expect(tree().querySelector("a")).toBeNull();
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  it("renders data-less legacy events without crashing", () => {
    show([ev("prompt", "fix it"), ev("tool", "Bash: npm test"), ev("tool_result", "passed", { error: false }), ev("message", "All good."), ev("turn_done", "All good.")]);
    expect(screen.getByText("fix it")).toBeInTheDocument();
    expect(screen.getByText("All good.")).toBeInTheDocument();
    fireEvent.click(run());
    const terminal = document.querySelector<HTMLElement>('[data-slot="terminal-result"]')!;
    expect(terminal).toHaveTextContent(/\$\s*npm test/);
    fireEvent.click(within(terminal).getByRole("button", { name: /npm test/ }));
    expect(within(terminal).getByLabelText("Command output")).toHaveTextContent("passed");
  });

  it("loads a generated image through Agents.artifact and shows it as a data URL", async () => {
    api.artifact.mockResolvedValue({ mime: "image/png", data: "AAAA" });
    show([ev("image", "a fox", { id: "i1", path: "C:\\scratch\\fox.png", mime: "image/png", prompt: "a red fox" })]);
    const img = await screen.findByRole("img", { name: "a red fox" });
    expect(api.artifact).toHaveBeenCalledWith("s1", "C:\\scratch\\fox.png");
    expect(img).toHaveAttribute("src", "data:image/png;base64,AAAA");
  });

  it("refuses artifact types that are not raster images", async () => {
    api.artifact.mockResolvedValue({ mime: "image/svg+xml", data: "AAAA" });
    show([ev("image", "x", { id: "i2", path: "C:\\scratch\\x.svg", mime: "image/svg+xml", prompt: "svg" })]);
    expect(await screen.findByRole("alert")).toHaveTextContent("Unsupported image type");
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("renders Markdown without raw HTML or remote images, and opens links through the shell", async () => {
    const { container } = show([ev("message", "**bold** <script>alert(1)</script> <img src=x onerror=y> ![remote](https://evil.test/a.png) [docs](https://example.com/a)")]);
    expect(container.querySelector("strong")).toHaveTextContent("bold");
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("img")).toBeNull();
    fireEvent.click(screen.getByRole("link", { name: "docs" }));
    await waitFor(() => expect(api.openExternal).toHaveBeenCalledWith("https://example.com/a"));
  });

  it("shows a live Thinking… indicator only while the session says it is thinking", () => {
    const view = show([], session({ status: "working", activity: "Thinking…" }));
    expect(screen.getByTestId("live-activity")).toHaveTextContent("Thinking…");
    view.rerender(<Feed session={session({ status: "idle" })} events={[]} loading={false} loadingMore={false} error={null} hasMore={false} onLoadMore={() => Promise.resolve(true)} />);
    expect(screen.queryByTestId("live-activity")).toBeNull();
  });

  it("shows a pending approval as a compact card outside the run, and the waiting command in the open run", () => {
    const input = '{"kind":"command","command":"rm x","cwd":"C:\\\\work"}';
    const { container } = show([
      ev("prompt", "clean up"),
      ev("tool", "", { id: "t1", name: "shell", status: "running", input: { command: "rm x" } }),
      ev("permission", "shell", { id: "p1", tool: "shell", summary: "rm x", input: JSON.parse(input), inputJSON: input, at: "2026-10-08T12:00:00Z" }),
    ], session({ status: "needs-you", pending: [{ id: "p1" } as Session["pending"][number]] }));
    expect(run()).toHaveTextContent("Needs your input");
    expect(run()).toHaveAttribute("aria-expanded", "true");
    expect(container.querySelector('[data-slot="terminal-result"]')).toHaveTextContent("Needs approval");
    expect(screen.getByText("Run a command")).toBeInTheDocument();
    expect(screen.getByLabelText("Command")).toHaveTextContent("rm x");
    expect(screen.queryByText(/Tool permission|Permission request history/)).toBeNull();
    expect(container.textContent).not.toContain('"kind"');
    expect(screen.getByRole("button", { name: "Allow once" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeEnabled();
  });

  it("folds a decided approval into the run as a one-line receipt", () => {
    show([
      ev("prompt", "clean up"),
      ev("permission", "shell", { id: "p1", tool: "shell", summary: "ls", input: { command: "ls" }, inputJSON: '{"command":"ls"}' }),
      ev("decision", "", { id: "p1", allow: true, reason: "" }),
      ev("message", "Listed."),
    ]);
    expect(screen.queryByRole("button", { name: "Allow once" })).toBeNull();
    fireEvent.click(run());
    expect(screen.getByRole("button", { name: /Allowed/ })).toBeInTheDocument();
  });

  it("opens a collapsed run to reveal a search match inside its interim messages", () => {
    const { container } = show([
      ev("prompt", "go"),
      ev("message", "Checking the needle first."),
      ev("tool", "", { id: "t1", name: "Read", status: "ok", input: { file_path: "a.go" } }),
      ev("message", "Final answer."),
    ]);
    expect(screen.queryByText("Checking the needle first.")).toBeNull();
    fireEvent.keyDown(screen.getByRole("log"), { key: "f", ctrlKey: true });
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "needle" } });
    expect(container.querySelectorAll("[data-search-hit] mark")).toHaveLength(1);
    expect(run()).toHaveAttribute("aria-expanded", "true");
  });

  it("holds the place of a running image generation with the ImageGeneration frame", () => {
    show([ev("prompt", "draw"), ev("image_status", "", { id: "g1", status: "running", prompt: "a calm lake" })], session({ status: "working", activity: "Working" }));
    expect(screen.getByText("Generating")).toBeInTheDocument();
    expect(screen.getByText("a calm lake")).toBeInTheDocument();
    expect(api.artifact).not.toHaveBeenCalled();
  });
});

describe("Feed navigation", () => {
  it("steps through highlighted matches without filtering history or hiding paging", async () => {
    const older = ev("message", "Older needle");
    const loaded = [ev("prompt", "Find the needle"), ev("message", "Another needle, then needle.")];
    const load = vi.fn().mockResolvedValue(true);
    const props = { session: session(), loading: false, loadingMore: false, error: null, hasMore: true, onLoadMore: load };
    const view = render(<Feed {...props} events={loaded} />);
    fireEvent.keyDown(screen.getByRole("log"), { key: "f", ctrlKey: true });
    const input = screen.getByRole("searchbox", { name: "Find in conversation" });
    fireEvent.change(input, { target: { value: "needle" } });
    expect(screen.getByRole("status")).toHaveTextContent("1/3");
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("status")).toHaveTextContent("2/3");
    expect(view.container.querySelectorAll('[data-search-hit] mark')).toHaveLength(3);
    const active = view.container.querySelector('[data-search-hit][aria-current="true"]')?.getAttribute("data-search-hit");
    fireEvent.click(screen.getByRole("button", { name: "Load earlier messages" }));
    expect(load).toHaveBeenCalledTimes(1);
    view.rerender(<Feed {...props} events={[older, ...loaded]} />);
    expect(screen.getByRole("status")).toHaveTextContent("3/4");
    expect(view.container.querySelector('[data-search-hit][aria-current="true"]')).toHaveAttribute("data-search-hit", active);
    expect(screen.getByText("Older needle")).toBeInTheDocument();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(view.container.querySelectorAll("[data-search-hit]")).toHaveLength(0);
  });

  it("resets search on session switch and supports scoped keyboard find", () => {
    const props = { loading: false, loadingMore: false, error: null, hasMore: false, onLoadMore: vi.fn().mockResolvedValue(false) };
    const view = render(<Feed {...props} session={session()} events={[ev("message", "needle")]} />);
    expect(screen.queryByRole("searchbox")).toBeNull();
    const log = screen.getByRole("log");
    fireEvent.keyDown(log, { key: "f", ctrlKey: true });
    const input = screen.getByRole("searchbox");
    expect(input).toHaveFocus();
    fireEvent.change(input, { target: { value: "needle" } });
    view.rerender(<Feed {...props} session={session({ id: "s2" })} events={[]} />);
    expect(screen.queryByRole("searchbox")).toBeNull();
  });

  it("distinguishes failed loading from empty history and retries only the supplied operation", () => {
    const retry = vi.fn();
    render(<Feed session={session()} events={[]} loading={false} loadingMore={false} error="Transcript unavailable" hasMore={false} onLoadMore={vi.fn()} onRetry={retry} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Transcript unavailable");
    expect(screen.queryByText("No output has been recorded yet.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry loading conversation" }));
    expect(retry).toHaveBeenCalledTimes(1);
  });
});

describe("pending inputs outside the loaded page", () => {
  it("keeps a real pending question reachable and deduplicates it when its event loads", () => {
    const request = {
      id: "outside-page", at: "2026-10-08T11:00:00Z",
      questions: [{ id: "choice", header: "", prompt: "Choose the target", options: [{ id: "a", label: "Project A", description: "" }], multiple: false, allowText: false, secret: false }],
    };
    const s = session({ questions: [request] });
    const props = { session: s, loading: false, loadingMore: false, error: null, hasMore: true, onLoadMore: vi.fn().mockResolvedValue(true) };
    const view = render(<Feed {...props} events={[]} />);
    expect(screen.getAllByText("Choose the target")).toHaveLength(1);
    view.rerender(<Feed {...props} events={[ev("question", "", request)]} />);
    expect(screen.getAllByText("Choose the target")).toHaveLength(1);
  });
});
