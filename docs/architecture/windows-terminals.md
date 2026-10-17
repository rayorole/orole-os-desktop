# Terminals on Windows (ConPTY)

Source: `internal/terminals` (`terminals.go` manager, `backend_windows.go` ConPTY backend, tests in
`terminals_windows_test.go`). Library: `github.com/UserExistsError/conpty`.

## Model

The daemon's `terminals.Manager` owns every session; the UI is a view.

- `Backend` abstracts the platform (`Start(StartSpec)`, `Shells()`). Windows uses ConPTY;
  Linux uses a native PTY with opt-in [tmux persistence](linux-terminals.md). This does not
  make the entire desktop or credential stack portable to Linux/macOS.
- Shells detected on `PATH`/known locations: Windows PowerShell (`powershell`), PowerShell 7 (`pwsh`),
  Command Prompt (`cmd`), Git Bash (`git-bash`, from `%ProgramFiles%\Git`).
- Limits: 64 *running* sessions per manager; per-session scrollback of 1 MiB (`ScrollbackBytes`), a fixed
  ring of 64 KiB pages that is reused once full, so reading a flooding PTY allocates nothing.
- Exited sessions do not count against the 64. They stay attachable (final screen, exit code) for 30 minutes
  and at most 16 are kept; older ones are forgotten and the daemon publishes `terminal.removed` for them.
- Output is coalesced per session: the first bytes after an idle gap go out immediately (keystroke echo is
  not delayed), then at most one `terminal.output` event every ~12 ms, each at most 64 KiB, and at most
  ~2 MiB/s of event payload. `seq` is the stream offset (total bytes) at the end of the event, so an event
  covers `[seq-len(data), seq)`.
- Flood policy: a terminal cannot close the GUI's IPC connection. When more than 256 KiB is waiting to be
  delivered the oldest undelivered bytes are skipped (they stay in the scrollback), which a client sees as an
  event starting past the end of the previous one. The terminal view then calls `terminals.attach` again
  and resumes from the returned `seq`.
- `terminals.attach` returns the scrollback plus the current `seq`; live `terminal.output` events with
  `seq <=` that value are already included, so a reattaching window neither loses nor duplicates output.
  The session lock is held only to snapshot page headers; the ~1 MiB copy happens outside it.
- In-process consumers (agents) use `Manager.Wait(ctx, id)` to block until a session exits instead of
  polling `List`.
- `Close` kills the shell process and releases the pseudoconsole exactly once (`winProc.once`): closing a
  pseudoconsole twice corrupts the heap.
- `init()` calls `SetConsoleCtrlHandler(NULL, FALSE)` so Ctrl-C (`0x03`) works even if a parent started the
  daemon with Ctrl-C ignored (children inherit that flag).
- IPC methods: `terminals.shells|list|create|write|resize|attach|close`. `owner` and explicit `command` are
  stripped from UI requests; only trusted daemon code (including agents) may set them.

## Honest persistence

| Event | Terminals |
|---|---|
| Window closed / reopened / app crash | **Survive** (sessions live in the daemon; reattach with scrollback) |
| Daemon stopped (`orole daemon stop`, crash, log off) | **End** |
| Reboot | **End** (nothing is restored; there is no tmux equivalent on Windows) |

The UI and docs must not imply otherwise. Linux's optional tmux server can outlive a daemon,
but not a reboot; Windows has no equivalent persistent terminal mode.

## Measured echo latency

See `docs/performance.md` (ConPTY keystroke echo, inside the daemon, excluding IPC and xterm.js rendering).

## Known limits

- Scrollback is memory only, 1 MiB per session; it is not persisted to disk.
- Resizing and full-screen TUIs depend on ConPTY behaviour of the installed Windows build.

## Agent runtime and review

