package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/chat/extract"
	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

const (
	defaultMaxSteps     = 8
	defaultMaxToolCalls = 20
	// A bot with its own computer works in longer loops (shell, browser), so its defaults are higher.
	computerMaxSteps     = 30
	computerMaxToolCalls = 60
	doomLoopRepeats      = 3 // the third identical tool call is not executed (see repeated)
	maxToolResultBytes   = 100 << 10
	toolSummaryBytes     = 160
)

// turnEnv is everything one model/tool loop needs.
type turnEnv struct {
	bot       Bot
	rc        RunContext
	provider  llm.Provider
	modelID   string
	reasoning string
	system    string
	history   []llm.Message // ends with the current user message
	tools     toolset
	sink      *sink
	onText    func(string)             // voice: every text delta, as it streams
	onTool    func(name, label string) // voice: a tool name and its label (see ToolLabeler), just before the tool runs
}

// toolset is the tool registry for one run: specs offered to the model and the client tools that
// answer them. Hosted specs pass through untouched and have no route.
type toolset struct {
	specs []llm.ToolSpec
	route map[string]Tool
}

// buildToolset collects the specs every registered tool offers for this bot and model.
func (m *Manager) buildToolset(bot Bot, caps llm.Caps, draft bool) toolset {
	m.mu.Lock()
	tools := m.toolsLocked(draft)
	m.mu.Unlock()
	ts := toolset{route: map[string]Tool{}}
	for _, t := range tools {
		for _, sp := range t.Specs(bot, caps) {
			if sp.Hosted != "" {
				if !slices.ContainsFunc(ts.specs, func(x llm.ToolSpec) bool { return x.Hosted == sp.Hosted }) {
					ts.specs = append(ts.specs, sp)
				}
				continue
			}
			if sp.Name == "" || !caps.Tools {
				continue
			}
			if _, dup := ts.route[sp.Name]; dup {
				m.log.Warn("chat: duplicate tool name ignored", "name", sp.Name, "tool", t.ID())
				continue
			}
			ts.route[sp.Name] = t
			ts.specs = append(ts.specs, sp)
		}
	}
	return ts
}

