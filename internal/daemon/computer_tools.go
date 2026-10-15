package daemon

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/chat"
	"github.com/rayorole/orole-os-desktop/internal/chat/tools"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/notifications"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

// refTakeover is the ref of a bot's "Needs you" takeover request; its target is "computer:<botId>".
const refTakeover = "computer-takeover:"

// maxReleaseNote caps the note the user hands back with Done, in characters.
const maxReleaseNote = 500

// computerStartTimeout bounds starting a bot's VM before a computer tool's guest call: the manager's boot timeout
// and busy wait plus slack (tools.ComputerStartTimeout, which the tools' own deadline allows for).
const computerStartTimeout = sandboxBootTimeout + sandboxBusyWait + 10*time.Second

// computerGuest is the part of *sandbox.Guest the computer tools use.
type computerGuest interface {
	Exec(ctx context.Context, req wire.ExecRequest) (wire.ExecResult, error)
	ReadFile(ctx context.Context, path string) (wire.ReadFileResult, error)
	WriteFile(ctx context.Context, path string, content []byte) (wire.WriteFileResult, error)
	Browser(ctx context.Context, req wire.BrowserRequest) (wire.BrowserResult, error)
}

// computerHost is the part of *sandbox.Manager the computer tools and the takeover handlers use.
type computerHost interface {
	Ensure(ctx context.Context, botID string) (computerGuest, error)
	Status(botID string) sandbox.Status
	TakeOver(botID string) error
	Release(botID string)
	TakenOver(botID string) bool
}

// managerHost is a *sandbox.Manager as a computerHost.
type managerHost struct{ *sandbox.Manager }

func (m managerHost) Ensure(ctx context.Context, botID string) (computerGuest, error) {
	g, err := m.Manager.Ensure(ctx, botID)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// handoffs returns the daemon's takeover requests, created on first use.
func (d *Daemon) handoffs() *sandbox.Handoffs {
	d.handoffsOnce.Do(func() { d.handoffsSet = sandbox.NewHandoffs(d.handoffChanged) })
	return d.handoffsSet
}

// handoffChanged publishes a takeover request (sandbox.handoff) and keeps its "Needs you" notification in step: one
// entry while the bot waits, opening the bot's computer, resolved when the wait ends however it ends.
func (d *Daemon) handoffChanged(st sandbox.HandoffState) {
	if d.IPC != nil {
		d.IPC.Publish("sandbox.handoff", st)
	}
	if d.DB == nil {
		return
	}
	ref := refTakeover + st.BotID
	if !st.Pending {
		d.resolveNotification(ref)
		return
	}
	// The reason was validated by ask_user_to_take_over: plain single-line text without invisible characters.
	_, err := d.Notify(notifications.Input{Source: "bot", Kind: notifications.Approval, Ref: ref, Target: "computer:" + st.BotID,
		Title: d.computerBotName(st.BotID), Body: clipTo("Wants you to take over its computer: "+st.Reason, noticeBody)})
	if err != nil && d.Log != nil {
		d.Log.Warn("takeover notification failed", "bot", st.BotID, "err", err)
	}
}

func (d *Daemon) computerBotName(id string) string {
	if d.Chat != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if b, err := d.Chat.Bot(ctx, id); err == nil && strings.TrimSpace(b.Name) != "" {
			return b.Name
		}
	}
	return "Bot"
}

// computerStatusChanged ends a bot's takeover request when its computer stops or crashes: there is nothing left to
// take over, and the takeover lock went with the VM. It runs from Manager.Subscribe and does not block.
func (d *Daemon) computerStatusChanged(s sandbox.Status) {
	switch s.State {
	case sandbox.StateStopping, sandbox.StateOff, sandbox.StateFailed:
		d.handoffs().Cancel(s.BotID, sandbox.ErrHandoffStopped)
	}
}

// takeoverLock is the part of *sandbox.Manager the Take over and Done handlers use.
type takeoverLock interface {
	TakeOver(botID string) error
	Release(botID string)
}

// sandboxReleaseParams is sandbox.release's request.
type sandboxReleaseParams struct {
	BotID string `json:"botId"`
	Note  string `json:"note,omitempty"`
}

func checkComputerBotID(id string) error {
	if id == "" || len(id) > 128 || strings.ContainsFunc(id, func(r rune) bool { return r == '/' || r == '\\' || unicode.IsControl(r) }) {
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: "A valid bot id is required."}
	}
	return nil
}

