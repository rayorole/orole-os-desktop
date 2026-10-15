// Package policy is the single place where "may this actor do this?" is decided.
//
// Every side-effecting capability (MCP tool calls, sending mail, deleting Drive
// files, agent actions, automations) calls Engine.Check. The engine looks for a
// stored rule, falls back to defaults, and when the answer is "ask" it parks the
// request in a pending queue that the UI resolves, blocking the caller until the
// user answers or the request times out. Every decision is written to audit_events.
//
// Defaults: read -> allow, write/destructive -> ask, unclassified -> deny.
// Effects derived from untrusted server annotations (Request.FromHint) never
// unlock more than "ask": annotations are hints, not proof. An AutoApprove hook
// (SetAutoApprove) may turn a default "ask" into "allow" for an actor up to an
// effect the user chose; explicit per-tool permissions and stored rules always win.
package policy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

// Decision is the outcome of a policy evaluation.
type Decision string

const (
	Allow Decision = "allow"
	Ask   Decision = "ask"
	Deny  Decision = "deny"
)

// Effect classifies what an action does to the world.
type Effect string

const (
	EffectUnknown     Effect = ""
	EffectRead        Effect = "read"
	EffectWrite       Effect = "write"
	EffectDestructive Effect = "destructive"
)

func (e Effect) rank() int {
	switch e {
	case EffectRead:
		return 1
	case EffectWrite:
		return 2
	case EffectDestructive:
		return 3
	}
	return 0
}

// Valid reports whether e is one of the defined values (including unknown "").
func (e Effect) Valid() bool { return e == "" || e.rank() > 0 }

// Request describes something an actor wants to do.
type Request struct {
	// Actor identifies who asks: "user", "agent:<id>", "automation:<id>", "system".
	Actor string `json:"actor"`
	// Action is a dotted verb: "mcp.tool.call", "mail.send", "drive.trash", ...
	Action string `json:"action"`
	// Resource is what is acted on: "mcp/<server>/<tool>", "mail/<server>", ...
	Resource string `json:"resource"`
	// Detail is human-readable context shown in the approval prompt. Untrusted
	// content may appear here; the engine sanitises it.
	Detail string `json:"detail,omitempty"`
	// Effect is the caller's classification of the action.
	Effect Effect `json:"effect"`
	// FromHint marks Effect as derived from server-provided annotations.
	FromHint bool `json:"fromHint,omitempty"`
	// Timeout bounds how long Check waits for an answer (0 = DefaultApprovalTimeout).
	Timeout time.Duration `json:"-"`
}

// Evaluation is the result of Decide: a decision with its provenance.
type Evaluation struct {
	Decision Decision `json:"decision"`
	// Source is "rule:<id>", "mcp_permission", "default" or SourceAutoApprove.
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// Rule is a stored policy rule.
type Rule struct {
	ID        int64    `json:"id"`
	Actor     string   `json:"actor"`
	Action    string   `json:"action"`
	Resource  string   `json:"resource"`
	MaxEffect Effect   `json:"maxEffect"`
	Decision  Decision `json:"decision"`
	Note      string   `json:"note"`
	CreatedAt int64    `json:"createdAt"`
}

// Pending is an approval waiting for the user.
type Pending struct {
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Resource  string `json:"resource"`
	Detail    string `json:"detail"`
	Effect    Effect `json:"effect"`
	FromHint  bool   `json:"fromHint"`
	Reason    string `json:"reason"`
	CreatedAt int64  `json:"createdAt"` // unix ms
	ExpiresAt int64  `json:"expiresAt"` // unix ms
	// CanRemember is false for destructive actions: those always ask.
	CanRemember bool `json:"canRemember"`
}

// Resolution is the user's answer to a pending approval.
type Resolution struct {
	Allow bool `json:"allow"`
	// Remember stores an exact-match rule so the same actor/action/resource is not asked again.
	Remember bool `json:"remember"`
}

// Errors.
var (
	ErrApprovalTimeout = errors.New("policy: approval timed out")
	ErrNotFound        = errors.New("policy: no such pending approval")
	ErrDenied          = errors.New("policy: denied")
	ErrNoRemember      = errors.New("policy: destructive actions cannot be remembered; they always ask")
)

// DefaultApprovalTimeout is how long an unanswered approval stays open.
const DefaultApprovalTimeout = 5 * time.Minute

// SourceAutoApprove is the Evaluation.Source of a default "ask" the AutoApprove hook allowed.
const SourceAutoApprove = "auto-approve"

// AutoApprove reports the highest effect actor may perform without being asked when no explicit
// per-tool permission or stored rule applies, and a reason naming the setting that grants it (it is
// written to the audit log). EffectUnknown grants nothing. It must fail closed (EffectUnknown) when
// the setting cannot be read, and must not call back into the Engine.
type AutoApprove func(ctx context.Context, actor string) (max Effect, reason string)

// MaxDetailBytes is how much of a Request's Detail the engine keeps and shows in an approval prompt
// (untrusted.Text(Detail, MaxDetailBytes)). Callers that must show a detail whole keep it within this.
const MaxDetailBytes = 2000

const (
	maxPending  = 100
	maxResource = 512
)

// Publisher delivers events to the UI (ipc.Server satisfies it).
type Publisher interface {
	Publish(event string, data any)
}

type pendingEntry struct {
	p  Pending
	ch chan Resolution
}

// Engine evaluates requests and manages the approval queue.
type Engine struct {
	db  *sql.DB
	pub Publisher
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]*pendingEntry

	autoApprove atomic.Pointer[AutoApprove]
}

