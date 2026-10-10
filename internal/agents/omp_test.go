package agents_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/database"
	"github.com/rayorole/orole-os-desktop/migrations"
)

var (
	ompOnce sync.Once
	ompBin  string
	ompErr  string
)

// fakeOmp builds testdata/fakeomp next to the fake agent (TestMain removes the directory).
func fakeOmp(t *testing.T) string {
	t.Helper()
	ompOnce.Do(func() {
		name := "fakeomp"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		ompBin = filepath.Join(filepath.Dir(fakeBin), name)
		if out, err := exec.Command("go", "build", "-o", ompBin, "./testdata/fakeomp").CombinedOutput(); err != nil {
			ompErr = err.Error() + "\n" + string(out)
		}
	})
	if ompErr != "" {
		t.Fatalf("building fake omp: %s", ompErr)
	}
	return ompBin
}

type ompRun struct {
	p    agents.Process
	dir  string // working directory
	data string // Host.DataDir
	sess string // LaunchSpec.SessionID
}

func startOmp(t *testing.T, spec agents.LaunchSpec) *ompRun {
	t.Helper()
	r := &ompRun{dir: spec.Dir, data: t.TempDir(), sess: spec.SessionID}
	if r.dir == "" {
		r.dir = t.TempDir()
		spec.Dir = r.dir
	}
	if r.sess == "" {
		r.sess = "sess-omp"
		spec.SessionID = r.sess
	}
	p, err := agents.Omp{Bin: fakeOmp(t)}.Start(context.Background(), &agents.Host{DataDir: r.data}, spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	r.p = p
	t.Cleanup(func() {
		go func() {
			for range p.Events() {
			}
		}()
		_ = p.Stop()
	})
	return r
}

// until reads events until stop reports true (that event is included).
func until(t *testing.T, p agents.Process, what string, stop func(agents.Event) bool) []agents.Event {
	t.Helper()
	var got []agents.Event
	timeout := time.After(15 * time.Second)
	for {
		select {
		case e, ok := <-p.Events():
			if !ok {
				t.Fatalf("events closed while waiting for %s; got %v", what, eventKinds(got))
			}
			got = append(got, e)
			if stop(e) {
				return got
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s; got %v", what, eventKinds(got))
		}
	}
}

func untilKind(t *testing.T, p agents.Process, kind string) []agents.Event {
	t.Helper()
	return until(t, p, kind, func(e agents.Event) bool { return e.Kind == kind })
}

func eventKinds(evs []agents.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

// eventsDetail is eventKinds with the text of error and activity events, for failure messages.
func eventsDetail(evs []agents.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
		if e.Kind == agents.EvError || e.Kind == agents.EvActivity {
			out[i] += "(" + e.Text + ")"
		}
	}
	return out
}

func firstOf(t *testing.T, evs []agents.Event, kind string) agents.Event {
	t.Helper()
	for _, e := range evs {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %s event in %v", kind, eventKinds(evs))
	return agents.Event{}
}

func countOf(evs []agents.Event, kind string) int {
	n := 0
	for _, e := range evs {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// inOrder asserts that kinds occur in evs as a subsequence.
func inOrder(t *testing.T, evs []agents.Event, kinds ...string) {
	t.Helper()
	i := 0
	for _, e := range evs {
		if i < len(kinds) && e.Kind == kinds[i] {
			i++
		}
	}
	if i != len(kinds) {
		t.Fatalf("events %v do not contain %v in order", eventKinds(evs), kinds)
	}
}

func decodeData[T any](t *testing.T, e agents.Event) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Data, &v); err != nil {
		t.Fatalf("decoding %s data %s: %v", e.Kind, e.Data, err)
	}
	return v
}

func startedOmp(t *testing.T) *ompRun {
	t.Helper()
	r := startOmp(t, agents.LaunchSpec{})
	untilKind(t, r.p, agents.EvInit)
	return r
}

func TestOmpDetect(t *testing.T) {
	a := agents.Omp{Bin: fakeOmp(t)}
	d := a.Detect(context.Background())
	if !d.Installed || d.Version != "omp/0.0.0-fake" || d.Kind != "omp" || d.Name != "Oh My Pi" {
		t.Fatalf("detect: %+v", d)
	}
	if !d.Resume || !d.Permissions || !slices.Equal(d.Modes, []agents.Mode{agents.ModeStructured}) || !a.CanResume() {
		t.Fatalf("capabilities: %+v", d)
	}
	if missing := (agents.Omp{Bin: "definitely-not-installed-omp"}).Detect(context.Background()); missing.Installed || !strings.Contains(missing.Error, "not found") {
		t.Fatalf("missing omp must be reported honestly: %+v", missing)
	}
}

func TestOmpPromptToolTurn(t *testing.T) {
	r := startOmp(t, agents.LaunchSpec{})
	init := untilKind(t, r.p, agents.EvInit)
	ev := decodeData[map[string]string](t, firstOf(t, init, agents.EvInit))
	if ev["agentSessionId"] != "omp-session-1" || ev["model"] != "anthropic/claude-fable-5" {
		t.Fatalf("init: %v", ev)
	}
	if err := r.p.Send("TOOL please"); err != nil {
		t.Fatal(err)
	}
	evs := append(init, untilKind(t, r.p, agents.EvTurnDone)...)
	if got := firstOf(t, evs, agents.EvTranscript).Text; got != "/fake/omp-session-1.jsonl" {
		t.Fatalf("transcript = %q", got)
	}
	inOrder(t, evs, agents.EvWorking, agents.EvReasoning, agents.EvTool, agents.EvToolResult, agents.EvMessage, agents.EvTurnDone)
	if countOf(evs, agents.EvPrompt) != 0 || countOf(evs, agents.EvError) != 0 || countOf(evs, agents.EvSystem) != 0 {
		t.Fatalf("unexpected events (the manager records prompts; widgets are ignored): %v", eventKinds(evs))
	}
	if !slices.ContainsFunc(evs, func(e agents.Event) bool { return e.Kind == agents.EvActivity && e.Text == "Thinking" }) {
		t.Fatalf("no streaming activity: %v", eventKinds(evs))
	}
	if reasoning := firstOf(t, evs, agents.EvReasoning); reasoning.Text != "I should look at this first." || !strings.Contains(string(reasoning.Data), `"id"`) {
		t.Fatalf("reasoning: %+v", reasoning)
	}
	call := decodeData[agents.ToolData](t, firstOf(t, evs, agents.EvTool))
	if call.ID != "call-1" || call.Name != "bash" || call.Status != "running" || call.Server != "" || !strings.Contains(string(call.Input), "echo hi") {
		t.Fatalf("tool call: %+v", call)
	}
	if text := firstOf(t, evs, agents.EvTool).Text; text != "bash: echo hi" {
		t.Fatalf("tool summary = %q", text)
	}
	res := decodeData[agents.ToolData](t, firstOf(t, evs, agents.EvToolResult))
	if res.ID != "call-1" || res.Status != "ok" || res.Output != "hi" || res.Name != "bash" {
		t.Fatalf("tool result: %+v", res)
	}
	done := decodeData[struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}](t, firstOf(t, evs, agents.EvTurnDone))
	if !done.OK || done.Result != "TOOL done" || firstOf(t, evs, agents.EvMessage).Text != "TOOL done" {
		t.Fatalf("turn done: %+v", done)
	}

	// The process stays up: a second prompt starts a new turn.
	if err := r.p.Send("again"); err != nil {
		t.Fatal(err)
	}
	again := untilKind(t, r.p, agents.EvTurnDone)
	if firstOf(t, again, agents.EvMessage).Text != "done: again" {
		t.Fatalf("second turn: %v", eventKinds(again))
	}
}

func TestOmpMCPToolNaming(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("MCP"); err != nil {
		t.Fatal(err)
	}
	evs := untilKind(t, r.p, agents.EvTurnDone)
	call := decodeData[agents.ToolData](t, firstOf(t, evs, agents.EvTool))
	if call.Name != "skill_list" || call.Server != "orole" || string(call.Input) != "{}" {
		t.Fatalf("mcp call: %+v", call)
	}
	res := decodeData[agents.ToolData](t, firstOf(t, evs, agents.EvToolResult))
	if res.Name != "skill_list" || res.Server != "orole" || res.Output != "skills: alpha, beta" || res.Status != "ok" {
		t.Fatalf("mcp result: %+v", res)
	}
}

func TestOmpPermissionRoundTrip(t *testing.T) {
	for name, allow := range map[string]bool{"approve": true, "deny": false} {
		t.Run(name, func(t *testing.T) {
			r := startedOmp(t)
			if err := r.p.Send("PERM"); err != nil {
				t.Fatal(err)
			}
			evs := untilKind(t, r.p, agents.EvPermission)
			req := decodeData[agents.PermissionRequest](t, firstOf(t, evs, agents.EvPermission))
			if req.Tool != "bash" || req.Summary != "Command: echo hi" || req.ID == "" ||
				!strings.Contains(req.InputJSON, `"Approve"`) || string(req.Input) != req.InputJSON {
				t.Fatalf("permission: %+v", req)
			}
			if err := r.p.Respond("no-such-request", true, ""); err == nil {
				t.Fatal("answering an unknown request must fail")
			}
			if err := r.p.Respond(req.ID, allow, "nope"); err != nil {
				t.Fatal(err)
			}
			if err := r.p.Respond(req.ID, allow, ""); err == nil {
				t.Fatal("a request can only be answered once")
			}
			rest := untilKind(t, r.p, agents.EvTurnDone)
			res := decodeData[agents.ToolData](t, firstOf(t, rest, agents.EvToolResult))
			msg := firstOf(t, rest, agents.EvMessage).Text
			if allow && (res.Status != "ok" || msg != "approved") {
				t.Fatalf("approval did not reach omp: %+v %q", res, msg)
			}
			if !allow && (res.Status != "error" || msg != "denied") {
				t.Fatalf("denial did not reach omp: %+v %q", res, msg)
			}
		})
	}
}

func TestOmpConfirmDialogIsAPermission(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("CONFIRM"); err != nil {
		t.Fatal(err)
	}
	evs := untilKind(t, r.p, agents.EvPermission)
	req := decodeData[agents.PermissionRequest](t, firstOf(t, evs, agents.EvPermission))
	if req.Tool != "omp" || req.Summary != "Delete branch? feature/x will be removed" {
		t.Fatalf("confirm permission: %+v", req)
	}
	if err := r.p.Respond(req.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if msg := firstOf(t, untilKind(t, r.p, agents.EvTurnDone), agents.EvMessage).Text; msg != "confirmed" {
		t.Fatalf("confirm answer = %q", msg)
	}
}

func TestOmpQuestionsUseNativeInputResponse(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("ASK"); err != nil {
		t.Fatal(err)
	}
	evs := untilKind(t, r.p, agents.EvQuestion)
	if countOf(evs, agents.EvPermission) != 0 {
		t.Fatalf("a question must not become an approval: %v", eventKinds(evs))
	}
	req := decodeData[agents.QuestionRequest](t, firstOf(t, evs, agents.EvQuestion))
	responder, ok := r.p.(agents.InputResponder)
	if !ok {
		t.Fatal("OMP must expose native input responses")
	}
	if err := responder.AnswerInput(req.ID, nil, true); err != nil {
		t.Fatal(err)
	}
	if msg := firstOf(t, untilKind(t, r.p, agents.EvTurnDone), agents.EvMessage).Text; msg != "question cancelled" {
		t.Fatalf("omp must see a cancelled dialog, got %q", msg)
	}
	if err := responder.AnswerInput(req.ID, nil, true); err == nil {
		t.Fatal("a resolved question must not be answered twice")
	}
}

func TestOmpSendDuringTurnSteers(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("SLOW first"); err != nil {
		t.Fatal(err)
	}
	untilKind(t, r.p, agents.EvWorking)
	if err := r.p.Send("second note"); err != nil {
		t.Fatal(err)
	}
	evs := untilKind(t, r.p, agents.EvTurnDone)
	if msg := firstOf(t, evs, agents.EvMessage).Text; !strings.Contains(msg, "steered: second note") {
		t.Fatalf("the prompt sent mid-turn was not queued as steering: %q", msg)
	}
	if countOf(evs, agents.EvError) != 0 {
		t.Fatalf("errors: %v", eventKinds(evs))
	}
}

func TestOmpStopAbortsAndExits(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("HANG"); err != nil {
		t.Fatal(err)
	}
	untilKind(t, r.p, agents.EvWorking)
	start := time.Now()
	if err := r.p.Stop(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("stop took %v", d)
	}
	var last agents.Event
	for e := range r.p.Events() {
		last = e
	}
	if last.Kind != agents.EvExit {
		t.Fatalf("last event = %s", last.Kind)
	}
	exit := decodeData[struct {
		Code    int  `json:"code"`
		Stopped bool `json:"stopped"`
	}](t, last)
	if exit.Code != 0 || !exit.Stopped {
		t.Fatalf("exit: %+v", exit)
	}
	if err := r.p.Send("late"); err == nil {
		t.Fatal("sending to a stopped agent must fail")
	}
}

func TestOmpFailedTurnAndLocalCommand(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("FAIL"); err != nil {
		t.Fatal(err)
	}
	done := decodeData[struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}](t, firstOf(t, untilKind(t, r.p, agents.EvTurnDone), agents.EvTurnDone))
	if done.OK || done.Result != "529 overloaded" {
		t.Fatalf("failed turn: %+v", done)
	}
	if err := r.p.Send("LOCAL command"); err != nil {
		t.Fatal(err)
	}
	local := decodeData[struct {
		OK bool `json:"ok"`
	}](t, firstOf(t, untilKind(t, r.p, agents.EvTurnDone), agents.EvTurnDone))
	if !local.OK {
		t.Fatal("a builtin command that starts no agent turn must still close the turn")
	}
}

func TestOmpChunkedFrames(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("CHUNK"); err != nil {
		t.Fatal(err)
	}
	evs := untilKind(t, r.p, agents.EvTurnDone)
	var msg string
	for _, e := range evs {
		if e.Kind == agents.EvMessage {
			msg = e.Text
		}
	}
	if len(msg) != 307200 || countOf(evs, agents.EvError) != 0 {
		t.Fatalf("reassembled message length %d, events %v", len(msg), eventsDetail(evs))
	}
	if err := r.p.Send("BADCHUNK"); err != nil {
		t.Fatal(err)
	}
	bad := untilKind(t, r.p, agents.EvTurnDone)
	if e := firstOf(t, bad, agents.EvError).Text; !strings.Contains(e, "malformed rpc_chunk") {
		t.Fatalf("error = %q", e)
	}
}

func TestOmpGeneratedImage(t *testing.T) {
	r := startedOmp(t)
	if err := r.p.Send("IMAGE"); err != nil {
		t.Fatal(err)
	}
	evs := untilKind(t, r.p, agents.EvTurnDone)
	img := decodeData[agents.ImageData](t, firstOf(t, evs, agents.EvImage))
	if img.Mime != "image/png" || img.Prompt != "a red dot" || img.ID == "" {
		t.Fatalf("image: %+v", img)
	}
	scratch := filepath.Join(r.data, "agents", r.sess)
	if rel, err := filepath.Rel(scratch, img.Path); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("image %s is outside the session scratch directory %s", img.Path, scratch)
	}
	raw, err := os.ReadFile(img.Path)
	if err != nil || !strings.HasPrefix(string(raw), "\x89PNG") {
		t.Fatalf("saved image: %v %q", err, raw)
	}
	res := decodeData[agents.ToolData](t, firstOf(t, evs, agents.EvToolResult))
	if res.Output != "Generated 1 image" {
		t.Fatalf("tool result: %+v", res)
	}
}

func readJSONFile[T any](t *testing.T, path string) T {
	t.Helper()
	var v T
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, raw)
	}
	return v
}

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestOmpLaunchArgsResumeAndGateway(t *testing.T) {
	gw := filepath.Join(t.TempDir(), "orole-daemon.exe")
	r := startOmp(t, agents.LaunchSpec{
		Model: "anthropic/claude-fable-5", Reasoning: "high", PermissionMode: "acceptEdits", Resume: "omp-session-9",
		SkillsPrompt: "## Skills\n- alpha: does alpha",
		Gateway: &agents.Gateway{Command: gw, Args: []string{"mcp-gateway"},
			Env: map[string]string{"OROLE_DATA_DIR": `C:\Users\me\AppData\Local\Orole`, "OROLE_SESSION_ID": "sess-omp"}},
	})
	init := decodeData[map[string]string](t, firstOf(t, untilKind(t, r.p, agents.EvInit), agents.EvInit))
	if init["agentSessionId"] != "omp-session-9" {
		t.Fatalf("a resumed session must keep its id: %v", init)
	}
	args := readJSONFile[[]string](t, filepath.Join(r.dir, "fakeomp-argv.json"))
	root := filepath.Join(r.data, "agents", r.sess, "omp")
	for flag, want := range map[string]string{
		"--mode": "rpc", "--cwd": r.dir, "--session-dir": filepath.Join(root, "sessions"), "--resume": "omp-session-9",
		"--model": "anthropic/claude-fable-5", "--thinking": "high", "--approval-mode": "write",
		"-e": filepath.Join(root, "ext"), "--append-system-prompt": filepath.Join(root, "skills-prompt.md"),
	} {
		if !hasPair(args, flag, want) {
			t.Errorf("missing %s %s in %v", flag, want, args)
		}
	}
	if slices.Contains(args, "--tools") {
		t.Errorf("acceptEdits must not restrict tools: %v", args)
	}
	seen := readJSONFile[map[string]string](t, filepath.Join(r.dir, "fakeomp-seen.json"))
	if strings.TrimSpace(seen["prompt"]) != "## Skills\n- alpha: does alpha" {
		t.Errorf("skills prompt file = %q", seen["prompt"])
	}
	var mcp struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(seen["mcp"]), &mcp); err != nil {
		t.Fatalf("mcp config %q: %v", seen["mcp"], err)
	}
	srv, ok := mcp.Servers["orole"]
	if !ok || len(mcp.Servers) != 1 || srv.Type != "stdio" || srv.Command != gw || !slices.Equal(srv.Args, []string{"mcp-gateway"}) ||
		srv.Env["OROLE_SESSION_ID"] != "sess-omp" || srv.Env["OROLE_DATA_DIR"] != `C:\Users\me\AppData\Local\Orole` {
		t.Fatalf("gateway config: %+v", mcp.Servers)
	}
	if !strings.Contains(seen["package"], `"./index.js"`) {
		t.Errorf("extension package manifest = %q", seen["package"])
	}
}

