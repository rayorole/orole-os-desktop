package chat

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/llm/llmtest"
)

func executive(in *BotInput) { in.Config.Executive = true }

// fromBot is the RunContext of a user-started run of bot b that is about to hand off: the chain
// holds the handing-off bot, as the ask_bot tool sets it.
func fromBot(b Bot) RunContext { return RunContext{BotID: b.ID, Bot: b, Chain: []string{b.ID}} }

// reports returns the messages of the bot's conversation in which another bot reported back.
func (e *env) reports(botID string) []Message {
	var out []Message
	for _, m := range e.history(botID) {
		if m.Role == "user" && strings.HasPrefix(m.Author, ReportPrefix) {
			out = append(out, m)
		}
	}
	return out
}

func TestHandoffIsARealTurnOnTheCalleeAndTheFindingsComeBack(t *testing.T) {
	e := newEnv(t, func(call int, req llm.Request) []llm.Delta {
		if strings.Contains(req.System, "Be Callee.") {
			return []llm.Delta{llmtest.Text("callee says 42")}
		}
		return []llm.Delta{llmtest.Text("caller relays it")}
	})
	callee := e.bot("Callee")
	caller := e.bot("Caller", executive)
	from := RunContext{BotID: caller.ID, Bot: caller, Chain: []string{"origin", caller.ID}}

	if err := e.m.Handoff(context.Background(), from, callee.ID, "what is the answer?"); err != nil {
		t.Fatal(err)
	}
	e.until("the report to be answered", func() bool {
		h := e.history(caller.ID)
		return len(h) == 2 && h[1].Status == StatusComplete
	})
	e.idle(callee.ID)
	e.idle(caller.ID)

	// The task is part of the callee's own conversation, answered as a normal assistant message.
	h := e.history(callee.ID)
	if len(h) != 2 || h[0].Author != "bot:"+caller.ID || messageText(h[0].Parts) != "what is the answer?" || h[1].Status != StatusComplete || replyText(h[1]) != "callee says 42" {
		t.Fatalf("callee conversation: %+v", h)
	}
	first := e.fake.Calls()[0]
	if !strings.Contains(first.System, `"Caller"`) || !strings.Contains(first.System, "handed off") {
		t.Fatalf("the callee must be told it carries out a handed-off task: %q", first.System)
	}

	// The findings come back to the caller as a report, and the caller answers it.
	ch := e.history(caller.ID)
	if len(ch) != 2 {
		t.Fatalf("caller conversation: %+v", ch)
	}
	r, a := ch[0], ch[1]
	if r.Author != ReportPrefix+callee.ID || messageText(r.Parts) != "callee says 42" || r.Status != StatusComplete {
		t.Fatalf("report: %+v", r)
	}
	if a.Role != "assistant" || a.Author != "bot:"+caller.ID || replyText(a) != "caller relays it" {
		t.Fatalf("the caller must tell the user what came back: %+v", a)
	}
	var seen llm.Request
	for _, c := range e.fake.Calls() {
		if !strings.Contains(c.System, "Be Callee.") {
			seen = c
		}
	}
	if !strings.Contains(seen.System, "reported back") {
		t.Fatalf("the report turn needs its instructions: %q", seen.System)
	}
	if text := seen.Messages[len(seen.Messages)-1].Parts[0].Text; !strings.Contains(text, "<untrusted") || !strings.Contains(text, "callee says 42") {
		t.Fatalf("the report must reach the model as fenced data: %q", text)
	}
}

