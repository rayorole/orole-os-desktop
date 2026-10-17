# CLI account runtimes

Claude, ChatGPT (Codex) and Grok can be used with the user's own plan through the vendor's CLI. The CLI signs in
and keeps the credentials; Orole never reads, stores or logs them. `internal/cliruntime` owns the state, the daemon
exposes it as `cli.providers.{list,install,login,logout}`, and Settings > AI providers > Accounts shows it.

## Which CLI runs
In order: `OROLE_CLAUDE_PATH` / `OROLE_CODEX_PATH` / `OROLE_GROK_PATH` (a missing override means "not installed",
never a silent fallback), the newest managed copy in `<data>/cli/<id>/<version>/`, then the first match on `PATH`.

## Status
Each provider reports `installed`, `path`, `source`, `version`, `latest`, `updateAvailable`, `auth`, `account` and
`installable`. A lookup that fails (offline, rate limit) is reported in `problem` and never hides the rest.

| Provider | Version | Latest release | Sign-in |
| --- | --- | --- | --- |
| Claude | `claude --version` | npm `@anthropic-ai/claude-code` `latest` | `claude auth status` (JSON) |
| ChatGPT | `codex --version` | GitHub `openai/codex` latest release | `codex login status` |
| Grok | `grok --version` | `https://x.ai/cli/stable` | `grok models` (no status command): "not authenticated" means signed out, a model list means signed in |

Latest versions are cached for 30 minutes.

## Managed install and update
Install downloads from the vendor's own channel into a staging directory, checks the hash where the channel
publishes one (Claude: npm `dist.integrity` sha512; Codex: the GitHub asset sha256; Grok: none, TLS only), runs
`--version` and requires the expected version, then moves it into place. A copy that fails any step never replaces a
working one. The two newest versions are kept. Update is the same operation: it adds Orole's own copy and leaves a
CLI the user installed themselves untouched. Install and sign-in are audited (`cli.install`, `cli.login`,
`cli.logout`).

## Terms
Anthropic limits Claude subscription sign-in to ordinary individual use of the unmodified CLI. The Accounts page says so
next to the Claude row.

## ChatGPT (Codex) as a model for bots
`internal/llm/cli` registers the provider `codex` (kind `account`, no key). It counts as connected while the CLI is
installed and signed in (`cliruntime.Manager.Ready`, from the last known status: the daemon refreshes it at start and
after every install, sign-in and sign-out and then publishes `llm.changed`). Models come from `codex debug models`,
so the picker lists what the account may use.

Each model call is one `codex exec --json --ephemeral --ignore-user-config --ignore-rules -s read-only` run in an
empty scratch directory, with the prompt on stdin. The CLI is used as a plain model, not as an agent: the prompt
tells it not to run commands or read files, and the read-only sandbox backs that up. Orole's own tool loop stays in
charge. Bot tools are described in the prompt and a reply holding a `tool_calls` block becomes tool-call deltas (a
malformed block, or one naming a tool that was not offered, is ordinary text), so memory, budgets, handoffs, queue and
`show_*` components work as with any provider. Claude models sometimes ignore the block and answer in the XML call
markup they are trained on (`invoke` elements with `parameter` children, optionally inside `function_calls` and a code
fence); that markup is parsed as a second call format under the same rules, so it runs the tool instead of reaching
the user as raw text. Parameter values are raw text: a parameter the schema types as `string` is taken verbatim, any
other is used as JSON when it parses.

The prompt distinguishes native CLI tools from Orole application actions explicitly. The native-tool restriction
must not prohibit text dispatch requests: the CLI does not register `computer_run` as a native tool, but Orole
can execute its `tool_calls` request. A user request to start the bot computer must dispatch an action in the same
reply, rather than merely promise to start it. Earlier conversation refusals do not remove the offered actions.

An opt-in live regression check verifies this interpretation with the configured signed-in CLI and model. It requests
`computer_run` after a prior promise or refusal, but never executes the returned action or boots a VM:

```powershell
$env:OROLE_LIVE_CODEX_DISPATCH = "1"
$env:OROLE_LIVE_CODEX_PATH = "C:\path\to\codex.exe"
$env:OROLE_LIVE_CODEX_MODEL = "your-model-id"
go test ./internal/llm/cli -run TestLiveCodexRequestsApplicationComputer -v
```

Windows VM communication endpoints live in a short, profile-specific directory under the user's temp directory.
The backend applies its protected user-and-SYSTEM DACL before creating the sockets and per-boot token there;
VM disks and downloaded images remain in the profile. A local VM smoke test can reuse an installed runtime
without downloading it: set `OROLE_SANDBOX_LOCAL_RUNTIME_ROOT` to the installed `sandbox` directory and run
`go test ./internal/sandbox -run TestLocalInstalledRuntimeBoot -v`. It boots an isolated test VM, runs `pwd`,
and shuts it down; it does not use the bot's disk.

Limits: `codex exec` prints whole messages, so replies appear at once instead of streaming; every step of a tool loop is a
new CLI start (a few seconds); images and PDFs are not forwarded (vision is off for these models).

## Claude as a model for bots
The provider `claude` (`internal/llm/cli/claude.go`) works like the Codex one, with two differences. Claude Code streams,
so text appears as it is written: `replyStream` holds back only a possible start of a call (a `tool_calls` fence or XML
call markup), so a tool call never flashes its raw markup. Models are the aliases the CLI documents for `--model` (`opus`, `sonnet`, `haiku`, each the
newest of its family), because the CLI has no model list. Each call is
`claude -p --output-format stream-json --include-partial-messages --no-session-persistence --tools "" --strict-mcp-config --setting-sources "" --disable-slash-commands`:
no built-in tool, user setting, MCP server or skill is loaded, and Claude Code's own system prompt is replaced. The bot's
instructions and the conversation go on stdin, never on the command line.

Anthropic's terms limit subscription sign-in to ordinary individual use of the unmodified CLI; bots that work in the
background on a plan can go past that. The Accounts page says so next to the Claude row.

## Grok as a model for bots
Grok Build's headless mode (`--prompt-file`, `--output-format streaming-messages-json --include-partial-messages`) speaks
the same Anthropic Messages wire format as Claude Code, so `internal/llm/cli/grok.go` shares `readStreamJSON` with the
Claude provider. Text streams. Models come from `grok models`, which also tells whether anyone is signed in.

The CLI has no switch for "no tools". Orole removes its built-in tools with `--disallowed-tools`, `--no-subagents` and
`--no-plan`, replaces its system prompt and runs it in an empty scratch directory. A few shell and subagent tools cannot be
removed; headless mode has nobody to approve them, so a call is cancelled instead of run (verified: a requested
`echo > file` created no file). A turn where the model insists on a tool ends as an error result.
