import { expect, test, type Page } from "@playwright/test";
import type { Comment, Issue, IssueAgentRun, IssueAgentStart, IssueQuery, IssueWrite, Label, Project, User } from "../src/mygo";

interface Recorded { method: string; args: unknown[] }

/** mygo-runtime calls globalThis.mygo directly. Fixtures and call recording exist only in this harness. */
async function installIssuesBridge(page: Page) {
  await page.addInitScript(() => {
    const now = Date.now();
    const iso = (hoursAgo: number) => new Date(now - hoursAgo * 3_600_000).toISOString();
    const user = (login: string): User => ({ login, id: login.length, avatar_url: "", html_url: `https://github.com/${login}`, type: "User" });
    const lab = (name: string, color: string): Label => ({ id: name.length * 7, name, color, description: "" });
    const L = { bug: lab("bug", "d73a4a"), infra: lab("infra", "5319e7"), repro: lab("needs repro", "fbca04"), docs: lab("docs", "0075ca"), search: lab("search", "1d76db") };
    const mkIssue = (repo: string, number: number, title: string, hours: number, labels: Label[], assignees: string[], pull = false): Issue => ({
      id: number * 10, number, title, body: `Body of ${title}. cc @tomas-r, see #${number - 1}.`, state: "open", state_reason: "", user: user("ana-ng"), labels, assignees: assignees.map(user), milestone: null,
      comments: 2, html_url: `https://github.com/${repo}/issues/${number}`, repository_url: `https://api.github.com/repos/${repo}`, created_at: iso(hours + 24), updated_at: iso(hours), closed_at: null,
      ...(pull ? { pull_request: { url: "x" } } : {}), repo,
    });
    const issues: Issue[] = [
      mkIssue("acme/shop", 18204, "Login loops after password reset", 9, [L.bug, L.repro], []),
      mkIssue("acme/shop", 18197, "Discount codes are ignored with a gift card", 26, [L.bug], ["me"]),
      mkIssue("acme/shop", 18190, "Product page takes 4s to become interactive", 50, [L.search], []),
      mkIssue("acme/shop", 18188, "Not an issue: refactor cart totals", 60, [L.docs], [], true),
      mkIssue("me/notes", 412, "Rotate the staging database credentials monthly", 72, [L.infra], []),
      mkIssue("me/notes", 409, "Seed script leaves orphaned memberships", 200, [L.bug], ["me"]),
    ];
    const project = (id: string, name: string, remoteUrl: string): Project => ({ id, name, machineId: "local", path: `C:\\code\\${name}`, remoteUrl, lastOpened: 1, createdAt: 0, worktreeRoot: "", worktreeRootCustom: false, missing: false, isGit: true });
    const projects = [project("p-shop", "shop", "https://github.com/acme/shop.git"), project("p-notes", "notes", "git@github.com:me/notes.git"), project("p-local", "scratch", "")];
    const comment = (id: number, login: string, body: string, hours: number, association: string): Comment => ({ id, user: user(login), body, created_at: iso(hours), updated_at: iso(hours), html_url: "", author_association: association });
    const comments = [comment(1, "tomas-r", "Can reproduce on a fresh local install.", 5, "COLLABORATOR"), comment(2, "me", "Looks like `invalidateSessions` only runs for Account settings.", 3, "OWNER")];
    const calls: Recorded[] = [];
    const g = globalThis as unknown as { __calls: Recorded[]; mygo: unknown };
    g.__calls = calls;
    const handlers: Record<string, (...args: never[]) => unknown> = {
      "Daemon.Health": () => ({ version: "0.1.0", memAllocMB: 24, startedAt: new Date(now).toISOString() }), "Settings.All": () => ({}),
      "Projects.List": () => projects, "Worktrees.List": () => [], "Notifications.List": () => ({ items: [], unread: 0, needsYou: 0 }),
      "Notifications.Prefs": () => ({ kinds: {} }), "Policy.Pending": () => [], "Plugins.List": () => [], "Desktop.Ready": () => ({ view: "", sequence: 0 }),
      "Updates.Check": () => ({ enabled: false, available: false, error: "" }), "Terminals.List": () => [], "Machines.List": () => [{ id: "local", name: "This computer", kind: "local", status: "online" }], "MCP.List": () => [],
      "Settings.Set": () => null, "Agents.List": () => [], "Agents.Runs": () => [],
      "Agents.Options": () => ({ machine: "local", harnesses: [], projects: [], directories: [], worktree: false, unavailableReason: "" }),
      "Agents.Detect": () => ["claude", "codex"].map((kind) => ({ kind, name: kind === "claude" ? "Claude Code" : "Codex", installed: true, path: kind, version: "1.0", error: "", modes: ["structured"] })),
      "GitHub.Status": () => ({ connected: true, source: "gh", user: user("me"), ghDetected: true, ghLogin: "me", patStored: false, rate: [] }),
      "GitHub.Labels": () => Object.values(L),
      "GitHub.Issues": (q: IssueQuery) => {
        calls.push({ method: "GitHub.Issues", args: [q] });
        return issues.filter((i) => i.repo === q.repo && i.state === "open" && q.labels.every((name) => i.labels.some((x) => x.name === name))
          && (q.assignee === "" || (q.assignee === "none" ? i.assignees.length === 0 : i.assignees.some((a) => a.login === q.assignee))));
      },
      "GitHub.Issue": (repo: string, number: number) => ({ issue: issues.find((i) => i.repo === repo && i.number === number), comments }),
      "GitHub.IssueAgentRuns": () => [],
      "GitHub.IssueAgentProjects": (repo: string) => projects.filter((p) => p.remoteUrl.includes(repo)),
      "GitHub.Comment": (repo: string, number: number, body: string, confirmed: boolean) => {
        calls.push({ method: "GitHub.Comment", args: [repo, number, body, confirmed] });
        const posted = comment(99, "me", body, 0, "OWNER");
        comments.push(posted);
        return posted;
      },
      "GitHub.UpdateIssue": (w: IssueWrite) => {
        calls.push({ method: "GitHub.UpdateIssue", args: [w] });
        const target = issues.find((i) => i.repo === w.repo && i.number === w.number)!;
        target.state = w.state ?? target.state;
        return target;
      },
      "GitHub.StartIssueAgent": (spec: IssueAgentStart): IssueAgentRun => {
        calls.push({ method: "GitHub.StartIssueAgent", args: [spec] });
        return { id: "run1", key: spec.key, repo: spec.repo, number: spec.number, issueUrl: "", issueTitle: "", issueBody: "", issueUpdatedAt: spec.issueUpdatedAt, projectId: spec.projectId, kind: spec.kind, task: spec.task, sessionId: "", worktree: "", branch: "issue-18204", baseRef: "", state: "starting", error: "", prUrl: "" };
      },
    };
    g.mygo = {
      call: async (method: string, ...args: unknown[]) => {
        const fn = handlers[method] as ((...a: unknown[]) => unknown) | undefined;
        if (!fn) throw new Error(`Harness method not provided: ${method}`);
        return structuredClone(fn(...args));
      },
      on: () => () => {}, once: () => () => {}, channel: () => ({}), window: {},
    };
  });
}

