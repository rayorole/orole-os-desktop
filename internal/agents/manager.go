package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/github"
	"github.com/rayorole/orole-os-desktop/internal/machines"
	"github.com/rayorole/orole-os-desktop/internal/pricing"
)

// Config wires a Manager.
type Config struct {
	Store          *Store
	Host           *Host
	Log            *slog.Logger
	Worktrees      Worktrees // optional; without it NewWorktree and diffs are unavailable
	Machines       *machines.Manager
	ProjectOptions func(context.Context) ([]ProjectOption, error)
	// ProjectDirectory resolves and validates a registered project's task directory.
	ProjectDirectory func(ctx context.Context, projectID, machine, dir string) (string, error)
	// Publish delivers "agents.updated" (Session), "agents.delta" (SessionDelta: display
	// fields changed on a hot path), "agents.event" (Event) and "agents.removed" ({id}) to
	// subscribed clients.
	Publish  func(event string, data any)
	Finished func(Session) // called once per completed turn, or process-only completion
	Created  func(Session) // called once when Start launched a new session (never for a retried key)
	// PermissionDecision checks policy before honoring a user's Allow. Errors fail closed
	// and send Deny to the CLI; the existing agent approval UI supplies the Ask decision.
	PermissionDecision func(context.Context, string, PermissionRequest) error
	// Authorize gates process execution, prompts, checks and destructive review in the daemon.
	// Missing authorization fails closed; trusted test hosts must explicitly supply it.
	Authorize func(context.Context, string, string, string) error
	// Audit records security-relevant decisions (permission answers, process launches).
	Audit    func(action, target, outcome, detail string)
	Adapters []Adapter // default: all built in
	MaxLive  int       // default 16
	// GitHub returns the daemon's authorized GitHub client (its Authorize hook applies policy and
	// requires the user's confirmation for writes). Publish needs it only to open pull requests
	// and to look up a repository's default branch; without it those steps fail clearly.
	GitHub func() (*github.Client, error)
	// SkillsPrompt returns the enabled central skills index appended to structured harnesses'
	// instructions ("" = none). Optional.
	SkillsPrompt func() string
	// YoloEnabled reports whether the daemon setting allows YOLO sessions (nil = allowed). It is
	// read on every start, switch and detection, so turning it off takes effect immediately.
	YoloEnabled func() bool
	// BotYoloEnabled reports whether a bot actor may start or switch a task to YOLO (nil = never).
	// It is read on every start and switch; YoloEnabled still applies.
	BotYoloEnabled func() bool
	// DefaultPermissionMode returns the mode a start that names none runs in ("" = manual). It is the
	// user's setting, so a YOLO default counts as confirmed; the mode falls back to manual where the
	// launch cannot honour it.
	DefaultPermissionMode func() string
	// TitleAccount resolves the model account of kind's own vendor ("claude", "codex") for naming a
	// task with one short request (nil = the heuristic and harness-chosen titles only). It must return
	// an error unless that vendor's CLI is installed and signed in, and never another vendor's account.
	TitleAccount func(ctx context.Context, kind string) (TitleRoute, error)
	// TitlesEnabled reports whether that request may be made (nil = enabled). It is read each time.
	TitlesEnabled func() bool
	// TitleTimeout bounds one naming request (default 15 s).
	TitleTimeout time.Duration
	// Prices prices the usage ledger in Usage (default: the embedded models.dev snapshot).
	Prices *pricing.Catalog
}

// Manager owns every live agent process and the session records.
type Manager struct {
	cfg      Config
	st       *Store
	adapters map[string]Adapter
	order    []string
	boot     string

	launchMu     sync.Mutex   // serialize starts/resumes against each other and shutdown
	attachmentMu sync.Mutex   // staging expiry/discard excludes launch handoff
	worktreeMu   sync.RWMutex // removal excludes manual and background Git readers
	publishMu    sync.Mutex   // one Publish at a time so retries are idempotent
	bgWork       sync.WaitGroup
	mu           sync.Mutex
	live         map[string]*liveSess
	changed      chan struct{}
	closing      bool

	bg       context.Context // cancelled by Shutdown; owns background checks
	bgCancel context.CancelFunc

	detectMu sync.Mutex
	detectAt time.Time
	detected []Detection

	w *sessionWriter // writes live sessions outside mu, debounced

	diffMu   sync.Mutex // guards diffRuns
	diffRuns map[string]*diffRun
	diffSem  chan struct{} // bounds concurrent diff-stat refreshes

	titleMu sync.Mutex // guards titles; a leaf lock, never held across another lock or a call out
	titles  map[string]*titleState
}

type liveSess struct {
	// controlMu serializes process input, pending-request transitions and exits.
	// Lock order: launchMu (when needed), controlMu, then Manager.mu.
	controlMu         sync.Mutex
	deliveryUncertain bool   // no further dispatch until this process is explicitly replaced
	id                string // immutable; s is guarded by Manager.mu
	s                 Session
	ver               uint64 // bumped by every in-memory change to s; guarded by Manager.mu
	proc              Process
	stopping          bool
	shutdown          bool
	done              chan struct{}
	// expectPrompt is the prompt we typed/passed ourselves, so the matching
	// UserPromptSubmit hook doesn't record it twice.
	expectPrompt string
	// toolIDs are the tool calls of the current turn already counted in Session.ToolCalls, so a
	// result whose call was never reported is counted once. Guarded by controlMu.
	toolIDs map[string]bool
	// subagents are the background agents the harness reported as running (EvSubagent), and
	// turnDone is set when a turn ended while some still ran: the session then stays working and
	// finishes when the last one reports. Both guarded by controlMu.
	subagents map[string]bool
	turnDone  bool
	// estimates are the provisional token readings (EvTokenEstimate) of requests not billed yet,
	// by request, and estimated their sum, which Session.Tokens includes. Guarded by controlMu.
	estimates map[string]int64
	estimated int64
}

// NewManager creates a manager. Call Recover once at startup.
func NewManager(cfg Config) *Manager {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Publish == nil {
		cfg.Publish = func(string, any) {}
	}
	if cfg.Audit == nil {
		cfg.Audit = func(string, string, string, string) {}
	}
	if cfg.MaxLive <= 0 {
		cfg.MaxLive = 16
	}
	if cfg.Prices == nil {
		cfg.Prices = pricing.Embedded()
	}
	if len(cfg.Adapters) == 0 {
		cfg.Adapters = []Adapter{Claude{}, Codex(), Omp{}, OpenCode(), Gemini(), Custom{}}
	}
	m := &Manager{cfg: cfg, st: cfg.Store, adapters: map[string]Adapter{}, boot: uuid.NewString(),
		live: map[string]*liveSess{}, changed: make(chan struct{}),
		diffRuns: map[string]*diffRun{}, diffSem: make(chan struct{}, diffStatWorkers), titles: map[string]*titleState{}}
	if cfg.Store != nil {
		cfg.Store.SetLogger(cfg.Log)
	}
	m.w = newSessionWriter(cfg.Store, cfg.Log)
	m.bg, m.bgCancel = context.WithCancel(context.Background())
	for _, a := range cfg.Adapters {
		m.adapters[a.Kind()] = a
		m.order = append(m.order, a.Kind())
	}
	if cfg.Host != nil && cfg.Host.DataDir != "" {
		m.bgWork.Add(1)
		go m.attachmentJanitor()
	}
	return m
}

// Store exposes persistence to orchestration.
func (m *Manager) Store() *Store { return m.st }

// Boot identifies this daemon run in the journal.
func (m *Manager) Boot() string { return m.boot }

