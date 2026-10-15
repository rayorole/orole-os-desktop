# Bot computer phase 2: Windows backend, runtime, setup and guest image Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The daemon can boot one QEMU/WHPX VM per bot from a downloaded, hash-pinned NixOS image, tell the UI whether this PC can run it, and enable Windows Hypervisor Platform with a single UAC prompt.

**Architecture:** `internal/sandbox` gets a Windows `Backend` (`backend_windows.go`) that runs `qemu-system-x86_64.exe -accel whpx` as a child in a kill-on-close job object. The guest channel is a virtio-serial port whose QEMU chardev connects to an AF_UNIX socket the daemon listens on under `<data>\sandbox\run\`; a second AF_UNIX socket carries QMP for ACPI power-down. A `Runtime` downloads a repackaged QEMU zip and the guest image (kernel, initrd, kernel command line, compressed qcow2 base) from GitHub releases of this repo and checks every file against SHA-256 values pinned in Go. A separate elevated `orole-sandbox-setup.exe` enables the `HypervisorPlatform` feature via DISM. The guest is a NixOS flake under `guest/`, built in Linux CI (or WSL) into a `guest-v*` release; QEMU is repackaged by a Windows workflow into a `qemu-v*` release. `internal/daemon/svc_sandbox.go` wires it all to IPC.

**Tech Stack:** Go 1.27 (stdlib, `golang.org/x/sys/windows`, `golang.org/x/sys/windows/registry`), QEMU for Windows (Stefan Weil's build `qemu-w64-setup-20260811.exe`), NixOS (nixpkgs `7fd4004d0511991fff3a0524856a9be09e28c5f0`, `make-disk-image.nix`, cage, wayvnc, Chromium), GitHub Actions (`windows-latest`, `ubuntu-latest`, `cachix/install-nix-action`), PowerShell 7.

**Spec:** `docs/superpowers/specs/2026-10-15-bot-computer-design.md` · Master plan (shared contracts): `docs/superpowers/plans/2026-10-15-bot-computer.md` · Background: `docs/research/Local agent micro VM sandboxes.md`, `docs/research/NixOS vs Debian agent VMs.md`

**Issue / branch / PR:** #197 · `feat/197-sandbox-windows` cut from `feat/196-sandbox-core` · draft PR with base `feat/196-sandbox-core`, body ends `Closes #197`.

---

## Contract issues

These are places where the master plan's shared contracts (or the phase 2 brief) cannot be used as written. Each says what this plan does instead. Nothing else deviates.

