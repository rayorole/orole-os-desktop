// Package agents runs coding agents (Claude Code, Codex, OpenCode, Gemini CLI, custom CLIs)
// in daemon-owned sessions, locally or on a paired SSH machine.
//
// Local processes live in per-session Windows job objects and die with the daemon.
// Remote processes use pinned, revocable SSH transports; losing that transport does
// not prove the remote process or descendants stopped. Recovery marks either kind
// lost. Only local adapters with a structured resume contract resume CLI sessions.
package agents

import (
	"encoding/json"
	"time"
)

// Status is where a session sits in the dashboard.
type Status string

const (
	StatusIdle     Status = "idle"      // alive (or created) but no task in flight
	StatusWorking  Status = "working"   // a turn is running
	StatusNeedsYou Status = "needs-you" // waiting on a permission decision or user input
	StatusDone     Status = "done"      // last turn finished successfully
	StatusFailed   Status = "failed"    // last turn or the process failed
	StatusStopped  Status = "stopped"   // stopped by the user
	StatusLost     Status = "lost"      // process vanished with a previous daemon run
)

// Terminal reports whether no further events can arrive without a (re)start.
func (s Status) Terminal() bool {
	return s == StatusFailed || s == StatusStopped || s == StatusLost
}

// Mode is how an agent is run.
type Mode string

const (
	// ModeStructured runs the CLI headless over pipes speaking its structured protocol
	// (Claude Code stream-json). Status and permission requests come from that protocol.
	ModeStructured Mode = "structured"
	// ModeTerminal runs the CLI interactively in a ConPTY terminal the UI can attach to.
	// Status comes from CLI hooks where the CLI has them (Claude Code), never from parsing
	// terminal output; CLIs without hooks only report running/exited.
	ModeTerminal Mode = "terminal"
	// ModeProcess is a remote one-shot CLI with text output and exit status only.
	ModeProcess Mode = "process"
)

// Review decisions recorded on a session.
const (
	ReviewNone     = ""
	ReviewApproved = "approved"
	ReviewSentBack = "sent-back"
	ReviewDiscard  = "discarded"
)

// Check states.
const (
	CheckNone    = ""
	CheckRunning = "running"
	CheckPassing = "passing"
	CheckFailing = "failing"
)

// PermissionRequest is a tool-use approval the agent is blocked on.
type PermissionRequest struct {
	ID      string          `json:"id"`
	Tool    string          `json:"tool"`
	Summary string          `json:"summary"`
	Input   json.RawMessage `json:"input"`
	// InputJSON preserves the exact JSON representation for approval disclosure.
	InputJSON string    `json:"inputJSON"`
	At        time.Time `json:"at"`
}

