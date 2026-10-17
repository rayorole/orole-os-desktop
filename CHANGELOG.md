# Changelog

All notable changes to the Orole OS desktop app. Format follows [Keep a Changelog](https://keepachangelog.com).

Each release needs a `## X.Y.Z - YYYY-MM-DD` section matching `version` in `mygo.json`: the release
workflow refuses to build without it, and the in-app update window shows that section as the release
notes. See `docs/development/updates.md`.

## Unreleased

### Changed
- Token counts no longer include cache reads, the conversation context a coding agent re-sends with every request. Those were over 90% of the total and made a task look about ten times bigger than the work it did (a Claude task showed 1.2M tokens for about 101K of new input and output). A task's meter in chat, the run header in a task, and Home's Tokens, By agent, Latest sessions and All time widgets now count input + cache writes + output. Hover a count to see input, cache writes, cache reads, output and cost. Spend is unchanged.

### Fixed
- Resuming a Claude Code task no longer bills everything the task had used before again. A resumed Claude Code process reports the session's earlier usage as part of its first turn; that turn now counts only its own tokens. Usage recorded for earlier resumes stays as it was.

## 0.4.2-dev - 2026-10-17

### Fixed
- The installer can now update Orole OS while it is open. It closes the app and its daemon first (a bot's computer stops with it, its files stay) instead of failing with "Error opening file for writing".

## 0.4.1-dev - 2026-10-17

### Changed
- A bot's Computer pane now shows one view at a time: Screen, Terminal or Activity, with the last one remembered. Activity lists what the bot ran (commands with their output, file writes, browser actions) as it happens. A Full screen button gives the whole chat area to the pane (Esc leaves), so you can browse the web on the computer at a comfortable size. The Computer button in the title bar no longer opens a pane when the computer is off; it says why instead, and the pane's footer note about network reach was removed. Terminals draw on the GPU when the browser allows it, which keeps fast output smooth.
- Panes beside the conversation (code task, call, computer, document) open and close without the sliding animation, like the bot settings panel.
- A bot's Tools page is shorter: its switches are one plain list without descriptions.
- Settings › Phone is a step-by-step setup (Twilio, ElevenLabs, Relay, Your phone, Calls) with an animated stepper.

### Fixed
- A long unbroken text such as a link no longer runs out of your message bubble into the sidebar.
- Some models wrote a tool call as plain JSON text instead of running it. Bots now run such calls instead of showing the JSON.

## 0.4.0-dev - 2026-10-15

### Changed
- A bot's computer now uses your keyboard layout: what you type while you control it comes out as on your keyboard (AZERTY, QWERTZ, Swiss, Nordic, Dvorak and others; your Windows sign-in layout, read when the computer starts). You also see the mouse pointer on its screen while you control it. The computer is named after the bot: its account, home folder and computer name follow the bot's name ("Atlas 2" works as `atlas-2` in `/home/atlas-2` on `atlas-2-computer`), and a renamed bot gets its new name the next time its computer starts, with the same files. This needs computer image 0.1.4: a bot's existing computer keeps its image until the bot is archived and made again.
- Phone call voices now follow the call language. For Dutch (and Auto) the voice pickers on a bot's Phone calls page and in Settings › Phone list Dutch voices from the ElevenLabs Voice Library, grouped as Standaard Nederlands, Vlaams and Andere; for English, a short "Best for calls" list of conversational voices. The top of the open list says which language the voices are for, and the list reloads when you change the call language. Picking a Voice Library voice adds it to your ElevenLabs voices first. Each voice in the open list has a play button to hear a short preview; the closed select shows only the chosen voice. On a bot's Phone calls page, Voice and Voice model have their own icons.
- The notification center (the bell at the top right) was redesigned. It has two tabs: All, and Needs you, which lists what is waiting on you (a task asking for approval, a bot's question or permission request, a flow at an approval step) with a count, and empties itself once you answer. Rows are grouped by day (Today, Yesterday, This week, Earlier), show what reported them (a bot's avatar, a task or flow icon) with a badge for what happened, and open that task, bot chat or flow on click. Mark all read and Settings stay; the overflow menu (Unread only, Refresh, Dismiss shown) and the source filter are gone.
- Notifications now come from all of the app: code tasks report when they need you, fail or finish; bots when they reply, fail, ask you something or want a permission; flows when they wait for approval, fail, or finish a run you started (scheduled and event runs only notify when they fail). A Notify step still sends its message. Tasks a bot started report to that bot, so they do not notify twice. Anything already open in the focused window is marked read as it arrives, without a sound.
- Settings › Alerts & automations › Alerts was rebuilt: one row per kind (Needs you, Failed, Finished, Bot replies, Flow messages) with In app and Sound toggles and a button to hear its sound. Needs you cannot be hidden. Each kind plays its own sound, also while Orole is in the background; volume stays under Customization › Sounds. Earlier per-source preferences are reset to these defaults.
- Settings › System no longer holds bot and task settings. They now have pages of their own: Task permissions (Yolo, default and bot permission modes, remembered approvals), Bot tools and Bot code tasks. "Name tasks with AI" stays under System › Tasks. The bot permissions pane drops the descriptions under each tool access option, and shows agent and model logos in its code task selects (and in Settings › Bot code tasks).
- A coding task's card in a bot chat names its model ("Claude Sonnet 5.5" rather than `claude-sonnet-5-5`) with the model maker's logo, and its token count now counts up while the task works instead of showing a near-zero number until the turn ends. The count covers all of the task's model requests, cache reads included, so it is larger than before.
- The task pane beside a bot chat no longer repeats the task's status under its title.
- Everything a bot shows in chat (tables, charts, diagrams, timelines, cards, questions, reports) now looks like its chat bubbles: same rounded corners and background, no border.
- Bot tables are easier to read: short columns stay on one line, long text columns keep a readable width, rows are separated, and a wide table scrolls sideways instead of cutting off its last column. Table text can be selected, and a copy button puts the whole table on the clipboard (pastes as a table in documents and spreadsheets).
- A coding task started with a model alias such as "opus" is labelled with the model it runs ("Claude Opus 5.5") from the start, instead of showing "opus" until the agent reports its model. Bots now see which model each alias runs when they pick one, and are told it when the task starts.
- A coding task's context window popover now shows where the context goes, as Claude Code itself counts it: a grid of the window coloured by category (system prompt, tools, MCP tools, memory files, skills, messages), each category's token count with its largest contributors (memory files, MCP servers, skills, tool results...), the auto-compact reserve, tool schemas that are loaded only on demand, tokens left and where auto-compact kicks in. The breakdown refreshes when the task starts and after each turn. Codex and Oh My Pi report only totals, so their tasks show a plain meter and say so instead of guessing a split.
- The message boxes for bots, new code tasks and task replies have a new look: one card for the text and its controls, with the buttons in a row underneath.
- The bot message box's `@` and `/` menus were rebuilt: grouped lists (Bots, Integrations, Skills) with each one's icon and description, arrow keys and Enter or Tab to pick, Escape to close, and `@` and `/` buttons to open them. `@` now also lists the bot's integrations: mention one ("@Canvas LMS") and the bot is told to use that integration's tools for that message.
- While a bot works you now see what it is doing: a panel in its chat bubble names the step it is on with the time so far, and lists its thinking as it streams, each tool it uses, the pages it read and its handoffs to other bots, instead of a shimmering "Thinking…" line. When the reply is done the panel folds to "Thought for 7s" and opens again on click.
- A coding task's conversation now shows what the agent is doing as it works, instead of one "Working for…" line and everything dumped at the end. Each turn is a tree: its header names the current step with the time so far (and the session's tokens on the latest turn), and under the agent you see its thinking, the messages it writes while working (as they arrive), its tool calls as compact rows with their time, shell commands as terminal cards (a failed one opens with its output and exit code), its plan, and any subagents under the call that started them. When the turn ends the tree folds to "Worked for 12m 03s" and opens again on click; the final answer shows as a card with its first lines and a Show all button.
- A bot's Memories page was redesigned. Notes are grouped by how the bot uses them: "Always in mind" (with how much of the character budget is used) and "Recalled when relevant", with ones waiting for approval on top. Add a note with + (Ctrl+Enter saves, Escape cancels), edit or forget one from its row, or Forget all after a confirmation. The automation ideas list no longer shows on this page.
- Compact density (the default) is tighter: padding, margins and gaps across the app are three quarters of what they were, rounded to whole pixels. Control and icon sizes are unchanged. Comfortable density keeps the previous spacing.

### Removed
- Windows desktop notifications (toasts). Notifications appear only in the app's notification center, with sound.

### Fixed
- The Jump to latest buttons in code task conversations and bot chats turned see-through on hover, so the messages behind them showed through. They now stay solid.
- The daemon and the `orole` CLI reported version 0.1.0-dev unless built with `scripts/build.ps1` (for example the daemon `go tool mygo dev` builds). The daemon, CLI and app now all read their version from `mygo.json`, so Settings › Diagnostics, `orole version` and `orole daemon status` match the app.
- The notification center no longer opens over the window's minimise, maximise and close buttons in a short window. It always opens below the title bar and gets shorter to fit.

### Added
- Groundwork for bots with their own computer: a "Has its own computer" switch in a bot's Tools settings gives the bot tools to run commands, read and write files and use a web browser on a private Linux computer (a local VM) on your PC. A new Computer tab on the bot's page shows the computer live: you watch its screen, use a terminal inside it and see the commands the bot runs; chat shows a "Using the computer" card whenever the bot uses it. The first time, the tab walks you through turning on Windows Hypervisor Platform (one administrator prompt and a restart; Hyper-V users usually have it already) and downloading the computer (about 1.6 GB). It needs Windows 10 version 2004 or Windows 11. Your bot tool access setting decides what runs without asking. With "Ask me first" or "Look things up without asking", the bot asks before each new website it opens by address (you can allow a site for good); that check covers only addresses the bot types itself: links it clicks, redirects, new tabs and its own commands can reach other sites, and bots allowed to read and change (or everything) are never asked. It will not open addresses on your own PC or network when it types them, but the computer can reach the internet, your home network and anything on this PC that accepts network connections on its network address (only the PC's own loopback is blocked). Approving a command shows the command, not the contents of a script the bot wrote earlier (approving a file write shows its first 300 bytes). When a task needs you to log in, solve a CAPTCHA or confirm something, the bot asks you to take over: a "Needs you" notification opens the bot, and it waits until you hand the computer back: the tab shows the bot's reason, **Take over** gives you the mouse and keyboard, and **Done** (with an optional note) hands the computer back. If you leave the Computer tab or minimise or hide the window for about 2½ minutes, a takeover you started goes back to the bot automatically; a takeover the bot asked for waits up to 30 minutes for Done. The terminal runs as the bot, so what it shows comes from the bot (its programs can also see what you type there): do not type secrets into it. While you have the computer, the bot's commands, file writes and browser are refused, and the bot's own programs are blocked from the browser's debugging port and the screen-sharing port, but they run as the same user as the browser, so this is not a full separation (a later update runs the browser separately). The bot refuses to type into regular password fields; ask it to hand over for passwords (custom web-component password fields may not be detected). What you sign in to stays in that computer's browser: the bot can use those sessions, and its own commands can read the browser's cookie files.
- Composio in Settings › Integrations: one-click browser sign-in to Composio Connect (`https://connect.composio.dev/mcp`), which gives bots tools for more than a thousand apps through one connection.
- An open code task has a Session Rail on its right, shown or hidden with the Session Rail button at the top right of the window (the choice is remembered). It holds sections you can fold, and drag into another order by the grip at each section's top right (or focus the grip and use Space and the arrow keys); the order is remembered. Environment (computer, folder and branch), Context (the context window grid and categories, with each one's share of the window), Connectors (each MCP server with its status, an on/off switch, Sign in or Reconnect when it dropped, and switches for single tools under a connected server; these apply to every task and bot), Changes (lines added and removed, with Create PR, which commits, pushes the branch and opens a pull request after you confirm, then links it) and Background tasks (the agent's subagents: failed ones on top, running ones with their time so far, finished ones folded).
- Jarvis mode: bots with full permissions can phone you when you tell them to (rules from chat, for example "call me when that task finishes") or when their "When to call me" policy says so, and you can call them. Needs a Twilio number, an ElevenLabs key and a small Cloudflare relay (Settings › Phone). Calls need Orole running on this PC and the relay online. Each call shows as one card in the bot's chat, drawn like the bot's messages; click it to read the call beside the conversation: who called, when, how long, the estimated cost and the transcript with what the bot did (code tasks open from there), updating live while the call is on. What was said stays out of the chat itself, but the bot remembers it. The bot's Phone calls page keeps the Test call button but no longer lists recent calls. Calls are in Dutch by default: Settings › Phone › Call language offers Nederlands, English or Auto (starts in Dutch and switches if you speak another language); it sets the language the bot speaks, what it hears, and its fixed lines such as greetings and "one moment". A bot calling you greets you by name first and says why only once you answer, so a voicemail never hears the reason; it hangs up by itself when an answering machine picks up. A bot that already has Jarvis on and loses a needed permission is saved with Jarvis switched off (you get a notice) instead of the save failing. On a call the bot no longer answers a silence ("I didn't hear anything"), no longer restarts when the voice service repeats your question, asks whether there is anything else when it is done, and says goodbye and hangs up when you are. Voice and voice model pickers show each voice with a short description and a generated avatar, and offer a short curated list of models with a recommended default for calls.
- Phone calls with a bot are steadier and more natural. The voice service no longer hangs up when the bot takes a while: it now waits up to 15 seconds for an answer, the bot keeps the line alive without talking over you. It no longer says "one moment" on every question: it speaks while working only when it uses a tool, and then says what it is doing ("I'm using your Gmail tools now, give me a second for the results", "I'm searching the web for that now"), or when an answer takes more than about 4 seconds, and a call the voice service does end by itself now gives a "Call with <bot> dropped" notice with the reason. If one question takes more than a minute, the bot says it is taking too long on the phone and offers to run it as a background task. The bot waits for you to finish: after an "uhm" or a trailing "en…" it holds a moment, and if you carry on it answers everything you said as one. When you say nothing it stays quiet, asks "Are you still there?" after about 30 seconds and says goodbye and hangs up after about 90 seconds of silence. It only ends a call when you say you are done ("nee, bedankt", "that's all", "doei"), never on its own, and the call now ends as soon as its goodbye has been spoken instead of a few seconds later. A caller no longer waits 7 seconds for the greeting. When the bot hands work to another bot or starts a background task it says who is doing it and why, asks whether there is anything else, and can tell you how it is going. Nothing that comes out of a call lands in the bot's chat any more (only the call card does): the reports of bots and code tasks started on the call, and the bot's written answers to them, stay with the call and show in the call pane, also after you hang up, when you get a "reported back" notice that opens the bot. While you are still on the line, the bot tells you when that work reports back. Ask "call me back when it's done" and the bot saves a rule for it, now also for work it gave to another bot ("when Code Assistant reports back"). Redeploy the relay for this (`bunx wrangler@4 deploy` in `relay/`); the first start after updating also updates the phone agent once.
- Dictation no longer needs an OpenAI API key: speech is now recognized on your computer, and neither the recording nor its text leaves it. A new Settings › Voice page lists two models with their download size, Parakeet TDT 0.6B v3 (fast, finds the language itself, 25 European languages, 639.4 MB) and Whisper large-v3 turbo (slower, about 100 languages, 988.6 MB), and downloads, cancels, resumes and removes them. Choose which one is in use, and with Whisper the spoken language. Nothing downloads until you ask; the first dictation offers the Parakeet download in one click. Once a model is ready, the microphone button starts recording right away instead of opening a menu first. Dictation needs Windows on x64.
- A custom connector can now be added with an access token, for MCP servers that use a fixed bearer token instead of browser sign-in. The token is kept in Windows Credential Manager. On such a server's page, Sign in is replaced by a field to swap in a new token.
- A bot's Phone calls page can use any ElevenLabs voice by its ID: pick "Custom voice ID" at the bottom of the voice list and paste the ID of a voice in your ElevenLabs account.
- Bots can use integrations and tools without asking you first. Under Settings › Bot tools, choose Ask me first, Look things up without asking, Read and change without asking, or Everything without asking (deleting and sending included). Each bot can override this on its new Permissions page. If you set a tool to "Always ask" or "Block" on an integration's page, that still wins, and everything a bot does without asking is recorded in the audit log.
- Settings › Bot code tasks sets the agent, model, effort and permission mode for code tasks your bots start. Agent, model and effort can each be "Auto", which lets the bot choose what fits the job. Any value you fix is used as-is, whatever the bot asks for. Each bot can override these on its Permissions page.
- A task can now be deleted from its own page: a trash button in the title bar (and on the task beside a bot's chat) asks first, then removes the task and its history. A running task is stopped before it is deleted. Files, branches and worktrees in your project stay on disk. The task's sidebar menu offers the same Delete.

### Changed
- Scrollbars across the app are thinner, with a smaller thumb and no arrow buttons. In code tasks and bot chats the conversation's scrollbar now runs the full height of the page, down past the message box, and the conversation scrolls under the message box; in a code task it sits at the far right, beside the Session Rail.
- AI providers settings are shorter: the intro box about where keys are stored is gone, account rows no longer carry a "Uses your … plan through …" note, and a CLI Orole OS installed itself is no longer labelled "Orole's copy".
- A coding agent or document opened from a bot's chat now sits beside the conversation like the bot settings panel, at any window width, instead of sliding over the chat on narrower windows. Drag the divider on its left edge (or focus it and use the arrow keys) to resize it; the width is remembered, and double-clicking the divider resets it.
- The permission mode of a bot's code task now always comes from your settings (the bot's own setting, then Bot code tasks, then your default mode). The bot can no longer choose Manual itself. If your setting is Yolo but "Bots may use Yolo" is off, the task doesn't start and the bot tells you why, instead of quietly starting in Manual.
- Bot approvals now also show up in Home › Needs you and as a count on the bot in the sidebar, so a bot no longer waits on a chat you aren't looking at. Settings › Remembered approvals lists every rule you saved for a bot.

### Fixed
- Orole Drive, Orole Mail and other MCP servers that only speak the 2026-07-28 protocol now connect after sign-in instead of retrying with "Unsupported protocol version: 2025-06-18". Orole OS still starts with the older handshake, which some servers need, and switches to 2026-07-28 when a server rejects it.
- Windows Codex downloads now include the code-mode host and sandbox resources from the hash-verified official npm platform package. Incomplete managed installations can be repaired by downloading again. The Codex agent picker uses the same managed runtime as Settings and chat.
- Connecting an MCP server that accepts OAuth client ID metadata documents but not dynamic registration (such as Orole Drive and Orole Mail) no longer fails with "the authorization server offers no way to register this client". Orole OS now signs in with its own published client document, `https://orole.be/oauth/orole-os.json`, so you don't have to enter a client ID. A client ID you set on the server still takes priority.
- "Always allow this bot" on a bot's integration, Mail or Drive approval now remembers that tool. Before, it only allowed it once and asked again next time. It is hidden for destructive actions, because those can't be remembered.
- When you don't answer a bot's approval within 5 minutes, the bot tells you it timed out instead of reporting a vague denial.

## 0.3.1-dev - 2026-10-13

### Fixed
- Bots no longer show an activity indicator while they are idle. The sidebar dot appears only while a bot is replying (pulsing) or running a code task it started (pulsing, blue), or when its last message is a question waiting for you, and it disappears as soon as the work finishes, fails or is stopped. The "bots" count in the status bar follows the same rule.

### Changed
- In a bot's settings, "Runs code tasks" moved to the Tools page, and the "All changes saved" footer is gone (changes still save on their own and errors still show).
- A task's activity list is easier to read. Each step is one plain line with its icon, what it did and the command or file in monospace, without the boxed chips. A chevron appears when you hover a step that has details.
- Hover text across the app now uses Orole's tooltips instead of the browser's native ones, and only where something isn't clear without it: icon-only buttons, why a control is disabled, or a cut-off path, link or time you can't read anywhere else. Tooltips that only repeated visible text are gone.

### Added
- When an agent keeps a to-do list while it works (Claude Code, Codex, Gemini CLI or omp), the task shows it as a Tasks list: how many steps are done, a progress bar, the step in progress with what it is doing right now, and how long each step took. Open a step to see the commands and tool calls behind it. Codex plan updates, which weren't shown before, now appear there too.
- The permission mode you pick when starting a task (Manual, Auto or Yolo) is now remembered as your default. New tasks start in it, and bots that start tasks follow it too; Yolo only applies to bots when "Bots may use Yolo" is on, otherwise they use Manual. Running tasks keep their mode.
- Bots can set up automations from chat. Every bot can do this, with no setting to turn on. Ask it, for example, to "watch CI
  on main and tell me when it fails". The bot drafts the flow, Orole validates it, and it appears as a card in the chat with
  the trigger, the steps and anything it runs or sends. Nothing is saved until you choose Turn on, Add turned off or
  Dismiss; drafts that run programs or send web requests can only be added turned off, for review in Automations. Bots can
  also read your automations and their recent runs, and draft changes, which you apply from the card. Automations a bot
  set up show "By <bot>" in the list.
- Bots notice when you keep asking for the same check, such as CI status on a branch, on different
  days, and offer once to automate it. Only requests you typed count; messages from automations, tasks or other bots do
  not. A bot offers at most one idea a day and stops after you decline. The ideas are listed under the bot's Memories,
  where each can be forgotten.
- Skills settings are redesigned with Yours and Discover tabs. Discover browses and searches the skills.sh catalogue
  (Most installed, Trending, and skills.sh's topics such as Marketing and Design); a skill is added from a preview that shows
  its full SKILL.md and flags script files, then is available to every agent through Orole. Adding does not change your agents'
  own skill folders.
- Yours lists every skill with a source badge (the agent's own logo for skills imported from Claude Code, Codex or Oh My Pi) and when
  it was added. A skill opens on its own page with Overview and Contents tabs instead of an editor beside the list.
- Agent syncing is easier to read: one "Used by" line shows which agents are up to date and one "Update agents" button copies
  skills into Claude Code, Codex and Oh My Pi; a help popover explains how skills reach agents and holds the details.
- Integrations directory: PostHog, Resend, Firecrawl, Clay, Apollo, Figma, Mobbin and Similarweb (one-click browser sign-in), Paper
  (local: connects to Paper Desktop at `http://127.0.0.1:29979/mcp` while it is running with a file open, no sign-in), and Gmail,
  Google Drive and Google Calendar (Google's remote MCP servers; you supply an OAuth client from your own Google Cloud project).
  Notion and Sentry were already listed.
- Automations can start on app events: a task is created, changes status, waits for you, finishes or fails, a pull request is
  opened, a project is added, or a bot replies. Each event carries the task's title, project, branch, status and last message.
- Automation flows have condition lanes: a Condition block runs one lane or the other and the flow continues after it. New
  blocks: Ask a bot (the message lands in the bot's conversation; optionally wait for its reply), Start / Message / Wait for a
  code task, and Wait. Existing automations are upgraded automatically; a jump that has no lane equivalent is flagged and the
  automation will not run until it is rebuilt.
- Bots can run code tasks. Turn on "Runs code tasks" in a bot's panel and it can start a coding agent in one of your projects,
  check on any task, and message or stop the ones it started. A start is approved inline in the bot's chat (Allow once,
  Always allow this bot, Deny) instead of a dialog; "Always allow" is listed under Settings › Task permissions, where
  "Bots start tasks without asking" skips the question for every bot. The bot tells you in its chat when its task finishes,
  fails or needs you ("Code task update", with a link to the task).
- A task is reported to its bot only when it is really finished: a turn that ends while the agent's background subagents
  (Oh My Pi, Claude Code, Codex) are still running keeps the task "working" ("Turn ended; waiting for N background agents")
  and completes once the last one reports and the follow-up turn ends; a completed turn also settles for a few seconds so a
  harness that immediately continues never counts as done. The finished report carries the task's complete result (up to
  32 KiB, no longer clipped to 2,000 characters), and the bot is told to relay it as a full overview; `task_status` returns
  the full result and the number of completed turns and running background agents.
- Tasks have a default permission mode (Settings › Task permissions: Manual, Auto or Yolo) used by the New task composer and
  by bots and automations that name none. "Bots may use Yolo" lets a bot start a task in Yolo when the user allows it;
  otherwise a Yolo request or default falls back as before (bots: Manual).
- Bots can show their working list (`show_todo_list`: pending, active, done, failed, cancelled steps with an n/m counter,
  updated in place as they work) and documents (`show_document`: a titled Markdown artifact shown as a card in the reply and
  opened beside the conversation with copy and close; revisions bump the version).
- A bot's reply shows each code task it started as a chat bubble (the same bubble as the bot's messages) with live status
  (working, done, needs you, failed), its current activity, the coding agent and model it runs on, and a live meter of
  elapsed time and tokens that freezes when the task stops (a dash where the harness reported nothing). Clicking a bubble opens the task beside the conversation,
  with the agent's live activity and a composer to message that agent directly; "Open in Tasks" moves it to the Tasks view.
  The side pane (task or document) animates its width and slides out when closed instead of snapping.
- Automations can watch GitHub (new commits on a branch, pull requests opened or merged, failed workflow runs, new issues)
  and deployments of connected integrations such as Vercel or Railway. Orole checks every minute or two (it never opens a
  network port), starts from what already exists, and runs the flow once for each new item.
- Home has an Overview: tasks started, completed, pull requests and lines changed for this week, month or year, each
  compared with the same point of the previous one; a year-long activity graph; and your current and longest streaks with
  your busiest month and weekday. It is computed from your tasks; the former Activity heat graph widget becomes this Overview.
- New Home widgets: Recent tasks (opens a task in one click), Running now (what each working agent is doing and for how
  long) and Quick actions (new task, new automation, review and settings, plus commands you ran recently from search).
- The daemon keeps a token usage ledger for Claude Code, Codex and Oh My Pi tasks (input, output, cache reads and writes,
  reasoning) per task, model and quarter hour, and counts each task's tool calls and failed tool calls. `agents.usage` (`Agents.usage`)
  reports the last 24 hours, 7 days and all time, usage per agent, a year of daily totals and the latest tasks. Spend is
  the harness's own reported cost where it gives one (Claude Code, Oh My Pi), otherwise the models.dev list price; models
  without a known price are named instead of guessed. Prices ship built in and refresh from models.dev at most once a day.
  Usage is recorded from this version on; terminal and custom-command tasks report none.
- Home usage widgets built on `agents.usage`: Needs you (tasks waiting on a permission, question or reply, held MCP actions
  and automation approvals, each opening where it is decided), Sessions (last 24 hours with the agents that ran them), Tokens
  and Spend (24 hours, 7 days or all time, with the average per active day and a trend line; spend reads "≥" with the models
  it excludes while some have no price), All time totals, By agent (tokens and spend per harness) and Latest sessions (tool
  calls, failed tools and tokens; a row opens the task). They are on the default Home layout; saved layouts keep theirs.

### Changed
- The sidebar's Agents section is now called Bots.
- The task composer no longer shows the "Follow-ups wait in the daemon's queue" note while a task is running.
- Bot chats no longer show "Code task update" cards. A code task's report still reaches the bot, and you read the bot's
  reply to it; open the task from the bot's task chip or the sidebar.
- The Code Home and New task composers are rebuilt on HextaUI's agent Prompt Input: the prompt sits in a card with attach,
  permission mode, the agent menu and send; a footer row below picks where it runs, the project and the worktree. The agent menu
  now shows every agent (with its version, or why it is unavailable) beside its models, searchable once an agent lists seven or
  more, with 1–9 to pick a model and an animated reasoning-effort slider. Enter starts the task; Shift+Enter adds a line.
- The status bar shows `bots active/total` next to the tasks count: bots replying right now out of all your bots. Clicking it
  opens chat.
- The sidebar is one list instead of Bots and Code tabs: Search, then Home, Tasks, Review, Issues and Automations, then the
  Agents section (your bots) and the Code section (projects and their tasks), which scroll together. "Switch to Bots/Code"
  commands are gone; "Go to …" commands and the bots' Chat entries in the palette remain.
- New tasks start in the project's current checkout by default; New worktree is still one pick away in the composer footer.
- Claude models are named in full with their version, such as "Claude Opus 5.0" and "Claude Fable 5.1", and the 1M variants
  say "1M context". Agent versions show without the CLI's suffix.
- The Claude model list is read with your signed-in account, so models your plan includes (such as Fable) are offered as in
  Claude Code itself; it was previously read signed out and showed only the base catalog.
- Workspaces are removed: the sidebar workspace switcher and Settings > Workspaces are gone. Home keeps one widget dashboard
  (stored as the `dashboard` setting); the first existing workspace's widgets and notes carry over, other workspaces are dropped.
- Automations moved to the Bots side of the app. The Bots sidebar now has the same Search (Ctrl+K) as Code, an Automations
  entry (with a badge when a run waits for your approval) and a "Bots" list with a + button; each bot shows a status dot
  (responding, waiting for you, idle). The palette finds bots and automations and has "New automation".
- Automations are built as flows: pick a trigger (manual, schedule, app event, GitHub or deployment watch, local webhook),
  then drag blocks from the library into the flow or add them with +, reorder by drag or keyboard, nest conditions, and set
  each block in the inspector, where the daemon's problems appear on the right field. The Runs tab shows each run block by
  block with links to the code tasks and bot conversations it started. The old form editor is gone.
- Home's widgets sit in one centered column. Drag a widget by its heading (or focus its move handle and use Space and the
  arrow keys) to reorder it, and pick Small, Medium, Large or Full width, optionally Tall, from its menu. Customize has a
  widget gallery and Reset layout. Saved layouts are carried over in their existing order and closest size.

### Fixed
- Text you type in a code task's message box or a bot's chat box is kept when you switch to another screen and come back.
  Each task and each bot keeps its own unsent text until you send it or close the app. In bot chat this now also covers
  a picked @mention or /skill.
- Bots running through the Claude Code or Codex CLI no longer show a long tool call (such as starting a code task) as a raw
  `tool_calls` block. Orole now reads the call's JSON from the block's start, so raw line breaks inside an argument, a
  code fence inside a prompt, one call written as an object, or a reply that stops before the closing fence still run the
  tool. A block that is cut off or not valid JSON stays text.
- Bots running on Claude through the Claude Code CLI no longer show a tool call as raw `<invoke ...>` text. When the model
  writes its own XML call format instead of the tool block Orole asks for, Orole now runs the tool (for example shows the
  question card) and keeps the markup out of the chat.
- A bot's reply to a message from an automation now carries that automation's chain on the "Bot replied" event. A flow that
  starts on a bot reply and messages the same bot can no longer loop; it stops at the chain limit like any other flow.
- Home widget rows always reach the column's right edge: when the sizes in a row add up to less than the full width (a
  Medium next to a Small), the last widget in the row widens to fill it instead of leaving a gap.
- The "Jump to latest" buttons on chat and task pages are opaque again in dark themes instead of see-through.
- Claude Code builds older than `--system-prompt-snapshot` (such as 2.1.250) can start tasks again; the option is passed only
  when the installed CLI lists it, instead of failing with "unknown option".
- A screen whose code fails to load (for example after the dev server re-bundles or restarts) or that crashes now shows an error
  with a Reload button inside that screen, instead of blanking the whole window.

## 0.2.0 - 2026-10-12

### Added
- Right-click a task in the sidebar to rename it, pin it to the top of its project, or delete it. A name you give a task is final
  (automatic naming never replaces it); a running task is stopped before it is deleted.
- Code task conversations have a conversation map: a thin rail of ticks in the right gutter, one per turn. The turn being read is
  solid, turns on screen are darker, hovering shows the turn's title and the start of the reply, and clicking jumps there. It
  appears when the conversation is wide enough to leave a gutter and has more than one turn.
- New Code tasks get a real name. Oh My Pi and Codex name the task themselves (shown as soon as they do), and for Claude Code (and
  Codex until it names one) Orole makes one short request to the task's own Claude or ChatGPT account (Haiku, or Codex's smallest
  listed model) for a 3-6 word title, using the first prompts that say something. Until then the title is the first line of your
  prompt, a title you typed or changed is never replaced, and nothing is sent when the account is not installed and signed in. The
  request is audited without the prompt, and Settings > System > Task permissions > "Name tasks with AI" turns it off (names the
  harness chooses itself still apply).
