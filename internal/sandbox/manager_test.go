package sandbox

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

type harness struct {
	m     *Manager
	b     *fakeBackend
	store *Store
	paths Paths
	cfg   Config

	mu   sync.Mutex
	now  time.Time
	free uint64
}

func newHarness(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	data := t.TempDir()
	h := &harness{
		b:     newFakeBackend(t),
		store: NewStore(testDB(t)),
		paths: Paths{Root: filepath.Join(data, "sandbox")},
		now:   time.Unix(1_800_000_000, 0),
		free:  64 << 30,
	}
	h.cfg = Config{
		Paths: h.paths, MaxRunning: 4, IdleTimeout: 15 * time.Minute, BootTimeout: 5 * time.Second, BusyWait: 200 * time.Millisecond,
		FreeMemory: func() (uint64, error) { h.mu.Lock(); defer h.mu.Unlock(); return h.free, nil },
		Now:        h.clock,
		Image: func() (ImageSet, error) {
			return ImageSet{Version: "v1", Kernel: "k", Initrd: "i", BaseImage: "base.qcow2"}, nil
		},
	}
	if tweak != nil {
		tweak(&h.cfg)
	}
	h.m = NewManager(h.cfg, h.b, h.store)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.m.Shutdown(ctx)
		h.b.killAll()
	})
	return h
}

func (h *harness) clock() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }

func (h *harness) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }

func (h *harness) setFree(n uint64) { h.mu.Lock(); h.free = n; h.mu.Unlock() }

// active is the number of guest unary requests in flight for botID, or -1 without a VM.
func (h *harness) active(botID string) int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if inst := h.m.vms[botID]; inst != nil {
		return inst.active
	}
	return -1
}

func ensure(t *testing.T, h *harness, botID string) *Guest {
	t.Helper()
	g, err := h.m.Ensure(context.Background(), botID)
	if err != nil {
		t.Fatalf("Ensure(%s): %v", botID, err)
	}
	return g
}

func TestEnsureBootsOnceForConcurrentCallers(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.hold = make(chan struct{}) })
	const callers = 8
	guests := make([]*Guest, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() { defer wg.Done(); guests[i], errs[i] = h.m.Ensure(context.Background(), "bot1") }()
	}
	eventually(t, func() bool { return h.b.bootCount() == 1 })
	if st := h.m.Status("bot1"); st.State != StateStarting {
		t.Fatalf("status while booting = %+v", st)
	}
	h.b.update(func(b *fakeBackend) { close(b.hold) })
	wg.Wait()
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if guests[i] != guests[0] {
			t.Fatalf("caller %d got a different guest", i)
		}
	}
	if n := h.b.bootCount(); n != 1 {
		t.Fatalf("boots = %d, want 1", n)
	}
	if st := h.m.Status("bot1"); st.State != StateRunning {
		t.Fatalf("status = %+v, want running", st)
	}
}

func TestEnsureRecordsSandboxAndBootSpec(t *testing.T) {
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	if res, err := g.Exec(context.Background(), wire.ExecRequest{Command: "echo hi"}); err != nil || res.Stdout != "hi\n" {
		t.Fatalf("Exec through the manager = %+v, %v", res, err)
	}
	rec, ok, err := h.store.Get(context.Background(), "bot1")
	if err != nil || !ok {
		t.Fatalf("record: %v, %v", ok, err)
	}
	if rec.ImageVersion != "v1" || rec.OverlayPath != h.paths.Overlay("bot1") || rec.Limits != DefaultLimits() || !rec.CreatedAt.Equal(h.clock()) {
		t.Fatalf("record = %+v", rec)
	}
	spec := h.b.spec(0)
	if spec.Overlay != h.paths.Overlay("bot1") || spec.Kernel != "k" || spec.Initrd != "i" || spec.BaseImage != "base.qcow2" ||
		spec.Limits != DefaultLimits() || spec.LogPath != h.paths.Log(filepath.Dir(h.paths.Root), "bot1") {
		t.Fatalf("spec = %+v", spec)
	}
	if b, err := hex.DecodeString(spec.Token); err != nil || len(b) != 32 {
		t.Fatalf("token %q is not 32 bytes of hex", spec.Token)
	}
}