// Recover marks sessions whose process died with the previous daemon as lost, and flags
// unfinished journal rows/runs as interrupted. It never re-runs anything.
func (m *Manager) Recover() (lost int, err error) {
	sessions, err := m.st.ListSessions(0, 0)
	if err != nil {
		return 0, err
	}
	billed, err := m.ledgerTotals()
	if err != nil {
		return 0, err
	}
	for _, s := range sessions {
		// Queues survive, authority to dispatch does not. Even a completed session
		// may have a persisted sending entry from the previous daemon.
		s.Pending, s.Questions = nil, nil
		pauseQueue(&s, "The daemon restarted. Resume this queue explicitly.")
		// Sessions saved before Billed existed counted cache reads in Tokens.
		t, backfill := billed[s.ID]
		backfill = backfill && s.Billed == nil
		if backfill {
			s.Billed, s.Tokens = &t, t.Work
		}
		if len(s.Queue) > 0 || backfill {
			if err := m.st.SaveSession(s, ""); err != nil {
				return lost, err
			}
		}
		switch s.Status {
		case StatusIdle, StatusWorking, StatusNeedsYou:
		default:
			continue
		}
		now := time.Now().UTC()
		s.Status = StatusLost
		s.EndedAt = &now
		s.UpdatedAt = now
		s.Activity = ""
		s.Error = "The daemon restarted while this agent was running; its process was owned by the previous daemon and is gone."
		if !localMachine(s.Machine) {
			s.Error = "The daemon restarted and lost SSH process control. The remote process may still be running; verify the selected box before restarting."
		}
		if m.adapterCanResume(s.Kind) && s.AgentSessionID != "" {
			s.Error += " It can be resumed from its CLI session."
		}
		if err := m.st.SaveSession(s, ""); err != nil {
			return lost, err
		}
		ev := Event{SessionID: s.ID, At: now, Kind: EvSystem, Text: "Marked lost: " + s.Error}
		_ = m.st.AppendEvent(&ev)
		lost++
	}
	_, _, err = m.st.MarkInterrupted(m.boot)
	return lost, err
}

func (m *Manager) adapterCanResume(kind string) bool {
	a, ok := m.adapters[kind]
	return ok && a.CanResume()
}

// Detect reports which agent CLIs are installed. Results are cached for a minute.
func (m *Manager) Detect(ctx context.Context, refresh bool) []Detection {
	m.detectMu.Lock()
	defer m.detectMu.Unlock()
	if !refresh && m.detected != nil && time.Since(m.detectAt) < time.Minute {
		return m.withYolo(m.detected)
	}
	out := make([]Detection, len(m.order))
	var wg sync.WaitGroup
	for i, k := range m.order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := m.adapters[k]
			d := a.Detect(ctx)
			d.Modes, d.Resume = a.Modes(), a.CanResume()
			if r, ok := a.(permissionModeReporter); ok {
				d.PermissionModes = r.PermissionModes(ctx, d)
			}
			out[i] = d
		}()
	}
	wg.Wait()
	m.detected, m.detectAt = out, time.Now()
	return m.withYolo(out)
}

// ---- reading ----

func (m *Manager) snapshotLocked(ls *liveSess) Session {
	s := ls.s
	s.Live = true
	s.Pending = append([]PermissionRequest{}, s.Pending...)
	for i := range s.Pending {
		s.Pending[i].Input = append(json.RawMessage(nil), s.Pending[i].Input...)
	}
	s.Questions = cloneQuestions(s.Questions)
	s.Queue = append([]QueuedPrompt(nil), s.Queue...)
	s.Usage = cloneUsage(s.Usage)
	s.Followup = m.followupCapabilities(s, ls)
	return s
}

