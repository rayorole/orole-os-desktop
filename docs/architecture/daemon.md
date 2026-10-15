# Daemon (`orole-daemon`)

Headless per-user background process. Entry point `cmd/daemon/main.go`; wiring in `internal/daemon`.

## Lifecycle

1. `main` resolves the data dir (`internal/paths`: `OROLE_HOME`, else `%LOCALAPPDATA%\Orole`), opens
   `logs/daemon.log` through `daemon.OpenRotating` (5 MiB per file, keeps `daemon.log.1`..`.5`, a write is never
   split across files) and builds a JSON `slog` logger (`-v` debug, `-stderr` also to stderr).
2. `daemon.New` opens `orole.db` (WAL, foreign keys on, busy timeout 5 s), applies pending migrations, creates a
   random token, the IPC server and the terminal manager, then `registerServices()`.
3. `Run` listens on the endpoint **first**; failing means another daemon owns it (single instance per data dir).
   Only then is `ipc.token` written (0600).
4. Shutdown (`daemon.shutdown` over IPC, or Ctrl-C): close listener, close client connections, terminate all
   terminals, delete `ipc.token`, close the DB.

The desktop app and CLI never start a second daemon: `control.Ensure` first asks the existing one
(`daemon.health`), and only launches `orole-daemon` (detached, no console window) if nothing answers.
`control.FindDaemonExe` looks next to the running executable, then on `PATH`; the packaged app passes the
resource path explicitly.

## Where services live

Each module is `internal/daemon/svc_<name>.go` with `func (d *Daemon) register<Name>()`, called from
`registerServices()` in `services.go`. Methods are typed with `handle[P,R](d, "name", func(ctx, P) (R, error))`.
Sensitive actions record `d.audit(action, target, outcome, detail)` into `audit_events`.

Core methods (`registerCore`):

| Method | Behaviour |
|---|---|
| `daemon.health` | version, pid, start time, uptime, DB schema version, platform, Go version, goroutines, heap MB, terminal count, protocol version |
| `daemon.shutdown` | replies, then stops ~100 ms later |
| `settings.list` / `settings.set` | opaque JSON values by string key (key <= 128 chars, value <= 256 KiB, valid JSON); `set` publishes `settings.changed` |
| `terminals.*` | see `windows-terminals.md`; create is audited and publishes `terminal.changed` |

`registerServices` also wires automations, MCP/policy, declarative plugins, host metrics,
application shortcuts, Mail/Drive adapters, projects/Git, GitHub, machines, files and agents.
Notifications and search are registered through the integration service initializers.
These modules are reachable through authenticated IPC; unavailable configuration is reported
explicitly rather than replaced with mock data. See the module guides and `ipc-protocol.md`.

### Dictation (`svc_dictation.go`, `internal/dictation`, `internal/dictation/engine`)

Speech is recognized **on this computer**: no API key, no network call per recording. The engine is sherpa-onnx's C API
(v1.13.8, ONNX Runtime, CPU, static C runtime) and one of two int8 models the user downloads from Settings > Voice:
NVIDIA Parakeet TDT 0.6B v3 (639.4 MB, finds the language itself, 25 European languages) or Whisper large-v3 turbo
(988.6 MB, about 100 languages, the spoken language can be set). Nothing is downloaded until the user asks.

| Method | Behaviour |
|---|---|
| `dictation.status` | `VoiceStatus`: supported, ready, model in use, language, each model's state/sizes/progress, Whisper's languages, limits |
| `dictation.download` / `.cancel` / `.remove` | `{id}` -> `VoiceStatus`; download runs in the background, cancel keeps the partial files for resuming, remove deletes the model (and the engine once no model is left) |
| `dictation.use` / `.language` | `{id}` / `{language}` -> `VoiceStatus`; stored as JSON under the `dictation` settings key |
| `dictation.prepare` | starts loading the model in use while the user is still speaking; returns at once |
| `dictation.upload` | `{id, part, pcmBase64}`: one in-order part (≤ 1 MiB) of 16 kHz mono 16-bit PCM |
| `dictation.transcribe` / `.discard` | `{id}` -> `{text, model}` / drops an upload |

Event `dictation.changed` carries the `VoiceStatus` on every change (download progress at most about 4 per second).

Downloads: every file is pinned in `internal/dictation/catalog.go` by URL (a Hugging Face repository revision or the
sherpa-onnx GitHub release asset), size and SHA-256, and checked while streaming; a mismatch deletes the file. Files land
in `<DataDir>/voice/downloads/<id>/*.part` (resumed with HTTP `Range` after an interruption) and move to
`voice/models/<id>` once all are verified; the engine DLLs go to `voice/engine/<version>`. Downloads and removals are
audited (`dictation.download`, `dictation.remove`).