// An overlay is a qcow2 delta on the base image it was made from, so a bot keeps booting the kernel, initrd
// and base of its recorded image version while those files are still installed, and never boots it against
// another base.
func TestEnsureBootsTheImageVersionTheOverlayWasMadeFrom(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed bool
	}{{"old version installed", true}, {"old version gone", false}} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			old := h.paths.Image("v0")
			if tc.installed {
				if err := os.MkdirAll(old, 0o700); err != nil {
					t.Fatal(err)
				}
				for _, f := range []string{imageKernel, imageInitrd, imageCmdline, imageBase, completeMark} {
					if err := os.WriteFile(filepath.Join(old, f), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			rec := Record{BotID: "bot1", ImageVersion: "v0", OverlayPath: h.paths.Overlay("bot1"), Limits: DefaultLimits(), CreatedAt: h.clock()}
			if err := h.store.Upsert(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			if !tc.installed {
				_, err := h.m.Ensure(context.Background(), "bot1")
				if !errors.Is(err, ErrImageGone) || !errors.Is(err, ErrBootFailed) || !strings.Contains(err.Error(), "v0") {
					t.Fatalf("Ensure = %v, want ErrImageGone naming v0", err)
				}
				if h.b.bootCount() != 0 {
					t.Fatal("booted the overlay against another image version")
				}
			} else {
				ensure(t, h, "bot1")
				spec := h.b.spec(0)
				want := ImageSet{Kernel: filepath.Join(old, imageKernel), Initrd: filepath.Join(old, imageInitrd), BaseImage: filepath.Join(old, imageBase)}
				if spec.Kernel != want.Kernel || spec.Initrd != want.Initrd || spec.BaseImage != want.BaseImage {
					t.Fatalf("spec = %+v, want %+v", spec, want)
				}
			}
			if got, _, _ := h.store.Get(context.Background(), "bot1"); got.ImageVersion != "v0" {
				t.Fatalf("recorded image version changed to %q", got.ImageVersion)
			}
		})
	}
}

func TestEnsureRefusesWhenNotReady(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.avail = Availability{Reason: "whp-disabled"} })
	_, err := h.m.Ensure(context.Background(), "bot1")
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Ensure = %v, want ErrNotReady", err)
	}
	if st := h.m.Status("bot1"); st.State != StateFailed || !strings.Contains(st.Error, "whp-disabled") {
		t.Fatalf("status = %+v", st)
	}
	if h.b.bootCount() != 0 {
		t.Fatal("booted although not ready")
	}
}

func TestEnsureRefusesWithoutRuntime(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.Image = func() (ImageSet, error) { return ImageSet{}, errors.New("no runtime installed") }
	})
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Ensure = %v, want ErrNotReady", err)
	}
}

func TestEnsureRefusesOnLowMemory(t *testing.T) {
	h := newHarness(t, nil)
	h.setFree((2048+1024)<<20 - 1)
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrLowMemory) {
		t.Fatalf("Ensure = %v, want ErrLowMemory", err)
	}
	if h.b.bootCount() != 0 {
		t.Fatal("booted with too little memory")
	}
	h.setFree((2048 + 1024) << 20)
	ensure(t, h, "bot1")
}

