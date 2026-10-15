package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

// sandboxViewerTTL is how long a Computer tab's viewer lease lasts without renewal (the UI renews every 45 s). A
// variable for tests.
var sandboxViewerTTL = 150 * time.Second

const (
	// screenInputMax bounds one sandbox.screen.input call before it reaches the screen (which takes at most
	// sandbox.ScreenChunkMax and closes on more).
	screenInputMax = 1 << 20
	// ptyOpenTimeout bounds opening the guest terminal stream; the open terminal lives until it ends.
	ptyOpenTimeout = 30 * time.Second
	// execEchoMax bounds the output echoed for one command, counted after newlines became CRLF.
	execEchoMax = 64 << 10
	// execEchoCommandMax bounds the echoed command line.
	execEchoCommandMax = 1 << 10
)

// screenRef names one open screen (sandbox.screen.open's answer, sandbox.screen.close's request).
type screenRef struct {
	ID string `json:"id"`
}

type sandboxViewerParams struct {
	BotID    string `json:"botId"`
	Watching bool   `json:"watching"`
}

type screenInputParams struct {
	ID   string `json:"id"`
	Data []byte `json:"data"` // base64 in JSON
}

type sandboxPTYParams struct {
	BotID string `json:"botId"`
	Cols  int    `json:"cols"`
	Rows  int    `json:"rows"`
}

// registerSandboxStreams wires what the Computer tab needs on top of registerSandbox: the viewer lease, the live
// screen and the guest terminal. registerSandbox calls it last. Desktop user only; the UI never starts a computer
// (screen.open and pty.open answer not_found unless it already runs).
//
// IPC: sandbox.viewer{botId, watching}, sandbox.screen.open{botId} -> {id}, sandbox.screen.input{id, data},
// sandbox.screen.close{id}, sandbox.pty.open{botId, cols, rows} -> terminals.Info (owner "sandbox:<botId>"; one per
// bot, reused). Events: sandbox.screen.data (sandbox.ScreenChunk), terminal.changed for the adopted terminal.
func (d *Daemon) registerSandboxStreams() {
	d.attachSandboxStreams(d.Sandbox)
	handle(d, "sandbox.viewer", func(ctx context.Context, p sandboxViewerParams) (none, error) {
		return none{}, d.sandboxViewer(ctx, p)
	})
	handle(d, "sandbox.screen.open", d.openSandboxScreen)
	handle(d, "sandbox.screen.input", func(ctx context.Context, p screenInputParams) (none, error) {
		return none{}, d.sandboxScreenInput(ctx, p)
	})
	handle(d, "sandbox.screen.close", func(ctx context.Context, p screenRef) (none, error) {
		return none{}, d.closeSandboxScreen(ctx, p)
	})
	handle(d, "sandbox.pty.open", d.openSandboxPTY)
}

// attachSandboxStreams creates the screen hub and viewer leases over m. sandbox.status itself is published by
// registerSandbox's subscriber.
//
// Lock order: ViewerLeases calls SetViewer and the expiry callback under its lock (lease -> takeoverMu ->
// Manager.mu); Manager publishes statuses without its lock, so the subscriber below may end screens, which waits
// on the guest at most the hub's write timeout. Nothing here calls ViewerLeases from a status subscriber.
func (d *Daemon) attachSandboxStreams(m *sandbox.Manager) {
	screens := sandbox.NewScreenHub(func(c sandbox.ScreenChunk) { d.IPC.Publish("sandbox.screen.data", c) }, m.TakenOver)
	viewers := sandbox.NewViewerLeases(m.SetViewer, sandboxViewerTTL)
	// A Computer tab that went away without saying so (the GUI crashed) must not keep the bot locked out: once its
	// lease runs out, a takeover the user started on their own goes back to the bot. One the bot asked for (a
	// pending handoff) has its own timeouts. Under takeoverMu, like every other takeover change.
	abandon := sandbox.ReleaseAbandonedTakeover(m.TakenOver,
		func(botID string) bool { return d.handoffs().State(botID).Pending }, m.Release)
	viewers.OnExpire(func(botID string) {
		d.takeoverMu.Lock()
		defer d.takeoverMu.Unlock()
		abandon(botID)
	})
	m.Subscribe(func(st sandbox.Status) {
		switch {
		case st.State == sandbox.StateStopping || st.State == sandbox.StateOff || st.State == sandbox.StateFailed:
			screens.CloseBot(st.BotID) // ending a screen also releases whatever the user held down
		case !st.TakenOver:
			// Every path that ends a takeover (Done, a handoff that timed out or whose turn ended, an abandoned
			// viewer lease) goes through Manager.Release, which publishes this status: release held keys and
			// buttons in the guest. A no-op when nothing is held or the user took over again.
			screens.TakeoverEnded(st.BotID)
		}
	})
	d.sandboxScreens, d.sandboxViewers = screens, viewers
}

