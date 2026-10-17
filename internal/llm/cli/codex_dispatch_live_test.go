package cli

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// This checks the real model's interpretation of the text dispatch protocol. It requests
// an application action but never executes it, boots a VM, or changes a bot's chat.
func TestLiveCodexRequestsApplicationComputer(t *testing.T) {
	if os.Getenv("OROLE_LIVE_CODEX_DISPATCH") != "1" {
		t.Skip("set OROLE_LIVE_CODEX_DISPATCH=1, OROLE_LIVE_CODEX_PATH and OROLE_LIVE_CODEX_MODEL")
	}
	bin, model := os.Getenv("OROLE_LIVE_CODEX_PATH"), os.Getenv("OROLE_LIVE_CODEX_MODEL")
	if bin == "" || model == "" {
		t.Fatal("explicit CLI path and model are required for this live check")
	}
	for _, history := range []struct{ name, assistant string }{
		{"after-promise", "Ik start mijn beperkte werkomgeving nu op."},
		{"after-denial", "Ik kan hier geen computer_run uitvoeren; die tool is niet beschikbaar in mijn huidige omgeving."},
	} {
		t.Run(history.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			p := NewCodex(fakeRuntime{path: bin, ready: true}, nil)
			ch, err := p.Stream(ctx, llm.Request{
				Model:  model,
				System: "You have a private Linux computer that starts on the first computer_run action. Never claim a tool ran before receiving its result.",
				Tools:  []llm.ToolSpec{{Name: "computer_run", Description: "Run a bash command on your own Linux computer. Starts your computer automatically if it is off.", Schema: json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}`)}},
				Messages: []llm.Message{
					{Role: llm.RoleUser, Parts: []llm.Part{{Kind: llm.PartText, Text: "Start jouw computer op."}}},
					{Role: llm.RoleAssistant, Parts: []llm.Part{{Kind: llm.PartText, Text: history.assistant}}},
					{Role: llm.RoleUser, Parts: []llm.Part{{Kind: llm.PartText, Text: "Start nu jouw computer op en controleer of hij werkt."}}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			for d := range ch {
				if d.Kind == llm.DeltaError {
					t.Fatalf("provider error: %v", d.Err)
				}
				if d.Kind == llm.DeltaToolCall && d.Call != nil && d.Call.Name == "computer_run" {
					var args struct {
						Command string `json:"command"`
					}
					if err := json.Unmarshal(d.Call.Args, &args); err != nil || args.Command == "" {
						t.Fatalf("invalid dispatch: %s", d.Call.Args)
					}
					called = true
				}
			}
			if !called {
				t.Fatal("model did not dispatch computer_run")
			}
		})
	}
}
