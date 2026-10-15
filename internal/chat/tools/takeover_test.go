package tools

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
)

func TestTakeoverSpecs(t *testing.T) {
	tool := Takeover{Runtime: &fakeComputer{}}
	if got := specNames(tool.Specs(pcBot(), llm.Caps{Tools: true})); !slices.Equal(got, []string{TakeoverName}) {
		t.Fatalf("specs: %v", got)
	}
	off := pcBot()
	off.Config.Computer = false
	if tool.Specs(off, llm.Caps{Tools: true}) != nil {
		t.Fatal("offered without the switch")
	}
}

func TestTakeoverWaitsAndReturnsTheUserNote(t *testing.T) {
	rt := &fakeComputer{ask: func(_ context.Context, reason string) (string, error) {
		if reason != "Log in to example.com" {
			return "", errors.New("wrong reason " + reason)
		}
		return "Logged in as ray\x1b[31m", nil
	}}
	var cards []chat.MsgPart
	res, err := Takeover{Runtime: rt}.Call(context.Background(), pcRun(&cards), TakeoverName, json.RawMessage(`{"reason":"Log in to example.com"}`))
	if err != nil || res.IsError || res.Untrusted || !strings.Contains(res.Text, "The user finished: Logged in as ray") || strings.Contains(res.Text, "\x1b") {
		t.Fatalf("result: %+v %v", res, err)
	}
	if len(cards) != 2 || cards[0].Status != chat.ComputerWaiting || cards[0].Summary != "Log in to example.com" || cards[1].Status != chat.ComputerDone {
		t.Fatalf("cards: %+v", cards)
	}
	rt.ask = func(context.Context, string) (string, error) { return "  ", nil }
	res, _ = Takeover{Runtime: rt}.Call(context.Background(), pcRun(nil), TakeoverName, json.RawMessage(`{"reason":"Solve the CAPTCHA"}`))
	if !strings.Contains(res.Text, "handed the computer back") {
		t.Fatalf("no note: %+v", res)
	}
}

func TestTakeoverNoteIsCapped(t *testing.T) {
	rt := &fakeComputer{ask: func(context.Context, string) (string, error) { return strings.Repeat("n", 5000), nil }}
	res, _ := Takeover{Runtime: rt}.Call(context.Background(), pcRun(nil), TakeoverName, json.RawMessage(`{"reason":"help"}`))
	if res.IsError || len(res.Text) > maxTakeoverNote+200 {
		t.Fatalf("note not capped: %d bytes", len(res.Text))
	}
}

func TestTakeoverFailuresAreExplained(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{sandbox.ErrHandoffTimeout, "did not take over within 5 minutes"},
		{sandbox.ErrHandoffHoldTimeout, "did not hand the computer back within 30 minutes"},
		{sandbox.ErrHandoffBusy, "already waiting"},
		{sandbox.ErrHandoffStopped, "Your computer stopped while you waited"},
		{ErrComputerGone, "no longer exist,"},
		{sandbox.ErrNotReady, "isn't set up yet"},
	} {
		rt := &fakeComputer{ask: func(context.Context, string) (string, error) { return "", tt.err }}
		var cards []chat.MsgPart
		res, err := Takeover{Runtime: rt}.Call(context.Background(), pcRun(&cards), TakeoverName, json.RawMessage(`{"reason":"help"}`))
		if err != nil || !res.IsError || !strings.Contains(res.Text, tt.want) {
			t.Errorf("%v: %+v %v", tt.err, res, err)
		}
		if len(cards) != 2 || cards[1].Status != chat.ComputerFailed {
			t.Errorf("%v: cards %+v", tt.err, cards)
		}
	}
}

func TestTakeoverEndsWithTheTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &fakeComputer{ask: func(ctx context.Context, _ string) (string, error) { cancel(); <-ctx.Done(); return "", ctx.Err() }}
	var cards []chat.MsgPart
	if _, err := (Takeover{Runtime: rt}).Call(ctx, pcRun(&cards), TakeoverName, json.RawMessage(`{"reason":"help"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("a cancelled turn must show no failure: %+v", cards)
	}
}

func TestTakeoverIsRefusedOnAPhoneCall(t *testing.T) {
	asked := false
	rt := &fakeComputer{ask: func(context.Context, string) (string, error) { asked = true; return "", nil }}
	res, err := Takeover{Runtime: rt}.Call(policy.WithoutApprovals(context.Background()), pcRun(nil), TakeoverName, json.RawMessage(`{"reason":"help"}`))
	if err != nil || !res.IsError || !strings.Contains(res.Text, "phone call") || asked {
		t.Fatalf("%+v %v asked=%v", res, err, asked)
	}
}

func TestTakeoverValidatesTheReason(t *testing.T) {
	for _, args := range []string{`{"reason":""}`, `{"reason":"` + strings.Repeat("x", 201) + `"}`, `{}`, `{"reason":"x","when":"now"}`,
		`{"reason":"a\nb"}`, `{"reason":"a\u001b[2Jb"}`, `{"reason":"a​b"}`, `{"reason":"a‮b"}`} {
		asked := false
		rt := &fakeComputer{ask: func(context.Context, string) (string, error) { asked = true; return "", nil }}
		res, _ := Takeover{Runtime: rt}.Call(context.Background(), pcRun(nil), TakeoverName, json.RawMessage(args))
		if !res.IsError || asked {
			t.Errorf("%s: %+v asked=%v", args, res, asked)
		}
	}
	rt := &fakeComputer{ask: func(context.Context, string) (string, error) { return "", nil }}
	if res, _ := (Takeover{Runtime: rt}).Call(context.Background(), pcRun(nil), TakeoverName, json.RawMessage(`{"reason":"`+strings.Repeat("x", 200)+`"}`)); res.IsError {
		t.Fatalf("200 characters must pass: %+v", res)
	}
}