func TestBootFailsWhenGuestNeverReady(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BootTimeout = 300 * time.Millisecond })
	h.b.update(func(b *fakeBackend) { b.notReady = true })
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrBootFailed) {
		t.Fatalf("Ensure = %v, want ErrBootFailed", err)
	}
	vm := h.b.vm(0)
	select {
	case <-vm.Done():
	default:
		t.Fatal("VM left running after a boot timeout")
	}
	if vm.kills.Load() == 0 {
		t.Fatal("VM was not killed")
	}
	log, err := os.ReadFile(h.paths.Log(filepath.Dir(h.paths.Root), "bot1"))
	if err != nil || !strings.Contains(string(log), "failed to start") {
		t.Fatalf("log = %q, %v", log, err)
	}
	if strings.Contains(string(log), h.b.spec(0).Token) {
		t.Fatal("the guest token reached the log")
	}
	if st := h.m.Status("bot1"); st.State != StateFailed {
		t.Fatalf("status = %+v", st)
	}
	if n := h.b.bootCount(); n != 1 {
		t.Fatalf("boots = %d: a failed boot must not retry by itself", n)
	}
	h.b.update(func(b *fakeBackend) { b.notReady = false })
	ensure(t, h, "bot1")
	if n := h.b.bootCount(); n != 2 {
		t.Fatalf("boots = %d, want 2", n)
	}
}

func TestBootFailsWhenBackendFails(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.bootErr = errors.New("whpx: injection failed") })
	if _, err := h.m.Ensure(context.Background(), "bot1"); !errors.Is(err, ErrBootFailed) {
		t.Fatalf("Ensure = %v, want ErrBootFailed", err)
	}
	log, _ := os.ReadFile(h.paths.Log(filepath.Dir(h.paths.Root), "bot1"))
	if !strings.Contains(string(log), "injection failed") {
		t.Fatalf("log = %q", log)
	}
}

func TestEnsureRejectsInvalidBotID(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []string{"", "../evil", `a\b`} {
		if _, err := h.m.Ensure(context.Background(), id); err == nil {
			t.Errorf("Ensure(%q) = nil error", id)
		}
	}
	if h.b.bootCount() != 0 {
		t.Fatal("booted for an invalid id")
	}
}

func TestStatusEvents(t *testing.T) {
	h := newHarness(t, nil)
	var mu sync.Mutex
	var seen []State
	unsubscribe := h.m.Subscribe(func(s Status) {
		if s.BotID == "bot1" {
			mu.Lock()
			seen = append(seen, s.State)
			mu.Unlock()
		}
	})
	ensure(t, h, "bot1")
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) >= 2 })
	mu.Lock()
	if seen[0] != StateStarting || seen[1] != StateRunning {
		t.Fatalf("events = %v, want [starting running]", seen)
	}
	mu.Unlock()
	unsubscribe()
	h.b.vmFor("bot1").crash()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateOff })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("events after unsubscribe: %v", seen)
	}
}

func TestCrashFailsInFlightCallAndNextCallBoots(t *testing.T) {
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	errc := make(chan error, 1)
	go func() {
		_, err := g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 10s"})
		errc <- err
	}()
	eventually(t, func() bool { return h.active("bot1") == 1 })
	h.b.vmFor("bot1").crash()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrCrashed) {
			t.Fatalf("in-flight Exec = %v, want ErrCrashed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Exec never returned")
	}
	if st := h.m.Status("bot1"); st.State != StateOff || st.Error != ErrCrashed.Error() {
		t.Fatalf("status = %+v", st)
	}
	g2 := ensure(t, h, "bot1")
	if g2 == g || h.b.bootCount() != 2 {
		t.Fatalf("after a crash: new guest=%v boots=%d", g2 != g, h.b.bootCount())
	}
	if h.b.spec(0).Token == h.b.spec(1).Token {
		t.Fatal("the token was reused across boots")
	}
	if st := h.m.Status("bot1"); st.State != StateRunning || st.Error != "" {
		t.Fatalf("status after reboot = %+v", st)
	}
}

