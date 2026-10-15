package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/chat/tools"
	"github.com/rayorole/orole-os-desktop/internal/database"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/notifications"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/migrations"
)

// fakeGuest is a running guest. hang makes every call block until its context ends.
type fakeGuest struct {
	mu    sync.Mutex
	calls int
	hang  bool
}

func (g *fakeGuest) hit(ctx context.Context) error {
	g.mu.Lock()
	g.calls++
	hang := g.hang
	g.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (g *fakeGuest) count() int { g.mu.Lock(); defer g.mu.Unlock(); return g.calls }

func (g *fakeGuest) Exec(ctx context.Context, _ wire.ExecRequest) (wire.ExecResult, error) {
	return wire.ExecResult{Stdout: "ok"}, g.hit(ctx)
}
func (g *fakeGuest) ReadFile(ctx context.Context, _ string) (wire.ReadFileResult, error) {
	return wire.ReadFileResult{Content: []byte("hi")}, g.hit(ctx)
}
func (g *fakeGuest) WriteFile(ctx context.Context, _ string, c []byte) (wire.WriteFileResult, error) {
	return wire.WriteFileResult{Bytes: len(c)}, g.hit(ctx)
}
func (g *fakeGuest) Browser(ctx context.Context, _ wire.BrowserRequest) (wire.BrowserResult, error) {
	return wire.BrowserResult{Title: "T", URL: "https://example.com/"}, g.hit(ctx)
}

// fakeHost is the sandbox manager: one guest for every bot, a running flag and the takeover lock.
type fakeHost struct {
	mu        sync.Mutex
	guest     *fakeGuest
	state     sandbox.State
	taken     map[string]bool
	ensures   int
	ensureErr error
	ensureCtx func(ctx context.Context) // sees Ensure's context
	takenHook func()                    // runs inside TakenOver
}

func newFakeHost() *fakeHost {
	return &fakeHost{guest: &fakeGuest{}, state: sandbox.StateRunning, taken: map[string]bool{}}
}

func (h *fakeHost) Ensure(ctx context.Context, _ string) (computerGuest, error) {
	h.mu.Lock()
	h.ensures++
	err, see := h.ensureErr, h.ensureCtx
	h.mu.Unlock()
	if see != nil {
		see(ctx)
	}
	if err != nil {
		return nil, err
	}
	return h.guest, nil
}

func (h *fakeHost) Status(botID string) sandbox.Status {
	h.mu.Lock()
	defer h.mu.Unlock()
	return sandbox.Status{BotID: botID, State: h.state, TakenOver: h.taken[botID]}
}

func (h *fakeHost) TakeOver(botID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != sandbox.StateRunning {
		return errors.New("not running")
	}
	h.taken[botID] = true
	return nil
}

func (h *fakeHost) Release(botID string) { h.mu.Lock(); delete(h.taken, botID); h.mu.Unlock() }

func (h *fakeHost) TakenOver(botID string) bool {
	if h.takenHook != nil {
		h.takenHook()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.taken[botID]
}

func (h *fakeHost) held(botID string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.taken[botID] }
func (h *fakeHost) ensureCount() int       { h.mu.Lock(); defer h.mu.Unlock(); return h.ensures }

func computerBot(id, access string) chat.Bot {
	return chat.Bot{ID: id, Name: "Scout", Config: chat.BotConfig{Computer: true, ToolAccess: access}}
}

func computerRun(b chat.Bot) chat.RunContext {
	return chat.RunContext{BotID: b.ID, Bot: b, Actor: "bot:" + b.ID}
}

// Review focus: the bot's tool access level decides through the real auto-approve hook. Reads run at every level
// (policy default), writes only at write and all, and a stored deny rule beats "all".
func TestComputerToolsFollowBotToolAccess(t *testing.T) {
	db, err := database.Open(filepath.Join(t.TempDir(), "orole.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	d := &Daemon{DB: db}
	levels := []string{chat.ToolAccessAsk, chat.ToolAccessRead, chat.ToolAccessWrite, chat.ToolAccessAll}
	bots := fakeBots{}
	for _, lvl := range levels {
		bots[lvl] = computerBot(lvl, lvl)
	}
	pol := policy.New(db.DB, nil, nil)
	pol.SetAutoApprove(d.botAutoApproveHook(bots))
	calls := []struct {
		browser bool
		name    string
		args    string
		write   bool
	}{
		{false, tools.ComputerReadFileName, `{"path":"notes.txt"}`, false},
		{true, tools.BrowserReadName, `{}`, false},
		{false, tools.ComputerRunName, `{"command":"ls"}`, true},
		{false, tools.ComputerWriteFileName, `{"path":"a.txt","content":"x"}`, true},
		{true, tools.BrowserClickName, `{"target":"text=Next"}`, true},
		{true, tools.BrowserOpenName, `{"url":"https://example.com/"}`, true},
	}
	tool := func(rt tools.ComputerRuntime, browser bool) chat.Tool {
		if browser {
			return tools.Browser{Runtime: rt, Policy: pol}
		}
		return tools.Computer{Runtime: rt, Policy: pol}
	}
	// A prompt cannot be answered in a test: WithoutApprovals turns "ask" into an immediate refusal.
	ctx := policy.WithoutApprovals(context.Background())
	for _, lvl := range levels {
		for _, c := range calls {
			t.Run(lvl+"/"+c.name, func(t *testing.T) {
				host := newFakeHost()
				rt := computerRuntime{d: d, host: host, bots: bots}
				res, err := tool(rt, c.browser).Call(ctx, computerRun(bots[lvl]), c.name, json.RawMessage(c.args))
				want := !c.write || lvl == chat.ToolAccessWrite || lvl == chat.ToolAccessAll
				if ran := host.guest.count() == 1; err != nil || ran != want || res.IsError == want {
					t.Fatalf("ran=%v want %v: %+v %v", ran, want, res, err)
				}
			})
		}
	}
	if _, err := pol.AddRule(context.Background(), policy.Rule{Actor: "bot:all", Action: "computer.exec", Resource: "*", Decision: policy.Deny}); err != nil {
		t.Fatal(err)
	}
	host := newFakeHost()
	res, err := tools.Computer{Runtime: computerRuntime{d: d, host: host, bots: bots}, Policy: pol}.Call(ctx, computerRun(bots[chat.ToolAccessAll]), tools.ComputerRunName, json.RawMessage(`{"command":"ls"}`))
	if err != nil || !res.IsError || host.guest.count() != 0 {
		t.Fatalf("a stored deny must win: %+v %v", res, err)
	}
}

// Review focus: starting the VM is bounded by the manager's boot timeout and busy wait plus slack, and once it runs
// the guest call gets its own GuestCallTimeout, so a hung guest returns in that time, not the start budget's.
func TestComputerRuntimeBoundsStartAndGuestCalls(t *testing.T) {
	if computerStartTimeout != tools.ComputerStartTimeout {
		t.Fatalf("start timeout %v, tools expect %v", computerStartTimeout, tools.ComputerStartTimeout)
	}
	host := newFakeHost()
	var startLeft time.Duration
	host.ensureCtx = func(ctx context.Context) {
		if dl, ok := ctx.Deadline(); ok {
			startLeft = time.Until(dl)
		}
	}
	host.guest.hang = true
	rt := computerRuntime{d: &Daemon{}, host: host}
	const guestTimeout = 100 * time.Millisecond
	calls := map[string]func(ctx context.Context) error{
		"exec": func(ctx context.Context) error {
			_, err := rt.Exec(ctx, "b1", wire.ExecRequest{Command: "sleep 999"})
			return err
		},
		"read": func(ctx context.Context) error { _, err := rt.ReadFile(ctx, "b1", "a"); return err },
		"write": func(ctx context.Context) error {
			_, err := rt.WriteFile(ctx, "b1", "a", []byte("x"))
			return err
		},
		"browser": func(ctx context.Context) error {
			_, err := rt.Browser(ctx, "b1", wire.BrowserRequest{Action: "read"})
			return err
		},
	}
	for name, call := range calls {
		startLeft = 0
		ctx := tools.WithGuestCallTimeout(context.Background(), guestTimeout)
		res := make(chan error, 1)
		go func() { res <- call(ctx) }()
		select {
		case err := <-res:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: a hung guest call did not return within its guest call timeout", name)
		}
		if startLeft <= computerStartTimeout-5*time.Second || startLeft > computerStartTimeout {
			t.Errorf("%s: Ensure deadline in %v, want about %v", name, startLeft, computerStartTimeout)
		}
	}
	// A context that did not come from a computer tool keeps its own deadline for the guest call.
	host.guest.hang = false
	if _, err := rt.Exec(context.Background(), "b1", wire.ExecRequest{Command: "ls"}); err != nil {
		t.Fatal(err)
	}
}

// Review focus: an archived or missing bot gets a clear refusal before anything boots.
func TestComputerToolsRefuseArchivedOrMissingBots(t *testing.T) {
	host := newFakeHost()
	b := computerBot("b1", chat.ToolAccessAll)
	d := &Daemon{}
	d.handoffs().AskTimeout = 50 * time.Millisecond           // a takeover that wrongly starts fails fast
	rt := computerRuntime{d: d, host: host, bots: fakeBots{}} // b1 archived: the chat store no longer lists it
	pol := &allowAll{}
	ctx := context.Background()
	for _, tc := range []struct {
		tool chat.Tool
		name string
		args string
	}{
		{tools.Computer{Runtime: rt, Policy: pol}, tools.ComputerRunName, `{"command":"ls"}`},
		{tools.Browser{Runtime: rt, Policy: pol}, tools.BrowserReadName, `{}`},
		{tools.Takeover{Runtime: rt}, tools.TakeoverName, `{"reason":"Log in"}`},
	} {
		res, err := tc.tool.Call(ctx, computerRun(b), tc.name, json.RawMessage(tc.args))
		if err != nil || !res.IsError || !strings.Contains(res.Text, "no longer exist") {
			t.Errorf("%s: %+v %v", tc.name, res, err)
		}
	}
	if host.ensureCount() != 0 || host.guest.count() != 0 {
		t.Fatalf("an archived bot's computer was started: %d ensures", host.ensureCount())
	}
	if s := d.handoffs().State("b1"); s.Pending {
		t.Fatal("no takeover may be pending")
	}
	// A lookup that fails for another reason is not mistaken for an archived bot, and still does not boot.
	rt.bots = failingBots{}
	if _, err := rt.Exec(ctx, "b1", wire.ExecRequest{Command: "ls"}); err == nil || errors.Is(err, tools.ErrComputerGone) || host.ensureCount() != 0 {
		t.Fatalf("lookup failure: %v", err)
	}
}

type failingBots struct{}

func (failingBots) Bot(context.Context, string) (chat.Bot, error) {
	return chat.Bot{}, errors.New("database is locked")
}

type allowAll struct{}

func (*allowAll) Require(context.Context, policy.Request) error { return nil }

func waitComputer(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type takeoverOut struct {
	res chat.ToolResult
	err error
}

// askTakeover runs ask_user_to_take_over for b1 through the daemon's runtime in the background.
func askTakeover(ctx context.Context, d *Daemon, host *fakeHost) <-chan takeoverOut {
	done := make(chan takeoverOut, 1)
	b := computerBot("b1", chat.ToolAccessAsk)
	rt := computerRuntime{d: d, host: host, bots: fakeBots{"b1": b}}
	go func() {
		res, err := tools.Takeover{Runtime: rt}.Call(ctx, computerRun(b), tools.TakeoverName, json.RawMessage(`{"reason":"Log in to example.com"}`))
		done <- takeoverOut{res, err}
	}()
	return done
}

// recvTakeover is the tool's result, or a failure once the turn has waited 5 seconds too long.
func recvTakeover(t *testing.T, done <-chan takeoverOut) takeoverOut {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("ask_user_to_take_over did not return")
		return takeoverOut{}
	}
}

func needsYou(t *testing.T, d *Daemon) []notifications.Notification {
	t.Helper()
	list, err := notifications.New(d.DB.DB).List(context.Background(), notifications.ListOptions{NeedsYou: true})
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// takeoverClient serves d's IPC with the takeover methods over host and connects a desktop client subscribed to
// sandbox.handoff, whose events it returns.
func takeoverClient(t *testing.T, d *Daemon, host computerHost) (*ipc.Client, func() []sandbox.HandoffState) {
	t.Helper()
	ep := ipc.Endpoint(t.TempDir())
	ln, err := ipc.Listen(ep)
	if err != nil {
		t.Fatal(err)
	}
	d.registerSandboxTakeover(host)
	go d.IPC.Serve(ln)
	t.Cleanup(func() { ln.Close(); d.IPC.CloseAll() })
	var mu sync.Mutex
	var seen []sandbox.HandoffState
	cl := ipc.NewClient(ipc.Dial(ep), "token", "gui")
	cl.OnEvent(func(event string, data json.RawMessage) {
		var s sandbox.HandoffState
		if event == "sandbox.handoff" && json.Unmarshal(data, &s) == nil {
			mu.Lock()
			seen = append(seen, s)
			mu.Unlock()
		}
	})
	ctx := context.Background()
	if err := cl.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	if err := cl.Subscribe(ctx, "sandbox.handoff"); err != nil {
		t.Fatal(err)
	}
	return cl, func() []sandbox.HandoffState {
		mu.Lock()
		defer mu.Unlock()
		return append([]sandbox.HandoffState(nil), seen...)
	}
}

// Review focus: ask_user_to_take_over pauses the turn, raises one "Needs you" entry that opens the bot's computer,
// publishes sandbox.handoff, and resumes with the user's note once they press Take over and Done (over IPC).
func TestTakeoverPausesTheTurnUntilTheUserIsDone(t *testing.T) {
	d := noticesDaemon(t)
	host := newFakeHost()
	cl, events := takeoverClient(t, d, host)
	done := askTakeover(context.Background(), d, host)
	ctx := context.Background()
	waitComputer(t, "the Needs you notification", func() bool {
		list := needsYou(t, d)
		return len(list) == 1 && list[0].Target == "computer:b1" && list[0].Kind == notifications.Approval &&
			list[0].Title == "Bot" && strings.Contains(list[0].Body, "Log in to example.com")
	})
	var st sandbox.HandoffState
	if err := cl.Call(ctx, "sandbox.handoff", map[string]string{"botId": "b1"}, &st); err != nil || !st.Pending || st.Reason != "Log in to example.com" {
		t.Fatalf("state: %+v %v", st, err)
	}
	select {
	case r := <-done:
		t.Fatalf("the turn must wait for the user: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	if err := cl.Call(ctx, "sandbox.takeover", map[string]string{"botId": "b1"}, nil); err != nil || !host.held("b1") {
		t.Fatalf("take over: %v", err)
	}
	if err := cl.Call(ctx, "sandbox.release", map[string]string{"botId": "b1", "note": "  Logged in​[31m  "}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.res.IsError || !strings.Contains(r.res.Text, "The user finished: Logged in[31m") || strings.ContainsAny(r.res.Text, "​") {
			t.Fatalf("result: %+v %v", r.res, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn did not resume")
	}
	if host.held("b1") {
		t.Fatal("Done must release the lock")
	}
	if list := needsYou(t, d); len(list) != 0 {
		t.Fatalf("the notification must resolve: %+v", list)
	}
	waitComputer(t, "the sandbox.handoff events", func() bool { return len(events()) == 2 })
	if ev := events(); !ev[0].Pending || ev[0].BotID != "b1" || ev[1].Pending || ev[1].BotID != "b1" {
		t.Fatalf("sandbox.handoff events: %+v", ev)
	}
}

// Review focus: a wait ends when the bot's computer stops or crashes, with a clear message, and its notification
// resolves; a stop that came just before the wait registered ends it too.
func TestTakeoverEndsWhenTheComputerStops(t *testing.T) {
	for _, state := range []sandbox.State{sandbox.StateStopping, sandbox.StateOff, sandbox.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			d := noticesDaemon(t)
			host := newFakeHost()
			done := askTakeover(context.Background(), d, host)
			waitComputer(t, "the wait", func() bool { return d.handoffs().State("b1").Pending })
			d.computerStatusChanged(sandbox.Status{BotID: "b2", State: state}) // another bot: no effect
			d.computerStatusChanged(sandbox.Status{BotID: "b1", State: sandbox.StateRunning, TakenOver: true})
			if !d.handoffs().State("b1").Pending {
				t.Fatal("a running computer must not end the wait")
			}
			d.computerStatusChanged(sandbox.Status{BotID: "b1", State: state})
			r := recvTakeover(t, done)
			if r.err != nil || !r.res.IsError || !strings.Contains(r.res.Text, "Your computer stopped while you waited") {
				t.Fatalf("result: %+v %v", r.res, r.err)
			}
			if list := needsYou(t, d); len(list) != 0 {
				t.Fatalf("the notification must resolve: %+v", list)
			}
		})
	}
	t.Run("stopped before the wait", func(t *testing.T) {
		d := noticesDaemon(t)
		host := newFakeHost()
		host.ensureCtx = func(context.Context) { host.mu.Lock(); host.state = sandbox.StateOff; host.mu.Unlock() }
		r := recvTakeover(t, askTakeover(context.Background(), d, host))
		if r.err != nil || !strings.Contains(r.res.Text, "Your computer stopped while you waited") {
			t.Fatalf("result: %+v %v", r.res, r.err)
		}
	})
}

// Review focus: when a wait ends without Done (hold timeout, the turn canceled) the user's takeover lock is released,
// so the bot's browser is not left refused.
func TestTakeoverEndingWithoutDoneReleasesTheLock(t *testing.T) {
	t.Run("hold timeout", func(t *testing.T) {
		d := noticesDaemon(t)
		d.handoffs().HoldTimeout = 100 * time.Millisecond // set before any wait uses it
		host := newFakeHost()
		done := askTakeover(context.Background(), d, host)
		waitComputer(t, "the wait", func() bool { return d.handoffs().State("b1").Pending })
		if err := d.takeOverComputer(context.Background(), host, "b1"); err != nil {
			t.Fatal(err)
		}
		r := recvTakeover(t, done)
		if r.err != nil || !strings.Contains(r.res.Text, "did not hand the computer back within 30 minutes") {
			t.Fatalf("result: %+v %v", r.res, r.err)
		}
		if host.held("b1") {
			t.Fatal("the lock must be released after the hold timeout")
		}
	})
	t.Run("turn canceled", func(t *testing.T) {
		d := noticesDaemon(t)
		host := newFakeHost()
		ctx, cancel := context.WithCancel(context.Background())
		done := askTakeover(ctx, d, host)
		waitComputer(t, "the wait", func() bool { return d.handoffs().State("b1").Pending })
		if err := d.takeOverComputer(context.Background(), host, "b1"); err != nil {
			t.Fatal(err)
		}
		cancel()
		if r := recvTakeover(t, done); !errors.Is(r.err, context.Canceled) {
			t.Fatalf("result: %+v %v", r.res, r.err)
		}
		if host.held("b1") || d.handoffs().State("b1").Pending {
			t.Fatal("the lock must be released when the turn ends")
		}
		if list := needsYou(t, d); len(list) != 0 {
			t.Fatalf("the notification must resolve: %+v", list)
		}
	})
	t.Run("nobody took over", func(t *testing.T) {
		d := noticesDaemon(t)
		d.handoffs().AskTimeout = 50 * time.Millisecond
		host := newFakeHost()
		if r := recvTakeover(t, askTakeover(context.Background(), d, host)); !strings.Contains(r.res.Text, "did not take over within 5 minutes") {
			t.Fatalf("result: %+v %v", r.res, r.err)
		}
	})
}

// Review focus: whether the user already holds the computer is read under the lock the Take over and Done handlers
// hold, so the wait cannot start from a stale answer; a user who holds it already gets the hold timeout.
func TestTakeoverReadsTheLockUnderTheTakeoverMutex(t *testing.T) {
	d := noticesDaemon(t)
	d.handoffs().AskTimeout = 30 * time.Millisecond
	host := newFakeHost()
	var unlocked bool
	host.takenHook = func() {
		if d.takeoverMu.TryLock() {
			unlocked = true
			d.takeoverMu.Unlock()
		}
	}
	if err := d.takeOverComputer(context.Background(), host, "b1"); err != nil {
		t.Fatal(err)
	}
	done := askTakeover(context.Background(), d, host)
	waitComputer(t, "the wait", func() bool { return d.handoffs().State("b1").Pending })
	time.Sleep(100 * time.Millisecond) // past AskTimeout: already taken over, so the hold timeout applies
	if !d.handoffs().State("b1").Pending {
		t.Fatal("a user who already holds the computer must get the hold timeout")
	}
	if err := d.releaseComputer(context.Background(), host, sandboxReleaseParams{BotID: "b1", Note: "done"}); err != nil {
		t.Fatal(err)
	}
	if r := recvTakeover(t, done); r.err != nil || !strings.Contains(r.res.Text, "The user finished: done") {
		t.Fatalf("result: %+v %v", r.res, r.err)
	}
	if unlocked {
		t.Fatal("TakenOver was read without the takeover mutex")
	}
}

func TestTakeoverHandlersValidateInput(t *testing.T) {
	d := noticesDaemon(t)
	host := newFakeHost()
	ctx := context.Background()
	isCode := func(err error, code string) bool {
		var ie *ipc.Error
		return errors.As(err, &ie) && ie.Code == code
	}
	for _, id := range []string{"", "a/b", strings.Repeat("x", 129), "a\nb"} {
		if err := d.takeOverComputer(ctx, host, id); !isCode(err, ipc.CodeBadRequest) {
			t.Errorf("take over %q: %v", id, err)
		}
		if err := d.releaseComputer(ctx, host, sandboxReleaseParams{BotID: id}); !isCode(err, ipc.CodeBadRequest) {
			t.Errorf("release %q: %v", id, err)
		}
		if _, err := d.handoffState(ctx, id); !isCode(err, ipc.CodeBadRequest) {
			t.Errorf("handoff %q: %v", id, err)
		}
	}
	if err := d.releaseComputer(ctx, host, sandboxReleaseParams{BotID: "b1", Note: strings.Repeat("é", maxReleaseNote+1)}); !isCode(err, ipc.CodeBadRequest) {
		t.Fatalf("long note: %v", err)
	}
	if err := d.releaseComputer(ctx, host, sandboxReleaseParams{BotID: "b1", Note: strings.Repeat("é", maxReleaseNote)}); err != nil {
		t.Fatalf("a 500-character note is fine: %v", err)
	}
	if err := d.releaseComputer(ctx, host, sandboxReleaseParams{BotID: "b1"}); err != nil {
		t.Fatalf("release without a wait is fine: %v", err)
	}
	host.state = sandbox.StateOff
	if err := d.takeOverComputer(ctx, host, "b1"); !isCode(err, ipc.CodeBadRequest) || host.held("b1") {
		t.Fatalf("take over a stopped computer: %v", err)
	}
	// Only the user, from the app: a bot or automation context is refused.
	bot := agents.WithActor(ctx, "bot:b1")
	if err := d.takeOverComputer(bot, host, "b1"); !isCode(err, ipc.CodeDenied) {
		t.Fatalf("bot take over: %v", err)
	}
	if err := d.releaseComputer(bot, host, sandboxReleaseParams{BotID: "b1"}); !isCode(err, ipc.CodeDenied) {
		t.Fatalf("bot release: %v", err)
	}
	if _, err := d.handoffState(bot, "b1"); !isCode(err, ipc.CodeDenied) {
		t.Fatalf("bot handoff: %v", err)
	}
}

// Review focus: takeover steps are audited as the user.
func TestTakeoverStepsAreAuditedAsTheUser(t *testing.T) {
	d := noticesDaemon(t)
	d.Policy = policy.New(d.DB.DB, nil, nil)
	host := newFakeHost()
	ctx := context.Background()
	if err := d.takeOverComputer(ctx, host, "b1"); err != nil {
		t.Fatal(err)
	}
	if err := d.releaseComputer(ctx, host, sandboxReleaseParams{BotID: "b1"}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.DB.Query(`SELECT actor, action, target, outcome FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var actor, action, target, outcome string
		if err := rows.Scan(&actor, &action, &target, &outcome); err != nil {
			t.Fatal(err)
		}
		got = append(got, strings.Join([]string{actor, action, target, outcome}, " "))
	}
	want := []string{"user computer.takeover computer/b1 allowed", "user computer.release computer/b1 allowed"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("audit: %q", got)
	}
}

// Review focus: takeover waits live in memory, so a restart resolves their "Needs you" entries.
func TestReconcileDropsComputerTakeovers(t *testing.T) {
	d := noticesDaemon(t)
	if _, err := d.Notify(notifications.Input{Source: "bot", Kind: notifications.Approval, Ref: refTakeover + "b1", Target: "computer:b1", Title: "Scout", Body: "Log in"}); err != nil {
		t.Fatal(err)
	}
	d.notices.reconcile()
	for _, n := range settled(t, d) {
		if n.NeedsYou {
			t.Fatalf("still waiting: %+v", n)
		}
	}
}

// Review focus: a takeover that started while the VM booted refuses commands and file writes at the adapter too;
// reads still run.
func TestComputerRuntimeRefusesRunAndWriteWhileTakenOver(t *testing.T) {
	host := newFakeHost()
	host.taken["b1"] = true
	rt := computerRuntime{d: &Daemon{}, host: host}
	if _, err := rt.Exec(context.Background(), "b1", wire.ExecRequest{Command: "ls"}); !errors.Is(err, sandbox.ErrTakenOver) {
		t.Errorf("exec: %v", err)
	}
	if _, err := rt.WriteFile(context.Background(), "b1", "/home/bot/a", []byte("x")); !errors.Is(err, sandbox.ErrTakenOver) {
		t.Errorf("write: %v", err)
	}
	if host.guest.count() != 0 {
		t.Fatalf("guest called %d times while taken over", host.guest.count())
	}
	if _, err := rt.ReadFile(context.Background(), "b1", "/home/bot/a"); err != nil {
		t.Fatalf("read: %v", err)
	}
}
