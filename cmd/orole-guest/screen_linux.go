//go:build linux

package main

import (
	"context"
	"net"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

// vncAddr is the guest's VNC server, bound to loopback inside the VM only.
const vncAddr = "127.0.0.1:5900"

func dialVNC(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp", vncAddr)
}

// guestScreen relays /screen to the VNC server in the Linux image.
func guestScreen(cfg *guestagent.Config) { cfg.Screen = dialVNC }
