//go:build !windows

package sandbox

import "context"

// EnableWHP is Windows-only.
func EnableWHP(context.Context, string, Paths) (SetupResult, error) {
	return SetupResult{}, ErrSetupUnsupported
}
