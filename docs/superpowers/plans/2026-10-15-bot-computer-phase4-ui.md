# Bot computer Phase 4: Computer tab, live view, terminal, docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The user can open a bot's **Computer** tab, watch the bot's VM screen live (view-only), take over mouse and keyboard and hand back, use a terminal in the VM, and read honest docs about what the bot computer can reach.

**Architecture:** The daemon gains four IPC streams on top of phase 2/3's `svc_sandbox.go`: a viewer lease (`sandbox.viewer`), a screen relay (`sandbox.screen.open/input/close` + `sandbox.screen.data` events) whose client-to-guest half is checked by a Go RFB filter that drops keyboard/pointer/clipboard unless the user holds the takeover lock, and `sandbox.pty.open`, which adopts the guest PTY into the existing `terminals.Manager` so `terminals.write/resize/attach/close` and `terminal.output` work unchanged. A stateless MyGo service (`internal/app/sandbox.go`) forwards calls and events. The React Computer tab is a new side-pane kind beside the conversation; noVNC 1.7.0 draws the screen through a WebSocket-shaped `ScreenChannel` built on the MyGo calls and events (no socket anywhere), and the existing `TerminalPane` shows the guest terminal.

**Tech Stack:** Go 1.27 (stdlib, `golang.org/x/net/http2` already added in phase 1), MyGo (`go tool mygo generate`), React 19 + TypeScript (strict, `erasableSyntaxOnly`), Zustand, Tailwind v4, Base UI/shadcn components in `components/ui`, xterm.js 6 via `features/terminals/TerminalPane.tsx`, `@novnc/novnc` 1.7.0 (MPL-2.0), Vitest + Testing Library.

**Spec:** `docs/superpowers/specs/2026-10-15-bot-computer-design.md` (master plan: `docs/superpowers/plans/2026-10-15-bot-computer.md`, whose Global Constraints and Shared contracts apply here verbatim).

**Issue / branch / PR:** #199 · `feat/199-computer-tab`, cut from `feat/198-bot-computer-tools` · draft PR with base `feat/198-bot-computer-tools`, body ending `Closes #199`. Never merge.

## Contract issues