// Get returns one session with runtime state overlaid.
func (m *Manager) Get(id string) (Session, error) {
	m.mu.Lock()
	if ls, ok := m.live[id]; ok {
		s := m.snapshotLocked(ls)
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()
	s, err := m.st.GetSession(id)
	s.Followup = m.followupCapabilities(s, nil)
	return s, err
}

// List returns every session, newest first.
func (m *Manager) List() ([]Session, error) { return m.ListPage(0, 0) }

// ListPage returns at most limit sessions (0 = all) after skipping offset, newest first, with
// runtime state overlaid.
func (m *Manager) ListPage(limit, offset int) ([]Session, error) {
	all, err := m.st.ListSessions(limit, offset)
	if err != nil {
		return nil, err
	}
	stored := make([]bool, len(all))
	m.mu.Lock()
	for i := range all {
		if ls, ok := m.live[all[i].ID]; ok {
			all[i] = m.snapshotLocked(ls)
		} else {
			stored[i] = true
		}
	}
	m.mu.Unlock()
	for i := range all {
		if stored[i] {
			all[i].Followup = m.followupCapabilities(all[i], nil)
		}
	}
	return all, nil
}

// Events returns a session's history after the given event id.
func (m *Manager) Events(id string, after int64, limit int) ([]Event, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	return m.st.Events(id, after, limit)
}

// transcriptKinds are the events that make up the readable conversation.
var transcriptKinds = []string{EvPrompt, EvMessage, EvReasoning, EvTool, EvToolResult, EvImage, EvPermission, EvDecision, EvTurnDone, EvError, EvSystem, EvNeedsInput, EvUsage, EvQuestion, EvQuestionCancel, EvAnswer, EvSources, EvFile, EvImageStatus, EvSubagent, EvGuardrail}

// Transcript returns the newest `limit` conversation events, oldest first.
func (m *Manager) Transcript(id string, limit int) ([]Event, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	return m.st.LastEvents(id, limit, transcriptKinds...)
}

// WaitFor blocks until pred holds for the session (or ctx ends).
func (m *Manager) WaitFor(ctx context.Context, id string, pred func(Session) bool) (Session, error) {
	for {
		m.mu.Lock()
		ch := m.changed
		m.mu.Unlock()
		s, err := m.Get(id)
		if err != nil {
			return s, err
		}
		if pred(s) {
			return s, nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return s, ctx.Err()
		}
	}
}

// ---- mutation plumbing ----

// notifyLocked wakes WaitFor callers after a state change.
func (m *Manager) notifyLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// mutate loads the session (live cache first), applies f, persists and publishes it.
//
// A live session changes in memory under m.mu and is written as soon as the lock is released;
// when that write fails the change is rolled back and the error returned, so memory never runs
// ahead of what a restart would find. A stored session is read, changed and written under
// m.mu. Callers that must test state and mutate atomically hold m.mu and call mutateLocked.
func (m *Manager) mutate(id string, f func(s *Session)) (Session, error) {
	m.mu.Lock()
	ls := m.live[id]
	if ls == nil {
		s, err := m.mutateLocked(id, f)
		m.mu.Unlock()
		if err == nil {
			m.cfg.Publish("agents.updated", s)
		}
		return s, err
	}
	prev := ls.s
	s := m.applyLocked(ls, f)
	ver := ls.ver
	m.mu.Unlock()
	return m.commit(ls, prev, ver, s)
}

// applyLocked applies f to a live session in memory and hands the new state to the writer.
func (m *Manager) applyLocked(ls *liveSess, f func(s *Session)) Session {
	s := ls.s
	f(&s)
	s.UpdatedAt = time.Now().UTC()
	ls.s = s
	ls.ver++
	m.w.mark(s)
	m.notifyLocked()
	return m.snapshotLocked(ls)
}

// commit writes a change applyLocked made and publishes it, or rolls it back.
func (m *Manager) commit(ls *liveSess, prev Session, ver uint64, s Session) (Session, error) {
	if err := m.w.flush(ls.id); err != nil {
		m.rollback(ls, prev, ver)
		return s, err
	}
	m.cfg.Publish("agents.updated", s)
	return s, nil
}

// rollback restores prev unless the session changed again since ver.
func (m *Manager) rollback(ls *liveSess, prev Session, ver uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.live[ls.id] == ls && ls.ver == ver {
		ls.s = prev
		ls.ver++
		m.notifyLocked()
	}
}

// mutateHot is mutate for the per-event display fields (activity, tail, last message, usage,
// diff counts...). It never waits for SQLite: the new state is written by the debounce timer,
// and only a SessionDelta is published. A change to the status or to any field a delta cannot
// carry takes the full mutate path instead (write now, publish the whole session), so f may
// change Status but must otherwise stay within SessionDelta's fields to remain cheap. f runs on
// a throw-away copy first and so must be a pure function of the session.
func (m *Manager) mutateHot(id string, f func(s *Session)) {
	m.mu.Lock()
	ls := m.live[id]
	if ls == nil {
		m.mu.Unlock()
		_, _ = m.mutate(id, f)
		return
	}
	s := ls.s
	f(&s)
	if !onlyDeltaFieldsChanged(ls.s, s) {
		m.mu.Unlock()
		_, _ = m.mutate(id, f)
		return
	}
	delta, changed := sessionDelta(&ls.s, &s)
	if !changed {
		m.mu.Unlock()
		return
	}
	s.UpdatedAt = time.Now().UTC()
	delta.UpdatedAt = s.UpdatedAt
	ls.s = s
	ls.ver++
	scheduled := m.w.mark(s)
	m.notifyLocked()
	m.mu.Unlock()
	if !scheduled { // shutting down: nothing will flush later
		if err := m.w.flush(id); err != nil {
			m.cfg.Log.Warn("agent session write failed", "session", id, "err", err)
		}
	}
	m.cfg.Publish("agents.delta", delta)
}

// mutateLocked is mutate for callers that hold m.mu to test and change state atomically. It
// writes synchronously, so a persistence error reaches the caller before anything happens
// on the strength of the new state, and memory is only changed once the write succeeded.
func (m *Manager) mutateLocked(id string, f func(s *Session)) (Session, error) {
	ls := m.live[id]
	var s Session
	if ls != nil {
		s = ls.s
	} else {
		var err error
		if s, err = m.st.GetSession(id); err != nil {
			return Session{}, err
		}
	}
	f(&s)
	s.UpdatedAt = time.Now().UTC()
	if err := m.w.write(s); err != nil {
		return s, err
	}
	if ls != nil {
		ls.s = s
		ls.ver++
		s = m.snapshotLocked(ls)
	} else {
		s.Followup = m.followupCapabilities(s, nil)
		if s.Pending == nil {
			s.Pending = []PermissionRequest{}
		}
	}
	m.notifyLocked()
	return s, nil
}

// record persists (queued, written in batches) and publishes an event.
func (m *Manager) record(sessionID string, e Event) Event {
	e.SessionID = sessionID
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	e.Text = truncate(e.Text, 16<<10)
	if len(e.Data) == 0 {
		e.Data = json.RawMessage("null")
	}
	if e.Kind != EvOutputBytes {
		if err := m.st.QueueEvent(&e); err != nil {
			m.cfg.Log.Warn("agent event write failed", "session", sessionID, "err", err)
		}
	}
	m.cfg.Publish("agents.event", e)
	if e.Kind == EvPrompt {
		m.titlePrompt(sessionID, e.Text)
	}
	return e
}

// ---- start ----

func newBranchName(kind, id string) string { return kind + "-" + id[:8] }

func titleFor(spec StartSpec, a Adapter) string {
	if t := strings.TrimSpace(spec.Title); t != "" {
		return truncate(t, 120)
	}
	if p := strings.TrimSpace(spec.Prompt); p != "" {
		return truncate(firstLine(p), 80)
	}
	return a.Name()
}

func (m *Manager) authorize(ctx context.Context, action, resource, detail string) error {
	if m.cfg.Authorize == nil {
		return errors.New("agent authorization is unavailable")
	}
	return m.cfg.Authorize(ctx, action, resource, detail)
}

// Start creates a session and launches its agent. With spec.Key set, a repeated request
// returns the session the first one created instead of starting another.
func (m *Manager) Start(ctx context.Context, spec StartSpec) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if spec.Machine == "" {
		spec.Machine = "local"
	}
	if err := validateModel(spec.Model); err != nil {
		return Session{}, err
	}
	if err := validateReasoning(spec.Kind, spec.Reasoning); err != nil {
		return Session{}, err
	}
	if len(spec.Prompt) > 100<<10 || strings.ContainsRune(spec.Prompt, 0) {
		return Session{}, errors.New("prompt must be at most 100 KiB and contain no NUL")
	}
	// Reading an already-dispatched result has no execution side effect. Keep
	// retries idempotent without requiring a second process-launch approval.
	if spec.Key != "" {
		if s, ok, err := m.st.SessionByKey(spec.Key); err != nil {
			return Session{}, err
		} else if ok {
			return m.Get(s.ID)
		}
	}
	if spec.ProjectID != "" {
		if m.cfg.ProjectDirectory == nil {
			return Session{}, errors.New("project workspaces are unavailable")
		}
		dir, err := m.cfg.ProjectDirectory(ctx, spec.ProjectID, spec.Machine, spec.Dir)
		if err != nil {
			return Session{}, err
		}
		spec.Dir = dir
	}
	if err := m.authorize(ctx, "agent.start", spec.Machine+":"+spec.Dir, fmt.Sprintf("Start %s on box %s in %s. Command: %s", spec.Kind, spec.Machine, spec.Dir, spec.Command)); err != nil {
		return Session{}, err
	}
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if spec.Key != "" {
		if s, ok, err := m.st.SessionByKey(spec.Key); err != nil {
			return Session{}, err
		} else if ok {
			return m.Get(s.ID)
		}
	}
	// Only new launches resolve uploads. Retried keys return above without copying files.
	if len(spec.AttachmentIDs) > 0 {
		m.attachmentMu.Lock()
		defer m.attachmentMu.Unlock()
	}
	attachments, err := m.resolveAttachments(spec.AttachmentIDs)
	if err != nil {
		return Session{}, err
	}
	if len(attachments) > 0 && !localMachine(spec.Machine) && m.cfg.Machines == nil {
		return Session{}, errors.New("attachment transfer is unavailable on this box")
	}
	a, ok := m.adapters[spec.Kind]
	if !ok {
		return Session{}, fmt.Errorf("unknown agent kind %q", spec.Kind)
	}
	mode := spec.Mode
	modes := a.Modes()
	if !localMachine(spec.Machine) {
		if spec.Kind != "claude" && spec.Kind != "codex" {
			return Session{}, errors.New("this harness has no remote process adapter")
		}
		if spec.NewWorktree {
			return Session{}, errors.New("remote worktree creation is not supported")
		}
		if strings.TrimSpace(spec.Prompt) == "" && len(attachments) == 0 {
			return Session{}, errors.New("remote process mode requires a task")
		}
		modes = []Mode{ModeProcess}
	}
	if mode == "" {
		mode = modes[0]
	}
	supported := false
	for _, x := range modes {
		supported = supported || x == mode
	}
	if !supported {
		return Session{}, fmt.Errorf("%s cannot run in %s mode on this box", a.Name(), mode)
	}
	if err := validPermissionMode(spec.PermissionMode); err != nil {
		return Session{}, err
	}
	if spec.PermissionMode == "" && m.cfg.DefaultPermissionMode != nil {
		// The user's default: confirmed by the setting, downgraded to manual where it cannot apply or
		// the caller (an automation, a bot without BotYoloEnabled) may not use it.
		def := normalizePermissionMode(m.cfg.DefaultPermissionMode())
		yoloOK := def != PermYolo || (m.yoloEnabled() && len(events.ChainFrom(ctx)) == 0 && (ActorFrom(ctx) == "" || m.botYoloEnabled()))
		if validPermissionMode(def) == nil && def != PermPlan && yoloOK && permissionModesApply(spec.Kind, mode, !localMachine(spec.Machine), def) == nil {
			spec.PermissionMode, spec.ConfirmYolo = def, spec.ConfirmYolo || def == PermYolo
		}
	}
	spec.PermissionMode = normalizePermissionMode(spec.PermissionMode)
	if err := permissionModesApply(spec.Kind, mode, !localMachine(spec.Machine), spec.PermissionMode); err != nil {
		return Session{}, err
	}
	if err := m.gateYolo(ctx, spec.Kind+":"+spec.Dir, spec.PermissionMode, spec.ConfirmYolo); err != nil {
		return Session{}, err
	}
	if spec.Kind == "custom" && strings.TrimSpace(spec.Command) == "" {
		return Session{}, errors.New("custom agent needs a command")
	}
	dir := spec.Dir
	if dir == "" {
		return Session{}, errors.New("a project or worktree directory is required")
	}
	if localMachine(spec.Machine) {
		var err error
		dir, err = filepath.Abs(spec.Dir)
		if err != nil {
			return Session{}, err
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return Session{}, fmt.Errorf("directory %s does not exist", dir)
		}
	}
	if spec.NewWorktree && m.cfg.Worktrees == nil {
		return Session{}, errors.New("worktrees are unavailable")
	}
	m.mu.Lock()
	n, closing := len(m.live), m.closing
	m.mu.Unlock()
	if closing {
		return Session{}, errors.New("daemon is shutting down")
	}
	if n >= m.cfg.MaxLive {
		return Session{}, fmt.Errorf("agent limit reached (%d running); stop one first", m.cfg.MaxLive)
	}

	now := time.Now().UTC()
	s := Session{
		ID: uuid.NewString(), Kind: spec.Kind, Machine: spec.Machine, Project: dir, Dir: dir, Model: spec.Model, Reasoning: spec.Reasoning,
		ProjectID: spec.ProjectID,
		Title:     titleFor(spec, a), Task: strings.TrimSpace(spec.Prompt), Mode: mode, Command: strings.TrimSpace(spec.Command),
		PermissionMode: spec.PermissionMode, Status: StatusIdle, ParentID: spec.ParentID, Group: spec.Group,
		Pending: []PermissionRequest{}, StartedAt: now, UpdatedAt: now, Chain: events.ChainFrom(ctx), Owner: ActorFrom(ctx),
		CallID: CallFrom(ctx),
	}
	if localMachine(s.Machine) && a.CanResume() && a.Kind() == "claude" {
		s.AgentSessionID = NewClaudeSessionID()
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	// The row (with its unique key) goes in before any side effect so a retry or a crash
	// can never produce a second session or a second worktree.
	if err := m.st.SaveSession(s, spec.Key); err != nil {
		if spec.Key != "" {
			if ex, ok, e2 := m.st.SessionByKey(spec.Key); e2 == nil && ok {
				return m.Get(ex.ID)
			}
		}
		return Session{}, err
	}
	if strings.TrimSpace(spec.Title) == "" {
		m.trackTitle(s.ID, s.Kind, s.Title) // a title the user chose is never tracked, so never replaced
	}
	if spec.NewWorktree {
		if err := ctx.Err(); err != nil {
			return m.failStart(s, err)
		}
		path, branch, err := m.createWorktree(ctx, dir, spec, s)
		if err != nil {
			return m.failStart(s, fmt.Errorf("create worktree: %w", err))
		}
		s.Dir, s.Branch, s.WorktreeOwned = path, branch, true
	}
	if localMachine(s.Machine) && m.cfg.Worktrees != nil {
		if head, err := m.cfg.Worktrees.Head(ctx, s.Dir); err == nil {
			s.BaseRef = head
		}
	}
	if s.WorktreeOwned {
		// Record the worktree before launching so a crash cannot orphan it unrecorded.
		if err := m.st.SaveSession(s, ""); err != nil {
			return m.failStart(s, err)
		}
	}
	if len(attachments) > 0 {
		prompt, err := m.handoffAttachments(ctx, s, attachments)
		if err != nil {
			return m.failStart(s, err)
		}
		s.Task = prompt
		if err := m.st.SaveSession(s, ""); err != nil {
			return m.failStart(s, err)
		}
	}
	if err := m.launch(ctx, &s, s.Task, false); err != nil {
		return m.failStart(s, err)
	}
	m.cfg.Audit("agent.start", s.ID, "allowed", fmt.Sprintf("%s mode=%s permission=%s dir=%s", s.Kind, s.Mode, s.PermissionMode, s.Dir))
	if m.cfg.Created != nil {
		if created, err := m.Get(s.ID); err == nil {
			m.cfg.Created(created)
		}
	}
	if s.PermissionMode == PermYolo {
		m.cfg.Audit("agent.yolo", s.ID, "allowed", "started in YOLO: "+s.Kind+" in "+s.Dir)
	}
	return m.Get(s.ID)
}

func (m *Manager) failStart(s Session, err error) (Session, error) {
	now := time.Now().UTC()
	s.Status, s.Error, s.EndedAt, s.UpdatedAt = StatusFailed, err.Error(), &now, now
	if errors.Is(err, machines.ErrAgentDispatchUncertain) {
		s.Status = StatusLost
	}
	_ = m.st.SaveSession(s, "")
	m.forgetTitle(s.ID)
	m.record(s.ID, Event{Kind: EvError, Text: err.Error()})
	m.cfg.Publish("agents.updated", s)
	m.cfg.Audit("agent.start", s.ID, "error", err.Error())
	return s, err
}

// launch starts the process for s and registers it as live. prompt is the first prompt to
// deliver ("" = none). resume continues s.AgentSessionID.
func (m *Manager) launch(ctx context.Context, s *Session, prompt string, resume bool) error {
	a := m.adapters[s.Kind]
	spec := LaunchSpec{SessionID: s.ID, Kind: s.Kind, Machine: s.Machine, Dir: s.Dir, Model: s.Model, Reasoning: s.Reasoning, Mode: s.Mode, Command: s.Command, PermissionMode: s.PermissionMode, AllowYolo: m.yoloEnabled()}
	if resume {
		spec.Resume = s.AgentSessionID
	} else if s.Kind == "claude" {
		spec.NewAgentSessionID = s.AgentSessionID
	}
	if s.Mode == ModeTerminal || s.Mode == ModeProcess {
		spec.Prompt = prompt // delivered on the command line (or via a prompt file)
	}
	if s.Mode == ModeStructured && localMachine(s.Machine) {
		m.attachGateway(&spec)
	}
	// Cancellation is honored until dispatch. Once dispatched the daemon, not the
	// requesting GUI connection, owns the process lifetime.
	if err := ctx.Err(); err != nil {
		return err
	}
	var proc Process
	var err error
	if localMachine(s.Machine) {
		proc, err = a.Start(context.WithoutCancel(ctx), m.cfg.Host, spec)
	} else {
		proc, err = m.startRemote(ctx, spec)
	}
	if err != nil {
		return err
	}
	ls := &liveSess{id: s.ID, proc: proc, done: make(chan struct{}), expectPrompt: prompt}
	s.Pid, s.TerminalID, s.Live = proc.Pid(), proc.TerminalID(), true
	s.Status, s.Error, s.Result, s.ExitCode, s.EndedAt = StatusIdle, "", "", nil, nil
	s.Pending = []PermissionRequest{}
	s.Questions = nil
	pauseQueue(s, "The agent restarted. Resume this queue explicitly.")
	s.Activity = "Starting " + a.Name()
	if prompt != "" && (s.Mode == ModeTerminal || s.Mode == ModeProcess) {
		s.Status, s.Activity = StatusWorking, "Working on the task"
		s.Prompts++
	}
	s.UpdatedAt = time.Now().UTC()
	ls.s = *s
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		_ = proc.Stop()
		return errors.New("daemon is shutting down")
	}
	m.live[s.ID] = ls
	err = m.st.SaveSession(ls.s, "")
	snap := m.snapshotLocked(ls)
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
	if err != nil {
		_ = proc.Stop()
		return err
	}
	*s = snap
	m.cfg.Publish("agents.updated", snap)
	go m.consume(ls)
	if prompt != "" {
		if s.Mode == ModeTerminal || s.Mode == ModeProcess {
			m.record(s.ID, Event{Kind: EvPrompt, Text: prompt})
		} else if err := m.sendInitialPrompt(ls, prompt); err != nil {
			_ = proc.Stop()
			return fmt.Errorf("send first prompt: %w", err)
		}
	}
	return nil
}

