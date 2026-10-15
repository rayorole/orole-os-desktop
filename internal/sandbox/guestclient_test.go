package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

const testToken = "tok"

// startAgent serves the real guest handler on one end of a net.Pipe and returns a Guest on the other.
func startAgent(t *testing.T, cfg guestagent.Config) (*Guest, *guestagent.Agent) {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	if cfg.Home == "" {
		cfg.Home = scratchHome(t)
	}
	if cfg.Shell == nil {
		cfg.Shell = agenttest.Shell()
	}
	a := guestagent.New(cfg)
	host, guest := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.ServeConn(ctx, guest); close(done) }()
	g, err := NewGuest(host, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close(); cancel(); <-done })
	return g, a
}

// startHandler serves any handler (a hostile guest, a stream echo) the same way.
func startHandler(t *testing.T, h http.Handler) *Guest {
	t.Helper()
	host, guest := net.Pipe()
	done := make(chan struct{})
	go func() { (&http2.Server{}).ServeConn(guest, &http2.ServeConnOpts{Handler: h}); close(done) }()
	g, err := NewGuest(host, testToken)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close(); guest.Close(); <-done })
	return g
}

func TestGuestHealthAndExec(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{Version: "9"})
	ctx := context.Background()
	h, err := g.Health(ctx)
	if err != nil || !h.Ready || h.Version != "9" {
		t.Fatalf("Health = %+v, %v", h, err)
	}
	res, err := g.Exec(ctx, wire.ExecRequest{Command: "echo hi"})
	if err != nil || res.Stdout != "hi\n" {
		t.Fatalf("Exec = %+v, %v", res, err)
	}
	res, err = g.Exec(ctx, wire.ExecRequest{Command: "flood 3000000"})
	if err != nil || len(res.Stdout) != wire.MaxExecOutput || !res.Truncated {
		t.Fatalf("flood through the channel: len=%d truncated=%v err=%v", len(res.Stdout), res.Truncated, err)
	}
}

func TestGuestWrongTokenIsRejected(t *testing.T) {
	a := guestagent.New(guestagent.Config{Token: "right", Home: scratchHome(t)})
	host, guest := net.Pipe()
	go a.ServeConn(context.Background(), guest)
	g, err := NewGuest(host, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if _, err := g.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Health with the wrong token = %v", err)
	}
}

func TestGuestFilesRoundTrip(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	ctx := context.Background()
	w, err := g.WriteFile(ctx, "dir/a.txt", []byte("hello"))
	if err != nil || w.Bytes != 5 {
		t.Fatalf("WriteFile = %+v, %v", w, err)
	}
	r, err := g.ReadFile(ctx, "dir/a.txt")
	if err != nil || string(r.Content) != "hello" {
		t.Fatalf("ReadFile = %+v, %v", r, err)
	}
	if _, err := g.WriteFile(ctx, "big", make([]byte, wire.MaxWriteFile+1)); err == nil {
		t.Fatal("WriteFile over 16 MiB = nil error")
	}
}

func TestGuestCapsHostileExecOutput(t *testing.T) {
	big := strings.Repeat("x", 3<<20)
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wire.ExecResult{Stdout: big, Stderr: "tail"})
	}))
	res, err := g.Exec(context.Background(), wire.ExecRequest{Command: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != wire.MaxExecOutput || res.Stderr != "" || !res.Truncated {
		t.Fatalf("host cap: stdout=%d stderr=%q truncated=%v", len(res.Stdout), res.Stderr, res.Truncated)
	}
}

func TestGuestCapsHostileReadFile(t *testing.T) {
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wire.ReadFileResult{Content: make([]byte, 5<<20)})
	}))
	res, err := g.ReadFile(context.Background(), "x")
	if err != nil || len(res.Content) != wire.MaxReadFile || !res.Truncated {
		t.Fatalf("ReadFile = %d bytes truncated=%v err=%v", len(res.Content), res.Truncated, err)
	}
}

func TestGuestRejectsOversizedResponse(t *testing.T) {
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"stdout":"`)
		chunk := strings.Repeat("x", 1<<20)
		for range 9 {
			if _, err := io.WriteString(w, chunk); err != nil {
				return
			}
		}
		fmt.Fprint(w, `"}`)
	}))
	done := make(chan error, 1)
	go func() { _, err := g.Exec(context.Background(), wire.ExecRequest{Command: "x"}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("Exec = %v, want a too-large error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("oversized answer blocked the client")
	}
}

func TestGuestUnsupportedEndpoints(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	if _, err := g.Browser(context.Background(), wire.BrowserRequest{Action: "read"}); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("Browser = %v, want ErrUnsupported", err)
	}
}

func TestGuestAbortMapsTransportErrors(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	started := make(chan struct{}, 1)
	g.setOnUse(func(delta int) {
		if delta > 0 {
			select {
			case started <- struct{}{}:
			default:
			}
		}
	})
	errc := make(chan error, 1)
	go func() {
		_, err := g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 10s"})
		errc <- err
	}()
	<-started
	g.abort(ErrCrashed) // what the manager does when the VM process dies
	g.conn.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrCrashed) {
			t.Fatalf("in-flight Exec = %v, want ErrCrashed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Exec never returned")
	}
	if _, err := g.Health(context.Background()); !errors.Is(err, ErrCrashed) {
		t.Fatalf("call after abort = %v, want ErrCrashed", err)
	}
}

func TestGuestReportsUse(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	var mu sync.Mutex
	var deltas []int
	g.setOnUse(func(d int) { mu.Lock(); deltas = append(deltas, d); mu.Unlock() })
	if _, err := g.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != 2 || deltas[0] != 1 || deltas[1] != -1 {
		t.Fatalf("use deltas = %v, want [1 -1]", deltas)
	}
}

func TestServeLoopReconnects(t *testing.T) {
	a := guestagent.New(guestagent.Config{Token: testToken, Home: scratchHome(t), Shell: agenttest.Shell()})
	ctx, cancel := context.WithCancel(context.Background())
	conns := make(chan net.Conn)
	done := make(chan error, 1)
	go func() {
		done <- a.ServeLoop(ctx, func() (net.Conn, error) {
			select {
			case c := <-conns:
				return c, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}, 10*time.Millisecond)
	}()
	for i := range 2 {
		host, guest := net.Pipe()
		conns <- guest
		g, err := NewGuest(host, testToken)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Health(ctx); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
		g.Close()
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ServeLoop = %v, want context.Canceled", err)
	}
}

// A guest that reads everything but never answers (hung, channel still open) must fail calls via keepalive.
func TestGuestKeepaliveFailsCallsToHungGuest(t *testing.T) {
	oldIdle, oldPing := guestReadIdleTimeout, guestPingTimeout
	guestReadIdleTimeout, guestPingTimeout = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { guestReadIdleTimeout, guestPingTimeout = oldIdle, oldPing })

	host, far := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, far) }() // reads the preface and requests, never writes
	t.Cleanup(func() { _ = far.Close() })

	g, err := newGuestCtx(context.Background(), host, "tok")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })

	start := time.Now()
	errc := make(chan error, 1)
	go func() { _, err := g.Health(context.Background()); errc <- err }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("Health to a hung guest succeeded")
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("took %v to notice the hung guest", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("call to a hung guest never returned")
	}
}
