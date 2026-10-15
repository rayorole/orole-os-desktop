# Bot computer: a local VM per bot (design)

Date: 2026-10-15 · Status: draft for review · Background: `reports/Local agent micro VM sandboxes.md`,
`reports/NixOS vs Debian agent VMs.md`

## Goal

Every chat bot can have its own small Linux computer on the user's PC: its own disk, a terminal and a visible
browser. The user can watch what the bot does live and take over mouse and keyboard, for example to log in to a
website or solve a CAPTCHA, which the bot must never attempt itself.

Done when the manual checklist in "Testing" passes on the developer's Windows 11 PC and automated tests are green.

## Scope

In scope (this piece):
- One VM per bot, started on demand, stopped after 15 minutes idle, stopped with the daemon. Disk persists.
- Bot tools: run a command, read/write files on the VM disk, drive the browser, ask the user to take over.
- Live screen (noVNC, view-only by default) and a terminal in a new **Computer** tab on the bot page.
- First-use setup: detect Windows Hypervisor Platform (WHP), one elevated helper to enable it, download and verify
  QEMU and the guest image.

Out of scope (later pieces): code tasks in VMs, host file access, egress proxy / network allowlist, memory
snapshots, warm pools, start/stop/reset buttons and resource display, wiping saved logins from the UI,
standalone (non-bot) sandboxes, non-Windows hosts.

## Decisions

