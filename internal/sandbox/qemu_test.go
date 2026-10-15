package sandbox

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func testQEMUConfig() qemuConfig {
	return qemuConfig{
		Spec: BootSpec{
			BotID: "bot-1", Limits: Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10},
			BaseImage: `C:\d\sandbox\images\0.1.0\base.qcow2`, Kernel: `C:\d\sandbox\images\0.1.0\vmlinuz`,
			Initrd: `C:\d\sandbox\images\0.1.0\initrd`, Overlay: `C:\d\sandbox\bots\bot-1.qcow2`,
			Token: strings.Repeat("ab", 32), LogPath: `C:\d\logs\sandbox-bot-1.log`,
		},
		Share: `C:\d\sandbox\runtime\20260811\share`, CPU: "host",
		Cmdline:     "init=/nix/store/abc-nixos-system/init root=/dev/disk/by-label/nixos\n",
		GuestSocket: `C:\d\sandbox\run\0123456789abcdef.sock`, QMPSocket: `C:\d\sandbox\run\0123456789abcdef.qmp`,
	}
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestQEMUArgsBootTheImageWithWHPX(t *testing.T) {
	c := testQEMUConfig()
	args := qemuArgs(c)
	for _, want := range [][2]string{
		{"-machine", "q35"}, {"-accel", "whpx,kernel-irqchip=off"}, {"-cpu", "host"}, {"-m", "2048"}, {"-smp", "2"},
		{"-L", c.Share}, {"-display", "none"}, {"-vga", "none"},
		{"-kernel", c.Spec.Kernel}, {"-initrd", c.Spec.Initrd},
		{"-append", "init=/nix/store/abc-nixos-system/init root=/dev/disk/by-label/nixos console=ttyS0 panic=-1"},
		{"-fw_cfg", "name=opt/orole/token,file=" + c.TokenFile},
		{"-drive", `if=none,id=root,format=qcow2,cache=writeback,discard=unmap,file=` + c.Spec.Overlay},
		{"-device", "virtio-blk-pci,drive=root"},
		{"-netdev", "user,id=net0"}, {"-device", "virtio-net-pci,netdev=net0,romfile="},
		{"-device", "virtio-rng-pci"}, {"-device", "virtio-serial-pci,id=vser"},
		{"-chardev", "socket,id=guest,path=" + c.GuestSocket},
		{"-device", "virtserialport,bus=vser.0,chardev=guest,name=org.orole.guest.0"},
		{"-chardev", "socket,id=qmp,path=" + c.QMPSocket}, {"-mon", "chardev=qmp,mode=control"},
		{"-chardev", "file,id=con,append=on,path=" + c.Spec.LogPath}, {"-serial", "chardev:con"},
		{"-name", "orole-bot-1"},
	} {
		if !hasPair(args, want[0], want[1]) {
			t.Errorf("missing %s %s in\n%q", want[0], want[1], args)
		}
	}
	for _, flag := range []string{"-nodefaults", "-no-user-config", "-no-reboot"} {
		if !slices.Contains(args, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if strings.Contains(strings.Join(args, " "), c.Spec.Token) {
		t.Fatal("the token is in argv")
	}
	for i, a := range args {
		if a == "-append" && strings.Contains(args[i+1], c.Spec.Token) {
			t.Fatal("the token is on the kernel command line, which the guest console prints")
		}
	}
}

func TestQEMUArgsEscapeCommasInPaths(t *testing.T) {
	c := testQEMUConfig()
	c.Spec.Overlay = `C:\Users\a,b\bot.qcow2`
	if !hasPair(qemuArgs(c), "-drive", `if=none,id=root,format=qcow2,cache=writeback,discard=unmap,file=C:\Users\a,,b\bot.qcow2`) {
		t.Fatal("comma in the overlay path is not doubled")
	}
}

func TestQEMUArgsNeverOpenANetworkListener(t *testing.T) {
	joined := strings.Join(qemuArgs(testQEMUConfig()), " ")
	for _, bad := range []string{"hostfwd", "tcp:", "-vnc", "-spice", "-gdb", "telnet", "server=on", "ssd=off"} {
		if strings.Contains(joined, bad) {
			t.Errorf("argv contains %q", bad)
		}
	}
}

func TestQMPSocketSitsNextToTheGuestSocket(t *testing.T) {
	if got := qmpSocket(`C:\d\run\0123456789abcdef.sock`); got != `C:\d\run\0123456789abcdef.qmp` {
		t.Fatal(got)
	}
}

func TestRedactArgsHidesTheToken(t *testing.T) {
	c := testQEMUConfig()
	args := append(qemuArgs(c), "-fw_cfg", "name=opt/orole/token,string="+c.Spec.Token)
	red := redactArgs(args, c.Spec.Token)
	if strings.Contains(strings.Join(red, " "), c.Spec.Token) {
		t.Fatal("token survived redaction")
	}
	if !hasPair(red, "-fw_cfg", "name=opt/orole/token,string=<redacted>") {
		t.Fatalf("%q", red)
	}
}

func TestTailBufferKeepsTheEndAndRedactsTheToken(t *testing.T) {
	tb := newTailBuffer(32, "secret-token")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(tb, "line %d\n", i)
	}
	fmt.Fprint(tb, "qemu: -fw_cfg string=secret-token: bad\n")
	s := tb.suffix()
	if strings.Contains(s, "secret-token") || !strings.HasPrefix(s, ": ") || !strings.Contains(s, "bad") || strings.Contains(s, "line 0") {
		t.Fatalf("suffix %q", s)
	}
	if newTailBuffer(32, "x").suffix() != "" {
		t.Fatal("empty buffer has a suffix")
	}
}

func TestQEMUArgsAcceleratorIsAParameter(t *testing.T) {
	c := testQEMUConfig()
	c.Accel, c.CPU = "tcg", "max"
	args := qemuArgs(c)
	if !hasPair(args, "-accel", "tcg") || !hasPair(args, "-cpu", "max") {
		t.Fatalf("%q", args)
	}
}

func TestQEMUArgsCPUDefaults(t *testing.T) {
	for _, tc := range []struct{ accel, cpu, want string }{
		{"", "", defaultCPU}, {"whpx", "host", "host"}, {"whpx,kernel-irqchip=off", "", "host"},
		{"tcg", "", "max"}, {"tcg", "host", "max"}, {"tcg,thread=multi", "host", "max"}, {"tcg", "qemu64", "qemu64"},
	} {
		c := testQEMUConfig()
		c.Accel, c.CPU = tc.accel, tc.cpu
		if !hasPair(qemuArgs(c), "-cpu", tc.want) {
			t.Errorf("accel %q cpu %q: want -cpu %s", tc.accel, tc.cpu, tc.want)
		}
	}
}

func TestTailBufferRedactsATokenSplitAcrossWrites(t *testing.T) {
	tb := newTailBuffer(8, "secret-token")
	fmt.Fprint(tb, "xxxxxxxxxxxx secret-")
	fmt.Fprint(tb, "token tail")
	if s := tb.suffix(); strings.Contains(s, "secret") || strings.Contains(s, "token") && !strings.Contains(s, "<redacted>") {
		t.Fatalf("%q", s)
	}
	tb = newTailBuffer(8, "secret-token")
	fmt.Fprint(tb, "aaaa secret-")
	fmt.Fprint(tb, "token")
	if s := tb.suffix(); strings.Contains(s, "secret") || strings.Contains(s, "ecret") {
		t.Fatalf("partial token left: %q", s)
	}
}
