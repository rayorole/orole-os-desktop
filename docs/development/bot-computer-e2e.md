# Bot computer end-to-end checklist

A manual run on real hardware. Design and behaviour: `docs/architecture/bot-computer.md`. Use a dev data dir
(`.\start.ps1`, or set `OROLE_HOME` to an absolute path) unless you mean to test your real data; never delete
`%LOCALAPPDATA%\Orole`. Start the daemon in the foreground (`go run ./cmd/daemon -stderr -v`) and the app
(`go tool mygo dev`). Note Windows build, CPU and RAM at the top of your run notes.

**Release gate.** Before shipping, publish the draft releases `qemu-v20260811` and `guest-v0.1.3` (until then the
pinned URLs answer 404 and the download fails) and decide how the GPL source offer is made (see "Licences" in the
architecture doc and `docs/research/upstream-attribution.md`).

1. [ ] **Fresh install to ready.** With Windows Hypervisor Platform off, switch **Computer** on in a bot's Tools and
   open the Computer tab: the setup screen explains Windows Hypervisor Platform and the network risk. Click Enable,
   accept the UAC prompt, restart Windows. The tab now offers the download (about 1.6 GB); click Download: it
   downloads, verifies and then shows that the computer is off and starts when the bot uses it. Corrupt a downloaded
   file (or block the network) and confirm the tab shows the error and offers to try again, and that nothing
   half-downloaded remains under `<data>\sandbox\`.
2. [ ] **Real UAC run.** During the Enable step in item 1, confirm the UAC prompt appears **in front** of the Orole
   window (not behind it or only as a flashing taskbar button). Decline it once and confirm the tab reports the failure and
   lets you try again.
3. [ ] **Run a command.** Ask the bot to run `python -c "print(1+1)"`. Chat shows "Starting the computer..." then the
   result `2`; the Computer tab shows the screen and the terminal echoes `[bot] $ python -c "print(1+1)"`, `2` and
   `(exit 0)`. Type `ls` in the terminal yourself and see output.
4. [ ] **Watch a website.** Ask the bot to open `https://example.com`. The page appears on the live screen while the
   bot works. Clicking or typing on the screen does nothing (the screen only takes input while you have taken over).
5. [ ] **Take over and Done.** Ask the bot to log in to a site that needs your password. A "Needs you" notification
   appears with the bot's reason; clicking it opens the bot's Computer tab, which shows the reason. Click Take over,
   log in with mouse and keyboard, then click Done (try a note once). The bot continues with your note and uses the
   logged-in page. While you held control, a bot browser step or command was refused with "the user is controlling
   the computer", and Done is offered only while you hold control.
6. [ ] **Password fields.** Ask the bot to type a password into the login page's password field: it refuses and asks
   you to take over instead.
7. [ ] **Closing the pane mid-takeover keeps control.** Start a bot-requested takeover (item 5), then close the
   Computer pane (or switch to another bot) without pressing Done. The bot keeps waiting and is not told you
   finished; reopening the pane still shows you in control with Done available. Press Done to finish. Then start a
   takeover by hand (no request pending), close the pane and wait about 150 s: control returns to the bot.
8. [ ] **Live screen input only while taken over.** Before Take over, click and type on the screen: nothing happens
   in the guest. After Take over, they work. Hold a key down, press Done, and confirm the key does not stay stuck in
   the guest.
9. [ ] **Persistence.** Quit the app and stop the daemon, start both again, and ask the bot to read a file it wrote in
   item 3 and to reopen the site from item 5: the file is there and you are still logged in.
10. [ ] **Idle stop and cap.** Leave a computer idle with the tab closed for 15 minutes: it stops (Task Manager shows
    no QEMU for it). With the tab open it does not stop. Start computers for five bots: at most four run; an idle one
    is stopped for the fifth, or the fifth waits about 30 s and then reports "all computers are busy".
11. [ ] **Windows versions.** Run items 1-10 on the developer's Windows 11 21H2 PC and, if one is available, on 24H2
    (and on Windows 10 2004 or later if one is available). Record boot time to "running" and the QEMU process's
    memory for each.
