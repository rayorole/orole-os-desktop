//go:build !linux

package main

import "github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"

// guestBrowser leaves Config.Browser nil: there is no guest Chromium outside the Linux image.
func guestBrowser(*guestagent.Config) {}