// sendPrompt is also the final scoped-handoff gate: never queue or steer here.
func (m *Manager) sendPrompt(ls *liveSess, prompt string) error {
	ls.controlMu.Lock()
	defer ls.controlMu.Unlock()
	return m.sendPromptControlled(ls, prompt, "", false, "", "")
}

func (m *Manager) sendInitialPrompt(ls *liveSess, prompt string) error {
	ls.controlMu.Lock()
	defer ls.controlMu.Unlock()
	return m.sendPromptControlled(ls, prompt, "", true, "", "")
}

// sendPromptControlled requires controlMu. A failed Send is delivery-uncertain,
// not evidence that nothing reached the CLI; it must never trigger a replay.
func (m *Manager) sendPromptControlled(ls *liveSess, prompt, queueID string, initial bool, model, reasoning string) error {
	id := ls.id
	m.mu.Lock()
	if m.live[id] != ls || ls.stopping || m.closing {
		m.mu.Unlock()
		return errors.New("agent is stopping")
	}
	if ls.deliveryUncertain {
		m.mu.Unlock()
		return errors.New("previous prompt delivery is uncertain; stop and restart the agent before sending again")
	}
	if len(ls.s.Pending) != 0 || len(ls.s.Questions) != 0 || ls.s.Status == StatusWorking || ls.s.Status == StatusNeedsYou {
		m.mu.Unlock()
		return errors.New("the agent is busy or waiting for a pending request")
	}
	if queueID == "" && len(ls.s.Queue) > 0 && !initial {
		m.mu.Unlock()
		return errors.New("the agent has queued follow-ups; send through its queue")
	}
	capabilities := m.followupCapabilities(ls.s, ls)
	if err := validateFollowupOptions(ls.s, capabilities, model, reasoning); err != nil {
		m.mu.Unlock()
		return err
	}
	if model == ls.s.Model {
		model = ""
	}
	if reasoning == ls.s.Reasoning {
		reasoning = ""
	}
	if ls.s.Kind == "omp" && model != "" && reasoning == "" {
		reasoning = ls.s.Reasoning
	}
	s, err := m.mutateLocked(id, func(s *Session) {
		s.Prompts++
		s.Status, s.Activity, s.Error, s.Result = StatusWorking, "Working", "", ""
	})
	if err == nil {
		ls.expectPrompt = prompt
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.cfg.Publish("agents.updated", s)
	m.record(id, Event{Kind: EvPrompt, Text: prompt})
	var sendErr error
	if model != "" || reasoning != "" {
		if p, ok := ls.proc.(configuredProcess); ok {
			sendErr = p.SendConfigured(prompt, model, reasoning)
		} else {
			sendErr = errors.New("this live agent cannot change follow-up settings")
		}
	} else {
		sendErr = ls.proc.Send(prompt)
	}
	if sendErr != nil {
		m.mu.Lock()
		ls.deliveryUncertain = true
		m.mu.Unlock()
		_, persistErr := m.mutate(id, func(s *Session) {
			pauseQueue(s, "Prompt delivery failed; resume the queue explicitly.")
			s.Status, s.Activity, s.Error = StatusNeedsYou, "Follow-up delivery is uncertain", "The configuration or prompt may have reached the agent. Stop or wait for its turn before sending again."
		})
		m.cfg.Audit("agent.prompt", id, "error", "Prompt delivery is uncertain")
		return errors.Join(sendErr, persistErr)
	}
	if queueID != "" || model != "" || reasoning != "" {
		_, err = m.mutate(id, func(s *Session) {
			if model != "" {
				s.Model = model
			}
			if reasoning != "" {
				s.Reasoning = reasoning
			}
			if len(s.Queue) > 0 && s.Queue[0].ID == queueID {
				s.Queue = append([]QueuedPrompt(nil), s.Queue[1:]...)
			}
		})
		if err != nil {
			// The persisted sending marker remains. In-memory uncertainty blocks
			// subsequent sends even if persistence itself is unavailable.
			m.mu.Lock()
			ls.deliveryUncertain = true
			pauseQueue(&ls.s, "Delivery was accepted but could not be persisted.")
			m.mu.Unlock()
			return fmt.Errorf("prompt accepted but delivery receipt could not be saved: %w", err)
		}
	}
	return nil
}

// ---- operations ----

// Prompt sends text to a session. A session whose process has ended is resumed first
// (through the CLI's session id where supported; otherwise restarted fresh).
func (m *Manager) Prompt(ctx context.Context, id, text string) error {
	text = strings.TrimSpace(text)
	if err := validPrompt(text); err != nil {
		return err
	}
	if err := m.authorize(ctx, "agent.prompt", id, "Send a task to the agent; it may execute tools under its configured permissions."); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	ls := m.controlSession(id)
	if ls == nil {
		if chain := events.ChainFrom(ctx); len(chain) > 0 {
			if _, err := m.mutate(id, func(s *Session) { s.Chain = chain }); err != nil {
				return err
			}
		}
		_, err := m.relaunchLocked(ctx, id, text, false)
		return err
	}
	ls.controlMu.Lock()
	defer ls.controlMu.Unlock()
	m.mu.Lock()
	s := m.snapshotLocked(ls)
	stopping := m.live[id] != ls || ls.stopping || m.closing
	m.mu.Unlock()
	if stopping {
		return errors.New("agent is stopping")
	}
	if s.Mode == ModeProcess {
		return errors.New("remote process mode accepts one task per launch; wait for exit, then restart with a new task")
	}
	if s.Mode == ModeTerminal {
		return errors.New("continue in the agent's attached terminal; programmatic PTY prompts are not supported")
	}
	if len(s.Pending) > 0 || len(s.Questions) > 0 {
		return errors.New("the agent is waiting for a permission decision or question answer; resolve it first")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if chain := events.ChainFrom(ctx); len(chain) > 0 {
		if _, err := m.mutate(id, func(s *Session) { s.Chain = chain }); err != nil {
			return err
		}
	}
	if localMachine(s.Machine) && (s.Status == StatusWorking || s.Status == StatusNeedsYou || len(s.Queue) > 0) {
		_, err := m.enqueueControlled(id, ls, text, "", "")
		return err
	}
	return m.sendPromptControlled(ls, text, "", false, "", "")
}

// Approve answers a pending permission request. The decision is made here, in the daemon,
// and audited; the UI only relays it.
func (m *Manager) Approve(ctx context.Context, id, requestID string, allow bool, reason string) error {
	m.mu.Lock()
	ls := m.live[id]
	var req *PermissionRequest
	if ls != nil {
		for i := range ls.s.Pending {
			if ls.s.Pending[i].ID == requestID {
				r := ls.s.Pending[i]
				req = &r
			}
		}
	}
	m.mu.Unlock()
	if ls == nil || req == nil {
		return fmt.Errorf("no pending permission request %q for this session", requestID)
	}
	var policyErr error
	if allow && m.cfg.PermissionDecision != nil {
		policyErr = m.cfg.PermissionDecision(ctx, id, *req)
		if policyErr != nil {
			allow, reason = false, policyErr.Error()
		}
	}
	ls.controlMu.Lock()
	defer ls.controlMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	current := false
	if m.live[id] == ls && !ls.stopping && !m.closing {
		for _, pending := range ls.s.Pending {
			if pending.ID == req.ID && pending.Tool == req.Tool && pending.Summary == req.Summary &&
				pending.At.Equal(req.At) && pending.InputJSON == req.InputJSON && string(pending.Input) == string(req.Input) {
				current = true
			}
		}
	}
	m.mu.Unlock()
	if !current {
		return errors.New("permission request changed or was withdrawn before dispatch")
	}
	outcome := "allowed"
	if !allow {
		outcome = "denied"
	}
	// Claim exactly once before touching the process. On failure it is unsafe to
	// assume a reply was not delivered and offer the same approval again.
	_, err := m.mutate(id, func(s *Session) {
		rest := s.Pending[:0:0]
		for _, p := range s.Pending {
			if p.ID != requestID {
				rest = append(rest, p)
			}
		}
		s.Pending = rest
		if len(rest) == 0 && len(s.Questions) == 0 && s.Status == StatusNeedsYou {
			s.Status, s.Activity = StatusWorking, "Continuing after "+outcome
		}
	})
	if err != nil {
		return err
	}
	if err := ls.proc.Respond(requestID, allow, reason); err != nil {
		m.mu.Lock()
		ls.deliveryUncertain = true
		m.mu.Unlock()
		m.cfg.Audit("agent.permission", id, "error", err.Error())
		m.record(id, Event{Kind: EvPermCancel, Data: jraw(map[string]string{"id": requestID}), Text: "Permission response delivery is uncertain."})
		_, _ = m.mutate(id, func(s *Session) { pauseQueue(s, "Permission response delivery is uncertain.") })
		return err
	}
	m.cfg.Audit("agent.permission", id, outcome, req.Tool+": "+truncate(req.Summary, 300))
	m.record(id, Event{Kind: EvDecision, Text: outcome + " " + req.Tool, Data: jraw(map[string]any{"id": requestID, "allow": allow, "reason": reason, "tool": req.Tool})})
	return policyErr
}

// Stop ends a running session's process tree.
func (m *Manager) Stop(ctx context.Context, id string) (Session, error) {
	ls := m.controlSession(id)
	if ls == nil {
		return Session{}, errors.New("agent is not running")
	}
	ls.controlMu.Lock()
	m.mu.Lock()
	if m.live[id] != ls {
		m.mu.Unlock()
		ls.controlMu.Unlock()
		return m.Get(id)
	}
	ls.stopping = true
	s, persistErr := m.mutateLocked(id, func(s *Session) {
		s.Pending, s.Questions = nil, nil
		pauseQueue(s, "The agent was stopped. Resume the queue explicitly.")
	})
	m.mu.Unlock()
	ls.controlMu.Unlock()
	if persistErr == nil {
		m.cfg.Publish("agents.updated", s)
	}
	m.cfg.Audit("agent.stop", id, "allowed", "")
	if err := ls.proc.Stop(); err != nil {
		return Session{}, err
	}
	select {
	case <-ls.done:
	case <-ctx.Done():
		return Session{}, ctx.Err()
	case <-time.After(10 * time.Second):
	}
	return m.Get(id)
}

// WithStoppedSession prevents a session from being relaunched while its committed
// work is inspected or published. The callback must not launch another agent.
// External editors remain outside this lock; callers must pin exact commit IDs.
func (m *Manager) WithStoppedSession(ctx context.Context, id string, fn func() error) error {
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	if !localMachine(s.Machine) {
		return errRemoteOperation
	}
	if s.Live {
		return errors.New("stop the agent before preparing or publishing its work")
	}
	return fn()
}

// Restart (re)launches an ended or running session. Without fresh, a CLI that supports it is
// resumed in its own session; otherwise (or with fresh) the task is started over.
func (m *Manager) Restart(ctx context.Context, id string, fresh bool, prompt string) (Session, error) {
	if err := m.authorize(ctx, "agent.restart", id, "Restart this agent under its configured permissions."); err != nil {
		return Session{}, err
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	ls := m.live[id]
	m.mu.Unlock()
	if ls != nil {
		current, err := m.Get(id)
		if err != nil {
			return Session{}, err
		}
		if !localMachine(current.Machine) {
			return Session{}, errors.New("stop the remote process and verify it ended on the box before starting a fresh task")
		}
		if _, err := m.Stop(ctx, id); err != nil {
			return Session{}, err
		}
	}
	return m.relaunch(ctx, id, strings.TrimSpace(prompt), fresh)
}

func (m *Manager) relaunch(ctx context.Context, id, prompt string, fresh bool) (Session, error) {
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	return m.relaunchLocked(ctx, id, prompt, fresh)
}

// relaunchLocked requires launchMu; scoped delivery uses it to pin the recipient
// across the final identity check and a continuation launch.
func (m *Manager) relaunchLocked(ctx context.Context, id, prompt string, fresh bool) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return Session{}, errors.New("daemon is shutting down")
	}
	_, running := m.live[id]
	n := len(m.live)
	m.mu.Unlock()
	if running {
		return Session{}, errors.New("agent is already running")
	}
	if n >= m.cfg.MaxLive {
		return Session{}, fmt.Errorf("agent limit reached (%d running); stop one first", m.cfg.MaxLive)
	}
	s, err := m.st.GetSession(id)
	if err != nil {
		return Session{}, err
	}
	if s.PermissionMode == PermYolo && !m.yoloEnabled() {
		return Session{}, errors.New("this session runs in YOLO mode, which is turned off in Orole's settings; change its permission mode first")
	}
	if !localMachine(s.Machine) && s.Status == StatusLost && !fresh {
		return Session{}, errors.New("remote process termination is unconfirmed; verify the box, then explicitly restart fresh")
	}
	if localMachine(s.Machine) {
		if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
			return Session{}, fmt.Errorf("working directory %s no longer exists", s.Dir)
		}
	}
	resume := localMachine(s.Machine) && !fresh && m.adapterCanResume(s.Kind) && s.AgentSessionID != ""
	note := "Resumed the CLI session " + s.AgentSessionID
	if !resume {
		note = "Restarted from the task (a new CLI session)"
		if !fresh && !m.adapterCanResume(s.Kind) {
			note = "This agent cannot resume by session id; restarted from the task"
		}
		if localMachine(s.Machine) && s.Kind == "claude" {
			s.AgentSessionID = NewClaudeSessionID()
		} else {
			s.AgentSessionID = ""
		}
		s.Turns = 0
		if prompt == "" {
			prompt = s.Task
		}
	}
	if old := s.TerminalID; old != "" && m.cfg.Host != nil && m.cfg.Host.Terminals != nil {
		_ = m.cfg.Host.Terminals.Close(old)
	}
	s.TerminalID = ""
	m.record(id, Event{Kind: EvSystem, Text: note})
	if err := m.launch(ctx, &s, prompt, resume); err != nil {
		return m.failStart(s, err)
	}
	m.cfg.Audit("agent.restart", id, "allowed", note)
	return m.Get(id)
}

