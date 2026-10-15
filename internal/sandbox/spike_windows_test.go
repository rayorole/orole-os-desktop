//go:build windows && spike

// Phase 2 spike (issue #197). Not part of the normal test run; run by hand on a Windows PC:
//
//	$env:OROLE_SPIKE_QEMU   = "$env:TEMP\orole-spike\qemu"    # folder with qemu-system-x86_64.exe, qemu-img.exe, share\
//	$env:OROLE_SPIKE_ALPINE = "$env:TEMP\orole-spike\alpine"  # vmlinuz-virt, initramfs-virt (Alpine netboot)
//	go test -tags spike -run TestSpike -v -timeout 15m ./internal/sandbox
//
// The boot spike runs whpx/host, whpx/max (both with kernel-irqchip=off, see TestSpikeWHPXAccelInit) and
// tcg/max. Set OROLE_SPIKE_ACCEL=whpx or tcg to run only one accelerator. Every finding is logged as a "SPIKE RESULT" line; docs/architecture/bot-computer.md holds them.
package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const spikeToken = "spike-token-123"

func spikeDirs(t *testing.T) (qemuDir, alpineDir string) {
	t.Helper()
	qemuDir, alpineDir = os.Getenv("OROLE_SPIKE_QEMU"), os.Getenv("OROLE_SPIKE_ALPINE")
	if qemuDir == "" || alpineDir == "" {
		t.Skip("set OROLE_SPIKE_QEMU and OROLE_SPIKE_ALPINE")
	}
	return qemuDir, alpineDir
}

// spikeTempDir is short on purpose: AF_UNIX paths are limited to 107 bytes.
func spikeTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "osp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestSpikeHostProbes prints what an unelevated process can learn about WHP, through the same helpers
// probes_windows.go uses.
func TestSpikeHostProbes(t *testing.T) {
	_, _, build := windows.RtlGetNtVersionNumbers()
	t.Logf("SPIKE RESULT probe_build=%d", build&0xffff)
	t.Logf("SPIKE RESULT probe_whp_dll_loads=%v", modWHP.Load() == nil)
	t.Logf("SPIKE RESULT probe_hypervisor_present=%v", whvHypervisorPresent())
	t.Logf("SPIKE RESULT probe_create_partition_err=%v", whvCreatePartition())
	r, _, _ := procIsProcessorFeaturePresent.Call(pfVirtFirmwareEnabled)
	t.Logf("SPIKE RESULT probe_firmware_virtualization=%v", r != 0)
	t.Logf("SPIKE RESULT probe_feature_install_state=%v", optionalFeatureState(context.Background()))
	t.Logf("SPIKE RESULT probe_cbs_reboot_pending=%v", cbsRebootPending())
}

// serialShell drives the guest's /bin/sh on the serial console.
type serialShell struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	n    int
}

func (s *serialShell) readUntil(marker string, timeout time.Duration) string {
	s.t.Helper()
	_ = s.conn.SetReadDeadline(time.Now().Add(timeout))
	var buf bytes.Buffer
	for !strings.Contains(buf.String(), marker) {
		b, err := s.r.ReadByte()
		if err != nil {
			s.t.Fatalf("serial: waiting for %q: %v\n--- console so far ---\n%s", marker, err, buf.String())
		}
		buf.WriteByte(b)
	}
	return buf.String()
}

// run executes one shell line and returns everything printed up to its end marker (the echoed command
// included). The marker is split by quotes in the command, so the echo of the command never matches it.
func (s *serialShell) run(cmd string, timeout time.Duration) string {
	s.t.Helper()
	s.n++
	if _, err := fmt.Fprintf(s.conn, "%s; echo __END\"\"%d__\n", cmd, s.n); err != nil {
		s.t.Fatal(err)
	}
	return s.readUntil(fmt.Sprintf("__END%d__", s.n), timeout)
}

// spikeWHPX is the WHPX accelerator string that initialises on build 22000: QEMU's default in-kernel irqchip
// path also asks WHP for nested virtualization, which this build rejects (WHV_E_UNKNOWN_PROPERTY).
const spikeWHPX = "whpx,kernel-irqchip=off"

