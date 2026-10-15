package sandbox

import (
	"sync"
	"testing"
	"time"
)

type viewerLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *viewerLog) set(botID string, watching bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if watching {
		l.calls = append(l.calls, botID+":on")
	} else {
		l.calls = append(l.calls, botID+":off")
	}
}

func (l *viewerLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.calls) == 0 {
		return ""
	}
	return l.calls[len(l.calls)-1]
}

func TestViewerLeaseExpiresWithoutRenewal(t *testing.T) {
	log := &viewerLog{}
	v := NewViewerLeases(log.set, 40*time.Millisecond)
	t.Cleanup(v.Close)
	v.Watch("b1")
	if log.last() != "b1:on" {
		t.Fatalf("watch: %q", log.last())
	}
	for range 4 { // renewals keep it alive past several TTLs
		time.Sleep(20 * time.Millisecond)
		v.Watch("b1")
	}
	if log.last() != "b1:on" {
		t.Fatalf("renewed lease expired: %q", log.last())
	}
	deadline := time.Now().Add(2 * time.Second)
	for log.last() != "b1:off" {
		if time.Now().After(deadline) {
			t.Fatal("lease never expired")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestViewerUnwatchClearsAtOnceAndCloseStopsTimers(t *testing.T) {
	log := &viewerLog{}
	v := NewViewerLeases(log.set, time.Hour)
	v.Watch("b1")
	v.Unwatch("b1")
	if log.last() != "b1:off" {
		t.Fatalf("unwatch: %q", log.last())
	}
	v.Watch("b2")
	v.Close()
	before := log.last()
	v.Watch("b3") // after Close: ignored
	if log.last() != before {
		t.Fatal("watch after Close reached the manager")
	}
}

func TestViewerLeaseExpiryCallsOnExpire(t *testing.T) {
	log := &viewerLog{}
	expired := make(chan string, 4)
	v := NewViewerLeases(log.set, 20*time.Millisecond)
	t.Cleanup(v.Close)
	v.OnExpire(func(botID string) { expired <- botID })
	v.Watch("b1")
	select {
	case id := <-expired:
		if id != "b1" {
			t.Fatalf("expired %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no expiry")
	}
	if log.last() != "b1:off" {
		t.Fatalf("flag not cleared before the expiry callback: %q", log.last())
	}
}

// An Unwatch clears the flag at once, and unless a Watch supersedes it, the abandonment callback still runs one ttl
// later without touching the flag again.
func TestViewerUnwatchStillExpiresAbandonment(t *testing.T) {
	tests := []struct {
		name         string
		taken, hand  bool
		rewatch      bool
		wantReleased bool
	}{
		{"takeover, unwatch, ttl: released", true, false, false, true},
		{"takeover, unwatch, watch before ttl: kept", true, false, true, false},
		{"pending handoff, unwatch, ttl: kept", true, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &viewerLog{}
			timers := &manualTimers{}
			v := NewViewerLeases(log.set, time.Minute)
			v.afterFunc = timers.afterFunc
			var released []string
			v.OnExpire(ReleaseAbandonedTakeover(
				func(string) bool { return tt.taken },
				func(string) bool { return tt.hand },
				func(botID string) { released = append(released, botID) },
			))
			v.Watch("b1")   // timer 0
			v.Unwatch("b1") // timer 1
			if log.last() != "b1:off" {
				t.Fatalf("unwatch did not clear at once: %q", log.last())
			}
			if tt.rewatch {
				v.Watch("b1") // timer 2
			}
			timers.fire(0) // the superseded lease never acts
			timers.fire(1)
			if got := len(released) == 1 && released[0] == "b1"; got != tt.wantReleased {
				t.Fatalf("released = %v", released)
			}
			want := "b1:off"
			if tt.rewatch {
				want = "b1:on"
			}
			log.mu.Lock()
			n := len(log.calls)
			log.mu.Unlock()
			if wantN := map[bool]int{false: 2, true: 3}[tt.rewatch]; log.last() != want || n != wantN {
				t.Fatalf("flag calls: last %q, %d calls", log.last(), n)
			}
		})
	}
}

func TestReleaseAbandonedTakeover(t *testing.T) {
	tests := []struct {
		name           string
		taken, pending bool
		wantRelease    bool
	}{
		{"taken over, no handoff: released", true, false, true},
		{"taken over for a pending handoff: kept", true, true, false},
		{"not taken over", false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var released []string
			fn := ReleaseAbandonedTakeover(
				func(string) bool { return tt.taken },
				func(string) bool { return tt.pending },
				func(botID string) { released = append(released, botID) },
			)
			fn("b1")
			if got := len(released) == 1 && released[0] == "b1"; got != tt.wantRelease {
				t.Fatalf("released = %v", released)
			}
		})
	}
}

func TestViewerLeaseExpiryReleasesAnAbandonedTakeover(t *testing.T) {
	released := make(chan string, 1)
	v := NewViewerLeases(func(string, bool) {}, 20*time.Millisecond)
	t.Cleanup(v.Close)
	v.OnExpire(ReleaseAbandonedTakeover(
		func(string) bool { return true },
		func(string) bool { return false },
		func(botID string) { released <- botID },
	))
	v.Watch("b1")
	select {
	case id := <-released:
		if id != "b1" {
			t.Fatalf("released %q", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("takeover not released on lease expiry")
	}
}

// manualTimers replaces ViewerLeases.afterFunc: callbacks run only when the test fires them.
type manualTimers struct {
	mu  sync.Mutex
	fns []func()
}

func (m *manualTimers) afterFunc(_ time.Duration, f func()) *time.Timer {
	m.mu.Lock()
	m.fns = append(m.fns, f)
	m.mu.Unlock()
	return time.NewTimer(time.Hour)
}

func (m *manualTimers) fire(i int) {
	m.mu.Lock()
	f := m.fns[i]
	m.mu.Unlock()
	f()
}

func TestViewerStaleExpiryAfterRenewalDoesNothing(t *testing.T) {
	log := &viewerLog{}
	timers := &manualTimers{}
	var expired []string
	v := NewViewerLeases(log.set, time.Minute)
	v.afterFunc = timers.afterFunc
	v.OnExpire(func(botID string) { expired = append(expired, botID) })
	v.Watch("b1") // lease 0
	v.Watch("b1") // renewed: lease 1
	timers.fire(0)
	if log.last() != "b1:on" || len(expired) != 0 {
		t.Fatalf("stale expiry acted: last %q, expired %v", log.last(), expired)
	}
	v.Unwatch("b1") // timer 2
	v.Watch("b1")   // lease 3
	timers.fire(1)
	timers.fire(2)
	if log.last() != "b1:on" || len(expired) != 0 {
		t.Fatalf("expiry of an unwatched lease acted: last %q, expired %v", log.last(), expired)
	}
	timers.fire(3)
	if log.last() != "b1:off" || len(expired) != 1 {
		t.Fatalf("current lease did not expire: last %q, expired %v", log.last(), expired)
	}
}

func TestViewerRenewalRacingExpiryEndsWatched(t *testing.T) {
	for range 200 {
		log := &viewerLog{}
		timers := &manualTimers{}
		v := NewViewerLeases(log.set, time.Minute)
		v.afterFunc = timers.afterFunc
		var mu sync.Mutex
		released := false
		v.OnExpire(func(string) { mu.Lock(); released = true; mu.Unlock() })
		v.Watch("b1")
		var wg sync.WaitGroup
		wg.Go(func() { timers.fire(0) })
		wg.Go(func() { v.Watch("b1") })
		wg.Wait()
		// Either order ends with the renewed lease watching; an expiry, if it won, came before the renewal.
		if log.last() != "b1:on" {
			t.Fatalf("renewed lease ended unwatched (released=%v)", released)
		}
		log.mu.Lock()
		calls := append([]string(nil), log.calls...)
		log.mu.Unlock()
		mu.Lock()
		if released && (len(calls) != 3 || calls[1] != "b1:off") {
			t.Fatalf("expiry interleaved with the renewal: %v", calls)
		}
		mu.Unlock()
	}
}
