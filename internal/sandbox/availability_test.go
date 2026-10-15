package sandbox

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDecideAvailability(t *testing.T) {
	fail := errors.New("WHvCreatePartition: HRESULT 0x80370102")
	cases := []struct {
		name    string
		probe   hostProbe
		runtime bool
		ready   bool
		reason  string
	}{
		{"old windows", hostProbe{Build: 18363, PartitionErr: fail}, true, false, "unsupported-os"},
		{"ready", hostProbe{Build: 22000}, true, true, ""},
		{"partition works, runtime missing", hostProbe{Build: 22000}, false, false, "runtime-missing"},
		// The optional feature reads as disabled while the Hyper-V role provides WHP: a working
		// partition is the truth, the feature state only explains a failure.
		{"feature disabled but partition works", hostProbe{Build: 22000, Feature: featureDisabled}, true, true, ""},
		{"feature disabled, partition works, runtime missing", hostProbe{Build: 22000, Feature: featureDisabled}, false, false, "runtime-missing"},
		{"partition works despite stale reboot flag", hostProbe{Build: 22000, Feature: featureDisabled, RebootPending: true}, true, true, ""},
		{"enabled, waiting for reboot", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled, RebootPending: true}, true, false, "reboot-required"},
		{"feature off", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureDisabled, HypervisorPresent: true, FirmwareVirt: true}, true, false, "whp-disabled"},
		{"feature state unknown", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureUnknown, FirmwareVirt: true}, true, false, "whp-disabled"},
		{"enabled but BIOS off", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled}, true, false, "virtualization-off"},
		{"enabled, hypervisor not launched", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled, FirmwareVirt: true}, true, false, "virtualization-off"},
		{"enabled, hypervisor up, partition refused", hostProbe{Build: 22000, PartitionErr: fail, Feature: featureEnabled, FirmwareVirt: true, HypervisorPresent: true}, true, false, "virtualization-off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := decideAvailability(c.probe, c.runtime)
			if a.Ready != c.ready || a.Reason != c.reason {
				t.Fatalf("got %+v, want ready=%v reason=%q", a, c.ready, c.reason)
			}
			if !a.Ready && a.Detail == "" {
				t.Fatal("a reason without a detail for the setup screen")
			}
		})
	}
}

func TestDecideAvailabilityNamesTheFirmwareWhenItIsOff(t *testing.T) {
	a := decideAvailability(hostProbe{Build: 22000, PartitionErr: errors.New("x"), Feature: featureEnabled}, true)
	if a.Detail == decideAvailability(hostProbe{Build: 22000, PartitionErr: errors.New("x"), Feature: featureEnabled, FirmwareVirt: true}, true).Detail {
		t.Fatal("BIOS-off and hypervisor-not-running give the same advice")
	}
}

func TestParseInstallState(t *testing.T) {
	cases := map[string]featureState{"1\r\n": featureEnabled, "2\r\n": featureDisabled, "3": featureDisabled, "": featureUnknown, "garbage": featureUnknown}
	for out, want := range cases {
		if got := parseInstallState([]byte(out), nil); got != want {
			t.Errorf("%q: got %v want %v", out, got, want)
		}
	}
	if parseInstallState([]byte("1"), errors.New("powershell failed")) != featureUnknown {
		t.Error("an error must give featureUnknown")
	}
}

func TestRebootMarkerOnlyCountsForTheCurrentBoot(t *testing.T) {
	boot := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	m := rebootMarker(boot)
	if !rebootMarkerActive(m, boot.Add(30*time.Second)) {
		t.Fatal("marker from this boot is not active")
	}
	if rebootMarkerActive(m, boot.Add(3*time.Hour)) {
		t.Fatal("marker survives a restart")
	}
	if rebootMarkerActive([]byte("not a number"), boot) || rebootMarkerActive(nil, boot) {
		t.Fatal("unreadable marker counts")
	}
}

func TestTheRuntimeMissingDetailStatesThePinnedDownloadSize(t *testing.T) {
	p := Pins{QEMU: Asset{Size: 62_624_686}, Image: []Asset{{Size: 1_522_466_816}, {Size: 44_354_934}}}
	if got := p.DownloadSize(); got != 1_629_446_436 {
		t.Fatalf("DownloadSize %d", got)
	}
	if got := downloadSizeText(p.DownloadSize()); got != "about 1.6 GB" {
		t.Fatalf("text %q", got)
	}
	if got := downloadSizeText(0); got != "" {
		t.Fatalf("no pins: %q", got)
	}
	a := decideAvailability(hostProbe{Build: 22000}, false)
	want := "(QEMU and the Linux image, " + downloadSizeText(DefaultPins.DownloadSize()) + ")"
	if !DefaultPins.Complete() {
		want = "(QEMU and the Linux image)"
	}
	if !strings.Contains(a.Detail, want) {
		t.Fatalf("detail %q lacks %q", a.Detail, want)
	}
}