// sandboxViewer starts, renews (watching) or ends the Computer tab's viewer lease: a watched computer is not idle.
func (d *Daemon) sandboxViewer(ctx context.Context, p sandboxViewerParams) error {
	if err := requireDesktopUser(ctx, "watch a bot's computer"); err != nil {
		return err
	}
	if err := checkComputerBotID(p.BotID); err != nil {
		return err
	}
	if p.Watching {
		d.sandboxViewers.Watch(p.BotID)
	} else {
		d.sandboxViewers.Unwatch(p.BotID)
	}
	return nil
}

// openSandboxScreen opens the running computer's screen; its bytes follow as sandbox.screen.data events.
func (d *Daemon) openSandboxScreen(ctx context.Context, p sandboxRef) (screenRef, error) {
	if err := requireDesktopUser(ctx, "open a bot's screen"); err != nil {
		return screenRef{}, err
	}
	g, err := d.runningGuest(p.BotID)
	if err != nil {
		return screenRef{}, err
	}
	// Connect bounds the opening (sandbox.ScreenOpenTimeout) by this call; the open screen lives until closed.
	id, err := d.sandboxScreens.Connect(ctx, p.BotID, g.OpenScreen)
	if err != nil {
		return screenRef{}, d.guestStreamError(p.BotID, "screen", err)
	}
	d.audit("sandbox.screen.open", p.BotID, "allowed", id)
	return screenRef{ID: id}, nil
}

// sandboxScreenInput passes viewer bytes to a screen; keyboard and mouse reach the guest only during a takeover.
func (d *Daemon) sandboxScreenInput(ctx context.Context, p screenInputParams) error {
	if err := requireDesktopUser(ctx, "use a bot's screen"); err != nil {
		return err
	}
	if len(p.Data) > screenInputMax {
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: "Screen input is too large."}
	}
	err := d.sandboxScreens.Input(p.ID, p.Data)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sandbox.ErrNoScreen):
		return &ipc.Error{Code: ipc.CodeNotFound, Message: "The screen is closed."}
	case errors.Is(err, sandbox.ErrScreenInputTooLarge):
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: fmt.Sprintf("Screen input is limited to %d KiB at a time; the screen was closed.", sandbox.ScreenChunkMax>>10)}
	case errors.Is(err, sandbox.ErrScreenStalled):
		return &ipc.Error{Code: ipc.CodeTimeout, Message: "The computer's screen stopped responding and was closed."}
	default: // input outside the accepted RFB subset, or the stream failed
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: "The screen was closed: " + err.Error()}
	}
}

// closeSandboxScreen ends a screen; unknown ids are fine (it may have closed already).
func (d *Daemon) closeSandboxScreen(ctx context.Context, p screenRef) error {
	if err := requireDesktopUser(ctx, "close a bot's screen"); err != nil {
		return err
	}
	d.sandboxScreens.Close(p.ID)
	return nil
}

