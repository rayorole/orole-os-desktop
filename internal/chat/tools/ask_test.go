package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// Every bot whose model calls tools has ask_user: no flag, no setting, setup or not.
func TestAskUserIsOfferedToEveryBotWithToolCalling(t *testing.T) {
	bots := map[string]chat.Bot{
		"plain":          {ID: "b"},
		"setting up":     {ID: "b", Config: chat.BotConfig{Setup: true}},
		"with other cfg": {ID: "b", Config: chat.BotConfig{Computer: true, Executive: true, Tools: []chat.BotTool{{ID: chat.ToolWebSearch}}}},
	}
	for name, bot := range bots {
		if got := specNames((AskUser{}).Specs(bot, llm.Caps{Tools: true})); !slices.Equal(got, []string{AskUserName}) {
			t.Errorf("%s: specs = %v", name, got)
		}
		if got := (AskUser{}).Specs(bot, llm.Caps{}); got != nil {
			t.Errorf("%s: offered to a model that cannot call tools: %+v", name, got)
		}
	}
}

func TestAskUserEmitsQuestionPartOutsideSetup(t *testing.T) {
	tool := AskUser{}
	rc := chat.RunContext{BotID: "b", Bot: chat.Bot{ID: "b"}}
	res, err := tool.Call(context.Background(), rc, AskUserName, json.RawMessage(`{"question":" What for? ","options":[{"label":"Writing","description":" Prose "},{"label":" Coding "}]}`))
	if err != nil || res.IsError {
		t.Fatalf("unexpected failure: %v %+v", err, res)
	}
	if len(res.Parts) != 1 || res.Parts[0].Type != chat.PartQuestion || res.Parts[0].Text != "What for?" {
		t.Fatalf("parts = %+v", res.Parts)
	}
	if want := []chat.Choice{{Label: "Writing", Description: "Prose"}, {Label: "Coding"}}; !slices.Equal(res.Parts[0].Choices, want) {
		t.Fatalf("choices = %+v, want %+v", res.Parts[0].Choices, want)
	}
	if !strings.Contains(res.Text, "End your turn") {
		t.Fatalf("the model is not told to end its turn: %q", res.Text)
	}
}

func TestAskUserLimits(t *testing.T) {
	rc := chat.RunContext{BotID: "b", Bot: chat.Bot{ID: "b"}}
	opts := func(n int) string {
		var b []string
		for range n {
			b = append(b, `{"label":"a"}`)
		}
		return strings.Join(b, ",")
	}
	for name, args := range map[string]string{
		"empty question":     `{"question":"","options":[{"label":"a"},{"label":"b"}]}`,
		"long question":      `{"question":"` + strings.Repeat("q", maxQuestionRunes+1) + `","options":[` + opts(2) + `]}`,
		"one option":         `{"question":"q","options":[{"label":"only"}]}`,
		"seven options":      `{"question":"q","options":[` + opts(maxOptions+1) + `]}`,
		"empty label":        `{"question":"q","options":[{"label":"a"},{"label":""}]}`,
		"long label":         `{"question":"q","options":[{"label":"a"},{"label":"` + strings.Repeat("l", maxOptionRunes+1) + `"}]}`,
		"long description":   `{"question":"q","options":[{"label":"a"},{"label":"b","description":"` + strings.Repeat("d", maxDescriptionRunes+1) + `"}]}`,
		"not an object list": `{"question":"q","options":"a,b"}`,
	} {
		if res, _ := (AskUser{}).Call(context.Background(), rc, AskUserName, json.RawMessage(args)); !res.IsError || len(res.Parts) != 0 {
			t.Errorf("%s accepted: %+v", name, res)
		}
	}
	for _, n := range []int{minOptions, maxOptions} {
		args := `{"question":"` + strings.Repeat("q", maxQuestionRunes) + `","options":[` + opts(n) + `]}`
		if res, err := (AskUser{}).Call(context.Background(), rc, AskUserName, json.RawMessage(args)); err != nil || res.IsError || len(res.Parts) != 1 {
			t.Errorf("%d options refused: %v %+v", n, err, res)
		}
	}
}

// The section tells bots how to ask; the setup section builds on it instead of repeating it.
func TestAskUserPromptSaysWhenAndHowToAsk(t *testing.T) {
	for _, want := range []string{"ask_user", "ONE question", "2 to 6", "recommend", "first", "end your turn", "next message"} {
		if !strings.Contains(AskUserPrompt, want) {
			t.Errorf("AskUserPrompt lacks %q", want)
		}
	}
	setup := SetupPrompt(chat.Bot{ID: "me"}, nil)
	for _, dup := range []string{"ONE question", "2 to 6", "next message"} {
		if strings.Contains(setup, dup) {
			t.Errorf("setup prompt repeats the ask_user rules (%q)", dup)
		}
	}
}
