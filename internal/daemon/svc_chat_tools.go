package daemon

import (
	"context"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/chat/tools"
)

// registerChatTools runs after registerChat and registerChatMemory: it adds the web, research, MCP and
// skills tools to the chat Manager and answers chat.tools.list. knowledge_search is listed only when
// the memory subsystem registered it, which is why this registrar comes last.
func (d *Daemon) registerChatTools() {
	m := d.Chat
	if m == nil {
		return
	}

	fetcher := &tools.Fetcher{}
	if d.Policy != nil {
		fetcher.Policy = d.Policy
	}
	research := &tools.Research{LLM: d.LLM, Fetcher: fetcher}
	list := []chat.Tool{
		tools.WebSearch{},
		tools.WebFetch{Fetcher: fetcher},
		tools.Datetime{},
		research,
	}
	if d.MCP != nil {
		list = append(list, &tools.MCPTools{Client: d.MCP})
		m.SetIntegrationNames(func(ctx context.Context) map[string]string {
			servers, err := d.MCP.List(ctx)
			if err != nil {
				return nil
			}
			names := make(map[string]string, len(servers))
			for _, s := range servers {
				names[s.ID] = s.Name
			}
			return names
		})
	}
	if d.Skills != nil {
		list = append(list, &tools.SkillsTool{Store: d.Skills})
		m.AddPromptSection(func(b chat.Bot) string { return tools.SkillsPrompt(d.Skills, b) })
		m.SetBuiltinSkills(func() []string { return tools.BuiltinSkillNames(d.Skills) })
	}
	if d.Agents != nil {
		rt := newBotTasks(d)
		list = append(list, tools.Tasks{Runtime: rt})
		m.AddPromptSection(func(b chat.Bot) string { return tools.TasksPrompt(rt, b) })
	}
	if d.Automations != nil {
		list = append(list, tools.Automations{Runtime: d.Automations})
	}
	list = append(list, tools.Jarvis{Runtime: jarvisTools{d}}) // reads d.Jarvis lazily: registerJarvis runs after this
	m.AddPromptSection(tools.JarvisPrompt)
	// The bot computer: computerRuntime reads d.Sandbox per call (nil answers "not set up"); m refuses archived bots.
	computer := computerRuntime{d: d, bots: m}
	if d.Policy != nil {
		list = append(list, tools.Computer{Runtime: computer, Policy: d.Policy}, tools.Browser{Runtime: computer, Policy: d.Policy})
	}
	list = append(list, tools.Takeover{Runtime: computer})
	m.AddPromptSection(tools.ComputerPrompt)
	list = append(list, tools.Show{})
	m.AddPromptSection(func(chat.Bot) string { return tools.ShowPrompt })
	// ask_user: every bot with a tool-calling model has it (the engine withholds it from runs nobody can answer).
	// Its section comes before the setup one, which builds on it.
	list = append(list, tools.AskUser{})
	m.AddToolSection(chat.ToolAskUser, func(chat.Bot) string { return tools.AskUserPrompt })
	list = append(list, &tools.Setup{Bots: m})
	m.AddPromptSection(func(b chat.Bot) string {
		if !b.Config.Setup {
			return tools.SelfEditPrompt
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		others, _ := m.Bots(ctx) // a failed list only means no suggestions are tailored
		return tools.SetupPrompt(b, others)
	})
	m.AddTools(list...)

	info := tools.InfoSource{
		LLM:       d.LLM,
		ModelCaps: m.ModelCaps,
		Fetcher:   fetcher,
		Native:    research.Native,
	}
	handle(d, "chat.tools.list", func(ctx context.Context, p chat.ToolsRequest) ([]chat.ToolInfo, error) {
		out, err := info.Describe(ctx, p)
		return out, chatError(err)
	})
}
