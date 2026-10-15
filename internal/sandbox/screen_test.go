package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type chunkLog struct {
	mu     sync.Mutex
	chunks []ScreenChunk
}

func (l *chunkLog) publish(c ScreenChunk) { l.mu.Lock(); l.chunks = append(l.chunks, c); l.mu.Unlock() }

func (l *chunkLog) wait(t *testing.T, ok func([]ScreenChunk) bool) []ScreenChunk {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		l.mu.Lock()
		cs := append([]ScreenChunk(nil), l.chunks...)
		l.mu.Unlock()
		if ok(cs) {
			return cs
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met; chunks: %+v", cs)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func closedFor(id string) func([]ScreenChunk) bool {
	return func(cs []ScreenChunk) bool {
		for _, c := range cs {
			if c.ID == id && c.Closed {
				return true
			}
		}
		return false
	}
}

func dataFor(cs []ScreenChunk, id string) []byte {
	var b []byte
	for _, c := range cs {
		if c.ID == id {
			b = append(b, c.Data...)
		}
	}
	return b
}

func TestScreenHubRelaysGuestOutputInBoundedChunks(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	big := bytes.Repeat([]byte{7}, 3*ScreenChunkMax+5)
	go func() { _, _ = guestEnd.Write(big) }()
	cs := log.wait(t, func(cs []ScreenChunk) bool { return len(dataFor(cs, id)) == len(big) })
	for _, c := range cs {
		if len(c.Data) > ScreenChunkMax {
			t.Fatalf("chunk of %d bytes exceeds %d", len(c.Data), ScreenChunkMax)
		}
	}
	if !bytes.Equal(dataFor(cs, id), big) {
		t.Fatal("relayed bytes differ")
	}
}

// floodGuest is a guest screen that sends bytes as fast as they are read.
type floodGuest struct{ closed atomic.Bool }

func (g *floodGuest) Read(p []byte) (int, error) {
	if g.closed.Load() {
		return 0, io.EOF
	}
	return len(p), nil
}
func (g *floodGuest) Write(p []byte) (int, error) { return len(p), nil }
func (g *floodGuest) Close() error                { g.closed.Store(true); return nil }

func TestScreenHubClosesAScreenThatExceedsItsByteBudget(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	h.byteRate, h.byteBurst = 1024, 4*ScreenChunkMax
	t.Cleanup(h.CloseAll)
	g := &floodGuest{}
	id := h.Open("b1", g)
	cs := log.wait(t, closedFor(id))
	last := cs[len(cs)-1]
	if !last.Closed || last.Error != screenFloodReason {
		t.Fatalf("closed chunk = %+v", last)
	}
	if got := len(dataFor(cs, id)); got > 4*ScreenChunkMax {
		t.Fatalf("relayed %d bytes past a %d-byte burst", got, 4*ScreenChunkMax)
	}
	if !g.closed.Load() {
		t.Fatal("guest stream left open")
	}
}

func TestByteBudgetRefillsAtItsRate(t *testing.T) {
	t0 := time.Unix(0, 0)
	b := byteBudget{rate: 100, burst: 200, tokens: 200, last: t0}
	if !b.take(200, t0) || b.take(1, t0) {
		t.Fatal("burst not enforced")
	}
	b = byteBudget{rate: 100, burst: 200, tokens: 0, last: t0}
	if !b.take(100, t0.Add(time.Second)) {
		t.Fatal("did not refill at the rate")
	}
	if b.take(250, t0.Add(10*time.Second)) {
		t.Fatal("refill exceeded the burst")
	}
}

func TestScreenHubForwardsInputOnlyAsTheFilterAllows(t *testing.T) {
	var taken atomic.Bool
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(botID string) bool { return botID == "b1" && taken.Load() })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	got := make(chan []byte, 8)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := guestEnd.Read(buf)
			if err != nil {
				close(got)
				return
			}
			got <- append([]byte(nil), buf[:n]...)
		}
	}()
	if err := h.Input(id, append([]byte("RFB 003.008\n"), 1, 1)); err != nil {
		t.Fatal(err)
	}
	if b := <-got; !bytes.Equal(b, append([]byte("RFB 003.008\n"), 1, 1)) {
		t.Fatalf("handshake = %v", b)
	}
	keyEvent := []byte{4, 1, 0, 0, 0, 0, 0xff, 0x0d}
	if err := h.Input(id, keyEvent); err != nil { // view-only: dropped, no error
		t.Fatal(err)
	}
	taken.Store(true)
	if err := h.Input(id, keyEvent); err != nil {
		t.Fatal(err)
	}
	if b := <-got; !bytes.Equal(b, keyEvent) { // the first key never arrived; this is the second
		t.Fatalf("after take over the guest got %v", b)
	}
}

