package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// Sentinel errors. Handlers map them to IPC codes; messages are safe to show to the user.
var (
	ErrInvalid  = errors.New("invalid request")
	ErrNotFound = errors.New("not found")
	ErrBusy     = errors.New("busy")
	ErrClosed   = errors.New("chat is shutting down")
)

// userError carries a user-facing message and the sentinel it maps to.
type userError struct {
	kind error
	msg  string
}

func (e *userError) Error() string { return e.msg }
func (e *userError) Unwrap() error { return e.kind }

func invalidf(format string, args ...any) error {
	return &userError{kind: ErrInvalid, msg: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...any) error {
	return &userError{kind: ErrNotFound, msg: fmt.Sprintf(format, args...)}
}

func busyf(format string, args ...any) error {
	return &userError{kind: ErrBusy, msg: fmt.Sprintf(format, args...)}
}

const (
	storeTimeout = 10 * time.Second // bounds the store work of a report delivered in the background
	// answerLaterFor is how long AnswerFollowUp's request for a follow-up not stored yet is kept: far longer than the
	// follow-up's own way into the store (its hook, then at most storeTimeout).
	answerLaterFor     = 5 * time.Minute
	maxQueued          = 20 // queued user messages per bot
	maxDelegationDepth = 2
	defaultIdleAfter   = 2 * time.Minute
	catalogTTL         = 5 * time.Minute
)

// Config wires the Manager.
type Config struct {
	DB      *sql.DB
	DataDir string
	LLM     llm.Service
	// Publish delivers chat.message, chat.run and chat.bot events. It must not block and must not
	// call back into the Manager.
	Publish func(event string, payload any)
	Log     *slog.Logger
	Clock   func() time.Time
	// IdleConsolidateAfter is how long a bot must be idle before Memory.Consolidate runs (default 2 minutes).
	IdleConsolidateAfter time.Duration
}

// Manager owns bots, conversations, runs, attachments and the tool registry. Policy decisions for
// individual tool calls live in the tools themselves (they receive the actor "bot:<id>").
type Manager struct {
	saveCheck SaveCheck // guarded by mu; see SetSaveCheck
	cfg       Config
	log       *slog.Logger
	store     *store
	up        *uploads

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	memory   Memory
	tools    []Tool
	sections []func(Bot) string
	// userSections are added only to runs that answer the user's own message (not drafts,
	// automation messages, reports or hand-offs).
	userSections []func(Bot) string
	observer     ToolObserver
	followUp     FollowUpHook                            // see SetFollowUpHook; nil = no follow-up reaches a call
	builtin      func() []string                         // enabled built-in skill names every bot may use; nil when there is no registry
	mcpNames     func(context.Context) map[string]string // MCP server id -> display name; nil when there are no integrations
	runs         map[string]*runState                    // active runs by bot id or "draft:<id>"
	handoffs     map[string][]*handoff                   // bot id -> tasks it handed to other bots that have not reported back
	// answerLater holds the message ids of follow-ups AnswerFollowUp was asked to answer before they were stored, with
	// when it was asked; a request older than answerLaterFor is dropped (that message is never stored).
	answerLater map[string]time.Time
	idle        map[string]*idleJob
	nonces      nonceCache
	closed      bool

	botMu sync.Map // bot id -> *sync.Mutex, see lockBot

	catMu   sync.Mutex
	catalog map[string]catalogEntry

	sumMu   sync.Mutex
	sumFail map[string]summaryFailure // failed summarisations by conversation, see summaryBackoff

	closeOnce sync.Once
}

type catalogEntry struct {
	at     time.Time
	models []llm.Model
}

// New opens the chat service: it marks runs interrupted by a previous shutdown as incomplete,
// clears orphaned upload staging and starts the attachment expiry sweeper.
func New(cfg Config) (*Manager, error) {
	if cfg.DB == nil {
		return nil, errors.New("chat: database is required")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("chat: data directory is required")
	}
	if cfg.LLM == nil {
		return nil, errors.New("chat: llm service is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.IdleConsolidateAfter <= 0 {
		cfg.IdleConsolidateAfter = defaultIdleAfter
	}
	m := &Manager{
		cfg:         cfg,
		log:         cfg.Log,
		store:       &store{db: cfg.DB, now: cfg.Clock},
		runs:        map[string]*runState{},
		handoffs:    map[string][]*handoff{},
		answerLater: map[string]time.Time{},
		idle:        map[string]*idleJob{},
		catalog:     map[string]catalogEntry{},
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	up, err := newUploads(cfg.DataDir, m.store, cfg.Clock)
	if err != nil {
		m.cancel()
		return nil, err
	}
	m.up = up
	if err := m.store.markInterrupted(context.Background()); err != nil {
		m.cancel()
		return nil, err
	}
	m.wg.Add(1)
	go m.sweepLoop()
	return m, nil
}

// SetMemory attaches the memory subsystem. A nil Memory is valid: the engine then runs without it.
func (m *Manager) SetMemory(mem Memory) {
	m.mu.Lock()
	m.memory = mem
	m.mu.Unlock()
}

// AddTools registers tools offered to bots (each tool decides per bot and model whether it is available).
func (m *Manager) AddTools(tools ...Tool) {
	m.mu.Lock()
	m.tools = append(m.tools, tools...)
	m.mu.Unlock()
}

// AddPromptSection registers a function whose non-empty result is appended, as plain text, to every
// bot's system prompt after the standing rules. Sections appear in registration order. The text must
// be trusted (for example the user-approved skill index): it is not fenced.
func (m *Manager) AddPromptSection(fn func(bot Bot) string) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	m.sections = append(m.sections, fn)
	m.mu.Unlock()
}

// AddUserSection registers a prompt section like AddPromptSection that is only added to runs answering
// a message the user typed. Rendering it may have side effects (for example a rate-limited offer).
func (m *Manager) AddUserSection(fn func(bot Bot) string) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	m.userSections = append(m.userSections, fn)
	m.mu.Unlock()
}

// SetToolObserver registers the observer of successful client tool calls (one; nil removes it).
func (m *Manager) SetToolObserver(o ToolObserver) {
	m.mu.Lock()
	m.observer = o
	m.mu.Unlock()
}

// SetFollowUpHook registers the hook told about every report and task report (one; nil removes it).
func (m *Manager) SetFollowUpHook(h FollowUpHook) {
	m.mu.Lock()
	m.followUp = h
	m.mu.Unlock()
}

// botExists reports whether botID is an active bot, giving the lookup storeTimeout.
func (m *Manager) botExists(ctx context.Context, botID string) bool {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	_, err := m.store.getBot(ctx, botID, false)
	return err == nil
}

// liveFollowUp tells the follow-up hook about f and reports whether it handed f to f's live call, so no written answer
// is needed. Only a follow-up of work started on a call can be handed to that call: one from chat is always answered
// in chat. The caller holds no chat lock.
func (m *Manager) liveFollowUp(f FollowUp) bool {
	m.mu.Lock()
	h := m.followUp
	m.mu.Unlock()
	if h == nil {
		return false
	}
	f.Text = clipRunes(f.Text, MaxFollowUpText)
	return h(f) && f.CallID != ""
}

// takeAnswerLater reports whether AnswerFollowUp asked for the follow-up message id to be answered before it was stored,
// and forgets the request. The caller holds lockBot of the message's bot.
func (m *Manager) takeAnswerLater(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.answerLater[id]
	delete(m.answerLater, id)
	return ok
}

// AnswerFollowUp queues the bot's written answer to a follow-up the hook handed to its live call (it returned true)
// that the call never told: the call ended first. The answer goes where it goes for an ended call, into the call's
// thread, with the usual report or task-report instructions. f is the follow-up as the hook was told it. The hook runs
// before the follow-up is stored, so its message may not exist yet: it is then answered as soon as it is stored.
func (m *Manager) AnswerFollowUp(ctx context.Context, f FollowUp) error {
	if f.MessageID == "" || f.BotID == "" || f.CallID == "" {
		return invalidf("Only a follow-up of a call can be answered this way.")
	}
	unlock := m.lockBot(f.BotID)
	defer unlock()
	if m.isClosed() {
		return ErrClosed
	}
	msg, err := m.store.getMessage(ctx, f.MessageID)
	if errors.Is(err, ErrNotFound) {
		// Still on its way into the store (awaitReport, ReportTask): it takes this bot lock next and answers it.
		m.mu.Lock()
		now := time.Now()
		for id, at := range m.answerLater {
			if now.Sub(at) > answerLaterFor {
				delete(m.answerLater, id) // its store path already ran and failed: it will never be stored
			}
		}
		m.answerLater[f.MessageID] = now
		m.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	extra := reportExtra
	switch {
	case msg.BotID != f.BotID || msg.Role != "user" || msg.CallID != f.CallID:
		return invalidf("That message is not a follow-up of this call.")
	case strings.HasPrefix(msg.Author, TaskPrefix):
		extra = taskReportExtra
	case !strings.HasPrefix(msg.Author, ReportPrefix):
		return invalidf("That message is not a follow-up of this call.")
	}
	m.mu.Lock()
	full := m.runs[f.BotID] != nil && len(m.runs[f.BotID].queue) >= maxQueued
	m.mu.Unlock()
	if full {
		return busyf("Too many messages are waiting. Wait for the bot to catch up.")
	}
	if !m.enqueue(msg, turn{extra: extra}) {
		return ErrClosed
	}
	return nil
}

func (m *Manager) toolObserver() ToolObserver {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.observer
}

// SetBuiltinSkills registers the source of the built-in skill names: skills shipped with Orole that
// every bot may use without being granted them (see tools.SkillsTool). It is used to accept them
// when the user picks one with `/` in the composer.
func (m *Manager) SetBuiltinSkills(fn func() []string) {
	m.mu.Lock()
	m.builtin = fn
	m.mu.Unlock()
}

func (m *Manager) builtinSkills() []string {
	m.mu.Lock()
	fn := m.builtin
	m.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

// SetIntegrationNames registers the source of MCP server display names by id. It is used to name the
// integrations the user @mentions in the answering bot's prompt, the way its MCP tools are labelled.
func (m *Manager) SetIntegrationNames(fn func(context.Context) map[string]string) {
	m.mu.Lock()
	m.mcpNames = fn
	m.mu.Unlock()
}

// integrationNames returns the display names of MCP servers by id, or nil when ids is empty or no
// source is registered (callers then fall back to the id).
func (m *Manager) integrationNames(ctx context.Context, ids []string) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	m.mu.Lock()
	fn := m.mcpNames
	m.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

// promptSections renders the registered sections for a run; user sections only when the run answers
// the user's own message in a stored conversation.
func (m *Manager) promptSections(bot Bot, fromUser bool) []string {
	m.mu.Lock()
	fns := slices.Clone(m.sections)
	if fromUser {
		fns = append(fns, m.userSections...)
	}
	m.mu.Unlock()
	var out []string
	for _, fn := range fns {
		if s := strings.TrimSpace(fn(bot)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Tools returns every registered tool, including the memory tools.
func (m *Manager) Tools() []Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.toolsLocked(false)
}

// toolsLocked lists registered tools. Drafts exclude the memory tools: they write persistent state
// and a draft has no bot identity.
func (m *Manager) toolsLocked(draft bool) []Tool {
	out := slices.Clone(m.tools)
	if m.memory != nil && !draft {
		out = append(out, m.memory.Tools()...)
	}
	return out
}

func (m *Manager) memoryOrNil() Memory {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.memory
}

func (m *Manager) emit(event string, payload any) {
	if m.cfg.Publish != nil {
		m.cfg.Publish(event, payload)
	}
}

func (m *Manager) publishMessage(msg Message) { m.emit("chat.message", msg) }

func (m *Manager) publishDelta(d MessageDelta) { m.emit("chat.message.delta", d) }

// publishAny publishes msg on the timeline it belongs to: a call thread message (CallID set) only as chat.call.message.
func (m *Manager) publishAny(msg Message) {
	if msg.CallID != "" {
		m.publishCallMessage(msg)
		return
	}
	m.publishMessage(msg)
}

// publishCallMessage and publishCallDelta carry the messages of a call's thread: the chat timeline never sees them.
func (m *Manager) publishCallMessage(msg Message) { m.emit("chat.call.message", msg) }

func (m *Manager) publishCallDelta(d MessageDelta) { m.emit("chat.call.delta", d) }

func (m *Manager) publishBot(id, action string) {
	m.emit("chat.bot", BotChange{ID: id, Action: action})
}

// Close cancels every run, waits for them to stop and removes staged uploads. Runs that were
// interrupted by shutdown are stored as incomplete. It is safe to call more than once.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		for _, j := range m.idle {
			j.timer.Stop()
		}
		clear(m.idle)
		m.mu.Unlock()
		m.cancel()
		m.wg.Wait()
		m.up.close()
	})
}

// Bots lists active bots, most recently active first.
func (m *Manager) Bots(ctx context.Context) ([]Bot, error) {
	bots, err := m.store.listBots(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	for i := range bots {
		bots[i].Running = m.chatRunningLocked(bots[i].ID)
	}
	m.mu.Unlock()
	return bots, nil
}

// Bot returns one active bot.
func (m *Manager) Bot(ctx context.Context, id string) (Bot, error) {
	b, err := m.store.getBot(ctx, id, false)
	if err != nil {
		return Bot{}, err
	}
	m.mu.Lock()
	b.Running = m.chatRunningLocked(id)
	m.mu.Unlock()
	return b, nil
}

// chatRunningLocked reports whether the chat sees the bot's worker running: one that so far only answered in a call's
// thread is not announced to the chat (runState.shown). m.mu must be held.
func (m *Manager) chatRunningLocked(botID string) bool {
	st := m.runs[botID]
	return st != nil && st.shown
}

// SaveCheck runs before a bot with Jarvis enabled is saved. stored is the bot as saved now (nil when creating). It
// returns the input to save, possibly adjusted (Jarvis switched off), or an error that refuses the save.
type SaveCheck func(ctx context.Context, stored *Bot, in BotInput) (BotInput, error)

// SetSaveCheck installs the daemon's Jarvis check (Jarvis needs full permissions).
func (m *Manager) SetSaveCheck(f SaveCheck) {
	m.mu.Lock()
	m.saveCheck = f
	m.mu.Unlock()
}

// SaveBot creates (empty ID) or replaces a bot.
func (m *Manager) SaveBot(ctx context.Context, in BotInput) (Bot, error) {
	m.mu.Lock()
	check := m.saveCheck
	m.mu.Unlock()
	if check != nil && in.Config.Jarvis.Enabled {
		var stored *Bot
		if in.ID != "" {
			if b, err := m.Bot(ctx, in.ID); err == nil {
				stored = &b
			}
		}
		var err error
		if in, err = check(ctx, stored, in); err != nil {
			return Bot{}, err
		}
	}
	creating := in.ID == ""
	b, err := m.store.saveBot(ctx, in)
	if err != nil {
		return Bot{}, err
	}
	m.publishBot(b.ID, "saved")
	if creating && b.Config.Setup {
		if err := m.greet(ctx, b.ID); err != nil {
			m.log.Warn("chat: cannot start the setup conversation", "bot", b.ID, "err", err)
		}
	}
	return m.Bot(ctx, b.ID)
}

// PinBot pins a bot to the top of the bot list, or unpins it.
func (m *Manager) PinBot(ctx context.Context, id string, pinned bool) error {
	if err := m.store.setPinned(ctx, id, pinned); err != nil {
		return err
	}
	m.publishBot(id, "saved")
	return nil
}

// setupKickoff is the hidden first message of a new bot: the model reads it, the user never sees it.
const setupKickoff = "(The user just created you. Start the setup now: greet them and ask your first question.)"

// greet makes a new bot speak first: it stores a hidden user message and runs a turn on it.
func (m *Manager) greet(ctx context.Context, botID string) error {
	unlock := m.lockBot(botID)
	defer unlock()
	m.mu.Lock()
	closed, running := m.closed, m.runs[botID] != nil
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if running {
		return nil
	}
	msg := Message{
		ID: newID(), BotID: botID, Role: "user", Author: "user", Status: StatusComplete, CreatedAt: m.nowMs(),
		Parts: []MsgPart{{Type: PartText, Text: setupKickoff, Hidden: true}},
	}
	if err := m.store.insertMessage(ctx, &msg); err != nil {
		return err
	}
	m.publishMessage(msg)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.startLocked(botID, turn{userMsgID: msg.ID})
	return nil
}

// lockBot serialises everything that stores a message for one bot and starts or queues its runs:
// Send, Regenerate, greet, delegation, and Archive/Clear (whenIdle). Holding it keeps message order
// and queue order identical without holding the global m.mu across SQLite work. Lock order: a bot
// lock is taken before m.mu, never while holding it.
func (m *Manager) lockBot(botID string) (unlock func()) {
	mu, _ := m.botMu.LoadOrStore(botID, new(sync.Mutex))
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// GrantSkill adds a registry skill to a bot's own skills so the bot can read it, and does nothing when
// it already has it. The bot's other settings are kept exactly as they are.
func (m *Manager) GrantSkill(ctx context.Context, botID, name string) error {
	b, err := m.store.getBot(ctx, botID, false)
	if err != nil {
		return err
	}
	if slices.Contains(b.Config.Skills, name) {
		return nil
	}
	b.Config.Skills = append(slices.Clone(b.Config.Skills), name)
	_, err = m.SaveBot(ctx, BotInput{
		ID: b.ID, Name: b.Name, Avatar: b.Avatar, Color: b.Color, Description: b.Description, Instructions: b.Instructions,
		Provider: b.Provider, Model: b.Model, Reasoning: b.Reasoning, Config: b.Config,
	})
	return err
}

// ArchiveBot stops the bot's run, then hides it. The conversation and memory are kept.
func (m *Manager) ArchiveBot(ctx context.Context, id string) error {
	err := m.whenIdle(ctx, id, func() error { return m.store.archiveBot(ctx, id) })
	if err == nil {
		m.cancelIdleTimer(id)
		m.publishBot(id, "archived")
	}
	return err
}

// ExportBot returns the portable form of a bot: settings and instructions, never memory, messages
// or secrets. Grants that name local resources (delegates, memory scopes, MCP tools, skills) and
// trust settings (tool access, code task defaults) are left out: an imported file must not be able
// to grant itself access.
func (m *Manager) ExportBot(ctx context.Context, id string) (BotExport, error) {
	b, err := m.store.getBot(ctx, id, false)
	if err != nil {
		return BotExport{}, err
	}
	return BotExport{Version: 1, Bot: portableInput(BotInput{
		Name: b.Name, Avatar: b.Avatar, Color: b.Color, Description: b.Description,
		Instructions: b.Instructions, Provider: b.Provider, Model: b.Model, Reasoning: b.Reasoning, Config: b.Config,
	})}, nil
}

// ImportBot creates a new bot from an export. The ID in the file is ignored.
func (m *Manager) ImportBot(ctx context.Context, in BotExport) (Bot, error) {
	if in.Version != 1 {
		return Bot{}, invalidf("Unsupported bot file version %d.", in.Version)
	}
	bi := portableInput(in.Bot)
	bi.ID = ""
	return m.SaveBot(ctx, bi)
}

func portableInput(in BotInput) BotInput {
	in.ID = ""
	in.Config.MCPServers = nil
	in.Config.Skills = nil
	in.Config.Executive = false
	in.Config.CodeTasks = false
	in.Config.Computer = false
	in.Config.Memory = nil
	in.Config.ToolAccess = ""
	in.Config.Tasks = BotTaskDefaults{}
	in.Config.Jarvis = BotJarvis{}
	return in
}

// Messages pages a conversation (ascending order, newest page first).
func (m *Manager) Messages(ctx context.Context, req MessagesRequest) (MessagePage, error) {
	if _, err := m.store.getBot(ctx, req.BotID, false); err != nil {
		return MessagePage{}, err
	}
	return m.store.pageMessages(ctx, req)
}

// CallMessages returns the spoken turns of a phone call in order (see Message.CallID), without the daemon cues
// (VoiceRequest.Cue) that only steered the bot.
func (m *Manager) CallMessages(ctx context.Context, callID string) ([]Message, error) {
	if callID == "" {
		return []Message{}, nil
	}
	return m.store.callMessages(ctx, callID)
}

// Clear stops the bot's run and deletes its conversation, summary and sent attachments. Memory is kept.
func (m *Manager) Clear(ctx context.Context, id string) error {
	err := m.whenIdle(ctx, id, func() error {
		shas, err := m.store.clearConversation(ctx, id)
		if err != nil {
			return err
		}
		m.up.gcBlobs(shas)
		return nil
	})
	if err == nil {
		m.cancelIdleTimer(id)
		m.publishBot(id, "cleared")
	}
	return err
}

// ModelCaps resolves provider and model (empty values use the defaults) and returns the model's
// capabilities from the cached provider catalogue.
func (m *Manager) ModelCaps(ctx context.Context, provider, model string) (llm.Caps, error) {
	_, mdl, err := m.modelInfo(ctx, provider, model)
	if err != nil {
		return llm.Caps{}, err
	}
	return mdl.Caps, nil
}

// resolveModel applies defaults to an incomplete provider/model pair.
func (m *Manager) resolveModel(ctx context.Context, provider, model string) (string, string, error) {
	if provider != "" && model != "" {
		return provider, model, nil
	}
	d, err := m.cfg.LLM.Defaults(ctx)
	if err != nil {
		return "", "", err
	}
	dp, dm, ok := llm.SplitModel(d.ChatModel)
	switch {
	case provider == "" && model == "":
		if !ok {
			return "", "", errNoModel
		}
		return dp, dm, nil
	case provider != "" && ok && dp == provider:
		return provider, dm, nil
	case provider == "":
		return "", "", errNoModel
	}
	return "", "", errNoModel
}

var errNoModel = &userError{kind: ErrInvalid, msg: "No model is chosen for this bot and no default model is set. Pick a model in the bot settings or set a default in Settings > Models."}

func (m *Manager) modelInfo(ctx context.Context, provider, model string) (string, llm.Model, error) {
	provider, model, err := m.resolveModel(ctx, provider, model)
	if err != nil {
		return "", llm.Model{}, err
	}
	models, err := m.catalogue(ctx, provider)
	if err != nil {
		return "", llm.Model{}, err
	}
	for _, mdl := range models {
		if mdl.ID == model {
			return provider, mdl, nil
		}
	}
	return "", llm.Model{}, invalidf("Model %q is not offered by %s. Pick another model in the bot settings.", model, provider)
}

// catalogue returns the provider's models, cached for catalogTTL.
func (m *Manager) catalogue(ctx context.Context, provider string) ([]llm.Model, error) {
	m.catMu.Lock()
	if e, ok := m.catalog[provider]; ok && m.cfg.Clock().Sub(e.at) < catalogTTL {
		m.catMu.Unlock()
		return e.models, nil
	}
	m.catMu.Unlock()
	models, err := m.cfg.LLM.Models(ctx, provider)
	if err != nil {
		return nil, err
	}
	m.catMu.Lock()
	m.catalog[provider] = catalogEntry{at: m.cfg.Clock(), models: models}
	m.catMu.Unlock()
	return models, nil
}

// nonceCache remembers recent client nonces so a retried send returns the stored message.
type nonceCache struct {
	ids  map[string]string
	ring [1024]string
	pos  int
}

func (c *nonceCache) get(botID, nonce string) (string, bool) {
	id, ok := c.ids[botID+"\x00"+nonce]
	return id, ok
}

func (c *nonceCache) put(botID, nonce, msgID string) {
	if c.ids == nil {
		c.ids = make(map[string]string, len(c.ring))
	}
	if old := c.ring[c.pos]; old != "" {
		delete(c.ids, old)
	}
	k := botID + "\x00" + nonce
	c.ring[c.pos] = k
	c.pos = (c.pos + 1) % len(c.ring)
	c.ids[k] = msgID
}

func newID() string { return uuid.NewString() }

func (m *Manager) nowMs() int64 { return m.cfg.Clock().UnixMilli() }

func (m *Manager) sweepLoop() {
	defer m.wg.Done()
	m.sweep()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			m.sweep()
		}
	}
}

func (m *Manager) sweep() {
	if err := m.up.sweep(m.ctx); err != nil && m.ctx.Err() == nil {
		m.log.Warn("chat attachment sweep failed", "err", err)
	}
}