`internal/agents` owns coding-agent processes, sessions, permissions and transcripts in the daemon.
Migration `0020_agents.sql` adds `agent_sessions`, `agent_events`, `agent_journal` and `agent_runs`.
`internal/agents/orchestrate` implements send prompt, hand-off, review-with, loop-until and parallel
worktree attempts. The desktop's bound `Agents` service forwards requests; it owns no durable state.
Agent Dashboard and Review subscribe to the same persisted sessions through `stores/agents.ts`.

### Token usage and spend

Two different numbers come out of the harnesses and are never mixed. `Session.usage` (`EvUsage`) is the
latest context reading for the composer: occupancy, not billing. Billed tokens arrive as `EvTokens`
(`agents.TokenUsage`), which the manager folds into the `agent_usage` ledger (migration 0104: session ×
provider × model × 15-minute UTC bucket) through the same batched writer as events; they are neither recorded as history
nor published. Exactly one summable source per harness, so nothing is counted twice:

| Harness | Billed source | Notes |
|---|---|---|
| Claude Code, structured | growth of `result.modelUsage` per model since the previous result | `modelUsage` and `total_cost_usd` are cumulative for the Claude Code process; per-model `costUSD` growth is the reported cost. A `--resume`d process restores the session's cumulative `modelUsage` (seen with Claude Code 2.1.289), so its first result bills the per-turn `result.usage` (no reported cost: priced from the catalogue) and only sets the `modelUsage` baseline. Also falls back to `result.usage` when `modelUsage` has no token counts. Assistant-message usage is a context reading and never summed. `output_tokens_details.thinking_tokens` is the reasoning share |
| Codex | `thread/tokenUsage/updated` `last` (one model request) | billed once per change of the thread's cumulative `total`, since Codex repeats the notification unchanged on rate-limit updates; the first report of a process outside a turn only sets the baseline (a resumed thread restating its history). `cachedInputTokens` is a subset of `inputTokens` and is split out as cache reads; `reasoningOutputTokens` is the reasoning share. No reported cost |
| Oh My Pi | `message_end` usage of each assistant message | pi-ai reports uncached `input`, `output`, `cacheRead`, `cacheWrite`, `reasoningTokens` and `cost.total` with the message's `provider`/`model`. A cost of 0 means pi-ai could not price the model and is treated as no report. `get_state` context usage is occupancy only |

Terminal and custom-command sessions report no usage. `Session.toolCalls` counts tool calls (a call
once, by tool id within the turn; a result whose call never arrived counts the call; Claude terminal
`PreToolUse` hooks count too) and `failedTools` the results with status `error`; both travel in
`agents.delta`.

`Session.tokens` is the session's headline count, input + cache writes + output (`TokenTotals.work`), and
travels in `agents.delta`. Cache reads are left out of every headline: they are the context re-sent on each
request, often over 90% of the total, and would make the count look far larger than the work done.
`Session.billed` (`TokenTotals`, also in `agents.delta`) holds the billed breakdown (input, cache writes,
cache reads, output, reasoning, cost, unpriced models), which the meters show on hover. Each `EvTokens`
adds to both; `recordTokens` prices it like `agents.usage` does (reported cost, else catalogue price) and
replaces `billed` rather than updating it in place. Claude Code bills a turn only when it ends, so while a
turn runs its decoder also emits `EvTokenEstimate` (`agents.TokenEstimate`: request id and that request's
input + cache write + output so far, from assistant-message usage, subagents included): the largest
reading per request is added to `Session.tokens`, never to `billed`, and the turn's billing replaces the
estimates. Estimates never reach the ledger. On start, `Recover` fills `billed` and `tokens` from their
`agent_usage` rows for sessions saved before `billed` existed (whose `tokens` still counted cache reads).

