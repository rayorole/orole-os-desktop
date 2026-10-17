package agents

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/pricing"
)

// EvTokens is a billed-token report from a harness; Data = TokenUsage. The manager folds it into
// the usage ledger (agent_usage) and neither records nor publishes it as an event.
//
// Exactly one summable source per harness emits it, so nothing is counted twice:
//   - Claude Code: the per-model deltas of result.modelUsage, which Claude Code keeps cumulative
//     per process (falling back to result.usage, the per-turn total, when modelUsage carries no
//     token counts). A resumed process restores the session's cumulative modelUsage, so its first
//     result bills result.usage and only sets the baseline. Assistant-message usage is a context
//     reading and is never billed; it only feeds the live EvTokenEstimate.
//   - Codex: thread/tokenUsage/updated "last" (one model request), once per change of the
//     thread's cumulative total, since Codex repeats the notification with unchanged totals.
//   - Oh My Pi: message_end usage of each assistant message. get_state usage is occupancy only.
//
// Terminal and process sessions report no usage.
const EvTokens = "tokens"

// EvTokenEstimate is a provisional reading of one model request a harness reports before billing
// it (Claude Code bills a turn only when the turn ends); Data = TokenEstimate. It moves
// Session.Tokens while the turn runs, never the ledger, and the turn's EvTokens replace it.
// Like EvTokens it is neither recorded nor published as an event.
const EvTokenEstimate = "token_estimate"

// TokenEstimate is Event.Data for EvTokenEstimate: request ID's tokens so far, cache reads
// excluded like Session.Tokens. Repeated readings of one request are not added up; the largest counts.
type TokenEstimate struct {
	ID     string `json:"id"`
	Tokens int64  `json:"tokens"`
}

// TokenUsage is Event.Data for EvTokens: one increment of billed tokens. Input excludes cache
// reads and writes; Output includes Reasoning. CostUSD is the harness's own cost for exactly
// these tokens (nil when it reported none).
type TokenUsage struct {
	Source     string   `json:"source"`
	Provider   string   `json:"provider,omitempty"` // models.dev provider id or harness provider ("" = derive from the session)
	Model      string   `json:"model,omitempty"`    // "" = the session's model
	Input      int64    `json:"input"`
	Output     int64    `json:"output"`
	CacheRead  int64    `json:"cacheRead"`
	CacheWrite int64    `json:"cacheWrite"`
	Reasoning  int64    `json:"reasoning"`
	CostUSD    *float64 `json:"costUsd,omitempty"`
}

func (u TokenUsage) total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

// tokensEvent returns the EvTokens event for u, or nothing when it carries no billed tokens.
// Negative counts are not observations and become zero; a reported cost that is not positive
// is treated as no report (harnesses report 0 for models they cannot price).
func tokensEvent(u TokenUsage) []Event {
	for _, v := range []*int64{&u.Input, &u.Output, &u.CacheRead, &u.CacheWrite, &u.Reasoning} {
		*v = max(*v, 0)
	}
	u.Reasoning = min(u.Reasoning, u.Output)
	if u.CostUSD != nil && !(*u.CostUSD > 0) {
		u.CostUSD = nil
	}
	if u.total() == 0 {
		return nil
	}
	return []Event{{Kind: EvTokens, Data: jraw(u)}}
}

