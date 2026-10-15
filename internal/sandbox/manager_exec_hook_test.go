package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// cannedExecGuest is a Guest whose /exec answers at once with the command echoed, so a test can run many execs.
func cannedExecGuest(t *testing.T) *Guest {
	t.Helper()
	client, server := net.Pipe()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/exec":
			var req wire.ExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(wire.ExecResult{Stdout: strings.TrimPrefix(req.Command, "echo ") + "\n"})
		default:
			http.NotFound(w, r)
		}
	})
	go (&http2.Server{}).ServeConn(server, &http2.ServeConnOpts{Handler: h})
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	g, err := NewGuest(client, "tok")
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A consumer that blocks must never hold up a bot's commands: every exec returns, the queue drops what does not fit,
// and what is delivered arrives in the order the commands ran. The delivery goroutine ends with the VM.
func TestExecHookNeverBlocksCommandsAndKeepsOrder(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	var seen []int
	m := &Manager{cfg: Config{OnExec: func(botID string, req wire.ExecRequest, res wire.ExecResult) {
		<-release
		n, err := strconv.Atoi(strings.TrimPrefix(req.Command, "echo "))
		if botID != "b1" || err != nil || strings.TrimSpace(res.Stdout) != strconv.Itoa(n) {
			t.Errorf("hook got %q %+v %+v", botID, req, res)
		}
		mu.Lock()
		seen = append(seen, n)
		mu.Unlock()
	}}}
	vm := &fakeVM{done: make(chan struct{})}
	defer close(vm.done)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	g := cannedExecGuest(t)
	m.hookExec("b1", vm, g)

	const execs = 200
	done := make(chan error, 1)
	go func() {
		for i := range execs {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := g.Exec(ctx, wire.ExecRequest{Command: fmt.Sprintf("echo %d", i)})
			cancel()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("execs blocked behind a stuck hook consumer")
	}

	// The consumer holds one event and the queue 64 more; the rest were dropped while it was stuck.
	close(release)
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 64 })
	time.Sleep(50 * time.Millisecond) // a stray extra delivery would show now
	mu.Lock()
	defer mu.Unlock()
	if len(seen) > 66 || seen[0] != 0 {
		t.Fatalf("%d of %d events delivered, first %d: the queue is not bounded", len(seen), execs, seen[0])
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("events out of order: %v", seen)
		}
	}
}

func TestRunningGuestNeverBoots(t *testing.T) {
	h := newHarness(t, nil)
	if g, ok := h.m.RunningGuest("b1"); ok || g != nil {
		t.Fatal("a guest for a computer that is off")
	}
	if h.b.bootCount() != 0 {
		t.Fatal("RunningGuest booted a VM")
	}
	want := ensure(t, h, "b1")
	if g, ok := h.m.RunningGuest("b1"); !ok || g != want {
		t.Fatal("no guest for a running computer")
	}
	h.b.vmFor("b1").crash()
	eventually(t, func() bool { _, ok := h.m.RunningGuest("b1"); return !ok })
	if h.b.bootCount() != 1 {
		t.Fatal("RunningGuest booted a VM")
	}
}
