package tools

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// AskUserName is the question card tool every bot with a tool-calling model has.
const AskUserName = chat.ToolAskUser

const (
	maxQuestionRunes    = 300
	maxOptionRunes      = 80
	maxDescriptionRunes = 160
	minOptions          = 2
	maxOptions          = 6
)

// AskUserPrompt is the system-prompt section of ask_user. Register it with Manager.AddToolSection so it only
// reaches runs that are offered the tool.
const AskUserPrompt = `## Asking the user
When a decision or a missing detail needs the user and you cannot reasonably settle it yourself, call ask_user instead of guessing. It shows a question card with suggested answers; the user can pick one or type their own.
- Ask exactly ONE question at a time: a single ask_user call, never several, with 2 to 6 suggested answers. Each has a short label and, where it helps, a one-line description.
- Put the answer you recommend first and say so in its description (for example "Recommended: ...").
- The card shows the question itself, so do not repeat it in text. After calling ask_user, end your turn: the answer arrives as the next message.`

// AskUser lets a bot ask the user one multiple-choice question as a card in the chat. It is offered to every bot
// whose model can call tools; the engine withholds it from runs the user cannot answer (phone calls, delegated
// runs, builder previews). Code-task harnesses have their own question tool and never see this one.
type AskUser struct{}

func (AskUser) ID() string { return AskUserName }

func (AskUser) Specs(_ chat.Bot, caps llm.Caps) []llm.ToolSpec {
	if !caps.Tools {
		return nil
	}
	return []llm.ToolSpec{{
		Name:        AskUserName,
		Description: "Ask the user one question with suggested answers shown as buttons. The user can pick one or type their own answer; it arrives as their next message. Stop after calling this and wait for the reply.",
		Schema: json.RawMessage(`{"type":"object","properties":{` +
			`"question":{"type":"string","description":"The question, one short sentence"},` +
			`"options":{"type":"array","minItems":2,"maxItems":6,"items":{"type":"object","properties":{"label":{"type":"string","description":"Short answer"},"description":{"type":"string","description":"Optional one-line explanation"}},"required":["label"],"additionalProperties":false}}},` +
			`"required":["question","options"],"additionalProperties":false}`),
	}}
}

func (AskUser) Call(_ context.Context, _ chat.RunContext, name string, args json.RawMessage) (chat.ToolResult, error) {
	if name != AskUserName {
		return errResult("this tool is not available right now")
	}
	var a struct {
		Question string        `json:"question"`
		Options  []chat.Choice `json:"options"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return errResult("invalid arguments: question and options (objects with label and optional description) are required")
	}
	question := strings.TrimSpace(a.Question)
	if question == "" || utf8.RuneCountInString(question) > maxQuestionRunes {
		return errResult("question must be 1 to %d characters", maxQuestionRunes)
	}
	var choices []chat.Choice
	for _, o := range a.Options {
		c := chat.Choice{Label: strings.TrimSpace(o.Label), Description: strings.TrimSpace(o.Description)}
		if c.Label == "" || utf8.RuneCountInString(c.Label) > maxOptionRunes || utf8.RuneCountInString(c.Description) > maxDescriptionRunes {
			return errResult("each option needs a label of 1 to %d characters and a description of at most %d", maxOptionRunes, maxDescriptionRunes)
		}
		choices = append(choices, c)
	}
	if len(choices) < minOptions || len(choices) > maxOptions {
		return errResult("give %d to %d options", minOptions, maxOptions)
	}
	return chat.ToolResult{
		Text:  "The question and its options are now shown to the user. End your turn without writing anything else; their answer arrives as the next message.",
		Parts: []chat.MsgPart{{Type: chat.PartQuestion, Text: question, Choices: choices}},
	}, nil
}
