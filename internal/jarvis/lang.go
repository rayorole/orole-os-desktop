package jarvis

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// phrases are the daemon's fixed spoken lines and model instructions for one call language. Auto uses the Dutch
// lines: a call starts in Dutch and the bot follows the owner from there. Every opener template has one %s for the
// bot's name and nothing else: an opener never says why the bot is calling (see FallbackOpener).
type phrases struct {
	openers []string // name-only greetings for an outbound call
	inbound string   // greeting when the owner phoned, %s = bot name
	verify  string   // %[1]s = the spaced digits, said twice
	cannot  string   // a turn that cannot run (bot missing, transient error)
	refused string   // prefix of the gate refusal; the gate's reason follows
	failed  string   // prefix of a model or tool error; its first sentence follows
	// tools are the lines said the moment a tool of a kind starts (see toolKinds); mcp says an MCP tool started, with
	// the integration's name for %s, and mcpAny when there is no name to say (see phrases.toolLine).
	tools       map[string]string
	mcp, mcpAny string
	// longWait is said once when a turn has had no words for Calls.ackAfter and said no tool line (see speaker.tick).
	longWait string
	still    []string // said now and then during a long silence while a tool runs
	bye      string   // goodbye when end_call ended the call without the bot saying one
	// tooLong ends a turn that ran past Calls.turnDeadline and offers to run it in the background; stillThere answers
	// the third silence prompt in a row and byeSilent the ninth, before the call is hung up (see Calls.plan).
	tooLong, stillThere, byeSilent string
	writeIn                        string // appended to the opener and inbound greeting instructions
}

var english = phrases{
	openers: []string{
		"Hey, it's %s. Got a minute?",
		"Hi, %s here. Do you have a moment?",
		"%s here. Got a minute?",
		"Hey, %s calling. Is now a good time?",
		"It's %s. Have you got a second?",
		"Hey, %s here. Quick one, got a minute?",
	},
	inbound: "Hey, %s here. What do you need?",
	verify:  "Your verification code is %[1]s. Again, %[1]s.",
	cannot:  "I can't do that right now.",
	refused: "I can't act on that: ",
	failed:  " That failed: ",
	tools: map[string]string{
		toolWeb:         "I'm searching the web for that now.",
		toolPage:        "I'm opening that page now.",
		toolResearch:    "I'm starting the research, that can take a moment.",
		toolTaskStart:   "I'm starting that coding task now.",
		toolTaskMessage: "I'm passing that on to the task.",
		toolTaskStop:    "I'm stopping that task.",
		toolAskBot:      "I'm passing it on.",
		toolSwitchBot:   "I'll put you through.",
		toolAutomations: "I'm checking your automations.",
		toolAutomation:  "I'm looking at that automation and its runs.",
	},
	mcp:        "I'm using your %s tools now. Give me a second to get the results.",
	mcpAny:     "I'm using one of your integrations now. Give me a second to get the results.",
	longWait:   "Bear with me, I'm still working on it.",
	still:      []string{"Still on it.", "Almost there, bear with me.", "Still working on it."},
	bye:        "Okay, talk later!",
	tooLong:    "This is taking too long on the phone. Shall I run it as a background task instead?",
	stillThere: "Are you still there?",
	byeSilent:  "I can't hear you anymore, so I'll hang up. Call me back anytime.",
	writeIn:    " Write it in English.",
}

