package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/rayorole/orole-os-desktop/internal/cliruntime"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// claudeSystem replaces Claude Code's own coding-agent system prompt: the bot's instructions travel with the
// conversation on stdin (a Windows command line is far too short for them).
const claudeSystem = "You are the language model behind a chat assistant. Follow the system instructions given in the conversation."

// claudeLevels are the effort levels `claude --effort` accepts.
var claudeLevels = []string{"low", "medium", "high", "xhigh", "max"}

// Claude runs Anthropic's Claude Code CLI with the user's Claude account as a model.
type Claude struct {
	rt    Runtime
	start Start
}

// ClaudeDescriptor registers the provider. It needs no key: it counts as connected while the CLI is installed and
// signed in.
func ClaudeDescriptor(rt Runtime) llm.Descriptor {
	return llm.Descriptor{
		ID: cliruntime.Claude, Name: "Claude", Kind: "account", NoKey: true,
		Ready: func() (bool, string) { return rt.Ready(cliruntime.Claude) },
		New:   func(llm.Config) (llm.Provider, error) { return NewClaude(rt, nil), nil },
	}
}

// NewClaude returns the provider; start nil uses a real process.
func NewClaude(rt Runtime, start Start) *Claude {
	if start == nil {
		start = startProcess
	}
	return &Claude{rt: rt, start: start}
}

func (*Claude) ID() string { return cliruntime.Claude }

func (p *Claude) path() (string, error) {
	if path := p.rt.Path(cliruntime.Claude); path != "" {
		return path, nil
	}
	return "", &llm.Error{Code: llm.ErrAuth, Message: "Claude is not installed. Download it in Settings > AI providers."}
}

// Validate checks that the CLI is installed and signed in.
func (p *Claude) Validate(context.Context) (llm.Account, error) {
	if _, err := p.path(); err != nil {
		return llm.Account{}, err
	}
	if ok, detail := p.rt.Ready(cliruntime.Claude); !ok {
		return llm.Account{}, &llm.Error{Code: llm.ErrAuth, Message: detail}
	}
	return llm.Account{Label: "Claude account"}, nil
}

// Models lists the aliases the CLI documents for `--model`: each always means the newest model of that family, so
// the list needs no update when Anthropic ships one. The CLI has no command that lists models. Every Claude model
// reads images and PDFs, and Stream forwards both, so each alias declares that once instead of per model id.
func (*Claude) Models(context.Context) ([]llm.Model, error) {
	multimodal := llm.Caps{Tools: true, Vision: true, PDF: true}
	reasoning := multimodal
	reasoning.Reasoning = true
	return []llm.Model{
		{ID: "opus", Name: "Claude Opus", Caps: reasoning, Reasoning: claudeLevels},
		{ID: "sonnet", Name: "Claude Sonnet", Caps: reasoning, Reasoning: claudeLevels},
		{ID: "haiku", Name: "Claude Haiku", Caps: multimodal},
	}, nil
}

// claudeMedia is what Stream hands the CLI natively: images and PDFs as content blocks of a stream-json user turn.
var claudeMedia = media{images: true, pdfs: true}

// claudeInput is the one user turn `claude --input-format stream-json` reads from stdin: the rendered prompt
// followed by each image or PDF as a base64 content block.
func claudeInput(prompt string, atts []llm.Part) (string, error) {
	type source struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      []byte `json:"data"` // encoded as base64 by encoding/json
	}
	type block struct {
		Type   string  `json:"type"`
		Text   string  `json:"text,omitempty"`
		Source *source `json:"source,omitempty"`
	}
	content := []block{{Type: "text", Text: prompt}}
	for _, a := range atts {
		kind := "image"
		if a.Kind == llm.PartFile {
			kind = "document"
		}
		content = append(content, block{Type: kind, Source: &source{Type: "base64", MediaType: a.MIME, Data: a.Data}})
	}
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": content}})
	return string(line) + "\n", err
}