// runTurn answers one stored user message: it creates (or resets) the assistant message, runs the
// engine and stores the terminal state. It never returns an error; failures end up on the message.
// A delegated turn (t.result set) also reports its outcome to the asking bot, whatever happens.
// A call thread turn (t.callID set) keeps its reply in that call's thread and out of the chat.
func (m *Manager) runTurn(ctx context.Context, botID string, t turn) {
	res := handoffResult{status: StatusError, errText: "the task could not be started"}
	defer func() { t.deliver(res) }()
	bg := context.WithoutCancel(ctx)
	convo, err := m.store.getBot(bg, botID, false)
	if err != nil {
		m.log.Warn("chat: run skipped", "bot", botID, "err", err)
		_ = m.store.setStatus(bg, t.userMsgID, StatusCanceled, "Not sent: the bot no longer exists.")
		res = handoffResult{status: StatusCanceled, errText: "the bot no longer exists"}
		return
	}
	bot := convo
	if t.responder != "" {
		if bot, err = m.store.getBot(bg, t.responder, false); err != nil {
			m.log.Warn("chat: mention skipped", "bot", t.responder, "err", err)
			gone := Message{ID: newID(), BotID: botID, Role: "assistant", Author: "bot:" + t.responder, Status: StatusError, Error: "The mentioned bot no longer exists.", CreatedAt: m.nowMs(), CallID: t.callID}
			if m.store.insertMessage(bg, &gone) == nil {
				m.publishAny(gone)
			}
			return
		}
	}
	user, err := m.store.getMessage(bg, t.userMsgID)
	if err != nil {
		m.log.Warn("chat: run skipped", "bot", botID, "err", err)
		return
	}
	if user.Status != StatusComplete {
		user.FinishedAt, _ = m.store.setStatusAt(bg, user.ID, StatusComplete, "", m.nowMs())
		user.Status = StatusComplete
		user.Error = ""
		m.publishAny(user)
	}
	var asst Message
	if t.asstID != "" {
		if err = m.store.resetMessage(bg, t.asstID); err == nil {
			asst, err = m.store.getMessage(bg, t.asstID)
		}
	} else {
		asst = Message{ID: newID(), BotID: botID, Role: "assistant", Author: "bot:" + bot.ID, Status: StatusRunning, CreatedAt: m.nowMs(), CallID: t.callID}
		err = m.store.insertMessage(bg, &asst)
	}
	if err != nil {
		m.log.Error("chat: cannot create reply", "bot", botID, "err", err)
		return
	}
	persist := func(msg Message) error {
		pc, cancel := context.WithTimeout(bg, 10*time.Second)
		defer cancel()
		return m.store.updateMessage(pc, msg)
	}
	asst.Automations = slices.Clone(t.automations)
	publish, publishDelta := m.publishMessage, m.publishDelta
	if t.callID != "" {
		publish, publishDelta = m.publishCallMessage, m.publishCallDelta
	}
	s := newSink(asst, persist, publish, publishDelta)
	s.clock = m.cfg.Clock
	if t.callID == "" {
		m.emit("chat.run", RunState{BotID: botID, Running: true, MessageID: asst.ID})
	}
	s.checkpoint()

	rc := RunContext{BotID: bot.ID, Bot: bot, MessageID: asst.ID, Actor: "bot:" + bot.ID, Depth: t.depth, Chain: t.chain,
		Automations: slices.Clone(t.automations), Author: user.Author, Emit: s.upsertTool,
		// A call thread answer writes into the call's thread but is not spoken: no CallID, so the call tools refuse
		// and call_owner works after the call; work it starts reports into the same thread.
		CallThread: t.callID}
	status, errText := m.executeTurn(ctx, bot, convo, rc, user, t, s)
	if status == StatusCanceled && m.ctx.Err() != nil {
		status = StatusIncomplete // the daemon is shutting down, not the user stopping the run
	}
	if err := s.finish(status, errText); err != nil {
		m.log.Error("chat: cannot store reply", "bot", botID, "err", err)
	}
	res = handoffResult{text: s.finalText(), status: status, errText: errText}
}