var dutch = phrases{
	openers: []string{
		"Hoi, met %s. Heb je even?",
		"Hallo, %s hier. Heb je een momentje?",
		"%s hier. Heb je even tijd?",
		"Hoi, je spreekt met %s. Komt het nu uit?",
		"Met %s. Heb je een minuutje?",
		"Hé, %s hier. Even kort, heb je een minuut?",
	},
	inbound: "Hoi, met %s. Wat kan ik voor je doen?",
	verify:  "Je verificatiecode is %[1]s. Nog een keer: %[1]s.",
	cannot:  "Dat lukt me nu even niet.",
	refused: "Dat kan ik niet doen: ",
	failed:  " Dat is mislukt: ",
	tools: map[string]string{
		toolWeb:         "Ik zoek het even op internet op.",
		toolPage:        "Ik open die pagina even.",
		toolResearch:    "Ik start het onderzoek, dat kan even duren.",
		toolTaskStart:   "Ik start die codetaak nu.",
		toolTaskMessage: "Ik geef het door aan de taak.",
		toolTaskStop:    "Ik stop die taak.",
		toolAskBot:      "Ik geef het door.",
		toolSwitchBot:   "Ik verbind je door.",
		toolAutomations: "Ik kijk even naar je automatiseringen.",
		toolAutomation:  "Ik bekijk die automatisering en de runs.",
	},
	mcp:        "Ik gebruik nu je %s koppeling. Even geduld, dan heb ik de resultaten.",
	mcpAny:     "Ik gebruik nu een van je koppelingen. Even geduld, dan heb ik de resultaten.",
	longWait:   "Even geduld, ik ben er nog mee bezig.",
	still:      []string{"Nog even geduld.", "Ik ben er nog mee bezig.", "Bijna klaar."},
	bye:        "Oké, tot later!",
	tooLong:    "Dit duurt te lang voor aan de telefoon. Zal ik het als taak op de achtergrond laten doen?",
	stillThere: "Ben je er nog?",
	byeSilent:  "Ik hoor je niet meer, dus ik hang op. Bel me gerust terug.",
	writeIn:    " Write it in Dutch (Nederlands), as a native speaker would say it on the phone.",
}

// phrasesFor returns the lines of a call language (see PhoneSettings.CallLanguage); anything but English is Dutch.
func phrasesFor(lang string) phrases {
	if lang == LangEnglish {
		return english
	}
	return dutch
}

// Tool kinds: what the voice says the moment a tool starts on a call (see toolKinds). toolSilent says nothing.
const (
	toolSilent      = ""
	toolWeb         = "web"
	toolPage        = "page"
	toolResearch    = "research"
	toolTaskStart   = "task_start"
	toolTaskMessage = "task_message"
	toolTaskStop    = "task_stop"
	toolAskBot      = "ask_bot"
	toolSwitchBot   = "switch_bot"
	toolAutomations = "automations"
	toolAutomation  = "automation"
)

// toolKinds sorts every tool a bot can run on a call (the Specs of internal/chat/tools and internal/chat/memory) by
// what the voice says the moment it starts. A tool that leaves the PC or hands work on says what it is doing; a fast
// tool that only reads or writes local state says nothing, and if the turn then goes quiet the long-wait line covers it
// (see speaker.tick). MCP tools (mcp__<server id>__<tool>) are told by their prefix and say the integration's name
// (see phrases.toolLine); any other tool that is not listed is silent.
//
// Hosted tools (the provider's web_search, and web_fetch when the provider fetches pages itself) run at the provider
// and never reach VoiceRequest.OnTool; their entries count only for a provider that runs them as client tools.
var toolKinds = map[string]string{
	// The web and research: slow, and outside the PC.
	"web_search": toolWeb,
	"web_fetch":  toolPage,
	"fetch_url":  toolPage, // the daemon-side page fetch when the provider cannot fetch pages itself
	"research":   toolResearch,
	// Work handed on: a code task, another bot, another bot on the line.
	"start_task":   toolTaskStart,
	"message_task": toolTaskMessage,
	"stop_task":    toolTaskStop,
	"ask_bot":      toolAskBot,
	"switch_bot":   toolSwitchBot,
	// Automations the owner asks about.
	"list_automations":  toolAutomations,
	"automation_detail": toolAutomation, // one automation's definition and its recent runs
	// Fast and local: silent.
	"memory": toolSilent, "memory_search": toolSilent, "search_history": toolSilent, "knowledge_search": toolSilent,
	"knowledge_write": toolSilent, "propose_skill": toolSilent, "automation_suggestion": toolSilent,
	"orole_get_skill": toolSilent, "automation_reference": toolSilent, "propose_automation": toolSilent,
	"list_tasks": toolSilent, "task_status": toolSilent, "datetime": toolSilent,
	"jarvis_rule_add": toolSilent, "jarvis_rule_list": toolSilent, "jarvis_rule_delete": toolSilent,
	"call_owner": toolSilent, "end_call": toolSilent,
	"save_profile": toolSilent, "update_instructions": toolSilent,
	"show_table": toolSilent, "show_chart": toolSilent, "show_diagram": toolSilent, "show_flow_graph": toolSilent,
	"show_math": toolSilent, "show_spec_sheet": toolSilent, "show_comparison": toolSilent, "show_timeline": toolSilent,
	"show_job_progress": toolSilent, "show_todo_list": toolSilent, "show_document": toolSilent,
}

