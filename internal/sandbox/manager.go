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
	MaxRunning  int                    // 4
	IdleTimeout time.Duration          // 15m
	BootTimeout time.Duration          // 60s
	BusyWait    time.Duration          // 30s
	FreeMemory  func() (uint64, error) // bytes; injectable for tests; nil skips the check
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
	active    int       // unary guest requests in flight (open streams do not count)
	replaces  *instance // the stopped-for-eviction VM this one waits for before booting
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
	go m.idleLoop()
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
			return nil, ErrDeleting
		}
		if inst := m.vms[botID]; inst != nil {
			switch inst.state {
			case StateRunning:
				g := inst.guest
				if !g.alive() {
					// The channel died while the VM lives (a hung guest failed keepalive): treat it as a crash.
					g.abort(ErrCrashed)
					vm, gone := inst.vm, inst.gone
					m.mu.Unlock()
					_ = vm.Kill()
					select {
					case <-gone:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					continue
				}
				inst.lastUsed = m.cfg.Now()
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
			inst.replaces = victim
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
	// Every QEMU process counts, a stopping one too. A starting VM that waits for its eviction victim has no
	// process yet and is already represented by that victim.
	n := 0
	for _, inst := range m.vms {
		switch inst.state {
		case StateRunning, StateStopping:
			n++
		case StateStarting:
			if r := inst.replaces; r == nil || m.vms[r.botID] != r {
				n++
			}
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

// idleLocked: running with no request in flight, no viewer and no takeover. Open streams (a terminal) are not
// busy; the bytes moving on them refresh lastUsed instead.
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
	go func() { // Shutdown cancels a boot in progress
		select {
		case <-m.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	vm, g, err := m.start(ctx, inst.botID)

	m.mu.Lock()
	if err == nil && m.closed {
		err = errClosed
		_ = g.Close()
		_ = vm.Kill()
	} else if err != nil {
		vm = nil // start already stopped it
		if m.closed {
			err = errClosed
		}
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
	g.setOnTouch(func() { m.touchInstance(inst) })
	close(inst.ready)
	m.signalLocked()
	st := m.statusLocked(inst.botID)
	m.mu.Unlock()
	m.publish(st)
	go m.watch(inst)
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
	// The overlay is a delta on the base image it was made from: boot that version, and never another one.
	if found && rec.ImageVersion != img.Version {
		prev, ok := installedImage(m.cfg.Paths, rec.ImageVersion)
		if !ok {
			return nil, nil, fmt.Errorf("%w (version %s)", ErrImageGone, rec.ImageVersion)
		}
		img = prev
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
		m.logf(botID, "the computer failed to start: %q", err.Error())
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
		m.logf(botID, "the computer failed to start: %q", err.Error())
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

// touchInstance is the guest's stream-activity hook: bytes moved, so the bot is not idle.
func (m *Manager) touchInstance(inst *instance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst.state == StateRunning {
		inst.lastUsed = m.cfg.Now()
	}
}

// use is the guest's activity hook: unary requests count as busy and as bot activity.
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
	delete(m.vms, inst.botID)
	inst.state = StateOff
	inst.takenOver = false
	close(inst.gone)
	st := Status{BotID: inst.botID, State: StateOff, Error: ErrCrashed.Error()}
	m.last[inst.botID] = st
	m.signalLocked()
	m.mu.Unlock()
	m.logf(inst.botID, "the computer stopped unexpectedly: %q", fmt.Sprint(inst.vm.ExitErr()))
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
	var stoppingStatus []Status
	for _, inst := range m.vms {
		switch inst.state {
		case StateRunning:
			m.beginStopLocked(inst)
			running = append(running, inst)
			stoppingStatus = append(stoppingStatus, m.statusLocked(inst.botID))
		case StateStopping:
			stopping = append(stopping, inst)
		}
	}
	m.mu.Unlock()
	for _, st := range stoppingStatus {
		m.publish(st)
	}

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