func messageText(parts []MsgPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == PartText {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func (m *Manager) executeTurn(ctx context.Context, bot, convo Bot, rc RunContext, user Message, t turn, s *sink) (string, string) {
	text := messageText(user.Parts)
	if fenced, ok := fenceAuthored(user.Role, user.Author, text); ok {
		text = fenced
	}
	if t.mode == ModeResearch {
		return m.runResearch(ctx, bot, rc, text, s)
	}
	var atts []MsgPart
	for _, p := range user.Parts {
		if p.Type == PartAttachment {
			atts = append(atts, p)
		}
	}
	env, err := m.prepare(ctx, prep{bot: bot, convo: convo.ID, rc: rc, sink: s, text: text, attachments: atts, userSeq: user.Seq, memory: true, extra: strings.TrimSpace(answerExtra(t, bot, convo, m.builtinSkills(), m.integrationNames(ctx, t.integrations)) + "\n\n" + m.handoffsNote(convo.ID))})
	if err != nil {
		if ctx.Err() != nil {
			return StatusCanceled, ""
		}
		return StatusError, describeError(err)
	}
	return m.loop(ctx, env)
}

// prep describes the inputs of one model run.
type prep struct {
	convo       string // conversation whose history is loaded; "" = the bot's own
	bot         Bot    // ID is "" in drafts
	rc          RunContext
	sink        *sink
	text        string
	attachments []MsgPart
	// userSeq > 0 loads stored history before that message (with summary and compaction);
	// otherwise history is used as given (drafts, delegated runs).
	userSeq int64
	history []llm.Message
	memory  bool // use the memory snapshot and recall for this run
	draft   bool
	extra   string // system prompt addendum
	// voice makes this a phone-call turn: spoken style, cheapest reasoning, tight limits.
	voice bool
	// language is the call language of a voice turn (VoiceRequest.Language).
	language string
	onText   func(string)
	onTool   func(name, label string)
}

// prepare resolves the model, gates attachments by capability, gathers memory and history and
// builds the system prompt. Every failure happens before the provider is called.
func (m *Manager) prepare(ctx context.Context, p prep) (*turnEnv, error) {
	providerID, mdl, err := m.modelInfo(ctx, p.bot.Provider, p.bot.Model)
	if err != nil {
		return nil, err
	}
	prov, err := m.cfg.LLM.Provider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	p.sink.setModel(providerID + "/" + mdl.ID)
	p.rc.Caps = mdl.Caps

	atts, err := m.resolveAttachments(ctx, p.attachments, mdl.Caps)
	if err != nil {
		return nil, err
	}

	var snapshot, recall string
	if mem := m.memoryOrNil(); mem != nil && p.memory && p.bot.ID != "" {
		snapshot, err = mem.Snapshot(ctx, p.bot)
		if err != nil {
			p.sink.addPart(MsgPart{Type: PartNotice, Text: "Memory could not be loaded for this reply: " + untrusted.Line(err.Error(), 200)})
			snapshot = ""
		}
		recall, err = mem.Recall(ctx, p.bot, p.text)
		if err != nil {
			p.sink.addPart(MsgPart{Type: PartNotice, Text: "Memory search failed for this reply: " + untrusted.Line(err.Error(), 200)})
			recall = ""
		}
	}

	fromUser := p.rc.Author == "user" && !p.draft && p.bot.ID != ""
	in := systemInput{Bot: p.bot, Sections: m.promptSections(p.bot, fromUser), Snapshot: snapshot, Recall: recall, Extra: p.extra, Voice: p.voice, Language: p.language}
	cur := llm.Message{Role: llm.RoleUser, Parts: userParts(p.text, atts)}
	history := p.history
	if p.userSeq > 0 {
		convoID := p.convo
		if convoID == "" {
			convoID = p.bot.ID
		}
		base := estimateTokens(buildSystem(in)) + estimateTokens(p.text) + attachmentTokens(atts) + 4
		fit, err := m.fitHistory(ctx, p.bot, convoID, mdl.Caps.ContextWindow, base, p.userSeq)
		if err != nil {
			return nil, err
		}
		for _, n := range fit.notices {
			p.sink.addPart(MsgPart{Type: PartNotice, Text: n})
		}
		in.Summary = fit.summary
		history = historyMessages(m.attribute(ctx, fit.msgs, p.bot.ID))
	}
	history = append(slices.Clone(history), cur)
	if n := len(history); n > 1 && history[n-2].Role == llm.RoleUser {
		// merge a trailing history user message with the current one
		history[n-2].Parts = append(slices.Clone(history[n-2].Parts), cur.Parts...)
		history = history[:n-1]
	}
	env := &turnEnv{
		bot: p.bot, rc: p.rc, provider: prov, modelID: mdl.ID, system: buildSystem(in), history: history,
		tools: m.buildToolset(p.bot, mdl.Caps, p.draft), sink: p.sink,
	}
	if p.bot.Reasoning != "" && slices.Contains(mdl.Reasoning, p.bot.Reasoning) {
		env.reasoning = p.bot.Reasoning
	}
	if p.voice {
		env.reasoning = voiceReasoning(mdl.Reasoning)
		env.bot.Config.Limits = voiceLimits(p.bot.Config.Limits)
		env.onText, env.onTool = p.onText, p.onTool
	}
	return env, nil
}

// resolveAttachments loads the current message's attachments from the blob store and refuses the
// run when the model cannot take them.
func (m *Manager) resolveAttachments(ctx context.Context, parts []MsgPart, caps llm.Caps) ([]attachmentInput, error) {
	out := make([]attachmentInput, 0, len(parts))
	for _, p := range parts {
		a, err := m.up.get(ctx, p.AttachmentID)
		if err != nil {
			return nil, fmt.Errorf("The attachment %q is no longer available.", p.Name)
		}
		data, err := m.up.readBlob(a.SHA, 0)
		if err != nil {
			return nil, fmt.Errorf("The attachment %q could not be read: %w", a.Name, err)
		}
		mime, kind := extract.Sniff(a.Name, data)
		switch {
		case kind == extract.KindImage && !caps.Vision:
			return nil, fmt.Errorf("This model cannot read images, so %q was not sent. Choose a vision-capable model in the bot settings or remove the image.", a.Name)
		case kind == extract.KindPDF && !caps.PDF:
			return nil, fmt.Errorf("This model cannot read PDFs, so %q was not sent. Choose a model with PDF support or attach the text instead.", a.Name)
		case kind == extract.KindUnsupported:
			return nil, fmt.Errorf("%q is not a supported file type.", a.Name)
		}
		out = append(out, attachmentInput{Name: a.Name, MIME: mime, Kind: kind, Data: data})
	}
	return out, nil
}

// streamResult is what one provider call produced.
type streamResult struct {
	text     string
	calls    []llm.ToolCall
	stop     llm.StopReason
	err      *llm.Error
	canceled bool
}

// loop runs model calls and client tools until the model stops, a budget is hit, an error occurs or
// ctx is canceled. The last permitted model call is made without tools so a turn can end in an
// answer; a model that still asks for tools then is stopped with a notice.
func (m *Manager) loop(ctx context.Context, env *turnEnv) (string, string) {
	maxSteps, maxCalls := turnBudget(env.bot.Config)
	msgs := slices.Clone(env.history)
	used := 0
	seen := map[string]int{}
	lastKey, streak := "", 0 // the previous tool call and how many times in a row it was made
	imageMsgs := []int{}     // indexes in msgs of the transient tool-image messages
	for step := 1; ; step++ {
		if ctx.Err() != nil {
			return StatusCanceled, ""
		}
		final := step >= maxSteps || used >= maxCalls
		req := llm.Request{Model: env.modelID, System: env.system, Messages: msgs, Reasoning: env.reasoning}
		if !final && len(env.tools.specs) > 0 {
			req.Tools = env.tools.specs
			req.MaxHostedToolCalls = maxCalls - used
		}
		res := m.consume(ctx, env, step, req, &used)
		switch {
		case res.canceled:
			return StatusCanceled, ""
		case res.err != nil:
			return StatusError, userMessage(res.err)
		}
		if len(res.calls) == 0 {
			switch res.stop {
			case llm.StopLength:
				env.sink.addPart(MsgPart{Type: PartNotice, Text: "The reply was cut off because it reached the model's output limit."})
			case llm.StopFilter:
				return StatusError, "The provider blocked this reply."
			}
			return StatusComplete, ""
		}
		if final {
			if step >= maxSteps {
				env.sink.addPart(MsgPart{Type: PartNotice, Text: fmt.Sprintf("Stopped after %d model calls (the limit for this bot). Ask again to continue.", maxSteps)})
			} else {
				env.sink.addPart(MsgPart{Type: PartNotice, Text: fmt.Sprintf("Stopped: this turn reached its limit of %d tool calls. Ask again to continue.", maxCalls)})
			}
			return StatusComplete, ""
		}

		asst := llm.Message{Role: llm.RoleAssistant}
		if res.text != "" {
			asst.Parts = append(asst.Parts, llm.Part{Kind: llm.PartText, Text: res.text})
		}
		for _, c := range res.calls {
			asst.Parts = append(asst.Parts, llm.Part{Kind: llm.PartToolCall, ToolCallID: c.ID, ToolName: c.Name, Args: c.Args})
		}
		results := llm.Message{Role: llm.RoleTool}
		asked := false
		var images []llm.Part
		for _, c := range res.calls {
			key := callKey(c)
			if key == lastKey {
				streak++
			} else {
				lastKey, streak = key, 1
			}
			seen[key]++
			if repeated(c.Name, seen[key], streak) {
				env.sink.addPart(MsgPart{Type: PartNotice, Text: fmt.Sprintf("Stopped: the model kept repeating the same %s call without making progress.", c.Name)})
				return StatusComplete, ""
			}
			if asked {
				// One question at a time: the user answers before anything else happens.
				results.Parts = append(results.Parts, llm.Part{Kind: llm.PartToolResult, ToolCallID: c.ID, ToolName: c.Name, IsError: true,
					Text: "A question is already waiting for the user's answer; this call was not executed."})
				continue
			}
			if used >= maxCalls {
				results.Parts = append(results.Parts, llm.Part{Kind: llm.PartToolResult, ToolCallID: c.ID, ToolName: c.Name, IsError: true,
					Text: "Tool call budget reached; this call was not executed."})
				continue
			}
			used++
			part, q, imgs := m.runTool(ctx, env, step, c)
			images = append(images, imgs...)
			results.Parts = append(results.Parts, part)
			asked = q
			if ctx.Err() != nil {
				return StatusCanceled, ""
			}
		}
		if err := env.sink.checkpoint(); err != nil {
			m.log.Warn("chat: checkpoint failed", "err", err)
		}
		if asked {
			return StatusComplete, "" // the answer arrives as the next message
		}
		msgs = append(msgs, asst, results)
		if len(images) > MaxToolImages {
			images = images[:MaxToolImages]
		}
		if len(images) > 0 {
			// Transient: msgs lives for this turn only; the images are never stored or logged. Only the latest step's
			// images stay live: earlier ones become placeholders so a long computer turn does not re-send every
			// screenshot on every call.
			msgs = omitImages(msgs, imageMsgs)
			imageMsgs = append(imageMsgs, len(msgs))
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Parts: append([]llm.Part{{Kind: llm.PartText, Text: toolImagesNote}}, images...)})
		}
	}
}

