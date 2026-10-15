package guestagent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// waitDelay bounds how long a finished command may keep its output pipes open through background children.
const waitDelay = 2 * time.Second

func (a *Agent) handleExec(w http.ResponseWriter, r *http.Request) {
	var req wire.ExecRequest
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		http.Error(w, "empty command", http.StatusBadRequest)
		return
	}
	res, err := a.exec(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *Agent) exec(parent context.Context, req wire.ExecRequest) (wire.ExecResult, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(wire.ClampTimeout(req.Timeout))*time.Second)
	defer cancel()
	argv := append(slices.Clone(a.cfg.Shell), req.Command)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = a.resolve(req.Cwd)
	cmd.Env = append(os.Environ(), "HOME="+a.cfg.Home)
	cmd.WaitDelay = waitDelay
	setProcessGroup(cmd)
	out := newCapped(wire.MaxExecOutput)
	stdout, stderr := out.writer(), out.writer()
	cmd.Stdout, cmd.Stderr = stdout, stderr

	runErr := cmd.Run()
	res := wire.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), Truncated: out.truncated()}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && parent.Err() == nil {
		res.TimedOut, res.ExitCode = true, -1
		return res, nil
	}
	if cmd.ProcessState == nil {
		return wire.ExecResult{}, fmt.Errorf("start %s: %w", argv[0], runErr)
	}
	res.ExitCode = cmd.ProcessState.ExitCode()
	return res, nil
}

// resolve maps a guest path: empty is home, relative is under home, absolute is kept (the VM is the boundary).
func (a *Agent) resolve(p string) string {
	if p == "" {
		return a.cfg.Home
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(a.cfg.Home, p)
}
