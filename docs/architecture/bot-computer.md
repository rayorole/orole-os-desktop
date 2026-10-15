# Bot computer: a local VM per bot

Every chat bot can have its own small Linux computer on the user's PC: its own disk, a terminal and (later) a
visible browser. Design: `docs/superpowers/specs/2026-10-15-bot-computer-design.md`. Plans:
`docs/superpowers/plans/2026-10-15-bot-computer*.md`.

**Status: phase 2 (Windows backend).** The manager, store, guest protocol and `orole-guest` (phase 1) now run on
a real QEMU backend on Windows, with the runtime download, the setup helper and the NixOS guest image, all
checked end to end on build 22000 (see "Guest image checks"). The pinned release assets are drafts until the owner
publishes them, so a user's build cannot download them yet (the pinned URLs answer 404). Nothing is visible in the
app: bot tools come in phase 3, the Computer tab and its setup screens in phase 4.

**A bot's computer lives in the daemon: it survives GUI restarts, not a daemon exit or reboot. Its disk persists;
running commands do not.**

## Architecture

```
 bot tool call (phase 3)
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
  evicted, and (phase 3) bot browser tools are refused.
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
| `POST /screen`, `POST /browser` | 501 until phases 3-4 | |
| `POST /poweroff` | 202, then a clean power off | |

The host never trusts the guest: answers over 8 MiB are refused, exec output is cut to 1 MiB and file content to
4 MiB again on the host, and everything a bot receives from the guest is untrusted data (phase 3 wraps it).

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
wayvnc on `127.0.0.1:5900`; `orole-guest` as a root systemd unit after module loading, restarted forever;
`qemu_fw_cfg` loaded in the initrd; Python 3 + uv, Node.js, git, curl, jq, ripgrep; nix-ld and envfs so downloaded
Linux binaries and `#!/bin/bash` scripts run; fontconfig with DejaVu and Noto for Chromium. `orole-guest` reads its token from fw_cfg
(`/sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw`) and falls back to `orole.token=` on the kernel command line.

#### Guest image checks

Run on 2026-10-15 on the developer PC (Windows 11 Pro 21H2, build 22000, Hyper-V running, 32 GB RAM, unelevated)
with `TestRealGuestImageThroughTheManager` (`internal/sandbox/e2e_windows_test.go`): the pinned `guest-v0.1.1` and
`qemu-v20260811` assets (draft releases at the time, read with `gh`) installed through the real `Runtime` (size and
SHA-256 against `DefaultPins`, unzip) from a local stand-in for the release server, then booted through the real
`Manager` and host backend (`-accel whpx,kernel-irqchip=off`, 2048 MB, 2 vCPU). Every command below ran as `bot`
through `orole-guest`. Two full runs on `guest-v0.1.0` and one on `guest-v0.1.1`, same results; times from the
`guest-v0.1.1` run.

| Check | Result |
|---|---|
| Boot to guest ready (`Manager.Ensure`, fresh overlay) | 8.1 s; `systemd-analyze`: 1.02 s kernel + 3.00 s initrd + 3.22 s userspace = 7.24 s |
| Boot to ready again: clean restart / after a hard kill / a second bot | 7.6 s / 7.4 s / 7.9 s |
| Clean shutdown (agent power-off, then QMP) | 1.8 s |
| QEMU process RAM (host) | one bot: working set 503 MB, private 2130 MB; with two bots running: 1276 MB and 507 MB working set, 2418 MB and 2132 MB private (the guest touches its RAM lazily; private bytes are the 2 GB reservation) |
| Guest memory (`free -m`) | 461 MB used of 1969 MB, idle with Chromium open |
| `id -un` | `bot` |
| Services (`systemctl is-active cage-tty1 wayvnc orole-guest`) | active, active, active |
| Chromium CDP (`curl -s 127.0.0.1:9222/json/version`) | up 4.2 s after guest ready, `Chrome/155.0.8059.79` |
| wayvnc (`ss -ltn`) | listening on `127.0.0.1:5900` only, 33 ms after guest ready |
| Fonts (`fc-list`) | 921 faces (DejaVu, Noto) |
| fw_cfg token / channel device | `400 root` / `600 root`; `cat` of the token as bot fails |
| Host loopback (`curl http://10.0.2.2:<port>` to a Go listener on the host's 127.0.0.1) | refused at once (`curl: (7)`, the nftables reject); the listener accepted nothing |
| Internet (`curl -sI https://example.com`) | HTTP 200 |
| Tools as bot | Python 3.14.7, Node v24.21.0, uv 0.13.0, git 2.55.0, `bash -c` works |
| Disk (`df -h /`) | `/dev/vda` 9.8 G, 4.1 G used (ext4 grown to the 10 GB overlay on first boot) |
| File write through `/files/write` | owner `bot`; read back after a clean restart |
| Hard kill (`TerminateProcess`, what the job object does), then boot on the same overlay | boots; a file written and `sync`ed before the kill is there; the manager reports the kill as a crash |
| Two bots at once on one base image | both run; separate homes; `base.qcow2` size and mtime unchanged |
| Guest tokens in the console logs (`sandbox-<bot>.log`) | none (3.2 KB and 0.8 KB of log) |
| Test binary killed with `taskkill /F` mid-run (a crashed daemon; `guest-v0.1.0` run) | its QEMU was gone 3 s later (job object, kill on close) |

Not checked here yet: input through wayvnc and glyphs on screen (phase 3, the screen relay), the NixOS report's
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
