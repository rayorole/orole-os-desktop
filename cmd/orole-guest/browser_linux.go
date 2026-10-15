package main

import "github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"

// guestBrowser attaches the browser tools to the visible Chromium in the guest.
func guestBrowser(cfg *guestagent.Config) { cfg.Browser = newCDPPage(cdpEndpoint) }
