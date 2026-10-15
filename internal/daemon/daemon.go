// Package daemon wires orole-daemon: database, IPC server, and services.
package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	oroleos "github.com/rayorole/orole-os-desktop"
	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/automations"
	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/chat/memory"
	"github.com/rayorole/orole-os-desktop/internal/cliruntime"
	"github.com/rayorole/orole-os-desktop/internal/daemon/api"
	"github.com/rayorole/orole-os-desktop/internal/database"
	"github.com/rayorole/orole-os-desktop/internal/dictation"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/gateway"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/jarvis"
	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/llm/manifest"
	"github.com/rayorole/orole-os-desktop/internal/machines"
	"github.com/rayorole/orole-os-desktop/internal/mcp"
	"github.com/rayorole/orole-os-desktop/internal/plugins"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/internal/projects"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/skills"
	"github.com/rayorole/orole-os-desktop/internal/systemmetrics"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
	"github.com/rayorole/orole-os-desktop/migrations"
)

// Daemon is the long-lived background controller.
type Daemon struct {
	DataDir     string
	Log         *slog.Logger
	DB          *database.DB
	IPC         *ipc.Server
	Terminals   *terminals.Manager
	Projects    *projects.Service
	Events      *events.Bus
	Automations *automations.Engine
	MCP         *mcp.Manager
	Policy      *policy.Engine
	Machines    *machines.Manager
	Plugins     *plugins.Manager
	// CLI finds, installs and reads the sign-in state of the Claude, Codex and Grok CLIs (svc_cliruntime.go).
	CLI *cliruntime.Manager
	// LLM resolves provider credentials and models (svc_llm.go); registered before Chat.
	LLM llm.Service
	// LLMManifests stores declarative OpenAI-compatible providers and their trust (svc_llm_manifests.go).
	LLMManifests *manifest.Service
	// Dictation is local speech recognition for the composers (svc_dictation.go).
	Dictation *dictation.Service
	// Chat owns bots, conversations and runs (svc_chat.go).
	Chat *chat.Manager
	// ChatMemory is the chat memory subsystem attached to Chat (svc_chat_memory.go).
	ChatMemory *memory.Store
	// Jarvis places and takes the owner's phone calls for bots (svc_jarvis.go).
	Jarvis *jarvis.Service
	// Sandbox runs one local VM per bot (svc_sandbox.go). sandboxLife is the lifetime of its background setup,
	// downloads, deletes and startup sweep (sandboxStart); sandboxStop ends them and sandboxWG waits for them;
	// sandboxClosing (under sandboxMu) refuses new ones once shutdown began. sandboxStore is its table.
	Sandbox        *sandbox.Manager
	sandboxLife    context.Context
	sandboxStop    context.CancelFunc
	sandboxStart   func()
	sandboxMu      sync.Mutex
	sandboxClosing bool
	sandboxWG      sync.WaitGroup
	sandboxStore   *sandbox.Store
	Metrics        *systemmetrics.Sampler
	// Skills is the central skill registry and Gateway the MCP gateway hub behind the
	// `orole-daemon mcp-gateway` processes (svc_skills.go). gatewayExe is this executable.
	Skills     *skills.Store
	Gateway    *gateway.Hub
	gatewayExe string
	hub        *skills.Hub // skills.sh catalogue and installer (svc_skills.go)
	syncMu     sync.Mutex  // serializes native skill/MCP-config syncs
	// Agents runs coding-agent sessions; agentsStart runs once this daemon owns the IPC
	// endpoint (crash recovery must not run in a refused second daemon), agentsShutdown on exit.
	Agents                      *agents.Manager
	agentsStart, agentsShutdown func()
	gh                          *ghService  // GitHub service; agents publish through its client
	notices                     *appNotices // reports tasks, bots, flows and approvals to the notification center
	Token                       string
	Endpoint                    string
	started                     time.Time
	stop                        chan struct{}
	stopOnce                    sync.Once
}

