//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

func main() {
	os.Exit(run(os.Args[1:], windows.GetCurrentProcessToken().IsElevated(), runDISM))
}

// runDISM runs System32\Dism.exe by full path (never a PATH lookup) without a console window.
func runDISM(args []string) (int, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(filepath.Join(sys, "Dism.exe"), args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