func TestScreenHubCloseEndsTheStreamOnce(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	h.Close(id)
	h.Close(id) // idempotent
	cs := log.wait(t, closedFor(id))
	n := 0
	for _, c := range cs {
		if c.Closed {
			n++
			if c.Error != "" {
				t.Fatalf("user close reported an error: %q", c.Error)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d closed chunks, want 1", n)
	}
	if _, err := guestEnd.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("guest end still open: %v", err)
	}
	if err := h.Input(id, []byte{3}); !errors.Is(err, ErrNoScreen) {
		t.Fatalf("input after close: %v", err)
	}
}

func TestScreenHubReportsWhenTheGuestEndsTheStream(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	_ = guestEnd.Close()
	log.wait(t, closedFor(id))
	if err := h.Input(id, []byte{3}); !errors.Is(err, ErrNoScreen) {
		t.Fatalf("screen still registered: %v", err)
	}
}

func TestScreenHubKeepsTwoScreensPerBotAndClosesPerBot(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	t.Cleanup(h.CloseAll)
	open := func(bot string) string { a, _ := net.Pipe(); return h.Open(bot, a) }
	first, second, other := open("b1"), open("b1"), open("b2")
	third := open("b1")
	cs := log.wait(t, closedFor(first))
	for _, c := range cs {
		if c.ID == first && c.Closed && c.Error == "" {
			t.Fatal("evicted screen should say why")
		}
	}
	h.CloseBot("b1")
	log.wait(t, closedFor(second))
	log.wait(t, closedFor(third))
	if err := h.Input(other, nil); errors.Is(err, ErrNoScreen) { // nil input: nothing is written to the unread pipe
		t.Fatal("CloseBot closed another bot's screen")
	}
}

func TestScreenHubClosesTheStreamOnBadInput(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return true })
	hostEnd, _ := net.Pipe()
	id := h.Open("b1", hostEnd)
	if err := h.Input(id, []byte("HTTP/1.1 GET\n")); !errors.Is(err, errRFBInput) {
		t.Fatalf("err = %v", err)
	}
	log.wait(t, closedFor(id))
}

// guestReader collects what the guest end of a pipe receives; a Closed chunk marks the end.
func guestReader(guestEnd net.Conn) *chunkLog {
	log := &chunkLog{}
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := guestEnd.Read(buf)
			if n > 0 {
				log.publish(ScreenChunk{Data: append([]byte(nil), buf[:n]...)})
			}
			if err != nil {
				log.publish(ScreenChunk{Closed: true})
				return
			}
		}
	}()
	return log
}

func hasBytes(want []byte) func([]ScreenChunk) bool {
	return func(cs []ScreenChunk) bool { return bytes.Contains(dataFor(cs, ""), want) }
}

