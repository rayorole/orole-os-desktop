//go:build windows

package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// TestRealBootConnectsTheGuestAgent boots a real QEMU through hostBackend (job object, overlay, run folder
// DACL, token file, QMP) with the spike's Alpine netboot kernel and an initramfs that runs orole-guest, then
// talks HTTP/2 to the agent over the AF_UNIX channel. Alpine's netboot initramfs lacks the qemu_fw_cfg module,
// so orole-guest falls back to the kernel command line for its token and this test puts it there; the init
// script also adds the bot user that orole-guest runs commands as. Run by hand:
//
//	$env:OROLE_SPIKE_QEMU   = "$env:TEMP\orole-spike\qemu"    # qemu-system-x86_64.exe, qemu-img.exe, share\
//	$env:OROLE_SPIKE_ALPINE = "$env:TEMP\orole-spike\alpine"  # vmlinuz-virt, initramfs-virt
//	go test -run TestRealBoot -v ./internal/sandbox
func TestRealBootConnectsTheGuestAgent(t *testing.T) {
	qemuDir, alpineDir := os.Getenv("OROLE_SPIKE_QEMU"), os.Getenv("OROLE_SPIKE_ALPINE")
	if qemuDir == "" || alpineDir == "" {
		t.Skip("set OROLE_SPIKE_QEMU and OROLE_SPIKE_ALPINE to boot a real VM")
	}
	root := shortRoot(t)
	paths := Paths{Root: filepath.Join(root, "sandbox")}
	const version = "t"
	// Hard links: the runtime folder looks installed without copying 170 MB or touching the spike folder.
	if err := linkTree(qemuDir, paths.Runtime(version)); err != nil {
		t.Fatal(err)
	}
	img := paths.Image(version)
	if err := os.MkdirAll(img, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(alpineDir, "vmlinuz-virt"), filepath.Join(img, imageKernel)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(img, imageInitrd), guestInitrd(t, alpineDir), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(filepath.Join(qemuDir, "qemu-img.exe"), "create", "-q", "-f", "qcow2", filepath.Join(img, imageBase), "1G").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img: %v\n%s", err, out)
	}
	token := strings.Repeat("cd", 32)
	if err := os.WriteFile(filepath.Join(img, imageCmdline), []byte("rdinit=/orole-init quiet orole.token="+token), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{paths.Runtime(version), img} {
		if err := os.WriteFile(filepath.Join(dir, completeMark), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pins := Pins{QEMUVersion: version, QEMUTag: "x", QEMU: Asset{Name: "q.zip", SHA256: "00", Size: 1},
		ImageVersion: version, ImageTag: "x", Image: []Asset{{Name: imageKernel, SHA256: "00", Size: 1}}}
	rt := NewRuntime(RuntimeConfig{Paths: paths, Pins: pins})
	b := NewHostBackend(rt, paths)
	if av := b.Availability(context.Background()); !av.Ready {
		t.Fatalf("availability %+v", av)
	}
	set, err := rt.Image()
	if err != nil {
		t.Fatal(err)
	}
	spec := BootSpec{BotID: "real", Limits: Limits{MemoryMB: 512, CPUs: 2, DiskGB: 1}, BaseImage: set.BaseImage,
		Kernel: set.Kernel, Initrd: set.Initrd, Overlay: paths.Overlay("real"), Token: token,
		LogPath: filepath.Join(root, "logs", "sandbox-real.log")}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()
	vm, err := b.Boot(ctx, spec)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	defer func() { vm.Kill(); <-vm.Done() }()
	t.Logf("REALBOOT channel connected after %s", time.Since(start).Round(time.Millisecond))
	if pathExists(paths.Token("real")) {
		t.Error("token file still there after the boot")
	}
	g, err := newGuestCtx(ctx, vm.Conn(), token)
	if err != nil {
		t.Fatalf("h2c: %v", err)
	}
	defer g.Close()
	if err := waitReady(ctx, g, vm); err != nil {
		log, _ := os.ReadFile(spec.LogPath)
		t.Fatalf("guest agent: %v\n--- console ---\n%s", err, log)
	}
	t.Logf("REALBOOT agent ready after %s", time.Since(start).Round(time.Millisecond))
	res, err := g.Exec(ctx, wire.ExecRequest{Command: "uname -sr; id -un"})
	if err != nil || !strings.HasPrefix(res.Stdout, "Linux ") || !strings.HasSuffix(res.Stdout, "\nbot\n") {
		log, _ := os.ReadFile(spec.LogPath)
		t.Fatalf("exec = %+v, %v (want uname, then the bot user)\n--- console ---\n%s", res, err, log)
	}
	t.Logf("REALBOOT exec uname: %s", strings.TrimSpace(res.Stdout))
	// File operations run as bot through the re-executed helper: home is writable, / (root's) is not.
	if _, err := g.WriteFile(ctx, "notes/hi.txt", []byte("hi")); err != nil {
		t.Fatalf("write in home: %v", err)
	}
	if rf, err := g.ReadFile(ctx, "notes/hi.txt"); err != nil || string(rf.Content) != "hi" {
		t.Fatalf("read back = %q, %v", rf.Content, err)
	}
	if _, err := g.WriteFile(ctx, "/root-only.txt", []byte("x")); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("write to / as bot = %v, want 403", err)
	}
	if !pathExists(spec.Overlay) {
		t.Fatal("no overlay")
	}

	// The init script has no ACPI handler, so this exercises powerdown-then-kill.
	sctx, scancel := context.WithTimeout(ctx, 3*time.Second)
	defer scancel()
	if err := vm.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	if vm.ExitErr() != nil {
		t.Fatalf("a requested stop is not a crash: %v", vm.ExitErr())
	}
	t.Logf("REALBOOT stopped after %s", time.Since(start).Round(time.Millisecond))
}

// linkTree mirrors src into dst with hard links (same volume) and real folders.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return os.Link(p, target)
	})
}