// auditComputer checks and records a takeover step as the user (the policy engine audits the decision).
func (d *Daemon) auditComputer(ctx context.Context, action, botID string) error {
	if d.Policy == nil {
		return nil
	}
	if err := d.Policy.Require(policy.WithoutApprovals(ctx), policy.Request{Actor: "user", Action: action, Resource: "computer/" + botID, Effect: policy.EffectRead}); err != nil {
		return &ipc.Error{Code: ipc.CodeDenied, Message: "This is not allowed by your policy."}
	}
	return nil
}

// takeOverComputer gives the user the bot's computer (sandbox.takeover): bot browser tools are refused until
// releaseComputer. A bot waiting in ask_user_to_take_over switches to the hold timeout.
func (d *Daemon) takeOverComputer(ctx context.Context, lock takeoverLock, botID string) error {
	if err := requireDesktopUser(ctx, "take over a bot's computer"); err != nil {
		return err
	}
	if err := checkComputerBotID(botID); err != nil {
		return err
	}
	if err := d.auditComputer(ctx, "computer.takeover", botID); err != nil {
		return err
	}
	d.takeoverMu.Lock()
	defer d.takeoverMu.Unlock()
	if err := lock.TakeOver(botID); err != nil {
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: "The computer is not running."}
	}
	d.handoffs().Started(botID)
	return nil
}

// releaseComputer hands the computer back (sandbox.release, the Done button) and resumes a bot waiting in
// ask_user_to_take_over with the user's note.
func (d *Daemon) releaseComputer(ctx context.Context, lock takeoverLock, p sandboxReleaseParams) error {
	if err := requireDesktopUser(ctx, "hand a bot's computer back"); err != nil {
		return err
	}
	if err := checkComputerBotID(p.BotID); err != nil {
		return err
	}
	if !utf8.ValidString(p.Note) || utf8.RuneCountInString(p.Note) > maxReleaseNote {
		return &ipc.Error{Code: ipc.CodeBadRequest, Message: "The note is limited to 500 characters."}
	}
	// The note is the user's own text, but control and invisible characters are still stripped before a bot reads it.
	note := strings.TrimSpace(untrusted.Text(p.Note, 0))
	if err := d.auditComputer(ctx, "computer.release", p.BotID); err != nil {
		return err
	}
	d.takeoverMu.Lock()
	defer d.takeoverMu.Unlock()
	lock.Release(p.BotID)
	d.handoffs().Finish(p.BotID, note)
	return nil
}

// handoffState answers sandbox.handoff: whether the bot waits for the user to take over, and why.
func (d *Daemon) handoffState(ctx context.Context, botID string) (sandbox.HandoffState, error) {
	if err := requireDesktopUser(ctx, "see a bot's takeover request"); err != nil {
		return sandbox.HandoffState{}, err
	}
	if err := checkComputerBotID(botID); err != nil {
		return sandbox.HandoffState{}, err
	}
	return d.handoffs().State(botID), nil
}

// registerSandboxTakeover answers the Computer tab's Take over / Done buttons and the pending takeover request, and
// ends a request when its computer stops. Desktop user only; each step is audited as the user.
//
// IPC: sandbox.takeover{botId}, sandbox.release{botId, note?} (note at most 500 characters), sandbox.handoff{botId}
// -> sandbox.HandoffState. Event: sandbox.handoff (sandbox.HandoffState) whenever a request starts or ends.
func (d *Daemon) registerSandboxTakeover(host computerHost) {
	handle(d, "sandbox.takeover", func(ctx context.Context, p sandboxRef) (struct{}, error) {
		return struct{}{}, d.takeOverComputer(ctx, host, p.BotID)
	})
	handle(d, "sandbox.release", func(ctx context.Context, p sandboxReleaseParams) (struct{}, error) {
		return struct{}{}, d.releaseComputer(ctx, host, p)
	})
	handle(d, "sandbox.handoff", func(ctx context.Context, p sandboxRef) (sandbox.HandoffState, error) {
		return d.handoffState(ctx, p.BotID)
	})
}

// computerRuntime is the sandbox manager as the bot computer tools see it (tools.ComputerRuntime). host nil reads
// d.Sandbox on every call: a daemon without the sandbox answers sandbox.ErrNotReady. bots (the chat store) refuses
// bots that were archived or no longer exist before anything boots; nil skips that check.
type computerRuntime struct {
	d    *Daemon
	host computerHost
	bots botLookup
}

func (r computerRuntime) sandbox() (computerHost, error) {
	if r.host != nil {
		return r.host, nil
	}
	if r.d.Sandbox == nil {
		return nil, sandbox.ErrNotReady
	}
	return managerHost{r.d.Sandbox}, nil
}

