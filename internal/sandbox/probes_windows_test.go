//go:build windows

package sandbox

import (
	"context"
	"os"
	"testing"
)

// TestRealHostProbe runs the real unelevated probes on this PC. It changes nothing; it is opt-in
// because its result depends on the machine: OROLE_SANDBOX_PROBE=1 go test -run TestRealHostProbe -v
func TestRealHostProbe(t *testing.T) {
	if os.Getenv("OROLE_SANDBOX_PROBE") != "1" {
		t.Skip("set OROLE_SANDBOX_PROBE=1 to run the real host probe")
	}
	p := probeHost(context.Background(), Paths{Root: t.TempDir()})
	t.Logf("probe=%+v", p)
	t.Logf("availability(runtime missing)=%+v", decideAvailability(p, false))
	t.Logf("availability(runtime ready)=%+v", decideAvailability(p, true))
	// Always query the feature, to document a mismatch with a working partition.
	t.Logf("feature state (queried regardless)=%v (0 unknown, 1 enabled, 2 disabled)", optionalFeatureState(context.Background()))
}