// omitImages returns msgs with the image parts of the messages at idx replaced by a text placeholder. It copies what
// it changes: earlier requests share msgs' backing array.
func omitImages(msgs []llm.Message, idx []int) []llm.Message {
	if len(idx) == 0 {
		return msgs
	}
	msgs = slices.Clone(msgs)
	for _, i := range idx {
		parts := slices.Clone(msgs[i].Parts)
		for j, p := range parts {
			if p.Kind == llm.PartImage {
				parts[j] = llm.Part{Kind: llm.PartText, Text: omittedToolImage}
			}
		}
		msgs[i].Parts = parts
	}
	return msgs
}

// turnBudget resolves a bot's per-turn model-call and tool-call budgets. Unset limits use the engine
// defaults, or the larger computer defaults when the bot has its own computer.
func turnBudget(c BotConfig) (steps, calls int) {
	defSteps, defCalls := defaultMaxSteps, defaultMaxToolCalls
	if c.Computer {
		defSteps, defCalls = computerMaxSteps, computerMaxToolCalls
	}
	return orDefault(c.Limits.MaxSteps, defSteps), orDefault(c.Limits.MaxToolCalls, defCalls)
}

func orDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

// callKey identifies a tool call by name and canonical arguments for loop detection.
func callKey(c llm.ToolCall) string {
	var buf strings.Builder
	buf.WriteString(c.Name)
	buf.WriteByte(0)
	var v any
	if json.Unmarshal(c.Args, &v) == nil {
		if b, err := json.Marshal(v); err == nil { // map keys are sorted: argument order does not matter
			buf.Write(b)
			return buf.String()
		}
	}
	buf.Write(c.Args)
	return buf.String()
}

