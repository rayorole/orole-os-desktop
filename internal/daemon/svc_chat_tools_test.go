package daemon

import (
	"slices"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// The daemon registers ask_user as a built-in tool: a plain bot gets it with no flag, a bot in setup gets it next
// to save_profile, and a model that cannot call tools gets nothing.
func TestDaemonOffersAskUserToEveryBot(t *testing.T) {
	d := jarvisDaemon(t)
	offered := func(bot chat.Bot, caps llm.Caps) []string {
		var names []string
		for _, tool := range d.Chat.Tools() {
			for _, sp := range tool.Specs(bot, caps) {
				if sp.Name == chat.ToolAskUser || sp.Name == "save_profile" {
					names = append(names, sp.Name)
				}
			}
		}
		return names
	}
	tools := llm.Caps{Tools: true}
	if got := offered(chat.Bot{ID: "plain"}, tools); !slices.Equal(got, []string{"ask_user"}) {
		t.Fatalf("plain bot: %v", got)
	}
	if got := offered(chat.Bot{ID: "new", Config: chat.BotConfig{Setup: true}}, tools); !slices.Equal(got, []string{"ask_user", "save_profile"}) {
		t.Fatalf("bot in setup: %v", got)
	}
	if got := offered(chat.Bot{ID: "plain"}, llm.Caps{}); got != nil {
		t.Fatalf("model without tool calling: %v", got)
	}
}
