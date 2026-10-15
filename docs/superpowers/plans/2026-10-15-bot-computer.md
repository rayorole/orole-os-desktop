# Bot computer Implementation Plan (master)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every chat bot can have its own local Linux VM (disk, terminal, visible browser) that the user can watch live and take over.

**Architecture:** A daemon-owned `internal/sandbox` manager runs one QEMU (WHPX) VM per bot from a shared read-only NixOS image plus a per-bot qcow2 overlay. The daemon talks to `orole-guest` inside the VM over one local byte stream carrying HTTP/2 (h2c) for multiplexed exec/files/pty/screen/browser streams. Bots get policy-checked `chat.Tool`s; the UI gets a Computer tab with noVNC (fed through MyGo events, no sockets) and an xterm.js terminal.

**Tech Stack:** Go 1.27 (stdlib, `golang.org/x/net/http2`, `golang.org/x/sys/windows`, `github.com/Microsoft/go-winio`, `github.com/chromedp/chromedp` in the guest only), QEMU (official Windows build, `-accel whpx`), NixOS flake (Linux CI), React + TypeScript, xterm.js, `@novnc/novnc`.

**Spec:** `docs/superpowers/specs/2026-10-15-bot-computer-design.md`

**Phase plans (execute in order; each is one issue, one stacked branch, one draft PR):**

| Phase | Issue | Branch (stacked) | Plan file |
|---|---|---|---|
| 1 Sandbox core and guest agent | #196 | `feat/196-sandbox-core` (from `jarvis-mode`) | `2026-10-15-bot-computer-phase1-core.md` |
| 2 Windows backend, runtime, setup, image | #197 | `feat/197-sandbox-windows` (from phase 1) | `2026-10-15-bot-computer-phase2-windows.md` |
| 3 Bot tools, browser, takeover | #198 | `feat/198-bot-computer-tools` (from phase 2) | `2026-10-15-bot-computer-phase3-tools.md` |
| 4 Computer tab, live view, docs | #199 | `feat/199-computer-tab` (from phase 3) | `2026-10-15-bot-computer-phase4-ui.md` |

PR bases: #196 → `jarvis-mode`, each later PR → the previous phase branch. Body ends with `Closes #N`. Never merge.

## Global Constraints

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

## Review Focus

- Daemon restart while a VM runs: the old QEMU must be gone (job object kill-on-close) and the next call must boot a fresh VM on the same overlay without "overlay locked" errors.
- Two tool calls for the same bot at once while it is booting: exactly one QEMU process starts; both calls wait for it.
- Guest writes a huge or never-ending output (`yes`, binary garbage): the host caps it (exec 1 MiB, file read 4 MiB), returns truncated text and never blocks the daemon.
- User clicks "Take over" while a bot browser step is running: the step finishes, the next browser tool call is refused with a clear "the user is controlling the computer" message, and released on "Done".
- A bot is deleted while its VM runs: VM stopped, overlay file deleted, row removed, no orphan QEMU.

## Shared contracts (all phases must use these names exactly)

### Package layout

```
internal/sandbox/              (host, all platforms)
  sandbox.go        types: Limits, State, Status, Availability, errors
  manager.go        Manager (lifecycle, cap, idle, takeover lock)
  store.go          Store over table sandboxes
  backend.go        Backend, VM interfaces
  fake_test.go      fakeBackend used by tests (phase 1)
  guestclient.go    Guest: h2c client over a net.Conn
  paths.go          Paths under <data>\sandbox
  backend_windows.go / backend_other.go   (phase 2)
  runtime.go        downloader + pins (phase 2)
  setup_windows.go / setup_other.go       (phase 2)
internal/sandbox/wire/         request/response JSON types shared with the guest
cmd/orole-guest/               linux guest agent (h2c server)
cmd/orole-sandbox-setup/       elevated WHP enabler (phase 2, windows)
guest/                         NixOS flake for the image (phase 2)
internal/daemon/svc_sandbox.go IPC wiring (phase 2+)
internal/chat/tools/computer.go, browser.go, takeover.go (phase 3)
internal/app/sandbox.go        MyGo service + events (phase 4)
frontend/src/features/computer/ (phase 4)
migrations/0112_sandboxes.sql
```

### Core types (`internal/sandbox/sandbox.go`)

