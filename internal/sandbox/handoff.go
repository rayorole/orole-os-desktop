package sandbox

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Handoff timeouts. DefaultHandoffAsk matches policy.DefaultApprovalTimeout: a takeover request waits as long as an
// approval for the user to press Take over; after that the bot waits until Done, at most DefaultHandoffHold.
const (
	DefaultHandoffAsk  = 5 * time.Minute
	DefaultHandoffHold = 30 * time.Minute
)

var (
	ErrHandoffTimeout     = errors.New("sandbox: the user did not take over in time")
	ErrHandoffHoldTimeout = errors.New("sandbox: the user did not hand the computer back in time")
	ErrHandoffBusy        = errors.New("sandbox: already waiting for the user to take over")
	ErrHandoffStopped     = errors.New("sandbox: the computer stopped while waiting for the user")
)

// HandoffState is the sandbox.handoff event payload and the sandbox.handoff method's answer.
type HandoffState struct {
	BotID   string `json:"botId"`
	Pending bool   `json:"pending"`
	Reason  string `json:"reason,omitempty"`
	Since   int64  `json:"since,omitempty"` // unix ms
}

// Handoffs tracks bots that asked the user to take over their computer (ask_user_to_take_over). A wait ends when
// the user presses Done (Finish), when nobody took over within AskTimeout, when the user took over but did not
// finish within HoldTimeout, when the bot's turn ends, or when Cancel ends it (the computer stopped). It holds no VM
// state: the lock itself is Manager.TakeOver.
type Handoffs struct {
	AskTimeout  time.Duration
	HoldTimeout time.Duration

	onChange func(HandoffState)
	mu       sync.Mutex
	waits    map[string]*handoff
}

type handoff struct {
	reason  string
	since   time.Time
	started chan struct{} // closed when the user takes over
	took    bool
	done    chan handoffEnd // how the wait ended, buffered 1
}

// handoffEnd is the user's note (Finish) or why the wait was canceled (Cancel).
type handoffEnd struct {
	note string
	err  error
}

// NewHandoffs returns Handoffs with the default timeouts. onChange (may be nil) is called without locks held
// whenever a wait starts or ends.
func NewHandoffs(onChange func(HandoffState)) *Handoffs {
	return &Handoffs{AskTimeout: DefaultHandoffAsk, HoldTimeout: DefaultHandoffHold, onChange: onChange, waits: map[string]*handoff{}}
}

// Ask blocks until the user hands the bot's computer back and returns their note. takenOver says the user already
// holds the computer, so the hold timeout applies at once. It is Begin followed by Wait.
func (h *Handoffs) Ask(ctx context.Context, botID, reason string, takenOver bool) (string, error) {
	w, err := h.Begin(ctx, botID, reason, takenOver)
	if err != nil {
		return "", err
	}
	return w.Wait(ctx)
}

// HandoffWait is a registered takeover request; Wait must be called once Begin returned it.
type HandoffWait struct {
	h         *Handoffs
	botID     string
	w         *handoff
	takenOver bool
}

// Begin registers the bot's wait without blocking, so a caller can decide takenOver under the same lock as its
// Take over and Done handlers: a Started or Finish that arrives after Begin is never lost. It does not call the
// change callback; Wait does, outside the caller's locks.
func (h *Handoffs) Begin(ctx context.Context, botID, reason string, takenOver bool) (*HandoffWait, error) {
	if err := ctx.Err(); err != nil {
		return nil, err // the turn already ended: do not register a wait or notify anyone
	}
	w := &handoff{reason: reason, since: time.Now(), started: make(chan struct{}), done: make(chan handoffEnd, 1)}
	if takenOver {
		w.took = true
		close(w.started)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, busy := h.waits[botID]; busy {
		return nil, ErrHandoffBusy
	}
	h.waits[botID] = w
	return &HandoffWait{h: h, botID: botID, w: w, takenOver: takenOver}, nil
}

// Wait blocks until the user hands the computer back (their note), the wait is canceled (Cancel's error), a timeout
// passes (ErrHandoffTimeout, ErrHandoffHoldTimeout) or ctx ends; then it unregisters the wait.
func (hw *HandoffWait) Wait(ctx context.Context) (string, error) {
	h, botID, w, takenOver := hw.h, hw.botID, hw.w, hw.takenOver
	h.changed(botID)
	defer func() {
		h.mu.Lock()
		if h.waits[botID] == w {
			delete(h.waits, botID)
		}
		h.mu.Unlock()
		h.changed(botID)
	}()

	limit, started := h.AskTimeout, (<-chan struct{})(w.started)
	if takenOver {
		limit, started = h.HoldTimeout, nil
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case end := <-w.done:
			return end.note, end.err
		case <-started:
			started, takenOver = nil, true
			timer.Reset(h.HoldTimeout) // Go 1.23+ timers: Reset never delivers a stale tick
		case <-timer.C:
			// A Done or Take over that raced the timer must not be lost: select picks among ready cases at random.
			select {
			case end := <-w.done:
				return end.note, end.err
			default:
			}
			if takenOver {
				return "", ErrHandoffHoldTimeout
			}
			select {
			case <-started:
				started, takenOver = nil, true
				timer.Reset(h.HoldTimeout)
				continue
			default:
			}
			return "", ErrHandoffTimeout
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// Started records that the user pressed Take over. It does nothing when the bot is not waiting.
func (h *Handoffs) Started(botID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if w := h.waits[botID]; w != nil && !w.took {
		w.took = true
		close(w.started)
	}
}

// Finish hands the computer back with the user's note. It does nothing when the bot is not waiting.
func (h *Handoffs) Finish(botID, note string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if w := h.waits[botID]; w != nil {
		select {
		case w.done <- handoffEnd{note: note}:
		default:
		}
	}
}

// Cancel ends the bot's wait with err (for example ErrHandoffStopped when its computer stopped). It does nothing
// when the bot is not waiting or the wait already ended.
func (h *Handoffs) Cancel(botID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if w := h.waits[botID]; w != nil {
		select {
		case w.done <- handoffEnd{err: err}:
		default:
		}
	}
}

// State reports whether the bot waits for the user and why.
func (h *Handoffs) State(botID string) HandoffState {
	h.mu.Lock()
	defer h.mu.Unlock()
	if w := h.waits[botID]; w != nil {
		return HandoffState{BotID: botID, Pending: true, Reason: w.reason, Since: w.since.UnixMilli()}
	}
	return HandoffState{BotID: botID}
}

func (h *Handoffs) changed(botID string) {
	if h.onChange != nil {
		h.onChange(h.State(botID))
	}
}