Recognition: the webview records with `MediaRecorder` (WebView2 has no Web Speech service), decodes the recording to
16 kHz mono PCM with Web Audio and uploads it in parts (a recording is larger than one 4 MiB IPC frame). The daemon runs
the model in a helper process, `orole-daemon voice-engine`, which loads `sherpa-onnx-c-api.dll` without cgo and refuses
a DLL reporting another version (the C structs it fills are laid out for exactly that build). A native fault ends only the
helper; the model's memory is returned 10 minutes after the last use. Whisper reads 30 s windows, so longer recordings
are split at the quietest point before 28 s. Caps: 120 s per recording (the daemon rejects more than 125 s of PCM), two
recordings in flight, one transcription at a time, 3 minutes per transcription. Neither audio nor transcript is logged,
stored or sent anywhere. Failures are IPC errors coded `dictation_<code>` (`not_ready`, `unsupported`, `unknown`,
`too_long`, `no_audio`, `bad_audio`, `busy`, `canceled`, `timeout`, `engine`, `downloading`) with a user-ready message.
The engine runs on Windows x64 only; elsewhere the Voice page says so. The MyGo WebView2 shell grants the microphone to
the app's own pages by default; Windows can still block it under Settings > Privacy & security > Microphone, which the
UI explains.

### Jarvis (`svc_jarvis.go`, `internal/jarvis`)

Phone calls with bots (`jarvis.md`). The service is built by `registerJarvis`, **after** `registerChatTools` (it runs bot
turns through `d.Chat`). `Daemon.Run` starts it (`Jarvis.Start`: the 20 s ticker and the relay connection) and
shutdown closes it **before** the chat manager and notices, because voice turns run in chat and call endings report
there. A live call is left to the provider; it does not survive a daemon restart. The service also hooks the agent
manager (task finished/failed), the automation engine (run results) and the notifications store (never its own `jarvis`
notices) to evaluate rules, and installs the bot save check that enforces the permission gate. The desktop app and CLI
need not be open for calls.

| Method | Behaviour |
|---|---|
| `jarvis.status` | `StatusView`: settings, which credentials exist (never values), relay connected, verification pending, calls today, month cost, last error |
| `jarvis.secrets.set` | `{twilioSid, twilioToken, elevenLabsKey}`; empty fields unchanged; audited by name only |
| `jarvis.relaySecrets.generate` | creates the relay token and LLM secret, returns them once |
| `jarvis.configure` | saves the settings and provisions ElevenLabs; returns the status and, once, the post-call webhook secret |
| `jarvis.verify.start` / `.confirm` | place the code call to a number / enter the six digits |
| `jarvis.voices` / `jarvis.models` | ElevenLabs voices and TTS models for the pickers |
| `jarvis.rules.list` / `.delete` | `{botId}` / `{id}` |
| `jarvis.calls.transcript` | `{callId}` -> `CallTranscript{call, messages}`: one call and its spoken turns in order (hidden from the chat timeline) |
| `jarvis.call.test` | `{botId}` -> places a test call (caps apply; quiet hours and cool-down do not) |

Event `jarvis.changed` carries the `StatusView` when the relay connects or drops, a call starts or ends, or settings and
rules change. `settings.set` refuses the `jarvis` and `jarvis.state` keys. The bot tools (`jarvis_rule_*`, `call_owner`,
`switch_bot`) go through the same service, scoped to the calling bot.

### Bot computers (`svc_sandbox.go`, `internal/sandbox`)

One local VM per bot (`bot-computer.md`). `registerSandbox` runs before `registerChat`: archiving a bot deletes its
computer. IPC `sandbox.availability`, `sandbox.status`, `sandbox.setup.enable` (one UAC prompt for Windows Hypervisor
Platform, user only) and `sandbox.runtime.install` (background download, user only), and the desktop-only takeover methods `sandbox.takeover`,
`sandbox.release` and `sandbox.handoff` (`computer_tools.go`); events `sandbox.status`, `sandbox.availability`,
`sandbox.runtime`, `sandbox.setup`, `sandbox.handoff`. The Computer tab's desktop-only methods (`svc_sandbox_streams.go`):
`sandbox.viewer{botId,watching}` (a 150 s lease), `sandbox.screen.open|input|close` with the event `sandbox.screen.data`
(guest bytes in chunks; viewer input passes the RFB filter) and `sandbox.pty.open{botId,cols,rows}` (adopts a guest
shell as a daemon terminal). The bot tools (`computer_run`, `browser_*`,
`ask_user_to_take_over`) run through `computerRuntime`; a pending takeover keeps a "Needs you" notification open.
VMs stop on daemon exit; disks survive.

## Operating it

```
orole daemon start | stop | restart | status | logs [-n N] [-f] | diagnostics [-json]
orole daemon install | uninstall      # start at logon via Task Scheduler (see windows-runtime.md)
```

`diagnostics` prints app/Go versions, data dir, pipe name, daemon exe, daemon health, DB schema version
(from the daemon, else read-only from `orole.db` in the desktop app; the CLI reports it unavailable), whether the credential store round-trips, the WebView2
runtime version, autostart state and the last 20 log lines. The same report backs Settings > Diagnostics.

`orole-cli` is a thin IPC client: it must not link the daemon, sqlite, sftp/ssh or the MCP SDK. Wire types it needs
(`Health`, token path) live in the leaf package `internal/daemon/api`; `scripts/check-cli-linkage.sh` enforces this in CI.

## Logging

JSON lines in `logs/daemon.log`. Secrets are never logged (`AGENTS.md`); credential store values are not read
by diagnostics beyond a throwaway write/read/delete probe key.
