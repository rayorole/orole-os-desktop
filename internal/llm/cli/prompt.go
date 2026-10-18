// Package cli provides model providers that run a vendor's own CLI (Codex, Claude Code) with the user's account, so
// bots use that account's plan. This is the one place where Orole starts a harness for chat: the CLI signs in and keeps
// the credentials, Orole never reads them. The CLI is used as a plain model, not as an agent: it runs in an empty
// scratch directory with the strictest sandbox the CLI offers and is told to answer from the conversation alone.
// Orole's own tool loop stays in charge: tools are offered to the model in the prompt and a reply that asks for them is
// turned into tool-call deltas, so memory, limits, handoffs and structured components work like with any provider.
package cli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/llm"
)

// maxToolResultPrompt bounds one tool result in a rendered prompt; the engine already clips results.
const maxToolResultPrompt = 100 << 10

const preamble = `You are the language model behind a chat assistant, not a coding agent. Do not run commands, read or edit files or use any tool of your own. Answer only from the conversation below and follow the system instructions in it.`

const toolProtocol = `# Tools
You may call the tools listed below. To call one or more, reply with exactly one fenced block labelled tool_calls holding a JSON array, and nothing after it:

` + "```tool_calls" + `
[{"name": "tool_name", "arguments": {"arg": "value"}}]
` + "```" + `

You will receive the results and can then answer or call more tools. When no tool is needed, just write the answer. Only call tools from this list, with arguments that match their schema. Use this JSON block and no other call format: no XML or tag markup, and never describe a call in prose instead of making it.

`

// media is what a CLI can be handed besides text. A CLI that takes neither gets the placeholder line it always did.
type media struct{ images, pdfs bool }

// imageExt maps the image types the CLIs accept to a file extension.
var imageExt = map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp"}

// forwards reports whether the CLI receives p itself (so the prompt can say it is attached) rather than a placeholder.
func (m media) forwards(p llm.Part) bool {
	if len(p.Data) == 0 {
		return false
	}
	return p.Kind == llm.PartImage && m.images && imageExt[p.MIME] != "" || p.Kind == llm.PartFile && m.pdfs && p.MIME == "application/pdf"
}