// Remove deletes a session that is not running, with its history and scratch files.
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mu.Lock()
	_, running := m.live[id]
	m.mu.Unlock()
	if running {
		return errors.New("stop the agent before removing it")
	}
	s, err := m.st.GetSession(id)
	if err != nil {
		return err
	}
	if s.TerminalID != "" && m.cfg.Host != nil && m.cfg.Host.Terminals != nil {
		_ = m.cfg.Host.Terminals.Close(s.TerminalID)
	}
	m.w.forget(id)
	if err := m.st.DeleteSession(id); err != nil {
		return err
	}
	if m.cfg.Host != nil && m.cfg.Host.DataDir != "" {
		_ = os.RemoveAll(filepath.Join(m.cfg.Host.DataDir, "agents", id))
	}
	m.forgetTitle(id)
	m.cfg.Audit("agent.remove", id, "allowed", "")
	m.cfg.Publish("agents.removed", map[string]string{"id": id})
	return nil
}

// Shutdown kills local process trees and cancels remote transports. Remote sessions
// whose termination cannot be confirmed remain lost, not stopped.
func (m *Manager) Shutdown() {
	m.bgCancel()
	m.launchMu.Lock()
	defer m.launchMu.Unlock()
	m.mu.Lock()
	m.closing = true
	var all []*liveSess
	for _, ls := range m.live {
		ls.stopping, ls.shutdown = true, true
		all = append(all, ls)
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, ls := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ls.proc.Stop()
			select {
			case <-ls.done:
			case <-time.After(5 * time.Second):
			}
		}()
	}
	wg.Wait()
	m.bgWork.Wait()
	if err := m.w.close(); err != nil {
		m.cfg.Log.Warn("agent session flush failed", "err", err)
	}
	if m.st != nil {
		if err := m.st.FlushEvents(); err != nil {
			m.cfg.Log.Warn("agent event flush failed", "err", err)
		}
	}
}

