package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

const (
	guestBase = "http://guest"
	// maxResponseBytes bounds any JSON answer: 1 MiB of exec output escapes to at most ~6 MiB, a 4 MiB file
	// base64-encodes to ~5.4 MiB.
	maxResponseBytes = 8 << 20
	// crashGrace is how long a failed request waits for the manager to say why the channel died.
	crashGrace = 500 * time.Millisecond
)

// HTTP/2 keepalive: a guest that hangs but keeps the channel open is detected by an unanswered PING, which
// closes the connection and fails calls in flight. Variables so tests can shorten them.
var (
	guestReadIdleTimeout = 15 * time.Second
	guestPingTimeout     = 10 * time.Second
)

// Guest is an HTTP/2 (h2c) client bound to one VM's byte stream. Its answers are untrusted guest data.
type Guest struct {
	token string
	conn  net.Conn
	cc    *http2.ClientConn

	abortOnce sync.Once
	aborted   chan struct{}
	abortErr  error

	mu      sync.Mutex
	onUse   func(delta int)
	onTouch func()

	hookMu sync.RWMutex
	onExec func(wire.ExecRequest, wire.ExecResult)
}

func NewGuest(conn net.Conn, token string) (*Guest, error) {
	cc, err := (&http2.Transport{
		AllowHTTP: true, ReadIdleTimeout: guestReadIdleTimeout, PingTimeout: guestPingTimeout,
		// Without this, CanTakeNewRequest (alive) turns false whenever the guest's stream limit is reached, and
		// the manager would mistake a busy channel for a dead one and reboot the VM.
		StrictMaxConcurrentStreams: true,
	}).NewClientConn(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sandbox: guest channel: %w", err)
	}
	return &Guest{token: token, conn: conn, cc: cc, aborted: make(chan struct{})}, nil
}

// newGuestCtx is NewGuest bounded by ctx: writing the HTTP/2 preface blocks until the guest reads it.
func newGuestCtx(ctx context.Context, conn net.Conn, token string) (*Guest, error) {
	type result struct {
		g   *Guest
		err error
	}
	ch := make(chan result, 1)
	go func() {
		g, err := NewGuest(conn, token)
		ch <- result{g, err}
	}()
	select {
	case r := <-ch:
		return r.g, r.err
	case <-ctx.Done():
		_ = conn.Close()
		if r := <-ch; r.g != nil {
			_ = r.g.Close()
		}
		return nil, ctx.Err()
	}
}

func (g *Guest) Health(ctx context.Context) (wire.Health, error) {
	var h wire.Health
	err := g.call(ctx, http.MethodGet, "/health", nil, &h)
	return h, err
}

// Exec runs a command; output is capped again here (1 MiB total) whatever the guest sends.
func (g *Guest) Exec(ctx context.Context, req wire.ExecRequest) (wire.ExecResult, error) {
	var res wire.ExecResult
	if err := g.call(ctx, http.MethodPost, "/exec", req, &res); err != nil {
		return wire.ExecResult{}, err
	}
	if len(res.Stdout)+len(res.Stderr) > wire.MaxExecOutput {
		res.Truncated = true
		if len(res.Stdout) > wire.MaxExecOutput {
			res.Stdout = res.Stdout[:wire.MaxExecOutput]
		}
		res.Stderr = res.Stderr[:min(len(res.Stderr), wire.MaxExecOutput-len(res.Stdout))]
	}
	g.hookMu.RLock()
	hook := g.onExec
	g.hookMu.RUnlock()
	if hook != nil {
		hook(req, res)
	}
	return res, nil
}

// OnExec registers fn to be told about every command that ran successfully (the daemon echoes them in the bot's
// Computer terminal). fn runs on the caller's goroutine after Exec has its (capped) result; it must not block.
func (g *Guest) OnExec(fn func(req wire.ExecRequest, res wire.ExecResult)) {
	g.hookMu.Lock()
	g.onExec = fn
	g.hookMu.Unlock()
}

