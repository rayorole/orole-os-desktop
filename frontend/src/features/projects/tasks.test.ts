import { describe, expect, it } from "vitest";
import type { Project, Session } from "@/mygo";
import { belongsToProject, countNeedsYou, groupTasksByProject, pathKey, projectTasks } from "./tasks";

const project = (over: Partial<Project>): Project => ({
  id: "p1", name: "alpha", machineId: "local", path: "C:\\code\\alpha", remoteUrl: "", lastOpened: 0, createdAt: 0,
  worktreeRoot: "", worktreeRootCustom: false, missing: false, isGit: true, ...over,
});
const session = (over: Partial<Session>): Session => ({
  id: "s", kind: "claude", machine: "", project: "C:\\code\\alpha", dir: "C:\\code\\alpha", status: "working", review: "",
  startedAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", ...over,
}) as Session;

describe("pathKey", () => {
  it("ignores separators, trailing slashes and drive-letter case on Windows paths", () => {
    expect(pathKey("C:\\code\\alpha\\")).toBe(pathKey("c:/code/alpha"));
  });
  it("keeps POSIX paths case-sensitive", () => {
    expect(pathKey("/home/Me/app")).not.toBe(pathKey("/home/me/app"));
    expect(pathKey("/")).toBe("/");
  });
});

describe("belongsToProject", () => {
  it("matches the project folder on the same machine only", () => {
    const p = project({});
    expect(belongsToProject(session({ project: "c:/code/alpha" }), p)).toBe(true);
    expect(belongsToProject(session({ machine: "local" }), p)).toBe(true);
    expect(belongsToProject(session({ machine: "box-2" }), p)).toBe(false);
    expect(belongsToProject(session({ project: "C:\\code\\alpha-two" }), p)).toBe(false);
  });
  it("matches folderless tasks by stable ID and never by an empty path", () => {
    const p = project({ path: "" });
    expect(belongsToProject(session({ project: "" }), p)).toBe(false);
    expect(belongsToProject(session({ projectId: p.id, project: "/managed/p1" }), p)).toBe(true);
    expect(belongsToProject(session({ projectId: p.id, machine: "remote" }), p)).toBe(false);
    expect(belongsToProject(session({ projectId: "other" }), project({}))).toBe(false);
  });
});

describe("groupTasksByProject", () => {
  const alpha = project({});
  const beta = project({ id: "p2", name: "beta", path: "C:\\code\\beta", isGit: false });
  const folderless = project({ id: "p3", name: "ideas", path: "" });

  it("groups by project folder, newest created first, including a worktree session by its project", () => {
    const sessions = [
      session({ id: "old", startedAt: "2026-01-01T00:00:00Z" }),
      session({ id: "wt", dir: "C:\\code\\.orole-worktrees\\alpha\\x", startedAt: "2026-01-03T00:00:00Z" }),
      session({ id: "b", project: "C:\\code\\beta", startedAt: "2026-01-02T00:00:00Z" }),
      session({ id: "mid", startedAt: "2026-01-02T00:00:00Z" }),
    ];
    const grouped = groupTasksByProject(sessions, [alpha, beta, folderless]);
    expect(grouped.get("p1")?.map((s) => s.id)).toEqual(["wt", "mid", "old"]);
    expect(grouped.get("p2")?.map((s) => s.id)).toEqual(["b"]);
    expect(grouped.has("p3")).toBe(false);
  });

  it("keeps the order when activity timestamps, statuses and input order change", () => {
    const base = [
      session({ id: "a", startedAt: "2026-01-03T00:00:00Z", status: "working" }),
      session({ id: "b", startedAt: "2026-01-02T00:00:00Z", status: "working" }),
      session({ id: "c", startedAt: "2026-01-01T00:00:00Z", status: "working" }),
    ];
    const order = (list: Session[]) => groupTasksByProject(list, [alpha]).get("p1")?.map((s) => s.id);
    const expected = ["a", "b", "c"];
    expect(order(base)).toEqual(expected);
    // The oldest task becomes the most recently active one, and statuses flip.
    const busy = base.map((s) => s.id === "c" ? { ...s, updatedAt: "2026-02-01T00:00:00Z", status: "needs-you" as const } : { ...s, status: "done" as const });
    expect(order(busy)).toEqual(expected);
    expect(order([...busy].reverse())).toEqual(expected);
    expect(projectTasks(busy, alpha).map((s) => s.id)).toEqual(expected);
  });

  it("breaks equal creation times by id and keeps pinned tasks first", () => {
    const t = "2026-01-01T00:00:00Z";
    const list = [session({ id: "z", startedAt: t }), session({ id: "m", startedAt: "2025-01-01T00:00:00Z", pinned: true }), session({ id: "a", startedAt: t })];
    expect(groupTasksByProject(list, [alpha]).get("p1")?.map((s) => s.id)).toEqual(["m", "a", "z"]);
    expect(groupTasksByProject([...list].reverse(), [alpha]).get("p1")?.map((s) => s.id)).toEqual(["m", "a", "z"]);
  });

  it("drops discarded sessions and sessions of no project", () => {
    const grouped = groupTasksByProject([
      session({ id: "gone", review: "discarded" }),
      session({ id: "stray", project: "D:\\elsewhere" }),
      session({ id: "ok" }),
    ], [alpha]);
    expect(grouped.get("p1")?.map((s) => s.id)).toEqual(["ok"]);
    expect(grouped.size).toBe(1);
  });

  it("groups managed tasks by ID without stealing removed projects' tasks through a matching path", () => {
    const sessions = [
      session({ id: "managed", projectId: folderless.id, project: "/managed/p3", dir: "/managed/p3" }),
      session({ id: "removed", projectId: "removed", project: alpha.path }),
      session({ id: "wrong-machine", projectId: folderless.id, machine: "remote" }),
    ];
    const grouped = groupTasksByProject(sessions, [alpha, folderless]);
    expect(grouped.get(folderless.id)?.map((s) => s.id)).toEqual(["managed"]);
    expect(grouped.has(alpha.id)).toBe(false);
    expect(projectTasks(sessions, folderless).map((s) => s.id)).toEqual(["managed"]);
  });

  it("agrees with projectTasks", () => {
    const sessions = [session({ id: "a" }), session({ id: "b", project: "C:\\code\\beta" })];
    expect(projectTasks(sessions, alpha).map((s) => s.id)).toEqual(["a"]);
    expect(groupTasksByProject(sessions, [alpha]).get("p1")?.map((s) => s.id)).toEqual(["a"]);
  });
});

describe("countNeedsYou", () => {
  it("counts needs-you and failed, not working or done", () => {
    expect(countNeedsYou([
      session({ status: "needs-you" }), session({ status: "failed" }), session({ status: "working" }), session({ status: "done" }),
    ])).toBe(2);
  });
});
