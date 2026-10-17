# Automations (#57–#59)

Automations are daemon-owned. The desktop `Automations` service forwards typed IPC calls; React keeps only editor/view state. SQLite migration 0060 stores versioned definitions, immutable per-run definition snapshots and a write-ahead step journal. Times exposed to the UI are Unix milliseconds. Definitions are not created automatically.

## Authoring and running

The Automations view is listed in the sidebar's navigation after Home, Tasks, Review and Issues, above the Agents (bots) and Code (projects) sections. Its title comes only from the shell title bar (`lib/nav.ts`); `features/automations/FlowList.tsx` adds a toolbar with the flow count, Refresh and New flow, not a second heading. Its list (one card, a row per flow) shows each flow's trigger icon and trigger in words, block count, validation issues, last result, next scheduled run and last watch check, then Run now, Delete and the enabled switch; pending approvals are approved or rejected at the top. With no flows, the page shows a New flow button and the daemon's templates as cards (the same drafts as the New flow menu). The builder (`features/automations/FlowBuilder.tsx`) is a trigger card plus a vertical block stack with condition lanes; blocks come from the daemon's `automations.stepTypes` catalog (grouped library, drag via `@dnd-kit` with pointer and keyboard sensors, or `+`), the inspector renders fields by kind (bot, project and harness pickers included) and shows save issues by path. Pure tree operations live in `flow.ts`. Run now works even for a disabled flow; disabling stops automatic triggers, not existing runs. The Runs tab shows the step timeline (hidden `:join` rows, the lane a condition took) with links to started tasks and bot conversations; cancellation is explicit. The dashboard's Recent automation results widget uses the same daemon data. Browser preview has no backend and reports this honestly.

Parameter and schema errors are returned as field paths (`steps.1.then.0.params.prompt`). Unavailable integration adapters appear disabled with a reason. Conditional `when` guards can skip individual steps. Templates open unsaved drafts: “Tell me when an agent needs me”, “Have a bot review finished tasks”, “Nightly code task”, “Notify me when CI fails” and “Morning GitHub issues”. Empty pickers (repository, project, bot, task prompt) must be filled in before the flow validates. The schedule editor previews the next five times using the daemon's local timezone. `automations.list` summaries carry the definition's validation issues and, for enabled schedules, the next fire time.

Bots can draft automations from chat (`propose_automation`, see `docs/architecture/chat.md`). A draft is an immutable `automation_proposals` row validated like a builder save; it becomes an automation only through `automations.proposals.resolve`, the user's decision on the chat card. Drafts with allowed executables or hosts, `command.run`, `test.run` or `http.request` (`automations.Risks`) can only be saved turned off. `automations.created_by` (migration 0105) records `user` or `bot:<id>`; later saves keep it, and the list shows "By <bot>". `automations.list` summaries carry `createdBy` and `updatedAt`.

### Flow schema v2

A definition is a tree. A `condition` step evaluates its expression and runs its `then` or `else` lane, then the flow continues after the condition. Lanes nest at most three deep; a flow has at most 50 blocks, lanes included; step IDs are unique across the whole tree. A condition cannot have a `when` guard and always stops the run when its expression fails. The runner executes `flatten(steps)`: each condition compiles to a forward `branch` that keeps the condition's step ID (its output is `{condition, next}`), and when both lanes have blocks a hidden `<id>:join` jump follows the `then` lane. Journals therefore show `branch` rows for conditions; the builder hides `:join` rows.

Stored schema v1 documents (flat steps with forward `branch` jumps) are upgraded when read (`Upgrade`): `agent.start/prompt/wait` become `task.start/prompt/wait` (`adapter`→`harness`, `agentId`→`taskId`, `until: finished`), and a jump whose `then` (or `else`) is the very next step becomes a condition whose lane runs up to the other target. Any other jump has no lane equivalent: it is kept, reported as an issue, and the flow cannot run until it is rebuilt. Run snapshots are upgraded the same way; a compiled condition keeps the step IDs an in-flight v1 journal recorded. Saving always stores v2.

### Blocks

