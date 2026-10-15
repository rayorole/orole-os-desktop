//go:build linux

package main

import (
	"net"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

const portPath = "/dev/virtio-ports/org.orole.guest.0"

func openPort() (net.Conn, error) {
	f, err := os.OpenFile(portPath, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &fileConn{File: f}, nil
}

// powerOff asks systemd for a clean shutdown and falls back to the reboot syscall.
func powerOff() error {
	if err := exec.Command("systemctl", "poweroff").Run(); err == nil {
		return nil
	}
	unix.Sync()
	return unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