// Session is one agent run, persisted in agent_sessions.
type Session struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`                // claude | codex | opencode | gemini | custom
	Machine   string `json:"machine"`             // "local" or a paired machine id
	Project   string `json:"project"`             // repository the working directory belongs to ("" if none)
	ProjectID string `json:"projectId,omitempty"` // stable registry association; Project remains the execution root
	Dir       string `json:"dir"`                 // working directory (a worktree when one was created)
	Branch    string `json:"branch"`
	BaseRef   string `json:"baseRef"` // commit the work is reviewed against
	// WorktreeOwned is set when the daemon created Dir as a worktree for this session.
	WorktreeOwned bool   `json:"worktreeOwned"`
	Model         string `json:"model"`
	Reasoning     string `json:"reasoning,omitempty"`
	Title         string `json:"title"`
	// Pinned keeps the task above unpinned ones in its project's list. The user sets it.
	Pinned  bool   `json:"pinned,omitempty"`
	Task    string `json:"task"`
	Mode    Mode   `json:"mode"`
	Command string `json:"command,omitempty"` // custom CLI command line
	// PermissionMode is the Orole permission mode the session runs with: manual | auto | yolo,
	// or plan (read-only, daemon-internal). Each harness maps it onto its own approval controls.
	PermissionMode string `json:"permissionMode"`
	Pid            int    `json:"pid"`
	Status         Status `json:"status"`
	// Live is true while the agent process is running (runtime state, not persisted).
	Live        bool                 `json:"live"`
	Followup    FollowupCapabilities `json:"followup"`
	Activity    string               `json:"activity"`    // what the agent is doing right now
	Tail        string               `json:"tail"`        // last few lines of output, for the card
	LastMessage string               `json:"lastMessage"` // the agent's most recent message in full
	// AgentSessionID is the CLI's own session id, used to resume.
	AgentSessionID string `json:"agentSessionId"`
	TerminalID     string `json:"terminalId"`    // ConPTY terminal id while the session has one
	TranscriptRef  string `json:"transcriptRef"` // path of the CLI's own transcript when known
	ParentID       string `json:"parentId"`      // session this one was handed off / reviews
	// PRURL and PRNumber are set once the session's branch was published as a pull request.
	PRURL    string `json:"prUrl"`
	PRNumber int    `json:"prNumber"`
	Group    string `json:"group"` // try-N-ways group
	// Chain records automation provenance for lifecycle events, never tool authority.
	Chain []string `json:"chain,omitempty"`
	// Owner is the principal that started the session through WithActor ("bot:<id>"); "" for the user.
	// It decides who may steer the task and who hears about its progress, never tool authority.
	Owner string `json:"owner,omitempty"`
	// CallID is the phone call whose thread the owning bot started the session from (WithCall), or last steered it
	// from (SetCall); "" otherwise. The session's reports to its owner go to that call's thread instead of the chat.
	CallID       string `json:"callId,omitempty"`
	Review       string `json:"review"` // see Review* constants
	ReviewNote   string `json:"reviewNote"`
	CheckState   string `json:"checkState"`
	CheckCommand string `json:"checkCommand"`
	CheckOutput  string `json:"checkOutput"`
	Files        int    `json:"files"`
	Additions    int    `json:"additions"`
	Deletions    int    `json:"deletions"`
	Turns        int    `json:"turns"`   // completed turns
	Prompts      int    `json:"prompts"` // prompts sent
	// ToolCalls counts the tool calls structured harnesses (and Claude hooks) reported; FailedTools
	// those whose result was an error.
	ToolCalls   int `json:"toolCalls,omitempty"`
	FailedTools int `json:"failedTools,omitempty"`
	// Subagents counts the background agents (omp/Claude subagents, Codex child tasks) the harness
	// reported as running. A turn that ends while they run is not finished: the session stays
	// working until the last one reports and the harness's follow-up turn ends.
	Subagents int `json:"subagents,omitempty"`
	// Tokens is the session's headline token count: input, cache writes and output of its model
	// requests (Billed.Work). Cache reads, the context re-sent on every request, are left out:
	// they dwarf the rest. Billed reports settle it; while a turn runs it includes the harness's
	// live estimates.
	Tokens int64 `json:"tokens,omitempty"`
	// Billed breaks down the billed tokens, cache reads included, and their cost. It excludes the
	// running turn's estimates; nil until the first billed report.
	Billed *TokenTotals `json:"billed,omitempty"`
	// Pending are the permission requests awaiting a decision (runtime state).
	Pending   []PermissionRequest `json:"pending"`
	Usage     *ContextUsage       `json:"usage,omitempty"`
	Questions []QuestionRequest   `json:"questions,omitempty"` // runtime requests; never replayed after restart
	Queue     []QueuedPrompt      `json:"queue,omitempty"`     // durable follow-ups, paused after daemon restart
	Result    string              `json:"result"`
	Error     string              `json:"error"`
	ExitCode  *int                `json:"exitCode,omitempty"`
	StartedAt time.Time           `json:"startedAt"`
	EndedAt   *time.Time          `json:"endedAt,omitempty"`
	UpdatedAt time.Time           `json:"updatedAt"`
}

// SessionDelta is the "agents.delta" payload: the display fields a live session changed on a
// hot path (a message, tool call, activity line, usage or diff-stat update). A field is
// present only when it changed; everything else is unchanged since the last "agents.updated"
// or delta. Status changes, requests, queue and review changes always publish the full
// Session as "agents.updated" instead. Clients drop a delta older than the session they hold.
type SessionDelta struct {
	ID             string        `json:"id"`
	UpdatedAt      time.Time     `json:"updatedAt"`
	Activity       *string       `json:"activity,omitempty"`
	Tail           *string       `json:"tail,omitempty"`
	LastMessage    *string       `json:"lastMessage,omitempty"`
	AgentSessionID *string       `json:"agentSessionId,omitempty"`
	Model          *string       `json:"model,omitempty"`
	TranscriptRef  *string       `json:"transcriptRef,omitempty"`
	Files          *int          `json:"files,omitempty"`
	Additions      *int          `json:"additions,omitempty"`
	Deletions      *int          `json:"deletions,omitempty"`
	ToolCalls      *int          `json:"toolCalls,omitempty"`
	FailedTools    *int          `json:"failedTools,omitempty"`
	Tokens         *int64        `json:"tokens,omitempty"`
	Billed         *TokenTotals  `json:"billed,omitempty"`
	Usage          *ContextUsage `json:"usage,omitempty"`
}

// Event kinds.
const (
	EvInit        = "init"        // CLI session established; Data = {agentSessionId, model}
	EvPrompt      = "prompt"      // a prompt was sent to the agent
	EvMessage     = "message"     // assistant text
	EvTool        = "tool"        // tool use; Text = summary
	EvToolResult  = "tool_result" // tool output summary
	EvPermission  = "permission"  // blocked on approval; Data = PermissionRequest
	EvDecision    = "decision"    // approval decision; Data = {id, allow, reason}
	EvTurnDone    = "turn_done"   // turn finished; Data = {ok, result}
	EvNeedsInput  = "needs_input" // CLI is waiting on the user (hook notification)
	EvWorking     = "working"     // a turn started
	EvActivity    = "activity"    // activity update only
	EvError       = "error"       // error text
	EvExit        = "exit"        // process ended; Data = {code}
	EvSystem      = "system"      // daemon-authored note (resume, recovery, review...)
	EvTranscript  = "transcript"  // transcript path known; Text = path
	EvPermCancel  = "perm_cancel" // agent withdrew a permission request; Data = {id}
	EvOutputBytes = "output"      // terminal output counter bump (not persisted)
	// EvTitle is a harness-chosen task name (omp session_info_update/setTitle, Codex thread/name/updated);
	// Text = the name. The manager renames the session when its title is still automatic and never
	// records or publishes the event itself.
	EvTitle = "title"

	// Structured-harness events (omp RPC, Codex app-server, Claude stream-json). Adapters emit
	// these when an item completes (never per streamed token); live typing shows through
	// EvActivity. Data shapes are the contract with frontend/src/lib/taskEvents.ts.
	EvReasoning = "reasoning" // model reasoning/thinking; Text = full text; Data = {id}
	EvImage     = "image"     // generated image; Text = revised prompt; Data = ImageData
)

// ToolData is Event.Data for EvTool and EvToolResult: the same Id links a call to its result.
// Adapters set Status "running" on the call and "ok" or "error" on the result.
type ToolData struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Server string          `json:"server,omitempty"` // MCP server name when the tool is an MCP tool
	Status string          `json:"status"`           // running | ok | error
	Input  json.RawMessage `json:"input,omitempty"`
	Output string          `json:"output,omitempty"` // truncated to 8 KiB
}

// ImageData is Event.Data for EvImage. Path is an absolute file under the session's scratch
// directory; the frontend reads it through Agents.Artifact, never by raw path.
type ImageData struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Mime   string `json:"mime"`
	Prompt string `json:"prompt"`
}

// Event is one entry in a session's history.
type Event struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"sessionId"`
	At        time.Time       `json:"at"`
	Kind      string          `json:"kind"`
	Text      string          `json:"text"`
	Data      json.RawMessage `json:"data"`
}

