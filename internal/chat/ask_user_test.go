package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/llm/llmtest"
)

const askSection = "ASK-USER-SECTION"

// askEnv registers a stand-in for the ask_user tool and its prompt section the way the daemon does.
func askEnv(t *testing.T, script llmtest.Script) (*env, *fakeTool) {
	t.Helper()
	ask := &fakeTool{id: ToolAskUser, call: func(context.Context, RunContext, string, json.RawMessage) (ToolResult, error) {
		return ToolResult{Text: "shown", Parts: []MsgPart{{Type: PartQuestion, Text: "Which one?", Choices: []Choice{{Label: "A"}, {Label: "B"}}}}}, nil
	}}
	e := newEnv(t, script)
	e.m.AddTools(ask)
	e.m.AddToolSection(ToolAskUser, func(Bot) string { return askSection })
	return e, ask
}

func offersAsk(req llm.Request) bool {
	for _, sp := range req.Tools {
		if sp.Name == ToolAskUser {
			return true
		}
	}
	return false
}

func textOnly(int, llm.Request) []llm.Delta { return []llm.Delta{llmtest.Text("ok")} }

// A bot with no setup flag and no configuration has the tool and its guidance, can ask, and the turn ends on the card.
func TestAnyBotWithToolCallingCanAskAndTheTurnEndsOnTheCard(t *testing.T) {
	e, ask := askEnv(t, func(call int, req llm.Request) []llm.Delta { return llmtest.Call("q1", ToolAskUser, `{}`) })
	b := e.bot("Plain")
	e.send(b.ID, "help me pick")
	e.idle(b.ID)

	calls := e.fake.Calls()
	if len(calls) != 1 || !offersAsk(calls[0]) || !strings.Contains(calls[0].System, askSection) {
		t.Fatalf("a plain bot must be offered ask_user and its guidance: %d calls, tools %+v", len(calls), calls[0].Tools)
	}
	reply := e.lastReply(b.ID)
	if ask.callCount() != 1 || reply.Status != StatusComplete || len(partsOfType(reply.Parts, PartQuestion)) != 1 {
		t.Fatalf("asked %d times, reply: %+v", ask.callCount(), reply)
	}
}

func TestAskUserAndItsGuidanceNeedAToolCallingModel(t *testing.T) {
	e, _ := askEnv(t, textOnly)
	e.llm.models = []llm.Model{{ID: "m1", Name: "M1", Caps: llm.Caps{Tools: false}}}
	b := e.bot("Plain")
	e.send(b.ID, "hi")
	e.idle(b.ID)

	req := e.fake.Calls()[0]
	if offersAsk(req) || strings.Contains(req.System, askSection) {
		t.Fatalf("a model without tool calling got ask_user or its guidance: tools %+v", req.Tools)
	}
}

// A question card needs a chat message the user can answer. Previews, phone calls and delegated runs have none,
// so neither the tool nor the guidance reaches them.
func TestAskUserIsWithheldFromRunsNobodyCanAnswer(t *testing.T) {
	e, _ := askEnv(t, textOnly)
	ctx := context.Background()
	callee := e.bot("Callee")
	caller := e.bot("Caller", executive)

	if _, err := e.m.VoiceTurn(ctx, VoiceRequest{BotID: callee.ID, CallID: "c1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.DraftSend(ctx, DraftRequest{DraftID: "d1", Bot: BotInput{Name: "Draft", Instructions: "Be a draft."}, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	e.until("the preview run to end", func() bool { return e.m.runCount() == 0 })
	if err := e.m.Handoff(ctx, RunContext{BotID: caller.ID, Bot: caller, Chain: []string{"origin", caller.ID}}, callee.ID, "do the thing"); err != nil {
		t.Fatal(err)
	}
	e.idle(callee.ID)
	e.idle(caller.ID)

	kinds := map[string]bool{}
	for _, c := range e.fake.Calls() {
		kind := "other"
		switch {
		case strings.Contains(c.System, "on a phone call"):
			kind = "call"
		case strings.Contains(c.System, "Be a draft."):
			kind = "preview"
		case strings.Contains(c.System, "handed off"):
			kind = "delegated"
		}
		if kind == "other" {
			continue
		}
		kinds[kind] = true
		if offersAsk(c) || strings.Contains(c.System, askSection) {
			t.Errorf("%s run was offered ask_user or its guidance", kind)
		}
	}
	for _, want := range []string{"call", "preview", "delegated"} {
		if !kinds[want] {
			t.Errorf("no %s run was observed", want)
		}
	}
}

// The same bot is offered the tool again as soon as the user is the one who wrote the message.
func TestAskUserIsStillOfferedToTheCalleeInItsOwnChat(t *testing.T) {
	e, _ := askEnv(t, textOnly)
	b := e.bot("Callee")
	if _, err := e.m.VoiceTurn(context.Background(), VoiceRequest{BotID: b.ID, CallID: "c1", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	e.send(b.ID, "hi")
	e.idle(b.ID)
	calls := e.fake.Calls()
	if offersAsk(calls[0]) || !offersAsk(calls[1]) || !strings.Contains(calls[1].System, askSection) {
		t.Fatalf("call offered %v, chat offered %v", offersAsk(calls[0]), offersAsk(calls[1]))
	}
}

func TestToolSectionsKeepRegistrationOrderWithPlainSections(t *testing.T) {
	e, _ := askEnv(t, textOnly)
	e.m.AddPromptSection(func(Bot) string { return "LATER-SECTION" })
	e.m.AddToolSection("not_offered", func(Bot) string { return "NEVER-SECTION" })
	b := e.bot("Plain")
	e.send(b.ID, "hi")
	e.idle(b.ID)
	sys := e.fake.Calls()[0].System
	if first, second := strings.Index(sys, askSection), strings.Index(sys, "LATER-SECTION"); first < 0 || second < first || strings.Contains(sys, "NEVER-SECTION") {
		t.Fatalf("sections out of order or an unoffered tool's section present: %q", sys)
	}
}
