package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

// streamTouchEvery limits how often bytes on a stream report activity to the manager. A variable for tests.
var streamTouchEvery = time.Second

// stream is one full-duplex HTTP/2 request: writes go to the guest, reads come from it. An open stream is not
// "busy" (a forgotten terminal must not pin the VM); bytes moving on it count as bot activity instead.
type stream struct {
	g         *Guest
	w         *io.PipeWriter
	resp      *http.Response
	cancel    context.CancelFunc
	closeOnce sync.Once
	lastTouch atomic.Int64 // unix nanos of the last reported activity
}

// activity reports bot activity, at most once per streamTouchEvery.
func (s *stream) activity() {
	now := time.Now().UnixNano()
	last := s.lastTouch.Load()
	if now-last >= int64(streamTouchEvery) && s.lastTouch.CompareAndSwap(last, now) {
		s.g.touch()
	}
}

func (s *stream) Read(p []byte) (int, error) {
	n, err := s.resp.Body.Read(p)
	if n > 0 {
		s.activity()
	}
	return n, err
}

func (s *stream) Write(p []byte) (int, error) {
	n, err := s.w.Write(p)
	if n > 0 {
		s.activity()
	}
	return n, err
}

func (s *stream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.w.Close()
		s.cancel()
		_ = s.resp.Body.Close()
	})
	return nil
}

// openStream starts a full-duplex request. ctx bounds only the opening; the stream lives until Close.
func (g *Guest) openStream(ctx context.Context, path string) (*stream, error) {
	select {
	case <-g.aborted:
		return nil, g.abortErr
	default:
	}
	pr, pw := io.Pipe()
	sctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, guestBase+path, pr)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set(wire.TokenHeader, g.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	type result struct {
		resp *http.Response
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := g.cc.RoundTrip(req)
		ch <- result{resp, err}
	}()
	var r result
	select {
	case r = <-ch:
	case <-ctx.Done():
		cancel()
		_ = pw.Close()
		if late := <-ch; late.resp != nil {
			_ = late.resp.Body.Close()
		}
		return nil, ctx.Err()
	}
	if r.err != nil {
		cancel()
		_ = pw.Close()
		return nil, g.mapErr(ctx, r.err)
	}
	if err := statusError(path, r.resp); err != nil {
		_ = r.resp.Body.Close()
		cancel()
		_ = pw.Close()
		return nil, err
	}
	s := &stream{g: g, w: pw, resp: r.resp, cancel: cancel}
	s.lastTouch.Store(time.Now().UnixNano())
	g.touch() // opening a stream is bot activity
	return s, nil
}

// OpenScreen relays raw RFB bytes to the guest's VNC server (wayvnc on the guest's 127.0.0.1:5900).
func (g *Guest) OpenScreen(ctx context.Context) (io.ReadWriteCloser, error) {
	return g.openStream(ctx, "/screen")
}

var errPTYClosed = errors.New("sandbox: terminal closed")

// OpenPTY starts a login shell in the guest. The result plugs into the daemon's terminal manager; Pid is 0.
func (g *Guest) OpenPTY(ctx context.Context, cols, rows int) (terminals.Process, error) {
	s, err := g.openStream(ctx, fmt.Sprintf("/pty?cols=%d&rows=%d", cols, rows))
	if err != nil {
		return nil, err
	}
	id := s.resp.Header.Get(wire.PTYHeader)
	if id == "" {
		_ = s.Close()
		return nil, errors.New("sandbox: guest /pty: no terminal id")
	}
	return &guestPTY{stream: s, id: id, done: make(chan struct{})}, nil
}

type guestPTY struct {
	*stream
	id       string
	doneOnce sync.Once
	done     chan struct{}
	code     int
	err      error
}

func (p *guestPTY) Read(b []byte) (int, error) {
	n, err := p.stream.Read(b)
	if err != nil {
		p.finish(err)
	}
	return n, err
}

func (p *guestPTY) Close() error {
	p.finish(errPTYClosed) // first: a concurrent Read must not report the teardown as a channel failure
	return p.stream.Close()
}

func (p *guestPTY) finish(err error) {
	p.doneOnce.Do(func() {
		p.code = -1
		switch {
		case errors.Is(err, io.EOF):
			if c, convErr := strconv.Atoi(p.resp.Trailer.Get(wire.ExitTrailer)); convErr == nil {
				p.code = c
			} else {
				p.err = errors.New("sandbox: terminal ended without an exit code")
			}
		case errors.Is(err, errPTYClosed):
		default:
			p.err = p.g.mapErr(context.Background(), err)
		}
		close(p.done)
	})
}

func (p *guestPTY) Resize(cols, rows int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return p.g.call(ctx, http.MethodPost, "/pty/resize", wire.ResizeRequest{ID: p.id, Cols: cols, Rows: rows}, nil)
}

func (p *guestPTY) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		return p.code, p.err
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

func (p *guestPTY) Pid() int { return 0 }
