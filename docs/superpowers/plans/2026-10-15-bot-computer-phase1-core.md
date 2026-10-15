# Bot computer, phase 1: sandbox core and guest agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A tested, platform-neutral `internal/sandbox` (types, store, manager, guest client) and the `orole-guest` agent, so that later phases only add the Windows QEMU backend, bot tools and UI.

**Architecture:** `internal/sandbox.Manager` maps bot id to one VM, booted on demand through a `Backend` (only a fake exists in this phase), with single-flight boot, a cap of 4 with LRU eviction of idle VMs, idle stop, crash detection, a takeover lock and delete. The host talks to the guest with HTTP/2 without TLS (h2c, `golang.org/x/net/http2`) over the VM's one byte stream; `internal/sandbox/guestagent` is the OS-neutral guest handler that `cmd/orole-guest` serves on the virtio-serial port and that host tests run in-process over `net.Pipe`.

**Tech Stack:** Go 1.27, stdlib, `golang.org/x/net/http2` (new direct dependency), `github.com/creack/pty` (already in `go.mod`, Linux guest only), `golang.org/x/sys/unix` (already in `go.mod`), SQLite via `internal/database`.

**Spec:** `docs/superpowers/specs/2026-10-15-bot-computer-design.md`; master plan with the shared contracts: `docs/superpowers/plans/2026-10-15-bot-computer.md`.

## Contract issues

None blocking. Every name and type in the master plan's "Shared contracts" is used as written. Notes for later phases, all additive:

1. `Manager.Ensure` has no matching "done" call, so the manager cannot see when a tool call ends. This plan tracks in-flight work inside `Guest` (every request and open stream reports start and end to the manager through an unexported hook). "Busy" for eviction and idle stop means: a request or stream in flight, a viewer, or the takeover lock. A VM whose guest was just returned by `Ensure` but has no request in flight yet can, in principle, be evicted when every other VM is busy; phase 3 tools call `Ensure` immediately before their request, so the window is microseconds.
2. Unexported errors are added next to the exported ones: `errStopped` (idle stop or eviction cut a call), `errDeleting`, `errClosed` (daemon shutting down), `errNotRunning` (`TakeOver` with no running VM). Phase 3 may export any it needs to show to bots.
3. `wire` gains constants (`TokenHeader`, `PTYHeader`, `ExitTrailer`, limits, `Home`) and lowerCamel JSON tags; `ClampTimeout` is the one helper. Both sides are Go, so the tags only have to agree with themselves.
4. New packages not in the master layout: `internal/sandbox/guestagent` (the guest handler, importable by host tests, under `internal/` per AGENTS.md) and `internal/sandbox/guestagent/agenttest` (makes a test binary act as the guest's shell so exec tests run on Windows). Extra files in `internal/sandbox`: `guestclient_stream.go`, `manager_activity.go`, `manager_delete.go`.
5. `Paths.Log(dataDir, botID)` takes the data dir although `Paths` only knows `Root`; the manager passes `filepath.Dir(Paths.Root)`.
6. The table `sandboxes` has no `state` column (the spec mentions one; the contract wins): run state is in memory only. It has no foreign key to `bots`, so deleting a bot must call `Manager.Delete` (phase 3 wires that).
7. For phase 2: the token on the kernel command line is readable by any process in the guest. That is acceptable because the VM is the boundary and the token authenticates the host's connection, but phase 2 must make sure only `orole-guest` can open `/dev/virtio-ports/org.orole.guest.0`, and must decide which user `/exec` and `/pty` run as.

## Global Constraints

See the master plan's Global Constraints; all apply. Phase-specific:

- The only new third-party module is `golang.org/x/net` (for `http2`). PTY code uses `github.com/creack/pty`, which `internal/terminals` already uses on Linux; poweroff uses `golang.org/x/sys/unix`, already a dependency.
- `internal/sandbox/guestagent` must never import `internal/sandbox` (import cycle with the sandbox tests, and the guest must not link host code).
- Guest limits (exact): exec timeout default 120 s, max 600 s; exec stdout+stderr kept 1 MiB total; file read cap 4 MiB; file write max 16 MiB; relative guest paths resolve against `/home/bot`.
- The guest token is never written to a log, an error message or SQLite.
- No real backend in this phase: nothing in the daemon calls `internal/sandbox` yet (phase 2 wires it).
- `cmd/orole-guest` must build for `linux/amd64` with `CGO_ENABLED=0`; every package must also compile on Windows so `go vet ./...` and `go test -race ./...` stay green there. Linux-only code lives in `_linux.go` / `_unix.go` files with `!linux` / `!unix` stubs.
- Commands are written for Git Bash. In PowerShell, write `$env:GOOS='linux'; $env:GOARCH='amd64'; <command>; Remove-Item Env:GOOS,Env:GOARCH` instead of the `GOOS=linux GOARCH=amd64 <command>` prefix.

## Review Focus

Taken from the master plan where they belong to this phase, each pinned by a test in the named task:

- Two or more `Ensure` calls for the same bot while it boots: exactly one `Backend.Boot`, all callers get the same guest (Task 7, `TestEnsureBootsOnceForConcurrentCallers`).
- A command that prints without end (`yes`, `forever`) or a hostile guest that sends a 3 MiB or 9 MiB answer: the guest keeps 1 MiB, the host re-caps to 1 MiB and refuses answers over 8 MiB, nothing blocks (Task 3 `TestExecCapsOutput`/`TestExecTimeout`, Task 4 `TestGuestCapsHostileExecOutput`/`TestGuestRejectsOversizedResponse`, Linux `TestExecRealShell`).
- The VM dies while a call is in flight: the call returns `ErrCrashed` within the crash grace, status is `off` with the crash error, the next call boots again (Task 7, `TestCrashFailsInFlightCallAndNextCallBoots`).
- The user holds the takeover lock: `TakenOver` is true, the VM is never idle-stopped or evicted, `Release` clears it, a crash clears it (Task 8, `TestTakeOverLock`, `TestTakenOverVMIsNeverEvicted`).
- A bot is deleted while its VM runs or boots: VM stopped, in-flight call fails with a clear error, overlay and row removed, no live VM left (Task 9, `TestDeleteWhileRunning`, `TestDeleteWhileBooting`). Restart: a new manager on the same store boots on the same overlay (Task 9, `TestNewManagerReusesOverlay`); the "old QEMU is gone" half is phase 2 (job object).

---

### Task 0: Commit the design documents

**Files:**
- Modify: `docs/superpowers/specs/2026-10-15-bot-computer-design.md` (already edited in the worktree)
- Add: `docs/superpowers/plans/2026-10-15-bot-computer*.md`

**Interfaces:** none.

- [ ] **Step 1: Check the branch and what is uncommitted**

Run: `git -C C:/code/projects/.orole-worktrees/orole-os/196-sandbox-core status --short`
Expected: branch `feat/196-sandbox-core`; ` M docs/superpowers/specs/2026-10-15-bot-computer-design.md` and `?? docs/superpowers/plans/...` lines. If the spec and plans are already committed, skip this task.

- [ ] **Step 2: Commit**

Add the spec, the master plan, this phase plan and any other phase plan files that are finished (do not commit a phase plan another agent is still writing):

```bash
git add docs/superpowers/specs/2026-10-15-bot-computer-design.md docs/superpowers/plans/2026-10-15-bot-computer.md docs/superpowers/plans/2026-10-15-bot-computer-phase1-core.md
git commit -m "docs(plan): bot computer implementation plans" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 1: Sandbox types, paths, backend interfaces and wire types

**Files:**
- Create: `internal/sandbox/sandbox.go`
- Create: `internal/sandbox/paths.go`
- Create: `internal/sandbox/backend.go`
- Create: `internal/sandbox/wire/wire.go`
- Test: `internal/sandbox/sandbox_test.go`
- Test: `internal/sandbox/wire/wire_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (exact, later tasks rely on them):
  - `sandbox.Limits`, `DefaultLimits()`, `(Limits).withDefaults() Limits`, `State` + `StateOff/StateStarting/StateRunning/StateStopping/StateFailed`, `Status`, `Availability`, `ErrNotReady/ErrBootFailed/ErrCrashed/ErrBusy/ErrLowMemory/ErrTakenOver`, unexported `errStopped/errDeleting/errClosed/errNotRunning`, `checkBotID(id string) error`.
  - `sandbox.Paths{Root}` with `Runtime(version)`, `Image(version)`, `Overlay(botID)`, `Socket(botID)`, `Log(dataDir, botID)`.
  - `sandbox.BootSpec`, `Backend`, `VM` exactly as in the master plan.
  - `wire.Health`, `ExecRequest`, `ExecResult`, `ReadFileRequest`, `ReadFileResult`, `WriteFileRequest`, `WriteFileResult`, `ResizeRequest`, `BrowserRequest`, `BrowserResult`; constants `TokenHeader`, `PTYHeader`, `ExitTrailer`, `DefaultExecTimeout`, `MaxExecTimeout`, `MaxExecOutput`, `MaxReadFile`, `MaxWriteFile`, `Home`; `ClampTimeout(seconds int) int`.

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/wire/wire_test.go`:

```go
package wire

import (
	"encoding/json"
	"testing"
)

func TestClampTimeout(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, DefaultExecTimeout},
		{-5, DefaultExecTimeout},
		{1, 1},
		{600, 600},
		{601, MaxExecTimeout},
		{100000, MaxExecTimeout},
	} {
		if got := ClampTimeout(tc.in); got != tc.want {
			t.Errorf("ClampTimeout(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The guest and the host are built separately; the JSON names are the protocol and must not drift.
func TestJSONNames(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want string
	}{
		{ExecRequest{Command: "ls", Timeout: 5, Cwd: "w"}, `{"command":"ls","timeout":5,"cwd":"w"}`},
		{ExecResult{Stdout: "o", Stderr: "e", ExitCode: 2, TimedOut: true, Truncated: true}, `{"stdout":"o","stderr":"e","exitCode":2,"timedOut":true,"truncated":true}`},
		{ReadFileRequest{Path: "a"}, `{"path":"a"}`},
		{ReadFileResult{Content: []byte("hi")}, `{"content":"aGk="}`},
		{WriteFileRequest{Path: "a", Content: []byte("hi"), Mode: 0o600}, `{"path":"a","content":"aGk=","mode":384}`},
		{WriteFileResult{Bytes: 2}, `{"bytes":2}`},
		{ResizeRequest{ID: "p", Cols: 80, Rows: 24}, `{"id":"p","cols":80,"rows":24}`},
		{Health{Version: "1", Ready: true}, `{"version":"1","ready":true}`},
		{BrowserRequest{Action: "open", URL: "u"}, `{"action":"open","url":"u"}`},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("%T = %s, want %s", tc.v, b, tc.want)
		}
	}
}
```

`internal/sandbox/sandbox_test.go`:

```go
package sandbox

import (
	"path/filepath"
	"testing"
)

func TestDefaultLimits(t *testing.T) {
	if got := DefaultLimits(); got != (Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10}) {
		t.Fatalf("DefaultLimits() = %+v", got)
	}
	if got := (Limits{CPUs: 4}).withDefaults(); got != (Limits{MemoryMB: 2048, CPUs: 4, DiskGB: 10}) {
		t.Fatalf("withDefaults kept the wrong fields: %+v", got)
	}
	if got := (Limits{MemoryMB: -1, CPUs: 0, DiskGB: 20}).withDefaults(); got != (Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 20}) {
		t.Fatalf("withDefaults with bad values: %+v", got)
	}
}

func TestCheckBotID(t *testing.T) {
	for _, id := range []string{"b1", "0b6a2c1e-6f7d-4d0a-9a39-3f1c2b8f9e10", "bot_2"} {
		if err := checkBotID(id); err != nil {
			t.Errorf("checkBotID(%q) = %v, want nil", id, err)
		}
	}
	for _, id := range []string{"", "../x", `a\b`, "a/b", "-lead", "a b", string(make([]byte, 65))} {
		if err := checkBotID(id); err == nil {
			t.Errorf("checkBotID(%q) = nil, want an error", id)
		}
	}
}

func TestPaths(t *testing.T) {
	root := filepath.Join("data", "sandbox")
	p := Paths{Root: root}
	for _, tc := range []struct{ got, want string }{
		{p.Runtime("q1"), filepath.Join(root, "runtime", "q1")},
		{p.Image("i1"), filepath.Join(root, "images", "i1")},
		{p.Overlay("b1"), filepath.Join(root, "bots", "b1.qcow2")},
		{p.Socket("b1"), filepath.Join(root, "run", "b1.sock")},
		{p.Log("data", "b1"), filepath.Join("data", "logs", "sandbox-b1.log")},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/...`
Expected: FAIL, build errors such as `undefined: ClampTimeout`, `undefined: DefaultLimits`, `undefined: Paths`.

- [ ] **Step 3: Write the implementation**

`internal/sandbox/wire/wire.go`:

```go
// Package wire holds the request and response types of the protocol between the daemon and orole-guest, the
// agent inside a bot's VM. The transport is HTTP/2 without TLS (h2c) over the VM's one byte stream; every
// request carries TokenHeader. See docs/architecture/bot-computer.md.
package wire

const (
	TokenHeader = "X-Orole-Token" // per-boot secret; the guest answers 401 without it
	PTYHeader   = "X-Orole-Pty"   // response header of POST /pty: the terminal id for /pty/resize
	ExitTrailer = "X-Orole-Exit"  // response trailer of POST /pty: the shell's exit code

	DefaultExecTimeout = 120      // seconds
	MaxExecTimeout     = 600      // seconds
	MaxExecOutput      = 1 << 20  // stdout+stderr bytes kept per command
	MaxReadFile        = 4 << 20  // bytes returned by /files/read
	MaxWriteFile       = 16 << 20 // bytes accepted by /files/write

	Home = "/home/bot" // relative guest paths resolve here
)

type Health struct {
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
}

type ExecRequest struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds; see ClampTimeout
	Cwd     string `json:"cwd,omitempty"`
}

type ExecResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exitCode"`
	TimedOut  bool   `json:"timedOut,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ReadFileRequest struct {
	Path string `json:"path"`
}

type ReadFileResult struct {
	Content   []byte `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type WriteFileRequest struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Mode    uint32 `json:"mode,omitempty"` // permission bits; 0 means 0644
}

type WriteFileResult struct {
	Bytes int `json:"bytes"`
}

type ResizeRequest struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type BrowserRequest struct {
	Action string `json:"action"` // open, click, type, read, screenshot
	URL    string `json:"url,omitempty"`
	Target string `json:"target,omitempty"`
	Text   string `json:"text,omitempty"`
}

type BrowserResult struct {
	Text  string `json:"text,omitempty"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
	PNG   []byte `json:"png,omitempty"`
}

// ClampTimeout returns the exec timeout in seconds that the guest applies to a requested value.
func ClampTimeout(seconds int) int {
	switch {
	case seconds <= 0:
		return DefaultExecTimeout
	case seconds > MaxExecTimeout:
		return MaxExecTimeout
	}
	return seconds
}
```

`internal/sandbox/sandbox.go`:

```go
// Package sandbox runs one local Linux VM, "the bot's computer", per chat bot. The daemon owns every VM: this
// package holds the lifecycle manager, the per-bot store, the guest client and the Backend interface that
// platform code implements. See docs/architecture/bot-computer.md.
package sandbox

import (
	"errors"
	"fmt"
	"regexp"
)

type Limits struct {
	MemoryMB int `json:"memoryMB"` // default 2048
	CPUs     int `json:"cpus"`     // default 2
	DiskGB   int `json:"diskGB"`   // default 10
}

func DefaultLimits() Limits { return Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10} }

// withDefaults replaces zero or negative fields with DefaultLimits.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MemoryMB <= 0 {
		l.MemoryMB = d.MemoryMB
	}
	if l.CPUs <= 0 {
		l.CPUs = d.CPUs
	}
	if l.DiskGB <= 0 {
		l.DiskGB = d.DiskGB
	}
	return l
}

type State string

const (
	StateOff      State = "off"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type Status struct {
	BotID     string `json:"botId"`
	State     State  `json:"state"`
	TakenOver bool   `json:"takenOver"`
	Error     string `json:"error,omitempty"`
}

type Availability struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"` // "whp-disabled", "virtualization-off", "reboot-required", "runtime-missing", "unsupported-os"
	Detail string `json:"detail,omitempty"`
}

var (
	ErrNotReady   = errors.New("sandbox: the computer isn't set up yet")
	ErrBootFailed = errors.New("sandbox: the computer failed to start")
	ErrCrashed    = errors.New("sandbox: the computer stopped unexpectedly")
	ErrBusy       = errors.New("sandbox: all computers are busy, try again shortly")
	ErrLowMemory  = errors.New("sandbox: not enough free memory to start the computer")
	ErrTakenOver  = errors.New("sandbox: the user is controlling the computer")
)

var (
	errStopped    = errors.New("sandbox: the computer was stopped")
	errDeleting   = errors.New("sandbox: the computer is being deleted")
	errClosed     = errors.New("sandbox: the app is shutting down")
	errNotRunning = errors.New("sandbox: the computer is not running")
)

// Bot ids become file names (overlay, socket, log), so only a safe alphabet is accepted.
var botIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func checkBotID(id string) error {
	if !botIDPattern.MatchString(id) {
		return fmt.Errorf("sandbox: invalid bot id %q", id)
	}
	return nil
}
```

`internal/sandbox/paths.go`:

```go
package sandbox

import "path/filepath"

// Paths lays out <data>\sandbox. Bot ids must pass checkBotID before they reach these methods.
type Paths struct{ Root string } // <data>\sandbox

