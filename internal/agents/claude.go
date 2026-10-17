package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Claude is the Claude Code adapter (binary `claude`).
//
// Structured mode runs `claude -p --output-format stream-json --input-format stream-json
// --verbose --permission-prompt-tool stdio`: prompts go in as stream-json user messages,
// permission requests arrive as control_request/can_use_tool messages and are answered with
// control_response messages. Terminal mode runs the interactive TUI in a ConPTY with a
// --settings file whose hooks call back into the daemon (see Manager.Hook).
type Claude struct {
	// Bin overrides the executable (tests); "" = `claude` from PATH.
	Bin string
}

func (c Claude) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "claude"
}

func (Claude) Kind() string    { return "claude" }
func (Claude) Name() string    { return "Claude Code" }
func (Claude) Modes() []Mode   { return []Mode{ModeStructured, ModeTerminal} }
func (Claude) CanResume() bool { return true }

func (c Claude) Detect(ctx context.Context) Detection {
	d := detectBinary(ctx, "claude", "Claude Code", c.bin())
	d.Modes, d.Resume, d.Permissions, d.StatusFeed = c.Modes(), true, true, "stream-json"
	return d
}

// PermissionModes: Manual and YOLO map one to one. Auto is Claude Code's own classifier mode
// when the installed build lists it, otherwise acceptEdits, which Orole reports as approximated.
func (c Claude) PermissionModes(ctx context.Context, d Detection) []PermissionModeSupport {
	auto := modeSupport(PermAuto, true, "This Claude Code has no auto mode; Auto approves file edits in the workspace and asks for everything else.")
	if d.Installed && claudeSupportsAuto(ctx, d.Path) {
		auto = modeSupport(PermAuto, false, "Claude Code's own classifier decides which actions are safe to run without asking.")
	}
	return []PermissionModeSupport{
		modeSupport(PermManual, false, "Claude Code asks before anything that is not read-only."),
		auto,
		modeSupport(PermYolo, false, "Claude Code runs everything without asking."),
	}
}

// claudePermissionMode maps an Orole mode onto a --permission-mode value. Manual is passed
// explicitly: the CLI's built-in default can otherwise be auto.
func claudePermissionMode(mode string, autoNative bool) (string, error) {
	switch mode {
	case "", PermManual:
		return "default", nil
	case PermAuto:
		if autoNative {
			return "auto", nil
		}
		return "acceptEdits", nil
	case PermYolo:
		return "bypassPermissions", nil
	case PermPlan:
		return "plan", nil
	}
	return "", fmt.Errorf("unsupported permission mode %q", mode)
}

func (c Claude) Start(ctx context.Context, h *Host, spec LaunchSpec) (Process, error) {
	if !localMachine(spec.Machine) {
		return nil, fmt.Errorf("local Claude adapter cannot launch on a remote machine")
	}
	if err := validPermissionMode(spec.PermissionMode); err != nil {
		return nil, err
	}
	if err := checkToken("model", spec.Model); err != nil {
		return nil, err
	}
	if err := validateReasoning("claude", spec.Reasoning); err != nil {
		return nil, err
	}
	if err := checkToken("resume id", spec.Resume); err != nil {
		return nil, err
	}
	path, err := lookBin(c.bin())
	if err != nil {
		return nil, err
	}
	var args []string
	if spec.Resume != "" {
		args = append(args, "--resume", spec.Resume)
	} else if spec.NewAgentSessionID != "" {
		args = append(args, "--session-id", spec.NewAgentSessionID)
	}
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Reasoning != "" {
		args = append(args, "--effort", spec.Reasoning)
	}
	autoNative := spec.PermissionMode == PermAuto && claudeSupportsAuto(ctx, path)
	permission, err := claudePermissionMode(spec.PermissionMode, autoNative)
	if err != nil {
		return nil, err
	}
	args = append(args, "--permission-mode", permission)
	if spec.Mode == ModeTerminal {
		return c.startTerminal(h, spec, path, args)
	}
	if spec.AllowYolo && spec.PermissionMode != PermYolo {
		// Lets set_permission_mode reach bypassPermissions later; it does not activate it.
		args = append(args, "--allow-dangerously-skip-permissions")
	}
	args, err = claudeStructuredArgs(h, spec, args, strings.Contains(claudeHelp(ctx, path), "--system-prompt-snapshot"))
	if err != nil {
		return nil, err
	}
	args = append([]string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose",
		"--permission-prompt-tool", "stdio"}, args...)
	decoder := claudeDecoder{tools: make(map[string]ToolData), wantMode: permission, resumed: spec.Resume != ""}
	p, err := startPipe(spec.Dir, path, args, decoder.decode)
	if err != nil {
		return nil, err
	}
	p.encodePrompt = func(prompt string) []byte {
		b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": prompt}})
		return append(b, '\n')
	}
	p.encodeReply = encodeClaudeReply
	return &claudeProc{pipeProc: p, decoder: &decoder, autoNative: claudeSupportsAuto(ctx, path)}, nil
}

