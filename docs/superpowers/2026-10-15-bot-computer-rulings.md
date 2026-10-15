# Bot computer: decisions made during implementation

Every ruling the coordinator made while the owner was away, in order, with what it costs if wrong. Source: the per-phase SDD ledgers.

## phase1-core

- Ruling: M1 Task 5 echoPTY.Write must return len(b) after writing the echo — plan test code hangs otherwise — cost if wrong: none, test-only.
- Ruling: M2 Task 10 write the architecture doc's own fences correctly (plan's outer fence is just presentation) — cost: none.
- Ruling: M3 every task runs gofmt on its files and commits the formatted result; Task 9/11 "gofmt changes nothing" checks then hold — cost: none.
- Ruling: M4 phase 2 plan committed (b/c it finished) so Task 11's empty git status holds; Task 11 ignores untracked .superpowers — cost: none.
- Ruling: implementers use model sonnet where plan code is complete; reviews of concurrency tasks (4,5,7,8,9) use opus — user asked for sonnet where deep understanding not needed — cost: possible extra fix rounds.
- Ruling: Task 7 dispatch must ensure the manager bounds calls when a guest hangs with the channel open (transport ReadIdleTimeout/PingTimeout or per-call deadline) — Task 4 review ⚠️ — cost if wrong: hung bot turns until ctx ends
- Ruling: phase 3 must include test 'step in flight, TakeOver, step finishes, next browser call ErrTakenOver, Release, call succeeds' — Task 8 ⚠️ — cost: Review Focus item untested
- Ruling: fold Task 9 minors 1-3 (flaky busy test window, unbounded wait, overlay path must lie under Paths.Root before remove) into Task 10 dispatch — CI lane will run these tests; flake would block — cost: small scope growth
- Ruling: open streams (pty/screen) do not count as busy for idle/eviction; bytes moving on a stream count as activity (Touch) — spec defines idle as no bot activity and no viewer — cost: a silent open terminal lets the VM stop after 15 min (terminal ends)
- Ruling: image upgrades (final review Important #2) handled in phase 2: boot with the base image of the recorded version while it exists; if the pinned version changed, keep old base until the overlay is recreated; spec says recreate after telling the user — phase 2 records it — cost: disk for two base images
- Ruling: alive() fix via http2.Transport StrictMaxConcurrentStreams: true — cost: none

## phase2-windows

- Ruling: M1 socket tests update TestPaths in internal/sandbox/sandbox_test.go (no paths_test.go) — cost: none.
- Ruling: M2 token reading stays cross-platform: keep readToken()/tokenFromCmdline + main_test; add fw_cfg reader in a linux-tagged helper with a non-linux stub so Windows vet/test pass — cost: none.
- Ruling: M3 fw_cfg test uses an injected temp path with writable perms, no reliance on rewriting 0o444 — cost: none.
- Ruling: M4 Task 13 edits the existing bot-computer.md lines (token via fw_cfg, hashed socket name), not only appends — cost: none.
- Ruling: M5 branch already exists and is checked out; skip git switch -c — cost: none.
- Ruling: M6 WHvCreatePartition succeeds with Hyper-V on although HypervisorPlatform feature shows disabled: Availability treats a successful partition probe as ready regardless of feature state; feature state only explains a failure. No DISM/reboot needed on this PC unless QEMU WHPX boot fails — cost: wrong reason text on unusual systems.
- Ruling: M7/U2/U4 no local 7-Zip or Nix installs (user away; installs outside worktree): QEMU zip and guest image are built in GitHub Actions (windows runner has 7z; nix action on ubuntu). Order: Task 9 (qemu workflow) + Task 10 (guest workflow) early, push branch + tags qemu-v20260811 / guest-v0.1.0 (user allowed pushing for CI), download draft assets with gh for the local spike and real-boot test — cost: slower iteration via CI.
- Ruling: U6 releases stay DRAFT; publishing is the user's call (public). DefaultPins point at the release asset URLs with hashes from SHA256SUMS; in-app download works once the user publishes — cost: app download 404 until published.
- Ruling: U3 manual UAC/DISM test skipped (would change Windows features); documented in e2e checklist for the user — cost: elevated helper untested on real UAC.
- Ruling: U8 spike gates: if AF_UNIX chardev fails, use QEMU pipe chardev with a pre-created user-only pipe or verified DACL; do not stop — cost: design rework later if wrong.
- Ruling: M8 every task gofmts and commits formatted code — cost: none.
- Ruling: execution order 9,10,(push tags),2,3,4,5,1(spike with CI assets),6,7,8,11,12,13,14 — tasks are mostly independent; spike needs QEMU binary — cost: spike findings may force rework of 3/6.
- Ruling: guest token passed via -fw_cfg file=<user-only file deleted after QEMU start>, never argv — global constraint beats plan text — cost: Task 6 must manage the file lifetime
- Ruling: defaultAccel = "whpx,kernel-irqchip=off"; tcg detection by prefix "tcg" (Task 6 dispatch) — spike evidence — cost: none.
- Ruling: backend sets an explicit user-only DACL (current user SID + SYSTEM) on <data>\sandbox\run\ and token files, because %LOCALAPPDATA%\Orole inherits read for local group CodexSandboxUsers — global constraint user-only channel — cost: none.
- Ruling: guest image must load qemu_fw_cfg (built-in or in initrd/early modules) before orole-guest reads the token, and must block guest→10.0.2.2 with nftables (Task 8) — cost: none.
- Ruling: per-user install dir is user-writable, so EnableWHP must verify the helper's SHA-256 against a value embedded in the daemon at build time (-ldflags -X from build.ps1) before runas; path always SetupHelperPath(os.Executable()), never from IPC — Task 12 — cost: build script complexity
- Ruling: Task 11 — releases stay DRAFT (user publishes); pins.go gets hashes from the draft SHA256SUMS and the final release asset URLs; real-boot test uses gh-downloaded draft assets; fold in deferred minors (staging sweep, idle HTTP timeout, helper start failure 500, helper stderr cap, ubuntu-24.04 pin, errDeleting audit) — cost: in-app download 404 until published
- Ruling: rebuild guest image as 0.1.1 (draft) and re-pin now; publishing + GPL offer decision = owner release gate, stated in PR — cost: none
- Ruling: UAC foreground handoff (desktop calls AllowSetForegroundWindow(daemon PID) before sandbox.setup.enable) is implemented in phase 4 where the setup screen lives; manual UAC check goes into phase 4's e2e checklist — cost: setup prompt may hide until phase 4

## phase3-tools

- Ruling: M1 /browser and /screen mount via guestagent.Config hooks (e.g. Config.Browser http.Handler / browserPage, Config.Screen dialer) replacing the notAvailable stubs when set; cmd/orole-guest wires them; 501 test keeps asserting the unset case — cost: none.
- Ruling: M2 screen handler follows handlePTY structure (handler waits for its copy goroutines before returning) — race-free — cost: none.
- Ruling: M3 all text derived from guest responses/errors is returned with Untrusted:true; a 501 from the guest maps to a clear "this computer runs an older image without browser support" message — cost: none.
- Ruling: M4 every host→guest call gets a deadline: exec = clamp(timeout)+30s, files 60s, browser 90s, Ensure BootTimeout+BusyWait+10s — cost: none.
- Ruling: M5 computer tools refuse archived bots before Ensure (daemon adapter checks the chat store) — cost: none.
- Ruling: M6 appNotices.reconcile drops computer-takeover entries on start (handoffs do not survive a daemon restart) — cost: none.
- Ruling: M7 add Task 5b after Task 5: bump guest/VERSION to 0.1.2, update vendorHash (CI prints it), build via guest-image workflow (draft), re-pin with scripts/sandbox-pins.ps1, run real-boot e2e incl. browser/screen. Existing 0.1.1 overlays keep 0.1.1 (dev only; no published users) — cost: dev bots need delete to get browser.
- Ruling: M8 branch exists; skip checkout -b.
- Ruling: M9 browser_read/browser_screenshot/computer_read_file are exempt from the identical-call doom-loop guard (state changes between calls); when Computer is on, default Limits rise to 30 steps / 60 tool calls unless the bot sets its own — cost: longer runaway turns possible (still capped).
- Ruling: M10 browser_read caps page text at 90 KiB (under the engine's 100 KiB clip) with the truncation marker — cost: none.
- Ruling: M11 computer card part sets Text (human summary) as well as Summary; statuses running|done|failed|waiting are the contract; phase 4 renders all four — cost: none.
- Ruling: M12 single echo path: phase 4's Guest.OnExec/Inject; phase 3 does NOT add sandbox.activity; phase 3 does add sandbox.handoff (takeover UI needs it) — cost: none.
- Ruling: M13 phase 4 must edit (not recreate) targets.ts / targets.test.ts created here — note for phase 4.
- Ruling: M14 gofmt every file.
- Ruling: M15 Task 12 also fixes bot-computer.md lines 11,19,79,83,194 and daemon.md:106-111 (new IPC + events).
- Ruling: fold Task 3 minors into Task 4 (create per-request timeout after acquiring mu; reject URL userinfo; add data:/uppercase/empty-host/opaque/control-char URL test cases; text= prefix case rule consistent)
- Ruling: Task 10 must (a) end a pending handoff when the bot's VM stops/crashes (Finish/cancel from status subscription), (b) Release the takeover lock when a handoff ends by hold timeout or turn cancel, (c) decide takenOver under the same lock as sandbox.takeover/release to avoid the snapshot race; Task 9 fold-ins: Ask returns ctx.Err() before registering if ctx done; re-check done before returning a timeout — from Task 6 review — cost: none
- Ruling: Tasks 8-9 briefs predate Task 7: use 5-arg emitComputer(rc,name,status,text,summary), chat.Computer* status constants (failed not error), guestCall(ctx, 90s) for browser — carry in dispatch
- Ruling: Task 10 acceptance — adapter bounds Ensure itself and applies context.WithTimeout(ctx, GuestCallTimeout(ctx)) to the guest call after boot
- Ruling: browser_open becomes action computer.browser.open, EffectWrite, resource computer/<botID>/browser/<host> (per-host 'always allow' rules possible); read/screenshot stay EffectRead on computer/<botID>/browser; browser_open refuses literal private/loopback/link-local/CGNAT IPs and localhost (DNS-to-private not checkable host-side; LAN reachability remains documented); export policy.MaxDetailBytes and use it; shared takeover message says 'the computer'; Detail quotes the target — security over plan text (spec silent on effect detail beyond table) — cost: more approval prompts for ask/read bots; spec table deviation recorded
- Ruling: fix wave — computer_run/computer_write_file refused while taken over; guest nftables blocks uid bot from 127.0.0.1/::1 tcp 9222 and 5900 (orole-guest runs as root); browser_type refuses password inputs; zoned IPv6 + trailing dots; attach deadline; repeated-call guard counts consecutive repeats only for computer/browser tools; docs: write-then-run limitation, cookie files readable via computer_run, per-site check scope; changelog honest + setup caveat; image 0.1.3 + pins + live gate — cost: one more CI/image cycle. Hold-timeout releasing lock kept (ruling 5b).
- Ruling: running Chromium/cage/wayvnc as a separate user (closing the same-uid gap: Wayland socket, process, profile files) is a follow-up issue, documented in threat model — cost: bot commands can still read browser profile and interfere with the session

## phase4-ui

- Ruling: M1/G2 setup and runtime install are async: app service returns immediately; daemon events sandbox.runtime, sandbox.setup, sandbox.availability, sandbox.handoff forwarded to the UI (bridge subscription); setup screen shows combined download progress (sum of Done/Total across files computed in the UI or a daemon-side aggregate) and errors from the events — cost: none.
- Ruling: M2 OpenScreen/OpenPTY opening gets a bounded ctx (e.g. 30 s) — no WithoutCancel on the open; the stream lifetime is separate — cost: none.
- Ruling: M3 do not add a second sandbox.status publish; reuse the existing m.Subscribe path — cost: none.
- Ruling: M4/M5 fix formatExecEcho: strip C1 controls incl. raw 0x9b; apply the cap after CRLF expansion — cost: none.
- Ruling: M6/M7 real anchors (newGuestCtx in Manager.start), wire import, gofmt — cost: none.
- Ruling: M8 edit targets.ts/test (extend parseTarget; computer:<botId> opens the Computer pane) — cost: none.
- Ruling: M9/M10/M11/M12 docs: edit existing threat-model section, existing CHANGELOG Added entry (replace "next update" text), fix stale "phase 4" lines and daemon.md IPC list; minimum Windows per availability.go (build 19041+, Windows 10 2004+ / 11; WHP or Hyper-V); guest-to-host loopback IS blocked (nftables), home LAN reachable — cost: none.
- Ruling: M13 setup copy derived from daemon Availability reason+detail (UI shows detail text), not hardcoded Windows 11/BIOS strings — cost: none.
- Ruling: M14 plan contract issues 3/9 superseded: statuses running|done|failed|waiting; release takes note.
- Ruling: M15 change "reset the computer in the Computer tab" texts (computer.go:164, docs) to "ask the user to delete and recreate the bot's computer" OR add a Reset action? No reset UI (user said keep UI simple) → reword to: the computer runs an older image; it updates when the bot's computer is recreated (archiving the bot deletes it). — cost: no in-app fix path for old images.
- Ruling: G1 desktop app calls AllowSetForegroundWindow(daemon PID from sandbox.status/daemon health) right before sandbox.setup.enable (internal/app, windows-only file) — cost: none.
- Ruling: G3 Computer tab shows the pending handoff reason (sandbox.handoff) with Take over; G4 Done has an optional note field (≤500).
- Ruling: G5 when the viewer lease expires while taken over and no handoff is pending, the daemon releases the takeover lock — cost: user loses control if the window is hidden >150 s (acceptable; re-take over).
- Ruling: G6 add a vitest that drives noVNC RFB with the channel adapter against a scripted RFB server transcript if feasible, and a manual checklist item; verify @novnc/novnc version exists in the registry before pinning.
- Ruling: G7 docs/PR mention publishing drafts qemu-v20260811 + guest-v0.1.3 as a release gate.
- Ruling: G8 add daemon tests for screen open/close, pty.open reuse, exec echo, stop-closes-screens.
- Ruling: fold Task 2 minors into Task 3 (QEMU up canonical bytes; cap held-key maps 256; reset extClip per SetEncodings; tests); Task 3 must call filter.Release() on takeover end and on stream close while allowed, on the Feed goroutine between messages
- Ruling: Task 5 must never call ScreenHub Close/CloseBot/CloseAll while holding Manager.mu (lock order inMu → Manager.mu)
- Ruling: Task 5 — export errScreenStalled (ErrScreenStalled) for IPC mapping; never add a status subscriber that calls ViewerLeases.Watch/Unwatch (ViewerLeases holds its lock across OnExpire→Release→publish); TakeoverEnded may be wired from Release paths (spawns goroutines)
- Ruling: Task 7 ScreenChannel serializes screenInput (promise chain; each send awaits the previous) — RFB stream must stay ordered
- Ruling: fold Task 8 minors into Task 9 (refresh race guard via request counter/cancel on botId change; drop mock-only ?.catch guards; clear the other action's error when starting enable/install; tests)
- Ruling: pane unmount never releases; user-initiated takeovers released by viewer-lease expiry, bot-requested ones by handoff hold timeout — matches daemon ReleaseAbandonedTakeover — cost: a user-started takeover lingers up to ~150 s after leaving the pane
- Ruling: Unwatch keeps a generation-checked TTL abandonment timer that runs only onExpire (cancelled by Watch); screen pump gets a per-screen byte budget (token bucket) closing the screen with a reason; docs fixed (45 s renewal, stray 0x08, echo/terminal untrusted, flood residual, fake-server-on-5900 residual); minors 5,6,8,9 + cols/rows normalization + 3 s health timeout fixed. Ruling: per-file download progress (step + current file %) accepted instead of combined (daemon resets per file; no pinned total exposed) — cost: progress bar restarts per file