func TestTheCallerStaysFreeWhileTheTaskRuns(t *testing.T) {
	release := make(chan struct{})
	e := newEnv(t, func(call int, req llm.Request) []llm.Delta {
		if strings.Contains(req.System, "Be Callee.") {
			<-release
			return []llm.Delta{llmtest.Text("findings")}
		}
		return []llm.Delta{llmtest.Text("caller here")}
	})
	callee := e.bot("Callee")
	caller := e.bot("Caller", executive)

	start := time.Now()
	if err := e.m.Handoff(context.Background(), fromBot(caller), callee.ID, "research the thing"); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("Handoff must return as soon as the task is queued")
	}

	// The user talks to the caller while the callee is still working; the caller knows what is out.
	e.send(caller.ID, "how is it going?")
	e.until("the caller to answer", func() bool {
		h := e.history(caller.ID)
		return len(h) == 2 && h[1].Status == StatusComplete
	})
	// The callee's worker starts on its own goroutine; its reply row appears once it does.
	e.until("the callee to start", func() bool {
		for _, m := range e.history(callee.ID) {
			if m.Role == "assistant" {
				return true
			}
		}
		return false
	})
	if r := e.lastReply(callee.ID); r.Status != StatusRunning {
		t.Fatalf("the callee should still be working: %+v", r)
	}
	var system string
	for _, c := range e.fake.Calls() {
		if !strings.Contains(c.System, "Be Callee.") {
			system = c.System
		}
	}
	if !strings.Contains(system, "have not reported back yet") || !strings.Contains(system, `"Callee"`) || !strings.Contains(system, "research the thing") {
		t.Fatalf("the caller must see its running tasks: %q", system)
	}

	close(release)
	// The report row is stored before the caller's run that answers it is registered, so idle alone could
	// return before that run starts: wait for the answer itself.
	e.until("the caller to answer the report", func() bool {
		h := e.history(caller.ID)
		return len(h) == 4 && h[3].Status == StatusComplete
	})
	e.idle(caller.ID)
	e.idle(callee.ID)
	if note := e.m.handoffsNote(caller.ID); note != "" {
		t.Fatalf("a finished task is still listed: %q", note)
	}
	// The user's own message was answered first, the report after it.
	h := e.history(caller.ID)
	if len(h) != 4 || h[0].Author != "user" || h[2].Author != ReportPrefix+callee.ID || h[3].Status != StatusComplete {
		t.Fatalf("caller conversation: %+v", h)
	}
}

func TestStoppingTheCalleeIsReportedToTheCaller(t *testing.T) {
	bp := &blockingProvider{started: make(chan struct{}, 2), release: make(chan struct{}), text: "working"}
	e := newEnv(t, nil)
	e.llm.prov = bp
	callee := e.bot("Callee")
	caller := e.bot("Caller", executive)
	if err := e.m.Handoff(context.Background(), fromBot(caller), callee.ID, "task"); err != nil {
		t.Fatal(err)
	}
	<-bp.started
	if err := e.m.Cancel(context.Background(), callee.ID); err != nil {
		t.Fatal(err)
	}
	e.until("the report to arrive", func() bool { return len(e.reports(caller.ID)) == 1 })
	if text := messageText(e.reports(caller.ID)[0].Parts); !strings.Contains(text, "was stopped") {
		t.Fatalf("report: %q", text)
	}
	close(bp.release)
	e.idle(caller.ID)
}

func TestHandoffRules(t *testing.T) {
	e := newEnv(t, func(int, llm.Request) []llm.Delta { return []llm.Delta{llmtest.Text("ok")} })
	callee := e.bot("Callee")
	stranger := e.bot("Stranger")
	caller := e.bot("Caller", executive)
	from := fromBot(caller)

	for _, tc := range []struct {
		name   string
		from   RunContext
		callee string
		task   string
	}{
		{"only executives hand off", fromBot(stranger), callee.ID, "hi"},
		{"depth limit", RunContext{BotID: caller.ID, Bot: caller, Depth: maxDelegationDepth, Chain: from.Chain}, callee.ID, "hi"},
		{"previews cannot delegate", RunContext{}, callee.ID, "hi"},
		{"not itself", from, caller.ID, "hi"},
		{"no cycles", RunContext{BotID: caller.ID, Bot: caller, Chain: []string{callee.ID, caller.ID}}, callee.ID, "hi"},
		{"the callee must exist", from, "ghost", "hi"},
		{"empty task", from, callee.ID, "  "},
		{"oversized task", from, callee.ID, strings.Repeat("x", MaxMessageTextLen+1)},
	} {
		if err := e.m.Handoff(context.Background(), tc.from, tc.callee, tc.task); err == nil {
			t.Errorf("%s: Handoff succeeded", tc.name)
		}
	}
	if n := len(e.history(callee.ID)) + len(e.history(stranger.ID)) + len(e.history(caller.ID)); n != 0 {
		t.Fatalf("refused handoffs left %d messages behind", n)
	}
	if err := e.m.Handoff(context.Background(), from, callee.ID, "hi"); err != nil {
		t.Fatalf("a valid handoff: %v", err)
	}
	e.until("the report to be answered", func() bool {
		h := e.history(caller.ID)
		return len(h) == 2 && h[1].Status == StatusComplete
	})
	if !slices.ContainsFunc(e.history(caller.ID), func(m Message) bool { return m.Author == ReportPrefix+callee.ID }) {
		t.Fatal("no report reached the caller")
	}
}
