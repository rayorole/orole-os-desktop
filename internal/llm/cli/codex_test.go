package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/llm"
)

type fakeRuntime struct {
	path  string
	ready bool
}

func (f fakeRuntime) Path(string) string { return f.path }
func (f fakeRuntime) Ready(string) (bool, string) {
	return f.ready, "Sign in to ChatGPT in Settings > AI providers."
}

// run replays canned CLI output and records what it was started with.
type run struct {
	out   string
	exit  error
	args  []string
	stdin string
	dir   string
}

func (r *run) start(_ context.Context, _, dir, stdin string, args ...string) (io.ReadCloser, func() error, error) {
	r.args, r.stdin, r.dir = args, stdin, dir
	return io.NopCloser(strings.NewReader(r.out)), func() error { return r.exit }, nil
}

func collect(t *testing.T, ch <-chan llm.Delta) []llm.Delta {
	t.Helper()
	var out []llm.Delta
	for d := range ch {
		out = append(out, d)
	}
	return out
}

const okOutput = `Reading additional input from stdin...
{"type":"thread.started","thread_id":"t"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"pong"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":5,"reasoning_output_tokens":2}}
`

func TestStreamRunsAStrictSandboxedTurnAndReturnsTheReply(t *testing.T) {
	r := &run{out: okOutput}
	p := NewCodex(fakeRuntime{path: "codex", ready: true}, r.start)
	ch, err := p.Stream(context.Background(), llm.Request{
		Model: "gpt-5.6-luna", Reasoning: "low", System: "Be brief.",
		Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Kind: llm.PartText, Text: "ping?"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ds := collect(t, ch)
	if len(ds) != 3 || ds[0].Kind != llm.DeltaText || ds[0].Text != "pong" || ds[1].Kind != llm.DeltaUsage || ds[2].Kind != llm.DeltaStop || ds[2].Stop != llm.StopEnd {
		t.Fatalf("deltas: %+v", ds)
	}
	if u := ds[1].Usage; *u.InputTokens != 100 || *u.CachedTokens != 40 || *u.OutputTokens != 5 || *u.ReasoningTokens != 2 {
		t.Fatalf("usage: %+v", u)
	}
	for _, want := range []string{"exec", "--json", "--ephemeral", "--ignore-user-config", "--ignore-rules", "read-only", "-m", "gpt-5.6-luna", `model_reasoning_effort="low"`} {
		if !slices.Contains(r.args, want) {
			t.Errorf("args lack %q: %v", want, r.args)
		}
	}
	if last := r.args[len(r.args)-1]; last != "-" {
		t.Errorf("the prompt must come on stdin, got %q", last)
	}
	if !strings.Contains(r.stdin, "Be brief.") || !strings.Contains(r.stdin, "[user]\nping?") || !strings.Contains(r.stdin, "Do not use this CLI's native shell") {
		t.Errorf("prompt: %q", r.stdin)
	}
	if r.dir == "" {
		t.Error("the CLI must run in a scratch directory")
	}
}

func TestAReplyThatAsksForToolsBecomesToolCalls(t *testing.T) {
	reply := "Let me check.\\n\\n```tool_calls\\n[{\\\"name\\\":\\\"web_search\\\",\\\"arguments\\\":{\\\"q\\\":\\\"oslo\\\"}},{\\\"name\\\":\\\"ghost\\\",\\\"arguments\\\":{}}]\\n```"
	r := &run{out: `{"type":"item.completed","item":{"type":"agent_message","text":"` + reply + `"}}` + "\n"}
	p := NewCodex(fakeRuntime{path: "codex", ready: true}, r.start)
	ch, err := p.Stream(context.Background(), llm.Request{
		Tools:    []llm.ToolSpec{{Name: "web_search", Description: "Search.", Schema: []byte(`{"type":"object"}`)}},
		Messages: []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Kind: llm.PartText, Text: "weather?"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ds := collect(t, ch)
	if len(ds) != 3 || ds[0].Text != "Let me check." || ds[1].Kind != llm.DeltaToolCall || ds[1].Call.Name != "web_search" || string(ds[1].Call.Args) != `{"q":"oslo"}` || ds[2].Stop != llm.StopToolCalls {
		t.Fatalf("deltas: %+v", ds)
	}
	if !strings.Contains(r.stdin, "# Tools") || !strings.Contains(r.stdin, "## web_search") {
		t.Errorf("tools must be offered in the prompt: %q", r.stdin)
	}
}

func TestSplitReplyIgnoresAMalformedOrUnofferedToolBlock(t *testing.T) {
	tools := []llm.ToolSpec{{Name: "a"}, {Name: "hosted", Hosted: "web_search"}}
	for _, reply := range []string{
		"```tool_calls\nnot json\n```",
		"```tool_calls\n[{\"name\":\"b\",\"arguments\":{}}]\n```",
		"```tool_calls\n[{\"name\":\"hosted\",\"arguments\":{}}]\n```",
	} {
		text, calls := splitReply(reply, tools)
		if len(calls) != 0 || text != strings.TrimSpace(reply) {
			t.Errorf("%q -> %q %v", reply, text, calls)
		}
	}
}

// Long calls (a code task's prompt) are where models slip: raw newlines inside JSON strings, a fence or a newline in an
// argument, one call object instead of an array, or a reply that ends before the closing fence. Each must still call
// the tool with the argument intact, and the block must not be shown.
func TestSplitReplyReadsLongToolBlocksModelsActuallyWrite(t *testing.T) {
	tools := []llm.ToolSpec{{Name: "start_task"}}
	for name, c := range map[string]struct{ reply, prompt string }{
		"raw newlines in a string": {"Starting it.\n```tool_calls\n[{\"name\": \"start_task\", \"arguments\": {\"projectId\": \"p\", \"prompt\": \"Audit.\nReport:\n\t- a\"}}]\n```", "Audit.\nReport:\n\t- a"},
		"fence inside a string":    {"Starting it.\n```tool_calls\n[{\"name\": \"start_task\", \"arguments\": {\"projectId\": \"p\", \"prompt\": \"Use\n```go\ncode\n```\nblocks\"}}]\n```", "Use\n```go\ncode\n```\nblocks"},
		"no closing fence":         {"Starting it.\n```tool_calls\n[{\"name\": \"start_task\", \"arguments\": {\"projectId\": \"p\", \"prompt\": \"x\"}}]", "x"},
		"one object, not an array": {"Starting it.\n```tool_calls\n{\"name\": \"start_task\", \"arguments\": {\"projectId\": \"p\", \"prompt\": \"x\"}}\n```", "x"},
	} {
		text, calls := splitReply(c.reply, tools)
		if text != "Starting it." || len(calls) != 1 || calls[0].Name != "start_task" {
			t.Errorf("%s: text %q calls %+v", name, text, calls)
			continue
		}
		var args struct{ Prompt string }
		if err := json.Unmarshal(calls[0].Args, &args); err != nil || args.Prompt != c.prompt {
			t.Errorf("%s: args %s (%v)", name, calls[0].Args, err)
		}
	}
	// A truncated block is still not a call: nothing is invented.
	if _, calls := splitReply("```tool_calls\n[{\"name\": \"start_task\", \"arguments\": {\"prompt\": \"x", tools); len(calls) != 0 {
		t.Errorf("truncated block called %+v", calls)
	}
}

func TestToolHistoryIsRenderedForTheModel(t *testing.T) {
	got := buildPrompt(llm.Request{Messages: []llm.Message{
		{Role: llm.RoleUser, Parts: []llm.Part{{Kind: llm.PartText, Text: "hi"}}},
		{Role: llm.RoleAssistant, Parts: []llm.Part{{Kind: llm.PartToolCall, ToolName: "web_search", Args: []byte(`{ "q": "x" }`)}}},
		{Role: llm.RoleTool, Parts: []llm.Part{{Kind: llm.PartToolResult, ToolName: "web_search", Text: "result text"}}},
	}})
	for _, want := range []string{"[assistant called the tool web_search with {\"q\":\"x\"}]", "[tool result of web_search]\nresult text"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
}

func TestFailuresBecomeUsefulErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		exit error
		code llm.ErrorCode
		msg  string
	}{
		{"unsupported model", `{"type":"turn.failed","error":{"message":"{\"type\":\"error\",\"status\":400,\"error\":{\"message\":\"The 'x' model is not supported when using Codex with a ChatGPT account.\"}}"}}`, nil, llm.ErrBadRequest, "not supported"},
		{"usage limit", `{"type":"turn.failed","error":{"message":"You hit your usage limit."}}`, nil, llm.ErrRateLimit, "usage limit"},
		{"not signed in", `{"type":"error","message":"Not logged in"}`, nil, llm.ErrAuth, "Not logged in"},
		{"crash without output", "", errors.New("exit status 2"), llm.ErrServer, "exit status 2"},
	} {
		r := &run{out: tc.out + "\n", exit: tc.exit}
		ch, err := NewCodex(fakeRuntime{path: "codex", ready: true}, r.start).Stream(context.Background(), llm.Request{})
		if err != nil {
			t.Fatal(err)
		}
		ds := collect(t, ch)
		if len(ds) != 1 || ds[0].Kind != llm.DeltaError || ds[0].Err.Code != tc.code || !strings.Contains(ds[0].Err.Message, tc.msg) {
			t.Errorf("%s: %+v", tc.name, ds)
		}
	}
	// Not installed: refused before anything starts, pointing at the Settings page.
	_, err := NewCodex(fakeRuntime{}, (&run{}).start).Stream(context.Background(), llm.Request{})
	var le *llm.Error
	if !errors.As(err, &le) || le.Code != llm.ErrAuth || !strings.Contains(le.Message, "Settings > AI providers") {
		t.Fatalf("not installed: %v", err)
	}
}

func TestCanceledRunReportsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &run{out: "", exit: errors.New("killed")}
	p := NewCodex(fakeRuntime{path: "codex", ready: true}, func(ctx context.Context, path, dir, stdin string, args ...string) (io.ReadCloser, func() error, error) {
		cancel()
		return r.start(ctx, path, dir, stdin, args...)
	})
	ch, err := p.Stream(ctx, llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	ds := collect(t, ch)
	if len(ds) != 1 || ds[0].Err == nil || ds[0].Err.Code != llm.ErrCanceled {
		t.Fatalf("deltas: %+v", ds)
	}
}

func TestModelsComeFromTheCLIsOwnCatalogue(t *testing.T) {
	r := &run{out: `warning: something
{"models":[
 {"slug":"gpt-6-luna","display_name":"GPT-6-Luna","visibility":"list","context_window":272000,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
 {"slug":"hidden","display_name":"Hidden","visibility":"hide"}]}`}
	p := NewCodex(fakeRuntime{path: "codex", ready: true}, r.start)
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-6-luna" || models[0].Name != "GPT-6-Luna" || !models[0].Caps.Tools || !models[0].Caps.Reasoning || models[0].Caps.ContextWindow != 272000 || !slices.Equal(models[0].Reasoning, []string{"low", "high"}) {
		t.Fatalf("models: %+v", models)
	}
	if !slices.Equal(r.args, []string{"debug", "models"}) {
		t.Fatalf("args: %v", r.args)
	}
	r.out = "garbage" // cached: a second call does not run the CLI again
	if again, err := p.Models(context.Background()); err != nil || len(again) != 1 {
		t.Fatalf("cached models: %v %v", again, err)
	}
}

func TestDescriptorIsReadyOnlyWhenTheCLIIsSignedIn(t *testing.T) {
	d := CodexDescriptor(fakeRuntime{path: "codex", ready: false})
	if d.ID != "codex" || d.Kind != "account" || !d.NoKey {
		t.Fatalf("descriptor: %+v", d)
	}
	if ok, detail := d.Ready(); ok || !strings.Contains(detail, "Sign in") {
		t.Fatalf("ready: %v %q", ok, detail)
	}
}