// openSandboxPTY returns the bot's Computer terminal: a login shell in the guest, adopted by the terminal manager so
// the ordinary terminal methods and events serve it. A bot has at most one; an open one is returned as is.
func (d *Daemon) openSandboxPTY(ctx context.Context, p sandboxPTYParams) (terminals.Info, error) {
	if err := requireDesktopUser(ctx, "open a bot's terminal"); err != nil {
		return terminals.Info{}, err
	}
	if err := checkComputerBotID(p.BotID); err != nil {
		return terminals.Info{}, err
	}
	d.sandboxPTYMu.Lock() // two Computer tabs opening at once share one terminal
	defer d.sandboxPTYMu.Unlock()
	if info, ok := d.sandboxTerminal(p.BotID); ok {
		return info, nil
	}
	g, err := d.runningGuest(p.BotID)
	if err != nil {
		return terminals.Info{}, err
	}
	cols, rows := ptySize(p.Cols, p.Rows)                    // the guest and the terminal's Info must agree
	octx, cancel := context.WithTimeout(ctx, ptyOpenTimeout) // bounds only the opening
	proc, err := g.OpenPTY(octx, cols, rows)
	cancel()
	if err != nil {
		return terminals.Info{}, d.guestStreamError(p.BotID, "terminal", err)
	}
	info, err := d.Terminals.Adopt(proc, terminals.AdoptOptions{Title: "Computer", Shell: "orole-guest",
		Owner: sandboxOwner(p.BotID), Cols: cols, Rows: rows})
	if err != nil {
		return terminals.Info{}, err
	}
	d.audit("sandbox.pty.open", p.BotID, "allowed", info.ID)
	d.IPC.Publish("terminal.changed", info)
	return info, nil
}

// Computer terminal size: unset dimensions get the defaults, others are clamped to the range.
const (
	ptyDefaultCols, ptyMinCols, ptyMaxCols = 120, 20, 500
	ptyDefaultRows, ptyMinRows, ptyMaxRows = 30, 5, 200
)

// ptySize normalizes a requested Computer terminal size before the guest sees it.
func ptySize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = ptyDefaultCols
	}
	if rows <= 0 {
		rows = ptyDefaultRows
	}
	return min(max(cols, ptyMinCols), ptyMaxCols), min(max(rows, ptyMinRows), ptyMaxRows)
}

func sandboxOwner(botID string) string { return "sandbox:" + botID }

// runningGuest returns the guest of a computer that is already running. The UI never boots one: a computer starts
// when its bot first uses it.
func (d *Daemon) runningGuest(botID string) (*sandbox.Guest, error) {
	if err := checkComputerBotID(botID); err != nil {
		return nil, err
	}
	g, ok := d.Sandbox.RunningGuest(botID)
	if !ok {
		return nil, &ipc.Error{Code: ipc.CodeNotFound, Message: "The computer is off; it starts when the bot uses it."}
	}
	return g, nil
}

// guestStreamError reports a screen or terminal that could not be opened. A computer that stopped meanwhile is
// not_found, like one that was never running.
func (d *Daemon) guestStreamError(botID, what string, err error) error {
	if _, running := d.Sandbox.RunningGuest(botID); !running || errors.Is(err, sandbox.ErrCrashed) {
		return &ipc.Error{Code: ipc.CodeNotFound, Message: "The computer stopped."}
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &ipc.Error{Code: ipc.CodeTimeout, Message: "The computer did not open its " + what + " in time."}
	case errors.Is(err, context.Canceled):
		return &ipc.Error{Code: ipc.CodeCanceled, Message: "Canceled."}
	}
	return &ipc.Error{Code: ipc.CodeInternal, Message: "The computer could not open its " + what + ": " + err.Error()}
}

// sandboxTerminal finds the running Computer terminal of a bot.
func (d *Daemon) sandboxTerminal(botID string) (terminals.Info, bool) {
	owner := sandboxOwner(botID)
	for _, in := range d.Terminals.List() {
		if in.Owner == owner && in.State == terminals.Running {
			return in, true
		}
	}
	return terminals.Info{}, false
}

// echoSandboxExec shows a command the bot ran, with its output, in the bot's open Computer terminal (Config.OnExec,
// called in order per bot from the manager's queue). Nothing is kept when no terminal is open.
func (d *Daemon) echoSandboxExec(botID string, req wire.ExecRequest, res wire.ExecResult) {
	if info, ok := d.sandboxTerminal(botID); ok {
		_ = d.Terminals.Inject(info.ID, formatExecEcho(req, res))
	}
}