| Type | Group | What it does |
| --- | --- | --- |
| `bot.tell` | Bots | Stores the message in the bot's conversation, authored `automation:<id>`, and queues a turn behind anything the bot is doing. The bot is told the message came from the named automation and that quoted event fields are data. With `wait`, the step returns `{messageId, status, reply}` once the bot answered and fails if the turn did not complete. |
| `task.start` | Tasks | Starts a code task in a registered project (`harness`, `prompt`, optional `title`, `model`, `newWorktree`) in `manual` or `auto` permission mode. The agent runtime still requires the shared policy's `agent.start` approval for `automation:<id>`; YOLO is refused. Output: the task fields of the event payload plus `result`, `files`, `additions`, `deletions`. Supports `strategy: key` through the agent journal. |
| `task.prompt` | Tasks | Sends a follow-up to a task through the orchestrator journal. |
| `task.wait` | Tasks | Waits until the task is done or ended (`until: finished`), or also until it needs you (`until: attention`). Ending failed, stopped or lost fails the step with the task state in the output. The run keeps its execution slot while waiting; raise the step and flow timeouts for long tasks. |
| `delay` | Logic | Waits 1–1440 minutes; its step timeout is raised to cover the wait. It keeps the execution slot. |
| `condition`, `approval` | Logic | Lanes (above) and the durable human approval. |
| `notify`, `http.request` (“Call webhook”) | Notify | Unchanged. |
| `github.query`, `mcp.tool` | Integrations | Unchanged; `mcp.tool` reaches connected integrations such as Vercel or Railway through `InvokeChecked`. |
| `command.run`, `test.run` | Run | Unchanged. |

Event payloads arrive under `event.data` (for example `{{ event.data.title }}`); `trigger.data` is the whole event (`topic`, `data`, `time`).

The CI template is a scheduled check every 15 minutes, not a CI push watcher. GitHub query results include `checks`, `failed`, `repo`, and `ref` for the checks kind.

## Triggers and provenance

- Manual: authenticated `automations.run` IPC.
- Schedule: five-field cron (`minute hour day month weekday`), lists/ranges/steps, month/weekday names, common aliases, or `every 15m`/`every 2h`/`every 1d`. Minimum interval one minute. Fixed wall times follow local DST, skipping nonexistent times and the second occurrence of repeated times. Restricted day-of-month and weekday use cron's OR semantics. After downtime/sleep, `skip` consumes missed slots without running; `run-once` fires once, never a backlog burst. Ninety seconds of lateness is scheduling grace.
- Application event: exact topic, `prefix.*`, or `*`, with optional filter. `internal/events` has bounded, nonblocking subscriber queues; a full queue drops the newest event and increments `Subscription.Dropped`. This bus is not a durable message broker. `automations.events` returns the topic catalog (`events.Catalog`) with each topic's payload fields; the builder offers only those.
- Webhook: opt-in loopback listener, described below.
- Watch: the daemon polls an integration, since Orole has no network listener. `trigger.watch` is `{source, kind, ...}`: GitHub `push` (commits of `branch`, default branch when empty), `pr_opened`, `pr_merged`, `workflow_failed` (optionally one branch) and `issue_opened` for `repo`, using the signed-in GitHub client with ETag caching; or `mcp` `deployment`, which calls a connected integration tool that lists deployments (`server`, `tool`, `args`; for example Vercel `list_deployments` or Railway `list-deployments`) through `InvokeChecked` as `automation:<id>`, finds the first JSON array of objects (structured content or JSON text, GraphQL `edges` included) and keys each by id (`uid`/`id`/`deploymentId`) plus state (`state`/`readyState`/`status`), so a state change is a new item; `states` limits which states run. Polls run one at a time every `intervalSec` (default 60 s GitHub, 120 s MCP, minimum 30), with exponential backoff up to 30 minutes after a failure; with no watch triggers the loop arms no timer. The first poll after a watch is created or changed records a baseline and runs nothing; afterwards each unseen item runs the flow once, oldest first, with `event = {topic: "<source>.<kind>", data, time}` (filters apply). `automation_watch_state` (migration 0103) keeps the watch configuration, the last 500 keys and the last error; the trigger key `watch|<id>|<item>` also deduplicates durably. `automations.list` reports each watch's last poll, error and seen count. Delivery is up to one interval late; instant delivery needs the user's own tunnel to the local webhook.

