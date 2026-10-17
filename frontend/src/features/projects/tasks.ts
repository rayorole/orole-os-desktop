import type { Project, Session } from "@/mygo";
import { columnOf, onDashboard } from "@/features/agents/columns";
import { useAgents } from "@/stores/agents";
import { useUI } from "@/stores/ui";

/** Windows paths compare case-insensitively and with either separator; POSIX paths stay exact. */
export function pathKey(path: string): string {
  const normal = path.trim().replace(/[\\/]+/g, "/").replace(/(.)\/$/, "$1");
  return /^[a-z]:/i.test(normal) || path.includes("\\") ? normal.toLowerCase() : normal;
}

const scope = (machine: string, path: string) => `${machine || "local"}\0${pathKey(path)}`;

/** Stable registry IDs own new tasks; older sessions fall back to their original folder. */
export function belongsToProject(session: Session, project: Project): boolean {
  if ((session.machine || "local") !== (project.machineId || "local")) return false;
  return session.projectId ? session.projectId === project.id : Boolean(project.path) && pathKey(session.project) === pathKey(project.path);
}

/**
 * Pinned tasks first, then newest created. Keyed on `startedAt` (set once at creation) with the id as tie-breaker, never on
 * `updatedAt`: that moves on every tool call, message, status change and token update, which made running tasks swap places.
 */
const newestFirst = (a: Session, b: Session) =>
  Number(!!b.pinned) - Number(!!a.pinned) || (Date.parse(b.startedAt) || 0) - (Date.parse(a.startedAt) || 0) || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0);

/** Dashboard-visible sessions of one project, newest first. */
export function projectTasks(sessions: Iterable<Session>, project: Project): Session[] {
  return [...sessions].filter((s) => onDashboard(s) && belongsToProject(s, project)).sort(newestFirst);
}

/** Groups dashboard-visible sessions by owning project id, newest first. Sessions of no project are dropped. */
export function groupTasksByProject(sessions: Iterable<Session>, projects: readonly Project[]): Map<string, Session[]> {
  const owner = new Map<string, string>();
  for (const p of projects) if (p.path) owner.set(scope(p.machineId, p.path), p.id);
  const byId = new Map(projects.map((p) => [p.id, p]));
  const out = new Map<string, Session[]>();
  for (const s of sessions) {
    if (!onDashboard(s)) continue;
    const explicit = s.projectId ? byId.get(s.projectId) : undefined;
    const id = s.projectId ? (explicit && belongsToProject(s, explicit) ? explicit.id : undefined) : owner.get(scope(s.machine, s.project));
    if (id === undefined) continue;
    const list = out.get(id);
    if (list) list.push(s); else out.set(id, [s]);
  }
  for (const list of out.values()) list.sort(newestFirst);
  return out;
}

/** Tasks that need the person: waiting for a decision, or failed. */
export const countNeedsYou = (tasks: readonly Session[]) => tasks.filter((s) => columnOf(s.status) === "needs-you").length;

/** Opens one task in the Tasks view. */
export function openTask(id: string) {
  useAgents.getState().setOpenId(id);
  useUI.getState().setView("agents");
}

/** Opens the Tasks dashboard filtered to a project. */
export function openProjectTasks(projectId: string) {
  useAgents.getState().setProjectFilter(projectId);
  useAgents.getState().setOpenId(null);
  useUI.getState().setView("agents");
}
