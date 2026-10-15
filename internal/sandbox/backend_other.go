//go:build !windows

package sandbox

import "context"

type unsupportedBackend struct{}

// NewHostBackend returns a backend that reports the computer as unavailable: only Windows is built.
func NewHostBackend(*Runtime, Paths) Backend { return unsupportedBackend{} }

func (unsupportedBackend) Availability(context.Context) Availability {
	return Availability{Reason: "unsupported-os", Detail: "The computer is only available on Windows for now."}
}

func (unsupportedBackend) Boot(context.Context, BootSpec) (VM, error) { return nil, ErrNotReady }