// ---- event handling ----

func (m *Manager) consume(ls *liveSess) {
	id := ls.id
	defer close(ls.done)
	for e := range ls.proc.Events() {
		if e.Kind == EvExit {
			m.finalize(ls, e)
			return
		}
		m.apply(id, e)
	}
	// Channel closed without an exit event: treat as an unknown exit.
	m.finalize(ls, Event{Kind: EvExit, Data: jraw(map[string]any{"code": -1})})
}

func lastLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")), "\n")
	var keep []string
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			keep = append([]string{truncate(l, 200)}, keep...)
		}
	}
	return strings.Join(keep, "\n")
}

// apply folds one process event into the session record.
func (m *Manager) apply(id string, e Event) {
	ls := m.controlSession(id)
	if ls == nil {
		return
	}
	ls.controlMu.Lock()
	defer ls.controlMu.Unlock()
	m.mu.Lock()
	current := m.live[id] == ls
	stopping := ls.stopping || m.closing
	m.mu.Unlock()
	if !current {
		return
	}
	// Late requests during stop cannot resurrect an actionable permission/form.
	if stopping && (e.Kind == EvQuestion || e.Kind == EvPermission) {
		return
	}
	if e.Kind == EvTitle {
		m.applyTitle(id, e.Text, true) // a name, not history: neither recorded nor published as an event
		return
	}
	switch e.Kind {
	case EvTokens:
		m.recordTokens(ls, e) // billed usage feeds the ledger; it is not history
		return
	case EvTokenEstimate:
		m.estimateTokens(ls, e)
		return
	}
	e = m.record(id, e)
	if m.applyChatEvent(id, e) {
		return
	}
	var data map[string]json.RawMessage
	_ = json.Unmarshal(e.Data, &data)
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(data[k], &s)
		return s
	}
	switch e.Kind {
	case EvInit:
		aid, model := str("agentSessionId"), str("model")
		m.mutateHot(id, func(s *Session) {
			if aid != "" {
				s.AgentSessionID = aid
			}
			if model != "" {
				s.Model = model
			}
		})
	case EvMessage:
		m.mutateHot(id, func(s *Session) {
			s.LastMessage = truncate(e.Text, 32<<10)
			s.Tail = lastLines(e.Text, 3)
			s.Activity = truncate(firstLine(e.Text), 120)
			wakeIdle(s)
		})
	case EvTool:
		counted := ls.countTool(e.Data, false)
		m.mutateHot(id, func(s *Session) {
			s.Activity, s.Tail = truncate(e.Text, 160), truncate(e.Text, 200)
			if counted {
				s.ToolCalls++
			}
			wakeIdle(s)
		})
	case EvToolResult:
		var td ToolData
		_ = json.Unmarshal(e.Data, &td)
		counted, failed := ls.countTool(e.Data, true), td.Status == "error"
		m.mutateHot(id, func(s *Session) {
			s.Tail = lastLines(e.Text, 3)
			if counted {
				s.ToolCalls++
			}
			if failed {
				s.FailedTools++
			}
		})
	case EvActivity:
		m.mutateHot(id, func(s *Session) { s.Activity = truncate(e.Text, 160) })
	case EvWorking:
		ls.turnDone = false
		m.mutateHot(id, func(s *Session) {
			if s.Status != StatusNeedsYou || (len(s.Pending) == 0 && len(s.Questions) == 0) {
				s.Status = StatusWorking
			}
			if e.Text != "" {
				s.Activity = truncate(e.Text, 160)
			}
		})
	case EvSubagent:
		var a SubagentData
		if json.Unmarshal(e.Data, &a) != nil || a.ID == "" {
			return
		}
		if subagentRunning(a.Status) {
			if ls.subagents == nil {
				ls.subagents = map[string]bool{}
			}
			ls.subagents[a.ID] = true
		} else {
			delete(ls.subagents, a.ID)
		}
		n := len(ls.subagents)
		settled := n == 0 && ls.turnDone
		ls.turnDone = ls.turnDone && !settled
		s, err := m.mutate(id, func(s *Session) {
			s.Subagents = n
			switch {
			case settled && s.Status == StatusWorking:
				s.Status, s.Activity = StatusDone, ""
			case n > 0 && s.Status == StatusWorking && ls.turnDone:
				s.Activity = backgroundActivity(n)
			}
		})
		if err == nil && settled && s.Status == StatusDone {
			m.turnSettled(ls, s, true)
		}
	case EvTranscript:
		m.mutateHot(id, func(s *Session) { s.TranscriptRef = e.Text })
	case EvPermission:
		var req PermissionRequest
		if json.Unmarshal(e.Data, &req) != nil || req.ID == "" {
			return
		}
		_, _ = m.mutate(id, func(s *Session) {
			s.Pending = append(s.Pending, req)
			s.Status, s.Activity = StatusNeedsYou, "Waiting for approval: "+req.Tool+" "+truncate(req.Summary, 100)
		})
	case EvPermCancel:
		pid := str("id")
		_, _ = m.mutate(id, func(s *Session) {
			rest := s.Pending[:0:0]
			for _, p := range s.Pending {
				if p.ID != pid {
					rest = append(rest, p)
				}
			}
			s.Pending = rest
			if len(rest) == 0 && len(s.Questions) == 0 && s.Status == StatusNeedsYou {
				s.Status, s.Activity = StatusWorking, "Working"
			}
		})
	case EvNeedsInput:
		_, _ = m.mutate(id, func(s *Session) {
			s.Status, s.Activity = StatusNeedsYou, truncate(e.Text, 160)
		})
	case EvTurnDone:
		m.mu.Lock()
		ls.deliveryUncertain = false // observed completion; paused entries still require explicit resume
		m.mu.Unlock()
		ls.toolIDs = nil // a turn's call ids are not reused by its results after it ends
		ok := true
		_ = json.Unmarshal(data["ok"], &ok)
		result := str("result")
		if result == "" {
			result = e.Text
		}
		running := len(ls.subagents)
		ls.turnDone = ok && running > 0
		s, err := m.mutate(id, func(s *Session) {
			s.Turns++
			s.Pending = []PermissionRequest{}
			s.Questions = nil
			s.Result = truncate(result, 32<<10)
			if result != "" && s.LastMessage == "" {
				s.LastMessage = truncate(result, 32<<10)
			}
			if s.Review == ReviewSentBack {
				s.Review = ReviewNone
			}
			switch {
			case ok && running > 0:
				// The harness delivers the agents' results as a follow-up turn; this one is not the end.
				s.Status, s.Activity, s.Error = StatusWorking, backgroundActivity(running), ""
			case ok:
				s.Status, s.Activity, s.Error = StatusDone, "", ""
				if s.Tail == "" {
					s.Tail = lastLines(result, 3)
				}
			default:
				s.Status, s.Activity, s.Error = StatusFailed, "", truncate(result, 2000)
				pauseQueue(s, "The previous turn failed. Resume the queue explicitly.")
			}
		})
		if err == nil && !ls.turnDone {
			m.turnSettled(ls, s, ok)
		}
	}
}

