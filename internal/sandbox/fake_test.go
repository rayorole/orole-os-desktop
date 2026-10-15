package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
)

// fakeBackend "boots" a VM by serving the real guest handler on one end of a net.Pipe.
type fakeBackend struct {
	t        *testing.T
	mu       sync.Mutex
	avail    Availability
	bootErr  error
	notReady bool          // the guest's /health never reports ready
	hold     chan struct{} // non-nil: Boot waits until it is closed
	specs    []BootSpec
	vms      []*fakeVM
}

func newFakeBackend(t *testing.T) *fakeBackend {
	return &fakeBackend{t: t, avail: Availability{Ready: true}}
}

func (b *fakeBackend) update(fn func(*fakeBackend)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fn(b)
}

func (b *fakeBackend) Availability(context.Context) Availability {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.avail
}

func (b *fakeBackend) Boot(ctx context.Context, spec BootSpec) (VM, error) {
	b.mu.Lock()
	b.specs = append(b.specs, spec)
	hold, bootErr, notReady := b.hold, b.bootErr, b.notReady
	b.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if bootErr != nil {
		return nil, bootErr
	}
	if _, err := os.Stat(spec.Overlay); errors.Is(err, fs.ErrNotExist) { // stands in for qemu-img create
		if err := os.WriteFile(spec.Overlay, []byte("qcow2"), 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	host, guest := net.Pipe()
	vm := &fakeVM{spec: spec, conn: &stallConn{Conn: host}, guestEnd: guest, done: make(chan struct{})}
	agent := guestagent.New(guestagent.Config{
		Token: spec.Token, Version: "test", Home: scratchHome(b.t), Shell: agenttest.Shell(),
		Ready:    func() bool { return !notReady },
		StartPTY: func(s guestagent.PTYSpec) (guestagent.PTY, error) { return newEchoPTY(s), nil },
		PowerOff: func() error { go vm.exit(nil); return nil },
	})
	go agent.ServeConn(context.Background(), guest)
	b.mu.Lock()
	b.vms = append(b.vms, vm)
	b.mu.Unlock()
	return vm, nil
}

func (b *fakeBackend) bootCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.specs)
}

func (b *fakeBackend) spec(i int) BootSpec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.specs[i]
}

func (b *fakeBackend) vm(i int) *fakeVM {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.vms[i]
}

// vmFor returns the newest VM booted for botID, or nil.
func (b *fakeBackend) vmFor(botID string) *fakeVM {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.vms) - 1; i >= 0; i-- {
		if b.vms[i].spec.BotID == botID {
			return b.vms[i]
		}
	}
	return nil
}

func (b *fakeBackend) liveCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, vm := range b.vms {
		select {
		case <-vm.done:
		default:
			n++
		}
	}
	return n
}

func (b *fakeBackend) killAll() {
	b.mu.Lock()
	vms := append([]*fakeVM(nil), b.vms...)
	b.mu.Unlock()
	for _, vm := range vms {
		vm.exit(errors.New("test cleanup"))
	}
}

// stallConn drops everything the guest sends once stalled: a hung guest that keeps the channel open.
type stallConn struct {
	net.Conn
	stalled atomic.Bool
}

func (c *stallConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if err != nil || !c.stalled.Load() {
			return n, err
		}
	}
}

func (v *fakeVM) stall() { v.conn.(*stallConn).stalled.Store(true) }

type fakeVM struct {
	spec      BootSpec
	conn      net.Conn
	guestEnd  net.Conn
	once      sync.Once
	done      chan struct{}
	mu        sync.Mutex
	exitErr   error
	shutdowns atomic.Int32
	kills     atomic.Int32
}

func (v *fakeVM) Conn() net.Conn { return v.conn }

func (v *fakeVM) Shutdown(context.Context) error {
	v.shutdowns.Add(1)
	v.exit(nil)
	return nil
}

func (v *fakeVM) Kill() error {
	v.kills.Add(1)
	v.exit(errors.New("killed"))
	return nil
}

func (v *fakeVM) Done() <-chan struct{} { return v.done }

func (v *fakeVM) ExitErr() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exitErr
}

// crash makes the "QEMU process" die on its own.
func (v *fakeVM) crash() { v.exit(errors.New("exit status 0xc0000005")) }

func (v *fakeVM) exit(err error) {
	v.once.Do(func() {
		v.mu.Lock()
		v.exitErr = err
		v.mu.Unlock()
		_ = v.conn.Close()
		_ = v.guestEnd.Close()
		close(v.done)
	})
}
