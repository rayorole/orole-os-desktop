package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
)

// sandboxRef names one bot's computer.
type sandboxRef struct {
	BotID string `json:"botId"`
}

// sandboxReplyWait is how long sandbox.setup.enable waits for the setup to finish before it answers with
// "running" (the IPC call deadline is 30 s; a UAC prompt can stay open longer). The result follows as events.
const sandboxReplyWait = 25 * time.Second

// Manager timings; sandboxDeleteTimeout covers a delete that waits for a boot (BootTimeout) and then stops the VM.
const (
	sandboxBootTimeout   = 60 * time.Second
	sandboxBusyWait      = 30 * time.Second
	sandboxDeleteTimeout = sandboxBootTimeout + 2*time.Minute
)

// sandboxSetupRun is one Windows Hypervisor Platform setup in flight; callers that arrive meanwhile share it.
type sandboxSetupRun struct {
	done  chan struct{}
	state sandbox.SetupState
	err   error
}

// registerSandbox owns the bot computers: one QEMU/WHPX VM per bot (internal/sandbox), the pinned runtime
// download and the one-time Windows Hypervisor Platform setup. VMs are children of this daemon (job object)
// and stop with it; disks under <data>\sandbox survive.
//
// IPC: sandbox.availability, sandbox.status{botId}, sandbox.setup.enable (one UAC prompt, audited),
// sandbox.runtime.install (starts the background download, returns its state; calling it again while it runs
// returns progress), and the takeover methods (registerSandboxTakeover). Events: sandbox.status,
// sandbox.availability, sandbox.runtime, sandbox.setup, sandbox.handoff.
//
// Setup and install are user actions: they run with the daemon's lifetime, not the request's, so closing the
// window or a call timeout does not abort them, and report progress as events.
func (d *Daemon) registerSandbox() {
	paths := sandbox.Paths{Root: filepath.Join(d.DataDir, "sandbox")}
	life, cancel := context.WithCancel(context.Background())
	d.sandboxLife, d.sandboxStop = life, cancel
	d.sandboxStore = sandbox.NewStore(d.DB.DB)

	var m *sandbox.Manager
	rt := sandbox.NewRuntime(sandbox.RuntimeConfig{Paths: paths, Pins: sandbox.DefaultPins,
		OnChange: func(s sandbox.RuntimeState) {
			d.IPC.Publish("sandbox.runtime", s)
			if !s.Installing && m != nil && life.Err() == nil { // no availability probe once the daemon is exiting
				d.IPC.Publish("sandbox.availability", m.Availability(life))
			}
		}})
	m = sandbox.NewManager(sandbox.Config{
		Paths: paths, MaxRunning: 4, IdleTimeout: 15 * time.Minute, BootTimeout: sandboxBootTimeout, BusyWait: sandboxBusyWait,
		FreeMemory: d.freeMemory, Now: time.Now, Image: rt.Image,
	}, sandbox.NewHostBackend(rt, paths), d.sandboxStore)
	d.Sandbox = m
	m.Subscribe(func(s sandbox.Status) { d.IPC.Publish("sandbox.status", s) })
	m.Subscribe(d.computerStatusChanged) // a takeover request ends when its computer stops
	d.registerSandboxTakeover(managerHost{m})
	// Run calls this once the daemon owns the IPC endpoint (a refused second daemon must not delete anything).
	d.sandboxStart = func() { d.sandboxGo(func() { d.sweepBotComputers(paths) }) }

	// The helper is always the file next to this executable, never a path from IPC or settings: it runs elevated.
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

	var setupMu sync.Mutex
	var setup *sandboxSetupRun
	handle(d, "sandbox.setup.enable", func(ctx context.Context, _ none) (sandbox.SetupState, error) {
		if err := requireDesktopUser(ctx, "turn on Windows Hypervisor Platform"); err != nil {
			return sandbox.SetupState{}, err
		}
		setupMu.Lock()
		run := setup
		if run == nil {
			run = &sandboxSetupRun{done: make(chan struct{})}
			// If a UAC prompt is still open when the daemon exits, shutdownSandbox stops waiting for this
			// goroutine and its audit row may be lost (the database is closed by then).
			started := d.sandboxGo(func() {
				d.IPC.Publish("sandbox.setup", sandbox.SetupState{Running: true})
				res, err := sandbox.EnableWHP(life, helper, paths)
				st := sandbox.SetupState{RebootRequired: res.RebootRequired}
				detail := "enabled"
				if res.RebootRequired {
					detail = "enabled, restart required"
				}
				if err != nil {
					st.Error, detail = err.Error(), err.Error()
				}
				d.audit("sandbox.setup", "HypervisorPlatform", outcomeOf(err), detail)
				run.state, run.err = st, err
				setupMu.Lock()
				setup = nil
				setupMu.Unlock()
				close(run.done)
				d.IPC.Publish("sandbox.setup", st)
				d.IPC.Publish("sandbox.availability", m.Availability(life))
			})
			if !started {
				setupMu.Unlock()
				return sandbox.SetupState{}, &ipc.Error{Code: ipc.CodeCanceled, Message: "Orole is shutting down"}
			}
			setup = run
		}
		setupMu.Unlock()
		wait := time.NewTimer(sandboxReplyWait)
		defer wait.Stop()
		select {
		case <-run.done:
			return run.state, run.err
		case <-wait.C:
		case <-ctx.Done():
		}
		return sandbox.SetupState{Running: true}, nil
	})

	handle(d, "sandbox.runtime.install", func(ctx context.Context, _ none) (sandbox.RuntimeState, error) {
		if err := requireDesktopUser(ctx, "download the computer runtime"); err != nil {
			return sandbox.RuntimeState{}, err
		}
		if !sandbox.DefaultPins.Complete() {
			return rt.State(), sandbox.ErrNoPins
		}
		before := rt.State()
		st := rt.Start(life) // the daemon's lifetime: the download outlives this request
		if !before.Installing && !before.Ready {
			d.audit("sandbox.runtime.install", sandbox.DefaultPins.ImageVersion, "allowed", "download started")
		}
		return st, nil
	})
}