func (p Paths) Runtime(version string) string { return filepath.Join(p.Root, "runtime", version) }
func (p Paths) Image(version string) string   { return filepath.Join(p.Root, "images", version) }
func (p Paths) Overlay(botID string) string   { return filepath.Join(p.Root, "bots", botID+".qcow2") }
func (p Paths) Socket(botID string) string    { return filepath.Join(p.Root, "run", botID+".sock") }

func (p Paths) Log(dataDir, botID string) string {
	return filepath.Join(dataDir, "logs", "sandbox-"+botID+".log")
}
```

`internal/sandbox/backend.go`:

```go
package sandbox

import (
	"context"
	"net"
)

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

// Backend starts VMs. backend_windows.go (phase 2) runs QEMU; other platforms report NotImplemented; tests use
// fakeBackend.
type Backend interface {
	Availability(ctx context.Context) Availability
	// Boot starts the VM and returns once the guest channel is connected (not necessarily ready).
	Boot(ctx context.Context, spec BootSpec) (VM, error)
}

type VM interface {
	Conn() net.Conn                     // the raw guest byte stream
	Shutdown(ctx context.Context) error // ask guest to power off, wait, then kill
	Kill() error
	Done() <-chan struct{} // closed when the VM process exits
	ExitErr() error
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/...`
Expected: `ok  github.com/rayorole/orole-os-desktop/internal/sandbox` and `ok  .../internal/sandbox/wire`.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox
git commit -m "feat(sandbox): core types, paths, backend interface and guest wire types" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: Store over table `sandboxes`

**Files:**
- Create: `migrations/0112_sandboxes.sql`
- Create: `internal/sandbox/store.go`
- Test: `internal/sandbox/store_test.go`
- Modify: `docs/architecture/database-schema.md` (migration list on line 4, new section before "## Secrets")

**Interfaces:**
- Consumes: `Limits` (Task 1); `database.Open(path string, migs fs.FS) (*database.DB, error)` from `internal/database`, `migrations.FS`.
- Produces: `Record{BotID, ImageVersion, OverlayPath string; Limits Limits; LastUsedAt, CreatedAt time.Time}`, `NewStore(db *sql.DB) *Store`, `(*Store).Get(ctx, botID) (Record, bool, error)`, `Upsert(ctx, Record) error`, `Touch(ctx, botID, at time.Time) error`, `Delete(ctx, botID) error`. Test helper `testDB(t) *sql.DB` in `store_test.go` (used by Tasks 7-9). Times are stored as Unix milliseconds; the zero time is stored as 0 and read back as the zero time.

- [ ] **Step 1: Write the failing test**

`internal/sandbox/store_test.go`:

```go
package sandbox

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/database"
	"github.com/rayorole/orole-os-desktop/migrations"
)

// testDB opens a fresh database with every migration applied.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func TestStoreRoundTrip(t *testing.T) {
	s := NewStore(testDB(t))
	ctx := context.Background()
	if _, ok, err := s.Get(ctx, "b1"); err != nil || ok {
		t.Fatalf("Get on empty table = %v, %v", ok, err)
	}
	created := time.UnixMilli(1_700_000_000_000)
	r := Record{BotID: "b1", ImageVersion: "v1", OverlayPath: `C:\d\sandbox\bots\b1.qcow2`, Limits: Limits{MemoryMB: 4096, CPUs: 4, DiskGB: 20}, LastUsedAt: created, CreatedAt: created}
	if err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "b1")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if got.BotID != r.BotID || got.ImageVersion != "v1" || got.OverlayPath != r.OverlayPath || got.Limits != r.Limits ||
		!got.CreatedAt.Equal(created) || !got.LastUsedAt.Equal(created) {
		t.Fatalf("Get = %+v, want %+v", got, r)
	}

	// Upsert updates everything but created_at.
	r2 := r
	r2.ImageVersion, r2.Limits, r2.CreatedAt = "v2", DefaultLimits(), created.Add(time.Hour)
	if err := s.Upsert(ctx, r2); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "b1")
	if got.ImageVersion != "v2" || got.Limits != DefaultLimits() || !got.CreatedAt.Equal(created) {
		t.Fatalf("after second Upsert = %+v", got)
	}

	later := created.Add(2 * time.Hour)
	if err := s.Touch(ctx, "b1", later); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "b1")
	if !got.LastUsedAt.Equal(later) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, later)
	}
	if err := s.Touch(ctx, "missing", later); err != nil {
		t.Fatalf("Touch on a missing row = %v, want nil", err)
	}

	if err := s.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "b1"); ok {
		t.Fatal("row still there after Delete")
	}
	if err := s.Delete(ctx, "b1"); err != nil {
		t.Fatalf("second Delete = %v, want nil", err)
	}
}

func TestStoreRejectsIncompleteRecords(t *testing.T) {
	s := NewStore(testDB(t))
	for _, r := range []Record{
		{ImageVersion: "v1", OverlayPath: "o"},
		{BotID: "b1", OverlayPath: "o"},
		{BotID: "b1", ImageVersion: "v1"},
	} {
		if err := s.Upsert(context.Background(), r); err == nil {
			t.Errorf("Upsert(%+v) = nil, want an error", r)
		}
	}
}

func TestStoreReportsCorruptLimits(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO sandboxes(bot_id,image_version,overlay_path,limits,last_used_at,created_at) VALUES('b1','v1','o','{not json',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStore(db).Get(context.Background(), "b1"); err == nil {
		t.Fatal("Get with corrupt limits = nil error")
	}
}

func TestStoreDefaultLimitsColumn(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO sandboxes(bot_id,image_version,overlay_path,created_at) VALUES('b1','v1','o',5)`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := NewStore(db).Get(context.Background(), "b1")
	if err != nil || !ok || got.Limits != (Limits{}) || !got.LastUsedAt.IsZero() {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sandbox/ -run TestStore`
Expected: FAIL, `undefined: NewStore` / `undefined: Record`.

- [ ] **Step 3: Write the migration and the store**

`migrations/0112_sandboxes.sql`:

```sql
-- One row per bot that has (had) a computer: its image version, overlay disk and limits. Run state is in memory
-- only (internal/sandbox.Manager). No foreign key to bots: Manager.Delete removes the overlay file and the row
-- together, which a cascade could not do. Times are Unix ms.
CREATE TABLE sandboxes (
  bot_id        TEXT PRIMARY KEY,
  image_version TEXT NOT NULL,
  overlay_path  TEXT NOT NULL,
  limits        TEXT NOT NULL DEFAULT '{}',
  last_used_at  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);
```

`internal/sandbox/store.go`:

```go
package sandbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Record is one row of table sandboxes.
type Record struct {
	BotID, ImageVersion, OverlayPath string
	Limits                           Limits
	LastUsedAt, CreatedAt            time.Time
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Get(ctx context.Context, botID string) (Record, bool, error) {
	var (
		r             Record
		limits        string
		last, created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT bot_id, image_version, overlay_path, limits, last_used_at, created_at FROM sandboxes WHERE bot_id=?`, botID).
		Scan(&r.BotID, &r.ImageVersion, &r.OverlayPath, &limits, &last, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := json.Unmarshal([]byte(limits), &r.Limits); err != nil {
		return Record{}, false, fmt.Errorf("sandbox: limits of %s: %w", botID, err)
	}
	r.LastUsedAt, r.CreatedAt = fromMS(last), fromMS(created)
	return r, true, nil
}

// Upsert inserts r, or updates every column but created_at when the bot already has a row.
func (s *Store) Upsert(ctx context.Context, r Record) error {
	if r.BotID == "" || r.ImageVersion == "" || r.OverlayPath == "" {
		return errors.New("sandbox: a record needs a bot id, image version and overlay path")
	}
	limits, err := json.Marshal(r.Limits)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO sandboxes(bot_id, image_version, overlay_path, limits, last_used_at, created_at) VALUES (?,?,?,?,?,?)
ON CONFLICT(bot_id) DO UPDATE SET image_version=excluded.image_version, overlay_path=excluded.overlay_path,
  limits=excluded.limits, last_used_at=excluded.last_used_at`,
		r.BotID, r.ImageVersion, r.OverlayPath, string(limits), toMS(r.LastUsedAt), toMS(r.CreatedAt))
	return err
}

// Touch records the last use; a bot without a row is not an error.
func (s *Store) Touch(ctx context.Context, botID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET last_used_at=? WHERE bot_id=?`, toMS(at), botID)
	return err
}

func (s *Store) Delete(ctx context.Context, botID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sandboxes WHERE bot_id=?`, botID)
	return err
}

func toMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/ ./internal/database/`
Expected: `ok` for both (the database package's tests apply every migration, including 0112).

- [ ] **Step 5: Update the schema doc**

In `docs/architecture/database-schema.md` line 4 replace `` and the chat series `0096`-`0111`. `` with `` the chat series `0096`-`0111`, and `0112`. ``. Insert before `## Secrets`:

```markdown
## Bot computers (migration 0112)

| Table | Purpose |
|---|---|
| `sandboxes(bot_id PK, image_version, overlay_path, limits JSON, last_used_at, created_at)` | one row per bot that has a computer (a local VM): the guest image version its overlay was made from, the overlay disk path, per-bot limits (`{"memoryMB","cpus","diskGB"}`, zero = default) and times in Unix ms. Run state is in memory only. No foreign key to `bots`: `sandbox.Manager.Delete` removes the overlay file and the row together. See `bot-computer.md`. |
```

- [ ] **Step 6: Commit**

```bash
git add migrations/0112_sandboxes.sql internal/sandbox/store.go internal/sandbox/store_test.go docs/architecture/database-schema.md
git commit -m "feat(sandbox): sandboxes table and store" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Guest agent handlers (health, exec, files, poweroff)

**Files:**
- Create: `internal/sandbox/guestagent/agent.go`
- Create: `internal/sandbox/guestagent/exec.go`
- Create: `internal/sandbox/guestagent/files.go`
- Create: `internal/sandbox/guestagent/capped.go`
- Create: `internal/sandbox/guestagent/procgroup_unix.go`
- Create: `internal/sandbox/guestagent/procgroup_other.go`
- Create: `internal/sandbox/guestagent/agenttest/agenttest.go`
- Test: `internal/sandbox/guestagent/agent_test.go`
- Test: `internal/sandbox/guestagent/exec_linux_test.go`

Why `internal/sandbox/guestagent`: the handler is OS-neutral Go that host tests (Task 7's fake backend) must run in-process, and AGENTS.md keeps shared code under `internal/`; `cmd/orole-guest` (Task 6) stays a thin Linux main. It imports only `internal/sandbox/wire`.

**Interfaces:**
- Consumes: `wire.*` (Task 1).
- Produces:
  - `guestagent.Config{Token, Version, Home string; Shell []string; Ready func() bool; PowerOff func() error; Logf func(format string, args ...any)}` (Task 5 adds `LoginShell []string` and `StartPTY func(PTYSpec) (PTY, error)`).
  - `guestagent.New(cfg Config) *Agent`, `(*Agent).Handler() http.Handler`.
  - Routes: `GET /health`, `POST /exec`, `POST /files/read`, `POST /files/write`, `POST /screen` (501), `POST /browser` (501), `POST /poweroff` (202, then `PowerOff` after 100 ms; 501 when nil).
  - `agenttest.Main(m *testing.M)` and `agenttest.Shell() []string`: the helper understands `echo TEXT`, `err TEXT`, `exit N`, `flood N`, `forever`, `sleep DURATION`, `pwd`, `env NAME`.

- [ ] **Step 1: Write the test helper package**

`internal/sandbox/guestagent/agenttest/agenttest.go`:

```go
// Package agenttest lets a test binary act as the shell that guestagent runs for /exec, so exec tests behave the
// same on Windows and Linux without /bin/sh. Call Main from TestMain and put Shell() in guestagent.Config.Shell.
package agenttest

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperArg = "orole-guest-helper"

// Main runs one helper command when the binary was started as the helper, otherwise the tests.
func Main(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == helperArg {
		os.Exit(run(os.Args[2]))
	}
	os.Exit(m.Run())
}

// Shell is the guestagent.Config.Shell value that runs commands through this binary; the agent appends the command.
func Shell() []string {
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return []string{exe, helperArg}
}

// run understands: echo TEXT, err TEXT, exit N, flood N (N bytes of 'y'), forever (writes until killed),
// sleep DURATION, pwd, env NAME.
func run(command string) int {
	verb, arg, _ := strings.Cut(command, " ")
	switch verb {
	case "echo":
		fmt.Println(arg)
	case "err":
		fmt.Fprintln(os.Stderr, arg)
	case "exit":
		n, _ := strconv.Atoi(arg)
		return n
	case "flood":
		n, _ := strconv.Atoi(arg)
		os.Stdout.Write(bytes.Repeat([]byte("y"), n))
	case "forever":
		chunk := bytes.Repeat([]byte("y\n"), 4096)
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return 1
			}
		}
	case "sleep":
		d, _ := time.ParseDuration(arg)
		time.Sleep(d)
	case "pwd":
		wd, _ := os.Getwd()
		fmt.Println(wd)
	case "env":
		fmt.Println(os.Getenv(arg))
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown command %q\n", command)
		return 127
	}
	return 0
}
```

- [ ] **Step 2: Write the failing tests**

`internal/sandbox/guestagent/agent_test.go`:

```go
package guestagent_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestMain(m *testing.M) { agenttest.Main(m) }

const token = "secret-token"

func newAgent(t *testing.T, mod func(*guestagent.Config)) (*guestagent.Agent, string) {
	t.Helper()
	home := t.TempDir()
	cfg := guestagent.Config{Token: token, Version: "1.2.3", Home: home, Shell: agenttest.Shell()}
	if mod != nil {
		mod(&cfg)
	}
	return guestagent.New(cfg), home
}

func call(t *testing.T, a *guestagent.Agent, method, path, tok string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "http://guest"+path, rd)
	if tok != "" {
		req.Header.Set(wire.TokenHeader, tok)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTokenRequired(t *testing.T) {
	a, _ := newAgent(t, nil)
	for _, tc := range []struct {
		tok  string
		want int
	}{{"", 401}, {"wrong", 401}, {token + "x", 401}, {token, 200}} {
		if rec := call(t, a, "GET", "/health", tc.tok, nil); rec.Code != tc.want {
			t.Errorf("token %q: status %d, want %d", tc.tok, rec.Code, tc.want)
		}
	}
	open, _ := newAgent(t, func(c *guestagent.Config) { c.Token = "" })
	if rec := call(t, open, "GET", "/health", "", nil); rec.Code != 401 {
		t.Errorf("agent without a token accepted a request: %d", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	ready := false
	a, _ := newAgent(t, func(c *guestagent.Config) { c.Ready = func() bool { return ready } })
	if h := decode[wire.Health](t, call(t, a, "GET", "/health", token, nil)); h.Ready || h.Version != "1.2.3" {
		t.Fatalf("health = %+v", h)
	}
	ready = true
	if h := decode[wire.Health](t, call(t, a, "GET", "/health", token, nil)); !h.Ready {
		t.Fatalf("health = %+v, want ready", h)
	}
	if rec := call(t, a, "POST", "/health", token, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health = %d", rec.Code)
	}
}

func TestExec(t *testing.T) {
	a, home := newAgent(t, nil)
	if err := os.Mkdir(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := func(req wire.ExecRequest) wire.ExecResult {
		return decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, req))
	}
	if r := exec(wire.ExecRequest{Command: "echo hi"}); r.Stdout != "hi\n" || r.ExitCode != 0 || r.Truncated || r.TimedOut {
		t.Errorf("echo = %+v", r)
	}
	if r := exec(wire.ExecRequest{Command: "err oops"}); r.Stderr != "oops\n" || r.Stdout != "" {
		t.Errorf("err = %+v", r)
	}
	if r := exec(wire.ExecRequest{Command: "exit 3"}); r.ExitCode != 3 {
		t.Errorf("exit 3 = %+v", r)
	}
	if r := exec(wire.ExecRequest{Command: "env HOME"}); strings.TrimSpace(r.Stdout) != home {
		t.Errorf("HOME = %q, want %q", r.Stdout, home)
	}
	r := exec(wire.ExecRequest{Command: "pwd", Cwd: "work"})
	got, err1 := os.Stat(strings.TrimSpace(r.Stdout))
	want, err2 := os.Stat(filepath.Join(home, "work"))
	if err1 != nil || err2 != nil || !os.SameFile(got, want) {
		t.Errorf("relative cwd ran in %q, want %s/work", r.Stdout, home)
	}
	if rec := call(t, a, "POST", "/exec", token, wire.ExecRequest{Command: "  "}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty command = %d", rec.Code)
	}
}

func TestExecCapsOutput(t *testing.T) {
	a, _ := newAgent(t, nil)
	r := decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, wire.ExecRequest{Command: "flood 3000000"}))
	if len(r.Stdout) != wire.MaxExecOutput || !r.Truncated || r.ExitCode != 0 {
		t.Fatalf("flood: %d bytes, truncated=%v, exit=%d", len(r.Stdout), r.Truncated, r.ExitCode)
	}
}

func TestExecTimeout(t *testing.T) {
	a, _ := newAgent(t, nil)
	start := time.Now()
	r := decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, wire.ExecRequest{Command: "forever", Timeout: 1}))
	if !r.TimedOut || r.ExitCode != -1 || !r.Truncated || len(r.Stdout) > wire.MaxExecOutput {
		t.Fatalf("forever: timedOut=%v exit=%d truncated=%v len=%d", r.TimedOut, r.ExitCode, r.Truncated, len(r.Stdout))
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("a 1 s timeout took %v", d)
	}
}

