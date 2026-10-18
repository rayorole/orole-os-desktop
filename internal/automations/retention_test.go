package automations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rayorole/orole-os-desktop/internal/events"
)

// insertRun writes a run and its journal row in one transaction. The engine's
// scheduler prunes concurrently (once at start, on its first tick), so with two
// separate statements a stale run could be deleted before its journal row is
// written, failing that insert on the run_id foreign key.
func insertRun(t *testing.T, e *Engine, d Definition, status string, created, finished int64) string {
	t.Helper()
	raw, _ := json.Marshal(d)
	id := uuid.NewString()
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO automation_runs(id,automation_id,status,trigger_type,trigger_data,chain,definition,created_at,started_at,finished_at) VALUES(?,?,?,'manual','{}','[]',?,?,0,?)`, id, d.ID, status, string(raw), created, finished); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO automation_steps_journal(run_id,step_id,seq,step_type,status,idempotency_key) VALUES(?,?,?,?,?,?)`, id, "s", 0, "notify", "succeeded", id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func countRows(t *testing.T, e *Engine, table, where string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func notifyDef(name string) Definition {
	return Definition{Name: name, Steps: []Step{{ID: "n", Type: "notify", Params: json.RawMessage(`{"title":"Hi","level":"info"}`)}}}
}

func TestPruneKeepsActiveRunsAndHonoursAgeAndCap(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{Clock: clock})
	cfg := e.Config()
	cfg.RetentionDays, cfg.RetentionMaxRuns = 2, 10
	if _, err := e.SetConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	a := saveDef(t, e, notifyDef("busy"))
	b := saveDef(t, e, notifyDef("quiet"))
	now := clock.Now().UnixMilli()
	ago := func(d time.Duration) int64 { return now - d.Milliseconds() }

	// Older than the retention window: every finished status goes, every active one stays.
	var staleTerminal, active []string
	for _, s := range []string{"succeeded", "failed", "canceled", "uncertain"} {
		staleTerminal = append(staleTerminal, insertRun(t, e, a, s, ago(72*time.Hour), ago(71*time.Hour)))
	}
	for _, s := range []string{"queued", "running", "waiting-approval"} {
		active = append(active, insertRun(t, e, a, s, ago(240*time.Hour), 0))
	}
	// Inside the window but beyond the per-automation cap: oldest 5 of 15 go.
	var recent []string
	for i := range 15 {
		recent = append(recent, insertRun(t, e, a, "succeeded", ago(time.Duration(3+i)*time.Hour)-1, ago(time.Duration(2+i)*time.Hour)))
	}
	// Over the cap but finished inside the grace period: kept.
	graced := insertRun(t, e, a, "failed", ago(30*time.Minute), ago(10*time.Minute))
	other := insertRun(t, e, b, "succeeded", ago(5*time.Hour), ago(4*time.Hour))

	// The scheduler also prunes once at start, concurrently, so the returned count is not asserted.
	if _, err := e.Prune(context.Background()); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool { return countRows(t, e, "automation_runs", "id=?", id) == 1 }
	for _, id := range staleTerminal {
		if exists(id) {
			t.Fatalf("stale finished run %s survived", id)
		}
	}
	for _, id := range active {
		if !exists(id) {
			t.Fatalf("active run %s was pruned", id)
		}
	}
	// recent is ordered newest first: indexes 0..9 stay under the cap (the graced
	// run took no slot from them because it is newer than all of them).
	for i, id := range recent {
		// The graced run is newest, so it occupies rank 1 and pushes one more out.
		if want := i < 9; exists(id) != want {
			t.Fatalf("recent[%d] exists=%v want %v", i, !want, want)
		}
	}
	if !exists(graced) || !exists(other) {
		t.Fatal("grace-period or other-automation run was pruned")
	}
	if got := countRows(t, e, "automation_steps_journal", "1=1"); got != len(active)+9+2 {
		t.Fatalf("journal rows %d: pruned runs left journal behind or active runs lost theirs", got)
	}
}

func TestSetConfigRetention(t *testing.T) {
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{})
	if c := e.Config(); c.RetentionDays != DefaultRetentionDays || c.RetentionMaxRuns != DefaultRetentionMaxRuns {
		t.Fatalf("defaults: %+v", c)
	}
	cfg := DefaultConfig()
	cfg.RetentionDays, cfg.RetentionMaxRuns = 0, 0 // a client that predates the fields
	got, err := e.SetConfig(context.Background(), cfg)
	if err != nil || got.RetentionDays != DefaultRetentionDays || got.RetentionMaxRuns != DefaultRetentionMaxRuns {
		t.Fatalf("zero values must select defaults: %+v %v", got, err)
	}
	for _, bad := range [][2]int{{-1, 100}, {MaxRetentionDays + 1, 100}, {7, MinRetentionMaxRuns - 1}, {7, MaxRetentionMaxRuns + 1}} {
		cfg.RetentionDays, cfg.RetentionMaxRuns = bad[0], bad[1]
		if _, err = e.SetConfig(context.Background(), cfg); err == nil {
			t.Fatalf("accepted retention %v", bad)
		}
	}
	cfg.RetentionDays, cfg.RetentionMaxRuns = 7, 50
	if got, err = e.SetConfig(context.Background(), cfg); err != nil || got.RetentionDays != 7 || got.RetentionMaxRuns != 50 {
		t.Fatalf("%+v %v", got, err)
	}
}

