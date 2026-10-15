//go:build linux

package guestagent

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

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

func TestStartPTYRunsShell(t *testing.T) {
	p, err := startPTY(PTYSpec{Argv: []string{"/bin/sh"}, Dir: t.TempDir(), Env: append(os.Environ(), "TERM=xterm-256color"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	io.WriteString(p, "stty size\n")
	readUntil(t, p, "24 80")
	if err := p.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	io.WriteString(p, "stty size; exit 3\n")
	readUntil(t, p, "30 100")
	go io.Copy(io.Discard, p)
	if code, _ := p.Wait(); code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

// A shell that exits while a background job (ignoring SIGHUP) holds the terminal must still end the session with its exit code.
func TestPTYSessionEndsWhenShellExitsWithBackgroundJob(t *testing.T) {
	a := New(Config{Token: "t", Home: t.TempDir(), LoginShell: []string{"/bin/sh", "-c", "trap '' HUP; sleep 30 & echo pid=$!; exit 3"}})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/pty", nil)
	req.Header.Set(wire.TokenHeader, "t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	start := time.Now()
	done := make(chan struct{})
	var out []byte
	go func() { out, _ = io.ReadAll(resp.Body); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session still open after the shell exited")
	}
	// The job ignores SIGHUP and keeps the terminal open, so only the watcher (150 ms after the shell exits) can end this.
	if d := time.Since(start); d < 100*time.Millisecond {
		t.Fatalf("session ended after %v: the background job did not hold the terminal, so the watcher was not exercised", d)
	}
	if got := resp.Trailer.Get(wire.ExitTrailer); got != "3" {
		t.Fatalf("exit trailer = %q, want 3", got)
	}
	m := regexp.MustCompile(`pid=([0-9]+)`).FindSubmatch(out)
	if m == nil {
		t.Fatalf("no background pid in output %q", out)
	}
	pid, _ := strconv.Atoi(string(m[1]))
	for i := 0; ; i++ {
		if gone(pid) {
			break
		}
		if i > 40 {
			t.Fatalf("background job %d still alive after the session ended", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// gone reports whether pid no longer runs (a zombie awaiting a reaper counts as gone).
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return true
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	return i >= 0 && len(s) > i+2 && s[i+2] == 'Z'
}