| Topic | When | Publisher |
| --- | --- | --- |
| `task.created` | A code task was launched (never for a retried idempotency key) | `agents.Config.Created` |
| `task.status` | A task's status changed (`from` holds the previous one) | agent runtime `agents.updated` |
| `agent.waiting` | A task needs a permission decision or an answer | agent runtime `agents.updated` |
| `agent.finished` | A task finished: its turn ended, no background agents remain and it settled (`result` holds its summary) | `agents.Config.Finished` via `taskEvents.finished` |
| `agent.failed` | A task or its process failed | agent runtime `agents.updated` |
| `pr.opened` | Publishing opened a pull request for the task | agent runtime `agents.updated` |
| `project.created` | A project was added or created | `projects.add` / `projects.create` |
| `bot.message` | A bot completed a reply (builder drafts excluded; spoken Jarvis call turns too, as they are not `chat.message` events, see `chat.md`). A reply to a message an automation sent (`bot.tell`), and replies in hand-offs and reports that follow from it, carry that run's chain | chat runtime `chat.message` |
| `automation.run.finished`, `github.changed`, `mcp.tool.called` | As named | engine, GitHub service, MCP manager |

Task topics share one payload (`id`, `title`, `kind`, `status`, `projectId`, `project`, `branch`, `activity`, `lastMessage`, `error`, `prUrl`); free text is clipped to 2000 characters and is untrusted data. Status transitions are relative to the last state this daemon run observed: a session from a previous run reports `agent.waiting`/`agent.failed` but no `task.status` until its next change.

Modules publish facts through `d.Events.Publish(events.Event{Topic,Data,Chain})`. Capture `events.ChainFrom(ctx)` when automation-driven work starts and persist it with work that outlives the call. The chain contains causing automation IDs; an ID cannot reappear, and the maximum chain length is three. Automation completion publishes `automation.run.finished`. Event filters access `event.topic` and `event.data`; templates also access `trigger.data`, `run.id`, and `steps.<id>.output`. Agent and integration payloads are untrusted data, not instructions.

## Steps and safety

Command/test steps require an executable allowlist and explicit absolute working directory. Executables resolve with `exec.LookPath`; actual file identity is compared against resolved allowlist entries. Batch scripts are rejected. Each templated argument remains one argv element; there is no shell interpolation. Only a small environment allowlist is inherited. Explicit extra environment values are inputs, not a secret storage mechanism. Exit zero means pass; nonzero records output and fails the step. `onError=continue` allows later steps to inspect failed output. Definition allowlists are additional restrictions, not authorization grants.

HTTP steps require explicit allowed hosts and HTTPS unless the definition opts into HTTP. Private, loopback, link-local and other nonpublic addresses are blocked after resolution, and the checked numeric IP is dialed directly. No proxy environment is used. Every redirect is rechecked; at most three are allowed, and supplied headers are stripped on redirect. Authorization/cookie credentials and URL userinfo are rejected. Responses default to 64 KiB, capped at 1 MiB. These requests intentionally cannot access local network services.

Immediately before dispatch, command/test and HTTP steps use the daemon's shared policy as `automation:<id>`. A missing authorizer fails closed. Arbitrary commands (including test runners) and non-read HTTP methods are classified destructive; GET/HEAD are read effects. Command approvals show the resolved executable, every rendered argv element, working directory and explicit environment. HTTP policy sees the concrete method and full target, including each redirect. Stored denies and cancellation prevent dispatch. Native steps do not resolve credential references: secret-bearing authority details are rejected rather than concealed in approvals; use integration credential identifiers instead. Details that cannot fit the shared policy display without truncation or sanitization changes fail closed.

MCP, agent and GitHub steps call injected interfaces. Their adapters retain the daemon's own permission checks. MCP has no durable idempotency-key support and cannot select key strategy. Agent adapters use the orchestrator journal. Notify steps record a Flow message notification through the notification service; turning Flow messages off produces none. The daemon also reports runs itself: an approval step is a Needs you entry until answered, a failed or uncertain run notifies, and a succeeded run notifies only when the user started it.

### HTTP request authorization

