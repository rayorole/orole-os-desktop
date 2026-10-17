# GitHub and issue-to-agent workflow

The daemon owns GitHub API access; `internal/app/github.go` and `github_agents.go`
are stateless typed bridges. React uses generated MyGo bindings. Authentication
uses the existing GitHub CLI login or a PAT stored in Windows Credential Manager;
tokens are not returned to the frontend or persisted in SQLite.

## Issues view

The sidebar's **Issues** view (`frontend/src/features/issues/`) is a read/comment/launch surface over the same
daemon-owned GitHub client; it adds no Go API. Scope is derived in the UI from registered projects: a project is
included when `internal/github.RepoFromRemote` (mirrored by `repoFromRemote`) accepts its `origin`, so only
github.com HTTPS/SSH remotes without ports or queries appear. "All projects" fans out one `github.issues`
call per repository (four at a time); one failing repository is reported in a banner and does not hide the rest.
Pull requests are filtered out.

Filters sit on one toolbar row: *Mine* sends `assignee=<login>`, *Unassigned* sends `assignee=none`, the *Labels*
menu sends `labels` (all selected labels must match; it lists up to 14 labels, selected and most-used first, and
keeps its order while open), state is open/closed/any. Free-text search matches title, repository, number,
author, assignees and labels **within the issues already loaded** (50 per repository, *Load more* extends it);
it does not call GitHub's search API, which is rate-limited separately.

List rows show the title, `repo#number`, last update, comment count and first assignee; labels and the author are
shown in the issue itself. Keyboard shortcuts have no on-screen hint bar: `/` search, `J`/`K` (or arrows) move,
`X` select, `S` start an agent, `G` open on GitHub. The issue header puts the title first, then state, number and
author; Start an agent is the primary action, Open on GitHub and Refresh are icon buttons beside the title.

The thread uses `Message`/`Bubble`. Comment text is untrusted Markdown rendered as React elements; `@login`
and `#123` references are linked, never executed. The composer completes `@` from the issue's participants and
`#` from the loaded issues of the same repository. Posting a comment still requires the confirmation dialog
and `confirmed: true`; typing a mention posts nothing.

"Start an agent" (`S`) opens the same reviewed launch as before, now as a side sheet: project and agent are
preselected when there is one sensible choice, the suggested prompt refers to the issue instead of quoting its
untrusted title, and the review checkbox plus the issue-version check still gate `github.issue.agent.start`.
Avatars are initials only; no remote avatar images are requested.

Multi-select: each row has a checkbox (shown on hover, focus, or once anything is selected; `X` toggles the open issue),
up to 10 issues across projects. The footer's "Start N agents" opens one sheet with a shared agent and prompt
(`{issue}` expands to `owner/repo#n`). Each issue is its own reviewed, idempotent `github.issue.agent.start` request
with its own key, saved to local storage before dispatch and checked against the version of the issue as listed
(an issue edited since is refused for that issue only). Launches run one after another; a failure does not stop the
rest, and "Retry" resends the same keys. Issues with a saved single-launch retry are not overwritten. Without a
matching registered local clone, an issue is skipped.

## Reviewed issue launch

Issue details expose the agent workflow. The user selects an installed built-in
agent and a registered local project whose current GitHub origin matches the
issue repository, reviews the source, and supplies an explicit task. Go checks
confirmation, live origin, installed harness, and the issue's update timestamp.
Issue text is fenced as untrusted reference material, not execution authority.
The agent manager separately authorizes launch through central policy.

The daemon creates an owned branch/worktree and persists the issue URL, exact
reviewed title/body/timestamp, project, user task, session ID, worktree, branch,
and base commit in `github_issue_runs` (migration 0091). A durable request key
prevents retries from starting a second session. Interrupted linkage can be
recovered from the agent session's idempotency key. Worktrees isolate Git changes,
not OS permissions. Agents survive GUI closure, not daemon termination.

