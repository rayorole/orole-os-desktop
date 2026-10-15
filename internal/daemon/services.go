package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"time"

	oroleos "github.com/rayorole/orole-os-desktop"
	"github.com/rayorole/orole-os-desktop/internal/daemon/api"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

// registerServices wires every daemon module. Each module lives in its own file
// (svc_<name>.go) and adds one line here.
func (d *Daemon) registerServices() {
	d.notices = newAppNotices(d) // first: every runtime below reports to it
	d.registerCore()
	d.registerAutomations()
	d.registerMCP()
	d.registerSkills() // before registerAgents: agent launches read the skills index and gateway exe
	d.registerPlugins()
	d.registerMetrics()
	d.registerApplications()
	d.registerMailDrive()
	d.registerProjects()
	d.registerGitHub()
	d.registerMachines()
	d.registerFiles()
	d.registerAgents()
	d.registerImages()
	d.registerPublish()
	d.registerDictation()   // local speech recognition; independent of the other services
	d.registerCLIRuntimes() // before registerLLM: the CLI providers resolve through d.CLI
	d.registerSandbox()     // before registerChat: archiving a bot deletes its computer; phase 3 tools read d.Sandbox
	d.registerLLM()         // first of the chat services: registerChat resolves providers through d.LLM
	d.registerChat()        // after registerLLM; registerChatMemory and registerChatTools attach to d.Chat
	d.registerChatMemory()  // after registerChat: memory, knowledge and skill proposals; before registerChatTools
	d.registerChatTools()   // last of the chat services: lists knowledge_search only if registerChatMemory (before it) added it
	d.registerJarvis()      // after registerChatTools: phone calls run bot turns through d.Chat
}

func (d *Daemon) registerCore() {
	handle(d, "daemon.health", func(ctx context.Context, _ none) (api.Health, error) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		v, err := d.DB.Version()
		if err != nil {
			return api.Health{}, err
		}
		return api.Health{Version: oroleos.Version, PID: os.Getpid(), StartedAt: d.started, UptimeSec: int64(time.Since(d.started).Seconds()),
			DBVersion: v, Platform: runtime.GOOS + "/" + runtime.GOARCH, GoVersion: runtime.Version(), Goroutines: runtime.NumGoroutine(),
			MemAllocMB: float64(ms.Alloc) / (1 << 20), Terminals: len(d.Terminals.List()), ProtocolVer: ipc.ProtocolVersion}, nil
	})
	handle(d, "daemon.shutdown", func(ctx context.Context, _ none) (none, error) {
		d.Log.Info("shutdown requested over IPC")
		go func() { time.Sleep(100 * time.Millisecond); d.Shutdown() }()
		return none{}, nil
	})

	// Settings: opaque JSON values keyed by string.
	handle(d, "settings.list", func(ctx context.Context, _ none) (map[string]json.RawMessage, error) {
		rows, err := d.DB.QueryContext(ctx, `SELECT key, value FROM settings`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]json.RawMessage{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				return nil, err
			}
			out[k] = json.RawMessage(v)
		}
		return out, rows.Err()
	})
	handle(d, "settings.set", func(ctx context.Context, p struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}) (none, error) {
		if p.Key == "" || len(p.Key) > 128 || !json.Valid(p.Value) || len(p.Value) > 256<<10 {
			return none{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "invalid key or value"}
		}
		if jarvisProtectedKey(p.Key) { // owner number, verification and tick state change only through jarvis.* methods
			return none{}, &ipc.Error{Code: ipc.CodeBadRequest, Message: "use jarvis.configure to change Jarvis settings"}
		}
		_, err := d.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			p.Key, string(p.Value), time.Now().Unix())
		if err == nil {
			d.IPC.Publish("settings.changed", map[string]any{"key": p.Key, "value": p.Value})
			if p.Key == "agents" {
				d.jarvisSweep(ctx) // bot permissions may have dropped below what Jarvis needs
			}
		}
		return none{}, err
	})

	// Terminals.
	handle(d, "terminals.shells", func(ctx context.Context, _ none) ([]terminals.Shell, error) { return d.Terminals.Shells(), nil })
	handle(d, "terminals.list", func(ctx context.Context, _ none) ([]terminals.Info, error) { return d.Terminals.List(), nil })
	handle(d, "terminals.persistent", func(ctx context.Context, _ none) ([]terminals.PersistentSession, error) {
		return d.Terminals.PersistentSessions()
	})
	handle(d, "terminals.restore", func(ctx context.Context, p struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}) (terminals.Info, error) {
		info, err := d.Terminals.Restore(p.ID, p.Cols, p.Rows)
		if err == nil {
			d.audit("terminal.restore", info.ID, "allowed", p.ID)
			d.IPC.Publish("terminal.changed", info)
		}
		return info, err
	})
	handle(d, "terminals.create", func(ctx context.Context, p terminals.CreateOptions) (terminals.Info, error) {
		p.Owner = "" // owners are assigned by trusted daemon code only
		p.Command = ""
		info, err := d.Terminals.Create(p)
		if err == nil {
			d.audit("terminal.create", info.ID, "allowed", info.Shell)
			d.IPC.Publish("terminal.changed", info)
		}
		return info, err
	})
	handle(d, "terminals.write", func(ctx context.Context, p struct {
		ID   string `json:"id"`
		Data []byte `json:"data"`
	}) (none, error) {
		return none{}, d.Terminals.Write(p.ID, p.Data)
	})
	handle(d, "terminals.resize", func(ctx context.Context, p struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}) (none, error) {
		return none{}, d.Terminals.Resize(p.ID, p.Cols, p.Rows)
	})
	handle(d, "terminals.attach", func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (terminals.Attachment, error) {
		return d.Terminals.Attach(p.ID)
	})
	handle(d, "terminals.close", func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) (none, error) {
		err := d.Terminals.Close(p.ID)
		if err == nil {
			d.IPC.Publish("terminal.removed", map[string]string{"id": p.ID})
		}
		return none{}, err
	})
}

func (d *Daemon) audit(action, target, outcome, detail string) {
	_, err := d.DB.Exec(`INSERT INTO audit_events(at,actor,action,target,outcome,detail) VALUES(?,?,?,?,?,?)`,
		time.Now().Unix(), "desktop", action, target, outcome, detail)
	if err != nil && !errors.Is(err, sql.ErrConnDone) {
		d.Log.Warn("audit write failed", "err", err)
	}
}