// SetAutoApprove installs (or, with nil, removes) the AutoApprove hook. Safe to call at any time.
func (e *Engine) SetAutoApprove(f AutoApprove) {
	if f == nil {
		e.autoApprove.Store(nil)
		return
	}
	e.autoApprove.Store(&f)
}

// New returns an Engine. pub may be nil (no events); log may be nil.
func New(db *sql.DB, pub Publisher, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{db: db, pub: pub, log: log, pending: map[string]*pendingEntry{}}
}

// Decide evaluates req without side effects (no queueing, no audit).
func (e *Engine) Decide(ctx context.Context, req Request) (Evaluation, error) {
	if req.Action == "" || !req.Effect.Valid() {
		return Evaluation{Deny, "default", "unclassified action"}, nil
	}
	if ev, ok, err := e.mcpPermission(ctx, req); err != nil {
		return Evaluation{}, err
	} else if ok {
		return ev, nil
	}
	rules, err := e.Rules(ctx)
	if err != nil {
		return Evaluation{}, err
	}
	var best *Rule
	bestScore := -1
	for i := range rules {
		r := &rules[i]
		if !match(r.Actor, req.Actor) || !match(r.Action, req.Action) || !match(r.Resource, req.Resource) {
			continue
		}
		if r.MaxEffect != "" && req.Effect.rank() > r.MaxEffect.rank() {
			continue
		}
		s := specificity(r.Actor) + specificity(r.Action) + specificity(r.Resource)
		if r.MaxEffect != "" {
			s++
		}
		if s > bestScore || (s == bestScore && severity(r.Decision) > severity(best.Decision)) {
			best, bestScore = r, s
		}
	}
	if best != nil {
		return Evaluation{best.Decision, fmt.Sprintf("rule:%d", best.ID), "matched stored rule"}, nil
	}
	return e.applyAutoApprove(ctx, req, defaultFor(req)), nil
}

// applyAutoApprove turns a default "ask" into "allow" when the hook grants req's (known) effect to
// its actor. Explicit decisions never reach here, and a default "deny" (unknown effect) stays.
func (e *Engine) applyAutoApprove(ctx context.Context, req Request, ev Evaluation) Evaluation {
	hook := e.autoApprove.Load()
	if hook == nil || ev.Decision != Ask || ev.Source != "default" || req.Effect.rank() == 0 {
		return ev
	}
	limit, reason := (*hook)(ctx, req.Actor)
	if limit.rank() == 0 || req.Effect.rank() > limit.rank() {
		return ev
	}
	if reason == "" {
		reason = "allowed without asking by setting"
	}
	return Evaluation{Allow, SourceAutoApprove, reason}
}

func defaultFor(req Request) Evaluation {
	// Submitting a task is the user's instruction, not a tool-use approval.
	// Stored rules are evaluated first; delegated launches and tool actions keep
	// their normal policy. Never trust an external effect hint for this default.
	if req.Actor == "user" && !req.FromHint {
		switch req.Action {
		case "agent.start", "agent.prompt", "agent.restart", "agent.permissionMode":
			return Evaluation{Allow, "default", "user-requested task submission"}
		}
	}
	switch req.Effect {
	case EffectRead:
		if req.FromHint {
			return Evaluation{Ask, "default", "read-only claim comes from the server's own annotations"}
		}
		return Evaluation{Allow, "default", "read-only action"}
	case EffectWrite, EffectDestructive:
		return Evaluation{Ask, "default", "action changes data"}
	}
	return Evaluation{Deny, "default", "unclassified action"}
}

func severity(d Decision) int {
	switch d {
	case Deny:
		return 3
	case Ask:
		return 2
	}
	return 1
}

