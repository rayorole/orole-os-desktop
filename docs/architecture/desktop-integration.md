# Desktop integration and application launcher

## Ownership and security

The Applications module combines built Orole modules from the existing frontend `NAV` + `views` registries with explicitly configured native application shortcuts. It does not scan installed applications, synthesize example entries, or claim unbuilt modules work.

Shortcuts are persisted in daemon-owned SQLite (`0080_applications.sql`, `application_shortcuts`). The bound `app.Applications` service is a stateless authenticated-IPC bridge. Saving, editing, and removing shortcuts does not execute anything. Updates use optimistic revisions so a stale editor cannot silently replace a newer configuration.

A launch request contains only the persisted shortcut ID. The daemon loads its configuration and requires Go policy authorization for `applications.launch`, actor `user`, resource `applications/<id>/revision/<revision>`, effect `destructive`. Native executable execution is not a safe/read-only operation. The policy engine controls whether a configured rule denies/allows the request or presents an approval; the default is an approval, with no remember checkbox for destructive actions. This feature does **not** register an agent or automation launch capability. Agent process/session execution has its own Go authorization in the agent manager and must not route through this user-only launcher entry point.

After approval, the manager rechecks the shortcut revision, existence, executable, working directory, and request cancellation. An edit or deletion during approval invalidates that launch. A lock serializes that final check/start with shortcut mutations, without holding a lock while the user decides. Policy decisions and launch outcomes are audited. A successful response means the OS created a process, not that the application finished initialization or completed a task.

Executable boundaries on Windows:

- An existing absolute local `.exe` file is required. No `PATH` search, UNC paths, device namespaces, alternate data streams, `.lnk` files, batch files, or PowerShell scripts.
- Paths are canonicalized at save time; changing a canonical path into a different symlink target invalidates the launch. This is not a binary-signature or file-content attestation mechanism. An executable installed at the same path can change between launches.
- Arguments are an explicit bounded string array, passed directly through `exec.Command`, never parsed by a shell. A user can intentionally configure a shell executable; that remains arbitrary native execution and requires the same authorization. The launcher is not a sandbox.
- Working directory is an optional existing absolute local folder; the default is the executable's folder.
- Name, arguments, and paths reject control characters. The complete command description must fit inside the policy approval detail limit; hidden truncated argument suffixes cannot be authorized.
- There are no credential or environment-override fields. No secret is fetched from Credential Manager or sent to the frontend. Shortcut arguments are ordinary plaintext metadata: never put passwords, API keys, or tokens in them. Use the launched application's own secure sign-in instead.
- Child processes are independent of the launch request and desktop lifetime. The daemon reaps process completion but does not present this launcher as a process supervisor, and quitting the desktop does not kill launched applications.

## Real framework APIs

Implementation research read both the local `../_upstream/mygo` source and the pinned `github.com/egoist/mygo@v0.3.3` module. Relevant sources are `app.go`, `modules.go` (including `GlobalShortcut.Register`/`Unregister`), `menu.go`, `window.go`, `deeplink.go`, `single_instance.go`, and `docs/app.md`/`docs/native.md`. No tray, menu, system-wide shortcut, scheme-registration, or instance-forwarding framework was copied or reimplemented.

`cmd/desktop/native.go` uses MyGo's `NewTray`, `NewMenu`, `App.SetMenu`, `Window.SetAutoHideMenuBar`, `GlobalShortcut.Register`, `GlobalShortcut.Unregister`, `OnClose`, `OnBeforeQuit`, `OnWillQuit`, `RequestSingleInstanceLock`, `OnSecondInstance`, `RegisterURLScheme`, and `OnOpenURL` APIs.

- The tray has Show, Hide, Tasks, and Quit actions. Show also restores a minimized window and focuses it.
- Closing the main window hides it while a tray exists. Quit marks the GUI as quitting before window-close interception, then destroys the tray while the event loop is alive. No daemon shutdown or start-at-login setting changes occur.
- If tray creation fails, Hide remains disabled and closing the window uses the normal application shutdown behavior. The error is logged; there is no fake tray state.
- The application menu is auto-hidden to preserve the existing title bar, accessible with Alt/F10. Shortcuts: Tasks `Ctrl+Shift+L`, Home `Ctrl+Shift+1`, Settings `Ctrl+,`, Hide `Ctrl+Shift+H`, Quit `Ctrl+Q`. MyGo supplies normal Edit menu roles. These are application-menu shortcuts, not registered system-wide hotkeys.
- Separately, `Ctrl+Alt+Space` is registered **system-wide** after the app is ready and the main window exists. It shows/restores/focuses Orole and navigates to Home, including when Orole is hidden to the tray or another application is focused. It uses the same navigation event/snapshot path as menu actions and deep links. `Ctrl+K` is not captured globally.
- The Orole OS menu's Open Home action displays the system-wide shortcut when registration succeeds. If registration fails (for example, another app owns the keys), it instead displays “system-wide shortcut unavailable” and logs the error. The menu action remains usable, startup continues, and tray/menu navigation still works. This label is not a menu accelerator or a claim of a second registration.
- Quit unregisters `Ctrl+Alt+Space` in `OnWillQuit`, while the native event loop is alive; hiding the GUI does not unregister it. Binding generation bypasses native registration.
- There are no native (OS toast) notifications. The in-app notification center and its sounds are the default delivery; ntfy push (below) is an opt-in addition.