// TestSpikeWHPXAccelInit records which -accel strings get past QEMU's accelerator setup (-S: no guest runs).
func TestSpikeWHPXAccelInit(t *testing.T) {
	qemuDir, _ := spikeDirs(t)
	for _, accel := range []string{"whpx", "whpx,hyperv=off", "whpx,kernel-irqchip=on", "whpx,kernel-irqchip=off", "whpx,kernel-irqchip=off,hyperv=off"} {
		cmd := exec.Command(filepath.Join(qemuDir, "qemu-system-x86_64.exe"), "-nodefaults", "-no-user-config",
			"-machine", "q35", "-accel", accel, "-cpu", "host", "-display", "none", "-monitor", "none", "-S",
			"-L", filepath.Join(qemuDir, "share"))
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			t.Logf("SPIKE RESULT accel_init %q=fail (%v): %s", accel, err, strings.TrimSpace(out.String()))
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			t.Logf("SPIKE RESULT accel_init %q=ok %s", accel, strings.TrimSpace(out.String()))
		}
	}
}

func TestSpikeDirectBootAndUnixSocketChannel(t *testing.T) {
	qemuDir, alpineDir := spikeDirs(t)
	runs := []struct{ accel, cpu string }{{spikeWHPX, "host"}, {spikeWHPX, "max"}, {"tcg", "max"}}
	for _, r := range runs {
		if a := os.Getenv("OROLE_SPIKE_ACCEL"); a != "" && !strings.HasPrefix(r.accel, a) {
			continue
		}
		name := strings.SplitN(r.accel, ",", 2)[0] + "/cpu=" + r.cpu
		t.Run(name, func(t *testing.T) { spikeBoot(t, qemuDir, alpineDir, r.accel, r.cpu) })
	}
}

