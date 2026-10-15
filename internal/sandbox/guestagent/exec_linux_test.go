//go:build linux

package guestagent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestExecRealShell(t *testing.T) {
	a, home := newAgent(t, func(c *guestagent.Config) { c.Shell = nil }) // default /bin/sh -c
	exec := func(req wire.ExecRequest) (wire.ExecResult, time.Duration) {
		start := time.Now()
		r := decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, req))
		return r, time.Since(start)
	}
	if r, _ := exec(wire.ExecRequest{Command: `yes | head -c 3000000`}); len(r.Stdout) != wire.MaxExecOutput || !r.Truncated || r.ExitCode != 0 {
		t.Errorf("yes|head: len=%d truncated=%v exit=%d", len(r.Stdout), r.Truncated, r.ExitCode)
	}
	if r, _ := exec(wire.ExecRequest{Command: `echo "$HOME"; pwd`}); r.Stdout != home+"\n"+home+"\n" {
		t.Errorf("HOME/pwd = %q", r.Stdout)
	}
	// A background child keeps stdout open; the call still returns (WaitDelay closes the pipes).
	if r, d := exec(wire.ExecRequest{Command: `sleep 20 & echo started`}); !strings.HasPrefix(r.Stdout, "started") || d > 10*time.Second {
		t.Errorf("background child: %q after %v", r.Stdout, d)
	}
	// The timeout kills the whole process group, not just sh.
	if r, d := exec(wire.ExecRequest{Command: `sleep 30; echo never`, Timeout: 1}); !r.TimedOut || r.Stdout != "" || d > 5*time.Second {
		t.Errorf("timeout: %+v after %v", r, d)
	}
}