func TestScreenHubReleasesHeldInputWhenTheTakeoverEnds(t *testing.T) {
	var taken atomic.Bool
	taken.Store(true)
	h := NewScreenHub(func(ScreenChunk) {}, func(string) bool { return taken.Load() })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	got := guestReader(guestEnd)
	pointerDown := []byte{5, 1, 0, 3, 0, 4}
	if err := h.Input(id, rfbConcat(rfbHandshake(), rfbKey(1), pointerDown)); err != nil {
		t.Fatal(err)
	}
	got.wait(t, hasBytes(pointerDown))

	h.TakeoverEnded("b1") // still taken over: nothing is released
	h.bg.Wait()
	h.mu.Lock()
	s := h.screens[id]
	h.mu.Unlock()
	s.inMu.Lock()
	held := len(s.filter.heldKeys) == 1 && s.filter.buttons == 1
	s.inMu.Unlock()
	if !held {
		t.Fatal("TakeoverEnded released input while the user still holds the lock")
	}
	taken.Store(false)
	h.TakeoverEnded("b1")
	want := rfbConcat(rfbKey(0), []byte{5, 0, 0, 3, 0, 4})
	got.wait(t, hasBytes(want))
	// Nothing is held any more: closing the screen writes no second release.
	h.Close(id)
	cs := got.wait(t, closedFor(""))
	if n := bytes.Count(dataFor(cs, ""), rfbKey(0)); n != 1 {
		t.Fatalf("key up written %d times", n)
	}
}

func TestScreenHubReleasesHeldInputWhenTheScreenCloses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close func(h *ScreenHub, id string)
	}{
		{"user close", func(h *ScreenHub, id string) { h.Close(id) }},
		{"computer stopped", func(h *ScreenHub, _ string) { h.CloseBot("b1") }},
		{"bad input", func(h *ScreenHub, id string) { _ = h.Input(id, []byte{7, 0, 0, 0}) }},
		{"evicted", func(h *ScreenHub, _ string) {
			for range 2 {
				a, b := net.Pipe()
				go func() { _, _ = io.Copy(io.Discard, b) }()
				h.Open("b1", a)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewScreenHub(func(ScreenChunk) {}, func(string) bool { return true })
			t.Cleanup(h.CloseAll)
			hostEnd, guestEnd := net.Pipe()
			id := h.Open("b1", hostEnd)
			got := guestReader(guestEnd)
			if err := h.Input(id, rfbConcat(rfbHandshake(), rfbKey(1))); err != nil {
				t.Fatal(err)
			}
			got.wait(t, hasBytes(rfbKey(1)))
			tc.close(h, id)
			cs := got.wait(t, closedFor(""))
			if !bytes.HasSuffix(dataFor(cs, ""), rfbKey(0)) {
				t.Fatalf("guest got %v, want a final key up", dataFor(cs, ""))
			}
		})
	}
}

func TestScreenHubReleaseIsBestEffortWhenTheGuestEnded(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return true })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	id := h.Open("b1", hostEnd)
	got := guestReader(guestEnd)
	if err := h.Input(id, rfbConcat(rfbHandshake(), rfbKey(1))); err != nil {
		t.Fatal(err)
	}
	got.wait(t, hasBytes(rfbKey(1)))
	_ = guestEnd.Close() // the stream fails while input is allowed and a key is held
	log.wait(t, closedFor(id))
	if err := h.Input(id, nil); !errors.Is(err, ErrNoScreen) {
		t.Fatalf("screen still registered: %v", err)
	}
}

// stalledScreen opens a screen whose guest takes the handshake and a key down, then stops reading.
func stalledScreen(t *testing.T, log *chunkLog) (*ScreenHub, string) {
	t.Helper()
	h := NewScreenHub(log.publish, func(string) bool { return true })
	h.releaseTimeout = 20 * time.Millisecond
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	t.Cleanup(func() { _ = guestEnd.Close() })
	id := h.Open("b1", hostEnd)
	in := rfbConcat(rfbHandshake(), rfbKey(1))
	read := make(chan struct{})
	go func() { _, _ = io.ReadFull(guestEnd, make([]byte, len(in))); close(read) }()
	if err := h.Input(id, in); err != nil {
		t.Fatal(err)
	}
	<-read
	return h, id
}