// mcpPermission consults the per-tool mcp_permissions table for "mcp/<server>/<tool>".
// An explicit per-tool row is the most specific rule there is and wins over policy_rules.
func (e *Engine) mcpPermission(ctx context.Context, req Request) (Evaluation, bool, error) {
	if !strings.HasPrefix(req.Resource, "mcp/") {
		return Evaluation{}, false, nil
	}
	rest := strings.TrimPrefix(req.Resource, "mcp/")
	server, tool, ok := strings.Cut(rest, "/")
	if !ok || server == "" || tool == "" {
		return Evaluation{}, false, nil
	}
	var dec string
	err := e.db.QueryRowContext(ctx, `SELECT decision FROM mcp_permissions WHERE server_id=? AND tool=?`, server, tool).Scan(&dec)
	if errors.Is(err, sql.ErrNoRows) {
		return Evaluation{}, false, nil
	}
	if err != nil {
		return Evaluation{}, false, err
	}
	d := Decision(dec)
	if d != Allow && d != Ask && d != Deny {
		return Evaluation{}, false, nil
	}
	return Evaluation{d, "mcp_permission", "per-tool permission"}, true, nil
}

// Check decides req and, when the answer is "ask", blocks until the user resolves
// it, the timeout elapses (Deny, ErrApprovalTimeout) or ctx is done (Deny, ctx error).
// The returned Decision is always Allow or Deny; the audit log records it.
func (e *Engine) Check(ctx context.Context, req Request) (Decision, error) {
	req = normalize(req)
	ev, err := e.Decide(ctx, req)
	if err != nil {
		e.audit(req, "error", "policy lookup failed: "+err.Error())
		return Deny, err
	}
	switch ev.Decision {
	case Allow:
		detail := ev.Source
		if ev.Source == SourceAutoApprove {
			detail += ": " + ev.Reason
		}
		e.audit(req, "allowed", detail)
		return Allow, nil
	case Deny:
		e.audit(req, "denied", ev.Source+": "+ev.Reason)
		return Deny, nil
	}
	return e.await(ctx, req, ev)
}

// Require is Check that returns ErrDenied unless the request is allowed.
func (e *Engine) Require(ctx context.Context, req Request) error {
	d, err := e.Check(ctx, req)
	if err != nil {
		return err
	}
	if d != Allow {
		return fmt.Errorf("%w: %s %s", ErrDenied, req.Action, req.Resource)
	}
	return nil
}

func normalize(req Request) Request {
	req.Actor = untrusted.Line(req.Actor, 128)
	req.Action = untrusted.Line(req.Action, 128)
	req.Resource = untrusted.Line(req.Resource, maxResource)
	req.Detail = untrusted.Text(req.Detail, MaxDetailBytes)
	if req.Actor == "" {
		req.Actor = "unknown"
	}
	return req
}

func (e *Engine) await(ctx context.Context, req Request, ev Evaluation) (Decision, error) {
	if ApprovalsUnavailable(ctx) {
		e.audit(req, "denied", "needs approval; nobody can answer on a call")
		return Deny, ErrApprovalUnavailable
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultApprovalTimeout
	}
	now := time.Now()
	ent := &pendingEntry{ch: make(chan Resolution, 1), p: Pending{
		ID: uuid.NewString(), Actor: req.Actor, Action: req.Action, Resource: req.Resource, Detail: req.Detail,
		Effect: req.Effect, FromHint: req.FromHint, Reason: ev.Reason,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(timeout).UnixMilli(),
		CanRemember: req.Effect != EffectDestructive,
	}}
	e.mu.Lock()
	if len(e.pending) >= maxPending {
		e.mu.Unlock()
		e.audit(req, "denied", "approval queue full")
		return Deny, nil
	}
	e.pending[ent.p.ID] = ent
	e.mu.Unlock()
	e.publish()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var res Resolution
	var outcome string
	var retErr error
	select {
	case res = <-ent.ch:
	case <-timer.C:
		outcome, retErr = "approval timed out", ErrApprovalTimeout
	case <-ctx.Done():
		outcome, retErr = "request canceled", ctx.Err()
	}
	e.mu.Lock()
	delete(e.pending, ent.p.ID)
	e.mu.Unlock()
	e.publish()
	if e.pub != nil {
		e.pub.Publish("policy.resolved", map[string]any{"id": ent.p.ID})
	}
	if retErr != nil {
		e.audit(req, "denied", outcome)
		return Deny, retErr
	}
	if res.Allow {
		e.audit(req, "allowed", "approved by user")
		return Allow, nil
	}
	e.audit(req, "denied", "rejected by user")
	return Deny, nil
}