func spikeBoot(t *testing.T, qemuDir, alpineDir, accel, cpu string) {
	tag := "accel=" + accel + " cpu=" + cpu
	dir := spikeTempDir(t)
	guestPath, conPath := filepath.Join(dir, "g.sock"), filepath.Join(dir, "c.sock")
	t.Logf("socket path length %d", len(guestPath))
	listen := func(p string) net.Listener {
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatalf("SPIKE RESULT %s af_unix_listen=fail: %v", tag, err)
		}
		t.Cleanup(func() { ln.Close() })
		return ln
	}
	guestLn, conLn, qmpLn := listen(guestPath), listen(conPath), listen(qmpSocket(guestPath))

	// The token goes in through a file, as qemuArgs does in production.
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(spikeToken), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay := filepath.Join(dir, "o.qcow2")
	if out, err := exec.Command(filepath.Join(qemuDir, "qemu-img.exe"), "create", "-f", "qcow2", overlay, "64M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img: %v\n%s", err, out)
	}

	// A service on host loopback, standing in for any local service: can the guest reach it via 10.0.2.2?
	hits := make(chan string, 4)
	hostLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hostLn.Close()
	// It also serves Alpine's modloop: the netboot initramfs lacks the qemu_fw_cfg module.
	go http.Serve(hostLn, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/modloop-virt" {
			http.ServeFile(w, r, filepath.Join(alpineDir, "modloop-virt"))
			return
		}
		hits <- r.URL.Path
		fmt.Fprint(w, "host")
	}))
	hostPort := hostLn.Addr().(*net.TCPAddr).Port

	// The real production builder, with the serial console moved from a file to a socket so the test can drive it.
	args := qemuArgs(qemuConfig{
		Spec: BootSpec{
			BotID: "spike", Limits: Limits{MemoryMB: 512, CPUs: 2},
			Kernel: filepath.Join(alpineDir, "vmlinuz-virt"), Initrd: filepath.Join(alpineDir, "initramfs-virt"),
			Overlay: overlay, LogPath: filepath.Join(dir, "console.log"),
		},
		Share: filepath.Join(qemuDir, "share"), CPU: cpu, Accel: accel, TokenFile: tokenFile,
		Cmdline: "rdinit=/bin/sh", GuestSocket: guestPath, QMPSocket: qmpSocket(guestPath),
	})
	for i, a := range args {
		if strings.HasPrefix(a, "file,id=con,") {
			args[i] = "socket,id=con,path=" + qemuOpt(conPath)
		}
	}
	t.Logf("qemu %s", strings.Join(args, " "))

	var stderr bytes.Buffer
	cmd := exec.Command(filepath.Join(qemuDir, "qemu-system-x86_64.exe"), args...)
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-exited
		}
	}()
	accept := func(ln net.Listener, what string) net.Conn {
		got := make(chan net.Conn, 1)
		go func() {
			if c, err := ln.Accept(); err == nil {
				got <- c
			}
		}()
		select {
		case c := <-got:
			return c
		case err := <-exited:
			stopped = true
			t.Fatalf("SPIKE RESULT %s %s_connect=fail: qemu exited: %v\n%s", tag, what, err, stderr.String())
		case <-time.After(20 * time.Second):
			t.Fatalf("SPIKE RESULT %s %s_connect=fail: timeout\n%s", tag, what, stderr.String())
		}
		return nil
	}
	// QEMU connects its socket chardevs in command-line order: guest, qmp, con.
	guest := accept(guestLn, "guest")
	defer guest.Close()
	qmp := accept(qmpLn, "qmp")
	defer qmp.Close()
	con := accept(conLn, "console")
	defer con.Close()
	t.Logf("SPIKE RESULT %s af_unix_chardev_connect=ok connected_after=%s", tag, time.Since(start).Round(time.Millisecond))
	qmpGreeting, _ := bufio.NewReader(qmp).ReadString('\n')
	t.Logf("SPIKE RESULT %s qmp_greeting_ok=%v", tag, strings.Contains(qmpGreeting, `"QMP"`))

	sh := &serialShell{t: t, conn: con, r: bufio.NewReader(con)}
	// busybox sh announces itself; the kernel log can contain '#' ("#1-Alpine SMP"), so wait for this line.
	sh.readUntil("job control turned off", 180*time.Second)
	sh.readUntil("# ", 10*time.Second)
	t.Logf("SPIKE RESULT %s shell_after=%s", tag, time.Since(start).Round(time.Millisecond))
	// rdinit=/bin/sh skips Alpine's init, which installs the busybox applet links.
	sh.run("/bin/busybox mkdir -p /usr/bin /usr/sbin; /bin/busybox --install -s", 10*time.Second)
	sh.run("mount -t proc proc /proc; mount -t sysfs sys /sys; mount -t devtmpfs dev /dev", 10*time.Second)
	sh.run("modprobe virtio_pci 2>/dev/null; modprobe virtio_console 2>/dev/null; sleep 1", 15*time.Second)

	out := sh.run(`for p in /sys/class/virtio-ports/*; do echo PORT:$(basename $p):$(cat $p/name); done`, 10*time.Second)
	port := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PORT:vport") && strings.HasSuffix(line, ":"+guestPortName) {
			port = strings.Split(line, ":")[1]
		}
	}
	if port == "" {
		t.Fatalf("SPIKE RESULT %s virtio_port_name=fail\n%s", tag, out)
	}
	t.Logf("SPIKE RESULT %s virtio_port_name=ok device=/dev/%s", tag, port)

	sh.run("echo ping-from-guest > /dev/"+port, 10*time.Second)
	_ = guest.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(guest).ReadString('\n')
	t.Logf("SPIKE RESULT %s guest_to_host=%q err=%v", tag, strings.TrimSpace(line), err)

	sh.run("mkdir -p /tmp; (head -n1 /dev/"+port+" > /tmp/in &)", 5*time.Second)
	time.Sleep(time.Second)
	fmt.Fprint(guest, "pong-from-host\n")
	time.Sleep(time.Second)
	out = sh.run("cat /tmp/in", 5*time.Second)
	// The command echo never contains the payload, so a match is the guest having read it.
	t.Logf("SPIKE RESULT %s host_to_guest_ok=%v", tag, strings.Contains(out, "pong-from-host"))
	if !strings.Contains(out, "pong-from-host") {
		t.Logf("host_to_guest output:\n%s", out)
	}

	sh.run("modprobe virtio_net 2>&1; sleep 1; ip link set eth0 up && ip addr add 10.0.2.15/24 dev eth0 && ip route add default via 10.0.2.2; echo nameserver 10.0.2.3 > /etc/resolv.conf", 15*time.Second)
	out = sh.run(fmt.Sprintf("wget -q -T 5 -O - http://10.0.2.2:%d/from-guest 2>&1; echo RC=$?", hostPort), 15*time.Second)
	reached := false
	select {
	case <-hits:
		reached = true
	case <-time.After(2 * time.Second):
	}
	t.Logf("SPIKE RESULT %s host_loopback_reachable=%v", tag, reached)
	if !reached {
		t.Logf("wget output:\n%s", out)
	}
	out = sh.run("wget -q -T 10 -O /dev/null http://example.com 2>&1; echo RC=$?", 20*time.Second)
	t.Logf("SPIKE RESULT %s internet_ok=%v", tag, strings.Contains(out, "RC=0"))

	// qemu_fw_cfg is a module in Alpine's modloop, not in the netboot initramfs: fetch the modloop from the host.
	out = sh.run(fmt.Sprintf("wget -q -O /modloop http://10.0.2.2:%d/modloop-virt && modprobe squashfs; mkdir -p /.modloop && "+
		"mount -o loop,ro -t squashfs /modloop /.modloop && mount --bind /.modloop/modules /lib/modules; echo RC=$?", hostPort), 60*time.Second)
	t.Logf("modloop: %s", out)
	out = sh.run("modprobe qemu_fw_cfg 2>&1; cat /sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw 2>&1", 10*time.Second)
	// The command echo never contains the token, so a match is the guest reading it.
	t.Logf("SPIKE RESULT %s fw_cfg_token_ok=%v", tag, strings.Contains(out, spikeToken))
	if !strings.Contains(out, spikeToken) {
		t.Logf("fw_cfg output:\n%s", out)
	}
	t.Logf("SPIKE RESULT %s token_in_qemu_argv=%v", tag, strings.Contains(strings.Join(args, " "), spikeToken))

	fmt.Fprint(con, "poweroff -f\n")
	select {
	case err := <-exited:
		stopped = true
		t.Logf("SPIKE RESULT %s poweroff_exit=%v total=%s", tag, err, time.Since(start).Round(time.Millisecond))
	case <-time.After(15 * time.Second):
		t.Logf("SPIKE RESULT %s poweroff_exit=timeout", tag)
	}
}

