package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/cliruntime"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// codexMedia is what Stream hands the CLI natively: images, as `-i` files.
var codexMedia = media{images: true}

// Runtime is the part of *cliruntime.Manager the providers use.
type Runtime interface {
	Path(id string) string
	Ready(id string) (bool, string)
}

// Start runs a CLI with stdin as its input. Output is read until it closes; wait returns the exit error.
// Tests replace it; production starts the process.
type Start func(ctx context.Context, path, dir, stdin string, args ...string) (out io.ReadCloser, wait func() error, err error)

func startProcess(ctx context.Context, path, dir, stdin string, args ...string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	cliruntime.HideWindow(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return out, cmd.Wait, nil
}

// Codex runs OpenAI's Codex CLI with the user's ChatGPT account as a model.
type Codex struct {
	rt    Runtime
	start Start

	mu       sync.Mutex
	models   []llm.Model
	modelsAt time.Time
}

const modelsTTL = 10 * time.Minute

// CodexDescriptor registers the provider. It needs no key: it counts as connected while the CLI is installed and
// signed in.
func CodexDescriptor(rt Runtime) llm.Descriptor {
	return llm.Descriptor{
		ID: cliruntime.Codex, Name: "ChatGPT", Kind: "account", NoKey: true,
		Ready: func() (bool, string) { return rt.Ready(cliruntime.Codex) },
		New:   func(llm.Config) (llm.Provider, error) { return NewCodex(rt, nil), nil },
	}
}

// NewCodex returns the provider; start nil uses a real process.
func NewCodex(rt Runtime, start Start) *Codex {
	if start == nil {
		start = startProcess
	}
	return &Codex{rt: rt, start: start}
}

func (*Codex) ID() string { return cliruntime.Codex }

func (p *Codex) path() (string, error) {
	if path := p.rt.Path(cliruntime.Codex); path != "" {
		return path, nil
	}
	return "", &llm.Error{Code: llm.ErrAuth, Message: "ChatGPT is not installed. Download it in Settings > AI providers."}
}

// Validate checks that the CLI is installed and signed in.
func (p *Codex) Validate(context.Context) (llm.Account, error) {
	if _, err := p.path(); err != nil {
		return llm.Account{}, err
	}
	if ok, detail := p.rt.Ready(cliruntime.Codex); !ok {
		return llm.Account{}, &llm.Error{Code: llm.ErrAuth, Message: detail}
	}
	return llm.Account{Label: "ChatGPT account"}, nil
}

// Models reads the catalogue the CLI itself reports (`codex debug models`): the models this account may use.
func (p *Codex) Models(ctx context.Context) ([]llm.Model, error) {
	p.mu.Lock()
	if p.models != nil && time.Since(p.modelsAt) < modelsTTL {
		defer p.mu.Unlock()
		return p.models, nil
	}
	p.mu.Unlock()
	path, err := p.path()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, wait, err := p.start(ctx, path, os.TempDir(), "", "debug", "models")
	if err != nil {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not start Codex: " + err.Error()}
	}
	raw, _ := io.ReadAll(io.LimitReader(out, 8<<20))
	_ = wait()
	models, err := parseCodexModels(raw)
	if err != nil {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "Codex did not report its models"}
	}
	p.mu.Lock()
	p.models, p.modelsAt = models, time.Now()
	p.mu.Unlock()
	return models, nil
}

func parseCodexModels(raw []byte) ([]llm.Model, error) {
	var doc struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
			Context int64 `json:"context_window"`
			// Modalities is what the model reads; Codex's own catalogue treats an omitted list as text and images.
			Modalities *[]string `json:"input_modalities"`
		} `json:"models"`
	}
	// The CLI may print notices before the JSON document.
	if i := strings.Index(string(raw), "{"); i >= 0 {
		raw = raw[i:]
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []llm.Model
	for _, m := range doc.Models {
		if m.Visibility != "list" || m.Slug == "" {
			continue
		}
		// The catalogue lists what each model reads. Only images are forwarded (`-i`); the CLI takes no PDF.
		model := llm.Model{ID: m.Slug, Name: m.DisplayName, Caps: llm.Caps{Tools: true, Vision: m.Modalities == nil || slices.Contains(*m.Modalities, "image"), ContextWindow: m.Context}}
		if model.Name == "" {
			model.Name = m.Slug
		}
		for _, l := range m.Levels {
			model.Reasoning = append(model.Reasoning, l.Effort)
		}
		model.Caps.Reasoning = len(model.Reasoning) > 0
		out = append(out, model)
	}
	if len(out) == 0 {
		return nil, errors.New("no models")
	}
	return out, nil
}

