//go:build unix

package guestagent

import (
	"os/exec"
	"syscall"
)

// setIdentity makes cmd run as id (nil leaves it as is). It keeps any SysProcAttr already set (the process group).
func setIdentity(cmd *exec.Cmd, id *Identity) error {
	if id == nil {
		return nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: id.UID, Gid: id.GID, Groups: id.Groups}
	return nil
}