`agents.usage` (`Agents.usage(query)`) answers 24-hour and 7-day windows from the quarter-hour buckets (a
bucket counts when it starts inside the window's first quarter hour or later), all time, per agent kind for
the requested range, 365 local days (a bucket belongs to the local day it starts in; quarter hours make that
exact in every zone, including +05:30 and +05:45) and the most
recently updated sessions. Sessions are counted by `started_at`. Cost is computed at query time, so a
price refresh reprices history: a bucket whose every report carried the harness's cost uses that cost,
otherwise the `internal/pricing` catalogue price; models it cannot price are listed in `unpriced`
(`provider/model`, or `kind/model` without a provider) and add nothing, so the total is a lower bound.
Each `TokenTotals` carries `total` (cache reads included) and `work` (cache reads excluded); the daily
series, the per-agent order and every widget headline use `work`, and the widgets show the breakdown on hover.

`internal/pricing` holds a trimmed models.dev snapshot (`snapshot.json`, regenerated with
`go generate ./internal/pricing`) of list prices per million tokens. The daemon loads its cached download
from `<data dir>/pricing/models-dev.json` when newer, and a minute after start checks hourly whether to
download `https://models.dev/api.json` again: at most once per 24 hours, attempts included across restarts
(`pricing/last-attempt`), 60 s timeout; any failure keeps the catalogue it has. Lookup lowercases the model,
drops `[1m]`-style suffixes, release dates (`-20250514`, `@20250514`) and provider prefixes, and treats
`4.1` and `4-1` alike; the provider comes from the harness (Claude Code → anthropic, Codex → openai, Oh My Pi →
the message's provider). With no provider, a kept provider, an alias (`openai-codex`, `google-gemini-cli`...)
or a hosted gateway (Vercel, Cloudflare AI Gateway, Requesty, ZenMux) that has no match, every kept provider
is searched; any other provider (LM Studio, Ollama, a custom proxy) stays unpriced rather than borrowing a
cloud price. Cache prices: a model with
neither `cache_read` nor `cache_write` bills both cache buckets at its input price; one listed field leaves
the other unbilled. Tiered and long-context surcharges are ignored. Prices are list prices, so subscription
users see the API-equivalent spend.

### Adapter capabilities

| Adapter | Launch and status | Resume / approval |
|---|---|---|
| Claude Code, structured | `claude -p --output-format stream-json --input-format stream-json --verbose --permission-prompt-tool stdio`; prompts and replies use stdin/stdout JSON, not terminal parsing. Per-session `--mcp-config` JSON registers the central `orole` gateway regardless of selected model; `--append-system-prompt-file` adds the enabled-skills index without putting JSON or large/multiline prompts on the command line. Native harness configuration is not modified. Completed thinking becomes reasoning; tool calls/results carry linked IDs, names, MCP server, status, input and bounded output | Saves CLI session ID; resumes with `--resume` and refreshed gateway/skills files. `--system-prompt-snapshot off` ensures current central skills replace the original prompt snapshot on resume. `can_use_tool` requests become Needs you; daemon validates the pending request, sends allow/deny and audits the decision |
| Claude Code, terminal | Interactive ConPTY with a per-session `--settings` file; SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Notification, Stop and SessionEnd hooks invoke `orole-daemon agent-hook` over authenticated IPC | Saves CLI session ID; resumes with `--resume`. Permission notifications say Needs you, but the user answers in the terminal, not a fabricated dashboard permission request |
| Oh My Pi (`omp`), structured | `omp --mode rpc --cwd <dir> --session-dir <session>/omp/sessions --approval-mode <mode> [--model m] [--thinking level]`; newline-delimited JSON on stdio (ready frame, protocol v2 `rpc_chunk` framing, `set_event_filter` delta projection). Completed thinking blocks, assistant text, tool calls/results and generated images become transcript items; `agent_end` closes the turn. A prompt sent mid-turn is queued as steering (`prompt` with `streamingBehavior: "steer"`). Model list comes from `omp models --json` (only models the user has credentials for), reasoning levels are `off`…`max` | Saves omp's session id; resumes with `--resume <id> --session-dir`. omp defaults to never asking, so Orole passes `--approval-mode`: Manual and `plan` → `always-ask`, Auto → `write`, YOLO → `yolo` (`plan` also narrows tools to `read,glob,grep,web_search,todo`; omp's `write` tool stays, behind approval). Approval `select` (Approve/Deny) and `confirm` dialogs become Needs you and are answered with `extension_ui_response`; any other dialog (free-form questions) is cancelled and noted in the transcript. The central `orole` MCP gateway and skills prompt are injected per session, never into the user's omp config or the repo: a generated extension package in the session directory (`package.json`, no-op `index.js`, `.mcp.json`) is loaded with `-e`, and `--append-system-prompt <file>` carries the skills index. Stop sends `abort`, closes stdin (omp drains and exits 0) and falls back to the job-object kill |
| Codex, structured (default) | `codex app-server` (JSON-RPC lines on stdio): `initialize`, then `thread/start` (`cwd`, `model`, `approvalPolicy`, `sandbox`, `developerInstructions` = enabled-skills index, and a per-thread `config` override that defines the stdio MCP server `orole` = the Orole gateway with `default_tools_approval_mode = "approve"`; `~/.codex/config.toml` is never read for this or modified) and `turn/start` per prompt (`effort` = chosen reasoning level, `summary: auto`). A prompt sent mid-turn is `turn/steer`; Stop sends `turn/interrupt`, closes stdin and kills the job. Completed items become transcript events: reasoning (summary + content), agent messages, `commandExecution` → tool `shell`, `fileChange` → tool `apply_patch` (changed paths and truncated diffs), `mcpToolCall` (server + tool), web search, and `imageGeneration` → an image event whose base64 result is validated by magic bytes and saved to `<data>/agents/<id>/images/<item id>.<ext>`. Streamed text only updates the activity line (throttled). Permission mode maps to Codex policy: Manual = `untrusted` + `workspace-write`, Auto = `on-request` + `workspace-write` + `approvalsReviewer: auto_review`, YOLO = `never` + `danger-full-access`, plan = `on-request` + `read-only`. Model list and per-model reasoning levels come from `model/list` | Saves the Codex thread id; resumes with `thread/resume` (skills/gateway config is re-sent). Approvals are server requests: command, file-change (paths and diff shown from the started item; declined if Codex sent none), permissions, legacy `execCommandApproval`/`applyPatchApproval`, and MCP tool-call approvals (`mcpServer/elicitation/request` forms without fields) become Needs you with the exact request JSON; deny = `decline`. Forms that need typed input, URL elicitations and `item/tool/requestUserInput` questions are declined with a visible system note because the dashboard cannot collect answers yet |
| Codex, terminal | `codex [-m model] [prompt]` in ConPTY (opt-in second mode of the same kind) | Process running/exited only; approvals in terminal; no inferred CLI session ID |
| OpenCode | `opencode [--model model] [--prompt prompt]` in ConPTY | Process running/exited only; approvals in terminal |
| Gemini CLI | `gemini [-m model] [-i prompt]` in ConPTY | Process running/exited only; approvals in terminal |
| Custom CLI | User command via `cmd.exe` in ConPTY; `{prompt_file}` expands to a file containing the initial task | Process running/exited only; no automatic resume protocol. Include the placeholder if the command needs the initial task |

Terminal-mode sessions use `terminals.Manager` with owner `agent:<session-id>` and are inspectable in
the session's Terminal tab. Structured sessions deliberately have no terminal: their readable transcript
comes from the protocol. Terminal output is never regex-parsed to invent agent status. Long/multiline
initial terminal prompts use a task file; subsequent terminal replies are sent as one input line.
Detection reports missing binaries, version-probe failures and these capability differences honestly.
Only Claude's structured protocol/hook turns support loop-until's wait-for-turn behavior.

Each Windows agent gets a job object with `KILL_ON_JOB_CLOSE`. **Processes die with the daemon**, including
on a crash; GUI restarts do not affect them. A clean shutdown stops active work. After endpoint ownership
is established on the next start, persisted idle/working/needs-you sessions are marked **lost**, never
silently relaunched. Completed work stays done. Resume explicitly invokes the CLI session ID when
available; other adapters restart from the original task. The UI must not describe that as continuation
of a known OpenCode/Gemini/custom CLI conversation, or of a Codex terminal session (only structured Codex
sessions record a thread id). Image tasks (`images.start`) are structured Codex sessions in the `images` group
with a read-only sandbox in `<data>/images-workdir`; `agents.artifact` serves only image files inside the
session's own `<data>/agents/<id>` directory (symlinks resolved, traversal rejected) and downsizes images over
3 MiB to a JPEG preview because an IPC frame is 4 MiB.

### Durable orchestration and review

- Start requests insert a session's unique idempotency key before creating a worktree or process.
  Journaled operations insert pending before dispatch, then store their result. Pending rows from a
  previous boot become interrupted: an ambiguous outcome is not retried automatically. This is
  **at-most-once dispatch**, not exactly-once completion. Each parallel attempt has its own key/worktree.
- Loop-until runs user-selected checks under a bounded process-tree lifetime, passes failed output
  as explicitly untrusted data to the agent, waits for the next turn and respects the maximum rounds
  and cancellation. A daemon restart marks running orchestration interrupted.
- Automation provenance is persisted on sessions and propagated through `agent.finished` bus events,
  preserving cycle detection across agent turns and restart.
- Review compares against the starting commit, including untracked files, using a temporary index.
  It does not stage changes in the user's real index. Approve records a decision, **not a merge**;
  Send back delivers feedback; Discard removes only a worktree created for that session. Ordinary
  project-directory changes are not reverted by Discard. Run check records actual exit/output state.
  Worktree removal excludes the manager's manual/background Git readers, so an automatic
  diff cannot retain a Windows directory handle while Discard removes the worktree.
- Session transcripts are backed by bounded event history; CLI transcript paths are retained when
  provided by hooks. Structured permissions are transient: there is no live request to approve after
  the daemon process that owned it dies.
- Before honoring a structured Allow, the daemon evaluates central policy for `agent.tool.use`
  with actor `agent:<id>` and resource equal to the tool name. A Deny or policy lookup failure
  sends Deny to the CLI and is audited; the existing agent approval UI satisfies Ask without
  a second prompt. This covers CLI-relayed requests, not tools Claude's own rules already allow.
- Process launch, restart, prompts, check commands (including orchestration loops) and destructive
  worktree discard call the manager's required authorizer. The desktop daemon routes this through
  central policy with destructive effect: an explicit approval is required, stored denials are
  enforced, and a missing authorizer fails closed. Automation provenance selects the requesting
  automation actor; it is not authorization. Stopping an agent remains available without approval.

### Task titles

A task's title is chosen in layers (`internal/agents/titles.go`); a title the user gave (`StartSpec.Title`) or
changed later is never replaced, and the tracking of which titles are still automatic is in memory only (a
daemon restart leaves every title as it is).

1. **Heuristic.** The first line of the prompt (80 characters) is the title at once, offline.
2. **Harness-native.** Oh My Pi's `session_info_update` and `setTitle`, and Codex's `thread/name/updated`, arrive as
   an `EvTitle` event. The manager sanitizes it (one line, control characters stripped, at most 80 characters, empty
   or `null` ignored), renames the session while its title is still automatic and publishes `agents.updated`. It is
   not recorded in the transcript. After a native title, layer 3 stays quiet. Claude Code emits no title.
3. **One short request through the task's own account.** For Claude and Codex tasks that still carry the heuristic
   title, up to the first three user prompts may trigger one request (in the manager's background work, so it never
   delays `Start` or fails a task; 15 s timeout; cancelled by `Shutdown`) through the *same vendor's* account provider
   from `internal/llm/cli`: Claude's `haiku` alias, or Codex's smallest `nano`/`mini` model with minimal/low reasoning
   when its catalogue lists one. Another vendor is never used: with no installed, signed-in account, no listed small
   model, a different provider id, or for Oh My Pi (which names its own tasks) and custom tasks, nothing is sent. The
   first 1500 bytes of the prompt, with image markers removed, are sent as data inside `<task>` tags of a system
   prompt that asks for `<title>…</title>` (`<title/>` for none) and tells the model not to follow the text.
   Greetings, acknowledgements, bare numbers and emoji, and very short prompts are skipped and a later prompt is
   tried. Only a single-line `<title>` of at most 80 characters is accepted. Each request is audited as
   `agent.title` (vendor and model, no prompt text). A branch created for a new worktree keeps the heuristic name.

Settings > System > Tasks > "Name tasks with AI" stores `aiTitlesDisabled` in the `agents` setting (the
object shared with `yoloDisabled`; default on, an unreadable value fails closed). It disables only layer 3.

### Task branches and publishing

A task started with a new worktree gets branch `orole/<slug of the title, else of the prompt's first
line>`; when that name is taken a six-character suffix from the session id makes it unique (an
explicit branch is used as given and conflicts stay errors). `Session.BaseRef` is the commit the
worktree was created at and the worktree/branch are recorded before the process launches, so a crash
cannot orphan an unrecorded worktree.

`Agents.Publish(id, PublishSpec)` (`agents.publish`) turns the work into a commit, a pushed branch
and optionally a pull request, in the session directory, through `internal/git`:

1. **commit** — `git add --all` and commit with the user's configured `user.name`/`user.email`
   (an unconfigured identity fails with a message saying how to set it; nothing is staged). The message
   is `spec.message`, else the task title. A clean tree skips the step. Orole's Git wrapper disables
   `core.hooksPath`, so repository hooks do not run; `--no-verify` is never used or needed.
2. **push** — the checked-out branch to `origin` with upstream set, never forced. A detached HEAD, a
   missing `origin`, `main`/`master`/`develop`/`trunk`/... and the repository's default branch
   (`refs/remotes/origin/HEAD`) are refused before anything is committed.
3. **pull request** — against `spec.base`, else the remote branch the session's `BaseRef` sits on
   (the default branch when several do), else the default branch from `origin/HEAD` or the GitHub API.
   Title defaults to the task title, body to the task plus the last result. An existing PR (recorded
   on the session, or found by head branch after a lost response) is reused, so re-running publishes
   only new commits. `prUrl`/`prNumber` are stored in the session and in the `agent_sessions.pr_url`/
   `pr_number` columns (migration `0095`).

The first failing step stops the run and is named (`publish: push: ...`); earlier steps are not
rolled back and a re-run continues from them. Push and PR require `spec.confirmed` (the dialog's
explicit confirmation) and pass the GitHub gates described in `github.md` before the commit is made.
Each step appends a system event and an audit record, and the session is published as `agents.updated`.
Publishing is refused for remote-machine sessions, for sessions whose agent is mid-turn, and when
invoked by an automation.

### Permission modes

A task runs in one of three Orole modes, chosen at launch and changeable mid-task. The stored value is
`manual`, `auto` or `yolo`; `plan` is a fourth, read-only mode the daemon uses for reviewer and image
sessions and the UI never offers. Each adapter maps the mode onto its harness's own approval controls
(inspected on Claude Code 2.1.289, Codex CLI 0.156.1 and Oh My Pi 18.9.0):

| Mode | Claude Code | Codex | Oh My Pi |
|---|---|---|---|
| Manual (ask before anything not known safe) | `--permission-mode default` (always passed: the CLI's built-in default can be `auto`) | `untrusted` + `workspace-write` | `--approval-mode always-ask` |
| Auto (the AI decides which actions are safe) | `--permission-mode auto`, Claude Code's own classifier, when `claude --help` lists it; otherwise `acceptEdits` | `on-request` + `workspace-write` + `approvalsReviewer: auto_review` (Codex's auto-review subagent) | **Approximated**: omp has no AI reviewer, so `--approval-mode write` (file edits run, commands still ask) |
| YOLO (never ask) | `bypassPermissions` | `never` + `danger-full-access` | `--approval-mode yolo` |

`Detection.permissionModes` and `HarnessOption.permissionModes` report, per harness, whether each mode is
available, whether it is `approximated` and a one-line `detail`; the UI shows that text instead of claiming
more than the harness does. Claude Code starts in Manual when auto mode is unavailable to the session, so
the adapter compares the mode in its `init` message with the requested one and records a system note on a
mismatch; Codex's reply to `thread/start` is checked the same way for the reviewer.

**YOLO is enforced in Go.** Starting in or switching to YOLO is rejected unless the request carries
`confirmYolo: true`, the `agents` daemon setting (`{"yoloDisabled": true}`; Settings > Task
permissions) has not turned it off, and the caller is not an automation. Rejections and grants are
audited as `agent.yolo`. A disabled setting also marks YOLO unavailable in detection at once, and a
YOLO session cannot be resumed until its mode is changed. YOLO removes the harness's prompts only: Orole's
own MCP gateway still decides every `mcp.tool.call` through the policy engine. Remote machines never run
YOLO; remote Claude passes `--permission-mode default|auto|plan` explicitly and remote Codex `exec` always
runs in its workspace sandbox without prompts, so only Manual is offered there. Non-Claude terminal CLIs
keep their own approvals and reject Orole modes.

**Changing the mode mid-task** (`agents.setPermissionMode {id, mode, confirmYolo}`, audited as
`agent.permissionMode`, persisted on the session, recorded as a system event):

| Harness | How it applies |
|---|---|
| Claude Code | `set_permission_mode` control request, acknowledged by the CLI, effective immediately. YOLO needs the process to have been launched with `--allow-dangerously-skip-permissions` (passed for structured sessions while the YOLO setting is on); otherwise the conversation is resumed with the new mode |
| Codex | the next `turn/start` carries `approvalPolicy`, `approvalsReviewer` and `sandboxPolicy` overrides, which Codex keeps for later turns; a running turn finishes under the old policy |
| Oh My Pi | no live setter (`--approval-mode` is a launch flag), so the conversation is resumed with the new mode, only while idle with no pending request |
| Ended session | only recorded; the next launch uses it |
| Terminal, remote SSH, read-only review | refused, with the reason in `followup.permissionModeReason` |

Runtime IPC: `agents.detect/list/get/start/stop/restart/prompt/setPermissionMode/approve/events/transcript/remove/diff/review/check/hook/usage`,
`agents.publish`, `agents.handoff/reviewWith/loopUntil/tryN/runs/cancelRun`. Live topics: `agents.updated`,
`agents.delta`, `agents.event`, `agents.removed`, `agents.run`.

#### Persistence and live updates

- `agents.list` takes `{limit, offset}` (default 200, at most 500), newest first; the desktop `Agents.List`
  pages through it so no reply or query grows with the number of sessions.
- A live session's hot-path changes (assistant message, tool call/result, activity, usage, transcript path,
  diff counts) update memory under the manager lock and are written to SQLite outside it, debounced per
  session (250 ms). Every status change, permission/question, queue, review, exit and `Shutdown` writes
  immediately, so only display text of the last quarter second can be lost if the daemon is killed rather
  than stopped. Memory never runs ahead of a failed durable write: the change is rolled back.
