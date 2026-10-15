package terminals_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

func TestAdoptRegistersAnExternalProcess(t *testing.T) {
	sink := &countSink{}
	m := terminals.NewManager(idleBackend(), sink)
	t.Cleanup(m.Shutdown)
	p := newFakeProc(5, 1) // one 5-byte read, then blocks until closed
	info, err := m.Adopt(p, terminals.AdoptOptions{Title: "Computer", Shell: "orole-guest", Owner: "sandbox:b1", Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if info.Owner != "sandbox:b1" || info.Title != "Computer" || info.Shell != "orole-guest" ||
		info.State != terminals.Running || info.Cols != 80 || info.Rows != 24 || info.Pid != 4242 || info.ID == "" {
		t.Fatalf("info = %+v", info)
	}
	if !eventually(5*time.Second, func() bool { _, b, _, _, _ := sink.snapshot(); return b == 5 }) {
		t.Fatal("adopted output was not delivered")
	}
	if err := m.Write(info.ID, []byte("ls\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := m.Resize(info.ID, 100, 30); err != nil {
		t.Fatalf("resize: %v", err)
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.closed:
	default:
		t.Fatal("Close did not close the adopted process")
	}
}

func TestAdoptRespectsTheSessionLimitAndClosesTheRefusedProcess(t *testing.T) {
	m := terminals.NewManager(idleBackend(), &countSink{})
	t.Cleanup(m.Shutdown)
	for range 64 {
		if _, err := m.Create(terminals.CreateOptions{Shell: "fake"}); err != nil {
			t.Fatal(err)
		}
	}
	p := newFakeProc(1, 0)
	if _, err := m.Adopt(p, terminals.AdoptOptions{Owner: "sandbox:b1"}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("65th session: err = %v, want limit error", err)
	}
	select {
	case <-p.closed:
	default:
		t.Fatal("refused process was left open")
	}
}

func TestInjectAddsOutputWithoutWritingToTheProcess(t *testing.T) {
	sink := &countSink{}
	m := terminals.NewManager(idleBackend(), sink)
	t.Cleanup(m.Shutdown)
	info, err := m.Create(terminals.CreateOptions{Shell: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Inject(info.ID, []byte("$ echo hi\r\n")); err != nil {
		t.Fatal(err)
	}
	if !eventually(5*time.Second, func() bool { _, b, _, _, _ := sink.snapshot(); return b == 11 }) {
		t.Fatal("injected output was not delivered")
	}
	a, err := m.Attach(info.ID)
	if err != nil || string(a.Data) != "$ echo hi\r\n" {
		t.Fatalf("scrollback = %q, %v", a.Data, err)
	}
	if err := m.Inject("missing", []byte("x")); err == nil {
		t.Fatal("inject into an unknown session succeeded")
	}
}