// requireDesktopUser refuses a call made for a bot or an automation. IPC calls come from the desktop app or the
// CLI (the token holder, the user), whose context carries neither; this keeps it that way if a bot tool or an
// automation step is ever routed to these handlers.
func requireDesktopUser(ctx context.Context, what string) error {
	if agents.ActorFrom(ctx) != "" || len(events.ChainFrom(ctx)) > 0 {
		return &ipc.Error{Code: ipc.CodeDenied, Message: "only you can " + what + ", from the Orole app"}
	}
	return nil
}

// freeMemory is the Manager's free-RAM check (refuse a boot below VM RAM + 1 GB).
func (d *Daemon) freeMemory() (uint64, error) {
	s, err := d.Metrics.Snapshot(context.Background())
	return s.MemoryAvailableBytes, err
}

// sandboxGo runs fn in a goroutine that shutdownSandbox waits for, unless the daemon is already shutting down
// (then it returns false and fn never runs), so Add never races the shutdown's Wait.
func (d *Daemon) sandboxGo(fn func()) bool {
	d.sandboxMu.Lock()
	defer d.sandboxMu.Unlock()
	if d.sandboxClosing {
		return false
	}
	d.sandboxWG.Add(1)
	go func() {
		defer d.sandboxWG.Done()
		fn()
	}()
	return true
}

// deleteBotComputer stops an archived bot's VM and removes its disk and row in the background: a boot in
// progress is waited for (up to the boot timeout), which must not hold up the archive call. The result reaches
// the UI as a sandbox.status event; a delete cut short by daemon exit is finished by the next start's sweep.
// Archiving is how bots are deleted. A bot that never had a computer is a no-op.
func (d *Daemon) deleteBotComputer(botID string) {
	if d.Sandbox == nil {
		return
	}
	d.sandboxGo(func() { d.deleteBotComputerNow(botID, "Bot archived") })
}

func (d *Daemon) deleteBotComputerNow(botID, why string) {
	ctx, cancel := context.WithTimeout(d.sandboxLife, sandboxDeleteTimeout)
	defer cancel()
	_, had, err := d.sandboxStore.Get(ctx, botID)
	if err != nil {
		d.Log.Warn("delete bot computer", "bot", botID, "err", err)
		had = true // unknown: audit the delete rather than hide it
	}
	running := d.Sandbox.Status(botID).State != sandbox.StateOff
	if err := d.Sandbox.Delete(ctx, botID); err != nil {
		if errors.Is(err, sandbox.ErrDeleting) {
			return // already in progress: that delete finishes the job and audits it
		}
		d.Log.Warn("delete bot computer", "bot", botID, "err", err)
		if d.sandboxLife.Err() != nil {
			return // cut short by daemon exit: the next start's sweep finishes it
		}
		d.audit("sandbox.delete", botID, "failed", err.Error())
		return
	}
	if had || running {
		d.audit("sandbox.delete", botID, "allowed", why)
	}
}

// sweepBotComputers deletes the computers of bots that are archived or gone: a delete that daemon exit cut short,
// or an overlay left without a row. It runs once per daemon start.
func (d *Daemon) sweepBotComputers(paths sandbox.Paths) {
	ctx := d.sandboxLife
	ids := map[string]bool{}
	rows, err := d.DB.QueryContext(ctx, `SELECT bot_id FROM sandboxes`)
	if err != nil {
		d.Log.Warn("sandbox sweep", "err", err)
		return
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids[id] = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		d.Log.Warn("sandbox sweep", "err", err)
		return
	}
	if entries, err := os.ReadDir(filepath.Join(paths.Root, "bots")); err == nil {
		for _, e := range entries {
			if id, ok := strings.CutSuffix(e.Name(), ".qcow2"); ok && id != "" && !e.IsDir() {
				ids[id] = true
			}
		}
	}
	for id := range ids {
		if ctx.Err() != nil {
			return
		}
		var live bool
		err := d.DB.QueryRowContext(ctx, `SELECT archived_at IS NULL FROM bots WHERE id=?`, id).Scan(&live)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			d.deleteBotComputerNow(id, "Bot no longer exists")
		case err != nil:
			d.Log.Warn("sandbox sweep", "bot", id, "err", err)
		case !live:
			d.deleteBotComputerNow(id, "Bot archived")
		}
	}
}

// shutdownSandbox ends the background setup and downloads, then stops every VM cleanly on daemon exit (the job
// object kills any that do not stop).
func (d *Daemon) shutdownSandbox() {
	d.sandboxMu.Lock()
	d.sandboxClosing = true // no new background work: sandboxGo refuses from now on
	d.sandboxMu.Unlock()
	if d.sandboxStop != nil {
		d.sandboxStop()
	}
	if d.Sandbox != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := d.Sandbox.Shutdown(ctx); err != nil {
			d.Log.Warn("sandbox shutdown", "err", err)
		}
		cancel()
	}
	// Background setup, deletes and the sweep write to the database, which closes next. Deletes and the sweep end
	// with the lifetime context; a UAC prompt still open keeps the setup in ShellExecuteEx, which no context
	// interrupts: do not hang the exit on it (its audit row is then lost).
	done := make(chan struct{})
	go func() { d.sandboxWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		d.Log.Warn("sandbox shutdown: the Windows Hypervisor Platform setup is still waiting for its UAC prompt")
	}
}
