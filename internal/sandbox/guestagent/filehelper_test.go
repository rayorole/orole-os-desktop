package guestagent

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// A helper that cannot start is the agent's fault, not a missing or forbidden file: 500, never 404 or 403.
func TestFileHelperStartFailureIs500(t *testing.T) {
	dir := t.TempDir()
	for name, exe := range map[string]string{
		"missing":   filepath.Join(dir, "no-such-helper"),
		"directory": dir,
	} {
		t.Run(name, func(t *testing.T) {
			a := New(Config{Token: "t", Home: dir, HelperExe: exe})
			_, err := a.readFileAs(t.Context(), filepath.Join(dir, "f.txt"), nil)
			if err == nil {
				t.Fatal("helper started")
			}
			rec := httptest.NewRecorder()
			fileError(rec, err)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status %d (%v), want 500", rec.Code, err)
			}
			werr := a.writeFileAs(t.Context(), filepath.Join(dir, "f.txt"), []byte("x"), 0o644, nil)
			rec = httptest.NewRecorder()
			fileError(rec, werr)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("write status %d (%v), want 500", rec.Code, werr)
			}
		})
	}
}

// The helper's stderr only feeds a 512-byte error message: a helper that floods it must not grow the agent.
func TestFileHelperStderrIsCapped(t *testing.T) {
	w := newHelperStderr()
	chunk := []byte(strings.Repeat("e", 64<<10))
	for range 64 { // 4 MiB
		if n, err := w.Write(chunk); n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v (must accept everything so the helper never blocks)", n, err)
		}
	}
	if got := len(w.String()); got > helperStderrMax {
		t.Fatalf("kept %d bytes of stderr, cap %d", got, helperStderrMax)
	}
	if !strings.HasPrefix(w.String(), "eee") {
		t.Fatal("the start of stderr (the helper's message) was not kept")
	}
}
