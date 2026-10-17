//go:build windows

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// fakeProc is a QEMU process that exits when told to.
type fakeProc struct {
	exit chan error
	once sync.Once
}

func (p *fakeProc) Wait() error { return <-p.exit }
func (p *fakeProc) Kill() error { p.exitWith(errors.New("killed")); return nil }
func (p *fakeProc) exitWith(err error) {
	p.once.Do(func() { p.exit <- err })
}

// fakeQEMU stands in for qemu-system-x86_64: it reads the token file and connects to the guest and QMP
// sockets named in argv, like the real one, and powers off on system_powerdown unless ignorePowerdown is set.
type fakeQEMU struct {
	ignorePowerdown   bool
	exitBeforeConnect bool
	args              []string
	proc              *fakeProc
	guest             net.Conn // the guest's end of the channel
	token             string   // what QEMU read from the fw_cfg token file
	machineNotReady   int      // query-cpus-fast answers "only after machine initialization" this many times

	mu           sync.Mutex
	commands     []string
	tokenAtQuery []bool // whether the token file existed at each query-cpus-fast
}

func chardevPath(args []string, id string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-chardev" && strings.HasPrefix(args[i+1], "socket,id="+id+",path=") {
			return strings.ReplaceAll(strings.TrimPrefix(args[i+1], "socket,id="+id+",path="), ",,", ",")
		}
	}
	return ""
}

func tokenFileArg(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-fw_cfg" && strings.HasPrefix(args[i+1], "name=opt/orole/token,file=") {
			return strings.ReplaceAll(strings.TrimPrefix(args[i+1], "name=opt/orole/token,file="), ",,", ",")
		}
	}
	return ""
}

func (f *fakeQEMU) Start(exe string, args []string, out io.Writer) (process, error) {
	f.args = args
	f.proc = &fakeProc{exit: make(chan error, 1)}
	if b, err := os.ReadFile(tokenFileArg(args)); err == nil {
		f.token = string(b)
	}
	if f.exitBeforeConnect {
		fmt.Fprintln(out, "qemu-system-x86_64: WHPX: No accelerator found, hr=80370102")
		f.proc.exitWith(errors.New("exit status 1"))
		return f.proc, nil
	}
	g, err := net.Dial("unix", chardevPath(args, "guest"))
	if err != nil {
		return nil, err
	}
	q, err := net.Dial("unix", chardevPath(args, "qmp"))
	if err != nil {
		return nil, err
	}
	f.guest = g
	go func() {
		fmt.Fprintln(q, `{"QMP": {"version": {}, "capabilities": []}}`)
		sc := bufio.NewScanner(q)
		for sc.Scan() {
			f.mu.Lock()
			f.commands = append(f.commands, sc.Text())
			reply := `{"return": {}}`
			if strings.Contains(sc.Text(), "query-cpus-fast") {
				f.tokenAtQuery = append(f.tokenAtQuery, pathExists(tokenFileArg(args)))
				if f.machineNotReady > 0 {
					f.machineNotReady--
					reply = `{"error": {"class": "GenericError", "desc": "The command query-cpus-fast is permitted only after machine initialization has completed"}}`
				}
			}
			f.mu.Unlock()
			fmt.Fprintln(q, reply)
			if strings.Contains(sc.Text(), "system_powerdown") && !f.ignorePowerdown {
				f.proc.exitWith(nil)
			}
		}
	}()
	return f.proc, nil
}

func shortRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbx") // short: AF_UNIX paths are limited to MaxSocketPath
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func installedRuntime(t *testing.T, paths Paths) *Runtime {
	t.Helper()
	pins := Pins{QEMUVersion: "t", QEMUTag: "qemu-vt", QEMU: Asset{Name: "q.zip", SHA256: "00", Size: 1},
		ImageVersion: "t", ImageTag: "guest-vt", Image: []Asset{{Name: imageKernel, SHA256: "00", Size: 1}}}
	for _, dir := range []string{paths.Runtime("t"), paths.Image("t")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, completeMark), nil, 0o600)
	}
	os.WriteFile(filepath.Join(paths.Image("t"), imageCmdline), []byte("init=/nix/store/abc-nixos-system/init"), 0o600)
	return NewRuntime(RuntimeConfig{Paths: paths, Pins: pins})
}