APIs: `github.issue.agent.projects`, `github.issue.agent.start`,
`github.issue.agent.runs`, `github.issue.pr.prepare`, `github.issue.pr.publish`.
Existing agent detail/review controls inspect execution, diff and checks.

## Publication is separate from review

The user reviews and commits changes with existing Git tools and stops the agent.
Preparation requires a clean, committed, daemon-owned branch and a local base
matching GitHub's target base. It freezes the exact commit, base, diff, title and
body into a review token. Publication requires explicit confirmation, the same
reviewed snapshot, and both Go policy approvals: `github.branch.push` and
`github.pull.create`. Neither approval may be replaced by issue text or agent
output.

Publication pushes the exact approved SHA to canonical GitHub HTTPS using isolated
Git configuration, without force, hooks, tags or submodule recursion, then creates
a draft PR. It does not automatically commit or merge. Existing-PR lookup avoids
duplicate creation on retry. Unsupported index/filter/submodule configurations
fail closed. API authentication and Git push credentials are independent; a PAT
entered for API access does not configure Git's credential helper.

External editors and remote branch updates cannot be transactionally locked with
the local review. Exact commit pinning and base rechecks protect the reviewed
snapshot; already-dispatched remote effects cannot be rolled back by cancellation.

## Publishing an agent task (commit, push, pull request)

Separate from the reviewed issue flow above, any agent task can be published from its Review view:
`agents.publish {id, spec}` (`internal/agents/publish.go`, handler in `internal/daemon/svc_publish.go`).
It commits the session worktree with the user's git identity, pushes the session branch to `origin` and
opens a pull request (`internal/github` `OpenPull`, ready for review or draft per `spec.draft`), resolving
`owner/repo` from the configured `origin` URL (`github.RepoFromRemote`; only github.com https/ssh URLs).
See `windows-terminals.md` ("Task branches and publishing") for the step semantics and refusals.

The write gates are the same as above and are never bypassed. The agent runtime receives the daemon's
GitHub client, whose `Authorize` hook consults central policy (a stored Deny always wins) and requires
`ctx` confirmation, audited per action. `spec.confirmed` (set from the user's dialog) supplies the
confirmation. Before the first change `Publish` resolves `github.branch.push` and, when a pull request
has to be created, `github.pull.create`, so a denial leaves nothing committed; a push to a non-GitHub origin
or without a GitHub sign-in goes through the agent authorizer (`agent.publish.push`) instead. The method is
refused for automation callers. Unlike the issue flow it pins no reviewed snapshot: the user's review of the
session diff is the review, and the commit it publishes is whatever the worktree holds at that moment.
Git credentials for the push come from the user's Git configuration, independent of API sign-in.

## Verification

The Go race suite passed issue provenance/recovery, origin mismatch, cancellation,
confirmation, stale/dirty preparation, changed bases, denied publication, local Git
publication/config isolation, and GitHub HTTP publication behavior tests.

On 2026-10-08, an isolated real daemon used existing GitHub CLI authentication to
read repository issue #54. A temporary registered Git repository with the matching
origin was selected. Denying launch created no agent session; explicit approval
launched installed Codex in a daemon-owned worktree with recorded issue provenance.
The smoke stopped that process, retried the same request key, and observed the
same session and persisted source snapshot. An unconfirmed publication request
was rejected. Temporary smoke data was removed after shutdown.

This smoke did not request an external push or PR creation and does not claim a
successful model response for that issue. A separate installed Home composer
smoke received a real Codex response; see `windows-terminals.md`. Live publication
requires user-reviewed real changes, working Git credentials and explicit approval.

A separate executable smoke exercised the production `internal/github` client with
the existing `gh` login: authenticated repository, PR and notification reads;
unconfirmed issue creation rejected; confirmed creation of temporary issue #70;
confirmed body edit and exact readback; then confirmed closure of that smoke issue.
This supplies live create/edit evidence for #38 without publishing source changes.
