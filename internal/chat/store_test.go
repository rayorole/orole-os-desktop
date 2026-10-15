package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/llm/llmtest"
)

func TestBotCRUDAndNormalisation(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	b, err := e.m.SaveBot(ctx, BotInput{
		Name: "  Ada  ", Color: "blue", Avatar: "🦉", Instructions: "Be Ada.", Provider: "fake", Model: "m1",
		Config: BotConfig{
			Tools:      []BotTool{{ID: ToolWebSearch}, {ID: ToolWebSearch}, {ID: ToolDatetime}},
			MCPServers: []string{"srv-a", "srv-a", "srv-b"},
			Memory:     []ScopeAccess{{Scope: ScopeUser, Write: true}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "Ada" || len(b.Config.Tools) != 2 || len(b.Config.MCPServers) != 2 {
		t.Fatalf("not normalised: %+v", b)
	}
	if !b.Config.Memory[0].Read {
		t.Fatal("write access must imply read")
	}
	if b.Config.Skills == nil {
		t.Fatal("lists must be non-nil so the UI receives arrays")
	}
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM bot_memory_access WHERE bot_id=? AND scope='user' AND can_write=1`, b.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("bot_memory_access not synced: %d", n)
	}

	b.Config.Memory = nil
	upd, err := e.m.SaveBot(ctx, BotInput{ID: b.ID, Name: "Ada 2", Provider: "fake", Model: "m1", Config: b.Config})
	if err != nil || upd.Name != "Ada 2" || upd.CreatedAt != b.CreatedAt {
		t.Fatalf("update: %+v %v", upd, err)
	}
	e.db.QueryRow(`SELECT COUNT(*) FROM bot_memory_access WHERE bot_id=?`, b.ID).Scan(&n)
	if n != 0 {
		t.Fatal("access rows must follow the config")
	}
	if _, err := e.m.SaveBot(ctx, BotInput{ID: "nope", Name: "X"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of unknown bot: %v", err)
	}

	list, err := e.m.Bots(ctx)
	if err != nil || len(list) != 1 || list[0].LastMessage != nil || list[0].Running {
		t.Fatalf("list: %+v %v", list, err)
	}
	if err := e.m.ArchiveBot(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.m.Bots(ctx); len(list) != 0 {
		t.Fatal("archived bot must be hidden")
	}
	if _, err := e.m.Bot(ctx, b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("archived bot lookup: %v", err)
	}
	if _, err := e.m.Send(ctx, SendRequest{BotID: b.ID, Text: "hi"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("send to archived bot: %v", err)
	}
}

func TestBotValidation(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	long := strings.Repeat("a", MaxInstructionsLen+1)
	cases := []struct {
		name string
		in   BotInput
	}{
		{"empty name", BotInput{Name: "  "}},
		{"long name", BotInput{Name: strings.Repeat("n", MaxNameLen+1)}},
		{"long instructions", BotInput{Name: "x", Instructions: long}},
		{"long description", BotInput{Name: "x", Description: strings.Repeat("d", 501)}},
		{"bad color", BotInput{Name: "x", Color: "Not A Token"}},
		{"provider without model", BotInput{Name: "x", Provider: "fake"}},
		{"unknown tool", BotInput{Name: "x", Config: BotConfig{Tools: []BotTool{{ID: "shell"}}}}},
		{"bad mcp server id", BotInput{Name: "x", Config: BotConfig{MCPServers: []string{"srv/tool"}}}},
		{"empty mcp server id", BotInput{Name: "x", Config: BotConfig{MCPServers: []string{""}}}},
		{"unknown scope", BotInput{Name: "x", Config: BotConfig{Memory: []ScopeAccess{{Scope: "shared:ghost", Read: true}}}}},
		{"private scope grant", BotInput{Name: "x", Config: BotConfig{Memory: []ScopeAccess{{Scope: "bot:abc", Read: true}}}}},
		{"negative steps", BotInput{Name: "x", Config: BotConfig{Limits: BotLimits{MaxSteps: -1}}}},
		{"too many calls", BotInput{Name: "x", Config: BotConfig{Limits: BotLimits{MaxToolCalls: 1000}}}},
		{"unknown tool access", BotInput{Name: "x", Config: BotConfig{ToolAccess: "everything"}}},
		{"capitalised tool access", BotInput{Name: "x", Config: BotConfig{ToolAccess: "Write"}}},
		{"unknown task permission mode", BotInput{Name: "x", Config: BotConfig{Tasks: BotTaskDefaults{PermissionMode: "bypass"}}}},
		{"task harness with space", BotInput{Name: "x", Config: BotConfig{Tasks: BotTaskDefaults{Harness: "claude code"}}}},
		{"task model with control char", BotInput{Name: "x", Config: BotConfig{Tasks: BotTaskDefaults{Model: "m\x00"}}}},
		{"long task reasoning", BotInput{Name: "x", Config: BotConfig{Tasks: BotTaskDefaults{Reasoning: strings.Repeat("r", 201)}}}},
	}
	for _, c := range cases {
		if _, err := e.m.SaveBot(ctx, c.in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", c.name, err)
		}
	}
}

func TestBotTrustSettingsAccepted(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	tasks := BotTaskDefaults{Harness: TaskChoiceAuto, Model: "claude-opus-4.1", Reasoning: "high", PermissionMode: "auto"}
	for _, level := range []string{"", ToolAccessAsk, ToolAccessRead, ToolAccessWrite, ToolAccessAll} {
		b, err := e.m.SaveBot(ctx, BotInput{Name: "x" + level, Config: BotConfig{ToolAccess: level, Tasks: tasks}})
		if err != nil || b.Config.ToolAccess != level || b.Config.Tasks != tasks {
			t.Fatalf("level %q: %+v %v", level, b.Config, err)
		}
	}
}

func TestBotLimit(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	for i := range MaxBots {
		if _, err := e.m.SaveBot(ctx, BotInput{Name: fmt.Sprintf("bot %d", i)}); err != nil {
			t.Fatalf("bot %d: %v", i, err)
		}
	}
	if _, err := e.m.SaveBot(ctx, BotInput{Name: "one too many"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limit: %v", err)
	}
}

func TestImportExportIsPortable(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	src := e.bot("Src", func(in *BotInput) {
		in.Config = BotConfig{
			Tools:      []BotTool{{ID: ToolWebSearch, Params: map[string]any{"allowed_domains": []string{"go.dev"}}}},
			MCPServers: []string{"danger"},
			Skills:     []string{"deploy"},
			Executive:  true,
			Memory:     []ScopeAccess{{Scope: ScopeUser, Read: true}},
			Limits:     BotLimits{MaxSteps: 3},
			ToolAccess: ToolAccessAll,
			Tasks:      BotTaskDefaults{Harness: "codex", PermissionMode: "yolo"},
			Jarvis:     BotJarvis{Enabled: true, Policy: "only failures", VoiceID: "v1"},
		}
	})
	e.send(src.ID, "secret conversation")
	e.idle(src.ID)

	exp, err := e.m.ExportBot(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if exp.Version != 1 || exp.Bot.ID != "" || exp.Bot.Name != "Src" || len(exp.Bot.Config.Tools) != 1 || exp.Bot.Config.Limits.MaxSteps != 3 {
		t.Fatalf("export: %+v", exp)
	}
	if len(exp.Bot.Config.MCPServers)+len(exp.Bot.Config.Skills)+len(exp.Bot.Config.Memory) != 0 || exp.Bot.Config.Executive ||
		exp.Bot.Config.ToolAccess != "" || exp.Bot.Config.Tasks != (BotTaskDefaults{}) || exp.Bot.Config.Jarvis != (BotJarvis{}) {
		t.Fatalf("export leaks local grants: %+v", exp.Bot.Config)
	}

	// A hostile file cannot grant itself access.
	exp.Bot.ID = src.ID
	exp.Bot.Config.MCPServers = []string{"danger"}
	exp.Bot.Config.Executive = true
	exp.Bot.Config.ToolAccess = ToolAccessAll
	exp.Bot.Config.Tasks = BotTaskDefaults{Harness: "codex", PermissionMode: "yolo"}
	exp.Bot.Config.Jarvis = BotJarvis{Enabled: true, Policy: "call me always"}
	imp, err := e.m.ImportBot(ctx, exp)
	if err != nil {
		t.Fatal(err)
	}
	if imp.ID == src.ID || len(imp.Config.MCPServers) != 0 || imp.Config.Executive || imp.Config.ToolAccess != "" || imp.Config.Tasks != (BotTaskDefaults{}) || imp.Config.Jarvis != (BotJarvis{}) {
		t.Fatalf("import: %+v", imp)
	}
	if h := e.history(imp.ID); len(h) != 0 {
		t.Fatal("import must not carry messages")
	}
	if _, err := e.m.ImportBot(ctx, BotExport{Version: 2, Bot: exp.Bot}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("version: %v", err)
	}
}

func TestSeqIsMonotonicAndPaginationAscending(t *testing.T) {
	e := newEnv(t, func(int, llmRequest) []llmDelta { return []llmDelta{llmtest.Text("ok")} })
	ctx := context.Background()
	b := e.bot("Pager")
	for i := range 6 {
		e.send(b.ID, fmt.Sprintf("q%d", i))
		e.idle(b.ID)
	}
	all := e.history(b.ID)
	if len(all) != 12 {
		t.Fatalf("messages: %d", len(all))
	}
	for i, m := range all {
		if m.Seq != int64(i+1) {
			t.Fatalf("seq[%d]=%d", i, m.Seq)
		}
	}
	page, err := e.m.Messages(ctx, MessagesRequest{BotID: b.ID, Limit: 5})
	if err != nil || len(page.Messages) != 5 || !page.HasMore || page.Messages[0].Seq != 8 || page.Messages[4].Seq != 12 {
		t.Fatalf("latest page: %+v %v", page, err)
	}
	older, err := e.m.Messages(ctx, MessagesRequest{BotID: b.ID, BeforeSeq: 8, Limit: 5})
	if err != nil || len(older.Messages) != 5 || !older.HasMore || older.Messages[0].Seq != 3 || older.Messages[4].Seq != 7 {
		t.Fatalf("older page: %+v %v", older, err)
	}
	oldest, _ := e.m.Messages(ctx, MessagesRequest{BotID: b.ID, BeforeSeq: 3})
	if len(oldest.Messages) != 2 || oldest.HasMore {
		t.Fatalf("oldest page: %+v", oldest)
	}
	list, _ := e.m.Bots(ctx)
	if list[0].LastMessage == nil || list[0].LastMessage.Role != "assistant" || list[0].LastMessage.Text != "ok" {
		t.Fatalf("preview: %+v", list[0].LastMessage)
	}
}

func TestClearKeepsBotAndStaysMonotonic(t *testing.T) {
	e := newEnv(t, func(int, llmRequest) []llmDelta { return []llmDelta{llmtest.Text("ok")} })
	ctx := context.Background()
	b := e.bot("Clearable")
	e.send(b.ID, "hello")
	e.idle(b.ID)
	e.db.Exec(`INSERT INTO conversation_summaries(bot_id, upto_seq, text, updated_at) VALUES (?, 2, 's', 1)`, b.ID)
	e.db.Exec(`INSERT INTO memory_consolidation(bot_id, upto_seq, ran_at) VALUES (?, 2, 1)`, b.ID)
	if err := e.m.Clear(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if len(e.history(b.ID)) != 0 {
		t.Fatal("messages must be gone")
	}
	var n int
	e.db.QueryRow(`SELECT COUNT(*) FROM conversation_summaries WHERE bot_id=?`, b.ID).Scan(&n)
	if n != 0 {
		t.Fatal("summary must be gone")
	}
	if _, err := e.m.Bot(ctx, b.ID); err != nil {
		t.Fatal("bot must stay")
	}
	e.db.QueryRow(`SELECT COUNT(*) FROM memory_consolidation WHERE bot_id=?`, b.ID).Scan(&n)
	if n != 1 {
		t.Fatal("clear must not touch memory state")
	}
	e.send(b.ID, "again")
	e.idle(b.ID)
	if h := e.history(b.ID); h[0].Seq <= 2 {
		t.Fatalf("seq must stay above the consolidation watermark, got %d", h[0].Seq)
	}
}

func TestStartupMarksInterruptedRuns(t *testing.T) {
	e := newEnv(t, nil)
	b := e.bot("Crashy")
	for i, st := range []string{StatusRunning, StatusPending, StatusComplete} {
		_, err := e.db.Exec(`INSERT INTO messages(id, bot_id, seq, role, status, created_at) VALUES (?,?,?,?,?,1)`,
			fmt.Sprintf("m%d", i), b.ID, i+1, map[bool]string{true: "assistant", false: "user"}[st == StatusRunning], st)
		if err != nil {
			t.Fatal(err)
		}
	}
	m2, err := New(Config{DB: e.db.DB, DataDir: e.dir, LLM: e.llm, Clock: e.clock})
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	got := map[string]string{}
	finished := map[string]int64{}
	for _, m := range e.history(b.ID) {
		got[m.ID] = m.Status
		finished[m.ID] = m.FinishedAt
	}
	if got["m0"] != StatusIncomplete || got["m1"] != StatusCanceled || got["m2"] != StatusComplete {
		t.Fatalf("statuses after restart: %v", got)
	}
	// Interrupted messages finish at startup; one that finished before finish times were recorded keeps 0.
	now := e.clock().UnixMilli()
	if finished["m0"] != now || finished["m1"] != now || finished["m2"] != 0 {
		t.Fatalf("finish times after restart: %v", finished)
	}
}

// The computer switch is stored with the bot but is a local grant: exports drop it and imports cannot set it.
func TestComputerSwitchIsStoredButNeverExported(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	b := e.bot("Pc", func(in *BotInput) { in.Config.Computer = true })
	got, err := e.m.Bot(ctx, b.ID)
	if err != nil || !got.Config.Computer {
		t.Fatalf("stored: %+v %v", got.Config, err)
	}
	exp, err := e.m.ExportBot(ctx, b.ID)
	if err != nil || exp.Bot.Config.Computer {
		t.Fatalf("export must drop the computer switch: %+v %v", exp.Bot.Config, err)
	}
	exp.Bot.Config.Computer = true
	imp, err := e.m.ImportBot(ctx, exp)
	if err != nil || imp.Config.Computer {
		t.Fatalf("import must not enable the computer: %+v %v", imp.Config, err)
	}
}

func TestTurnBudgetDefaults(t *testing.T) {
	cases := []struct {
		name         string
		cfg          BotConfig
		steps, calls int
	}{
		{"plain", BotConfig{}, 8, 20},
		{"computer", BotConfig{Computer: true}, 30, 60},
		{"computer own limits", BotConfig{Computer: true, Limits: BotLimits{MaxSteps: 5, MaxToolCalls: 9}}, 5, 9},
		{"computer partial", BotConfig{Computer: true, Limits: BotLimits{MaxSteps: 12}}, 12, 60},
		{"plain own limits", BotConfig{Limits: BotLimits{MaxSteps: 5, MaxToolCalls: 9}}, 5, 9},
	}
	for _, c := range cases {
		s, k := turnBudget(c.cfg)
		if s != c.steps || k != c.calls {
			t.Errorf("%s: got %d/%d want %d/%d", c.name, s, k, c.steps, c.calls)
		}
	}
}
