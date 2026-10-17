import { lazy, useSyncExternalStore, type ComponentType } from "react";
import { CalendarDays, ChartBar, CheckCircle2, Coins, Cpu, Hand, History, Layers, LayoutDashboard, LoaderCircle, NotebookPen, Rows3, Server, Sigma, Wallet, Workflow, Zap, type LucideIcon } from "lucide-react";
import type { WidgetSize } from "./frame";

export interface WidgetProps {
  widgetType: string;
  config?: Record<string, unknown>;
  onConfig: (patch: Record<string, unknown>) => void;
  onRemove: () => void;
}

/** A widget kind. Adding one to Home is one entry here plus its component. */
export interface WidgetDef {
  type: string;
  title: string;
  description: string;
  /** Column span a new instance starts with; people change it from the widget menu. */
  size: WidgetSize;
  /** New instances span two grid rows. */
  tall?: boolean;
  /** Shown in Customize. */
  icon?: LucideIcon;
  component: ComponentType<WidgetProps>;
}

/** Widget bodies load on demand so charts and the grid's data code stay out of the entry chunk. */
const builtins: WidgetDef[] = [
  { type: "overview", title: "Overview", description: "Tasks, pull requests and lines changed, a year of activity and your streaks.", size: "full", icon: LayoutDashboard, component: lazy(() => import("@/features/home/OverviewWidget").then((m) => ({ default: m.OverviewWidget }))) },
  { type: "needs-you", title: "Needs you", description: "Tasks waiting on a permission, question or reply, held actions and automation approvals.", size: "s", tall: true, icon: Hand, component: lazy(() => import("@/features/home/NeedsYouWidget").then((m) => ({ default: m.NeedsYouWidget }))) },
  { type: "usage-sessions", title: "Sessions", description: "Sessions started in the last 24 hours and the agents that ran them.", size: "s", icon: Layers, component: lazy(() => import("@/features/home/UsageWidgets").then((m) => ({ default: m.SessionsWidget }))) },
  { type: "usage-tokens", title: "Tokens", description: "Tokens used (cache reads excluded) in the last 24 hours, 7 days or all time, with a trend line.", size: "s", icon: Coins, component: lazy(() => import("@/features/home/UsageWidgets").then((m) => ({ default: m.TokensWidget }))) },
  { type: "usage-spend", title: "Spend", description: "What your usage costs at API list prices, with a trend line.", size: "s", icon: Wallet, component: lazy(() => import("@/features/home/UsageWidgets").then((m) => ({ default: m.SpendWidget }))) },
  { type: "usage-totals", title: "All time", description: "Tokens, spend, sessions and active days since usage tracking began.", size: "s", icon: Sigma, component: lazy(() => import("@/features/home/UsageWidgets").then((m) => ({ default: m.UsageTotalsWidget }))) },
  { type: "usage-by-agent", title: "By agent", description: "Tokens and spend per agent harness for a range.", size: "m", tall: true, icon: ChartBar, component: lazy(() => import("@/features/home/UsageWidgets").then((m) => ({ default: m.ByAgentWidget }))) },
  { type: "latest-sessions", title: "Latest sessions", description: "Recently active sessions with tool calls, failures and tokens.", size: "m", tall: true, icon: Rows3, component: lazy(() => import("@/features/home/UsageWidgets").then((m) => ({ default: m.LatestSessionsWidget }))) },
  { type: "recent-tasks", title: "Recent tasks", description: "Your latest agent tasks and where each one stands.", size: "l", icon: History, component: lazy(() => import("@/features/home/TaskWidgets").then((m) => ({ default: m.RecentTasksWidget }))) },
  { type: "running-now", title: "Running now", description: "Agents working at the moment and what they are doing.", size: "m", icon: LoaderCircle, component: lazy(() => import("@/features/home/TaskWidgets").then((m) => ({ default: m.RunningNowWidget }))) },
  { type: "quick-actions", title: "Quick actions", description: "Start a task or automation and jump to settings in one click.", size: "s", icon: Zap, component: lazy(() => import("@/features/home/QuickActionsWidget").then((m) => ({ default: m.QuickActionsWidget }))) },
  { type: "notes", title: "Notes", description: "Free-form notes saved on the dashboard.", size: "m", icon: NotebookPen, component: lazy(() => import("./builtin").then((m) => ({ default: m.NotesWidget }))) },
  { type: "setup", title: "Get started", description: "Remaining first-run setup steps.", size: "s", icon: CheckCircle2, component: lazy(() => import("./builtin").then((m) => ({ default: m.SetupWidget }))) },
  { type: "clock", title: "Clock & calendar", description: "Current time and month calendar.", size: "s", icon: CalendarDays, component: lazy(() => import("./builtin").then((m) => ({ default: m.ClockWidget }))) },
  { type: "daemon-health", title: "Daemon", description: "Local daemon status, version, uptime and memory.", size: "s", icon: Server, component: lazy(() => import("./builtin").then((m) => ({ default: m.DaemonHealthWidget }))) },
  { type: "activity", title: "System activity", description: "Host CPU, memory, disk and uptime, plus daemon heap history.", size: "m", tall: true, icon: Cpu, component: lazy(() => import("@/features/system/HostActivity").then((m) => ({ default: m.HostActivity }))) },
  { type: "automation-results", title: "Automation results", description: "Recent daemon-owned automation runs and their status.", size: "m", icon: Workflow, component: lazy(() => import("@/features/automations/AutomationResultsWidget").then((m) => ({ default: m.AutomationResultsWidget }))) },
];

const registry = new Map(builtins.map((widget) => [widget.type, widget]));
const listeners = new Set<() => void>();
let snapshot: readonly WidgetDef[] = builtins;
const getSnapshot = () => snapshot;
const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
};
const emit = () => {
  snapshot = [...registry.values()];
  listeners.forEach((listener) => listener());
};

/** Contributions share the built-in registry; cleanup only removes its own entries. */
export function registerWidgets(contributions: WidgetDef[]): () => void {
  const types = new Set<string>();
  for (const widget of contributions) {
    if (registry.has(widget.type) || types.has(widget.type)) throw new Error(`Widget type already registered: ${widget.type}`);
    types.add(widget.type);
  }
  for (const widget of contributions) registry.set(widget.type, widget);
  if (contributions.length) emit();
  return () => {
    let changed = false;
    for (const widget of contributions) {
      if (registry.get(widget.type) === widget) {
        registry.delete(widget.type);
        changed = true;
      }
    }
    if (changed) emit();
  };
}

export function useWidgetRegistry() {
  return useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
}
