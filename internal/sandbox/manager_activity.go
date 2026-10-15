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

// Release hands control back to the bot. It is a no-op when the user does not hold the lock.
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
