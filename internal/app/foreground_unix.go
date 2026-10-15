//go:build !windows

package app

// allowSetForeground is Windows-only: other systems have no foreground lock for the daemon to need.
func allowSetForeground(int) error { return nil }