- Starting a task opens its page instantly. Pressing Send in the Home or New task prompt switches to the task page at once, showing
  your message, the agent logo and a provisional title, and "Starting <agent>…" (or "Preparing workspace…" when a new worktree is
  being created), while the task launches in the background. The real task replaces it when ready; if the start fails, the page
  stays with the error, **Try again** and **Edit prompt** (which restores your prompt, files and choices). Leaving the page does
  not cancel the start, and the task appears in the sidebar as "Starting…".
- Code tasks have three permission modes, chosen before launch and switchable while the task runs: **Manual** asks before
  anything not known to be safe, **Auto** lets the agent decide which actions are safe, and **YOLO** never asks. Auto uses
  Claude Code's own auto mode and Codex's auto-review agent; Oh My Pi has no such mode, so Auto approves file edits there and
  still asks before commands (the picker says so). YOLO needs an explicit confirmation, can be turned off in Settings > System >
  Task permissions, is never available on remote machines, and Orole's MCP tool policy still applies in it. Switching applies
  immediately in Claude Code, from the next turn in Codex, and by resuming the conversation (while idle) in Oh My Pi; terminal and
  remote tasks cannot switch. Existing tasks keep working: stored `default` became Manual and `acceptEdits` became Auto.
- The Code composer is one rounded surface with a single toolbar: attach and permission mode on the left; model, dictation,
  context and send on the right. The folder and branch moved to a quiet line underneath.