func TestCapEvictsLeastRecentlyUsedIdleVM(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 2 })
	ensure(t, h, "a")
	h.advance(time.Minute)
	ensure(t, h, "b")
	h.advance(time.Minute)
	ensure(t, h, "a") // a is now more recent than b
	h.advance(time.Minute)
	ensure(t, h, "c")
	if st := h.m.Status("b"); st.State != StateOff {
		t.Fatalf("b = %+v, want off (evicted)", st)
	}
	for _, id := range []string{"a", "c"} {
		if st := h.m.Status(id); st.State != StateRunning {
			t.Fatalf("%s = %+v, want running", id, st)
		}
	}
	select {
	case <-h.b.vmFor("b").Done():
	default:
		t.Fatal("b's VM still running")
	}
	if n := h.b.bootCount(); n != 3 {
		t.Fatalf("boots = %d, want 3", n)
	}
}

func TestCapReturnsBusyWhenEveryVMIsBusy(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 1; c.BusyWait = 200 * time.Millisecond })
	g := ensure(t, h, "a")
	go func() { _, _ = g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 3s"}) }()
	eventually(t, func() bool { return h.active("a") == 1 })
	start := time.Now()
	if _, err := h.m.Ensure(context.Background(), "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Ensure(b) = %v, want ErrBusy", err)
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("gave up after %v, want the 200ms busy wait", d)
	}
	if st := h.m.Status("a"); st.State != StateRunning || h.b.bootCount() != 1 {
		t.Fatalf("a = %+v, boots = %d", st, h.b.bootCount())
	}
}

func TestCapWaitGetsSlotWhenBusyVMFinishes(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 1; c.BusyWait = 10 * time.Second })
	g := ensure(t, h, "a")
	done := make(chan error, 1)
	go func() {
		_, err := g.Exec(context.Background(), wire.ExecRequest{Command: "sleep 300ms"})
		done <- err
	}()
	eventually(t, func() bool { return h.active("a") == 1 })
	ensure(t, h, "b")
	if err := <-done; err != nil {
		t.Fatalf("a's command was cut off: %v", err)
	}
	if h.m.Status("a").State != StateOff || h.m.Status("b").State != StateRunning {
		t.Fatalf("a = %+v, b = %+v", h.m.Status("a"), h.m.Status("b"))
	}
}

func TestShutdownStopsEveryVM(t *testing.T) {
	h := newHarness(t, nil)
	for _, id := range []string{"a", "b", "c"} {
		ensure(t, h, id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if n := h.b.liveCount(); n != 0 {
		t.Fatalf("%d VMs still running after Shutdown", n)
	}
	if _, err := h.m.Ensure(context.Background(), "a"); !errors.Is(err, errClosed) {
		t.Fatalf("Ensure after Shutdown = %v, want errClosed", err)
	}
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown = %v", err)
	}
}

func TestShutdownDuringBoot(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.hold = make(chan struct{}) })
	ensured := make(chan error, 1)
	go func() { _, err := h.m.Ensure(context.Background(), "bot1"); ensured <- err }()
	eventually(t, func() bool { return h.m.Status("bot1").State == StateStarting })
	shut := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shut <- h.m.Shutdown(ctx)
	}()
	eventually(t, func() bool { h.m.mu.Lock(); defer h.m.mu.Unlock(); return h.m.closed })
	h.b.update(func(b *fakeBackend) { close(b.hold) })
	if err := <-shut; err != nil {
		t.Fatal(err)
	}
	if err := <-ensured; !errors.Is(err, errClosed) {
		t.Fatalf("Ensure = %v, want errClosed", err)
	}
	if n := h.b.liveCount(); n != 0 {
		t.Fatalf("%d VMs left after Shutdown during boot", n)
	}
}

func TestHungGuestIsReplacedOnNextEnsure(t *testing.T) {
	oldIdle, oldPing := guestReadIdleTimeout, guestPingTimeout
	guestReadIdleTimeout, guestPingTimeout = 100*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { guestReadIdleTimeout, guestPingTimeout = oldIdle, oldPing })
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	h.b.vmFor("bot1").stall()
	if _, err := g.Health(context.Background()); err == nil {
		t.Fatal("Health to a hung guest succeeded")
	}
	g2 := ensure(t, h, "bot1")
	if g2 == g || h.b.bootCount() != 2 {
		t.Fatalf("new guest=%v boots=%d, want a fresh boot", g2 != g, h.b.bootCount())
	}
	if h.b.vm(0).kills.Load() == 0 {
		t.Fatal("the hung VM was not killed")
	}
	if hl, err := g2.Health(context.Background()); err != nil || !hl.Ready {
		t.Fatalf("Health on the new guest = %+v, %v", hl, err)
	}
}

