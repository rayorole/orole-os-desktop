package sandbox

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/setupcode"
)

// SetupResult is what turning on Windows Hypervisor Platform did.
type SetupResult struct {
	RebootRequired bool `json:"rebootRequired"`
}

// SetupState is what the setup screen shows while (and after) Windows Hypervisor Platform is turned on.
type SetupState struct {
	Running        bool   `json:"running"`
	RebootRequired bool   `json:"rebootRequired"`
	Error          string `json:"error,omitempty"`
}

var (
	ErrSetupDeclined      = errors.New("sandbox: Windows did not get permission to turn on Windows Hypervisor Platform")
	ErrSetupUnsupported   = errors.New("sandbox: setup is only available on Windows")
	ErrSetupHelperMissing = errors.New("sandbox: orole-sandbox-setup.exe is missing next to the daemon; reinstall Orole")
)

// SetupHelperSHA256 is the SHA-256 (hex) of the orole-sandbox-setup.exe built with this daemon. The per-user
// install folder is writable without elevation, so EnableWHP refuses to start a helper that does not match it.
// scripts/build.ps1 and scripts/build-dev-daemon.ps1 build the helper first and set this with
// -ldflags "-X github.com/rayorole/orole-os-desktop/internal/sandbox.SetupHelperSHA256=<hex>".
var SetupHelperSHA256 string

// SetupHelperUnverifiedEnv, set to "1", lets a daemon built without SetupHelperSHA256 (go run, go test) start
// whatever helper sits next to it. Development only: it never overrides a mismatch.
const SetupHelperUnverifiedEnv = "OROLE_SANDBOX_SETUP_UNVERIFIED"

// maxSetupHelperBytes bounds what is hashed; the helper is a few MB.
const maxSetupHelperBytes = 256 << 20

var (
	ErrSetupHelperUnverified = errors.New("sandbox: setup helper not verified: this daemon was built without the helper's SHA-256 (build it with scripts/build.ps1, or set " + SetupHelperUnverifiedEnv + "=1 while developing)")
	ErrSetupHelperLink       = errors.New("sandbox: setup helper not verified: orole-sandbox-setup.exe next to the daemon is a link, not the file this build shipped; reinstall Orole")
	ErrSetupHelperMismatch   = errors.New("sandbox: setup helper not verified: orole-sandbox-setup.exe next to the daemon is not the one this build shipped; reinstall Orole")
)

func allowUnverifiedSetupHelper() bool { return os.Getenv(SetupHelperUnverifiedEnv) == "1" }

// checkSetupHelper hashes the helper and compares it with want, the embedded hash. An empty want is refused
// unless allowUnverified (the explicit dev override) is set.
func checkSetupHelper(r io.Reader, want string, allowUnverified bool) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		if allowUnverified {
			return nil
		}
		return ErrSetupHelperUnverified
	}
	pin, err := hex.DecodeString(want)
	if err != nil || len(pin) != sha256.Size {
		return ErrSetupHelperMismatch
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r, maxSetupHelperBytes+1))
	if err != nil {
		return fmt.Errorf("sandbox: setup helper not verified: %w", err)
	}
	if n > maxSetupHelperBytes || subtle.ConstantTimeCompare(h.Sum(nil), pin) != 1 {
		return ErrSetupHelperMismatch
	}
	return nil
}

// SetupHelperPath is where the installer puts orole-sandbox-setup.exe: next to orole-daemon.exe.
func SetupHelperPath(daemonExe string) string {
	return filepath.Join(filepath.Dir(daemonExe), "orole-sandbox-setup.exe")
}

func setupOutcome(code uint32) (SetupResult, error) {
	switch code {
	case setupcode.OK:
		return SetupResult{}, nil
	case setupcode.RebootRequired:
		return SetupResult{RebootRequired: true}, nil
	case setupcode.NotElevated:
		return SetupResult{}, errors.New("sandbox: the setup helper did not run as administrator")
	default:
		return SetupResult{}, fmt.Errorf("sandbox: turning on Windows Hypervisor Platform failed (code %d); run `dism /online /enable-feature /featurename:HypervisorPlatform /all` in an administrator terminal to see why", code)
	}
}
