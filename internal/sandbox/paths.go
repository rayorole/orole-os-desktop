package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// Paths lays out <data>\sandbox. Bot ids must pass checkBotID before they reach these methods.
type Paths struct{ Root string } // <data>\sandbox

func (p Paths) Runtime(version string) string { return filepath.Join(p.Root, "runtime", version) }
func (p Paths) Image(version string) string   { return filepath.Join(p.Root, "images", version) }
func (p Paths) Overlay(botID string) string   { return filepath.Join(p.Root, "bots", botID+".qcow2") }

// MaxSocketPath is the longest AF_UNIX socket path Windows and Linux accept (sun_path is 108 bytes
// including the terminating NUL).
const MaxSocketPath = 107

// Socket is the AF_UNIX socket the VM's virtio-serial chardev connects to. The file name is a hash of
// the bot id, not the id itself, so the full path stays under MaxSocketPath for ordinary data folders.
func (p Paths) Socket(botID string) string {
	sum := sha256.Sum256([]byte(botID))
	return filepath.Join(socketRunDir(p.Root), hex.EncodeToString(sum[:8])+".sock")
}

// Token is the file that hands the per-boot guest token to QEMU (fw_cfg), next to the bot's socket. It exists
// only while QEMU starts.
func (p Paths) Token(botID string) string {
	return strings.TrimSuffix(p.Socket(botID), ".sock") + ".tok"
}

// CheckSocketPath returns a readable error when path is too long to bind as an AF_UNIX socket.
func CheckSocketPath(path string) error {
	if len(path) > MaxSocketPath {
		return fmt.Errorf("sandbox: the data folder path is too long for the computer's channel (%d characters, at most %d): %s",
			len(path), MaxSocketPath, path)
	}
	return nil
}

func (p Paths) Log(dataDir, botID string) string {
	return filepath.Join(dataDir, "logs", "sandbox-"+botID+".log")
}