// SetPermissionMode is the stream-json set_permission_mode control request, acknowledged by the
// CLI. bypassPermissions only works when the process was launched with
// --allow-dangerously-skip-permissions; otherwise the conversation has to be resumed.
func (p *claudeProc) SetPermissionMode(mode string) (bool, error) {
	claudeMode, err := claudePermissionMode(mode, p.autoNative)
	if err != nil {
		return false, err
	}
	err = p.controlCall(map[string]any{"subtype": "set_permission_mode", "mode": claudeMode}, true, nil)
	if err != nil && strings.Contains(err.Error(), "was not launched with") {
		return false, ErrRestartRequired
	}
	return false, err
}

// Files keep arbitrary JSON, multiline prompts and large skill indexes out of command
// lines. These are daemon-owned session files, never native Claude configuration.
// snapshot says whether this Claude Code build accepts --system-prompt-snapshot; older
// builds reject it as an unknown option (and have no snapshot to go stale).
func claudeStructuredArgs(h *Host, spec LaunchSpec, args []string, snapshot bool) ([]string, error) {
	if spec.Gateway == nil && spec.SkillsPrompt == "" {
		return args, nil
	}
	dir, err := h.sessionDir(spec.SessionID)
	if err != nil {
		return nil, err
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if spec.Gateway != nil {
		config, err := json.Marshal(map[string]any{"mcpServers": map[string]*Gateway{"orole": spec.Gateway}})
		if err != nil {
			return nil, err
		}
		path := filepath.Join(dir, "claude-mcp.json")
		if err := os.WriteFile(path, config, 0o600); err != nil {
			return nil, fmt.Errorf("write Claude gateway config: %w", err)
		}
		args = append(args, "--mcp-config", path)
	}
	if spec.SkillsPrompt != "" {
		path := filepath.Join(dir, "claude-skills.txt")
		if err := os.WriteFile(path, []byte(spec.SkillsPrompt), 0o600); err != nil {
			return nil, fmt.Errorf("write Claude skills prompt: %w", err)
		}
		args = append(args, "--append-system-prompt-file", path)
	}
	// Claude otherwise reuses the original system-prompt snapshot on resume,
	// ignoring changes to the central skills index (including removal of all skills).
	if snapshot {
		args = append(args, "--system-prompt-snapshot", "off")
	}
	return args, nil
}

func encodeClaudeReply(id string, allow bool, reason string, input json.RawMessage) []byte {
	var resp map[string]any
	if allow {
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
	} else {
		if reason == "" {
			reason = "The user denied this action."
		}
		resp = map[string]any{"behavior": "deny", "message": reason}
	}
	b, _ := json.Marshal(map[string]any{"type": "control_response",
		"response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
	return append(b, '\n')
}

// hookEvents are the Claude Code hook events the daemon listens to in terminal mode.
var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop", "SessionEnd"}

// HookSettings renders the --settings JSON that routes Claude Code hooks to
// `<hookExe> agent-hook <sessionID> <Event>`.
func HookSettings(hookExe, sessionID string) ([]byte, error) {
	exe := filepath.ToSlash(hookExe) // forward slashes survive bash, cmd and PowerShell
	hooks := map[string]any{}
	for _, ev := range hookEvents {
		hooks[ev] = []any{map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": fmt.Sprintf(`"%s" agent-hook %s %s`, exe, sessionID, ev), "timeout": 10,
		}}}}
	}
	return json.MarshalIndent(map[string]any{"hooks": hooks}, "", "  ")
}

