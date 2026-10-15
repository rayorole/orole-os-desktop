package sandbox

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestGuestReportsEachExecToItsHook(t *testing.T) {
	client, server := net.Pipe()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Orole-Token") != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(wire.Health{Version: "test", Ready: true})
		case "/exec":
			var req wire.ExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Command == "fail" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(wire.ExecResult{Stdout: "2\n", ExitCode: 0})
		default:
			http.NotFound(w, r)
		}
	})
	go (&http2.Server{}).ServeConn(server, &http2.ServeConnOpts{Handler: h})
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })

	g, err := NewGuest(client, "tok")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	g.OnExec(func(req wire.ExecRequest, res wire.ExecResult) {
		mu.Lock()
		seen = append(seen, req.Command+"|"+res.Stdout)
		mu.Unlock()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := g.Exec(ctx, wire.ExecRequest{Command: `python -c "print(1+1)"`}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Exec(ctx, wire.ExecRequest{Command: "fail"}); err == nil {
		t.Fatal("a failed exec reported success")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != `python -c "print(1+1)"|2`+"\n" {
		t.Fatalf("hook saw %q, want only the successful exec", seen)
	}
}
