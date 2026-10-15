# Security and permissions

Principles from `AGENTS.md`: permissions are decided in Go; the UI is not a security boundary; secrets only in
the credential store; no network-facing management listener; external content is data.

## Local access control

| Boundary | Control | Code |
|---|---|---|
| Who can talk to the daemon | pipe DACL = current user SID only, protected; plus a per-run 256-bit token in a 0600 file, constant-time compare; `hello` first, 5 s deadline | `internal/ipc` |
| Network exposure | No TCP/HTTP daemon management listener. OAuth callbacks and explicitly enabled signed webhooks bind loopback only; acceptance-only WebView2 CDP is opt-in loopback | `internal/mcp`, `internal/automations`, packaged acceptance launcher |
| Bot computers | add no listener: QEMU connects to AF_UNIX sockets the daemon creates in `<data>\sandbox\run\`, a folder with an explicit protected DACL (user and SYSTEM). `orole-sandbox-setup.exe` is the only component Orole runs elevated (once, via UAC, hash-pinned at build time); the daemon never is | `internal/sandbox`, `cmd/orole-sandbox-setup` |
| Frontend to Go | MyGo bindings expose only methods of bound services; they hold no authority beyond what the daemon enforces | `internal/app` |
| Terminals created from the UI | `owner` and `command` fields are cleared; only daemon code can mark agent-owned terminals | `svc` `terminals.create` |
| Files | only inside approved, fully-resolved roots (`file_roots` for local; pinned `projects.path` for git) | `internal/files` |

### Project file roots and upgrades

Explicit project registration resolves the repository's main checkout to its native canonical path,
including symlinks, Windows junctions, and short (8.3) names, before persisting `projects.path`.
The git file provider uses that stored path verbatim as its approval. Every access checks that the root
still resolves to the approved location; replacing a checkout or an ancestor with a link to another
location does not grant access to that target, even when it is another Git repository. Callers may use
an ancestor alias only when it resolves inside an unchanged approved root.

No SQL backfill or startup filesystem canonicalization is performed for existing project records.
Earlier registration already stored absolute, symlink-resolved paths; those stored values remain the
authority, rather than whatever they point at during upgrade. Unchanged canonical records continue to
work. Legacy noncanonical records (including unexpanded Windows aliases), inaccessible roots, and
retargeted roots fail closed in the git file provider and are shown as unhealthy roots. Remove the
registration and explicitly add the intended repository again to approve its current canonical location.
Listing, opening, or restarting never silently repairs an approval or rewrites a stored project path.

## Decision engine (`internal/policy`)

Single place that answers "may this actor do this?". Inputs: actor, action, resource, effect
(read/write/destructive), optional `FromHint` (effect derived from untrusted server annotations).

- Match stored `policy_rules` (wildcards `*`; most specific wins; at equal specificity deny > ask > allow;
  `max_effect` limits a rule to requests not stronger than it) and `mcp_permissions`.
- Defaults: read -> allow, write/destructive -> ask, unclassified -> deny. User-requested `agent.start`,
  `agent.prompt`, `agent.restart` and `agent.permissionMode` are allowed without a second confirmation: submitting the task
  or choosing its Manual/Auto/YOLO mode is the instruction. YOLO has its own gate in the agents manager (explicit
  `confirmYolo`, the `agents.yoloDisabled` setting, never from an automation, from a bot only with `agents.botYolo`; audited as `agent.yolo`), and it
  does not bypass this engine for Orole's own MCP tools. Stored ask/deny rules still win.
  Automation/bot/agent-originated launches, actual tool actions, and external hints do not receive this default: a bot
  acting through `agents.WithActor` is the actor `bot:<id>`, an automation `automation:<id>`.
  Annotations from servers never unlock more than ask, with one exception the user opts into: bot tool access.
- Bot tool access (`agents.botToolAccess`, per bot `BotConfig.toolAccess`: ask | read | write | all) is the engine's
  auto-approve hook. It lifts only a **default** ask to allow for `bot:<id>` actors up to the chosen effect (`all`
  includes destructive; `read` trusts the server's read-only annotation). Explicit `mcp_permissions` and stored rules
  still win, unclassified stays denied, `bot:draft` always asks, and each lift is audited with source `auto-approve`.
  Imported bot files never carry it.
- `ask` parks the request (max 100 pending, 5 min timeout) until the UI resolves it; destructive actions cannot be remembered.
- Every decision is written to `audit_events`. Pending-approval text is sanitised with `internal/untrusted`.

**Wiring:** `internal/daemon/svc_mcp.go` exposes pending requests, resolution, and rule management over
authenticated IPC. The global `PolicyApprovals` dialog subscribes before loading its initial snapshot and skips `bot:` actors:
a bot's held request is decided inline in that bot's conversation (`features/chat/parts/JobApprovalCard.tsx`; Allow once,
Always allow this bot = `agent.start` + `agent.prompt` allow rules for `bot:<id>` on `*` for task requests and an exact
rule for any other rememberable request, Deny) and is also listed in Home › Needs you and counted on the bot in the rail.
Remembered bot rules are listed and removable under Settings > Task permissions > Remembered approvals; `agents.botAutoApprove`
skips the task-start ask altogether and `agents.botYolo` lets bots start in YOLO while YOLO is allowed. The permission
mode of a bot's task comes from the user's settings (`agents.botTasks`, per bot `BotConfig.tasks`), never from the bot.
MCP invocation uses `InvokeChecked`; Mail/Drive operations pass through the daemon adapters and policy.
The agent manager requires an authorizer for launch, restart, prompts, checks, and destructive worktree
discard, including calls from automations. Missing authorization fails closed. A retry of an already
dispatched agent key reads the existing result without launching again or requesting another approval.
CLI-reported tool approvals are also checked against policy before an Allow reaches the CLI.
This does not sandbox an agent's process: worktree separation is not OS isolation.

### Complete agent tool approval

Structured Claude requests disclose the complete original JSON and tool name, not the
short activity summary. The UI escapes control/directional characters, preserves numeric
representations, and requires review before enabling Allow. The daemon supplies an opaque
one-use snapshot ID; cancellation or agent request-ID reuse cannot authorize replacement
arguments. Duplicate pending wire IDs and inputs exceeding 64 KiB are denied before
publishing an approvable request. The existing Go policy decision still runs before dispatch.

A disposable browser surface exercised a padded command's otherwise-hidden suffix, omitted
write content, a 64-bit numeric representation and a bidi character. All remained visible
as inert escaped text; replacing the snapshot reset the review gate. This UI smoke does not
substitute for the backend protocol regressions or a production Claude authentication check.

## Untrusted text (`internal/untrusted`)

`Text`/`Line` remove control characters, bidi override/isolate marks, zero-width and other invisible format
characters, invalid UTF-8, and truncate. This stops spoofing a prompt or a file name; it does **not** make
content safe to obey.

## Audit log

`audit_events(at, actor, action, target, outcome, detail)`. The daemon writes entries for sensitive actions
(`d.audit`), e.g. `terminal.create`. Do not put secrets or full command output in `detail`.

See `credentials.md` for secret handling and `docs/security/threat-model.md` for the threat model.