// formatExecEcho renders one bot command for the terminal: a dim "[bot]" prompt, the command on one line, the
// output with newlines as CRLF and capped at execEchoMax (after that expansion), and a dim status line. The command
// and output come from the bot and the guest: escape sequences and control characters are removed (tab and newline
// kept in the output), so neither can drive the user's terminal.
func formatExecEcho(req wire.ExecRequest, res wire.ExecResult) []byte {
	var b bytes.Buffer
	b.WriteString("\r\n\x1b[2m[bot]\x1b[0m $ ")
	cmd := sanitizeEcho(req.Command, false)
	if len(cmd) > execEchoCommandMax {
		cmd = cutUTF8(cmd, execEchoCommandMax) + "…"
	}
	b.WriteString(cmd)
	b.WriteString("\r\n")

	raw, cut := res.Stdout+res.Stderr, res.Truncated
	if len(raw) > 2*execEchoMax { // bound the work: at most half of this survives CRLF expansion
		raw, cut = raw[:2*execEchoMax], true
	}
	out := strings.ReplaceAll(sanitizeEcho(raw, true), "\n", "\r\n")
	if len(out) > execEchoMax {
		n := len(cutUTF8(out, execEchoMax))
		if n > 0 && out[n-1] == '\r' { // never split a CRLF
			n--
		}
		out, cut = out[:n], true
	}
	b.WriteString(out)
	if out != "" && !strings.HasSuffix(out, "\r\n") {
		b.WriteString("\r\n")
	}
	notes := []string{fmt.Sprintf("exit %d", res.ExitCode)}
	if res.TimedOut {
		notes = append(notes, "timed out")
	}
	if cut {
		notes = append(notes, "output cut")
	}
	fmt.Fprintf(&b, "\x1b[2m(%s)\x1b[0m\r\n", strings.Join(notes, ", "))
	return b.Bytes()
}

// cutUTF8 returns at most n bytes of s, not splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// sanitizeEcho removes escape sequences (ESC and C1 introduced: CSI with its parameters, OSC/DCS/SOS/PM/APC strings
// up to their terminator), every other C0 and C1 control, DEL and bytes that are not UTF-8 (a raw 0x9b would be
// CSI to an 8-bit terminal). multiline keeps "\n" (a "\r\n" becomes "\n") and "\t"; otherwise both become a space.
func sanitizeEcho(s string, multiline bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			if s[i] == 0x9b { // raw 8-bit CSI
				i = skipCSI(s, i+1)
			} else {
				i++
			}
			continue
		}
		switch {
		case r == '\n' || r == '\t':
			if multiline {
				b.WriteRune(r)
			} else {
				b.WriteByte(' ')
			}
			i += size
		case r == 0x1b:
			i = skipEscape(s, i+size)
		case r == 0x9b: // C1 CSI
			i = skipCSI(s, i+size)
		case r == 0x90 || r == 0x98 || r == 0x9d || r == 0x9e || r == 0x9f: // C1 DCS, SOS, OSC, PM, APC
			i = skipString(s, i+size)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			i += size // "\r" included: a lone one would overwrite the line; "\r\n" leaves the "\n"
		default:
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return b.String()
}

// skipEscape skips the sequence after an ESC at s[i-1] and returns the index after it.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_': // OSC, DCS, SOS, PM, APC
		return skipString(s, i+1)
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f { // intermediates, then one final byte
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipCSI skips a CSI's parameter and intermediate bytes and its final byte, starting at s[i].
func skipCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipString skips a control string up to and including its terminator (BEL, ESC \, or the C1 ST), or to the end.
func skipString(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == 0x07:
			return i + 1
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		case s[i] == 0x9c: // raw 8-bit ST
			return i + 1
		case strings.HasPrefix(s[i:], "\u009c"):
			return i + len("\u009c")
		}
		i++
	}
	return i
}
