//go:build !linux

package guestagent

// startPTY: the guest is Linux; host builds (Windows tests) answer /pty with 501 unless a test injects StartPTY.
func startPTY(PTYSpec) (PTY, error) { return nil, errPTYUnsupported }