async function calls<T extends unknown[]>(page: Page, method: string): Promise<T[]> {
  return page.evaluate((name) => (globalThis as unknown as { __calls: Recorded[] }).__calls.filter((c) => c.method === name).map((c) => c.args), method) as Promise<T[]>;
}

async function openIssues(page: Page) {
  await installIssuesBridge(page);
  await page.goto("/");
  await page.getByRole("navigation", { name: "Applications" }).getByRole("button", { name: "Issues", exact: true }).click();
}

test("lists issues across projects, filters by ownership, label and project, and searches", async ({ page }, testInfo) => {
  await openIssues(page);
  const list = page.getByRole("region", { name: "Issue list" });
  // Five issues across both GitHub projects; the pull request and the project without a remote are not listed.
  await expect(list.getByRole("button")).toHaveCount(5);
  await expect(list.getByText("Not an issue: refactor cart totals")).toHaveCount(0);
  await page.screenshot({ path: testInfo.outputPath("issues-all.png") });

  await page.getByRole("button", { name: "Mine", exact: true }).click();
  await expect(list.getByRole("button")).toHaveCount(2);
  expect((await calls<[IssueQuery]>(page, "GitHub.Issues")).at(-1)![0]).toMatchObject({ assignee: "me" });
  await page.getByRole("button", { name: "Unassigned", exact: true }).click();
  await expect(list.getByRole("button")).toHaveCount(3);
  expect((await calls<[IssueQuery]>(page, "GitHub.Issues")).at(-1)![0]).toMatchObject({ assignee: "none" });
  await page.getByRole("button", { name: "All", exact: true }).click();
  await expect(list.getByRole("button")).toHaveCount(5);

  await page.getByRole("button", { name: "Labels", exact: true }).click();
  const labels = page.getByRole("group", { name: "Labels" });
  await labels.getByRole("menuitemcheckbox", { name: "bug" }).click();
  await expect(list.getByRole("button")).toHaveCount(3);
  expect((await calls<[IssueQuery]>(page, "GitHub.Issues")).at(-1)![0]).toMatchObject({ labels: ["bug"] });
  await expect(page.getByRole("button", { name: "Labels: bug", exact: true })).toBeVisible();
  await labels.getByRole("menuitemcheckbox", { name: "bug" }).click();
  await page.keyboard.press("Escape");
  await expect(labels).toHaveCount(0);

  await page.getByLabel("Search issues").fill("credentials");
  await expect(list.getByRole("button")).toHaveCount(1);
  await expect(list.getByRole("button")).toContainText("Rotate the staging database credentials");
  await page.getByLabel("Search issues").fill("");

  await page.getByRole("combobox", { name: "Project" }).click();
  await page.getByRole("option", { name: /shop/ }).click();
  await expect(list.getByRole("button")).toHaveCount(3);
  expect((await calls<[IssueQuery]>(page, "GitHub.Issues")).at(-1)![0]).toMatchObject({ repo: "acme/shop" });
});