// consume streams one provider call into the sink.
func (m *Manager) consume(ctx context.Context, env *turnEnv, step int, req llm.Request, used *int) streamResult {
	var res streamResult
	ch, err := env.provider.Stream(ctx, req)
	if err != nil {
		return failedStream(ctx, err)
	}
	var text strings.Builder
	hosted := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			go drain(ch)
			res.canceled = true
			return res
		case d, ok := <-ch:
			if !ok {
				res.text = text.String()
				if ctx.Err() != nil {
					res.canceled = true
				} else if res.stop == "" {
					res.err = &llm.Error{Code: llm.ErrNetwork, Message: "The reply ended unexpectedly. Try again."}
				}
				return res
			}
			switch d.Kind {
			case llm.DeltaText:
				text.WriteString(d.Text)
				env.sink.appendText(PartText, d.Text)
				if env.onText != nil {
					env.onText(d.Text)
				}
			case llm.DeltaReasoning:
				env.sink.appendText(PartReasoning, d.Text)
			case llm.DeltaToolCall:
				if d.Call != nil {
					res.calls = append(res.calls, *d.Call)
				}
			case llm.DeltaServer:
				if d.Server == nil {
					break
				}
				id := fmt.Sprintf("h%d-%s", step, d.Server.ID)
				if !hosted[id] {
					hosted[id] = true
					*used++
				}
				env.sink.upsertTool(MsgPart{Type: PartTool, ID: id, Name: d.Server.Tool, Status: d.Server.Status, Summary: untrusted.Line(d.Server.Detail, toolSummaryBytes)})
			case llm.DeltaCitation:
				if d.Citation != nil {
					env.sink.addCitation(*d.Citation)
				}
			case llm.DeltaUsage:
				if d.Usage != nil {
					env.sink.addUsage(*d.Usage)
				}
			case llm.DeltaStop:
				res.stop = d.Stop
			case llm.DeltaError:
				go drain(ch)
				res.text = text.String()
				e := d.Err
				if e == nil {
					e = &llm.Error{Code: llm.ErrServer, Message: "The provider reported an error."}
				}
				if e.Code == llm.ErrCanceled || ctx.Err() != nil {
					res.canceled = true
				} else {
					res.err = e
				}
				return res
			}
		}
	}
}