// New opens state and registers services. Call Run to serve.
func New(dataDir string, log *slog.Logger) (*Daemon, error) {
	db, err := database.Open(filepath.Join(dataDir, "orole.db"), migrations.FS)
	if err != nil {
		return nil, fmt.Errorf("database: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	d := &Daemon{DataDir: dataDir, Log: log, DB: db, Token: hex.EncodeToString(buf), started: time.Now(), stop: make(chan struct{})}
	d.Endpoint = ipc.Endpoint(dataDir)
	d.IPC = ipc.NewServer(d.Token, oroleos.Version, os.Getpid(), log)
	d.Terminals = terminals.NewManager(terminals.NewBackend(), termSink{d})
	d.Events = events.NewBus()
	d.registerServices()
	return d, nil
}

// Run serves IPC until ctx is done or shutdown is requested.
func (d *Daemon) Run(ctx context.Context) error {
	ln, err := ipc.Listen(d.Endpoint)
	if err != nil {
		if d.Jarvis != nil {
			d.Jarvis.Close()
		}
		if d.ChatMemory != nil {
			d.ChatMemory.Close()
		}
		if d.Chat != nil {
			d.Chat.Close() // releases the chat storage root so the data directory can be removed
		}
		d.notices.close()
		d.Terminals.Shutdown()
		if d.Dictation != nil {
			d.Dictation.Close()
		}
		d.DB.Close()
		return fmt.Errorf("listen %s (is another daemon running?): %w", d.Endpoint, err)
	}
	// Token is written only after we own the endpoint, readable by this user only.
	if err := os.WriteFile(api.TokenPath(d.DataDir), []byte(d.Token), 0o600); err != nil {
		ln.Close()
		return err
	}
	if d.MCP != nil {
		if err := d.MCP.Start(ctx); err != nil {
			d.cleanup(ln)
			return fmt.Errorf("mcp: %w", err)
		}
	}
	if d.agentsStart != nil {
		d.agentsStart()
	}
	if d.sandboxStart != nil {
		d.sandboxStart() // deletes computers of archived bots: only once this daemon owns the endpoint
	}
	if d.Automations != nil {
		if err := d.Automations.Start(ctx); err != nil {
			d.cleanup(ln)
			return fmt.Errorf("automations: %w", err)
		}
	}
	if d.Jarvis != nil {
		d.Jarvis.Start(ctx)
	}
	d.notices.reconcile()
	d.Log.Info("daemon listening", "endpoint", d.Endpoint, "version", oroleos.Version, "pid", os.Getpid(), "go", runtime.Version())
	errc := make(chan error, 1)
	go func() { errc <- d.IPC.Serve(ln) }()
	select {
	case <-ctx.Done():
	case <-d.stop:
	case err := <-errc:
		d.cleanup(ln)
		return err
	}
	d.cleanup(ln)
	return nil
}

func (d *Daemon) cleanup(ln interface{ Close() error }) {
	ln.Close()
	d.IPC.CloseAll()
	if d.Automations != nil {
		d.Automations.Close()
	}
	if d.agentsShutdown != nil {
		d.agentsShutdown()
	}
	if d.Jarvis != nil {
		d.Jarvis.Close() // before Chat.Close (voice turns run in chat) and notices.close (it reports there)
	}
	d.notices.close() // after the runtimes stopped: drains what they reported last
	if d.ChatMemory != nil {
		d.ChatMemory.Close() // cancels in-flight consolidation so Chat.Close does not wait on a model call
	}
	if d.Chat != nil {
		d.Chat.Close()
	}
	d.shutdownSandbox() // after Chat.Close: no bot turn uses a computer any more; before DB.Close: the Store writes on stop
	if d.Plugins != nil {
		d.Plugins.Close()
	}
	if d.LLMManifests != nil {
		d.LLMManifests.Close() // cancels in-flight provider requests built from manifests
	}
	if d.MCP != nil {
		d.MCP.Close()
	}
	if d.Machines != nil {
		d.Machines.Close()
	}
	if d.Dictation != nil {
		d.Dictation.Close() // stops downloads and the speech engine helper
	}
	d.Terminals.Shutdown()
	os.Remove(api.TokenPath(d.DataDir))
	d.DB.Close()
	d.Log.Info("daemon stopped")
}

// Shutdown asks Run to return.
func (d *Daemon) Shutdown() { d.stopOnce.Do(func() { close(d.stop) }) }

type termSink struct{ d *Daemon }

type outputEvent struct {
	ID   string `json:"id"`
	Seq  uint64 `json:"seq"`
	Data []byte `json:"data"` // base64 in JSON
}

func (t termSink) Output(id string, seq uint64, data []byte) {
	t.d.IPC.Publish("terminal.output", outputEvent{id, seq, data})
}
func (t termSink) Changed(info terminals.Info) { t.d.IPC.Publish("terminal.changed", info) }

// Removed tells the GUI that the manager reaped an exited session.
func (t termSink) Removed(id string) {
	t.d.IPC.Publish("terminal.removed", map[string]string{"id": id})
}

// handle registers a typed method.
func handle[P any, R any](d *Daemon, name string, f func(ctx context.Context, p P) (R, error)) {
	d.IPC.Handle(name, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, &ipc.Error{Code: ipc.CodeBadRequest, Message: err.Error()}
			}
		}
		return f(ctx, p)
	})
}

type none struct{}