| Topic | Decision |
|---|---|
| Owner | Each bot owns at most one VM. |
| Hypervisor | QEMU (`-accel whpx`) on WHP, started by the daemon as a child in its job object. |
| Guest OS | NixOS, built from a pinned flake in Linux CI; read-only base image + per-bot qcow2 overlay. Fallback to Debian if the NixOS spike criteria in the NixOS report fail. |
| Network | QEMU user-mode NAT, open internet. Known risk: the guest can reach the home LAN and services on the host. Documented in UI and security docs. |
| Host files | None. The VM sees only its own disk. |
| Live view | Minimal Wayland desktop + headful Chromium; VNC server bound inside the guest, relayed by `orole-guest` over the guest channel; noVNC in the UI fed by Orole IPC, never a network socket. |
| Shipping | Download on first use: pinned QEMU build + guest image, hash-checked, stored under `<data>\sandbox\`. |
| Admin | Daemon never elevated. Setup screen + separate `orole-sandbox-setup.exe` run once via UAC to enable WHP, then reboot. |
| Lifetime | VMs stop cleanly with the daemon; disks and browser profile survive; running commands do not. |
| Limits | Default 2 GB RAM, 2 vCPU, 10 GB disk per bot (per-bot override in bot config, no UI in this piece); max 4 running VMs; idle stop 15 min. |
| UI | Computer tab = live screen with "Take over" / "Done" + terminal. Nothing else. |

## Architecture

### Host (daemon)

New package `internal/sandbox`:
- `Manager`: maps bot id → VM; start-on-demand, idle timer (no bot activity and no viewer for 15 min), cap of 4
  with least-recently-used eviction of idle VMs, wait up to 30 s when all are busy, shutdown of all VMs on daemon
  exit, free-memory check (refuse if free RAM < VM RAM + 1 GB).
- `Backend` interface (`Start`, `Stop`, `Wait`, `Channel`): `backend_windows.go` runs QEMU; a stub on other
  platforms returns `NotImplemented`; a fake backend for tests.
- `Runtime`: downloads, verifies (SHA-256 pinned in the release manifest) and stores QEMU and the image; versioned
  directories; a partial or mismatched download is deleted.
- `Setup`: WHP detection without admin; launches `orole-sandbox-setup.exe` (new `cmd/orole-sandbox-setup`) with UAC
  to enable the feature; reports "reboot needed".
- Guest channel: QEMU virtio-serial chardev connected to a local AF_UNIX socket in the user-only data dir (fallback: a named pipe with the same user-only DACL as `internal/ipc`); HTTP/2 (h2c) over that stream gives
  multiplexed streams (`exec`, `files`, `pty`, `screen`, `browser`) authenticated with a per-VM random
  token injected at boot (kernel command line or a one-time first frame).
- Storage: `<data>\sandbox\runtime\<version>\` (QEMU), `<data>\sandbox\images\<version>\` (base image),
  `<data>\sandbox\bots\<bot-id>.qcow2` (overlay), `logs/sandbox-<bot-id>.log` (QEMU stderr, guest console).
- Migration `migrations/0112_sandboxes.sql`: table `sandboxes` (bot id, image version, overlay path, limits,
  last used, state). Deleting a bot deletes its row and overlay.
- Wiring in `internal/daemon/svc_sandbox.go`; IPC methods for the UI (status, screen stream, input, takeover,
  terminal attach).

### Guest (NixOS image)

- `orole-guest` (Go, new `cmd/orole-guest`, built for linux/amd64 and baked into the image): serves the stream
  types above. `exec` with timeout and output cap; `files` read/write within the guest; `pty` for the terminal;
  `screen` relays the local VNC server; `browser` drives Chromium via CDP (chromedp).
- Desktop: a minimal Wayland compositor with a single visible Chromium using a persistent profile on the overlay;
  VNC server bound to localhost inside the guest only.
- Preinstalled: Python 3, Node, git, curl, jq, ripgrep and common CLI tools; nix-ld enabled so vendor binaries run.
- Image built in Linux CI from a pinned flake; release manifest records version and hashes; compatibility flag
  decides whether existing overlays survive an image update (if not, the overlay is recreated after telling the
  user).

### Bot tools (`internal/chat/tools`)

New `chat.Tool`s, registered in `svc_chat_tools.go`, available when the bot's computer is enabled. Enabling is one
"Computer" switch in the bot's existing tool settings (stored in `bots.config`, default off); no VM is created
until the first tool call after it is switched on.
- `computer_run(command, timeout?)` — effect: write.
- `computer_read_file(path)` — effect: read. `computer_write_file(path, content)` — effect: write.
- `browser_open(url)`, `browser_click(target)`, `browser_type(target, text)`, `browser_read()`,
  `browser_screenshot()` — open/read/screenshot are read, click/type are write.
- `ask_user_to_take_over(reason)` — pauses the turn until the user hands back or the approval timeout passes.

Every call goes through `policy.Check` with the bot as actor; the bot's tool access level applies as today. Command
output and page content are returned as untrusted data. Browser tools are refused while the user holds the
takeover lock; the lock and its state live in the daemon.

### UI

- Bot page gets a **Computer** tab: setup screen when not ready (with "Enable" / "Try again" / reason it cannot
  work); otherwise the live screen (view-only, "Take over" / "Done") and an xterm.js terminal attached through the
  existing terminal plumbing.
- Chat shows a compact "Using the computer" card for computer/browser tool calls, opening the Computer tab.
- `ask_user_to_take_over` raises a "Needs you" notification that opens the Computer tab.
- New bound services/events require `go tool mygo generate`.

## Data flow

1. Bot calls a computer tool → policy check (approval card under "ask") → Manager ensures the VM is running
   (chat shows "Starting computer…") → request over the guest channel → streamed result back, capped, as untrusted
   data. Commands are also echoed in the Computer tab terminal.
2. Browser tools are executed by `orole-guest` via Playwright; the user watches through the screen stream.
3. Takeover: bot calls `ask_user_to_take_over` → turn paused, notification → user clicks "Take over" (input
   enabled, bot browser tools blocked) → "Done" → bot receives "the user finished: <note>" and continues. Timeout
   behaves like other approvals. The user may take over at any time; a running bot step finishes first.
4. Idle 15 min without bot activity or viewer → clean guest shutdown. Daemon exit → all VMs shut down.

## Errors

- WHP off / virtualization unavailable → tools return "The computer isn't set up yet"; Computer tab shows setup
  or the reason (e.g. virtualization disabled in BIOS).
- Download failure / hash mismatch → nothing installed, file removed, "Try again".
- Boot timeout 60 s or guest not answering → VM stopped, log written, bot told "the computer failed to start"; no
  retry loop.
- VM crash mid-call → "the computer stopped unexpectedly"; next call boots again.
- Cap reached → evict least-recently-used idle VM; if all busy, wait 30 s then "all computers are busy".
- Low host memory → refuse to boot with a clear message.
- Disk full → normal guest error surfaces.
- Untrusted content can never trigger takeover or bypass approvals (decided in Go).

## Security notes (to document)

- The VM has open internet and can reach the home network and services on the host.
- Logins the user performs during takeover persist on the bot's disk and are usable by the bot afterwards; there is
  no UI to wipe them in this piece.
- Credentials typed during takeover go into the guest browser; Orole never stores them in SQLite or logs.
- No network listener on the host: all host↔guest traffic uses the user-only named pipe.

## Testing

Automated (CI, `go test -race`, `bun run --cwd frontend check`):
- `internal/sandbox` with a fake backend: on-demand start, idle stop, cap/LRU, waiting, timeouts, crash, takeover
  lock, bot deletion removes overlay.
- Guest channel framing, token check, close/reconnect over an in-memory pipe.
- `orole-guest` on Linux: exec timeout and cap, files, PTY.
- Bot tools: policy per access level, untrusted wrapping, takeover pause/resume.
- Runtime downloader: hash mismatch rejected, partial file removed.
- Frontend: Computer tab states (setup, starting, view-only, taken over) and chat card.

Manual checklist (`docs/development/bot-computer-e2e.md`):
1. Fresh install → setup screen → enable WHP + reboot → download completes.
2. Bot runs `python -c "print(1+1)"`; result and terminal echo appear.
3. Bot opens a website; it is visible live.
4. Bot asks for takeover; user logs in; hands back; bot uses the logged-in page.
5. After app restart, files and logins persist.
6. Idle stop after 15 min and the 4-VM cap behave.
7. Run on the developer's 21H2 PC and, if available, on 24H2.

## Docs and changelog

Architecture doc `docs/architecture/bot-computer.md`, updates to the threat model and security-permissions docs,
the manual checklist, and a CHANGELOG entry, all in the same PR.

## Open risks

- noVNC over Orole IPC (custom transport instead of WebSocket) is unverified; first task of the plan is a spike.
- QEMU virtio-serial on a Windows named pipe and WHPX stability for a Wayland desktop are unverified.
- No published boot/RAM numbers for this stack on WHP; measure in the first milestone.
- NixOS guest compatibility (Playwright, nix-ld) per the NixOS report's spike criteria.