// TestSpikePipeChardevDACL records the DACL QEMU puts on a `-chardev pipe` (the fallback channel).
func TestSpikePipeChardevDACL(t *testing.T) {
	qemuDir, _ := spikeDirs(t)
	accel := spikeWHPX
	if os.Getenv("OROLE_SPIKE_ACCEL") == "tcg" {
		accel = "tcg"
	}
	name := fmt.Sprintf("orole-spike-%d", os.Getpid())
	cmd := exec.Command(filepath.Join(qemuDir, "qemu-system-x86_64.exe"),
		"-nodefaults", "-no-user-config", "-machine", "q35", "-accel", accel, "-display", "none", "-S",
		"-L", filepath.Join(qemuDir, "share"),
		"-device", "virtio-serial-pci,id=vser", "-chardev", "pipe,id=guest,path="+name,
		"-device", "virtserialport,bus=vser.0,chardev=guest,name="+guestPortName)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// Opening the pipe takes QEMU's only instance, so open it once (READ_CONTROL) and read the DACL off the handle.
	pipe, _ := windows.UTF16PtrFromString(`\\.\pipe\` + name)
	var h windows.Handle
	var err error
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if h, err = windows.CreateFile(pipe, windows.READ_CONTROL, 0, nil, windows.OPEN_EXISTING, 0, 0); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("SPIKE RESULT pipe_sddl=unknown: open: %v\n%s", err, stderr.String())
	}
	defer windows.CloseHandle(h)
	sd, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("SPIKE RESULT pipe_sddl=unknown: %v\n%s", err, stderr.String())
	}
	s := sd.String()
	t.Logf("SPIKE RESULT accel=%s pipe_sddl=%s", accel, s)
	t.Logf("SPIKE RESULT pipe_everyone_or_anonymous_ace=%v", strings.Contains(s, ";;;WD)") || strings.Contains(s, ";;;AN)"))
}
