package automations

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeWatch struct {
	mu    sync.Mutex
	items []WatchItem
	err   error
	actor string
}

func (f *fakeWatch) Poll(_ context.Context, actor string, _ Watch) ([]WatchItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actor = actor
	return f.items, f.err
}

func (f *fakeWatch) polledAs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.actor
}

func (f *fakeWatch) set(err error, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err, f.items = err, nil
	for _, k := range keys { // newest first, as sources return them
		f.items = append(f.items, WatchItem{Key: k, Data: map[string]any{"sha": k, "branch": "main"}})
	}
}

// pollOnce polls one saved watch automation the way the loop does.
func pollOnce(t *testing.T, e *Engine, id string) error {
	t.Helper()
	snap, err := e.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	it := snap.byID[id]
	if it == nil || it.def.Trigger.Watch == nil {
		t.Fatalf("no watch automation %s", id)
	}
	return e.pollWatch(it, *it.def.Trigger.Watch)
}

// awaitLoopBaseline waits for the engine's watch loop to record its first poll of
// a just-saved watch. Saving wakes the loop, which polls a new watch at once and
// then not again for watchInterval, so the test's own pollOnce calls that follow
// cannot race it. The source must already return the baseline items.
func awaitLoopBaseline(t *testing.T, e *Engine, id string) {
	t.Helper()
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(5 * time.Millisecond) {
		var watch string
		err := e.db.QueryRow(`SELECT watch FROM automation_watch_state WHERE automation_id=?`, id).Scan(&watch)
		if err == nil && watch != "" {
			return
		}
	}
	t.Fatalf("watch loop never polled %s", id)
}

func runKeys(t *testing.T, e *Engine, id string) []string {
	t.Helper()
	runs, err := e.Runs(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(runs) - 1; i >= 0; i-- { // oldest first
		var data struct {
			Topic string `json:"topic"`
			Data  struct {
				Sha string `json:"sha"`
			} `json:"data"`
		}
		_ = json.Unmarshal(runs[i].TriggerData, &data)
		out = append(out, data.Topic+"/"+data.Data.Sha)
	}
	return out
}

func watchDef(filter string, branch string) Definition {
	return Definition{Name: "on push", Enabled: true,
		Trigger: Trigger{Type: "watch", Filter: filter, Watch: &Watch{Source: "github", Kind: "push", Repo: "o/r", Branch: branch}},
		Steps:   []Step{{ID: "n", Type: "approval", Params: mustJSON(map[string]any{"message": "new commit {{ event.data.sha }}"})}}}
}

func TestWatchRunsOncePerNewItemAfterABaseline(t *testing.T) {
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{})
	src := &fakeWatch{}
	e.SetWatchSource("github", src)
	// The source holds the baseline before the save: saving an enabled watch makes
	// the engine's loop poll it right away, and a loop poll of an empty source
	// would make c1 and c2 look new to the test's poll below.
	src.set(nil, "c2", "c1")
	d := saveDef(t, e, watchDef("", "main"))
	awaitLoopBaseline(t, e, d.ID)
	if err := pollOnce(t, e, d.ID); err != nil {
		t.Fatal(err)
	}
	if got := runKeys(t, e, d.ID); len(got) != 0 {
		t.Fatalf("baseline poll ran %v", got)
	}
	if actor := src.polledAs(); actor != "automation:"+d.ID {
		t.Fatalf("polled as %q", actor)
	}
	src.set(nil, "c4", "c3", "c2", "c1")
	if err := pollOnce(t, e, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := pollOnce(t, e, d.ID); err != nil { // nothing new
		t.Fatal(err)
	}
	if got := runKeys(t, e, d.ID); len(got) != 2 || got[0] != "github.push/c3" || got[1] != "github.push/c4" {
		t.Fatalf("runs %v, want c3 then c4 once each", got)
	}

	// Changing what is watched starts a new baseline instead of running the backlog.
	d.Trigger.Watch.Branch = "release"
	d = saveDef(t, e, d)
	src.set(nil, "r9", "c4")
	if err := pollOnce(t, e, d.ID); err != nil {
		t.Fatal(err)
	}
	if got := runKeys(t, e, d.ID); len(got) != 2 {
		t.Fatalf("changed watch ran its backlog: %v", got)
	}
}

func TestWatchFilterAndErrorsAreVisible(t *testing.T) {
	e := openEngine(t, filepath.Join(t.TempDir(), "db"), Options{})
	src := &fakeWatch{}
	e.SetWatchSource("github", src)
	d := saveDef(t, e, watchDef("event.data.sha == 'keep'", "main"))
	// Saving an enabled watch makes the loop poll it once: let that baseline land (from the empty source) so it
	// cannot overlap the test's polls, where a late loop poll overwrote the 401 error below with a success.
	awaitLoopBaseline(t, e, d.ID)
	src.set(nil)
	_ = pollOnce(t, e, d.ID)
	src.set(nil, "keep", "skip")
	_ = pollOnce(t, e, d.ID)
	if got := runKeys(t, e, d.ID); len(got) != 1 || got[0] != "github.push/keep" {
		t.Fatalf("filter: %v", got)
	}

	src.set(errors.New("github: 401 bad credentials"))
	if err := pollOnce(t, e, d.ID); err == nil {
		t.Fatal("poll error swallowed")
	}
	list, err := e.List(context.Background())
	if err != nil || len(list) != 1 || list[0].Watch == nil || list[0].Watch.Error == "" || list[0].Watch.Seen != 2 {
		t.Fatalf("summary: %+v %v", list, err)
	}
	// Recovering keeps the cursor: the items seen before the failure do not run again.
	src.set(nil, "keep", "skip")
	if err := pollOnce(t, e, d.ID); err != nil {
		t.Fatal(err)
	}
	if got := runKeys(t, e, d.ID); len(got) != 1 {
		t.Fatalf("recovery reran items: %v", got)
	}
}

func TestValidateWatch(t *testing.T) {
	cases := []struct {
		w    *Watch
		path string
	}{
		{nil, "trigger.watch"},
		{&Watch{Source: "ftp"}, "trigger.watch.source"},
		{&Watch{Source: "github", Kind: "push", Repo: "not a repo"}, "trigger.watch.repo"},
		{&Watch{Source: "github", Kind: "stars", Repo: "o/r"}, "trigger.watch.kind"},
		{&Watch{Source: "github", Kind: "push", Repo: "o/r", IntervalSec: 5}, "trigger.watch.intervalSec"},
		{&Watch{Source: "mcp", Kind: "deployment", Tool: "list_deployments"}, "trigger.watch.server"},
	}
	for _, c := range cases {
		d := Normalize(Definition{Name: "w", Trigger: Trigger{Type: "watch", Watch: c.w}, Steps: []Step{tool("a")}})
		found := false
		for _, i := range Validate(d) {
			found = found || i.Path == c.path
		}
		if !found {
			t.Errorf("%+v: no issue at %s: %+v", c.w, c.path, Validate(d))
		}
	}
}