func failedStream(ctx context.Context, err error) streamResult {
	e := llm.AsError(err)
	if e.Code == llm.ErrCanceled || ctx.Err() != nil {
		return streamResult{canceled: true}
	}
	if errors.Is(err, llm.ErrNotConfigured) {
		return streamResult{err: &llm.Error{Code: llm.ErrAuth, Message: "The model provider is not connected."}}
	}
	return streamResult{err: e}
}

func drain(ch <-chan llm.Delta) {
	for range ch {
	}
}

// runTool executes one client tool call and records its row and parts on the message.
// A tool that shows the user a question reports it as asked: the turn must then end and wait for the answer.
// It also returns the tool's usable images (ToolResult.Images), only when the model of this turn has vision.
func (m *Manager) runTool(ctx context.Context, env *turnEnv, step int, c llm.ToolCall) (llm.Part, bool, []llm.Part) {
	if env.onTool != nil {
		label := ""
		if l, ok := env.tools.route[c.Name].(ToolLabeler); ok {
			label = l.ToolLabel(ctx, env.rc, c.Name)
		}
		env.onTool(c.Name, label)
	}
	row := MsgPart{Type: PartTool, ID: fmt.Sprintf("s%d-%s", step, c.ID), Name: c.Name, Status: "running"}
	env.sink.upsertTool(row)
	start := time.Now()

	var res ToolResult
	var err error
	args := c.Args
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	switch tool := env.tools.route[c.Name]; {
	case tool == nil:
		err = fmt.Errorf("unknown tool %q", c.Name)
	case !json.Valid(args):
		err = errors.New("the tool arguments were not valid JSON")
	default:
		res, err = callTool(ctx, tool, env.rc, c.Name, args)
	}

	row.DurationMs = time.Since(start).Milliseconds()
	part := llm.Part{Kind: llm.PartToolResult, ToolCallID: c.ID, ToolName: c.Name}
	switch {
	case err != nil:
		part.IsError = true
		part.Text = "Tool error: " + clip(err.Error(), 2000)
		row.Status = "error"
		row.Summary = untrusted.Line(err.Error(), toolSummaryBytes)
	default:
		text := clip(res.Text, maxToolResultBytes)
		if res.Untrusted {
			text = Fence("tool:"+c.Name, text)
		}
		part.Text = text
		part.IsError = res.IsError
		row.Status = "done"
		if o := m.toolObserver(); o != nil && !res.IsError {
			o.ObserveTool(ctx, env.rc, c.Name, args)
		}
		if res.IsError {
			row.Status = "error"
		}
		row.Summary = untrusted.Line(res.Text, toolSummaryBytes)
	}
	env.sink.upsertTool(row)
	asked := false
	for _, p := range res.Parts {
		env.sink.addPart(p)
		asked = asked || p.Type == PartQuestion
	}
	var images []llm.Part
	if err == nil && env.rc.Caps.Vision {
		for _, img := range res.Images {
			if img.Kind == llm.PartImage && toolImageMIME[img.MIME] && len(img.Data) > 0 && len(img.Data) <= MaxToolImageBytes {
				images = append(images, img)
			}
		}
	}
	return part, asked, images
}

