//go:build !unix

package guestagent

import (
	"errors"
	"os/exec"
)

var errRunAsUnsupported = errors.New("running commands as another user needs a Unix guest")

func setIdentity(_ *exec.Cmd, id *Identity) error {
	if id != nil {
		return errRunAsUnsupported
	}
	return nil
}