// Detection reports whether an agent CLI is installed.
type Detection struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	Error     string `json:"error"`
	// Modes this adapter can run in; Structured/Resume/Permissions describe what the UI
	// can honestly rely on.
	Modes       []Mode `json:"modes"`
	Resume      bool   `json:"resume"`      // can continue a session by the CLI's session id
	Permissions bool   `json:"permissions"` // permission requests can be approved from the dashboard
	StatusFeed  string `json:"statusFeed"`  // "stream-json" | "app-server" | "hooks" | "process" (exit only)
	// PermissionModes says how the harness realises Manual, Auto and YOLO for structured
	// sessions, including when Auto is only approximated and when YOLO is turned off.
	PermissionModes []PermissionModeSupport `json:"permissionModes"`
}

// StartSpec is a request to start an agent.
type StartSpec struct {
	Kind          string   `json:"kind"`
	ProjectID     string   `json:"projectId,omitempty"`     // resolves Dir through the daemon's project registry
	Machine       string   `json:"machine,omitempty"`       // empty means local
	Reasoning     string   `json:"reasoning,omitempty"`     // empty preserves the CLI default
	Dir           string   `json:"dir"`                     // project or worktree directory
	Prompt        string   `json:"prompt"`                  // the task; may be empty (session starts idle)
	AttachmentIDs []string `json:"attachmentIds,omitempty"` // daemon-uploaded files handed to the task
	Model         string   `json:"model"`                   // optional
	Mode          Mode     `json:"mode"`                    // "" = adapter default
	Command       string   `json:"command"`                 // custom CLI command line
	Title         string   `json:"title"`
	// PermissionMode: "" (manual) | manual | auto | yolo | plan.
	PermissionMode string `json:"permissionMode"`
	// ConfirmYolo must be true for a yolo session; the daemon rejects it otherwise.
	ConfirmYolo bool `json:"confirmYolo"`
	// NewWorktree creates a fresh worktree of the repository at Dir on a new branch and
	// runs the agent there.
	NewWorktree bool   `json:"newWorktree"`
	Branch      string `json:"branch"` // branch name for NewWorktree ("" = generated)
	BaseRef     string `json:"baseRef"`
	ParentID    string `json:"parentId"`
	Group       string `json:"group"`
	// Key makes the request idempotent: the same key never starts a second session.
	Key string `json:"key"`
}

