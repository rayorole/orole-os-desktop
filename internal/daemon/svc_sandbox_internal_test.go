package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
)

func TestSandboxUserActionsRefuseBotsAndAutomations(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		ok   bool
	}{
		{"desktop user", context.Background(), true},
		{"bot", agents.WithActor(context.Background(), "bot:b1"), false},
		{"automation", events.WithChain(context.Background(), []string{"auto-1"}), false},
	}
	for _, c := range cases {
		err := requireDesktopUser(c.ctx, "turn on Windows Hypervisor Platform")
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v", c.name, err)
		}
		var ie *ipc.Error
		if err != nil && (!errors.As(err, &ie) || ie.Code != ipc.CodeDenied) {
			t.Errorf("%s: want a denied ipc error, got %v", c.name, err)
		}
	}
}

// bootBlocker is a backend whose boot waits until release is closed, then fails.
type bootBlocker struct {
	booting chan struct{}
	release chan struct{}
}

func (b *bootBlocker) Availability(context.Context) sandbox.Availability {
	return sandbox.Availability{Ready: true}
}

func (b *bootBlocker) Boot(ctx context.Context, _ sandbox.BootSpec) (sandbox.VM, error) {
	close(b.booting)
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil, errors.New("boot released")
}

func TestArchivingABotWhoseComputerBootsDoesNotWaitForTheBoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROLE_HOME", dir)
	defer UseMemoryLLMCredentials()()
	d, err := New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer d.cleanup(nopCloser{}) // what Run does on exit: stops the services, the sandbox and the database
	paths := sandbox.Paths{Root: filepath.Join(dir, "sandbox")}
	b := &bootBlocker{booting: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-b.release:
		default:
			close(b.release)
		}
	}()
	d.Sandbox = sandbox.NewManager(sandbox.Config{Paths: paths, BootTimeout: time.Minute, Now: time.Now,
		Image: func() (sandbox.ImageSet, error) { return sandbox.ImageSet{Version: "0.1.0"}, nil }}, b, d.sandboxStore)

	const bot = "booting-bot"
	go func() { _, _ = d.Sandbox.Ensure(context.Background(), bot) }()
	<-b.booting // the row is written before the boot starts
	overlay := paths.Overlay(bot)
	if err := os.WriteFile(overlay, []byte("QFI"), 0o600); err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	d.deleteBotComputer(bot)
	if took := time.Since(began); took > time.Second {
		t.Fatalf("archiving waited %v for the boot", took)
	}
	if _, err := os.Stat(overlay); err != nil {
		t.Fatalf("the overlay went before the boot ended: %v", err)
	}

	close(b.release) // the boot fails; the background delete goes on
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, found, err := d.sandboxStore.Get(context.Background(), bot)
		_, statErr := os.Stat(overlay)
		if err == nil && !found && os.IsNotExist(statErr) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not deleted: row %v (%v), overlay %v", found, err, statErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSandboxBackgroundWorkIsRefusedAfterShutdown(t *testing.T) {
	d := &Daemon{}
	d.sandboxLife, d.sandboxStop = context.WithCancel(context.Background())
	d.shutdownSandbox()
	if d.sandboxGo(func() { t.Error("ran after shutdown") }) {
		t.Fatal("sandboxGo accepted work after shutdown")
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// A second delete while one is in progress (archive, then the start sweep, or two archives) is not a failure:
// the first one finishes the job, so only it is audited.
func TestASecondDeleteWhileOneRunsIsNotAuditedAsFailed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OROLE_HOME", dir)
	defer UseMemoryLLMCredentials()()
	d, err := New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer d.cleanup(nopCloser{})
	paths := sandbox.Paths{Root: filepath.Join(dir, "sandbox")}
	b := &bootBlocker{booting: make(chan struct{}), release: make(chan struct{})}
	released := false
	defer func() {
		if !released {
			close(b.release)
		}
	}()
	d.Sandbox = sandbox.NewManager(sandbox.Config{Paths: paths, BootTimeout: time.Minute, Now: time.Now,
		Image: func() (sandbox.ImageSet, error) { return sandbox.ImageSet{Version: "0.1.0"}, nil }}, b, d.sandboxStore)

	const bot = "twice-deleted"
	go func() { _, _ = d.Sandbox.Ensure(context.Background(), bot) }()
	<-b.booting
	first := make(chan struct{})
	go func() { defer close(first); d.deleteBotComputerNow(bot, "Bot archived") }() // waits for the boot
	deadline := time.Now().Add(5 * time.Second)
	deleting := func() bool { // Ensure fails at once while a delete runs, and otherwise waits for the boot
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := d.Sandbox.Ensure(ctx, bot)
		return errors.Is(err, sandbox.ErrDeleting)
	}
	for !deleting() {
		if time.Now().After(deadline) {
			t.Fatal("the first delete never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.deleteBotComputerNow(bot, "Bot archived") // the second one, while the first waits

	close(b.release)
	released = true
	<-first
	rows, err := d.DB.Query(`SELECT outcome FROM audit_events WHERE action='sandbox.delete' AND target=?`, bot)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var outcomes []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			t.Fatal(err)
		}
		outcomes = append(outcomes, o)
	}
	if len(outcomes) != 1 || outcomes[0] != "allowed" {
		t.Fatalf("audit outcomes %q, want one allowed delete", outcomes)
	}
}
