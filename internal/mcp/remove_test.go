package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/credentials"
	"github.com/rayorole/orole-os-desktop/internal/database"
	"github.com/rayorole/orole-os-desktop/internal/mcp/mcptest"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/migrations"
)

// slowCreds is a credential store whose Delete can hang or fail, like a wedged OS credential manager.
type slowCreds struct {
	*credentials.Memory
	block   chan struct{} // Delete waits for this to close; nil = no wait
	err     error
	deletes atomic.Int32
}

func (s *slowCreds) Delete(key string) error {
	s.deletes.Add(1)
	if s.block != nil {
		<-s.block
	}
	if s.err != nil {
		return s.err
	}
	return s.Memory.Delete(key)
}

// eventRecorder keeps event payloads, which the shared recorder drops.
type eventRecorder struct {
	recorder
	mu2  sync.Mutex
	data map[string][]any
}

func (r *eventRecorder) Publish(event string, data any) {
	r.recorder.Publish(event, data)
	r.mu2.Lock()
	if r.data == nil {
		r.data = map[string][]any{}
	}
	r.data[event] = append(r.data[event], data)
	r.mu2.Unlock()
}

func (r *eventRecorder) payloads(event string) []any {
	r.mu2.Lock()
	defer r.mu2.Unlock()
	return append([]any(nil), r.data[event]...)
}

type removeEnv struct {
	m     *Manager
	db    *database.DB
	creds *slowCreds
	rec   *eventRecorder
	// url of an MCP server whose session DELETE hangs until release is closed.
	url     string
	release chan struct{}
	deletes atomic.Int32
}

func newRemoveEnv(t *testing.T, hangDelete bool) *removeEnv {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "orole.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	env := &removeEnv{db: db, creds: &slowCreds{Memory: credentials.NewMemory()}, rec: &eventRecorder{recorder: recorder{auth: make(chan AuthStart, 4)}}, release: make(chan struct{})}
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(env.release) }) }
	t.Cleanup(releaseAll)

	srv := mcptest.New(t, mcptest.Options{OpenMCP: true, Setup: mcptest.DefaultTools})
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			env.deletes.Add(1)
			if hangDelete {
				select {
				case <-env.release:
				case <-r.Context().Done():
				}
				return
			}
		}
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	env.url = front.URL + strings.TrimPrefix(srv.MCPURL, srv.URL)

	env.m = New(Options{DB: db.DB, Creds: env.creds, Pub: env.rec, Policy: policy.New(db.DB, env.rec, nil),
		BackoffBase: 20 * time.Millisecond, BackoffMax: 100 * time.Millisecond, ConnectTimeout: 5 * time.Second, CallTimeout: 5 * time.Second})
	env.m.teardownTimeout = 300 * time.Millisecond
	if err := env.m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Registered last so it runs first: free anything still hung before Close waits on it.
	t.Cleanup(env.m.Close)
	t.Cleanup(releaseAll)
	return env
}

