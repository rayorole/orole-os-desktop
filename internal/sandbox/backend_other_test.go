//go:build !windows

package sandbox

import (
	"context"
	"errors"
	"testing"
)

func TestHostBackendIsNotImplementedOffWindows(t *testing.T) {
	b := NewHostBackend(NewRuntime(RuntimeConfig{Paths: Paths{Root: t.TempDir()}}), Paths{Root: t.TempDir()})
	if a := b.Availability(context.Background()); a.Ready || a.Reason != "unsupported-os" {
		t.Fatalf("%+v", a)
	}
	if _, err := b.Boot(context.Background(), BootSpec{}); !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v", err)
	}
}
