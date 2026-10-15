package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type handoffResult struct {
	note string
	err  error
}

func askAsync(h *Handoffs, ctx context.Context, botID, reason string, takenOver bool) <-chan handoffResult {
	out := make(chan handoffResult, 1)
	go func() {
		note, err := h.Ask(ctx, botID, reason, takenOver)
		out <- handoffResult{note, err}
	}()
	return out
}

func waitPending(t *testing.T, h *Handoffs, botID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !h.State(botID).Pending {
		if time.Now().After(deadline) {
			t.Fatal("the wait never started")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHandoffWaitsForTheUserToFinish(t *testing.T) {
	var mu sync.Mutex
	var seen []HandoffState
	h := NewHandoffs(func(s HandoffState) { mu.Lock(); seen = append(seen, s); mu.Unlock() })
	h.AskTimeout = 200 * time.Millisecond
	h.HoldTimeout = 5 * time.Second
	res := askAsync(h, context.Background(), "b1", "Log in to example.com", false)
	waitPending(t, h, "b1")
	if st := h.State("b1"); st.Reason != "Log in to example.com" || st.Since == 0 {
		t.Fatalf("state: %+v", st)
	}
	h.Started("b1")
	time.Sleep(300 * time.Millisecond) // past AskTimeout: taking over switched to the hold timeout
	h.Finish("b1", "logged in")
	r := <-res
	if r.err != nil || r.note != "logged in" {
		t.Fatalf("Ask = %q %v", r.note, r.err)
	}
	if h.State("b1").Pending {
		t.Fatal("the wait must end")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || !seen[0].Pending || seen[1].Pending {
		t.Fatalf("changes: %+v", seen)
	}
}

func TestHandoffTimesOutWhenNobodyTakesOver(t *testing.T) {
	h := NewHandoffs(nil)
	h.AskTimeout = 20 * time.Millisecond
	if _, err := h.Ask(context.Background(), "b1", "help", false); !errors.Is(err, ErrHandoffTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestHandoffHoldTimesOutWhenTheUserNeverHandsBack(t *testing.T) {
	h := NewHandoffs(nil)
	h.AskTimeout = 5 * time.Second
	h.HoldTimeout = 30 * time.Millisecond
	res := askAsync(h, context.Background(), "b1", "help", false)
	waitPending(t, h, "b1")
	h.Started("b1")
	if r := <-res; !errors.Is(r.err, ErrHandoffHoldTimeout) {
		t.Fatalf("err = %v", r.err)
	}
}

func TestHandoffWhileAlreadyTakenOverUsesTheHoldTimeout(t *testing.T) {
	h := NewHandoffs(nil)
	h.AskTimeout = 10 * time.Millisecond
	h.HoldTimeout = 5 * time.Second
	res := askAsync(h, context.Background(), "b1", "help", true)
	waitPending(t, h, "b1")
	time.Sleep(50 * time.Millisecond)
	h.Finish("b1", "")
	if r := <-res; r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
}

func TestHandoffEndsWithTheTurn(t *testing.T) {
	h := NewHandoffs(nil)
	ctx, cancel := context.WithCancel(context.Background())
	res := askAsync(h, ctx, "b1", "help", false)
	waitPending(t, h, "b1")
	cancel()
	if r := <-res; !errors.Is(r.err, context.Canceled) {
		t.Fatalf("err = %v", r.err)
	}
	if h.State("b1").Pending {
		t.Fatal("a canceled wait must not stay pending")
	}
}

func TestHandoffOneWaitPerBot(t *testing.T) {
	h := NewHandoffs(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = askAsync(h, ctx, "b1", "first", false)
	waitPending(t, h, "b1")
	if _, err := h.Ask(ctx, "b1", "second", false); !errors.Is(err, ErrHandoffBusy) {
		t.Fatalf("err = %v", err)
	}
	h.Finish("b2", "nobody waits") // no-op
	h.Started("b2")                // no-op
}

func TestHandoffAskWithEndedContextDoesNothing(t *testing.T) {
	changes := 0
	h := NewHandoffs(func(HandoffState) { changes++ })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Ask(ctx, "b1", "help", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if changes != 0 || h.State("b1").Pending {
		t.Fatalf("an ended turn registered a wait (changes %d)", changes)
	}
}

// A Done or Take over that arrives together with the timeout must win: the select picks a ready case at random.
func TestHandoffSimultaneousFinishBeatsTheTimeout(t *testing.T) {
	for _, takenOver := range []bool{false, true} {
		for range 50 {
			var h *Handoffs
			first := true
			h = NewHandoffs(func(s HandoffState) {
				if s.Pending && first {
					first = false
					h.Finish("b1", "done")
				}
			})
			h.AskTimeout, h.HoldTimeout = 0, 0
			note, err := h.Ask(context.Background(), "b1", "help", takenOver)
			if err != nil || note != "done" {
				t.Fatalf("takenOver=%v: Ask = %q %v", takenOver, note, err)
			}
		}
	}
}

func TestHandoffSimultaneousTakeOverBeatsTheAskTimeout(t *testing.T) {
	for range 50 {
		var h *Handoffs
		first := true
		h = NewHandoffs(func(s HandoffState) {
			if s.Pending && first {
				first = false
				h.Started("b1")
			}
		})
		h.AskTimeout, h.HoldTimeout = 0, 5*time.Second
		res := askAsync(h, context.Background(), "b1", "help", false)
		time.Sleep(20 * time.Millisecond)
		select {
		case r := <-res:
			t.Fatalf("Ask ended early: %+v", r)
		default:
		}
		h.Finish("b1", "ok")
		if r := <-res; r.err != nil || r.note != "ok" {
			t.Fatalf("Ask = %+v", r)
		}
	}
}

func TestHandoffCancelEndsTheWaitWithTheError(t *testing.T) {
	h := NewHandoffs(nil)
	res := askAsync(h, context.Background(), "b1", "help", false)
	waitPending(t, h, "b1")
	h.Cancel("b2", ErrHandoffStopped) // nobody waits: no-op
	h.Cancel("b1", ErrHandoffStopped)
	if r := <-res; !errors.Is(r.err, ErrHandoffStopped) {
		t.Fatalf("err = %v", r.err)
	}
	if h.State("b1").Pending {
		t.Fatal("a canceled wait must not stay pending")
	}
}

// Begin registers the wait at once, so a Take over or Done that arrives before Wait runs is not lost; the change
// callback fires only once Wait runs (outside the caller's locks).
func TestHandoffBeginRegistersBeforeWaiting(t *testing.T) {
	changes := 0
	h := NewHandoffs(func(HandoffState) { changes++ })
	w, err := h.Begin(context.Background(), "b1", "help", false)
	if err != nil {
		t.Fatal(err)
	}
	if !h.State("b1").Pending || changes != 0 {
		t.Fatalf("pending %v changes %d", h.State("b1").Pending, changes)
	}
	if _, err := h.Begin(context.Background(), "b1", "again", false); !errors.Is(err, ErrHandoffBusy) {
		t.Fatalf("second Begin: %v", err)
	}
	h.Started("b1")
	h.Finish("b1", "early")
	if note, err := w.Wait(context.Background()); err != nil || note != "early" {
		t.Fatalf("Wait = %q %v", note, err)
	}
	if h.State("b1").Pending || changes != 2 {
		t.Fatalf("pending %v changes %d", h.State("b1").Pending, changes)
	}
}
