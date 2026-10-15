package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// ScreenChunk is live screen output for the UI, published as the sandbox.screen.data event. The last chunk of a
// stream has Closed set; Error says why when it did not end because the user closed it.
type ScreenChunk struct {
	ID     string `json:"id"`
	Data   []byte `json:"data,omitempty"` // base64 in JSON
	Closed bool   `json:"closed,omitempty"`
	Error  string `json:"error,omitempty"`
}

var (
	// ErrNoScreen is returned for input to a screen that is not open (closed, evicted or never opened).
	ErrNoScreen = errors.New("sandbox: no such screen")
	// ErrScreenInputTooLarge is returned for one Input larger than ScreenChunkMax; the screen is closed, since the
	// dropped bytes would desynchronise the RFB stream. The UI splits larger writes (a big clipboard) itself.
	ErrScreenInputTooLarge = errors.New("sandbox: screen input too large")
	// ErrScreenStalled is returned for input the guest did not take within the write timeout; the screen is closed.
	ErrScreenStalled = errors.New("sandbox: the screen stopped responding")
)

const (
	// ScreenChunkMax bounds one sandbox.screen.data event and one Input. The RFB filter keeps the stream
	// client-pull (it drops EnableContinuousUpdates and strips the ContinuousUpdates pseudo-encoding), so the guest
	// sends an update only after the viewer asks for one and a stream cannot flood the IPC queue.
	ScreenChunkMax = 128 << 10
	// ScreenOpenTimeout bounds opening a guest screen stream in Connect; the open stream lives until closed.
	ScreenOpenTimeout = 30 * time.Second
	screensPerBot     = 2
	// screenReleaseTimeout bounds every write to the guest (input, key and button releases) and how long ending a
	// screen waits for one in progress; a guest that does not take the bytes in time has its screen closed.
	screenReleaseTimeout = 2 * time.Second
	// ScreenByteRate and ScreenByteBurst are each screen's guest byte budget (a token bucket): a guest that sends
	// more than the burst above the sustained rate has its screen closed with screenFloodReason. The RFB filter keeps
	// updates client-pull, so an honest guest stays far below this; it bounds a compromised one.
	ScreenByteRate    = 32 << 20 // bytes per second
	ScreenByteBurst   = 64 << 20
	screenFloodReason = "the screen sent too much data"
)

// ScreenHub relays the guest's VNC stream for open Computer tabs. Guest bytes go out as events; user bytes come in
// through Input and pass the RFB filter, which lets keyboard and mouse through only while the user holds the
// takeover lock.
type ScreenHub struct {
	publish        func(ScreenChunk)
	takenOver      func(botID string) bool
	releaseTimeout time.Duration // screenReleaseTimeout; shorter in tests
	byteRate       float64       // ScreenByteRate; smaller in tests
	byteBurst      float64       // ScreenByteBurst; smaller in tests

	mu      sync.Mutex
	screens map[string]*screenStream
	order   []string // open ids, oldest first

	bg sync.WaitGroup // TakeoverEnded releases in flight (tests wait on it)
}

type screenStream struct {
	id, botID string
	rwc       io.ReadWriteCloser

	// inMu serialises everything that touches filter or writes to rwc: Input, TakeoverEnded and the final release.
	// The filter has no lock of its own, and release bytes must fall between complete forwarded messages.
	inMu   sync.Mutex
	filter *rfbFilter
	ended  bool // under inMu: the final release went out and rwc is closed

	finishOnce sync.Once
	stalled    atomic.Bool // a write timed out and closed rwc

	pubMu  sync.Mutex // no data chunk is published after the closed chunk
	closed bool
}

// NewScreenHub creates a hub. publish must not block for long; takenOver is Manager.TakenOver.
func NewScreenHub(publish func(ScreenChunk), takenOver func(botID string) bool) *ScreenHub {
	return &ScreenHub{
		publish: publish, takenOver: takenOver, releaseTimeout: screenReleaseTimeout,
		byteRate: ScreenByteRate, byteBurst: ScreenByteBurst, screens: map[string]*screenStream{},
	}
}

