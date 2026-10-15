// Package app is the desktop process's side of Orole OS: the typed MyGo
// services the React frontend calls. Every method forwards to orole-daemon
// over authenticated local IPC; the desktop process holds no durable state.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/rayorole/orole-os-desktop/internal/daemon/api"
	"github.com/rayorole/orole-os-desktop/internal/daemon/control"
	"github.com/rayorole/orole-os-desktop/internal/dictation"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

// Typed events delivered to pages.
var (
	// TerminalOutput carries pty output; Data is base64.
	TerminalOutput = mygo.NewEvent[TerminalChunk]("terminal:output")
	// TerminalChanged fires when a terminal starts or exits.
	TerminalChanged = mygo.NewEvent[terminals.Info]("terminal:changed")
	// TerminalRemoved fires when a terminal is closed.
	TerminalRemoved = mygo.NewEvent[TerminalRef]("terminal:removed")
	// SettingChanged fires when a setting changes.
	SettingChanged = mygo.NewEvent[SettingChange]("settings:changed")
	// DaemonState reports connection state of the daemon link.
	DaemonState = mygo.NewEvent[DaemonLink]("daemon:state")
)

// TerminalChunk is a piece of terminal output.
type TerminalChunk struct {
	ID   string `json:"id"`
	Seq  uint64 `json:"seq"`
	Data string `json:"data"`
}

// TerminalRef identifies a terminal.
type TerminalRef struct {
	ID string `json:"id"`
}

// SettingChange is a changed setting.
type SettingChange struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// DaemonLink is the link status shown in the status bar.
type DaemonLink struct {
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
}

// Link manages the connection to the daemon.
type Link struct {
	DataDir string
	// DaemonExe is the orole-daemon binary to launch when none is running ("" = search).
	DaemonExe   string
	mu          sync.Mutex
	c           *ipc.Client
	maintenance bool
}

// NewLink creates the link; Start connects (launching the daemon if needed).
func NewLink(dataDir string) *Link { return &Link{DataDir: dataDir} }