- Dictation in the Code composer works in the desktop app. The WebView's Web Speech API had no speech service behind it
  ("Speech recognition could not reach its service"), so Dictate now records the microphone and the daemon transcribes the
  recording with OpenAI (`gpt-4o-mini-transcribe`, falling back to `whisper-1`) using the OpenAI API key you connected under
  Settings > AI providers. The consent text says the audio is sent to OpenAI; without a key, Dictate explains this and opens
  AI providers. Recordings stop at 2 minutes, the text lands in an editable draft and is never sent automatically, the key stays
  in Windows Credential Manager, and neither audio nor transcript is logged or saved. Each transcription is audited without its text.
- Settings > Integrations and AI providers open instantly: the last known connections, accounts, API-key providers and GitHub status
  show at once and refresh in the background, so the loading spinner no longer blocks the page. Listing the Claude, ChatGPT and
  Grok accounts also runs each tool's version, sign-in and update checks in parallel.
- App loading states use the Snake spinner from loading.dev. AI progress glyphs are unchanged.
- Bots can be pinned: "Pin to top" in a bot's menu in the sidebar keeps it above the others, marked with a pin; "Unpin" puts it back.
- Select text in a bot's reply and a small menu appears below it: Shorten, Improve or Explain. The bot gets the selected passage quoted
  (drawn as a quote in your message) with the request.
