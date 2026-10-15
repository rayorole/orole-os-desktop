package guestagent_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent/agenttest"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestMain(m *testing.M) { agenttest.Main(m) }

const token = "secret-token"

func newAgent(t *testing.T, mod func(*guestagent.Config)) (*guestagent.Agent, string) {
	t.Helper()
	home := t.TempDir()
	cfg := guestagent.Config{Token: token, Version: "1.2.3", Home: home, Shell: agenttest.Shell()}
	if mod != nil {
		mod(&cfg)
	}
	return guestagent.New(cfg), home
}

func call(t *testing.T, a *guestagent.Agent, method, path, tok string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "http://guest"+path, rd)
	if tok != "" {
		req.Header.Set(wire.TokenHeader, tok)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestTokenRequired(t *testing.T) {
	a, _ := newAgent(t, nil)
	for _, tc := range []struct {
		tok  string
		want int
	}{{"", 401}, {"wrong", 401}, {token + "x", 401}, {token, 200}} {
		if rec := call(t, a, "GET", "/health", tc.tok, nil); rec.Code != tc.want {
			t.Errorf("token %q: status %d, want %d", tc.tok, rec.Code, tc.want)
		}
	}
	open, _ := newAgent(t, func(c *guestagent.Config) { c.Token = "" })
	if rec := call(t, open, "GET", "/health", "", nil); rec.Code != 401 {
		t.Errorf("agent without a token accepted a request: %d", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	ready := false
	a, _ := newAgent(t, func(c *guestagent.Config) { c.Ready = func() bool { return ready } })
	if h := decode[wire.Health](t, call(t, a, "GET", "/health", token, nil)); h.Ready || h.Version != "1.2.3" {
		t.Fatalf("health = %+v", h)
	}
	ready = true
	if h := decode[wire.Health](t, call(t, a, "GET", "/health", token, nil)); !h.Ready {
		t.Fatalf("health = %+v, want ready", h)
	}
	if rec := call(t, a, "POST", "/health", token, nil); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /health = %d", rec.Code)
	}
}

func TestExec(t *testing.T) {
	a, home := newAgent(t, nil)
	if err := os.Mkdir(filepath.Join(home, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := func(req wire.ExecRequest) wire.ExecResult {
		return decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, req))
	}
	if r := exec(wire.ExecRequest{Command: "echo hi"}); r.Stdout != "hi\n" || r.ExitCode != 0 || r.Truncated || r.TimedOut {
		t.Errorf("echo = %+v", r)
	}
	if r := exec(wire.ExecRequest{Command: "err oops"}); r.Stderr != "oops\n" || r.Stdout != "" {
		t.Errorf("err = %+v", r)
	}
	if r := exec(wire.ExecRequest{Command: "exit 3"}); r.ExitCode != 3 {
		t.Errorf("exit 3 = %+v", r)
	}
	if r := exec(wire.ExecRequest{Command: "env HOME"}); strings.TrimSpace(r.Stdout) != home {
		t.Errorf("HOME = %q, want %q", r.Stdout, home)
	}
	r := exec(wire.ExecRequest{Command: "pwd", Cwd: "work"})
	got, err1 := os.Stat(strings.TrimSpace(r.Stdout))
	want, err2 := os.Stat(filepath.Join(home, "work"))
	if err1 != nil || err2 != nil || !os.SameFile(got, want) {
		t.Errorf("relative cwd ran in %q, want %s/work", r.Stdout, home)
	}
	if rec := call(t, a, "POST", "/exec", token, wire.ExecRequest{Command: "  "}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty command = %d", rec.Code)
	}
}

func TestExecCapsOutput(t *testing.T) {
	a, _ := newAgent(t, nil)
	r := decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, wire.ExecRequest{Command: "flood 3000000"}))
	if len(r.Stdout) != wire.MaxExecOutput || !r.Truncated || r.ExitCode != 0 {
		t.Fatalf("flood: %d bytes, truncated=%v, exit=%d", len(r.Stdout), r.Truncated, r.ExitCode)
	}
}