func TestScreenHubInputToAStalledGuestIsBounded(t *testing.T) {
	log := &chunkLog{}
	h, id := stalledScreen(t, log)
	if err := h.Input(id, []byte{4, 1, 0, 0, 0, 0, 0, 0x41}); !errors.Is(err, ErrScreenStalled) {
		t.Fatalf("err = %v", err)
	}
	cs := log.wait(t, closedFor(id))
	if c := cs[len(cs)-1]; c.Error != "the screen stopped responding" {
		t.Fatalf("closed with %q", c.Error)
	}
	if err := h.Input(id, nil); !errors.Is(err, ErrNoScreen) {
		t.Fatalf("stalled screen still open: %v", err)
	}
}

func TestScreenHubCloseIsBoundedWhenTheGuestStopsReading(t *testing.T) {
	for _, withInput := range []bool{false, true} {
		h, id := stalledScreen(t, &chunkLog{})
		inputDone := make(chan struct{})
		if withInput { // races Close for inMu; either order must end in bounded time
			go func() { _ = h.Input(id, []byte{4, 1, 0, 0, 0, 0, 0, 0x41}); close(inputDone) }()
		} else {
			close(inputDone)
		}
		closed := make(chan struct{})
		go func() { h.Close(id); close(closed) }() // the key up release cannot be written either
		for _, ch := range []chan struct{}{closed, inputDone} {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
				t.Fatalf("withInput=%v: Close or Input blocked on a guest that stopped reading", withInput)
			}
		}
	}
}

func TestScreenHubClosesStalledScreensInParallel(t *testing.T) {
	h := NewScreenHub(func(ScreenChunk) {}, func(string) bool { return true })
	h.releaseTimeout = 200 * time.Millisecond
	var guests []net.Conn
	for _, bot := range []string{"b1", "b2", "b3", "b4"} {
		hostEnd, guestEnd := net.Pipe()
		guests = append(guests, guestEnd)
		id := h.Open(bot, hostEnd)
		in := rfbConcat(rfbHandshake(), rfbKey(1))
		go func() { _, _ = io.ReadFull(guestEnd, make([]byte, len(in))) }()
		if err := h.Input(id, in); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, g := range guests {
			_ = g.Close()
		}
	})
	start := time.Now()
	h.CloseAll() // every release write stalls for releaseTimeout
	if d := time.Since(start); d >= 4*h.releaseTimeout {
		t.Fatalf("CloseAll took %v: screens closed one after another", d)
	}
}

func TestScreenHubRefusesOversizedInput(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return true })
	hostEnd, guestEnd := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, guestEnd) }()
	id := h.Open("b1", hostEnd)
	if err := h.Input(id, rfbHandshake()); err != nil {
		t.Fatal(err)
	}
	if err := h.Input(id, rfbCutText(ScreenChunkMax-8, ScreenChunkMax-8)); err != nil { // exactly the cap
		t.Fatalf("input at the cap: %v", err)
	}
	if err := h.Input(id, make([]byte, ScreenChunkMax+1)); !errors.Is(err, ErrScreenInputTooLarge) {
		t.Fatalf("err = %v", err)
	}
	log.wait(t, closedFor(id))
}

func TestScreenHubConnectBoundsOnlyTheOpen(t *testing.T) {
	log := &chunkLog{}
	h := NewScreenHub(log.publish, func(string) bool { return false })
	t.Cleanup(h.CloseAll)
	hostEnd, guestEnd := net.Pipe()
	var openCtx context.Context
	id, err := h.Connect(context.Background(), "b1", func(ctx context.Context) (io.ReadWriteCloser, error) {
		openCtx = ctx
		return hostEnd, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline, ok := openCtx.Deadline()
	if !ok || time.Until(deadline) > ScreenOpenTimeout {
		t.Fatalf("open ctx deadline = %v, %v", deadline, ok)
	}
	if openCtx.Err() == nil {
		t.Fatal("open ctx still live after Connect returned")
	}
	go func() { _, _ = guestEnd.Write([]byte("RFB 003.008\n")) }() // the stream outlives the open ctx
	log.wait(t, func(cs []ScreenChunk) bool { return string(dataFor(cs, id)) == "RFB 003.008\n" })

	boom := errors.New("boom")
	if _, err := h.Connect(context.Background(), "b1", func(context.Context) (io.ReadWriteCloser, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
