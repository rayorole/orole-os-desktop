# Bot computer: a local VM per bot

Every chat bot can have its own small Linux computer on the user's PC: its own disk, a terminal and a
visible browser. Design: `docs/superpowers/specs/2026-10-15-bot-computer-design.md`. Plans:
`docs/superpowers/plans/2026-10-15-bot-computer*.md`.

**Status: phase 3 (bot tools).** The manager, store, guest protocol and `orole-guest` run on a real QEMU backend
on Windows, with the runtime download, the setup helper and the NixOS guest image, all checked end to end on build 22000
(see "Guest image checks"). A bot with "Has its own computer" switched on gets terminal, file and browser tools and can
ask the user to take over (see "Bot tools"). The pinned release assets are drafts until the owner publishes them, so a
user's build cannot download them yet (the pinned URLs answer 404). What is visible in the app: the Tools switch, the
card in chat and the "Needs you" notification. The Computer tab (screen, terminal, Take over and Done buttons) and its
setup screens come in phase 4.

**A bot's computer lives in the daemon: it survives GUI restarts, not a daemon exit or reboot. Its disk persists;
running commands do not.**

## Architecture

```
 bot tool call (internal/chat/tools)
        |
 internal/sandbox.Manager ---- Store (table sandboxes)
        |  Ensure(bot) -> *Guest
        v
 Backend.Boot (Windows: QEMU + WHPX) ---> VM.Conn(): one byte stream (virtio-serial <-> AF_UNIX socket / pipe)
        |                                          |
 Guest (h2c client, golang.org/x/net/http2)  <---->  orole-guest (h2c server) in the Linux guest
                                                     internal/sandbox/guestagent
```

| Unit | Location | Responsibility |
|---|---|---|
| Types, paths | `internal/sandbox/sandbox.go`, `paths.go` | `Limits` (2048 MB, 2 vCPU, 10 GB by default), `State`, `Status`, `Availability`, errors; files under `<data>\sandbox\` |
| Backend | `internal/sandbox/backend.go`, `backend_windows.go`, `qmp.go` | `Backend.Boot(BootSpec) -> VM`; `VM.Conn/Shutdown/Kill/Done/ExitErr`. Windows: `qemu-system-x86_64 -accel whpx,kernel-irqchip=off` in the daemon's kill-on-close job object; QMP `system_powerdown`, then kill. Other platforms: `unsupported-os`. |
| Store | `internal/sandbox/store.go`, `migrations/0112_sandboxes.sql` | one row per bot: image version, overlay path, limits, last use |
| Manager | `internal/sandbox/manager*.go` | lifecycle, cap, idle stop, crash detection, takeover lock, delete |
| Guest client | `internal/sandbox/guestclient*.go` | typed calls over h2c; caps every answer; maps a dead channel to the manager's reason |
| Wire | `internal/sandbox/wire` | JSON types and limits shared with the guest |
| Guest agent | `internal/sandbox/guestagent`, `cmd/orole-guest` | the handler (OS-neutral, run in-process by host tests) and the Linux main serving it on `/dev/virtio-ports/org.orole.guest.0` |

### Lifecycle

- `Ensure(bot)` boots on demand. Concurrent callers share one boot (single flight); the boot runs with its own
  60 s timeout, independent of the first caller's context. A boot is refused with "the computer isn't set up yet"
  when the backend is not available or no image is installed, and with "not enough free memory" when free RAM is
  below the VM's RAM + 1024 MB. It then records the sandbox, boots, and waits until the guest's `/health` says
  ready. A boot that fails or times out kills the VM, writes a line to `logs\sandbox-<bot>.log` and reports
  "the computer failed to start"; there is no retry loop, the next call tries again.
- At most 4 VMs run. When a fifth is needed the least recently used idle VM is stopped first. Idle means no guest
  request in flight, no viewer and no takeover. When every VM is busy, `Ensure` waits up to 30 s for
  one to become idle, then fails with "all computers are busy".
- A VM idle for 15 min (no bot activity, no viewer, no takeover) is powered off (`/poweroff`, then the backend's
  shutdown, then kill). The overlay disk stays.
  An open terminal does not keep the VM alive by itself: only bytes moving on it (at most once per second) count
  as activity, so a forgotten, silent terminal is stopped with the VM and its session ends.
- A VM that exits on its own is a crash: calls in flight fail with "the computer stopped unexpectedly", the
  status becomes off with that error, the takeover lock is dropped, and the next call boots again.
- Restarting `orole-guest` inside the VM makes the host treat the channel as dead; the next call reboots the VM.
- A VM that is stopping keeps its overlay open, so `Ensure` waits for it to be gone before booting again.
- `TakeOver`/`Release` hold the takeover lock in the daemon; while it is held the VM is never idle-stopped or
  evicted, and bot browser tools are refused.
- `Delete(bot)` stops the VM (calls in flight fail), then removes the overlay, the socket and the record. The log
  is kept.
- `Shutdown` (daemon exit) stops every VM and refuses new ones.

### Guest protocol

HTTP/2 without TLS (prior knowledge) over the VM's one byte stream; base URL `http://guest`. Every request
carries `X-Orole-Token`, a 32-byte random hex token generated per boot, passed to the guest through QEMU fw_cfg
(`opt/orole/token`, from a user-only file that is deleted once QEMU has read it) and otherwise held only in daemon
memory; the guest answers 401 otherwise.