// turnSettled runs once a turn is over for good: the harness reported its end and no background
// agent of that turn is still running. The caller holds controlMu.
func (m *Manager) turnSettled(ls *liveSess, s Session, ok bool) {
	if m.cfg.Finished != nil {
		m.cfg.Finished(s)
	}
	m.background(func() { m.refreshDiffStat(s.ID) })
	if ok {
		m.background(func() {
			ls.controlMu.Lock()
			defer ls.controlMu.Unlock()
			if err := m.dispatchQueueControlled(ls); err != nil {
				m.cfg.Log.Warn("queued prompt dispatch failed", "session", s.ID, "err", err)
			}
		})
	}
}

// subagentRunning reports whether a harness's subagent status means the agent is still at work.
// Harness vocabularies: omp/Claude "started", Codex "pendingInit"/"running"; anything else ended it.
func subagentRunning(status string) bool {
	switch strings.ToLower(status) {
	case "started", "running", "pending", "pendinginit", "queued", "in_progress", "working":
		return true
	}
	return false
}

// backgroundActivity is the activity line of a session whose turn ended while agents still run.
func backgroundActivity(n int) string {
	if n == 1 {
		return "Turn ended; waiting for 1 background agent"
	}
	return fmt.Sprintf("Turn ended; waiting for %d background agents", n)
}

