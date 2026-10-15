package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestDeleteWhileRunning(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	g := ensure(t, h, "bot1")
	overlay := h.paths.Overlay("bot1")
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("overlay missing before delete: %v", err)
	}
	errc := make(chan error, 1)
	go func() { _, err := g.Exec(ctx, wire.ExecRequest{Command: "sleep 10s"}); errc <- err }()
	eventually(t, func() bool { return h.active("bot1") == 1 })

	if err := h.m.Delete(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.b.vmFor("bot1").Done():
	default:
		t.Fatal("VM still running after Delete")
	}
	if _, err := os.Stat(overlay); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	if _, found, _ := h.store.Get(ctx, "bot1"); found {
		t.Fatal("row still there")
	}
	if st := h.m.Status("bot1"); st != (Status{BotID: "bot1", State: StateOff}) {
		t.Fatalf("status = %+v", st)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, ErrDeleting) {
			t.Fatalf("in-flight Exec = %v, want ErrDeleting", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Exec never returned")
	}
	if n := h.b.liveCount(); n != 0 {
		t.Fatalf("%d VMs left", n)
	}
}

func TestDeleteWhileBooting(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	h.b.update(func(b *fakeBackend) { b.hold = make(chan struct{}) })
	type result struct {
		g   *Guest
		err error
	}
	ensured := make(chan result, 1)
	go func() { g, err := h.m.Ensure(ctx, "bot1"); ensured <- result{g, err} }()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateStarting })

	deleted := make(chan error, 1)
	go func() { deleted <- h.m.Delete(ctx, "bot1") }()
	select {
	case err := <-deleted:
		t.Fatalf("Delete returned %v while the VM was still booting", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := h.m.Ensure(ctx, "bot1"); !errors.Is(err, ErrDeleting) {
		t.Fatalf("Ensure during Delete = %v, want ErrDeleting", err)
	}
	h.b.update(func(b *fakeBackend) { close(b.hold) })
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	r := <-ensured
	if r.err != nil {
		t.Fatalf("the waiting Ensure = %v; it should get the guest that booted", r.err)
	}
	if _, err := r.g.Health(ctx); !errors.Is(err, ErrDeleting) {
		t.Fatalf("using the deleted guest = %v, want ErrDeleting", err)
	}
	if _, err := os.Stat(h.paths.Overlay("bot1")); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	if n := h.b.liveCount(); n != 0 || h.b.bootCount() != 1 {
		t.Fatalf("live=%d boots=%d, want 0 and 1", n, h.b.bootCount())
	}
}

func TestDeleteWithoutRunningVM(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	ensure(t, h, "bot1")
	h.advance(16 * time.Minute)
	h.m.reapIdle()
	if h.m.Status("bot1").State != StateOff {
		t.Fatal("setup: VM did not stop")
	}
	if err := h.m.Delete(ctx, "bot1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.paths.Overlay("bot1")); !os.IsNotExist(err) {
		t.Fatalf("overlay still there: %v", err)
	}
	if _, found, _ := h.store.Get(ctx, "bot1"); found {
		t.Fatal("row still there")
	}
	if err := h.m.Delete(ctx, "bot1"); err != nil {
		t.Fatalf("second Delete = %v", err)
	}
	if err := h.m.Delete(ctx, "../x"); err == nil {
		t.Fatal("Delete accepted an invalid id")
	}
}

// A tampered record must not make Delete remove a file outside the sandbox directory.
func TestDeleteRefusesOverlayOutsideRoot(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	ensure(t, h, "bot1")
	h.advance(16 * time.Minute)
	h.m.reapIdle()
	outside := filepath.Join(t.TempDir(), "precious.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _, _ := h.store.Get(ctx, "bot1")
	rec.OverlayPath = outside
	if err := h.store.Upsert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	err := h.m.Delete(ctx, "bot1")
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("Delete = %v, want an error about the overlay path being outside the sandbox directory", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("file outside the sandbox directory was removed: %v", statErr)
	}
	if _, statErr := os.Stat(h.paths.Overlay("bot1")); !os.IsNotExist(statErr) {
		t.Fatalf("the bot's own overlay should still be removed: %v", statErr)
	}
}

// After a daemon restart the new manager boots the same bot on the same overlay and keeps the record.
func TestNewManagerReusesOverlay(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	ensure(t, h, "bot1")
	rec, _, _ := h.store.Get(ctx, "bot1")
	const marker = "disk contents written by the first boot"
	if err := os.WriteFile(h.paths.Overlay("bot1"), []byte(marker), 0o600); err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.m.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	h.m = NewManager(h.cfg, h.b, h.store)
	h.advance(time.Hour)
	ensure(t, h, "bot1")
	if got := h.b.spec(1).Overlay; got != h.paths.Overlay("bot1") {
		t.Fatalf("second boot overlay = %q", got)
	}
	if got, err := os.ReadFile(h.paths.Overlay("bot1")); err != nil || string(got) != marker {
		t.Fatalf("overlay after the second boot = %q, %v; want the first boot's contents", got, err)
	}
	again, _, _ := h.store.Get(ctx, "bot1")
	if !again.CreatedAt.Equal(rec.CreatedAt) {
		t.Fatalf("created_at changed: %v -> %v", rec.CreatedAt, again.CreatedAt)
	}
	if n := h.b.liveCount(); n != 1 {
		t.Fatalf("%d VMs running, want 1", n)
	}
}
