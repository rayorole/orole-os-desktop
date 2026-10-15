import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Message } from "@/mygo";
import { useChat } from "@/stores/chat";

vi.mock("@/lib/openExternal", () => ({ isWebURL: (u: string) => /^https?:/.test(u), openExternal: vi.fn() }));

import { ChatMessageView } from "./MessageBubble";

const message = (parts: Message["parts"], over: Partial<Message> = {}): Message => ({
  id: "m", botId: "a", seq: 2, role: "assistant", author: "bot:a", parts, status: "complete", createdAt: 1, ...over,
});
const view = (m: Message, onRetry?: () => void) => render(<ChatMessageView message={m} botName="Atlas" botAvatar="" onRetry={onRetry} />);
afterEach(cleanup);

describe("message parts", () => {
  it("folds a finished reply's work into 'Thought for …' that opens to its thinking and every step in order", () => {
    view(message([
      { type: "reasoning", text: "weighing options", durationMs: 4200 },
      { type: "tool", id: "t", name: "web_search", status: "done", durationMs: 1200, summary: "oslo flights" },
      { type: "text", text: "**Bold** answer" },
    ], { createdAt: 10_000, finishedAt: 17_000 }));
    expect(screen.getByText("Bold").tagName).toBe("STRONG");
    expect(screen.queryByText("weighing options")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Thought for 7s" }));
    const steps = [...document.querySelectorAll("[data-slot=thinking-step]")].map((step) => step.textContent);
    expect(steps).toEqual(["Thinkingweighing options", "Searched the weboslo flights", "Writing the answer"]);
  });

  it("draws the bot's computer use as one card that opens the Computer tab", () => {
    useChat.setState({ selectedBotId: "a", pane: null });
    view(message([
      { type: "computer", status: "done", text: "Starting computer…" },
      { type: "text", text: "Opening the site." },
      { type: "computer", status: "running", text: "browser_open https://example.test" },
    ]));
    expect(screen.getAllByText("Using the computer")).toHaveLength(1);
    expect(screen.getByText("browser_open https://example.test")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Using the computer/ }));
    expect(useChat.getState().pane).toEqual({ kind: "computer", id: "a" });
  });

  it.each([["running", "Working"], ["done", "Done"], ["failed", "Failed"], ["waiting", "Waiting for you"]])("labels a %s computer card", (status, label) => {
    useChat.setState({ selectedBotId: "a", pane: null });
    view(message([{ type: "computer", status, text: "<b>hi</b>" }]));
    expect(screen.getByRole("button", { name: /Using the computer/ })).toHaveTextContent(label);
    expect(screen.getByText("<b>hi</b>")).toBeInTheDocument();
    if (status === "waiting") expect(screen.getByText("Take over")).toBeInTheDocument();
    else expect(screen.queryByText("Take over")).toBeNull();
  });

  it("labels an unknown or inherited status as working", () => {
    useChat.setState({ selectedBotId: "a", pane: null });
    view(message([{ type: "computer", status: "toString", text: "step" }]));
    expect(screen.getByRole("button", { name: /Using the computer/ })).toHaveTextContent("Working");
  });

  it("shows what the computer did as plain text under the step, unless it repeats it", () => {
    useChat.setState({ selectedBotId: "a", pane: null });
    view(message([{ type: "computer", status: "done", text: "Same", summary: "Same" }]));
    expect(screen.getAllByText("Same")).toHaveLength(1);
    cleanup();
    view(message([{ type: "computer", status: "done", text: "Opened site", summary: "<i>Read the page</i>" }]));
    expect(screen.getByText("<i>Read the page</i>")).toBeInTheDocument();
    expect(document.querySelector("[data-slot=computer-card] i")).toBeNull();
  });

  it("draws a call part as a call card", () => {
    view(message([{ type: "call", id: "c1", name: "outbound", status: "completed", title: "deploy failed", durationMs: 75_000, summary: "est. $0.15" }]));
    expect(screen.getByText("Called you")).toBeInTheDocument();
    expect(screen.getByText("1:15 · est. $0.15")).toBeInTheDocument();
    expect(document.querySelector("[data-slot=thinking-panel]")).toBeNull();
  });

  it("leaves no panel behind a reply that only answered", () => {
    view(message([{ type: "text", text: "Done." }]));
    expect(document.querySelector("[data-slot=thinking-panel]")).toBeNull();
  });

  it("gathers citations into one sources popover and lists only http(s) links as links", () => {
    view(message([
      { type: "text", text: "see" },
      { type: "citation", url: "https://a.test/x", title: "A page" },
      { type: "citation", url: "https://a.test/x", title: "dup" },
      { type: "citation", url: "javascript:alert(1)", title: "bad" },
    ]));
    fireEvent.click(screen.getByRole("button", { name: /2 sources/ }));
    const list = screen.getByRole("list");
    expect(within(list).getByRole("link", { name: /A page/ })).toHaveAttribute("href", "https://a.test/x");
    expect(within(list).queryByRole("link", { name: /bad/ })).toBeNull();
  });

  it("shows a research report with its title and a copy action", () => {
    view(message([{ type: "report", title: "Sky colour", text: "Rayleigh scattering." }]));
    const report = screen.getByRole("region", { name: "Sky colour" });
    expect(within(report).getByText("Rayleigh scattering.")).toBeInTheDocument();
    expect(within(report).getByRole("button", { name: "Copy report" })).toBeEnabled();
  });

  it("shows memory as chips and notices inline, and a delegation in the thinking panel", () => {
    view(message([
      { type: "delegation", name: "Coder", status: "done" },
      { type: "memory", text: "likes tea" },
      { type: "notice", text: "Earlier messages were summarised." },
    ]));
    expect(screen.queryByText(/Handoff by/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "1 step" }));
    expect(screen.getByText("Handoff by Atlas")).toBeInTheDocument();
    expect(screen.getAllByText("Coder").length).toBeGreaterThan(0);
    expect(screen.getByText("remembered 1")).toBeInTheDocument();
    expect(screen.getByText("likes tea")).toBeInTheDocument();
    expect(screen.getByRole("note")).toHaveTextContent("Earlier messages were summarised.");
  });

  it("marks a tool left running in a settled message as not finished", () => {
    view(message([{ type: "tool", id: "t", name: "web_fetch", status: "running" }]));
    fireEvent.click(screen.getByRole("button", { name: "1 step" }));
    expect(screen.getByText("Page read did not finish")).toBeInTheDocument();
  });

  it("turns research progress rows into an outline and keeps the sources out of the answer", () => {
    view(message([
      { type: "tool", id: "research:plan", name: "research", status: "done", summary: "2 questions" },
      { type: "tool", id: "research:q1", name: "research", status: "done", summary: "What is X? (3 sources)" },
      { type: "tool", id: "research:q2", name: "research", status: "error", summary: "What is Y?" },
      { type: "tool", id: "research:sources", name: "research", status: "done", summary: "3 sources read" },
      { type: "tool", id: "research:synthesis", name: "research", status: "done", summary: "Report ready" },
      { type: "report", title: "X and Y", text: "## Summary\n\nX is a thing [1]." },
      { type: "citation", url: "https://a.test/x", title: "A page" },
    ]));
    expect(screen.queryByText("What is X?")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "1 step" }));
    const outline = screen.getByText("Research").closest("[data-slot=research-report]") as HTMLElement;
    expect(within(outline).getByText("What is X?")).toBeInTheDocument();
    expect(within(outline).getByText("3 src")).toBeInTheDocument();
    expect(within(outline).getByText("Could not be researched")).toBeInTheDocument();
    expect(within(outline).getByText("2/3 sections · 3 sources read")).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "X and Y" })).toHaveTextContent("X is a thing");
  });

  it("shows a handoff to another bot in the timeline with a link to its chat, and hides the raw ask_bot row", () => {
    const open = vi.fn();
    render(<ChatMessageView message={message([
      { type: "tool", id: "t", name: "ask_bot", status: "done", summary: "Answer from Researcher" },
      { type: "delegation", id: "d", name: "Researcher", botId: "r1", status: "done", text: "Research local agent computers", summary: "Use Incus per agent." },
    ])} botName="Executive" botAvatar="" bots={[{ id: "r1", name: "Researcher", avatar: "" }]} onOpenBot={open} />);
    fireEvent.click(screen.getByRole("button", { name: "1 step" }));
    const card = document.querySelector("[data-slot=agent-handoff]") as HTMLElement;
    expect(within(card).getByText("Executive")).toBeInTheDocument();
    expect(within(card).getByText("Researcher")).toBeInTheDocument();
    expect(screen.getByText("Research local agent computers")).toBeInTheDocument();
    expect(screen.getByText(/Researcher answered:/)).toBeInTheDocument();
    expect(screen.getByText("Use Incus per agent.")).toBeInTheDocument();
    expect(screen.queryByText(/Ask bot/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Open Researcher's chat" }));
    expect(open).toHaveBeenCalledWith("r1");
  });

  it("draws a task another bot handed off as a handoff card naming who handed it off", () => {
    render(<ChatMessageView message={message([{ type: "text", text: "Compare Incus and Docker for agent sandboxes" }], { role: "user", author: "bot:exec" })}
      botName="Researcher" botAvatar="" bots={[{ id: "exec", name: "Executive", avatar: "" }]} handoffSettled={false} />);
    const card = screen.getByText("Handoff by Executive", { selector: "p" }).closest("[data-slot=agent-handoff]") as HTMLElement;
    expect(within(card).getByText("Executive")).toBeInTheDocument();
    expect(within(card).getByText("Researcher")).toBeInTheDocument();
    expect(screen.getByText("Compare Incus and Docker for agent sandboxes")).toBeInTheDocument();
    expect(card).not.toHaveAttribute("data-settled");
  });

  it("keeps the panel open while the reply runs, showing the running step and the pages read so far", () => {
    view(message([
      { type: "tool", id: "t", name: "web_search", status: "running", summary: "csp frame-src" },
      { type: "citation", url: "https://a.test/csp", title: "CSP: frame-src" },
    ], { status: "running" }));
    expect(screen.getByRole("button", { name: /^Searching the web/ })).toHaveAttribute("aria-expanded", "true");
    const step = document.querySelector("[data-slot=thinking-step][data-state=running]") as HTMLElement;
    expect(step).toHaveTextContent("csp frame-src");
    expect(within(step).getByRole("button", { name: "CSP: frame-src (a.test)" })).toBeInTheDocument();
  });

  it("shows an error with Retry, and a stopped reply", () => {
    const retry = vi.fn();
    view(message([], { status: "error", error: "Invalid API key" }), retry);
    expect(screen.getByRole("alert")).toHaveTextContent("Invalid API key");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(retry).toHaveBeenCalled();
    cleanup();
    view(message([{ type: "text", text: "partial" }], { status: "canceled" }));
    expect(screen.getByText("Stopped")).toBeInTheDocument();
  });

  it("renders a user message as plain text with attachment chips", () => {
    view(message([{ type: "attachment", attachmentId: "f", name: "notes.txt", mime: "text/plain", size: 2048 }, { type: "text", text: "**not bold**" }], { role: "user", author: "user" }));
    expect(screen.getByText("**not bold**")).toBeInTheDocument();
    expect(screen.getByText("notes.txt")).toBeInTheDocument();
    expect(screen.getByText("2 KB")).toBeInTheDocument();
  });

  describe("while a reply runs", () => {
    const status = () => document.querySelector("[data-slot=thinking-panel] [role=status]");

    it("says what it is doing, from the first moment to the last", () => {
      const { unmount } = view(message([], { status: "running" }));
      expect(status()).toHaveTextContent("Thinking");
      unmount();
      view(message([{ type: "tool", id: "t1", name: "web_search", status: "running", summary: "q" }], { status: "running" }));
      expect(status()).toHaveTextContent("Searching the web");
    });

    it("shows the model's thinking as it streams, then that it is writing", () => {
      const { unmount } = view(message([{ type: "reasoning", text: "Check the deploy log first." }], { status: "running" }));
      expect(status()).toHaveTextContent("Thinking");
      expect(screen.getByText("Check the deploy log first.")).toBeInTheDocument();
      unmount();
      view(message([{ type: "reasoning", text: "Check the deploy log first." }, { type: "text", text: "Here is" }], { status: "running" }));
      expect(status()).toHaveTextContent("Writing the answer");
      expect(document.querySelectorAll("[data-slot=thinking-step][data-state=running]")).toHaveLength(1);
    });

    it("keeps saying it is thinking between tool calls", () => {
      view(message([{ type: "tool", id: "t1", name: "orole_get_skill", status: "done", summary: "grill-me" }], { status: "running" }));
      expect(status()).toHaveTextContent("Thinking");
    });

    it("names the bot it is asking, and a queued reply as starting", () => {
      const { unmount } = view(message([{ type: "delegation", id: "d1", name: "Coder", status: "running" }], { status: "running" }));
      expect(status()).toHaveTextContent("Asking Coder");
      unmount();
      view(message([], { status: "pending" }));
      expect(status()).toHaveTextContent("Starting");
    });

    it("counts the time it has been working", () => {
      view(message([], { status: "running", createdAt: Date.now() - 75_000 }));
      expect(document.querySelector("[data-slot=thinking-elapsed]")).toHaveTextContent("1m 15s");
    });

    it("labels skill and visual tools in plain words", () => {
      view(message([{ type: "tool", id: "t1", name: "orole_get_skill", status: "done", summary: "x" }, { type: "tool", id: "t2", name: "show_table", status: "running" }], { status: "running" }));
      expect(status()).toHaveTextContent("Preparing a visual");
    });
  });
});