func (g *Guest) ReadFile(ctx context.Context, path string) (wire.ReadFileResult, error) {
	var res wire.ReadFileResult
	if err := g.call(ctx, http.MethodPost, "/files/read", wire.ReadFileRequest{Path: path}, &res); err != nil {
		return wire.ReadFileResult{}, err
	}
	if len(res.Content) > wire.MaxReadFile {
		res.Content, res.Truncated = res.Content[:wire.MaxReadFile], true
	}
	return res, nil
}

func (g *Guest) WriteFile(ctx context.Context, path string, content []byte) (wire.WriteFileResult, error) {
	if len(content) > wire.MaxWriteFile {
		return wire.WriteFileResult{}, fmt.Errorf("sandbox: the file is %d bytes; the limit is %d", len(content), wire.MaxWriteFile)
	}
	var res wire.WriteFileResult
	err := g.call(ctx, http.MethodPost, "/files/write", wire.WriteFileRequest{Path: path, Content: content}, &res)
	return res, err
}

func (g *Guest) Browser(ctx context.Context, req wire.BrowserRequest) (wire.BrowserResult, error) {
	var res wire.BrowserResult
	err := g.call(ctx, http.MethodPost, "/browser", req, &res)
	return res, err
}

// PowerOff asks the guest to shut down; it answers 202 before it does.
func (g *Guest) PowerOff(ctx context.Context) error {
	return g.call(ctx, http.MethodPost, "/poweroff", nil, nil)
}

func (g *Guest) Close() error {
	_ = g.cc.Close()
	_ = g.conn.Close()
	return nil
}

// alive reports whether the client connection can still take requests; it is false once the channel died
// (keepalive failure, VM exit) or the Guest was closed.
func (g *Guest) alive() bool { return g.cc.CanTakeNewRequest() }

func (g *Guest) setOnUse(fn func(delta int)) {
	g.mu.Lock()
	g.onUse = fn
	g.mu.Unlock()
}

func (g *Guest) setOnTouch(fn func()) {
	g.mu.Lock()
	g.onTouch = fn
	g.mu.Unlock()
}

// touch reports bot activity that is not a request in flight (bytes moving on an open stream).
func (g *Guest) touch() {
	g.mu.Lock()
	fn := g.onTouch
	g.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// use reports a unary request starting (+1) or ending (-1) to the manager. Open streams do not count as busy.
func (g *Guest) use(delta int) {
	g.mu.Lock()
	fn := g.onUse
	g.mu.Unlock()
	if fn != nil {
		fn(delta)
	}
}

// abort records why the channel is going away; calls in flight and later calls return err.
func (g *Guest) abort(err error) {
	g.abortOnce.Do(func() {
		g.abortErr = err
		close(g.aborted)
	})
}

// mapErr turns a transport failure into the manager's reason (crash, stop, delete) when it gives one in time.
func (g *Guest) mapErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	t := time.NewTimer(crashGrace)
	defer t.Stop()
	select {
	case <-g.aborted:
		return g.abortErr
	case <-t.C:
		return fmt.Errorf("sandbox: guest channel: %w", err)
	}
}

func (g *Guest) call(ctx context.Context, method, path string, in, out any) error {
	select {
	case <-g.aborted:
		return g.abortErr
	default:
	}
	var body io.Reader = http.NoBody
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, guestBase+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(wire.TokenHeader, g.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !g.alive() { // a dead channel is not bot activity: do not count it as use
		return g.mapErr(ctx, errors.New("connection closed"))
	}
	g.use(+1)
	defer g.use(-1)
	resp, err := g.cc.RoundTrip(req)
	if err != nil {
		return g.mapErr(ctx, err)
	}
	defer resp.Body.Close()
	if err := statusError(path, resp); err != nil {
		return err
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return g.mapErr(ctx, err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("sandbox: guest %s: answer larger than %d bytes", path, maxResponseBytes)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("sandbox: guest %s: %w", path, err)
	}
	return nil
}

func statusError(path string, resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	text := strings.TrimSpace(string(msg))
	if resp.StatusCode == http.StatusNotImplemented {
		return fmt.Errorf("sandbox: guest %s: %w: %s", path, errors.ErrUnsupported, text)
	}
	return fmt.Errorf("sandbox: guest %s: %s: %s", path, resp.Status, text)
}
