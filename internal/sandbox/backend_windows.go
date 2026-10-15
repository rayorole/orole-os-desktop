//go:build windows

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// qemuTailBytes is how much of QEMU's stdout/stderr a boot or crash error quotes (at least 4 KiB).
const qemuTailBytes = 16 << 10

type process interface {
	Wait() error
	Kill() error
}

type starter interface {
	Start(exe string, args []string, out io.Writer) (process, error)
}

type commandRunner func(ctx context.Context, exe string, args ...string) ([]byte, error)

// hostBackend runs each bot VM as qemu-system-x86_64 -accel whpx inside the daemon's job object.
type hostBackend struct {
	rt     *Runtime
	paths  Paths
	probe  func(ctx context.Context) hostProbe
	start  starter
	run    commandRunner
	cpu    string
	accel  string        // empty: defaultAccel
	accept time.Duration // how long QEMU may take to connect its two sockets and finish initialising

	runMu sync.Mutex // serialises restrictRun
}

// NewHostBackend returns the QEMU/WHPX backend.
func NewHostBackend(rt *Runtime, p Paths) Backend {
	j, err := newJobObject()
	return &hostBackend{rt: rt, paths: p, cpu: defaultCPU, accept: 20 * time.Second, run: runCommand,
		probe: func(ctx context.Context) hostProbe { return probeHost(ctx, p) },
		start: &execStarter{job: j, jobErr: err}}
}

func (b *hostBackend) Availability(ctx context.Context) Availability {
	return decideAvailability(b.probe(ctx), b.rt.Ready())
}

// Boot starts QEMU and returns once the guest channel and QMP are connected. spec.Kernel's folder is the
// image version the overlay was made from; its cmdline file supplies the kernel command line.
func (b *hostBackend) Boot(ctx context.Context, spec BootSpec) (VM, error) {
	q, err := b.rt.QEMU()
	if err != nil {
		return nil, err
	}
	guestPath := b.paths.Socket(spec.BotID)
	qmpPath := qmpSocket(guestPath)
	for _, p := range []string{guestPath, qmpPath} {
		if err := CheckSocketPath(p); err != nil {
			return nil, err
		}
	}
	cmdline, err := os.ReadFile(filepath.Join(filepath.Dir(spec.Kernel), imageCmdline))
	if err != nil {
		return nil, fmt.Errorf("%w: the image has no kernel command line: %v", ErrBootFailed, err)
	}
	if err := b.ensureOverlay(ctx, q.Img, spec); err != nil {
		return nil, err
	}
	if err := prepareLog(spec.LogPath); err != nil {
		return nil, err
	}
	if err := b.restrictRun(filepath.Dir(guestPath)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	guestLn, err := listenUnix(guestPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	defer guestLn.Close()
	qmpLn, err := listenUnix(qmpPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	defer qmpLn.Close()

	// QEMU reads the token into fw_cfg while it builds the machine; the file goes once QMP says the machine
	// is initialised, and on every failure path.
	tokenPath := b.paths.Token(spec.BotID)
	if err := os.Remove(tokenPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	if err := writeUserOnlyFile(tokenPath, []byte(spec.Token)); err != nil {
		return nil, fmt.Errorf("%w: could not write the guest token file: %v", ErrBootFailed, err)
	}
	defer os.Remove(tokenPath)

	args := qemuArgs(qemuConfig{Spec: spec, Share: q.Share, CPU: b.cpu, Accel: b.accel, TokenFile: tokenPath,
		Cmdline: string(cmdline), GuestSocket: guestPath, QMPSocket: qmpPath})
	tail := newTailBuffer(qemuTailBytes, spec.Token)
	proc, err := b.start.Start(q.System, args, tail)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBootFailed, err)
	}
	vm := newQEMUVM(proc, tail, []string{guestPath, qmpPath})
	bctx, cancel := context.WithTimeout(ctx, b.accept)
	defer cancel()
	guest, qmpConn, err := acceptBoth(bctx, guestLn, qmpLn, vm.done)
	var qmp *qmpClient
	if err == nil {
		if qmp, err = connectQMP(bctx, qmpConn, vm.done); err != nil {
			guest.Close()
		}
	}
	if err != nil {
		vm.Kill()
		<-vm.done
		return nil, fmt.Errorf("%w: %v%s", ErrBootFailed, err, tail.suffix())
	}
	os.Remove(tokenPath)
	vm.attach(guest, qmp)
	return vm, nil
}

// restrictRun gives the run folder its user-only DACL (it is created if missing). It runs on every boot, so a
// folder deleted and recreated while the daemon runs never keeps the inherited DACL; setting it is cheap.
func (b *hostBackend) restrictRun(dir string) error {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	return restrictDir(dir)
}

// connectQMP negotiates QMP and waits until QEMU has built the machine, giving up when QEMU exits or ctx ends.
func connectQMP(ctx context.Context, conn net.Conn, exited <-chan struct{}) (*qmpClient, error) {
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-exited:
			conn.Close()
		case <-ctx.Done():
			conn.Close()
		case <-finished:
		}
	}()
	c, err := qmpConnect(ctx, conn)
	if err == nil {
		err = c.waitMachineReady(ctx)
	}
	if err != nil {
		conn.Close()
		select {
		case <-exited:
			return nil, errors.New("QEMU exited before the computer connected")
		default:
		}
		if ctx.Err() != nil {
			return nil, errors.New("QEMU did not connect in time")
		}
		return nil, err
	}
	return c, nil
}

