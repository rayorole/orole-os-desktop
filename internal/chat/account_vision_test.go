package chat

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/llm/cli"
)

type readyRuntime struct{ path string }

func (r readyRuntime) Path(string) string          { return r.path }
func (r readyRuntime) Ready(string) (bool, string) { return true, "" }

// A bot on a Claude account used to be refused every image: the provider declared its models without vision. The
// engine, the gate and the provider are the real ones here; only the CLI process is replaced.
func TestAnImageReachesAClaudeAccountModelAndAModelWithoutVisionStillRefusesIt(t *testing.T) {
	var stdin string
	var args []string
	claude := cli.NewClaude(readyRuntime{path: "claude"}, func(_ context.Context, _, _, in string, a ...string) (io.ReadCloser, func() error, error) {
		stdin, args = in, a
		return io.NopCloser(strings.NewReader(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"A tiny image."}}}` + "\n" +
			`{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":1,"output_tokens":1}}` + "\n")), func() error { return nil }, nil
	})
	models, err := claude.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, nil)
	e.llm.prov, e.llm.models, e.llm.defaults = claude, models, llm.Defaults{ChatModel: "claude/sonnet", MemoryModel: "claude/sonnet"}
	b := e.bot("Eyes")
	img, err := e.upload("shot.png", pngBytes(t), 64)
	if err != nil {
		t.Fatal(err)
	}

	e.send(b.ID, "what is this?", img.ID)
	e.idle(b.ID)
	if r := e.lastReply(b.ID); r.Status != StatusComplete || !strings.Contains(r.Text(), "A tiny image.") {
		t.Fatalf("reply: %+v", r)
	}
	if !strings.Contains(stdin, `"type":"image"`) || !strings.Contains(stdin, `"media_type":"image/png"`) || !strings.Contains(stdin, "what is this?") {
		t.Fatalf("the image must reach the CLI as a content block: %.300s", stdin)
	}
	if !strings.Contains(strings.Join(args, " "), "--input-format stream-json") {
		t.Fatalf("args: %v", args)
	}

	// Grok's CLI cannot be given an image, so its models say so and the warning stays.
	grok := cli.NewGrok(readyRuntime{path: "grok"}, func(context.Context, string, string, string, ...string) (io.ReadCloser, func() error, error) {
		return io.NopCloser(strings.NewReader("Available models:\n  * grok-4.7 (default)\n")), func() error { return nil }, nil
	})
	gm, err := grok.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e.llm.prov, e.llm.models, e.llm.defaults = grok, gm, llm.Defaults{ChatModel: "grok/grok-4.7", MemoryModel: "grok/grok-4.7"}
	e.advance(catalogTTL + 1)
	g := e.bot("Blind")
	img2, err := e.upload("shot2.png", append(pngBytes(t), 0), 64)
	if err != nil {
		t.Fatal(err)
	}
	e.send(g.ID, "and this?", img2.ID)
	e.idle(g.ID)
	if r := e.lastReply(g.ID); r.Status != StatusError || !strings.Contains(r.Error, "cannot read images") {
		t.Fatalf("a model without vision must still refuse an image: %+v", r)
	}
}
