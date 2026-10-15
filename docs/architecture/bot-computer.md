# Bot computer: a local VM per bot

Every chat bot can have its own small Linux computer on the user's PC: its own disk, a terminal and (later) a
visible browser. Design: `docs/superpowers/specs/2026-10-15-bot-computer-design.md`. Plans:
`docs/superpowers/plans/2026-10-15-bot-computer*.md`.

**Status: phase 1 (sandbox core and guest agent).** The manager, store, guest protocol and `orole-guest` exist
and are tested with a fake backend. Nothing starts a real VM yet: the Windows QEMU backend, runtime download and
setup come in phase 2; bot tools in phase 3; the Computer tab in phase 4.

**A bot's computer lives in the daemon: it survives GUI restarts, not a daemon exit or reboot. Its disk persists;
running commands do not.**

## Architecture

```
 bot tool call (phase 3)
        |
 internal/sandbox.Manager ---- Store (table sandboxes)
        |  Ensure(bot) -> *Guest
        v
 Backend.Boot (phase 2: QEMU + WHPX) ---> VM.Conn(): one byte stream (virtio-serial <-> AF_UNIX socket / pipe)
        |                                          |
 Guest (h2c client, golang.org/x/net/http2)  <---->  orole-guest (h2c server) in the Linux guest
                                                     internal/sandbox/guestagent
```

| Unit | Location | Responsibility |
|---|---|---|
| Types, paths | `internal/sandbox/sandbox.go`, `paths.go` | `Limits` (2048 MB, 2 vCPU, 10 GB by default), `State`, `Status`, `Availability`, errors; files under `<data>\sandbox\` |
| Backend | `internal/sandbox/backend.go` | `Backend.Boot(BootSpec) -> VM`; `VM.Conn/Shutdown/Kill/Done/ExitErr`. Phase 1 has only the test fake. |
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
carries `X-Orole-Token`, a 32-byte random hex token generated per boot, passed to the guest on the kernel
command line (`orole.token=`) and held only in daemon memory; the guest answers 401 otherwise.

| Route | Purpose | Limits |
|---|---|---|
| `GET /health` | version and readiness | |
| `POST /exec` | `/bin/sh -c <command>` in its own process group | timeout 120 s default, 600 s max; stdout+stderr 1 MiB kept, the rest dropped (`truncated`); background children get 2 s to release the output |
| `POST /files/read`, `/files/write` | guest files; relative paths under `/home/bot` | read 4 MiB (`truncated`), write 16 MiB max |
| `POST /pty?cols=&rows=` | login shell on a pseudo-terminal, full duplex; `X-Orole-Pty` id, `X-Orole-Exit` trailer | |
| `POST /pty/resize` | resize by id | |
| `POST /screen`, `POST /browser` | 501 until phases 3-4 | |
| `POST /poweroff` | 202, then a clean power off | |

The host never trusts the guest: answers over 8 MiB are refused, exec output is cut to 1 MiB and file content to
4 MiB again on the host, and everything a bot receives from the guest is untrusted data (phase 3 wraps it).

### Storage

`<data>\sandbox\runtime\<version>\` (QEMU), `images\<version>\` (base image), `bots\<bot>.qcow2` (overlay),
`run\<bot>.sock` (guest channel), `<data>\logs\sandbox-<bot>.log`. Bot ids are checked against
`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` before they become file names.

### Security notes

- No network listener on the host; the guest channel is a local stream (phase 2).
- The VM is the security boundary. The token authenticates the daemon's connection to the guest; code inside the
  guest can read the kernel command line, which is acceptable because it already controls the guest.
- The guest has open internet and can reach the home network (phase 2 documents this in the UI).

### Testing

`go test -race ./internal/sandbox/... ./cmd/orole-guest/...` runs everywhere: the fake backend serves the real
guest handler over `net.Pipe`, and the test binary doubles as the guest's shell (`guestagent/agenttest`). The
Linux CI lane also runs the real `/bin/sh` exec tests and the real PTY test, and builds `orole-guest`.
