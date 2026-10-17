package agents

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/pricing"
)

func billed(t *testing.T, events []Event) []TokenUsage {
	t.Helper()
	var out []TokenUsage
	for _, e := range events {
		if e.Kind == EvTokens {
			var u TokenUsage
			if err := json.Unmarshal(e.Data, &u); err != nil {
				t.Fatal(err)
			}
			out = append(out, u)
		}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Claude Code's modelUsage is cumulative per process: each result bills only its growth, per
// model, with the reported cost; assistant-message usage is never billed.
func TestClaudeBillsModelUsageGrowthOnce(t *testing.T) {
	d := &claudeDecoder{tools: map[string]ToolData{}}
	if got := billed(t, d.decode(nil, []byte(`{"type":"system","subtype":"init","session_id":"s","model":"claude-haiku-4-5-20251001"}`))); got != nil {
		t.Fatalf("init billed %+v", got)
	}
	assistant := `{"type":"assistant","message":{"id":"m","role":"assistant","model":"claude-haiku-4-5-20251001","content":[],"usage":{"input_tokens":10,"output_tokens":6,"cache_read_input_tokens":0,"cache_creation_input_tokens":24196}}}`
	if got := billed(t, d.decode(nil, []byte(assistant))); got != nil {
		t.Fatalf("assistant usage billed: %+v", got)
	}
	first := `{"type":"result","subtype":"success","result":"Hi!","total_cost_usd":0.0456921,
		"usage":{"input_tokens":19,"output_tokens":609,"cache_read_input_tokens":46481,"cache_creation_input_tokens":18990,"output_tokens_details":{"thinking_tokens":427}},
		"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":19,"outputTokens":609,"cacheReadInputTokens":46481,"cacheCreationInputTokens":18990,"costUSD":0.0456921,"contextWindow":200000}}}`
	got := billed(t, d.decode(nil, []byte(first)))
	if len(got) != 1 || got[0].Model != "claude-haiku-4-5-20251001" || got[0].Provider != "anthropic" || got[0].Input != 19 || got[0].Output != 609 ||
		got[0].CacheRead != 46481 || got[0].CacheWrite != 18990 || got[0].Reasoning != 427 || got[0].CostUSD == nil || !near(*got[0].CostUSD, 0.0456921) {
		t.Fatalf("first result: %+v", got)
	}
	second := `{"type":"result","subtype":"success","result":"Bye!","usage":{"input_tokens":10,"output_tokens":82},
		"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":29,"outputTokens":691,"cacheReadInputTokens":83642,"cacheCreationInputTokens":19294,"costUSD":0.0504362},
		"claude-sonnet-4-5":{"inputTokens":5,"outputTokens":7,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.00012}}}`
	got = billed(t, d.decode(nil, []byte(second)))
	if len(got) != 2 {
		t.Fatalf("second result: %+v", got)
	}
	haiku, sonnet := got[0], got[1]
	if haiku.Input != 10 || haiku.Output != 82 || haiku.CacheRead != 37161 || haiku.CacheWrite != 304 || !near(*haiku.CostUSD, 0.0504362-0.0456921) {
		t.Fatalf("haiku growth: %+v", haiku)
	}
	if sonnet.Model != "claude-sonnet-4-5" || sonnet.Input != 5 || sonnet.Output != 7 || !near(*sonnet.CostUSD, 0.00012) {
		t.Fatalf("new model: %+v", sonnet)
	}
	// A failed turn repeats the cumulative totals: nothing new is billed.
	if got := billed(t, d.decode(nil, []byte(second))); got != nil {
		t.Fatalf("repeated totals billed again: %+v", got)
	}
	// Without token counts in modelUsage, the per-turn usage is billed for the session model.
	old := &claudeDecoder{tools: map[string]ToolData{}}
	got = billed(t, old.decode(nil, []byte(`{"type":"result","subtype":"success","usage":{"input_tokens":3,"output_tokens":4,"cache_read_input_tokens":5,"cache_creation_input_tokens":6},"modelUsage":{"m":{"contextWindow":1000}}}`)))
	if len(got) != 1 || got[0].Input != 3 || got[0].Output != 4 || got[0].CacheRead != 5 || got[0].CacheWrite != 6 || got[0].CostUSD != nil {
		t.Fatalf("per-turn fallback: %+v", got)
	}
}

// A resumed Claude Code process restores the session's cumulative modelUsage (observed with
// Claude Code 2.1.289); a process that did not restore it starts from zero. Either way its first
// result bills only that turn's own usage, and later results bill growth.
func TestClaudeResumeDoesNotRebillEarlierUsage(t *testing.T) {
	// Readings from a real resume: the previous process ended at 10/429/16351/15253, $0.0342961.
	turn := `"usage":{"input_tokens":10,"output_tokens":58,"cache_read_input_tokens":31604,"cache_creation_input_tokens":1279,"output_tokens_details":{"thinking_tokens":51}}`
	for name, modelUsage := range map[string]string{
		"restored": `{"claude-haiku-4-5-20251001":{"inputTokens":20,"outputTokens":487,"cacheReadInputTokens":47955,"cacheCreationInputTokens":16532,"costUSD":0.0403145}}`,
		"fresh":    `{"claude-haiku-4-5-20251001":{"inputTokens":10,"outputTokens":58,"cacheReadInputTokens":31604,"cacheCreationInputTokens":1279,"costUSD":0.0060184}}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := &claudeDecoder{tools: map[string]ToolData{}, resumed: true}
			got := billed(t, d.decode(nil, []byte(`{"type":"result","subtype":"success","result":"two",`+turn+`,"modelUsage":`+modelUsage+`}`)))
			if len(got) != 1 || got[0].Input != 10 || got[0].Output != 58 || got[0].CacheRead != 31604 || got[0].CacheWrite != 1279 ||
				got[0].Reasoning != 51 || got[0].Provider != "anthropic" || got[0].CostUSD != nil {
				t.Fatalf("first result after resume: %+v", got)
			}
			var next map[string]map[string]float64
			if err := json.Unmarshal([]byte(modelUsage), &next); err != nil {
				t.Fatal(err)
			}
			u := next["claude-haiku-4-5-20251001"]
			u["inputTokens"] += 3
			u["outputTokens"] += 4
			u["costUSD"] += 0.001
			got = billed(t, d.decode(nil, []byte(`{"type":"result","subtype":"success","result":"three","usage":{"input_tokens":3,"output_tokens":4},"modelUsage":`+string(jraw(next))+`}`)))
			if len(got) != 1 || got[0].Input != 3 || got[0].Output != 4 || got[0].CacheRead != 0 || got[0].CostUSD == nil || !near(*got[0].CostUSD, 0.001) {
				t.Fatalf("second result after resume: %+v", got)
			}
		})
	}
}

// Codex repeats tokenUsage notifications with unchanged totals; each request is billed once,
// cached input is split out of input, and a resumed thread's first idle report is a baseline.
func TestCodexBillsEachRequestOnce(t *testing.T) {
	note := func(total, input, cached, output, reasoning int) json.RawMessage {
		return jraw(map[string]any{"threadId": "thread", "tokenUsage": map[string]any{
			"total": map[string]any{"totalTokens": total},
			"last":  map[string]any{"inputTokens": input, "cachedInputTokens": cached, "outputTokens": output, "reasoningOutputTokens": reasoning, "totalTokens": input + output},
		}})
	}
	p, _ := chatCodexFixture()
	p.turn = "turn"
	p.notification("thread/tokenUsage/updated", note(100, 80, 50, 20, 5))
	got := billed(t, chatDrain(p))
	if len(got) != 1 || got[0].Input != 30 || got[0].CacheRead != 50 || got[0].Output != 20 || got[0].Reasoning != 5 || got[0].CostUSD != nil {
		t.Fatalf("first request: %+v", got)
	}
	p.notification("thread/tokenUsage/updated", note(100, 80, 50, 20, 5))
	if got := billed(t, chatDrain(p)); got != nil {
		t.Fatalf("repeat billed: %+v", got)
	}
	p.notification("thread/tokenUsage/updated", note(160, 50, 0, 10, 0))
	if got := billed(t, chatDrain(p)); len(got) != 1 || got[0].Input != 50 || got[0].Output != 10 {
		t.Fatalf("second request: %+v", got)
	}

	resumed, _ := chatCodexFixture()
	resumed.notification("thread/tokenUsage/updated", note(5000, 80, 50, 20, 5))
	if got := billed(t, chatDrain(resumed)); got != nil {
		t.Fatalf("resume baseline billed: %+v", got)
	}
	resumed.turn = "turn"
	resumed.notification("thread/tokenUsage/updated", note(5100, 90, 0, 10, 0))
	if got := billed(t, chatDrain(resumed)); len(got) != 1 || got[0].Input != 90 {
		t.Fatalf("request after resume: %+v", got)
	}
}

func TestOmpBillsAssistantMessages(t *testing.T) {
	s := &ompState{}
	got := billed(t, s.messageEnd([]byte(`{"type":"message_end","message":{"role":"assistant","provider":"openrouter","model":"anthropic/claude-sonnet-4.5","content":[],
		"usage":{"input":20,"output":5,"cacheRead":10,"cacheWrite":4,"reasoningTokens":2,"totalTokens":39,"cost":{"input":0.1,"output":0.2,"cacheRead":0,"cacheWrite":0,"total":0.3}}}}`)))
	if len(got) != 1 || got[0].Provider != "openrouter" || got[0].Model != "anthropic/claude-sonnet-4.5" || got[0].Input != 20 || got[0].Output != 5 ||
		got[0].CacheRead != 10 || got[0].CacheWrite != 4 || got[0].Reasoning != 2 || got[0].CostUSD == nil || !near(*got[0].CostUSD, 0.3) {
		t.Fatalf("message_end: %+v", got)
	}
	// pi-ai reports 0 for models it cannot price: that is no report, not a free model.
	got = billed(t, s.messageEnd([]byte(`{"type":"message_end","message":{"role":"assistant","provider":"local","model":"llama","content":[],"usage":{"input":1,"output":1,"cost":{"total":0}}}}`)))
	if len(got) != 1 || got[0].CostUSD != nil {
		t.Fatalf("zero cost kept: %+v", got)
	}
	if got := billed(t, s.response([]byte(`{"type":"response","command":"get_state","success":true,"data":{"contextUsage":{"tokens":120,"contextWindow":4000}}}`))); got != nil {
		t.Fatalf("context occupancy billed: %+v", got)
	}
}

func ledger(t *testing.T, m *Manager) map[usageKey]usageAmount {
	t.Helper()
	rows, err := m.st.usageRows()
	if err != nil {
		t.Fatal(err)
	}
	out := map[usageKey]usageAmount{}
	for _, r := range rows {
		out[r.key] = r.amount
	}
	return out
}

// The session's token count moves with each request's estimate while a Claude turn runs, a
// repeated reading of one request is not added again, and the turn's billing replaces the
// estimates rather than adding to them.
func TestSessionTokensLiveThenSettledByBilling(t *testing.T) {
	m, _, _ := chatFixture(t, StatusWorking)
	d := &claudeDecoder{tools: make(map[string]ToolData)}
	tokens := func() int64 {
		s, err := m.Get("chat")
		if err != nil {
			t.Fatal(err)
		}
		return s.Tokens
	}
	feed := func(line string) {
		for _, e := range d.decode(nil, []byte(line)) {
			m.apply("chat", e)
		}
	}
	feed(`{"type":"assistant","parent_tool_use_id":null,"message":{"id":"m1","usage":{"input_tokens":2,"cache_read_input_tokens":900,"cache_creation_input_tokens":100,"output_tokens":8},"content":[{"type":"text","text":"a"}]}}`)
	if got := tokens(); got != 1010 {
		t.Fatalf("after first request: %d", got)
	}
	feed(`{"type":"assistant","parent_tool_use_id":null,"message":{"id":"m1","usage":{"input_tokens":2,"cache_read_input_tokens":900,"cache_creation_input_tokens":100,"output_tokens":50},"content":[{"type":"text","text":"b"}]}}`)
	feed(`{"type":"assistant","parent_tool_use_id":"call","message":{"id":"sub1","usage":{"input_tokens":500},"content":[{"type":"text","text":"c"}]}}`)
	if got := tokens(); got != 1552 {
		t.Fatalf("repeated reading and subagent request: %d", got)
	}
	feed(`{"type":"result","subtype":"success","model":"claude-sonnet-5-5","result":"done","modelUsage":{"claude-sonnet-5-5":{"inputTokens":600,"outputTokens":60,"cacheReadInputTokens":900,"cacheCreationInputTokens":100}}}`)
	if got := tokens(); got != 1660 {
		t.Fatalf("billed turn: %d", got)
	}
	feed(`{"type":"assistant","parent_tool_use_id":null,"message":{"id":"m2","usage":{"input_tokens":40},"content":[{"type":"text","text":"d"}]}}`)
	if got := tokens(); got != 1700 {
		t.Fatalf("next turn adds to the billed total: %d", got)
	}
}

func TestManagerLedgerAccumulatesAndDeletesWithSession(t *testing.T) {
	m, ls, _ := chatFixture(t, StatusWorking)
	m.mu.Lock()
	ls.s.Model = "claude-opus-4-1"
	m.mu.Unlock()
	hour := time.Now().UTC().Truncate(usageBucket) // every report below lands in this bucket
	cost := 0.5
	m.apply("chat", Event{Kind: EvTokens, At: hour.Add(time.Minute), Data: jraw(TokenUsage{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, Reasoning: 5, CostUSD: &cost})})
	m.apply("chat", Event{Kind: EvTokens, At: hour.Add(2 * time.Minute), Data: jraw(TokenUsage{Input: 1, Output: 2, CostUSD: &cost})})
	m.apply("chat", Event{Kind: EvTokens, At: hour.Add(3 * time.Minute), Data: jraw(TokenUsage{Model: "claude-haiku-4-5", Input: 7})})
	m.apply("chat", Event{Kind: EvTokens, At: hour.Add(3 * time.Minute), Data: jraw(TokenUsage{Model: "claude-haiku-4-5", Input: 1, CostUSD: &cost})})
	in := int64(999)
	m.apply("chat", Event{Kind: EvUsage, Data: jraw(ContextUsage{Source: "claude:assistant:usage", InputTokens: &in})})
	got := ledger(t, m)
	opus := got[usageKey{session: "chat", provider: "anthropic", model: "claude-opus-4-1", start: hour.Unix()}]
	if len(got) != 2 || opus != (usageAmount{input: 11, output: 22, cacheRead: 30, cacheWrite: 40, reasoning: 5, reportedCost: 1, hasReportedCost: true}) {
		t.Fatalf("ledger: %+v", got)
	}
	// One unreported increment makes the bucket priced from the catalogue.
	if haiku := got[usageKey{session: "chat", provider: "anthropic", model: "claude-haiku-4-5", start: hour.Unix()}]; haiku.input != 8 || haiku.hasReportedCost {
		t.Fatalf("mixed bucket: %+v", haiku)
	}
	// A later flush adds to the stored bucket.
	m.apply("chat", Event{Kind: EvTokens, At: hour.Add(4 * time.Minute), Data: jraw(TokenUsage{Input: 100, CostUSD: &cost})})
	if opus := ledger(t, m)[usageKey{session: "chat", provider: "anthropic", model: "claude-opus-4-1", start: hour.Unix()}]; opus.input != 111 || !near(opus.reportedCost, 1.5) || !opus.hasReportedCost {
		t.Fatalf("stored bucket: %+v", opus)
	}
	if evs, err := m.st.Events("chat", 0, 0, EvTokens); err != nil || len(evs) != 0 {
		t.Fatalf("token reports were recorded as history: %v %v", evs, err)
	}
	m.apply("chat", Event{Kind: EvTokens, Data: jraw(TokenUsage{Input: 5})}) // queued, not yet written
	if err := m.st.DeleteSession("chat"); err != nil {
		t.Fatal(err)
	}
	if got := ledger(t, m); len(got) != 0 {
		t.Fatalf("ledger rows survived their session: %+v", got)
	}
}

func TestManagerLedgerProviderFromOmpModel(t *testing.T) {
	m, ls, _ := chatFixture(t, StatusWorking)
	m.mu.Lock()
	ls.s.Kind, ls.s.Model = "omp", "openrouter/anthropic/claude-sonnet-4.5"
	m.mu.Unlock()
	m.apply("chat", Event{Kind: EvTokens, Data: jraw(TokenUsage{Input: 1})})
	m.apply("chat", Event{Kind: EvTokens, Data: jraw(TokenUsage{Provider: "anthropic", Model: "claude-opus-4-1", Input: 2})})
	keys := []string{}
	for k := range ledger(t, m) {
		keys = append(keys, k.provider+"|"+k.model)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"anthropic|claude-opus-4-1", "openrouter|anthropic/claude-sonnet-4.5"}) {
		t.Fatalf("ledger keys: %v", keys)
	}
}

func TestToolCountersCountEachCallOnce(t *testing.T) {
	m, _, _ := chatFixture(t, StatusWorking)
	tool := func(kind, id, status string) {
		m.apply("chat", Event{Kind: kind, Text: "bash: ls", Data: jraw(ToolData{ID: id, Name: "bash", Status: status})})
	}
	tool(EvTool, "a", "running")
	tool(EvTool, "a", "running") // a re-reported call
	tool(EvToolResult, "a", "error")
	tool(EvToolResult, "b", "ok")                                                                     // a result whose call never arrived
	tool(EvToolResult, "", "error")                                                                   // unattributable result: a failure, not a call
	m.apply("chat", Event{Kind: EvTool, Text: "Read: x", Data: jraw(map[string]any{"tool": "Read"})}) // terminal hook call
	s, err := m.Get("chat")
	if err != nil {
		t.Fatal(err)
	}
	if s.ToolCalls != 3 || s.FailedTools != 2 {
		t.Fatalf("toolCalls=%d failedTools=%d", s.ToolCalls, s.FailedTools)
	}
}

func statsCatalog(t *testing.T) *pricing.Catalog {
	t.Helper()
	c, err := pricing.Parse([]byte(`{"fetchedAt":"2026-03-01T00:00:00Z","providers":{
"anthropic":{"claude-opus-4-1":{"input":10,"output":100,"cache_read":1,"cache_write":20}},
"openai":{"gpt-5":{"input":1,"output":10,"cache_read":0.1}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuildUsageStatsWindows(t *testing.T) {
	loc := time.FixedZone("test", 2*3600)
	now := time.Date(2026, 3, 10, 12, 30, 0, 0, loc)
	bucketAt := func(d time.Duration) int64 { return now.Add(-d).UTC().Truncate(usageBucket).Unix() }
	row := func(session, kind, provider, model string, ago time.Duration, a usageAmount) usageRow {
		return usageRow{key: usageKey{session: session, provider: provider, model: model, start: bucketAt(ago)}, kind: kind, amount: a}
	}
	rows := []usageRow{
		// 1M input + 1M output on opus = $110, two hours ago.
		row("c1", "claude", "anthropic", "claude-opus-4-1", 2*time.Hour, usageAmount{input: 1_000_000, output: 1_000_000, reasoning: 10}),
		// Reported cost wins over the catalogue.
		row("x1", "codex", "openai", "gpt-5", 3*24*time.Hour, usageAmount{input: 1_000_000, reportedCost: 0.25, hasReportedCost: true}),
		// Unpriced model, with and without a provider.
		row("o1", "omp", "", "mystery", 30*24*time.Hour, usageAmount{output: 500}),
		row("o1", "omp", "local", "llama", 30*24*time.Hour, usageAmount{output: 100}),
		// Older than the daily chart, still counted in all.
		row("c1", "claude", "anthropic", "claude-opus-4-1", 400*24*time.Hour, usageAmount{cacheRead: 1_000_000, cacheWrite: 1_000_000}),
	}
	starts := []sessionStart{
		{"claude", now.Add(-3 * time.Hour)}, {"codex", now.Add(-3 * 24 * time.Hour)}, {"omp", now.Add(-30 * 24 * time.Hour)}, {"claude", now.Add(-400 * 24 * time.Hour)},
		{"gemini", now.Add(-time.Hour)}, // a session with no usage still counts and lists its agent
	}
	recent := []Session{
		{ID: "x1", Kind: "codex", Model: "gpt-5", Task: "Fix the build\nand more", Status: StatusDone, ToolCalls: 4, FailedTools: 1},
		{ID: "c1", Kind: "claude", Model: "claude-opus-4-1", Title: "Named", Project: "/code/app"},
		{ID: "none", Kind: "custom", Project: "/code/app"},
	}
	st := buildUsageStats(now, loc, UsageQuery{ByAgentRange: "7d"}, rows, starts, recent, statsCatalog(t))

	if st.Day.Sessions != 2 || st.Day.Tokens.Total != 2_000_000 || !near(st.Day.Tokens.CostUSD, 110) || st.Day.ActiveDays != 1 ||
		!slices.Equal(st.Day.Agents, []string{"claude", "gemini"}) || st.Day.Tokens.Reasoning != 10 || len(st.Day.Tokens.Unpriced) != 0 {
		t.Fatalf("day: %+v", st.Day)
	}
	if st.Week.Sessions != 3 || st.Week.Tokens.Total != 3_000_000 || !near(st.Week.Tokens.CostUSD, 110.25) || st.Week.ActiveDays != 2 ||
		!slices.Equal(st.Week.Agents, []string{"claude", "codex", "gemini"}) {
		t.Fatalf("week: %+v", st.Week)
	}
	if st.All.Sessions != 5 || st.All.Tokens.Total != 5_000_600 || !near(st.All.Tokens.CostUSD, 110.25+21) || st.All.ActiveDays != 4 ||
		!slices.Equal(st.All.Tokens.Unpriced, []string{"local/llama", "omp/mystery"}) {
		t.Fatalf("all: %+v", st.All)
	}
	if len(st.ByAgent) != 3 || st.ByAgent[0].Kind != "claude" || st.ByAgent[0].Sessions != 1 || st.ByAgent[1].Kind != "codex" ||
		st.ByAgent[1].Tokens.Total != 1_000_000 || !near(st.ByAgent[1].Tokens.CostUSD, 0.25) || st.ByAgent[2].Kind != "gemini" || st.ByAgent[2].Sessions != 1 {
		t.Fatalf("byAgent 7d: %+v", st.ByAgent)
	}
	if len(st.Daily) != 365 || st.Daily[364].Day != "2026-03-10" || st.Daily[0].Day != "2025-03-11" {
		t.Fatalf("daily bounds: %d %+v %+v", len(st.Daily), st.Daily[0], st.Daily[len(st.Daily)-1])
	}
	if d := st.Daily[364]; d.Sessions != 2 || d.Tokens != 2_000_000 || !near(d.CostUSD, 110) {
		t.Fatalf("today: %+v", d)
	}
	if d := st.Daily[364-30]; d.Sessions != 1 || d.Tokens != 600 || d.CostUSD != 0 {
		t.Fatalf("30 days ago: %+v", d)
	}
	if len(st.Latest) != 3 || st.Latest[0].Title != "Fix the build" || st.Latest[0].Provider != "openai" || st.Latest[0].ToolCalls != 4 || st.Latest[0].FailedTools != 1 ||
		!near(st.Latest[0].Tokens.CostUSD, 0.25) || st.Latest[1].Title != "Named" || st.Latest[1].Tokens.Total != 4_000_000 ||
		st.Latest[2].Title != "app" || st.Latest[2].Tokens.Total != 0 || st.Latest[2].Tokens.Unpriced == nil {
		t.Fatalf("latest: %+v", st.Latest)
	}
	if !st.PricesUpdatedAt.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("pricesUpdatedAt: %v", st.PricesUpdatedAt)
	}
	if all := buildUsageStats(now, loc, UsageQuery{ByAgentRange: "all"}, rows, starts, recent, statsCatalog(t)); len(all.ByAgent) != 4 || all.ByAgent[0].Kind != "claude" || all.ByAgent[0].Sessions != 2 {
		t.Fatalf("byAgent all: %+v", all.ByAgent)
	}
}

// In a half-hour zone, tokens reported just after local midnight belong to the new day and those
// just before it to the old one; the 24-hour window starts at the quarter hour it began in.
func TestUsageBucketsFollowHalfHourZones(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+30*60)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)
	at := func(day, hour, minute int, input int64) usageRow {
		start := time.Date(2026, 3, day, hour, minute, 0, 0, loc).UTC().Truncate(usageBucket).Unix()
		return usageRow{key: usageKey{session: "s", provider: "anthropic", model: "claude-opus-4-1", start: start}, kind: "claude", amount: usageAmount{input: input}}
	}
	rows := []usageRow{
		at(10, 0, 10, 1),    // 00:10 local today
		at(9, 23, 50, 10),   // 23:50 local yesterday
		at(9, 12, 5, 100),   // inside the 24-hour window, which opened at 12:00 local yesterday
		at(9, 11, 50, 1000), // just before it
	}
	st := buildUsageStats(now, loc, UsageQuery{}, rows, nil, nil, statsCatalog(t))
	today, yesterday := st.Daily[364], st.Daily[363]
	if today.Day != "2026-03-10" || today.Tokens != 1 || yesterday.Day != "2026-03-09" || yesterday.Tokens != 1110 {
		t.Fatalf("daily: today %+v, yesterday %+v", today, yesterday)
	}
	if st.Day.Tokens.Total != 111 || st.Day.ActiveDays != 2 {
		t.Fatalf("day window: %+v", st.Day)
	}
}

func TestUsageQueryValidation(t *testing.T) {
	m, _, _ := chatFixture(t, StatusWorking)
	if _, err := m.Usage(context.Background(), UsageQuery{ByAgentRange: "1y"}); !errors.Is(err, ErrBadUsageQuery) {
		t.Fatalf("bad range: %v", err)
	}
	st, err := m.Usage(context.Background(), UsageQuery{Latest: 500})
	if err != nil || len(st.Daily) != usageDays || len(st.Latest) != 1 || st.Latest[0].ID != "chat" || st.Day.Tokens.Unpriced == nil || st.ByAgent == nil {
		t.Fatalf("defaults: %+v %v", st, err)
	}
}
