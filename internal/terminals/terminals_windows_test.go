//go:build windows

package terminals_test

import (
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

type capSink struct {
	mu      sync.Mutex
	out     strings.Builder
	changed []terminals.Info
}

func (c *capSink) Output(_ string, _ uint64, d []byte) { c.mu.Lock(); c.out.Write(d); c.mu.Unlock() }
func (c *capSink) Changed(i terminals.Info) {
	c.mu.Lock()
	c.changed = append(c.changed, i)
	c.mu.Unlock()
}
func (c *capSink) text() string { c.mu.Lock(); defer c.mu.Unlock(); return c.out.String() }

func waitFor(t *testing.T, f func() bool, what string) {
	t.Helper()
	waitWithin(t, 10*time.Second, f, what)
}

func waitWithin(t *testing.T, d time.Duration, f func() bool, what string) {
	t.Helper()
	for until := time.Now().Add(d); time.Now().Before(until); time.Sleep(100 * time.Millisecond) {
		if f() {
			return
		}
	}
	if !f() {
		t.Fatalf("timeout waiting for %s", what)
	}
}

// prompt matches a PowerShell ("PS C:\dir>") or cmd ("C:\dir>") prompt.
var prompt = regexp.MustCompile(`[A-Za-z]:\\[^\r\n\a\x1b>]*>`)

func prompts(c *capSink) int { return len(prompt.FindAllString(c.text(), -1)) }

// shellStartup bounds how long a shell may take to show its first prompt. A cold
// powershell.exe on a CI runner busy with `go test -race ./...` has printed no
// prompt at all after 12 s; once a shell is up, the tests' own waits stay at 10 s.
const shellStartup = 60 * time.Second

// awaitPrompt waits for the shell's first prompt.
func awaitPrompt(t *testing.T, c *capSink) {
	t.Helper()
	waitWithin(t, shellStartup, func() bool { return prompts(c) > 0 }, "first prompt")
}

func TestShellsInteractive(t *testing.T) {
	for _, id := range []string{"powershell", "cmd"} {
		t.Run(id, func(t *testing.T) {
			sink := &capSink{}
			m := terminals.NewManager(terminals.NewBackend(), sink)
			defer m.Shutdown()
			info, err := m.Create(terminals.CreateOptions{Shell: id, Cols: 100, Rows: 30})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if t.Failed() {
					t.Logf("%q", sink.text())
				}
			}()
			// Unicode + interactive input + exit code reporting. The line is typed at
			// once, while the shell may still be starting: the console buffers it, so
			// its echo can take as long as the shell's startup.
			if err := m.Write(info.ID, []byte("echo héllo-✓-orole\r")); err != nil {
				t.Fatal(err)
			}
			waitWithin(t, shellStartup, func() bool { return strings.Contains(sink.text(), "héllo-✓-orole") }, "echo output")
			if err := m.Resize(info.ID, 80, 24); err != nil {
				t.Fatal(err)
			}
			// Reconnect: scrollback must contain earlier output.
			att, err := m.Attach(info.ID)
			if err != nil || !strings.Contains(string(att.Data), "héllo-✓-orole") {
				t.Fatalf("attach: %v %q", err, att.Data)
			}
			if err := m.Write(info.ID, []byte("exit 7\r")); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool {
				for _, i := range m.List() {
					if i.State == terminals.Exited && i.ExitCode != nil && *i.ExitCode == 7 {
						return true
					}
				}
				return false
			}, "exit code 7")
		})
	}
}

func TestCtrlCInterruptsForegroundProcess(t *testing.T) {
	sink := &capSink{}
	m := terminals.NewManager(terminals.NewBackend(), sink)
	defer m.Shutdown()
	info, err := m.Create(terminals.CreateOptions{Shell: "powershell"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			t.Logf("%q", sink.text())
		}
	}()
	awaitPrompt(t, sink)
	if err := m.Write(info.ID, []byte("Start-Sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	// While PSReadLine still owns the console (reading or just accepting the line)
	// 0x03 is queued as a key instead of raising Ctrl-C, and the sleep then runs
	// its full 30 s. Repeat Ctrl-C until a prompt comes back; the elapsed time
	// below shows the sleep was interrupted rather than finished.
	before := prompts(sink)
	for i := 0; prompts(sink) <= before; i++ {
		if i == 10 {
			t.Fatalf("no prompt after %d Ctrl-C", i)
		}
		if err := m.Write(info.ID, []byte{0x03}); err != nil {
			t.Fatal(err)
		}
		for until := time.Now().Add(time.Second); prompts(sink) <= before && time.Now().Before(until); {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if err := m.Write(info.ID, []byte("echo AFTER-INTERRUPT\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return strings.Count(sink.text(), "AFTER-INTERRUPT") >= 2 }, "prompt responsive after Ctrl-C")
	if elapsed := time.Since(started); elapsed >= 25*time.Second {
		t.Fatalf("prompt came back after %v: Start-Sleep 30 was not interrupted", elapsed)
	}
}