- Hot-path changes publish `agents.delta` (`SessionDelta`: `id`, `updatedAt` and only the fields that changed).
  Everything else still publishes the whole `Session` as `agents.updated`. Clients merge a delta into the
  session they hold and ignore one for an unknown or newer session.
- Events are queued, get their id at once, and are inserted in batches (at most 100 ms later, 128 per flush);
  every read of events flushes first. History is pruned to 5000 events per session by a per-session counter.
- The post-turn and post-exit diff statistics use `git diff --numstat` (through the same isolated review
  repository as the diff view), coalesce per session and run at most two at a time.
- Headless agent protocol lines are bounded (16 MiB, Codex 64 MiB); an oversized line is dropped with an error event.

### Verification and installed CLI evidence

`go test -race ./internal/agents` exercises a compiled scripted fake CLI: working → needs-you →
approve/deny → done, stop/process death, CLI-session resume, crash/error handling, lost recovery,
idempotency, real ConPTY ownership/hooks, temporary-index diff, hand-off, parallel worktrees and
bounded/cancelled loops. Fixtures live under `internal/agents/testdata`; production never uses them.

On 2026-10-08, this machine reported Claude Code **2.1.289** and Codex **0.156.1**. Claude's local
`--help` confirmed the structured flags and hooks/resume settings. The structured smoke invocation
produced an init event and a real authentication error: **OAuth session expired and could not be
refreshed**. No successful Claude model turn or real Claude permission round-trip is claimed.
OpenCode/Gemini were not found on PATH. Their terminal launch adapters remain capability-limited
as shown above.

