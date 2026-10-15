// Command orole-guest is the agent inside a bot's VM. It serves the daemon's requests (exec, files, terminal)
// over HTTP/2 on the virtio-serial port org.orole.guest.0, authenticated with the per-boot token from the kernel
// command line. It only works inside the Linux guest image; the handler is internal/sandbox/guestagent.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	logger := log.New(os.Stderr, "orole-guest: ", log.LstdFlags)
	token, err := readToken()
	if err != nil {
		logger.Fatal(err)
	}
	agent := guestagent.New(guestagent.Config{Token: token, Version: version, PowerOff: powerOff, Logf: logger.Printf})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.ServeLoop(ctx, openPort, time.Second); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal(err)
	}
}

// readToken is the only place that knows where the per-boot token comes from (the kernel command line today;
// phase 2 moves it to QEMU fw_cfg).
func readToken() (string, error) {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return "", fmt.Errorf("read kernel command line: %w", err)
	}
	token := tokenFromCmdline(string(cmdline))
	if token == "" {
		return "", errors.New("no orole.token= on the kernel command line")
	}
	return token, nil
}

// tokenFromCmdline returns the first orole.token= value of a kernel command line, or "".
func tokenFromCmdline(cmdline string) string {
	for _, field := range strings.Fields(cmdline) {
		if v, ok := strings.CutPrefix(field, "orole.token="); ok {
			return v
		}
	}
	return ""
}
