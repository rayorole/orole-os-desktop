//go:build !unix

package guestagent

import "os/exec"

// setProcessGroup is a no-op off Unix (host tests on Windows); exec's default Cancel kills the process.
func setProcessGroup(*exec.Cmd) {}