type countingClock struct {
	*fakeClock
	after atomic.Int32
}

func (c *countingClock) After(d time.Duration) <-chan time.Time {
	c.after.Add(1)
	return c.fakeClock.After(d)
}

func TestSchedulerUsesOneTimerRegardlessOfEventTraffic(t *testing.T) {
	bus := events.NewBus()
	clock := &countingClock{fakeClock: &fakeClock{now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}}
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{Bus: bus, Clock: clock})
	for i := range 200 {
		bus.Publish(events.Event{Topic: "noise.event"})
		if i%10 == 0 {
			e.wake()
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := clock.after.Load(); n != 1 {
		t.Fatalf("%d timers armed; one pending tick must survive event bursts", n)
	}
	clock.advance(time.Second)
	deadline := time.Now().Add(2 * time.Second)
	for clock.after.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := clock.after.Load(); n != 2 {
		t.Fatalf("timer was not re-armed after firing: %d", n)
	}
}

func TestEventTopicIndex(t *testing.T) {
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{})
	ev := func(name, pattern string, enabled bool) Definition {
		return saveDef(t, e, Definition{Name: name, Enabled: enabled, Trigger: Trigger{Type: "event", Event: pattern}, Steps: []Step{toolStep("call")}})
	}
	ev("b-exact", "github.push", true)
	ev("a-prefix", "github.*", true)
	all := ev("c-all", "*", true)
	ev("d-disabled", "github.push", false)
	ev("e-other", "mail.new", true)
	names := func(topic string) string {
		s, err := e.snapshot(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range s.matching(topic) {
			out = append(out, it.def.Name)
		}
		return strings.Join(out, ",")
	}
	for topic, want := range map[string]string{
		"github.push":  "a-prefix,b-exact,c-all",
		"github":       "a-prefix,c-all",
		"githubx.push": "c-all",
		"mail.new":     "c-all,e-other",
	} {
		if got := names(topic); got != want {
			t.Fatalf("%s matched %q, want %q", topic, got, want)
		}
	}
	// Save must be visible to the next event: disable the catch-all and re-check.
	all.Enabled = false
	saveDef(t, e, all)
	if got := names("mail.new"); got != "e-other" {
		t.Fatalf("stale index after save: %q", got)
	}
	if err := e.Delete(context.Background(), all.ID); err != nil {
		t.Fatal(err)
	}
	if got := names("github.push"); got != "a-prefix,b-exact" {
		t.Fatalf("stale index after delete: %q", got)
	}
}

func TestRunSummariesComeFromMemory(t *testing.T) {
	var mu sync.Mutex
	var seen []Run
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{Notifier: notifyFunc(func(context.Context, NotificationRequest) (any, error) { return true, nil }), Changed: func(r Run) { mu.Lock(); seen = append(seen, r); mu.Unlock() }})
	r := runDef(t, e, Definition{Name: "gate", Steps: []Step{{ID: "ok", Type: "approval", Params: json.RawMessage(`{"message":"go?"}`)}, {ID: "n", Type: "notify", Params: json.RawMessage(`{"title":"Hi","level":"info"}`)}}})
	awaitStatus(t, e, r.ID, "waiting-approval")
	// status() commits the row before applyStatus mirrors it into memory, so the
	// committed status Detail reads can briefly lead the in-memory one.
	live, ok := e.liveRun(r.ID)
	for until := time.Now().Add(2 * time.Second); (!ok || live.Status != "waiting-approval") && time.Now().Before(until); live, ok = e.liveRun(r.ID) {
		time.Sleep(5 * time.Millisecond)
	}
	if !ok || live.Status != "waiting-approval" || live.StartedAt == 0 {
		t.Fatalf("waiting run not tracked: %+v %v", live, ok)
	}
	if err := e.Approve(context.Background(), r.ID, "ok", true, ""); err != nil {
		t.Fatal(err)
	}
	awaitStatus(t, e, r.ID, "succeeded")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := e.liveRun(r.ID); !ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := e.liveRun(r.ID); ok {
		t.Fatal("finished run still tracked")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 3 {
		t.Fatalf("expected status and journal summaries, got %d", len(seen))
	}
	last := seen[len(seen)-1]
	if last.ID != r.ID || last.Status != "succeeded" || last.FinishedAt == 0 || last.Name != "gate" || len(last.Definition.Steps) != 2 {
		t.Fatalf("final summary wrong: %+v", last)
	}
	sawWaiting := false
	for _, s := range seen {
		sawWaiting = sawWaiting || s.Status == "waiting-approval"
	}
	if !sawWaiting {
		t.Fatal("waiting-approval summary was never published")
	}
}

type countingCredentials struct {
	*memoryCredentials
	gets atomic.Int32
}

func (c *countingCredentials) Get(k string) ([]byte, error) {
	c.gets.Add(1)
	return c.memoryCredentials.Get(k)
}

func TestWebhookSecretIsCachedAndRotated(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	store := &countingCredentials{memoryCredentials: &memoryCredentials{m: map[string][]byte{webhookCredential: secret}}}
	clock := &fakeClock{now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{Credentials: store, Clock: clock, Notifier: notifyFunc(func(context.Context, NotificationRequest) (any, error) { return true, nil })})
	d := saveDef(t, e, Definition{Name: "hook", Enabled: true, Trigger: Trigger{Type: "webhook"}, Steps: []Step{{ID: "n", Type: "notify", Params: json.RawMessage(`{"title":"Hi","level":"info"}`)}}})
	h := e.WebhookHandler()
	stamp := strconv.FormatInt(clock.Now().Unix(), 10)
	target := "/automations/" + d.ID
	post := func(key []byte, body string) int {
		w := &recorder{h: http.Header{}}
		r, _ := http.NewRequest(http.MethodPost, target, strings.NewReader(body))
		r.Header.Set("X-Orole-Timestamp", stamp)
		r.Header.Set("X-Orole-Signature", webhookTestSignature(t, key, http.MethodPost, target, stamp, "", body))
		h.ServeHTTP(w, r)
		return w.code
	}
	for i := range 5 {
		if code := post(secret, fmt.Sprintf(`{"n":%d}`, i)); code != http.StatusAccepted {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if n := store.gets.Load(); n != 1 {
		t.Fatalf("credential store read %d times for 5 requests", n)
	}
	if err := e.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	rotated, _ := store.memoryCredentials.Get(webhookCredential)
	if code := post(rotated, `{"after":"rotation"}`); code != http.StatusAccepted {
		t.Fatalf("rotated secret rejected: %d", code)
	}
	if code := post(secret, `{"after":"rotation2"}`); code != http.StatusUnauthorized {
		t.Fatalf("old secret still accepted: %d", code)
	}
}

type recorder struct {
	h    http.Header
	code int
}

func (r *recorder) Header() http.Header { return r.h }
func (r *recorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = 200
	}
	return len(b), nil
}
func (r *recorder) WriteHeader(c int) { r.code = c }

func TestScheduleFiresThroughContinuousEventBurst(t *testing.T) {
	bus := events.NewBus()
	clock := &countingClock{fakeClock: &fakeClock{now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}}
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{Bus: bus, Clock: clock, Notifier: notifyFunc(func(context.Context, NotificationRequest) (any, error) { return true, nil })})
	d := saveDef(t, e, Definition{Name: "tick", Enabled: true, Trigger: Trigger{Type: "schedule", Schedule: "every 1m"}, Steps: []Step{{ID: "n", Type: "notify", Params: json.RawMessage(`{"title":"Tick","level":"info"}`)}}})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // events arrive faster than the 1s tick for the whole test
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				bus.Publish(events.Event{Topic: "noise.event"})
				time.Sleep(time.Millisecond)
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	// Only the timer is advanced (no wake): the schedule must fire from the tick alone.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		clock.advance(time.Minute)
		time.Sleep(20 * time.Millisecond)
		if runs, _ := e.Runs(context.Background(), d.ID); len(runs) > 0 {
			return
		}
	}
	t.Fatal("schedule never fired while events were flowing")
}