// Stream runs one `codex exec` turn. Codex prints whole messages, not token deltas, so the reply arrives at once.
func (p *Codex) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	path, err := p.path()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "orole-codex-")
	if err != nil {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not create a scratch directory"}
	}
	args := []string{"exec", "--json", "--ephemeral", "--skip-git-repo-check", "--ignore-user-config", "--ignore-rules", "-s", "read-only", "-C", dir}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Reasoning != "" {
		args = append(args, "-c", `model_reasoning_effort="`+req.Reasoning+`"`)
	}
	args = append(args, "-")
	// The prompt comes first: -i takes every value after it. Images are files in the scratch directory the CLI runs
	// in, named by position (an attachment's own name is untrusted), and removed with it.
	atts := codexMedia.attachments(req)
	for i, a := range atts {
		name := fmt.Sprintf("attachment-%d%s", i+1, imageExt[a.MIME])
		if err := os.WriteFile(filepath.Join(dir, name), a.Data, 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, &llm.Error{Code: llm.ErrServer, Message: "could not prepare the attachments for Codex"}
		}
		args = append(args, "-i", name)
	}
	out, wait, err := p.start(ctx, path, dir, buildPrompt(req, codexMedia), args...)
	if err != nil {
		os.RemoveAll(dir)
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not start Codex: " + err.Error()}
	}
	ch := make(chan llm.Delta, 8)
	go func() {
		defer close(ch)
		defer os.RemoveAll(dir)
		for _, d := range p.read(ctx, out, wait, req) {
			select {
			case ch <- d: // a free slot always takes the delta, so a cancellation error is never dropped
			default:
				select {
				case ch <- d:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

// read turns Codex's JSON lines into deltas.
func (p *Codex) read(ctx context.Context, out io.ReadCloser, wait func() error, req llm.Request) []llm.Delta {
	var reply strings.Builder
	var usage *llm.Usage
	var failure string
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				In     int64 `json:"input_tokens"`
				Cached int64 `json:"cached_input_tokens"`
				Out    int64 `json:"output_tokens"`
				Reason int64 `json:"reasoning_output_tokens"`
			} `json:"usage"`
			Error   struct{ Message string } `json:"error"`
			Message string                   `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue // CLI log lines
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item.Type == "agent_message" {
				if reply.Len() > 0 {
					reply.WriteString("\n\n")
				}
				reply.WriteString(ev.Item.Text)
			}
		case "turn.completed":
			u := ev.Usage
			usage = &llm.Usage{InputTokens: &u.In, CachedTokens: &u.Cached, OutputTokens: &u.Out, ReasoningTokens: &u.Reason}
		case "turn.failed":
			failure = firstNonEmpty(ev.Error.Message, failure)
		case "error":
			failure = firstNonEmpty(failure, ev.Message)
		}
	}
	out.Close()
	werr := wait()
	if ctx.Err() != nil {
		return []llm.Delta{{Kind: llm.DeltaError, Err: &llm.Error{Code: llm.ErrCanceled, Message: "canceled"}}}
	}
	if failure != "" || (werr != nil && reply.Len() == 0) {
		return []llm.Delta{{Kind: llm.DeltaError, Err: codexError(failure, werr)}}
	}
	text, calls := splitReply(reply.String(), req.Tools)
	var ds []llm.Delta
	if text != "" {
		ds = append(ds, llm.Delta{Kind: llm.DeltaText, Text: text})
	}
	for i := range calls {
		ds = append(ds, llm.Delta{Kind: llm.DeltaToolCall, Call: &calls[i]})
	}
	if usage != nil {
		ds = append(ds, llm.Delta{Kind: llm.DeltaUsage, Usage: usage})
	}
	stop := llm.StopEnd
	if len(calls) > 0 {
		stop = llm.StopToolCalls
	}
	return append(ds, llm.Delta{Kind: llm.DeltaStop, Stop: stop})
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// codexError turns what Codex printed into a message a user can act on.
func codexError(failure string, exit error) *llm.Error {
	msg := failure
	// Codex wraps the API error as JSON text: {"type":"error","status":400,"error":{"message":"..."}}.
	var wrapped struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(failure), &wrapped) == nil && wrapped.Error.Message != "" {
		msg = wrapped.Error.Message
	}
	if msg == "" && exit != nil {
		msg = "Codex stopped: " + exit.Error()
	}
	low := strings.ToLower(msg)
	code := llm.ErrServer
	switch {
	case strings.Contains(low, "not supported when using codex"), strings.Contains(low, "model metadata"):
		code = llm.ErrBadRequest
	case strings.Contains(low, "usage limit"), strings.Contains(low, "quota"), strings.Contains(low, "rate limit"):
		code = llm.ErrRateLimit
	case strings.Contains(low, "not logged in"), strings.Contains(low, "log in"), strings.Contains(low, "401"), strings.Contains(low, "unauthorized"):
		code = llm.ErrAuth
	}
	return &llm.Error{Code: code, Message: msg}
}