// Connect opens a guest screen stream with open (Guest.OpenScreen) and starts relaying it. ctx bounded by
// ScreenOpenTimeout limits only the opening: the stream itself lives until it is closed.
func (h *ScreenHub) Connect(ctx context.Context, botID string, open func(context.Context) (io.ReadWriteCloser, error)) (string, error) {
	octx, cancel := context.WithTimeout(ctx, ScreenOpenTimeout)
	defer cancel()
	stream, err := open(octx)
	if err != nil {
		return "", err
	}
	return h.Open(botID, stream), nil
}

// Open starts relaying stream (from Guest.OpenScreen) and returns the screen id. A bot keeps at most two open
// screens; the oldest is closed when a third opens.
func (h *ScreenHub) Open(botID string, stream io.ReadWriteCloser) string {
	s := &screenStream{id: uuid.NewString(), botID: botID, rwc: stream}
	s.filter = newRFBFilter(func() bool { return h.takenOver(botID) })
	var evict []*screenStream
	h.mu.Lock()
	h.screens[s.id] = s
	h.order = append(h.order, s.id)
	count := 0
	for i := len(h.order) - 1; i >= 0; i-- { // newest first
		o := h.screens[h.order[i]]
		if o.botID != botID {
			continue
		}
		if count++; count > screensPerBot {
			evict = append(evict, o)
		}
	}
	for _, o := range evict {
		h.removeLocked(o.id)
	}
	h.mu.Unlock()
	h.finishAll(evict, "the screen was opened somewhere else")
	go h.pump(s)
	return s.id
}

// Input passes user bytes (at most ScreenChunkMax) through the RFB filter to the guest.
func (h *ScreenHub) Input(id string, data []byte) error {
	h.mu.Lock()
	s := h.screens[id]
	h.mu.Unlock()
	if s == nil {
		return ErrNoScreen
	}
	if len(data) > ScreenChunkMax {
		h.drop(s, "the screen received unexpected input")
		return ErrScreenInputTooLarge
	}
	s.inMu.Lock()
	if s.ended {
		s.inMu.Unlock()
		return ErrNoScreen
	}
	forward, err := s.filter.Feed(data)
	if err != nil {
		s.inMu.Unlock()
		h.drop(s, "the screen received unexpected input")
		return err
	}
	if len(forward) > 0 {
		err = h.write(s, forward)
	}
	s.inMu.Unlock() // before drop: finish takes inMu for the final release
	if err != nil {
		h.drop(s, writeFailure(err))
		return err
	}
	return nil
}

// write sends p to the guest, closing the stream when the guest does not take it within releaseTimeout so a stalled
// guest cannot pile up blocked Input calls. s.inMu must be held.
func (h *ScreenHub) write(s *screenStream, p []byte) error {
	t := time.AfterFunc(h.releaseTimeout, func() { s.stalled.Store(true); _ = s.rwc.Close() })
	_, err := s.rwc.Write(p)
	if !t.Stop() && s.stalled.Load() {
		return ErrScreenStalled
	}
	return err
}

func writeFailure(err error) string {
	if errors.Is(err, ErrScreenStalled) {
		return "the screen stopped responding"
	}
	return "the screen stream ended unexpectedly"
}

// TakeoverEnded releases, in the guest, every key and pointer button the user still holds down on botID's screens,
// so control goes back to the bot with nothing stuck. Call it when Manager reports the takeover lock released; it
// does nothing while the user still holds the lock. It does not block on the guest.
func (h *ScreenHub) TakeoverEnded(botID string) {
	h.mu.Lock()
	var mine []*screenStream
	for _, id := range h.order {
		if s := h.screens[id]; s.botID == botID {
			mine = append(mine, s)
		}
	}
	h.mu.Unlock()
	for _, s := range mine {
		h.bg.Go(func() {
			s.inMu.Lock()
			var err error
			if !s.ended && !h.takenOver(botID) {
				if rel := s.filter.Release(); len(rel) > 0 {
					err = h.write(s, rel)
				}
			}
			s.inMu.Unlock()
			if err != nil {
				h.drop(s, writeFailure(err))
			}
		})
	}
}

// Close ends a screen the user closed. Unknown ids are ignored.
func (h *ScreenHub) Close(id string) {
	h.mu.Lock()
	s := h.screens[id]
	if s != nil {
		h.removeLocked(id)
	}
	h.mu.Unlock()
	if s != nil {
		h.finish(s, "")
	}
}