func testBackend(t *testing.T, f *fakeQEMU) (*hostBackend, BootSpec, *[][]string) {
	t.Helper()
	root := shortRoot(t)
	paths := Paths{Root: filepath.Join(root, "sandbox")}
	rt := installedRuntime(t, paths)
	var calls [][]string
	b := &hostBackend{rt: rt, paths: paths, start: f, cpu: "host", accept: 5 * time.Second,
		probe: func(context.Context) hostProbe { return hostProbe{Build: 22000} },
		run: func(ctx context.Context, exe string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{exe}, args...))
			return nil, os.WriteFile(args[len(args)-2], []byte("QFI"), 0o600) // qemu-img writes the overlay
		},
	}
	img, err := rt.Image()
	if err != nil {
		t.Fatal(err)
	}
	spec := BootSpec{BotID: "bot-1", Limits: DefaultLimits(), BaseImage: img.BaseImage, Kernel: img.Kernel, Initrd: img.Initrd,
		Overlay: paths.Overlay("bot-1"), Token: strings.Repeat("ab", 32), LogPath: filepath.Join(root, "logs", "sandbox-bot-1.log")}
	return b, spec, &calls
}

func TestBootCreatesTheOverlayOnceAndConnectsTheGuestChannel(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, calls := testBackend(t, f)
	ctx := context.Background()
	vm, err := b.Boot(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatalf("qemu-img calls %q", *calls)
	}
	c := strings.Join((*calls)[0], " ")
	for _, want := range []string{"create", "-f qcow2", "-F qcow2", "-b " + spec.BaseImage, spec.Overlay + ".new 10G"} {
		if !strings.Contains(c, want) {
			t.Errorf("qemu-img %q lacks %q", c, want)
		}
	}
	if !pathExists(spec.Overlay) || pathExists(spec.Overlay+".new") {
		t.Fatal("overlay not moved into place")
	}
	if _, err := vm.Conn().Write([]byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(f.guest).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("guest read %q, %v", line, err)
	}

	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := vm.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	<-vm.Done()
	if vm.ExitErr() != nil {
		t.Fatalf("clean shutdown has ExitErr %v", vm.ExitErr())
	}
	f.mu.Lock()
	cmds := strings.Join(f.commands, " ")
	f.mu.Unlock()
	if !strings.Contains(cmds, "qmp_capabilities") || !strings.Contains(cmds, "system_powerdown") {
		t.Fatalf("qmp commands %q", cmds)
	}
	if sock := b.paths.Socket("bot-1"); pathExists(sock) || pathExists(qmpSocket(sock)) {
		t.Fatal("socket files left behind")
	}

	vm2, err := b.Boot(ctx, spec) // the overlay exists now: no second qemu-img call
	if err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 {
		t.Fatal("overlay created twice")
	}
	vm2.Kill()
	<-vm2.Done()
}

func TestBootNeverRecreatesAnExistingOverlay(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, calls := testBackend(t, f)
	if err := os.MkdirAll(filepath.Dir(spec.Overlay), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.Overlay, []byte("the bot's files"), 0o600); err != nil {
		t.Fatal(err)
	}
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	if len(*calls) != 0 {
		t.Fatalf("qemu-img ran on an existing overlay: %q", *calls)
	}
	if b, _ := os.ReadFile(spec.Overlay); string(b) != "the bot's files" {
		t.Fatalf("overlay changed: %q", b)
	}
}

func TestBootFailsWhenQEMUImgFails(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	b.run = func(ctx context.Context, exe string, args ...string) ([]byte, error) {
		os.WriteFile(args[len(args)-2], []byte("partial"), 0o600)
		return []byte("qemu-img: disk full\n"), errors.New("exit status 1")
	}
	_, err := b.Boot(context.Background(), spec)
	if !errors.Is(err, ErrBootFailed) || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	if pathExists(spec.Overlay) || pathExists(spec.Overlay+".new") {
		t.Fatal("a failed qemu-img left a disk behind")
	}
	if f.proc != nil {
		t.Fatal("QEMU started without a disk")
	}
}

func TestBootKeepsTheTokenFileUntilTheMachineIsInitialised(t *testing.T) {
	f := &fakeQEMU{machineNotReady: 2}
	b, spec, _ := testBackend(t, f)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	f.mu.Lock()
	seen := append([]bool(nil), f.tokenAtQuery...)
	f.mu.Unlock()
	if len(seen) != 3 || !seen[0] || !seen[1] || !seen[2] {
		t.Fatalf("token file present at each readiness query: %v (want 3 x true)", seen)
	}
	if pathExists(b.paths.Token("bot-1")) {
		t.Fatal("token file still there after the machine was initialised")
	}
}

