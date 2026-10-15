package sandbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

func TestSetupOutcomeMapsHelperExitCodes(t *testing.T) {
	if r, err := setupOutcome(setupcode.OK); err != nil || r.RebootRequired {
		t.Fatalf("OK: %+v %v", r, err)
	}
	if r, err := setupOutcome(setupcode.RebootRequired); err != nil || !r.RebootRequired {
		t.Fatalf("3010: %+v %v", r, err)
	}
	if _, err := setupOutcome(setupcode.NotElevated); err == nil || !strings.Contains(err.Error(), "administrator") {
		t.Fatalf("740: %v", err)
	}
	if _, err := setupOutcome(setupcode.Failed); err == nil || !strings.Contains(err.Error(), "dism") {
		t.Fatalf("1: %v", err)
	}
}

func TestSetupHelperSitsNextToTheDaemon(t *testing.T) {
	exe := filepath.Join("C:", "Program Files", "Orole OS", "resources", "orole-daemon.exe")
	if got := SetupHelperPath(exe); got != filepath.Join(filepath.Dir(exe), "orole-sandbox-setup.exe") {
		t.Fatal(got)
	}
}

func TestCheckSetupHelperComparesTheEmbeddedHash(t *testing.T) {
	content := []byte("orole-sandbox-setup")
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])
	cases := []struct {
		name    string
		want    string
		allow   bool
		wantErr error
	}{
		{"match", good, false, nil},
		{"match, upper-case pin", strings.ToUpper(good), false, nil},
		{"mismatch", strings.Repeat("0", 64), false, ErrSetupHelperMismatch},
		{"mismatch ignores the dev override", strings.Repeat("0", 64), true, ErrSetupHelperMismatch},
		{"malformed pin", "not-hex", false, ErrSetupHelperMismatch},
		{"empty pin", "", false, ErrSetupHelperUnverified},
		{"empty pin with the dev override", "", true, nil},
	}
	for _, c := range cases {
		err := checkSetupHelper(bytes.NewReader(content), c.want, c.allow)
		if !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "setup helper not verified") {
			t.Errorf("%s: unclear error %q", c.name, err)
		}
	}
}

func TestSetupHelperUnverifiedOverrideIsExplicit(t *testing.T) {
	for _, c := range []struct {
		val  string
		want bool
	}{{"", false}, {"0", false}, {"true", false}, {"1", true}} {
		t.Setenv(SetupHelperUnverifiedEnv, c.val)
		if got := allowUnverifiedSetupHelper(); got != c.want {
			t.Errorf("%s=%q: %v", SetupHelperUnverifiedEnv, c.val, got)
		}
	}
}