Before DNS resolution or connection establishment, HTTP authorization receives a detached snapshot of the concrete method, full URL, all explicit headers (including automation chain and idempotency headers), and exact rendered body. Headers are copied and the body is retained as an immutable string; authorization cannot mutate the live request. A body-only change to a recipient or operation therefore changes the disclosed approval detail even when the endpoint is unchanged. Permitted headers and body bytes are not removed to make an approval fit.

The entire snapshot passes through the same fail-closed secret and display-completeness checks as other native effects. Recognized secrets in headers or bodies, oversized detail, invisible display alterations, or invalid UTF-8 prevent dispatch rather than presenting a concealed, truncated, or replacement-character version. These checks are defense in depth, not detection of every possible opaque credential.

Redirect authorization describes the next actual request after HTTP method/body transformations and the existing redirect header-stripping policy. In particular, POST redirects rewritten to GET disclose an empty body; 307/308 redirects disclose the preserved body. Every redirected request must pass policy before its own DNS lookup or connection, and denial sends nothing to that destination. Host allowlists, pinned public-address dialing, redirect limits, and the prohibition on redirecting idempotency-key requests remain unchanged.

## Crash recovery, approvals and limits

Each step is journaled before invocation and after completion, with a stable SHA-256 `(run,step)` idempotency key. Finished steps are never repeated on restart. For side effects:

- `at-most-once` (default): a journal still marked running after interruption becomes `uncertain`; it is not retried. Inspect the external system before starting a fresh run.
- `key`: only supported executors may select it. The external endpoint/adapter must durably honor the supplied idempotency key; an HTTP header alone cannot force a remote server to deduplicate.
- `repeatable`: the user explicitly asserts repeats are safe. Retries use exponential backoff, capped at one minute, up to eight retries.

Native command/HTTP steps first journal `authorizing`, then persist `running` immediately before the first actual dispatch. Restarting during a pending shared-policy approval reauthorizes safely; once dispatch is journaled, existing at-most-once uncertainty rules apply. Shared-policy denial, missing authorization, and approval timeout are not automatically retried, even with `key` or `repeatable`. Shared-policy approvals are distinct from explicit durable `approval` steps: they use the global policy queue and hold the active execution slot while waiting.

Per-step and per-run timeouts are enforced with contexts. Run elapsed time survives restart and includes time waiting for approval. Adapters must honor cancellation. Command cancellation terminates the directly launched process; arbitrary grandchildren are not promised to be killed. Human approval is a durable journal state and does not hold an execution slot. Approval resolution uses a transaction and cannot resolve an already canceled run. Settings controls global and default per-automation concurrency (run-history retention is in the same config, without a UI yet); each automation can override its own limit. Pending runs are bounded to 100 per automation.

### Retention, caching and the scheduler

Finished runs and their journal rows are pruned so the database does not grow without bound. Config fields `retentionDays` (default 30, 1–3650) and `retentionMaxRuns` (default 500 per automation, 10–100000) are both applied; a zero value from a client that predates the fields selects the default. Only `succeeded`, `failed`, `canceled` and `uncertain` runs are ever deleted: `queued`, `running` and `waiting-approval` runs are kept whatever their age. The per-automation cap additionally spares runs that finished within the last hour, which keeps a webhook delivery's `trigger_key` around for its replay window. Journal rows go with their run through the foreign-key cascade. Pruning runs off the scheduler goroutine at start and then hourly, in batches of 500 rows, and `Engine.Prune` runs it synchronously.

The engine keeps non-terminal runs in memory and mirrors every committed status change into that copy. `automations.run` summaries, queue decisions, cancellation and restart recovery read the in-memory run; SQLite is read only for a run that is not tracked (for example a `waiting-approval` run after a daemon restart, which is loaded on first use). Saved definitions are parsed once into an immutable snapshot (validation result, event filter, schedule, and an index of enabled event triggers by literal topic or wildcard); `Save` and `Delete` drop the snapshot and the next reader rebuilds it. An event that no automation subscribes to costs one map lookup. The scheduler arms a single one-second timer that is re-armed only after it fires, so event bursts and wake-ups cannot postpone schedules, approval expiry or pruning. The webhook HMAC key is read from Credential Manager on first use and cached in the daemon; `GenerateSecret` replaces the cached value. A key changed outside Orole is picked up after the daemon restarts.