func TestExecTimeout(t *testing.T) {
	a, _ := newAgent(t, nil)
	start := time.Now()
	r := decode[wire.ExecResult](t, call(t, a, "POST", "/exec", token, wire.ExecRequest{Command: "forever", Timeout: 1}))
	if !r.TimedOut || r.ExitCode != -1 || !r.Truncated || len(r.Stdout) > wire.MaxExecOutput {
		t.Fatalf("forever: timedOut=%v exit=%d truncated=%v len=%d", r.TimedOut, r.ExitCode, r.Truncated, len(r.Stdout))
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("a 1 s timeout took %v", d)
	}
}

func TestFiles(t *testing.T) {
	a, home := newAgent(t, nil)
	w := decode[wire.WriteFileResult](t, call(t, a, "POST", "/files/write", token, wire.WriteFileRequest{Path: "notes/a.txt", Content: []byte("hello"), Mode: 0o600}))
	if w.Bytes != 5 {
		t.Fatalf("write = %+v", w)
	}
	onDisk, err := os.ReadFile(filepath.Join(home, "notes", "a.txt"))
	if err != nil || string(onDisk) != "hello" {
		t.Fatalf("file on disk = %q, %v", onDisk, err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(home, "notes", "a.txt"))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", st.Mode().Perm())
		}
	}
	r := decode[wire.ReadFileResult](t, call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: "notes/a.txt"}))
	if string(r.Content) != "hello" || r.Truncated {
		t.Fatalf("read = %+v", r)
	}
	abs := filepath.Join(home, "notes", "a.txt")
	if r := decode[wire.ReadFileResult](t, call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: abs})); string(r.Content) != "hello" {
		t.Fatalf("absolute read = %+v", r)
	}
	for _, tc := range []struct {
		path string
		want int
	}{{"missing.txt", 404}, {"notes", 400}, {"", 400}} {
		if rec := call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: tc.path}); rec.Code != tc.want {
			t.Errorf("read %q = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}
}

func TestReadFileCap(t *testing.T) {
	a, home := newAgent(t, nil)
	if err := os.WriteFile(filepath.Join(home, "big.bin"), bytes.Repeat([]byte{0}, 5<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	r := decode[wire.ReadFileResult](t, call(t, a, "POST", "/files/read", token, wire.ReadFileRequest{Path: "big.bin"}))
	if len(r.Content) != wire.MaxReadFile || !r.Truncated {
		t.Fatalf("read %d bytes, truncated=%v", len(r.Content), r.Truncated)
	}
}

func TestWriteFileLimit(t *testing.T) {
	a, home := newAgent(t, nil)
	rec := call(t, a, "POST", "/files/write", token, wire.WriteFileRequest{Path: "big.bin", Content: make([]byte, wire.MaxWriteFile+1)})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized write = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(home, "big.bin")); !os.IsNotExist(err) {
		t.Fatalf("oversized write left a file: %v", err)
	}
	if rec := call(t, a, "POST", "/files/write", token, wire.WriteFileRequest{Path: "ok.bin", Content: make([]byte, wire.MaxWriteFile)}); rec.Code != 200 {
		t.Fatalf("16 MiB write = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestScreenAndBrowserNotAvailableYet(t *testing.T) {
	a, _ := newAgent(t, nil)
	for _, p := range []string{"/screen", "/browser"} {
		if rec := call(t, a, "POST", p, token, nil); rec.Code != http.StatusNotImplemented {
			t.Errorf("%s = %d, want 501", p, rec.Code)
		}
	}
}

func TestPowerOff(t *testing.T) {
	called := make(chan struct{})
	a, _ := newAgent(t, func(c *guestagent.Config) { c.PowerOff = func() error { close(called); return nil } })
	if rec := call(t, a, "POST", "/poweroff", token, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("poweroff = %d", rec.Code)
	}
	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("PowerOff never called")
	}
	none, _ := newAgent(t, nil)
	if rec := call(t, none, "POST", "/poweroff", token, nil); rec.Code != http.StatusNotImplemented {
		t.Fatalf("poweroff without a hook = %d", rec.Code)
	}
}

func TestPTYResizeErrors(t *testing.T) {
	a, _ := newAgent(t, nil)
	if rec := call(t, a, "POST", "/pty/resize", token, wire.ResizeRequest{ID: "nope", Cols: 80, Rows: 24}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown terminal = %d", rec.Code)
	}
	if rec := call(t, a, "POST", "/pty/resize", token, wire.ResizeRequest{ID: "nope", Cols: 0, Rows: 24}); rec.Code != http.StatusBadRequest {
		t.Errorf("zero columns = %d", rec.Code)
	}
}
