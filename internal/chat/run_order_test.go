package chat

import (
	"sync/atomic"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/llm/llmtest"
)

// A run's chat.run running=false must be published while the Manager still holds the run state. Otherwise a message
// sent right after the run ends starts a new worker whose running=true can be published first, and clients that apply
// events in order show the bot idle (no Stop) for the whole new run.
func TestRunFinishedIsPublishedBeforeTheNextRunCanStart(t *testing.T) {
	var m atomic.Pointer[Manager]
	var finished, unordered atomic.Int32
	e := newEnv(t, func(int, llm.Request) []llm.Delta { return []llm.Delta{llmtest.Text("ok")} }, func(cfg *Config) {
		publish := cfg.Publish
		cfg.Publish = func(event string, payload any) {
			publish(event, payload)
			rs, ok := payload.(RunState)
			mgr := m.Load()
			if !ok || event != "chat.run" || rs.Running || rs.Paused || mgr == nil {
				return
			}
			finished.Add(1)
			// TryLock succeeds only if nothing holds m.mu, i.e. the worker already let go of the run state.
			if mgr.mu.TryLock() {
				mgr.mu.Unlock()
				unordered.Add(1)
			}
		}
	})
	m.Store(e.m)
	b := e.bot("Order")
	e.send(b.ID, "hi")
	e.idle(b.ID)
	if finished.Load() != 1 {
		t.Fatalf("running=false published %d times, want 1", finished.Load())
	}
	if unordered.Load() != 0 {
		t.Fatal("running=false was published after the run state was released: a new run's running=true could overtake it")
	}
}
