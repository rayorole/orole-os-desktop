//go:build !linux

package main

import "github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"

// guestScreen leaves Config.Screen nil: there is no guest VNC server outside the Linux image.
func guestScreen(*guestagent.Config) {}