// Stream runs one `claude -p` turn with every Claude Code tool, setting source, MCP server and skill turned off, so
// it answers as a plain model. Text streams as it is written.
func (p *Claude) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	path, err := p.path()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "orole-claude-")
	if err != nil {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not create a scratch directory"}
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--no-session-persistence",
		"--tools", "", "--strict-mcp-config", "--setting-sources", "", "--disable-slash-commands", "--system-prompt", claudeSystem}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Reasoning != "" {
		args = append(args, "--effort", req.Reasoning)
	}
	stdin := buildPrompt(req, claudeMedia)
	if atts := claudeMedia.attachments(req); len(atts) > 0 {
		// Images and PDFs cannot travel in the text prompt; the CLI reads them from a stream-json user turn.
		if stdin, err = claudeInput(stdin, atts); err != nil {
			os.RemoveAll(dir)
			return nil, &llm.Error{Code: llm.ErrServer, Message: "could not prepare the attachments for Claude"}
		}
		args = append(args, "--input-format", "stream-json")
	}
	out, wait, err := p.start(ctx, path, dir, stdin, args...)
	if err != nil {
		os.RemoveAll(dir)
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not start Claude: " + err.Error()}
	}
	ch := make(chan llm.Delta, 16)
	go func() {
		defer close(ch)
		defer os.RemoveAll(dir)
		readStreamJSON(ctx, out, wait, req, func(d llm.Delta) bool {
			select {
			case ch <- d: // a free slot always takes the delta, so a cancellation error is never dropped
				return true
			default:
				select {
				case ch <- d:
					return true
				case <-ctx.Done():
					return false
				}
			}
		}, claudeError)
	}()
	return ch, nil
}

// readStreamJSON turns the stream-json lines of Claude Code (and of Grok, whose headless mode speaks the same
// Anthropic Messages wire format) into deltas. fail maps a reported failure or a bad exit to an error.
func readStreamJSON(ctx context.Context, out io.ReadCloser, wait func() error, req llm.Request, send func(llm.Delta) bool, fail func(string, error) *llm.Error) {
	var reply replyStream
	var usage *llm.Usage
	var failure string
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
			Event   struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			Usage struct {
				In      int64 `json:"input_tokens"`
				Created int64 `json:"cache_creation_input_tokens"`
				Cached  int64 `json:"cache_read_input_tokens"`
				Out     int64 `json:"output_tokens"`
			} `json:"usage"`
			Cost *float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "stream_event" && ev.Event.Type == "content_block_delta" && ev.Event.Delta.Type == "text_delta":
			if text := reply.Push(ev.Event.Delta.Text); text != "" && !send(llm.Delta{Kind: llm.DeltaText, Text: text}) {
				return
			}
		case ev.Type == "result":
			u := ev.Usage
			in := u.In + u.Created + u.Cached
			usage = &llm.Usage{InputTokens: &in, CachedTokens: &u.Cached, OutputTokens: &u.Out}
			if ev.IsError {
				failure = ev.Result
			}
		}
	}
	out.Close()
	werr := wait()
	if ctx.Err() != nil {
		send(llm.Delta{Kind: llm.DeltaError, Err: &llm.Error{Code: llm.ErrCanceled, Message: "canceled"}})
		return
	}
	if failure != "" || (werr != nil && reply.full.Len() == 0) {
		send(llm.Delta{Kind: llm.DeltaError, Err: fail(failure, werr)})
		return
	}
	rest, calls := reply.Finish(req.Tools)
	if rest != "" && !send(llm.Delta{Kind: llm.DeltaText, Text: rest}) {
		return
	}
	for i := range calls {
		if !send(llm.Delta{Kind: llm.DeltaToolCall, Call: &calls[i]}) {
			return
		}
	}
	if usage != nil && !send(llm.Delta{Kind: llm.DeltaUsage, Usage: usage}) {
		return
	}
	stop := llm.StopEnd
	if len(calls) > 0 {
		stop = llm.StopToolCalls
	}
	send(llm.Delta{Kind: llm.DeltaStop, Stop: stop})
}

// claudeError turns what Claude Code reported into a message a user can act on.
func claudeError(failure string, exit error) *llm.Error {
	msg := strings.TrimSpace(failure)
	if msg == "" && exit != nil {
		msg = "Claude stopped: " + exit.Error()
	}
	low := strings.ToLower(msg)
	code := llm.ErrServer
	switch {
	case strings.Contains(low, "not logged in"), strings.Contains(low, "/login"), strings.Contains(low, "authenticat"), strings.Contains(low, "401"):
		code = llm.ErrAuth
	case strings.Contains(low, "usage limit"), strings.Contains(low, "limit reached"), strings.Contains(low, "rate limit"), strings.Contains(low, "429"):
		code = llm.ErrRateLimit
	case strings.Contains(low, "model") && (strings.Contains(low, "not found") || strings.Contains(low, "invalid") || strings.Contains(low, "unavailable")):
		code = llm.ErrBadRequest
	}
	return &llm.Error{Code: code, Message: msg}
}