func TestFiles(t *testing.T) {
	a, home := newAgent(t, nil)
	w := decode[wire.WriteFileResult](t, call(t, a, "POST", "/files/write", token, wire.WriteFileRequest{Path: "notes/a.txt", Content: []byte("hello"), Mode: 0o600}))
	if w.Bytes != 5 {
		t.Fatalf("write = %+v", w)
	}
	onDisk, err := os.ReadFile(filepath.Join(home, "notes", "a.txt"))
	if err != nil || string(onDisk) != "hello" {
		t.Fatalf("file on disk = %q, %v", onDisk, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(home, "notes", "a.txt"))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", st.Mode().Perm())
		}
	}
	r := decode[wire.ReadFileResult](t, call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: "notes/a.txt"}))
	if string(r.Content) != "hello" || r.Truncated {
		t.Fatalf("read = %+v", r)
	}
	abs := filepath.Join(home, "notes", "a.txt")
	if r := decode[wire.ReadFileResult](t, call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: abs})); string(r.Content) != "hello" {
		t.Fatalf("absolute read = %+v", r)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"missing.txt", 404}, {"notes", 400}, {"", 400}} {
		if rec := call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: tc.path}); rec.Code != tc.want {
			t.Errorf("read %q = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}

func TestReadFileCap(t *testing.T) {
	a, home := newAgent(t, nil)
	if err := os.WriteFile(filepath.Join(home, "big.bin"), bytes.Repeat([]byte{0}, 5<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	r := decode[wire.ReadFileResult](t, call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: "big.bin"}))
	if len(r.Content) != wire.MaxReadFile || !r.Truncated {
		t.Fatalf("read %d bytes, truncated=%v", len(r.Content), r.Truncated)
	}
}

func TestWriteFileLimit(t *testing.T) {
	a, home := newAgent(t, nil)
	rec := call(t, a, "POST", "/files/write", token, wire.WriteFileRequest{Path: "big.bin", Content: make([]byte, wire.MaxWriteFile+1)})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized write = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(home, "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("oversized write left a file: %v", err)
	}
	if rec := call(t, a, "POST", "/files/write", token, wire.WriteFileRequest{Path: "ok.bin", Content: make([]byte, wire.MaxWriteFile)}); rec.Code != 200 {
		t.Fatalf("16 MiB write = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenAndBrowserNotAvailableYet(t *testing.T) {
	a, _ := newAgent(t, nil)
	for _, p := range []string{"/screen", "/browser"} {
		if rec := call(t, a, "POST", p, token, nil); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s = %d, want 501", p, rec.Code)
		}
	}
}

func TestPowerOff(t *testing.T) {
	called := make(chan struct{})
	a, _ := newAgent(t, func(c *guestagent.Config) { c.PowerOff = func() error { close(called); return nil } })
	if rec := call(t, a, "POST", "/poweroff", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("poweroff = %d", rec.Code)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("PowerOff never called")
	}
	none, _ := newAgent(t, nil)
	if rec := call(t, none, "POST", "/poweroff", token, nil); rec.Code != http.StatusNotImplemented {
		t.Fatalf("poweroff without a hook = %d", rec.Code)
	}
}
```

`internal/sandbox/guestagent/exec_linux_test.go` (real `/bin/sh`, runs in the Linux CI lane):

```go
//go:build linux

package guestagent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestExecRealShell(t *testing.T) {
	a, home := newAgent(t, func(c *guestagent.Config) { c.Shell = nil }) // default /bin/sh -c
	exec := func(req wire.ExecRequest) (wire.ExecResult, time.Duration) {
		start := time.Now()
		r := decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, req))
		return r, time.Since(start)
	}
	if r, _ := exec(wire.ExecRequest{Command: `yes | head -c 3000000`}); len(r.Stdout) != wire.MaxExecOutput || !r.Truncated || r.ExitCode != 0 {
		t.Errorf("yes|head: len=%d truncated=%v exit=%d", len(r.Stdout), r.Truncated, r.ExitCode)
	}
	if r, _ := exec(wire.ExecRequest{Command: `echo "$HOME"; pwd`}); r.Stdout != home+"\n"+home+"\n" {
		t.Errorf("HOME/pwd = %q", r.Stdout)
	}
	// A background child keeps stdout open; the call still returns (WaitDelay closes the pipes).
	if r, d := exec(wire.ExecRequest{Command: `sleep 20 & echo started`}); !strings.HasPrefix(r.Stdout, "started") || d > 10*time.Second {
		t.Errorf("background child: %q after %v", r.Stdout, d)
	}
	// The timeout kills the whole process group, not just sh.
	if r, d := exec(wire.ExecRequest{Command: `sleep 30; echo never`, Timeout: 1}); !r.TimedOut || r.Stdout != "" || d > 5*time.Second {
		t.Errorf("timeout: %+v after %v", r, d)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/guestagent/...`
Expected: FAIL, `undefined: guestagent.Config` / `undefined: guestagent.New`.

- [ ] **Step 4: Write the implementation**

`internal/sandbox/guestagent/agent.go`:

```go
// Package guestagent is the HTTP handler of orole-guest, the agent inside a bot's VM. It is OS-neutral so host
// tests can run the real handler in-process over net.Pipe; cmd/orole-guest serves it on the virtio-serial port
// inside the Linux guest. It must never import internal/sandbox.
package guestagent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

type Config struct {
	Token    string                           // per-boot secret; requests without it get 401. Empty rejects everything.
	Version  string                           // reported by /health
	Home     string                           // default cwd and base of relative paths; default wire.Home
	Shell    []string                         // argv prefix for /exec, the command is appended; default /bin/sh -c
	Ready    func() bool                      // /health "ready"; nil means ready
	PowerOff func() error                     // called 100 ms after /poweroff answers 202; nil answers 501
	Logf     func(format string, args ...any) // nil discards
}

type Agent struct {
	cfg Config
	mux *http.ServeMux
}

func New(cfg Config) *Agent {
	if cfg.Home == "" {
		cfg.Home = wire.Home
	}
	if len(cfg.Shell) == 0 {
		cfg.Shell = []string{"/bin/sh", "-c"}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	a := &Agent{cfg: cfg, mux: http.NewServeMux()}
	a.mux.HandleFunc("GET /health", a.handleHealth)
	a.mux.HandleFunc("POST /exec", a.handleExec)
	a.mux.HandleFunc("POST /files/read", a.handleReadFile)
	a.mux.HandleFunc("POST /files/write", a.handleWriteFile)
	a.mux.HandleFunc("POST /screen", notAvailable)
	a.mux.HandleFunc("POST /browser", notAvailable)
	a.mux.HandleFunc("POST /poweroff", a.handlePowerOff)
	return a
}

// Handler checks the token on every request, then routes it.
func (a *Agent) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(wire.TokenHeader)
		if a.cfg.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.mux.ServeHTTP(w, r)
	})
}

func (a *Agent) handleHealth(w http.ResponseWriter, _ *http.Request) {
	ready := a.cfg.Ready == nil || a.cfg.Ready()
	writeJSON(w, http.StatusOK, wire.Health{Version: a.cfg.Version, Ready: ready})
}

// notAvailable answers the stream types later phases add (/screen in phase 4, /browser in phase 3).
func notAvailable(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "not available in this guest version", http.StatusNotImplemented)
}

func (a *Agent) handlePowerOff(w http.ResponseWriter, _ *http.Request) {
	if a.cfg.PowerOff == nil {
		http.Error(w, "power off is not available", http.StatusNotImplemented)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = http.NewResponseController(w).Flush()
	off := a.cfg.PowerOff
	time.AfterFunc(100*time.Millisecond, func() { // let the 202 reach the host first
		if err := off(); err != nil {
			a.cfg.Logf("power off: %v", err)
		}
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return false
	}
	http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
```

`internal/sandbox/guestagent/capped.go`:

```go
package guestagent

import (
	"bytes"
	"sync"
)

// capped is a byte budget shared by a command's stdout and stderr: writes beyond it are dropped (and reported as
// written, so the command never blocks on a full pipe).
type capped struct {
	mu   sync.Mutex
	left int
	cut  bool
}

func newCapped(n int) *capped { return &capped{left: n} }

func (c *capped) writer() *capWriter { return &capWriter{c: c} }

func (c *capped) truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cut
}

type capWriter struct {
	c   *capped
	buf bytes.Buffer
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	n := min(len(p), w.c.left)
	w.buf.Write(p[:n])
	w.c.left -= n
	if n < len(p) {
		w.c.cut = true
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.buf.String()
}
```

`internal/sandbox/guestagent/exec.go`:

```go
package guestagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// waitDelay bounds how long a finished command may keep its output pipes open through background children.
const waitDelay = 2 * time.Second

func (a *Agent) handleExec(w http.ResponseWriter, r *http.Request) {
	var req wire.ExecRequest
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		http.Error(w, "empty command", http.StatusBadRequest)
		return
	}
	res, err := a.exec(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *Agent) exec(parent context.Context, req wire.ExecRequest) (wire.ExecResult, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(wire.ClampTimeout(req.Timeout))*time.Second)
	defer cancel()
	argv := append(slices.Clone(a.cfg.Shell), req.Command)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = a.resolve(req.Cwd)
	cmd.Env = append(os.Environ(), "HOME="+a.cfg.Home)
	cmd.WaitDelay = waitDelay
	setProcessGroup(cmd)
	out := newCapped(wire.MaxExecOutput)
	stdout, stderr := out.writer(), out.writer()
	cmd.Stdout, cmd.Stderr = stdout, stderr

	runErr := cmd.Run()
	res := wire.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), Truncated: out.truncated()}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil {
		res.TimedOut, res.ExitCode = true, -1
		return res, nil
	}
	if cmd.ProcessState == nil {
		return wire.ExecResult{}, fmt.Errorf("start %s: %w", argv[0], runErr)
	}
	res.ExitCode = cmd.ProcessState.ExitCode()
	return res, nil
}

// resolve maps a guest path: empty is home, relative is under home, absolute is kept (the VM is the boundary).
func (a *Agent) resolve(p string) string {
	if p == "" {
		return a.cfg.Home
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(a.cfg.Home, p)
}
```

`internal/sandbox/guestagent/files.go`:

```go
package guestagent

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func (a *Agent) handleReadFile(w http.ResponseWriter, r *http.Request) {
	var req wire.ReadFileRequest
	if !readJSON(w, r, 64<<10, &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "empty path", http.StatusBadRequest)
		return
	}
	f, err := os.Open(a.resolve(req.Path))
	if err != nil {
		fileError(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fileError(w, err)
		return
	}
	if st.IsDir() {
		http.Error(w, "is a directory", http.StatusBadRequest)
		return
	}
	buf, err := io.ReadAll(io.LimitReader(f, wire.MaxReadFile+1))
	if err != nil {
		fileError(w, err)
		return
	}
	res := wire.ReadFileResult{Content: buf}
	if len(buf) > wire.MaxReadFile {
		res.Content, res.Truncated = buf[:wire.MaxReadFile], true
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *Agent) handleWriteFile(w http.ResponseWriter, r *http.Request) {
	var req wire.WriteFileRequest
	// base64 makes the content 4/3 larger; 1 MiB of slack covers the rest of the JSON.
	if !readJSON(w, r, wire.MaxWriteFile*4/3+(1<<20), &req) {
		return
	}
	if req.Path == "" {
		http.Error(w, "empty path", http.StatusBadRequest)
		return
	}
	if len(req.Content) > wire.MaxWriteFile {
		http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
		return
	}
	mode := fs.FileMode(req.Mode).Perm()
	if mode == 0 {
		mode = 0o644
	}
	p := a.resolve(req.Path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fileError(w, err)
		return
	}
	if err := os.WriteFile(p, req.Content, mode); err != nil {
		fileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, wire.WriteFileResult{Bytes: len(req.Content)})
}

func fileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, fs.ErrPermission):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
```

`internal/sandbox/guestagent/procgroup_unix.go`:

```go
//go:build unix

package guestagent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group so a timeout kills everything it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
```

`internal/sandbox/guestagent/procgroup_other.go`:

```go
//go:build !unix

package guestagent

import "os/exec"

// setProcessGroup is a no-op off Unix (host tests on Windows); exec's default Cancel kills the process.
func setProcessGroup(*exec.Cmd) {}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/guestagent/...`
Expected: `ok  github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent` (agenttest has no tests).
Run: `GOOS=linux GOARCH=amd64 go vet ./internal/sandbox/guestagent/...`
Expected: no output (the Linux files and `exec_linux_test.go` type-check).

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/guestagent
git commit -m "feat(sandbox): guest agent handlers for health, exec, files and poweroff" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: h2c serving and the host's guest client

**Files:**
- Modify: `go.mod`, `go.sum` (add `golang.org/x/net`)
- Create: `internal/sandbox/guestagent/serve.go`
- Create: `internal/sandbox/guestclient.go`
- Test: `internal/sandbox/main_test.go`
- Test: `internal/sandbox/guestclient_test.go`

**Interfaces:**
- Consumes: `guestagent.New`, `Config`, `Handler` (Task 3); `agenttest` (Task 3); `wire.*`; `errStopped` (Task 1).
- Produces:
  - `(*guestagent.Agent).ServeConn(ctx context.Context, c net.Conn)`; `(*guestagent.Agent).ServeLoop(ctx context.Context, open func() (net.Conn, error), retry time.Duration) error` (returns `ctx.Err()`).
  - `sandbox.Guest` with the master-plan methods `NewGuest`, `Health`, `Exec`, `ReadFile`, `WriteFile`, `Browser`, `PowerOff`, `Close` (`OpenPTY`/`OpenScreen` in Task 5).
  - Unexported, used by the manager (Task 7): `newGuestCtx(ctx, conn, token) (*Guest, error)`, `(*Guest).setOnUse(func(delta int))`, `(*Guest).abort(err error)`, `(*Guest).use(delta int)`, `(*Guest).mapErr(ctx, err) error`, `(*Guest).call(ctx, method, path string, in, out any) error`, `statusError(path string, resp *http.Response) error`, constants `guestBase = "http://guest"`, `maxResponseBytes = 8 << 20`, `crashGrace = 500 * time.Millisecond`.
  - Test helpers in `main_test.go`: `TestMain`, `eventually(t, cond func() bool)`, `readUntil(t, r io.Reader, want string)`, `scratchHome(t) string`; in `guestclient_test.go`: `startAgent(t, cfg guestagent.Config) (*Guest, *guestagent.Agent)`, `startHandler(t, h http.Handler) *Guest`.

- [ ] **Step 1: Add the dependency**

Run: `go get golang.org/x/net@v0.58.0`
Expected: `go: added golang.org/x/net v0.58.0` (it may also upgrade `golang.org/x/sys`, `x/text` or `x/crypto`; accept that).

- [ ] **Step 2: Write the failing tests**

`internal/sandbox/main_test.go`:

```go
package sandbox

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
)

func TestMain(m *testing.M) { agenttest.Main(m) }

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readUntil(t *testing.T, r io.Reader, want string) {
	t.Helper()
	found := make(chan struct{})
	go func() {
		var got []byte
		buf := make([]byte, 1024)
		for {
			n, err := r.Read(buf)
			got = append(got, buf[:n]...)
			if bytes.Contains(got, []byte(want)) {
				close(found)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(5 * time.Second):
		t.Fatalf("never read %q", want)
	}
}

// scratchHome is a guest home outside t.TempDir: helper processes killed at the end of a test may still hold it as
// their working directory for a moment, which would make TempDir's cleanup fail on Windows.
func scratchHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "orole-guest-home-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
```

`internal/sandbox/guestclient_test.go`:

```go
package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

const testToken = "tok"

// startAgent serves the real guest handler on one end of a net.Pipe and returns a Guest on the other.
func startAgent(t *testing.T, cfg guestagent.Config) (*Guest, *guestagent.Agent) {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	if cfg.Home == "" {
		cfg.Home = scratchHome(t)
	}
	if cfg.Shell == nil {
		cfg.Shell = agenttest.Shell()
	}
	a := guestagent.New(cfg)
	host, guest := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.ServeConn(ctx, guest); close(done) }()
	g, err := NewGuest(host, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close(); cancel(); <-done })
	return g, a
}

// startHandler serves any handler (a hostile guest, a stream echo) the same way.
func startHandler(t *testing.T, h http.Handler) *Guest {
	t.Helper()
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() { (&http2.Server{}).ServeConn(guest, &http2.ServeConnOpts{Handler: h}); close(done) }()
	g, err := NewGuest(host, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close(); guest.Close(); <-done })
	return g
}

func TestGuestHealthAndExec(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{Version: "9"})
	ctx := context.Background()
	h, err := g.Health(ctx)
	if err != nil || !h.Ready || h.Version != "9" {
		t.Fatalf("Health = %+v, %v", h, err)
	}
	res, err := g.Exec(ctx, wire.ExecRequest{Command: "echo hi"})
	if err != nil || res.Stdout != "hi\n" {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	res, err = g.Exec(ctx, wire.ExecRequest{Command: "flood 3000000"})
	if err != nil || len(res.Stdout) != wire.MaxExecOutput || !res.Truncated {
		t.Fatalf("flood through the channel: len=%d truncated=%v err=%v", len(res.Stdout), res.Truncated, err)
	}
}

func TestGuestWrongTokenIsRejected(t *testing.T) {
	a := guestagent.New(guestagent.Config{Token: "right", Home: scratchHome(t)})
	host, guest := net.Pipe()
	go a.ServeConn(context.Background(), guest)
	g, err := NewGuest(host, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Health with the wrong token = %v", err)
	}
}

func TestGuestFilesRoundTrip(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	ctx := context.Background()
	w, err := g.WriteFile(ctx, "dir/a.txt", []byte("hello"))
	if err != nil || w.Bytes != 5 {
		t.Fatalf("WriteFile = %+v, %v", w, err)
	}
	r, err := g.ReadFile(ctx, "dir/a.txt")
	if err != nil || string(r.Content) != "hello" {
		t.Fatalf("ReadFile = %+v, %v", r, err)
	}
	if _, err := g.WriteFile(ctx, "big", make([]byte, wire.MaxWriteFile+1)); err == nil {
		t.Fatal("WriteFile over 16 MiB = nil error")
	}
}

