//go:build unix

package guestagent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group so a timeout kills everything it started.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
