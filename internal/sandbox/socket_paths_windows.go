//go:build windows

package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Keep transient AF_UNIX endpoints in the user temp directory. Besides shortening
// long profile paths, this avoids profile-directory socket failures on Windows.
// The backend still applies its protected user-and-SYSTEM DACL before listening.
func socketRunDir(root string) string {
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(root))))
	return filepath.Join(os.TempDir(), "orole-"+hex.EncodeToString(sum[:8]), "run")
}
