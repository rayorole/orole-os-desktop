// Command fakeomp is a scripted stand-in for `omp` used by the agents tests. It speaks omp's
// `--mode rpc` JSONL protocol (ready frame, protocol v2 negotiation, prompt/steer queueing,
// extension_ui_request dialogs, rpc_chunk frames) closely enough to exercise the adapter.
//
// At startup in rpc mode it writes fakeomp-argv.json (its arguments) and fakeomp-seen.json
// (the contents of the -e package's .mcp.json and the --append-system-prompt file as they
// were at launch) into its working directory.
//
// Directives found in a prompt:
//
//	TOOL      think, run a bash tool, answer
//	MCP       call the orole MCP tool skill_list through the xd:// device
//	PERM      ask Approve/Deny for a bash tool and run it only when approved
//	CONFIRM   ask a confirm dialog and report the answer
//	ASK       ask a select dialog and report the native answer/cancellation
//	SLOW      keep the turn open ~500ms and report steered messages
//	HANG      never finish (only abort or EOF ends the process)
//	FAIL      end the turn with a model error
//	IMAGE     return a generated image in a tool result
//	CHUNK     deliver a 300 KiB assistant message as rpc_chunk frames
//	BADCHUNK  send an out-of-sequence rpc_chunk
//	LOCAL     answer with agentInvoked:false (a builtin slash command)
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var (
	out  = bufio.NewWriter(os.Stdout)
	omu  sync.Mutex
	args []string
)

func emit(v any) {
	b, _ := json.Marshal(v)
	omu.Lock()
	out.Write(b)
	out.WriteByte('\n')
	out.Flush()
	omu.Unlock()
}

// emitSequence writes frames back to back: omp's v2 transport promises an uninterrupted
// rpc_chunk sequence, so a response to a concurrent command (the adapter's get_state after
// a message_end) must never land between two chunks.
func emitSequence(vs []msg) {
	omu.Lock()
	defer omu.Unlock()
	for _, v := range vs {
		b, _ := json.Marshal(v)
		out.Write(b)
		out.WriteByte('\n')
	}
	out.Flush()
}

func arg(name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

type msg = map[string]any

func text(role, s string) msg {
	return msg{"role": role, "content": []msg{{"type": "text", "text": s}}}
}

func assistant(stop string, blocks ...msg) msg {
	return msg{"role": "assistant", "content": blocks, "stopReason": stop}
}

var mid int

func messageEnd(m msg) {
	mid++
	emit(msg{"type": "message_end", "messageId": fmt.Sprintf("msg-%d", mid), "message": m})
}

func main() {
	args = os.Args[1:]
	if len(args) > 0 && args[0] == "--version" {
		fmt.Println("omp/0.0.0-fake")
		return
	}
	if len(args) > 0 && args[0] == "--help" {
		fmt.Println("--thinking=<value>  Set thinking level: off, minimal, low, medium, high, xhigh, max, auto")
		return
	}
	if len(args) > 0 && args[0] == "models" {
		fmt.Println(`{"models":[
{"provider":"anthropic","kind":"chat","id":"claude-fable-5","selector":"anthropic/claude-fable-5","name":"Claude Fable 5","thinking":["low","medium","high"]},
{"provider":"openai","kind":"chat","id":"gpt-plain","selector":"openai/gpt-plain","name":"GPT Plain","thinking":null},
{"provider":"openai","kind":"image","id":"img","selector":"openai/img","name":"Image","thinking":null},
{"provider":"bad","kind":"chat","id":"x","selector":"bad model\ninjected","name":"Bad","thinking":null}]}`)
		return
	}
	mode := arg("--mode")
	if mode != "rpc" {
		fmt.Fprintln(os.Stderr, "fakeomp only implements --mode rpc")
		os.Exit(2)
	}
	recordLaunch()

	emit(msg{"type": "ready", "protocolVersion": 1, "supportedProtocolVersions": []int{1, 2}, "maxFrameBytes": 1 << 20, "maxReassembledFrameBytes": 64 << 20})

	sid := "omp-session-1"
	if r := arg("--resume"); r != "" {
		sid = r
	}
	var (
		mu      sync.Mutex
		running bool
		aborted bool
		steered []string
		uiReply = map[string]chan map[string]any{}
	)
	// ask emits a dialog request and waits for its extension_ui_response.
	ask := func(req msg) map[string]any {
		id, _ := req["id"].(string)
		mu.Lock()
		ch := make(chan map[string]any, 1)
		uiReply[id] = ch
		mu.Unlock()
		emit(req)
		return <-ch
	}

	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := in.ReadString('\n')
		if strings.TrimSpace(line) != "" {
			var c map[string]any
			if json.Unmarshal([]byte(line), &c) != nil {
				emit(msg{"type": "response", "command": "parse", "success": false, "error": "bad json"})
				continue
			}
			id, _ := c["id"].(string)
			typ, _ := c["type"].(string)
			switch typ {
			case "negotiate_protocol":
				emit(msg{"id": id, "type": "response", "command": typ, "success": true, "data": msg{"protocolVersion": 2}})
			case "set_event_filter":
				emit(msg{"id": id, "type": "response", "command": typ, "success": true, "data": msg{"events": nil, "messageUpdates": "delta"}})
			case "set_subagent_subscription":
				emit(msg{"id": id, "type": "response", "command": typ, "success": true, "data": msg{"level": c["level"]}})
			case "get_state":
				emit(msg{"id": id, "type": "response", "command": typ, "success": true, "data": msg{
					"sessionId": sid, "sessionFile": "/fake/" + sid + ".jsonl", "model": msg{"provider": "anthropic", "id": "claude-fable-5"}, "isStreaming": false}})
			case "abort":
				mu.Lock()
				aborted = true
				mu.Unlock()
				emit(msg{"id": id, "type": "response", "command": typ, "success": true})
			case "extension_ui_response":
				mu.Lock()
				ch := uiReply[id]
				delete(uiReply, id)
				mu.Unlock()
				if ch != nil {
					ch <- c
				}
			case "prompt":
				message, _ := c["message"].(string)
				mu.Lock()
				if running {
					steered = append(steered, message)
					mu.Unlock()
					emit(msg{"id": id, "type": "response", "command": typ, "success": true})
					break
				}
				running, aborted = true, false
				mu.Unlock()
				if strings.Contains(message, "LOCAL") {
					mu.Lock()
					running = false
					mu.Unlock()
					emit(msg{"id": id, "type": "response", "command": typ, "success": true, "data": msg{"agentInvoked": false}})
					break
				}
				emit(msg{"id": id, "type": "response", "command": typ, "success": true})
				go func() {
					run(message, id, &mu, &aborted, &steered, ask)
					mu.Lock()
					running = false
					mu.Unlock()
				}()
			default:
				emit(msg{"id": id, "type": "response", "command": typ, "success": false, "error": "unknown command " + typ})
			}
		}
		if err != nil {
			// stdin closed: omp drains and exits 0.
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}
	}
}