func TestBootPassesTheTokenInAFileAndDeletesIt(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	if f.token != spec.Token {
		t.Fatalf("QEMU read token %q", f.token)
	}
	file := tokenFileArg(f.args)
	if file != b.paths.Token("bot-1") || filepath.Dir(file) != filepath.Dir(b.paths.Socket("bot-1")) {
		t.Fatalf("token file %s is not Paths.Token in the run folder", file)
	}
	if pathExists(file) {
		t.Fatal("token file still there after the guest channel connected")
	}
	if strings.Contains(strings.Join(f.args, " "), spec.Token) {
		t.Fatal("token in argv")
	}
}

// daclSIDs returns the SIDs of path's DACL entries and whether the DACL is protected (does not inherit).
// It opens path itself, without following reparse points: an AF_UNIX socket file is one.
func daclSIDs(t *testing.T, path string) (sids []string, protected bool) {
	t.Helper()
	name, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(name, windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("%s: no DACL: %v", path, err)
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Errorf("%s: ACE %d has type %d", path, i, ace.Header.AceType)
		}
		sids = append(sids, (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String())
	}
	return sids, control&windows.SE_DACL_PROTECTED != 0
}

// userAndSystem is the set of SIDs a user-only DACL may name.
func userAndSystem(t *testing.T) map[string]bool {
	t.Helper()
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return map[string]bool{u.User.Sid.String(): true, "S-1-5-18": true} // the user and SYSTEM
}

// checkUserOnly fails unless path's DACL names exactly the user and SYSTEM (and is protected, when asked).
func checkUserOnly(t *testing.T, path string, mustBeProtected bool) {
	t.Helper()
	want := userAndSystem(t)
	sids, protected := daclSIDs(t, path)
	if mustBeProtected && !protected {
		t.Errorf("%s: the DACL inherits from its parent", path)
	}
	seen := map[string]bool{}
	for _, s := range sids {
		if !want[s] {
			t.Errorf("%s grants %s (ACEs %q)", path, s, sids)
		}
		seen[s] = true
	}
	if len(seen) != len(want) {
		t.Errorf("%s: ACEs %q, want the user and SYSTEM", path, sids)
	}
}

func TestTheRunFolderIsUserOnly(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	sock := b.paths.Socket("bot-1")
	b.start = starterFunc(func(exe string, args []string, out io.Writer) (process, error) {
		// While QEMU starts, the sockets it connects to exist; they inherit the folder's ACEs and nothing else.
		checkUserOnly(t, sock, false)
		checkUserOnly(t, qmpSocket(sock), false)
		checkUserOnly(t, tokenFileArg(args), true)
		return f.Start(exe, args, out)
	})
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	checkUserOnly(t, filepath.Dir(sock), true)
}

func TestEveryBootRestrictsARecreatedRunFolder(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	dir := filepath.Dir(b.paths.Socket("bot-1"))
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	vm.Kill()
	<-vm.Done()
	// Someone deletes the run folder and something recreates it with the inherited DACL.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	vm, err = b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	checkUserOnly(t, dir, true)
}