// mcpPrefix starts the function name of every MCP tool: mcp__<server id>__<tool> (see internal/chat/tools/mcp.go).
const mcpPrefix = "mcp__"

// toolLine is what the voice says the moment tool starts: the line of its kind (see toolKinds), the MCP line for an
// MCP tool, or "" for a tool that runs silently. label is the tool's label from VoiceRequest.OnTool: for an MCP tool
// the name the owner gave its server, which the MCP line says (see integrationName); without one it says the line
// without a name.
func (p phrases) toolLine(tool, label string) string {
	if strings.HasPrefix(tool, mcpPrefix) {
		if name := integrationName(label); name != "" {
			return fmt.Sprintf(p.mcp, name)
		}
		return p.mcpAny
	}
	return p.tools[toolKinds[tool]]
}

// maxIntegrationName caps the integration name an MCP line says.
const maxIntegrationName = 40

// integrationName is an MCP server's name as the voice can say it: one line of words, with a leading "Orole" or
// "Orole OS" dropped ("Orole Mail" -> "Mail"). It is "" when nothing is left, when it is longer than
// maxIntegrationName or has no letters, and when it still names the app (the sanitizer would turn that into "the app").
func integrationName(label string) string {
	name := strings.Join(strings.Fields(label), " ")
	for _, prefix := range []string{"orole os ", "orole "} {
		if len(name) > len(prefix) && strings.EqualFold(name[:len(prefix)], prefix) {
			name = name[len(prefix):]
			break
		}
	}
	if name == "" || utf8.RuneCountInString(name) > maxIntegrationName || !strings.ContainsFunc(name, unicode.IsLetter) ||
		appNameRE.MatchString(name) {
		return ""
	}
	return name
}

// stillLine is the nth "still working" line (n from 1).
func (p phrases) stillLine(n int) string { return p.still[(n-1)%len(p.still)] }

// verifyLine reads a verification code twice, digit by digit.
func (p phrases) verifyLine(digits string) string { return fmt.Sprintf(p.verify, spaced(digits)) }

// inboundLine is the fallback greeting when the owner phoned.
func (p phrases) inboundLine(name string) string {
	return fmt.Sprintf(p.inbound, runesCut(normalize(name), maxName))
}

// inboundInstruction asks the model for the greeting of a call the owner placed.
func inboundInstruction(lang, name string) string {
	return fmt.Sprintf("The owner just phoned you. Write your greeting: say your name (%s) naturally and ask what they need. "+
		"One short sentence, never name the app.", name) + phrasesFor(lang).writeIn
}

// fallbackOpener picks a name-only greeting in the call language; see FallbackOpener.
func fallbackOpener(lang, name string, seed int) string {
	list := phrasesFor(lang).openers
	name = runesCut(normalize(name), maxName)
	f := list[((seed%len(list))+len(list))%len(list)]
	return strings.TrimSpace(fmt.Sprintf(f, name))
}