func recordLaunch() {
	b, _ := json.Marshal(args)
	_ = os.WriteFile("fakeomp-argv.json", b, 0o600)
	seen := map[string]string{}
	if ext := arg("-e"); ext != "" {
		if b, err := os.ReadFile(ext + string(os.PathSeparator) + ".mcp.json"); err == nil {
			seen["mcp"] = string(b)
		}
		if b, err := os.ReadFile(ext + string(os.PathSeparator) + "package.json"); err == nil {
			seen["package"] = string(b)
		}
	}
	if f := arg("--append-system-prompt"); f != "" {
		if b, err := os.ReadFile(f); err == nil {
			seen["prompt"] = string(b)
		}
	}
	b, _ = json.Marshal(seen)
	_ = os.WriteFile("fakeomp-seen.json", b, 0o600)
}

func run(message, promptID string, mu *sync.Mutex, aborted *bool, steered *[]string, ask func(msg) map[string]any) {
	emit(msg{"type": "agent_start"})
	emit(msg{"type": "turn_start"})
	mid++
	emit(msg{"type": "message_end", "messageId": fmt.Sprintf("msg-%d", mid), "message": text("user", message)})
	end := func() {
		emit(msg{"type": "agent_end", "messages": []msg{}, "isTerminal": true, "yielded": true})
		emit(msg{"type": "prompt_result", "id": promptID, "agentInvoked": true, "status": "completed", "sessionSettled": true})
		emit(msg{"type": "session_settled"})
	}
	finish := func(answer string) {
		mu.Lock()
		extra := append([]string(nil), *steered...)
		mu.Unlock()
		if len(extra) > 0 {
			answer += " | steered: " + strings.Join(extra, "; ")
		}
		messageEnd(assistant("stop", msg{"type": "text", "text": answer}))
		end()
	}
	thinking := msg{"type": "thinking", "thinking": "I should look at this first."}
	// Presentation frames omp interleaves; the adapter must ignore them.
	emit(msg{"type": "extension_ui_request", "id": "w1", "method": "setWidget", "widgetKey": "autoresearch"})
	emit(msg{"type": "message_update", "messageId": "msg-x", "message": msg{"role": "assistant"}, "assistantMessageEvent": msg{"type": "thinking_start", "contentIndex": 0}})
	emit(msg{"type": "message_update", "messageId": "msg-x", "message": msg{"role": "assistant"}, "assistantMessageEvent": msg{"type": "text_delta", "contentIndex": 0, "delta": "x"}})

	call := func(id, name string, args msg) {
		messageEnd(assistant("toolUse", thinking, msg{"type": "toolCall", "id": id, "name": name, "arguments": args}))
		emit(msg{"type": "tool_execution_start", "toolCallId": id, "toolName": name, "args": args, "intent": "doing " + name})
	}
	result := func(id, name, outText string, isErr bool, details msg) {
		res := msg{"content": []msg{{"type": "text", "text": outText}}}
		if details != nil {
			res["details"] = details
		}
		emit(msg{"type": "tool_execution_end", "toolCallId": id, "toolName": name, "result": res, "isError": isErr})
		mid++
		emit(msg{"type": "message_end", "messageId": fmt.Sprintf("msg-%d", mid), "message": msg{"role": "toolResult", "toolCallId": id, "toolName": name, "content": res["content"], "isError": isErr}})
		emit(msg{"type": "turn_end"})
		emit(msg{"type": "turn_start"})
	}

	switch {
	case strings.Contains(message, "MCP"):
		path := "xd://mcp__orole_skill_list"
		call("call-mcp", "write", msg{"path": path, "content": "{}"})
		result("call-mcp", "write", "skills: alpha, beta", false, msg{"xdev": msg{"tool": "mcp__orole_skill_list", "inner": msg{"serverName": "orole", "mcpToolName": "skill_list"}}})
		finish("MCP returned skills")
	case strings.Contains(message, "TOOL"):
		call("call-1", "bash", msg{"command": "echo hi", "pty": false})
		result("call-1", "bash", "hi\n", false, nil)
		finish("TOOL done")
	case strings.Contains(message, "PERM"):
		call("call-perm", "bash", msg{"command": "echo hi"})
		reply := ask(msg{"type": "extension_ui_request", "id": "ui-perm", "method": "select", "title": "Allow tool: bash\nCommand: echo hi", "options": []string{"Approve", "Deny"}})
		if v, _ := reply["value"].(string); v == "Approve" {
			result("call-perm", "bash", "ran: echo hi", false, nil)
			finish("approved")
		} else {
			result("call-perm", "bash", "Tool call denied by user", true, nil)
			finish("denied")
		}
	case strings.Contains(message, "CONFIRM"):
		reply := ask(msg{"type": "extension_ui_request", "id": "ui-conf", "method": "confirm", "title": "Delete branch?", "message": "feature/x will be removed"})
		if ok, _ := reply["confirmed"].(bool); ok {
			finish("confirmed")
		} else {
			finish("not confirmed")
		}
	case strings.Contains(message, "ASK"):
		reply := ask(msg{"type": "extension_ui_request", "id": "ui-ask", "method": "select", "title": "Which database?", "options": []string{"Postgres", "SQLite"}})
		if c, _ := reply["cancelled"].(bool); c {
			finish("question cancelled")
		} else {
			finish(fmt.Sprintf("question answered: %v", reply))
		}
	case strings.Contains(message, "SLOW"):
		for range 25 {
			time.Sleep(20 * time.Millisecond)
		}
		finish("SLOW done")
	case strings.Contains(message, "HANG"):
		for {
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			a := *aborted
			mu.Unlock()
			if a {
				messageEnd(assistant("aborted", msg{"type": "text", "text": ""}))
				end()
				return
			}
		}
	case strings.Contains(message, "FAIL"):
		m := assistant("error")
		m["errorMessage"] = "529 overloaded"
		messageEnd(m)
		end()
	case strings.Contains(message, "IMAGE"):
		png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==")
		call("call-img", "generate_image", msg{"subject": "a red dot"})
		result("call-img", "generate_image", "Generated 1 image", false, msg{"images": []msg{{"data": base64.StdEncoding.EncodeToString(png), "mimeType": "image/png"}}})
		finish("IMAGE done")
	case strings.Contains(message, "BADCHUNK"):
		emit(msg{"type": "rpc_chunk", "chunkId": "bad-1", "index": 1, "count": 3, "byteLength": 10, "data": "AAAA"})
		finish("BADCHUNK done")
	case strings.Contains(message, "CHUNK"):
		big := strings.Repeat("0123456789abcdef", 300<<10/16)
		m := assistant("stop", msg{"type": "text", "text": big})
		frame, _ := json.Marshal(msg{"type": "message_end", "messageId": "msg-big", "message": m})
		const size = 100 << 10
		count := (len(frame) + size - 1) / size
		chunks := make([]msg, 0, count)
		for i := range count {
			part := frame[i*size : min(len(frame), (i+1)*size)]
			chunks = append(chunks, msg{"type": "rpc_chunk", "chunkId": "rpc-1", "index": i, "count": count, "byteLength": len(frame), "data": base64.StdEncoding.EncodeToString(part)})
		}
		emitSequence(chunks)
		end()
	default:
		finish("done: " + message)
	}
}
