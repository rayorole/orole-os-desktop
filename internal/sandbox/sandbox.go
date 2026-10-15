// Package sandbox runs one local Linux VM, "the bot's computer", per chat bot. The daemon owns every VM: this
// package holds the lifecycle manager, the per-bot store, the guest client and the Backend interface that
// platform code implements. See docs/architecture/bot-computer.md.
package sandbox

import (
	"errors"
	"fmt"
	"regexp"
)

type Limits struct {
	MemoryMB int `json:"memoryMB"` // default 2048
	CPUs     int `json:"cpus"`     // default 2
	DiskGB   int `json:"diskGB"`   // default 10
}

func DefaultLimits() Limits { return Limits{MemoryMB: 2048, CPUs: 2, DiskGB: 10} }

// withDefaults replaces zero or negative fields with DefaultLimits.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MemoryMB <= 0 {
		l.MemoryMB = d.MemoryMB
	}
	if l.CPUs <= 0 {
		l.CPUs = d.CPUs
	}
	if l.DiskGB <= 0 {
		l.DiskGB = d.DiskGB
	}
	return l
}

type State string

const (
	StateOff      State = "off"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateFailed   State = "failed"
)

type Status struct {
	BotID     string `json:"botId"`
	State     State  `json:"state"`
	TakenOver bool   `json:"takenOver"`
	Error     string `json:"error,omitempty"`
}

type Availability struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"` // "whp-disabled", "virtualization-off", "reboot-required", "runtime-missing", "unsupported-os"
	Detail string `json:"detail,omitempty"`
}

var (
	ErrNotReady   = errors.New("sandbox: the computer isn't set up yet")
	ErrBootFailed = errors.New("sandbox: the computer failed to start")
	ErrCrashed    = errors.New("sandbox: the computer stopped unexpectedly")
	ErrBusy       = errors.New("sandbox: all computers are busy, try again shortly")
	ErrLowMemory  = errors.New("sandbox: not enough free memory to start the computer")
	ErrTakenOver  = errors.New("sandbox: the user is controlling the computer")
	// ErrDeleting: the computer is being deleted; Ensure and a second Delete fail with it meanwhile.
	ErrDeleting = errors.New("sandbox: the computer is being deleted")
	// ErrImageGone: the image version the bot's disk was made from is no longer installed. The disk is a delta
	// on that version's base image, so it is not booted against another one.
	ErrImageGone = fmt.Errorf("%w: the image version this computer's disk was made from is no longer installed", ErrBootFailed)
)

var (
	errStopped    = errors.New("sandbox: the computer was stopped")
	errClosed     = errors.New("sandbox: the app is shutting down")
	errNotRunning = errors.New("sandbox: the computer is not running")
)

// Bot ids become file names (overlay, socket, log), so only a safe alphabet is accepted.
var botIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// windowsReserved are device names Windows refuses as file names (with or without an extension).
var windowsReserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])$`)

func checkBotID(id string) error {
	if !botIDPattern.MatchString(id) || windowsReserved.MatchString(id) {
		return fmt.Errorf("sandbox: invalid bot id %q", id)
	}
	return nil
}
