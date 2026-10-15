package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/llm"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

const (
	maxTakeoverReason = 200     // runes; the reason is shown to the user as written, on the card and in the notification
	maxTakeoverNote   = 4 * 500 // bytes of the user's note returned to the bot: sandbox.release takes at most 500 characters
)

// Takeover lets the bot ask the user to take over its computer (log in, solve a CAPTCHA, enter payment details):
// the turn waits until the user hands the computer back or the wait times out. It only asks; the takeover itself
// happens when the user presses Take over in the Computer tab, so nothing untrusted can start one.
type Takeover struct{ Runtime ComputerRuntime }

func (Takeover) ID() string { return "computer_takeover" }

func (t Takeover) Specs(bot chat.Bot, caps llm.Caps) []llm.ToolSpec {
	if !computerOn(bot, caps, t.Runtime) {
		return nil
	}
	return []llm.ToolSpec{{
		Name: TakeoverName,
		Description: "Ask the user to take over your computer's mouse and keyboard, for example to log in, solve a CAPTCHA, enter payment details or confirm something only they may do. You must never do those yourself. " +
			"Your turn waits: up to " + minutes(sandbox.DefaultHandoffAsk) + " for the user to start, then until they press Done (at most " + minutes(sandbox.DefaultHandoffHold) + "). You then get their note. Say exactly what they should do, in one or two short sentences.",
		Schema: json.RawMessage(`{"type":"object","properties":{"reason":{"type":"string","maxLength":200,"description":"What the user should do, shown to them as written"}},"required":["reason"],"additionalProperties":false}`),
	}}
}

func (t Takeover) Call(ctx context.Context, rc chat.RunContext, name string, args json.RawMessage) (chat.ToolResult, error) {
	if name != TakeoverName || !computerCallable(rc, t.Runtime) {
		return errResult("your computer is not enabled")
	}
	if policy.ApprovalsUnavailable(ctx) {
		return errResult("The user cannot take over your computer during a phone call. Ask them to do it later in the Orole app.")
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decode(args, &in); err != nil {
		return errResult("invalid arguments: %s", err)
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || utf8.RuneCountInString(in.Reason) > maxTakeoverReason {
		return errResult("reason is required and at most %d characters", maxTakeoverReason)
	}
	if !shownAsIs(in.Reason) || strings.ContainsFunc(in.Reason, unicode.IsControl) {
		return errResult("reason must be plain text without control or invisible characters (including newlines), so the user sees exactly what you wrote")
	}
	emitComputer(rc, TakeoverName, chat.ComputerWaiting, "Waiting for you to take over", in.Reason)
	note, err := t.Runtime.AskTakeover(ctx, rc.BotID, in.Reason)
	if err != nil {
		if ctx.Err() != nil {
			return chat.ToolResult{}, ctx.Err()
		}
		var msg, short string
		switch {
		case errors.Is(err, sandbox.ErrHandoffTimeout):
			msg, short = "The user did not take over within "+minutes(sandbox.DefaultHandoffAsk)+". Tell them what you need; they can ask you to try again.", "Nobody took over"
		case errors.Is(err, sandbox.ErrHandoffHoldTimeout):
			msg, short = "The user took over but did not hand the computer back within "+minutes(sandbox.DefaultHandoffHold)+". Stop and tell them what you were waiting for.", "The computer was not handed back"
		case errors.Is(err, sandbox.ErrHandoffStopped):
			msg, short = "Your computer stopped while you waited for the user, so the takeover ended. Tell the user; your next computer call starts it again.", "The computer stopped"
		case errors.Is(err, sandbox.ErrHandoffBusy):
			msg, short = "You are already waiting for the user to take over.", "Already waiting for you"
		}
		if msg == "" {
			return failed(ctx, rc, TakeoverName, "Asked you to take over", err)
		}
		emitComputer(rc, TakeoverName, chat.ComputerFailed, "Asked you to take over", short)
		return errResult("%s", msg)
	}
	emitComputer(rc, TakeoverName, chat.ComputerDone, "You handed the computer back", "")
	msg := "The user finished and handed the computer back."
	// The note is typed by the user, so it is trusted; control and invisible characters are still stripped.
	if note = strings.TrimSpace(untrusted.Text(note, maxTakeoverNote)); note != "" {
		msg = "The user finished: " + note
	}
	return chat.ToolResult{Text: msg + "\nCheck the page (browser_read) before you continue."}, nil
}

// minutes formats a handoff timeout for the model ("5 minutes").
func minutes(d time.Duration) string {
	if n := int(d / time.Minute); n != 1 {
		return fmt.Sprintf("%d minutes", n)
	}
	return "1 minute"
}

var _ chat.Tool = Takeover{}