func (c Claude) startTerminal(h *Host, spec LaunchSpec, path string, args []string) (Process, error) {
	if h.HookExe != "" {
		dir, err := h.sessionDir(spec.SessionID)
		if err != nil {
			return nil, err
		}
		b, err := HookSettings(h.HookExe, spec.SessionID)
		if err != nil {
			return nil, err
		}
		sp := filepath.Join(dir, "claude-settings.json")
		if err := os.WriteFile(sp, b, 0o600); err != nil {
			return nil, err
		}
		args = append(args, "--settings", filepath.ToSlash(sp))
	}
	pa, err := promptArg(h, spec, path)
	if err != nil {
		return nil, err
	}
	if pa != "" {
		args = append(args, "--", pa)
	}
	line, err := commandLine(path, args...)
	if err != nil {
		return nil, err
	}
	p, err := startPty(h, spec.SessionID, spec.Dir, line, "Claude Code")
	if err != nil {
		return nil, err
	}
	return p, nil
}

// ---- stream-json decoding ----

type claudeMsg struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	// PermissionMode is the mode Claude Code actually started in (system init).
	PermissionMode string `json:"permissionMode"`
	Message        struct {
		ID         string          `json:"id"`
		Role       string          `json:"role"`
		Model      string          `json:"model"`
		Usage      *claudeUsage    `json:"usage"`
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
	Usage      *claudeUsage `json:"usage"`
	ModelUsage map[string]struct {
		ContextWindow *int64 `json:"contextWindow"`
		// Cumulative for the Claude Code process (all turns so far), unlike result.usage.
		Input         *int64   `json:"inputTokens"`
		Output        *int64   `json:"outputTokens"`
		CacheRead     *int64   `json:"cacheReadInputTokens"`
		CacheCreation *int64   `json:"cacheCreationInputTokens"`
		CostUSD       *float64 `json:"costUSD"`
	} `json:"modelUsage"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	Terminal  string `json:"terminal_reason"`
	RequestID string `json:"request_id"`
	Request   struct {
		Subtype   string          `json:"subtype"`
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	} `json:"request"`
	ParentToolUseID *string `json:"parent_tool_use_id"`
	TaskID          string  `json:"task_id"`
	TaskType        string  `json:"task_type"`
	ToolUseID       string  `json:"tool_use_id"`
	Description     string  `json:"description"`
	Status          string  `json:"status"`
	Summary         string  `json:"summary"`
	OutputFile      string  `json:"output_file"`
	Ambient         bool    `json:"ambient"`
}

type claudeUsage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	Cached        *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
	OutputDetails *struct {
		Thinking *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// claudeBilled is one model's cumulative modelUsage reading.
type claudeBilled struct {
	input, output, cacheRead, cacheWrite int64
	cost                                 float64
}

type claudeInput struct {
	wireID  string
	input   json.RawMessage
	request QuestionRequest
}

type claudeProc struct {
	*pipeProc
	decoder *claudeDecoder
	// autoNative is true when this Claude Code build has its own auto mode.
	autoNative bool
}

func (p *claudeProc) AnswerInput(id string, answers []QuestionAnswer, cancel bool) error {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	pending, ok := p.decoder.inputs[id]
	if !ok {
		return fmt.Errorf("no pending question request %q", id)
	}
	if err := adapterValidateAnswers(pending.request, answers, cancel); err != nil {
		return err
	}
	delete(p.decoder.inputs, id)
	if cancel {
		return p.write(encodeClaudeReply(pending.wireID, false, "The user cancelled the question.", nil))
	}
	// Preserve all original input fields and the exact questions array.
	var input map[string]json.RawMessage
	if err := json.Unmarshal(pending.input, &input); err != nil {
		return err
	}
	values := make(map[string]any, len(answers))
	for _, answer := range answers {
		for _, q := range pending.request.Questions {
			if q.ID == answer.QuestionID {
				if q.Multiple {
					values[q.Prompt] = answer.Values
				} else {
					values[q.Prompt] = answer.Values[0]
				}
			}
		}
	}
	input["answers"] = jraw(values)
	return p.write(encodeClaudeReply(pending.wireID, true, "", jraw(input)))
}

func (d *claudeDecoder) askInput(p *pipeProc, m claudeMsg) []Event {
	var input struct {
		Questions []struct {
			Question    string `json:"question"`
			Header      string `json:"header"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label       string `json:"label"`
				Description string `json:"description"`
			} `json:"options"`
		} `json:"questions"`
	}
	req := QuestionRequest{ID: uuid.NewString(), At: time.Now().UTC()}
	valid := len(m.Request.Input) <= 64<<10 && json.Unmarshal(m.Request.Input, &input) == nil
	prompts := make(map[string]bool, len(input.Questions))
	for index, q := range input.Questions {
		// Native answers are keyed by prompt; local IDs stay bounded even for
		// long prompts. Duplicate native keys cannot be answered unambiguously.
		valid = valid && !prompts[q.Question]
		prompts[q.Question] = true
		question := Question{ID: fmt.Sprintf("q-%d", index), Header: q.Header, Prompt: q.Question,
			Multiple: q.MultiSelect, AllowText: true, Options: []QuestionOption{}}
		for _, o := range q.Options {
			question.Options = append(question.Options, QuestionOption{ID: o.Label, Label: o.Label, Description: o.Description})
		}
		req.Questions = append(req.Questions, question)
	}
	data := jraw(req)
	p.pmu.Lock()
	var canceled []Event
	for id, pending := range p.pendingIn {
		if pending.requestID == m.RequestID {
			delete(p.pendingIn, id)
			canceled = append(canceled, Event{Kind: EvPermCancel, Data: jraw(map[string]string{"id": id})})
		}
	}
	if !valid || len(data) > 64<<10 || m.RequestID == "" || !adapterValidQuestions(req.Questions) || len(canceled) > 0 || len(d.inputs)+len(p.pendingIn) >= 64 {
		p.pmu.Unlock()
		_ = p.write(encodeClaudeReply(m.RequestID, false, "Question input is invalid, oversized, or reuses a pending request ID", nil))
		return canceled
	}
	if d.inputs == nil {
		d.inputs = make(map[string]claudeInput)
	}
	d.inputs[req.ID] = claudeInput{wireID: m.RequestID, input: append(json.RawMessage(nil), m.Request.Input...), request: req}
	p.pmu.Unlock()
	return []Event{{Kind: EvQuestion, Data: data}}
}