// ReviewDiff is the work a session produced, for the Review view.
type ReviewDiff struct {
	SessionID string `json:"sessionId"`
	BaseRef   string `json:"baseRef"`
	Dir       string `json:"dir"`
	Files     int    `json:"files"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Truncated bool   `json:"truncated"`
	Diff      Diff   `json:"diff"`
	Error     string `json:"error"`
}

// Artifact is a file a session produced (a generated image), returned by value because the
// frontend never gets a raw filesystem path to read. Data is base64 in JSON.
type Artifact struct {
	Mime string `json:"mime"`
	Data []byte `json:"data"`
}

// PublishSpec turns a session's work into a commit, a pushed branch and optionally a pull request,
// using git and the GitHub CLI/API credentials the daemon already holds. Steps run in order and stop
// at the first failure; each step is skipped when its flag is off or it is already done.
type PublishSpec struct {
	Message string `json:"message"` // commit message ("" = generated from the task title); empty working tree skips the commit
	Push    bool   `json:"push"`    // push the session branch to origin (sets upstream)
	PR      bool   `json:"pr"`      // open a pull request for the branch (implies Push)
	Title   string `json:"title"`   // PR title ("" = session title)
	Body    string `json:"body"`    // PR body ("" = the task prompt and result)
	Base    string `json:"base"`    // PR base branch ("" = the session's base ref's branch or the default branch)
	Draft   bool   `json:"draft"`
	// Confirmed is the user's explicit confirmation of the remote effects (push, pull request).
	// Go requires it before anything leaves the machine; the dialog that offers Publish sets it.
	Confirmed bool `json:"confirmed"`
}