func TestGuestCapsHostileExecOutput(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wire.ExecResult{Stdout: big, Stderr: "tail"})
	}))
	res, err := g.Exec(context.Background(), wire.ExecRequest{Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != wire.MaxExecOutput || res.Stderr != "" || !res.Truncated {
		t.Fatalf("host cap: stdout=%d stderr=%q truncated=%v", len(res.Stdout), res.Stderr, res.Truncated)
	}
}

func TestGuestCapsHostileReadFile(t *testing.T) {
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wire.ReadFileResult{Content: make([]byte, 5<<20)})
	}))
	res, err := g.ReadFile(context.Background(), "x")
	if err != nil || len(res.Content) != wire.MaxReadFile || !res.Truncated {
		t.Fatalf("ReadFile = %d bytes truncated=%v err=%v", len(res.Content), res.Truncated, err)
	}
}

func TestGuestRejectsOversizedResponse(t *testing.T) {
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"stdout":"`)
		chunk := strings.Repeat("x", 1<<20)
		for range 9 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
		fmt.Fprint(w, `"}`)
	}))
	done := make(chan error, 1)
	go func() { _, err := g.Exec(context.Background(), wire.ExecRequest{Command: "x"}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("Exec = %v, want a too-large error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("oversized answer blocked the client")
	}
}

func TestGuestUnsupportedEndpoints(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	if _, err := g.Browser(context.Background(), wire.BrowserRequest{Action: "read"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Browser = %v, want ErrUnsupported", err)
	}
}

func TestGuestAbortMapsTransportErrors(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	started := make(chan struct{}, 1)
	g.setOnUse(func(delta int) {
		if delta > 0 {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})
	errc := make(chan error, 1)
	go func() {
		_, err := g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 10s"})
		errc <- err
	}()
	<-started
	g.abort(ErrCrashed) // what the manager does when the VM process dies
	g.conn.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrCrashed) {
			t.Fatalf("in-flight Exec = %v, want ErrCrashed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Exec never returned")
	}
	if _, err := g.Health(context.Background()); !errors.Is(err, ErrCrashed) {
		t.Fatalf("call after abort = %v, want ErrCrashed", err)
	}
}

func TestGuestReportsUse(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	var mu sync.Mutex
	var deltas []int
	g.setOnUse(func(d int) { mu.Lock(); deltas = append(deltas, d); mu.Unlock() })
	if _, err := g.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != 2 || deltas[0] != 1 || deltas[1] != -1 {
		t.Fatalf("use deltas = %v, want [1 -1]", deltas)
	}
}

func TestServeLoopReconnects(t *testing.T) {
	a := guestagent.New(guestagent.Config{Token: testToken, Home: scratchHome(t), Shell: agenttest.Shell()})
	ctx, cancel := context.WithCancel(context.Background())
	conns := make(chan net.Conn)
	done := make(chan error, 1)
	go func() {
		done <- a.ServeLoop(ctx, func() (net.Conn, error) {
			select {
			case c := <-conns:
				return c, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, 10*time.Millisecond)
	}()
	for i := range 2 {
		host, guest := net.Pipe()
		conns <- guest
		g, err := NewGuest(host, testToken)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Health(ctx); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		g.Close()
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeLoop = %v, want context.Canceled", err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'TestGuest|TestServeLoop'`
Expected: FAIL, `undefined: NewGuest` and `a.ServeConn undefined`.

- [ ] **Step 4: Write the implementation**

`internal/sandbox/guestagent/serve.go`:

```go
package guestagent

import (
	"context"
	"net"
	"time"

	"golang.org/x/net/http2"
)

// ServeConn serves HTTP/2 without TLS (prior knowledge, no upgrade) on c until c fails or ctx ends.
func (a *Agent) ServeConn(ctx context.Context, c net.Conn) {
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	(&http2.Server{}).ServeConn(c, &http2.ServeConnOpts{Context: ctx, Handler: a.Handler()})
}

// ServeLoop serves one connection at a time from open, opening the next when one ends, until ctx ends. The
// virtio-serial port reports the host side going away as EOF; reopening picks up the next host connection.
func (a *Agent) ServeLoop(ctx context.Context, open func() (net.Conn, error), retry time.Duration) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := open()
		if err != nil {
			a.cfg.Logf("open guest channel: %v", err)
		} else {
			a.ServeConn(ctx, c)
			_ = c.Close()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retry):
		}
	}
}
```

`internal/sandbox/guestclient.go`:

```go
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

const (
	guestBase = "http://guest"
	// maxResponseBytes bounds any JSON answer: 1 MiB of exec output escapes to at most ~6 MiB, a 4 MiB file
	// base64-encodes to ~5.4 MiB.
	maxResponseBytes = 8 << 20
	// crashGrace is how long a failed request waits for the manager to say why the channel died.
	crashGrace = 500 * time.Millisecond
)

// Guest is an HTTP/2 (h2c) client bound to one VM's byte stream. Its answers are untrusted guest data.
type Guest struct {
	token string
	conn  net.Conn
	cc    *http2.ClientConn

	abortOnce sync.Once
	aborted   chan struct{}
	abortErr  error

	mu    sync.Mutex
	onUse func(delta int)
}

func NewGuest(conn net.Conn, token string) (*Guest, error) {
	cc, err := (&http2.Transport{AllowHTTP: true}).NewClientConn(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: guest channel: %w", err)
	}
	return &Guest{token: token, conn: conn, cc: cc, aborted: make(chan struct{})}, nil
}

// newGuestCtx is NewGuest bounded by ctx: writing the HTTP/2 preface blocks until the guest reads it.
func newGuestCtx(ctx context.Context, conn net.Conn, token string) (*Guest, error) {
	type result struct {
		g   *Guest
		err error
	}
	ch := make(chan result, 1)
	go func() {
		g, err := NewGuest(conn, token)
		ch <- result{g, err}
	}()
	select {
	case r := <-ch:
		return r.g, r.err
	case <-ctx.Done():
		_ = conn.Close()
		if r := <-ch; r.g != nil {
			_ = r.g.Close()
		}
		return nil, ctx.Err()
	}
}

func (g *Guest) Health(ctx context.Context) (wire.Health, error) {
	var h wire.Health
	err := g.call(ctx, http.MethodGet, "/health", nil, &h)
	return h, err
}

// Exec runs a command; output is capped again here (1 MiB total) whatever the guest sends.
func (g *Guest) Exec(ctx context.Context, req wire.ExecRequest) (wire.ExecResult, error) {
	var res wire.ExecResult
	if err := g.call(ctx, http.MethodPost, "/exec", req, &res); err != nil {
		return wire.ExecResult{}, err
	}
	if len(res.Stdout)+len(res.Stderr) > wire.MaxExecOutput {
		res.Truncated = true
		if len(res.Stdout) > wire.MaxExecOutput {
			res.Stdout = res.Stdout[:wire.MaxExecOutput]
		}
		res.Stderr = res.Stderr[:min(len(res.Stderr), wire.MaxExecOutput-len(res.Stdout))]
	}
	return res, nil
}

func (g *Guest) ReadFile(ctx context.Context, path string) (wire.ReadFileResult, error) {
	var res wire.ReadFileResult
	if err := g.call(ctx, http.MethodPost, "/files/read", wire.ReadFileRequest{Path: path}, &res); err != nil {
		return wire.ReadFileResult{}, err
	}
	if len(res.Content) > wire.MaxReadFile {
		res.Content, res.Truncated = res.Content[:wire.MaxReadFile], true
	}
	return res, nil
}

func (g *Guest) WriteFile(ctx context.Context, path string, content []byte) (wire.WriteFileResult, error) {
	if len(content) > wire.MaxWriteFile {
		return wire.WriteFileResult{}, fmt.Errorf("sandbox: the file is %d bytes; the limit is %d", len(content), wire.MaxWriteFile)
	}
	var res wire.WriteFileResult
	err := g.call(ctx, http.MethodPost, "/files/write", wire.WriteFileRequest{Path: path, Content: content}, &res)
	return res, err
}

func (g *Guest) Browser(ctx context.Context, req wire.BrowserRequest) (wire.BrowserResult, error) {
	var res wire.BrowserResult
	err := g.call(ctx, http.MethodPost, "/browser", req, &res)
	return res, err
}

// PowerOff asks the guest to shut down; it answers 202 before it does.
func (g *Guest) PowerOff(ctx context.Context) error {
	return g.call(ctx, http.MethodPost, "/poweroff", nil, nil)
}

func (g *Guest) Close() error {
	_ = g.cc.Close()
	_ = g.conn.Close()
	return nil
}

func (g *Guest) setOnUse(fn func(delta int)) {
	g.mu.Lock()
	g.onUse = fn
	g.mu.Unlock()
}

// use reports a request or stream starting (+1) or ending (-1) to the manager.
func (g *Guest) use(delta int) {
	g.mu.Lock()
	fn := g.onUse
	g.mu.Unlock()
	if fn != nil {
		fn(delta)
	}
}

// abort records why the channel is going away; calls in flight and later calls return err.
func (g *Guest) abort(err error) {
	g.abortOnce.Do(func() {
		g.abortErr = err
		close(g.aborted)
	})
}

// mapErr turns a transport failure into the manager's reason (crash, stop, delete) when it gives one in time.
func (g *Guest) mapErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	t := time.NewTimer(crashGrace)
	defer t.Stop()
	select {
	case <-g.aborted:
		return g.abortErr
	case <-t.C:
		return fmt.Errorf("sandbox: guest channel: %w", err)
	}
}

func (g *Guest) call(ctx context.Context, method, path string, in, out any) error {
	select {
	case <-g.aborted:
		return g.abortErr
	default:
	}
	var body io.Reader = http.NoBody
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, guestBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(wire.TokenHeader, g.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	g.use(+1)
	defer g.use(-1)
	resp, err := g.cc.RoundTrip(req)
	if err != nil {
		return g.mapErr(ctx, err)
	}
	defer resp.Body.Close()
	if err := statusError(path, resp); err != nil {
		return err
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return g.mapErr(ctx, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("sandbox: guest %s: answer larger than %d bytes", path, maxResponseBytes)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("sandbox: guest %s: %w", path, err)
	}
	return nil
}

func statusError(path string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	text := strings.TrimSpace(string(msg))
	if resp.StatusCode == http.StatusNotImplemented {
		return fmt.Errorf("sandbox: guest %s: %w: %s", path, errors.ErrUnsupported, text)
	}
	return fmt.Errorf("sandbox: guest %s: %s: %s", path, resp.Status, text)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/sandbox/...`
Expected: `ok` for `internal/sandbox`, `internal/sandbox/guestagent`, `internal/sandbox/wire`.
If `RoundTrip` fails with a scheme error, check that the Transport has `AllowHTTP: true`; if the first request hangs on `net.Pipe`, confirm `ServeConn` runs in its own goroutine before `NewGuest` (the preface write is synchronous).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/sandbox/guestclient.go internal/sandbox/guestclient_test.go internal/sandbox/main_test.go internal/sandbox/guestagent/serve.go
git commit -m "feat(sandbox): h2c guest channel and host guest client" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Terminal and screen streams

**Files:**
- Create: `internal/sandbox/guestagent/pty.go`
- Create: `internal/sandbox/guestagent/pty_linux.go`
- Create: `internal/sandbox/guestagent/pty_other.go`
- Modify: `internal/sandbox/guestagent/agent.go` (Config, Agent struct, New)
- Create: `internal/sandbox/guestclient_stream.go`
- Test: `internal/sandbox/guestclient_stream_test.go`
- Test: `internal/sandbox/guestagent/pty_linux_test.go`
- Test: `internal/sandbox/guestagent/agent_test.go` (append resize test)

PTY library: `github.com/creack/pty` is already a direct dependency (`internal/terminals/backend_linux.go`), handles `Setsid`/`Setctty` and window size, so no new code enters `go.mod`. Raw `golang.org/x/sys/unix` would mean re-implementing `/dev/ptmx` + `TIOCSPTLCK`/`TIOCGPTN` + controlling-tty setup for no gain.

**Interfaces:**
- Consumes: Task 3/4 agent and client internals (`readJSON`, `statusError`, `mapErr`, `use`, `guestBase`), `terminals.Process` from `internal/terminals` (`Read`, `Write`, `Resize(cols, rows int) error`, `Wait(ctx) (int, error)`, `Pid() int`, `Close() error`).
- Produces:
  - `guestagent.PTYSpec{Argv []string; Dir string; Env []string; Cols, Rows int}`, `guestagent.PTY` interface (`io.ReadWriteCloser`, `Resize(cols, rows int) error`, `Wait() (int, error)`), new Config fields `LoginShell []string` (default `/bin/sh -l`) and `StartPTY func(PTYSpec) (PTY, error)` (nil = platform: Linux creack/pty, elsewhere 501).
  - Routes `POST /pty?cols=&rows=` (header `X-Orole-Pty`, trailer `X-Orole-Exit`) and `POST /pty/resize` (204, 404 unknown id, 400 bad size).
  - `(*Guest).OpenPTY(ctx, cols, rows int) (terminals.Process, error)` (`Pid()` is 0), `(*Guest).OpenScreen(ctx) (io.ReadWriteCloser, error)` (`errors.ErrUnsupported` from this phase's guest).

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/guestclient_stream_test.go`:

```go
package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

// echoPTY stands in for a shell: it echoes input as "echo:<input>" and exits with N on "exit N".
type echoPTY struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	mu   sync.Mutex
	cols int
	rows int
	once sync.Once
	code int
	done chan struct{}
}

func newEchoPTY(s guestagent.PTYSpec) *echoPTY {
	r, w := io.Pipe()
	return &echoPTY{r: r, w: w, cols: s.Cols, rows: s.Rows, done: make(chan struct{})}
}

func (e *echoPTY) Read(b []byte) (int, error) { return e.r.Read(b) }

func (e *echoPTY) Write(b []byte) (int, error) {
	s := string(b)
	if rest, ok := strings.CutPrefix(s, "exit "); ok {
		n, _ := strconv.Atoi(strings.TrimSpace(rest))
		e.finish(n)
		return len(b), nil
	}
	return e.w.Write([]byte("echo:" + s))
}

func (e *echoPTY) finish(code int) {
	e.once.Do(func() { e.code = code; e.w.Close(); close(e.done) })
}

func (e *echoPTY) Close() error { e.finish(-1); return nil }

func (e *echoPTY) Resize(cols, rows int) error {
	e.mu.Lock()
	e.cols, e.rows = cols, rows
	e.mu.Unlock()
	return nil
}

func (e *echoPTY) size() [2]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return [2]int{e.cols, e.rows}
}

func (e *echoPTY) Wait() (int, error) { <-e.done; return e.code, nil }

func startEchoAgent(t *testing.T) (*Guest, chan *echoPTY) {
	fakes := make(chan *echoPTY, 4)
	g, _ := startAgent(t, guestagent.Config{StartPTY: func(s guestagent.PTYSpec) (guestagent.PTY, error) {
		p := newEchoPTY(s)
		fakes <- p
		return p, nil
	}})
	return g, fakes
}

func TestGuestPTYRoundTrip(t *testing.T) {
	g, fakes := startEchoAgent(t)
	ctx := context.Background()
	p, err := g.OpenPTY(ctx, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fake := <-fakes
	if fake.size() != [2]int{100, 30} {
		t.Fatalf("started at %v, want 100x30", fake.size())
	}
	if _, err := io.WriteString(p, "hi"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "echo:hi")
	if err := p.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if fake.size() != [2]int{120, 40} {
		t.Fatalf("after resize %v, want 120x40", fake.size())
	}
	if _, err := io.WriteString(p, "exit 3"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, p)
	code, err := p.Wait(ctx)
	if err != nil || code != 3 {
		t.Fatalf("Wait = %d, %v; want 3", code, err)
	}
	if p.Pid() != 0 {
		t.Fatalf("Pid = %d, want 0 for a guest terminal", p.Pid())
	}
}

func TestGuestPTYCloseEndsGuestSession(t *testing.T) {
	g, fakes := startEchoAgent(t)
	p, err := g.OpenPTY(context.Background(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	fake := <-fakes
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.done:
	case <-time.After(5 * time.Second):
		t.Fatal("guest terminal still running after Close")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Wait(ctx); err != nil {
		t.Fatalf("Wait after Close = %v", err)
	}
}

func TestGuestPTYUnsupportedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the platform PTY exists on Linux")
	}
	g, _ := startAgent(t, guestagent.Config{})
	if _, err := g.OpenPTY(context.Background(), 80, 24); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("OpenPTY = %v, want ErrUnsupported", err)
	}
}

func TestGuestScreenNotAvailableYet(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	if _, err := g.OpenScreen(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("OpenScreen = %v, want ErrUnsupported", err)
	}
}

func TestGuestStreamIsFullDuplex(t *testing.T) {
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				_ = rc.Flush()
			}
			if err != nil {
				return
			}
		}
	}))
	s, err := g.OpenScreen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, msg := range []string{"abc", "def"} {
		if _, err := io.WriteString(s, msg); err != nil {
			t.Fatal(err)
		}
		readUntil(t, s, msg)
	}
}
```

Append to `internal/sandbox/guestagent/agent_test.go`:

```go
func TestPTYResizeErrors(t *testing.T) {
	a, _ := newAgent(t, nil)
	if rec := call(t, a, "POST", "/pty/resize", token, wire.ResizeRequest{ID: "nope", Cols: 80, Rows: 24}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown terminal = %d", rec.Code)
	}
	if rec := call(t, a, "POST", "/pty/resize", token, wire.ResizeRequest{ID: "nope", Cols: 0, Rows: 24}); rec.Code != http.StatusBadRequest {
		t.Errorf("zero columns = %d", rec.Code)
	}
}
```

`internal/sandbox/guestagent/pty_linux_test.go`:

```go
//go:build linux

