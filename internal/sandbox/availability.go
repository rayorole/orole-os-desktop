package sandbox

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// minWHPXBuild is Windows 10 2004, QEMU's tested WHPX baseline on x86_64.
const minWHPXBuild = 19041

type featureState int

const (
	featureUnknown featureState = iota
	featureEnabled
	featureDisabled
)

// hostProbe is what an unelevated process can learn about Windows Hypervisor Platform.
type hostProbe struct {
	Build             uint32
	PartitionErr      error // nil when WHvCreatePartition worked: QEMU can use WHPX
	HypervisorPresent bool  // WHvGetCapability(WHvCapabilityCodeHypervisorPresent)
	FirmwareVirt      bool  // IsProcessorFeaturePresent(PF_VIRT_FIRMWARE_ENABLED)
	Feature           featureState
	RebootPending     bool // Windows servicing or our own setup is waiting for a restart
}

// downloadSuffix names the pinned download size for the setup screen, or nothing in a build without pins.
func downloadSuffix() string {
	if !DefaultPins.Complete() {
		return ""
	}
	return ", " + downloadSizeText(DefaultPins.DownloadSize())
}

// downloadSizeText is n bytes in decimal gigabytes with one decimal ("about 1.6 GB"); empty for n <= 0.
func downloadSizeText(n int64) string {
	if n <= 0 {
		return ""
	}
	return "about " + strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + " GB"
}

// decideAvailability turns probes into what the Computer tab shows. Reasons are the contract's strings.
//
// A successful partition probe is authoritative: the HypervisorPlatform optional feature can read as
// disabled while the Hyper-V role provides WHP, so Feature, FirmwareVirt and RebootPending only explain
// a failed probe and never veto a working one.
func decideAvailability(p hostProbe, runtimeReady bool) Availability {
	switch {
	case p.Build < minWHPXBuild:
		return Availability{Reason: "unsupported-os", Detail: "The computer needs Windows 10 version 2004 (build 19041) or later."}
	case p.PartitionErr == nil && runtimeReady:
		return Availability{Ready: true}
	case p.PartitionErr == nil:
		return Availability{Reason: "runtime-missing", Detail: "Download the computer (QEMU and the Linux image" + downloadSuffix() + ") to continue."}
	case p.RebootPending:
		return Availability{Reason: "reboot-required", Detail: "Restart Windows to finish turning on Windows Hypervisor Platform."}
	case p.Feature != featureEnabled:
		return Availability{Reason: "whp-disabled", Detail: "Windows Hypervisor Platform is turned off. Turning it on needs administrator permission once and a restart."}
	case !p.FirmwareVirt && !p.HypervisorPresent:
		return Availability{Reason: "virtualization-off", Detail: "Virtualization is turned off in this PC's firmware (BIOS/UEFI). Turn on Intel VT-x or AMD-V (SVM) there, then restart."}
	case !p.HypervisorPresent:
		return Availability{Reason: "virtualization-off", Detail: "Windows Hypervisor Platform is on but the Windows hypervisor is not running. In an administrator terminal, `bcdedit /enum` should show `hypervisorlaunchtype Auto`; restart after changing it."}
	default:
		return Availability{Reason: "virtualization-off", Detail: "Windows refused to create a virtual machine: " + p.PartitionErr.Error()}
	}
}

// parseInstallState reads Win32_OptionalFeature.InstallState (1 enabled, 2 disabled, 3 absent).
func parseInstallState(out []byte, err error) featureState {
	if err != nil {
		return featureUnknown
	}
	switch strings.TrimSpace(string(out)) {
	case "1":
		return featureEnabled
	case "2", "3":
		return featureDisabled
	}
	return featureUnknown
}

// rebootMarkerPath is written when the setup helper reported that Windows must restart.
func rebootMarkerPath(p Paths) string { return filepath.Join(p.Root, "setup-reboot") }

// rebootMarker records the boot time of the Windows session the setup ran in.
func rebootMarker(boot time.Time) []byte { return []byte(strconv.FormatInt(boot.Unix(), 10)) }

// rebootMarkerActive reports whether a marker was written during the current boot (same boot time
// within two minutes); after a restart it no longer counts.
func rebootMarkerActive(content []byte, boot time.Time) bool {
	v, err := strconv.ParseInt(strings.TrimSpace(string(content)), 10, 64)
	if err != nil {
		return false
	}
	d := boot.Unix() - v
	if d < 0 {
		d = -d
	}
	return d <= 120
}