func (e *removeEnv) addConnected(t *testing.T) ServerState {
	t.Helper()
	st, err := e.m.Add(context.Background(), AddParams{Name: "Notes", URL: e.url, AuthKind: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cur, err := e.m.Get(context.Background(), st.ID); err == nil && cur.Status == StatusConnected {
			return cur
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server never connected")
	return st
}

// timed runs Remove and fails if it takes longer than limit.
func (e *removeEnv) timedRemove(t *testing.T, id string, limit time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- e.m.Remove(context.Background(), id) }()
	select {
	case err := <-done:
		if d := time.Since(start); d > limit {
			t.Fatalf("Remove took %s, want under %s", d, limit)
		}
		return err
	case <-time.After(limit):
		t.Fatalf("Remove still blocked after %s", limit)
		return nil
	}
}

func (e *removeEnv) listed(t *testing.T, id string) bool {
	t.Helper()
	list, err := e.m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A server that accepts the session-closing DELETE and never answers used to hold Remove (and the
// confirmation dialog behind it) for the SDK's close timeout; the registry delete never ran if the
// 30 s IPC deadline hit first.
func TestRemoveDoesNotWaitForHungSessionClose(t *testing.T) {
	e := newRemoveEnv(t, true)
	st := e.addConnected(t)
	if err := e.timedRemove(t, st.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if e.listed(t, st.ID) {
		t.Fatal("server still registered after Remove returned")
	}
	if !e.rec.seen("mcp.removed") {
		t.Fatal("mcp.removed not published")
	}
	waitFor(t, "session DELETE to be attempted", func() bool { return e.deletes.Load() > 0 })
	if _, err := e.m.Get(context.Background(), st.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Remove: %v", err)
	}
}

// A credential store that stalls (or a token refresh holding the session lock) must not hold Remove.
func TestRemoveDoesNotWaitForCredentialStore(t *testing.T) {
	e := newRemoveEnv(t, false)
	st := e.addConnected(t)
	e.creds.block = make(chan struct{})
	t.Cleanup(func() { close(e.creds.block) })
	if err := e.timedRemove(t, st.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	if e.listed(t, st.ID) {
		t.Fatal("server still registered after Remove returned")
	}
	// The stall outlasts the teardown timeout: that is reported instead of swallowed.
	waitFor(t, "cleanup failure event", func() bool { return len(e.rec.payloads("mcp.cleanup.failed")) == 1 })
}

func TestRemoveReportsFailedCredentialCleanup(t *testing.T) {
	e := newRemoveEnv(t, false)
	st := e.addConnected(t)
	e.creds.err = errors.New("credential manager unavailable")
	if err := e.timedRemove(t, st.ID, time.Second); err != nil {
		t.Fatalf("Remove must succeed once the registration is gone: %v", err)
	}
	waitFor(t, "cleanup failure event", func() bool { return len(e.rec.payloads("mcp.cleanup.failed")) == 1 })
	got, ok := e.rec.payloads("mcp.cleanup.failed")[0].(CleanupFailed)
	if !ok || got.ServerID != st.ID || got.Name != "Notes" || !strings.Contains(got.Error, "credential manager unavailable") {
		t.Fatalf("cleanup event = %#v", e.rec.payloads("mcp.cleanup.failed")[0])
	}
	var outcome string
	if err := e.db.QueryRow(`SELECT outcome FROM audit_events WHERE action='mcp.server.cleanup' AND target=?`, "mcp/"+st.ID).Scan(&outcome); err != nil || outcome != "error" {
		t.Fatalf("cleanup audit outcome = %q (%v)", outcome, err)
	}
}

func TestRemoveCleansUpInBackground(t *testing.T) {
	e := newRemoveEnv(t, false)
	st := e.addConnected(t)
	if err := e.m.authSession(st.ID).Set(&credBlob{AccessToken: "tok", Kind: "bearer"}); err != nil {
		t.Fatal(err)
	}
	if err := e.timedRemove(t, st.ID, time.Second); err != nil {
		t.Fatal(err)
	}
	e.m.cleanup.Wait()
	if keys := e.creds.Keys(); len(keys) != 0 {
		t.Fatalf("credentials left behind: %v", keys)
	}
	if n := len(e.rec.payloads("mcp.cleanup.failed")); n != 0 {
		t.Fatalf("clean teardown reported %d failures", n)
	}
	if e.deletes.Load() == 0 {
		t.Fatal("session was never closed")
	}
}

// If the registry delete fails nothing may have changed: still registered, still connected,
// credentials intact. Removing the connection first left a registered server with no connection.
func TestRemoveFailureLeavesServerConnected(t *testing.T) {
	e := newRemoveEnv(t, false)
	st := e.addConnected(t)
	if err := e.m.authSession(st.ID).Set(&credBlob{AccessToken: "tok", Kind: "bearer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Exec(`CREATE TRIGGER no_remove BEFORE DELETE ON mcp_servers BEGIN SELECT RAISE(ABORT, 'locked'); END`); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Remove(context.Background(), st.ID); err == nil {
		t.Fatal("Remove succeeded although the registry delete failed")
	}
	cur, err := e.m.Get(context.Background(), st.ID)
	if err != nil || cur.Status != StatusConnected {
		t.Fatalf("after failed Remove: %+v %v", cur, err)
	}
	if len(e.creds.Keys()) == 0 {
		t.Fatal("credentials deleted by a failed Remove")
	}
	if e.rec.seen("mcp.removed") {
		t.Fatal("mcp.removed published for a failed Remove")
	}
}