func TestDeadChannelCallsAreNotActivity(t *testing.T) {
	h := newHarness(t, nil)
	g := ensure(t, h, "bot1")
	h.m.mu.Lock()
	before := h.m.vms["bot1"].lastUsed
	h.m.mu.Unlock()
	_ = g.cc.Close()
	h.advance(time.Minute)
	if _, err := g.Health(context.Background()); err == nil {
		t.Fatal("Health on a closed channel succeeded")
	}
	h.m.mu.Lock()
	after := h.m.vms["bot1"].lastUsed
	h.m.mu.Unlock()
	if !after.Equal(before) {
		t.Fatalf("lastUsed moved from %v to %v on a dead channel", before, after)
	}
}

func TestShutdownCancelsBootInProgress(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.BootTimeout = 30 * time.Second })
	h.b.update(func(b *fakeBackend) { b.notReady = true })
	ensured := make(chan error, 1)
	go func() { _, err := h.m.Ensure(context.Background(), "bot1"); ensured <- err }()
	eventually(t, func() bool { return h.b.bootCount() == 1 && h.b.vmFor("bot1") != nil })
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Shutdown took %v: the boot was not cancelled", d)
	}
	if err := <-ensured; !errors.Is(err, errClosed) {
		t.Fatalf("Ensure = %v, want errClosed", err)
	}
	if vm := h.b.vm(0); vm.kills.Load() == 0 || h.b.liveCount() != 0 {
		t.Fatalf("fake VM not killed (kills=%d, live=%d)", vm.kills.Load(), h.b.liveCount())
	}
}

func TestShutdownPublishesStopping(t *testing.T) {
	h := newHarness(t, nil)
	ensure(t, h, "bot1")
	var mu sync.Mutex
	var seen []State
	h.m.Subscribe(func(s Status) { mu.Lock(); seen = append(seen, s.State); mu.Unlock() })
	if err := h.m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != StateStopping || seen[len(seen)-1] != StateOff {
		t.Fatalf("events = %v, want stopping ... off", seen)
	}
}

// A VM that is stopping still holds a QEMU process, so it counts against MaxRunning.
func TestStoppingVMCountsAgainstCap(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MaxRunning = 1; c.BusyWait = 100 * time.Millisecond })
	ensure(t, h, "a")
	h.m.mu.Lock()
	inst := h.m.vms["a"]
	inst.state = StateStopping
	h.m.mu.Unlock()
	if _, err := h.m.Ensure(context.Background(), "b"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Ensure(b) while a stops = %v, want ErrBusy", err)
	}
	if n := h.b.bootCount(); n != 1 {
		t.Fatalf("boots = %d, want 1: a second QEMU must not start while one stops", n)
	}
	h.m.mu.Lock()
	inst.state = StateRunning
	h.m.mu.Unlock()
}

// Text from the guest/VM goes into the log quoted, so it cannot forge log lines.
func TestBootFailureLogQuotesGuestText(t *testing.T) {
	h := newHarness(t, nil)
	h.b.update(func(b *fakeBackend) { b.bootErr = errors.New("boom\n2026-01-01T00:00:00Z orole: forged") })
	if _, err := h.m.Ensure(context.Background(), "bot1"); err == nil {
		t.Fatal("want a boot error")
	}
	data, err := os.ReadFile(h.paths.Log(filepath.Dir(h.paths.Root), "bot1"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(strings.TrimSpace(string(data)), "\n"); lines != 0 {
		t.Fatalf("log has %d extra lines:\n%s", lines, data)
	}
}