### Push notifications (ntfy)

[ntfy](https://ntfy.sh) is a small HTTP pub-sub service with apps for Windows, Android and iOS. Orole can publish each notification to a topic there, so it reaches the ntfy app on the PC or, later, a phone. It is off by default and independent of the in-app mailbox and sound.

- **Settings** (Settings > Alerts): a Push toggle per kind next to In app and Sound (`notifications.prefs`, field `push`, default off); a master switch; the server URL (default `https://ntfy.sh`); the topic, which can be copied and regenerated; an optional access token; and Send test, which sends one sample push with the saved configuration and reports success or the error. The master switch and server URL are the `notifications.ntfy` setting (seeded off by migration 0114). The topic (`orole-` plus 32 random URL-safe characters, created the first time push is switched on) and the token are secrets and live only in Credential Manager (`notifications/ntfy/topic`, `notifications/ntfy/token`); the window never receives the token.
- **Sending** (`notifications.Ntfy`, daemon): when a notification is created and its kind's Push toggle is on (also when the kind is hidden in app), the daemon POSTs a JSON body to the server root with `topic`, `title`, `message`, `priority` (4 for Needs you and Failed, 3 otherwise), one `tags` entry per kind and, for tasks and flows, a `click` link to the Tasks or Automations view (`orole://open/agents`, `orole://open/automations`; the deep-link parser accepts views, not single items, so bots and computers get no link). JSON rather than headers keeps UTF-8 titles intact. A token is sent as `Authorization: Bearer <token>`. Delivery runs in the background with a 10 s timeout, at most 8 at once, does not follow redirects, and never delays or fails the notification; failures are logged without the topic or token. Messages are cut to 4000 bytes, under ntfy's limit of about 4 KB.
- **Privacy.** On ntfy.sh, messages pass through their server and are cached for about 12 hours, and anyone who knows the topic can read and post to it: treat the topic like a password. Notification text can come from mail, issues and bot replies and is sent as it is. For local-only use, run your own server with `ntfy serve` and point Server at it. Plain `http://` is accepted for such a server; a token sent that way is not encrypted.

### Deep links

The accepted syntax is `orole://open/<destination>`, with an exact built-in allowlist in `applications.InternalView`. Top-level destinations are `home`, `agents` (Tasks), `review`, `automations`, and `settings`. Skills and MCP use `orole://open/settings/skills` and `orole://open/settings/mcp`, opening their Settings sections. The former top-level `skills` and `mcp` destinations, and removed modules (images, mail, drive, github, projects, files, terminals, browser, machines, applications, worktrees), are rejected. Examples: `orole://open/agents`, `orole://open/settings/skills`, `orole://open/settings`.

Queries, fragments, encoded aliases, credentials, ports, external schemes/hosts, executable IDs, arbitrary paths, and unsupported modules are rejected. A deep link can only request internal navigation. It cannot launch a configured shortcut, access a file, run a command, change settings, or submit an agent prompt. Rejected URL contents are not logged.

MyGo receives both cold-start launch arguments and forwarded second-instance arguments. A GUI-lifetime controller stores the latest navigation request and sequence, and emits the typed `desktop:navigate` event. The React shell subscribes before calling `Desktop.ready()`, ignoring an older snapshot if a newer event arrived. This handles React startup/reload without evaluating JavaScript or navigating the WebView to untrusted URLs. The pending navigation is ephemeral native-shell state, not daemon/business state.

`mygo.json` already declares `urlSchemes: ["orole"]`. Production builds also register the scheme through MyGo's current-user API. Development runs deliberately do not replace the installed app's OS scheme association. Explicit launch arguments still work in a MyGo development bundle using its declared scheme metadata. Binding generation bypasses native setup entirely, so it cannot register a temporary generator executable as a URL handler.

### Framework limitations, not fabricated support

- MyGo v0.3.3's single-instance transport uses a local Unix-domain socket on Windows as well as Unix, not a TCP/network-interface listener. This is framework-owned GUI activation, separate from Orole's authenticated named-pipe daemon IPC. Incoming activation data is still untrusted and passes the strict parser.
- The framework lock is best-effort: if its socket cannot be created or it exhausts retries, its implementation returns true and permits another instance. This implementation does not claim a stronger lock or replace it with a hand-rolled service.
- Native tray availability is platform/framework dependent. Linux requires AppIndicator support; click behavior differs by platform. This is a Windows-first feature, not a promise of identical tray behavior everywhere.
- System-wide shortcut registration depends on the desktop. MyGo uses native registration on Windows and key grabs on X11. Wayland uses the XDG desktop portal, may ask for permission or let the user remap the keys, and requires an installed desktop entry. Portal activation may finish after `Register` returns; a successful return alone does not prove live activation on every desktop.
- Programmatically registering a portable production executable makes that path the current-user URL handler. Moving/deleting that executable can leave a stale OS association. Installed builds should use the installer/uninstaller lifecycle; registration success is not proof that a user's OS/browser will permit opening a link.
- Native UI, installer routing, and OS process creation require Windows runtime acceptance. Source-level implementation and added regression tests are not a claim that this manual acceptance has already run.

## Integration

`internal/daemon/services.go` registers applications after MCP initializes the policy engine.
`internal/app/bridge.go` subscribes to application changes, forwards them through the generated
typed event, and gives native launch requests the same approval-aware timeout as agent/MCP requests.
`cmd/desktop/main.go` prepares the native lifecycle before binding services, binds Applications and
Desktop, and attaches the lifecycle controller to the main window. `NativeNavigation` is mounted
once beside the global policy approval surface; Applications is a lazy registered feature.
`frontend/src/mygo.ts` contains the generated service/event contracts.

## Verification

Behavior regressions cover persistence/revisions, stale edits, denial and absent policy preventing
execution, edits/deletes during approval, cancellation, executable boundaries, deep-link allowlisting,
and navigation snapshot/event races. The combined Go run passed `internal/applications` under the
race detector; the frontend unit suite passed the native-navigation regressions.

On 2026-10-08, a real isolated daemon was reached over authenticated named-pipe IPC. A configured
PowerShell shortcut was denied first: no output file was created. A second launch was explicitly
approved through the policy queue and the native process wrote the expected proof file. This verifies
daemon-side persistence/authorization/process creation, not the interactive launcher editor.
The NSIS-installed MyGo/WebView2 application also launched and connected to its daemon.

Additional installed-desktop smoke exercised the native Settings and Applications
menu accelerators, Hide to tray, second-instance activation, and minimized-window
restoration. The second instance exited with code 0; the original desktop PID and
daemon PID remained unchanged. Window visibility/minimized state was observed
through Win32 APIs, and native navigation was observed in the actual WebView.

The final isolated installed-desktop smoke also exercised the launcher editor:
save, deny launch without a file effect, approve launch with an actual PowerShell
file write, rename, reload persistence, and confirmed removal.

Native Ctrl+Q exited only the GUI. The daemon retained PID 34640 and its terminal;
launching the rebuilt installer through the OS `orole://open/terminals` association
restored scrollback and the same PowerShell PID 2340. A variable set before quit
was still present, and another command ran afterward. A warm
`orole://open/applications` link navigated the existing window. A link containing
`?command=whoami` left that view unchanged; deep links do not execute commands.
These observations do not claim process survival after daemon exit or reboot.

Tray-menu mouse selection of Show/Hide/Quit remains unverified: Windows UI
Automation exposed the app window but neither an Orole tray button nor a hidden-icon
control. Native menu shortcuts, hide/restore behavior and GUI-only quit were exercised.
Use isolated data for OS-handler acceptance; do not drive a window receiving other user input.

### System-wide shortcut acceptance

The new system-wide shortcut requires a fresh native build; earlier menu-shortcut evidence above does not verify it. Parent/native acceptance should:

1. Start the desktop, focus another application, and press `Ctrl+Alt+Space`. Confirm Orole comes forward on Home, not merely the previously selected module.
2. Repeat with Orole minimized and hidden to the tray. Confirm restoration, focus, and Home navigation; verify `Ctrl+K` remains available to the other application.
3. Quit Orole via its menu. Confirm the shortcut no longer activates Orole and a subsequent launch can register it again.
4. In an isolated session, reserve `Ctrl+Alt+Space` in another app before starting Orole. Confirm startup/navigation still work, the Open Home menu action says “system-wide shortcut unavailable”, and the registration error is logged. Release the competing registration and restart Orole to restore the shortcut.

Do not claim conflict, background activation, or quit-time release verified from a build or application-menu smoke alone.

Installed acceptance attempt on 2026-10-08 found `Ctrl+Alt+Space` already reserved:
Win32 `RegisterHotKey` failed **before Orole was launched**, with no Orole GUI running.
Synthetic activation therefore did not restore the minimized app. The smoke did not
unregister or replace the other application's shortcut. Background activation,
Home navigation and release/re-registration remain blocked on an available shortcut;
tray-menu mouse selection remains unverified as described above.
