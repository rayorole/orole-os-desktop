package sandbox

import (
	"context"
	"net"
)

type BootSpec struct {
	BotID     string
	Limits    Limits
	BaseImage string // absolute path, read-only
	Kernel    string // absolute path
	Initrd    string // absolute path
	Overlay   string // absolute path to <data>\sandbox\bots\<botID>.qcow2
	Token     string // per-boot random, 32 bytes hex
	LogPath   string // <data>\logs\sandbox-<botID>.log
}

// Backend starts VMs. backend_windows.go (phase 2) runs QEMU; other platforms report NotImplemented; tests use
// fakeBackend.
type Backend interface {
	Availability(ctx context.Context) Availability
	// Boot starts the VM and returns once the guest channel is connected (not necessarily ready).
	Boot(ctx context.Context, spec BootSpec) (VM, error)
}

type VM interface {
	Conn() net.Conn                     // the raw guest byte stream
	Shutdown(ctx context.Context) error // ask guest to power off, wait, then kill
	Kill() error
	Done() <-chan struct{} // closed when the VM process exits
	ExitErr() error
}
