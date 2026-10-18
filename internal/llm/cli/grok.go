package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/cliruntime"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

const grokSystem = "You are the language model behind a chat assistant. Follow the system instructions given in the conversation."

// grokTools are the built-in tools of Grok Build that are taken away. The CLI has no "no tools" switch, and a few
// shell and subagent tools cannot be removed: with no approver in headless mode their calls are cancelled instead of
// run, which is what keeps the CLI from acting on the computer.
const grokTools = "run_terminal_command,read_file,search_replace,list_dir,grep,todo_write,monitor,search_tool,use_tool,workflow,enter_plan_mode,exit_plan_mode,ask_user_question,send_feedback,web_search,web_fetch,image_gen,image_edit,image_to_video,reference_to_video,write,kill_command_or_subagent,get_command_or_subagent_output,spawn_subagent,scheduler_create,scheduler_delete,scheduler_list"

// Grok runs x.ai's Grok Build CLI with the user's Grok account as a model.
type Grok struct {
	rt    Runtime
	start Start

	mu       sync.Mutex
	models   []llm.Model
	modelsAt time.Time
}

// GrokDescriptor registers the provider. It needs no key: it counts as connected while the CLI is installed and
// signed in.
func GrokDescriptor(rt Runtime) llm.Descriptor {
	return llm.Descriptor{
		ID: cliruntime.Grok, Name: "Grok", Kind: "account", NoKey: true,
		Ready: func() (bool, string) { return rt.Ready(cliruntime.Grok) },
		New:   func(llm.Config) (llm.Provider, error) { return NewGrok(rt, nil), nil },
	}
}

// NewGrok returns the provider; start nil uses a real process.
func NewGrok(rt Runtime, start Start) *Grok {
	if start == nil {
		start = startProcess
	}
	return &Grok{rt: rt, start: start}
}

func (*Grok) ID() string { return cliruntime.Grok }

func (p *Grok) path() (string, error) {
	if path := p.rt.Path(cliruntime.Grok); path != "" {
		return path, nil
	}
	return "", &llm.Error{Code: llm.ErrAuth, Message: "Grok is not installed. Download it in Settings > AI providers."}
}

// Validate checks that the CLI is installed and signed in.
func (p *Grok) Validate(context.Context) (llm.Account, error) {
	if _, err := p.path(); err != nil {
		return llm.Account{}, err
	}
	if ok, detail := p.rt.Ready(cliruntime.Grok); !ok {
		return llm.Account{}, &llm.Error{Code: llm.ErrAuth, Message: detail}
	}
	return llm.Account{Label: "Grok account"}, nil
}

var grokModelLine = regexp.MustCompile(`^\s*[*-]\s+(\S+)`)

// Models reads the list `grok models` prints: the models this account may use.
func (p *Grok) Models(ctx context.Context) ([]llm.Model, error) {
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
	out, wait, err := p.start(ctx, path, os.TempDir(), "", "models")
	if err != nil {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not start Grok: " + err.Error()}
	}
	raw, _ := io.ReadAll(io.LimitReader(out, 1<<20))
	_ = wait()
	var models []llm.Model
	for _, line := range strings.Split(string(raw), "\n") {
		if m := grokModelLine.FindStringSubmatch(line); m != nil {
			models = append(models, llm.Model{ID: m[1], Name: m[1], Caps: llm.Caps{Tools: true}})
		}
	}
	if len(models) == 0 {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "Grok did not report its models. Sign in under Settings > AI providers."}
	}
	p.mu.Lock()
	p.models, p.modelsAt = models, time.Now()
	p.mu.Unlock()
	return models, nil
}

// Stream runs one headless Grok turn. The prompt goes in a file in the scratch directory: Grok takes a prompt
// as an argument or a file, not on stdin. Images are not forwarded and the models declare no vision: Grok's image
// input is `--prompt-json`, a JSON value on the command line, which cannot hold a real image (a Windows command
// line stops at about 32 KB) and takes no file.
func (p *Grok) Stream(ctx context.Context, req llm.Request) (<-chan llm.Delta, error) {
	path, err := p.path()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "orole-grok-")
	if err != nil {
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not create a scratch directory"}
	}
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte(buildPrompt(req, media{})), 0o600); err != nil {
		os.RemoveAll(dir)
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not write the prompt"}
	}
	args := []string{"--prompt-file", promptFile, "--output-format", "streaming-messages-json", "--include-partial-messages",
		"--cwd", dir, "--no-subagents", "--no-plan", "--disallowed-tools", grokTools, "--system-prompt-override", grokSystem}
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	out, wait, err := p.start(ctx, path, dir, "", args...)
	if err != nil {
		os.RemoveAll(dir)
		return nil, &llm.Error{Code: llm.ErrServer, Message: "could not start Grok: " + err.Error()}
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
		}, grokError)
	}()
	return ch, nil
}

// grokError turns what Grok reported into a message a user can act on.
func grokError(failure string, exit error) *llm.Error {
	msg := strings.TrimSpace(failure)
	if msg == "" && exit != nil {
		msg = "Grok stopped: " + exit.Error()
	}
	low := strings.ToLower(msg)
	code := llm.ErrServer
	switch {
	case strings.Contains(low, "not authenticated"), strings.Contains(low, "not logged in"), strings.Contains(low, "sign in"), strings.Contains(low, "401"):
		code = llm.ErrAuth
	case strings.Contains(low, "rate limit"), strings.Contains(low, "usage limit"), strings.Contains(low, "quota"), strings.Contains(low, "429"):
		code = llm.ErrRateLimit
	case strings.Contains(low, "model") && (strings.Contains(low, "not found") || strings.Contains(low, "invalid") || strings.Contains(low, "unknown")):
		code = llm.ErrBadRequest
	}
	return &llm.Error{Code: code, Message: msg}
}