package guestagent

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"
)

func readUntil(t *testing.T, r io.Reader, want string) {
	t.Helper()
	found := make(chan struct{})
	go func() {
		var got []byte
		buf := make([]byte, 1024)
		for {
			n, err := r.Read(buf)
			got = append(got, buf[:n]...)
			if bytes.Contains(got, []byte(want)) {
				close(found)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(5 * time.Second):
		t.Fatalf("never read %q", want)
	}
}

func TestStartPTYRunsShell(t *testing.T) {
	p, err := startPTY(PTYSpec{Argv: []string{"/bin/sh"}, Dir: t.TempDir(), Env: append(os.Environ(), "TERM=xterm-256color"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	io.WriteString(p, "stty size\n")
	readUntil(t, p, "24 80")
	if err := p.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	io.WriteString(p, "stty size; exit 3\n")
	readUntil(t, p, "30 100")
	go io.Copy(io.Discard, p)
	if code, _ := p.Wait(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/... -run 'PTY|Screen|Stream'`
Expected: FAIL, `unknown field StartPTY in struct literal`, `g.OpenPTY undefined`.

- [ ] **Step 3: Extend the agent config and routes**

In `internal/sandbox/guestagent/agent.go`:

Add `"sync"` to the imports.

Replace the `Config` struct's last field line

```go
	Logf     func(format string, args ...any) // nil discards
}
```

with

```go
	Logf     func(format string, args ...any) // nil discards

	LoginShell []string                   // argv for /pty; default /bin/sh -l
	StartPTY   func(PTYSpec) (PTY, error) // nil uses the platform terminal (Linux only; elsewhere /pty answers 501)
}
```

Replace

```go
type Agent struct {
	cfg Config
	mux *http.ServeMux
}
```

with

```go
type Agent struct {
	cfg Config
	mux *http.ServeMux

	ptyMu sync.Mutex
	ptys  map[string]PTY // open terminals by id, for /pty/resize
}
```

In `New`, replace

```go
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	a := &Agent{cfg: cfg, mux: http.NewServeMux()}
```

with

```go
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if len(cfg.LoginShell) == 0 {
		cfg.LoginShell = []string{"/bin/sh", "-l"}
	}
	if cfg.StartPTY == nil {
		cfg.StartPTY = startPTY
	}
	a := &Agent{cfg: cfg, mux: http.NewServeMux(), ptys: map[string]PTY{}}
	a.mux.HandleFunc("POST /pty", a.handlePTY)
	a.mux.HandleFunc("POST /pty/resize", a.handleResize)
```

- [ ] **Step 4: Write the PTY handler and platform terminals**

`internal/sandbox/guestagent/pty.go`:

```go
package guestagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// PTYSpec describes the terminal /pty starts.
type PTYSpec struct {
	Argv       []string
	Dir        string
	Env        []string
	Cols, Rows int
}

// PTY is a running terminal process inside the guest.
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() (exitCode int, err error)
}

var errPTYUnsupported = errors.New("terminals are not available on this guest OS")

// handlePTY runs a login shell on a pseudo-terminal: the request body carries keystrokes, the response body the
// terminal output, the trailer the exit code. Closing the stream from the host closes the terminal.
func (a *Agent) handlePTY(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := a.cfg.StartPTY(PTYSpec{
		Argv: a.cfg.LoginShell,
		Dir:  a.cfg.Home,
		Env:  append(os.Environ(), "HOME="+a.cfg.Home, "TERM=xterm-256color"),
		Cols: dimension(q.Get("cols"), 80),
		Rows: dimension(q.Get("rows"), 24),
	})
	if errors.Is(err, errPTYUnsupported) {
		http.Error(w, err.Error(), http.StatusNotImplemented)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := newID()
	a.ptyMu.Lock()
	a.ptys[id] = p
	a.ptyMu.Unlock()
	defer func() {
		a.ptyMu.Lock()
		delete(a.ptys, id)
		a.ptyMu.Unlock()
	}()
	stop := context.AfterFunc(r.Context(), func() { _ = p.Close() })
	defer stop()

	w.Header().Set("Trailer", wire.ExitTrailer)
	w.Header().Set(wire.PTYHeader, id)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()

	go func() { _, _ = io.Copy(p, r.Body) }()
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			_ = rc.Flush()
		}
		if err != nil {
			break
		}
	}
	_ = p.Close()
	code, _ := p.Wait()
	w.Header().Set(wire.ExitTrailer, strconv.Itoa(code))
}

func (a *Agent) handleResize(w http.ResponseWriter, r *http.Request) {
	var req wire.ResizeRequest
	if !readJSON(w, r, 4<<10, &req) {
		return
	}
	if req.Cols <= 0 || req.Rows <= 0 || req.Cols > 1000 || req.Rows > 1000 {
		http.Error(w, "bad terminal size", http.StatusBadRequest)
		return
	}
	a.ptyMu.Lock()
	p := a.ptys[req.ID]
	a.ptyMu.Unlock()
	if p == nil {
		http.Error(w, "no such terminal", http.StatusNotFound)
		return
	}
	if err := p.Resize(req.Cols, req.Rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dimension(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 1000 {
		return def
	}
	return n
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
```

`internal/sandbox/guestagent/pty_linux.go`:

```go
//go:build linux

package guestagent

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// startPTY starts argv on a new pseudo-terminal (creack/pty sets the session and controlling terminal).
func startPTY(s PTYSpec) (PTY, error) {
	if len(s.Argv) == 0 {
		return nil, errors.New("no shell configured")
	}
	cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(s.Cols), Rows: uint16(s.Rows)})
	if err != nil {
		return nil, err
	}
	p := &linuxPTY{f: f, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	return p, nil
}

type linuxPTY struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
	done chan struct{}
	code int
}

func (p *linuxPTY) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if errors.Is(err, syscall.EIO) { // Linux reports a terminal whose shell exited as EIO
		err = io.EOF
	}
	return n, err
}

func (p *linuxPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *linuxPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Close hangs up the terminal (the shell gets SIGHUP) and kills the shell if it is still there 2 s later.
func (p *linuxPTY) Close() error {
	p.once.Do(func() {
		_ = p.f.Close()
		go func() {
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				_ = p.cmd.Process.Kill()
			}
		}()
	})
	return nil
}

func (p *linuxPTY) Wait() (int, error) {
	<-p.done
	return p.code, nil
}
```

`internal/sandbox/guestagent/pty_other.go`:

```go
//go:build !linux

package guestagent

// startPTY: the guest is Linux; host builds (Windows tests) answer /pty with 501 unless a test injects StartPTY.
func startPTY(PTYSpec) (PTY, error) { return nil, errPTYUnsupported }
```

- [ ] **Step 5: Write the client streams**

`internal/sandbox/guestclient_stream.go`:

```go
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

// stream is one full-duplex HTTP/2 request: writes go to the guest, reads come from it.
type stream struct {
	g         *Guest
	w         *io.PipeWriter
	resp      *http.Response
	cancel    context.CancelFunc
	closeOnce sync.Once
}

func (s *stream) Read(p []byte) (int, error)  { return s.resp.Body.Read(p) }
func (s *stream) Write(p []byte) (int, error) { return s.w.Write(p) }

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.w.Close()
		s.cancel()
		_ = s.resp.Body.Close()
		s.g.use(-1)
	})
	return nil
}

// openStream starts a full-duplex request. ctx bounds only the opening; the stream lives until Close.
func (g *Guest) openStream(ctx context.Context, path string) (*stream, error) {
	select {
	case <-g.aborted:
		return nil, g.abortErr
	default:
	}
	pr, pw := io.Pipe()
	sctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, guestBase+path, pr)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set(wire.TokenHeader, g.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := g.cc.RoundTrip(req)
		ch <- result{resp, err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
		cancel()
		_ = pw.Close()
		if late := <-ch; late.resp != nil {
			_ = late.resp.Body.Close()
		}
		return nil, ctx.Err()
	}
	if r.err != nil {
		cancel()
		_ = pw.Close()
		return nil, g.mapErr(ctx, r.err)
	}
	if err := statusError(path, r.resp); err != nil {
		_ = r.resp.Body.Close()
		cancel()
		_ = pw.Close()
		return nil, err
	}
	g.use(+1)
	return &stream{g: g, w: pw, resp: r.resp, cancel: cancel}, nil
}

// OpenScreen relays raw RFB bytes to the guest's VNC server (the guest side arrives in phase 4).
func (g *Guest) OpenScreen(ctx context.Context) (io.ReadWriteCloser, error) {
	return g.openStream(ctx, "/screen")
}

var errPTYClosed = errors.New("sandbox: terminal closed")

// OpenPTY starts a login shell in the guest. The result plugs into the daemon's terminal manager; Pid is 0.
func (g *Guest) OpenPTY(ctx context.Context, cols, rows int) (terminals.Process, error) {
	s, err := g.openStream(ctx, fmt.Sprintf("/pty?cols=%d&rows=%d", cols, rows))
	if err != nil {
		return nil, err
	}
	id := s.resp.Header.Get(wire.PTYHeader)
	if id == "" {
		_ = s.Close()
		return nil, errors.New("sandbox: guest /pty: no terminal id")
	}
	return &guestPTY{stream: s, id: id, done: make(chan struct{})}, nil
}

type guestPTY struct {
	*stream
	id       string
	doneOnce sync.Once
	done     chan struct{}
	code     int
	err      error
}

func (p *guestPTY) Read(b []byte) (int, error) {
	n, err := p.stream.Read(b)
	if err != nil {
		p.finish(err)
	}
	return n, err
}

func (p *guestPTY) Close() error {
	err := p.stream.Close()
	p.finish(errPTYClosed)
	return err
}

func (p *guestPTY) finish(err error) {
	p.doneOnce.Do(func() {
		p.code = -1
		switch {
		case errors.Is(err, io.EOF):
			if c, convErr := strconv.Atoi(p.resp.Trailer.Get(wire.ExitTrailer)); convErr == nil {
				p.code = c
			} else {
				p.err = errors.New("sandbox: terminal ended without an exit code")
			}
		case errors.Is(err, errPTYClosed):
		default:
			p.err = p.g.mapErr(context.Background(), err)
		}
		close(p.done)
	})
}

func (p *guestPTY) Resize(cols, rows int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.g.call(ctx, http.MethodPost, "/pty/resize", wire.ResizeRequest{ID: p.id, Cols: cols, Rows: rows}, nil)
}

func (p *guestPTY) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		return p.code, p.err
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (p *guestPTY) Pid() int { return 0 }
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/...`
Expected: `ok` for all three packages.
Run: `GOOS=linux GOARCH=amd64 go vet ./internal/sandbox/...`
Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add internal/sandbox
git commit -m "feat(sandbox): guest terminal and screen streams" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: `cmd/orole-guest`

**Files:**
- Create: `cmd/orole-guest/main.go`
- Create: `cmd/orole-guest/conn.go`
- Create: `cmd/orole-guest/port_linux.go`
- Create: `cmd/orole-guest/port_other.go`
- Test: `cmd/orole-guest/main_test.go`

**Interfaces:**
- Consumes: `guestagent.New`, `Config`, `ServeLoop` (Tasks 3-5).
- Produces: binary `orole-guest` (linux/amd64, `CGO_ENABLED=0`), version via `-ldflags "-X main.version=<v>"` (phase 2 bakes it into the image). Reads `orole.token=` from `/proc/cmdline`; serves `/dev/virtio-ports/org.orole.guest.0`; poweroff via `systemctl poweroff`, falling back to `reboot(2)`.

- [ ] **Step 1: Write the failing test**

`cmd/orole-guest/main_test.go`:

```go
package main

import "testing"