A separate authenticated named-pipe smoke launched installed Codex through the Custom CLI adapter
in a daemon-owned ConPTY. The model reviewed an inline Go `Add` function with incorrect subtraction,
returned `return a + b`, and the session reached `done`. This proves real model execution through
the daemon, not file editing or the interactive Codex adapter. An earlier read-only attempt to
read the fixture file was blocked by Codex execution policy; no policy bypass was used. Both runs
used an isolated development home. After adding manager-level execution authorization, a fresh
daemon smoke observed the pending `agent.start` policy request, approved only that isolated launch
over IPC, and again received the correct real model result. Denied/missing-authorizer regression
tests passed under the Go race detector without creating a session or executing a check command.

The installed MyGo/WebView2 Home composer was also exercised with the built-in
Codex adapter: selected the registered smoke project, local box, new worktree,
custom model `gpt-6-astra`, and `low` reasoning; approved the daemon's
`agent.start` request. The real Codex terminal identified `GPT-6-Astra low` and
returned `return a + b`. Home's Running agents tab opened that session's terminal.
An earlier harness-default model was rejected by the account; model selection
does not imply account entitlement, and the application does not invent a model
catalog. This smoke proves prompt execution, not agent file editing.

After the structured Codex cutover, the opt-in `OROLE_LIVE_CODEX=1` smoke exercised
`codex app-server` directly: real file editing and command execution with approvals,
stop and `thread/resume`, and image generation producing a PNG read through the
artifact API. OMP also completed a real task through the daemon's central MCP
gateway, reading a registry-only skill. These headless checks supersede the older
terminal-only evidence above; the new React task surface has not been visually
checked. Claude, Codex and OMP model choices now come from their installed harnesses,
with per-model reasoning levels; discovery is not a guarantee of account entitlement.

Claude discovery sends only a stream-json `control_request` with subtype `initialize`;
it never sends a user message or starts a billable model turn. The matching
`control_response.response.response.models` catalog supplies each selector `value`,
`displayName`, optional `resolvedModel`, and optional `supportedEffortLevels`.
Versioned display names are retained; unversioned labels include the authoritative
resolved model ID when supplied. Selectors remain unchanged (including aliases),
and models without reported effort levels have no reasoning choices. Disabled
catalog entries are excluded.

Discovery uses a temporary working directory and `CLAUDE_CONFIG_DIR`, safe mode,
disabled hooks/tools/MCP configuration and session persistence, and disabled
auto-updates/nonessential traffic. It does not read or modify the user's native
Claude configuration or need their OAuth credentials. The CLI's built-in catalog
is therefore an installed-CLI inventory, not a signed-in account entitlement list.
Results are cached per executable for five minutes; initialization has a 12-second
deadline and its process group and temporary directory are cleaned up afterward.
Older CLIs without this protocol or its isolation flags leave the catalog empty
rather than using a guessed version list. `go test ./internal/agents -run 'TestClaudeModel'`
covers the metadata mapping, initialization-only request, cache, isolation,
process cleanup, failed initialization and cancellation.