func TestWriteUserOnlyFileHasAProtectedUserOnlyDACL(t *testing.T) {
	p := filepath.Join(shortRoot(t), "x.tok")
	if err := writeUserOnlyFile(p, []byte("secret")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "secret" {
		t.Fatalf("content %q", b)
	}
	checkUserOnly(t, p, true)
	if err := writeUserOnlyFile(p, []byte("again")); err == nil {
		t.Fatal("overwrote an existing file")
	}
}

func TestShutdownKillsAGuestThatIgnoresPowerdown(t *testing.T) {
	f := &fakeQEMU{ignorePowerdown: true}
	b, spec, _ := testBackend(t, f)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	vm.Shutdown(ctx)
	select {
	case <-vm.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("VM still running after Shutdown's deadline")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("Shutdown waited far past its deadline")
	}
	if vm.ExitErr() != nil {
		t.Fatalf("a requested stop is not a crash: %v", vm.ExitErr())
	}
}

func TestBootFailsFastWhenQEMUExitsBeforeConnecting(t *testing.T) {
	f := &fakeQEMU{exitBeforeConnect: true}
	b, spec, _ := testBackend(t, f)
	b.accept = 30 * time.Second
	start := time.Now()
	_, err := b.Boot(context.Background(), spec)
	if !errors.Is(err, ErrBootFailed) || !strings.Contains(err.Error(), "No accelerator") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if strings.Contains(err.Error(), spec.Token) {
		t.Fatal("token in the error")
	}
	if sock := b.paths.Socket("bot-1"); pathExists(sock) || pathExists(qmpSocket(sock)) {
		t.Fatal("socket files left behind")
	}
	if f.token != spec.Token {
		t.Fatalf("QEMU read token %q", f.token)
	}
	if pathExists(tokenFileArg(f.args)) {
		t.Fatal("token file left behind after a failed boot")
	}
}

func TestBootFailsWhenQEMUNeverConnects(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	b.accept = 200 * time.Millisecond
	b.start = starterFunc(func(exe string, args []string, out io.Writer) (process, error) {
		f.args = args
		f.proc = &fakeProc{exit: make(chan error, 1)}
		return f.proc, nil // hangs without connecting
	})
	_, err := b.Boot(context.Background(), spec)
	if !errors.Is(err, ErrBootFailed) || !strings.Contains(err.Error(), "did not connect") {
		t.Fatalf("err = %v", err)
	}
	if pathExists(tokenFileArg(f.args)) {
		t.Fatal("token file left behind after a timeout")
	}
}

type starterFunc func(exe string, args []string, out io.Writer) (process, error)

func (s starterFunc) Start(exe string, args []string, out io.Writer) (process, error) {
	return s(exe, args, out)
}

func TestAnUnexpectedExitIsACrash(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	f.proc.exitWith(errors.New("exit status 3"))
	<-vm.Done()
	if !errors.Is(vm.ExitErr(), ErrCrashed) {
		t.Fatalf("ExitErr = %v", vm.ExitErr())
	}
	if _, err := vm.Conn().Write([]byte("x")); err == nil {
		t.Fatal("guest channel still open after the crash")
	}
}

func TestBootSupportsALongDataDirWithShortTempSockets(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	b.paths = Paths{Root: filepath.Join(b.paths.Root, strings.Repeat("x", 100))}
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	if strings.HasPrefix(chardevPath(f.args, "guest"), b.paths.Root) {
		t.Fatal("socket still uses the long data directory")
	}
}

func TestBootReadsTheCmdlineNextToTheKernel(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	old := b.paths.Image("old")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, imageCmdline), []byte("init=/nix/store/old-system/init\n"), 0o600)
	spec.Kernel, spec.Initrd = filepath.Join(old, imageKernel), filepath.Join(old, imageInitrd)
	vm, err := b.Boot(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	if !hasPair(f.args, "-append", "init=/nix/store/old-system/init console=ttyS0 panic=-1") {
		t.Fatalf("args %q", f.args)
	}
}

// Closing the job handle (what Windows does when the daemon dies) kills the processes in it.
func TestTheJobKillsItsProcessesWhenClosed(t *testing.T) {
	j, err := newJobObject()
	if err != nil {
		t.Fatal(err)
	}
	s := &execStarter{job: j}
	ping, err := exec.LookPath("ping.exe")
	if err != nil {
		t.Skip(err)
	}
	p, err := s.Start(ping, []string{"-n", "30", "127.0.0.1"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- p.Wait() }()
	select {
	case err := <-exited:
		t.Fatalf("ping exited early: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	j.close()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		p.Kill()
		t.Fatal("closing the job did not kill its process")
	}
}

func TestAvailabilityNeedsTheRuntime(t *testing.T) {
	b, _, _ := testBackend(t, &fakeQEMU{})
	if a := b.Availability(context.Background()); !a.Ready {
		t.Fatalf("installed runtime + working WHP: %+v", a)
	}
	b.rt = NewRuntime(RuntimeConfig{Paths: Paths{Root: shortRoot(t)}, Pins: b.rt.cfg.Pins})
	if a := b.Availability(context.Background()); a.Reason != "runtime-missing" {
		t.Fatalf("%+v", a)
	}
}

func TestBootPassesTheHostKeymapReadAtEachBoot(t *testing.T) {
	f := &fakeQEMU{}
	b, spec, _ := testBackend(t, f)
	layout := "be"
	b.keymap = func() string { return layout }
	for _, want := range []string{"be", "ch(fr)"} {
		layout = want
		vm, err := b.Boot(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if !hasPair(f.args, "-fw_cfg", "name=opt/orole/keymap,string="+want) {
			t.Errorf("boot with %q: args %q", want, f.args)
		}
		vm.Kill()
		<-vm.Done()
	}
}