| Route | Purpose | Limits |
|---|---|---|
| `GET /health` | version and readiness | |
| `POST /exec` | `/bin/sh -lc <command>` as `bot`, in its own process group (host tests: `/bin/sh -c` as the agent's user) | timeout 120 s default, 600 s max; stdout+stderr 1 MiB kept, the rest dropped (`truncated`); background children get 2 s to release the output |
| `POST /files/read`, `/files/write` | guest files with `bot`'s permissions (a helper process: `orole-guest` re-executed as `bot`); relative paths under `/home/bot`; 403 where `bot` may not read or write | read 4 MiB (`truncated`), write 16 MiB max |
| `POST /pty?cols=&rows=` | login shell (bash) as `bot` on a pseudo-terminal it owns, full duplex; `X-Orole-Pty` id, `X-Orole-Exit` trailer | |
| `POST /pty/resize` | resize by id | |
| `POST /browser` | chromedp on the visible Chromium: open, click, type, read, screenshot (see "Bot tools"); 501 on an image without it | page load 60 s, other steps 20 s; page text 90 KiB, screenshot 5 MiB |
| `POST /screen` | full-duplex relay to the guest's VNC server (the viewer is phase 4); 501 on an image without it | |
| `POST /poweroff` | 202, then a clean power off | |

The host never trusts the guest: answers over 8 MiB are refused, exec output is cut to 1 MiB and file content to
4 MiB again on the host, and everything a bot receives from the guest is untrusted data (the bot tools wrap it).

### Daemon wiring

`internal/daemon/svc_sandbox.go` builds the `Runtime`, the host backend and the `Manager` from `<data>\sandbox`
(`Daemon.Sandbox`), stops every VM on daemon exit (after chat, before the database closes) and deletes a bot's
computer (VM, overlay, socket, row) when the bot is archived (`chat.bots.archive`; a bot without one is a no-op).
The delete runs in the background (it may wait for a boot in progress) and reports through `sandbox.status`; each
daemon start sweeps away computers whose bot is archived or gone, finishing deletes that an exit cut short.

| IPC method | Does |
|---|---|
| `sandbox.availability` | `Availability`: Windows probes Windows Hypervisor Platform without admin; other platforms say `unsupported-os`. Without pinned runtime assets it is never ready (`runtime-missing`). |
| `sandbox.status {botId}` | the bot's `Status` (`off` when it has no computer) |
| `sandbox.setup.enable` | starts `orole-sandbox-setup.exe` elevated (one UAC prompt), audited as `sandbox.setup`. Answers with the result, or `running: true` after 25 s while the prompt is still open. |
| `sandbox.runtime.install` | starts the pinned QEMU and image download in the background and returns its `RuntimeState`; called again while it runs, it returns progress. Fails with "no pinned computer runtime" in a build without pins. |

Events: `sandbox.status`, `sandbox.availability`, `sandbox.runtime`, `sandbox.setup`. Setup and download run with
the daemon's lifetime, not the call's, and only for the user: a context acting for a bot or an automation is refused.
A download has no overall time limit (QEMU and the image total ~1.6 GB; the setup screen states the size computed
from the pins) but fails when nothing arrives for 2 minutes; each install first removes `.staging-*` folders a
killed daemon left in `runtime\` and `images\`.

### Storage

`<data>\sandbox\runtime\<version>\` (QEMU), `images\<version>\` (base image), `bots\<bot>.qcow2` (overlay),
`run\<16 hex>.sock` (guest channel; the name is a hash of the bot id so the path stays under the 107-character AF_UNIX limit) with `.qmp` (QEMU monitor) and `.tok` (the per-boot token, only while QEMU starts) next to it, `<data>\logs\sandbox-<bot>.log`.
The overlay is created with `qemu-img create -F qcow2 -b <base>` on first boot only, never recreated, and a bot keeps
booting the kernel, initrd, cmdline and base of the image version recorded for it while that version is installed; when it is
gone the boot fails with `ErrImageGone` instead of booting the overlay against another base. Bot ids are checked against
`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` before they become file names.

### Security notes

- No network listener on the host; the guest channel is a local stream. QEMU connects out to two AF_UNIX sockets the
  daemon listens on (guest channel and QMP) and opens no listener of its own.
- `<data>\sandbox\run\` gets an explicit protected DACL (the user and SYSTEM only), once per daemon run, because
  `%LOCALAPPDATA%\Orole` inherits read access for other local groups; the sockets inherit it. The token reaches QEMU
  in a file there with its own user-only DACL (`-fw_cfg name=opt/orole/token,file=`), never in argv; the file is
  deleted once QMP answers a command that is only allowed after machine initialization (QEMU has read its fw_cfg
  files by then) and on every failed boot, and the token is never logged.
- QEMU runs in a job object with kill-on-close, so VMs never outlive the daemon.
- The setup helper is always `orole-sandbox-setup.exe` next to `orole-daemon.exe`, never a path from IPC or
  settings. That folder is writable by the user, so before the elevated start the daemon opens the helper itself
  (refusing a symbolic link or other reparse point) denying writes, renames and deletes (of it and its folders), checks its SHA-256 against `sandbox.SetupHelperSHA256` (embedded at build time by
  `scripts/build.ps1` and `scripts/build-dev-daemon.ps1`, the mygo `devCommand`), and keeps that handle until the
  elevated process has started. A daemon built without the hash (`go run`, tests) refuses with "setup helper not
  verified" unless `OROLE_SANDBOX_SETUP_UNVERIFIED=1` is set; a mismatch is always refused.
- The VM is the security boundary. The token authenticates the daemon's connection to the guest. In the guest,
  `orole-guest` runs as root and is the only process that can read the token (fw_cfg sysfs files are 0400 root)
  or open the channel (`/dev/virtio-ports/org.orole.guest.0` is 0600 root by a udev rule); commands and terminals
  run as the unprivileged `bot`, which has no sudo. `/files/read` and `/files/write` run in a helper process
  (`orole-guest` re-executed with a hidden argument) as `bot`, so they reach exactly what the bot's own commands
  can: no root-owned paths, no writes through symlinks to them. Size limits are enforced by the agent, not the helper.
- Commands, terminals and file helpers run in `orole-guest`'s cgroup: restarting `orole-guest` (crash or
  update) kills the bot's running commands and terminals.
- Inside the guest, nftables rejects everything to 10.0.2.2 and fec0::2 (QEMU user networking's aliases for the
  host's loopback) except DHCP renewals from port 68, which only root can bind.
- The guest has open internet and can reach the home network (printers, NAS, router pages, other PCs); only the host's
  own loopback is blocked, and only for `bot` (guest root could bypass it). Phase 4 states this in the UI.
- Since image 0.1.3, nftables also rejects TCP connects from uid 1000 (`bot`: its commands, and Chromium's own pages)
  to 127.0.0.0/8 and ::1 ports 9222 (Chromium DevTools) and 5900 (wayvnc). Only `orole-guest` (root) connects to them,
  so the bot cannot drive the browser or read the screen around the tools' approvals and the takeover lock. Chromium,
  cage and wayvnc still run as `bot`, so this closes the ports, not same-user access to those processes, their files
  or the Wayland socket.

### Guest image

`guest/flake.nix` builds a NixOS system (nixpkgs pinned in `guest/flake.lock`, version in `guest/VERSION`) into
`vmlinuz`, `initrd`, `cmdline` (`init=<toplevel>/init` plus kernel parameters; the host appends `console=ttyS0
panic=-1`), `base.qcow2` (compressed qcow2, ext4 labelled `nixos` without a partition table, grown to the overlay's
size on boot) and `SHA256SUMS`. `nix build ./guest#image-debug` is the same with root autologin on the serial console.
Building it: `docs/development/guest-image.md`.

Facts later phases rely on: user `bot` (uid 1000, primary group `users` gid 100, home `/home/bot`, no sudo,
locked password); root's password is locked too and there is no sshd, so the image has no login method at all
(`users.allowNoPasswordLogin = true`; the debug image's serial-console autologin is the only way in); Chromium in cage
(wlroots headless backend, pixman) with CDP on `127.0.0.1:9222` and its profile in `/home/bot/.config/orole-chromium`;
wayvnc on `127.0.0.1:5900` (from 0.1.3 only root can connect to either); `orole-guest` as a root systemd unit after module loading, restarted forever;
`qemu_fw_cfg` loaded in the initrd; Python 3 + uv, Node.js, git, curl, jq, ripgrep; nix-ld and envfs so downloaded
Linux binaries and `#!/bin/bash` scripts run; fontconfig with DejaVu and Noto for Chromium. `orole-guest` reads its token from fw_cfg
(`/sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw`) and falls back to `orole.token=` on the kernel command line.

#### Guest image checks

Run on 2026-10-15 on the developer PC (Windows 11 Pro 21H2, build 22000, Hyper-V running, 32 GB RAM, unelevated)
with `TestRealGuestImageThroughTheManager` (`internal/sandbox/e2e_windows_test.go`): the pinned `guest-v0.1.3` and
`qemu-v20260811` assets (draft releases at the time, read with `gh`) installed through the real `Runtime` (size and
SHA-256 against `DefaultPins`, unzip) from a local stand-in for the release server, then booted through the real
`Manager` and host backend (`-accel whpx,kernel-irqchip=off`, 2048 MB, 2 vCPU). Every command below ran as `bot`
through `orole-guest`. Two full runs on `guest-v0.1.0`, one on `guest-v0.1.1` and one on `guest-v0.1.2` (the first
image with the browser and screen relay: `orole-guest` with chromedp v0.14.2), same results; times from the
`guest-v0.1.2` run. A run on `guest-v0.1.3` (draft, Actions run 38740342706: the CDP/VNC firewall for `bot`, the
password-field refusal and the attach deadline) passed every row again with similar times (boot 9.6 s, CDP up 4.1 s
after ready) and the rows marked 0.1.3 below.

| Check | Result |
|---|---|
| Boot to guest ready (`Manager.Ensure`, fresh overlay) | 8.1 s; `systemd-analyze`: 0.99 s kernel + 3.03 s initrd + 3.17 s userspace = 7.18 s |
| Boot to ready again: clean restart / after a hard kill / a second bot | 7.6 s / 7.5 s / 7.8 s |
| Clean shutdown (agent power-off, then QMP) | 1.8 s |
| QEMU process RAM (host) | one bot: working set 510 MB, private 2139 MB; with two bots running (the first after the browser checks): 1305 MB and 524 MB working set, 2136 MB and 2132 MB private (the guest touches its RAM lazily; private bytes are the 2 GB reservation) |
| Guest memory (`free -m`) | 477 MB used of 1969 MB, idle with Chromium open |
| `id -un` | `bot` |
| Services (`systemctl is-active cage-tty1 wayvnc orole-guest`) | active, active, active |
| Chromium CDP (`curl -s 127.0.0.1:9222/json/version`; from 0.1.3 `ss -ltn`, since bot can no longer connect) | up 3.2 s after guest ready, `Chrome/155.0.8059.79` |
| 0.1.3: bot connects to `127.0.0.1:9222`, `[::1]:9222`, `127.0.0.2:9222` (curl, bash `/dev/tcp`) | refused (`curl` exit 7, `Connection refused`), no DevTools JSON |
| 0.1.3: bot connects to `127.0.0.1:5900`, `[::1]:5900` (bash `/dev/tcp`; the image has no `nc`) | refused; `Guest.Browser` and `Guest.OpenScreen` (root `orole-guest`) still work in the same run |
| wayvnc (`ss -ltn`) | listening on `127.0.0.1:5900` only, 46 ms after guest ready |
| Fonts (`fc-list`) | 921 faces (DejaVu, Noto) |
| fw_cfg token / channel device | `400 root` / `600 root`; `cat` of the token as bot fails |
| Host loopback (`curl http://10.0.2.2:<port>` to a Go listener on the host's 127.0.0.1) | refused at once (`curl: (7)`, the nftables reject); the listener accepted nothing |
| Internet (`curl -sI https://example.com`) | HTTP 200 |
| Tools as bot | Python 3.14.7, Node v24.21.0, uv 0.13.0, git 2.55.0, `bash -c` works |
| Disk (`df -h /`) | `/dev/vda` 9.8 G, 4.1 G used (ext4 grown to the 10 GB overlay on first boot) |
| Browser `open https://example.com` (`Guest.Browser`, chromedp v0.14.2 attaching to the visible tab) | 0.50 s, title `Example Domain` (no CDP decode errors) |
| Browser `read` / `screenshot` | 3 ms, text has example.com's paragraph (its 2026 page has no "Example Domain" heading and no link) / 160 ms, valid 1280x630 PNG, 59.8 KB |
| Browser `click` on a missing target | 6 ms, `502: nothing visible on the page matches "#orole-missing"` |
| Browser `click text=More information` (same-tab link on a page served from the guest's loopback) | 64 ms, the result already reports the new page |
| Browser `click` on a `target=_blank` link | 137 ms; the result and the next `read` drive the new front tab |
| 0.1.3: `type` into a text field / a password field (`#pw` and `text=Password`) | 62 ms, typed / refused in 2-7 ms: "that is a password field, and you never type passwords: call ask_user_to_take_over…" |
| Screen relay (`Guest.OpenScreen`, wayvnc) | banner `RFB 003.008` 1 ms after opening; RFB 3.8 handshake with security None, framebuffer 1280x720 |
| Long streams: `/screen` and `/pty` open and idle for 65 s | both still work (a FramebufferUpdate after the idle, a shell command echoes); the guest's `http2.Server` sets no timeouts and the host transport only pings (15 s read idle, 10 s ping timeout) |
| File write through `/files/write` | owner `bot`; read back after a clean restart |
| Hard kill (`TerminateProcess`, what the job object does), then boot on the same overlay | boots; a file written and `sync`ed before the kill is there; the manager reports the kill as a crash |
| Two bots at once on one base image | both run; separate homes; `base.qcow2` size and mtime unchanged |
| Guest tokens in the console logs (`sandbox-<bot>.log`) | none (3.2 KB and 0.8 KB of log) |
| Test binary killed with `taskkill /F` mid-run (a crashed daemon; `guest-v0.1.0` run) | its QEMU was gone 3 s later (job object, kill on close) |

Not checked here yet: input through wayvnc and glyphs on screen (phase 4, the viewer), the NixOS report's
package criteria (numpy/pandas through uv, a native npm module, a downloaded Node binary through nix-ld).

### Testing

`go test -race ./internal/sandbox/... ./cmd/orole-guest/...` runs everywhere: the fake backend serves the real
guest handler over `net.Pipe`, and the test binary doubles as the guest's shell (`guestagent/agenttest`). The
Linux CI lane also runs the real `/bin/sh` exec tests and the real PTY test, and builds `orole-guest`; the tests that
switch to another user (`TestRunAsSwitchesUser`, `TestFilesRunAsBot`) need root and skip otherwise.
On Windows the QEMU backend is tested against a fake QEMU (it connects the sockets and answers QMP like the real
one); `TestRealBootConnectsTheGuestAgent` boots real QEMU with the spike's Alpine kernel and `orole-guest` and talks h2c
to it when `OROLE_SPIKE_QEMU` and `OROLE_SPIKE_ALPINE` are set. `TestRealGuestImageThroughTheManager` boots the pinned
release image end to end (`$env:OROLE_SANDBOX_E2E = "1"`; it fetches the pinned assets with `gh` into
`%TEMP%\orole-realboot` once, or `OROLE_SANDBOX_E2E_ASSETS`), with the checks in "Guest image checks".

`DefaultPins` (`internal/sandbox/pins.go`) is generated: `./scripts/sandbox-pins.ps1 -QemuTag qemu-v<date> -GuestTag
guest-v<version>` reads both releases (drafts too) with `gh`, takes each hash from the release's `SHA256SUMS`, checks it
against GitHub's own asset digest, and writes the file. The pinned URLs
(`https://github.com/rayorole/orole-os-desktop/releases/download/<tag>/<name>`) work for users only once both releases
are published.

## Bot tools (phase 3)

A saved bot with `BotConfig.computer` (bot settings › Tools: "Has its own computer"; off by default, never exported)
gets these tools (`internal/chat/tools/computer.go`, `browser.go`, `takeover.go`, wired in
`internal/daemon/svc_chat_tools.go` through `computerRuntime` in `internal/daemon/computer_tools.go`). Builder drafts
never get them, and neither do models without tool calling. Every call except `ask_user_to_take_over` goes through
`policy.Engine` as `bot:<id>`, so the bot's tool access level (ask/read/write/all) and stored rules apply as for any
other tool:

| Tool | Action | Resource | Effect |
|---|---|---|---|
| `computer_run(command, timeout?, cwd?)` | `computer.exec` | `computer/<botID>` | write |
| `computer_read_file(path)` | `computer.files.read` | `computer/<botID>/<absolute path>` | read |
| `computer_write_file(path, content)` | `computer.files.write` | `computer/<botID>/<absolute path>` | write |
| `browser_open(url)` | `computer.browser.open` | `computer/<botID>/browser/<host>` | **write** |
| `browser_read()`, `browser_screenshot()` | `computer.browser.read` | `computer/<botID>/browser` | read |
| `browser_click(target)`, `browser_type(target, text)` | `computer.browser.write` | `computer/<botID>/browser` | write |
| `ask_user_to_take_over(reason)` | none (it only asks the user) | - | - |

**Deviation from the plan table.** The plan listed `browser_open` as `computer.browser.read` on
`computer/<botID>/browser`. Opening a page carries the user's logged-in sessions (the bot's browser keeps its
profile), so an unprompted open followed by a read is a way to move data out. It is therefore a write, on a resource
that names the host (lowercased, punycode, no port or user info): the user is asked once per new site and can choose
"always allow" for that host only. `browser_read` and `browser_screenshot` stay reads.

- **What the per-site approval covers.** Only the address the bot passes to `browser_open`. Links it clicks,
  redirects, pages opened in new tabs and `computer_run` (curl, a script) can reach any other host without a new
  prompt. Bots whose tool access is "read and change" or "everything" are never asked at all (the policy auto-approves
  writes for them).
- **`browser_open` refuses** (typed addresses only; a link or redirect to such an address is not checked) `localhost` and `*.localhost`, and literal loopback, private, link-local, CGNAT
  (100.64.0.0/10), "this network" and unspecified IPs, including the VM's gateway 10.0.2.2, IPv6 forms that carry such
  an IPv4 address (NAT64, 6to4, IPv4-compatible) and every spelling a browser would read as one (`127.1`, `0x7f.1`,
  `2130706433`, fullwidth or ideographic-dot Unicode forms, IPv6 zones such as `%25eth0` and any number of trailing
  dots; hosts are mapped with UTS-46 first). **Not caught:** a
  public hostname whose DNS answer is a private address. The computer can reach the user's home network (the guest
  firewall only blocks the host's own loopback), so the per-host approval is the guard there.
- **Approvals show exactly what runs.** The prompt carries the whole command (at most 1800 bytes: the approval detail
  limit minus 200), file paths in canonical absolute form (relative paths start in `/home/bot`; the policy resource
  uses the same path), a 300-byte preview of file content, and the full URL, target or typed text. Commands, URLs,
  targets and text containing control or invisible characters (carriage return, escape, zero-width, bidi marks,
  invalid UTF-8) are refused instead of being shown differently from what would run; newlines and tabs are allowed in
  commands and typed text.
- **Write, then run.** A `computer_run` approval shows the command (`python3 job.py`), not the content of a script
  the bot wrote before; the `computer_write_file` approval for that script showed only its first 300 bytes. Approving
  a run of a file the bot wrote means trusting the whole file.
- **Passwords.** `browser_type` refuses when the resolved field is an `input[type=password]` (checked in the guest)
  and tells the bot to ask the user to take over. The check does not see through shadow DOM, so a password field inside a custom web component is not detected (gap).
- **Results are untrusted.** Command output, file contents, page text, screenshots and guest error text are
  `Untrusted` (fenced by the engine). The model sees at most 64 KiB per command or file and 90 KiB of page text, with
  a note on how to get the rest; binary files are summarised. URLs must be absolute http(s); targets are a CSS
  selector or `text=<visible text>` (`wire.ValidateBrowser`, checked by the tool and again by the guest).
- **Screenshots.** `browser_screenshot` returns the PNG as `ToolResult.Images`. A vision model gets it in a transient
  user message after the tool results, for the latest step only (earlier ones become a text placeholder); it is never
  stored or logged. Other models get a text note and should use `browser_read`.
- **Limits.** Computer bots default to 30 steps and 60 tool calls per turn (`computerMaxSteps`, `computerMaxToolCalls`
  in `internal/chat/engine.go`) unless the bot sets its own. `computer_read_file`, `browser_read` and
  `browser_screenshot` are exempt from the repeated-identical-call guard, because the file or page changes between
  calls. For the other `computer_*` and `browser_*` tools the guard counts only identical calls in a row (the third
  in a row stops the turn), so a step repeated between others (click Next, read, click Next) is allowed; other tools
  still stop at the third identical call anywhere in the turn.
- **Deadlines.** Every guest call has one: a command's timeout (default 120 s, max 600 s) plus 30 s, files 60 s,
  browser 90 s. Starting the VM is bounded separately at 100 s (boot, busy wait, slack). The guest bounds a page load
  at 60 s and other browser steps at 20 s.
- **Archived or missing bots are refused** before anything boots ("you no longer exist, so you have no computer").
- **Card.** Each reply gets one compact card part (`type: "computer"`, `id: "computer"`, `name` the last tool,
  `status` running|waiting|done|failed, `text` the human line, `summary` the outcome), updated through
  `RunContext.Emit`; it says "Starting the computer…" while the VM boots. Errors map to plain messages: not set up,
  failed to start, stopped unexpectedly, all computers busy, low memory, the user is controlling the computer, an
  image too old for this feature. Phase 3 publishes no activity event; the Computer tab's live echo of commands is
  phase 4.
- **What the user sees today.** The Tools switch, the card in chat and the "Needs you" notification. The Computer
  tab, where the user watches the screen and presses Take over and Done, arrives with phase 4; until then
  `sandbox.takeover` and `sandbox.release` have no button.

### Takeover

`ask_user_to_take_over(reason)` (reason at most 200 characters, plain single-line text) waits in `sandbox.Handoffs`
(`internal/sandbox/handoff.go`): up to `DefaultHandoffAsk` (5 minutes, the same as `policy.DefaultApprovalTimeout`)
for the user to press Take over, then until Done, at most `DefaultHandoffHold` (30 minutes). The tool texts take both
numbers from those constants. The tool makes sure the computer runs first, so there is a screen to take over.
Phone-call turns cannot ask for a takeover. Untrusted content cannot start one: only the user's click takes over.

- **Notification.** While the bot waits the daemon publishes `sandbox.handoff` (`HandoffState{botId, pending, reason,
  since}`, also answered by IPC `sandbox.handoff{botId}`) and keeps one "Needs you" notification open (ref
  `computer-takeover:<botId>`, target `computer:<botId>`, which opens the bot). It resolves however the wait ends.
  Handoffs do not survive a daemon restart, so the notification list drops such entries on start.
- **IPC.** `sandbox.takeover{botId}` takes the lock (`Manager.TakeOver`; the computer must be running),
  `sandbox.release{botId, note?}` releases it and resumes the bot with "The user finished: <note>" (note at most 500
  characters, control and invisible characters stripped). `sandbox.takeover`, `sandbox.release` and `sandbox.handoff`
  are **desktop-only**: the daemon refuses them from any other IPC client, so neither the CLI nor a bot can use them.
  Take over and Done are audited as actor `user` (`computer.takeover`, `computer.release`).
- **Lock lifetime.** Whether the user holds the computer is decided under one mutex shared by the Take over and Done
  handlers and the start of a wait, so a wait never starts from a stale answer. A VM stop or crash ends the wait (the
  bot is told the computer stopped) and drops the lock. The lock is also released, so the bot's browser works again,
  when the hold timeout passes or the bot's turn is cancelled, including when the user started the takeover by hand
  with no request pending.
- **While taken over.** `computer_run`, `computer_write_file` and the browser tools are refused before any approval
  prompt, again after one, and by the daemon adapter after the VM starts. `computer_read_file` is allowed. A guest
  call already in flight finishes first (a command started before the takeover keeps running, and so does anything it
  left running in the background). The bot's user cannot connect to Chromium's DevTools port or wayvnc at all (guest
  nftables, image 0.1.3), so a command cannot connect to the DevTools port or the VNC port. Same-uid processes can still use the Wayland socket (screencopy), Chromium's ProcessSingleton socket (to open URLs in the running browser) and wayvnc's control socket; the fix is running Chromium, cage and wayvnc as a separate user (follow-up issue).
- **Signing in.** What the user signs in to stays in the browser profile on the bot's computer, and the bot can use
  those sessions afterwards. The profile's cookie files are owned by `bot`, so the bot's own commands can read them
  too (and use them outside the browser). Each new host the bot types still needs approval unless the user chose
  "always allow" for it. There is no UI to wipe the browser profile yet; the only way is to delete the computer.

### Guest browser and screen

`cmd/orole-guest` serves `POST /browser` with chromedp attached to the visible Chromium tab over CDP on
`127.0.0.1:9222` inside the guest (open waits for the load event; read returns title, URL and the visible text capped
at 90 KiB; screenshot is a viewport PNG, at most 5 MiB), and `POST /screen` as a full-duplex relay to the guest's VNC
server on `127.0.0.1:5900`. Neither port is reachable from the host except through the authenticated channel, nor
(from image 0.1.3) by the bot's own user inside the guest. Both routes need guest image 0.1.2; a computer still on an
older image answers 501 and the bot is told to ask the user to reset it. The viewer that uses `/screen` is phase 4.
Attaching chromedp to a tab has its own 20 s deadline: an attach that hangs is given up (and filed later if it ever
finishes) instead of blocking every following action. Image 0.1.3 adds the password-field refusal and that deadline.

## Windows backend (phase 2)

- **Process.** `backend_windows.go` starts `qemu-system-x86_64.exe -machine q35 -accel whpx,kernel-irqchip=off`
  (`qemu.go`, `qemuArgs`) as a child of the daemon, assigned to a job object with `KILL_ON_JOB_CLOSE`
  (`job_windows.go`). VMs stop with the daemon and a daemon crash kills them (checked: `taskkill /F` of the
  host process took its QEMU down within 3 s). Running commands do not survive; disks do. The command line is
  never logged without `redactArgs`, and QEMU's own output is kept only as a token-redacted tail.
- **Why `kernel-irqchip=off`.** On build 22000 with Hyper-V running, plain `-accel whpx` fails
  (`Failed to enable nested virtualization`, see the spike). The software `tcg` accelerator is only a
  configuration fallback.
- **Disk.** `<data>\sandbox\images\<version>\base.qcow2` (compressed, ext4, read-only backing file shared by all
  bots) and a per-bot overlay `<data>\sandbox\bots\<bot>.qcow2` made with `qemu-img create -F qcow2 -b` on first
  boot (10 GB virtual). Boot is direct: `vmlinuz`, `initrd` and the `cmdline` file of the same image folder.
- **Guest channel.** virtio-serial port `org.orole.guest.0`; its QEMU chardev *connects* to an AF_UNIX socket the
  daemon listens on in `<data>\sandbox\run\<16 hex of sha256(bot)>.sock`. That folder has an explicit protected
  user-only DACL (user and SYSTEM) set by the daemon, because the data folder's inherited ACL is not user-only.
  QEMU opens no listener. A second socket (`.qmp`) carries QMP, used for `system_powerdown` and as the signal that
  machine initialization is done. The path uses a hash of the bot id to stay under the 107-character AF_UNIX limit.
- **Token.** 32 random bytes per boot, written to a user-only file in `run\` and handed to QEMU as
  `-fw_cfg name=opt/orole/token,file=`; the file is deleted once QMP answers a command that is only valid after
  machine initialization, and on every failed boot. It is never in argv and never on the kernel command line,
  because the serial console is logged to `<data>\logs\sandbox-<bot>.log` (checked: no token in the logs). In the
  guest it is a root-only sysfs file read by `orole-guest`.
- **Stop.** `VM.Shutdown`: QMP ACPI power-down, then systemd powers off and QEMU exits (`-no-reboot`); after the
  deadline the process is killed. An exit the daemon did not ask for is `ErrCrashed`.
- **Network.** `-netdev user` (slirp NAT). The guest has open internet **and can reach the home LAN**. QEMU maps
  10.0.2.2 and fec0::2 to the host's loopback; the guest's nftables rejects both (DHCP renewals aside), so a bot
  cannot reach services listening on the host's 127.0.0.1, including the daemon's IPC. The rule binds the `bot` user
  (uid 1000, no sudo); guest root is only `orole-guest` and is not restricted.
- **What runs as whom in the guest.** `orole-guest` is a root service (it must read the token and open the
  channel). `/exec`, `/pty` and `/files/*` run as `bot`, so the bot reaches exactly what its own commands can.
- **Availability** (`availability.go`, `probes_windows.go`, no admin). `WHvCreatePartition` succeeds: ready (or
  `runtime-missing` while no pinned runtime is installed). Otherwise the reason: Windows build below 19041
  (`unsupported-os`); a servicing or Orole reboot marker (`reboot-required`); the `HypervisorPlatform` optional
  feature (WMI `Win32_OptionalFeature`) not enabled (`whp-disabled`); else `virtualization-off`.
  `WinHvPlatform.dll` exists even with the feature off, so its presence is never used.
- **Setup.** `sandbox.setup.enable` runs `orole-sandbox-setup.exe enable-whp` through `ShellExecuteExW` with verb
  `runas`: one UAC prompt, only for the user (never for a bot or automation). The helper takes that one fixed
  argument, exits 740 when not elevated, and runs `System32\Dism.exe /Online /Enable-Feature
  /FeatureName:HypervisorPlatform /All /NoRestart` by full path; 3010 means restart. It sits next to
  `orole-daemon.exe`, a user-writable folder, so before the elevated start the daemon opens it (refusing a
  symbolic link or other reparse point) denying writes, renames and deletes, compares its SHA-256 with the hash
  embedded at build time and keeps the handle until the elevated process has started. A build without the hash
  refuses ("setup helper not verified"). The daemon itself is never elevated.
- **Runtime.** `runtime.go` downloads `qemu-v<build>` (a repackaged subset of the official QEMU for Windows) and
  `guest-v<version>` (vmlinuz, initrd, cmdline, base.qcow2) from this repository's GitHub releases, checks every
  file's size and SHA-256 against `pins.go` while streaming, unpacks into a `.staging-*` folder and renames it into
  place with a `.complete` marker written last. A mismatch or a partial download leaves nothing. The release assets
  are produced by `.github/workflows/qemu-asset.yml` and `guest-image.yml` as **draft** releases; the owner
  publishes them after review, and until then the pinned URLs return 404 and the install fails.
  `scripts/sandbox-pins.ps1` regenerates `pins.go` from the releases (drafts included) and cross-checks each hash
  against GitHub's asset digest.
- **Guest image** (`guest/`, NixOS): see "Guest image" above. It is built in CI or locally through WSL
  (`docs/development/guest-image.md`).
- **IPC.** `sandbox.availability`, `sandbox.status`, `sandbox.setup.enable`, `sandbox.runtime.install` and the
  events listed under "Daemon wiring". Archiving a bot stops its VM and deletes its overlay and row.
- **Licences.** QEMU is GPL-2.0 and the guest image contains GPL software (the Linux kernel among it). The QEMU
  zip carries `SOURCE.txt` and the licence files of the installer; the corresponding source of the image is
  reproducible from the pinned nixpkgs revision. Both are recorded in `docs/research/upstream-attribution.md`.

## Phase 2 spike (WHPX on build 22000)

Run on 2026-10-15 on the developer PC (Windows 11 Pro 21H2, build 22000, Hyper-V running), unelevated. Re-run with
`go test -tags spike -run TestSpike -v ./internal/sandbox` (see the header of `internal/sandbox/spike_windows_test.go`).
The boot test uses the production `qemuArgs` builder (token via `-fw_cfg name=opt/orole/token,file=`), with only the
serial console moved from a file to a socket so the test can drive the guest's `/bin/sh`.

| Question | Result |
|---|---|
| QEMU build | `qemu-20260811-win64.zip` from draft release `qemu-v20260811` (SHA-256 `513d950c…54bf1b`, matches `SHA256SUMS`; unmodified files of `qemu-w64-setup-20260811.exe`, SHA-512 `5bcf9eed…f543`). `qemu-system-x86_64 --version`: `QEMU emulator version 11.1.0 (v11.1.0-12130-ge470268ff4)`. Accelerators: tcg, whpx |
| Alpine netboot 3.24.2 SHA-256 (`dl-cdn.alpinelinux.org/alpine/v3.24/releases/x86_64/netboot-3.24.2/`) | vmlinuz-virt `40f620bc8c93d952e57dd8dfc0f94fca1759d192a4fc4a260705d50ca378559c`, initramfs-virt `c990c63e4602aa84b92d7df54fd180cb0e56590d61b71221ba6d60e913d26357`, modloop-virt `1e7a3eea707d2ecb3d0502f97fb4dc4dcbac1fe06075e24ae19bfdf4343bb1cf` (kernel 6.18.52-0-virt) |
| Probes, this PC (Hyper-V role on, HypervisorPlatform feature reads disabled) | build=22000, dll_loads=true, hypervisor_present=true, create_partition=ok (no error), firmware_virtualization=false (Windows reports false inside the Hyper-V root partition), install_state=2 (disabled), cbs_reboot_pending=false |
| Probes, WHP enabled before / after reboot | not run: the feature was not changed (agents never change Windows features), and `WHvCreatePartition` already succeeds with it disabled |
| `-accel whpx` (default) | **fail**: `WHPX: Failed to enable nested virtualization, hr=80370302` then `failed to initialize whpx: Invalid argument`. Same with `whpx,hyperv=off` and `whpx,kernel-irqchip=on` |
| `-accel whpx,kernel-irqchip=off` | **ok** (also with `,hyperv=off`). QEMU 11.1 only asks WHP for nested virtualization on the in-kernel irqchip path, which build 22000 rejects |
| Direct kernel boot (`-kernel/-initrd`), boot to shell | whpx,kernel-irqchip=off cpu=host: ok, shell after 1.69 s; cpu=max: ok, 1.71 s. tcg cpu=max: ok, 3.20 s. Whole run (boot, all checks, poweroff): 6.5 s / 6.4 s / 8.5 s |
| AF_UNIX chardev (QEMU connects to a Go `net.Listen("unix")`) | ok, all three runs (guest channel, QMP and console sockets connected 32-34 ms after start; QMP greeting received) |
| virtio-serial port name `org.orole.guest.0` visible in guest | ok (`/dev/vport3p1`, `/sys/class/virtio-ports/vport3p1/name`) |
| guest→host / host→guest bytes | ok / ok, all three runs |
| `-fw_cfg name=opt/orole/token,file=<path>` readable in guest | ok: `/sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw` holds the token (needs the `qemu_fw_cfg` module; Alpine ships it in modloop, not the netboot initramfs). Token not in QEMU argv |
| `-chardev pipe` DACL | `O:<user SID>D:(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;<user SID>)(A;;FR;;;WD)(A;;FR;;;AN)`; Everyone/Anonymous ACE: **yes** (read) |
| Guest reaches host 127.0.0.1 via 10.0.2.2 (`-netdev user`) | yes (HTTP GET from guest reached a Go listener on 127.0.0.1) |
| Guest internet via `-netdev user` | yes (`wget http://example.com`) |
| `poweroff -f` with `-no-reboot` ends QEMU | yes (exit status 0) |
| `%LOCALAPPDATA%\Orole` ACL (socket files inherit it) | inherited: SYSTEM (F), BUILTIN\Administrators (F), the user (F), and a local group `CodexSandboxUsers` (RX) inherited from the profile. Not user-only |

Decisions: `-accel whpx,kernel-irqchip=off` on Windows (Task 3: `defaultAccel`); `-cpu host` (`defaultCPU`; `max`
also works); channel: AF_UNIX (the pipe chardev grants Everyone and Anonymous read, so it is not a fallback without
changing QEMU); host loopback: reachable, so the guest's nftables rules block 10.0.2.2 (Task 8). The
data folder's ACL is inherited, not user-only, so the backend sets an explicit protected DACL (the user and SYSTEM
only) on `<data>\sandbox\run\` (`acl_windows.go`) instead of relying on inheritance.
