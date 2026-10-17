//go:build windows

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// Reuses installed immutable runtime files but boots a new disk in an isolated test
// directory. No bot configuration, chat history or existing VM disk is changed.
func TestLocalInstalledRuntimeBoot(t *testing.T) {
	runtimeRoot := os.Getenv("OROLE_SANDBOX_LOCAL_RUNTIME_ROOT")
	if runtimeRoot == "" {
		t.Skip("set OROLE_SANDBOX_LOCAL_RUNTIME_ROOT to an installed sandbox root for a live VM smoke test")
	}
	root := shortRoot(t)
	paths := Paths{Root: filepath.Join(root, "sandbox")}
	rt := NewRuntime(RuntimeConfig{Paths: Paths{Root: runtimeRoot}, Pins: DefaultPins})
	m := NewManager(Config{Paths: paths, Image: rt.Image, BootTimeout: 90 * time.Second}, NewHostBackend(rt, paths), NewStore(testDB(t)))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	g, e := m.Ensure(ctx, "diag-bot")
	if e != nil {
		t.Fatal(e)
	}
	r, e := g.Exec(ctx, wire.ExecRequest{Command: "pwd"})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("VM pwd: exit=%d stdout=%q", r.ExitCode, r.Stdout)
	if r.ExitCode != 0 {
		t.Fail()
	}
}