// attachments returns the parts of req the CLI receives itself, in conversation order.
func (m media) attachments(req llm.Request) []llm.Part {
	var out []llm.Part
	for _, msg := range req.Messages {
		for _, p := range msg.Parts {
			if m.forwards(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// buildPrompt renders a request as one prompt for a CLI that takes text. File names are untrusted, so they are
// quoted: a name with a newline cannot start a fake turn or instruction.
func buildPrompt(req llm.Request, native media) string {
	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString("\n\n# System instructions\n")
	b.WriteString(strings.TrimSpace(req.System))
	b.WriteString("\n\n")
	var client []llm.ToolSpec
	for _, t := range req.Tools {
		if t.Hosted == "" && t.Name != "" {
			client = append(client, t)
		}
	}
	if len(client) > 0 {
		b.WriteString(toolProtocol)
		for _, t := range client {
			fmt.Fprintf(&b, "## %s\n%s\nSchema: %s\n\n", t.Name, strings.TrimSpace(t.Description), compact(t.Schema))
		}
	}
	b.WriteString("# Conversation\n")
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			switch p.Kind {
			case llm.PartText:
				fmt.Fprintf(&b, "[%s]\n%s\n\n", label(m.Role), strings.TrimSpace(p.Text))
			case llm.PartImage, llm.PartFile:
				noun := "an image"
				if p.Kind == llm.PartFile {
					noun = "a PDF"
				}
				if native.forwards(p) {
					fmt.Fprintf(&b, "[%s attached %s %q, passed to you alongside this prompt]\n\n", label(m.Role), noun, p.Name)
				} else {
					fmt.Fprintf(&b, "[%s attached a file the model cannot see: %q]\n\n", label(m.Role), p.Name)
				}
			case llm.PartToolCall:
				fmt.Fprintf(&b, "[assistant called the tool %s with %s]\n\n", p.ToolName, compact(p.Args))
			case llm.PartToolResult:
				text := p.Text
				if len(text) > maxToolResultPrompt {
					text = text[:maxToolResultPrompt] + "\n[truncated]"
				}
				kind := "result"
				if p.IsError {
					kind = "error"
				}
				fmt.Fprintf(&b, "[tool %s of %s]\n%s\n\n", kind, p.ToolName, text)
			}
		}
	}
	b.WriteString("Write the assistant's next message now.")
	return b.String()
}

func label(r llm.Role) string {
	if r == llm.RoleAssistant {
		return "assistant"
	}
	return "user"
}

func compact(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// toolOpener starts a tool_calls block. The block's end is found by reading its JSON, not by looking for the closing
// fence: a call's string arguments can hold fences and newlines, and a reply may stop before the closing fence.
var toolOpener = regexp.MustCompile("```tool_calls[ \t]*\r?\n")

// Claude models are trained on an XML call markup (an optional function_calls wrapper around invoke elements with
// parameter children, optionally namespaced) and fall back to it when tools are offered as plain text. It is
// accepted as a second call format so such a reply runs the tool instead of showing raw markup to the user.
var (
	xmlStart  = regexp.MustCompile(`<(?:[A-Za-z][\w-]*:)?(?:function_calls|invoke)\b`)
	xmlInvoke = regexp.MustCompile(`(?s)<(?:[A-Za-z][\w-]*:)?invoke\s+name="([^"]+)"\s*>(.*?)</(?:[A-Za-z][\w-]*:)?invoke>`)
	xmlParam  = regexp.MustCompile(`(?s)<(?:[A-Za-z][\w-]*:)?parameter\s+name="([^"]+)"\s*>(.*?)</(?:[A-Za-z][\w-]*:)?parameter>`)
	callStart = regexp.MustCompile("```tool_calls|" + xmlStart.String())
)

// maxMarker bounds how much of a streamed reply's tail can be the unfinished start of a call.
const maxMarker = 32

// splitReply separates a reply into the text before its tool calls and the calls it holds, read from a tool_calls
// block or, failing that, from XML call markup. A block that does not parse, or that names no offered tool, is
// ordinary text: a model's slip must not become an action.
func splitReply(reply string, tools []llm.ToolSpec) (text string, calls []llm.ToolCall) {
	offered := map[string]llm.ToolSpec{}
	for _, t := range tools {
		if t.Hosted == "" && t.Name != "" {
			offered[t.Name] = t
		}
	}
	for _, m := range toolOpener.FindAllStringIndex(reply, -1) {
		if calls = fenceCalls(reply[m[1]:], offered); len(calls) > 0 {
			return beforeCalls(reply[:m[0]]), calls
		}
	}
	if loc := xmlStart.FindStringIndex(reply); loc != nil {
		if calls = xmlCalls(reply[loc[0]:], offered); len(calls) > 0 {
			return beforeCalls(reply[:loc[0]]), calls
		}
	}
	return strings.TrimSpace(reply), nil
}

// fenceCalls reads the JSON value at the start of block (an array of calls, or one call object); what follows it,
// such as the closing fence, is ignored. Models writing long arguments often put raw newlines or tabs inside JSON
// strings; when strict parsing fails those are escaped and the value is read again. Anything else that does not
// parse is not a call.
func fenceCalls(block string, offered map[string]llm.ToolSpec) []llm.ToolCall {
	type call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	var raw []call
	read := func(s string) bool {
		var v json.RawMessage
		if json.NewDecoder(strings.NewReader(s)).Decode(&v) != nil {
			return false
		}
		if v = json.RawMessage(strings.TrimSpace(string(v))); len(v) > 0 && v[0] == '{' {
			var one call
			if json.Unmarshal(v, &one) != nil {
				return false
			}
			raw = []call{one}
			return true
		}
		return json.Unmarshal(v, &raw) == nil
	}
	if !read(block) && !read(escapeStringControls(block)) {
		return nil
	}
	var calls []llm.ToolCall
	for i, c := range raw {
		if _, ok := offered[c.Name]; !ok {
			continue
		}
		args := c.Arguments
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("call_%d", i+1), Name: c.Name, Args: args})
	}
	return calls
}

// escapeStringControls escapes control characters that appear inside JSON string literals (raw newlines, tabs, ...),
// leaving everything outside strings untouched.
func escapeStringControls(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)
	inString, escaped := false, false
	for i := range len(s) {
		c := s[i]
		switch {
		case !inString:
			inString = c == '"'
		case escaped:
			escaped = false
		case c == '\\':
			escaped = true
		case c == '"':
			inString = false
		case c < 0x20:
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			default:
				fmt.Fprintf(&b, `\u%04x`, c)
			}
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// xmlCalls reads invoke elements. Parameter values are raw text: a parameter the tool's schema types as a string is
// taken verbatim, any other value is used as JSON when it parses as JSON and as a string otherwise.
func xmlCalls(markup string, offered map[string]llm.ToolSpec) []llm.ToolCall {
	var calls []llm.ToolCall
	for i, m := range xmlInvoke.FindAllStringSubmatch(markup, -1) {
		spec, ok := offered[m[1]]
		if !ok {
			continue
		}
		strs := stringParams(spec.Schema)
		args := map[string]json.RawMessage{}
		for _, p := range xmlParam.FindAllStringSubmatch(m[2], -1) {
			v := strings.TrimSpace(p[2])
			if !strs[p[1]] && json.Valid([]byte(v)) {
				args[p[1]] = json.RawMessage(v)
			} else {
				args[p[1]], _ = json.Marshal(v)
			}
		}
		raw, err := json.Marshal(args)
		if err != nil {
			continue
		}
		calls = append(calls, llm.ToolCall{ID: fmt.Sprintf("call_%d", i+1), Name: m[1], Args: raw})
	}
	return calls
}

// stringParams names the top-level properties a JSON schema types as plain strings.
func stringParams(schema json.RawMessage) map[string]bool {
	var s struct {
		Properties map[string]struct {
			Type any `json:"type"`
		} `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	out := map[string]bool{}
	for name, p := range s.Properties {
		if p.Type == "string" {
			out[name] = true
		}
	}
	return out
}

// beforeCalls is the text shown before a reply's calls, without a code fence the model opened around them.
func beforeCalls(text string) string {
	return strings.TrimSpace(text[:openerStart(text)])
}

// openerStart returns where a trailing, still unclosed code fence opener (with the whitespace after it) begins in
// text, or len(text) when text does not end in one.
func openerStart(text string) int {
	t := strings.TrimRight(text, " \t\r\n")
	line := t[strings.LastIndexByte(t, '\n')+1:]
	if strings.HasPrefix(strings.TrimLeft(line, " \t"), "```") && strings.Count(t, "```")%2 == 1 {
		return len(t) - len(line)
	}
	return len(text)
}

// replyStream releases a reply's text as it arrives but holds back anything that could be the start of a tool call,
// so the user never sees a call's raw fence or markup flash by. Held text that turns out not to be a call is released
// by Finish.
type replyStream struct {
	full    strings.Builder
	emitted int // bytes of full already released
	held    bool
}

// Push adds streamed text and returns what is safe to show now.
func (r *replyStream) Push(s string) string {
	r.full.WriteString(s)
	if r.held {
		return ""
	}
	text := r.full.String()
	var safe int
	// Nothing released can hold a marker: the tail kept back is always long enough to contain one.
	if loc := callStart.FindStringIndex(text[r.emitted:]); loc != nil {
		r.held, safe = true, max(r.emitted, openerStart(text[:r.emitted+loc[0]]))
	} else {
		safe = max(r.emitted, len(text)-(maxMarker-1))
		for safe > r.emitted && !utf8.RuneStart(text[safe]) { // never cut a character in half
			safe--
		}
	}
	out := text[r.emitted:safe]
	r.emitted = safe
	return out
}

// Finish returns the text not yet released and the tool calls the complete reply asks for. When the reply holds
// calls, everything before them was released already.
func (r *replyStream) Finish(tools []llm.ToolSpec) (rest string, calls []llm.ToolCall) {
	full := r.full.String()
	if _, calls = splitReply(full, tools); len(calls) > 0 {
		return "", calls
	}
	return strings.TrimRight(full[r.emitted:], " \t\r\n"), nil
}