```go
type Limits struct {
	MemoryMB int `json:"memoryMB"` // default 2048
	CPUs     int `json:"cpus"`     // default 2
	DiskGB   int `json:"diskGB"`   // default 10
}

func DefaultLimits() Limits { return Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10} }

type State string

const (
	StateOff      State = "off"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type Status struct {
	BotID      string `json:"botId"`
	State      State  `json:"state"`
	TakenOver  bool   `json:"takenOver"`
	Error      string `json:"error,omitempty"`
}

type Availability struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"` // "whp-disabled", "virtualization-off", "reboot-required", "runtime-missing", "unsupported-os"
	Detail string `json:"detail,omitempty"`
}

var (
	ErrNotReady    = errors.New("sandbox: the computer isn't set up yet")
	ErrBootFailed  = errors.New("sandbox: the computer failed to start")
	ErrCrashed     = errors.New("sandbox: the computer stopped unexpectedly")
	ErrBusy        = errors.New("sandbox: all computers are busy, try again shortly")
	ErrLowMemory   = errors.New("sandbox: not enough free memory to start the computer")
	ErrTakenOver   = errors.New("sandbox: the user is controlling the computer")
)
```

### Backend (`internal/sandbox/backend.go`)

```go
type BootSpec struct {
	BotID     string
	Limits    Limits
	BaseImage string // absolute path, read-only
	Kernel    string // absolute path
	Initrd    string // absolute path
	Overlay   string // absolute path to <data>\sandbox\bots\<botID>.qcow2
	Token     string // per-boot random, 32 bytes hex
	LogPath   string // <data>\logs\sandbox-<botID>.log
}

type Backend interface {
	Availability(ctx context.Context) Availability
	// Boot starts the VM and returns once the guest channel is connected (not necessarily ready).
	Boot(ctx context.Context, spec BootSpec) (VM, error)
}

type VM interface {
	Conn() net.Conn              // the raw guest byte stream
	Shutdown(ctx context.Context) error // ask guest to power off, wait, then kill
	Kill() error
	Done() <-chan struct{}       // closed when the VM process exits
	ExitErr() error
}
```

### Guest protocol (`internal/sandbox/wire`, h2c over `VM.Conn()`)

All requests carry header `X-Orole-Token: <token>`; the guest rejects others with 401. Base URL `http://guest`.

| Method + path | Body | Response |
|---|---|---|
| `GET /health` | – | `wire.Health{Version string, Ready bool}` |
| `POST /exec` | `wire.ExecRequest{Command string, Timeout int /*seconds, default 120, max 600*/, Cwd string}` | `wire.ExecResult{Stdout, Stderr string, ExitCode int, TimedOut, Truncated bool}` (stdout+stderr capped at 1 MiB total) |
| `POST /files/read` | `wire.ReadFileRequest{Path string}` | `wire.ReadFileResult{Content []byte, Truncated bool}` (cap 4 MiB) |
| `POST /files/write` | `wire.WriteFileRequest{Path string, Content []byte, Mode uint32}` | `wire.WriteFileResult{Bytes int}` (max 16 MiB) |
| `POST /pty?cols=&rows=` | full-duplex body: client→guest keystrokes | full-duplex response: guest→client terminal output |
| `POST /pty/resize` | `wire.ResizeRequest{ID string, Cols, Rows int}` | 204 |
| `POST /screen` | full-duplex raw RFB bytes to the guest VNC server (127.0.0.1:5900) | raw RFB bytes |
| `POST /browser` | `wire.BrowserRequest{Action string /*open,click,type,read,screenshot*/, URL, Target, Text string}` | `wire.BrowserResult{Text string, URL string, Title string, PNG []byte}` |
| `POST /poweroff` | – | 202, then guest powers off |

PTY response header `X-Orole-Pty: <id>` identifies the session for resize. Paths in files requests are guest paths; relative paths resolve against `/home/bot`.

### Guest client (`internal/sandbox/guestclient.go`)

```go
type Guest struct { /* h2c client bound to one VM */ }

func NewGuest(conn net.Conn, token string) (*Guest, error)
func (g *Guest) Health(ctx context.Context) (wire.Health, error)
func (g *Guest) Exec(ctx context.Context, req wire.ExecRequest) (wire.ExecResult, error)
func (g *Guest) ReadFile(ctx context.Context, path string) (wire.ReadFileResult, error)
func (g *Guest) WriteFile(ctx context.Context, path string, content []byte) (wire.WriteFileResult, error)
func (g *Guest) OpenPTY(ctx context.Context, cols, rows int) (terminals.Process, error)
func (g *Guest) OpenScreen(ctx context.Context) (io.ReadWriteCloser, error)
func (g *Guest) Browser(ctx context.Context, req wire.BrowserRequest) (wire.BrowserResult, error)
func (g *Guest) PowerOff(ctx context.Context) error
func (g *Guest) Close() error
```