// countTool reports whether a tool event adds a call to Session.ToolCalls: a call counts unless its
// id was already counted this turn, a result only when its call never arrived. A call without an
// id (Claude terminal hooks) always counts; a result without one never does. The caller holds
// controlMu.
func (ls *liveSess) countTool(data json.RawMessage, result bool) bool {
	var ref struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &ref)
	if ref.ID == "" {
		return !result
	}
	if ls.toolIDs[ref.ID] {
		return false
	}
	if ls.toolIDs == nil {
		ls.toolIDs = map[string]bool{}
	}
	ls.toolIDs[ref.ID] = true
	return true
}

func wakeIdle(s *Session) {
	if s.Status == StatusIdle || s.Status == StatusDone {
		s.Status = StatusWorking
	}
}

// finalize records the process exit and removes the session from the live set.
func (m *Manager) finalize(ls *liveSess, e Event) {
	ls.controlMu.Lock()
	defer ls.controlMu.Unlock()
	id := ls.id
	var d struct {
		Code      int    `json:"code"`
		Stopped   bool   `json:"stopped"`
		Stderr    string `json:"stderr"`
		Uncertain bool   `json:"uncertain"`
	}
	_ = json.Unmarshal(e.Data, &d)
	e.Text = fmt.Sprintf("exit code %d", d.Code)
	if d.Uncertain {
		e.Text = "SSH process control lost; remote exit is unconfirmed"
	}
	m.record(id, e)

	m.mu.Lock()
	if m.live[id] != ls {
		m.mu.Unlock()
		return
	}
	stopping, shutdown := ls.stopping || d.Stopped, ls.shutdown
	completedTurn := ls.s.Status == StatusDone || ls.s.Status == StatusFailed
	s, err := m.mutateLocked(id, func(s *Session) {
		now := time.Now().UTC()
		code := d.Code
		s.ExitCode, s.EndedAt, s.Pending, s.Activity = &code, &now, []PermissionRequest{}, ""
		s.Questions = nil
		pauseQueue(s, "The agent process ended. Resume the queue explicitly.")
		switch {
		case d.Uncertain:
			s.Status, s.Error, s.ExitCode = StatusLost, d.Stderr, nil
		case s.Status == StatusDone || s.Status == StatusFailed:
			// A finished turn stays finished; the process exiting afterwards changes nothing.
		case stopping:
			s.Status = StatusStopped
			if shutdown {
				s.Error = "The daemon shut down; this agent was stopped with it. Resume it to continue."
			}
		case d.Code == 0:
			s.Status = StatusDone
			if s.Result == "" {
				s.Result = s.LastMessage
			}
		default:
			s.Status = StatusFailed
			s.Error = fmt.Sprintf("The agent process exited with code %d.", d.Code)
			if d.Stderr != "" {
				s.Error += " " + truncate(d.Stderr, 1500)
			}
		}
		if s.Status == StatusDone && s.Turns == 0 {
			s.Turns = 1
		}
	})
	delete(m.live, id)
	if err == nil {
		s.Live = false
		s.Followup = m.followupCapabilities(s, nil)
		close(m.changed)
		m.changed = make(chan struct{})
	}
	m.mu.Unlock()
	if err != nil {
		m.cfg.Log.Warn("finalize failed", "session", id, "err", err)
		return
	}
	m.cfg.Publish("agents.updated", s)
	if !completedTurn && m.cfg.Finished != nil && (s.Status == StatusDone || s.Status == StatusFailed) {
		m.cfg.Finished(s)
	}
	if !completedTurn { // a finished turn already queued its refresh; the exit changes no files
		m.background(func() { m.refreshDiffStat(id) })
	}
}

// diffStatWorkers bounds the diff-stat refreshes that run at once across all sessions; each
// one spawns several git processes.
const diffStatWorkers = 2

// diffRun is the in-flight diff-stat refresh of one session. again records that another
// request arrived meanwhile, which earns exactly one more run when this one ends.
type diffRun struct{ again bool }

// diffStatter is implemented by Worktrees that can count changes without building the diff.
type diffStatter interface {
	DiffStat(ctx context.Context, dir, base string) (files, additions, deletions int, err error)
}

func (m *Manager) diffStat(ctx context.Context, dir, base string) (files, additions, deletions int, err error) {
	if ds, ok := m.cfg.Worktrees.(diffStatter); ok {
		return ds.DiffStat(ctx, dir, base)
	}
	d, err := m.cfg.Worktrees.Diff(ctx, dir, base)
	return len(d.Files), d.Additions, d.Deletions, err
}

// refreshDiffStat updates the +/- counts shown in Review. Requests for one session coalesce:
// while a refresh runs, later requests only mark it to run once more afterwards.
func (m *Manager) refreshDiffStat(id string) {
	m.diffMu.Lock()
	if r, running := m.diffRuns[id]; running {
		r.again = true
		m.diffMu.Unlock()
		return
	}
	r := &diffRun{}
	m.diffRuns[id] = r
	m.diffMu.Unlock()
	for {
		m.refreshDiffStatOnce(id)
		m.diffMu.Lock()
		if !r.again {
			delete(m.diffRuns, id)
			m.diffMu.Unlock()
			return
		}
		r.again = false
		m.diffMu.Unlock()
	}
}

func (m *Manager) refreshDiffStatOnce(id string) {
	select {
	case m.diffSem <- struct{}{}:
		defer func() { <-m.diffSem }()
	case <-m.bg.Done():
		return
	}
	m.worktreeMu.RLock()
	defer m.worktreeMu.RUnlock()
	if m.cfg.Worktrees == nil {
		return
	}
	s, err := m.Get(id)
	if err != nil || !localMachine(s.Machine) || s.BaseRef == "" {
		return
	}
	ctx, cancel := context.WithTimeout(m.bg, 60*time.Second)
	defer cancel()
	files, additions, deletions, err := m.diffStat(ctx, s.Dir, s.BaseRef)
	if err != nil || (files == s.Files && additions == s.Additions && deletions == s.Deletions) {
		return
	}
	m.mutateHot(id, func(s *Session) { s.Files, s.Additions, s.Deletions = files, additions, deletions })
}

// background starts tracked work only while the manager is accepting it.
func (m *Manager) background(f func()) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return false
	}
	m.bgWork.Add(1)
	go func() { defer m.bgWork.Done(); f() }()
	return true
}

// Diff returns the work a session produced relative to its base commit.
func (m *Manager) Diff(ctx context.Context, id string) (ReviewDiff, error) {
	m.worktreeMu.RLock()
	defer m.worktreeMu.RUnlock()
	s, err := m.Get(id)
	if err != nil {
		return ReviewDiff{}, err
	}
	if !localMachine(s.Machine) {
		return ReviewDiff{}, errRemoteOperation
	}
	out := ReviewDiff{SessionID: id, BaseRef: s.BaseRef, Dir: s.Dir}
	if m.cfg.Worktrees == nil {
		out.Error = "git is unavailable"
		return out, nil
	}
	if s.BaseRef == "" {
		out.Error = "this session's directory is not a git work tree, so there is nothing to diff"
		return out, nil
	}
	d, err := m.cfg.Worktrees.Diff(ctx, s.Dir, s.BaseRef)
	if err != nil {
		out.Error = err.Error()
		return out, nil
	}
	out.Diff, out.Files, out.Additions, out.Deletions, out.Truncated = d, len(d.Files), d.Additions, d.Deletions, d.Truncated
	if d.Files == nil {
		out.Diff.Files = []DiffFile{}
	}
	if s.Files != out.Files || s.Additions != out.Additions || s.Deletions != out.Deletions {
		m.mutateHot(id, func(s *Session) { s.Files, s.Additions, s.Deletions = out.Files, out.Additions, out.Deletions })
	}
	return out, nil
}