- Bots can use your Grok account: Grok models appear in the bot model picker once Grok is downloaded and signed in under Settings > AI
  providers > Accounts. Grok's sign-in state is read with `grok models`.
- Bots can use your Claude plan too: Claude models (Opus, Sonnet, Haiku) appear in the bot model picker once the Claude account is signed in.
  Text streams as it is written. Claude Code runs as a plain model with all of its own tools, settings and MCP servers switched off.
- Bots can use your ChatGPT plan: once the ChatGPT account is signed in under Settings > AI providers > Accounts, its models appear in the
  bot model picker. The Codex CLI runs as a plain model in an empty scratch folder; replies arrive whole rather than streaming.
- Settings > AI providers has an Accounts section for Claude, ChatGPT (Codex) and Grok: it shows which are installed, their version,
  whether a newer release exists and who is signed in, with Download, Update, Sign in and Sign out. Orole downloads the
  vendor's own CLI (hash-checked where published) and never sees the credentials. Providers now show their logos.
- Handing work to another bot no longer freezes the bot that handed it off: it says it has started and stays available
  (you can ask it for status), and the other bot's findings come back to it as a "reported back" card it then relays.
- Bots talk like coworkers: the system prompt now asks for short, direct, chat-sized replies, and a reply is drawn as
  several chat bubbles (one per paragraph) instead of one wall of text. Tables and charts stay components.