// guestInitrd is Alpine's netboot initramfs followed by a second, uncompressed newc archive holding
// orole-guest and an init script; the kernel unpacks both.
func guestInitrd(t *testing.T, alpineDir string) []byte {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "orole-guest")
	cmd := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/orole-guest")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build orole-guest: %v\n%s", err, out)
	}
	guest, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(alpineDir, "initramfs-virt"))
	if err != nil {
		t.Fatal(err)
	}
	const initScript = `#!/bin/sh
/bin/busybox mkdir -p /usr/bin /usr/sbin /proc /sys /dev
/bin/busybox --install -s
mount -t proc proc /proc
mount -t sysfs sys /sys
mount -t devtmpfs dev /dev
modprobe virtio_pci
modprobe virtio_console
for i in 1 2 3 4 5 6 7 8 9 10; do
  for p in /sys/class/virtio-ports/*; do
    if [ "$(cat $p/name 2>/dev/null)" = org.orole.guest.0 ]; then
      mkdir -p /dev/virtio-ports
      ln -sf /dev/$(basename $p) /dev/virtio-ports/org.orole.guest.0
    fi
  done
  [ -e /dev/virtio-ports/org.orole.guest.0 ] && break
  sleep 1
done
mkdir -p /home/bot /tmp
echo 'bot:x:1000:1000::/home/bot:/bin/sh' >> /etc/passwd
echo 'bot:x:1000:' >> /etc/group
chown 1000:1000 /home/bot
chmod 755 /  # the initramfs root is 0700; bot needs to reach /bin
/orole-guest
poweroff -f
`
	var buf bytes.Buffer
	buf.Write(base)
	for buf.Len()%4 != 0 {
		buf.WriteByte(0)
	}
	ino := 1000
	add := func(name string, mode uint32, data []byte) {
		ino++
		fmt.Fprintf(&buf, "070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
			ino, mode, 0, 0, 1, 0, len(data), 0, 0, 0, 0, len(name)+1, 0)
		buf.WriteString(name)
		buf.WriteByte(0)
		for buf.Len()%4 != 0 {
			buf.WriteByte(0)
		}
		buf.Write(data)
		for buf.Len()%4 != 0 {
			buf.WriteByte(0)
		}
	}
	add("orole-init", 0o100755, []byte(initScript))
	add("orole-guest", 0o100755, guest)
	add("TRAILER!!!", 0, nil)
	return buf.Bytes()
}
