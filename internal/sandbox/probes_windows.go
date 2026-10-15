//go:build windows

package sandbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modWHP                        = windows.NewLazySystemDLL("WinHvPlatform.dll")
	procWHvGetCapability          = modWHP.NewProc("WHvGetCapability")
	procWHvCreatePartition        = modWHP.NewProc("WHvCreatePartition")
	procWHvDeletePartition        = modWHP.NewProc("WHvDeletePartition")
	procIsProcessorFeaturePresent = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessorFeaturePresent")
)

const (
	whvCapabilityCodeHypervisorPresent = 0
	pfVirtFirmwareEnabled              = 21
)

// probeHost runs the cheap partition check first and the slower diagnosis only when it fails.
func probeHost(ctx context.Context, p Paths) hostProbe {
	_, _, build := windows.RtlGetNtVersionNumbers()
	h := hostProbe{Build: build & 0xffff}
	if h.Build < minWHPXBuild {
		return h
	}
	if h.PartitionErr = whvCreatePartition(); h.PartitionErr == nil {
		return h
	}
	h.HypervisorPresent = whvHypervisorPresent()
	r, _, _ := procIsProcessorFeaturePresent.Call(pfVirtFirmwareEnabled)
	h.FirmwareVirt = r != 0
	h.Feature = optionalFeatureState(ctx)
	h.RebootPending = cbsRebootPending() || ourRebootPending(p)
	return h
}

func whvCreatePartition() error {
	if err := procWHvCreatePartition.Find(); err != nil {
		return fmt.Errorf("Windows Hypervisor Platform is not available: %w", err)
	}
	var partition uintptr
	hr, _, _ := procWHvCreatePartition.Call(uintptr(unsafe.Pointer(&partition)))
	if int32(uint32(hr)) < 0 {
		return fmt.Errorf("WHvCreatePartition: HRESULT 0x%08X", uint32(hr))
	}
	procWHvDeletePartition.Call(partition)
	return nil
}

func whvHypervisorPresent() bool {
	if procWHvGetCapability.Find() != nil {
		return false
	}
	var buf [16]byte // WHV_CAPABILITY union; HypervisorPresent is its first BOOL
	var written uint32
	hr, _, _ := procWHvGetCapability.Call(whvCapabilityCodeHypervisorPresent,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&written)))
	return int32(uint32(hr)) >= 0 && binary.LittleEndian.Uint32(buf[:4]) != 0
}

// optionalFeatureState asks WMI (readable without admin; DISM is not) for the HypervisorPlatform feature.
func optionalFeatureState(ctx context.Context) featureState {
	winDir, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return featureUnknown
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ps := filepath.Join(winDir, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := exec.CommandContext(ctx, ps, "-NoProfile", "-NonInteractive", "-Command",
		`(Get-CimInstance -ClassName Win32_OptionalFeature -Filter "Name='HypervisorPlatform'").InstallState`)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return parseInstallState(cmd.Output())
}

func cbsRebootPending() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	k.Close()
	return true
}

func ourRebootPending(p Paths) bool {
	b, err := os.ReadFile(rebootMarkerPath(p))
	return err == nil && rebootMarkerActive(b, bootTime())
}

// bootTime is when this Windows session started.
func bootTime() time.Time { return time.Now().Add(-windows.DurationSinceBoot()) }