Known secret values registered through `Engine.RegisterSecret` and bearer/GitHub/API token patterns are redacted from journal input/output/error and audit details. Sensitive JSON keys are also redacted. Redacted prior outputs are used consistently on restart. Do not paste credentials into definitions, command environments, prompts or URLs; definitions are configuration stored in SQLite, not a secret vault. Register secrets at the integration boundary before they can appear in tool output.

Save rejects recognized secret values and sensitive credential fields rather than persisting them in a definition snapshot. This is defense in depth, not a way to detect every possible opaque credential.

## Local webhooks

Disabled by default. Settings > Automations sets concurrency, enables the listener, selects a port (1024–65535), and generates/rotates the secret. The server binds **only `127.0.0.1`**, never a network interface. Exposing it externally requires the user's own tunnel and its associated security configuration. Orole does not create a tunnel.

The HMAC key is stored only under Credential Manager target `Orole OS/automations/webhook-hmac`. It is never returned to the frontend or stored in SQLite. A local sender running as the same Windows user can read it through `internal/credentials`; remote senders must be provisioned by the user's own trusted secret management workflow.

Send `POST /automations/<id>` with JSON (maximum 256 KiB), one `X-Orole-Timestamp` header (canonical decimal Unix seconds, no leading zeroes or `+`), and one `X-Orole-Signature` header containing `v1=<64 hex characters>`. **Only this versioned format is accepted:** the former bare/`sha256=` signature over timestamp and body is rejected. Use the stored credential bytes as the HMAC key; do not hex-decode the credential's text.

### Version 1 signing contract

The HMAC-SHA256 input is the literal 17-byte prefix `OROLE-WEBHOOK-V1\x00` (16 ASCII characters followed by one NUL byte), then **five length-prefixed fields in this order**:

1. Method: the exact ASCII bytes `POST`.
2. Target: the ASCII bytes of `/automations/` followed by Go `url.PathEscape(id)` (UTF-8 ID bytes, uppercase percent escapes). Ordinary generated UUID IDs require no escaping. Only the canonical spelling is accepted: no redundant escaping, slash/backslash in the decoded ID, `.`/`..` IDs, trailing slash, or query string (even a bare `?`). Scheme, host and port are not signed, allowing a user-managed tunnel to forward the same canonical path.
3. Timestamp: the exact `X-Orole-Timestamp` bytes.
4. Provenance: the exact `X-Orole-Chain` header value after HTTP's surrounding whitespace removal, or zero bytes if absent/empty. A nonempty value must be a JSON array of causing automation IDs, not `null`. Do not reserialize it after signing; even equivalent JSON with different internal whitespace changes the signature. ASCII JSON with escaped non-ASCII characters avoids header-encoding differences.
5. Body: the exact transmitted body bytes, not parsed or reserialized JSON.

Each field is encoded as its **unsigned 64-bit big-endian byte length followed immediately by those bytes**; there are no separators, terminators or implicit newlines. Prefix and field lengths are included in the HMAC. `X-Orole-Signature` itself is not. Hex digits may use either case; the version prefix is exactly `v1=`. Duplicate timestamp, signature or chain headers are rejected, rather than selecting one value. Any future field affecting provenance or dispatch must be added to a new authenticated envelope version, not an unsigned header.

Verification uses constant-time comparison and the existing inclusive ±5-minute timestamp window. Repeated identical signed deliveries deduplicate durably by signature and return the original run ID, including after daemon restart and regardless of hex digit case. Changing the target, method, timestamp, body or chain invalidates the captured signature. Authenticated chains still enforce self-recursion and maximum depth; knowing the global credential grants signing authority, so provision it only to trusted senders. Return status is 202 with `runId`, 204 for a nonmatching filter, 401 for invalid authentication, 404 for a noncanonical/unknown target, or an explicit error.

This Python sender accepts already-provisioned credential bytes in memory; it does not retrieve, print or persist secrets:

