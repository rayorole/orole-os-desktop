package sandbox

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
)

func TestMain(m *testing.M) { agenttest.Main(m) }

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readUntil(t *testing.T, r io.Reader, want string) {
	t.Helper()
	found := make(chan struct{})
	go func() {
		var got []byte
		buf := make([]byte, 1024)
		for {
			n, err := r.Read(buf)
			got = append(got, buf[:n]...)
			if bytes.Contains(got, []byte(want)) {
				close(found)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case <-found:
	case <-time.After(5 * time.Second):
		t.Fatalf("never read %q", want)
	}
}

// scratchHome is a guest home outside t.TempDir: helper processes killed at the end of a test may still hold it as
// their working directory for a moment, which would make TempDir's cleanup fail on Windows.
func scratchHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "orole-guest-home-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