The master contracts are workable. These gaps are filled here; phases 2 and 3 must match the names in **bold** (or this plan's executor adapts the one line that uses them):

1. **No "tab" mechanism exists on the bot page.** The bot page (`features/chat/ChatView.tsx`) is the conversation plus `BotPanel` (settings) plus `SidePane` (task / call / document). The Computer tab is a new `SidePane` kind **`{ kind: "computer"; id: <botId> }`**, opened by `useChat.getState().openComputerPane(botId)` and by a Computer button in the title bar (`BotActions`).
2. **Notification deep link.** The master plan does not name the target. Phase 3's "Needs you" notification for `ask_user_to_take_over` must use target **`computer:<botId>`**. This plan teaches `features/notifications/targets.ts` to open it (view `chat`, bot selected, Computer pane open). A `bot:<botId>` target would only open the conversation.
3. **"Using the computer" message part.** The master plan does not name it. This plan renders **`MsgPart{type: "computer", status: "running"|"done"|"error", text: <one-line summary, e.g. "Starting computer…" or the command>}`**. Phase 3 must emit that `type`; the card ignores other fields.
4. **`sandbox.screen.data` needs an end-of-stream signal.** The event payload is extended (same event name) to `{id, data(base64), closed?: bool, error?: string}`; the last event of a stream has `closed: true`. `sandbox.screen.open` returns `{id}`.
5. **Screen input must be filtered in Go.** noVNC's `viewOnly` is a frontend check, which AGENTS.md forbids as the only gate. `sandbox.screen.input` runs every byte through `internal/sandbox/rfbfilter.go`; keyboard, pointer, clipboard and QEMU key messages pass only while `Manager.TakenOver(botID)` is true.
6. **Viewer flag must not stick when the GUI dies.** `sandbox.viewer{botId,watching}` is a lease: the UI renews it every 60 s; the daemon clears it after 150 s without renewal, so the 15-minute idle stop still happens after a crash.
7. **`sandbox.pty.open` must not boot a VM.** The spec says no VM is created until the bot's first tool call. `pty.open` and `screen.open` return `not_found` ("the computer is off") unless `Manager.Status(botID).State == "running"`, then call `Manager.Ensure` (which does not boot a running VM; it marks activity, which is right for a user action).
8. **Command echo hook.** The spec says bot commands are echoed in the Computer terminal; no contract carries them. This plan adds `Guest.OnExec(fn)` and `Config.OnExec func(botID string, req wire.ExecRequest, res wire.ExecResult)`; `Manager` sets the guest hook right after it creates a guest with `NewGuest`. The daemon injects the echo into the bot's open terminal with a new `terminals.Manager.Inject`.
9. **`sandbox.release` has no note.** The spec's "the user finished: <note>" needs a note param the contract lacks. The UI sends no note (keep UI minimal); phase 3 tells the bot "the user finished".
10. **Setup calls can take minutes** (UAC prompt, a download of hundreds of MB). `sandbox.setup.enable` and `sandbox.runtime.install` join the 16-minute timeout list in `internal/app/bridge.go`; the contract has no download progress, so the UI shows "Downloading…" until the call returns.
11. **Field and helper names from phases 1-3 that this plan assumes:** `Daemon.Sandbox *sandbox.Manager` (phase 2), `registerSandbox()` in `internal/daemon/svc_sandbox.go` (phase 2), the `sandbox.Config{...}` literal built there, `BotConfig.computer?: boolean` in `frontend/src/mygo.ts` (phase 3). If a name differs, use the real one; do not rename phase 1-3 code.
12. **Generated TypeScript names.** mygo prefixes colliding type names with the package (`State` and `Status` are taken), so `sandbox.Status` may be generated as `SandboxStatus`. Frontend code never imports those names; it derives them: `Awaited<ReturnType<typeof Sandbox.status>>`.

## Global Constraints

- Daemon owns VMs, state and processes; the GUI process never starts QEMU or talks to a guest directly (AGENTS.md). `internal/app` imports `internal/sandbox` for types only.
- Permissions are decided in Go via `policy.Engine.Check`; no frontend-only checks. The takeover gate for screen input is `rfbFilter` in Go; `viewOnly` in noVNC is cosmetic.
- No listener on a network interface on the host. The screen and terminal travel over the guest channel and Orole IPC only; noVNC is given a channel object, never a URL.
- Secrets never in SQLite, logs or the frontend. Screen bytes and keystrokes are never logged or stored; audit rows hold only bot id and stream id.
- Guest output, page text and file contents reach bots as `ToolResult{Untrusted: true}`.
- Defaults: 2048 MB RAM, 2 vCPU, 10 GB overlay per bot; max 4 running VMs; idle stop 15 min (no bot activity and no viewer); boot timeout 60 s; busy wait 30 s.
- Tests: `go test -race ./...` clean; `bun run --cwd frontend check` clean. Table/behaviour tests, fake backend for anything needing a VM.
- Commits: conventional, lowercase scope, one per task, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Copied upstream code and bundled dependencies (noVNC) recorded in `docs/research/upstream-attribution.md`.
- Docs and CHANGELOG updated in this PR (phase 4 holds the user-facing docs).
- No native HTML `title` attributes in UI; hover text via `Tooltip` only where needed (icon-only controls). Compact density, one token layer (`index.css` + `themes/themes.css`), components from `components/ui`.
- UI scope (user decision): Computer tab = live screen with "Take over" / "Done" + terminal + setup screen. No start/stop/reset buttons, no resource display, no wipe-logins button.
- Changing a bound service or event in `internal/app` => `go tool mygo generate` and commit `frontend/src/mygo.ts`.
- Dev isolation: run the app with `OROLE_HOME=.orole-dev`; never delete `%LOCALAPPDATA%\Orole`.

## Review Focus

- A crafted `sandbox.screen.input` carrying a KeyEvent/PointerEvent while the user has not taken over: the daemon drops it; the guest never sees it (Task 2 filter tests, Task 3 hub test).
- The GUI crashes or is closed while the Computer tab is open: the viewer lease expires after 150 s and the 15-minute idle stop happens as if nobody watched (Task 3 `ViewerLeases` test).
- The VM stops (idle, crash, daemon shutdown) while the tab shows the screen: the screen stream ends with a `closed` event, the UI says the screen disconnected or the computer is off, and no goroutine or guest stream leaks (Task 3 `CloseBot` test, Task 9 disconnect test).
- Screen bytes arrive before `openScreen` resolves (the guest VNC server speaks first): they are buffered and replayed so the RFB handshake is not lost (Task 7 early-chunk test).
- The user closes the Computer tab (or switches bot) while holding the takeover: control is handed back so the bot is not left blocked (Task 10 unmount test).

## File structure

| File | Responsibility |
|---|---|
| `internal/terminals/terminals.go` (modify) | `Adopt` an externally started `Process`; `Inject` echo bytes into a session's output |
| `internal/sandbox/rfbfilter.go` (create) | Validate the client half of RFB 3.8; drop input unless taken over; strip risky encodings |
| `internal/sandbox/screen.go` (create) | `ScreenHub`: open screen streams, pump guest bytes to events, filtered input, close per id/bot/all |
| `internal/sandbox/viewer.go` (create) | `ViewerLeases`: viewer flag with expiry |
| `internal/sandbox/guestclient.go`, `manager.go` (modify) | `Guest.OnExec` hook; `Config.OnExec` wiring |
| `internal/daemon/svc_sandbox_streams.go` (create) | IPC `sandbox.viewer`, `sandbox.screen.*`, `sandbox.pty.open`, exec echo |
| `internal/daemon/daemon.go`, `svc_sandbox.go` (modify) | fields, teardown, `OnExec` and `registerSandboxStreams` hookup |
| `internal/app/sandbox.go` (create), `bridge.go`, `cmd/desktop/main.go` (modify) | MyGo `Sandbox` service + `sandbox:status`, `sandbox:screen` events |
| `frontend/src/types/novnc.d.ts` (create) | Types for the parts of noVNC we use |
| `frontend/src/features/computer/screenChannel.ts` | WebSocket-shaped adapter noVNC accepts |
| `frontend/src/features/computer/computerState.ts` | Pure view-state mapping, setup copy, error text |
| `frontend/src/features/computer/useComputer.ts` | Availability/status loading + status events; viewer lease hook |
| `frontend/src/features/computer/LiveScreen.tsx` | noVNC view, view-only unless taken over, reconnect |
| `frontend/src/features/computer/ComputerTab.tsx` | Setup, starting/off/failed, running (screen + takeover + terminal) |
| `frontend/src/features/computer/ComputerCard.tsx` | Chat card for the "computer" part |
| `frontend/src/features/computer/testing.ts` | Mocked MyGo bridge for computer tests |
| `frontend/src/stores/chat.ts`, `features/chat/SidePane.tsx`, `features/chat/BotHeader.tsx`, `features/chat/parts/MessageParts.tsx`, `features/notifications/{targets.ts,NotificationCenter.tsx}` (modify) | Pane kind, title-bar button, chat card, deep link |
| `docs/architecture/bot-computer.md`, `docs/security/threat-model.md`, `docs/architecture/security-permissions.md`, `docs/development/bot-computer-e2e.md`, `CHANGELOG.md`, `docs/research/upstream-attribution.md` | Docs |

---

### Task 1: Terminals: adopt an external process and inject echo output

**Files:**
- Modify: `internal/terminals/terminals.go` (add after `Create`, around line 225)
- Test: `internal/terminals/adopt_test.go` (create)

**Interfaces:**
- Consumes: existing `Process`, `Manager`, `Session.push`, `Manager.get`, test helpers `idleBackend`, `newFakeProc`, `countSink`, `eventually` (package `terminals_test`).
- Produces:
  ```go
  type AdoptOptions struct { Title, Shell, Owner string; Cols, Rows int }
  func (m *Manager) Adopt(p Process, o AdoptOptions) (Info, error) // closes p on error
  func (m *Manager) Inject(id string, data []byte) error           // output only; never written to the process
  ```

- [ ] **Step 1: Write the failing tests**

Create `internal/terminals/adopt_test.go`:

```go
package terminals_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

func TestAdoptRegistersAnExternalProcess(t *testing.T) {
	sink := &countSink{}
	m := terminals.NewManager(idleBackend(), sink)
	t.Cleanup(m.Shutdown)
	p := newFakeProc(5, 1) // one 5-byte read, then blocks until closed
	info, err := m.Adopt(p, terminals.AdoptOptions{Title: "Computer", Shell: "orole-guest", Owner: "sandbox:b1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if info.Owner != "sandbox:b1" || info.Title != "Computer" || info.Shell != "orole-guest" ||
		info.State != terminals.Running || info.Cols != 80 || info.Rows != 24 || info.Pid != 4242 || info.ID == "" {
		t.Fatalf("info = %+v", info)
	}
	if !eventually(5*time.Second, func() bool { _, b, _, _, _ := sink.snapshot(); return b == 5 }) {
		t.Fatal("adopted output was not delivered")
	}
	if err := m.Write(info.ID, []byte("ls\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Resize(info.ID, 100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.closed:
	default:
		t.Fatal("Close did not close the adopted process")
	}
}

func TestAdoptRespectsTheSessionLimitAndClosesTheRefusedProcess(t *testing.T) {
	m := terminals.NewManager(idleBackend(), &countSink{})
	t.Cleanup(m.Shutdown)
	for range 64 {
		if _, err := m.Create(terminals.CreateOptions{Shell: "fake"}); err != nil {
			t.Fatal(err)
		}
	}
	p := newFakeProc(1, 0)
	if _, err := m.Adopt(p, terminals.AdoptOptions{Owner: "sandbox:b1"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("65th session: err = %v, want limit error", err)
	}
	select {
	case <-p.closed:
	default:
		t.Fatal("refused process was left open")
	}
}

func TestInjectAddsOutputWithoutWritingToTheProcess(t *testing.T) {
	sink := &countSink{}
	m := terminals.NewManager(idleBackend(), sink)
	t.Cleanup(m.Shutdown)
	info, err := m.Create(terminals.CreateOptions{Shell: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Inject(info.ID, []byte("$ echo hi\r\n")); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool { _, b, _, _, _ := sink.snapshot(); return b == 11 }) {
		t.Fatal("injected output was not delivered")
	}
	a, err := m.Attach(info.ID)
	if err != nil || string(a.Data) != "$ echo hi\r\n" {
		t.Fatalf("scrollback = %q, %v", a.Data, err)
	}
	if err := m.Inject("missing", []byte("x")); err == nil {
		t.Fatal("inject into an unknown session succeeded")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/terminals/ -run "TestAdopt|TestInject" -v`
Expected: FAIL, compile error `m.Adopt undefined` / `terminals.AdoptOptions undefined`.

- [ ] **Step 3: Implement**

In `internal/terminals/terminals.go`, after `Create` (before `readLoop`), add:

```go
// AdoptOptions describes a process started outside this manager (the bot computer's guest PTY) that the manager
// should own from now on.
type AdoptOptions struct {
	Title string
	Shell string // label shown as the shell, e.g. "orole-guest"
	Owner string // set by trusted daemon code only, e.g. "sandbox:<botID>"
	Cols  int
	Rows  int
}

// Adopt registers an already running process as a session, so Write, Resize, Attach, Close and the output events
// work for it like for any terminal. It counts against the session cap. The manager owns p from here on: when Adopt
// fails it closes p.
func (m *Manager) Adopt(p Process, o AdoptOptions) (Info, error) {
	if o.Cols <= 0 {
		o.Cols = 120
	}
	if o.Rows <= 0 {
		o.Rows = 30
	}
	m.mu.Lock()
	if m.running >= m.max {
		m.mu.Unlock()
		_ = p.Close()
		return Info{}, errors.New("terminals: session limit reached")
	}
	m.running++
	m.mu.Unlock()
	info := Info{ID: uuid.NewString(), Title: o.Title, Shell: o.Shell, Cols: o.Cols, Rows: o.Rows, Pid: p.Pid(),
		State: Running, StartedAt: time.Now().UTC(), Owner: o.Owner}
	if info.Title == "" {
		info.Title = o.Shell
	}
	s := m.newSession(p, info)
	m.mu.Lock()
	m.sess[info.ID] = s
	m.mu.Unlock()
	go s.pump()
	return info, nil
}

// Inject adds bytes to a running session's output as if its process had written them; they are never written to
// the process. The bot computer uses it to show commands the bot ran in the user's Computer terminal.
func (m *Manager) Inject(id string, data []byte) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	running := s.info.State == Running
	s.mu.Unlock()
	if !running {
		return errors.New("terminals: session has exited")
	}
	if len(data) > 0 {
		s.push(data)
	}
	return nil
}
```

(`push` copies into the scrollback, so the caller may reuse `data`.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/terminals/ -v -run "TestAdopt|TestInject"` then `go test -race ./internal/terminals/`
Expected: PASS for both.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/terminals
git add internal/terminals/terminals.go internal/terminals/adopt_test.go
git commit -m "feat(terminals): adopt external processes and inject echo output

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: RFB input filter (the takeover gate for the screen)

**Files:**
- Create: `internal/sandbox/rfbfilter.go`
- Test: `internal/sandbox/rfbfilter_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (package-private, used by Task 3):
  ```go
  var errRFBInput = errors.New("sandbox: unexpected screen input")
  func newRFBFilter(allowInput func() bool) *rfbFilter
  func (f *rfbFilter) Feed(p []byte) (forward []byte, err error)
  ```

Background for the implementer: the guest VNC server (phase 2 image) speaks RFB 3.8 with security type None on 127.0.0.1 inside the guest. noVNC 1.7.0 (client) sends, in order: the 12-byte version `"RFB 003.008\n"`, one byte choosing the security type (1 = None), one ClientInit byte (shared flag), then client messages: SetPixelFormat (type 0, 20 bytes), SetEncodings (2, 4 + 4·n), FramebufferUpdateRequest (3, 10), KeyEvent (4, 8), PointerEvent (5, 6; 7 only if the server enabled ExtendedMouseButtons, which we prevent by stripping pseudo-encoding -316), ClientCutText (6, 8 + |length|, negative length = extended clipboard), EnableContinuousUpdates (150, 10), ClientFence (248, 9 + payload length at byte 8), xvp (250, 4: power off / reboot / reset), SetDesktopSize (251, 8 + 16·screens at byte 6), QEMU extended key event (255 subtype 0, 12). Messages can be split across sends (noVNC's send buffer is 10 KiB), so the filter is a streaming parser.

- [ ] **Step 1: Write the failing tests**

Create `internal/sandbox/rfbfilter_test.go`:

```go
package sandbox

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func handshake() []byte { return append([]byte("RFB 003.008\n"), 1, 0) }

func key(down byte) []byte { return []byte{4, down, 0, 0, 0, 0, 0xff, 0x0d} }

func encodings(list ...int32) []byte {
	b := []byte{2, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(len(list)))
	for _, e := range list {
		b = binary.BigEndian.AppendUint32(b, uint32(e))
	}
	return b
}

func cutText(n int32, payload int) []byte {
	b := []byte{6, 0, 0, 0}
	b = binary.BigEndian.AppendUint32(b, uint32(n))
	return append(b, bytes.Repeat([]byte{'a'}, payload)...)
}

func TestRFBFilter(t *testing.T) {
	fbReq := []byte{3, 1, 0, 0, 0, 0, 0x04, 0x00, 0x03, 0x00}
	pointer := []byte{5, 1, 0, 10, 0, 20}
	qemuKey := append([]byte{255, 0, 0, 1}, make([]byte, 8)...)
	xvp := []byte{250, 0, 1, 2}
	desktopSize := append([]byte{251, 0, 4, 0, 3, 0, 1, 0}, make([]byte, 16)...)
	fence := []byte{248, 0, 0, 0, 0, 0, 0, 0, 2, 'h', 'i'}
	pixelFormat := append([]byte{0}, make([]byte, 19)...)
	wantHandshake := append([]byte("RFB 003.008\n"), 1, 1) // ClientInit forced to shared

	tests := []struct {
		name    string
		allow   bool
		in      [][]byte
		want    []byte
		wantErr bool
	}{
		{name: "handshake passes, client init forced shared", in: [][]byte{handshake()}, want: wantHandshake},
		{name: "wrong protocol version", in: [][]byte{[]byte("RFB 003.003\n")}, wantErr: true},
		{name: "security type other than None", in: [][]byte{append([]byte("RFB 003.008\n"), 2)}, wantErr: true},
		{name: "view messages pass while view-only", in: [][]byte{handshake(), fbReq, pixelFormat, fence, {150, 1, 0, 0, 0, 0, 4, 0, 3, 0}},
			want: concat(wantHandshake, fbReq, pixelFormat, fence, []byte{150, 1, 0, 0, 0, 0, 4, 0, 3, 0})},
		{name: "key, pointer, clipboard and qemu key dropped while view-only", in: [][]byte{handshake(), key(1), pointer, cutText(2, 2), qemuKey, fbReq},
			want: concat(wantHandshake, fbReq)},
		{name: "key, pointer, clipboard and qemu key pass after take over", allow: true, in: [][]byte{handshake(), key(1), pointer, cutText(2, 2), qemuKey},
			want: concat(wantHandshake, key(1), pointer, cutText(2, 2), qemuKey)},
		{name: "extended clipboard negative length", allow: true, in: [][]byte{handshake(), cutText(-3, 3)}, want: concat(wantHandshake, cutText(-3, 3))},
		{name: "clipboard over 1 MiB is refused", allow: true, in: [][]byte{handshake(), cutText(1<<20+1, 0)}, wantErr: true},
		{name: "xvp power actions always dropped", allow: true, in: [][]byte{handshake(), xvp, fbReq}, want: concat(wantHandshake, fbReq)},
		{name: "desktop resize always dropped", allow: true, in: [][]byte{handshake(), desktopSize, fbReq}, want: concat(wantHandshake, fbReq)},
		{name: "extended mouse buttons and xvp encodings stripped", in: [][]byte{handshake(), encodings(7, -316, 16, -309, -223)},
			want: concat(wantHandshake, encodings(7, 16, -223))},
		{name: "unknown message type", in: [][]byte{handshake(), {7, 0, 0, 0}}, wantErr: true},
		{name: "qemu message other than extended key", allow: true, in: [][]byte{handshake(), {255, 1, 0, 0}}, wantErr: true},
		{name: "too many encodings", in: [][]byte{handshake(), {2, 0, 0x10, 0x00}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRFBFilter(func() bool { return tt.allow })
			var got []byte
			var err error
			for _, p := range tt.in {
				var out []byte
				out, err = f.Feed(p)
				if err != nil {
					break
				}
				got = append(got, out...)
			}
			if tt.wantErr {
				if !errors.Is(err, errRFBInput) {
					t.Fatalf("err = %v, want errRFBInput", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("forwarded\n %v\nwant\n %v", got, tt.want)
			}
		})
	}
}

func TestRFBFilterReassemblesMessagesSplitAcrossFeeds(t *testing.T) {
	allow := false
	f := newRFBFilter(func() bool { return allow })
	stream := concat(handshake(), key(1), []byte{3, 1, 0, 0, 0, 0, 4, 0, 3, 0})
	var got []byte
	for i := range stream { // one byte at a time
		out, err := f.Feed(stream[i : i+1])
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
	}
	want := concat(append([]byte("RFB 003.008\n"), 1, 1), []byte{3, 1, 0, 0, 0, 0, 4, 0, 3, 0})
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// The decision is taken when a message completes: a key that finishes after Take over passes.
	allow = true
	k := key(0)
	if out, _ := f.Feed(k[:3]); len(out) != 0 {
		t.Fatalf("partial message forwarded: %v", out)
	}
	if out, _ := f.Feed(k[3:]); !bytes.Equal(out, k) {
		t.Fatalf("completed key = %v", out)
	}
}

func concat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/sandbox/ -run TestRFBFilter -v`
Expected: FAIL, `undefined: newRFBFilter`.

- [ ] **Step 3: Implement**

Create `internal/sandbox/rfbfilter.go`:

```go
package sandbox

import (
	"encoding/binary"
	"errors"
)

// errRFBInput is returned for screen input outside the RFB subset Orole accepts; the caller closes the stream.
var errRFBInput = errors.New("sandbox: unexpected screen input")

const (
	rfbMaxCutText   = 1 << 20 // largest clipboard text accepted from the user
	rfbMaxEncodings = 256
	rfbMaxScreens   = 16
	rfbMaxPending   = 8 + rfbMaxCutText

	encExtendedMouseButtons = -316 // makes PointerEvent 7 bytes; stripped so every PointerEvent is 6
	encXvp                  = -309 // lets the client power off, reboot or reset the guest; stripped
)

// rfbFilter checks the client half of an RFB 3.8 session (noVNC -> the guest's VNC server) before it reaches the
// guest. The daemon, not the UI, decides whether the user may type into or click on the bot's computer: keyboard,
// pointer, clipboard and QEMU key messages pass only while allowInput reports true and are dropped otherwise.
// Power and resize messages are always dropped. Anything outside this subset is errRFBInput.
type rfbFilter struct {
	allowInput func() bool
	stage      int    // 0 version, 1 security type, 2 client init, 3 normal messages
	buf        []byte // an incomplete message
}

func newRFBFilter(allowInput func() bool) *rfbFilter { return &rfbFilter{allowInput: allowInput} }

// Feed takes the next client bytes and returns those to forward to the guest. Complete messages are judged when they
// complete; a partial message waits in the filter.
func (f *rfbFilter) Feed(p []byte) ([]byte, error) {
	f.buf = append(f.buf, p...)
	var out []byte
	for {
		n, forward, err := f.next()
		if err != nil {
			f.buf = nil
			return nil, err
		}
		if n == 0 {
			if len(f.buf) > rfbMaxPending {
				f.buf = nil
				return nil, errRFBInput
			}
			if len(f.buf) == 0 {
				f.buf = nil
			}
			return out, nil
		}
		out = append(out, forward...)
		f.buf = f.buf[n:]
	}
}

// next parses one message from f.buf. n == 0 means more bytes are needed; forward is nil for a dropped message.
func (f *rfbFilter) next() (n int, forward []byte, err error) {
	b := f.buf
	switch f.stage {
	case 0:
		if len(b) < 12 {
			return 0, nil, nil
		}
		if string(b[:12]) != "RFB 003.008\n" {
			return 0, nil, errRFBInput
		}
		f.stage = 1
		return 12, b[:12], nil
	case 1:
		if len(b) < 1 {
			return 0, nil, nil
		}
		if b[0] != 1 { // security type None; the guest server listens on its own loopback only
			return 0, nil, errRFBInput
		}
		f.stage = 2
		return 1, b[:1], nil
	case 2:
		if len(b) < 1 {
			return 0, nil, nil
		}
		f.stage = 3
		return 1, []byte{1}, nil // ClientInit: always shared, so a second viewer never disconnects the first
	}
	if len(b) == 0 {
		return 0, nil, nil
	}
	fixed := func(size int) (int, []byte, error) {
		if len(b) < size {
			return 0, nil, nil
		}
		return size, b[:size], nil
	}
	drop := func(size int) (int, []byte, error) {
		if len(b) < size {
			return 0, nil, nil
		}
		return size, nil, nil
	}
	switch b[0] {
	case 0: // SetPixelFormat
		return fixed(20)
	case 2: // SetEncodings
		if len(b) < 4 {
			return 0, nil, nil
		}
		count := int(binary.BigEndian.Uint16(b[2:4]))
		if count > rfbMaxEncodings {
			return 0, nil, errRFBInput
		}
		size := 4 + 4*count
		if len(b) < size {
			return 0, nil, nil
		}
		return size, stripEncodings(b[:size]), nil
	case 3, 150: // FramebufferUpdateRequest, EnableContinuousUpdates
		return fixed(10)
	case 248: // ClientFence
		if len(b) < 9 {
			return 0, nil, nil
		}
		return fixed(9 + int(b[8]))
	case 4: // KeyEvent
		return f.input(8)
	case 5: // PointerEvent
		return f.input(6)
	case 6: // ClientCutText; a negative length is the extended clipboard format
		if len(b) < 8 {
			return 0, nil, nil
		}
		length := int64(int32(binary.BigEndian.Uint32(b[4:8])))
		if length < 0 {
			length = -length
		}
		if length > rfbMaxCutText {
			return 0, nil, errRFBInput
		}
		return f.input(8 + int(length))
	case 255: // QEMU client message: only the extended key event
		if len(b) < 2 {
			return 0, nil, nil
		}
		if b[1] != 0 {
			return 0, nil, errRFBInput
		}
		return f.input(12)
	case 250: // xvp: power off, reboot, reset
		return drop(4)
	case 251: // SetDesktopSize: the bot's screen size is not the viewer's to change
		if len(b) < 8 {
			return 0, nil, nil
		}
		screens := int(b[6])
		if screens > rfbMaxScreens {
			return 0, nil, errRFBInput
		}
		return drop(8 + 16*screens)
	default:
		return 0, nil, errRFBInput
	}
}

// input forwards a complete input message only while the user holds the takeover lock.
func (f *rfbFilter) input(size int) (int, []byte, error) {
	if len(f.buf) < size {
		return 0, nil, nil
	}
	if f.allowInput() {
		return size, f.buf[:size], nil
	}
	return size, nil, nil
}

// stripEncodings removes pseudo-encodings that would change message sizes or allow power actions.
func stripEncodings(msg []byte) []byte {
	out := make([]byte, 4, len(msg))
	copy(out, msg[:4])
	for i := 4; i+4 <= len(msg); i += 4 {
		switch int32(binary.BigEndian.Uint32(msg[i : i+4])) {
		case encExtendedMouseButtons, encXvp:
			continue
		}
		out = append(out, msg[i:i+4]...)
	}
	binary.BigEndian.PutUint16(out[2:4], uint16((len(out)-4)/4))
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/ -run TestRFBFilter -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/rfbfilter.go internal/sandbox/rfbfilter_test.go
git commit -m "feat(sandbox): filter screen input so only a takeover can type

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Screen hub and viewer leases

**Files:**
- Create: `internal/sandbox/screen.go`, `internal/sandbox/viewer.go`
- Test: `internal/sandbox/screen_test.go`, `internal/sandbox/viewer_test.go`

**Interfaces:**
- Consumes: `newRFBFilter`, `errRFBInput` (Task 2); `github.com/google/uuid` (already a module dependency).
- Produces:
  ```go
  type ScreenChunk struct { ID string `json:"id"`; Data []byte `json:"data,omitempty"`; Closed bool `json:"closed,omitempty"`; Error string `json:"error,omitempty"` }
  var ErrNoScreen = errors.New("sandbox: no such screen")
  const ScreenChunkMax = 128 << 10
  func NewScreenHub(publish func(ScreenChunk), takenOver func(botID string) bool) *ScreenHub
  func (h *ScreenHub) Open(botID string, stream io.ReadWriteCloser) string
  func (h *ScreenHub) Input(id string, data []byte) error // ErrNoScreen, errRFBInput, or the write error
  func (h *ScreenHub) Close(id string)                    // no-op for unknown ids
  func (h *ScreenHub) CloseBot(botID string)
  func (h *ScreenHub) CloseAll()
  func NewViewerLeases(set func(botID string, watching bool), ttl time.Duration) *ViewerLeases
  func (v *ViewerLeases) Watch(botID string)
  func (v *ViewerLeases) Unwatch(botID string)
  func (v *ViewerLeases) Close()
  ```

RFB is client-pull: the guest sends a framebuffer update only after noVNC asks for one, so a screen stream never floods the IPC queue; chunks are capped at 128 KiB (≈171 KiB as base64, far below the 4 MiB IPC frame). At most two screens per bot; a third evicts the oldest.

- [ ] **Step 1: Write the failing tests**

Create `internal/sandbox/screen_test.go`:

```go
package sandbox

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chunkLog struct {
	mu     sync.Mutex
	chunks []ScreenChunk
}

func (l *chunkLog) publish(c ScreenChunk) { l.mu.Lock(); l.chunks = append(l.chunks, c); l.mu.Unlock() }

func (l *chunkLog) wait(t *testing.T, ok func([]ScreenChunk) bool) []ScreenChunk {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		cs := append([]ScreenChunk(nil), l.chunks...)
		l.mu.Unlock()
		if ok(cs) {
			return cs
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met; chunks: %+v", cs)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func closedFor(id string) func([]ScreenChunk) bool {
	return func(cs []ScreenChunk) bool {
		for _, c := range cs {
			if c.ID == id && c.Closed {
				return true
			}
		}
		return false
	}
}

func dataFor(cs []ScreenChunk, id string) []byte {
	var b []byte
	for _, c := range cs {
		if c.ID == id {
			b = append(b, c.Data...)
		}
	}
	return b
}

func TestScreenHubRelaysGuestOutputInBoundedChunks(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	big := bytes.Repeat([]byte{7}, 3*ScreenChunkMax+5)
	go func() { _, _ = guestEnd.Write(big) }()
	cs := log.wait(t, func(cs []ScreenChunk) bool { return len(dataFor(cs, id)) == len(big) })
	for _, c := range cs {
		if len(c.Data) > ScreenChunkMax {
			t.Fatalf("chunk of %d bytes exceeds %d", len(c.Data), ScreenChunkMax)
		}
	}
	if !bytes.Equal(dataFor(cs, id), big) {
		t.Fatal("relayed bytes differ")
	}
}

func TestScreenHubForwardsInputOnlyAsTheFilterAllows(t *testing.T) {
	var taken atomic.Bool
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(botID string) bool { return botID == "b1" && taken.Load() })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	got := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := guestEnd.Read(buf)
			if err != nil {
				close(got)
				return
			}
			got <- append([]byte(nil), buf[:n]...)
		}
	}()
	if err := h.Input(id, append([]byte("RFB 003.008\n"), 1, 1)); err != nil {
		t.Fatal(err)
	}
	if b := <-got; !bytes.Equal(b, append([]byte("RFB 003.008\n"), 1, 1)) {
		t.Fatalf("handshake = %v", b)
	}
	keyEvent := []byte{4, 1, 0, 0, 0, 0, 0xff, 0x0d}
	if err := h.Input(id, keyEvent); err != nil { // view-only: dropped, no error
		t.Fatal(err)
	}
	taken.Store(true)
	if err := h.Input(id, keyEvent); err != nil {
		t.Fatal(err)
	}
	if b := <-got; !bytes.Equal(b, keyEvent) { // the first key never arrived; this is the second
		t.Fatalf("after take over the guest got %v", b)
	}
}

func TestScreenHubCloseEndsTheStreamOnce(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	h.Close(id)
	h.Close(id) // idempotent
	cs := log.wait(t, closedFor(id))
	n := 0
	for _, c := range cs {
		if c.Closed {
			n++
			if c.Error != "" {
				t.Fatalf("user close reported an error: %q", c.Error)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d closed chunks, want 1", n)
	}
	if _, err := guestEnd.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("guest end still open: %v", err)
	}
	if err := h.Input(id, []byte{3}); !errors.Is(err, ErrNoScreen) {
		t.Fatalf("input after close: %v", err)
	}
}

func TestScreenHubReportsWhenTheGuestEndsTheStream(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	_ = guestEnd.Close()
	log.wait(t, closedFor(id))
	if err := h.Input(id, []byte{3}); !errors.Is(err, ErrNoScreen) {
		t.Fatalf("screen still registered: %v", err)
	}
}

func TestScreenHubKeepsTwoScreensPerBotAndClosesPerBot(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	t.Cleanup(h.CloseAll)
	open := func(bot string) string { a, _ := net.Pipe(); return h.Open(bot, a) }
	first, second, other := open("b1"), open("b1"), open("b2")
	third := open("b1")
	cs := log.wait(t, closedFor(first))
	for _, c := range cs {
		if c.ID == first && c.Closed && c.Error == "" {
			t.Fatal("evicted screen should say why")
		}
	}
	h.CloseBot("b1")
	log.wait(t, closedFor(second))
	log.wait(t, closedFor(third))
	if err := h.Input(other, nil); errors.Is(err, ErrNoScreen) { // nil input: nothing is written to the unread pipe
		t.Fatal("CloseBot closed another bot's screen")
	}
}

func TestScreenHubClosesTheStreamOnBadInput(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return true })
	hostEnd, _ := net.Pipe()
	id := h.Open("b1", hostEnd)
	if err := h.Input(id, []byte("HTTP/1.1 GET\n")); !errors.Is(err, errRFBInput) {
		t.Fatalf("err = %v", err)
	}
	log.wait(t, closedFor(id))
}
```

Create `internal/sandbox/viewer_test.go`:

```go
package sandbox

import (
	"sync"
	"testing"
	"time"
)

type viewerLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *viewerLog) set(botID string, watching bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if watching {
		l.calls = append(l.calls, botID+":on")
	} else {
		l.calls = append(l.calls, botID+":off")
	}
}

func (l *viewerLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.calls) == 0 {
		return ""
	}
	return l.calls[len(l.calls)-1]
}