test("reads an issue, completes @mentions and #issues, and posts a confirmed comment", async ({ page }, testInfo) => {
  await openIssues(page);
  const detail = page.getByRole("article", { name: "acme/shop#18204" });
  await expect(detail.getByRole("heading", { name: "Login loops after password reset" })).toBeVisible();
  // Body, two comments and the composer are Message rows; mentions in the body are links.
  await expect(detail.locator("[data-slot=message]")).toHaveCount(4);
  await expect(detail.getByRole("link", { name: "@tomas-r" })).toBeVisible();
  await expect(detail.getByRole("link", { name: "#18203" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("issue-detail.png") });

  const box = page.getByRole("combobox", { name: "New comment" });
  await box.click();
  await box.pressSequentially("Thanks @to");
  await expect(page.getByRole("option", { name: "@tomas-r" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("mention-popup.png") });
  await box.press("Enter");
  await expect(box).toHaveValue("Thanks @tomas-r ");
  await box.pressSequentially("fixed in #1819");
  // "#1819" matches the two newest issues; ArrowDown moves from #18197 to #18190 before accepting.
  await expect(page.getByRole("option")).toHaveText([/#18197/, /#18190/]);
  await box.press("ArrowDown");
  await box.press("Tab");
  await expect(box).toHaveValue("Thanks @tomas-r fixed in #18190 ");

  expect(await calls(page, "GitHub.Comment")).toHaveLength(0);
  await box.press("Control+Enter");
  await expect(page.getByRole("alertdialog")).toBeVisible();
  expect(await calls(page, "GitHub.Comment")).toHaveLength(0);
  await page.getByRole("button", { name: "Post comment" }).click();
  await expect(detail.locator("[data-slot=message]")).toHaveCount(5);
  expect(await calls(page, "GitHub.Comment")).toEqual([["acme/shop", 18204, "Thanks @tomas-r fixed in #18190 ", true]]);
});

test("starts an agent on the selected issue from the keyboard after reviewing the prompt", async ({ page }, testInfo) => {
  await openIssues(page);
  await expect(page.getByRole("article", { name: "acme/shop#18204" })).toBeVisible();
  await page.keyboard.press("j");
  await expect(page.getByRole("article", { name: "acme/shop#18197" })).toBeVisible();
  await page.keyboard.press("k");
  await page.keyboard.press("s");
  const sheet = page.getByRole("dialog");
  await expect(sheet.getByText("Start an agent on #18204")).toBeVisible();
  await expect(sheet.getByRole("radio", { name: "Claude Code" })).toBeChecked();
  await expect(sheet.getByLabel("First prompt")).toHaveValue(/Resolve acme\/shop#18204/);
  await page.screenshot({ path: testInfo.outputPath("start-agent.png") });
  const start = sheet.getByRole("button", { name: /Start agent/ });
  await expect(start).toBeDisabled();
  await sheet.getByRole("checkbox").check();
  await start.click();
  await expect(sheet).toHaveCount(0);
  const [[spec]] = await calls<[IssueAgentStart]>(page, "GitHub.StartIssueAgent");
  expect(spec).toMatchObject({ repo: "acme/shop", number: 18204, projectId: "p-shop", kind: "claude", confirmed: true });
  expect(spec!.key).not.toBe("");
});

test("selects several issues with the checkbox and X, then starts one agent per issue", async ({ page }, testInfo) => {
  await openIssues(page);
  const list = page.getByRole("region", { name: "Issue list" });
  await expect(page.getByRole("article", { name: "acme/shop#18204" })).toBeVisible();
  await page.keyboard.press("x");
  await list.getByRole("checkbox", { name: "Select me/notes#412" }).check();
  await expect(list.getByText("2 selected")).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("multi-select.png") });
  await list.getByRole("button", { name: "Start 2 agents" }).click();

  const sheet = page.getByRole("dialog");
  await expect(sheet.getByRole("radio", { name: "Claude Code" })).toBeChecked();
  const start = sheet.getByRole("button", { name: "Start 2 agents" });
  await expect(start).toBeDisabled();
  await sheet.getByRole("checkbox", { name: /I reviewed these issues/ }).check();
  await page.screenshot({ path: testInfo.outputPath("bulk-sheet.png") });
  await start.click();
  await expect(sheet).toHaveCount(0);
  await expect(list.getByText("selected")).toHaveCount(0);

  const specs = (await calls<[IssueAgentStart]>(page, "GitHub.StartIssueAgent")).map(([spec]) => spec);
  expect(specs.map((spec) => [spec.repo, spec.number, spec.projectId, spec.kind, spec.confirmed])).toEqual([
    ["acme/shop", 18204, "p-shop", "claude", true],
    ["me/notes", 412, "p-notes", "claude", true],
  ]);
  expect(specs[0]!.task).toContain("Resolve acme/shop#18204.");
  expect(new Set(specs.map((spec) => spec.key)).size).toBe(2);
});

test("closes the open issue only after confirmation and drops it from the open list", async ({ page }) => {
  await openIssues(page);
  const list = page.getByRole("region", { name: "Issue list" });
  const detail = page.getByRole("article", { name: "acme/shop#18204" });
  await expect(list.getByRole("button")).toHaveCount(5);
  await detail.getByRole("button", { name: "Close issue" }).click();
  await expect(page.getByRole("alertdialog")).toBeVisible();
  expect(await calls(page, "GitHub.UpdateIssue")).toHaveLength(0);
  await page.getByRole("alertdialog").getByRole("button", { name: "Cancel" }).click();
  expect(await calls(page, "GitHub.UpdateIssue")).toHaveLength(0);

  await detail.getByRole("button", { name: "Close issue" }).click();
  await page.getByRole("alertdialog").getByRole("button", { name: "Close issue" }).click();
  await expect(list.getByRole("button")).toHaveCount(4);
  await expect(list.getByText("Login loops after password reset")).toHaveCount(0);
  const [[write]] = await calls<[IssueWrite]>(page, "GitHub.UpdateIssue");
  expect(write).toMatchObject({ repo: "acme/shop", number: 18204, state: "closed", confirmed: true, setLabels: false, setAssignees: false });
});
