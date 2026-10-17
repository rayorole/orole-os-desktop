//go:build windows

package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsSocketsAreIsolatedByProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("long-profile-", 12))
	a := (Paths{Root: root}).Socket("bot")
	if err := CheckSocketPath(a); err != nil {
		t.Fatal(err)
	}
	if a == (Paths{Root: root + "-other"}).Socket("bot") {
		t.Fatal("different profiles share a socket")
	}
	if a != (Paths{Root: strings.ToUpper(root)}).Socket("bot") {
		t.Fatal("case variants of the same Windows profile use different sockets")
	}
}