// guest checks the bot still exists, then returns its running guest, starting the VM within computerStartTimeout.
func (r computerRuntime) guest(ctx context.Context, botID string) (computerHost, computerGuest, error) {
	h, err := r.sandbox()
	if err != nil {
		return nil, nil, err
	}
	if r.bots != nil {
		if _, err := r.bots.Bot(ctx, botID); errors.Is(err, chat.ErrNotFound) {
			return nil, nil, tools.ErrComputerGone // archived bots are not listed: their computer is being deleted
		} else if err != nil {
			return nil, nil, err
		}
	}
	start, cancel := context.WithTimeout(ctx, computerStartTimeout)
	defer cancel()
	g, err := h.Ensure(start, botID)
	if err != nil {
		return nil, nil, err
	}
	return h, g, nil
}

// guestCall bounds one guest call by the timeout its tool set (tools.GuestCallTimeout), counted from now: the VM
// is up, so a slow boot did not use any of it.
func guestCall(ctx context.Context) (context.Context, context.CancelFunc) {
	if d := tools.GuestCallTimeout(ctx); d > 0 {
		return context.WithTimeout(ctx, d)
	}
	return context.WithCancel(ctx)
}

func (r computerRuntime) Running(botID string) bool {
	h, err := r.sandbox()
	return err == nil && h.Status(botID).State == sandbox.StateRunning
}

func (r computerRuntime) TakenOver(botID string) bool {
	h, err := r.sandbox()
	return err == nil && h.TakenOver(botID)
}

func (r computerRuntime) Exec(ctx context.Context, botID string, req wire.ExecRequest) (wire.ExecResult, error) {
	h, g, err := r.guest(ctx, botID)
	if err != nil {
		return wire.ExecResult{}, err
	}
	if h.TakenOver(botID) { // the user may have taken over while the VM started
		return wire.ExecResult{}, sandbox.ErrTakenOver
	}
	cctx, cancel := guestCall(ctx)
	defer cancel()
	return g.Exec(cctx, req)
}

func (r computerRuntime) ReadFile(ctx context.Context, botID, path string) (wire.ReadFileResult, error) {
	_, g, err := r.guest(ctx, botID)
	if err != nil {
		return wire.ReadFileResult{}, err
	}
	cctx, cancel := guestCall(ctx)
	defer cancel()
	return g.ReadFile(cctx, path)
}

func (r computerRuntime) WriteFile(ctx context.Context, botID, path string, content []byte) (wire.WriteFileResult, error) {
	h, g, err := r.guest(ctx, botID)
	if err != nil {
		return wire.WriteFileResult{}, err
	}
	if h.TakenOver(botID) { // the user may have taken over while the VM started
		return wire.WriteFileResult{}, sandbox.ErrTakenOver
	}
	cctx, cancel := guestCall(ctx)
	defer cancel()
	return g.WriteFile(cctx, path, content)
}

func (r computerRuntime) Browser(ctx context.Context, botID string, req wire.BrowserRequest) (wire.BrowserResult, error) {
	h, g, err := r.guest(ctx, botID)
	if err != nil {
		return wire.BrowserResult{}, err
	}
	if h.TakenOver(botID) { // the user may have taken over while the VM started
		return wire.BrowserResult{}, sandbox.ErrTakenOver
	}
	cctx, cancel := guestCall(ctx)
	defer cancel()
	return g.Browser(cctx, req)
}

// AskTakeover makes sure the computer runs (the user needs a screen to take over), then waits for the user. Whether
// the user already holds the computer is read under takeoverMu, which the Take over and Done handlers hold, so the
// wait never starts from a stale answer. A wait that ends without Done (hold timeout, the turn ended) releases the
// takeover lock, so the bot's browser is not left refused.
func (r computerRuntime) AskTakeover(ctx context.Context, botID, reason string) (string, error) {
	h, _, err := r.guest(ctx, botID)
	if err != nil {
		return "", err
	}
	hs := r.d.handoffs()
	r.d.takeoverMu.Lock()
	w, err := hs.Begin(ctx, botID, reason, h.TakenOver(botID))
	r.d.takeoverMu.Unlock()
	if err != nil {
		return "", err
	}
	// A stop between Ensure and Begin found no wait to end: end it now.
	if h.Status(botID).State != sandbox.StateRunning {
		hs.Cancel(botID, sandbox.ErrHandoffStopped)
	}
	note, err := w.Wait(ctx)
	if errors.Is(err, sandbox.ErrHandoffHoldTimeout) || (err != nil && ctx.Err() != nil) {
		r.d.takeoverMu.Lock()
		h.Release(botID)
		r.d.takeoverMu.Unlock()
	}
	return note, err
}

var _ tools.ComputerRuntime = computerRuntime{}
