package agents

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/pricing"
)

// ErrBadUsageQuery is returned for a UsageQuery the daemon cannot answer.
var ErrBadUsageQuery = errors.New("agents: invalid usage query")

// Usage query bounds.
const (
	usageLatestDefault = 10
	usageLatestMax     = 50
	usageDays          = 365
)

// UsageQuery is the agents.usage request.
type UsageQuery struct {
	ByAgentRange string `json:"byAgentRange"` // "24h" | "7d" | "all"; default "24h"
	Latest       int    `json:"latest"`       // latest sessions to return; default 10, max 50
}

// TokenTotals sums billed tokens and their cost.
type TokenTotals struct {
	Input      int64 `json:"input"`  // uncached input tokens
	Output     int64 `json:"output"` // includes reasoning
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Reasoning  int64 `json:"reasoning"` // informational subset of Output
	Total      int64 `json:"total"`     // input + output + cacheRead + cacheWrite
	// Work is input + cacheWrite + output: Total without cache reads, the context re-sent on every
	// request, which dwarf the rest. Every token headline shows Work.
	Work    int64   `json:"work"`
	CostUSD float64 `json:"costUsd"` // harness-reported cost where given, else catalogue price; unpriced tokens add 0
	// Unpriced lists distinct "provider/model" (or "kind/model") with tokens but no reported cost
	// and no catalogue price: CostUSD is then a lower bound.
	Unpriced []string `json:"unpriced"`
}

// add folds one ledger amount and its cost into t; Unpriced is left to the caller.
func (t *TokenTotals) add(a usageAmount, cost float64) {
	t.Input += a.input
	t.Output += a.output
	t.CacheRead += a.cacheRead
	t.CacheWrite += a.cacheWrite
	t.Reasoning += a.reasoning
	t.Total += a.input + a.output + a.cacheRead + a.cacheWrite
	t.Work += a.work()
	t.CostUSD += cost
}

// UsageWindow summarises one time window.
type UsageWindow struct {
	Sessions   int         `json:"sessions"`   // sessions started in the window
	Agents     []string    `json:"agents"`     // kinds with tokens or sessions in the window, by tokens desc
	ActiveDays int         `json:"activeDays"` // local days in the window with tokens
	Tokens     TokenTotals `json:"tokens"`     // usage reported in the window
}

// DailyUsage is one local day.
type DailyUsage struct {
	Day      string  `json:"day"`      // local date "2006-01-02"
	Sessions int     `json:"sessions"` // sessions started that day
	Tokens   int64   `json:"tokens"`   // TokenTotals.Work: cache reads excluded
	CostUSD  float64 `json:"costUsd"`
}

// AgentUsage is one harness kind's usage in UsageQuery.ByAgentRange.
type AgentUsage struct {
	Kind     string      `json:"kind"`
	Sessions int         `json:"sessions"`
	Tokens   TokenTotals `json:"tokens"`
}

