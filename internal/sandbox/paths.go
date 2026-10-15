package sandbox

import "path/filepath"

// Paths lays out <data>\sandbox. Bot ids must pass checkBotID before they reach these methods.
type Paths struct{ Root string } // <data>\sandbox

func (p Paths) Runtime(version string) string { return filepath.Join(p.Root, "runtime", version) }
func (p Paths) Image(version string) string   { return filepath.Join(p.Root, "images", version) }
func (p Paths) Overlay(botID string) string   { return filepath.Join(p.Root, "bots", botID+".qcow2") }
func (p Paths) Socket(botID string) string    { return filepath.Join(p.Root, "run", botID+".sock") }

func (p Paths) Log(dataDir, botID string) string {
	return filepath.Join(dataDir, "logs", "sandbox-"+botID+".log")
}
