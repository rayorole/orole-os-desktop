package sandbox

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

// echoPTY stands in for a shell: it echoes input as "echo:<input>" and exits with N on "exit N".
type echoPTY struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	mu   sync.Mutex
	cols int
	rows int
	once sync.Once
	code int
	done chan struct{}
}

func newEchoPTY(s guestagent.PTYSpec) *echoPTY {
	r, w := io.Pipe()
	return &echoPTY{r: r, w: w, cols: s.Cols, rows: s.Rows, done: make(chan struct{})}
}

func (e *echoPTY) Read(b []byte) (int, error) { return e.r.Read(b) }

func (e *echoPTY) Write(b []byte) (int, error) {
	s := string(b)
	if rest, ok := strings.CutPrefix(s, "exit "); ok {
		n, _ := strconv.Atoi(strings.TrimSpace(rest))
		e.finish(n)
		return len(b), nil
	}
	if _, err := e.w.Write([]byte("echo:" + s)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (e *echoPTY) finish(code int) {
	e.once.Do(func() { e.code = code; e.w.Close(); close(e.done) })
}

func (e *echoPTY) Close() error { e.finish(-1); return nil }

func (e *echoPTY) Resize(cols, rows int) error {
	e.mu.Lock()
	e.cols, e.rows = cols, rows
	e.mu.Unlock()
	return nil
}

func (e *echoPTY) size() [2]int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return [2]int{e.cols, e.rows}
}

func (e *echoPTY) Wait() (int, error) { <-e.done; return e.code, nil }

func startEchoAgent(t *testing.T) (*Guest, chan *echoPTY) {
	fakes := make(chan *echoPTY, 4)
	g, _ := startAgent(t, guestagent.Config{StartPTY: func(s guestagent.PTYSpec) (guestagent.PTY, error) {
		p := newEchoPTY(s)
		fakes <- p
		return p, nil
	}})
	return g, fakes
}

func TestGuestPTYRoundTrip(t *testing.T) {
	g, fakes := startEchoAgent(t)
	ctx := context.Background()
	p, err := g.OpenPTY(ctx, 100, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	fake := <-fakes
	if fake.size() != [2]int{100, 30} {
		t.Fatalf("started at %v, want 100x30", fake.size())
	}
	if _, err := io.WriteString(p, "hi"); err != nil {
		t.Fatal(err)
	}
	readUntil(t, p, "echo:hi")
	if err := p.Resize(120, 40); err != nil {
		t.Fatal(err)
	}
	if fake.size() != [2]int{120, 40} {
		t.Fatalf("after resize %v, want 120x40", fake.size())
	}
	if _, err := io.WriteString(p, "exit 3"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, p)
	code, err := p.Wait(ctx)
	if err != nil || code != 3 {
		t.Fatalf("Wait = %d, %v; want 3", code, err)
	}
	if p.Pid() != 0 {
		t.Fatalf("Pid = %d, want 0 for a guest terminal", p.Pid())
	}
}

func TestGuestPTYCloseEndsGuestSession(t *testing.T) {
	g, fakes := startEchoAgent(t)
	p, err := g.OpenPTY(context.Background(), 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	fake := <-fakes
	go io.Copy(io.Discard, p) // the terminal manager reads while another goroutine closes
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.done:
	case <-time.After(5 * time.Second):
		t.Fatal("guest terminal still running after Close")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := p.Wait(ctx); err != nil {
		t.Fatalf("Wait after Close = %v", err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("Close and Wait took %v, want under 200ms", d)
	}
}

func TestGuestPTYUnsupportedOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("the platform PTY exists on Linux")
	}
	g, _ := startAgent(t, guestagent.Config{})
	if _, err := g.OpenPTY(context.Background(), 80, 24); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("OpenPTY = %v, want ErrUnsupported", err)
	}
}

func TestGuestScreenNotAvailableYet(t *testing.T) {
	g, _ := startAgent(t, guestagent.Config{})
	if _, err := g.OpenScreen(context.Background()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("OpenScreen = %v, want ErrUnsupported", err)
	}
}

func TestGuestStreamIsFullDuplex(t *testing.T) {
	g := startHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		buf := make([]byte, 1024)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				_ = rc.Flush()
			}
			if err != nil {
				return
			}
		}
	}))
	s, err := g.OpenScreen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, msg := range []string{"abc", "def"} {
		if _, err := io.WriteString(s, msg); err != nil {
			t.Fatal(err)
		}
		readUntil(t, s, msg)
	}
}