- Message queue: messages you send while a bot is busy wait in a queue above the composer. Cancel any of them, or pause
  the queue (the bot finishes its current reply, then holds) and resume it. Stop still cancels everything.
- Bot settings: Instructions and Title are editable, laid out as a contact card (Brain, Knows, Does). Memories open
  as a list you can prune; Skills, Tools and Integrations open as pages of their own.
- A reply that is still being produced always shows what the bot is doing: a shimmering line such as "Searching the web",
  "Reading a skill", "Thinking" or "Writing the reply", with the elapsed time after a few seconds. It replaces the bouncing dots, and
  covers the pauses between tool calls where nothing on screen used to move. The Code feed's activity line shimmers too.
- Skill proposals are now cards in the chat: the bot offers a skill, you read it and press Add. Adding saves the skill and
  gives it to that bot in one step, so there is nothing to approve or switch on in the bot settings.
- Built-in `skill-creator` skill for Chat bots: it teaches a bot to interview you, draft a skill and submit it with
  `propose_skill` for your approval. Built-in skills are for bots only and are never synced to coding agents.

### Changed
- CI also runs on pushes to `main` (so the Go, bun and NSIS caches are primed for pull requests) and is
  split into parallel `go`, `web` and `package` jobs; `release.yml` verifies Go and the frontend in
  parallel too. The frontend entry chunk size is checked against `scripts/bundle-budget.json`.