type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Citations []struct {
		Type          string `json:"type"`
		Title         string `json:"title"`
		DocumentTitle string `json:"document_title"`
		URL           string `json:"url"`
		Source        string `json:"source"`
		CitedText     string `json:"cited_text"`
	} `json:"citations"`
}

// The stdout reader calls decode serially. Keep call metadata only until its
// result so result cards retain tool names even though Claude sends only an ID.
type claudeDecoder struct {
	tools     map[string]ToolData
	inputs    map[string]claudeInput // guarded by pipeProc.pmu
	model     string
	subagents map[string]SubagentData
	// wantMode is the --permission-mode requested at launch; init reports what Claude Code did.
	wantMode string
	// billed is the last modelUsage reading per model; results report its growth as EvTokens.
	billed map[string]claudeBilled
	// resumed is set for a process started with --resume until its first billed result. Claude Code
	// then restores the session's cumulative modelUsage, so that result's growth includes usage an
	// earlier process already billed.
	resumed bool
	// contextSent numbers get_context_usage requests; contextSeen is the newest reply applied.
	contextSent, contextSeen uint64
}

// billedTokens turns a result into EvTokens. modelUsage is cumulative for this process and
// split by model (it includes subagents and side requests), so each model's growth since the
// previous result is billed once, with its reported cost. Without token counts in modelUsage
// the per-turn result.usage is billed for the session's model instead. So is the first result
// of a resumed process, whose modelUsage only becomes the baseline: it may carry the totals an
// earlier process billed, and nothing tells them apart from this turn's growth.
func (d *claudeDecoder) billedTokens(m claudeMsg) []Event {
	var thinking int64
	if m.Usage != nil && m.Usage.OutputDetails != nil {
		thinking = valueOr0(m.Usage.OutputDetails.Thinking)
	}
	var out []Event
	names := make([]string, 0, len(m.ModelUsage))
	for name, u := range m.ModelUsage {
		if u.Input != nil || u.Output != nil || u.CacheRead != nil || u.CacheCreation != nil {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	baseline := d.resumed && len(names) > 0
	if baseline {
		d.resumed = false
	}
	for _, name := range names {
		u := m.ModelUsage[name]
		cur := claudeBilled{input: valueOr0(u.Input), output: valueOr0(u.Output), cacheRead: valueOr0(u.CacheRead), cacheWrite: valueOr0(u.CacheCreation)}
		if u.CostUSD != nil {
			cur.cost = *u.CostUSD
		}
		prev := d.billed[name]
		if cur.input < prev.input || cur.output < prev.output || cur.cacheRead < prev.cacheRead || cur.cacheWrite < prev.cacheWrite {
			prev = claudeBilled{} // a counter went backwards: a new accumulation started
		}
		if d.billed == nil {
			d.billed = map[string]claudeBilled{}
		}
		d.billed[name] = cur
		if baseline {
			continue
		}
		t := TokenUsage{Source: "claude:result:modelUsage", Provider: "anthropic", Model: name,
			Input: cur.input - prev.input, Output: cur.output - prev.output,
			CacheRead: cur.cacheRead - prev.cacheRead, CacheWrite: cur.cacheWrite - prev.cacheWrite}
		if name == m.Model || len(names) == 1 {
			t.Reasoning = thinking
		}
		if u.CostUSD != nil {
			cost := cur.cost - prev.cost
			t.CostUSD = &cost
		}
		out = append(out, tokensEvent(t)...)
	}
	if (len(names) == 0 || baseline) && m.Usage != nil {
		out = tokensEvent(TokenUsage{Source: "claude:result:usage", Provider: "anthropic", Model: m.Model,
			Input: valueOr0(m.Usage.Input), Output: valueOr0(m.Usage.Output), CacheRead: valueOr0(m.Usage.Cached),
			CacheWrite: valueOr0(m.Usage.CacheCreation), Reasoning: thinking})
	}
	return out
}

func (d *claudeDecoder) decode(p *pipeProc, line []byte) []Event {
	if events, ok := d.contextResponse(line); ok {
		return events
	}
	if p != nil && p.acceptControlResponse(line, true) {
		return nil
	}
	var m claudeMsg
	if json.Unmarshal(line, &m) != nil {
		return []Event{{Kind: EvActivity, Text: truncate(string(line), 200)}}
	}
	if m.Model != "" {
		d.model = m.Model
	}
	if m.Type == "assistant" && m.ParentToolUseID == nil && m.Message.Model != "" {
		d.model = m.Message.Model
	}
	if m.Type == "result" {
		m.Model = d.model
	}
	var canceled []Event
	if p != nil && (m.Type == "control_request" || m.Type == "control_cancel_request" || m.Type == "result") {
		p.pmu.Lock()
		for id, pending := range d.inputs {
			if m.Type == "result" || pending.wireID == m.RequestID {
				delete(d.inputs, id)
				canceled = append(canceled, Event{Kind: EvQuestionCancel, Data: jraw(map[string]string{"id": id})})
			}
		}
		p.pmu.Unlock()
		if m.Type == "control_request" && len(canceled) > 0 {
			_ = p.write(encodeClaudeReply(m.RequestID, false, "A pending request ID cannot be reused", nil))
			return canceled
		}
	}
	if m.Type == "control_request" && m.Request.Subtype == "can_use_tool" && m.Request.ToolName == "AskUserQuestion" {
		return d.askInput(p, m)
	}
	if m.Type == "result" {
		canceled = append(canceled, d.billedTokens(m)...)
	}
	events := append(canceled, decodeClaudeMessage(p, m)...)
	// Refresh the breakdown when the session starts (system prompt, tools, memory) and after
	// every turn, which is when messages, tool results and compaction change it.
	if m.Type == "result" || m.Type == "system" && m.Subtype == "init" {
		d.requestContext(p)
	}
	if m.Type == "system" && m.Subtype == "init" && m.PermissionMode != "" && d.wantMode != "" && m.PermissionMode != d.wantMode {
		// Claude Code starts in Manual when auto mode is unavailable to the session.
		events = append(events, Event{Kind: EvSystem, Text: fmt.Sprintf("Claude Code started in %s mode instead of the requested %s.", m.PermissionMode, d.wantMode)})
	}
	if m.Type == "system" && m.TaskID != "" && !m.Ambient {
		switch m.Subtype {
		case "task_started":
			if m.TaskType == "local_agent" || m.TaskType == "remote_agent" {
				if d.subagents == nil {
					d.subagents = make(map[string]SubagentData)
				}
				agent := SubagentData{ID: m.TaskID, Status: "started", Task: m.Description, ToolCallID: m.ToolUseID}
				d.subagents[m.TaskID] = agent
				events = append(events, Event{Kind: EvSubagent, Data: jraw(agent)})
			}
		case "task_notification":
			if agent, ok := d.subagents[m.TaskID]; ok {
				agent.Status, agent.Message = m.Status, m.Summary
				delete(d.subagents, m.TaskID)
				events = append(events, Event{Kind: EvSubagent, Data: jraw(agent)})
			}
			if m.OutputFile != "" {
				events = append(events, Event{Kind: EvFile, Data: jraw(FileData{ID: m.TaskID, Path: m.OutputFile,
					Name: filepath.Base(m.OutputFile), Status: m.Status, Summary: m.Summary})})
			}
		}
	}
	for i := range events {
		ev := &events[i]
		if ev.Kind != EvTool && ev.Kind != EvToolResult {
			continue
		}
		var tool ToolData
		if json.Unmarshal(ev.Data, &tool) != nil || tool.ID == "" {
			continue
		}
		if ev.Kind == EvTool {
			d.tools[tool.ID] = ToolData{Name: tool.Name, Server: tool.Server}
		} else if call, ok := d.tools[tool.ID]; ok {
			tool.Name, tool.Server = call.Name, call.Server
			delete(d.tools, tool.ID)
			ev.Data = jraw(tool)
		}
	}
	return events
}

func claudeToolName(name string) (string, string) {
	if rest, ok := strings.CutPrefix(name, "mcp__"); ok {
		server, tool, found := strings.Cut(rest, "__")
		if found && server != "" && tool != "" {
			return tool, server
		}
	}
	return name, ""
}

func decodeClaudeMessage(p *pipeProc, m claudeMsg) []Event {
	switch m.Type {
	case "system":
		if m.Subtype == "init" {
			return []Event{{Kind: EvInit, Data: jraw(map[string]string{"agentSessionId": m.SessionID, "model": m.Model})}}
		}
	case "assistant":
		var blocks []claudeBlock
		_ = json.Unmarshal(m.Message.Content, &blocks)
		var out []Event
		for index, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					out = append(out, Event{Kind: EvMessage, Text: t})
				}
				var sources []SourceReference
				for citationIndex, citation := range b.Citations {
					source := SourceReference{ID: fmt.Sprintf("%s-%d-%d", m.Message.ID, index, citationIndex),
						Title: citation.Title, Snippet: citation.CitedText}
					switch citation.Type {
					case "web_search_result_location":
						source.URL = citation.URL
					case "search_result_location":
						source.URL = citation.Source
					case "char_location", "page_location", "content_block_location":
						source.Title = citation.DocumentTitle
					default:
						continue
					}
					sources = append(sources, source)
				}
				if len(sources) > 0 {
					out = append(out, Event{Kind: EvSources, Data: jraw(SourcesData{ID: fmt.Sprintf("%s-%d", m.Message.ID, index), Items: sources})})
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					out = append(out, Event{Kind: EvReasoning, Text: b.Thinking,
						Data: jraw(map[string]string{"id": fmt.Sprintf("%s:%d", m.Message.ID, index)})})
				}
			case "tool_use":
				name, server := claudeToolName(b.Name)
				label := name
				if server != "" {
					label = server + "/" + name
				}
				out = append(out, Event{Kind: EvTool, Text: label + ": " + summarizeTool(name, b.Input),
					Data: jraw(ToolData{ID: b.ID, Name: name, Server: server, Status: "running", Input: b.Input})})
			}
		}
		// Billing waits for the turn's result; meanwhile each request's usage (subagents' too,
		// which modelUsage bills as well) keeps the session's token count live.
		if u := m.Message.Usage; u != nil && m.Message.ID != "" {
			n := max(valueOr0(u.Input), 0) + max(valueOr0(u.CacheCreation), 0) + max(valueOr0(u.Output), 0)
			if n > 0 {
				out = append(out, Event{Kind: EvTokenEstimate, Data: jraw(TokenEstimate{ID: m.Message.ID, Tokens: n})})
			}
		}
		if m.ParentToolUseID == nil {
			if u := m.Message.Usage; u != nil {
				usage := ContextUsage{Source: "claude:assistant:usage", InputTokens: u.Input, CachedTokens: u.Cached}
				// Output counts on assistant messages can be placeholders. Only
				// result.usage reports reliable output counts.
				if observedTokens(u.Input) != nil && observedTokens(u.Cached) != nil && observedTokens(u.CacheCreation) != nil {
					context := *u.Input + *u.Cached + *u.CacheCreation
					if context >= *u.Input && context >= *u.Cached && context >= *u.CacheCreation {
						usage.ContextTokens = &context
					}
				}
				out = append(out, usageEvent(usage)...)
			}
		}
		if m.Message.StopReason == "refusal" {
			out = append(out, Event{Kind: EvGuardrail, Data: jraw(GuardrailData{ID: m.Message.ID,
				Kind: "refusal", Message: "The provider reported a refusal.", Source: "claude:message.stop_reason"})})
		}
		return out
	case "user":
		var blocks []claudeBlock
		if json.Unmarshal(m.Message.Content, &blocks) == nil {
			var out []Event
			for _, b := range blocks {
				if b.Type == "tool_result" {
					status := "ok"
					if b.IsError {
						status = "error"
					}
					output := toolResultText(b.Content)
					out = append(out, Event{Kind: EvToolResult, Text: truncate(output, 400),
						Data: jraw(ToolData{ID: b.ToolUseID, Status: status, Output: truncate(output, 8<<10)})})
				}
			}
			return out
		}
	case "control_request":
		if m.Request.Subtype == "can_use_tool" {
			p.pmu.Lock()
			var canceled []Event
			for id, pending := range p.pendingIn {
				if pending.requestID == m.RequestID {
					delete(p.pendingIn, id)
					canceled = append(canceled, Event{Kind: EvPermCancel, Data: jraw(map[string]string{"id": id})})
				}
			}
			if len(canceled) > 0 || m.RequestID == "" || len(m.Request.Input) > 64<<10 || !json.Valid(m.Request.Input) {
				p.pmu.Unlock()
				_ = p.write(encodeClaudeReply(m.RequestID, false, "Tool input is invalid, oversized, or reuses a pending request ID", nil))
				return canceled
			}
			// UI IDs identify immutable snapshots, never agent-controlled/reused IDs.
			id := uuid.NewString()
			p.pendingIn[id] = pendingPermission{requestID: m.RequestID, input: m.Request.Input}
			p.pmu.Unlock()
			return []Event{{Kind: EvPermission, Text: m.Request.ToolName, Data: jraw(PermissionRequest{
				ID: id, Tool: m.Request.ToolName, Summary: summarizeTool(m.Request.ToolName, m.Request.Input),
				Input: m.Request.Input, InputJSON: string(m.Request.Input), At: time.Now().UTC()})}}
		}
		// Anything else would block the CLI forever; refuse explicitly.
		b, _ := json.Marshal(map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "error", "request_id": m.RequestID, "error": "unsupported control request: " + m.Request.Subtype}})
		_ = p.write(append(b, '\n'))
	case "control_cancel_request":
		p.pmu.Lock()
		var canceled []Event
		for id, pending := range p.pendingIn {
			if pending.requestID == m.RequestID {
				delete(p.pendingIn, id)
				canceled = append(canceled, Event{Kind: EvPermCancel, Data: jraw(map[string]string{"id": id})})
			}
		}
		p.pmu.Unlock()
		return canceled
	case "result":
		ok := !m.IsError && (m.Subtype == "" || m.Subtype == "success")
		res := strings.TrimSpace(m.Result)
		if !ok && res == "" {
			res = strings.TrimSpace(m.Subtype + " " + m.Terminal)
		}
		var out []Event
		usage := ContextUsage{Source: "claude:result:usage"}
		if m.Usage != nil {
			usage.InputTokens, usage.OutputTokens, usage.CachedTokens = m.Usage.Input, m.Usage.Output, m.Usage.Cached
		}
		if model, ok := m.ModelUsage[m.Model]; ok {
			usage.ContextWindow = model.ContextWindow
		} else if m.Model == "" && len(m.ModelUsage) == 1 {
			for _, model := range m.ModelUsage {
				usage.ContextWindow = model.ContextWindow
			}
		}
		out = append(out, usageEvent(usage)...)
		return append(out, Event{Kind: EvTurnDone, Text: res, Data: jraw(map[string]any{"ok": ok, "result": res, "subtype": m.Subtype})})
	}
	return nil
}

func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

// summarizeTool is a one-line description of a tool call for the UI.
func summarizeTool(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	pick := ""
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "description", "prompt"} {
		if v := str(k); v != "" {
			pick = v
			break
		}
	}
	if pick == "" {
		return name
	}
	return truncate(strings.Join(strings.Fields(pick), " "), 240)
}

// NewClaudeSessionID makes a fresh CLI session id.
func NewClaudeSessionID() string { return uuid.NewString() }