func TestTokenFromCmdline(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"BOOT_IMAGE=/vmlinuz console=ttyS0 orole.token=abc123 quiet", "abc123"},
		{"orole.token=first orole.token=second", "first"},
		{"quiet splash", ""},
		{"orole.token=", ""},
		{"orole.tokens=x", ""},
		{"  orole.token=t1\n", "t1"},
	} {
		if got := tokenFromCmdline(tc.in); got != tc.want {
			t.Errorf("tokenFromCmdline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/orole-guest/`
Expected: FAIL, `undefined: tokenFromCmdline`.

- [ ] **Step 3: Write the implementation**

`cmd/orole-guest/main.go`:

```go
// Command orole-guest is the agent inside a bot's VM. It serves the daemon's requests (exec, files, terminal)
// over HTTP/2 on the virtio-serial port org.orole.guest.0, authenticated with the per-boot token from the kernel
// command line. It only works inside the Linux guest image; the handler is internal/sandbox/guestagent.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	logger := log.New(os.Stderr, "orole-guest: ", log.LstdFlags)
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		logger.Fatalf("read kernel command line: %v", err)
	}
	token := tokenFromCmdline(string(cmdline))
	if token == "" {
		logger.Fatal("no orole.token= on the kernel command line")
	}
	agent := guestagent.New(guestagent.Config{Token: token, Version: version, PowerOff: powerOff, Logf: logger.Printf})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.ServeLoop(ctx, openPort, time.Second); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal(err)
	}
}

// tokenFromCmdline returns the first orole.token= value of a kernel command line, or "".
func tokenFromCmdline(cmdline string) string {
	for _, field := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(field, "orole.token="); ok {
			return v
		}
	}
	return ""
}
```

`cmd/orole-guest/conn.go`:

```go
package main

import (
	"net"
	"os"
)

// fileConn adapts the virtio-serial character device to net.Conn for http2.Server.ServeConn.
type fileConn struct{ *os.File }

var _ net.Conn = (*fileConn)(nil)

func (c *fileConn) LocalAddr() net.Addr  { return portAddr{} }
func (c *fileConn) RemoteAddr() net.Addr { return portAddr{} }

type portAddr struct{}

func (portAddr) Network() string { return "virtio-serial" }
func (portAddr) String() string  { return "org.orole.guest.0" }
```

`cmd/orole-guest/port_linux.go`:

```go
//go:build linux

package main

import (
	"net"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

const portPath = "/dev/virtio-ports/org.orole.guest.0"

func openPort() (net.Conn, error) {
	f, err := os.OpenFile(portPath, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &fileConn{File: f}, nil
}

// powerOff asks systemd for a clean shutdown and falls back to the reboot syscall.
func powerOff() error {
	if err := exec.Command("systemctl", "poweroff").Run(); err == nil {
		return nil
	}
	unix.Sync()
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
```

`cmd/orole-guest/port_other.go`:

```go
//go:build !linux

package main

import (
	"errors"
	"net"
)

var errNotGuest = errors.New("orole-guest runs only inside the Linux guest")

func openPort() (net.Conn, error) { return nil, errNotGuest }

func powerOff() error { return errNotGuest }
```

- [ ] **Step 4: Run the tests and the Linux build**

Run: `go test -race ./cmd/orole-guest/`
Expected: `ok  github.com/rayorole/orole-os-desktop/cmd/orole-guest`.
Run: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o "$(mktemp -d)/orole-guest" ./cmd/orole-guest && GOOS=linux GOARCH=amd64 go vet ./cmd/orole-guest/`
Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add cmd/orole-guest
git commit -m "feat(sandbox): orole-guest agent binary for the linux guest" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: Manager: on-demand boot, cap and shutdown

**Files:**
- Create: `internal/sandbox/manager.go`
- Create: `internal/sandbox/fake_test.go`
- Test: `internal/sandbox/manager_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1-5 (`Store`, `Guest` with `newGuestCtx`/`setOnUse`/`abort`/`PowerOff`/`Health`, `guestagent.New`/`ServeConn`, `agenttest.Shell`, `testDB`, `eventually`, `scratchHome`).
- Produces (master contract): `Config`, `ImageSet`, `NewManager`, `(*Manager).Availability`, `Ensure`, `Status`, `Subscribe`, `Shutdown`. Unexported for Tasks 8-9: `instance` (fields `botID, state, vm, guest, ready, gone, bootErr, lastUsed, active, takenOver`), `m.mu`, `m.vms`, `m.last`, `m.viewers`, `m.deleting`, `m.closed`, `m.stop`, `statusLocked`, `signalLocked`, `idleLocked`, `beginStopLocked`, `finishStop(inst, reason error)`, `publish`. Test harness: `newHarness(t, tweak func(*Config)) *harness` with `h.m, h.b, h.store, h.paths, h.cfg`, `h.clock()`, `h.advance(d)`, `h.setFree(bytes)`, `h.active(botID) int`, `ensure(t, h, botID) *Guest`; fake: `fakeBackend` with `update(fn)`, `bootCount()`, `spec(i)`, `vm(i)`, `vmFor(botID)`, `liveCount()`, `killAll()`; `fakeVM.crash()`, `fakeVM.kills`.

- [ ] **Step 1: Write the fake backend**

`internal/sandbox/fake_test.go`:

```go
package sandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
)

// fakeBackend "boots" a VM by serving the real guest handler on one end of a net.Pipe.
type fakeBackend struct {
	t        *testing.T
	mu       sync.Mutex
	avail    Availability
	bootErr  error
	notReady bool          // the guest's /health never reports ready
	hold     chan struct{} // non-nil: Boot waits until it is closed
	specs    []BootSpec
	vms      []*fakeVM
}

func newFakeBackend(t *testing.T) *fakeBackend {
	return &fakeBackend{t: t, avail: Availability{Ready: true}}
}

func (b *fakeBackend) update(fn func(*fakeBackend)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(b)
}

func (b *fakeBackend) Availability(context.Context) Availability {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.avail
}

func (b *fakeBackend) Boot(ctx context.Context, spec BootSpec) (VM, error) {
	b.mu.Lock()
	b.specs = append(b.specs, spec)
	hold, bootErr, notReady := b.hold, b.bootErr, b.notReady
	b.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if bootErr != nil {
		return nil, bootErr
	}
	if err := os.WriteFile(spec.Overlay, []byte("qcow2"), 0o600); err != nil { // stands in for qemu-img create
		return nil, err
	}
	host, guest := net.Pipe()
	vm := &fakeVM{spec: spec, conn: host, guestEnd: guest, done: make(chan struct{})}
	agent := guestagent.New(guestagent.Config{
		Token: spec.Token, Version: "test", Home: scratchHome(b.t), Shell: agenttest.Shell(),
		Ready:    func() bool { return !notReady },
		PowerOff: func() error { go vm.exit(nil); return nil },
	})
	go agent.ServeConn(context.Background(), guest)
	b.mu.Lock()
	b.vms = append(b.vms, vm)
	b.mu.Unlock()
	return vm, nil
}

func (b *fakeBackend) bootCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.specs)
}

func (b *fakeBackend) spec(i int) BootSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.specs[i]
}

func (b *fakeBackend) vm(i int) *fakeVM {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.vms[i]
}

// vmFor returns the newest VM booted for botID, or nil.
func (b *fakeBackend) vmFor(botID string) *fakeVM {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.vms) - 1; i >= 0; i-- {
		if b.vms[i].spec.BotID == botID {
			return b.vms[i]
		}
	}
	return nil
}

func (b *fakeBackend) liveCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, vm := range b.vms {
		select {
		case <-vm.done:
		default:
			n++
		}
	}
	return n
}

func (b *fakeBackend) killAll() {
	b.mu.Lock()
	vms := append([]*fakeVM(nil), b.vms...)
	b.mu.Unlock()
	for _, vm := range vms {
		vm.exit(errors.New("test cleanup"))
	}
}

type fakeVM struct {
	spec      BootSpec
	conn      net.Conn
	guestEnd  net.Conn
	once      sync.Once
	done      chan struct{}
	mu        sync.Mutex
	exitErr   error
	shutdowns atomic.Int32
	kills     atomic.Int32
}

func (v *fakeVM) Conn() net.Conn { return v.conn }

func (v *fakeVM) Shutdown(context.Context) error {
	v.shutdowns.Add(1)
	v.exit(nil)
	return nil
}

func (v *fakeVM) Kill() error {
	v.kills.Add(1)
	v.exit(errors.New("killed"))
	return nil
}

func (v *fakeVM) Done() <-chan struct{} { return v.done }

func (v *fakeVM) ExitErr() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exitErr
}

// crash makes the "QEMU process" die on its own.
func (v *fakeVM) crash() { v.exit(errors.New("exit status 0xc0000005")) }

func (v *fakeVM) exit(err error) {
	v.once.Do(func() {
		v.mu.Lock()
		v.exitErr = err
		v.mu.Unlock()
		_ = v.conn.Close()
		_ = v.guestEnd.Close()
		close(v.done)
	})
}
```

- [ ] **Step 2: Write the failing tests**

`internal/sandbox/manager_test.go`:

```go
package sandbox

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

type harness struct {
	m     *Manager
	b     *fakeBackend
	store *Store
	paths Paths
	cfg   Config

	mu   sync.Mutex
	now  time.Time
	free uint64
}

func newHarness(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	data := t.TempDir()
	h := &harness{
		b:     newFakeBackend(t),
		store: NewStore(testDB(t)),
		paths: Paths{Root: filepath.Join(data, "sandbox")},
		now:   time.Unix(1_800_000_000, 0),
		free:  64 << 30,
	}
	h.cfg = Config{
		Paths: h.paths, MaxRunning: 4, IdleTimeout: 15 * time.Minute, BootTimeout: 5 * time.Second, BusyWait: 200 * time.Millisecond,
		FreeMemory: func() (uint64, error) { h.mu.Lock(); defer h.mu.Unlock(); return h.free, nil },
		Now:        h.clock,
		Image: func() (ImageSet, error) {
			return ImageSet{Version: "v1", Kernel: "k", Initrd: "i", BaseImage: "base.qcow2"}, nil
		},
	}
	if tweak != nil {
		tweak(&h.cfg)
	}
	h.m = NewManager(h.cfg, h.b, h.store)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.m.Shutdown(ctx)
		h.b.killAll()
	})
	return h
}

func (h *harness) clock() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }

func (h *harness) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }

func (h *harness) setFree(n uint64) { h.mu.Lock(); h.free = n; h.mu.Unlock() }

// active is the number of guest requests and streams in flight for botID, or -1 without a VM.
func (h *harness) active(botID string) int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if inst := h.m.vms[botID]; inst != nil {
		return inst.active
	}
	return -1
}

func ensure(t *testing.T, h *harness, botID string) *Guest {
	t.Helper()
	g, err := h.m.Ensure(context.Background(), botID)
	if err != nil {
		t.Fatalf("Ensure(%s): %v", botID, err)
	}
	return g
}

func TestEnsureBootsOnceForConcurrentCallers(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.hold = make(chan struct{}) })
	const callers = 8
	guests := make([]*Guest, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() { defer wg.Done(); guests[i], errs[i] = h.m.Ensure(context.Background(), "bot1") }()
	}
	eventually(t, func() bool { return h.b.bootCount() == 1 })
	if st := h.m.Status("bot1"); st.State != StateStarting {
		t.Fatalf("status while booting = %+v", st)
	}
	h.b.update(func(b *fakeBackend) { close(b.hold) })
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if guests[i] != guests[0] {
			t.Fatalf("caller %d got a different guest", i)
		}
	}
	if n := h.b.bootCount(); n != 1 {
		t.Fatalf("boots = %d, want 1", n)
	}
	if st := h.m.Status("bot1"); st.State != StateRunning {
		t.Fatalf("status = %+v, want running", st)
	}
}

func TestEnsureRecordsSandboxAndBootSpec(t *testing.T) {
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	if res, err := g.Exec(context.Background(), wire.ExecRequest{Command: "echo hi"}); err != nil || res.Stdout != "hi\n" {
		t.Fatalf("Exec through the manager = %+v, %v", res, err)
	}
	rec, ok, err := h.store.Get(context.Background(), "bot1")
	if err != nil || !ok {
		t.Fatalf("record: %v, %v", ok, err)
	}
	if rec.ImageVersion != "v1" || rec.OverlayPath != h.paths.Overlay("bot1") || rec.Limits != DefaultLimits() || !rec.CreatedAt.Equal(h.clock()) {
		t.Fatalf("record = %+v", rec)
	}
	spec := h.b.spec(0)
	if spec.Overlay != h.paths.Overlay("bot1") || spec.Kernel != "k" || spec.Initrd != "i" || spec.BaseImage != "base.qcow2" ||
		spec.Limits != DefaultLimits() || spec.LogPath != h.paths.Log(filepath.Dir(h.paths.Root), "bot1") {
		t.Fatalf("spec = %+v", spec)
	}
	if b, err := hex.DecodeString(spec.Token); err != nil || len(b) != 32 {
		t.Fatalf("token %q is not 32 bytes of hex", spec.Token)
	}
}

func TestEnsureRefusesWhenNotReady(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.avail = Availability{Reason: "whp-disabled"} })
	_, err := h.m.Ensure(context.Background(), "bot1")
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Ensure = %v, want ErrNotReady", err)
	}
	if st := h.m.Status("bot1"); st.State != StateFailed || !strings.Contains(st.Error, "whp-disabled") {
		t.Fatalf("status = %+v", st)
	}
	if h.b.bootCount() != 0 {
		t.Fatal("booted although not ready")
	}
}

func TestEnsureRefusesWithoutRuntime(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Image = func() (ImageSet, error) { return ImageSet{}, errors.New("no runtime installed") } })
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Ensure = %v, want ErrNotReady", err)
	}
}

func TestEnsureRefusesOnLowMemory(t *testing.T) {
	h := newHarness(t, nil)
	h.setFree((2048+1024)<<20 - 1)
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrLowMemory) {
		t.Fatalf("Ensure = %v, want ErrLowMemory", err)
	}
	if h.b.bootCount() != 0 {
		t.Fatal("booted with too little memory")
	}
	h.setFree((2048 + 1024) << 20)
	ensure(t, h, "bot1")
}

func TestBootFailsWhenGuestNeverReady(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BootTimeout = 300 * time.Millisecond })
	h.b.update(func(b *fakeBackend) { b.notReady = true })
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrBootFailed) {
		t.Fatalf("Ensure = %v, want ErrBootFailed", err)
	}
	vm := h.b.vm(0)
	select {
	case <-vm.Done():
	default:
		t.Fatal("VM left running after a boot timeout")
	}
	if vm.kills.Load() == 0 {
		t.Fatal("VM was not killed")
	}
	log, err := os.ReadFile(h.paths.Log(filepath.Dir(h.paths.Root), "bot1"))
	if err != nil || !strings.Contains(string(log), "failed to start") {
		t.Fatalf("log = %q, %v", log, err)
	}
	if strings.Contains(string(log), h.b.spec(0).Token) {
		t.Fatal("the guest token reached the log")
	}
	if st := h.m.Status("bot1"); st.State != StateFailed {
		t.Fatalf("status = %+v", st)
	}
	if n := h.b.bootCount(); n != 1 {
		t.Fatalf("boots = %d: a failed boot must not retry by itself", n)
	}
	h.b.update(func(b *fakeBackend) { b.notReady = false })
	ensure(t, h, "bot1")
	if n := h.b.bootCount(); n != 2 {
		t.Fatalf("boots = %d, want 2", n)
	}
}

func TestBootFailsWhenBackendFails(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.bootErr = errors.New("whpx: injection failed") })
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrBootFailed) {
		t.Fatalf("Ensure = %v, want ErrBootFailed", err)
	}
	log, _ := os.ReadFile(h.paths.Log(filepath.Dir(h.paths.Root), "bot1"))
	if !strings.Contains(string(log), "injection failed") {
		t.Fatalf("log = %q", log)
	}
}

func TestEnsureRejectsInvalidBotID(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []string{"", "../evil", `a\b`} {
		if _, err := h.m.Ensure(context.Background(), id); err == nil {
			t.Errorf("Ensure(%q) = nil error", id)
		}
	}
	if h.b.bootCount() != 0 {
		t.Fatal("booted for an invalid id")
	}
}

func TestStatusEvents(t *testing.T) {
	h := newHarness(t, nil)
	var mu sync.Mutex
	var seen []State
	unsubscribe := h.m.Subscribe(func(s Status) {
		if s.BotID == "bot1" {
			mu.Lock()
			seen = append(seen, s.State)
			mu.Unlock()
		}
	})
	ensure(t, h, "bot1")
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 2 })
	mu.Lock()
	if seen[0] != StateStarting || seen[1] != StateRunning {
		t.Fatalf("events = %v, want [starting running]", seen)
	}
	mu.Unlock()
	unsubscribe()
	h.b.vmFor("bot1").crash()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateOff })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("events after unsubscribe: %v", seen)
	}
}

func TestCrashFailsInFlightCallAndNextCallBoots(t *testing.T) {
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	errc := make(chan error, 1)
	go func() {
		_, err := g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 10s"})
		errc <- err
	}()
	eventually(t, func() bool { return h.active("bot1") == 1 })
	h.b.vmFor("bot1").crash()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrCrashed) {
			t.Fatalf("in-flight Exec = %v, want ErrCrashed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Exec never returned")
	}
	if st := h.m.Status("bot1"); st.State != StateOff || st.Error != ErrCrashed.Error() {
		t.Fatalf("status = %+v", st)
	}
	g2 := ensure(t, h, "bot1")
	if g2 == g || h.b.bootCount() != 2 {
		t.Fatalf("after a crash: new guest=%v boots=%d", g2 != g, h.b.bootCount())
	}
	if h.b.spec(0).Token == h.b.spec(1).Token {
		t.Fatal("the token was reused across boots")
	}
	if st := h.m.Status("bot1"); st.State != StateRunning || st.Error != "" {
		t.Fatalf("status after reboot = %+v", st)
	}
}

func TestCapEvictsLeastRecentlyUsedIdleVM(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 2 })
	ensure(t, h, "a")
	h.advance(time.Minute)
	ensure(t, h, "b")
	h.advance(time.Minute)
	ensure(t, h, "a") // a is now more recent than b
	h.advance(time.Minute)
	ensure(t, h, "c")
	if st := h.m.Status("b"); st.State != StateOff {
		t.Fatalf("b = %+v, want off (evicted)", st)
	}
	for _, id := range []string{"a", "c"} {
		if st := h.m.Status(id); st.State != StateRunning {
			t.Fatalf("%s = %+v, want running", id, st)
		}
	}
	select {
	case <-h.b.vmFor("b").Done():
	default:
		t.Fatal("b's VM still running")
	}
	if n := h.b.bootCount(); n != 3 {
		t.Fatalf("boots = %d, want 3", n)
	}
}

func TestCapReturnsBusyWhenEveryVMIsBusy(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 1; c.BusyWait = 200 * time.Millisecond })
	g := ensure(t, h, "a")
	go func() { _, _ = g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 3s"}) }()
	eventually(t, func() bool { return h.active("a") == 1 })
	start := time.Now()
	if _, err := h.m.Ensure(context.Background(), "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Ensure(b) = %v, want ErrBusy", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("gave up after %v, want the 200ms busy wait", d)
	}
	if st := h.m.Status("a"); st.State != StateRunning || h.b.bootCount() != 1 {
		t.Fatalf("a = %+v, boots = %d", st, h.b.bootCount())
	}
}

func TestCapWaitGetsSlotWhenBusyVMFinishes(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 1; c.BusyWait = 10 * time.Second })
	g := ensure(t, h, "a")
	done := make(chan error, 1)
	go func() {
		_, err := g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 300ms"})
		done <- err
	}()
	eventually(t, func() bool { return h.active("a") == 1 })
	ensure(t, h, "b")
	if err := <-done; err != nil {
		t.Fatalf("a's command was cut off: %v", err)
	}
	if h.m.Status("a").State != StateOff || h.m.Status("b").State != StateRunning {
		t.Fatalf("a = %+v, b = %+v", h.m.Status("a"), h.m.Status("b"))
	}
}

func TestShutdownStopsEveryVM(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []string{"a", "b", "c"} {
		ensure(t, h, id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.b.liveCount(); n != 0 {
		t.Fatalf("%d VMs still running after Shutdown", n)
	}
	if _, err := h.m.Ensure(context.Background(), "a"); !errors.Is(err, errClosed) {
		t.Fatalf("Ensure after Shutdown = %v, want errClosed", err)
	}
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown = %v", err)
	}
}

func TestShutdownDuringBoot(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.hold = make(chan struct{}) })
	ensured := make(chan error, 1)
	go func() { _, err := h.m.Ensure(context.Background(), "bot1"); ensured <- err }()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateStarting })
	shut := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shut <- h.m.Shutdown(ctx)
	}()
	eventually(t, func() bool { h.m.mu.Lock(); defer h.m.mu.Unlock(); return h.m.closed })
	h.b.update(func(b *fakeBackend) { close(b.hold) })
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if err := <-ensured; !errors.Is(err, errClosed) {
		t.Fatalf("Ensure = %v, want errClosed", err)
	}
	if n := h.b.liveCount(); n != 0 {
		t.Fatalf("%d VMs left after Shutdown during boot", n)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'TestEnsure|TestBoot|TestStatus|TestCrash|TestCap|TestShutdown'`
Expected: FAIL, `undefined: Config`, `undefined: NewManager`.

- [ ] **Step 4: Write the manager**

`internal/sandbox/manager.go`:

```go
package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Config struct {
	Paths       Paths
	MaxRunning  int                      // 4
	IdleTimeout time.Duration            // 15m
	BootTimeout time.Duration            // 60s
	BusyWait    time.Duration            // 30s
	FreeMemory  func() (uint64, error)   // bytes; injectable for tests; nil skips the check
	Now         func() time.Time
	Image       func() (ImageSet, error) // resolves kernel/initrd/base from the installed runtime (phase 2); tests stub it
}

type ImageSet struct{ Version, Kernel, Initrd, BaseImage string }

const (
	defaultMaxRunning  = 4
	defaultIdleTimeout = 15 * time.Minute
	defaultBootTimeout = 60 * time.Second
	defaultBusyWait    = 30 * time.Second
	memoryHeadroomMB   = 1024 // refuse to boot when free RAM < VM RAM + this
	stopTimeout        = 30 * time.Second
	powerOffTimeout    = 5 * time.Second
	healthPoll         = 100 * time.Millisecond
)

// instance is one VM's lifecycle. Fields are guarded by Manager.mu, except ready/gone which are closed once.
type instance struct {
	botID     string
	state     State
	vm        VM
	guest     *Guest
	ready     chan struct{} // closed when the boot ends, either way
	gone      chan struct{} // closed when the VM has fully exited (or never started)
	bootErr   error         // set before ready closes
	lastUsed  time.Time
	active    int // guest requests and streams in flight
	takenOver bool
}

// Manager owns every bot's VM: it boots one on demand (once, however many callers wait), keeps at most
// MaxRunning, stops idle ones and notices crashes. All state is in memory; the store keeps the overlay record.
type Manager struct {
	cfg     Config
	backend Backend
	store   *Store

	mu       sync.Mutex
	vms      map[string]*instance
	last     map[string]Status // final status of bots without an instance (failed boot, crash)
	viewers  map[string]bool
	deleting map[string]bool
	changed  chan struct{} // closed and replaced whenever a slot may have been freed
	closed   bool
	stop     chan struct{} // closed by Shutdown
	boots    sync.WaitGroup

	subMu  sync.Mutex
	subs   map[int]func(Status)
	nextID int
}

func NewManager(cfg Config, backend Backend, store *Store) *Manager {
	if cfg.MaxRunning <= 0 {
		cfg.MaxRunning = defaultMaxRunning
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	if cfg.BootTimeout <= 0 {
		cfg.BootTimeout = defaultBootTimeout
	}
	if cfg.BusyWait <= 0 {
		cfg.BusyWait = defaultBusyWait
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	m := &Manager{
		cfg: cfg, backend: backend, store: store,
		vms: map[string]*instance{}, last: map[string]Status{}, viewers: map[string]bool{}, deleting: map[string]bool{},
		changed: make(chan struct{}), stop: make(chan struct{}), subs: map[int]func(Status){},
	}
	return m
}

func (m *Manager) Availability(ctx context.Context) Availability { return m.backend.Availability(ctx) }

func (m *Manager) Status(botID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(botID)
}

func (m *Manager) statusLocked(botID string) Status {
	if inst := m.vms[botID]; inst != nil {
		return Status{BotID: botID, State: inst.state, TakenOver: inst.takenOver}
	}
	if st, ok := m.last[botID]; ok {
		return st
	}
	return Status{BotID: botID, State: StateOff}
}

// Subscribe calls fn on every status change, from the goroutine that made it; fn must not block.
func (m *Manager) Subscribe(fn func(Status)) (unsubscribe func()) {
	m.subMu.Lock()
	id := m.nextID
	m.nextID++
	m.subs[id] = fn
	m.subMu.Unlock()
	return func() {
		m.subMu.Lock()
		delete(m.subs, id)
		m.subMu.Unlock()
	}
}

func (m *Manager) publish(st Status) {
	m.subMu.Lock()
	fns := make([]func(Status), 0, len(m.subs))
	for _, fn := range m.subs {
		fns = append(fns, fn)
	}
	m.subMu.Unlock()
	for _, fn := range fns {
		fn(st)
	}
}

func (m *Manager) signalLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// Ensure returns the bot's running guest, booting the VM if needed. Concurrent callers share one boot. When
// MaxRunning VMs run it stops the least recently used idle one, or waits up to BusyWait for one to become idle.
func (m *Manager) Ensure(ctx context.Context, botID string) (*Guest, error) {
	if err := checkBotID(botID); err != nil {
		return nil, err
	}
	busyUntil := time.Now().Add(m.cfg.BusyWait)
	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, errClosed
		}
		if m.deleting[botID] {
			m.mu.Unlock()
			return nil, errDeleting
		}
		if inst := m.vms[botID]; inst != nil {
			switch inst.state {
			case StateRunning:
				inst.lastUsed = m.cfg.Now()
				g := inst.guest
				m.mu.Unlock()
				return g, nil
			case StateStarting:
				ready := inst.ready
				m.mu.Unlock()
				select {
				case <-ready:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if inst.bootErr != nil {
					return nil, inst.bootErr
				}
				return inst.guest, nil
			default: // stopping: the overlay is still open, wait until the old VM is gone
				gone := inst.gone
				m.mu.Unlock()
				select {
				case <-gone:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}
		}
		victim, ok := m.reserveLocked()
		if !ok {
			changed := m.changed
			m.mu.Unlock()
			if err := waitSlot(ctx, changed, busyUntil); err != nil {
				return nil, err
			}
			continue
		}
		inst := &instance{botID: botID, state: StateStarting, ready: make(chan struct{}), gone: make(chan struct{}), lastUsed: m.cfg.Now()}
		m.vms[botID] = inst
		delete(m.last, botID)
		var after <-chan struct{}
		var evicted Status
		if victim != nil {
			m.beginStopLocked(victim)
			after = victim.gone
			evicted = m.statusLocked(victim.botID)
		}
		starting := m.statusLocked(botID)
		m.boots.Add(1)
		m.mu.Unlock()
		if victim != nil {
			m.publish(evicted)
			go m.finishStop(victim, errStopped)
		}
		m.publish(starting)
		go m.boot(inst, after)
	}
}

func waitSlot(ctx context.Context, changed <-chan struct{}, until time.Time) error {
	wait := time.Until(until)
	if wait <= 0 {
		return ErrBusy
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-changed:
		return nil
	case <-t.C:
		return ErrBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}

// reserveLocked reports whether a VM may start now. At the cap it picks the least recently used idle VM to stop
// first; ok is false when every running VM is busy.
func (m *Manager) reserveLocked() (victim *instance, ok bool) {
	n := 0
	for _, inst := range m.vms {
		if inst.state == StateStarting || inst.state == StateRunning {
			n++
		}
	}
	if n < m.cfg.MaxRunning {
		return nil, true
	}
	for _, inst := range m.vms {
		if m.idleLocked(inst) && (victim == nil || inst.lastUsed.Before(victim.lastUsed)) {
			victim = inst
		}
	}
	return victim, victim != nil
}

// idleLocked: running with no request or stream in flight, no viewer and no takeover.
func (m *Manager) idleLocked(inst *instance) bool {
	return inst.state == StateRunning && inst.active == 0 && !inst.takenOver && !m.viewers[inst.botID]
}

func (m *Manager) boot(inst *instance, after <-chan struct{}) {
	defer m.boots.Done()
	if after != nil {
		<-after
	}
	ctx, cancel := context.WithTimeout(context.Background(), m.cfg.BootTimeout)
	defer cancel()
	vm, g, err := m.start(ctx, inst.botID)

	m.mu.Lock()
	if err == nil && m.closed {
		err = errClosed
		_ = g.Close()
		_ = vm.Kill()
	} else if err != nil {
		vm = nil // start already stopped it
	}
	if err != nil {
		delete(m.vms, inst.botID)
		inst.state = StateFailed
		inst.bootErr = err
		st := Status{BotID: inst.botID, State: StateFailed, Error: err.Error()}
		m.last[inst.botID] = st
		close(inst.ready)
		close(inst.gone)
		m.signalLocked()
		m.mu.Unlock()
		if vm != nil {
			<-vm.Done()
		}
		m.publish(st)
		return
	}
	inst.vm, inst.guest = vm, g
	inst.state = StateRunning
	inst.lastUsed = m.cfg.Now()
	g.setOnUse(func(delta int) { m.use(inst, delta) })
	close(inst.ready)
	m.signalLocked()
	st := m.statusLocked(inst.botID)
	m.mu.Unlock()
	go m.watch(inst)
	m.publish(st)
}

// start checks readiness and memory, records the sandbox, boots the VM and waits for the guest to be ready.
// On error nothing is left running.
func (m *Manager) start(ctx context.Context, botID string) (VM, *Guest, error) {
	if av := m.backend.Availability(ctx); !av.Ready {
		return nil, nil, fmt.Errorf("%w (%s)", ErrNotReady, av.Reason)
	}
	if m.cfg.Image == nil {
		return nil, nil, fmt.Errorf("%w (runtime-missing)", ErrNotReady)
	}
	img, err := m.cfg.Image()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrNotReady, err)
	}
	rec, found, err := m.store.Get(ctx, botID)
	if err != nil {
		return nil, nil, err
	}
	now := m.cfg.Now()
	if !found {
		rec = Record{BotID: botID, ImageVersion: img.Version, OverlayPath: m.cfg.Paths.Overlay(botID), Limits: DefaultLimits(), CreatedAt: now}
	}
	rec.Limits = rec.Limits.withDefaults()
	if m.cfg.FreeMemory != nil {
		need := uint64(rec.Limits.MemoryMB+memoryHeadroomMB) << 20
		if free, err := m.cfg.FreeMemory(); err == nil && free < need {
			return nil, nil, fmt.Errorf("%w (need %d MB free, %d MB available)", ErrLowMemory, need>>20, free>>20)
		}
	}
	rec.LastUsedAt = now
	if err := m.store.Upsert(ctx, rec); err != nil {
		return nil, nil, err
	}
	logPath := m.cfg.Paths.Log(m.dataDir(), botID)
	for _, dir := range []string{filepath.Dir(rec.OverlayPath), filepath.Dir(m.cfg.Paths.Socket(botID)), filepath.Dir(logPath)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, nil, err
		}
	}
	token, err := newToken()
	if err != nil {
		return nil, nil, err
	}
	vm, err := m.backend.Boot(ctx, BootSpec{
		BotID: botID, Limits: rec.Limits, BaseImage: img.BaseImage, Kernel: img.Kernel, Initrd: img.Initrd,
		Overlay: rec.OverlayPath, Token: token, LogPath: logPath,
	})
	if err != nil {
		m.logf(botID, "the computer failed to start: %v", err)
		return nil, nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	g, err := newGuestCtx(ctx, vm.Conn(), token)
	if err == nil {
		err = waitReady(ctx, g, vm)
	}
	if err != nil {
		if g != nil {
			_ = g.Close()
		}
		_ = vm.Kill()
		<-vm.Done()
		m.logf(botID, "the computer failed to start: %v", err)
		return nil, nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	return vm, g, nil
}

func waitReady(ctx context.Context, g *Guest, vm VM) error {
	for {
		hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		h, err := g.Health(hctx)
		cancel()
		if err == nil && h.Ready {
			return nil
		}
		t := time.NewTimer(healthPoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("guest not ready: %w", ctx.Err())
		case <-vm.Done():
			t.Stop()
			return fmt.Errorf("the VM exited during boot: %v", vm.ExitErr())
		case <-t.C:
		}
	}
}

// use is the guest's activity hook: requests and streams count as busy and as bot activity.
func (m *Manager) use(inst *instance, delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst.active += delta
	if inst.state == StateRunning {
		inst.lastUsed = m.cfg.Now()
	}
	if inst.active == 0 {
		m.signalLocked()
	}
}

// watch turns an unexpected VM exit into ErrCrashed for calls in flight and state off.
func (m *Manager) watch(inst *instance) {
	<-inst.vm.Done()
	m.mu.Lock()
	if m.vms[inst.botID] != inst || inst.state != StateRunning {
		m.mu.Unlock()
		return
	}
	inst.guest.abort(ErrCrashed)
	m.logf(inst.botID, "the computer stopped unexpectedly: %v", inst.vm.ExitErr())
	delete(m.vms, inst.botID)
	inst.state = StateOff
	inst.takenOver = false
	close(inst.gone)
	st := Status{BotID: inst.botID, State: StateOff, Error: ErrCrashed.Error()}
	m.last[inst.botID] = st
	m.signalLocked()
	m.mu.Unlock()
	_ = inst.guest.Close()
	m.publish(st)
}

func (m *Manager) beginStopLocked(inst *instance) {
	inst.state = StateStopping
	inst.takenOver = false
	m.signalLocked()
}

// finishStop powers the guest off cleanly, kills it if it does not stop, and marks it off. Calls still in
// flight fail with reason.
func (m *Manager) finishStop(inst *instance, reason error) {
	ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
	defer cancel()
	pctx, pcancel := context.WithTimeout(ctx, powerOffTimeout)
	_ = inst.guest.PowerOff(pctx) // best effort; Shutdown below kills what does not stop
	pcancel()
	inst.guest.abort(reason)
	if err := inst.vm.Shutdown(ctx); err != nil {
		_ = inst.vm.Kill()
	}
	select {
	case <-inst.vm.Done():
	case <-ctx.Done():
		_ = inst.vm.Kill()
		<-inst.vm.Done()
	}
	_ = inst.guest.Close()

	m.mu.Lock()
	if m.vms[inst.botID] == inst {
		delete(m.vms, inst.botID)
	}
	inst.state = StateOff
	last := inst.lastUsed
	close(inst.gone)
	m.signalLocked()
	st := m.statusLocked(inst.botID)
	m.mu.Unlock()

	_ = m.store.Touch(context.Background(), inst.botID, last)
	m.publish(st)
}

// Shutdown stops every VM (daemon exit) and refuses new ones. Boots in progress are stopped when they finish.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.stop)
	var running, stopping []*instance
	for _, inst := range m.vms {
		switch inst.state {
		case StateRunning:
			m.beginStopLocked(inst)
			running = append(running, inst)
		case StateStopping:
			stopping = append(stopping, inst)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, inst := range running {
		wg.Add(1)
		go func() { defer wg.Done(); m.finishStop(inst, errClosed) }()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		m.boots.Wait()
		for _, inst := range stopping {
			<-inst.gone
		}
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// logf appends one line to the bot's sandbox log (the backend writes QEMU output to the same file). Never pass
// the guest token.
func (m *Manager) logf(botID, format string, args ...any) {
	path := m.cfg.Paths.Log(m.dataDir(), botID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s orole: %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

func (m *Manager) dataDir() string { return filepath.Dir(m.cfg.Paths.Root) }

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/ -count=3`
Expected: `ok  github.com/rayorole/orole-os-desktop/internal/sandbox` (three runs to shake out races and flakes).

- [ ] **Step 6: Commit**

```bash
git add internal/sandbox/manager.go internal/sandbox/fake_test.go internal/sandbox/manager_test.go
git commit -m "feat(sandbox): manager with single-flight boot, vm cap and crash detection" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Manager: activity, viewer, takeover lock and idle stop

**Files:**
- Create: `internal/sandbox/manager_activity.go`
- Modify: `internal/sandbox/manager.go` (`NewManager`: start the idle loop)
- Test: `internal/sandbox/manager_activity_test.go`

**Interfaces:**
- Consumes: Task 7 internals (`m.mu`, `m.vms`, `m.viewers`, `m.stop`, `m.closed`, `idleLocked`, `beginStopLocked`, `finishStop`, `statusLocked`, `signalLocked`, `publish`), harness helpers.
- Produces (master contract): `Touch(botID)`, `SetViewer(botID, watching bool)`, `TakeOver(botID) error` (`errNotRunning` without a running VM; idempotent), `Release(botID)` (no-op without a lock), `TakenOver(botID) bool`. Unexported: `reapIdle()` (stops every VM idle for `IdleTimeout`, waits for the stops), `idleLoop()` (ticker every `min(IdleTimeout/4, 1m)` until `m.stop` closes). Phase 3 browser tools check `TakenOver` and return `ErrTakenOver`.

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/manager_activity_test.go`:

```go
package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestIdleStopAfterTimeout(t *testing.T) {
	h := newHarness(t, nil)
	ensure(t, h, "bot1")
	used := h.clock()
	h.advance(15*time.Minute - time.Second)
	h.m.reapIdle()
	if st := h.m.Status("bot1"); st.State != StateRunning {
		t.Fatalf("stopped before 15 min: %+v", st)
	}
	h.advance(time.Second)
	h.m.reapIdle()
	if st := h.m.Status("bot1"); st.State != StateOff || st.Error != "" {
		t.Fatalf("after 15 idle min = %+v, want off without error", st)
	}
	select {
	case <-h.b.vmFor("bot1").Done():
	default:
		t.Fatal("VM still running")
	}
	rec, _, _ := h.store.Get(context.Background(), "bot1")
	if !rec.LastUsedAt.Equal(used) {
		t.Fatalf("last_used_at = %v, want %v (the last bot activity, not the stop)", rec.LastUsedAt, used)
	}
}

func TestIdleStopSkipsViewersTakeoverAndBusyVMs(t *testing.T) {
	h := newHarness(t, nil)
	ensure(t, h, "a")
	ensure(t, h, "b")
	g := ensure(t, h, "c")
	h.m.SetViewer("a", true)
	if err := h.m.TakeOver("b"); err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 5s"}) }()
	eventually(t, func() bool { return h.active("c") == 1 })
	h.advance(time.Hour)
	h.m.reapIdle()
	for _, id := range []string{"a", "b", "c"} {
		if st := h.m.Status(id); st.State != StateRunning {
			t.Fatalf("%s = %+v, want running", id, st)
		}
	}
	h.m.SetViewer("a", false) // the idle clock restarts when the last viewer leaves
	h.advance(15 * time.Minute)
	h.m.reapIdle()
	if st := h.m.Status("a"); st.State != StateOff {
		t.Fatalf("a = %+v, want off", st)
	}
	if st := h.m.Status("b"); st.State != StateRunning || !st.TakenOver {
		t.Fatalf("b = %+v, want running and taken over", st)
	}
}

func TestActivityResetsIdleTimer(t *testing.T) {
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	h.advance(10 * time.Minute)
	h.m.Touch("bot1")
	h.advance(10 * time.Minute)
	h.m.reapIdle()
	if h.m.Status("bot1").State != StateRunning {
		t.Fatal("Touch did not count as activity")
	}
	if _, err := g.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.advance(10 * time.Minute)
	h.m.reapIdle()
	if h.m.Status("bot1").State != StateRunning {
		t.Fatal("a guest request did not count as activity")
	}
	h.advance(5 * time.Minute)
	h.m.reapIdle()
	if h.m.Status("bot1").State != StateOff {
		t.Fatal("not stopped 15 min after the last activity")
	}
}

func TestTakeOverLock(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.m.TakeOver("bot1"); !errors.Is(err, errNotRunning) {
		t.Fatalf("TakeOver without a VM = %v, want errNotRunning", err)
	}
	ensure(t, h, "bot1")
	var mu sync.Mutex
	var events []Status
	h.m.Subscribe(func(s Status) { mu.Lock(); events = append(events, s); mu.Unlock() })

	if err := h.m.TakeOver("bot1"); err != nil {
		t.Fatal(err)
	}
	if err := h.m.TakeOver("bot1"); err != nil {
		t.Fatalf("second TakeOver = %v, want nil", err)
	}
	if !h.m.TakenOver("bot1") || !h.m.Status("bot1").TakenOver {
		t.Fatal("lock not visible after TakeOver")
	}
	h.m.Release("bot1")
	h.m.Release("bot1")
	if h.m.TakenOver("bot1") || h.m.Status("bot1").TakenOver {
		t.Fatal("lock still held after Release")
	}
	mu.Lock()
	takes, releasedAfter := 0, false
	for _, e := range events {
		if e.TakenOver {
			takes++
		} else if takes > 0 && e.State == StateRunning {
			releasedAfter = true
		}
	}
	mu.Unlock()
	if takes != 1 || !releasedAfter {
		t.Fatalf("events = %+v, want one taken-over event then a released one", events)
	}

	if err := h.m.TakeOver("bot1"); err != nil {
		t.Fatal(err)
	}
	h.b.vmFor("bot1").crash()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateOff })
	if h.m.TakenOver("bot1") {
		t.Fatal("lock survived a crash")
	}
}

func TestTakenOverVMIsNeverEvicted(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 1; c.BusyWait = 200 * time.Millisecond })
	ensure(t, h, "a")
	if err := h.m.TakeOver("a"); err != nil {
		t.Fatal(err)
	}
	h.advance(time.Hour)
	if _, err := h.m.Ensure(context.Background(), "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Ensure(b) = %v, want ErrBusy", err)
	}
	h.m.reapIdle()
	if st := h.m.Status("a"); st.State != StateRunning || !st.TakenOver {
		t.Fatalf("a = %+v", st)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'TestIdle|TestActivity|TestTake'`
Expected: FAIL, `h.m.reapIdle undefined`, `h.m.SetViewer undefined`.

- [ ] **Step 3: Write the implementation**

`internal/sandbox/manager_activity.go`:

```go
package sandbox

import (
	"sync"
	"time"
)

// Touch records bot activity without a guest call (for example a tool that only reads the status).
func (m *Manager) Touch(botID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst := m.vms[botID]; inst != nil && inst.state == StateRunning {
		inst.lastUsed = m.cfg.Now()
	}
}

// SetViewer records whether the Computer tab is open for the bot. A watched VM is never idle; the idle clock
// restarts when the viewer leaves.
func (m *Manager) SetViewer(botID string, watching bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if watching {
		m.viewers[botID] = true
		return
	}
	delete(m.viewers, botID)
	if inst := m.vms[botID]; inst != nil && inst.state == StateRunning {
		inst.lastUsed = m.cfg.Now()
	}
	m.signalLocked()
}

// TakeOver gives the user the keyboard and mouse: bot browser tools are refused (phase 3 checks TakenOver)
// until Release. A step already running finishes. The lock lives only as long as the VM.
func (m *Manager) TakeOver(botID string) error {
	m.mu.Lock()
	inst := m.vms[botID]
	if inst == nil || inst.state != StateRunning {
		m.mu.Unlock()
		return errNotRunning
	}
	changed := !inst.takenOver
	inst.takenOver = true
	inst.lastUsed = m.cfg.Now()
	st := m.statusLocked(botID)
	m.mu.Unlock()
	if changed {
		m.publish(st)
	}
	return nil
}

func (m *Manager) Release(botID string) {
	m.mu.Lock()
	inst := m.vms[botID]
	if inst == nil || !inst.takenOver {
		m.mu.Unlock()
		return
	}
	inst.takenOver = false
	inst.lastUsed = m.cfg.Now()
	m.signalLocked()
	st := m.statusLocked(botID)
	m.mu.Unlock()
	m.publish(st)
}

func (m *Manager) TakenOver(botID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst := m.vms[botID]
	return inst != nil && inst.takenOver
}

// reapIdle stops every VM idle (no call, no viewer, no takeover) for IdleTimeout and waits for the stops.
func (m *Manager) reapIdle() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	now := m.cfg.Now()
	var stale []*instance
	var statuses []Status
	for _, inst := range m.vms {
		if m.idleLocked(inst) && now.Sub(inst.lastUsed) >= m.cfg.IdleTimeout {
			m.beginStopLocked(inst)
			stale = append(stale, inst)
			statuses = append(statuses, m.statusLocked(inst.botID))
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for i, inst := range stale {
		m.publish(statuses[i])
		wg.Add(1)
		go func() { defer wg.Done(); m.finishStop(inst, errStopped) }()
	}
	wg.Wait()
}

func (m *Manager) idleLoop() {
	every := min(m.cfg.IdleTimeout/4, time.Minute)
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			m.reapIdle()
		}
	}
}
```

In `internal/sandbox/manager.go`, inside `NewManager`, replace

```go
		changed: make(chan struct{}), stop: make(chan struct{}), subs: map[int]func(Status){},
	}
	return m
}
```

with

```go
		changed: make(chan struct{}), stop: make(chan struct{}), subs: map[int]func(Status){},
	}
	go m.idleLoop()
	return m
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/ -count=3`
Expected: `ok  github.com/rayorole/orole-os-desktop/internal/sandbox`.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/manager_activity.go internal/sandbox/manager_activity_test.go internal/sandbox/manager.go
git commit -m "feat(sandbox): idle stop, viewer flag and takeover lock" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: Manager: delete and restart

**Files:**
- Create: `internal/sandbox/manager_delete.go`
- Test: `internal/sandbox/manager_delete_test.go`

**Interfaces:**
- Consumes: Task 7/8 internals (`m.deleting`, `m.vms`, `m.last`, `m.viewers`, `beginStopLocked`, `finishStop`, `statusLocked`, `publish`), `Store.Get/Delete`, `Paths.Overlay/Socket`.
- Produces (master contract): `(*Manager).Delete(ctx, botID) error`: refuses `Ensure` for the bot while it runs (`errDeleting`), waits for a boot in progress, stops the VM (calls in flight get `errDeleting`), removes the overlay (both the recorded and the computed path), the socket file and the row, publishes `off`. Idempotent. The log file is kept for diagnostics. Phase 3 calls it when a bot is deleted.

- [ ] **Step 1: Write the failing tests**

`internal/sandbox/manager_delete_test.go`:

```go
package sandbox

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestDeleteWhileRunning(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	g := ensure(t, h, "bot1")
	overlay := h.paths.Overlay("bot1")
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("overlay missing before delete: %v", err)
	}
	errc := make(chan error, 1)
	go func() { _, err := g.Exec(ctx, wire.ExecRequest{Command: "sleep 10s"}); errc <- err }()
	eventually(t, func() bool { return h.active("bot1") == 1 })

	if err := h.m.Delete(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.b.vmFor("bot1").Done():
	default:
		t.Fatal("VM still running after Delete")
	}
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	if _, found, _ := h.store.Get(ctx, "bot1"); found {
		t.Fatal("row still there")
	}
	if st := h.m.Status("bot1"); st != (Status{BotID: "bot1", State: StateOff}) {
		t.Fatalf("status = %+v", st)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, errDeleting) {
			t.Fatalf("in-flight Exec = %v, want errDeleting", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Exec never returned")
	}
	if n := h.b.liveCount(); n != 0 {
		t.Fatalf("%d VMs left", n)
	}
}

func TestDeleteWhileBooting(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.b.update(func(b *fakeBackend) { b.hold = make(chan struct{}) })
	type result struct {
		g   *Guest
		err error
	}
	ensured := make(chan result, 1)
	go func() { g, err := h.m.Ensure(ctx, "bot1"); ensured <- result{g, err} }()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateStarting })

	deleted := make(chan error, 1)
	go func() { deleted <- h.m.Delete(ctx, "bot1") }()
	select {
	case err := <-deleted:
		t.Fatalf("Delete returned %v while the VM was still booting", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := h.m.Ensure(ctx, "bot1"); !errors.Is(err, errDeleting) {
		t.Fatalf("Ensure during Delete = %v, want errDeleting", err)
	}
	h.b.update(func(b *fakeBackend) { close(b.hold) })
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	r := <-ensured
	if r.err != nil {
		t.Fatalf("the waiting Ensure = %v; it should get the guest that booted", r.err)
	}
	if _, err := r.g.Health(ctx); !errors.Is(err, errDeleting) {
		t.Fatalf("using the deleted guest = %v, want errDeleting", err)
	}
	if _, err := os.Stat(h.paths.Overlay("bot1")); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	if n := h.b.liveCount(); n != 0 || h.b.bootCount() != 1 {
		t.Fatalf("live=%d boots=%d, want 0 and 1", n, h.b.bootCount())
	}
}

func TestDeleteWithoutRunningVM(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	ensure(t, h, "bot1")
	h.advance(16 * time.Minute)
	h.m.reapIdle()
	if h.m.Status("bot1").State != StateOff {
		t.Fatal("setup: VM did not stop")
	}
	if err := h.m.Delete(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.paths.Overlay("bot1")); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	if _, found, _ := h.store.Get(ctx, "bot1"); found {
		t.Fatal("row still there")
	}
	if err := h.m.Delete(ctx, "bot1"); err != nil {
		t.Fatalf("second Delete = %v", err)
	}
	if err := h.m.Delete(ctx, "../x"); err == nil {
		t.Fatal("Delete accepted an invalid id")
	}
}

// After a daemon restart the new manager boots the same bot on the same overlay and keeps the record.
func TestNewManagerReusesOverlay(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	ensure(t, h, "bot1")
	rec, _, _ := h.store.Get(ctx, "bot1")
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.m.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	h.m = NewManager(h.cfg, h.b, h.store)
	h.advance(time.Hour)
	ensure(t, h, "bot1")
	if got := h.b.spec(1).Overlay; got != h.paths.Overlay("bot1") {
		t.Fatalf("second boot overlay = %q", got)
	}
	again, _, _ := h.store.Get(ctx, "bot1")
	if !again.CreatedAt.Equal(rec.CreatedAt) {
		t.Fatalf("created_at changed: %v -> %v", rec.CreatedAt, again.CreatedAt)
	}
	if n := h.b.liveCount(); n != 1 {
		t.Fatalf("%d VMs running, want 1", n)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/sandbox/ -run 'TestDelete|TestNewManager'`
Expected: FAIL, `h.m.Delete undefined`.

- [ ] **Step 3: Write the implementation**

`internal/sandbox/manager_delete.go`:

```go
package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
)

// Delete stops the bot's VM if any (waiting for a boot in progress), then removes its overlay disk, socket and
// record. Ensure for the bot fails with errDeleting meanwhile. The sandbox log is kept.
func (m *Manager) Delete(ctx context.Context, botID string) error {
	if err := checkBotID(botID); err != nil {
		return err
	}
	m.mu.Lock()
	if m.deleting[botID] {
		m.mu.Unlock()
		return errDeleting
	}
	m.deleting[botID] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.deleting, botID)
		m.mu.Unlock()
	}()

	for {
		m.mu.Lock()
		inst := m.vms[botID]
		if inst == nil {
			m.mu.Unlock()
			break
		}
		switch inst.state {
		case StateStarting:
			ready := inst.ready
			m.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				return ctx.Err()
			}
		case StateRunning:
			m.beginStopLocked(inst)
			st := m.statusLocked(botID)
			m.mu.Unlock()
			m.publish(st)
			m.finishStop(inst, errDeleting)
		default:
			gone := inst.gone
			m.mu.Unlock()
			select {
			case <-gone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	paths := []string{m.cfg.Paths.Overlay(botID), m.cfg.Paths.Socket(botID)}
	rec, found, err := m.store.Get(ctx, botID)
	if err != nil {
		return err
	}
	if found && rec.OverlayPath != paths[0] {
		paths = append(paths, rec.OverlayPath)
	}
	var errs []error
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := m.store.Delete(ctx, botID); err != nil {
		errs = append(errs, err)
	}
	m.mu.Lock()
	delete(m.last, botID)
	delete(m.viewers, botID)
	st := m.statusLocked(botID)
	m.mu.Unlock()
	m.publish(st)
	return errors.Join(errs...)
}
```

- [ ] **Step 4: Run the whole package and the repo's checks**

Run: `go test -race ./internal/sandbox/... -count=3`
Expected: `ok` for all packages.
Run: `go fmt ./... && go vet ./... && go test -race ./...`
Expected: no formatting changes, no vet output, every package `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/manager_delete.go internal/sandbox/manager_delete_test.go
git commit -m "feat(sandbox): delete a bot's computer with its overlay and record" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Architecture doc and Linux CI lane

**Files:**
- Create: `docs/architecture/bot-computer.md`
- Modify: `.github/workflows/ci.yml` (job `portable`)

**Interfaces:** none (documentation and CI). No CHANGELOG entry: nothing user-visible changes until phase 4, which owns the user-facing docs.

- [ ] **Step 1: Write the architecture doc**

`docs/architecture/bot-computer.md`:

```markdown
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
  request or stream in flight, no viewer and no takeover. When every VM is busy, `Ensure` waits up to 30 s for
  one to become idle, then fails with "all computers are busy".
- A VM idle for 15 min (no bot activity, no viewer, no takeover) is powered off (`/poweroff`, then the backend's
  shutdown, then kill). The overlay disk stays.
- A VM that exits on its own is a crash: calls in flight fail with "the computer stopped unexpectedly", the
  status becomes off with that error, the takeover lock is dropped, and the next call boots again.
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
```

- [ ] **Step 2: Add the sandbox packages to the Linux CI lane**

In `.github/workflows/ci.yml`, job `portable`, replace

```yaml
      - name: Go test (race), portable packages and Linux terminals
        run: go test -race ./internal/ipc/... ./internal/database/... ./internal/platform/... ./internal/paths/... ./internal/credentials/... ./internal/terminals/...
```

with

```yaml
      - name: Go test (race), portable packages and Linux terminals
        run: go test -race ./internal/ipc/... ./internal/database/... ./internal/platform/... ./internal/paths/... ./internal/credentials/... ./internal/terminals/... ./internal/sandbox/... ./cmd/orole-guest/...

      # The guest agent ships inside the Linux VM image: it must build static for linux/amd64.
      - name: Build orole-guest (linux/amd64, static)
        run: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$RUNNER_TEMP/orole-guest" ./cmd/orole-guest
```

- [ ] **Step 3: Verify**

Run: `go vet ./... && GOOS=linux GOARCH=amd64 go vet ./internal/sandbox/... ./cmd/orole-guest/`
Expected: no output.
If `actionlint` is installed locally, run `actionlint .github/workflows/ci.yml`; expected: no output. (CI runs it otherwise.)

- [ ] **Step 4: Commit**

```bash
git add docs/architecture/bot-computer.md .github/workflows/ci.yml
git commit -m "docs(sandbox): bot computer architecture and linux ci lane for the guest" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: Push and open the draft PR

**Files:** none.

**Interfaces:** the PR is the base for phase 2's branch `feat/197-sandbox-windows`.

- [ ] **Step 1: Final verification**

Run: `go fmt ./... && go vet ./... && go test -race ./...`
Expected: no output from fmt/vet, every package `ok`.
Run: `git status --short`
Expected: empty.

- [ ] **Step 2: Push**

Run: `git push -u origin feat/196-sandbox-core`
Expected: `branch 'feat/196-sandbox-core' set up to track 'origin/feat/196-sandbox-core'`.

- [ ] **Step 3: Open the draft PR against `jarvis-mode`**

```bash
gh pr create --draft --base jarvis-mode --head feat/196-sandbox-core \
  --title "feat(sandbox): bot computer core and guest agent (phase 1)" \
  --body "$(cat <<'EOF'
Phase 1 of the bot computer (one local Linux VM per bot). Spec: `docs/superpowers/specs/2026-10-15-bot-computer-design.md`; plan: `docs/superpowers/plans/2026-10-15-bot-computer-phase1-core.md`.

## What
- `internal/sandbox`: types, paths, `Backend`/`VM` interfaces, `Store` over the new `sandboxes` table (migration 0112), and `Manager`: on-demand boot with single flight, cap of 4 with LRU eviction of idle VMs, 30 s busy wait, 60 s boot timeout waiting for guest health, free-memory check, 15 min idle stop, viewer flag, takeover lock, crash detection, delete, shutdown, status events.
- `internal/sandbox/wire` + `guestclient`: HTTP/2 without TLS over the VM's byte stream (`golang.org/x/net/http2`); exec, files, terminal (`terminals.Process`), screen stream; the host re-caps every answer.
- `internal/sandbox/guestagent` + `cmd/orole-guest`: the guest agent (token check, exec with timeout and 1 MiB cap, files with 4/16 MiB limits, PTY via the existing creack/pty dependency, poweroff; `/screen` and `/browser` answer 501 until later phases).
- `docs/architecture/bot-computer.md`, schema doc, Linux CI lane runs the sandbox tests and builds `orole-guest`.

Nothing starts a real VM yet; the Windows backend is phase 2 (#197). No user-visible change, so no CHANGELOG entry.

## Tests
`go test -race ./...` on Windows (fake backend running the real guest handler over `net.Pipe`), plus real `/bin/sh` exec and PTY tests on Linux CI. Covers concurrent `Ensure` (one boot), output caps, crash mid-call, takeover lock, delete while running or booting, restart on the same overlay.

Closes #196

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: a `https://github.com/rayorole/orole-os-desktop/pull/<n>` URL. Do not merge.

---

## Self-review

- **Spec coverage (phase 1 part):** manager behaviours (on-demand start, idle stop, cap/LRU, waiting, timeouts, crash, takeover lock, delete removes overlay) Tasks 7-9; guest channel framing, token check, close/reconnect over an in-memory pipe Task 4 (`TestGuestWrongTokenIsRejected`, `TestServeLoopReconnects`); `orole-guest` exec timeout and cap, files, PTY on Linux Tasks 3, 5 (`exec_linux_test.go`, `pty_linux_test.go`); store and migration Task 2; architecture doc Task 10. Runtime downloader, setup, Windows backend, bot tools, UI: phases 2-4.
- **Placeholders:** none; every code step has the full file or an exact before/after edit.
- **Type consistency:** `Guest` internals (`setOnUse`, `abort`, `use`, `mapErr`, `call`, `openStream`) are defined in Tasks 4-5 and used with the same signatures in Task 7; `harness`/`fakeBackend` helpers are defined in Task 7 and reused unchanged in Tasks 8-9; `guestagent.Config` fields added in Task 5 (`LoginShell`, `StartPTY`) are only used from Task 5 on.
- **Review Focus:** each line names its test and task above.