- Git-heavy tests in `internal/{git,agents,daemon,projects}` copy a repository built once per package
  (`internal/testrepo`) instead of running `git init` and `git commit` per test; the isolated ones run in parallel.
- Chat streaming no longer re-sends the whole reply every 50 ms: streamed text travels as small
  `chat.message.delta` events, with a full snapshot at tool boundaries, at least every 2 s and at the end,
  and the daemon builds the text in linear time. A 100 KB reply now publishes about 1 MB instead of 31 MB.
- Long conversations load only the text sent to the model, and the older backlog is summarised a page at a
  time. A failed summary is retried with a growing backoff instead of on every message.
- Sending, regenerating, clearing and delegating no longer hold the chat manager's global lock during
  database work.
- MCP gateway and bot tools no longer rebuild the tool index, re-query permissions or re-derive
  schemas on every call: the index is cached until a server is added, edited, enabled, disabled,
  removed, changes status or changes its tools. `gateway.tools` carries an ETag so the gateway
  process skips unchanged lists, and `mcp.status` is published only when a server's state changes.
- Automations: finished runs and their journal are now pruned (30 days and 500 runs per automation by
  default; `retentionDays` and `retentionMaxRuns` in the automations config, no settings UI yet).
  Queued, running and waiting-approval runs are never removed.
