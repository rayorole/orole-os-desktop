package guestagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// PTYSpec describes the terminal /pty starts.
type PTYSpec struct {
	Argv       []string
	Dir        string
	Env        []string
	Cols, Rows int
}

// PTY is a running terminal process inside the guest.
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	Wait() (exitCode int, err error)
}

var errPTYUnsupported = errors.New("terminals are not available on this guest OS")

// handlePTY runs a login shell on a pseudo-terminal: the request body carries keystrokes, the response body the
// terminal output, the trailer the exit code. Closing the stream from the host closes the terminal.
func (a *Agent) handlePTY(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, err := a.cfg.StartPTY(PTYSpec{
		Argv: a.cfg.LoginShell,
		Dir:  a.cfg.Home,
		Env:  append(os.Environ(), "HOME="+a.cfg.Home, "TERM=xterm-256color"),
		Cols: dimension(q.Get("cols"), 80),
		Rows: dimension(q.Get("rows"), 24),
	})
	if errors.Is(err, errPTYUnsupported) {
		http.Error(w, err.Error(), http.StatusNotImplemented)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := newID()
	a.ptyMu.Lock()
	a.ptys[id] = p
	a.ptyMu.Unlock()
	defer func() {
		a.ptyMu.Lock()
		delete(a.ptys, id)
		a.ptyMu.Unlock()
	}()
	stop := context.AfterFunc(r.Context(), func() { _ = p.Close() })
	defer stop()

	w.Header().Set("Trailer", wire.ExitTrailer)
	w.Header().Set(wire.PTYHeader, id)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.Flush()

	go func() { _, _ = io.Copy(p, r.Body) }()

	// A shell can exit while a background job still holds the terminal, so the read below never sees EOF.
	// When the shell is gone, give buffered output a moment to drain, then hang up to end the session.
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		_, _ = p.Wait()
		select {
		case <-time.After(150 * time.Millisecond):
			_ = p.Close()
		case <-sessionDone:
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				break
			}
			_ = rc.Flush()
		}
		if err != nil {
			break
		}
	}
	_ = p.Close()
	code, _ := p.Wait()
	w.Header().Set(wire.ExitTrailer, strconv.Itoa(code))
}

func (a *Agent) handleResize(w http.ResponseWriter, r *http.Request) {
	var req wire.ResizeRequest
	if !readJSON(w, r, 4<<10, &req) {
		return
	}
	if req.Cols <= 0 || req.Rows <= 0 || req.Cols > 1000 || req.Rows > 1000 {
		http.Error(w, "bad terminal size", http.StatusBadRequest)
		return
	}
	a.ptyMu.Lock()
	p := a.ptys[req.ID]
	a.ptyMu.Unlock()
	if p == nil {
		http.Error(w, "no such terminal", http.StatusNotFound)
		return
	}
	if err := p.Resize(req.Cols, req.Rows); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dimension(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 1000 {
		return def
	}
	return n
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