// SessionUsage is one session's lifetime usage.
type SessionUsage struct {
	ID          string      `json:"id"`
	Kind        string      `json:"kind"`
	Title       string      `json:"title"` // Session.Title, else the task's first line, else the project
	Project     string      `json:"project"`
	Model       string      `json:"model"`
	Provider    string      `json:"provider"`
	Status      string      `json:"status"`
	ToolCalls   int         `json:"toolCalls"`
	FailedTools int         `json:"failedTools"`
	Tokens      TokenTotals `json:"tokens"`
	StartedAt   time.Time   `json:"startedAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
}

// UsageStats is the agents.usage reply.
type UsageStats struct {
	GeneratedAt     time.Time      `json:"generatedAt"`
	Day             UsageWindow    `json:"day"`     // last 24 hours
	Week            UsageWindow    `json:"week"`    // last 7 days
	All             UsageWindow    `json:"all"`     // everything recorded
	ByAgent         []AgentUsage   `json:"byAgent"` // for ByAgentRange, tokens desc
	Daily           []DailyUsage   `json:"daily"`   // 365 local days, oldest first, ending today
	Latest          []SessionUsage `json:"latest"`  // most recently updated sessions first
	PricesUpdatedAt time.Time      `json:"pricesUpdatedAt"`
}

// Usage reports token usage and spend from the usage ledger. Its 15-minute buckets place usage in
// the 24-hour and 7-day windows (a bucket counts when it starts inside the window's first quarter
// hour or later) and on the local day it starts in, which is exact for every UTC offset (all are
// whole quarter hours). Cost is priced now, so a catalogue refresh
// reprices history; a harness-reported cost always wins.
func (m *Manager) Usage(ctx context.Context, q UsageQuery) (UsageStats, error) {
	switch q.ByAgentRange {
	case "":
		q.ByAgentRange = "24h"
	case "24h", "7d", "all":
	default:
		return UsageStats{}, fmt.Errorf("%w: byAgentRange must be 24h, 7d or all", ErrBadUsageQuery)
	}
	if q.Latest <= 0 {
		q.Latest = usageLatestDefault
	}
	q.Latest = min(q.Latest, usageLatestMax)
	if err := ctx.Err(); err != nil {
		return UsageStats{}, err
	}
	rows, err := m.st.usageRows()
	if err != nil {
		return UsageStats{}, err
	}
	starts, err := m.st.sessionStarts()
	if err != nil {
		return UsageStats{}, err
	}
	recent, err := m.st.recentSessions(q.Latest)
	if err != nil {
		return UsageStats{}, err
	}
	m.mu.Lock()
	for i, s := range recent {
		if ls := m.live[s.ID]; ls != nil {
			recent[i] = ls.s // the stored row may trail the live state by a debounce
		}
	}
	m.mu.Unlock()
	slices.SortStableFunc(recent, func(a, b Session) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return buildUsageStats(time.Now(), time.Local, q, rows, starts, recent, m.cfg.Prices), nil
}

// usagePricer prices ledger buckets, memoising catalogue lookups for one report.
type usagePricer struct {
	cat   *pricing.Catalog
	cache map[[2]string]*pricing.Price
}

// price returns provider/model's catalogue price, or nil. A pricer without a cache looks up each time.
func (p *usagePricer) price(provider, model string) *pricing.Price {
	k := [2]string{provider, model}
	if v, ok := p.cache[k]; ok {
		return v
	}
	var v *pricing.Price
	if price, ok := p.cat.Lookup(provider, model); ok {
		v = &price
	}
	if p.cache != nil {
		p.cache[k] = v
	}
	return v
}

// cost prices a ledger amount of kind's provider/model: the harness-reported cost when every
// increment carried one, else the catalogue price. unpriced labels tokens with neither.
func (p *usagePricer) cost(a usageAmount, kind, provider, model string) (cost float64, unpriced string) {
	if a.hasReportedCost {
		return a.reportedCost, ""
	}
	if price := p.price(provider, model); price != nil {
		return price.Cost(pricing.Tokens{Input: a.input, Output: a.output, CacheRead: a.cacheRead, CacheWrite: a.cacheWrite}), ""
	}
	if a.input+a.output+a.cacheRead+a.cacheWrite > 0 {
		return 0, usageLabel(provider, kind, model)
	}
	return 0, ""
}

// usageAcc accumulates TokenTotals; unpriced collects labels until finish.
type usageAcc struct {
	t        TokenTotals
	unpriced map[string]bool
}

func (a *usageAcc) add(r usageRow, cost float64, unpriced string) {
	if a.unpriced == nil {
		a.unpriced = map[string]bool{}
	}
	a.t.add(r.amount, cost)
	if unpriced != "" {
		a.unpriced[unpriced] = true
	}
}

func (a *usageAcc) totals() TokenTotals {
	t := a.t
	t.Unpriced = []string{}
	for label := range a.unpriced {
		t.Unpriced = append(t.Unpriced, label)
	}
	slices.Sort(t.Unpriced)
	return t
}

// windowAcc accumulates one UsageWindow.
type windowAcc struct {
	from     time.Time // zero = all time
	tokens   usageAcc
	sessions int
	byKind   map[string]*AgentUsage
	kindAcc  map[string]*usageAcc
	days     map[string]bool
}

func newWindowAcc(from time.Time) *windowAcc {
	return &windowAcc{from: from, byKind: map[string]*AgentUsage{}, kindAcc: map[string]*usageAcc{}, days: map[string]bool{}}
}

func (w *windowAcc) kind(k string) (*AgentUsage, *usageAcc) {
	if w.byKind[k] == nil {
		w.byKind[k], w.kindAcc[k] = &AgentUsage{Kind: k}, &usageAcc{}
	}
	return w.byKind[k], w.kindAcc[k]
}

func (w *windowAcc) agents() []AgentUsage {
	out := make([]AgentUsage, 0, len(w.byKind))
	for k, a := range w.byKind {
		a.Tokens = w.kindAcc[k].totals()
		out = append(out, *a)
	}
	slices.SortFunc(out, func(a, b AgentUsage) int {
		return cmp.Or(cmp.Compare(b.Tokens.Work, a.Tokens.Work), cmp.Compare(b.Tokens.Total, a.Tokens.Total), cmp.Compare(a.Kind, b.Kind))
	})
	return out
}

func (w *windowAcc) window() UsageWindow {
	kinds := []string{}
	for _, a := range w.agents() {
		kinds = append(kinds, a.Kind)
	}
	return UsageWindow{Sessions: w.sessions, Agents: kinds, ActiveDays: len(w.days), Tokens: w.tokens.totals()}
}

func buildUsageStats(now time.Time, loc *time.Location, q UsageQuery, rows []usageRow, starts []sessionStart, recent []Session, cat *pricing.Catalog) UsageStats {
	pricer := &usagePricer{cat: cat, cache: map[[2]string]*pricing.Price{}}
	day, week, all := newWindowAcc(now.Add(-24*time.Hour)), newWindowAcc(now.Add(-7*24*time.Hour)), newWindowAcc(time.Time{})
	windows := []*windowAcc{day, week, all}

	today := now.In(loc)
	first := time.Date(today.Year(), today.Month(), today.Day()-(usageDays-1), 0, 0, 0, 0, loc)
	daily := make([]DailyUsage, usageDays)
	dayIndex := make(map[string]int, usageDays)
	for i := range daily {
		daily[i].Day = first.AddDate(0, 0, i).Format(time.DateOnly)
		dayIndex[daily[i].Day] = i
	}

	for _, s := range starts {
		for _, w := range windows {
			if !s.at.Before(w.from) {
				w.sessions++
				a, _ := w.kind(s.kind)
				a.Sessions++
			}
		}
		if i, ok := dayIndex[s.at.In(loc).Format(time.DateOnly)]; ok {
			daily[i].Sessions++
		}
	}

	perSession := map[string]*usageAcc{}
	for _, r := range rows {
		cost, unpriced := pricer.cost(r.amount, r.kind, r.key.provider, r.key.model)
		tokens := r.amount.input + r.amount.output + r.amount.cacheRead + r.amount.cacheWrite
		start := time.Unix(r.key.start, 0)
		local := start.In(loc).Format(time.DateOnly)
		for _, w := range windows {
			if start.Before(w.from.Truncate(usageBucket)) {
				continue
			}
			w.tokens.add(r, cost, unpriced)
			_, acc := w.kind(r.kind)
			acc.add(r, cost, unpriced)
			if tokens > 0 {
				w.days[local] = true
			}
		}
		if i, ok := dayIndex[local]; ok {
			daily[i].Tokens += r.amount.work()
			daily[i].CostUSD += cost
		}
		if perSession[r.key.session] == nil {
			perSession[r.key.session] = &usageAcc{}
		}
		perSession[r.key.session].add(r, cost, unpriced)
	}

	byAgent := day
	switch q.ByAgentRange {
	case "7d":
		byAgent = week
	case "all":
		byAgent = all
	}
	latest := make([]SessionUsage, 0, len(recent))
	for _, s := range recent {
		acc := perSession[s.ID]
		if acc == nil {
			acc = &usageAcc{}
		}
		latest = append(latest, SessionUsage{ID: s.ID, Kind: s.Kind, Title: usageTitle(s), Project: s.Project,
			Model: s.Model, Provider: sessionProvider(s.Kind, s.Model), Status: string(s.Status),
			ToolCalls: s.ToolCalls, FailedTools: s.FailedTools, Tokens: acc.totals(), StartedAt: s.StartedAt, UpdatedAt: s.UpdatedAt})
	}
	return UsageStats{GeneratedAt: now.UTC(), Day: day.window(), Week: week.window(), All: all.window(),
		ByAgent: byAgent.agents(), Daily: daily, Latest: latest, PricesUpdatedAt: cat.UpdatedAt()}
}

// usageLabel names an unpriced model: "provider/model", else "kind/model".
func usageLabel(provider, kind, model string) string {
	if model == "" {
		model = "unknown"
	}
	if provider == "" {
		provider = kind
	}
	return provider + "/" + model
}

// usageTitle mirrors the frontend's sessionTitle: the title, else the task's first line, else the project.
func usageTitle(s Session) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if line, _, _ := strings.Cut(strings.TrimSpace(s.Task), "\n"); strings.TrimSpace(line) != "" {
		return truncate(strings.TrimSpace(line), 80)
	}
	if s.Project == "" {
		return ""
	}
	return filepath.Base(s.Project)
}
