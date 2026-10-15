// Command orole-guest is the agent inside a bot's VM. It serves the daemon's requests (exec, files, terminal)
// over HTTP/2 on the virtio-serial port org.orole.guest.0, authenticated with the per-boot token from QEMU
// fw_cfg. It runs as root; commands and terminals run as the unprivileged guest user (bot). It only works inside
// the Linux guest image; the handler is internal/sandbox/guestagent.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
	// The agent re-executes itself as the bot user for file operations.
	if code, ok := guestagent.RunFileHelper(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); ok {
		os.Exit(code)
	}
	logger := log.New(os.Stderr, "orole-guest: ", log.LstdFlags)
	token, err := readToken()
	if err != nil {
		logger.Fatal(err)
	}
	cfg := guestagent.Config{Token: token, Version: version, PowerOff: powerOff, Logf: logger.Printf}
	if err := guestUser(&cfg); err != nil {
		logger.Fatal(err)
	}
	guestBrowser(&cfg)
	guestScreen(&cfg)
	agent := guestagent.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := agent.ServeLoop(ctx, openPort, time.Second); err != nil && !errors.Is(err, context.Canceled) {
		logger.Fatal(err)
	}
}

// readToken is the only place that knows where the per-boot token comes from: QEMU fw_cfg (the host's only
// source since phase 2), with orole.token= on the kernel command line as a fallback.
func readToken() (string, error) { return readTokenFrom(fwCfgTokenPath, "/proc/cmdline") }

var errEmptyToken = errors.New("fw_cfg token is empty")

func readTokenFrom(fwCfg, cmdlinePath string) (string, error) {
	tok, err := fwCfgToken(fwCfg)
	if err == nil {
		return tok, nil
	}
	// Fall back only when there is no fw_cfg token at all; a token that exists but cannot be read is an error.
	if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, errEmptyToken) {
		return "", fmt.Errorf("read fw_cfg token: %w", err)
	}
	cmdline, err := os.ReadFile(cmdlinePath)
	if err != nil {
		return "", fmt.Errorf("no fw_cfg token, and the kernel command line is unreadable: %w", err)
	}
	token := tokenFromCmdline(string(cmdline))
	if token == "" {
		return "", errors.New("no channel token (fw_cfg opt/orole/token or orole.token= on the kernel command line)")
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