// Start ensures the daemon is running and subscribes to events.
func (l *Link) Start(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maintenance {
		return fmt.Errorf("desktop update in progress")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.c != nil {
		return nil
	}
	c, err := control.Ensure(ctx, l.DataDir, l.DaemonExe, "desktop")
	if err != nil {
		DaemonState.Broadcast(DaemonLink{Connected: false, Error: err.Error()})
		return err
	}
	c.OnEvent(forward)
	if err := c.Subscribe(ctx, "terminal.*", "settings.*", "projects.*", "worktrees.*", "github.*", "files.*", "notifications.*", "agents.*", "mcp.*", "policy.*", "applications.*", "plugins.*", "chat.*", "llm.*", "dictation.*", "sandbox.*"); err != nil {
		c.Close()
		return err
	}
	l.c = c
	DaemonState.Broadcast(DaemonLink{Connected: true})
	return nil
}

// beginMaintenance closes this window's link before stopping the daemon, so
// health polling cannot relaunch an old binary while its resources are replaced.
func (l *Link) beginMaintenance() error {
	l.mu.Lock()
	if l.maintenance {
		l.mu.Unlock()
		return fmt.Errorf("desktop update already in progress")
	}
	l.maintenance = true
	c := l.c
	l.c = nil
	l.mu.Unlock()
	if c != nil {
		c.Close()
	}
	return nil
}

func (l *Link) endMaintenance() {
	l.mu.Lock()
	l.maintenance = false
	l.mu.Unlock()
}

func forward(event string, data json.RawMessage) {
	forwardMCP(event, data)
	if forwardPlugins(event, data) || forwardChat(event, data) || forwardDictation(event, data) || forwardSandbox(event, data) {
		return
	}
	if forwardApplications(event, data) {
		return
	}
	if forwardAgents(event, data) {
		return
	}
	if forwardFilesNotifications(event, data) {
		return
	}
	if forwardGitHub(event, data) {
		return
	}
	if forwardProjects(event, data) {
		return
	}
	if forwardJarvis(event, data) {
		return
	}
	switch event {
	case "terminal.output":
		var o struct {
			ID   string `json:"id"`
			Seq  uint64 `json:"seq"`
			Data []byte `json:"data"`
		}
		if json.Unmarshal(data, &o) == nil {
			TerminalOutput.Broadcast(TerminalChunk{ID: o.ID, Seq: o.Seq, Data: string(mustB64(o.Data))})
		}
	case "terminal.changed":
		var i terminals.Info
		if json.Unmarshal(data, &i) == nil {
			TerminalChanged.Broadcast(i)
		}
	case "terminal.removed":
		var r TerminalRef
		if json.Unmarshal(data, &r) == nil {
			TerminalRemoved.Broadcast(r)
		}
	case "settings.changed":
		var s SettingChange
		if json.Unmarshal(data, &s) == nil {
			SettingChanged.Broadcast(s)
		}
	}
}

func (l *Link) call(ctx context.Context, method string, params, out any) error {
	l.mu.Lock()
	if l.maintenance {
		l.mu.Unlock()
		return fmt.Errorf("desktop update in progress")
	}
	c := l.c
	l.mu.Unlock()
	if c == nil {
		if err := l.Start(ctx); err != nil {
			return fmt.Errorf("daemon unavailable: %w", err)
		}
		l.mu.Lock()
		c = l.c
		if l.maintenance || c == nil {
			l.mu.Unlock()
			return fmt.Errorf("desktop update in progress")
		}
		l.mu.Unlock()
	}
	timeout := 30 * time.Second
	switch method {
	case "mcp.call", "mail.send", "mail.reply", "mail.trash", "drive.trash",
		"mail.folders", "mail.list", "mail.search", "mail.thread",
		"drive.list", "drive.search", "drive.stat", "drive.preview",
		"plugins.trust", "plugins.run", "llm.manifests.trust", "cli.providers.install", "cli.providers.login",
		"github.issue.agent.start", "github.issue.pr.publish",
		"agents.start", "agents.restart", "agents.prompt", "agents.sendMessage", "agents.check", "agents.review",
		"agents.handoff", "agents.reviewWith", "agents.tryN", "applications.launch",
		"machines.probe", "machines.pair", "machines.access", "machines.remove",
		"machines.health", "machines.execute", "machines.files.list", "machines.files.stat",
		"machines.files.read", "machines.files.write",
		"files.list", "files.stat", "files.read", "files.write", "files.search",
		"jarvis.configure", "jarvis.verify.start", "jarvis.call.test", "jarvis.voices", "jarvis.voices.add", "jarvis.models":
		timeout = 16 * time.Minute // five-minute approval followed by a bounded tool call
	case "sandbox.setup.enable":
		timeout = time.Minute // the daemon answers within about 25 s (then the result follows as events)
	case "dictation.transcribe":
		timeout = dictation.TranscribeTimeout + 15*time.Second // the daemon bounds it itself; this only catches a lost reply
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return c.Call(ctx, method, params, out)
}

// Daemon is the bound service for daemon health and lifecycle.
type Daemon struct{ L *Link }

// Health returns the daemon's status snapshot.
func (d Daemon) Health(ctx context.Context) (api.Health, error) {
	var h api.Health
	err := d.L.call(ctx, "daemon.health", struct{}{}, &h)
	return h, err
}

// Stop shuts the daemon down (agents and terminals owned by it end).
func (d Daemon) Stop(ctx context.Context) error {
	return control.Stop(ctx, d.L.DataDir)
}

// Settings is the bound service for persisted preferences.
type Settings struct{ L *Link }

// All returns every stored setting as JSON.
func (s Settings) All(ctx context.Context) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	err := s.L.call(ctx, "settings.list", struct{}{}, &m)
	return m, err
}

// Set stores one setting; value must be valid JSON.
func (s Settings) Set(ctx context.Context, key string, value json.RawMessage) error {
	return s.L.call(ctx, "settings.set", map[string]any{"key": key, "value": value}, nil)
}

// Terminals is the bound service for pseudoterminal sessions.
type Terminals struct{ L *Link }

// Shells lists installed shells.
func (t Terminals) Shells(ctx context.Context) ([]terminals.Shell, error) {
	var out []terminals.Shell
	err := t.L.call(ctx, "terminals.shells", struct{}{}, &out)
	return out, err
}

// List returns live and exited sessions.
func (t Terminals) List(ctx context.Context) ([]terminals.Info, error) {
	var out []terminals.Info
	err := t.L.call(ctx, "terminals.list", struct{}{}, &out)
	return out, err
}

// Create starts a terminal.
func (t Terminals) Create(ctx context.Context, shell string, cwd string, cols, rows int) (terminals.Info, error) {
	var out terminals.Info
	err := t.L.call(ctx, "terminals.create", terminals.CreateOptions{Shell: shell, Cwd: cwd, Cols: cols, Rows: rows}, &out)
	return out, err
}

// CreatePersistent starts a Linux terminal in this Orole home's tmux server.
// Unsupported platforms return an error rather than silently using a native PTY.
func (t Terminals) CreatePersistent(ctx context.Context, shell string, cwd string, cols, rows int) (terminals.Info, error) {
	var out terminals.Info
	err := t.L.call(ctx, "terminals.create", terminals.CreateOptions{Shell: shell, Cwd: cwd, Cols: cols, Rows: rows, Persistence: "tmux"}, &out)
	return out, err
}

// PersistentSessions lists surviving tmux sessions after a daemon restart.
func (t Terminals) PersistentSessions(ctx context.Context) ([]terminals.PersistentSession, error) {
	var out []terminals.PersistentSession
	err := t.L.call(ctx, "terminals.persistent", struct{}{}, &out)
	return out, err
}

// Restore reconnects to a tmux session without relaunching its command.
func (t Terminals) Restore(ctx context.Context, id string, cols, rows int) (terminals.Info, error) {
	var out terminals.Info
	err := t.L.call(ctx, "terminals.restore", map[string]any{"id": id, "cols": cols, "rows": rows}, &out)
	return out, err
}

// Write sends input; data is raw text (keystrokes/paste, "\u0003" for Ctrl-C).
func (t Terminals) Write(ctx context.Context, id string, data string) error {
	return t.L.call(ctx, "terminals.write", map[string]any{"id": id, "data": []byte(data)}, nil)
}

// Resize changes the pty size.
func (t Terminals) Resize(ctx context.Context, id string, cols, rows int) error {
	return t.L.call(ctx, "terminals.resize", map[string]any{"id": id, "cols": cols, "rows": rows}, nil)
}

// TerminalAttachment is scrollback to restore a screen after reconnect.
type TerminalAttachment struct {
	Info terminals.Info `json:"info"`
	Data string         `json:"data"` // base64
	Seq  uint64         `json:"seq"`
}

// Attach returns scrollback; live chunks with seq <= Seq are already included.
func (t Terminals) Attach(ctx context.Context, id string) (TerminalAttachment, error) {
	var a terminals.Attachment
	if err := t.L.call(ctx, "terminals.attach", map[string]string{"id": id}, &a); err != nil {
		return TerminalAttachment{}, err
	}
	return TerminalAttachment{Info: a.Info, Data: string(mustB64(a.Data)), Seq: a.Seq}, nil
}

// Close terminates and removes a session.
func (t Terminals) Close(ctx context.Context, id string) error {
	return t.L.call(ctx, "terminals.close", map[string]string{"id": id}, nil)
}