func valueOr0(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// usageBucket is the ledger's time granularity: a quarter hour, so every bucket falls on exactly
// one local day in any time zone (UTC offsets are whole quarter hours, e.g. +05:30, +05:45).
const usageBucket = 15 * time.Minute

// usageKey is one ledger bucket.
type usageKey struct {
	session, provider, model string
	start                    int64 // Unix seconds of the bucket's UTC start (a multiple of usageBucket)
}

// usageAmount is a ledger bucket's accumulated value.
type usageAmount struct {
	input, output, cacheRead, cacheWrite, reasoning int64
	reportedCost                                    float64
	hasReportedCost                                 bool // every increment carried a reported cost
}

func (a usageAmount) work() int64 { return a.input + a.cacheWrite + a.output }

func (a *usageAmount) add(b usageAmount) {
	a.input += b.input
	a.output += b.output
	a.cacheRead += b.cacheRead
	a.cacheWrite += b.cacheWrite
	a.reasoning += b.reasoning
	a.reportedCost += b.reportedCost
	a.hasReportedCost = a.hasReportedCost && b.hasReportedCost
}

// sessionProvider is the provider a session's model is billed by when the harness did not say.
func sessionProvider(kind, model string) string {
	switch kind {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	case "omp":
		if p, _, ok := strings.Cut(model, "/"); ok {
			return p
		}
	}
	return ""
}

// ledgerModel is the model id recorded for kind's usage: Oh My Pi's "provider/id" selector loses
// its provider prefix, which the ledger keeps in its own column.
func ledgerModel(kind, model string) string {
	if kind == "omp" {
		if _, id, ok := strings.Cut(model, "/"); ok {
			return id
		}
	}
	return model
}

// recordTokens folds an EvTokens event into the ledger and the session's Billed breakdown. The
// caller holds ls.controlMu.
func (m *Manager) recordTokens(ls *liveSess, e Event) {
	var u TokenUsage
	if json.Unmarshal(e.Data, &u) != nil {
		return
	}
	m.mu.Lock()
	kind, model := ls.s.Kind, ls.s.Model
	m.mu.Unlock()
	provider := u.Provider
	if provider == "" {
		provider = sessionProvider(kind, model)
	}
	if u.Model == "" {
		u.Model = ledgerModel(kind, model)
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	amount := usageAmount{input: u.Input, output: u.Output, cacheRead: u.CacheRead, cacheWrite: u.CacheWrite,
		reasoning: u.Reasoning, hasReportedCost: u.CostUSD != nil}
	if u.CostUSD != nil {
		amount.reportedCost = *u.CostUSD
	}
	m.st.QueueUsage(usageKey{session: ls.id, provider: provider, model: u.Model, start: at.UTC().Truncate(usageBucket).Unix()}, amount)
	cost, unpriced := (&usagePricer{cat: m.cfg.Prices}).cost(amount, kind, provider, u.Model)
	// Billing settles the turn's estimates: the billed count replaces them.
	estimated := ls.estimated
	ls.estimates, ls.estimated = nil, 0
	m.mutateHot(ls.id, func(s *Session) {
		// A new value, never an update in place: copies of the session share the old one.
		b := TokenTotals{Unpriced: []string{}}
		if s.Billed != nil {
			b = *s.Billed
		}
		b.add(amount, cost)
		if unpriced != "" && !slices.Contains(b.Unpriced, unpriced) {
			b.Unpriced = append(slices.Clone(b.Unpriced), unpriced)
			slices.Sort(b.Unpriced)
		}
		s.Billed = &b
		s.Tokens = max(s.Tokens+amount.work()-estimated, 0)
	})
}

// estimateTokens folds an EvTokenEstimate into Session.Tokens. The caller holds ls.controlMu.
func (m *Manager) estimateTokens(ls *liveSess, e Event) {
	var t TokenEstimate
	if json.Unmarshal(e.Data, &t) != nil || t.ID == "" || t.Tokens <= ls.estimates[t.ID] {
		return
	}
	if ls.estimates == nil {
		ls.estimates = map[string]int64{}
	}
	grew := t.Tokens - ls.estimates[t.ID]
	ls.estimates[t.ID], ls.estimated = t.Tokens, ls.estimated+grew
	m.mutateHot(ls.id, func(s *Session) { s.Tokens += grew })
}

// QueueUsage adds amount to a ledger bucket. Like events, buckets are written in batches within
// eventFlushDelay and always before the ledger is read.
func (st *Store) QueueUsage(k usageKey, amount usageAmount) {
	st.evMu.Lock()
	defer st.evMu.Unlock()
	if cur, ok := st.usage[k]; ok {
		cur.add(amount)
		st.usage[k] = cur
	} else {
		st.usage[k] = amount
	}
	st.armLocked()
}

// writeUsage upserts buckets; rows of sessions deleted meanwhile fail their foreign key and are
// dropped. The first other error is returned.
func (st *Store) writeUsage(batch map[usageKey]usageAmount) error {
	var first error
	for k, a := range batch {
		_, err := st.db.Exec(`INSERT INTO agent_usage(session_id,provider,model,bucket_start,input,output,cache_read,cache_write,reasoning,reported_cost_usd,has_reported_cost)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(session_id,provider,model,bucket_start) DO UPDATE SET input=input+excluded.input, output=output+excluded.output,
				cache_read=cache_read+excluded.cache_read, cache_write=cache_write+excluded.cache_write, reasoning=reasoning+excluded.reasoning,
				reported_cost_usd=reported_cost_usd+excluded.reported_cost_usd, has_reported_cost=has_reported_cost AND excluded.has_reported_cost`,
			k.session, k.provider, k.model, k.start, a.input, a.output, a.cacheRead, a.cacheWrite, a.reasoning, a.reportedCost, a.hasReportedCost)
		if err != nil && first == nil && !strings.Contains(err.Error(), "FOREIGN KEY") {
			first = err
		}
	}
	return first
}

// usageRow is one ledger bucket with its session's kind.
type usageRow struct {
	key    usageKey
	kind   string
	amount usageAmount
}

// usageRows returns every ledger bucket, after writing the queued ones.
func (st *Store) usageRows() ([]usageRow, error) {
	if err := st.FlushEvents(); err != nil {
		return nil, err
	}
	rows, err := st.db.Query(`SELECT u.session_id, s.kind, u.provider, u.model, u.bucket_start, u.input, u.output, u.cache_read,
		u.cache_write, u.reasoning, u.reported_cost_usd, u.has_reported_cost
		FROM agent_usage u JOIN agent_sessions s ON s.id = u.session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []usageRow{}
	for rows.Next() {
		var r usageRow
		if err := rows.Scan(&r.key.session, &r.kind, &r.key.provider, &r.key.model, &r.key.start, &r.amount.input, &r.amount.output,
			&r.amount.cacheRead, &r.amount.cacheWrite, &r.amount.reasoning, &r.amount.reportedCost, &r.amount.hasReportedCost); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ledgerTotals returns each session's billed tokens and cost from the ledger, for sessions saved
// before Session.Billed existed.
func (m *Manager) ledgerTotals() (map[string]TokenTotals, error) {
	rows, err := m.st.usageRows()
	if err != nil {
		return nil, err
	}
	pricer := &usagePricer{cat: m.cfg.Prices, cache: map[[2]string]*pricing.Price{}}
	acc := map[string]*usageAcc{}
	for _, r := range rows {
		cost, unpriced := pricer.cost(r.amount, r.kind, r.key.provider, r.key.model)
		if acc[r.key.session] == nil {
			acc[r.key.session] = &usageAcc{}
		}
		acc[r.key.session].add(r, cost, unpriced)
	}
	out := make(map[string]TokenTotals, len(acc))
	for id, a := range acc {
		out[id] = a.totals()
	}
	return out, nil
}

// sessionStart is a session's kind and start time, for session counts.
type sessionStart struct {
	kind string
	at   time.Time
}

func (st *Store) sessionStarts() ([]sessionStart, error) {
	rows, err := st.db.Query(`SELECT kind, started_at FROM agent_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sessionStart{}
	for rows.Next() {
		var s sessionStart
		var at int64
		if err := rows.Scan(&s.kind, &at); err != nil {
			return nil, err
		}
		s.at = time.Unix(at, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// recentSessions returns the limit most recently updated sessions, newest first.
func (st *Store) recentSessions(limit int) ([]Session, error) {
	rows, err := st.db.Query(`SELECT data FROM agent_sessions ORDER BY updated_at DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