- Automations: run updates, queue scheduling and event dispatch no longer reload runs and definitions
  from SQLite for every step or event; the scheduler uses one timer that event bursts cannot postpone,
  and the webhook secret is read from the credential store once instead of per request.
- Agents: live sessions no longer write to SQLite on every event under the global lock. Hot-path
  changes are debounced per session and published as small `agents.delta` updates, events are
  inserted in batches, `agents.list` is paged, diff statistics use `git diff --numstat` coalesced
  per session, headless agent output lines are bounded and history pruning counts per session.
- Remote machine file operations reuse one SSH/SFTP session per machine (closed when idle, or when
  the machine's access is edited or the machine is removed) instead of reconnecting for every call,
  and remote file etags use size and modification time instead of downloading and hashing the file.
  Reading a remote file no longer downloads it twice.
- Fewer git processes per request: a status or diff now takes 4 to 5 `git` spawns instead of 16, and
  keeps git's stat cache by reviewing a copy of the real index. Ref-to-ref diffs skip the index.
- Git calls on a worktree look its directory up in the database instead of listing every worktree
  with a full status; worktree refreshes only write rows that changed.
- Directory listings in Git files run one status limited to the listed directory, shared between
  concurrent listings and reused for one second.
- `orole-cli` no longer links the daemon, sqlite, sftp/ssh or the MCP SDK: `daemon.Health` and the IPC
  token helpers moved to the leaf package `internal/daemon/api`, and `diagnostics.Collect` takes an
  injected offline schema reader. Offline `doctor` in the CLI reports the DB schema version as
  unavailable when the daemon is not running (the desktop app still reads it). CI guards this with
  `scripts/check-cli-linkage.sh`.
- SQLite: transactions take the write lock up front (`_txlock=immediate`), 4 idle connections,
  `temp_store`/`cache_size`/`journal_size_limit` pragmas, and `PRAGMA optimize` plus a WAL checkpoint on close.
  Migration 0101 adds a partial index on in-flight `messages.status` for startup recovery.
- Code task conversations read like a conversation: each assistant turn collapses into one line (`Working`, then
  `Worked for 2m 13s`) that opens into its thoughts and tool steps, while the final answer, pending approvals and questions,
  errors, files and images stay visible. Tool steps use plain words (`Ran npm test`, `Searched the web`, `Read main.go`,
  `Edited a.ts +12 -3`, `Used docs · search`) instead of raw tool names and JSON; the technical input/output is behind a
  disclosure in the row. Thoughts are no longer separate dashed boxes, and the per-row `Done` label and right-aligned
  timestamps are gone (hover a row for its time; day separators stay). Searching with `Ctrl+F` opens a run that holds a match.
- Tool permission requests are a compact card: what is being asked for in plain words (`Run a command`, `Edit files`,
  `Use <server>`), who is asking, and the full command, folder or file paths it covers, with `Deny` and `Allow once`. The
  review checkbox and the optional reason field are gone because the whole request is visible in the card; the raw JSON
  (control characters escaped) is behind `Details`, and a request without its complete input still cannot be approved.
  A decided request is a single `Allowed once` / `Denied` badge.
- Running image generations show a dot-grid placeholder with a shimmering `Generating` label and the prompt.
- Code task chrome is quieter: the titlebar now contains only the provider logo and task title. Stop, status,
  task-file and canvas-change controls no longer compete with the conversation; files still open from their feed cards.
- Conversation search stays hidden until `Ctrl+F`/`Cmd+F`, then opens as a compact panel in the top-right corner.
- Status bar: the MCP count opens Settings > Integrations (every "open settings" link in the app now opens the settings window), a connected
  daemon shows a green dot, and the host popover is a compact card with the OS logo, architecture, status and memory.
- Model picker: the company filter is a row of chips shown only for All and aggregators like OpenRouter (an account serves only its own
  models), the "Use default" entry is gone (a bot always shows and runs on a named model; new bots start on the default chat model), and the
  instructions box in bot settings stops growing at a maximum height. Choosing a provider shows all of its models, not only the handpicked
  ones (All still shows the handpicked ones, and a search reaches everything); the first row no longer carries a selection border.
- The model picker (bot settings and new bot) starts with a provider chooser: tiles for each connected provider, marked "Your account"
  (Claude, ChatGPT, Grok) or "API key" (OpenRouter, OpenAI), with logos. Models say who serves them ("via Claude") and the picker links to
  Settings > AI providers to connect more.
- The app window no longer shows the browser's right-click menu (except in text fields, for paste) and DevTools are off, including Ctrl+Shift+I
  and F12. Developers set `OROLE_DEVTOOLS=1` to get them back.
- Bot settings: the MCP tools and Skills dropdowns are plain switch lists. Integrations / MCP picks whole servers with their
  company logos (a bot can use every tool of a connected server except the ones you deny in Settings > Integrations);
  existing per-tool selections are migrated to their servers. Skills show only an icon and the name.
- Skills a bot proposes keep the name the bot chose (the bot-name prefix is gone), may have descriptions up to 1024
  characters, and a revised draft replaces the earlier one instead of being refused.
- The "Proposed skills" section of the bot settings panel is gone.
- Add now signs in right away (browser opens at the vendor's consent page). The connector details page is simpler: logo, Reauthenticate/Disconnect, tool chips and the server URL; OAuth client, permission levels and diagnostics moved under a closed Advanced section.
- Integrations and MCP servers are one Settings > Integrations page: company logos, one-click Add for Vercel,
  Cloudflare, Notion, Linear, Stripe, Netlify, Railway, Supabase, Neon, Prisma, Sentry, Zapier, Intercom, PayPal
  and Mixpanel, and a Custom connector button that needs only a name and a server URL.
- Faster startup: the Home view, home widgets, command palette and the settings window's pages now load
  on demand. The entry chunk drops from 929 kB to 592 kB (gzip 266 kB to 163 kB). Widgets show a
  per-widget loading card while their code loads.
- Only the Latin subsets of Inter and JetBrains Mono are bundled.
- Chat: a streaming reply no longer re-renders the whole conversation. The rail's preview of a reply
  updates at most once a second, the open conversation ignores preview and run-flag changes, bots and
  the run state are read once per thread, and composer drafts live in their own store.
- Window focus now triggers one shared refresh, at most every 30 seconds, instead of every store reloading
  on every focus. Chat messages refresh only the open conversation, and projects load their worktrees
  only while a view that shows them is open (cached for a minute), not on every project reload.
- Daemon health polling no longer notifies the UI when the status is unchanged, and the status bar,
  sidebar and shell subscribe to the fields they show, so the 5 second poll does not re-render them.
- Tasks: a streaming agent no longer re-renders the whole task feed. Events are grouped once for the feed and
  the canvas, unchanged messages and turns skip rendering, and conversations over 60 entries (messages and
  assistant turns) render only the entries near the viewport (search, "Load earlier" and following the newest
  message keep working).
- Tasks board: a session update re-renders only its own card, counts are computed in one pass, and only live
  sessions tick their elapsed time (a finished task now shows how long it ran, not time since it started).
- Task changes (diff) in Review and issue workflows refresh at most once every two seconds, never overlap, and
  keep showing the previous diff while refreshing.
- Startup no longer waits for the full task list; the Tasks view and Home activity show their own loading state.
- Home activity calendar no longer rebuilds or reformats its 365 days on hover or on every session update.
- Terminal: faster output decoding, and changing the font size no longer recreates the terminal.

### Removed
- Unused frontend dependencies: `@tanstack/react-query`, `cn`, `@fontsource-variable/dm-sans`,
  `@fontsource-variable/geist`.

### Fixed
- Hovering over a message, a run step or a note in a task conversation no longer pops up a native tooltip with its date. Dates
  stay on the day separators, and the system label on sidebar tasks lost its tooltip too.
- A model stream that goes silent for 90 seconds is cancelled with a clear error instead of hanging.
- Rate-limit (429) and server (5xx) answers are retried with jittered backoff before the reply starts,
  honouring `Retry-After`; a reply that already began is never replayed.
- Terminals: a program that floods its terminal (`cat` of a huge file, a runaway build log) no longer gets
  the desktop's daemon connection closed. Output is coalesced (about 12 ms, 64 KiB per event) and capped
  per session; when the window cannot keep up the oldest undelivered output is skipped and the terminal
  view re-syncs from the daemon's scrollback instead of the connection being dropped.
- Terminals: scrollback is a fixed ring, so a busy terminal no longer allocates and copies ~1 MiB per read,
  and attaching to a terminal no longer stalls its output while the scrollback is copied.
- Terminals: exited sessions no longer count toward the 64-session limit and are forgotten after 30 minutes
  (at most 16 are kept). Agents running in a terminal learn about exit through a notification instead of
  polling every 250 ms.
- IPC: a daemon reply larger than the 4 MiB frame limit now returns a `too_large` error instead of
  making the client drop the link, reconnect and re-send the request (which ran the call twice).
  Oversized events are dropped and logged, and `Client.Call` only retries a request that was never
  written to the connection.
- IPC: when a link is dropped (end of stream, or a frame the client rejects) the client now marks itself
  disconnected before it closes the socket, so nothing that sees the hang-up can still observe a live connection.
- Code projects load quickly again and no longer fail with "Could not load projects: context deadline exceeded". The sidebar
  used to wait for `git status` in every worktree of every project; now projects appear immediately, worktrees fill in per
  project, and one slow or broken repository cannot hide the rest. Worktree ahead/behind and dirty counts are read on the
  Worktrees page only (shown as "…" until ready).
- AI-generated text, errors and URLs can be selected and copied again in Bots and Code. The `selectable` class used in
  98 places was never defined, so only inputs and code blocks were selectable.
- Connecting services with large OAuth token sets (Cloudflare) no longer fails with "credential store: The stub
  received bad data": secrets over Windows Credential Manager's 2560-byte limit are now split across credentials.
- Startup no longer flickers: the window shows on first paint, the loading screen is part of
  `index.html` and appears before the JS bundle runs, and your theme is restored from a cache
  before first paint instead of flashing the default theme first.

## 0.1.0 - 2026-10-10

First tagged release. Windows only.

### Added
- Desktop shell (MyGo + React) in front of a separate headless daemon that owns durable state; the
  two talk over an authenticated per-user named pipe.
- Terminals (ConPTY: PowerShell, pwsh, cmd, Git Bash). Sessions survive closing the window but not
  the daemon exiting or a reboot.
- Projects with git status, diff, log and worktrees; GitHub repositories, issues, pull requests and
  checks through the existing GitHub CLI authentication.
- Local and Git files with an editor, notifications and search; command palette, themes, workspaces
  and layouts, guided first-run setup.
- MCP connections with a global approval queue, declarative MCP plugins, agent runtime with review,
  automations, machines and remote files over SSH, application launcher.
- `orole` command-line tool for the daemon, terminals and settings; optional start at logon.
- NSIS per-user installer and signed auto-update through GitHub releases.

### Known limitations
- Mail and Drive: setup UI is in place, but live authorization is blocked until a registered OAuth
  client ID is configured.
- Chat is not yet exercised against live provider APIs.
- External Windows and Linux SSH hosts are not yet verified.
- The installer and executables are not Authenticode-signed, so Windows SmartScreen may warn.
  Updates are signed with the MyGo update key and verified by the app.
