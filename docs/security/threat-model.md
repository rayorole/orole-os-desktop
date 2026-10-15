# Threat model

Scope: desktop shell, daemon, CLI, local SQLite, authenticated IPC, Credential Manager,
agents, integrations, automations, machine transport and declarative plugins as wired in this tree.

## Assets

| Asset | Where | Impact if lost |
|---|---|---|
| Secrets (GitHub, MCP/OAuth, SSH and automation credentials) | Windows Credential Manager | account takeover |
| Local files and repositories the user opens | disk; file roots in `file_roots`; worktrees | data loss, code tampering |
| Terminal sessions | daemon memory (shell processes, 1 MiB scrollback) | arbitrary command execution as the user |
| Audit log, settings, project registry | `orole.db` | loss of accountability / config integrity |
| IPC token | `ipc.token` (0600), memory | control of the daemon |
| Bot computers (VM disks with browser profiles and logins) | `<data>\sandbox\bots\<bot>.qcow2` | use of the user's web sessions by the bot or by malware that reaches the guest |

## Trust boundaries

1. **Other users / remote machines -> daemon:** no network listener; pipe DACL admits only the current user SID.
2. **Other processes of the same user -> daemon:** can read `ipc.token` and call IPC, i.e. can already run commands as the
   user. **Out of scope:** same-user malware is not defended against (stated plainly in ADR 0003).
3. **Frontend -> Go:** the webview content is our bundle (served by MyGo), but UI code is not a security boundary;
   every sensitive action is decided in the daemon. Terminal creation strips `owner` and `command` from UI requests.
4. **External content -> app:** mail, issue/PR text, MCP tool output and descriptions, file names and repository files
   (including `AGENTS.md`-style instruction files) are attacker-controllable data.
5. **Daemon -> external services** (GitHub API, MCP servers): HTTPS to user-configured endpoints; OAuth endpoints
   must be https (or loopback http) with no embedded credentials (`internal/mcp/oauth_discovery.go`). On request, the
   daemon downloads the speech engine and dictation models from GitHub and Hugging Face; see `docs/architecture/daemon.md`.