// callTool runs a tool and converts a panic into an error: one broken tool must not take the daemon down.
func callTool(ctx context.Context, t Tool, rc RunContext, name string, args json.RawMessage) (res ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s crashed: %v", name, r)
		}
	}()
	return t.Call(ctx, rc, name, args)
}

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "\n[truncated]"
}

// runResearch dispatches research mode to the "research" tool instead of a normal model turn.
func (m *Manager) runResearch(ctx context.Context, bot Bot, rc RunContext, question string, s *sink) (string, string) {
	providerID, mdl, err := m.modelInfo(ctx, bot.Provider, bot.Model)
	if err != nil {
		if ctx.Err() != nil {
			return StatusCanceled, ""
		}
		return StatusError, describeError(err)
	}
	s.setModel(providerID + "/" + mdl.ID)
	rc.Caps = mdl.Caps
	m.mu.Lock()
	tools := m.toolsLocked(false)
	m.mu.Unlock()
	var tool Tool
	name := ToolResearch
	for _, t := range tools {
		if t.ID() != ToolResearch {
			continue
		}
		specs := t.Specs(bot, mdl.Caps)
		if len(specs) == 0 {
			continue
		}
		tool = t
		for _, sp := range specs {
			if sp.Hosted == "" && sp.Name != "" {
				name = sp.Name
				break
			}
		}
		break
	}
	if tool == nil {
		return StatusError, "Research is not available for this bot. Turn on the Research tool in the bot settings and pick a model that supports it."
	}
	args, _ := json.Marshal(map[string]string{"question": question})
	res, err := callTool(ctx, tool, rc, name, args)
	if ctx.Err() != nil {
		return StatusCanceled, ""
	}
	if err != nil {
		return StatusError, "Research failed: " + untrusted.Line(err.Error(), 400)
	}
	if res.IsError {
		return StatusError, "Research failed: " + untrusted.Line(res.Text, 400)
	}
	for _, p := range res.Parts {
		s.addPart(p)
	}
	if strings.TrimSpace(res.Text) != "" && !slices.ContainsFunc(res.Parts, func(p MsgPart) bool { return p.Type == PartReport && p.Text == res.Text }) {
		s.addPart(MsgPart{Type: PartText, Text: res.Text})
	}
	return StatusComplete, ""
}

// userMessage turns a provider error into text for the user.
func userMessage(e *llm.Error) string {
	msg := strings.TrimSpace(e.Message)
	switch e.Code {
	case llm.ErrAuth:
		if msg == "" {
			msg = "The provider rejected the API key."
		}
		return msg + " Reconnect the provider in Settings > Models."
	case llm.ErrQuota:
		if msg == "" {
			msg = "The provider reports no remaining credits or a billing problem."
		}
		return msg + " Check your balance with the provider."
	case llm.ErrRateLimit:
		if msg == "" {
			msg = "The provider is rate limiting requests."
		}
		if e.RetryAfter > 0 {
			return fmt.Sprintf("%s Try again in %d seconds.", msg, int((e.RetryAfter+time.Second-1)/time.Second))
		}
		return msg + " Try again shortly."
	case llm.ErrContextLength:
		if msg == "" {
			msg = "The conversation is too long for this model."
		}
		return msg + " Try a model with a larger context window or clear the conversation."
	}
	if msg == "" {
		return "The provider returned an error (" + string(e.Code) + ")."
	}
	return msg
}

// describeError renders any preparation error for the user.
func describeError(err error) string {
	var le *llm.Error
	switch {
	case errors.As(err, &le):
		return userMessage(le)
	case errors.Is(err, llm.ErrNotConfigured):
		return "The model provider is not connected. Connect it in Settings > Models."
	}
	return err.Error()
}
