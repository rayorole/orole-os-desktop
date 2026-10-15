package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/rayorole/orole-os-desktop/internal/daemon/api"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

var (
	// SandboxStatus fires when a bot's computer changes state or the user takes over or hands back.
	SandboxStatus = mygo.NewEvent[sandbox.Status]("sandbox:status")
	// SandboxScreen carries live screen bytes for an open screen; the last chunk of a stream has Closed set.
	SandboxScreen = mygo.NewEvent[ScreenChunk]("sandbox:screen")
	// SandboxAvailability fires when whether bot computers can run here may have changed (setup or download ended).
	SandboxAvailability = mygo.NewEvent[sandbox.Availability]("sandbox:availability")
	// SandboxRuntime reports the runtime download's progress and result.
	SandboxRuntime = mygo.NewEvent[sandbox.RuntimeState]("sandbox:runtime")
	// SandboxSetup reports the Windows Hypervisor Platform setup: running, then its result.
	SandboxSetup = mygo.NewEvent[sandbox.SetupState]("sandbox:setup")
	// SandboxHandoff fires when a bot starts or stops waiting for the user to take over its computer.
	SandboxHandoff = mygo.NewEvent[sandbox.HandoffState]("sandbox:handoff")
)

// ScreenChunk is a piece of a bot computer's screen stream; Data is base64.
type ScreenChunk struct {
	ID     string `json:"id"`
	Data   string `json:"data,omitempty"`
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

// forwardSandbox turns the daemon's sandbox.* events into page events. Every sandbox event it knows is consumed,
// malformed or not.
func forwardSandbox(event string, data json.RawMessage) bool {
	switch event {
	case "sandbox.status":
		broadcastJSON(SandboxStatus, data)
	case "sandbox.availability":
		broadcastJSON(SandboxAvailability, data)
	case "sandbox.runtime":
		broadcastJSON(SandboxRuntime, data)
	case "sandbox.setup":
		broadcastJSON(SandboxSetup, data)
	case "sandbox.handoff":
		broadcastJSON(SandboxHandoff, data)
	case "sandbox.screen.data":
		var c sandbox.ScreenChunk
		if json.Unmarshal(data, &c) == nil {
			out := ScreenChunk{ID: c.ID, Closed: c.Closed, Error: c.Error}
			if len(c.Data) > 0 {
				out.Data = string(mustB64(c.Data))
			}
			SandboxScreen.Broadcast(out)
		}
	default:
		return false
	}
	return true
}

func broadcastJSON[T any](ev *mygo.Event[T], data json.RawMessage) {
	var v T
	if json.Unmarshal(data, &v) == nil {
		ev.Broadcast(v)
	}
}

// callFunc is Link.call; a seam for tests.
type callFunc func(ctx context.Context, method string, params, out any) error

// screenInputParams is sandbox.screen.input's request; Data is base64 in JSON.
type screenInputParams struct {
	ID   string `json:"id"`
	Data []byte `json:"data"`
}

// Sandbox is the bound service for a bot's computer, a local Linux VM the daemon runs. It only forwards: the daemon
// decides what a viewer may do (keyboard and mouse reach the VM only during a takeover). Setup and the runtime
// download run in the daemon and report progress as events (sandbox:setup, sandbox:runtime, sandbox:availability).
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

// Enable starts turning on Windows Hypervisor Platform through the elevated helper (one UAC prompt, then a
// restart). It answers with the setup's result when it ends within about 25 seconds, otherwise with Running set;
// the result then arrives as a sandbox:setup event.
func (s Sandbox) Enable(ctx context.Context) (sandbox.SetupState, error) {
	return enableSandbox(ctx, s.L.call, allowSetForeground)
}

// enableHealthTimeout bounds the daemon.health call that finds the daemon's process before the setup starts.
var enableHealthTimeout = 3 * time.Second

// enableSandbox lets the daemon, which launches the UAC prompt, take the foreground before asking it to start the
// setup: without that the prompt can open behind this window. The grant is best effort.
func enableSandbox(ctx context.Context, call callFunc, allow func(pid int) error) (sandbox.SetupState, error) {
	var h api.Health
	hctx, cancel := context.WithTimeout(ctx, enableHealthTimeout) // a slow daemon must not hold up the setup
	err := call(hctx, "daemon.health", struct{}{}, &h)
	cancel()
	if err == nil && h.PID > 0 {
		_ = allow(h.PID)
	}
	var out sandbox.SetupState
	return out, call(ctx, "sandbox.setup.enable", struct{}{}, &out)
}

// Install starts downloading and verifying the VM runtime and the guest image and returns at once with its state;
// progress follows as sandbox:runtime events. Calling it again while it runs returns the progress.
func (s Sandbox) Install(ctx context.Context) (sandbox.RuntimeState, error) {
	var out sandbox.RuntimeState
	return out, s.L.call(ctx, "sandbox.runtime.install", struct{}{}, &out)
}

// SetViewer renews (true) or ends (false) this window's viewer lease while the Computer tab is visible.
func (s Sandbox) SetViewer(ctx context.Context, botID string, watching bool) error {
	return s.L.call(ctx, "sandbox.viewer", map[string]any{"botId": botID, "watching": watching}, nil)
}

// TakeOver gives the user the mouse and keyboard; the bot's browser tools wait.
func (s Sandbox) TakeOver(ctx context.Context, botID string) error {
	return s.L.call(ctx, "sandbox.takeover", map[string]string{"botId": botID}, nil)
}

// Release hands the computer back to the bot. note ("" for none, at most 500 characters) reaches a bot that asked
// the user to take over.
func (s Sandbox) Release(ctx context.Context, botID string, note string) error {
	return s.L.call(ctx, "sandbox.release", map[string]string{"botId": botID, "note": note}, nil)
}

// Handoff says whether the bot is waiting for the user to take over its computer, and why.
func (s Sandbox) Handoff(ctx context.Context, botID string) (sandbox.HandoffState, error) {
	var out sandbox.HandoffState
	return out, s.L.call(ctx, "sandbox.handoff", map[string]string{"botId": botID}, &out)
}

// OpenScreen starts a live screen stream of a running computer and returns its id; bytes follow as sandbox:screen.
func (s Sandbox) OpenScreen(ctx context.Context, botID string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := s.L.call(ctx, "sandbox.screen.open", map[string]string{"botId": botID}, &out)
	return out.ID, err
}

// ScreenInput sends viewer bytes (base64) to a screen, in pieces of at most 128 KiB (the daemon closes a screen
// that receives more at once). Pieces go in order; the first failure stops the rest.
func (s Sandbox) ScreenInput(ctx context.Context, id, data string) error {
	return screenInput(ctx, s.L.call, id, data)
}

func screenInput(ctx context.Context, call callFunc, id, data string) error {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("screen input is not base64: %w", err)
	}
	for len(b) > 0 {
		n := min(len(b), sandbox.ScreenChunkMax)
		if err := call(ctx, "sandbox.screen.input", screenInputParams{ID: id, Data: b[:n]}, nil); err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
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