describe("report-back messages", () => {
  it("draws what another bot reported back as a collapsed card naming that bot", () => {
    const report = message([{ type: "text", text: "Grok Bot is a new app from Acme." }], { role: "user", author: "report:r" });
    render(<ChatMessageView message={report} botName="Atlas" botAvatar="" bots={[{ id: "r", name: "Researcher", avatar: "" }]} />);
    expect(screen.getByText("Researcher reported back")).toBeInTheDocument();
    expect(screen.queryByText(/Handoff by/)).toBeNull();
    expect(screen.getByText("Grok Bot is a new app from Acme.")).toBeInTheDocument();
  });
});

describe("quoted user messages", () => {
  it("draws the quoted passage as a quote above the request", () => {
    const sent = message([{ type: "text", text: "> I wrote this in a neutral register.\n\nExplain this selected text." }], { role: "user", author: "user" });
    render(<ChatMessageView message={sent} botName="Atlas" botAvatar="" />);
    const quote = document.querySelector("[data-slot=chat-quote]") as HTMLElement;
    expect(quote).toHaveTextContent("I wrote this in a neutral register.");
    expect(screen.getByText("Explain this selected text.")).toBeInTheDocument();
    expect(screen.queryByText(/^>/)).toBeNull();
  });
});

describe("documents and todo lists a reply wrote", () => {
  const doc = (title: string, content: string, version: number) => ({ type: "ui" as const, id: "write_document", name: "document", spec: JSON.stringify({ title, content, version }) });

  it("shows a document once at its latest version, opens it beside the conversation and copies its markdown", async () => {
    const writeText = vi.fn(async () => {});
    Object.assign(navigator, { clipboard: { writeText } });
    useChat.setState({ pane: null });
    view(message([doc("Plan", "# Plan\n\nfirst", 1), { type: "text", text: "Revised." }, doc("Plan", "# Plan\n\nsecond draft here", 2)]));
    const cards = document.querySelectorAll("[data-slot=document-card]");
    expect(cards).toHaveLength(1);
    expect(cards[0]).toHaveTextContent("Plan");
    expect(cards[0]).toHaveTextContent("v2 · 4 words");
    const open = screen.getByRole("button", { name: "Open" });
    expect(open).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(open);
    expect(useChat.getState().pane).toEqual({ kind: "document", messageId: "m", partIndex: 2 });
    expect(screen.getByRole("button", { name: "Open" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(screen.getByRole("button", { name: "Copy document" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("# Plan\n\nsecond draft here"));
    useChat.setState({ pane: null });
  });

  it("draws the agent's working list with its counter", () => {
    view(message([{ type: "ui", id: "update_todos", name: "todo", spec: JSON.stringify({ title: "Plan", items: [
      { id: "a", text: "Read", status: "done" }, { id: "b", text: "Fix", status: "active" }, { id: "c", text: "Skip", status: "cancelled" },
    ] }) }]));
    expect(screen.getByRole("list", { name: "Plan" })).toBeInTheDocument();
    expect(screen.getByText("1/2")).toBeInTheDocument();
  });
});
