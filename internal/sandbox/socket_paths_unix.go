//go:build !windows

package sandbox

import "path/filepath"

func socketRunDir(root string) string { return filepath.Join(root, "run") }