// Pending lists unresolved approvals, oldest first.
func (e *Engine) Pending() []Pending {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Pending, 0, len(e.pending))
	for _, p := range e.pending {
		out = append(out, p.p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out
}

// Resolve answers a pending approval. If Remember is set, an exact-match rule is stored.
func (e *Engine) Resolve(ctx context.Context, id string, r Resolution) error {
	e.mu.Lock()
	ent, ok := e.pending[id]
	if ok && r.Remember && !ent.p.CanRemember {
		e.mu.Unlock()
		return ErrNoRemember
	}
	if ok {
		delete(e.pending, id)
	}
	e.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if r.Remember {
		d := Deny
		var maxEffect Effect
		if r.Allow {
			d = Allow
			maxEffect = ent.p.Effect
		}
		if _, err := e.AddRule(ctx, Rule{Actor: ent.p.Actor, Action: ent.p.Action, Resource: ent.p.Resource, MaxEffect: maxEffect, Decision: d, Note: "remembered from approval"}); err != nil {
			e.log.Warn("remember approval failed", "err", err)
		}
	}
	ent.ch <- r // buffered(1); the waiter is the only reader
	return nil
}

func (e *Engine) publish() {
	if e.pub != nil {
		e.pub.Publish("policy.pending", map[string]any{"items": e.Pending()})
	}
}

// Rules lists stored rules.
func (e *Engine) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := e.db.QueryContext(ctx, `SELECT id,actor,action,resource,max_effect,decision,note,created_at FROM policy_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		var me, d string
		if err := rows.Scan(&r.ID, &r.Actor, &r.Action, &r.Resource, &me, &d, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.MaxEffect, r.Decision = Effect(me), Decision(d)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AddRule stores (or replaces the decision of) a rule.
func (e *Engine) AddRule(ctx context.Context, r Rule) (Rule, error) {
	if r.Actor == "" {
		r.Actor = "*"
	}
	if r.Resource == "" {
		r.Resource = "*"
	}
	if r.Action == "" || len(r.Actor) > 128 || len(r.Action) > 128 || len(r.Resource) > maxResource || len(r.Note) > 256 {
		return r, errors.New("policy: rule needs an action and bounded fields")
	}
	if r.Decision != Allow && r.Decision != Ask && r.Decision != Deny {
		return r, errors.New("policy: decision must be allow, ask or deny")
	}
	if !r.MaxEffect.Valid() {
		return r, errors.New("policy: invalid maxEffect")
	}
	r.CreatedAt = time.Now().Unix()
	_, err := e.db.ExecContext(ctx, `INSERT INTO policy_rules(actor,action,resource,max_effect,decision,note,created_at) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(actor,action,resource,max_effect) DO UPDATE SET decision=excluded.decision, note=excluded.note`,
		r.Actor, r.Action, r.Resource, string(r.MaxEffect), string(r.Decision), r.Note, r.CreatedAt)
	if err != nil {
		return r, err
	}
	err = e.db.QueryRowContext(ctx, `SELECT id FROM policy_rules WHERE actor=? AND action=? AND resource=? AND max_effect=?`,
		r.Actor, r.Action, r.Resource, string(r.MaxEffect)).Scan(&r.ID)
	return r, err
}

// DeleteRule removes a rule.
func (e *Engine) DeleteRule(ctx context.Context, id int64) error {
	res, err := e.db.ExecContext(ctx, `DELETE FROM policy_rules WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (e *Engine) audit(req Request, outcome, detail string) {
	d := fmt.Sprintf("effect=%s%s %s", req.Effect, map[bool]string{true: " (hint)", false: ""}[req.FromHint], detail)
	if req.Detail != "" {
		d += " | " + untrusted.Line(req.Detail, 300)
	}
	_, err := e.db.Exec(`INSERT INTO audit_events(at,actor,action,target,outcome,detail) VALUES(?,?,?,?,?,?)`,
		time.Now().Unix(), req.Actor, req.Action, req.Resource, outcome, d)
	if err != nil {
		e.log.Warn("audit write failed", "err", err)
	}
}

// match reports whether text matches pattern, where '*' matches any run of characters.
func match(pattern, text string) bool {
	if pattern == "*" {
		return true
	}
	p, t := 0, 0
	star, mark := -1, 0
	for t < len(text) {
		switch {
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, t
			p++
		case p < len(pattern) && pattern[p] == text[t]:
			p++
			t++
		case star >= 0:
			p = star + 1
			mark++
			t = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

func specificity(pattern string) int { return len(pattern) - strings.Count(pattern, "*") }