// CloseBot ends every screen of a bot whose computer stopped.
func (h *ScreenHub) CloseBot(botID string) {
	h.closeWhere(func(s *screenStream) bool { return s.botID == botID }, "the computer stopped")
}

// CloseAll ends every screen (daemon shutdown).
func (h *ScreenHub) CloseAll() {
	h.closeWhere(func(*screenStream) bool { return true }, "the computer stopped")
}

func (h *ScreenHub) closeWhere(match func(*screenStream) bool, reason string) {
	var gone []*screenStream
	h.mu.Lock()
	for _, id := range append([]string(nil), h.order...) {
		if s := h.screens[id]; match(s) {
			gone = append(gone, s)
			h.removeLocked(id)
		}
	}
	h.mu.Unlock()
	h.finishAll(gone, reason)
}

// finishAll ends screens in parallel (each waits at most about releaseTimeout on its guest) and waits for all.
func (h *ScreenHub) finishAll(gone []*screenStream, reason string) {
	var wg sync.WaitGroup
	for _, s := range gone {
		wg.Go(func() { h.finish(s, reason) })
	}
	wg.Wait()
}

func (h *ScreenHub) pump(s *screenStream) {
	buf := make([]byte, ScreenChunkMax)
	budget := byteBudget{rate: h.byteRate, burst: h.byteBurst, tokens: h.byteBurst, last: time.Now()}
	for {
		n, err := s.rwc.Read(buf)
		if n > 0 && !budget.take(n, time.Now()) {
			h.drop(s, screenFloodReason)
			return
		}
		if n > 0 {
			s.emit(h, ScreenChunk{ID: s.id, Data: bytes.Clone(buf[:n])}, false)
		}
		if err != nil {
			reason := ""
			switch {
			case s.stalled.Load(): // the read failed because a stalled write closed the stream
				reason = writeFailure(ErrScreenStalled)
			case !errors.Is(err, io.EOF):
				reason = "the screen stream ended unexpectedly"
			}
			h.drop(s, reason)
			return
		}
	}
}

// byteBudget is a token bucket over guest bytes: it refills at rate per second up to burst.
type byteBudget struct {
	rate, burst, tokens float64
	last                time.Time
}

// take spends n bytes at now and reports whether the budget still covers them.
func (b *byteBudget) take(n int, now time.Time) bool {
	b.tokens = min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	b.tokens -= float64(n)
	return b.tokens >= 0
}

// drop removes s if it is still registered and ends it.
func (h *ScreenHub) drop(s *screenStream, reason string) {
	h.mu.Lock()
	if h.screens[s.id] == s {
		h.removeLocked(s.id)
	}
	h.mu.Unlock()
	h.finish(s, reason)
}

// finish publishes the closed chunk (once), releases in the guest whatever the user still holds down, and closes
// the guest stream. It never waits on the guest for longer than releaseTimeout: the stream is closed then,
// which also fails an Input write stuck on it.
func (h *ScreenHub) finish(s *screenStream, reason string) {
	s.emit(h, ScreenChunk{ID: s.id, Closed: true, Error: reason}, true)
	s.finishOnce.Do(func() {
		force := time.AfterFunc(h.releaseTimeout, func() { _ = s.rwc.Close() })
		defer force.Stop()
		s.inMu.Lock()
		defer s.inMu.Unlock()
		if rel := s.filter.Release(); len(rel) > 0 { // best effort: a failed stream just refuses it
			_, _ = s.rwc.Write(rel)
		}
		_ = s.rwc.Close() // under inMu, so no Input can slip a key down in after the release
		s.ended = true
	})
}

func (s *screenStream) emit(h *ScreenHub, c ScreenChunk, final bool) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	if s.closed {
		return
	}
	if final {
		s.closed = true
	}
	h.publish(c)
}

// removeLocked forgets id. h.mu must be held.
func (h *ScreenHub) removeLocked(id string) {
	delete(h.screens, id)
	for i, o := range h.order {
		if o == id {
			h.order = append(h.order[:i], h.order[i+1:]...)
			break
		}
	}
}