func TestViewerLeaseExpiresWithoutRenewal(t *testing.T) {
	log := &viewerLog{}
	v := NewViewerLeases(log.set, 40*time.Millisecond)
	t.Cleanup(v.Close)
	v.Watch("b1")
	if log.last() != "b1:on" {
		t.Fatalf("watch: %q", log.last())
	}
	for range 4 { // renewals keep it alive past several TTLs
		time.Sleep(20 * time.Millisecond)
		v.Watch("b1")
	}
	if log.last() != "b1:on" {
		t.Fatalf("renewed lease expired: %q", log.last())
	}
	deadline := time.Now().Add(2 * time.Second)
	for log.last() != "b1:off" {
		if time.Now().After(deadline) {
			t.Fatal("lease never expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestViewerUnwatchClearsAtOnceAndCloseStopsTimers(t *testing.T) {
	log := &viewerLog{}
	v := NewViewerLeases(log.set, time.Hour)
	v.Watch("b1")
	v.Unwatch("b1")
	if log.last() != "b1:off" {
		t.Fatalf("unwatch: %q", log.last())
	}
	v.Watch("b2")
	v.Close()
	before := log.last()
	v.Watch("b3") // after Close: ignored
	if log.last() != before {
		t.Fatal("watch after Close reached the manager")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/sandbox/ -run "TestScreenHub|TestViewer" -v`
Expected: FAIL, `undefined: NewScreenHub`, `undefined: NewViewerLeases`.

- [ ] **Step 3: Implement**

Create `internal/sandbox/screen.go`:

```go
package sandbox

import (
	"bytes"
	"errors"
	"io"
	"sync"

	"github.com/google/uuid"
)

// ScreenChunk is live screen output for the UI, published as the sandbox.screen.data event. The last chunk of a
// stream has Closed set; Error says why when it did not end because the user closed it.
type ScreenChunk struct {
	ID     string `json:"id"`
	Data   []byte `json:"data,omitempty"` // base64 in JSON
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// ErrNoScreen is returned for input to a screen that is not open (closed, evicted or never opened).
var ErrNoScreen = errors.New("sandbox: no such screen")

const (
	// ScreenChunkMax bounds one sandbox.screen.data event. RFB is client-pull (the guest sends an update only
	// after the viewer asks for one), so a stream cannot flood the IPC queue.
	ScreenChunkMax = 128 << 10
	screensPerBot  = 2
)

// ScreenHub relays the guest's VNC stream for open Computer tabs. Guest bytes go out as events; user bytes come in
// through Input and pass the RFB filter, which lets keyboard and mouse through only while the user holds the
// takeover lock.
type ScreenHub struct {
	publish   func(ScreenChunk)
	takenOver func(botID string) bool

	mu      sync.Mutex
	screens map[string]*screenStream
	order   []string // open ids, oldest first
}

type screenStream struct {
	id, botID string
	rwc       io.ReadWriteCloser
	filter    *rfbFilter

	inMu sync.Mutex // orders Input calls through the filter

	pubMu  sync.Mutex // no data chunk is published after the closed chunk
	closed bool
}

// NewScreenHub creates a hub. publish must not block for long; takenOver is Manager.TakenOver.
func NewScreenHub(publish func(ScreenChunk), takenOver func(botID string) bool) *ScreenHub {
	return &ScreenHub{publish: publish, takenOver: takenOver, screens: map[string]*screenStream{}}
}

// Open starts relaying stream (from Guest.OpenScreen) and returns the screen id. A bot keeps at most two open
// screens; the oldest is closed when a third opens.
func (h *ScreenHub) Open(botID string, stream io.ReadWriteCloser) string {
	s := &screenStream{id: uuid.NewString(), botID: botID, rwc: stream}
	s.filter = newRFBFilter(func() bool { return h.takenOver(botID) })
	var evict []*screenStream
	h.mu.Lock()
	h.screens[s.id] = s
	h.order = append(h.order, s.id)
	count := 0
	for i := len(h.order) - 1; i >= 0; i-- { // newest first
		o := h.screens[h.order[i]]
		if o.botID != botID {
			continue
		}
		if count++; count > screensPerBot {
			evict = append(evict, o)
		}
	}
	for _, o := range evict {
		h.removeLocked(o.id)
	}
	h.mu.Unlock()
	for _, o := range evict {
		h.finish(o, "the screen was opened somewhere else")
	}
	go h.pump(s)
	return s.id
}

// Input passes user bytes through the RFB filter to the guest.
func (h *ScreenHub) Input(id string, data []byte) error {
	h.mu.Lock()
	s := h.screens[id]
	h.mu.Unlock()
	if s == nil {
		return ErrNoScreen
	}
	s.inMu.Lock()
	defer s.inMu.Unlock()
	forward, err := s.filter.Feed(data)
	if err != nil {
		h.drop(s, "the screen received unexpected input")
		return err
	}
	if len(forward) == 0 {
		return nil
	}
	if _, err := s.rwc.Write(forward); err != nil {
		h.drop(s, "the screen stream ended unexpectedly")
		return err
	}
	return nil
}

// Close ends a screen the user closed. Unknown ids are ignored.
func (h *ScreenHub) Close(id string) {
	h.mu.Lock()
	s := h.screens[id]
	if s != nil {
		h.removeLocked(id)
	}
	h.mu.Unlock()
	if s != nil {
		h.finish(s, "")
	}
}

// CloseBot ends every screen of a bot whose computer stopped.
func (h *ScreenHub) CloseBot(botID string) { h.closeWhere(func(s *screenStream) bool { return s.botID == botID }, "the computer stopped") }

// CloseAll ends every screen (daemon shutdown).
func (h *ScreenHub) CloseAll() { h.closeWhere(func(*screenStream) bool { return true }, "the computer stopped") }

func (h *ScreenHub) closeWhere(match func(*screenStream) bool, reason string) {
	var gone []*screenStream
	h.mu.Lock()
	for _, id := range append([]string(nil), h.order...) {
		if s := h.screens[id]; match(s) {
			gone = append(gone, s)
			h.removeLocked(id)
		}
	}
	h.mu.Unlock()
	for _, s := range gone {
		h.finish(s, reason)
	}
}

func (h *ScreenHub) pump(s *screenStream) {
	buf := make([]byte, ScreenChunkMax)
	for {
		n, err := s.rwc.Read(buf)
		if n > 0 {
			s.emit(h, ScreenChunk{ID: s.id, Data: bytes.Clone(buf[:n])}, false)
		}
		if err != nil {
			reason := ""
			if !errors.Is(err, io.EOF) {
				reason = "the screen stream ended unexpectedly"
			}
			h.drop(s, reason)
			return
		}
	}
}

// drop removes s if it is still registered and ends it.
func (h *ScreenHub) drop(s *screenStream, reason string) {
	h.mu.Lock()
	if h.screens[s.id] == s {
		h.removeLocked(s.id)
	}
	h.mu.Unlock()
	h.finish(s, reason)
}

// finish publishes the closed chunk (once) and closes the guest stream.
func (h *ScreenHub) finish(s *screenStream, reason string) {
	s.emit(h, ScreenChunk{ID: s.id, Closed: true, Error: reason}, true)
	_ = s.rwc.Close()
}

func (s *screenStream) emit(h *ScreenHub, c ScreenChunk, final bool) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.closed {
		return
	}
	if final {
		s.closed = true
	}
	h.publish(c)
}

// removeLocked forgets id. h.mu must be held.
func (h *ScreenHub) removeLocked(id string) {
	delete(h.screens, id)
	for i, o := range h.order {
		if o == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}
```

Create `internal/sandbox/viewer.go`:

```go
package sandbox

import (
	"sync"
	"time"
)

// ViewerLeases keeps Manager's viewer flag honest when the GUI goes away without saying so: the Computer tab renews
// its lease every minute, and a lease not renewed within ttl clears the flag, so the idle stop still happens.
type ViewerLeases struct {
	set func(botID string, watching bool)
	ttl time.Duration

	mu     sync.Mutex
	timers map[string]*time.Timer
	closed bool
}

// NewViewerLeases creates leases over set (Manager.SetViewer).
func NewViewerLeases(set func(botID string, watching bool), ttl time.Duration) *ViewerLeases {
	return &ViewerLeases{set: set, ttl: ttl, timers: map[string]*time.Timer{}}
}

// Watch starts or renews botID's lease.
func (v *ViewerLeases) Watch(botID string) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	if t := v.timers[botID]; t != nil {
		t.Stop()
	}
	var t *time.Timer
	t = time.AfterFunc(v.ttl, func() {
		v.mu.Lock()
		mine := v.timers[botID] == t && !v.closed
		if mine {
			delete(v.timers, botID)
		}
		v.mu.Unlock()
		if mine {
			v.set(botID, false)
		}
	})
	v.timers[botID] = t
	v.mu.Unlock()
	v.set(botID, true)
}

// Unwatch ends botID's lease now.
func (v *ViewerLeases) Unwatch(botID string) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	if t := v.timers[botID]; t != nil {
		t.Stop()
		delete(v.timers, botID)
	}
	v.mu.Unlock()
	v.set(botID, false)
}

// Close stops every timer without touching the manager (daemon shutdown stops the VMs anyway).
func (v *ViewerLeases) Close() {
	v.mu.Lock()
	v.closed = true
	for id, t := range v.timers {
		t.Stop()
		delete(v.timers, id)
	}
	v.mu.Unlock()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/ -run "TestScreenHub|TestViewer|TestRFBFilter" -v -count=3`
Expected: PASS, no race reports.

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/screen.go internal/sandbox/viewer.go internal/sandbox/screen_test.go internal/sandbox/viewer_test.go
git commit -m "feat(sandbox): relay the live screen and lease the viewer flag

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Exec echo hook on the guest client

**Files:**
- Modify: `internal/sandbox/guestclient.go` (the `Guest` struct and `Exec`), `internal/sandbox/manager.go` (`Config` and the place where a booted VM's guest is created with `NewGuest`)
- Test: `internal/sandbox/guest_exec_hook_test.go`

**Interfaces:**
- Consumes: `NewGuest(conn net.Conn, token string) (*Guest, error)`, `Guest.Exec`, `wire.ExecRequest`, `wire.ExecResult`, `wire.Health` (phase 1 contract).
- Produces:
  ```go
  func (g *Guest) OnExec(fn func(req wire.ExecRequest, res wire.ExecResult)) // called after every successful Exec
  // Config field:
  OnExec func(botID string, req wire.ExecRequest, res wire.ExecResult) // optional
  ```

- [ ] **Step 1: Write the failing test**

Create `internal/sandbox/guest_exec_hook_test.go` (a self-contained h2c guest, independent of phase 1 test helpers):

```go
package sandbox

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestGuestReportsEachExecToItsHook(t *testing.T) {
	client, server := net.Pipe()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Orole-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(wire.Health{Version: "test", Ready: true})
		case "/exec":
			var req wire.ExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Command == "fail" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(wire.ExecResult{Stdout: "2\n", ExitCode: 0})
		default:
			http.NotFound(w, r)
		}
	})
	go (&http2.Server{}).ServeConn(server, &http2.ServeConnOpts{Handler: h})
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	g, err := NewGuest(client, "tok")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	g.OnExec(func(req wire.ExecRequest, res wire.ExecResult) {
		mu.Lock()
		seen = append(seen, req.Command+"|"+res.Stdout)
		mu.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := g.Exec(ctx, wire.ExecRequest{Command: `python -c "print(1+1)"`}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec(ctx, wire.ExecRequest{Command: "fail"}); err == nil {
		t.Fatal("a failed exec reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != `python -c "print(1+1)"|2`+"\n" {
		t.Fatalf("hook saw %q, want only the successful exec", seen)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race ./internal/sandbox/ -run TestGuestReportsEachExecToItsHook -v`
Expected: FAIL, `g.OnExec undefined`.

- [ ] **Step 3: Implement**

In `internal/sandbox/guestclient.go`, add to the `Guest` struct:

```go
	hookMu sync.RWMutex
	onExec func(wire.ExecRequest, wire.ExecResult)
```

and the method:

```go
// OnExec registers fn to be told about every command that ran successfully (the daemon echoes them in the bot's
// Computer terminal). fn runs on the caller's goroutine after Exec has its result; it must not block.
func (g *Guest) OnExec(fn func(req wire.ExecRequest, res wire.ExecResult)) {
	g.hookMu.Lock()
	g.onExec = fn
	g.hookMu.Unlock()
}
```

In `Guest.Exec`, immediately before the final successful `return res, nil` (after the response was decoded and status checked), add:

```go
	g.hookMu.RLock()
	hook := g.onExec
	g.hookMu.RUnlock()
	if hook != nil {
		hook(req, res)
	}
```

(Add `"sync"` to the imports if phase 1 did not import it.)

In `internal/sandbox/manager.go`, add to `Config`:

```go
	// OnExec, when set, is told about every command a bot's computer ran (the daemon echoes it in the terminal).
	OnExec func(botID string, req wire.ExecRequest, res wire.ExecResult)
```

and at the single place where the manager creates the guest for a freshly booted VM (`g, err := NewGuest(vm.Conn(), spec.Token)` or phase 1's equivalent), right after the error check:

```go
	if m.cfg.OnExec != nil {
		onExec, id := m.cfg.OnExec, botID
		g.OnExec(func(req wire.ExecRequest, res wire.ExecResult) { onExec(id, req, res) })
	}
```

(`m.cfg` / `botID` are phase 1's names for the stored config and the bot being booted; use the actual identifiers there.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/sandbox/ -v`
Expected: PASS (all sandbox tests, including phase 1's).

- [ ] **Step 5: Commit**

```bash
git add internal/sandbox/guestclient.go internal/sandbox/manager.go internal/sandbox/guest_exec_hook_test.go
git commit -m "feat(sandbox): report executed commands for the terminal echo

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Daemon IPC for viewer, screen, terminal and echo

**Files:**
- Create: `internal/daemon/svc_sandbox_streams.go`
- Modify: `internal/daemon/daemon.go` (Daemon fields; teardown), `internal/daemon/svc_sandbox.go` (call `registerSandboxStreams`, set `OnExec`)
- Test: `internal/daemon/svc_sandbox_streams_test.go` (package `daemon`)

**Interfaces:**
- Consumes: `d.Sandbox` (`Status`, `Ensure`, `SetViewer`, `TakenOver`, `Subscribe`), `Guest.OpenScreen`, `Guest.OpenPTY` (phase 1-3); `sandbox.NewScreenHub`, `sandbox.NewViewerLeases`, `sandbox.ErrNoScreen`, `sandbox.ScreenChunk` (Task 3); `terminals.Manager.Adopt/Inject` (Task 1); `jarvisDaemon`, `dialDaemon` test helpers (`svc_jarvis_test.go`).
- Produces (IPC; names are the master contract's):
  - `sandbox.viewer{botId, watching}` → `{}`
  - `sandbox.screen.open{botId}` → `{id}`; errors: `bad_request` (no botId), `not_found` ("the computer is off…")
  - `sandbox.screen.input{id, data(base64 bytes)}` → `{}`; `not_found` for unknown id, `bad_request` for > 1 MiB or filtered-out input
  - `sandbox.screen.close{id}` → `{}` (idempotent)
  - `sandbox.pty.open{botId, cols, rows}` → `terminals.Info` (owner `sandbox:<botId>`; an existing running one is returned instead of opening a second)
  - Events: `sandbox.screen.data` (`sandbox.ScreenChunk`), `terminal.changed` for the adopted terminal; `sandbox.status` (published by phase 2; verified in Step 3)
  - Go: `func formatExecEcho(req wire.ExecRequest, res wire.ExecResult) []byte`

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/svc_sandbox_streams_test.go`:

```go
package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestFormatExecEcho(t *testing.T) {
	tests := []struct {
		name string
		req  wire.ExecRequest
		res  wire.ExecResult
		want string
	}{
		{name: "plain", req: wire.ExecRequest{Command: `python -c "print(1+1)"`}, res: wire.ExecResult{Stdout: "2\n"},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ python -c \"print(1+1)\"\r\n2\r\n\x1b[2m(exit 0)\x1b[0m\r\n"},
		{name: "control characters removed from the command", req: wire.ExecRequest{Command: "ls\x1b[2J\x9b1m\nrm"}, res: wire.ExecResult{},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ ls[2J1m rm\r\n\x1b[2m(exit 0)\x1b[0m\r\n"},
		{name: "stderr, exit code, timeout and cut output", req: wire.ExecRequest{Command: "sleep 999"},
			res:  wire.ExecResult{Stdout: "a", Stderr: "b\r\nc", ExitCode: 124, TimedOut: true, Truncated: true},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ sleep 999\r\nab\r\nc\r\n\x1b[2m(exit 124, timed out, output cut)\x1b[0m\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(formatExecEcho(tt.req, tt.res)); got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
	big := formatExecEcho(wire.ExecRequest{Command: "yes"}, wire.ExecResult{Stdout: strings.Repeat("y\n", 100_000)})
	if len(big) > execEchoMax+256 || !strings.Contains(string(big), "output cut") {
		t.Fatalf("echo of %d bytes is not capped", len(big))
	}
}

func TestSandboxStreamsRefuseWhatTheyCannotServe(t *testing.T) {
	d := jarvisDaemon(t)
	c := dialDaemon(t, d)
	ctx := context.Background()
	code := func(err error) string {
		var ie *ipc.Error
		if errors.As(err, &ie) {
			return ie.Code
		}
		return ""
	}
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "nobot"}, nil); code(err) != ipc.CodeNotFound {
		t.Fatalf("screen.open on an off computer: %v", err)
	}
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": "nobot", "cols": 80, "rows": 24}, nil); code(err) != ipc.CodeNotFound {
		t.Fatalf("pty.open on an off computer: %v", err)
	}
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": ""}, nil); code(err) != ipc.CodeBadRequest {
		t.Fatalf("pty.open without a bot: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": "missing", "data": []byte{3}}, nil); code(err) != ipc.CodeNotFound {
		t.Fatalf("input to an unknown screen: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": "missing", "data": make([]byte, 1<<20+1)}, nil); code(err) != ipc.CodeBadRequest {
		t.Fatalf("oversized input: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.close", map[string]string{"id": "missing"}, nil); err != nil {
		t.Fatalf("closing an unknown screen: %v", err)
	}
	for _, watching := range []bool{true, false} {
		if err := c.Call(ctx, "sandbox.viewer", map[string]any{"botId": "nobot", "watching": watching}, nil); err != nil {
			t.Fatalf("viewer %v: %v", watching, err)
		}
	}
	if err := c.Call(ctx, "sandbox.viewer", map[string]any{"botId": "", "watching": true}, nil); code(err) != ipc.CodeBadRequest {
		t.Fatalf("viewer without a bot: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -race ./internal/daemon/ -run "TestFormatExecEcho|TestSandboxStreams" -v`
Expected: FAIL, `undefined: formatExecEcho`, `undefined: execEchoMax`.

- [ ] **Step 3: Check what phase 2 already publishes**

Run: `grep -n "sandbox.status\|Subscribe\|sandbox.setup.enable\|sandbox.runtime.install" internal/daemon/svc_sandbox.go internal/app/bridge.go`
Expected: `svc_sandbox.go` shows `d.Sandbox.Subscribe(func(st sandbox.Status) { d.IPC.Publish("sandbox.status", st) })`. If that line is missing, Step 4's `registerSandboxStreams` publishes it (the code below includes it guarded by a comment; delete the publish line there if phase 2 already has one, so each status goes out once). Note whether `bridge.go` already lists the two setup methods (Task 6 uses this).

- [ ] **Step 4: Implement**

Create `internal/daemon/svc_sandbox_streams.go`:

```go
package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

const (
	// sandboxViewerTTL is how long a Computer tab's viewer lease lasts without renewal (the UI renews every 60 s).
	sandboxViewerTTL = 150 * time.Second
	// screenInputMax bounds one sandbox.screen.input call.
	screenInputMax = 1 << 20
	// execEchoMax bounds the output echoed for one command.
	execEchoMax = 64 << 10
)

type screenRef struct {
	ID string `json:"id"`
}

// registerSandboxStreams wires what the Computer tab needs on top of registerSandbox: the viewer lease, the live
// screen and the guest terminal. registerSandbox calls it last.
func (d *Daemon) registerSandboxStreams() {
	d.sandboxScreens = sandbox.NewScreenHub(func(c sandbox.ScreenChunk) { d.IPC.Publish("sandbox.screen.data", c) }, d.Sandbox.TakenOver)
	d.sandboxViewers = sandbox.NewViewerLeases(d.Sandbox.SetViewer, sandboxViewerTTL)
	d.Sandbox.Subscribe(func(st sandbox.Status) {
		// Delete the next line if registerSandbox already publishes sandbox.status (see Task 5, Step 3).
		d.IPC.Publish("sandbox.status", st)
		if st.State != sandbox.StateRunning {
			d.sandboxScreens.CloseBot(st.BotID)
		}
	})

	handle(d, "sandbox.viewer", func(ctx context.Context, p struct {
		BotID    string `json:"botId"`
		Watching bool   `json:"watching"`
	}) (none, error) {
		if p.BotID == "" {
			return none{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "botId is required"}
		}
		if p.Watching {
			d.sandboxViewers.Watch(p.BotID)
		} else {
			d.sandboxViewers.Unwatch(p.BotID)
		}
		return none{}, nil
	})

	handle(d, "sandbox.screen.open", func(ctx context.Context, p struct {
		BotID string `json:"botId"`
	}) (screenRef, error) {
		g, err := d.runningGuest(ctx, p.BotID)
		if err != nil {
			return screenRef{}, err
		}
		stream, err := g.OpenScreen(context.WithoutCancel(ctx)) // outlives this call
		if err != nil {
			return screenRef{}, err
		}
		id := d.sandboxScreens.Open(p.BotID, stream)
		d.audit("sandbox.screen.open", p.BotID, "allowed", id)
		return screenRef{ID: id}, nil
	})

	handle(d, "sandbox.screen.input", func(ctx context.Context, p struct {
		ID   string `json:"id"`
		Data []byte `json:"data"`
	}) (none, error) {
		if len(p.Data) > screenInputMax {
			return none{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "screen input too large"}
		}
		err := d.sandboxScreens.Input(p.ID, p.Data)
		switch {
		case err == nil:
			return none{}, nil
		case errors.Is(err, sandbox.ErrNoScreen):
			return none{}, &ipc.Error{Code: ipc.CodeNotFound, Message: "the screen is closed"}
		default:
			return none{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "the screen was closed: " + err.Error()}
		}
	})

	handle(d, "sandbox.screen.close", func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (none, error) {
		d.sandboxScreens.Close(p.ID)
		return none{}, nil
	})

	handle(d, "sandbox.pty.open", func(ctx context.Context, p struct {
		BotID string `json:"botId"`
		Cols  int    `json:"cols"`
		Rows  int    `json:"rows"`
	}) (terminals.Info, error) {
		if p.BotID == "" {
			return terminals.Info{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "botId is required"}
		}
		if info, ok := d.sandboxTerminal(p.BotID); ok {
			return info, nil
		}
		g, err := d.runningGuest(ctx, p.BotID)
		if err != nil {
			return terminals.Info{}, err
		}
		proc, err := g.OpenPTY(context.WithoutCancel(ctx), p.Cols, p.Rows)
		if err != nil {
			return terminals.Info{}, err
		}
		info, err := d.Terminals.Adopt(proc, terminals.AdoptOptions{Title: "Computer", Shell: "orole-guest",
			Owner: sandboxOwner(p.BotID), Cols: p.Cols, Rows: p.Rows})
		if err != nil {
			return terminals.Info{}, err
		}
		d.audit("sandbox.pty.open", p.BotID, "allowed", info.ID)
		d.IPC.Publish("terminal.changed", info)
		return info, nil
	})
}

func sandboxOwner(botID string) string { return "sandbox:" + botID }

// runningGuest returns the guest of a computer that is already running. The UI never boots one: a computer starts
// when its bot first uses it.
func (d *Daemon) runningGuest(ctx context.Context, botID string) (*sandbox.Guest, error) {
	if botID == "" {
		return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: "botId is required"}
	}
	if d.Sandbox.Status(botID).State != sandbox.StateRunning {
		return nil, &ipc.Error{Code: ipc.CodeNotFound, Message: "the computer is off; it starts when the bot uses it"}
	}
	return d.Sandbox.Ensure(ctx, botID)
}

// sandboxTerminal finds the running Computer terminal of a bot.
func (d *Daemon) sandboxTerminal(botID string) (terminals.Info, bool) {
	owner := sandboxOwner(botID)
	for _, in := range d.Terminals.List() {
		if in.Owner == owner && in.State == terminals.Running {
			return in, true
		}
	}
	return terminals.Info{}, false
}

// echoSandboxExec shows a command the bot ran, with its output, in the bot's open Computer terminal. Nothing is
// kept when no terminal is open.
func (d *Daemon) echoSandboxExec(botID string, req wire.ExecRequest, res wire.ExecResult) {
	if info, ok := d.sandboxTerminal(botID); ok {
		_ = d.Terminals.Inject(info.ID, formatExecEcho(req, res))
	}
}

// formatExecEcho renders one bot command for the terminal: a dim "[bot]" prompt, the command on one line with
// control characters removed (the bot chose it; it must not drive the terminal), the output with bare newlines
// turned into CRLF and capped at execEchoMax, and a dim status line.
func formatExecEcho(req wire.ExecRequest, res wire.ExecResult) []byte {
	var b bytes.Buffer
	b.WriteString("\r\n\x1b[2m[bot]\x1b[0m $ ")
	b.WriteString(echoLine(req.Command))
	b.WriteString("\r\n")
	out := res.Stdout + res.Stderr
	cut := res.Truncated
	if len(out) > execEchoMax {
		out, cut = out[:execEchoMax], true
	}
	out = strings.ReplaceAll(strings.ReplaceAll(out, "\r\n", "\n"), "\n", "\r\n")
	b.WriteString(out)
	if out != "" && !strings.HasSuffix(out, "\r\n") {
		b.WriteString("\r\n")
	}
	notes := []string{fmt.Sprintf("exit %d", res.ExitCode)}
	if res.TimedOut {
		notes = append(notes, "timed out")
	}
	if cut {
		notes = append(notes, "output cut")
	}
	fmt.Fprintf(&b, "\x1b[2m(%s)\x1b[0m\r\n", strings.Join(notes, ", "))
	return b.Bytes()
}

func echoLine(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			return -1
		}
		return r
	}, s)
}
```

In `internal/daemon/daemon.go`, add to the `Daemon` struct (next to the phase 2 `Sandbox` field):

```go
	sandboxScreens *sandbox.ScreenHub    // live screens of open Computer tabs
	sandboxViewers *sandbox.ViewerLeases // Computer tab viewer leases
```

and in the teardown, immediately before phase 2's `d.Sandbox.Shutdown(...)` call:

```go
	if d.sandboxViewers != nil {
		d.sandboxViewers.Close()
	}
	if d.sandboxScreens != nil {
		d.sandboxScreens.CloseAll()
	}
```

In `internal/daemon/svc_sandbox.go`: in the `sandbox.Config{...}` literal add `OnExec: d.echoSandboxExec,`, and make `d.registerSandboxStreams()` the last statement of `registerSandbox()`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/daemon/ -run "TestFormatExecEcho|TestSandboxStreams" -v` then `go vet ./internal/daemon/`
Expected: PASS; vet clean.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/svc_sandbox_streams.go internal/daemon/svc_sandbox_streams_test.go internal/daemon/daemon.go internal/daemon/svc_sandbox.go
git commit -m "feat(daemon): stream the bot computer screen and terminal over ipc

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: MyGo `Sandbox` service and events

**Files:**
- Create: `internal/app/sandbox.go`, `internal/app/sandbox_test.go`
- Modify: `internal/app/bridge.go` (subscribe list, `forward`, long timeouts), `cmd/desktop/main.go` (bind)
- Regenerate: `frontend/src/mygo.ts`

**Interfaces:**
- Consumes: IPC methods from phase 2/3 (`sandbox.availability`, `sandbox.status`, `sandbox.setup.enable`, `sandbox.runtime.install`, `sandbox.takeover`, `sandbox.release`) and Task 5.
- Produces (TS after generate, in `@/mygo`):
  ```ts
  Sandbox.availability(): Promise<Availability>            // { ready: boolean; reason?: string; detail?: string }
  Sandbox.status(botID: string): Promise<SandboxStatus>    // { botId; state: "off"|"starting"|"running"|"stopping"|"failed"; takenOver: boolean; error?: string }
  Sandbox.enable(): Promise<void>                           // sandbox.setup.enable (may wait for UAC)
  Sandbox.install(): Promise<void>                          // sandbox.runtime.install (download)
  Sandbox.setViewer(botID: string, watching: boolean): Promise<void>
  Sandbox.takeOver(botID: string): Promise<void>
  Sandbox.release(botID: string): Promise<void>
  Sandbox.openScreen(botID: string): Promise<string>        // screen id
  Sandbox.screenInput(id: string, data: string): Promise<void> // data = base64
  Sandbox.closeScreen(id: string): Promise<void>
  Sandbox.openTerminal(botID: string, cols: number, rows: number): Promise<Info> // terminals.Info
  events.sandboxStatus  // event<SandboxStatus>("sandbox:status")
  events.sandboxScreen  // event<ScreenChunk>("sandbox:screen"): { id: string; data?: string; closed?: boolean; error?: string }
  ```
  (Generated TS type names may carry a package prefix; frontend code derives them from the functions.)

- [ ] **Step 1: Write the failing test**

Create `internal/app/sandbox_test.go`:

```go
package app

import (
	"encoding/json"
	"testing"
)

func TestForwardSandbox(t *testing.T) {
	for _, ev := range []string{"sandbox.status", "sandbox.screen.data"} {
		if !forwardSandbox(ev, json.RawMessage(`{"id":"s1","botId":"b1","data":"AQID"}`)) {
			t.Fatalf("%q must be routed", ev)
		}
		if !forwardSandbox(ev, json.RawMessage(`not json`)) {
			t.Fatalf("malformed %q is still consumed", ev)
		}
	}
	for _, ev := range []string{"sandbox.other", "terminal.output", ""} {
		if forwardSandbox(ev, json.RawMessage(`{}`)) {
			t.Fatalf("%q must not be routed", ev)
		}
	}
}

func TestScreenInputRejectsNonBase64(t *testing.T) {
	err := Sandbox{L: NewLink(t.TempDir())}.ScreenInput(t.Context(), "s1", "not base64!")
	if err == nil {
		t.Fatal("non-base64 input was sent")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race ./internal/app/ -run "TestForwardSandbox|TestScreenInput" -v`
Expected: FAIL, `undefined: forwardSandbox`.

- [ ] **Step 3: Implement**

Create `internal/app/sandbox.go`:

```go
package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

var (
	// SandboxStatus fires when a bot's computer changes state or the user takes over or hands back.
	SandboxStatus = mygo.NewEvent[sandbox.Status]("sandbox:status")
	// SandboxScreen carries live screen bytes for an open screen; the last chunk of a stream has Closed set.
	SandboxScreen = mygo.NewEvent[ScreenChunk]("sandbox:screen")
)

// ScreenChunk is a piece of a bot computer's screen stream; Data is base64.
type ScreenChunk struct {
	ID     string `json:"id"`
	Data   string `json:"data,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

func forwardSandbox(event string, data json.RawMessage) bool {
	switch event {
	case "sandbox.status":
		var s sandbox.Status
		if json.Unmarshal(data, &s) == nil {
			SandboxStatus.Broadcast(s)
		}
	case "sandbox.screen.data":
		var c sandbox.ScreenChunk
		if json.Unmarshal(data, &c) == nil {
			SandboxScreen.Broadcast(ScreenChunk{ID: c.ID, Data: string(mustB64(c.Data)), Closed: c.Closed, Error: c.Error})
		}
	default:
		return false
	}
	return true
}

// Sandbox is the bound service for a bot's computer, a local Linux VM the daemon runs. It only forwards: the daemon
// decides what a viewer may do (keyboard and mouse reach the VM only during a takeover).
type Sandbox struct{ L *Link }

// Availability says whether bot computers can run on this PC, and why not.
func (s Sandbox) Availability(ctx context.Context) (sandbox.Availability, error) {
	var out sandbox.Availability
	return out, s.L.call(ctx, "sandbox.availability", struct{}{}, &out)
}

// Status is the bot's computer state.
func (s Sandbox) Status(ctx context.Context, botID string) (sandbox.Status, error) {
	var out sandbox.Status
	return out, s.L.call(ctx, "sandbox.status", map[string]string{"botId": botID}, &out)
}

// Enable turns on Windows Hypervisor Platform through the elevated helper (one UAC prompt, then a restart).
func (s Sandbox) Enable(ctx context.Context) error {
	return s.L.call(ctx, "sandbox.setup.enable", struct{}{}, nil)
}

// Install downloads and verifies the VM runtime and the guest image.
func (s Sandbox) Install(ctx context.Context) error {
	return s.L.call(ctx, "sandbox.runtime.install", struct{}{}, nil)
}

// SetViewer renews (true) or ends (false) this window's viewer lease while the Computer tab is visible.
func (s Sandbox) SetViewer(ctx context.Context, botID string, watching bool) error {
	return s.L.call(ctx, "sandbox.viewer", map[string]any{"botId": botID, "watching": watching}, nil)
}

// TakeOver gives the user the mouse and keyboard; the bot's browser tools wait.
func (s Sandbox) TakeOver(ctx context.Context, botID string) error {
	return s.L.call(ctx, "sandbox.takeover", map[string]string{"botId": botID}, nil)
}

// Release hands the computer back to the bot.
func (s Sandbox) Release(ctx context.Context, botID string) error {
	return s.L.call(ctx, "sandbox.release", map[string]string{"botId": botID}, nil)
}

// OpenScreen starts a live screen stream of a running computer and returns its id.
func (s Sandbox) OpenScreen(ctx context.Context, botID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := s.L.call(ctx, "sandbox.screen.open", map[string]string{"botId": botID}, &out)
	return out.ID, err
}

// ScreenInput sends viewer bytes (base64) to a screen.
func (s Sandbox) ScreenInput(ctx context.Context, id, data string) error {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("screen input is not base64: %w", err)
	}
	return s.L.call(ctx, "sandbox.screen.input", map[string]any{"id": id, "data": b}, nil)
}

// CloseScreen ends a screen stream.
func (s Sandbox) CloseScreen(ctx context.Context, id string) error {
	return s.L.call(ctx, "sandbox.screen.close", map[string]string{"id": id}, nil)
}

// OpenTerminal opens (or returns the open) terminal in the bot's running computer. Use Terminals.* with its id.
func (s Sandbox) OpenTerminal(ctx context.Context, botID string, cols, rows int) (terminals.Info, error) {
	var out terminals.Info
	return out, s.L.call(ctx, "sandbox.pty.open", map[string]any{"botId": botID, "cols": cols, "rows": rows}, &out)
}
```

In `internal/app/bridge.go`:
- In `Link.Start`, add `"sandbox.*"` to the `c.Subscribe(...)` list (after `"dictation.*"`).
- In `forward`, change the first line's condition to `if forwardPlugins(event, data) || forwardChat(event, data) || forwardDictation(event, data) || forwardSandbox(event, data) {`.
- In `Link.call`'s long-timeout `case`, append `"sandbox.setup.enable", "sandbox.runtime.install"` to the list unless Task 5 Step 3 showed they are already there.

In `cmd/desktop/main.go`, after `mygo.Bind(app.Machines{L: link})` add `mygo.Bind(app.Sandbox{L: link})`.

- [ ] **Step 4: Run tests and regenerate bindings**

Run: `go test -race ./internal/app/ -v -run "TestForwardSandbox|TestScreenInput"` → PASS.
Run: `go tool mygo generate` then `git diff --stat frontend/src/mygo.ts` and `grep -n "export const Sandbox\|sandbox:status\|sandbox:screen" frontend/src/mygo.ts`
Expected: `Sandbox` object with the eleven methods above and `sandboxStatus` / `sandboxScreen` events present.
Run: `go vet ./internal/app/ ./cmd/desktop/ && bun run --cwd frontend typecheck`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/app/sandbox.go internal/app/sandbox_test.go internal/app/bridge.go cmd/desktop/main.go frontend/src/mygo.ts
git commit -m "feat(app): bind the bot computer service and screen events

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: noVNC dependency and the screen channel adapter

**Files:**
- Modify: `frontend/package.json`, `frontend/bun.lock` (via `bun add`)
- Create: `frontend/src/types/novnc.d.ts`, `frontend/src/features/computer/screenChannel.ts`, `frontend/src/features/computer/testing.ts`
- Test: `frontend/src/features/computer/screenChannel.test.ts`

**Interfaces:**
- Consumes: `Sandbox.openScreen/screenInput/closeScreen`, `events.sandboxScreen` (Task 6).
- Produces:
  ```ts
  export function toBase64(bytes: Uint8Array): string
  export function fromBase64(s: string): Uint8Array
  export class ScreenChannel { constructor(botId: string); open(): Promise<void>; send(data: ArrayBuffer | Uint8Array): void; close(): void;
    binaryType: string; protocol: string; readyState: "connecting" | "open" | "closing" | "closed";
    onopen; onmessage; onclose; onerror }
  // testing.ts
  export const sandbox: Record<"availability"|"status"|"enable"|"install"|"setViewer"|"takeOver"|"release"|"openScreen"|"screenInput"|"closeScreen"|"openTerminal", Mock>
  export const terminalsBridge: Record<"attach"|"write"|"resize"|"close", Mock>
  export function emit(name: "sandboxStatus" | "sandboxScreen" | "terminalOutput" | "terminalChanged", payload: unknown): void
  export function resetSandbox(): void
  export const mygoMock
  export const b64: (bytes: number[]) => string
  ```

What noVNC 1.7.0 needs from a channel (verified in `core/websock.js` `attach()` and `core/rfb.js` `_connect()` of the published package): the object (own keys plus its prototype's own property names) must have `send`, `close`, `binaryType`, `onerror`, `onmessage`, `onopen`, `protocol`, `readyState`; it sets `binaryType = "arraybuffer"` and assigns `onopen`, `onmessage`, `onclose`, `onerror`; `readyState` may be the strings `"connecting" | "open" | "closing" | "closed"`; if it is `"open"` at attach time noVNC starts at once, otherwise it waits for `onopen`; `onmessage` receives `{ data: ArrayBuffer }`; `onclose` receives `{ code, reason }`; `send` receives `new Uint8Array(sendBuffer.buffer, 0, len)`, a view into a buffer noVNC reuses, so the channel copies synchronously. noVNC attaches on a later tick than `new RFB(...)`, so bytes that arrive before `onmessage` is assigned are queued by an `onmessage` setter.

- [ ] **Step 1: Add the dependency and type declarations**

Run: `bun add --cwd frontend --exact @novnc/novnc@1.7.0`
Expected: `package.json` gains `"@novnc/novnc": "1.7.0"`.

Create `frontend/src/types/novnc.d.ts` (no top-level import, so it stays an ambient declaration):

```ts
// The parts of noVNC 1.7.0's RFB (core/rfb.js, docs/API.md) Orole uses. noVNC ships no TypeScript types.
declare module "@novnc/novnc" {
  export interface RFBChannel {
    binaryType: string;
    protocol: string;
    readyState: string | number;
    send(data: ArrayBuffer | Uint8Array): void;
    close(): void;
    onopen: unknown;
    onmessage: unknown;
    onerror: unknown;
    onclose: unknown;
  }
  export interface RFBOptions {
    shared?: boolean;
    credentials?: { username?: string; password?: string; target?: string };
    repeaterID?: string;
    wsProtocols?: string[];
  }
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, urlOrChannel: string | RFBChannel, options?: RFBOptions);
    viewOnly: boolean;
    scaleViewport: boolean;
    clipViewport: boolean;
    resizeSession: boolean;
    focusOnClick: boolean;
    showDotCursor: boolean;
    background: string;
    qualityLevel: number;
    compressionLevel: number;
    disconnect(): void;
    focus(options?: FocusOptions): void;
    blur(): void;
  }
}
```

- [ ] **Step 2: Write the shared mock and the failing tests**

Create `frontend/src/features/computer/testing.ts`:

```ts
import { vi } from "vitest";

/**
 * The mocked MyGo bridge for the Computer tab tests. Install with
 * `vi.mock("@/mygo", async () => (await import("./testing")).mygoMock)`.
 */
const listeners = new Map<string, Set<(payload: unknown) => void>>();

function event(name: string) {
  return {
    on: (listener: (payload: never) => void) => {
      const set = listeners.get(name) ?? new Set();
      set.add(listener as (payload: unknown) => void);
      listeners.set(name, set);
      return () => { set.delete(listener as (payload: unknown) => void); };
    },
  };
}

/** Delivers a bridge event to every current subscriber. */
export function emit(name: "sandboxStatus" | "sandboxScreen" | "terminalOutput" | "terminalChanged", payload: unknown) {
  for (const listener of [...(listeners.get(name) ?? [])]) listener(payload);
}

export const listenerCount = (name: string) => listeners.get(name)?.size ?? 0;

export const sandbox = {
  availability: vi.fn(), status: vi.fn(), enable: vi.fn(), install: vi.fn(), setViewer: vi.fn(), takeOver: vi.fn(),
  release: vi.fn(), openScreen: vi.fn(), screenInput: vi.fn(), closeScreen: vi.fn(), openTerminal: vi.fn(),
};
export const terminalsBridge = { attach: vi.fn(), write: vi.fn(), resize: vi.fn(), close: vi.fn() };

/** Clears subscriptions and calls; the void calls resolve by default. */
export function resetSandbox() {
  listeners.clear();
  for (const fn of [...Object.values(sandbox), ...Object.values(terminalsBridge)]) fn.mockReset();
  for (const fn of [sandbox.setViewer, sandbox.takeOver, sandbox.release, sandbox.screenInput, sandbox.closeScreen, sandbox.enable, sandbox.install]) fn.mockResolvedValue(undefined);
}

export const mygoMock = {
  Sandbox: sandbox,
  Terminals: terminalsBridge,
  events: {
    sandboxStatus: event("sandboxStatus"), sandboxScreen: event("sandboxScreen"),
    terminalOutput: event("terminalOutput"), terminalChanged: event("terminalChanged"), daemonState: event("daemonState"),
  },
};

export const b64 = (bytes: number[]) => btoa(String.fromCharCode(...bytes));
```

Create `frontend/src/features/computer/screenChannel.test.ts`:

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { ScreenChannel, fromBase64, toBase64 } from "./screenChannel";
import { b64, emit, listenerCount, resetSandbox, sandbox } from "./testing";

const bytesOf = (buffer: ArrayBuffer) => [...new Uint8Array(buffer)];
const flush = () => new Promise((resolve) => setTimeout(resolve, 0));

beforeEach(() => resetSandbox());
afterEach(() => vi.useRealTimers());

describe("screen channel", () => {
  it("has every property noVNC's Websock.attach checks", () => {
    const ch = new ScreenChannel("b1");
    const props = [...Object.keys(ch), ...Object.getOwnPropertyNames(Object.getPrototypeOf(ch))];
    for (const p of ["send", "close", "binaryType", "onerror", "onmessage", "onopen", "protocol", "readyState"]) expect(props).toContain(p);
    expect(ch.readyState).toBe("connecting");
  });

  it("opens the screen, then replays bytes that arrived before the id was known and ignores other screens", async () => {
    let resolve!: (id: string) => void;
    sandbox.openScreen.mockReturnValue(new Promise<string>((r) => { resolve = r; }));
    const ch = new ScreenChannel("b1");
    const onopen = vi.fn();
    const got: number[][] = [];
    ch.onopen = onopen;
    ch.onmessage = (e) => got.push(bytesOf(e.data));
    const opening = ch.open();
    emit("sandboxScreen", { id: "s1", data: b64([82, 70, 66]) }); // the guest speaks first
    emit("sandboxScreen", { id: "other", data: b64([9]) });
    resolve("s1");
    await opening;
    expect(sandbox.openScreen).toHaveBeenCalledWith("b1");
    expect(ch.readyState).toBe("open");
    expect(onopen).toHaveBeenCalledTimes(1);
    emit("sandboxScreen", { id: "s1", data: b64([1, 2]) });
    expect(got).toEqual([[82, 70, 66], [1, 2]]);
  });

  it("queues bytes until noVNC assigns onmessage", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    const ch = new ScreenChannel("b1");
    await ch.open();
    emit("sandboxScreen", { id: "s1", data: b64([5, 6]) });
    const got: number[][] = [];
    ch.onmessage = (e) => got.push(bytesOf(e.data));
    expect(got).toEqual([[5, 6]]);
  });

  it("copies outgoing bytes at once and sends them in order, one call at a time", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    let release!: () => void;
    sandbox.screenInput.mockReturnValueOnce(new Promise<void>((r) => { release = r; })).mockResolvedValue(undefined);
    const ch = new ScreenChannel("b1");
    await ch.open();
    const reused = new Uint8Array([1, 2, 3, 4]);
    ch.send(new Uint8Array(reused.buffer, 0, 2));
    reused.set([7, 7], 0); // noVNC reuses its send buffer
    ch.send(new Uint8Array(reused.buffer, 2, 2));
    ch.send(new Uint8Array([5]));
    expect(sandbox.screenInput).toHaveBeenCalledTimes(1);
    expect([...fromBase64(sandbox.screenInput.mock.calls[0]![1] as string)]).toEqual([1, 2]);
    release();
    await flush();
    expect(sandbox.screenInput).toHaveBeenCalledTimes(2);
    expect([...fromBase64(sandbox.screenInput.mock.calls[1]![1] as string)]).toEqual([3, 4, 5]);
  });

  it("close ends the stream once, tells noVNC and stops listening", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    const ch = new ScreenChannel("b1");
    const onclose = vi.fn();
    ch.onclose = onclose;
    await ch.open();
    ch.close();
    ch.close();
    expect(sandbox.closeScreen).toHaveBeenCalledTimes(1);
    expect(sandbox.closeScreen).toHaveBeenCalledWith("s1");
    expect(onclose).toHaveBeenCalledTimes(1);
    expect(onclose.mock.calls[0]![0]).toMatchObject({ code: 1000, wasClean: true });
    expect(ch.readyState).toBe("closed");
    expect(listenerCount("sandboxScreen")).toBe(0);
  });

  it("reports the daemon closing the stream with its reason", async () => {
    sandbox.openScreen.mockResolvedValue("s1");
    const ch = new ScreenChannel("b1");
    const onclose = vi.fn();
    ch.onclose = onclose;
    await ch.open();
    emit("sandboxScreen", { id: "s1", closed: true, error: "the computer stopped" });
    expect(onclose.mock.calls[0]![0]).toMatchObject({ code: 1011, reason: "the computer stopped", wasClean: false });
    expect(sandbox.closeScreen).not.toHaveBeenCalled();
  });

  it("fails with the daemon's message when the screen cannot open", async () => {
    sandbox.openScreen.mockRejectedValue(new Error("not_found: the computer is off"));
    const ch = new ScreenChannel("b1");
    const onerror = vi.fn();
    const onclose = vi.fn();
    ch.onerror = onerror;
    ch.onclose = onclose;
    await ch.open();
    expect(onerror).toHaveBeenCalledTimes(1);
    expect(onclose.mock.calls[0]![0]).toMatchObject({ code: 1006, reason: "not_found: the computer is off" });
  });

  it("closes a screen that opened after the channel was closed", async () => {
    sandbox.openScreen.mockResolvedValue("late");
    const ch = new ScreenChannel("b1");
    const opening = ch.open();
    ch.close();
    await opening;
    expect(sandbox.closeScreen).toHaveBeenCalledWith("late");
  });

  it("round-trips base64 for large payloads", () => {
    const data = new Uint8Array(200_000).map((_, i) => i % 256);
    expect([...fromBase64(toBase64(data))]).toEqual([...data]);
  });
});
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `bun run --cwd frontend test src/features/computer/screenChannel.test.ts`
Expected: FAIL, cannot resolve `./screenChannel`.

- [ ] **Step 4: Implement**

Create `frontend/src/features/computer/screenChannel.ts`:

```ts
import { Sandbox, events } from "@/mygo";

type Chunk = { id: string; data?: string; closed?: boolean; error?: string };
type CloseInfo = { code: number; reason: string; wasClean: boolean };
type Receiver = ((event: { data: ArrayBuffer }) => void) | null;

/** Chunks kept while the screen id is unknown (the guest's VNC server speaks first). */
const EARLY_MAX = 256;

/** Bytes to base64 in slices, so a large chunk never overflows the argument list. */
export function toBase64(bytes: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(bin);
}

/** Base64 to bytes with a plain loop. */
export function fromBase64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * The WebSocket-shaped object noVNC's RFB accepts instead of a URL (noVNC 1.7.0 `Websock.attach`), carried by the
 * MyGo `Sandbox` calls and the `sandbox:screen` event: no socket exists anywhere. The daemon decides what input
 * reaches the computer; this only moves bytes. Outgoing bytes are copied at once (noVNC reuses its send buffer) and
 * sent in order, one call at a time, joining what queued meanwhile.
 */
export class ScreenChannel {
  binaryType = "arraybuffer";
  protocol = "";
  readyState: "connecting" | "open" | "closing" | "closed" = "connecting";
  onopen: ((event: Event) => void) | null = null;
  onclose: ((event: CloseInfo) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  readonly botId: string;
  private id: string | null = null;
  private receiver: Receiver = null;
  private inbound: ArrayBuffer[] = [];
  private early: Chunk[] = [];
  private outbound: Uint8Array[] = [];
  private sending = false;
  private readonly off: () => void;

  constructor(botId: string) {
    this.botId = botId;
    this.off = events.sandboxScreen.on((chunk) => this.onChunk(chunk));
  }

  get onmessage(): Receiver { return this.receiver; }
  /** noVNC assigns this a tick after `new RFB(...)`; bytes that came first are handed over now. */
  set onmessage(fn: Receiver) {
    this.receiver = fn;
    if (fn) for (const data of this.inbound.splice(0)) fn({ data });
  }

  /** Opens the daemon's screen stream for the bot. */
  async open(): Promise<void> {
    let id: string;
    try {
      id = await Sandbox.openScreen(this.botId);
    } catch (e) {
      this.fail(e instanceof Error ? e.message : String(e));
      return;
    }
    if (this.readyState !== "connecting") { void Sandbox.closeScreen(id).catch(() => {}); return; }
    this.id = id;
    this.readyState = "open";
    this.onopen?.(new Event("open"));
    for (const chunk of this.early.splice(0)) this.onChunk(chunk);
    this.pump();
  }

  send(data: ArrayBuffer | Uint8Array): void {
    if (this.readyState !== "open" && this.readyState !== "connecting") return;
    this.outbound.push(data instanceof Uint8Array ? data.slice() : new Uint8Array(data.slice(0)));
    this.pump();
  }

  close(): void {
    if (this.readyState === "closed") return;
    const id = this.id;
    this.finish({ code: 1000, reason: "", wasClean: true });
    if (id) void Sandbox.closeScreen(id).catch(() => {});
  }

  private pump(): void {
    if (this.sending || !this.id || this.readyState !== "open" || this.outbound.length === 0) return;
    const parts = this.outbound.splice(0);
    const joined = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
    let at = 0;
    for (const p of parts) { joined.set(p, at); at += p.length; }
    this.sending = true;
    Sandbox.screenInput(this.id, toBase64(joined)).then(
      () => { this.sending = false; this.pump(); },
      (e: unknown) => { this.sending = false; this.fail(e instanceof Error ? e.message : String(e)); },
    );
  }

  private onChunk(chunk: Chunk): void {
    if (this.readyState === "closed") return;
    if (this.id === null) {
      if (this.early.length < EARLY_MAX) this.early.push(chunk);
      return;
    }
    if (chunk.id !== this.id) return;
    if (chunk.data) {
      const data = fromBase64(chunk.data).buffer as ArrayBuffer;
      if (this.receiver) this.receiver({ data }); else this.inbound.push(data);
    }
    if (chunk.closed) this.finish(chunk.error ? { code: 1011, reason: chunk.error, wasClean: false } : { code: 1000, reason: "", wasClean: true });
  }

  private fail(reason: string): void {
    if (this.readyState === "closed") return;
    const id = this.id;
    this.onerror?.(new Event("error"));
    this.finish({ code: 1006, reason, wasClean: false });
    if (id) void Sandbox.closeScreen(id).catch(() => {});
  }

  private finish(info: CloseInfo): void {
    this.readyState = "closed";
    this.off();
    this.outbound = [];
    this.early = [];
    this.onclose?.(info);
  }
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `bun run --cwd frontend test src/features/computer/screenChannel.test.ts` then `bun run --cwd frontend typecheck`
Expected: PASS; typecheck clean.

- [ ] **Step 6: Commit**

```bash
git add frontend/package.json frontend/bun.lock frontend/src/types/novnc.d.ts frontend/src/features/computer/screenChannel.ts frontend/src/features/computer/screenChannel.test.ts frontend/src/features/computer/testing.ts
git commit -m "feat(computer): add novnc and a screen channel over mygo events

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: Computer view state, setup copy, status and viewer lease hooks

**Files:**
- Create: `frontend/src/features/computer/computerState.ts`, `frontend/src/features/computer/useComputer.ts`
- Test: `frontend/src/features/computer/computerState.test.ts`, `frontend/src/features/computer/useComputer.test.tsx`

**Interfaces:**
- Consumes: `Sandbox.availability/status/setViewer`, `events.sandboxStatus` (Task 6); `testing.ts` (Task 7).
- Produces:
  ```ts
  export type Availability = Awaited<ReturnType<typeof Sandbox.availability>>;
  export type ComputerStatus = Awaited<ReturnType<typeof Sandbox.status>>;
  export type ComputerView = { kind: "loading" } | { kind: "setup"; reason: string; detail: string } | { kind: "off" } | { kind: "starting" }
    | { kind: "stopping" } | { kind: "failed"; error: string } | { kind: "running"; takenOver: boolean };
  export type SetupAction = "enable" | "install" | "recheck" | null;
  export function computerView(a: Availability | null, s: ComputerStatus | null): ComputerView
  export function setupCopy(reason: string): { title: string; body: string; action: SetupAction }
  export const SECURITY_NOTE: string
  export function errorText(e: unknown): string
  export function useComputer(botId: string): { availability; status; error: string | null; refresh: () => Promise<void> }
  export const VIEWER_REFRESH_MS = 60_000
  export function useViewerLease(botId: string): void
  ```

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/features/computer/computerState.test.ts`:

```ts
import { describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { computerView, errorText, setupCopy, type Availability, type ComputerStatus } from "./computerState";

const ready: Availability = { ready: true };
const status = (over: Partial<ComputerStatus>): ComputerStatus => ({ botId: "b1", state: "off", takenOver: false, ...over }) as ComputerStatus;

describe("computer view", () => {
  it.each([
    [null, null, { kind: "loading" }],
    [{ ready: false, reason: "whp-disabled", detail: "HypervisorPlatform: Disabled" }, status({}), { kind: "setup", reason: "whp-disabled", detail: "HypervisorPlatform: Disabled" }],
    [ready, status({ state: "off" }), { kind: "off" }],
    [ready, status({ state: "starting" }), { kind: "starting" }],
    [ready, status({ state: "stopping" }), { kind: "stopping" }],
    [ready, status({ state: "failed", error: "the computer failed to start" }), { kind: "failed", error: "the computer failed to start" }],
    [ready, status({ state: "failed" }), { kind: "failed", error: "The computer failed to start." }],
    [ready, status({ state: "running" }), { kind: "running", takenOver: false }],
    [ready, status({ state: "running", takenOver: true }), { kind: "running", takenOver: true }],
  ] as const)("maps %j + %j", (a, s, want) => {
    expect(computerView(a as Availability | null, s as ComputerStatus | null)).toEqual(want);
  });

  it("offers the right action for each setup reason", () => {
    expect(setupCopy("whp-disabled").action).toBe("enable");
    expect(setupCopy("runtime-missing").action).toBe("install");
    expect(setupCopy("reboot-required").action).toBe("recheck");
    expect(setupCopy("virtualization-off").action).toBe("recheck");
    expect(setupCopy("unsupported-os").action).toBeNull();
    expect(setupCopy("something-new").action).toBe("recheck");
    expect(setupCopy("virtualization-off").body).toMatch(/BIOS or UEFI/);
  });

  it("strips the daemon's error code from messages", () => {
    expect(errorText(new Error("not_found: the computer is off"))).toBe("the computer is off");
    expect(errorText("plain")).toBe("plain");
  });
});
```

Create `frontend/src/features/computer/useComputer.test.tsx`:

```tsx
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { emit, resetSandbox, sandbox } from "./testing";
import { useComputer, useViewerLease, VIEWER_REFRESH_MS } from "./useComputer";

beforeEach(() => {
  resetSandbox();
  sandbox.availability.mockResolvedValue({ ready: true });
  sandbox.status.mockResolvedValue({ botId: "b1", state: "off", takenOver: false });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("useComputer", () => {
  it("loads availability and status, then follows this bot's status events only", async () => {
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.status?.state).toBe("off"));
    expect(result.current.availability).toEqual({ ready: true });
    act(() => emit("sandboxStatus", { botId: "b2", state: "running", takenOver: false }));
    expect(result.current.status?.state).toBe("off");
    act(() => emit("sandboxStatus", { botId: "b1", state: "starting", takenOver: false }));
    expect(result.current.status?.state).toBe("starting");
  });

  it("keeps the load error for the tab to show", async () => {
    sandbox.availability.mockRejectedValue(new Error("internal: daemon unavailable"));
    const { result } = renderHook(() => useComputer("b1"));
    await waitFor(() => expect(result.current.error).toBe("daemon unavailable"));
  });
});

describe("useViewerLease", () => {
  it("watches on mount, renews every minute and stops on unmount", () => {
    vi.useFakeTimers();
    const { unmount } = renderHook(() => useViewerLease("b1"));
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", true);
    vi.advanceTimersByTime(VIEWER_REFRESH_MS);
    expect(sandbox.setViewer).toHaveBeenCalledTimes(2);
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", true);
    unmount();
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", false);
    vi.advanceTimersByTime(3 * VIEWER_REFRESH_MS);
    expect(sandbox.setViewer).toHaveBeenCalledTimes(3);
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bun run --cwd frontend test src/features/computer/computerState.test.ts src/features/computer/useComputer.test.tsx`
Expected: FAIL, cannot resolve `./computerState` / `./useComputer`.

- [ ] **Step 3: Implement**

Create `frontend/src/features/computer/computerState.ts`:

```ts
import type { Sandbox } from "@/mygo";

export type Availability = Awaited<ReturnType<typeof Sandbox.availability>>;
export type ComputerStatus = Awaited<ReturnType<typeof Sandbox.status>>;

export type ComputerView =
  | { kind: "loading" }
  | { kind: "setup"; reason: string; detail: string }
  | { kind: "off" }
  | { kind: "starting" }
  | { kind: "stopping" }
  | { kind: "failed"; error: string }
  | { kind: "running"; takenOver: boolean };

/** What the Computer tab shows for the PC's availability and the bot's computer status. */
export function computerView(availability: Availability | null, status: ComputerStatus | null): ComputerView {
  if (!availability || !status) return { kind: "loading" };
  if (!availability.ready) return { kind: "setup", reason: availability.reason ?? "", detail: availability.detail ?? "" };
  switch (status.state) {
    case "starting": return { kind: "starting" };
    case "running": return { kind: "running", takenOver: status.takenOver };
    case "stopping": return { kind: "stopping" };
    case "failed": return { kind: "failed", error: status.error || "The computer failed to start." };
    default: return { kind: "off" };
  }
}

export type SetupAction = "enable" | "install" | "recheck" | null;

/** The setup screen's copy and action for an Availability reason. */
export function setupCopy(reason: string): { title: string; body: string; action: SetupAction } {
  switch (reason) {
    case "whp-disabled":
      return { title: "Turn on Windows Hypervisor Platform", body: "Bot computers run on Windows Hypervisor Platform, which is off. Enabling it asks for administrator permission once, then Windows needs a restart.", action: "enable" };
    case "reboot-required":
      return { title: "Restart Windows to finish", body: "Windows Hypervisor Platform is on, but it starts working after a restart.", action: "recheck" };
    case "runtime-missing":
      return { title: "Download the bot computer", body: "The first time, Orole downloads the virtual machine software and the computer's Linux system. Every file is checked before it is used.", action: "install" };
    case "virtualization-off":
      return { title: "Virtualization is off", body: "Your PC's firmware (BIOS or UEFI) has virtualization turned off. Turn it on there, then try again.", action: "recheck" };
    case "unsupported-os":
      return { title: "Not available on this PC", body: "Bot computers need Windows 11 for now.", action: null };
    default:
      return { title: "The computer can't run here", body: "Orole could not use virtualization on this PC.", action: "recheck" };
  }
}

/** Shown on the setup screen: what the user agrees to by turning it on. */
export const SECURITY_NOTE = "A bot computer has open internet access and can reach your home network and services running on this PC. Logins you make on it stay on the bot's disk.";

/** An error for display, without the daemon's code prefix ("not_found: …"). */
export const errorText = (e: unknown): string => (e instanceof Error ? e.message : String(e)).replace(/^[a-z_]+: /, "");
```

Create `frontend/src/features/computer/useComputer.ts`:

```ts
import { useCallback, useEffect, useState } from "react";
import { Sandbox, events } from "@/mygo";
import { errorText, type Availability, type ComputerStatus } from "./computerState";

/** Availability and the bot's computer status, kept current by `sandbox:status` events. */
export function useComputer(botId: string) {
  const [availability, setAvailability] = useState<Availability | null>(null);
  const [status, setStatus] = useState<ComputerStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const refresh = useCallback(async () => {
    try {
      const [a, s] = await Promise.all([Sandbox.availability(), Sandbox.status(botId)]);
      setAvailability(a);
      setStatus(s);
      setError(null);
    } catch (e) {
      setError(errorText(e));
    }
  }, [botId]);
  useEffect(() => {
    setAvailability(null);
    setStatus(null);
    const off = events.sandboxStatus.on((s) => { if (s.botId === botId) setStatus(s); });
    void refresh();
    return off;
  }, [botId, refresh]);
  return { availability, status, error, refresh };
}

/** How often a visible Computer tab renews its viewer lease (the daemon drops it after 150 s). */
export const VIEWER_REFRESH_MS = 60_000;

/**
 * Tells the daemon someone is watching the bot's computer while the tab is mounted and the window visible, so the
 * idle stop waits. The daemon expires the lease on its own if this window disappears.
 */
export function useViewerLease(botId: string) {
  useEffect(() => {
    const set = (watching: boolean) => void Sandbox.setViewer(botId, watching).catch(() => {});
    let timer: ReturnType<typeof setInterval> | undefined;
    const start = () => {
      set(true);
      clearInterval(timer);
      timer = setInterval(() => set(true), VIEWER_REFRESH_MS);
    };
    const stop = () => {
      clearInterval(timer);
      timer = undefined;
      set(false);
    };
    const onVisibility = () => { if (document.visibilityState === "hidden") stop(); else start(); };
    if (document.visibilityState !== "hidden") start();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      document.removeEventListener("visibilitychange", onVisibility);
      stop();
    };
  }, [botId]);
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `bun run --cwd frontend test src/features/computer/computerState.test.ts src/features/computer/useComputer.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/computer/computerState.ts frontend/src/features/computer/computerState.test.ts frontend/src/features/computer/useComputer.ts frontend/src/features/computer/useComputer.test.tsx
git commit -m "feat(computer): computer view state, setup copy and viewer lease

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 9: LiveScreen (noVNC view)

**Files:**
- Create: `frontend/src/features/computer/LiveScreen.tsx`
- Test: `frontend/src/features/computer/LiveScreen.test.tsx`

**Interfaces:**
- Consumes: `ScreenChannel` (Task 7), `RFB` from `@novnc/novnc` (typed in Task 7), `Button` (`@/components/ui/button`).
- Produces: `export function LiveScreen({ botId, interactive }: { botId: string; interactive: boolean }): JSX.Element` — region labelled "Live screen"; view-only unless `interactive`.

- [ ] **Step 1: Write the failing test**

Create `frontend/src/features/computer/LiveScreen.test.tsx`:

```tsx
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { rfbs } = vi.hoisted(() => ({ rfbs: [] as Array<EventTarget & { target: HTMLElement; channel: unknown; options: unknown; viewOnly: boolean; scaleViewport: boolean; resizeSession: boolean; disconnect: () => void; focus: () => void }> }));

vi.mock("@novnc/novnc", async () => {
  const { vi: v } = await import("vitest");
  class FakeRFB extends EventTarget {
    target: HTMLElement;
    channel: unknown;
    options: unknown;
    viewOnly = false;
    scaleViewport = false;
    resizeSession = true;
    disconnect = v.fn();
    focus = v.fn();
    constructor(target: HTMLElement, channel: unknown, options: unknown) {
      super();
      this.target = target;
      this.channel = channel;
      this.options = options;
      rfbs.push(this as never);
    }
  }
  return { default: FakeRFB };
});
vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);

import { LiveScreen } from "./LiveScreen";
import { ScreenChannel } from "./screenChannel";
import { resetSandbox, sandbox } from "./testing";

beforeEach(() => { resetSandbox(); rfbs.length = 0; sandbox.openScreen.mockResolvedValue("s1"); });
afterEach(cleanup);

describe("live screen", () => {
  it("draws the bot's screen through a screen channel, view-only, scaled and shared", async () => {
    render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    expect(rfbs).toHaveLength(1);
    const rfb = rfbs[0]!;
    expect(rfb.channel).toBeInstanceOf(ScreenChannel);
    expect((rfb.channel as ScreenChannel).botId).toBe("b1");
    expect(rfb.options).toEqual({ shared: true });
    expect(rfb.viewOnly).toBe(true);
    expect(rfb.scaleViewport).toBe(true);
    expect(rfb.resizeSession).toBe(false);
    expect(screen.getByRole("region", { name: "Live screen" })).toContainElement(rfb.target);
    expect(sandbox.openScreen).toHaveBeenCalledWith("b1");
    expect(screen.getByText("Connecting to the screen…")).toBeInTheDocument();
    act(() => { rfb.dispatchEvent(new Event("connect")); });
    expect(screen.queryByText("Connecting to the screen…")).toBeNull();
  });

  it("accepts input only while the user has taken over", async () => {
    const { rerender } = render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    const rfb = rfbs[0]!;
    rerender(<LiveScreen botId="b1" interactive />);
    expect(rfb.viewOnly).toBe(false);
    expect(rfb.focus).toHaveBeenCalled();
    rerender(<LiveScreen botId="b1" interactive={false} />);
    expect(rfb.viewOnly).toBe(true);
  });

  it("says when the screen disconnected and reconnects on request", async () => {
    render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    act(() => { rfbs[0]!.dispatchEvent(new Event("disconnect")); });
    expect(screen.getByText("The screen disconnected.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    await act(async () => {});
    expect(rfbs).toHaveLength(2);
    expect(rfbs[0]!.disconnect).toHaveBeenCalled();
  });

  it("disconnects and closes the screen when it goes away", async () => {
    const { unmount } = render(<LiveScreen botId="b1" interactive={false} />);
    await act(async () => {});
    unmount();
    expect(rfbs[0]!.disconnect).toHaveBeenCalled();
    expect(sandbox.closeScreen).toHaveBeenCalledWith("s1");
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `bun run --cwd frontend test src/features/computer/LiveScreen.test.tsx`
Expected: FAIL, cannot resolve `./LiveScreen`.

- [ ] **Step 3: Implement**

Create `frontend/src/features/computer/LiveScreen.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import RFB from "@novnc/novnc";
import { Button } from "@/components/ui/button";
import { ScreenChannel } from "./screenChannel";

/**
 * The bot computer's screen, drawn by noVNC from the daemon's screen stream. View-only unless the user has taken
 * over; even then the daemon, not this flag, decides whether keyboard and mouse reach the computer.
 */
export function LiveScreen({ botId, interactive }: { botId: string; interactive: boolean }) {
  const host = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<RFB | null>(null);
  const interactiveRef = useRef(interactive);
  const [state, setState] = useState<"connecting" | "connected" | "disconnected">("connecting");
  const [attempt, setAttempt] = useState(0);
  interactiveRef.current = interactive;

  useEffect(() => {
    setState("connecting");
    const channel = new ScreenChannel(botId);
    const rfb = new RFB(host.current!, channel, { shared: true });
    rfb.viewOnly = !interactiveRef.current;
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
    rfb.addEventListener("connect", () => setState("connected"));
    rfb.addEventListener("disconnect", () => setState("disconnected"));
    rfbRef.current = rfb;
    void channel.open();
    return () => {
      rfbRef.current = null;
      try { rfb.disconnect(); } catch { /* already disconnected */ }
      channel.close();
    };
  }, [botId, attempt]);

  useEffect(() => {
    const rfb = rfbRef.current;
    if (!rfb) return;
    rfb.viewOnly = !interactive;
    if (interactive) rfb.focus();
  }, [interactive]);

  return (
    <div role="region" aria-label="Live screen" data-slot="live-screen"
      className="relative flex min-h-48 flex-[3] overflow-hidden rounded-lg border bg-muted/40">
      <div ref={host} className="size-full" />
      {state !== "connected" && (
        <div role="status" className="absolute inset-0 flex flex-col items-center justify-center gap-2 text-sm text-muted-foreground">
          {state === "connecting" ? "Connecting to the screen…" : "The screen disconnected."}
          {state === "disconnected" && <Button type="button" size="xs" variant="outline" onClick={() => setAttempt((a) => a + 1)}>Reconnect</Button>}
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Run the test and a production build**

Run: `bun run --cwd frontend test src/features/computer/LiveScreen.test.tsx` → PASS.
Run: `bun run --cwd frontend build`
Expected: build succeeds. noVNC's `core/util/browser.js` uses top-level `await`; Vite 8's default target supports it. If the build fails with a top-level-await error, add `build: { target: "es2022" }` to `frontend/vite.config.ts`'s `defineConfig({...})` and rebuild.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/computer/LiveScreen.tsx frontend/src/features/computer/LiveScreen.test.tsx
git commit -m "feat(computer): live screen view with novnc

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 10: Computer tab in the bot page

**Files:**
- Create: `frontend/src/features/computer/ComputerTab.tsx`
- Modify: `frontend/src/stores/chat.ts` (pane kind + `openComputerPane`), `frontend/src/features/chat/SidePane.tsx` (render), `frontend/src/features/chat/BotHeader.tsx` (title-bar button)
- Test: `frontend/src/features/computer/ComputerTab.test.tsx`, `frontend/src/features/chat/BotHeader.test.tsx`, `frontend/src/stores/chat.test.ts` (add a case)

**Interfaces:**
- Consumes: `useComputer`, `useViewerLease`, `computerView`, `setupCopy`, `SECURITY_NOTE`, `errorText` (Task 8); `LiveScreen` (Task 9); `TerminalPane({ id, active, onFocus, fontSize })` (`features/terminals/TerminalPane.tsx`); `Sandbox.enable/install/takeOver/release/openTerminal`; `useSettings((s) => s.terminal.fontSize)`.
- Produces:
  ```ts
  export function ComputerTab({ botId, onClose }: { botId: string; onClose: () => void }): JSX.Element
  // stores/chat.ts
  export type ChatPane = ... | { kind: "computer"; id: string } // id = botId
  openComputerPane: (botId: string) => void
  ```

- [ ] **Step 1: Write the failing tests**

Create `frontend/src/features/computer/ComputerTab.test.tsx`:

```tsx
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./testing")).mygoMock);
vi.mock("./LiveScreen", () => ({
  LiveScreen: ({ botId, interactive }: { botId: string; interactive: boolean }) => <div data-testid="live-screen" data-bot={botId} data-interactive={String(interactive)} />,
}));
vi.mock("@/features/terminals/TerminalPane", () => ({
  TerminalPane: ({ id }: { id: string }) => <div data-testid="terminal" data-id={id} />,
}));

import { ComputerTab } from "./ComputerTab";
import { emit, resetSandbox, sandbox } from "./testing";

const running = (takenOver = false) => ({ botId: "b1", state: "running", takenOver });
const view = () => render(<ComputerTab botId="b1" onClose={() => {}} />);

beforeEach(() => {
  resetSandbox();
  sandbox.availability.mockResolvedValue({ ready: true });
  sandbox.status.mockResolvedValue({ botId: "b1", state: "off", takenOver: false });
  sandbox.openTerminal.mockResolvedValue({ id: "t1", state: "running" });
});
afterEach(cleanup);

describe("computer tab setup", () => {
  it("explains Windows Hypervisor Platform, enables it and shows the restart step", async () => {
    sandbox.availability.mockResolvedValueOnce({ ready: false, reason: "whp-disabled", detail: "HypervisorPlatform: Disabled" })
      .mockResolvedValueOnce({ ready: false, reason: "reboot-required" });
    view();
    expect(await screen.findByRole("heading", { name: "Turn on Windows Hypervisor Platform" })).toBeInTheDocument();
    expect(screen.getByText("HypervisorPlatform: Disabled")).toBeInTheDocument();
    expect(screen.getByText(/can reach your home network/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Enable" }));
    await waitFor(() => expect(sandbox.enable).toHaveBeenCalledTimes(1));
    expect(await screen.findByRole("heading", { name: "Restart Windows to finish" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("downloads the runtime and offers Try again with the reason when it fails", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "runtime-missing" });
    let fail!: (e: Error) => void;
    sandbox.install.mockReturnValueOnce(new Promise((_, reject) => { fail = reject; })).mockResolvedValue(undefined);
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    expect(screen.getByRole("button", { name: "Downloading…" })).toBeDisabled();
    await act(async () => fail(new Error("internal: hash mismatch for qemu.zip")));
    expect(screen.getByRole("alert")).toHaveTextContent("hash mismatch for qemu.zip");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(sandbox.install).toHaveBeenCalledTimes(2));
  });

  it("only re-checks when the user must fix virtualization in the firmware", async () => {
    sandbox.availability.mockResolvedValue({ ready: false, reason: "virtualization-off" });
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Try again" }));
    await waitFor(() => expect(sandbox.availability).toHaveBeenCalledTimes(2));
    expect(sandbox.enable).not.toHaveBeenCalled();
    expect(sandbox.install).not.toHaveBeenCalled();
  });
});

describe("computer tab states", () => {
  it("says the computer is off, then starting, then shows the screen and terminal when it runs", async () => {
    view();
    expect(await screen.findByText(/The computer is off\. It starts when the bot uses it\./)).toBeInTheDocument();
    act(() => emit("sandboxStatus", { botId: "b1", state: "starting", takenOver: false }));
    expect(screen.getByText("Starting computer…")).toBeInTheDocument();
    act(() => emit("sandboxStatus", running()));
    expect(screen.getByTestId("live-screen")).toHaveAttribute("data-interactive", "false");
    expect(await screen.findByTestId("terminal")).toHaveAttribute("data-id", "t1");
    expect(sandbox.openTerminal).toHaveBeenCalledWith("b1", 100, 24);
    expect(screen.getByRole("button", { name: "Take over" })).toBeInTheDocument();
  });

  it("shows why the computer failed", async () => {
    sandbox.status.mockResolvedValue({ botId: "b1", state: "failed", takenOver: false, error: "the computer failed to start" });
    view();
    expect(await screen.findByText("the computer failed to start")).toBeInTheDocument();
  });

  it("marks the bot as watched while the tab is open", async () => {
    const { unmount } = view();
    await screen.findByText(/The computer is off/);
    expect(sandbox.setViewer).toHaveBeenCalledWith("b1", true);
    unmount();
    expect(sandbox.setViewer).toHaveBeenLastCalledWith("b1", false);
  });
});

describe("computer tab takeover", () => {
  it("takes over, makes the screen interactive, and hands back with Done", async () => {
    sandbox.status.mockResolvedValueOnce(running(false)).mockResolvedValueOnce(running(true)).mockResolvedValue(running(false));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Take over" }));
    await waitFor(() => expect(sandbox.takeOver).toHaveBeenCalledWith("b1"));
    expect(await screen.findByRole("button", { name: "Done" })).toBeInTheDocument();
    expect(screen.getByTestId("live-screen")).toHaveAttribute("data-interactive", "true");
    expect(screen.getByText(/The bot waits until you click Done/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(sandbox.release).toHaveBeenCalledWith("b1"));
    expect(await screen.findByRole("button", { name: "Take over" })).toBeInTheDocument();
    expect(screen.getByTestId("live-screen")).toHaveAttribute("data-interactive", "false");
  });

  it("shows the daemon's refusal", async () => {
    sandbox.status.mockResolvedValue(running(false));
    sandbox.takeOver.mockRejectedValue(new Error("bad_request: the computer is not running"));
    view();
    fireEvent.click(await screen.findByRole("button", { name: "Take over" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("the computer is not running");
  });

  it("hands control back when the tab closes during a takeover", async () => {
    sandbox.status.mockResolvedValue(running(true));
    const { unmount } = view();
    await screen.findByRole("button", { name: "Done" });
    unmount();
    expect(sandbox.release).toHaveBeenCalledWith("b1");
  });
});
```

Create `frontend/src/features/chat/BotHeader.test.tsx`:

```tsx
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("./builder/testing")).mygoMock);

import { useChat } from "@/stores/chat";
import { BotActions } from "./BotHeader";
import { bot } from "./builder/testing";

afterEach(() => { cleanup(); useChat.setState({ bots: [], selectedBotId: null, pane: null }); });

describe("bot title bar actions", () => {
  it("shows a Computer button only for bots with a computer, toggling the Computer pane", () => {
    useChat.setState({ bots: [bot("b1", { config: { ...bot("x").config, computer: true } })], selectedBotId: "b1", pane: null });
    render(<BotActions />);
    fireEvent.click(screen.getByRole("button", { name: "Show computer" }));
    expect(useChat.getState().pane).toEqual({ kind: "computer", id: "b1" });
    fireEvent.click(screen.getByRole("button", { name: "Hide computer" }));
    expect(useChat.getState().pane).toBeNull();
  });

  it("has no Computer button for other bots", () => {
    useChat.setState({ bots: [bot("b2")], selectedBotId: "b2", pane: null });
    render(<BotActions />);
    expect(screen.queryByRole("button", { name: "Show computer" })).toBeNull();
  });
});
```

Append to `frontend/src/stores/chat.test.ts` (inside its top-level `describe`, using that file's existing imports of `useChat`):

```ts
  it("opens a bot's Computer pane and drops it when another bot is selected", () => {
    useChat.setState({ selectedBotId: "b1", pane: null, settingsOpen: true });
    useChat.getState().openComputerPane("b1");
    expect(useChat.getState()).toMatchObject({ pane: { kind: "computer", id: "b1" }, settingsOpen: false });
    useChat.getState().select("b2");
    expect(useChat.getState().pane).toBeNull();
  });
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bun run --cwd frontend test src/features/computer/ComputerTab.test.tsx src/features/chat/BotHeader.test.tsx src/stores/chat.test.ts`
Expected: FAIL, cannot resolve `./ComputerTab`; `openComputerPane is not a function`; no "Show computer" button.

- [ ] **Step 3: Implement the store and side pane**

In `frontend/src/stores/chat.ts`:
- change the `ChatPane` type to
  ```ts
  export type ChatPane = { kind: "task"; id: string } | { kind: "call"; id: string } | { kind: "computer"; id: string } | { kind: "document"; messageId: string; partIndex: number };
  ```
- add to the state interface, after `openDocument`:
  ```ts
  /** Opens the bot's Computer tab: its live screen and terminal (id = the bot). */
  openComputerPane: (botId: string) => void;
  ```
- add to the store implementation, after `openDocument`:
  ```ts
  openComputerPane: (botId) => set({ pane: { kind: "computer", id: botId }, settingsOpen: false }),
  ```
- update the `pane` doc comment to mention "a bot's computer".

In `frontend/src/features/chat/SidePane.tsx`:
- after the `TaskPane` lazy import add
  ```ts
  // noVNC and the Computer tab load only when a bot's computer is opened.
  const ComputerTab = lazy(() => import("@/features/computer/ComputerTab").then((m) => ({ default: m.ComputerTab })));
  ```
- replace the pane body ternary with
  ```tsx
  {pane.kind === "task"
    ? <Suspense fallback={null}><TaskPane taskId={pane.id} ready={ready} onClose={close} /></Suspense>
    : pane.kind === "call"
      ? <CallPane callId={pane.id} botId={botId} onClose={close} />
      : pane.kind === "computer"
        ? <Suspense fallback={null}><ComputerTab botId={pane.id} onClose={close} /></Suspense>
        : <DocumentPane botId={botId} messageId={pane.messageId} partIndex={pane.partIndex} onClose={close} />}
  ```
- update the component doc comment's first sentence to "a code task, a phone call's transcript, a bot's computer or a document".

- [ ] **Step 4: Implement the title-bar button**

Replace `BotActions` in `frontend/src/features/chat/BotHeader.tsx` (add imports `MonitorIcon` from `lucide-react`, `Tooltip, TooltipPopup, TooltipTrigger` from `@/components/ui/tooltip`, `useSelectedBot` from `@/stores/chat`):

```tsx
/** The title bar's actions in Chat: the bot's computer (for bots that have one) and its settings. */
export function BotActions() {
  const open = useChat((s) => s.settingsOpen);
  const bot = useSelectedBot();
  const computerOpen = useChat((s) => s.pane?.kind === "computer");
  return <>
    {bot?.config.computer && <Tooltip>
      <TooltipTrigger render={<Button type="button" variant="ghost" size="icon-xs" aria-label={computerOpen ? "Hide computer" : "Show computer"} aria-pressed={computerOpen}
        onClick={() => computerOpen ? useChat.getState().closePane() : useChat.getState().openComputerPane(bot.id)} />}>
        <MonitorIcon aria-hidden />
      </TooltipTrigger>
      <TooltipPopup>Computer</TooltipPopup>
    </Tooltip>}
    <Button type="button" variant="ghost" size="icon-xs" aria-label={open ? "Hide bot settings" : "Show bot settings"} aria-pressed={open} onClick={() => useChat.getState().setSettingsOpen(!open)}><PanelRightIcon aria-hidden /></Button>
  </>;
}
```

- [ ] **Step 5: Implement the Computer tab**

Create `frontend/src/features/computer/ComputerTab.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import { ChevronLeftIcon } from "lucide-react";
import { Button } from "@/components/ui/button";
import { TerminalPane } from "@/features/terminals/TerminalPane";
import { Sandbox } from "@/mygo";
import { useSettings } from "@/stores/settings";
import { computerView, errorText, SECURITY_NOTE, setupCopy } from "./computerState";
import { LiveScreen } from "./LiveScreen";
import { useComputer, useViewerLease } from "./useComputer";

/**
 * A bot's computer beside its conversation: the setup screen until this PC can run one, otherwise its state, and
 * while it runs the live screen (view-only until Take over), Take over / Done, and a terminal in the computer.
 * Nothing else by design: no start, stop or reset; the computer starts when the bot uses it.
 */
export function ComputerTab({ botId, onClose }: { botId: string; onClose: () => void }) {
  const { availability, status, error, refresh } = useComputer(botId);
  useViewerLease(botId);
  const view = computerView(availability, status);
  const takenOver = view.kind === "running" && view.takenOver;
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const takenRef = useRef(false);
  takenRef.current = takenOver;
  // Closing the tab hands control back, so the bot is never left waiting on a screen nobody sees.
  useEffect(() => () => { if (takenRef.current) void Sandbox.release(botId).catch(() => {}); }, [botId]);

  const toggleTakeover = async () => {
    setBusy(true);
    setActionError(null);
    try {
      await (takenOver ? Sandbox.release(botId) : Sandbox.takeOver(botId));
      await refresh();
    } catch (e) {
      setActionError(errorText(e));
    } finally {
      setBusy(false);
    }
  };

  return <section aria-labelledby="side-pane-title" data-slot="computer-pane" className="flex h-full min-w-0 flex-col">
    <header className="flex h-10 shrink-0 items-center gap-2 border-b px-3">
      <Button type="button" variant="ghost" size="icon-xs" aria-label="Close computer" onClick={onClose}><ChevronLeftIcon aria-hidden /></Button>
      <h2 id="side-pane-title" tabIndex={-1} className="truncate text-sm font-medium outline-none">Computer</h2>
      {view.kind === "running" && <Button type="button" size="xs" variant={takenOver ? "default" : "outline"} className="ml-auto" disabled={busy} onClick={() => void toggleTakeover()}>
        {takenOver ? "Done" : "Take over"}
      </Button>}
    </header>
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-2">
      {actionError && <p role="alert" className="text-xs text-destructive-foreground">{actionError}</p>}
      {view.kind === "loading"
        ? error
          ? <div className="flex flex-col items-start gap-2 p-2"><p role="alert" className="selectable text-sm text-destructive-foreground">{error}</p><Button type="button" size="xs" variant="outline" onClick={() => void refresh()}>Try again</Button></div>
          : <p role="status" className="p-2 text-sm text-muted-foreground">Loading…</p>
        : view.kind === "setup"
          ? <Setup reason={view.reason} detail={view.detail} onChanged={refresh} />
          : view.kind === "running"
            ? <>
              {view.takenOver && <p role="status" className="text-xs text-muted-foreground">You have the mouse and keyboard. The bot waits until you click Done.</p>}
              <LiveScreen botId={botId} interactive={view.takenOver} />
              <ComputerTerminal botId={botId} />
            </>
            : <StateNote view={view} />}
    </div>
  </section>;
}

function StateNote({ view }: { view: { kind: "off" } | { kind: "starting" } | { kind: "stopping" } | { kind: "failed"; error: string } }) {
  if (view.kind === "failed") {
    return <div role="status" className="flex flex-col gap-1 p-2 text-sm">
      <p className="selectable text-destructive-foreground">{view.error}</p>
      <p className="text-muted-foreground">It starts again the next time the bot uses it.</p>
    </div>;
  }
  const text = view.kind === "starting" ? "Starting computer…"
    : view.kind === "stopping" ? "The computer is shutting down…"
      : "The computer is off. It starts when the bot uses it.";
  return <p role="status" className="p-2 text-sm text-muted-foreground">{text}</p>;
}

function Setup({ reason, detail, onChanged }: { reason: string; detail: string; onChanged: () => Promise<void> }) {
  const copy = setupCopy(reason);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<string | null>(null);
  const run = async () => {
    setBusy(true);
    setFailed(null);
    try {
      if (copy.action === "enable") await Sandbox.enable();
      else if (copy.action === "install") await Sandbox.install();
      await onChanged();
    } catch (e) {
      setFailed(errorText(e));
    } finally {
      setBusy(false);
    }
  };
  const label = busy
    ? copy.action === "install" ? "Downloading…" : copy.action === "enable" ? "Enabling…" : "Checking…"
    : failed || copy.action === "recheck" ? "Try again" : "Enable";
  return <div className="flex max-w-md flex-col items-start gap-2 p-2">
    <h3 className="text-sm font-medium">{copy.title}</h3>
    <p className="text-sm text-muted-foreground">{copy.body}</p>
    {detail && <p className="selectable text-xs text-muted-foreground">{detail}</p>}
    {failed && <p role="alert" className="selectable text-xs text-destructive-foreground">{failed}</p>}
    {copy.action && <Button type="button" size="sm" disabled={busy} onClick={() => void run()}>{label}</Button>}
    <p className="text-xs text-muted-foreground">{SECURITY_NOTE}</p>
  </div>;
}

/** The terminal inside the computer, a daemon terminal like any other: it stays open when the tab closes and ends with the computer. */
function ComputerTerminal({ botId }: { botId: string }) {
  const fontSize = useSettings((s) => s.terminal.fontSize);
  const [id, setId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [attempt, setAttempt] = useState(0);
  const [active, setActive] = useState(false);
  useEffect(() => {
    let live = true;
    setId(null);
    setError(null);
    Sandbox.openTerminal(botId, 100, 24).then(
      (info) => { if (live) setId(info.id); },
      (e: unknown) => { if (live) setError(errorText(e)); },
    );
    return () => { live = false; };
  }, [botId, attempt]);
  return <div className="flex min-h-40 flex-[2] flex-col">
    {id
      ? <TerminalPane id={id} active={active} onFocus={() => setActive(true)} fontSize={fontSize} />
      : error
        ? <div className="flex items-center gap-2 p-2 text-sm"><p role="alert" className="selectable text-destructive-foreground">The terminal could not open: {error}</p><Button type="button" size="xs" variant="outline" onClick={() => setAttempt((a) => a + 1)}>Retry</Button></div>
        : <p role="status" className="p-2 text-sm text-muted-foreground">Opening the terminal…</p>}
  </div>;
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `bun run --cwd frontend test src/features/computer src/features/chat/BotHeader.test.tsx src/stores/chat.test.ts src/features/chat/Conversation.test.tsx`
Expected: PASS (Conversation tests still pass with the new `BotActions`).

- [ ] **Step 7: Commit**

```bash
git add frontend/src/features/computer/ComputerTab.tsx frontend/src/features/computer/ComputerTab.test.tsx frontend/src/stores/chat.ts frontend/src/stores/chat.test.ts frontend/src/features/chat/SidePane.tsx frontend/src/features/chat/BotHeader.tsx frontend/src/features/chat/BotHeader.test.tsx
git commit -m "feat(computer): computer tab with setup, live screen, takeover and terminal

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 11: "Using the computer" chat card and the Needs-you deep link

**Files:**
- Create: `frontend/src/features/computer/ComputerCard.tsx`
- Modify: `frontend/src/features/chat/parts/MessageParts.tsx`, `frontend/src/features/notifications/targets.ts`, `frontend/src/features/notifications/NotificationCenter.tsx` (bot avatar for `computer:` targets)
- Test: `frontend/src/features/chat/parts/MessageParts.test.tsx` (add a case), `frontend/src/features/notifications/targets.test.ts` (create)

**Interfaces:**
- Consumes: `MsgPart` (`type: "computer"`, `status`, `text`), `Bubble` (`features/chat/parts/Bubble`), `useChat.openComputerPane` (Task 10), `useUI`.
- Produces: `export function ComputerCard({ part }: { part: MsgPart }): JSX.Element`; `parseTarget` returns `type: "task" | "bot" | "automation" | "computer"`; `openTarget("computer:<botId>")` opens the Computer pane.

- [ ] **Step 1: Write the failing tests**

Add to `frontend/src/features/chat/parts/MessageParts.test.tsx` (inside `describe("message parts", ...)`):

```tsx
  it("draws the bot's computer use as one card that opens the Computer tab", () => {
    useChat.setState({ selectedBotId: "a", pane: null });
    view(message([
      { type: "computer", status: "done", text: "Starting computer…" },
      { type: "text", text: "Opening the site." },
      { type: "computer", status: "running", text: "browser_open https://example.test" },
    ]));
    expect(screen.getAllByText("Using the computer")).toHaveLength(1);
    expect(screen.getByText("browser_open https://example.test")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Using the computer/ }));
    expect(useChat.getState().pane).toEqual({ kind: "computer", id: "a" });
  });
```

Create `frontend/src/features/notifications/targets.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/mygo", async () => (await import("@/features/chat/builder/testing")).mygoMock);

import { useChat } from "@/stores/chat";
import { useUI } from "@/stores/ui";
import { openTarget, parseTarget } from "./targets";

afterEach(() => useChat.setState({ selectedBotId: null, pane: null }));

describe("notification targets", () => {
  it("parses a computer target", () => {
    expect(parseTarget("computer:b1")).toEqual({ type: "computer", id: "b1" });
    expect(parseTarget("computer:")).toBeNull();
  });

  it("opens the bot's Computer tab for a computer target", () => {
    expect(openTarget("computer:b1")).toBe(true);
    expect(useChat.getState().selectedBotId).toBe("b1");
    expect(useChat.getState().pane).toEqual({ kind: "computer", id: "b1" });
    expect(useUI.getState().view).toBe("chat");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `bun run --cwd frontend test src/features/chat/parts/MessageParts.test.tsx src/features/notifications/targets.test.ts`
Expected: FAIL (no "Using the computer"; `parseTarget("computer:b1")` is `null`).

- [ ] **Step 3: Implement**

Create `frontend/src/features/computer/ComputerCard.tsx`:

```tsx
import { MonitorIcon } from "lucide-react";
import { Bubble } from "@/features/chat/parts/Bubble";
import { cn } from "@/lib/utils";
import type { MsgPart } from "@/mygo";
import { useChat } from "@/stores/chat";

/**
 * The bot used its computer in this reply: one compact card with what it is doing now. Clicking it opens the
 * Computer tab beside the conversation, where the screen and terminal are.
 */
export function ComputerCard({ part }: { part: MsgPart }) {
  const botId = useChat((s) => s.selectedBotId);
  const open = useChat((s) => s.pane?.kind === "computer");
  return (
    <Bubble className={cn("p-0", open && "ring-1 ring-ring/60")}>
      <button type="button" data-slot="computer-card" aria-pressed={open} disabled={!botId} onClick={() => { if (botId) useChat.getState().openComputerPane(botId); }}
        className="flex w-full min-w-56 items-center gap-3 rounded-[inherit] px-3 py-1.5 text-start outline-none transition-colors hover:bg-foreground/5 focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none motion-reduce:transition-none">
        <MonitorIcon aria-hidden className="size-4 shrink-0 text-muted-foreground" />
        <span className="flex min-w-0 flex-col">
          <span className="font-medium">Using the computer</span>
          {part.text && <span className="line-clamp-2 break-words text-muted-foreground">{part.text}</span>}
        </span>
      </button>
    </Bubble>
  );
}
```

In `frontend/src/features/chat/parts/MessageParts.tsx`:
- import: `import { ComputerCard } from "@/features/computer/ComputerCard";`
- after `const tasks = ...` add:
  ```ts
  // Every use of the computer in a reply is one card where the first one was, showing the latest step.
  const computer = message.parts.filter((p) => p.type === "computer");
  ```
- in the `switch`, before `default:` add:
  ```tsx
      case "computer":
        if (part === computer[0]) blocks.push(<ComputerCard key="computer" part={computer[computer.length - 1]!} />);
        break;
  ```

Replace `frontend/src/features/notifications/targets.ts` with:

```ts
import { useAgents } from "@/stores/agents";
import { useChat } from "@/stores/chat";
import { useUI } from "@/stores/ui";
import { openTask } from "@/features/projects/tasks";

type TargetType = "task" | "bot" | "automation" | "computer";
const TYPES: readonly TargetType[] = ["task", "bot", "automation", "computer"];

/** What a notification is about, from the daemon's `target`: "task:<id>", "bot:<id>", "automation:<id>" or "computer:<botId>". */
export function parseTarget(target: string): { type: TargetType; id: string } | null {
  const at = target.indexOf(":");
  const type = target.slice(0, at) as TargetType;
  const id = target.slice(at + 1);
  if (at < 1 || !id || !TYPES.includes(type)) return null;
  return { type, id };
}

/** Opens what the notification is about. Returns false when it names nothing this window can open. */
export function openTarget(target: string): boolean {
  const t = parseTarget(target);
  if (!t) return false;
  if (t.type === "task") openTask(t.id);
  else if (t.type === "bot") useChat.getState().open(t.id);
  else if (t.type === "computer") { useChat.getState().open(t.id); useChat.getState().openComputerPane(t.id); }
  else useUI.getState().focusAutomation(t.id);
  return true;
}

/** True when the focused window already shows what the notification is about, so it needs no cue. */
export function targetOnScreen(target: string): boolean {
  const t = parseTarget(target);
  if (!t || document.hidden || !document.hasFocus()) return false;
  const { view } = useUI.getState();
  if (t.type === "task") return view === "agents" && useAgents.getState().openId === t.id;
  if (t.type === "bot") return view === "chat" && useChat.getState().selectedBotId === t.id;
  if (t.type === "computer") {
    const chat = useChat.getState();
    return view === "chat" && chat.selectedBotId === t.id && chat.pane?.kind === "computer";
  }
  return false;
}
```

In `frontend/src/features/notifications/NotificationCenter.tsx`, in `Mark`, change the bot lookup to cover computer targets:

```tsx
  const bot = useChat((s) => target?.type === "bot" || target?.type === "computer" ? s.bots.find((b) => b.id === target.id) : undefined);
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `bun run --cwd frontend test src/features/chat/parts src/features/notifications`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/features/computer/ComputerCard.tsx frontend/src/features/chat/parts/MessageParts.tsx frontend/src/features/chat/parts/MessageParts.test.tsx frontend/src/features/notifications/targets.ts frontend/src/features/notifications/targets.test.ts frontend/src/features/notifications/NotificationCenter.tsx
git commit -m "feat(chat): using-the-computer card and computer notification target

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 12: Docs, checklist, changelog and attribution

**Files:**
- Modify: `docs/architecture/bot-computer.md` (phases 1-3 created it; keep their internal sections), `docs/security/threat-model.md`, `docs/architecture/security-permissions.md`, `CHANGELOG.md`, `docs/research/upstream-attribution.md`
- Create: `docs/development/bot-computer-e2e.md`

**Interfaces:**
- Consumes: behaviour from Tasks 1-11 and phases 1-3.
- Produces: user-facing docs; no code.

- [ ] **Step 1: Architecture doc**

In `docs/architecture/bot-computer.md`, add (or replace if a phase left a stub) these sections after the introduction and before the phase 1-3 internals:

```markdown
## What the user sees

- A bot gets a computer when **Computer** is switched on in its Tools (off by default). Nothing is created until the
  bot first uses it; the computer then starts ("Starting computer…" in chat), stops after 15 minutes without bot
  activity or anyone watching, and stops when the daemon exits. Its disk, files and browser logins stay.
- The bot's title bar gets a **Computer** button that opens the Computer tab beside the conversation. A "Using the
  computer" card in the chat and the "Needs you" notification from `ask_user_to_take_over` (target
  `computer:<botId>`) open the same tab.
- The Computer tab shows, in this order of precedence:
  - the **setup screen** when this PC cannot run one yet: Windows Hypervisor Platform off (Enable, one UAC prompt,
    then restart), restart pending (Try again), runtime not downloaded (Enable downloads and verifies it),
    virtualization off in the firmware (Try again after fixing it), not Windows 11 (no action). It always states the
    network and login risks below;
  - the computer's state when it is not running: off, starting, shutting down, or why it failed;
  - while it runs: the **live screen** (view-only), **Take over** / **Done**, and a **terminal** inside the computer.
- There is deliberately no start, stop, reset or wipe button, and no resource display.
- Take over gives the user the mouse and keyboard; the bot's browser tools are refused until Done. Closing the tab or
  switching bot while holding control hands it back.
- Commands the bot runs are echoed in the open terminal (dim `[bot] $ <command>`, output, exit code). The terminal
  is a daemon terminal: it survives closing the tab or restarting the GUI, and ends when the computer stops.

## Live screen and terminal transport

- `sandbox.screen.open{botId}` (only for a running computer; the UI never boots one) opens the guest's `/screen`
  stream and returns `{id}`. Guest bytes go out as `sandbox.screen.data{id, data, closed?, error?}` events in chunks
  of at most 128 KiB; RFB is client-pull, so a stream cannot flood IPC. A bot keeps at most two screens.
- Viewer bytes come back through `sandbox.screen.input{id, data}` and pass `internal/sandbox/rfbfilter.go`, which
  accepts only RFB 3.8 with security None, forces a shared session, strips the ExtendedMouseButtons and xvp
  pseudo-encodings, always drops power (xvp) and resize messages, and forwards keyboard, pointer, clipboard and QEMU
  key messages **only while the user holds the takeover lock**. Anything else closes the stream. noVNC's `viewOnly`
  is cosmetic.
- noVNC 1.7.0 is given a `ScreenChannel` (`frontend/src/features/computer/screenChannel.ts`) that looks like a
  WebSocket (`send`, `close`, `binaryType`, `protocol`, `readyState`, `onopen/onmessage/onclose/onerror`) but is
  carried by MyGo calls and events. No socket and no URL exist.
- `sandbox.pty.open{botId,cols,rows}` adopts the guest PTY into `terminals.Manager` (owner `sandbox:<botId>`), so the
  ordinary `terminals.*` calls and `terminal.output` events drive it; an open one is reused.
- `sandbox.viewer{botId,watching}` is a lease the tab renews every 60 s; the daemon clears it after 150 s without
  renewal, so a crashed GUI does not keep a computer running.

## Security notes

- **Open internet, including your network.** The computer uses QEMU user-mode NAT with no allowlist. It can reach the
  internet, devices on your home or office network, and services listening on this PC (for example a local database
  or admin page). Do not give a bot a computer if that is not acceptable.
- **Logins persist.** Whatever you sign in to during a takeover stays signed in on the bot's disk, and the bot can
  use those sessions afterwards. There is no button to wipe them yet; deleting the bot deletes its disk.
- **Credentials you type stay in the computer.** Keystrokes during a takeover go to the guest browser through the
  daemon; Orole never stores screen bytes, keystrokes or clipboard text in SQLite or logs. The audit log records only
  that a screen or terminal was opened for a bot.
- **No host files, no host listener.** The computer sees only its own disk; host-guest traffic uses a user-only local
  socket or pipe, never a network interface.
- The bot must never solve CAPTCHAs or log in for you: it asks you to take over instead.
```

- [ ] **Step 2: Threat model and security-permissions**

In `docs/security/threat-model.md`:
- add to the Assets table:
  `| Bot computers (VM disks with browser profiles and logins) | <data>\sandbox\bots\<bot>.qcow2 | use of the user's web sessions by the bot or by malware that reaches the guest |`
- add trust boundary 7 after item 6:
  `7. **Bot computer guest -> host:** each bot's VM is untrusted; it talks to the daemon only over a per-VM token-authenticated local channel and never sees host files, but its user-mode NAT gives it the internet, the LAN and host services. See [Bot computers](#bot-computers).`
- add a section before `## Prompt injection`:

```markdown
## Bot computers

| Threat | Control | Gap |
|---|---|---|
| Guest reaches the home network or services on the host | documented in the setup screen, CHANGELOG and `docs/architecture/bot-computer.md`; off by default per bot | no egress proxy or allowlist yet; the guest can reach anything the user's PC can |
| Bot or injected page uses the user's saved logins | logins come only from user takeovers; browser tools are policy-checked per bot tool access | logins persist until the bot is deleted; no wipe UI yet |
| UI types into the guest without a takeover | `rfbFilter` in the daemon drops keyboard, pointer and clipboard unless `Manager.TakenOver`; noVNC view-only is cosmetic | none known |
| Viewer power-cycles or resizes the guest | xvp and SetDesktopSize always dropped; xvp pseudo-encoding stripped | none known |
| Screen stream floods IPC | RFB client-pull, 128 KiB chunks, two screens per bot | a hostile guest can still send one large update per request |
| Credentials typed during takeover leak to Orole storage | screen bytes and keystrokes are never logged or stored | they live in the guest browser profile (by design) |
| Crashed GUI keeps VMs running | viewer lease expires after 150 s | none known |
```

In `docs/architecture/security-permissions.md`, under `## Decision engine (internal/policy)` after the bot tool access bullet, add:

```markdown
- Bot computer tools (`computer.exec`, `computer.files.read|write`, `computer.browser.read|write` on
  `computer/<botID>/...`) are ordinary policy actions for `bot:<id>` with the effects in the bot computer plan;
  `ask_user_to_take_over` is itself a user prompt. The takeover lock lives in the daemon: while the user holds it,
  browser tools are refused and only then does `sandbox.screen.input` let keyboard and mouse through
  (`internal/sandbox/rfbfilter.go`). The Computer tab's view-only mode is not a control.
```

- [ ] **Step 3: Manual checklist**

Create `docs/development/bot-computer-e2e.md`:

```markdown
# Bot computer end-to-end checklist

A manual run on real hardware. Design and behaviour: `docs/architecture/bot-computer.md`. Use a dev data dir
(`$env:OROLE_HOME = ".orole-dev"`) unless you mean to test your real data; never delete `%LOCALAPPDATA%\Orole`.
Start the daemon in the foreground (`go run ./cmd/daemon -stderr -v`) and the app (`go tool mygo dev`). Note Windows
build, CPU and RAM at the top of your run notes.

1. [ ] **Fresh install to ready.** With Windows Hypervisor Platform off, switch **Computer** on in a bot's Tools and
   open the Computer tab: the setup screen explains WHP and the network risk. Click Enable, accept UAC, restart
   Windows. The tab now offers the download; click Enable: it downloads, verifies and then shows "The computer is off.
   It starts when the bot uses it." Corrupt a downloaded file (or block the network) and confirm the tab shows the
   error and Try again, and that nothing half-downloaded remains under `<data>\sandbox\`.
2. [ ] **Run a command.** Ask the bot to run `python -c "print(1+1)"`. Chat shows "Starting computer…" then the
   result `2`; the Computer tab shows the screen and the terminal echoes `[bot] $ python -c "print(1+1)"`, `2` and
   `(exit 0)`. Type `ls` in the terminal yourself and see output.
3. [ ] **Watch a website.** Ask the bot to open `https://example.com`. The page appears on the live screen while the
   bot works. Clicking or typing on the screen does nothing (view-only).
4. [ ] **Takeover.** Ask the bot to log in to a site that needs your password. A "Needs you" notification appears;
   clicking it opens the bot's Computer tab. Click Take over, log in, click Done. The bot continues and uses the
   logged-in page. While you held control, a bot browser step was refused with "the user is controlling the computer".
5. [ ] **Persistence.** Quit the app and stop the daemon, start both again, and ask the bot to read a file it wrote in
   step 2 and to reopen the site from step 4: the file is there and you are still logged in.
6. [ ] **Idle stop and cap.** Leave a computer idle with the tab closed for 15 minutes: it stops (Task Manager shows no
   QEMU for it). With the tab open it does not stop. Start computers for five bots: at most four run; an idle one is
   stopped for the fifth, or the fifth waits about 30 s and then reports "all computers are busy".
7. [ ] **Windows versions.** Run items 1-6 on the developer's Windows 11 21H2 PC and, if one is available, on 24H2.
   Record boot time to "running" and the QEMU process's memory for each.
```

- [ ] **Step 4: CHANGELOG and attribution**

In `CHANGELOG.md` under `## Unreleased`, add an `### Added` section above `### Changed` (create the heading if it is not there) with:

```markdown
### Added
- Bots can have their own computer: a small Linux machine on your PC with its own disk, a terminal and a browser. Switch on **Computer** in a bot's Tools; the computer starts the first time the bot uses it and stops after 15 minutes without use. A new Computer button in the bot's title bar (and the "Using the computer" card in chat) opens the Computer tab, where you watch the bot's screen live, use a terminal inside the computer, and see the commands the bot runs. When the bot needs you (to log in or solve a CAPTCHA), you get a "Needs you" notification: click **Take over** to use the mouse and keyboard, then **Done** to hand back. The first time, the tab walks you through turning on Windows Hypervisor Platform (one administrator prompt and a restart) and downloading the computer. Be aware: the computer has open internet access and can reach your home network and services on this PC, and logins you make on it stay on the bot's disk (there is no button to clear them yet; deleting the bot deletes its disk). Windows 11 only.
```

In `docs/research/upstream-attribution.md`, add after the DiceBear section:

```markdown
### noVNC (bundled npm dependency, not copied)

`@novnc/novnc` 1.7.0 (npm, https://github.com/novnc/noVNC, MPL-2.0; its `vendor/pako` is MIT) draws the bot
computer's live screen in `frontend/src/features/computer/LiveScreen.tsx`. No noVNC file is copied or modified in
this repository; Vite bundles the unmodified package, whose license and notices ship in `node_modules/@novnc/novnc`
(`LICENSE.txt`, `docs/LICENSE*`). Under MPL-2.0 the noVNC source files stay under MPL-2.0 and their source is
available from the upstream repository at tag `v1.7.0`. Orole's `ScreenChannel` adapter
(`frontend/src/features/computer/screenChannel.ts`) and type declarations (`frontend/src/types/novnc.d.ts`) are
original, written against noVNC's documented API (`docs/API.md`) and `core/websock.js` raw-channel contract.
```

- [ ] **Step 5: Check and commit**

Run: `git diff --check`
Expected: no whitespace errors.

```bash
git add docs/architecture/bot-computer.md docs/security/threat-model.md docs/architecture/security-permissions.md docs/development/bot-computer-e2e.md CHANGELOG.md docs/research/upstream-attribution.md
git commit -m "docs(sandbox): bot computer behaviour, security notes and e2e checklist

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 13: Full verification, push and draft PR

**Files:** none new.

**Interfaces:**
- Consumes: everything above.
- Produces: branch `feat/199-computer-tab` on origin and a draft PR into `feat/198-bot-computer-tools`.

- [ ] **Step 1: Go checks**

Run: `go fmt ./... && go vet ./... && go test -race ./...`
Expected: no files reformatted (or commit them in a `style:` fixup if any), vet clean, all tests PASS.

- [ ] **Step 2: Frontend checks and bindings are current**

Run: `bun run --cwd frontend check && go tool mygo generate && git status --porcelain frontend/src/mygo.ts`
Expected: tsc, oxlint and vitest pass; `mygo.ts` unchanged by the regenerate (empty porcelain output).

- [ ] **Step 3: UI rule scan**

Run: `git diff feat/198-bot-computer-tools --name-only -- frontend/src | xargs grep -n " title=" || true`
Expected: no native `title` attributes in changed files.

- [ ] **Step 4: Smoke the tab in the browser preview**

Run `bun run --cwd frontend dev`, open http://localhost:5173, open a bot with Computer on. Expected in a browser (no daemon): the Computer button shows and the tab renders its load error with Try again, without console errors from noVNC (noVNC loads only when a computer is running). Note results in the PR body; the full flow is the manual checklist.

- [ ] **Step 5: Push and open the draft PR**

```bash
git push -u origin feat/199-computer-tab
gh pr create --draft --base feat/198-bot-computer-tools --head feat/199-computer-tab \
  --title "feat(computer): computer tab, live view, terminal and docs" \
  --body "$(cat <<'EOF'
## Summary
- Computer tab beside a bot's conversation: setup screen (Enable / Try again / reason), off / starting / failed states, live screen (noVNC, view-only) with Take over / Done, and a terminal inside the computer.
- Daemon: `sandbox.viewer` lease, `sandbox.screen.open/input/close` with `sandbox.screen.data` events, `sandbox.pty.open` adopting the guest PTY into the terminals manager; bot commands echoed in the terminal.
- Screen input is filtered in Go (`internal/sandbox/rfbfilter.go`): keyboard, mouse and clipboard reach the guest only during a takeover; power and resize messages are always dropped.
- "Using the computer" chat card and `computer:<botId>` notification target open the tab.
- Docs: architecture, threat model, security permissions, manual e2e checklist, CHANGELOG, noVNC (MPL-2.0) attribution.

## Contract additions
`sandbox.screen.data` gains `closed`/`error`; `sandbox.screen.open` returns `{id}`; viewer is a 150 s lease; notification target `computer:<botId>`; message part `type: "computer"`; `Guest.OnExec` / `Config.OnExec` for the echo. See the plan's "Contract issues".

## Test plan
- [ ] `go test -race ./...`
- [ ] `bun run --cwd frontend check`
- [ ] Manual: `docs/development/bot-computer-e2e.md` items 1-7

Closes #199

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: a draft PR URL. Do not merge.

---

## Self-review notes

- Spec coverage: Computer tab with setup/starting/view-only/taken-over states (Tasks 8-10), noVNC over IPC with no socket (Tasks 3, 5-7, 9), takeover in Go (Tasks 2-3, 5), xterm terminal via existing plumbing (Tasks 1, 5, 10), command echo (Tasks 1, 4, 5), chat card (Task 11), Needs-you deep link (Task 11, contract issue 2), viewer flag for the idle timer (Tasks 3, 5, 8), docs/threat model/checklist/CHANGELOG/attribution (Task 12), `mygo generate` committed (Task 6), draft PR (Task 13). Start/stop/reset buttons and resource display are intentionally absent.
- Type consistency: `ScreenChunk` (Go `sandbox` with `[]byte`, app with base64 `string`), `Sandbox.*` TS method names (Task 6) are used verbatim in Tasks 7-11; `openComputerPane(botId)` and pane `{kind: "computer", id}` match across Tasks 10-11.