### Manager (`internal/sandbox/manager.go`)

```go
type Config struct {
	Paths       Paths
	MaxRunning  int           // 4
	IdleTimeout time.Duration // 15m
	BootTimeout time.Duration // 60s
	BusyWait    time.Duration // 30s
	FreeMemory  func() (uint64, error) // bytes; injectable for tests
	Now         func() time.Time
	Image       func() (ImageSet, error) // resolves kernel/initrd/base from the installed runtime (phase 2); tests stub it
}

type ImageSet struct{ Version, Kernel, Initrd, BaseImage string }

func NewManager(cfg Config, backend Backend, store *Store) *Manager
func (m *Manager) Availability(ctx context.Context) Availability
func (m *Manager) Ensure(ctx context.Context, botID string) (*Guest, error) // boots on demand, marks activity
func (m *Manager) Touch(botID string)                                        // activity without a call
func (m *Manager) Status(botID string) Status
func (m *Manager) SetViewer(botID string, watching bool)
func (m *Manager) TakeOver(botID string) error
func (m *Manager) Release(botID string)
func (m *Manager) TakenOver(botID string) bool
func (m *Manager) Delete(ctx context.Context, botID string) error
func (m *Manager) Shutdown(ctx context.Context) error // all VMs, on daemon exit
func (m *Manager) Subscribe(fn func(Status)) (unsubscribe func())
```

### Store and migration

`migrations/0112_sandboxes.sql`:

```sql
CREATE TABLE sandboxes (
  bot_id        TEXT PRIMARY KEY,
  image_version TEXT NOT NULL,
  overlay_path  TEXT NOT NULL,
  limits        TEXT NOT NULL DEFAULT '{}',
  last_used_at  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);
```

```go
type Record struct { BotID, ImageVersion, OverlayPath string; Limits Limits; LastUsedAt, CreatedAt time.Time }
func NewStore(db *sql.DB) *Store
func (s *Store) Get(ctx context.Context, botID string) (Record, bool, error)
func (s *Store) Upsert(ctx context.Context, r Record) error
func (s *Store) Touch(ctx context.Context, botID string, at time.Time) error
func (s *Store) Delete(ctx context.Context, botID string) error
```

### Paths (`internal/sandbox/paths.go`)

```go
type Paths struct{ Root string } // <data>\sandbox
func (p Paths) Runtime(version string) string // Root\runtime\<version>
func (p Paths) Image(version string) string   // Root\images\<version>
func (p Paths) Overlay(botID string) string   // Root\bots\<botID>.qcow2
func (p Paths) Socket(botID string) string    // Root\run\<botID>.sock
func (p Paths) Log(dataDir, botID string) string // <data>\logs\sandbox-<botID>.log
```

### Policy actions (phase 3)

| Tool | Action | Resource | Effect |
|---|---|---|---|
| `computer_run` | `computer.exec` | `computer/<botID>` | write |
| `computer_read_file` | `computer.files.read` | `computer/<botID>/<path>` | read |
| `computer_write_file` | `computer.files.write` | `computer/<botID>/<path>` | write |
| `browser_open`, `browser_read`, `browser_screenshot` | `computer.browser.read` | `computer/<botID>/browser` | read |
| `browser_click`, `browser_type` | `computer.browser.write` | `computer/<botID>/browser` | write |
| `ask_user_to_take_over` | none (it is itself a user prompt) | – | – |

Bot config field (phase 3): `Computer bool \`json:"computer,omitempty"\`` on the bot config struct in `internal/chat/api.go`, default false.

### Daemon IPC (phase 2 adds status/setup, phase 4 adds streams)

`sandbox.availability`, `sandbox.status{botId}`, `sandbox.setup.enable`, `sandbox.runtime.install`, `sandbox.viewer{botId,watching}`, `sandbox.takeover{botId}`, `sandbox.release{botId}`, `sandbox.screen.open{botId}` / `sandbox.screen.input{id,data}` / `sandbox.screen.close{id}`, `sandbox.pty.open{botId,cols,rows}` (returns a terminal id usable by the existing terminal input/resize/close calls). Events: `sandbox.status`, `sandbox.screen.data{id,data(base64)}`.
