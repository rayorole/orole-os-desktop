//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
)

// shellExecuteInfo is SHELLEXECUTEINFOW (112 bytes on 64-bit Windows).
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

// runElevatedFn is swapped by tests so no UAC prompt is ever raised from a unit test.
var runElevatedFn = runElevated

// EnableWHP runs orole-sandbox-setup.exe elevated (one UAC prompt) and waits for it. When Windows must
// restart, it writes the reboot marker so Availability says "reboot-required" until it has.
func EnableWHP(ctx context.Context, helper string, p Paths) (SetupResult, error) {
	if helper == "" {
		return SetupResult{}, ErrSetupHelperMissing
	}
	f, err := openHelperLocked(helper)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
		return SetupResult{}, ErrSetupHelperMissing
	}
	if errors.Is(err, ErrSetupHelperLink) {
		return SetupResult{}, err
	}
	if err != nil {
		return SetupResult{}, fmt.Errorf("sandbox: setup helper not verified: %w", err)
	}
	// The handle stays open, denying writes, renames and deletes, until the elevated process has started from
	// the file that was just verified.
	defer f.Close()
	if err := checkSetupHelper(f, SetupHelperSHA256, allowUnverifiedSetupHelper()); err != nil {
		return SetupResult{}, err
	}
	code, err := runElevatedFn(ctx, helper, setupcode.EnableWHPArg)
	if err != nil {
		return SetupResult{}, err
	}
	res, err := setupOutcome(code)
	if err == nil && res.RebootRequired {
		if mkErr := os.MkdirAll(p.Root, 0o700); mkErr == nil {
			_ = os.WriteFile(rebootMarkerPath(p), rebootMarker(bootTime()), 0o600)
		}
	}
	return res, err
}

// openHelperLocked opens the helper itself, never what a link points to, for reading and shares it for reading
// only: nobody can change, rename or delete it (or rename a folder above it) while the handle is open, but
// Windows can still load it to run. A symbolic link or other reparse point is refused: ShellExecuteEx would follow
// it to a file that was never verified.
func openHelperLocked(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(h)
		return nil, ErrSetupHelperLink
	}
	return os.NewFile(uintptr(h), path), nil
}

func runElevated(ctx context.Context, exe, params string) (uint32, error) {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	args, _ := windows.UTF16PtrFromString(params)
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI, lpVerb: verb, lpFile: file, lpParameters: args, nShow: swHide}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ErrSetupDeclined
		}
		return 0, fmt.Errorf("sandbox: could not start the setup helper: %w", callErr)
	}
	if info.hProcess == 0 {
		return 0, errors.New("sandbox: the setup helper started without a process handle")
	}
	type result struct {
		code uint32
		err  error
	}
	res := make(chan result, 1)
	go func(h windows.Handle) {
		defer windows.CloseHandle(h)
		if _, err := windows.WaitForSingleObject(h, windows.INFINITE); err != nil {
			res <- result{err: err}
			return
		}
		var code uint32
		err := windows.GetExitCodeProcess(h, &code)
		res <- result{code, err}
	}(info.hProcess)
	select {
	case r := <-res:
		return r.code, r.err
	case <-ctx.Done():
		return 0, ctx.Err() // the helper keeps running; the next availability check sees its result
	}
}