6. **Phone -> bots (Jarvis):** calls pass through Twilio, ElevenLabs and a Cloudflare relay the owner deploys; the daemon only
   connects outbound to the relay. See [Jarvis phone calls](#jarvis-phone-calls).
7. **Bot computer guest -> host:** each bot's VM is untrusted; it talks to the daemon only over a per-VM token-authenticated local channel and never sees host files, but its user-mode NAT gives it the internet, the home network and anything on this PC that accepts network connections on its LAN address (host loopback is blocked). See [Bot computers](#bot-computers-local-vms).

## Threats and controls

| Threat | Control (today) | Gap |
|---|---|---|
| Local network attacker reaches daemon control API | named-pipe control API, no network-interface management listener | explicit OAuth callback and configured webhook listeners are separate, narrowly scoped endpoints |
| Another local user connects to the pipe | protected DACL, current user only | none known |
| Token guessing / replay | 256-bit random per run, constant-time compare, 5 s hello window, file deleted on exit | same-user read (above) |
| Secret leakage to disk/logs/UI | secrets only via `internal/credentials`; DB columns hold no tokens; diagnostics uses a probe key only | review discipline; no automated secret scan yet |
| Hostile text spoofs UI (bidi, control chars, invisible chars) | `internal/untrusted.Text/Line` at display/approval boundaries | sanitization does not make external content safe to obey; not a universal prompt-injection defense |
| Unapproved side effects by agents/MCP/automations | IPC-wired `internal/policy`; read/write/destructive defaults, audit, approval queue, fail-closed missing authorizer | approving a local CLI does not sandbox its process or guarantee that every CLI-internal action emits an approval |
| Path escape through files provider | operations only inside approved roots stored fully resolved (symlinks/junctions expanded) | tested in `internal/files` (as written by that slice) |
| Daemon DoS by a stuck client | bounded frames (4 MiB), bounded queues (256), write deadline 10 s, per-call 30 s deadline | no per-method rate limits |
| Tampered installer/update | updates require a signature that the embedded public key verifies; installer is **unsigned** | no code signing and no update keys configured yet (`docs/development/updates.md`) |
| Persistence abuse via autostart | per-user logon task, `LeastPrivilege`, runs only the daemon path chosen at install | task points at a fixed exe path; whoever can write that file can run code at logon (normal per-user install risk) |
| Microphone audio leaves the machine unexpectedly | dictation is recognized locally (`internal/dictation`); recording starts only from the dictation control and audio goes only to the daemon's helper process; audio and transcript are never logged, stored or sent; 120 s cap | same-user malware can still use the microphone itself |
| Tampered speech engine or model download | every file pinned by URL (repository revision or release tag), size and SHA-256, verified while streaming before it is moved into place; the helper refuses an engine DLL reporting another version; native code runs in a separate helper process | the pinned hashes are the ones Hugging Face and GitHub publish; whoever can write `%LOCALAPPDATA%\Orole\voice` (same user) can replace the DLLs after verification |

## Jarvis phone calls

Bots with full permissions can phone the owner and be phoned (`docs/architecture/jarvis.md`). New boundaries:

| Threat | Control | Gap / accepted risk |
|---|---|---|
| The relay (Cloudflare Worker) sees call traffic | it holds no database or storage and keeps only the in-flight request; transcripts and turns pass through it in memory; the daemon connects outbound (no listener); fails closed (503) per path: each path refuses while its own secret is unset (`/daemon` without `RELAY_TOKEN`, `/llm*` and `/inbound` without `LLM_SECRET`, `/status` without `WEBHOOK_SECRET`) | whoever controls the owner's Cloudflare account can read traffic in flight or redeploy the Worker |
| Stolen relay token | lets an attacker impersonate the daemon to the relay (see call traffic, answer ElevenLabs' requests) but not reach the daemon; rotate with "Generate relay secrets" in Settings > Phone, redeploy the relay secrets and run Connect again | no automatic rotation |
| Forged requests to the relay | bearer token for the daemon socket; `x-orole-relay` secret compared in constant time for `/llm` and `/inbound`; HMAC with a 30-minute timestamp window for `/status` | a replayed `/status` inside 30 minutes is ignored once the call has ended (the daemon only acts on the live call) |
| ElevenLabs and Twilio process call audio and transcripts | the owner chooses and pays for both accounts; keys live in Credential Manager; the app and UI state that conversations pass through them | provider retention is the provider's policy; the daemon cannot enforce it |
| ElevenLabs webhooks are workspace-wide | Connect sets the inbound personalization and post-call webhooks for the owner's whole workspace, so they apply to every agent there | use a workspace without other agents; the app notifies the id of an orphaned webhook after a relay URL change but does not delete it |
| Full-permission bots reachable by phone | Jarvis can only be enabled when the gate holds (tool access All, code tasks YOLO); re-checked at call start and every spoken turn, and Jarvis turns itself off when it fails; outbound calls go only to the verified owner number; the opener is a name-only greeting and the daemon hangs up on answering-machine phrases, so nothing about the reason reaches voicemail; a policy `ask` fails at once on a call instead of being approved by voice | whoever is on the line can direct the bot; the voicemail check is phrase-based and can miss an unusual greeting (it still hears only the name-only opener) |
| Caller-ID spoofing on inbound calls | **accepted risk (owner decision, 2026-10-14):** inbound acceptance is a caller-ID match against the verified number only, no second factor. Mitigations: nothing is spoken to a non-matching caller (`jarvis.inbound.rejected` audit entry); every accepted inbound call is audited (`jarvis.call.started`), shown in the bot's chat and notified, so a call the owner did not make is visible at once | a spoofed call from the owner's number reaches a full-permission bot |
| Prompt injection into a call | event titles, task output, automation errors, rule notes, notification text and the reasons of requests injected into a live call enter the brief only as fenced data (`chat.Fence`), titles and reasons clipped to 200 characters; the opener prompt carries no event text at all; tool results are fenced as in chat; the model cannot choose the number dialled | same limits as the prompt-injection rule above |
| Relay and webhook secrets in the frontend | **exception R8 (owner decision, 2026-10-14)** to "secrets never reach the frontend": the relay token, LLM secret and ElevenLabs webhook secret are shown once in Settings > Phone so the owner can `wrangler secret put` them; held only in that page's component state, never stored or readable again; Twilio and ElevenLabs keys are never sent to the frontend | a screenshot or screen share at that moment exposes them; rotate with "Generate relay secrets" and Connect |
| Runaway cost or call spam | daily call cap (10), monthly estimated-cost cap ($20), 10-minute cool-down per bot for `call_owner`, quiet hours, one call at a time, 30-minute ceiling per call | the cost figure is an estimate from a per-minute rate setting, not the provider's bill |
| Verification code | the owner number becomes verified only after a spoken six-digit code is entered (10 minutes, 5 attempts); `jarvis.*` settings cannot be written through `settings.set` | none known |

Calls and rules are audited; secrets, transcripts and spoken text are never logged (timings only).

## Bot computers (local VMs)

Each chat bot can have its own Linux VM (`docs/architecture/bot-computer.md`).

| Threat | Control | Gap / accepted risk |
|---|---|---|
| Bot code escapes to the host | a hardware VM per bot (QEMU on Windows Hypervisor Platform); the guest sees only its own disk and no host folder is shared; commands, terminals and file access run as the unprivileged `bot` user without sudo, and the daemon is never elevated | a QEMU or hypervisor escape is out of scope; the pinned QEMU build is only as current as the release the owner publishes |
| Another local user or process reaches the guest channel | one AF_UNIX socket per VM in `<data>\sandbox\run\`, which has an explicit protected DACL (user and SYSTEM); QEMU connects to the daemon and nothing listens on a network interface; every request carries a fresh 32-byte per-boot token that exists only in daemon memory, a user-only file deleted once QEMU has read it, and the guest's root-only sysfs file | same-user malware can read what the user can (see trust boundary 2); the token is also visible to guest root |
| The guest reaches host services or the home network | user-mode NAT with open internet. **The guest can reach the home network** (router, NAS, printers, other PCs). Host loopback via 10.0.2.2 and fec0::2 is rejected by nftables for the `bot` user, so host services bound only to loopback are unreachable from bot code; services on this PC that accept connections on its LAN address are reachable like any other device on the network (the daemon's IPC is a per-user named pipe and never listens on loopback or any network interface) | guest root (only `orole-guest`) could remove the rule; there is no egress allowlist yet |
| The bot uses its browser or screen beyond what the user approved | `browser_open` asks per site (policy resource `computer/<botID>/browser/<host>`) and refuses local addresses; while the user has taken over, `computer_run`, `computer_write_file` and the browser tools are refused before any approval; guest nftables rejects TCP connects from the `bot` user to Chromium's DevTools port (9222) and wayvnc (5900) on guest loopback, so the bot's commands cannot drive the browser or watch the screen past these checks (only root `orole-guest` connects); `browser_type` refuses password fields | the per-site approval and the local-address refusal cover only addresses the bot types into `browser_open`: links it clicks, redirects, new tabs and `computer_run` (curl, scripts) reach other hosts, and bots on "read and change" or "everything" access are never asked. A `computer_run` approval shows the command, not the content of a script written earlier (the write approval shows the first 300 bytes). The browser profile's cookie files belong to `bot`, so the bot's commands can read them: sessions the user signs into are usable by the bot. Chromium, cage and wayvnc themselves run as `bot`; the firewall closes the TCP ports, not same-user access to those processes or the Wayland socket |
| The Computer tab types into the guest without a takeover, or power-cycles it | the daemon's RFB filter (`internal/sandbox/rfbfilter.go`) forwards keyboard, pointer, clipboard and QEMU key messages **only while the user holds the takeover lock**; power (xvp) and resize messages are always dropped, the xvp and extended-mouse pseudo-encodings are stripped, continuous updates are disabled, and keys and buttons still held are released when control is handed back. noVNC's view-only mode is cosmetic, not a control; a guest that floods the screen stream past its byte budget (32 MiB/s sustained, 64 MiB burst) has the screen closed | wayvnc runs as `bot`, so a bot command could replace the VNC server during the gap while it restarts and then see what the user types during a takeover; a flood is bounded only by the byte budget |
| A crashed or hidden GUI keeps a computer running or the bot locked out | the Computer tab holds a viewer lease (renewed every 45 s) that the daemon clears after about 150 s; closing or hiding the pane clears the viewer flag at once but does **not** release a takeover at once (that would tell a waiting bot the user finished). A takeover the user started by hand ends about 150 s after the pane went away (closed, hidden or the GUI gone) unless the pane comes back; one the bot asked for ends at Done or after the 30-minute hold limit | a user takeover can linger up to about 150 s after the user leaves the pane; a bot-requested one blocks the bot for up to 30 minutes |
| Foreground handoff for the UAC prompt | the desktop app calls `AllowSetForegroundWindow` for the daemon's process id right before `sandbox.setup.enable` so the prompt appears in front; it grants no other privilege and the helper checks stay as described under Elevation | none known |
| The Computer terminal misleads or spies on the user | the terminal's shell runs as `bot` in the guest; the command echo strips escape sequences and control characters | the bot's commands can write escape sequences to the pty, see what the user types there and print clickable links: its output is untrusted, and the user should not type secrets into it |
| Guest output steers the bot (prompt injection) | everything from a guest (command output, files, page text) is untrusted data: sizes are capped again on the host and the bot computer tools return results as `ToolResult{Untrusted: true}` | same limits as the prompt-injection rule below |
| Elevation abused | only `orole-sandbox-setup.exe`, once, through UAC, to enable a Windows feature. It accepts one fixed argument, refuses to run unelevated and runs `System32\Dism.exe` by full path. The daemon opens it before the elevated start (refusing a reparse point, denying writes) and checks its SHA-256 against the hash embedded at build time; a build without the hash, or a mismatch, refuses | the hash protects against replacement after install, not against a tampered build |
| Tampered QEMU or guest image | files come from this repository's GitHub releases and are checked against sizes and SHA-256 values compiled into Orole, while streaming, before they are moved into place; releases are drafts until the owner publishes them | whoever controls the repository's release process before the pin is generated; the pinned hashes are not independently signed |
| VM outlives the daemon, or runs away | QEMU runs in a job object with kill-on-close; at most 4 VMs, 2048 MB and 2 vCPU each, 15-minute idle stop, a boot is refused when free RAM is low | a daemon killed by the OS leaves no VM, but a running command is lost |
| Secrets in logs | the token is never in QEMU's argv, the kernel command line or the console log (checked); logged command lines go through `redactArgs` | none known |
| Not covered yet | egress allowlist, credential sentinels, wiping saved browser logins (later phases) | |

## Prompt injection

Rule: **external content is data, never instructions.** Concretely, for every module that feeds text to an LLM or to
the user:

- Wrap external text as clearly delimited, labelled data in prompts; never concatenate it into the instruction role.
- Never let external text choose tools, targets, recipients or approvals. Tool selection and arguments that cause side
  effects go through `internal/policy.Check`; effects inferred from server-supplied annotations are hints and never
  unlock more than `ask`.
- Show approvals using sanitised text (`untrusted.Text`) with the actor, action and resource; the user approves the
  action, not a model-authored description of it.
- Repository instruction files are inputs the user opted into per project; they cannot widen permissions.
- Agent-owned terminals are created only by daemon code (`owner` set server-side), never by a UI or external request.

**Status:** the agent, MCP, automation and plugin services are registered in the daemon.
Go authorization and approval resolution are reachable through authenticated IPC, and
installed-app smoke exercised denied/approved launches and independent plugin trust/tool
approvals. These checks prove those boundaries, not immunity to arbitrary prompt injection.
Agent worktrees separate changes; they do not isolate credentials, the host filesystem or
the network from an approved process. Untrusted local agents require a separately reviewed
OS/container/VM boundary; no Windows Sandbox backend is claimed here.

### Repository review without implicit execution

Agent automatic statistics and manual review, project Git diff/status, and Git-file
listing annotations share `internal/git/review.go`. Review snapshots refs and the
index into a disposable Git directory, reads existing objects through alternates,
and writes new review objects only into that temporary directory. It imports native
normalization, binary classification, ignore/attribute data and upstream mappings,
not executable configuration. Repository clean/process filters, fsmonitor hooks,
index hooks, external diff/textconv drivers and lazy-fetch transports cannot run
through this boundary. Inherited Git configuration overrides are not forwarded.

Text/binary changes, untracked files, staged ignored files, linked worktrees and
split indexes remain reviewable without changing the real index. Generic status
and unstaged diff inspect initialized submodules recursively through the same
boundary rather than executing nested repositories' status commands; dirty
submodule indicators remain visible. Metadata overflow fails explicitly rather
than silently dropping changed files; large diffs retain the shared truncation flag.
External clean filters are deliberately not applied: review shows native Git
normalization of on-disk content, not arbitrary program output.

**Verification:** the integrated Windows `go vet ./...` and `go test -race ./...` passed.
Crafted-repository marker regressions are in `internal/agents/worktrees_security_test.go`,
`internal/git/review_security_test.go` and `internal/files/git_security_test.go`.
They cover automatic/manual agent review, real diff/status/listing results,
clean/process filters, fsmonitor, diff drivers, index hooks and nested dirty
submodules. This is regression evidence, not a claim that arbitrary approved agent code is sandboxed.

### MCP approval and credential boundaries

Direct MCP calls disclose a frozen JSON argument document and dispatch that same
document after approval. Calls fail closed if the complete server/tool/argument
detail cannot fit the 1,500-character disclosure limit, contains known session
credentials, or would change under display sanitization; a truncated prefix never
authorizes undisclosed arguments.

MCP auth sessions learn secret values when credentials are loaded, set and refreshed.
Tool results (including structured content and error blocks) and surfaced call errors
are scrubbed at that boundary before truncation or delivery to adapters, so automation
journals do not depend on a frontend or test registering MCP secrets. Retired values
remain in session memory to scrub responses already in flight during rotation.
OAuth token failures retain only HTTP status and recognized error identifiers;
unknown identifiers and provider descriptions are not reflected into diagnostics,
authorization events, logs or audit records. This is exact-value redaction, not a
claim to detect arbitrary encodings or transformations of secrets.

## Plugin trust

The implemented plugin system is declarative: a validated capability manifest contributes
fixed MCP actions, command entries and widgets. It never loads arbitrary React/JavaScript,
HTML or native plugin executables. Exact-digest trust requires a separate policy decision;
every invocation still checks the declared server/tool and MCP permission using the plugin
actor. Saving changed content clears trust; revocation/removal cancels active work. A remote
side effect already dispatched cannot be rolled back by cancellation.

Manifest arguments are plaintext configuration, not a secret store. Output is bounded,
sanitized text. See `docs/architecture/plugins.md` for validation limits, cancellation races,
runtime proof and the distinction between trusting a manifest and approving a tool call.

## Reporting

Open a private security advisory on the repository; do not post tokens or diagnostics JSON containing paths publicly
without reviewing them.