func TestOmpLaunchArgsDefaultsAndPlan(t *testing.T) {
	r := startedOmp(t)
	args := readJSONFile[[]string](t, filepath.Join(r.dir, "fakeomp-argv.json"))
	if !hasPair(args, "--approval-mode", "always-ask") {
		t.Fatalf("omp defaults to yolo; Orole must ask by default: %v", args)
	}
	for _, flag := range []string{"-e", "--append-system-prompt", "--model", "--thinking", "--resume", "--tools"} {
		if slices.Contains(args, flag) {
			t.Errorf("unexpected %s in %v", flag, args)
		}
	}
	plan := startOmp(t, agents.LaunchSpec{PermissionMode: "plan"})
	untilKind(t, plan.p, agents.EvInit)
	pargs := readJSONFile[[]string](t, filepath.Join(plan.dir, "fakeomp-argv.json"))
	if !hasPair(pargs, "--approval-mode", "always-ask") || !hasPair(pargs, "--tools", "read,glob,grep,web_search,todo") {
		t.Fatalf("plan mode args: %v", pargs)
	}
}

func TestOmpStartRejectsUnsafeLaunches(t *testing.T) {
	bin := fakeOmp(t)
	host := &agents.Host{DataDir: t.TempDir()}
	gw := func(args []string, env map[string]string) *agents.Gateway {
		return &agents.Gateway{Command: "orole-daemon", Args: args, Env: env}
	}
	cases := map[string]agents.LaunchSpec{
		"remote machine":      {Machine: "box"},
		"terminal mode":       {Mode: agents.ModeTerminal},
		"unknown permission":  {PermissionMode: "bypassPermissions"},
		"unknown reasoning":   {Reasoning: "ultra"},
		"model injection":     {Model: "x\n--auto-approve"},
		"resume injection":    {Resume: "a b"},
		"env command":         {Gateway: gw(nil, map[string]string{"X": "!calc"})},
		"env placeholder":     {Gateway: gw(nil, map[string]string{"X": "${HOME}"})},
		"arg placeholder":     {Gateway: gw([]string{"${HOME}"}, nil)},
		"env name":            {Gateway: gw(nil, map[string]string{"A B": "x"})},
		"gateway no command":  {Gateway: &agents.Gateway{}},
		"gateway cmd command": {Gateway: &agents.Gateway{Command: "!calc"}},
	}
	for name, spec := range cases {
		spec.SessionID, spec.Dir = "reject-"+strings.ReplaceAll(name, " ", "-"), t.TempDir()
		if p, err := (agents.Omp{Bin: bin}).Start(context.Background(), host, spec); err == nil {
			_ = p.Stop()
			t.Errorf("%s: Start succeeded", name)
		}
	}
	// Reasoning levels are the omp --thinking set.
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		spec := agents.LaunchSpec{SessionID: "level-" + level, Dir: t.TempDir(), Reasoning: level}
		p, err := (agents.Omp{Bin: bin}).Start(context.Background(), host, spec)
		if err != nil {
			t.Errorf("reasoning %q rejected: %v", level, err)
			continue
		}
		_ = p.Stop()
	}
}