```python
import hashlib
import hmac
import json
import struct
import time
import urllib.parse
import urllib.request


def send_webhook(base_url, automation_id, credential, body, causes=None):
    if not automation_id or automation_id in (".", "..") or any(
        c in automation_id for c in "/\\"
    ):
        raise ValueError("automation ID must be one canonical path segment")
    target = "/automations/" + urllib.parse.quote(automation_id, safe="$&+:=@")
    timestamp = str(int(time.time()))
    chain = "" if causes is None else json.dumps(
        causes, ensure_ascii=True, separators=(",", ":")
    )
    fields = [b"POST", target.encode("ascii"), timestamp.encode("ascii"),
              chain.encode("ascii"), body]
    envelope = b"OROLE-WEBHOOK-V1\x00" + b"".join(
        struct.pack(">Q", len(field)) + field for field in fields
    )
    headers = {
        "Content-Type": "application/json",
        "X-Orole-Timestamp": timestamp,
        "X-Orole-Signature": "v1=" + hmac.new(
            credential, envelope, hashlib.sha256
        ).hexdigest(),
    }
    if chain:
        headers["X-Orole-Chain"] = chain
    request = urllib.request.Request(
        base_url.rstrip("/") + target, data=body, headers=headers, method="POST"
    )
    with urllib.request.urlopen(request, timeout=10) as response:
        return response.status, json.load(response) if response.status == 202 else None
```

Supply `body` as the exact JSON byte sequence and `base_url` as the configured listener origin (for example `http://127.0.0.1:8765`). For retries, retain the original timestamp, chain, body and signature rather than creating a new delivery. The webhook regressions use the real loopback server and persisted runs to cover cross-target replay, changed provenance, canonical paths, malformed/legacy signatures, time/body limits and replay after engine restart.

## API and verification

IPC: `automations.list/save/delete/run/cancel/runs/run.detail/approvals.list/approve`, plus `automations.stepTypes`, `automations.events`, `automations.config`, `automations.config.set`, `automations.webhook.generateSecret`, `automations.schedule.preview`, `automations.templates`, and `automations.proposals.get/resolve` (bot drafts). Run/journal changes publish `automations.run` over IPC; the current frontend refreshes visible resources every five seconds.

Run `go test -race ./internal/automations/... ./internal/events/...` and `go vet ./internal/automations/... ./internal/events/...`. Tests inject clock, tool, notification, resolver and dialer implementations. Authorization regressions use the real shared policy with a child-process filesystem effect and a real HTTP server; missing/denied policy produces no effect, while approval permits dispatch. Crash tests close the first runner's database mid-operation and reopen it, exercising unfinished journal recovery without repeating finished actions.

The opt-in daemon smoke test runs the real `go version` command and sends a real notification through authenticated named-pipe IPC, retaining the complete journal in the isolated database:

```powershell
$env:OROLE_HOME='C:/code/projects/orole-os/.orole-dev'
$env:OROLE_AUTOMATION_SMOKE='1'
go test -v ./internal/daemon -run '^TestAutomationCommandNotification$'
```

It connects to an existing dev daemon, or starts/stops one through the existing daemon test harness if absent. It never stops a daemon it did not start. Approve the command through the shared policy UI when prompted; the executable allowlist alone does not grant authority. The output includes the redacted journal; no sample runs are seeded into production.

### Observed integration smoke

The real daemon workflow smoke passed on 2026-10-08 using the parent-approved isolated absolute home `C:/code/projects/orole-os/.orole-smoke-automation`; the existing `.orole-dev` instance was left untouched after it rejected authentication.

| Journal row | Status | Attempt | Output |
| --- | --- | --- | --- |
| `version` (`command.run`) | succeeded | 1 | exitCode `0`, stdout `go version go1.27.1 windows/amd64` |
| `notify` (`notify`) | succeeded | 1 | notification ID `1245de3e-afa3-4a36-8a8b-d6c4a1c595a2`, level `success` |

Run `c5760b67-7a27-4be7-a9fc-38e930a40d67` finished `succeeded`; its SQLite journal remains in the isolated smoke home. This verifies notification persistence/publication, not whether Windows displayed a native toast. Browser preview was also opened without mocks and rendered the honest “Automations needs the desktop app” state with no browser errors.
