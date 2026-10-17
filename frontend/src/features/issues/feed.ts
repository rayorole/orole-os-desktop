import { GitHub, type Issue, type Label } from "@/mygo";

export type IssueState = "open" | "closed" | "all";
export type Ownership = "all" | "mine" | "unassigned";
export interface FeedFilter { repos: string[]; state: IssueState; labels: string[]; ownership: Ownership; me: string; limit: number }
export interface FeedResult { issues: Issue[]; failures: { repo: string; message: string }[]; full: boolean }

const CONCURRENCY = 4;

/** Runs `task` over `items` with a small fixed fan-out so many projects never burst the GitHub API. */
async function pool<T, R>(items: T[], task: (item: T) => Promise<R>): Promise<PromiseSettledResult<R>[]> {
  const out: PromiseSettledResult<R>[] = new Array(items.length);
  let next = 0;
  await Promise.all(Array.from({ length: Math.min(CONCURRENCY, items.length) }, async () => {
    while (next < items.length) {
      const index = next++;
      out[index] = await task(items[index]!).then((value) => ({ status: "fulfilled", value }) as const, (reason: unknown) => ({ status: "rejected", reason }) as const);
    }
  }));
  return out;
}

const message = (reason: unknown) => (reason instanceof Error ? reason.message : String(reason)).replace(/^(bad_request|not_found|not_connected|denied|internal): /, "");

/** Issues from every repository in scope, newest activity first. Pull requests are not issues here. */
export async function loadIssues(filter: FeedFilter): Promise<FeedResult> {
  const assignee = filter.ownership === "unassigned" ? "none" : filter.ownership === "mine" ? filter.me : "";
  const settled = await pool(filter.repos, (repo) => GitHub.issues({ repo, state: filter.state, labels: filter.labels, assignee, creator: "", milestone: "", sort: "updated", limit: filter.limit }));
  const issues: Issue[] = [];
  const failures: FeedResult["failures"] = [];
  let full = false;
  settled.forEach((result, index) => {
    if (result.status === "rejected") { failures.push({ repo: filter.repos[index]!, message: message(result.reason) }); return; }
    if (result.value.length >= filter.limit) full = true;
    issues.push(...result.value.filter((issue) => !issue.pull_request));
  });
  issues.sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
  return { issues, failures, full };
}

/** Case-insensitive match on title, repo, number, author, assignees and labels; `#12` and `owner/repo#12` select by number. */
export function matchesQuery(issue: Issue, query: string): boolean {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  const haystack = [issue.title, issue.repo, `#${issue.number}`, `${issue.repo}#${issue.number}`, issue.user.login, ...issue.assignees.map((user) => user.login), ...issue.labels.map((label) => label.name)].join("\n").toLowerCase();
  return terms.every((term) => haystack.includes(term));
}

/** Labels for the Labels filter menu: the repositories' own labels first, then any seen on loaded issues, merged by name. */
export function mergeLabels(...groups: Label[][]): Label[] {
  const byName = new Map<string, Label>();
  for (const label of groups.flat()) if (!byName.has(label.name.toLowerCase())) byName.set(label.name.toLowerCase(), label);
  return [...byName.values()];
}

/** Most-used labels in the current results first so the menu favours what exists, then alphabetical; selected labels always lead. */
export function rankLabels(labels: Label[], issues: Issue[], selected: string[]): Label[] {
  const uses = new Map<string, number>();
  for (const issue of issues) for (const label of issue.labels) uses.set(label.name.toLowerCase(), (uses.get(label.name.toLowerCase()) ?? 0) + 1);
  const rank = (label: Label) => (selected.includes(label.name) ? Infinity : uses.get(label.name.toLowerCase()) ?? 0);
  return [...labels].sort((a, b) => rank(b) - rank(a) || a.name.localeCompare(b.name));
}
