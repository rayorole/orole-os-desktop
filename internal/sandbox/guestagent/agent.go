// Package guestagent is the HTTP handler of orole-guest, the agent inside a bot's VM. It is OS-neutral so host
// tests can run the real handler in-process over net.Pipe; cmd/orole-guest serves it on the virtio-serial port
// inside the Linux guest. It must never import internal/sandbox.
package guestagent

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

type Config struct {
	Token    string                           // per-boot secret; requests without it get 401. Empty rejects everything.
	Version  string                           // reported by /health
	Home     string                           // default cwd and base of relative paths; default wire.Home
	Shell    []string                         // argv prefix for /exec, the command is appended; default /bin/sh -c
	Ready    func() bool                      // /health "ready"; nil means ready
	PowerOff func() error                     // called 100 ms after /poweroff answers 202; nil answers 501
	Logf     func(format string, args ...any) // nil discards

	LoginShell []string                   // argv for /pty; default /bin/sh -l
	StartPTY   func(PTYSpec) (PTY, error) // nil uses the platform terminal (Linux only; elsewhere /pty answers 501)

	// RunAs is the unprivileged user that /exec, /pty and /files/* run as. nil runs them as the agent's own
	// user (host tests). Switching users needs root and Unix.
	RunAs *Identity
	// HelperExe is the binary started (as RunAs) for /files/* when RunAs is set; it must call RunFileHelper
	// first thing. Default: this executable.
	HelperExe string
}

// Identity is a Unix user to run guest commands as.
type Identity struct {
	Name     string   // login name, for USER and LOGNAME
	UID, GID uint32   // user and primary group
	Groups   []uint32 // supplementary groups; empty clears root's
}

type Agent struct {
	cfg Config
	mux *http.ServeMux

	ptyMu sync.Mutex
	ptys  map[string]PTY // open terminals by id, for /pty/resize
}

func New(cfg Config) *Agent {
	if cfg.Home == "" {
		cfg.Home = wire.Home
	}
	if len(cfg.Shell) == 0 {
		cfg.Shell = []string{"/bin/sh", "-c"}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if len(cfg.LoginShell) == 0 {
		cfg.LoginShell = []string{"/bin/sh", "-l"}
	}
	if cfg.StartPTY == nil {
		cfg.StartPTY = startPTY
	}
	a := &Agent{cfg: cfg, mux: http.NewServeMux(), ptys: map[string]PTY{}}
	a.mux.HandleFunc("POST /pty", a.handlePTY)
	a.mux.HandleFunc("POST /pty/resize", a.handleResize)
	a.mux.HandleFunc("GET /health", a.handleHealth)
	a.mux.HandleFunc("POST /exec", a.handleExec)
	a.mux.HandleFunc("POST /files/read", a.handleReadFile)
	a.mux.HandleFunc("POST /files/write", a.handleWriteFile)
	a.mux.HandleFunc("POST /screen", notAvailable)
	a.mux.HandleFunc("POST /browser", notAvailable)
	a.mux.HandleFunc("POST /poweroff", a.handlePowerOff)
	return a
}

// Handler checks the token on every request, then routes it.
func (a *Agent) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get(wire.TokenHeader)
		if a.cfg.Token == "" || subtle.ConstantTimeCompare([]byte(got), []byte(a.cfg.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.mux.ServeHTTP(w, r)
	})
}

func (a *Agent) handleHealth(w http.ResponseWriter, _ *http.Request) {
	ready := a.cfg.Ready == nil || a.cfg.Ready()
	writeJSON(w, http.StatusOK, wire.Health{Version: a.cfg.Version, Ready: ready})
}

// notAvailable answers the stream types later phases add (/screen in phase 4, /browser in phase 3).
func notAvailable(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "not available in this guest version", http.StatusNotImplemented)
}

func (a *Agent) handlePowerOff(w http.ResponseWriter, _ *http.Request) {
	if a.cfg.PowerOff == nil {
		http.Error(w, "power off is not available", http.StatusNotImplemented)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = http.NewResponseController(w).Flush()
	off := a.cfg.PowerOff
	time.AfterFunc(100*time.Millisecond, func() { // let the 202 reach the host first
		if err := off(); err != nil {
			a.cfg.Logf("power off: %v", err)
		}
	})
}

func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return false
	}
	http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
