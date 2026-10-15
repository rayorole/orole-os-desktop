package app

import (
	"fmt"

	"golang.org/x/sys/windows"
)

var procAllowSetForegroundWindow = windows.NewLazySystemDLL("user32.dll").NewProc("AllowSetForegroundWindow")

// allowSetForeground lets process pid bring its windows (here: the UAC prompt the daemon launches) to the front.
// Windows grants it only while this process may set the foreground itself, as it does right after a user click.
func allowSetForeground(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if err := procAllowSetForegroundWindow.Find(); err != nil {
		return err
	}
	if r, _, err := procAllowSetForegroundWindow.Call(uintptr(uint32(pid))); r == 0 {
		return fmt.Errorf("AllowSetForegroundWindow: %w", err)
	}
	return nil
}
