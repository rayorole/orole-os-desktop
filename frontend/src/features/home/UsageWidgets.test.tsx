import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { TokenTotals, UsageStats } from "@/mygo";

const usage = vi.hoisted(() => ({ stats: null as UsageStats | null }));
vi.mock("./useUsageStats", () => ({ useUsageStats: () => ({ stats: usage.stats, status: "ready" }) }));
vi.mock("@/mygo", () => ({}));
import { ByAgentWidget, LatestSessionsWidget, TokensWidget, UsageTotalsWidget } from "./UsageWidgets";

afterEach(cleanup);

const totals = (input: number, cacheRead: number, patch: Partial<TokenTotals> = {}): TokenTotals =>
  ({ input, output: 0, cacheRead, cacheWrite: 0, reasoning: 0, total: input + cacheRead, work: input, costUsd: 0, unpriced: [], ...patch });
const usageWindow = (tokens: TokenTotals) => ({ sessions: 1, agents: [], activeDays: 2, tokens });
const props = { widgetType: "usage", onConfig: () => {}, onRemove: () => {} };

it("headlines tokens without cache reads in every usage widget, like the task meter", () => {
  // Claude used far more tokens overall, nearly all cache reads; Codex did more new work.
  const claude = totals(100_000, 9_000_000), codex = totals(400_000, 0);
  usage.stats = {
    day: usageWindow(totals(500_000, 9_000_000)), week: usageWindow(totals(500_000, 9_000_000)), all: usageWindow(totals(500_000, 9_000_000)),
    byAgent: [{ kind: "codex", sessions: 1, tokens: codex }, { kind: "claude", sessions: 1, tokens: claude }],
    daily: [], latest: [{ id: "s1", kind: "claude", title: "Fix it", project: "", model: "", provider: "", status: "done", toolCalls: 0, failedTools: 0, tokens: claude, startedAt: "", updatedAt: "" }],
  } as unknown as UsageStats;

  render(<TokensWidget {...props} />);
  expect(screen.getByLabelText("500,000 tokens excluding cache reads, 24h")).toHaveTextContent("500.00K");
  expect(screen.getByText("250.00K per active day")).toBeInTheDocument();
  cleanup();

  render(<ByAgentWidget {...props} />);
  const rows = screen.getAllByRole("listitem");
  expect(within(rows[0]!).getByText("400.00K")).toBeInTheDocument();
  expect(within(rows[1]!).getByText("100.00K")).toBeInTheDocument();
  cleanup();

  render(<LatestSessionsWidget {...props} />);
  expect(screen.getByRole("button", { name: /Fix it/ })).toHaveTextContent("100.00K");
  cleanup();

  render(<UsageTotalsWidget {...props} />);
  expect(screen.getByText("Tokens").nextElementSibling).toHaveTextContent("500.00K");
});