// ensureOverlay creates the bot's qcow2 disk on first boot, backed by the read-only base image. An existing
// overlay is the bot's disk and is never recreated.
func (b *hostBackend) ensureOverlay(ctx context.Context, img string, spec BootSpec) error {
	if _, err := os.Stat(spec.Overlay); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spec.Overlay), 0o700); err != nil {
		return err
	}
	tmp := spec.Overlay + ".new"
	os.Remove(tmp)
	out, err := b.run(ctx, img, "create", "-q", "-f", "qcow2", "-F", "qcow2", "-b", spec.BaseImage, tmp, strconv.Itoa(spec.Limits.DiskGB)+"G")
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%w: could not create the computer's disk: %v: %s", ErrBootFailed, err, bytes.TrimSpace(out))
	}
	return os.Rename(tmp, spec.Overlay)
}

func listenUnix(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return net.Listen("unix", path)
}

// prepareLog makes the log folder and rotates a log over 4 MiB to <name>.1.
func prepareLog(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 4<<20 {
		os.Remove(path + ".1")
		return os.Rename(path, path+".1")
	}
	return nil
}

// acceptBoth waits for QEMU to connect to both listeners, until it exits or ctx ends.
func acceptBoth(ctx context.Context, a, b net.Listener, exited <-chan struct{}) (net.Conn, net.Conn, error) {
	type res struct {
		c   net.Conn
		err error
	}
	ca, cb := make(chan res, 1), make(chan res, 1)
	go func() { c, err := a.Accept(); ca <- res{c, err} }()
	go func() { c, err := b.Accept(); cb <- res{c, err} }()
	var ga, gb net.Conn
	fail := func(err error) (net.Conn, net.Conn, error) {
		for _, c := range []net.Conn{ga, gb} {
			if c != nil {
				c.Close()
			}
		}
		return nil, nil, err
	}
	for ga == nil || gb == nil {
		select {
		case r := <-ca:
			if r.err != nil {
				return fail(r.err)
			}
			ga = r.c
		case r := <-cb:
			if r.err != nil {
				return fail(r.err)
			}
			gb = r.c
		case <-exited:
			return fail(errors.New("QEMU exited before the computer connected"))
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fail(errors.New("QEMU did not connect in time"))
			}
			return fail(ctx.Err())
		}
	}
	return ga, gb, nil
}

func runCommand(ctx context.Context, exe string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.CombinedOutput()
}

type execStarter struct {
	job    *jobObject
	jobErr error
}

func (s *execStarter) Start(exe string, args []string, out io.Writer) (process, error) {
	if s.jobErr != nil {
		return nil, fmt.Errorf("job object: %w", s.jobErr)
	}
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := s.job.assign(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return &execProcess{cmd: cmd}, nil
}

type execProcess struct{ cmd *exec.Cmd }

func (p *execProcess) Wait() error { return p.cmd.Wait() }
func (p *execProcess) Kill() error { return p.cmd.Process.Kill() }

// qemuVM is one running QEMU.
type qemuVM struct {
	proc      process
	done      chan struct{}
	powerOnce sync.Once

	mu       sync.Mutex
	guest    net.Conn
	qmp      *qmpClient
	exited   bool
	stopping bool
	exitErr  error
}

func newQEMUVM(p process, tail *tailBuffer, files []string) *qemuVM {
	v := &qemuVM{proc: p, done: make(chan struct{})}
	go func() {
		err := p.Wait()
		v.mu.Lock()
		v.exited = true
		if !v.stopping {
			if err == nil {
				err = errors.New("the guest powered off by itself")
			}
			v.exitErr = fmt.Errorf("%w: %v%s", ErrCrashed, err, tail.suffix())
		}
		conns := []net.Conn{v.guest}
		if v.qmp != nil {
			conns = append(conns, v.qmp.conn)
		}
		v.mu.Unlock()
		for _, c := range conns {
			if c != nil {
				c.Close()
			}
		}
		for _, f := range files {
			os.Remove(f)
		}
		close(v.done)
	}()
	return v
}

func (v *qemuVM) attach(guest net.Conn, qmp *qmpClient) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.exited {
		guest.Close()
		qmp.conn.Close()
	}
	v.guest, v.qmp = guest, qmp
}

func (v *qemuVM) Conn() net.Conn {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.guest
}

// Shutdown asks the guest to power off (ACPI over QMP), waits for QEMU to exit until ctx is done, then kills it.
func (v *qemuVM) Shutdown(ctx context.Context) error {
	v.mu.Lock()
	v.stopping = true
	qmp := v.qmp
	v.mu.Unlock()
	if qmp != nil {
		v.powerOnce.Do(func() { _ = qmp.execute(ctx, "system_powerdown") }) // a guest that ignores it is killed below
	}
	select {
	case <-v.done:
		return nil
	case <-ctx.Done():
		if err := v.Kill(); err != nil {
			select {
			case <-v.done: // it exited meanwhile: the kill error does not matter
				return nil
			default:
				return err
			}
		}
		<-v.done
		return nil
	}
}

func (v *qemuVM) Kill() error {
	v.mu.Lock()
	v.stopping = true
	v.mu.Unlock()
	if err := v.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (v *qemuVM) Done() <-chan struct{} { return v.done }

func (v *qemuVM) ExitErr() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.exitErr
}