1. **`Paths.Socket` can exceed the AF_UNIX path limit.** `sun_path` is 108 bytes including the NUL on Windows and Linux. `Root\run\<uuid>.sock` is 88 characters plus the user name under `%LOCALAPPDATA%`, and 120+ characters under a dev `OROLE_HOME` such as `C:\code\projects\.orole-worktrees\orole-os\196-sandbox-core\.orole-dev`. Task 2 keeps the method name and signature but changes the file name to the first 16 hex characters of `sha256(botID)`, and adds `MaxSocketPath`/`CheckSocketPath` so a too-long data dir fails with a readable error instead of a bind failure.
2. **Token on the kernel command line leaks into the log.** The kernel prints its command line to `ttyS0`, which is written to `logs/sandbox-<bot>.log` (Global Constraint: secrets never in logs). The backend passes `BootSpec.Token` with `-fw_cfg name=opt/orole/token,string=<token>` instead (root-only in the guest at `/sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw`, never printed). Task 9 adds `cmd/orole-guest/token_linux.go` (`bootToken()`), which reads fw_cfg first and falls back to `orole.token=` on `/proc/cmdline`, and switches `orole-guest` to it. The `BootSpec` type is unchanged.
3. **`VM.Shutdown` cannot call `Guest.PowerOff`.** The VM only has the raw `Conn()`. The Manager's h2c client already owns that stream, and a second HTTP/2 client on the same connection would corrupt it. The Windows VM powers off with QMP `system_powerdown` (ACPI power button, which systemd turns into a clean poweroff) over a second AF_UNIX socket (`qmpSocket(Paths.Socket(id))`, same directory, `.qmp` suffix). It waits for exit until `ctx` is done, then kills. The Manager may still call `Guest.PowerOff` first; both paths end in the same exit.
4. **`ImageSet`/`BootSpec` carry no kernel command line.** A NixOS direct kernel boot needs `init=/nix/store/<hash>-nixos-system-…/init`, and that value changes with every image build. The image ships a fourth file, `cmdline`, next to `vmlinuz`. The backend reads `filepath.Join(filepath.Dir(spec.Kernel), "cmdline")`. It is pinned and hashed like the other files. No contract type changes.
5. **There is no hard bot delete.** `chat.bots.archive` (`chat.Manager.ArchiveBot`) is the only delete and there is no unarchive. Task 12 calls `Manager.Delete` after a successful archive.
6. **Additions to the IPC/event contract** (new names, nothing renamed): events `sandbox.availability` (payload `sandbox.Availability`) and `sandbox.runtime` (payload `sandbox.RuntimeState`). New exported types: `SetupResult`, `RuntimeState`, `Runtime`, `Pins`, `Asset`, `QEMUFiles`; the constructor `NewHostBackend(rt *Runtime, p Paths) Backend`. `sandbox.runtime.install` starts the download in the background and returns the current `RuntimeState` (calling it again while it runs returns progress instead of starting a second download).
7. **Base image format.** The base is a compressed qcow2 (`make-disk-image` format `qcow2-compressed`, ext4, no partition table), not raw/erofs. QEMU reads compressed clusters directly, so the host needs no decompression step. GitHub release assets are limited to 2 GiB, and the workflow refuses anything larger. Overlays are created with `qemu-img create -f qcow2 -F qcow2 -b <base>`.
8. **Named pipe vs AF_UNIX.** Issue #197's text says "virtio-serial on a user-only named pipe". The Global Constraints and the spec put AF_UNIX first and the pipe second. This plan builds AF_UNIX. The spike (Task 1) records QEMU's pipe-chardev DACL. If AF_UNIX does not work on build 22000, execution **stops** and the owner decides; it does not switch silently.
9. **Assumptions about phase 1** (verify while running Task 6/12; if one is false, fix it in phase 1's code in a separate commit on this branch and say so in the PR):
   - (a) The Manager builds `BootSpec.LogPath` as `Paths.Log(filepath.Dir(cfg.Paths.Root), botID)`, since `Config` has no data dir.
   - (b) `Manager.Delete` works when no VM is running: it removes the overlay file and the store row.
   - (c) `orole-guest` opens `/dev/virtio-ports/org.orole.guest.0` by default and runs as root.
   - (d) `fakeBackend` in `fake_test.go` does not define `NewHostBackend`, `hostBackend`, `qemuVM` or `tailBuffer`.
10. **Release drafts.** Following `release.yml`, the two asset workflows create **draft** releases and the owner publishes them by hand. Draft assets cannot be downloaded anonymously, so `DefaultPins` (Task 11) only works once the owner has published both drafts. Pushing a `qemu-v*`/`guest-v*` tag publishes repository content, so the executor asks the owner before each push.

---

## Global Constraints

Copied from the master plan (every task's requirements include these):

- Daemon owns VMs, state and processes; the GUI process never starts QEMU or talks to a guest directly (AGENTS.md).
- Permissions are decided in Go via `policy.Engine.Check`; no frontend-only checks.
- No listener on a network interface on the host. Host↔guest uses a local AF_UNIX socket under `<data>\sandbox\run\` (data dir is 0700 / user-only) or a user-only-DACL named pipe; nothing else.
- Platform code only in `_windows.go` / `_unix.go` files or `internal/platform/*`, behind interfaces; non-Windows builds return `NotImplemented`.
- Secrets never in SQLite, logs or the frontend. The per-VM guest token is generated per boot and held in memory only.
- Guest output, page text and file contents reach bots as `ToolResult{Untrusted: true}`.
- Defaults: 2048 MB RAM, 2 vCPU, 10 GB overlay per bot; max 4 running VMs; idle stop 15 min (no bot activity and no viewer); boot timeout 60 s; busy wait 30 s; refuse boot when free host RAM < VM RAM + 1024 MB.
- Downloads are SHA-256 verified against values pinned in Go source; mismatches delete the file.
- The daemon is never elevated; only `cmd/orole-sandbox-setup` requests UAC.
- Tests: `go test -race ./...` clean; `bun run --cwd frontend check` clean. Table/behaviour tests, fake backend for anything needing a VM.
- Commits: conventional, lowercase scope (`feat(sandbox): ...`), one per task, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Copied upstream code (noVNC, etc.) recorded in `docs/research/upstream-attribution.md`.
- Docs and CHANGELOG updated in the PR that changes behaviour (phase 4 holds the user-facing docs; earlier phases update `docs/architecture/bot-computer.md` as they go).
- No native HTML `title` attributes in UI; hover text via `Tooltip` only where needed.

Phase 2 specifics:

- QEMU is never given `hostfwd`, `-vnc`, `-spice`, `-gdb`, `telnet`, `tcp:` or `server=on`: the daemon listens and QEMU connects.
- QEMU never gets `-accel whpx,ssd=off`. It speeds up MMIO, but the QEMU docs say it weakens security mitigations.
- QEMU argv contains the token (fw_cfg). The argv is never logged unredacted (`redactArgs`), and QEMU's stderr tail is redacted before it goes into an error.
- Agents executing this plan must not change Windows features or reboot the PC. When a step needs WHP enabled, the owner runs the elevated command.
- Use the dev data dir for any manual daemon run: `$env:OROLE_HOME = "$PWD\.orole-dev"`. Never touch `%LOCALAPPDATA%\Orole`.

## Review Focus

The five inputs most likely to hurt a user that the spec implies but does not spell out. Each has a test in the task that owns the code.

1. **QEMU exits before it connects** (WHPX unavailable, missing DLL, Hyper-V conflict). Expected: `Boot` returns `ErrBootFailed` within seconds, with QEMU's stderr tail (redacted), no 60 s hang and no socket files left. Test: Task 6 `TestBootFailsFastWhenQEMUExitsBeforeConnecting`.
2. **Interrupted, truncated or tampered download.** Expected: nothing is installed, the staging folder is removed, `Ready()` stays false and a retry starts clean. Tests: Task 5 `TestInstallRejectsAHashMismatchAndLeavesNothingBehind`, `TestInstallRejectsATruncatedDownload`.
3. **Daemon killed while a VM runs, then a new boot on the same overlay.** Expected: the job object kills QEMU, and the next boot opens the overlay with no "locked" error and the files written before the kill still present. Tests: Task 11 `TestHostBackendBootsTheGuestImage` (kill + reboot same overlay), plus the manual `taskkill /F` check in Task 11.
4. **User declines UAC or DISM fails.** Expected: a clear error, no reboot marker, Availability unchanged. A reboot marker is ignored after the PC has actually restarted. Tests: Task 7 `TestSetupOutcomeMapsHelperExitCodes`, `TestRebootMarkerOnlyCountsForTheCurrentBoot`, and the helper's `TestRunNeedsElevationBeforeDISM`.
5. **Long data dir path** (`OROLE_HOME` deep in a worktree, long Windows user names). Expected: a clear "path too long" error naming the path, never a cryptic bind failure. Test: Task 2 `TestCheckSocketPathRejectsLongDataDirs`.

Also covered: the token never appears in QEMU argv logging, the stderr tail or the console log (Task 3 `TestRedactArgsHidesTheToken`, Task 3 `TestTailBufferRedactsTheToken`, Task 11 log check), and bot archive removes the overlay (Task 12 `TestArchivingABotDeletesItsComputer`).

## File structure

| Path | Responsibility |
|---|---|
| `internal/sandbox/spike_windows_test.go` | build tag `spike`: manual WHPX/AF_UNIX/pipe/loopback/probe spike (Task 1) |
| `internal/sandbox/paths.go` (modify) | short hashed socket names, `MaxSocketPath`, `CheckSocketPath` |
| `internal/sandbox/qemu.go` | pure QEMU argv builder, `qmpSocket`, `redactArgs`, `tailBuffer` |
| `internal/sandbox/availability.go` | pure availability decision from host probes, `parseInstallState` |
| `internal/sandbox/probes_windows.go` | WHP / firmware / feature / reboot probes (no admin) |
| `internal/sandbox/runtime.go`, `pins.go` | pinned QEMU + image download, verify, extract; `DefaultPins` |
| `internal/sandbox/qmp.go` | minimal QMP client: handshake + `system_powerdown` |
| `internal/sandbox/job_windows.go` | kill-on-close job object (same pattern as `internal/agents/job_windows.go`) |
| `internal/sandbox/backend_windows.go` | `hostBackend`, `qemuVM`, process start, overlay creation, socket accept |
| `internal/sandbox/backend_other.go` | `NewHostBackend` returning an unsupported backend |
| `internal/sandbox/setup.go`, `setup_windows.go`, `setup_other.go` | UAC launch of the helper, exit-code mapping, reboot marker |
| `internal/sandbox/setupcode/setupcode.go` | exit codes shared by helper and daemon |
| `internal/sandbox/e2e_windows_test.go` | env-gated real boot of the guest image |
| `cmd/orole-sandbox-setup/` | elevated helper: DISM enable HypervisorPlatform |
| `cmd/orole-guest/token_linux.go` | read the boot token from fw_cfg |
| `guest/` | NixOS flake: `flake.nix`, `configuration.nix`, `debug.nix`, `orole-guest.nix`, `VERSION` |
| `.github/workflows/qemu-asset.yml`, `guest-image.yml` | build and draft-release the runtime assets |
| `scripts/sandbox-pins.ps1` | generate `internal/sandbox/pins.go` from the published releases |
| `internal/daemon/svc_sandbox.go` | Manager construction, IPC handlers, shutdown, bot-archive hook |
| `mygo.json`, `scripts/build.ps1` (modify) | build and bundle `orole-sandbox-setup.exe` |
| `docs/architecture/bot-computer.md`, `docs/development/guest-image.md`, `docs/security/threat-model.md`, `docs/architecture/security-permissions.md`, `docs/development/packaging.md`, `docs/research/upstream-attribution.md` | docs |

---

### Task 1: Spike: WHPX direct boot, AF_UNIX chardev, pipe DACL, host loopback, probes

Time box: half a day. The output is a results table in `docs/architecture/bot-computer.md` plus a committed, build-tagged spike test that anyone can re-run. The decision gates at the end decide whether execution continues.

**Files:**
- Create: `internal/sandbox/spike_windows_test.go`
- Create or modify: `docs/architecture/bot-computer.md` (section "Phase 2 spike (WHPX on build 22000)")

**Interfaces:**
- Consumes: nothing from earlier tasks; it uses only `guestPortName`, which is defined in this file under the `spike` tag and again in Task 3. Because both files are in package `sandbox`, Task 3 deletes the spike's copy (see Task 3 Step 3).
- Produces: values later tasks rely on, recorded in the doc: the `-cpu` model (`defaultCPU`, Task 3), AF_UNIX works yes/no (gate), fw_cfg readable yes/no (gate), host loopback reachable yes/no (Task 8 nftables rule), the probe values per WHP state (Task 4 decision table).

- [ ] **Step 1: Create the branch**

```powershell
git switch feat/196-sandbox-core
git pull --ff-only
git switch -c feat/197-sandbox-windows
```

- [ ] **Step 2: Fetch QEMU and the Alpine netboot kernel into fresh, empty folders (downloads are untrusted data)**

```powershell
$spike = Join-Path $env:TEMP "orole-spike"
New-Item -ItemType Directory -Force "$spike\dl-qemu", "$spike\qemu", "$spike\alpine" | Out-Null
$exe = "$spike\dl-qemu\qemu-w64-setup-20260811.exe"
curl.exe -sSfL -o $exe https://qemu.weilnetz.de/w64/qemu-w64-setup-20260811.exe
$h = (Get-FileHash $exe -Algorithm SHA512).Hash.ToLower()
if ($h -ne "5bcf9eed634e8575a37b74f445af41a2fe4106da512d0c30c368301d4c105037fdfab40a5287367a28a957624cddebbc8c07e16c88ab6634f554cdf3d16bf543") { throw "QEMU installer SHA-512 mismatch: $h" }
# 7-Zip is not installed on the dev PC; use a portable copy (7zr.exe is the official standalone binary).
curl.exe -sSfL -o "$spike\dl-qemu\7zr.exe" https://www.7-zip.org/a/7zr.exe
& "$spike\dl-qemu\7zr.exe" x $exe "-o$spike\qemu" -y | Out-Null
Test-Path "$spike\qemu\qemu-system-x86_64.exe"   # True
foreach ($f in "vmlinuz-virt", "initramfs-virt") {
  curl.exe -sSfL -o "$spike\alpine\$f" "https://dl-cdn.alpinelinux.org/alpine/v3.24/releases/x86_64/netboot-3.24.2/$f"
}
Get-FileHash "$spike\alpine\*" -Algorithm SHA256 | Format-Table Hash, Path   # record in the doc
```

If `7zr.exe` cannot unpack the NSIS installer (it only handles .7z), ask the owner to approve a one-off `winget install --id 7zip.7zip` and use `"C:\Program Files\7-Zip\7z.exe"` instead. Do not run the QEMU installer itself.

- [ ] **Step 3: Write the spike test**

`internal/sandbox/spike_windows_test.go`:

```go
//go:build windows && spike

// Phase 2 spike (issue #197). Not part of the normal test run; run by hand on a Windows PC:
//
//	$env:OROLE_SPIKE_QEMU   = "$env:TEMP\orole-spike\qemu"    # folder with qemu-system-x86_64.exe
//	$env:OROLE_SPIKE_ALPINE = "$env:TEMP\orole-spike\alpine"  # vmlinuz-virt, initramfs-virt
//	go test -tags spike -run TestSpike -v -timeout 15m ./internal/sandbox
//
// Every finding is logged as a "SPIKE RESULT" line; copy them into docs/architecture/bot-computer.md.
package sandbox

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const guestPortName = "org.orole.guest.0" // Task 3 moves this into qemu.go and deletes this line

func spikeDirs(t *testing.T) (qemuDir, alpineDir string) {
	t.Helper()
	qemuDir, alpineDir = os.Getenv("OROLE_SPIKE_QEMU"), os.Getenv("OROLE_SPIKE_ALPINE")
	if qemuDir == "" || alpineDir == "" {
		t.Skip("set OROLE_SPIKE_QEMU and OROLE_SPIKE_ALPINE")
	}
	return qemuDir, alpineDir
}

// spikeTempDir is short on purpose: AF_UNIX paths are limited to 107 bytes.
func spikeTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "osp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestSpikeHostProbes prints what an unelevated process can learn about WHP. Run it with WHP off, then
// again after the owner enabled it and rebooted.
func TestSpikeHostProbes(t *testing.T) {
	_, _, build := windows.RtlGetNtVersionNumbers()
	t.Logf("SPIKE RESULT probe_build=%d", build&0xffff)

	whp := windows.NewLazySystemDLL("WinHvPlatform.dll")
	t.Logf("SPIKE RESULT probe_whp_dll_loads=%v", whp.Load() == nil)
	if getCap := whp.NewProc("WHvGetCapability"); getCap.Find() == nil {
		var buf [16]byte
		var written uint32
		hr, _, _ := getCap.Call(0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&written)))
		t.Logf("SPIKE RESULT probe_hypervisor_present=%v hr=0x%08X", binary.LittleEndian.Uint32(buf[:4]) != 0, uint32(hr))
	}
	if create := whp.NewProc("WHvCreatePartition"); create.Find() == nil {
		var h uintptr
		hr, _, _ := create.Call(uintptr(unsafe.Pointer(&h)))
		t.Logf("SPIKE RESULT probe_create_partition_hr=0x%08X", uint32(hr))
		if int32(uint32(hr)) >= 0 {
			whp.NewProc("WHvDeletePartition").Call(h)
		}
	}
	isPF := windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessorFeaturePresent")
	r, _, _ := isPF.Call(21) // PF_VIRT_FIRMWARE_ENABLED
	t.Logf("SPIKE RESULT probe_firmware_virtualization=%v", r != 0)

	winDir, _ := windows.GetSystemWindowsDirectory()
	ps := filepath.Join(winDir, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	out, err := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command",
		`(Get-CimInstance -ClassName Win32_OptionalFeature -Filter "Name='HypervisorPlatform'").InstallState`).CombinedOutput()
	t.Logf("SPIKE RESULT probe_feature_install_state=%q err=%v", strings.TrimSpace(string(out)), err)

	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, registry.QUERY_VALUE)
	if err == nil {
		k.Close()
	}
	t.Logf("SPIKE RESULT probe_cbs_reboot_pending=%v", err == nil)
}

// serialShell drives the guest's /bin/sh on the serial console.
type serialShell struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	n    int
}

func (s *serialShell) readUntil(marker string, timeout time.Duration) string {
	s.t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
	var buf bytes.Buffer
	for !strings.Contains(buf.String(), marker) {
		b, err := s.r.ReadByte()
		if err != nil {
			s.t.Fatalf("serial: waiting for %q: %v\n--- console so far ---\n%s", marker, err, buf.String())
		}
		buf.WriteByte(b)
	}
	return buf.String()
}

// run executes one shell line and returns everything printed up to its end marker (the echoed command
// included). The marker is split by quotes in the command, so the echo of the command never matches it.
func (s *serialShell) run(cmd string, timeout time.Duration) string {
	s.t.Helper()
	s.n++
	if _, err := fmt.Fprintf(s.conn, "%s; echo __END\"\"%d__\n", cmd, s.n); err != nil {
		s.t.Fatal(err)
	}
	return s.readUntil(fmt.Sprintf("__END%d__", s.n), timeout)
}

func TestSpikeWHPXDirectBootAndUnixSocketChannel(t *testing.T) {
	qemuDir, alpineDir := spikeDirs(t)
	for _, cpu := range []string{"host", "max"} {
		t.Run("cpu="+cpu, func(t *testing.T) { spikeBoot(t, qemuDir, alpineDir, cpu) })
	}
}

func spikeBoot(t *testing.T, qemuDir, alpineDir, cpu string) {
	dir := spikeTempDir(t)
	guestPath, conPath := filepath.Join(dir, "g.sock"), filepath.Join(dir, "c.sock")
	guestLn, err := net.Listen("unix", guestPath)
	if err != nil {
		t.Fatalf("SPIKE RESULT af_unix_listen=fail: %v", err)
	}
	defer guestLn.Close()
	conLn, err := net.Listen("unix", conPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conLn.Close()

	// A service on host loopback, standing in for any local service: can the guest reach it via 10.0.2.2?
	hits := make(chan string, 4)
	hostLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostLn.Close()
	go http.Serve(hostLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits <- r.URL.Path; fmt.Fprint(w, "host") }))
	hostPort := hostLn.Addr().(*net.TCPAddr).Port

	args := []string{
		"-nodefaults", "-no-user-config", "-no-reboot",
		"-machine", "q35", "-accel", "whpx", "-cpu", cpu, "-m", "512", "-smp", "2",
		"-display", "none", "-vga", "none", "-L", filepath.Join(qemuDir, "share"),
		"-kernel", filepath.Join(alpineDir, "vmlinuz-virt"), "-initrd", filepath.Join(alpineDir, "initramfs-virt"),
		"-append", "console=ttyS0 rdinit=/bin/sh",
		"-fw_cfg", "name=opt/orole/token,string=spike-token-123",
		"-netdev", "user,id=net0", "-device", "virtio-net-pci,netdev=net0,romfile=",
		"-device", "virtio-serial-pci,id=vser",
		"-chardev", "socket,id=guest,path=" + guestPath,
		"-device", "virtserialport,bus=vser.0,chardev=guest,name=" + guestPortName,
		"-chardev", "socket,id=con,path=" + conPath, "-serial", "chardev:con",
	}
	var stderr bytes.Buffer
	cmd := exec.Command(filepath.Join(qemuDir, "qemu-system-x86_64.exe"), args...)
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	accept := func(ln net.Listener, what string) net.Conn {
		got := make(chan net.Conn, 1)
		go func() {
			if c, err := ln.Accept(); err == nil {
				got <- c
			}
		}()
		select {
		case c := <-got:
			return c
		case err := <-exited:
			stopped = true
			t.Fatalf("SPIKE RESULT cpu=%s %s_connect=fail: qemu exited: %v\n%s", cpu, what, err, stderr.String())
		case <-time.After(20 * time.Second):
			t.Fatalf("SPIKE RESULT cpu=%s %s_connect=fail: timeout\n%s", cpu, what, stderr.String())
		}
		return nil
	}
	con := accept(conLn, "console")
	defer con.Close()
	guest := accept(guestLn, "guest")
	defer guest.Close()
	t.Logf("SPIKE RESULT cpu=%s af_unix_chardev=ok connected_after=%s", cpu, time.Since(start))

	sh := &serialShell{t: t, conn: con, r: bufio.NewReader(con)}
	sh.readUntil("#", 60*time.Second)
	t.Logf("SPIKE RESULT cpu=%s shell_after=%s", cpu, time.Since(start))
	sh.run("mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev", 10*time.Second)

	out := sh.run(`for p in /sys/class/virtio-ports/*; do echo PORT:$(basename $p):$(cat $p/name); done`, 10*time.Second)
	port := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PORT:vport") && strings.HasSuffix(line, ":"+guestPortName) {
			port = strings.Split(line, ":")[1]
		}
	}
	if port == "" {
		t.Fatalf("SPIKE RESULT virtio_port_name=fail\n%s", out)
	}
	t.Logf("SPIKE RESULT virtio_port_name=ok device=/dev/%s", port)

	sh.run("echo ping-from-guest > /dev/"+port, 10*time.Second)
	_ = guest.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(guest).ReadString('\n')
	t.Logf("SPIKE RESULT guest_to_host=%q err=%v", strings.TrimSpace(line), err)

	sh.run("(head -n1 /dev/"+port+" > /tmp/in &)", 5*time.Second)
	time.Sleep(time.Second)
	fmt.Fprint(guest, "pong-from-host\n")
	time.Sleep(time.Second)
	out = sh.run("cat /tmp/in", 5*time.Second)
	t.Logf("SPIKE RESULT host_to_guest_ok=%v", strings.Contains(out, "pong-from-host"))

	out = sh.run("modprobe qemu_fw_cfg 2>&1; cat /sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw 2>&1", 10*time.Second)
	t.Logf("SPIKE RESULT fw_cfg_token_ok=%v", strings.Contains(out, "spike-token-123"))

	sh.run("modprobe virtio_net 2>&1; ip link set eth0 up && ip addr add 10.0.2.15/24 dev eth0 && ip route add default via 10.0.2.2; echo nameserver 10.0.2.3 > /etc/resolv.conf", 15*time.Second)
	out = sh.run(fmt.Sprintf("wget -q -T 5 -O - http://10.0.2.2:%d/from-guest 2>&1; echo RC=$?", hostPort), 15*time.Second)
	reached := false
	select {
	case <-hits:
		reached = true
	case <-time.After(2 * time.Second):
	}
	t.Logf("SPIKE RESULT host_loopback_reachable=%v", reached)
	out = sh.run("wget -q -T 10 -O /dev/null http://example.com 2>&1; echo RC=$?", 20*time.Second)
	t.Logf("SPIKE RESULT internet_ok=%v", strings.Contains(out, "RC=0"))

	fmt.Fprint(con, "poweroff -f\n")
	select {
	case err := <-exited:
		stopped = true
		t.Logf("SPIKE RESULT cpu=%s poweroff_exit=%v total=%s", cpu, err, time.Since(start))
	case <-time.After(15 * time.Second):
		t.Logf("SPIKE RESULT cpu=%s poweroff_exit=timeout", cpu)
	}
}

// TestSpikePipeChardevDACL records the DACL QEMU puts on a `-chardev pipe` (the fallback channel).
func TestSpikePipeChardevDACL(t *testing.T) {
	qemuDir, _ := spikeDirs(t)
	name := fmt.Sprintf("orole-spike-%d", os.Getpid())
	cmd := exec.Command(filepath.Join(qemuDir, "qemu-system-x86_64.exe"),
		"-nodefaults", "-no-user-config", "-machine", "q35", "-accel", "whpx", "-display", "none", "-S",
		"-device", "virtio-serial-pci,id=vser", "-chardev", "pipe,id=guest,path="+name,
		"-device", "virtserialport,bus=vser.0,chardev=guest,name="+guestPortName)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pipe := `\\.\pipe\` + name
	var sd *windows.SECURITY_DESCRIPTOR
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if sd, err = windows.GetNamedSecurityInfo(pipe, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("SPIKE RESULT pipe_sddl=unknown: %v\n%s", err, stderr.String())
	}
	s := sd.String()
	t.Logf("SPIKE RESULT pipe_sddl=%s", s)
	t.Logf("SPIKE RESULT pipe_everyone_or_anonymous_ace=%v", strings.Contains(s, ";;;WD)") || strings.Contains(s, ";;;AN)"))
}
```

- [ ] **Step 4: Run the probes with WHP still off (the dev PC's current state)**

Run: `go test -tags spike -run TestSpikeHostProbes -v ./internal/sandbox`
Expected: PASS with `SPIKE RESULT probe_*` lines. On 2026-10-15 the dev PC reported `InstallState` = 2 (disabled) and `WinHvPlatform.dll` **present** in System32. So "DLL loads" is not a usable signal; record whether `WHvCreatePartition` fails here.

- [ ] **Step 5: Ask the owner to enable WHP (agents must not change Windows features)**

Message to the owner: "The spike needs Windows Hypervisor Platform. Please run in an elevated PowerShell: `dism /online /enable-feature /featurename:HypervisorPlatform /all /norestart`, note its exit code (3010 = restart needed), then run `go test -tags spike -run TestSpikeHostProbes -v ./internal/sandbox` once *before* restarting (records the reboot-pending state), restart Windows, and tell me when it is back." After the reboot, run Step 4's command again and record the third set of probe values.

- [ ] **Step 6: Run the boot and pipe spikes**

```powershell
$env:OROLE_SPIKE_QEMU = "$env:TEMP\orole-spike\qemu"
$env:OROLE_SPIKE_ALPINE = "$env:TEMP\orole-spike\alpine"
go test -tags spike -run "TestSpikeWHPXDirectBootAndUnixSocketChannel|TestSpikePipeChardevDACL" -v -timeout 15m ./internal/sandbox
icacls "$env:LOCALAPPDATA\Orole"
```

Expected: `SPIKE RESULT` lines for both CPU models. Record `icacls` (read-only command) to document the data-dir ACL that the socket files inherit.

- [ ] **Step 7: Record the results**

Append to `docs/architecture/bot-computer.md` (create the file with a `# Bot computer` heading if phase 1 did not):

```markdown
## Phase 2 spike (WHPX on build 22000)

Run on 2026-10-.. on the developer PC (Windows 11 Pro 21H2, build 22000, Hyper-V running). Re-run with
`go test -tags spike -run TestSpike -v ./internal/sandbox` (see the header of `internal/sandbox/spike_windows_test.go`).

| Question | Result |
|---|---|
| QEMU build | qemu-w64-setup-20260811.exe (SHA-512 5bcf9eed…f543), `qemu-system-x86_64 --version`: <output> |
| Alpine netboot 3.24.2 SHA-256 | vmlinuz-virt <hash>, initramfs-virt <hash> |
| Probes, WHP off | build=<n>, dll_loads=<b>, hypervisor_present=<b>, create_partition_hr=<hex>, firmware=<b>, install_state=<n>, cbs_reboot_pending=<b> |
| Probes, WHP enabled, before reboot | … |
| Probes, WHP enabled, after reboot | … |
| `-accel whpx` direct kernel boot (`-kernel/-initrd`) | cpu=host: <ok/fail, shell after Ns>; cpu=max: <ok/fail, shell after Ns> |
| AF_UNIX chardev (QEMU connects to a Go `net.Listen("unix")`) | <ok/fail> |
| virtio-serial port name `org.orole.guest.0` visible in guest | <ok/fail> (/dev/vportXpY) |
| guest→host / host→guest bytes | <ok/fail> / <ok/fail> |
| `-fw_cfg name=opt/orole/token` readable in guest | <ok/fail> |
| `-chardev pipe` DACL | `<SDDL>`; Everyone/Anonymous ACE: <yes/no> |
| Guest reaches host 127.0.0.1 via 10.0.2.2 (`-netdev user`) | <yes/no> |
| Guest internet via `-netdev user` | <yes/no> |
| `poweroff -f` with `-no-reboot` ends QEMU | <yes/no> |
| `%LOCALAPPDATA%\Orole` ACL (socket files inherit it) | <icacls summary> |

Decisions: `-cpu <model>`; channel: AF_UNIX; host loopback: <blocked in guest by nftables for uid 1000 / not reachable>.
```

Replace each `<…>` with the logged value. Do not commit the table with any `<…>` left in it.

- [ ] **Step 8: Apply the decision gates**

- `af_unix_chardev=fail` with both CPU models → **stop**. Report to the owner with the pipe SDDL and do not start Task 6. The fallback (pipe chardev) changes the security story and needs their decision.
- Both CPU models fail to reach a shell → **stop** and report the stderr output (WHPX does not work on this PC).
- `fw_cfg_token_ok=false` → **stop** and report (contract issue 2 has no fallback that keeps the token out of the log).
- Otherwise continue. Use the first CPU model that reached the shell (`host` preferred) as `defaultCPU` in Task 3. If `host_loopback_reachable=true`, keep the nftables rule in Task 8.

- [ ] **Step 9: Commit**

```bash
git add internal/sandbox/spike_windows_test.go docs/architecture/bot-computer.md
git commit -m "test(sandbox): spike whpx direct boot and af_unix guest channel on windows

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Short socket paths

**Files:**
- Modify: `internal/sandbox/paths.go` (the `Socket` method from phase 1)
- Test: `internal/sandbox/paths_test.go` (update phase 1's `Socket` assertion if it expects `<botID>.sock`)

**Interfaces:**
- Consumes: `Paths{Root string}` (phase 1).
- Produces: `func (p Paths) Socket(botID string) string` returns `Root\run\<16 hex>.sock`; `const MaxSocketPath = 107`; `func CheckSocketPath(path string) error`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/sandbox/paths_test.go` (add `path/filepath` and `strings` to its imports if missing):

```go
func TestSocketIsShortAndStablePerBot(t *testing.T) {
	p := Paths{Root: `C:\Users\averagename\AppData\Local\Orole\sandbox`}
	a := p.Socket("0f8fad5b-d9cb-469f-a165-70867728950e")
	if a != p.Socket("0f8fad5b-d9cb-469f-a165-70867728950e") {
		t.Fatal("the socket path changes between calls")
	}
	if a == p.Socket("7c9e6679-7425-40de-944b-e07fc1f90ae7") {
		t.Fatal("two bots share a socket")
	}
	if filepath.Dir(a) != filepath.Join(p.Root, "run") || filepath.Ext(a) != ".sock" || len(filepath.Base(a)) != 16+len(".sock") {
		t.Fatalf("socket %q", a)
	}
	if err := CheckSocketPath(a); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSocketPathRejectsLongDataDirs(t *testing.T) {
	long := Paths{Root: `C:\` + strings.Repeat("d", 100) + `\sandbox`}
	err := CheckSocketPath(long.Socket("bot"))
	if err == nil || !strings.Contains(err.Error(), "too long") || !strings.Contains(err.Error(), long.Root) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/sandbox -run "TestSocketIsShortAndStablePerBot|TestCheckSocketPathRejectsLongDataDirs"`
Expected: FAIL (`undefined: CheckSocketPath`, and the name-length assertion).

- [ ] **Step 3: Implement**

In `internal/sandbox/paths.go`, replace the body of `Socket` and add the check (add `crypto/sha256`, `encoding/hex`, `fmt` imports):

```go
// MaxSocketPath is the longest AF_UNIX socket path Windows and Linux accept (sun_path is 108 bytes
// including the terminating NUL).
const MaxSocketPath = 107

// Socket is the AF_UNIX socket the VM's virtio-serial chardev connects to. The file name is a hash of
// the bot id, not the id itself, so the full path stays under MaxSocketPath for ordinary data folders.
func (p Paths) Socket(botID string) string {
	sum := sha256.Sum256([]byte(botID))
	return filepath.Join(p.Root, "run", hex.EncodeToString(sum[:8])+".sock")
}

// CheckSocketPath returns a readable error when path is too long to bind as an AF_UNIX socket.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf("sandbox: the data folder path is too long for the computer's channel (%d characters, at most %d): %s",
			len(path), MaxSocketPath, path)
	}
	return nil
}
```

If phase 1's `paths_test.go` asserts `Socket` returns `…\run\<botID>.sock`, change that assertion to `filepath.Join(p.Root, "run")` for the directory only.

- [ ] **Step 4: Run the package tests**

Run: `go test -race ./internal/sandbox/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/paths.go internal/sandbox/paths_test.go
git commit -m "fix(sandbox): keep guest socket paths under the af_unix limit

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: QEMU command line builder

**Files:**
- Create: `internal/sandbox/qemu.go`
- Test: `internal/sandbox/qemu_test.go`
- Modify: `internal/sandbox/spike_windows_test.go` (delete its `guestPortName` line)

**Interfaces:**
- Consumes: `BootSpec`, `Limits` (phase 1).
- Produces:
  - `type qemuConfig struct { Spec BootSpec; Share, CPU, Cmdline, GuestSocket, QMPSocket string }`
  - `func qemuArgs(c qemuConfig) []string`
  - `func qemuOpt(v string) string`
  - `func qmpSocket(guestSocket string) string`
  - `func redactArgs(args []string, secret string) []string`
  - `const defaultCPU = "host"` (or the spike's choice), `const guestPortName = "org.orole.guest.0"`
  - `type tailBuffer struct{...}` with `newTailBuffer(max int, secret string) *tailBuffer`, `Write`, `suffix() string`

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/qemu_test.go`:

```go
package sandbox

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func testQEMUConfig() qemuConfig {
	return qemuConfig{
		Spec: BootSpec{
			BotID: "bot-1", Limits: Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10},
			BaseImage: `C:\d\sandbox\images\0.1.0\base.qcow2`, Kernel: `C:\d\sandbox\images\0.1.0\vmlinuz`,
			Initrd: `C:\d\sandbox\images\0.1.0\initrd`, Overlay: `C:\d\sandbox\bots\bot-1.qcow2`,
			Token: strings.Repeat("ab", 32), LogPath: `C:\d\logs\sandbox-bot-1.log`,
		},
		Share: `C:\d\sandbox\runtime\20260811\share`, CPU: "host",
		Cmdline:     "init=/nix/store/abc-nixos-system/init root=/dev/disk/by-label/nixos\n",
		GuestSocket: `C:\d\sandbox\run\0123456789abcdef.sock`, QMPSocket: `C:\d\sandbox\run\0123456789abcdef.qmp`,
	}
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestQEMUArgsBootTheImageWithWHPX(t *testing.T) {
	c := testQEMUConfig()
	args := qemuArgs(c)
	for _, want := range [][2]string{
		{"-machine", "q35"}, {"-accel", "whpx"}, {"-cpu", "host"}, {"-m", "2048"}, {"-smp", "2"},
		{"-L", c.Share}, {"-display", "none"}, {"-vga", "none"},
		{"-kernel", c.Spec.Kernel}, {"-initrd", c.Spec.Initrd},
		{"-append", "init=/nix/store/abc-nixos-system/init root=/dev/disk/by-label/nixos console=ttyS0 panic=-1"},
		{"-fw_cfg", "name=opt/orole/token,string=" + c.Spec.Token},
		{"-drive", `if=none,id=root,format=qcow2,cache=writeback,discard=unmap,file=` + c.Spec.Overlay},
		{"-device", "virtio-blk-pci,drive=root"},
		{"-netdev", "user,id=net0"}, {"-device", "virtio-net-pci,netdev=net0,romfile="},
		{"-device", "virtio-rng-pci"}, {"-device", "virtio-serial-pci,id=vser"},
		{"-chardev", "socket,id=guest,path=" + c.GuestSocket},
		{"-device", "virtserialport,bus=vser.0,chardev=guest,name=org.orole.guest.0"},
		{"-chardev", "socket,id=qmp,path=" + c.QMPSocket}, {"-mon", "chardev=qmp,mode=control"},
		{"-chardev", "file,id=con,append=on,path=" + c.Spec.LogPath}, {"-serial", "chardev:con"},
		{"-name", "orole-bot-1"},
	} {
		if !hasPair(args, want[0], want[1]) {
			t.Errorf("missing %s %s in\n%q", want[0], want[1], args)
		}
	}
	for _, flag := range []string{"-nodefaults", "-no-user-config", "-no-reboot"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	for i, a := range args {
		if a == "-append" && strings.Contains(args[i+1], c.Spec.Token) {
			t.Fatal("the token is on the kernel command line, which the guest console prints")
		}
	}
}

func TestQEMUArgsEscapeCommasInPaths(t *testing.T) {
	c := testQEMUConfig()
	c.Spec.Overlay = `C:\Users\a,b\bot.qcow2`
	if !hasPair(qemuArgs(c), "-drive", `if=none,id=root,format=qcow2,cache=writeback,discard=unmap,file=C:\Users\a,,b\bot.qcow2`) {
		t.Fatal("comma in the overlay path is not doubled")
	}
}

func TestQEMUArgsNeverOpenANetworkListener(t *testing.T) {
	joined := strings.Join(qemuArgs(testQEMUConfig()), " ")
	for _, bad := range []string{"hostfwd", "tcp:", "-vnc", "-spice", "-gdb", "telnet", "server=on", "ssd=off"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv contains %q", bad)
		}
	}
}

func TestQMPSocketSitsNextToTheGuestSocket(t *testing.T) {
	if got := qmpSocket(`C:\d\run\0123456789abcdef.sock`); got != `C:\d\run\0123456789abcdef.qmp` {
		t.Fatal(got)
	}
}

func TestRedactArgsHidesTheToken(t *testing.T) {
	c := testQEMUConfig()
	red := redactArgs(qemuArgs(c), c.Spec.Token)
	if strings.Contains(strings.Join(red, " "), c.Spec.Token) {
		t.Fatal("token survived redaction")
	}
	if !hasPair(red, "-fw_cfg", "name=opt/orole/token,string=<redacted>") {
		t.Fatalf("%q", red)
	}
}

func TestTailBufferKeepsTheEndAndRedactsTheToken(t *testing.T) {
	tb := newTailBuffer(32, "secret-token")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(tb, "line %d\n", i)
	}
	fmt.Fprint(tb, "qemu: -fw_cfg string=secret-token: bad\n")
	s := tb.suffix()
	if strings.Contains(s, "secret-token") || !strings.HasPrefix(s, ": ") || !strings.Contains(s, "bad") || strings.Contains(s, "line 0") {
		t.Fatalf("suffix %q", s)
	}
	if newTailBuffer(32, "x").suffix() != "" {
		t.Fatal("empty buffer has a suffix")
	}
}
```

Note: `Write` redacts each chunk before storing it, so the token never sits in the buffer. A token split across two `Write` calls would not be caught by the per-chunk pass, so `suffix` redacts the joined text again.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/sandbox -run "QEMU|QMPSocket|Redact|TailBuffer"`
Expected: FAIL (`undefined: qemuArgs`, etc.)

- [ ] **Step 3: Implement**

`internal/sandbox/qemu.go`:

```go
package sandbox

import (
	"strconv"
	"strings"
	"sync"
)

// guestPortName is the virtio-serial port orole-guest opens as /dev/virtio-ports/<name>.
const guestPortName = "org.orole.guest.0"

// defaultCPU is QEMU's -cpu model under WHPX; the phase 2 spike (docs/architecture/bot-computer.md) chose it.
const defaultCPU = "host"

// qemuConfig is everything one QEMU command line needs.
type qemuConfig struct {
	Spec        BootSpec
	Share       string // QEMU firmware folder (-L), <runtime>\share
	CPU         string // -cpu model
	Cmdline     string // the image's kernel command line (the cmdline file next to the kernel)
	GuestSocket string // AF_UNIX path the virtio-serial chardev connects to (the daemon listens)
	QMPSocket   string // AF_UNIX path the QMP monitor connects to (the daemon listens)
}

// qemuArgs builds the QEMU command line for one bot VM. QEMU only ever connects out to sockets the
// daemon listens on; it opens no listener of its own. The token travels in fw_cfg, never on the
// kernel command line (the guest console, which is logged, prints that).
func qemuArgs(c qemuConfig) []string {
	s := c.Spec
	appendLine := strings.Join(strings.Fields(c.Cmdline+" console=ttyS0 panic=-1"), " ")
	return []string{
		"-nodefaults", "-no-user-config", "-no-reboot",
		"-name", qemuOpt("orole-" + s.BotID),
		"-machine", "q35", "-accel", "whpx", "-cpu", c.CPU,
		"-m", strconv.Itoa(s.Limits.MemoryMB), "-smp", strconv.Itoa(s.Limits.CPUs),
		"-L", c.Share,
		"-display", "none", "-vga", "none",
		"-kernel", s.Kernel, "-initrd", s.Initrd, "-append", appendLine,
		"-fw_cfg", "name=opt/orole/token,string=" + qemuOpt(s.Token),
		"-drive", "if=none,id=root,format=qcow2,cache=writeback,discard=unmap,file=" + qemuOpt(s.Overlay),
		"-device", "virtio-blk-pci,drive=root",
		"-netdev", "user,id=net0",
		"-device", "virtio-net-pci,netdev=net0,romfile=",
		"-device", "virtio-rng-pci",
		"-device", "virtio-serial-pci,id=vser",
		"-chardev", "socket,id=guest,path=" + qemuOpt(c.GuestSocket),
		"-device", "virtserialport,bus=vser.0,chardev=guest,name=" + guestPortName,
		"-chardev", "socket,id=qmp,path=" + qemuOpt(c.QMPSocket),
		"-mon", "chardev=qmp,mode=control",
		"-chardev", "file,id=con,append=on,path=" + qemuOpt(s.LogPath),
		"-serial", "chardev:con",
	}
}

// qemuOpt escapes a value inside a QEMU option list, where a comma is written as two commas.
func qemuOpt(v string) string { return strings.ReplaceAll(v, ",", ",,") }

// qmpSocket is the QMP socket path that belongs to a guest socket path.
func qmpSocket(guestSocket string) string { return strings.TrimSuffix(guestSocket, ".sock") + ".qmp" }

// redactArgs returns a copy of args with secret replaced, for logging.
func redactArgs(args []string, secret string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if secret != "" {
			a = strings.ReplaceAll(a, secret, "<redacted>")
		}
		out[i] = a
	}
	return out
}

// tailBuffer keeps the last max bytes written to it (QEMU's stdout and stderr), with secret removed.
type tailBuffer struct {
	mu     sync.Mutex
	max    int
	secret string
	b      []byte
}

func newTailBuffer(max int, secret string) *tailBuffer { return &tailBuffer{max: max, secret: secret} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	chunk := p
	if t.secret != "" {
		chunk = []byte(strings.ReplaceAll(string(p), t.secret, "<redacted>"))
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, chunk...)
	if len(t.b) > t.max {
		t.b = append([]byte(nil), t.b[len(t.b)-t.max:]...)
	}
	return len(p), nil
}

// suffix is ": <tail>" for an error message, or "" when QEMU printed nothing.
func (t *tailBuffer) suffix() string {
	t.mu.Lock()
	s := strings.TrimSpace(string(t.b))
	t.mu.Unlock()
	if t.secret != "" {
		s = strings.ReplaceAll(s, t.secret, "<redacted>")
	}
	if s == "" {
		return ""
	}
	return ": " + s
}
```

Set `defaultCPU` to the spike's chosen model (`"host"` or `"max"`). Delete the line `const guestPortName = "org.orole.guest.0" // Task 3 moves this…` from `spike_windows_test.go`.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/sandbox/... && go vet -tags spike ./internal/sandbox`
Expected: PASS, vet clean (the spike file still compiles with the shared constant).

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/qemu.go internal/sandbox/qemu_test.go internal/sandbox/spike_windows_test.go
git commit -m "feat(sandbox): build the qemu command line for a bot vm

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Availability without admin

**Files:**
- Create: `internal/sandbox/availability.go`
- Create: `internal/sandbox/probes_windows.go`
- Test: `internal/sandbox/availability_test.go`

**Interfaces:**
- Consumes: `Availability` (phase 1); `rebootMarkerPath`, `rebootMarkerActive` from Task 7. Task 7 has not run yet, so this task defines those two functions in `availability.go` and Task 7 imports nothing new.
- Produces:
  - `type featureState int` with `featureUnknown`, `featureEnabled`, `featureDisabled`
  - `type hostProbe struct { Build uint32; PartitionErr error; HypervisorPresent, FirmwareVirt, RebootPending bool; Feature featureState }`
  - `const minWHPXBuild = 19041`
  - `func decideAvailability(p hostProbe, runtimeReady bool) Availability`
  - `func parseInstallState(out []byte, err error) featureState`
  - `func rebootMarkerPath(p Paths) string`, `func rebootMarker(boot time.Time) []byte`, `func rebootMarkerActive(content []byte, boot time.Time) bool`
  - windows: `func probeHost(ctx context.Context, p Paths) hostProbe`

The decision uses `WHvCreatePartition`. The spike showed `WinHvPlatform.dll` exists with the feature off, so DLL presence proves nothing. Creating (and immediately deleting) an empty partition is what QEMU does first. It needs no admin, and on a ready PC it is the only probe that runs, so it is cheap.

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/availability_test.go`:

```go
package sandbox

import (
	"errors"
	"testing"
	"time"
)

func TestDecideAvailability(t *testing.T) {
	fail := errors.New("WHvCreatePartition: HRESULT 0x80370102")
	cases := []struct {
		name    string
		probe   hostProbe
		runtime bool
		ready   bool
		reason  string
	}{
		{"old windows", hostProbe{Build: 18363, PartitionErr: fail}, true, false, "unsupported-os"},
		{"ready", hostProbe{Build: 22000}, true, true, ""},
		{"partition works, runtime missing", hostProbe{Build: 22000}, false, false, "runtime-missing"},
		{"enabled, waiting for reboot", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled, RebootPending: true}, true, false, "reboot-required"},
		{"feature off", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureDisabled, HypervisorPresent: true, FirmwareVirt: true}, true, false, "whp-disabled"},
		{"feature state unknown", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureUnknown, FirmwareVirt: true}, true, false, "whp-disabled"},
		{"enabled but BIOS off", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled}, true, false, "virtualization-off"},
		{"enabled, hypervisor not launched", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled, FirmwareVirt: true}, true, false, "virtualization-off"},
		{"enabled, hypervisor up, partition refused", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled, FirmwareVirt: true, HypervisorPresent: true}, true, false, "virtualization-off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := decideAvailability(c.probe, c.runtime)
			if a.Ready != c.ready || a.Reason != c.reason {
				t.Fatalf("got %+v, want ready=%v reason=%q", a, c.ready, c.reason)
			}
			if !a.Ready && a.Detail == "" {
				t.Fatal("a reason without a detail for the setup screen")
			}
		})
	}
}

func TestDecideAvailabilityNamesTheFirmwareWhenItIsOff(t *testing.T) {
	a := decideAvailability(hostProbe{Build: 22000, PartitionErr: errors.New("x"), Feature: featureEnabled}, true)
	if a.Detail == decideAvailability(hostProbe{Build: 22000, PartitionErr: errors.New("x"), Feature: featureEnabled, FirmwareVirt: true}, true).Detail {
		t.Fatal("BIOS-off and hypervisor-not-running give the same advice")
	}
}

func TestParseInstallState(t *testing.T) {
	cases := map[string]featureState{"1\r\n": featureEnabled, "2\r\n": featureDisabled, "3": featureDisabled, "": featureUnknown, "garbage": featureUnknown}
	for out, want := range cases {
		if got := parseInstallState([]byte(out), nil); got != want {
			t.Errorf("%q: got %v want %v", out, got, want)
		}
	}
	if parseInstallState([]byte("1"), errors.New("powershell failed")) != featureUnknown {
		t.Error("an error must give featureUnknown")
	}
}

func TestRebootMarkerOnlyCountsForTheCurrentBoot(t *testing.T) {
	boot := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	m := rebootMarker(boot)
	if !rebootMarkerActive(m, boot.Add(30*time.Second)) {
		t.Fatal("marker from this boot is not active")
	}
	if rebootMarkerActive(m, boot.Add(3*time.Hour)) {
		t.Fatal("marker survives a restart")
	}
	if rebootMarkerActive([]byte("not a number"), boot) || rebootMarkerActive(nil, boot) {
		t.Fatal("unreadable marker counts")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/sandbox -run "Availability|InstallState|RebootMarker"`
Expected: FAIL (`undefined: hostProbe` …)

- [ ] **Step 3: Implement the pure decision**

`internal/sandbox/availability.go`:

```go
package sandbox

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// minWHPXBuild is Windows 10 2004, QEMU's tested WHPX baseline on x86_64.
const minWHPXBuild = 19041

type featureState int

const (
	featureUnknown featureState = iota
	featureEnabled
	featureDisabled
)

// hostProbe is what an unelevated process can learn about Windows Hypervisor Platform.
type hostProbe struct {
	Build             uint32
	PartitionErr      error // nil when WHvCreatePartition worked: QEMU can use WHPX
	HypervisorPresent bool  // WHvGetCapability(WHvCapabilityCodeHypervisorPresent)
	FirmwareVirt      bool  // IsProcessorFeaturePresent(PF_VIRT_FIRMWARE_ENABLED)
	Feature           featureState
	RebootPending     bool // Windows servicing or our own setup is waiting for a restart
}

// decideAvailability turns probes into what the Computer tab shows. Reasons are the contract's strings.
func decideAvailability(p hostProbe, runtimeReady bool) Availability {
	switch {
	case p.Build < minWHPXBuild:
		return Availability{Reason: "unsupported-os", Detail: "The computer needs Windows 10 version 2004 (build 19041) or later."}
	case p.PartitionErr == nil && runtimeReady:
		return Availability{Ready: true}
	case p.PartitionErr == nil:
		return Availability{Reason: "runtime-missing", Detail: "Download the computer (QEMU and the Linux image, about 1 GB) to continue."}
	case p.RebootPending:
		return Availability{Reason: "reboot-required", Detail: "Restart Windows to finish turning on Windows Hypervisor Platform."}
	case p.Feature != featureEnabled:
		return Availability{Reason: "whp-disabled", Detail: "Windows Hypervisor Platform is turned off. Turning it on needs administrator permission once and a restart."}
	case !p.FirmwareVirt && !p.HypervisorPresent:
		return Availability{Reason: "virtualization-off", Detail: "Virtualization is turned off in this PC's firmware (BIOS/UEFI). Turn on Intel VT-x or AMD-V (SVM) there, then restart."}
	case !p.HypervisorPresent:
		return Availability{Reason: "virtualization-off", Detail: "Windows Hypervisor Platform is on but the Windows hypervisor is not running. In an administrator terminal, `bcdedit /enum` should show `hypervisorlaunchtype Auto`; restart after changing it."}
	default:
		return Availability{Reason: "virtualization-off", Detail: "Windows refused to create a virtual machine: " + p.PartitionErr.Error()}
	}
}

// parseInstallState reads Win32_OptionalFeature.InstallState (1 enabled, 2 disabled, 3 absent).
func parseInstallState(out []byte, err error) featureState {
	if err != nil {
		return featureUnknown
	}
	switch strings.TrimSpace(string(out)) {
	case "1":
		return featureEnabled
	case "2", "3":
		return featureDisabled
	}
	return featureUnknown
}

// rebootMarkerPath is written when the setup helper reported that Windows must restart.
func rebootMarkerPath(p Paths) string { return filepath.Join(p.Root, "setup-reboot") }

// rebootMarker records the boot time of the Windows session the setup ran in.
func rebootMarker(boot time.Time) []byte { return []byte(strconv.FormatInt(boot.Unix(), 10)) }

// rebootMarkerActive reports whether a marker was written during the current boot (same boot time
// within two minutes); after a restart it no longer counts.
func rebootMarkerActive(content []byte, boot time.Time) bool {
	v, err := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
	if err != nil {
		return false
	}
	d := boot.Unix() - v
	if d < 0 {
		d = -d
	}
	return d <= 120
}
```

- [ ] **Step 4: Implement the Windows probes**

`internal/sandbox/probes_windows.go`:

```go
//go:build windows

package sandbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modWHP                        = windows.NewLazySystemDLL("WinHvPlatform.dll")
	procWHvGetCapability          = modWHP.NewProc("WHvGetCapability")
	procWHvCreatePartition        = modWHP.NewProc("WHvCreatePartition")
	procWHvDeletePartition        = modWHP.NewProc("WHvDeletePartition")
	procIsProcessorFeaturePresent = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessorFeaturePresent")
)

const (
	whvCapabilityCodeHypervisorPresent = 0
	pfVirtFirmwareEnabled              = 21
)

// probeHost runs the cheap partition check first and the slower diagnosis only when it fails.
func probeHost(ctx context.Context, p Paths) hostProbe {
	_, _, build := windows.RtlGetNtVersionNumbers()
	h := hostProbe{Build: build & 0xffff}
	if h.Build < minWHPXBuild {
		return h
	}
	if h.PartitionErr = whvCreatePartition(); h.PartitionErr == nil {
		return h
	}
	h.HypervisorPresent = whvHypervisorPresent()
	r, _, _ := procIsProcessorFeaturePresent.Call(pfVirtFirmwareEnabled)
	h.FirmwareVirt = r != 0
	h.Feature = optionalFeatureState(ctx)
	h.RebootPending = cbsRebootPending() || ourRebootPending(p)
	return h
}

func whvCreatePartition() error {
	if err := procWHvCreatePartition.Find(); err != nil {
		return fmt.Errorf("Windows Hypervisor Platform is not available: %w", err)
	}
	var partition uintptr
	hr, _, _ := procWHvCreatePartition.Call(uintptr(unsafe.Pointer(&partition)))
	if int32(uint32(hr)) < 0 {
		return fmt.Errorf("WHvCreatePartition: HRESULT 0x%08X", uint32(hr))
	}
	procWHvDeletePartition.Call(partition)
	return nil
}

func whvHypervisorPresent() bool {
	if procWHvGetCapability.Find() != nil {
		return false
	}
	var buf [16]byte // WHV_CAPABILITY union; HypervisorPresent is its first BOOL
	var written uint32
	hr, _, _ := procWHvGetCapability.Call(whvCapabilityCodeHypervisorPresent,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&written)))
	return int32(uint32(hr)) >= 0 && binary.LittleEndian.Uint32(buf[:4]) != 0
}

// optionalFeatureState asks WMI (readable without admin; DISM is not) for the HypervisorPlatform feature.
func optionalFeatureState(ctx context.Context) featureState {
	winDir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return featureUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ps := filepath.Join(winDir, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command",
		`(Get-CimInstance -ClassName Win32_OptionalFeature -Filter "Name='HypervisorPlatform'").InstallState`)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return parseInstallState(cmd.Output())
}

func cbsRebootPending() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func ourRebootPending(p Paths) bool {
	b, err := os.ReadFile(rebootMarkerPath(p))
	return err == nil && rebootMarkerActive(b, bootTime())
}

// bootTime is when this Windows session started.
func bootTime() time.Time { return time.Now().Add(-windows.DurationSinceBoot()) }
```

`parseInstallState(cmd.Output())` passes both return values; `Output` returns `([]byte, error)`, which matches the signature.

- [ ] **Step 5: Run the tests and compile for both platforms**

Run: `go test -race ./internal/sandbox/... && go vet ./internal/sandbox/... && $env:GOOS="linux"; go vet ./internal/sandbox/...; Remove-Item Env:GOOS`
Expected: PASS; vet clean on windows and linux.

- [ ] **Step 6: Check the probe against the spike's values on the dev PC**

Add this temporary check to `spike_windows_test.go` (it stays, under the `spike` tag):

```go
func TestSpikeDecision(t *testing.T) {
	p := probeHost(context.Background(), Paths{Root: t.TempDir()})
	t.Logf("SPIKE RESULT decision probe=%+v availability=%+v", p, decideAvailability(p, false))
}
```

(add `"context"` to the spike file's imports). Run: `go test -tags spike -run TestSpikeDecision -v ./internal/sandbox`
Expected: with WHP enabled and rebooted, `PartitionErr:<nil>` and `Reason:runtime-missing`. If the decision disagrees with the spike table, fix `decideAvailability` and add the failing combination to `TestDecideAvailability` before continuing.

- [ ] **Step 7: Commit**

```bash
git add internal/sandbox/availability.go internal/sandbox/availability_test.go internal/sandbox/probes_windows.go internal/sandbox/spike_windows_test.go
git commit -m "feat(sandbox): detect windows hypervisor platform without admin

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Runtime download and verification

**Files:**
- Create: `internal/sandbox/runtime.go`
- Create: `internal/sandbox/pins.go`
- Test: `internal/sandbox/runtime_test.go`

**Interfaces:**
- Consumes: `Paths.Runtime`, `Paths.Image`, `ImageSet`, `ErrNotReady` (phase 1).
- Produces:
  - `type Asset struct { Name, SHA256 string; Size int64 }`
  - `type Pins struct { QEMUVersion, QEMUTag string; QEMU Asset; ImageVersion, ImageTag string; Image []Asset }`
  - `var DefaultPins Pins` (empty until Task 11)
  - `type RuntimeState struct { Ready, Installing bool; Step string; Done, Total int64; Error string }` (JSON `ready`, `installing`, `step`, `done`, `total`, `error`)
  - `type RuntimeConfig struct { Paths Paths; Pins Pins; BaseURL string; HTTP *http.Client; OnChange func(RuntimeState) }`
  - `type QEMUFiles struct { System, Img, Share string }`
  - `func NewRuntime(cfg RuntimeConfig) *Runtime`
  - `(*Runtime) Ready() bool`, `QEMU() (QEMUFiles, error)`, `Image() (ImageSet, error)`, `State() RuntimeState`, `Start(ctx) RuntimeState`, `Install(ctx) error`
  - `var ErrNoPins error`; consts `imageKernel = "vmlinuz"`, `imageInitrd = "initrd"`, `imageCmdline = "cmdline"`, `imageBase = "base.qcow2"`, `completeMark = ".complete"`
  - `func pathExists(p string) bool`

This follows `internal/cliruntime/install.go`: stage in a `.staging-*` folder next to the destination, verify while streaming, rename into place, and remove the stage on any failure. Unlike cliruntime, the hash and size come from Go source, not from the server.

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/runtime_test.go`:

```go
package sandbox

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func pinOf(name string, data []byte) Asset {
	sum := sha256.Sum256(data)
	return Asset{Name: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

type fixture struct {
	pins   Pins
	served map[string][]byte // URL path -> bytes the server sends
	hits   atomic.Int32
	srv    *httptest.Server
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{served: map[string][]byte{}}
	qemu := testZip(t, map[string]string{
		"qemu-system-x86_64.exe": "MZ qemu", "qemu-img.exe": "MZ img", "libglib-2.0-0.dll": "dll", "share/bios-256k.bin": "bios",
	})
	f.pins = Pins{QEMUVersion: "20260811", QEMUTag: "qemu-v20260811", QEMU: pinOf("qemu-20260811-win64.zip", qemu),
		ImageVersion: "0.1.0", ImageTag: "guest-v0.1.0"}
	f.served["/qemu-v20260811/qemu-20260811-win64.zip"] = qemu
	for name, body := range map[string]string{imageKernel: "kernel", imageInitrd: "initrd", imageCmdline: "init=/nix/store/x/init", imageBase: "qcow2 base"} {
		f.pins.Image = append(f.pins.Image, pinOf(name, []byte(body)))
		f.served["/guest-v0.1.0/"+name] = []byte(body)
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		b, ok := f.served[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fixture) runtime(t *testing.T) (*Runtime, Paths) {
	paths := Paths{Root: t.TempDir()}
	return NewRuntime(RuntimeConfig{Paths: paths, Pins: f.pins, BaseURL: f.srv.URL}), paths
}

func noStaging(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatalf("staging folder left behind: %s", filepath.Join(dir, e.Name()))
		}
	}
}

func TestInstallVerifiesAndUnpacksQEMUAndTheImage(t *testing.T) {
	f := newFixture(t)
	rt, paths := f.runtime(t)
	if rt.Ready() {
		t.Fatal("ready before install")
	}
	if err := rt.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !rt.Ready() {
		t.Fatal("not ready after install")
	}
	q, err := rt.QEMU()
	if err != nil {
		t.Fatal(err)
	}
	if q.System != filepath.Join(paths.Runtime("20260811"), "qemu-system-x86_64.exe") || !pathExists(q.Img) || !pathExists(filepath.Join(q.Share, "bios-256k.bin")) {
		t.Fatalf("qemu files %+v", q)
	}
	img, err := rt.Image()
	if err != nil {
		t.Fatal(err)
	}
	if img.Version != "0.1.0" || img.Kernel != filepath.Join(paths.Image("0.1.0"), imageKernel) || !pathExists(img.BaseImage) || !pathExists(img.Initrd) {
		t.Fatalf("image %+v", img)
	}
	if b, _ := os.ReadFile(filepath.Join(paths.Image("0.1.0"), imageCmdline)); string(b) != "init=/nix/store/x/init" {
		t.Fatalf("cmdline %q", b)
	}
	noStaging(t, filepath.Join(paths.Root, "runtime"))
	noStaging(t, filepath.Join(paths.Root, "images"))
}

func TestInstallRejectsAHashMismatchAndLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	f.served["/guest-v0.1.0/"+imageBase] = []byte("qcow2 bASE") // same size, different bytes
	rt, paths := f.runtime(t)
	err := rt.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v", err)
	}
	if rt.Ready() || pathExists(paths.Image("0.1.0")) {
		t.Fatal("a mismatched image was installed")
	}
	noStaging(t, filepath.Join(paths.Root, "images"))
	f.served["/guest-v0.1.0/"+imageBase] = []byte("qcow2 base") // the retry starts clean and succeeds
	if err := rt.Install(context.Background()); err != nil || !rt.Ready() {
		t.Fatalf("retry: %v", err)
	}
}

func TestInstallRejectsATruncatedDownload(t *testing.T) {
	f := newFixture(t)
	f.served["/qemu-v20260811/qemu-20260811-win64.zip"] = f.served["/qemu-v20260811/qemu-20260811-win64.zip"][:20]
	rt, paths := f.runtime(t)
	if err := rt.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v", err)
	}
	if pathExists(paths.Runtime("20260811")) {
		t.Fatal("a truncated QEMU was installed")
	}
	noStaging(t, filepath.Join(paths.Root, "runtime"))
}

func TestInstallReportsAMissingAsset(t *testing.T) {
	f := newFixture(t)
	delete(f.served, "/guest-v0.1.0/"+imageInitrd)
	rt, _ := f.runtime(t)
	if err := rt.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	f := newFixture(t)
	rt, _ := f.runtime(t)
	if err := rt.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := f.hits.Load()
	if err := rt.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.hits.Load() != before {
		t.Fatal("a second install downloaded again")
	}
}

func TestInstallRefusesUnsafeZipPaths(t *testing.T) {
	f := newFixture(t)
	evil := testZip(t, map[string]string{"qemu-system-x86_64.exe": "x", `..\evil.dll`: "x"})
	f.served["/qemu-v20260811/qemu-20260811-win64.zip"] = evil
	f.pins.QEMU = pinOf("qemu-20260811-win64.zip", evil)
	rt, paths := f.runtime(t)
	if err := rt.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("err = %v", err)
	}
	if pathExists(filepath.Join(paths.Root, "evil.dll")) || pathExists(filepath.Join(paths.Root, "runtime", "evil.dll")) {
		t.Fatal("zip slip")
	}
}

func TestInstallWithoutPinsSaysSo(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{Paths: Paths{Root: t.TempDir()}})
	if err := rt.Install(context.Background()); !errors.Is(err, ErrNoPins) {
		t.Fatalf("err = %v", err)
	}
	if rt.Ready() {
		t.Fatal("ready without pins")
	}
	if _, err := rt.Image(); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Image err = %v", err)
	}
}

func TestStartRunsOneInstallAtATimeAndReportsProgress(t *testing.T) {
	f := newFixture(t)
	paths := Paths{Root: t.TempDir()}
	var events atomic.Int32
	rt := NewRuntime(RuntimeConfig{Paths: paths, Pins: f.pins, BaseURL: f.srv.URL, OnChange: func(RuntimeState) { events.Add(1) }})
	first := rt.Start(context.Background())
	second := rt.Start(context.Background())
	if !first.Installing || !second.Installing {
		t.Fatalf("states %+v %+v", first, second)
	}
	deadline := time.Now().Add(10 * time.Second)
	for rt.State().Installing && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	st := rt.State()
	if st.Installing || !st.Ready || st.Error != "" {
		t.Fatalf("final state %+v", st)
	}
	if got := f.hits.Load(); got != int32(1+len(f.pins.Image)) {
		t.Fatalf("%d downloads, want %d (one install)", got, 1+len(f.pins.Image))
	}
	if events.Load() < 2 {
		t.Fatal("no progress events")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/sandbox -run "Install|StartRuns"`
Expected: FAIL (`undefined: NewRuntime` …)

- [ ] **Step 3: Implement**

`internal/sandbox/pins.go` (regenerated in Task 11):

```go
package sandbox

// DefaultPins is the QEMU build and guest image this Orole build downloads. It stays empty until the
// qemu-v* and guest-v* releases are published; scripts/sandbox-pins.ps1 then writes the values.
// With empty pins the runtime reports "runtime-missing" and Install returns ErrNoPins.
var DefaultPins = Pins{}
```

`internal/sandbox/runtime.go`:

```go
package sandbox

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Asset is one pinned release file.
type Asset struct {
	Name   string // file name in the GitHub release
	SHA256 string // lowercase hex
	Size   int64  // bytes
}

// Pins names the QEMU build and guest image this build runs. Every download is checked against them.
type Pins struct {
	QEMUVersion  string // e.g. "20260811" (the qemu.weilnetz.de installer date)
	QEMUTag      string // e.g. "qemu-v20260811"
	QEMU         Asset  // zip: qemu-system-x86_64.exe, qemu-img.exe, DLLs, share/
	ImageVersion string // guest/VERSION, e.g. "0.1.0"
	ImageTag     string // e.g. "guest-v0.1.0"
	Image        []Asset
}

func (p Pins) complete() bool {
	return p.QEMUVersion != "" && p.QEMUTag != "" && p.QEMU.SHA256 != "" && p.ImageVersion != "" && p.ImageTag != "" && len(p.Image) > 0
}

const defaultReleaseBase = "https://github.com/rayorole/orole-os-desktop/releases/download"

// Files inside Paths.Image(version).
const (
	imageKernel  = "vmlinuz"
	imageInitrd  = "initrd"
	imageCmdline = "cmdline"
	imageBase    = "base.qcow2"
	completeMark = ".complete" // written last: a folder without it is not installed
)

// maxQEMUUnpacked bounds what the QEMU zip may expand to.
const maxQEMUUnpacked = 2 << 30

// ErrNoPins means this build was made before the runtime assets were published.
var ErrNoPins = errors.New("sandbox: this build of Orole has no pinned computer runtime")

// RuntimeState is what the setup screen shows while the runtime downloads.
type RuntimeState struct {
	Ready      bool   `json:"ready"`
	Installing bool   `json:"installing"`
	Step       string `json:"step,omitempty"` // "qemu" or "image"
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

// RuntimeConfig wires a Runtime. Zero values use production defaults.
type RuntimeConfig struct {
	Paths    Paths
	Pins     Pins
	BaseURL  string       // default: GitHub releases of rayorole/orole-os-desktop
	HTTP     *http.Client // default: 60 minute timeout
	OnChange func(RuntimeState)
}

// QEMUFiles are the installed QEMU executables and firmware folder.
type QEMUFiles struct{ System, Img, Share string }

// Runtime downloads, verifies and locates QEMU and the guest image.
type Runtime struct {
	cfg       RuntimeConfig
	installMu sync.Mutex // one install at a time

	mu       sync.Mutex
	state    RuntimeState
	running  bool
	lastSent int64
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultReleaseBase
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 60 * time.Minute}
	}
	return &Runtime{cfg: cfg}
}

// Ready reports whether the pinned QEMU and image are installed.
func (r *Runtime) Ready() bool {
	p := r.cfg.Pins
	return p.complete() &&
		pathExists(filepath.Join(r.cfg.Paths.Runtime(p.QEMUVersion), completeMark)) &&
		pathExists(filepath.Join(r.cfg.Paths.Image(p.ImageVersion), completeMark))
}

func (r *Runtime) QEMU() (QEMUFiles, error) {
	if !r.Ready() {
		return QEMUFiles{}, ErrNotReady
	}
	dir := r.cfg.Paths.Runtime(r.cfg.Pins.QEMUVersion)
	return QEMUFiles{System: filepath.Join(dir, "qemu-system-x86_64.exe"), Img: filepath.Join(dir, "qemu-img.exe"), Share: filepath.Join(dir, "share")}, nil
}

// Image resolves the installed guest image; it is the Manager's Config.Image.
func (r *Runtime) Image() (ImageSet, error) {
	if !r.Ready() {
		return ImageSet{}, ErrNotReady
	}
	dir := r.cfg.Paths.Image(r.cfg.Pins.ImageVersion)
	return ImageSet{Version: r.cfg.Pins.ImageVersion, Kernel: filepath.Join(dir, imageKernel),
		Initrd: filepath.Join(dir, imageInitrd), BaseImage: filepath.Join(dir, imageBase)}, nil
}

func (r *Runtime) State() RuntimeState {
	r.mu.Lock()
	s := r.state
	r.mu.Unlock()
	s.Ready = r.Ready()
	return s
}

// Start installs in the background with ctx (the daemon's lifetime, not a request's) unless an install
// is already running, and returns the state right after.
func (r *Runtime) Start(ctx context.Context) RuntimeState {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return r.State()
	}
	r.running = true
	r.state = RuntimeState{Installing: true}
	r.mu.Unlock()
	go func() {
		err := r.Install(ctx)
		r.mu.Lock()
		r.running = false
		r.state.Installing = false
		r.state.Error = ""
		if err != nil {
			r.state.Error = err.Error()
		}
		r.mu.Unlock()
		r.changed()
	}()
	r.changed()
	return r.State()
}

func (r *Runtime) changed() {
	if r.cfg.OnChange != nil {
		r.cfg.OnChange(r.State())
	}
}

// Install downloads whatever of the pinned runtime is missing. A partial or mismatched download leaves
// nothing behind.
func (r *Runtime) Install(ctx context.Context) error {
	if !r.cfg.Pins.complete() {
		return ErrNoPins
	}
	r.installMu.Lock()
	defer r.installMu.Unlock()
	if err := r.installQEMU(ctx); err != nil {
		return fmt.Errorf("could not install QEMU: %w", err)
	}
	if err := r.installImage(ctx); err != nil {
		return fmt.Errorf("could not install the computer image: %w", err)
	}
	return nil
}

func (r *Runtime) installQEMU(ctx context.Context) error {
	p := r.cfg.Pins
	dest := r.cfg.Paths.Runtime(p.QEMUVersion)
	if pathExists(filepath.Join(dest, completeMark)) {
		return nil
	}
	stage, err := makeStage(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "download.zip")
	if err := r.fetch(ctx, "qemu", p.QEMUTag, p.QEMU, archive); err != nil {
		return err
	}
	files := filepath.Join(stage, "files")
	if err := extractZip(archive, files, maxQEMUUnpacked); err != nil {
		return err
	}
	for _, rel := range []string{"qemu-system-x86_64.exe", "qemu-img.exe", filepath.Join("share", "bios-256k.bin")} {
		if !pathExists(filepath.Join(files, rel)) {
			return fmt.Errorf("the QEMU archive is missing %s", rel)
		}
	}
	return finishStage(files, dest)
}

func (r *Runtime) installImage(ctx context.Context) error {
	p := r.cfg.Pins
	dest := r.cfg.Paths.Image(p.ImageVersion)
	if pathExists(filepath.Join(dest, completeMark)) {
		return nil
	}
	var names []string
	for _, a := range p.Image {
		names = append(names, a.Name)
	}
	for _, need := range []string{imageKernel, imageInitrd, imageCmdline, imageBase} {
		if !slices.Contains(names, need) {
			return fmt.Errorf("the pinned image has no %s", need)
		}
	}
	stage, err := makeStage(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	files := filepath.Join(stage, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	for _, a := range p.Image {
		if filepath.Base(a.Name) != a.Name || !filepath.IsLocal(a.Name) {
			return fmt.Errorf("unsafe asset name %q", a.Name)
		}
		if err := r.fetch(ctx, "image", p.ImageTag, a, filepath.Join(files, a.Name)); err != nil {
			return err
		}
	}
	return finishStage(files, dest)
}

func makeStage(parent string) (string, error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, ".staging-")
}

// finishStage marks src complete and moves it to dest, replacing an incomplete leftover.
func finishStage(src, dest string) error {
	if err := os.WriteFile(filepath.Join(src, completeMark), nil, 0o600); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(src, dest)
}

// fetch streams one release asset into dst, checking its size and SHA-256 against the pin.
func (r *Runtime) fetch(ctx context.Context, step, tag string, a Asset, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.BaseURL+"/"+tag+"/"+a.Name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "orole-os")
	resp, err := r.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s answered %s", a.Name, req.URL.Host, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	r.progress(step, 0, a.Size, true)
	n, copyErr := io.Copy(io.MultiWriter(f, h, &progressWriter{r: r, step: step, total: a.Size}), io.LimitReader(resp.Body, a.Size+1))
	if err := f.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr == nil && n != a.Size {
		copyErr = fmt.Errorf("%s has %d bytes, expected %d", a.Name, n, a.Size)
	}
	if copyErr == nil && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(a.SHA256) {
		copyErr = fmt.Errorf("%s does not match its pinned SHA-256", a.Name)
	}
	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}
	r.progress(step, a.Size, a.Size, true)
	return nil
}

type progressWriter struct {
	r     *Runtime
	step  string
	total int64
	done  int64
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	w.r.progress(w.step, w.done, w.total, false)
	return len(p), nil
}

// progress records download progress and reports it every 8 MiB (and always when force is set).
func (r *Runtime) progress(step string, done, total int64, force bool) {
	r.mu.Lock()
	r.state.Step, r.state.Done, r.state.Total = step, done, total
	send := force || done-r.lastSent >= 8<<20
	if send {
		r.lastSent = done
	}
	r.mu.Unlock()
	if send {
		r.changed()
	}
}

// extractZip unpacks archive into dst, refusing paths outside dst and more than limit bytes in total.
func extractZip(archive, dst string, limit int64) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total int64
	for _, zf := range zr.File {
		name := filepath.FromSlash(strings.ReplaceAll(zf.Name, `\`, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe path in the archive: %q", zf.Name)
		}
		target := filepath.Join(dst, name)
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if !zf.Mode().IsRegular() {
			return fmt.Errorf("unsupported entry in the archive: %q", zf.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		n, err := extractOne(zf, target, limit-total)
		if err != nil {
			return err
		}
		total += n
	}
	return nil
}

func extractOne(zf *zip.File, target string, room int64) (int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, room+1))
	if err := out.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr == nil && n > room {
		copyErr = errors.New("the archive expands beyond its size limit")
	}
	return n, copyErr
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/sandbox/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/runtime.go internal/sandbox/runtime_test.go internal/sandbox/pins.go
git commit -m "feat(sandbox): download and verify the pinned qemu and guest image

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Windows QEMU backend

**Files:**
- Create: `internal/sandbox/qmp.go`, `internal/sandbox/qmp_test.go`
- Create: `internal/sandbox/job_windows.go`
- Create: `internal/sandbox/backend_windows.go`, `internal/sandbox/backend_windows_test.go`
- Create: `internal/sandbox/backend_other.go`, `internal/sandbox/backend_other_test.go`
- Modify: `.github/workflows/ci.yml` (portable lane: add `./internal/sandbox/... ./cmd/orole-guest/...` unless phase 1 already did)

**Interfaces:**
- Consumes: `Backend`, `VM`, `BootSpec`, `Availability`, `ErrBootFailed`, `ErrCrashed`, `ErrNotReady`, `Paths` (phase 1); `qemuArgs`, `qemuConfig`, `qmpSocket`, `newTailBuffer`, `defaultCPU` (Task 3); `CheckSocketPath` (Task 2); `hostProbe`, `decideAvailability`, `probeHost` (Task 4); `Runtime`, `QEMUFiles`, `imageCmdline`, `pathExists` (Task 5).
- Produces:
  - `func NewHostBackend(rt *Runtime, p Paths) Backend` (both platforms)
  - `func qmpPowerdown(ctx context.Context, conn net.Conn) error`
  - windows internals: `type process interface { Wait() error; Kill() error }`, `type starter interface { Start(exe string, args []string, out io.Writer) (process, error) }`, `type commandRunner func(ctx context.Context, exe string, args ...string) ([]byte, error)`, `type hostBackend struct{...}`, `type qemuVM struct{...}`

- [ ] **Step 1: Write the failing QMP test**

`internal/sandbox/qmp_test.go`:

```go
package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeQMP answers like QEMU's monitor and records the commands it receives.
func fakeQMP(conn net.Conn, reply func(cmd string) string) <-chan []string {
	done := make(chan []string, 1)
	go func() {
		defer conn.Close()
		fmt.Fprintln(conn, `{"QMP": {"version": {"qemu": {"major": 11}}, "capabilities": []}}`)
		var got []string
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			line := sc.Text()
			got = append(got, line)
			fmt.Fprintln(conn, reply(line))
			if strings.Contains(line, "system_powerdown") {
				break
			}
		}
		done <- got
	}()
	return done
}

func TestQMPPowerdownDoesTheHandshakeFirst(t *testing.T) {
	host, qemu := net.Pipe()
	got := fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "system_powerdown") {
			return `{"event": "POWERDOWN", "timestamp": {"seconds": 1, "microseconds": 0}}` + "\n" + `{"return": {}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := qmpPowerdown(ctx, host); err != nil {
		t.Fatal(err)
	}
	cmds := <-got
	if len(cmds) != 2 || !strings.Contains(cmds[0], `"qmp_capabilities"`) || !strings.Contains(cmds[1], `"system_powerdown"`) {
		t.Fatalf("commands %q", cmds)
	}
}

func TestQMPPowerdownReportsAnError(t *testing.T) {
	host, qemu := net.Pipe()
	fakeQMP(qemu, func(cmd string) string {
		if strings.Contains(cmd, "system_powerdown") {
			return `{"error": {"class": "GenericError", "desc": "no ACPI"}}`
		}
		return `{"return": {}}`
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := qmpPowerdown(ctx, host); err == nil || !strings.Contains(err.Error(), "no ACPI") {
		t.Fatalf("err = %v", err)
	}
}

func TestQMPPowerdownGivesUpWhenQEMUIsSilent(t *testing.T) {
	host, qemu := net.Pipe()
	defer qemu.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := qmpPowerdown(ctx, host); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/sandbox -run QMP`
Expected: FAIL (`undefined: qmpPowerdown`)

- [ ] **Step 3: Implement QMP**

`internal/sandbox/qmp.go`:

```go
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// qmpPowerdown performs the QMP handshake on conn and asks QEMU for an ACPI power-down; the guest's
// systemd treats it as the power button and powers off cleanly. It does not wait for QEMU to exit.
// Call it at most once per connection (QEMU sends its greeting once).
func qmpPowerdown(ctx context.Context, conn net.Conn) error {
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	defer conn.SetDeadline(time.Time{})
	dec := json.NewDecoder(conn)
	var greeting map[string]json.RawMessage
	if err := dec.Decode(&greeting); err != nil {
		return fmt.Errorf("qmp greeting: %w", err)
	}
	if _, ok := greeting["QMP"]; !ok {
		return errors.New("qmp: unexpected greeting")
	}
	for _, cmd := range []string{"qmp_capabilities", "system_powerdown"} {
		if _, err := fmt.Fprintf(conn, "{\"execute\":%q}\n", cmd); err != nil {
			return err
		}
		if err := qmpReturn(dec); err != nil {
			return fmt.Errorf("qmp %s: %w", cmd, err)
		}
	}
	return nil
}

// qmpReturn reads messages until a command's return or error; asynchronous events are skipped.
func qmpReturn(dec *json.Decoder) error {
	for {
		var msg struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Class string `json:"class"`
				Desc  string `json:"desc"`
			} `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		if msg.Error != nil {
			return fmt.Errorf("%s: %s", msg.Error.Class, msg.Error.Desc)
		}
		if msg.Return != nil {
			return nil
		}
	}
}
```

Run: `go test -race ./internal/sandbox -run QMP` → PASS.

- [ ] **Step 4: Write the failing backend tests**

`internal/sandbox/backend_windows_test.go`:

```go
//go:build windows

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProc is a QEMU process that exits when told to.
type fakeProc struct {
	exit chan error
	once sync.Once
}

func (p *fakeProc) Wait() error { return <-p.exit }
func (p *fakeProc) Kill() error { p.exitWith(errors.New("killed")); return nil }
func (p *fakeProc) exitWith(err error) {
	p.once.Do(func() { p.exit <- err })
}

// fakeQEMU stands in for qemu-system-x86_64: it connects to the guest and QMP sockets named in argv,
// like the real one, and powers off on system_powerdown unless ignorePowerdown is set.
type fakeQEMU struct {
	ignorePowerdown   bool
	exitBeforeConnect bool
	args              []string
	proc              *fakeProc
	guest             net.Conn // the guest's end of the channel

	mu       sync.Mutex
	commands []string
}

func chardevPath(args []string, id string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-chardev" && strings.HasPrefix(args[i+1], "socket,id="+id+",path=") {
			return strings.ReplaceAll(strings.TrimPrefix(args[i+1], "socket,id="+id+",path="), ",,", ",")
		}
	}
	return ""
}

func (f *fakeQEMU) Start(exe string, args []string, out io.Writer) (process, error) {
	f.args = args
	f.proc = &fakeProc{exit: make(chan error, 1)}
	if f.exitBeforeConnect {
		fmt.Fprintln(out, "qemu-system-x86_64: WHPX: No accelerator found, hr=80370102")
		f.proc.exitWith(errors.New("exit status 1"))
		return f.proc, nil
	}
	g, err := net.Dial("unix", chardevPath(args, "guest"))
	if err != nil {
		return nil, err
	}
	q, err := net.Dial("unix", chardevPath(args, "qmp"))
	if err != nil {
		return nil, err
	}
	f.guest = g
	go func() {
		fmt.Fprintln(q, `{"QMP": {"version": {}, "capabilities": []}}`)
		sc := bufio.NewScanner(q)
		for sc.Scan() {
			f.mu.Lock()
			f.commands = append(f.commands, sc.Text())
			f.mu.Unlock()
			fmt.Fprintln(q, `{"return": {}}`)
			if strings.Contains(sc.Text(), "system_powerdown") && !f.ignorePowerdown {
				f.proc.exitWith(nil)
			}
		}
	}()
	return f.proc, nil
}

func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbx") // short: AF_UNIX paths are limited to MaxSocketPath
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func installedRuntime(t *testing.T, paths Paths) *Runtime {
	t.Helper()
	pins := Pins{QEMUVersion: "t", QEMUTag: "qemu-vt", QEMU: Asset{Name: "q.zip", SHA256: "00", Size: 1},
		ImageVersion: "t", ImageTag: "guest-vt", Image: []Asset{{Name: imageKernel, SHA256: "00", Size: 1}}}
	for _, dir := range []string{paths.Runtime("t"), paths.Image("t")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, completeMark), nil, 0o600)
	}
	os.WriteFile(filepath.Join(paths.Image("t"), imageCmdline), []byte("init=/nix/store/abc-nixos-system/init"), 0o600)
	return NewRuntime(RuntimeConfig{Paths: paths, Pins: pins})
}

func testBackend(t *testing.T, f *fakeQEMU) (*hostBackend, BootSpec, *[][]string) {
	t.Helper()
	root := shortRoot(t)
	paths := Paths{Root: filepath.Join(root, "sandbox")}
	rt := installedRuntime(t, paths)
	var calls [][]string
	b := &hostBackend{rt: rt, paths: paths, start: f, cpu: "host", accept: 5 * time.Second,
		probe: func(context.Context) hostProbe { return hostProbe{Build: 22000} },
		run: func(ctx context.Context, exe string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{exe}, args...))
			return nil, os.WriteFile(args[len(args)-2], []byte("QFI"), 0o600) // qemu-img writes the overlay
		},
	}
	img, err := rt.Image()
	if err != nil {
		t.Fatal(err)
	}
	spec := BootSpec{BotID: "bot-1", Limits: DefaultLimits(), BaseImage: img.BaseImage, Kernel: img.Kernel, Initrd: img.Initrd,
		Overlay: paths.Overlay("bot-1"), Token: strings.Repeat("ab", 32), LogPath: filepath.Join(root, "logs", "sandbox-bot-1.log")}
	return b, spec, &calls
}

func TestBootCreatesTheOverlayOnceAndConnectsTheGuestChannel(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, calls := testBackend(t, f)
	ctx := context.Background()
	vm, err := b.Boot(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("qemu-img calls %q", *calls)
	}
	c := strings.Join((*calls)[0], " ")
	for _, want := range []string{"create", "-f qcow2", "-F qcow2", "-b " + spec.BaseImage, spec.Overlay + ".new 10G"} {
		if !strings.Contains(c, want) {
			t.Errorf("qemu-img %q lacks %q", c, want)
		}
	}
	if !pathExists(spec.Overlay) || pathExists(spec.Overlay+".new") {
		t.Fatal("overlay not moved into place")
	}
	if _, err := vm.Conn().Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(f.guest).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("guest read %q, %v", line, err)
	}

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := vm.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	<-vm.Done()
	if vm.ExitErr() != nil {
		t.Fatalf("clean shutdown has ExitErr %v", vm.ExitErr())
	}
	f.mu.Lock()
	cmds := strings.Join(f.commands, " ")
	f.mu.Unlock()
	if !strings.Contains(cmds, "qmp_capabilities") || !strings.Contains(cmds, "system_powerdown") {
		t.Fatalf("qmp commands %q", cmds)
	}
	if sock := b.paths.Socket("bot-1"); pathExists(sock) || pathExists(qmpSocket(sock)) {
		t.Fatal("socket files left behind")
	}

	vm2, err := b.Boot(ctx, spec) // the overlay exists now: no second qemu-img call
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatal("overlay created twice")
	}
	vm2.Kill()
	<-vm2.Done()
}

func TestShutdownKillsAGuestThatIgnoresPowerdown(t *testing.T) {
	f := &fakeQEMU{ignorePowerdown: true}
	b, spec, _ := testBackend(t, f)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	vm.Shutdown(ctx)
	select {
	case <-vm.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("VM still running after Shutdown's deadline")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Shutdown waited far past its deadline")
	}
}

func TestBootFailsFastWhenQEMUExitsBeforeConnecting(t *testing.T) {
	f := &fakeQEMU{exitBeforeConnect: true}
	b, spec, _ := testBackend(t, f)
	b.accept = 30 * time.Second
	start := time.Now()
	_, err := b.Boot(context.Background(), spec)
	if !errors.Is(err, ErrBootFailed) || !strings.Contains(err.Error(), "No accelerator") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if strings.Contains(err.Error(), spec.Token) {
		t.Fatal("token in the error")
	}
	if sock := b.paths.Socket("bot-1"); pathExists(sock) || pathExists(qmpSocket(sock)) {
		t.Fatal("socket files left behind")
	}
}

func TestAnUnexpectedExitIsACrash(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	f.proc.exitWith(errors.New("exit status 3"))
	<-vm.Done()
	if !errors.Is(vm.ExitErr(), ErrCrashed) {
		t.Fatalf("ExitErr = %v", vm.ExitErr())
	}
	if _, err := vm.Conn().Write([]byte("x")); err == nil {
		t.Fatal("guest channel still open after the crash")
	}
}

func TestBootRefusesATooLongDataDir(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	b.paths = Paths{Root: filepath.Join(b.paths.Root, strings.Repeat("x", 100))}
	if _, err := b.Boot(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v", err)
	}
	if f.proc != nil {
		t.Fatal("QEMU started anyway")
	}
}

func TestAvailabilityNeedsTheRuntime(t *testing.T) {
	b, _, _ := testBackend(t, &fakeQEMU{})
	if a := b.Availability(context.Background()); !a.Ready {
		t.Fatalf("installed runtime + working WHP: %+v", a)
	}
	b.rt = NewRuntime(RuntimeConfig{Paths: Paths{Root: shortRoot(t)}, Pins: b.rt.cfg.Pins})
	if a := b.Availability(context.Background()); a.Reason != "runtime-missing" {
		t.Fatalf("%+v", a)
	}
}
```

- [ ] **Step 5: Run them to verify they fail**

Run: `go test ./internal/sandbox -run "Boot|Shutdown|Crash|AvailabilityNeeds"`
Expected: FAIL (`undefined: hostBackend` …)

- [ ] **Step 6: Implement the job object**

`internal/sandbox/job_windows.go` (the same pattern as `internal/agents/job_windows.go`, which is unexported there):

```go
//go:build windows

package sandbox

import (
	"errors"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobObject is a Windows job with KILL_ON_JOB_CLOSE: when the daemon exits or crashes, Windows closes
// the handle and kills every QEMU in it, so VMs never outlive the daemon.
type jobObject struct {
	mu sync.Mutex
	h  windows.Handle
}

func newJobObject() (*jobObject, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &jobObject{h: h}, nil
}

func (j *jobObject) assign(pid int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.h == 0 {
		return errors.New("job closed")
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ph)
	return windows.AssignProcessToJobObject(j.h, ph)
}
```

- [ ] **Step 7: Implement the backend**

`internal/sandbox/backend_windows.go`:

```go
//go:build windows

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

type process interface {
	Wait() error
	Kill() error
}

type starter interface {
	Start(exe string, args []string, out io.Writer) (process, error)
}

type commandRunner func(ctx context.Context, exe string, args ...string) ([]byte, error)

// hostBackend runs each bot VM as qemu-system-x86_64 -accel whpx inside the daemon's job object.
type hostBackend struct {
	rt     *Runtime
	paths  Paths
	probe  func(ctx context.Context) hostProbe
	start  starter
	run    commandRunner
	cpu    string
	accept time.Duration // how long QEMU may take to connect its two sockets
}

// NewHostBackend returns the QEMU/WHPX backend.
func NewHostBackend(rt *Runtime, p Paths) Backend {
	j, err := newJobObject()
	return &hostBackend{rt: rt, paths: p, cpu: defaultCPU, accept: 20 * time.Second, run: runCommand,
		probe: func(ctx context.Context) hostProbe { return probeHost(ctx, p) },
		start: &execStarter{job: j, jobErr: err}}
}

func (b *hostBackend) Availability(ctx context.Context) Availability {
	return decideAvailability(b.probe(ctx), b.rt.Ready())
}

func (b *hostBackend) Boot(ctx context.Context, spec BootSpec) (VM, error) {
	q, err := b.rt.QEMU()
	if err != nil {
		return nil, err
	}
	guestPath := b.paths.Socket(spec.BotID)
	qmpPath := qmpSocket(guestPath)
	for _, p := range []string{guestPath, qmpPath} {
		if err := CheckSocketPath(p); err != nil {
			return nil, err
		}
	}
	cmdline, err := os.ReadFile(filepath.Join(filepath.Dir(spec.Kernel), imageCmdline))
	if err != nil {
		return nil, fmt.Errorf("%w: the image has no kernel command line: %v", ErrBootFailed, err)
	}
	if err := b.ensureOverlay(ctx, q.Img, spec); err != nil {
		return nil, err
	}
	if err := prepareLog(spec.LogPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(guestPath), 0o700); err != nil {
		return nil, err
	}
	guestLn, err := listenUnix(guestPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	defer guestLn.Close()
	qmpLn, err := listenUnix(qmpPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	defer qmpLn.Close()

	args := qemuArgs(qemuConfig{Spec: spec, Share: q.Share, CPU: b.cpu, Cmdline: string(cmdline), GuestSocket: guestPath, QMPSocket: qmpPath})
	tail := newTailBuffer(16<<10, spec.Token)
	proc, err := b.start.Start(q.System, args, tail)
	if err != nil {
		os.Remove(guestPath)
		os.Remove(qmpPath)
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	vm := newQEMUVM(proc, tail, []string{guestPath, qmpPath})
	guest, qmp, err := acceptBoth(ctx, guestLn, qmpLn, vm.done, b.accept)
	if err != nil {
		vm.Kill()
		<-vm.done
		return nil, fmt.Errorf("%w: %v%s", ErrBootFailed, err, tail.suffix())
	}
	vm.attach(guest, qmp)
	return vm, nil
}

// ensureOverlay creates the bot's qcow2 disk on first boot, backed by the read-only base image.
func (b *hostBackend) ensureOverlay(ctx context.Context, img string, spec BootSpec) error {
	if _, err := os.Stat(spec.Overlay); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.Overlay), 0o700); err != nil {
		return err
	}
	tmp := spec.Overlay + ".new"
	os.Remove(tmp)
	out, err := b.run(ctx, img, "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", spec.BaseImage, tmp, strconv.Itoa(spec.Limits.DiskGB)+"G")
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%w: could not create the computer's disk: %v: %s", ErrBootFailed, err, bytes.TrimSpace(out))
	}
	return os.Rename(tmp, spec.Overlay)
}

func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", path)
}

// prepareLog makes the log folder and rotates a log over 4 MiB to <name>.1.
func prepareLog(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 4<<20 {
		os.Remove(path + ".1")
		return os.Rename(path, path+".1")
	}
	return nil
}

func acceptBoth(ctx context.Context, a, b net.Listener, exited <-chan struct{}, timeout time.Duration) (net.Conn, net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ca, cb := make(chan res, 1), make(chan res, 1)
	go func() { c, err := a.Accept(); ca <- res{c, err} }()
	go func() { c, err := b.Accept(); cb <- res{c, err} }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var ga, gb net.Conn
	fail := func(err error) (net.Conn, net.Conn, error) {
		for _, c := range []net.Conn{ga, gb} {
			if c != nil {
				c.Close()
			}
		}
		return nil, nil, err
	}
	for ga == nil || gb == nil {
		select {
		case r := <-ca:
			if r.err != nil {
				return fail(r.err)
			}
			ga = r.c
		case r := <-cb:
			if r.err != nil {
				return fail(r.err)
			}
			gb = r.c
		case <-exited:
			return fail(errors.New("QEMU exited before the computer connected"))
		case <-timer.C:
			return fail(errors.New("QEMU did not connect in time"))
		case <-ctx.Done():
			return fail(ctx.Err())
		}
	}
	return ga, gb, nil
}

func runCommand(ctx context.Context, exe string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.CombinedOutput()
}

type execStarter struct {
	job    *jobObject
	jobErr error
}

func (s *execStarter) Start(exe string, args []string, out io.Writer) (process, error) {
	if s.jobErr != nil {
		return nil, fmt.Errorf("job object: %w", s.jobErr)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := s.job.assign(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

type execProcess struct{ cmd *exec.Cmd }

func (p *execProcess) Wait() error { return p.cmd.Wait() }
func (p *execProcess) Kill() error { return p.cmd.Process.Kill() }

// qemuVM is one running QEMU.
type qemuVM struct {
	proc      process
	done      chan struct{}
	powerOnce sync.Once

	mu       sync.Mutex
	guest    net.Conn
	qmp      net.Conn
	exited   bool
	stopping bool
	exitErr  error
}

func newQEMUVM(p process, tail *tailBuffer, files []string) *qemuVM {
	v := &qemuVM{proc: p, done: make(chan struct{})}
	go func() {
		err := p.Wait()
		v.mu.Lock()
		v.exited = true
		if !v.stopping {
			if err == nil {
				err = errors.New("the guest powered off by itself")
			}
			v.exitErr = fmt.Errorf("%w: %v%s", ErrCrashed, err, tail.suffix())
		}
		conns := []net.Conn{v.guest, v.qmp}
		v.mu.Unlock()
		for _, c := range conns {
			if c != nil {
				c.Close()
			}
		}
		for _, f := range files {
			os.Remove(f)
		}
		close(v.done)
	}()
	return v
}

func (v *qemuVM) attach(guest, qmp net.Conn) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.exited {
		guest.Close()
		qmp.Close()
	}
	v.guest, v.qmp = guest, qmp
}

func (v *qemuVM) Conn() net.Conn {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.guest
}

// Shutdown asks the guest to power off (ACPI over QMP), waits for QEMU to exit until ctx is done, then kills it.
func (v *qemuVM) Shutdown(ctx context.Context) error {
	v.mu.Lock()
	v.stopping = true
	qmp := v.qmp
	v.mu.Unlock()
	if qmp != nil {
		v.powerOnce.Do(func() { _ = qmpPowerdown(ctx, qmp) }) // a guest that ignores it is killed below
	}
	select {
	case <-v.done:
		return nil
	case <-ctx.Done():
		err := v.Kill()
		<-v.done
		return err
	}
}

func (v *qemuVM) Kill() error {
	v.mu.Lock()
	v.stopping = true
	v.mu.Unlock()
	if err := v.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (v *qemuVM) Done() <-chan struct{} { return v.done }

func (v *qemuVM) ExitErr() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exitErr
}
```

- [ ] **Step 8: Implement the non-Windows backend and its test**

`internal/sandbox/backend_other.go`:

```go
//go:build !windows

package sandbox

import "context"

type unsupportedBackend struct{}

// NewHostBackend returns a backend that reports the computer as unavailable: only Windows is built.
func NewHostBackend(*Runtime, Paths) Backend { return unsupportedBackend{} }

func (unsupportedBackend) Availability(context.Context) Availability {
	return Availability{Reason: "unsupported-os", Detail: "The computer is only available on Windows for now."}
}

func (unsupportedBackend) Boot(context.Context, BootSpec) (VM, error) { return nil, ErrNotReady }
```

`internal/sandbox/backend_other_test.go`:

```go
//go:build !windows

package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestHostBackendIsNotImplementedOffWindows(t *testing.T) {
	b := NewHostBackend(NewRuntime(RuntimeConfig{Paths: Paths{Root: t.TempDir()}}), Paths{Root: t.TempDir()})
	if a := b.Availability(context.Background()); a.Ready || a.Reason != "unsupported-os" {
		t.Fatalf("%+v", a)
	}
	if _, err := b.Boot(context.Background(), BootSpec{}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 9: Add the sandbox packages to the portable CI lane**

In `.github/workflows/ci.yml`, job `portable`, step "Go test (race), portable packages and Linux terminals", append `./internal/sandbox/... ./cmd/orole-guest/...` to the `go test -race` list (skip any already listed by phase 1).

- [ ] **Step 10: Run the tests on both platforms**

Run: `go test -race ./internal/sandbox/... && go vet ./... ; $env:GOOS="linux"; go vet ./internal/sandbox/...; Remove-Item Env:GOOS`
Expected: PASS; vet clean. In WSL: `wsl -d Ubuntu -- bash -lc "cd /mnt/c/code/projects/.orole-worktrees/orole-os/196-sandbox-core && go test ./internal/sandbox/..."`. If Go is not installed in WSL, skip this, since CI's portable lane covers it.

- [ ] **Step 11: Commit**

```bash
git add internal/sandbox/qmp.go internal/sandbox/qmp_test.go internal/sandbox/job_windows.go internal/sandbox/backend_windows.go internal/sandbox/backend_windows_test.go internal/sandbox/backend_other.go internal/sandbox/backend_other_test.go .github/workflows/ci.yml
git commit -m "feat(sandbox): run bot vms with qemu whpx in the daemon job object

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Elevated setup helper and daemon-side launcher

**Files:**
- Create: `internal/sandbox/setupcode/setupcode.go`
- Create: `cmd/orole-sandbox-setup/run.go`, `run_test.go`, `main_windows.go`, `main_other.go`
- Create: `internal/sandbox/setup.go`, `setup_test.go`, `setup_windows.go`, `setup_windows_test.go`, `setup_other.go`
- Modify: `mygo.json` (`resources`, `devCommand`), `scripts/build.ps1`, `docs/development/packaging.md`

**Interfaces:**
- Consumes: `Paths`, `rebootMarkerPath`, `rebootMarker`, `bootTime` (Task 4).
- Produces:
  - package `setupcode`: `OK = 0`, `Failed = 1`, `Usage = 2`, `NotElevated = 740`, `RebootRequired = 3010`, `EnableWHPArg = "enable-whp"`
  - `type SetupResult struct { RebootRequired bool \`json:"rebootRequired"\` }`
  - `var ErrSetupDeclined, ErrSetupUnsupported, ErrSetupHelperMissing error`
  - `func setupOutcome(code uint32) (SetupResult, error)`
  - `func SetupHelperPath(daemonExe string) string`
  - `func EnableWHP(ctx context.Context, helper string, p Paths) (SetupResult, error)` (Windows: one UAC prompt; elsewhere `ErrSetupUnsupported`)

The daemon launches the helper with `ShellExecuteExW` verb `runas`, which shows Windows' UAC prompt. The daemon itself stays unelevated, and the helper is the only binary that runs elevated. `x/sys/windows` has no `ShellExecuteEx` (checked: `go doc` finds no such symbol), so it is declared through `NewLazySystemDLL`. A manifest with `requireAdministrator` would need a resource compiler and a new build dependency. The helper checks its own token instead and exits `740` when it is not elevated.

- [ ] **Step 1: Write the failing helper tests**

`internal/sandbox/setupcode/setupcode.go`:

```go
// Package setupcode holds the exit codes shared by orole-sandbox-setup.exe and the daemon.
package setupcode

const (
	OK             = 0    // Windows Hypervisor Platform is on; no restart needed
	Failed         = 1    // DISM failed or could not run
	Usage          = 2    // wrong arguments
	NotElevated    = 740  // ERROR_ELEVATION_REQUIRED: the helper was started without admin rights
	RebootRequired = 3010 // ERROR_SUCCESS_REBOOT_REQUIRED, as DISM reports it
)

// EnableWHPArg is the helper's only accepted argument.
const EnableWHPArg = "enable-whp"
```

`cmd/orole-sandbox-setup/run_test.go`:

```go
package main

import (
	"errors"
	"slices"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

func TestRunAcceptsOnlyTheOneCommand(t *testing.T) {
	never := func([]string) (int, error) { t.Fatal("DISM ran"); return 0, nil }
	for _, args := range [][]string{nil, {"enable-whp", "extra"}, {"/Online"}, {"ENABLE-WHP"}} {
		if got := run(args, true, never); got != setupcode.Usage {
			t.Errorf("%q: got %d", args, got)
		}
	}
}

func TestRunNeedsElevationBeforeDISM(t *testing.T) {
	never := func([]string) (int, error) { t.Fatal("DISM ran unelevated"); return 0, nil }
	if got := run([]string{setupcode.EnableWHPArg}, false, never); got != setupcode.NotElevated {
		t.Fatalf("got %d", got)
	}
}

func TestRunMapsDISMExitCodes(t *testing.T) {
	cases := []struct {
		code int
		err  error
		want int
	}{
		{0, nil, setupcode.OK}, {3010, nil, setupcode.RebootRequired}, {87, nil, setupcode.Failed}, {0, errors.New("no dism"), setupcode.Failed},
	}
	for _, c := range cases {
		var gotArgs []string
		got := run([]string{setupcode.EnableWHPArg}, true, func(a []string) (int, error) { gotArgs = a; return c.code, c.err })
		if got != c.want {
			t.Errorf("dism %d/%v: got %d want %d", c.code, c.err, got, c.want)
		}
		if !slices.Equal(gotArgs, []string{"/Online", "/Enable-Feature", "/FeatureName:HypervisorPlatform", "/All", "/NoRestart", "/Quiet"}) {
			t.Errorf("dism args %q", gotArgs)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/orole-sandbox-setup`
Expected: FAIL (`undefined: run`)

- [ ] **Step 3: Implement the helper**

`cmd/orole-sandbox-setup/run.go`:

```go
// orole-sandbox-setup turns on the Windows Hypervisor Platform feature for Orole's bot computer. The
// daemon starts it once through a UAC prompt; it does nothing else and accepts no other input.
package main

import "github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"

func dismArgs() []string {
	return []string{"/Online", "/Enable-Feature", "/FeatureName:HypervisorPlatform", "/All", "/NoRestart", "/Quiet"}
}

// run is the whole program: exactly one argument, elevation required, DISM's exit code mapped.
func run(args []string, elevated bool, dism func(args []string) (int, error)) int {
	if len(args) != 1 || args[0] != setupcode.EnableWHPArg {
		return setupcode.Usage
	}
	if !elevated {
		return setupcode.NotElevated
	}
	code, err := dism(dismArgs())
	switch {
	case err != nil:
		return setupcode.Failed
	case code == 0:
		return setupcode.OK
	case code == 3010:
		return setupcode.RebootRequired
	default:
		return setupcode.Failed
	}
}
```

`cmd/orole-sandbox-setup/main_windows.go`:

```go
//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func main() {
	os.Exit(run(os.Args[1:], windows.GetCurrentProcessToken().IsElevated(), runDISM))
}

// runDISM runs System32\Dism.exe by full path (never a PATH lookup) without a console window.
func runDISM(args []string) (int, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(filepath.Join(sys, "Dism.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
```

`cmd/orole-sandbox-setup/main_other.go`:

```go
//go:build !windows

package main

import (
	"fmt"
	"os"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

func main() {
	fmt.Fprintln(os.Stderr, "orole-sandbox-setup only runs on Windows")
	os.Exit(setupcode.Failed)
}
```

Run: `go test -race ./cmd/orole-sandbox-setup` → PASS.

- [ ] **Step 4: Write the failing daemon-side tests**

`internal/sandbox/setup_test.go`:

```go
package sandbox

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

func TestSetupOutcomeMapsHelperExitCodes(t *testing.T) {
	if r, err := setupOutcome(setupcode.OK); err != nil || r.RebootRequired {
		t.Fatalf("OK: %+v %v", r, err)
	}
	if r, err := setupOutcome(setupcode.RebootRequired); err != nil || !r.RebootRequired {
		t.Fatalf("3010: %+v %v", r, err)
	}
	if _, err := setupOutcome(setupcode.NotElevated); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("740: %v", err)
	}
	if _, err := setupOutcome(setupcode.Failed); err == nil || !strings.Contains(err.Error(), "dism") {
		t.Fatalf("1: %v", err)
	}
}

func TestSetupHelperSitsNextToTheDaemon(t *testing.T) {
	exe := filepath.Join("C:", "Program Files", "Orole OS", "resources", "orole-daemon.exe")
	if got := SetupHelperPath(exe); got != filepath.Join(filepath.Dir(exe), "orole-sandbox-setup.exe") {
		t.Fatal(got)
	}
}
```

`internal/sandbox/setup_windows_test.go`:

```go
//go:build windows

package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestShellExecuteInfoMatchesTheWin64Layout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 && unsafe.Sizeof(shellExecuteInfo{}) != 112 {
		t.Fatalf("sizeof(SHELLEXECUTEINFOW) = %d, want 112", unsafe.Sizeof(shellExecuteInfo{}))
	}
}

func TestEnableWHPWithoutTheHelperSaysSo(t *testing.T) {
	_, err := EnableWHP(context.Background(), filepath.Join(t.TempDir(), "orole-sandbox-setup.exe"), Paths{Root: t.TempDir()})
	if !errors.Is(err, ErrSetupHelperMissing) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 5: Run them to verify they fail**

Run: `go test ./internal/sandbox -run "Setup|ShellExecute|EnableWHP"`
Expected: FAIL (`undefined: setupOutcome` …)

- [ ] **Step 6: Implement**

`internal/sandbox/setup.go`:

```go
package sandbox

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

// SetupResult is what turning on Windows Hypervisor Platform did.
type SetupResult struct {
	RebootRequired bool `json:"rebootRequired"`
}

var (
	ErrSetupDeclined      = errors.New("sandbox: Windows did not get permission to turn on Windows Hypervisor Platform")
	ErrSetupUnsupported   = errors.New("sandbox: setup is only available on Windows")
	ErrSetupHelperMissing = errors.New("sandbox: orole-sandbox-setup.exe is missing next to the daemon; reinstall Orole")
)

// SetupHelperPath is where the installer puts orole-sandbox-setup.exe: next to orole-daemon.exe.
func SetupHelperPath(daemonExe string) string {
	return filepath.Join(filepath.Dir(daemonExe), "orole-sandbox-setup.exe")
}

func setupOutcome(code uint32) (SetupResult, error) {
	switch code {
	case setupcode.OK:
		return SetupResult{}, nil
	case setupcode.RebootRequired:
		return SetupResult{RebootRequired: true}, nil
	case setupcode.NotElevated:
		return SetupResult{}, errors.New("sandbox: the setup helper did not run as administrator")
	default:
		return SetupResult{}, fmt.Errorf("sandbox: turning on Windows Hypervisor Platform failed (code %d); run `dism /online /enable-feature /featurename:HypervisorPlatform /all` in an administrator terminal to see why", code)
	}
}
```

`internal/sandbox/setup_windows.go`:

```go
//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
)

// shellExecuteInfo is SHELLEXECUTEINFOW (112 bytes on 64-bit Windows).
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

// EnableWHP runs orole-sandbox-setup.exe elevated (one UAC prompt) and waits for it. When Windows must
// restart, it writes the reboot marker so Availability says "reboot-required" until it has.
func EnableWHP(ctx context.Context, helper string, p Paths) (SetupResult, error) {
	if helper == "" {
		return SetupResult{}, ErrSetupHelperMissing
	}
	if _, err := os.Stat(helper); err != nil {
		return SetupResult{}, ErrSetupHelperMissing
	}
	code, err := runElevated(ctx, helper, setupcode.EnableWHPArg)
	if err != nil {
		return SetupResult{}, err
	}
	res, err := setupOutcome(code)
	if err == nil && res.RebootRequired {
		if mkErr := os.MkdirAll(p.Root, 0o700); mkErr == nil {
			_ = os.WriteFile(rebootMarkerPath(p), rebootMarker(bootTime()), 0o600)
		}
	}
	return res, err
}

func runElevated(ctx context.Context, exe, params string) (uint32, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	args, _ := windows.UTF16PtrFromString(params)
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI, lpVerb: verb, lpFile: file, lpParameters: args, nShow: swHide}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrSetupDeclined
		}
		return 0, fmt.Errorf("sandbox: could not start the setup helper: %w", callErr)
	}
	if info.hProcess == 0 {
		return 0, errors.New("sandbox: the setup helper started without a process handle")
	}
	type result struct {
		code uint32
		err  error
	}
	res := make(chan result, 1)
	go func(h windows.Handle) {
		defer windows.CloseHandle(h)
		if _, err := windows.WaitForSingleObject(h, windows.INFINITE); err != nil {
			res <- result{err: err}
			return
		}
		var code uint32
		err := windows.GetExitCodeProcess(h, &code)
		res <- result{code, err}
	}(info.hProcess)
	select {
	case r := <-res:
		return r.code, r.err
	case <-ctx.Done():
		return 0, ctx.Err() // the helper keeps running; the next availability check sees its result
	}
}
```

`internal/sandbox/setup_other.go`:

```go
//go:build !windows

package sandbox

import "context"

// EnableWHP is Windows-only.
func EnableWHP(context.Context, string, Paths) (SetupResult, error) {
	return SetupResult{}, ErrSetupUnsupported
}
```

- [ ] **Step 7: Run the tests**

Run: `go test -race ./internal/sandbox/... ./cmd/orole-sandbox-setup/...`
Expected: PASS

- [ ] **Step 8: Package the helper**

`mygo.json`: add the helper to `resources` and build it in `devCommand`:

```json
  "devCommand": "go build -o build/orole-daemon.exe ./cmd/daemon && go build -ldflags=-H=windowsgui -o build/orole-sandbox-setup.exe ./cmd/orole-sandbox-setup && bun run --cwd frontend dev",
```

```json
  "resources": [
    "build/orole-daemon.exe",
    "build/orole-sandbox-setup.exe"
  ],
```

`scripts/build.ps1`: after the `orole.exe` build, add:

```powershell
# The sandbox setup helper is GUI-subsystem so its UAC-elevated run never flashes a console; it is a mygo
# resource next to orole-daemon.exe, which finds it there (sandbox.SetupHelperPath).
go build -trimpath -ldflags "$ld -H windowsgui" -o build\orole-sandbox-setup.exe ./cmd/orole-sandbox-setup
if ($LASTEXITCODE) { throw "sandbox setup helper build failed" }
```

and change the message to `Write-Host "built build\orole-daemon.exe, build\orole.exe and build\orole-sandbox-setup.exe ($Version)"`. Update the `.PARAMETER DaemonOnly` help line to mention the helper.

`docs/development/packaging.md`: add a bullet under the resources description: "`build/orole-sandbox-setup.exe` (from `cmd/orole-sandbox-setup`, built `-H windowsgui`): the only binary Orole ever runs elevated. The daemon starts it with a UAC prompt to enable Windows Hypervisor Platform for the bot computer. It sits next to `orole-daemon.exe` in the installed resources folder."

- [ ] **Step 9: Verify the build and the real UAC flow**

Run: `./scripts/build.ps1 -DaemonOnly; Test-Path build\orole-sandbox-setup.exe`
Expected: `True`.

Manual (the owner, because UAC needs a human): with WHP already enabled from Task 1, run
`go test -run TestManualEnableWHP -v ./internal/sandbox` using this temporary test, then delete it before committing:

```go
//go:build windows

package sandbox

func TestManualEnableWHP(t *testing.T) {
	r, err := EnableWHP(context.Background(), `..\..\build\orole-sandbox-setup.exe`, Paths{Root: t.TempDir()})
	t.Logf("result=%+v err=%v", r, err)
}
```

Expected: one UAC prompt. "Yes" → `result={RebootRequired:false} err=<nil>` (already enabled). "No" → `err=sandbox: Windows did not get permission…`. Record both in the PR description.

- [ ] **Step 10: Commit**

```bash
git add internal/sandbox/setupcode internal/sandbox/setup.go internal/sandbox/setup_test.go internal/sandbox/setup_windows.go internal/sandbox/setup_windows_test.go internal/sandbox/setup_other.go cmd/orole-sandbox-setup mygo.json scripts/build.ps1 docs/development/packaging.md
git commit -m "feat(sandbox): enable windows hypervisor platform through one elevated helper

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: NixOS guest image flake

**Files:**
- Create: `guest/flake.nix`, `guest/flake.lock` (generated), `guest/configuration.nix`, `guest/debug.nix`, `guest/orole-guest.nix`, `guest/VERSION`
- Create: `cmd/orole-guest/token_linux.go`, `cmd/orole-guest/token_linux_test.go`
- Modify: `cmd/orole-guest/main.go` (token source)
- Create: `docs/development/guest-image.md`
- Modify: `.gitignore` (add `/guest/result*`)

**Interfaces:**
- Consumes: `cmd/orole-guest` (phase 1); the QEMU argv from Task 3 (`/dev/vda` virtio-blk root, fw_cfg token, virtio-serial port `org.orole.guest.0`, `console=ttyS0`).
- Produces: `nix build ./guest#image` gives `vmlinuz`, `initrd`, `cmdline`, `base.qcow2`, `SHA256SUMS`; `./guest#image-debug` is the same plus root autologin on the serial console. Guest facts phases 3/4 rely on: user `bot` (uid 1000, home `/home/bot`), Chromium CDP on `127.0.0.1:9222` with profile `/home/bot/.config/orole-chromium`, VNC on `127.0.0.1:5900`. Also `func bootToken() (string, error)` in `cmd/orole-guest`.

- [ ] **Step 1: Write the failing token test**

`cmd/orole-guest/token_linux_test.go`:

```go
//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootTokenPrefersFwCfg(t *testing.T) {
	dir := t.TempDir()
	fw, cmdline := filepath.Join(dir, "raw"), filepath.Join(dir, "cmdline")
	os.WriteFile(fw, []byte("fwtoken\n"), 0o400)
	os.WriteFile(cmdline, []byte("init=/x orole.token=cmdtoken quiet\n"), 0o444)
	if tok, err := readBootToken(fw, cmdline); err != nil || tok != "fwtoken" {
		t.Fatalf("got %q %v", tok, err)
	}
	os.Remove(fw)
	if tok, err := readBootToken(fw, cmdline); err != nil || tok != "cmdtoken" {
		t.Fatalf("fallback got %q %v", tok, err)
	}
	os.WriteFile(cmdline, []byte("init=/x quiet\n"), 0o444)
	if _, err := readBootToken(fw, cmdline); err == nil {
		t.Fatal("no token anywhere must be an error")
	}
}
```

Run: `wsl -d Ubuntu -- bash -lc "cd /mnt/c/code/projects/.orole-worktrees/orole-os/196-sandbox-core && go test ./cmd/orole-guest -run BootToken"` (or rely on CI's portable lane if WSL has no Go)
Expected: FAIL (`undefined: readBootToken`)

- [ ] **Step 2: Implement and switch orole-guest to it**

`cmd/orole-guest/token_linux.go`:

```go
//go:build linux

package main

import (
	"errors"
	"os"
	"strings"
)

const fwCfgTokenPath = "/sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw"

// bootToken returns the per-boot channel token. The host passes it with -fw_cfg (root-only in sysfs and
// never printed); orole.token= on the kernel command line is only a fallback, because the console log
// shows the command line.
func bootToken() (string, error) { return readBootToken(fwCfgTokenPath, "/proc/cmdline") }

func readBootToken(fwCfg, cmdline string) (string, error) {
	if b, err := os.ReadFile(fwCfg); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			return tok, nil
		}
	}
	b, err := os.ReadFile(cmdline)
	if err != nil {
		return "", err
	}
	for _, f := range strings.Fields(string(b)) {
		if v, ok := strings.CutPrefix(f, "orole.token="); ok && v != "" {
			return v, nil
		}
	}
	return "", errors.New("orole-guest: no channel token (fw_cfg opt/orole/token or orole.token=)")
}
```

Find phase 1's token source with `grep -n "orole.token\|proc/cmdline" cmd/orole-guest/*.go`. Replace that call in `main.go` with `bootToken()`, and delete phase 1's parsing helper if it is now unused. Its test cases move to `token_linux_test.go`. Run `go vet ./cmd/orole-guest/...` with `GOOS=linux` and run the test from Step 1 → PASS.

- [ ] **Step 3: Write the flake**

`guest/VERSION`:

```
0.1.0
```

`guest/flake.nix`:

```nix
{
  description = "Orole bot computer: NixOS guest image (kernel, initrd, kernel command line, base disk)";

  # Pinned nixos-unstable (has go_1_27). Update with `nix flake update` and bump guest/VERSION.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/7fd4004d0511991fff3a0524856a9be09e28c5f0";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      lib = nixpkgs.lib;
      pkgs = nixpkgs.legacyPackages.${system};
      version = lib.strings.trim (builtins.readFile ./VERSION);
      orole-guest = pkgs.callPackage ./orole-guest.nix { inherit version; src = ../.; };

      # mkImage builds the four files the host downloads: vmlinuz, initrd, cmdline (holds init=<toplevel>/init,
      # which changes every build) and base.qcow2 (compressed qcow2, ext4 without a partition table, used
      # read-only as the backing file of every bot's overlay).
      mkImage = name: extraModules:
        let
          nixos = lib.nixosSystem {
            inherit system;
            specialArgs = { inherit orole-guest; };
            modules = [ ./configuration.nix ] ++ extraModules;
          };
          cfg = nixos.config;
          disk = import "${nixpkgs}/nixos/lib/make-disk-image.nix" {
            inherit pkgs lib;
            config = cfg;
            format = "qcow2-compressed";
            partitionTableType = "none";
            label = "nixos";
            diskSize = "auto";
            additionalSpace = "1024M";
            copyChannel = false;
            installBootLoader = false;
          };
          cmdline = "init=${cfg.system.build.toplevel}/init ${toString cfg.boot.kernelParams}";
        in
        pkgs.runCommand "${name}-${version}" { } ''
          mkdir -p $out
          cp ${cfg.system.build.kernel}/${cfg.system.boot.loader.kernelFile} $out/vmlinuz
          cp ${cfg.system.build.initialRamdisk}/${cfg.system.boot.loader.initrdFile} $out/initrd
          cp ${disk}/nixos.qcow2 $out/base.qcow2
          printf '%s' ${lib.escapeShellArg cmdline} > $out/cmdline
          cd $out && sha256sum vmlinuz initrd cmdline base.qcow2 > SHA256SUMS
        '';
    in
    {
      packages.${system} = {
        inherit orole-guest;
        image = mkImage "orole-guest-image" [ ];
        image-debug = mkImage "orole-guest-image-debug" [ ./debug.nix ];
        default = self.packages.${system}.image;
      };
    };
}
```

`guest/orole-guest.nix`:

```nix
# orole-guest (cmd/orole-guest) built from this repository. vendorHash must change whenever go.sum does:
# set it to lib.fakeHash, run `nix build ./guest#orole-guest`, and paste the "got:" hash.
{ lib, buildGoModule, go_1_27, src, version }:

(buildGoModule.override { go = go_1_27; }) {
  pname = "orole-guest";
  inherit version;
  src = lib.fileset.toSource {
    root = src;
    fileset = lib.fileset.fileFilter
      (f: f.hasExt "go" || f.name == "go.mod" || f.name == "go.sum" || f.name == "mygo.json")
      src;
  };
  vendorHash = lib.fakeHash;
  subPackages = [ "cmd/orole-guest" ];
  env.CGO_ENABLED = "0";
  ldflags = [ "-s" "-w" "-X github.com/rayorole/orole-os-desktop.Version=guest-${version}" ];
  doCheck = false; # tested by `go test` in CI
  meta.mainProgram = "orole-guest";
}
```

`guest/configuration.nix`:

```nix
# The bot computer. Agents see an ordinary Linux (nix-ld + envfs give FHS paths); Orole writes this file,
# agents never run nixos-rebuild (direct kernel boot, system.switch disabled).
{ config, lib, pkgs, modulesPath, orole-guest, ... }:

let
  # Chromium in the kiosk compositor. CDP listens on guest loopback only (orole-guest relays it over the
  # channel); the profile lives on the bot's disk so logins survive restarts. Restarted if it exits.
  browser = pkgs.writeShellScript "orole-browser" ''
    mkdir -p /home/bot/.config/orole-chromium
    while true; do
      ${pkgs.chromium}/bin/chromium \
        --ozone-platform=wayland \
        --user-data-dir=/home/bot/.config/orole-chromium \
        --remote-debugging-address=127.0.0.1 \
        --remote-debugging-port=9222 \
        --no-first-run --no-default-browser-check \
        --disable-gpu \
        --start-maximized \
        about:blank
      sleep 1
    done
  '';
in
{
  imports = [
    "${modulesPath}/profiles/minimal.nix"
    "${modulesPath}/profiles/qemu-guest.nix"
  ];

  system.stateVersion = "26.05";

  # Direct kernel boot: no bootloader, systemd initrd, no switch-to-configuration (perl-free activation).
  boot.loader.grub.enable = false;
  boot.initrd.systemd.enable = true;
  boot.initrd.availableKernelModules = [ "virtio_pci" "virtio_blk" "virtio_net" "virtio_console" "virtio_rng" "ext4" ];
  boot.kernelModules = [ "qemu_fw_cfg" ];
  boot.kernelParams = [ "console=ttyS0" "quiet" ];
  system.switch.enable = false;
  services.userborn.enable = true;
  documentation.enable = false;

  fileSystems."/" = { device = "/dev/disk/by-label/nixos"; fsType = "ext4"; autoResize = true; };

  networking.hostName = "orole-computer";
  networking.useNetworkd = true;
  systemd.network.networks."10-uplink" = { matchConfig.Name = "en* eth*"; networkConfig.DHCP = "ipv4"; };
  networking.firewall.enable = false;
  # QEMU user networking maps 10.0.2.2 to the host's loopback. Keep the bot (uid 1000) off it; DNS at
  # 10.0.2.3 stays reachable. Root in the guest could undo this, and the bot has no sudo. The home LAN
  # stays reachable: a documented, accepted risk (spec, "Network").
  networking.nftables = {
    enable = true;
    tables.orole-host = {
      family = "inet";
      content = ''
        chain output {
          type filter hook output priority 0; policy accept;
          meta skuid 1000 ip daddr 10.0.2.2 reject
        }
      '';
    };
  };

  users.mutableUsers = false;
  users.users.root.hashedPassword = "!";
  users.users.bot = { isNormalUser = true; uid = 1000; home = "/home/bot"; createHome = true; hashedPassword = "!"; };
  security.sudo.enable = false;

  # Desktop: cage on wlroots' headless backend (no GPU device), software rendering; wayvnc exports it on
  # guest loopback for orole-guest's screen relay.
  services.cage = {
    enable = true;
    user = "bot";
    program = browser;
    environment = {
      WLR_BACKENDS = "headless";
      WLR_RENDERER = "pixman";
      WLR_LIBINPUT_NO_DEVICES = "1";
      WLR_HEADLESS_OUTPUTS = "1";
    };
  };
  systemd.services."cage-tty1".serviceConfig = { Restart = "always"; RestartSec = 2; };
  systemd.services.wayvnc = {
    description = "VNC server for the bot computer's screen (guest loopback only)";
    after = [ "cage-tty1.service" ];
    wantedBy = [ "graphical.target" ];
    serviceConfig = {
      User = "bot";
      Environment = [ "XDG_RUNTIME_DIR=/run/user/1000" "WAYLAND_DISPLAY=wayland-0" ];
      ExecStart = "${pkgs.wayvnc}/bin/wayvnc 127.0.0.1 5900";
      Restart = "always";
      RestartSec = 2;
    };
  };
  systemd.defaultUnit = "graphical.target";

  systemd.services.orole-guest = {
    description = "Orole guest agent (virtio-serial org.orole.guest.0)";
    after = [ "systemd-modules-load.service" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = { ExecStart = lib.getExe orole-guest; Restart = "always"; RestartSec = 1; };
  };

  environment.systemPackages = with pkgs; [
    python3 uv nodejs git curl jq ripgrep chromium
    coreutils findutils gnugrep gnused gawk which file unzip zip gnutar xz
  ];
  programs.nix-ld = {
    enable = true;
    libraries = with pkgs; [ stdenv.cc.cc.lib zlib openssl glib nss nspr libgcc ];
  };
  services.envfs.enable = true;
  nix.settings.experimental-features = [ "nix-command" "flakes" ];
  services.journald.extraConfig = "SystemMaxUse=64M";
}
```

If the spike recorded `host_loopback_reachable=false`, keep the nftables table anyway. It costs nothing and protects against a later QEMU build that changes the default.

`guest/debug.nix`:

```nix
# Debug variant only (never released): root shell on the serial console for manual checks.
{ lib, ... }:
{
  services.getty.autologinUser = lib.mkForce "root";
  boot.kernelParams = lib.mkForce [ "console=ttyS0" ];
}
```

`.gitignore`: add a line `/guest/result*`.

- [ ] **Step 4: Write the local build instructions**

`docs/development/guest-image.md`:

````markdown
# Building the bot computer image

The guest image is a NixOS system built from `guest/flake.nix`. CI builds released images
(`.github/workflows/guest-image.yml`, tag `guest-v<guest/VERSION>`); this page is for building and testing one
locally on Windows through WSL. Nix does not run on Windows itself.

## One-time WSL setup (Ubuntu)

```bash
sh <(curl --proto '=https' --tlsv1.2 -L https://nixos.org/nix/install) --daemon --yes
sudo tee -a /etc/nix/nix.conf <<'EOF'
experimental-features = nix-command flakes
system-features = nixos-test benchmark big-parallel kvm
EOF
sudo systemctl restart nix-daemon
git config --global --add safe.directory '*'   # the repo lives on /mnt/c, owned by the Windows user
ls -l /dev/kvm                                 # make-disk-image is much faster with KVM (WSL2 nests it on Windows 11)
```

## Build

Nix only sees files tracked by git: `git add` new files under `guest/` first.

```bash
cd /mnt/c/code/projects/.orole-worktrees/orole-os/196-sandbox-core
nix build ./guest#image -L --out-link guest/result          # release variant
nix build ./guest#image-debug -L --out-link guest/result-debug
mkdir -p /mnt/c/Users/$WINUSER/AppData/Local/Temp/orole-guest && cp -L guest/result-debug/* "$_"/
```

When `go.sum` changes, the `orole-guest` build fails with a hash mismatch. Set `vendorHash = lib.fakeHash;` in
`guest/orole-guest.nix`, run `nix build ./guest#orole-guest`, and paste the `got: sha256-…` value.

## Boot it on Windows

With QEMU unpacked (see Task 1 of the phase 2 plan, or an installed runtime under `<data>\sandbox\runtime\`):

```powershell
$q = "$env:TEMP\orole-spike\qemu"; $img = "$env:TEMP\orole-guest"
& "$q\qemu-img.exe" create -f qcow2 -F qcow2 -b "$img\base.qcow2" "$env:TEMP\dbg.qcow2" 10G
& "$q\qemu-system-x86_64.exe" -nodefaults -no-user-config -machine q35 -accel whpx -cpu host -m 2048 -smp 2 `
  -display none -vga none -L "$q\share" -kernel "$img\vmlinuz" -initrd "$img\initrd" `
  -append "$(Get-Content -Raw $img\cmdline) console=ttyS0" `
  -drive "if=none,id=root,format=qcow2,file=$env:TEMP\dbg.qcow2" -device virtio-blk-pci,drive=root `
  -netdev user,id=net0 -device virtio-net-pci,netdev=net0,romfile= -device virtio-rng-pci `
  -fw_cfg name=opt/orole/token,string=test -serial stdio
```

The debug image logs root in on the serial console. Quit QEMU with `poweroff`.
````

- [ ] **Step 5: Build in WSL and fix the vendor hash**

```bash
cd /mnt/c/code/projects/.orole-worktrees/orole-os/196-sandbox-core
git add guest cmd/orole-guest .gitignore
nix flake lock ./guest
nix build ./guest#orole-guest -L 2>&1 | tail -5   # fails: hash mismatch, prints "got: sha256-…"
```

Replace `vendorHash = lib.fakeHash;` with `vendorHash = "sha256-<got value>";`, then:

```bash
nix build ./guest#orole-guest -L && ls -l result/bin/orole-guest
nix build ./guest#image-debug -L --out-link guest/result-debug && ls -l guest/result-debug/
```

Expected: `vmlinuz`, `initrd`, `cmdline`, `base.qcow2` (record its size; it must be < 2 GiB) and `SHA256SUMS`. If Chromium's closure drags in a source build (hours, no cache hit), check that nothing in `configuration.nix` overrides Chromium's inputs. The nixpkgs pin must be one Hydra has built (`nixos-unstable` heads are).

- [ ] **Step 6: Boot the debug image and run the checks (and the NixOS spike criteria)**

Boot with the command from `guest-image.md`. At the root prompt, run each check and record the results in `docs/architecture/bot-computer.md` under "Guest image checks":

```bash
systemd-analyze                                    # boot time
systemctl is-active cage-tty1 wayvnc               # active active
cat /sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw   # test
ss -ltnp | grep -E '5900|9222'                     # both bound to 127.0.0.1 only
runuser -u bot -- curl -s http://127.0.0.1:9222/json/version   # JSON with "Browser"
nft list ruleset | grep 10.0.2.2                   # the reject rule
runuser -u bot -- curl -sS -m 5 -o /dev/null -w '%{http_code}\n' https://example.com   # 200
df -h /                                            # about 10G (autoResize)
free -m                                            # idle RAM with Chromium running
python3 -c 'print(1+1)'; node -e 'console.log(2)'; git --version; jq --version; rg --version
# NixOS report spike criteria, as the bot:
runuser -u bot -- bash -lc 'cd ~ && uv venv && . .venv/bin/activate && uv pip install numpy pandas && python -c "import numpy, pandas; print(numpy.__version__)"'
runuser -u bot -- bash -lc 'mkdir -p ~/n && cd ~/n && npm init -y >/dev/null && npm install better-sqlite3 && node -e "new (require(\"better-sqlite3\"))(\":memory:\"); console.log(\"ok\")"'
runuser -u bot -- bash -lc 'cd ~ && curl -sSLo node.tar.xz https://nodejs.org/dist/v22.11.0/node-v22.11.0-linux-x64.tar.xz && tar xf node.tar.xz && ./node-v22.11.0-linux-x64/bin/node -v'   # nix-ld
runuser -u bot -- bash -lc 'printf "#!/bin/bash\necho bash-ok\n" > ~/t.sh && chmod +x ~/t.sh && ~/t.sh'   # envfs shebang
runuser -u bot -- bash -lc 'cd ~/n && npx -y playwright install chromium && echo playwright-ok'
```

Expected: all pass, except Playwright's browser download, which the NixOS report expects to fail. Record it; phase 3 drives the system Chromium over CDP, so it is not a blocker. **Gate:** if `uv pip install`, the native npm module, the nix-ld binary or the bash shebang fail and cannot be fixed with a `nix-ld` library addition, stop and report to the owner. The spec's fallback is Debian, which is a separate plan.

- [ ] **Step 7: Commit**

```bash
git add guest .gitignore cmd/orole-guest docs/development/guest-image.md docs/architecture/bot-computer.md
git commit -m "feat(guest): nixos flake for the bot computer image

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: QEMU asset workflow

**Files:**
- Create: `.github/workflows/qemu-asset.yml`

**Interfaces:**
- Consumes: nothing in Go.
- Produces: a draft release `qemu-v20260811` with assets `qemu-20260811-win64.zip` and `SHA256SUMS` (lines `<sha256>  <name>`). Task 11 reads them.

- [ ] **Step 1: Write the workflow**

`.github/workflows/qemu-asset.yml`:

```yaml
name: qemu-asset

# Tag qemu-v<installer date> -> download the pinned official QEMU for Windows installer (Stefan Weil's build,
# qemu.weilnetz.de), verify its SHA-512, unpack it with 7-Zip and repackage only what the bot computer runs
# (qemu-system-x86_64, qemu-img, their DLLs, firmware) into one zip on a DRAFT release. Nothing here
# publishes: a human reviews the draft and publishes it; Orole pins the zip's SHA-256 in
# internal/sandbox/pins.go (scripts/sandbox-pins.ps1).
on:
  push:
    tags: ["qemu-v*"]

permissions:
  contents: read

concurrency:
  group: qemu-asset-${{ github.ref }}
  cancel-in-progress: false

env:
  QEMU_BUILD: "20260811"
  QEMU_SHA512: "5bcf9eed634e8575a37b74f445af41a2fe4106da512d0c30c368301d4c105037fdfab40a5287367a28a957624cddebbc8c07e16c88ab6634f554cdf3d16bf543"

jobs:
  package:
    runs-on: windows-latest
    timeout-minutes: 30
    permissions:
      contents: write # the draft release and its uploads
    env:
      GH_TOKEN: ${{ github.token }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 1
          persist-credentials: false
      - name: Tag matches the pinned build
        shell: pwsh
        run: |
          if ($env:GITHUB_REF_NAME -ne "qemu-v$env:QEMU_BUILD") { throw "tag $env:GITHUB_REF_NAME does not match QEMU_BUILD $env:QEMU_BUILD" }
      - name: Download and verify the installer
        shell: pwsh
        run: |
          $f = "qemu-w64-setup-$env:QEMU_BUILD.exe"
          curl.exe -sSfL -o $f "https://qemu.weilnetz.de/w64/$f"
          if ($LASTEXITCODE) { throw "download failed" }
          $h = (Get-FileHash $f -Algorithm SHA512).Hash.ToLower()
          if ($h -ne $env:QEMU_SHA512) { throw "SHA-512 mismatch: $h" }
      - name: Unpack and keep what the bot computer needs
        shell: pwsh
        run: |
          7z x "qemu-w64-setup-$env:QEMU_BUILD.exe" -ofull -y | Out-Null
          if ($LASTEXITCODE) { throw "7z failed" }
          New-Item -ItemType Directory pkg, pkg\share | Out-Null
          Copy-Item full\qemu-system-x86_64.exe, full\qemu-img.exe -Destination pkg\
          Copy-Item full\*.dll -Destination pkg\
          Copy-Item full\share\*.bin, full\share\*.rom -Destination pkg\share\
          Get-ChildItem full -File | Where-Object { $_.Name -match '^(COPYING|LICENSE|README)' } | Copy-Item -Destination pkg\
          Set-Content pkg\SOURCE.txt -Value @(
            "QEMU for Windows, build $env:QEMU_BUILD, unmodified files from",
            "https://qemu.weilnetz.de/w64/qemu-w64-setup-$env:QEMU_BUILD.exe (SHA-512 $env:QEMU_SHA512).",
            "QEMU is free software under the GNU General Public License version 2.",
            "Source: https://gitlab.com/qemu-project/qemu ; Windows build scripts: https://qemu.weilnetz.de/"
          )
      - name: Smoke test (starts, has WHPX, qemu-img runs)
        shell: pwsh
        run: |
          & pkg\qemu-system-x86_64.exe --version
          if ($LASTEXITCODE) { throw "qemu-system-x86_64 does not start (missing DLL?)" }
          $accel = (& pkg\qemu-system-x86_64.exe -accel help) -join "`n"
          if ($accel -notmatch 'whpx') { throw "this build has no WHPX accelerator: $accel" }
          & pkg\qemu-img.exe --version
          if ($LASTEXITCODE) { throw "qemu-img does not start" }
          if (-not (Test-Path pkg\share\bios-256k.bin)) { throw "firmware missing" }
      - name: Zip and hash
        shell: pwsh
        run: |
          $zip = "qemu-$env:QEMU_BUILD-win64.zip"
          Push-Location pkg
          7z a -tzip "..\$zip" * -r | Out-Null
          if ($LASTEXITCODE) { throw "zip failed" }
          Pop-Location
          $h = (Get-FileHash $zip -Algorithm SHA256).Hash.ToLower()
          [IO.File]::WriteAllText("SHA256SUMS", "$h  $zip`n")
          Get-Content SHA256SUMS
          (Get-Item $zip).Length
      - name: Create the draft release
        shell: pwsh
        run: |
          $tag = $env:GITHUB_REF_NAME
          gh release view $tag --repo $env:GITHUB_REPOSITORY 2>$null | Out-Null
          if ($LASTEXITCODE -eq 0) { throw "release $tag exists; refusing to replace its files" }
          gh release create $tag --repo $env:GITHUB_REPOSITORY --draft --verify-tag --prerelease --latest=false `
            --title "QEMU for the bot computer ($env:QEMU_BUILD)" `
            --notes "Unmodified QEMU for Windows build $env:QEMU_BUILD (see SOURCE.txt in the zip), repackaged for Orole's bot computer." `
            "qemu-$env:QEMU_BUILD-win64.zip" SHA256SUMS
          if ($LASTEXITCODE) { throw "gh release create failed" }
      - name: Draft ready, publish manually
        shell: pwsh
        run: |
          $cmd = "gh release edit $env:GITHUB_REF_NAME --draft=false --latest=false"
          Write-Host "::notice title=Draft release $env:GITHUB_REF_NAME is ready::Review it, then publish with: $cmd"
          "Draft release **$env:GITHUB_REF_NAME** is ready. Review it, then publish with:`n`n``$cmd```n" >> $env:GITHUB_STEP_SUMMARY
```

- [ ] **Step 2: Lint it the way CI does**

Run (WSL or Git Bash):

```bash
curl -sSfL -o /tmp/actionlint.tar.gz https://github.com/rhysd/actionlint/releases/download/v1.7.12/actionlint_1.7.12_linux_amd64.tar.gz
echo "8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8  /tmp/actionlint.tar.gz" | sha256sum -c -
tar -xzf /tmp/actionlint.tar.gz -C /tmp actionlint && /tmp/actionlint .github/workflows/qemu-asset.yml
```

Expected: no output (clean). If you are not in WSL, the `actionlint` job in CI runs the same check on the PR.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/qemu-asset.yml
git commit -m "ci(sandbox): repackage the pinned qemu for windows as a release asset

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Guest image workflow

**Files:**
- Create: `.github/workflows/guest-image.yml`

**Interfaces:**
- Consumes: `guest/flake.nix` `packages.x86_64-linux.image`, `guest/VERSION` (Task 8).
- Produces: a draft release `guest-v0.1.0` with `vmlinuz`, `initrd`, `cmdline`, `base.qcow2`, `SHA256SUMS`.

- [ ] **Step 1: Write the workflow**

`.github/workflows/guest-image.yml`:

```yaml
name: guest-image

# Tag guest-v<guest/VERSION> -> build the NixOS bot computer image from guest/flake.nix (kernel, initrd,
# kernel command line, compressed qcow2 base disk) and upload it with SHA256SUMS to a DRAFT release. A human
# reviews and publishes the draft; Orole downloads only files whose hashes are pinned in
# internal/sandbox/pins.go (scripts/sandbox-pins.ps1).
on:
  push:
    tags: ["guest-v*"]

permissions:
  contents: read

concurrency:
  group: guest-image-${{ github.ref }}
  cancel-in-progress: false

jobs:
  image:
    runs-on: ubuntu-latest
    timeout-minutes: 120
    permissions:
      contents: write # the draft release and its uploads
    env:
      GH_TOKEN: ${{ github.token }}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          fetch-depth: 1
          persist-credentials: false
      - name: Tag matches guest/VERSION
        run: |
          want="guest-v$(tr -d '[:space:]' < guest/VERSION)"
          if [ "$GITHUB_REF_NAME" != "$want" ]; then
            echo "tag $GITHUB_REF_NAME does not match guest/VERSION ($want)"
            exit 1
          fi
      - name: Let the image build use KVM
        run: |
          echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' | sudo tee /etc/udev/rules.d/99-kvm4all.rules
          sudo udevadm control --reload-rules
          sudo udevadm trigger --name-match=kvm
      - uses: cachix/install-nix-action@448cde739abec1eaf0646249e3d25bb8d9bc7702 # v31.11.2
        with:
          extra_nix_config: |
            system-features = nixos-test benchmark big-parallel kvm
      - name: Build the image
        run: nix build ./guest#image --print-build-logs --out-link result
      - name: Hashes and the 2 GiB asset limit
        run: |
          mkdir out
          cp -L result/vmlinuz result/initrd result/cmdline result/base.qcow2 result/SHA256SUMS out/
          cd out
          sha256sum -c SHA256SUMS
          for f in vmlinuz initrd cmdline base.qcow2; do
            size=$(stat -c %s "$f")
            echo "$f: $size bytes"
            if [ "$size" -ge 2147483648 ]; then
              echo "$f is over GitHub's 2 GiB release asset limit"
              exit 1
            fi
          done
      - name: Create the draft release
        run: |
          version="${GITHUB_REF_NAME#guest-v}"
          if gh release view "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" >/dev/null 2>&1; then
            echo "release $GITHUB_REF_NAME exists; refusing to replace its files"
            exit 1
          fi
          gh release create "$GITHUB_REF_NAME" --repo "$GITHUB_REPOSITORY" --draft --verify-tag --prerelease --latest=false \
            --title "Bot computer image $version" \
            --notes "NixOS guest image for the bot computer, built from guest/flake.nix at this tag." \
            out/vmlinuz out/initrd out/cmdline out/base.qcow2 out/SHA256SUMS
      - name: Draft ready, publish manually
        run: |
          cmd="gh release edit $GITHUB_REF_NAME --draft=false --latest=false"
          echo "::notice title=Draft release $GITHUB_REF_NAME is ready::Review it, then publish with: $cmd"
          printf 'Draft release **%s** is ready. Review it, then publish with:\n\n`%s`\n' "$GITHUB_REF_NAME" "$cmd" >> "$GITHUB_STEP_SUMMARY"
```

- [ ] **Step 2: Lint it**

Run: `/tmp/actionlint .github/workflows/guest-image.yml` (binary from Task 9 Step 2)
Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/guest-image.yml
git commit -m "ci(guest): build the bot computer image and draft a guest release

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Publish the assets, pin them, real-boot test

**Files:**
- Create: `scripts/sandbox-pins.ps1`
- Modify: `internal/sandbox/pins.go` (generated)
- Create: `internal/sandbox/e2e_windows_test.go`

**Interfaces:**
- Consumes: the releases from Tasks 9 and 10; `NewHostBackend`, `Runtime`, `Pins`, `Asset` (Tasks 5 and 6); `NewGuest`, `Guest.Health`, `Guest.Exec`, `Guest.WriteFile`, `Guest.ReadFile`, `wire.ExecRequest` (phase 1).
- Produces: `DefaultPins` with real values.

- [ ] **Step 1: Ask the owner, then push the tags**

Message to the owner: "Tasks 9 and 10 are ready. Pushing tags `qemu-v20260811` and `guest-v0.1.0` (on this branch's HEAD) runs the two workflows, which create **draft** releases. May I push them?" Only after a yes:

```bash
git push -u origin feat/197-sandbox-windows
git tag qemu-v20260811 && git push origin qemu-v20260811
git tag guest-v0.1.0 && git push origin guest-v0.1.0
gh run watch --repo rayorole/orole-os-desktop $(gh run list --repo rayorole/orole-os-desktop --workflow qemu-asset.yml -L 1 --json databaseId --jq '.[0].databaseId')
gh run watch --repo rayorole/orole-os-desktop $(gh run list --repo rayorole/orole-os-desktop --workflow guest-image.yml -L 1 --json databaseId --jq '.[0].databaseId')
```

Expected: both runs green and two draft releases. Then ask the owner to review and publish both drafts (`gh release edit qemu-v20260811 --draft=false --latest=false` and the same for `guest-v0.1.0`). Check anonymous download:
`curl.exe -sSfIL https://github.com/rayorole/orole-os-desktop/releases/download/guest-v0.1.0/SHA256SUMS` → `200`.

- [ ] **Step 2: Write the pins generator**

`scripts/sandbox-pins.ps1`:

```powershell
<#
.SYNOPSIS
  Writes internal/sandbox/pins.go from the published qemu-v* and guest-v* releases: each asset's SHA-256 (from
  the release's SHA256SUMS, which its workflow computed) and size (from GitHub). Run after both releases are
  published; review the diff before committing.
#>
param(
  [Parameter(Mandatory)][string]$QemuTag,   # e.g. qemu-v20260811
  [Parameter(Mandatory)][string]$GuestTag   # e.g. guest-v0.1.0
)
$ErrorActionPreference = "Stop"
Set-Location (Split-Path $PSScriptRoot -Parent)
$repo = "rayorole/orole-os-desktop"

function Get-Assets([string]$tag) {
  $rel = gh release view $tag --repo $repo --json assets,isDraft | ConvertFrom-Json
  if ($LASTEXITCODE) { throw "cannot read release $tag" }
  if ($rel.isDraft) { throw "$tag is still a draft: users could not download it" }
  $dir = Join-Path ([IO.Path]::GetTempPath()) "orole-pins-$tag"
  New-Item -ItemType Directory -Force $dir | Out-Null
  gh release download $tag --repo $repo --pattern SHA256SUMS --dir $dir --clobber
  if ($LASTEXITCODE) { throw "cannot download SHA256SUMS of $tag" }
  $sums = @{}
  foreach ($line in Get-Content (Join-Path $dir "SHA256SUMS")) {
    $parts = $line.Trim() -split '\s+'
    if ($parts.Count -ge 2) { $sums[$parts[-1].TrimStart('*')] = $parts[0].ToLower() }
  }
  $out = @{}
  foreach ($a in $rel.assets | Where-Object { $_.name -ne "SHA256SUMS" }) {
    if (-not $sums.ContainsKey($a.name)) { throw "${tag}: $($a.name) is not listed in SHA256SUMS" }
    $out[$a.name] = [pscustomobject]@{ Name = $a.name; Sha = $sums[$a.name]; Size = [int64]$a.size }
  }
  return $out
}

function Format-Asset($a) { "Asset{Name: `"$($a.Name)`", SHA256: `"$($a.Sha)`", Size: $($a.Size)}" }

$qemuVersion = $QemuTag -replace '^qemu-v', ''
$imageVersion = $GuestTag -replace '^guest-v', ''
$q = Get-Assets $QemuTag
$g = Get-Assets $GuestTag
$zipName = "qemu-$qemuVersion-win64.zip"
if (-not $q.ContainsKey($zipName)) { throw "$QemuTag has no $zipName" }
$image = foreach ($n in "vmlinuz", "initrd", "cmdline", "base.qcow2") {
  if (-not $g.ContainsKey($n)) { throw "$GuestTag has no $n" }
  "`t`t" + (Format-Asset $g[$n]) + ","
}
$go = @"
// Code generated by scripts/sandbox-pins.ps1 -QemuTag $QemuTag -GuestTag $GuestTag; DO NOT EDIT.

package sandbox

// DefaultPins is the QEMU build and guest image this Orole build downloads (published GitHub releases of
// rayorole/orole-os-desktop). Every file is checked against these hashes before it is used.
var DefaultPins = Pins{
	QEMUVersion:  "$qemuVersion",
	QEMUTag:      "$QemuTag",
	QEMU:         $(Format-Asset $q[$zipName]),
	ImageVersion: "$imageVersion",
	ImageTag:     "$GuestTag",
	Image: []Asset{
$($image -join "`n")
	},
}
"@
[IO.File]::WriteAllText("internal/sandbox/pins.go", ($go -replace "`r`n", "`n"))
gofmt -w internal/sandbox/pins.go
Write-Host "wrote internal/sandbox/pins.go"
```

- [ ] **Step 3: Generate the pins and check them against the real download**

```powershell
./scripts/sandbox-pins.ps1 -QemuTag qemu-v20260811 -GuestTag guest-v0.1.0
git diff internal/sandbox/pins.go
```

Expected: hex hashes and sizes for 1 zip and 4 image files. The real download against these pins runs in Task 12 Step 6 (`TestManualSandboxRuntimeInstall`).

- [ ] **Step 4: Write the real-boot test**

`internal/sandbox/e2e_windows_test.go`:

```go
//go:build windows

package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// TestHostBackendBootsTheGuestImage boots the real guest on this PC (WHP on, ~4 GB free RAM):
//
//	$env:OROLE_SANDBOX_E2E_QEMU  = "<folder with qemu-system-x86_64.exe and share\>"
//	$env:OROLE_SANDBOX_E2E_IMAGE = "<folder with vmlinuz, initrd, cmdline, base.qcow2>"
//	go test -run TestHostBackendBootsTheGuestImage -v -timeout 20m ./internal/sandbox
func TestHostBackendBootsTheGuestImage(t *testing.T) {
	qemuDir, imageDir := os.Getenv("OROLE_SANDBOX_E2E_QEMU"), os.Getenv("OROLE_SANDBOX_E2E_IMAGE")
	if qemuDir == "" || imageDir == "" {
		t.Skip("set OROLE_SANDBOX_E2E_QEMU and OROLE_SANDBOX_E2E_IMAGE to run the real boot test")
	}
	root := shortRoot(t)
	paths := Paths{Root: filepath.Join(root, "sandbox")}
	copyTree(t, qemuDir, paths.Runtime("e2e"))
	copyTree(t, imageDir, paths.Image("e2e"))
	for _, d := range []string{paths.Runtime("e2e"), paths.Image("e2e")} {
		os.WriteFile(filepath.Join(d, completeMark), nil, 0o600)
	}
	pins := Pins{QEMUVersion: "e2e", QEMUTag: "e2e", QEMU: Asset{Name: "e2e", SHA256: "e2e", Size: 1},
		ImageVersion: "e2e", ImageTag: "e2e", Image: []Asset{{Name: imageKernel}}}
	rt := NewRuntime(RuntimeConfig{Paths: paths, Pins: pins})
	b := NewHostBackend(rt, paths)
	ctx := context.Background()
	if a := b.Availability(ctx); !a.Ready {
		t.Skipf("this PC cannot run the computer: %+v", a)
	}
	img, err := rt.Image()
	if err != nil {
		t.Fatal(err)
	}

	boot := func(botID string) (VM, *Guest, string) {
		t.Helper()
		tok := make([]byte, 32)
		rand.Read(tok)
		spec := BootSpec{BotID: botID, Limits: DefaultLimits(), BaseImage: img.BaseImage, Kernel: img.Kernel, Initrd: img.Initrd,
			Overlay: paths.Overlay(botID), Token: hex.EncodeToString(tok), LogPath: paths.Log(root, botID)}
		start := time.Now()
		bctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		vm, err := b.Boot(bctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		g, err := NewGuest(vm.Conn(), spec.Token)
		if err != nil {
			t.Fatal(err)
		}
		for {
			hctx, hcancel := context.WithTimeout(ctx, 5*time.Second)
			h, err := g.Health(hctx)
			hcancel()
			if err == nil && h.Ready {
				break
			}
			if time.Since(start) > 120*time.Second {
				t.Fatalf("guest not ready after 120s: %v", err)
			}
			time.Sleep(time.Second)
		}
		t.Logf("E2E RESULT %s boot_to_ready=%s", botID, time.Since(start))
		return vm, g, spec.Token
	}
	run := func(g *Guest, cmd string) string {
		t.Helper()
		res, err := g.Exec(ctx, wire.ExecRequest{Command: cmd, Timeout: 60})
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		return res.Stdout
	}

	vm, g, token := boot("e2e-bot")
	if out := run(g, `python3 -c "print(1+1)"`); strings.TrimSpace(out) != "2" {
		t.Fatalf("python: %q", out)
	}
	cdp := ""
	for i := 0; i < 30 && !strings.Contains(cdp, "Browser"); i++ {
		cdp = run(g, "curl -s http://127.0.0.1:9222/json/version || true")
		time.Sleep(time.Second)
	}
	if !strings.Contains(cdp, "Browser") {
		t.Fatalf("chromium CDP not up: %q", cdp)
	}
	t.Logf("E2E RESULT idle_ram=%s", strings.TrimSpace(run(g, "free -m | awk '/Mem:/{print $3\" MB used\"}'")))
	if _, err := g.WriteFile(ctx, "persist.txt", []byte("kept")); err != nil {
		t.Fatal(err)
	}
	run(g, "sync")
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	start := time.Now()
	if err := vm.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	t.Logf("E2E RESULT clean_shutdown=%s", time.Since(start))
	if log, _ := os.ReadFile(paths.Log(root, "e2e-bot")); strings.Contains(string(log), token) {
		t.Fatal("the channel token is in the console log")
	}

	// Daemon crash: a hard kill, then the same overlay boots again and kept the synced file.
	vm2, g2, _ := boot("e2e-bot")
	if f, err := g2.ReadFile(ctx, "persist.txt"); err != nil || string(f.Content) != "kept" {
		t.Fatalf("file after clean restart: %q %v", f.Content, err)
	}
	vm2.Kill()
	<-vm2.Done()
	vm3, _, _ := boot("e2e-bot")

	// Two bots share the read-only base image at the same time.
	vmB, _, _ := boot("e2e-other")
	for _, v := range []VM{vm3, vmB} {
		sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		v.Shutdown(sctx)
		cancel()
	}
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
```

If phase 1's `Guest.ReadFile` result field is not named `Content`, use the `wire.ReadFileResult` field from the master contract (`Content []byte`). It is named `Content` there.

- [ ] **Step 5: Run it on the dev PC**

```powershell
$env:OROLE_SANDBOX_E2E_QEMU = "$env:TEMP\orole-spike\qemu"
$env:OROLE_SANDBOX_E2E_IMAGE = "$env:TEMP\orole-e2e-image"   # gh release download guest-v0.1.0 --dir there
gh release download guest-v0.1.0 --repo rayorole/orole-os-desktop --dir $env:OROLE_SANDBOX_E2E_IMAGE
go test -run TestHostBackendBootsTheGuestImage -v -timeout 20m ./internal/sandbox
```

Expected: PASS with `E2E RESULT` lines (boot to ready, idle RAM, clean shutdown). Record them in `docs/architecture/bot-computer.md` under "Measured on the dev PC". Then check that a crashed daemon leaves no QEMU behind:
start the test again, wait for the first `E2E RESULT … boot_to_ready` line, kill the test binary with `taskkill /F /IM sandbox.test.exe`, then run `Get-Process qemu-system-x86_64 -ErrorAction SilentlyContinue`. Expected: nothing (the job object killed QEMU). Record the result.

- [ ] **Step 6: Run the unit tests and commit**

Run: `go test -race ./internal/sandbox/...`
Expected: PASS (the e2e test skips without its env vars).

```bash
git add scripts/sandbox-pins.ps1 internal/sandbox/pins.go internal/sandbox/e2e_windows_test.go docs/architecture/bot-computer.md
git commit -m "feat(sandbox): pin the published qemu and guest image releases

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Daemon wiring

**Files:**
- Create: `internal/daemon/svc_sandbox.go`
- Create: `internal/daemon/svc_sandbox_test.go`
- Modify: `internal/daemon/daemon.go` (fields, cleanup), `internal/daemon/services.go` (registration), `internal/daemon/svc_chat.go` (archive hook)

**Interfaces:**
- Consumes: `sandbox.NewManager`, `sandbox.Config`, `sandbox.NewStore`, `Manager.Availability/Status/Delete/Shutdown/Subscribe` (phase 1); `sandbox.NewRuntime`, `RuntimeConfig`, `RuntimeState`, `DefaultPins`, `NewHostBackend`, `EnableWHP`, `SetupHelperPath`, `SetupResult` (Tasks 5–7, 11); `d.Metrics.Snapshot` (`systemmetrics.HostMetrics.MemoryAvailableBytes`); `handle[P,R]`, `d.audit`, `outcomeOf`, `ipc.Error`, `ipc.CodeBadRequest`.
- Produces: `Daemon.Sandbox *sandbox.Manager` (phase 3 tools and phase 4 UI use it); IPC methods `sandbox.availability`, `sandbox.status{botId}`, `sandbox.setup.enable`, `sandbox.runtime.install`; events `sandbox.status`, `sandbox.availability`, `sandbox.runtime`; `func (d *Daemon) deleteBotComputer(botID string)`.

- [ ] **Step 1: Write the failing daemon test**

`internal/daemon/svc_sandbox_test.go`:

```go
package daemon_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/daemon"
	"github.com/rayorole/orole-os-desktop/internal/daemon/control"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
)

func TestSandboxIPCReportsAvailabilityAndStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROLE_HOME", dir)
	defer daemon.UseMemoryLLMCredentials()()
	stop := start(t, dir)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := control.NewClient(dir, "sandbox-wiring-test")
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var a sandbox.Availability
	if err := c.Call(ctx, "sandbox.availability", struct{}{}, &a); err != nil {
		t.Fatal(err)
	}
	// A fresh data dir never has the runtime installed, so it is never ready, and always says why.
	if a.Ready || a.Reason == "" || a.Detail == "" {
		t.Fatalf("fresh availability %+v", a)
	}
	if runtime.GOOS != "windows" && a.Reason != "unsupported-os" {
		t.Fatalf("non-Windows availability %+v", a)
	}

	var st sandbox.Status
	if err := c.Call(ctx, "sandbox.status", map[string]string{"botId": "no-such-bot"}, &st); err != nil {
		t.Fatal(err)
	}
	if st.State != sandbox.StateOff || st.BotID != "no-such-bot" {
		t.Fatalf("status %+v", st)
	}
	if err := c.Call(ctx, "sandbox.status", map[string]string{}, &st); err == nil {
		t.Fatal("status without botId must be refused")
	}
}

func TestArchivingABotDeletesItsComputer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROLE_HOME", dir)
	defer daemon.UseMemoryLLMCredentials()()
	stop := start(t, dir)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c := control.NewClient(dir, "sandbox-archive-test")
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var bot chat.Bot
	if err := c.Call(ctx, "chat.bots.save", chat.BotInput{Name: "Computer user", Avatar: "C", Color: "blue"}, &bot); err != nil {
		t.Fatal(err)
	}
	paths := sandbox.Paths{Root: filepath.Join(dir, "sandbox")}
	overlay := paths.Overlay(bot.ID)
	if err := os.MkdirAll(filepath.Dir(overlay), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("QFI"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "orole.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO sandboxes(bot_id,image_version,overlay_path,created_at) VALUES(?,?,?,?)`,
		bot.ID, "0.1.0", overlay, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	if err := c.Call(ctx, "chat.bots.archive", chat.BotRef{ID: bot.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandboxes WHERE bot_id=?`, bot.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left: %d, %v", n, err)
	}
}
```

If phase 1's `Store` writes `created_at` in seconds, not milliseconds, the insert still works (it is only an integer). `chat.BotRef` has field `ID` (JSON `id`), as `svc_chat.go` uses it.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/daemon -run "Sandbox|ArchivingABot" -v`
Expected: FAIL (`unknown method sandbox.availability` and the overlay still there)

- [ ] **Step 3: Implement the service**

`internal/daemon/svc_sandbox.go`:

```go
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
)

// sandboxRef names one bot's computer.
type sandboxRef struct {
	BotID string `json:"botId"`
}

// registerSandbox owns the bot computers: one QEMU/WHPX VM per bot (internal/sandbox), the pinned runtime
// download and the one-time Windows Hypervisor Platform setup. VMs are children of this daemon (job object)
// and stop with it; disks under <data>\sandbox survive.
//
// IPC: sandbox.availability, sandbox.status{botId}, sandbox.setup.enable (one UAC prompt, audited),
// sandbox.runtime.install (starts the background download, returns its state; calling it again while it runs
// returns progress). Events: sandbox.status, sandbox.availability, sandbox.runtime.
func (d *Daemon) registerSandbox() {
	paths := sandbox.Paths{Root: filepath.Join(d.DataDir, "sandbox")}
	life, cancel := context.WithCancel(context.Background())
	d.sandboxStop = cancel

	var m *sandbox.Manager
	rt := sandbox.NewRuntime(sandbox.RuntimeConfig{Paths: paths, Pins: sandbox.DefaultPins,
		OnChange: func(s sandbox.RuntimeState) {
			d.IPC.Publish("sandbox.runtime", s)
			if !s.Installing && m != nil {
				d.IPC.Publish("sandbox.availability", m.Availability(life))
			}
		}})
	m = sandbox.NewManager(sandbox.Config{
		Paths: paths, MaxRunning: 4, IdleTimeout: 15 * time.Minute, BootTimeout: 60 * time.Second, BusyWait: 30 * time.Second,
		FreeMemory: d.freeMemory, Now: time.Now, Image: rt.Image,
	}, sandbox.NewHostBackend(rt, paths), sandbox.NewStore(d.DB.DB))
	d.Sandbox = m
	m.Subscribe(func(s sandbox.Status) { d.IPC.Publish("sandbox.status", s) })

	helper := ""
	if exe, err := os.Executable(); err == nil {
		helper = sandbox.SetupHelperPath(exe)
	}

	handle(d, "sandbox.availability", func(ctx context.Context, _ none) (sandbox.Availability, error) {
		return m.Availability(ctx), nil
	})
	handle(d, "sandbox.status", func(ctx context.Context, p sandboxRef) (sandbox.Status, error) {
		if p.BotID == "" {
			return sandbox.Status{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "botId is required"}
		}
		return m.Status(p.BotID), nil
	})
	handle(d, "sandbox.setup.enable", func(ctx context.Context, _ none) (sandbox.SetupResult, error) {
		res, err := sandbox.EnableWHP(ctx, helper, paths)
		detail := "enabled"
		if res.RebootRequired {
			detail = "enabled, restart required"
		}
		if err != nil {
			detail = err.Error()
		}
		d.audit("sandbox.setup", "HypervisorPlatform", outcomeOf(err), detail)
		d.IPC.Publish("sandbox.availability", m.Availability(ctx))
		return res, err
	})
	handle(d, "sandbox.runtime.install", func(_ context.Context, _ none) (sandbox.RuntimeState, error) {
		before := rt.State()
		st := rt.Start(life) // the daemon's lifetime: the download outlives this request
		if !before.Installing && !before.Ready {
			d.audit("sandbox.runtime.install", sandbox.DefaultPins.ImageVersion, "allowed", "download started")
		}
		return st, nil
	})
}

// freeMemory is the Manager's free-RAM check (refuse a boot below VM RAM + 1 GB).
func (d *Daemon) freeMemory() (uint64, error) {
	s, err := d.Metrics.Snapshot(context.Background())
	return s.MemoryAvailableBytes, err
}

// deleteBotComputer stops an archived bot's VM and removes its disk and row. Archiving is how bots are deleted.
func (d *Daemon) deleteBotComputer(botID string) {
	if d.Sandbox == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.Sandbox.Delete(ctx, botID); err != nil {
		d.Log.Warn("delete bot computer", "bot", botID, "err", err)
		return
	}
	d.audit("sandbox.delete", botID, "allowed", "Bot archived")
}

// shutdownSandbox stops every VM cleanly on daemon exit (the job object kills any that do not).
func (d *Daemon) shutdownSandbox() {
	if d.Sandbox != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := d.Sandbox.Shutdown(ctx); err != nil {
			d.Log.Warn("sandbox shutdown", "err", err)
		}
		cancel()
	}
	if d.sandboxStop != nil {
		d.sandboxStop()
	}
}
```

- [ ] **Step 4: Wire fields, registration, shutdown and the archive hook**

`internal/daemon/daemon.go`: add `"github.com/rayorole/orole-os-desktop/internal/sandbox"` to the imports and, after the `Jarvis` field:

```go
	// Sandbox runs one local VM per bot (svc_sandbox.go); sandboxStop ends its background downloads.
	Sandbox     *sandbox.Manager
	sandboxStop context.CancelFunc
```

In `cleanup`, right after the `if d.Chat != nil { d.Chat.Close() }` block:

```go
	d.shutdownSandbox() // after Chat.Close: no bot turn uses a computer any more; before DB.Close: the Store writes on stop
```

`internal/daemon/services.go`: after `d.registerCLIRuntimes()`:

```go
	d.registerSandbox()     // before registerChat: archiving a bot deletes its computer; phase 3 tools read d.Sandbox
```

`internal/daemon/svc_chat.go`, in the `chat.bots.archive` handler:

```go
	handle(d, "chat.bots.archive", func(ctx context.Context, p chat.BotRef) (none, error) {
		err := m.ArchiveBot(ctx, p.ID)
		if err == nil {
			d.audit("chat.bot.delete", p.ID, "allowed", "Bot archived")
			d.deleteBotComputer(p.ID)
		}
		return none{}, chatError(err)
	})
```

- [ ] **Step 5: Run the daemon tests and the whole suite**

Run: `go test -race ./internal/daemon/... ./internal/sandbox/...`
Expected: PASS. If `TestArchivingABotDeletesItsComputer` fails because phase 1's `Manager.Delete` needs a running VM or does not remove the overlay (contract issue 9b), fix `Manager.Delete` in phase 1's `manager.go` (and add a phase 1 unit test for it) in a separate commit `fix(sandbox): delete an idle bot's overlay and row`.

- [ ] **Step 6: Real runtime install over IPC on the dev PC (env-gated test, committed)**

Add to `internal/daemon/svc_sandbox_test.go`:

```go
// TestManualSandboxRuntimeInstall downloads the pinned runtime (~1 GB) through the real daemon. Run by hand:
//
//	$env:OROLE_SANDBOX_MANUAL = "1"; go test -run TestManualSandboxRuntimeInstall -v -timeout 40m ./internal/daemon
func TestManualSandboxRuntimeInstall(t *testing.T) {
	if os.Getenv("OROLE_SANDBOX_MANUAL") != "1" {
		t.Skip("set OROLE_SANDBOX_MANUAL=1 to download the real runtime")
	}
	dir := t.TempDir()
	t.Setenv("OROLE_HOME", dir)
	defer daemon.UseMemoryLLMCredentials()()
	stop := start(t, dir)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	c := control.NewClient(dir, "sandbox-install-test")
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var st sandbox.RuntimeState
	if err := c.Call(ctx, "sandbox.runtime.install", struct{}{}, &st); err != nil || !st.Installing {
		t.Fatalf("install: %+v %v", st, err)
	}
	for st.Installing {
		time.Sleep(2 * time.Second)
		if err := c.Call(ctx, "sandbox.runtime.install", struct{}{}, &st); err != nil { // returns progress while running
			t.Fatal(err)
		}
		t.Logf("%s %d/%d", st.Step, st.Done, st.Total)
	}
	if !st.Ready || st.Error != "" {
		t.Fatalf("final %+v", st)
	}
	var a sandbox.Availability
	if err := c.Call(ctx, "sandbox.availability", struct{}{}, &a); err != nil {
		t.Fatal(err)
	}
	t.Logf("availability after install: %+v", a) // ready:true on a PC with WHP enabled
}
```

Run it with `$env:OROLE_SANDBOX_MANUAL = "1"; go test -run TestManualSandboxRuntimeInstall -v -timeout 40m ./internal/daemon`. Expected: PASS with progress lines and `availability after install: {Ready:true …}` (WHP is enabled on the dev PC since Task 1). After `stop()`, `Get-Process qemu-system-x86_64 -ErrorAction SilentlyContinue` returns nothing.

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/svc_sandbox.go internal/daemon/svc_sandbox_test.go internal/daemon/daemon.go internal/daemon/services.go internal/daemon/svc_chat.go
git commit -m "feat(daemon): wire the bot computer manager, setup and runtime install

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Architecture, security and attribution docs

**Files:**
- Modify: `docs/architecture/bot-computer.md`
- Modify: `docs/security/threat-model.md`
- Modify: `docs/architecture/security-permissions.md`
- Modify: `docs/research/upstream-attribution.md`

**Interfaces:**
- Consumes: everything above.
- Produces: docs only. No CHANGELOG entry: nothing in this phase is visible in the app. Phase 4 adds the user-facing entry, per the master plan.

- [ ] **Step 1: Write the architecture section**

Append to `docs/architecture/bot-computer.md` (above the spike and measurement sections):

```markdown
## Windows backend (phase 2)

- **Process.** `internal/sandbox/backend_windows.go` starts `qemu-system-x86_64.exe -accel whpx -machine q35`
  as a child of the daemon in a job object with `KILL_ON_JOB_CLOSE`. VMs stop with the daemon, and a daemon
  crash kills them. Running commands do not survive; disks do. The full command line is built in `qemu.go`
  (`qemuArgs`) and is never logged without `redactArgs`.
- **Disk.** `<data>\sandbox\images\<version>\base.qcow2` (compressed qcow2, ext4, read-only backing file shared
  by all bots) + `<data>\sandbox\bots\<bot>.qcow2` (per-bot overlay, created with `qemu-img create -F qcow2 -b`
  on first boot, 10 GB virtual). Boot is direct: `vmlinuz`, `initrd` and the `cmdline` file from the same folder.
- **Guest channel.** virtio-serial port `org.orole.guest.0`, whose QEMU chardev *connects* to an AF_UNIX
  socket the daemon listens on: `<data>\sandbox\run\<sha256(bot)[:16]>.sock`. The socket file inherits the
  data folder's user-only ACL. QEMU opens no listener. A second socket (`.qmp`) carries QMP, used only for
  `system_powerdown`. The per-boot token reaches the guest through `-fw_cfg name=opt/orole/token` (root-only
  in sysfs), never the kernel command line, because the serial console is logged to
  `<data>\logs\sandbox-<bot>.log`.
- **Stop.** `VM.Shutdown`: QMP ACPI power-down → systemd poweroff → QEMU exits (`-no-reboot`); after the
  deadline, kill. An exit the daemon did not ask for is `ErrCrashed`.
- **Network.** `-netdev user` (slirp NAT). The guest has open internet and can reach the home LAN. QEMU maps
  10.0.2.2 to the host's loopback; the guest's nftables rejects that address for the `bot` user (uid 1000).
  The bot has no sudo. Root in the guest (only `orole-guest`) is not restricted.
- **Availability** (`availability.go`, no admin): `WHvCreatePartition` succeeds → ready (or `runtime-missing`).
  Otherwise the reason comes from: Windows build ≥ 19041, servicing/our reboot marker → `reboot-required`,
  `Win32_OptionalFeature HypervisorPlatform` (WMI) not enabled → `whp-disabled`, else `virtualization-off`
  (firmware flag / hypervisor not running / partition refused, each with its own advice).
  `WinHvPlatform.dll` exists even with the feature off, so its presence is never used.
- **Setup.** `sandbox.setup.enable` runs `orole-sandbox-setup.exe enable-whp` (next to `orole-daemon.exe`) via
  `ShellExecuteExW` verb `runas`: one UAC prompt. The helper refuses to run unelevated (exit 740) and runs
  `System32\Dism.exe /Online /Enable-Feature /FeatureName:HypervisorPlatform /All /NoRestart`; 3010 means restart.
  The daemon is never elevated.
- **Runtime.** `runtime.go` downloads `qemu-v<build>` (repackaged official QEMU for Windows zip) and
  `guest-v<version>` (vmlinuz, initrd, cmdline, base.qcow2) from this repository's GitHub releases, checks each
  file's size and SHA-256 against `pins.go` while streaming, unpacks into a `.staging-*` folder and renames it
  into place with a `.complete` marker. A mismatch or a partial download leaves nothing. Release assets are
  produced by `.github/workflows/qemu-asset.yml` and `guest-image.yml` as drafts a human publishes;
  `scripts/sandbox-pins.ps1` regenerates `pins.go`.
- **Guest image** (`guest/`, NixOS). Built in CI, or locally via WSL (`docs/development/guest-image.md`):
  systemd initrd, no bootloader, no switch-to-configuration, userborn. User `bot` (`/home/bot`). cage (wlroots
  headless backend, pixman) runs Chromium with CDP on 127.0.0.1:9222 and its profile in
  `/home/bot/.config/orole-chromium`. wayvnc listens on 127.0.0.1:5900. `orole-guest` runs as a root service.
  Python 3 (+uv), Node, git, curl, jq, ripgrep; nix-ld and envfs for FHS-style binaries and shebangs.
- **IPC.** `sandbox.availability`, `sandbox.status{botId}`, `sandbox.setup.enable`, `sandbox.runtime.install`;
  events `sandbox.status`, `sandbox.availability`, `sandbox.runtime`. Archiving a bot (`chat.bots.archive`)
  stops its VM and deletes its overlay and row.
```

- [ ] **Step 2: Threat model and permissions**

In `docs/security/threat-model.md`, add a subsection under the local-agent/untrusted-code section:

```markdown
### Bot computers (local VMs)

- Boundary: a hardware VM (QEMU on Windows Hypervisor Platform) per bot. The guest sees only its own disk; no
  host folders are shared.
- Host↔guest: one AF_UNIX socket per VM under the user-only data folder; QEMU connects to the daemon, nothing
  listens on a network interface. Each boot gets a fresh 32-byte token (fw_cfg, kept in daemon memory only).
- Network: user-mode NAT with open internet. **The guest can reach the home network.** Host loopback via
  10.0.2.2 is rejected for the bot user inside the guest; guest root could bypass this.
- Elevation: only `orole-sandbox-setup.exe`, once, through UAC, to enable a Windows feature. It accepts one
  fixed argument and runs DISM by full path.
- Supply chain: QEMU (unmodified upstream Windows build) and the NixOS image are downloaded from this
  repository's releases and checked against SHA-256 values compiled into Orole.
- Not covered yet: egress allowlist, credential sentinels, wiping saved browser logins (later phases).
```

In `docs/architecture/security-permissions.md`, under the listeners/IPC rules, add: "Bot computers add no listener: QEMU connects to AF_UNIX sockets the daemon creates in `<data>\sandbox\run\` (inheriting the data folder's user-only ACL). `orole-sandbox-setup.exe` is the only component Orole runs elevated (once, via UAC); the daemon never is."

- [ ] **Step 3: Attribution**

Append rows to the register table in `docs/research/upstream-attribution.md`:

```markdown
| QEMU for Windows, `qemu-w64-setup-20260811.exe` from https://qemu.weilnetz.de/w64/ (SHA-512 5bcf9eed…f543), GPL-2.0 (QEMU) and the licences of its bundled DLLs | release asset `qemu-v20260811/qemu-20260811-win64.zip` (not in the repository) | none: a subset of the installer's files (qemu-system-x86_64.exe, qemu-img.exe, DLLs, share/*.bin, share/*.rom, licence files) repackaged by `.github/workflows/qemu-asset.yml`; `SOURCE.txt` in the zip points to the source |
| NixOS / nixpkgs `7fd4004d0511991fff3a0524856a9be09e28c5f0` (MIT for nixpkgs; packages under their own licences: Linux GPL-2.0, Chromium BSD-3-Clause and others, cage MIT, wayvnc ISC) | release asset `guest-v0.1.0/*` built from `guest/` | configuration only (`guest/configuration.nix`); no package sources modified |
```

Below the table, add a note: "GPL source obligations for the QEMU and guest release assets (offering the corresponding source alongside the binaries) are an open item tracked in the phase 2 PR."

- [ ] **Step 4: Commit**

```bash
git add docs/architecture/bot-computer.md docs/security/threat-model.md docs/architecture/security-permissions.md docs/research/upstream-attribution.md
git commit -m "docs(sandbox): windows backend, guest channel, setup and threat model

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 14: Verify everything, push, open the draft PR

**Files:** none new.

**Interfaces:**
- Consumes: the whole branch.
- Produces: draft PR `feat/197-sandbox-windows` → `feat/196-sandbox-core`.

- [ ] **Step 1: Run the full checks**

```powershell
go fmt ./...
go vet ./...
$env:GOOS = "linux"; go vet ./internal/sandbox/... ./cmd/orole-guest/... ./cmd/orole-sandbox-setup/...; Remove-Item Env:GOOS
go vet -tags spike ./internal/sandbox
go test -race ./...
bun run --cwd frontend check
git status --short   # no stray files (guest/result*, temp tests from Task 7 Step 9 deleted)
```

Expected: all clean and PASS. `go fmt` leaves no diff. If it reformats anything, amend the owning task's commit with a `style:` fixup commit instead of rewriting history.

- [ ] **Step 2: Push**

```bash
git push -u origin feat/197-sandbox-windows
```

- [ ] **Step 3: Open the draft PR**

```powershell
$body = @'
Phase 2 of the bot computer (spec: `docs/superpowers/specs/2026-10-15-bot-computer-design.md`, plan: `docs/superpowers/plans/2026-10-15-bot-computer-phase2-windows.md`).

## What this adds
- `internal/sandbox` Windows backend: `qemu-system-x86_64 -accel whpx` per bot in the daemon's kill-on-close job object; compressed qcow2 base image + per-bot overlay; virtio-serial guest channel over an AF_UNIX socket the daemon listens on (QEMU connects; no network listener); QMP power-down; token via fw_cfg, never on the kernel command line or in logs.
- Availability without admin (`WHvCreatePartition`, WMI feature state, firmware flag, reboot marker) and the elevated `orole-sandbox-setup.exe` (one UAC prompt, DISM `HypervisorPlatform`).
- Runtime download of pinned, SHA-256-checked release assets (`qemu-v20260811`, `guest-v0.1.0`), with `scripts/sandbox-pins.ps1`.
- `guest/` NixOS flake (cage + Chromium with CDP on guest loopback, wayvnc on guest loopback, orole-guest, Python/Node/git/jq/ripgrep, nix-ld, envfs) and the `qemu-asset` / `guest-image` workflows (draft releases).
- Daemon IPC: `sandbox.availability`, `sandbox.status`, `sandbox.setup.enable`, `sandbox.runtime.install`; archiving a bot deletes its computer; VMs stop on daemon exit.

## Contract changes vs the master plan
See "Contract issues" at the top of the phase 2 plan: hashed socket names, fw_cfg token, QMP shutdown, `cmdline` image file, archive = delete, new events `sandbox.availability` / `sandbox.runtime`.

## Verified
- Spike and real-boot results on the dev PC (21H2, build 22000): `docs/architecture/bot-computer.md`.
- `go test -race ./...`, `go vet`, `bun run --cwd frontend check`.
- UAC accept/decline: (results from Task 7 Step 9).

## Open items
- GPL source offer for the QEMU and guest release assets.
- 24H2 run of the e2e test.

Closes #197

🤖 Generated with [Claude Code](https://claude.com/claude-code)
'@
gh pr create --draft --base feat/196-sandbox-core --head feat/197-sandbox-windows `
  --title "feat(sandbox): windows qemu backend, runtime download, setup helper and guest image" --body $body
```

Fill in the "UAC accept/decline" line with the recorded results before running the command. Do not merge.

- [ ] **Step 4: Report**

Post the PR URL and the spike/e2e numbers (boot to ready, idle RAM) to the owner.

---

## Self-review notes

- **Spec coverage (phase 2 slice).**

  | Spec item | Task |
  |---|---|
  | Hypervisor: QEMU WHPX in the daemon job object | 6 |
  | Guest OS: NixOS, pinned flake, Linux CI, base + overlay | 8, 10 |
  | NixOS spike criteria with the Debian fallback gate | 8 Step 6 |
  | Network: user-mode NAT, risk documented | 3, 8, 13 |
  | Shipping: download on first use, hash-checked | 5, 11 |
  | Admin: separate helper via UAC, reboot | 7 |
  | Lifetime: stop with the daemon | 6 job, 12 cleanup |
  | Storage layout | 2, 5, 6 |
  | Bot deletion removes overlay | 12 |
  | Errors: WHP off / virtualization off | 4 |
  | Errors: download mismatch | 5 |
  | Errors: boot failure | 6 |
  | Errors: crash | 6 |
  | Docs | 13 |

  Manager cap, idle, LRU, memory and takeover are phase 1. Tools are phase 3. UI is phase 4.
- **Types used across tasks.** `hostProbe`/`decideAvailability` (Task 4) are used by Task 6. `Runtime.QEMU/Image/Ready`, `imageCmdline`, `completeMark`, `pathExists` (Task 5) are used by Tasks 6, 11 and 12. `qemuArgs`/`qmpSocket`/`newTailBuffer`/`defaultCPU`/`guestPortName` (Task 3) are used by Tasks 1 and 6. `rebootMarkerPath`/`rebootMarker`/`bootTime` (Task 4) are used by Task 7. `setupcode` is used by the helper and by `setup.go`. `NewHostBackend(rt, p)` has the same signature on both platforms.
- **Placeholders.** The spike table's `<…>` cells and `pins.go`'s values are measured or generated by named steps (Task 1 Step 7, Task 11 Step 3), and the plan forbids committing them unfilled. `vendorHash` is set by Task 8 Step 5 from Nix's own output.