func newOmpManager(t *testing.T) *agents.Manager {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(filepath.Join(dir, "orole.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := agents.NewManager(agents.Config{
		Store: agents.NewStore(db), Host: &agents.Host{DataDir: dir},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Publish:   func(string, any) {},
		Audit:     func(string, string, string, string) {},
		Authorize: func(context.Context, string, string, string) error { return nil },
		Adapters:  []agents.Adapter{agents.Omp{Bin: fakeOmp(t)}},
	})
	t.Cleanup(m.Shutdown)
	return m
}

func TestOmpUnderManager(t *testing.T) {
	m := newOmpManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := m.Start(ctx, agents.StartSpec{Kind: "omp", Dir: t.TempDir(), Prompt: "PERM please"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Mode != agents.ModeStructured {
		t.Fatalf("mode = %s", s.Mode)
	}
	wait := func(what string, pred func(agents.Session) bool) agents.Session {
		t.Helper()
		got, err := m.WaitFor(ctx, s.ID, pred)
		if err != nil {
			t.Fatalf("waiting for %s: %v (status=%s activity=%q error=%q)", what, err, got.Status, got.Activity, got.Error)
		}
		return got
	}
	s = wait("needs-you", func(s agents.Session) bool { return s.Status == agents.StatusNeedsYou && len(s.Pending) == 1 })
	if s.Pending[0].Tool != "bash" || s.AgentSessionID != "omp-session-1" || s.TranscriptRef != "/fake/omp-session-1.jsonl" {
		t.Fatalf("session: %+v", s)
	}
	if err := m.Approve(ctx, s.ID, s.Pending[0].ID, true, ""); err != nil {
		t.Fatal(err)
	}
	s = wait("done", func(s agents.Session) bool { return s.Status == agents.StatusDone })
	if s.Turns != 1 || s.LastMessage != "approved" || len(s.Pending) != 0 {
		t.Fatalf("after turn: turns=%d last=%q pending=%d", s.Turns, s.LastMessage, len(s.Pending))
	}
	if err := m.Prompt(ctx, s.ID, "again"); err != nil {
		t.Fatal(err)
	}
	s = wait("second turn", func(s agents.Session) bool { return s.Status == agents.StatusDone && s.Turns == 2 })
	if s.LastMessage != "done: again" {
		t.Fatalf("last message = %q", s.LastMessage)
	}
	tr, err := m.Transcript(s.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	k := map[string]int{}
	for _, e := range tr {
		k[e.Kind]++
	}
	for _, want := range []string{agents.EvReasoning, agents.EvTool, agents.EvToolResult, agents.EvPermission, agents.EvDecision, agents.EvMessage, agents.EvTurnDone} {
		if k[want] == 0 {
			t.Errorf("transcript missing %s: %v", want, k)
		}
	}
	if k[agents.EvPrompt] != 2 {
		t.Errorf("each prompt must be recorded exactly once (by the manager), got %d", k[agents.EvPrompt])
	}
	if _, err := m.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOmpOptionsListModelsAndReasoning(t *testing.T) {
	m := newOmpManager(t)
	opts, err := m.Options(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Harnesses) != 1 {
		t.Fatalf("harnesses: %+v", opts.Harnesses)
	}
	h := opts.Harnesses[0]
	if h.ID != "omp" || h.Name != "Oh My Pi" || !h.Available || !h.CustomModel || !h.Structured || h.Interactive {
		t.Fatalf("harness: %+v", h)
	}
	if !slices.Equal(h.Reasoning, []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("reasoning: %v", h.Reasoning)
	}
	if len(h.Models) != 2 {
		t.Fatalf("only chat models with a safe selector are offered: %+v", h.Models)
	}
	if h.Models[0].ID != "anthropic/claude-fable-5" || h.Models[0].Name != "Claude Fable 5" ||
		!slices.Equal(h.Models[0].Reasoning, []string{"off", "low", "medium", "high"}) {
		t.Fatalf("model 0: %+v", h.Models[0])
	}
	if h.Models[1].ID != "openai/gpt-plain" || len(h.Models[1].Reasoning) != 0 {
		t.Fatalf("model 1: %+v", h.Models[1])
	}
}
