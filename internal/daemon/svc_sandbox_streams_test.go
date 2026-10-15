package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"golang.org/x/net/http2"

	"github.com/rayorole/orole-os-desktop/internal/agents"
	"github.com/rayorole/orole-os-desktop/internal/daemon/control"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/ipc"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
	"github.com/rayorole/orole-os-desktop/internal/terminals"
)

func TestFormatExecEcho(t *testing.T) {
	tests := []struct {
		name string
		req  wire.ExecRequest
		res  wire.ExecResult
		want string
	}{
		{name: "plain", req: wire.ExecRequest{Command: `python -c "print(1+1)"`}, res: wire.ExecResult{Stdout: "2\n"},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ python -c \"print(1+1)\"\r\n2\r\n\x1b[2m(exit 0)\x1b[0m\r\n"},
		{name: "escape sequences and controls removed from the command", req: wire.ExecRequest{Command: "ls\x1b[2J\x9b1m\nrm\trf\a"},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ ls rm rf\r\n\x1b[2m(exit 0)\x1b[0m\r\n"},
		{name: "stderr, exit code, timeout and cut output", req: wire.ExecRequest{Command: "sleep 999"},
			res:  wire.ExecResult{Stdout: "a", Stderr: "b\r\nc", ExitCode: 124, TimedOut: true, Truncated: true},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ sleep 999\r\nab\r\nc\r\n\x1b[2m(exit 124, timed out, output cut)\x1b[0m\r\n"},
		{name: "output cannot drive the terminal", req: wire.ExecRequest{Command: "cat x"},
			res:  wire.ExecResult{Stdout: "\x1b]0;owned\a\x1b[31mred\x1b[0m\tcol\rover\u009b2Jc1\x9b3Jraw\x1bPdcs\x1b\\end\x00\n"},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ cat x\r\nred\tcoloverc1rawend\r\n\x1b[2m(exit 0)\x1b[0m\r\n"},
		{name: "a C1 CSI rune in the command", req: wire.ExecRequest{Command: "a\u009b31mb\u009d0;t\u009cc"},
			want: "\r\n\x1b[2m[bot]\x1b[0m $ abc\r\n\x1b[2m(exit 0)\x1b[0m\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(formatExecEcho(tt.req, tt.res)); got != tt.want {
				t.Fatalf("got  %q\nwant %q", got, tt.want)
			}
		})
	}

	const overhead = 256 // prompt and status line
	big := formatExecEcho(wire.ExecRequest{Command: "yes"}, wire.ExecResult{Stdout: strings.Repeat("y\n", 100_000)})
	if len(big) > execEchoMax+overhead || !strings.Contains(string(big), "output cut") {
		t.Fatalf("echo of %d bytes is not capped", len(big))
	}
	// Newlines double in size when they become CRLF: output under the cap before that can exceed it after.
	lines := formatExecEcho(wire.ExecRequest{Command: "seq"}, wire.ExecResult{Stdout: strings.Repeat("\n", execEchoMax-100)})
	if len(lines) > execEchoMax+overhead || !strings.Contains(string(lines), "output cut") {
		t.Fatalf("CRLF expansion escaped the cap: %d bytes", len(lines))
	}
	if strings.Contains(string(lines), "\r\r") || strings.Contains(strings.ReplaceAll(string(lines), "\r\n", ""), "\n") {
		t.Fatal("the cap split a CRLF")
	}
	wide := formatExecEcho(wire.ExecRequest{Command: "x"}, wire.ExecResult{Stdout: strings.Repeat("é", execEchoMax)})
	if !utf8.Valid(wide) {
		t.Fatal("the cap split a character")
	}
	long := formatExecEcho(wire.ExecRequest{Command: strings.Repeat("c", 10_000)}, wire.ExecResult{})
	if len(long) > execEchoCommandMax+overhead {
		t.Fatalf("a %d byte command line is not capped", len(long))
	}
}

func ipcCode(err error) string {
	var ie *ipc.Error
	if errors.As(err, &ie) {
		return ie.Code
	}
	return ""
}

func TestSandboxStreamsRefuseWhatTheyCannotServe(t *testing.T) {
	d := jarvisDaemon(t)
	c := dialDaemon(t, d)
	ctx := context.Background()
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "nobot"}, nil); ipcCode(err) != ipc.CodeNotFound {
		t.Fatalf("screen.open on an off computer: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": ""}, nil); ipcCode(err) != ipc.CodeBadRequest {
		t.Fatalf("screen.open without a bot: %v", err)
	}
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": "nobot", "cols": 80, "rows": 24}, nil); ipcCode(err) != ipc.CodeNotFound {
		t.Fatalf("pty.open on an off computer: %v", err)
	}
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": ""}, nil); ipcCode(err) != ipc.CodeBadRequest {
		t.Fatalf("pty.open without a bot: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": "missing", "data": []byte{3}}, nil); ipcCode(err) != ipc.CodeNotFound {
		t.Fatalf("input to an unknown screen: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": "missing", "data": make([]byte, 1<<20+1)}, nil); ipcCode(err) != ipc.CodeBadRequest {
		t.Fatalf("oversized input: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.close", map[string]string{"id": "missing"}, nil); err != nil {
		t.Fatalf("closing an unknown screen: %v", err)
	}
	for _, watching := range []bool{true, false} {
		if err := c.Call(ctx, "sandbox.viewer", map[string]any{"botId": "nobot", "watching": watching}, nil); err != nil {
			t.Fatalf("viewer %v: %v", watching, err)
		}
	}
	if err := c.Call(ctx, "sandbox.viewer", map[string]any{"botId": "", "watching": true}, nil); ipcCode(err) != ipc.CodeBadRequest {
		t.Fatalf("viewer without a bot: %v", err)
	}
	// The VM was never started by any of this: the UI does not boot computers.
	if st := d.Sandbox.Status("nobot"); st.State != sandbox.StateOff {
		t.Fatalf("status after the calls: %+v", st)
	}
}

// --- a daemon whose computers are in-process fake VMs ---

// fakeComputers boots "VMs" that serve the real guest agent on a net.Pipe, with a screen that echoes its input
// after an RFB greeting, a terminal that echoes keystrokes, and /exec answered by the test.
type fakeComputers struct {
	mu  sync.Mutex
	vms []*fakeComputer
}

const fakeRFBGreeting = "RFB 003.008\n"

func (b *fakeComputers) Availability(context.Context) sandbox.Availability {
	return sandbox.Availability{Ready: true}
}

func (b *fakeComputers) Boot(_ context.Context, spec sandbox.BootSpec) (sandbox.VM, error) {
	host, guest := net.Pipe()
	vm := &fakeComputer{conn: host, guestEnd: guest, done: make(chan struct{})}
	agent := guestagent.New(guestagent.Config{
		Token: spec.Token, Version: "test",
		StartPTY: func(s guestagent.PTYSpec) (guestagent.PTY, error) { return newEchoTerminal(), nil },
		Screen:   vm.dialScreen,
		PowerOff: func() error { go vm.exit(); return nil },
	})
	inner := agent.Handler()
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exec" && r.Header.Get(wire.TokenHeader) == spec.Token {
			var req wire.ExecRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(wire.ExecResult{Stdout: "ran " + req.Command + "\n"})
			return
		}
		inner.ServeHTTP(w, r)
	})
	go (&http2.Server{}).ServeConn(guest, &http2.ServeConnOpts{Handler: h})
	b.mu.Lock()
	b.vms = append(b.vms, vm)
	b.mu.Unlock()
	return vm, nil
}

func (b *fakeComputers) killAll() {
	b.mu.Lock()
	vms := append([]*fakeComputer(nil), b.vms...)
	b.mu.Unlock()
	for _, vm := range vms {
		vm.exit()
	}
}

type fakeComputer struct {
	conn, guestEnd net.Conn
	once           sync.Once
	done           chan struct{}
}

func (v *fakeComputer) Conn() net.Conn                 { return v.conn }
func (v *fakeComputer) Shutdown(context.Context) error { v.exit(); return nil }
func (v *fakeComputer) Kill() error                    { v.exit(); return nil }
func (v *fakeComputer) Done() <-chan struct{}          { return v.done }
func (v *fakeComputer) ExitErr() error                 { return nil }

func (v *fakeComputer) exit() {
	v.once.Do(func() {
		_ = v.conn.Close()
		_ = v.guestEnd.Close()
		close(v.done)
	})
}

// dialScreen is the guest's VNC server: it greets, then echoes what the viewer sends.
func (v *fakeComputer) dialScreen(context.Context) (net.Conn, error) {
	agentEnd, server := net.Pipe()
	go func() {
		defer server.Close()
		if _, err := server.Write([]byte(fakeRFBGreeting)); err != nil {
			return
		}
		go func() { <-v.done; _ = server.Close() }()
		_, _ = io.Copy(server, server)
	}()
	return agentEnd, nil
}

// echoTerminal is a guest terminal that echoes keystrokes back.
type echoTerminal struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	once sync.Once
	done chan struct{}
}

func newEchoTerminal() *echoTerminal {
	r, w := io.Pipe()
	return &echoTerminal{r: r, w: w, done: make(chan struct{})}
}

func (e *echoTerminal) Read(b []byte) (int, error)  { return e.r.Read(b) }
func (e *echoTerminal) Write(b []byte) (int, error) { return e.w.Write(append([]byte("echo:"), b...)) }
func (e *echoTerminal) Resize(int, int) error       { return nil }
func (e *echoTerminal) Close() error {
	e.once.Do(func() { _ = e.w.Close(); close(e.done) })
	return nil
}
func (e *echoTerminal) Wait() (int, error) { <-e.done; return 0, nil }

// computerDaemon runs a daemon whose sandbox manager boots fakeComputers and echoes execs into the terminal.
func computerDaemon(t *testing.T) *Daemon {
	t.Helper()
	restore := UseMemoryLLMCredentials()
	t.Cleanup(restore)
	dir := t.TempDir()
	d, err := New(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeComputers{}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = d.Sandbox.Shutdown(sctx) // the host-backed manager registerSandbox made: nothing runs on it
	cancel()
	m := sandbox.NewManager(sandbox.Config{Paths: sandbox.Paths{Root: filepath.Join(dir, "sandbox")}, BootTimeout: 10 * time.Second,
		Now: time.Now, OnExec: d.echoSandboxExec,
		Image: func() (sandbox.ImageSet, error) {
			return sandbox.ImageSet{Version: "v1", Kernel: "k", Initrd: "i", BaseImage: "base.qcow2"}, nil
		}}, b, d.sandboxStore)
	d.Sandbox = m
	m.Subscribe(d.computerStatusChanged)
	d.registerSandboxTakeover(managerHost{m}) // replaces the handlers bound to the first manager
	d.attachSandboxStreams(m)
	d.sandboxStart = nil // the start sweep would delete the computers of these bots, which the chat store lacks
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	t.Cleanup(func() {
		d.Shutdown()
		if err := <-done; err != nil {
			t.Error(err)
		}
		b.killAll()
	})
	return d
}

// streamClient is a desktop client that records sandbox.screen.data and terminal events.
type streamClient struct {
	*ipc.Client
	mu      sync.Mutex
	screens []sandbox.ScreenChunk
	output  map[string]string // terminal id -> output seen
	changed []terminals.Info
}

func newStreamClient(t *testing.T, d *Daemon) *streamClient {
	t.Helper()
	sc := &streamClient{output: map[string]string{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := 0; ; i++ {
		c := control.NewClient(d.DataDir, "streams-test")
		c.OnEvent(sc.event)
		if err := c.Connect(ctx); err == nil {
			sc.Client = c
			break
		} else if i == 100 {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Cleanup(sc.Close)
	if err := sc.Subscribe(ctx, "sandbox.screen.data", "terminal.output", "terminal.changed"); err != nil {
		t.Fatal(err)
	}
	return sc
}

func (sc *streamClient) event(name string, data json.RawMessage) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	switch name {
	case "sandbox.screen.data":
		var c sandbox.ScreenChunk
		if json.Unmarshal(data, &c) == nil {
			sc.screens = append(sc.screens, c)
		}
	case "terminal.output":
		var o struct {
			ID   string `json:"id"`
			Data []byte `json:"data"`
		}
		if json.Unmarshal(data, &o) == nil {
			sc.output[o.ID] += string(o.Data)
		}
	case "terminal.changed":
		var in terminals.Info
		if json.Unmarshal(data, &in) == nil {
			sc.changed = append(sc.changed, in)
		}
	}
}

// screenData is everything screen id sent, and whether its closed chunk arrived (with its error).
func (sc *streamClient) screenData(id string) (data string, closed bool, why string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for _, c := range sc.screens {
		if c.ID != id {
			continue
		}
		data += string(c.Data)
		if c.Closed {
			closed, why = true, c.Error
		}
	}
	return data, closed, why
}

func (sc *streamClient) terminalOutput(id string) string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.output[id]
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func bootComputer(t *testing.T, d *Daemon, botID string) *sandbox.Guest {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	g, err := d.Sandbox.Ensure(ctx, botID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSandboxScreenOpensRelaysInputAndCloses(t *testing.T) {
	d := computerDaemon(t)
	c := newStreamClient(t, d)
	ctx := context.Background()
	bootComputer(t, d, "b1")

	var ref struct{ ID string }
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "b1"}, &ref); err != nil || ref.ID == "" {
		t.Fatalf("screen.open: %v %+v", err, ref)
	}
	waitFor(t, "the guest greeting", func() bool { got, _, _ := c.screenData(ref.ID); return got == fakeRFBGreeting })

	// The viewer's version message passes the RFB filter and reaches the guest, which echoes it.
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": ref.ID, "data": []byte("RFB 003.008\n")}, nil); err != nil {
		t.Fatalf("screen.input: %v", err)
	}
	waitFor(t, "the echoed input", func() bool { got, _, _ := c.screenData(ref.ID); return got == fakeRFBGreeting+"RFB 003.008\n" })

	if err := c.Call(ctx, "sandbox.screen.close", map[string]string{"id": ref.ID}, nil); err != nil {
		t.Fatalf("screen.close: %v", err)
	}
	waitFor(t, "the closed chunk", func() bool { _, closed, why := c.screenData(ref.ID); return closed && why == "" })
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": ref.ID, "data": []byte{0}}, nil); ipcCode(err) != ipc.CodeNotFound {
		t.Fatalf("input after close: %v", err)
	}
	if err := c.Call(ctx, "sandbox.screen.close", map[string]string{"id": ref.ID}, nil); err != nil {
		t.Fatalf("second close: %v", err)
	}

	// Input outside the accepted RFB subset closes the screen with a clear error.
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "b1"}, &ref); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": ref.ID, "data": []byte("HTTP/1.1 nonsense\n")}, nil); ipcCode(err) != ipc.CodeBadRequest {
		t.Fatalf("garbage input: %v", err)
	}
	waitFor(t, "the closed chunk after bad input", func() bool { _, closed, why := c.screenData(ref.ID); return closed && why != "" })
}

func TestSandboxPTYIsAdoptedReusedAndEchoesBotCommands(t *testing.T) {
	d := computerDaemon(t)
	c := newStreamClient(t, d)
	ctx := context.Background()
	g := bootComputer(t, d, "b1")

	var first terminals.Info
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": "b1", "cols": 100, "rows": 30}, &first); err != nil {
		t.Fatalf("pty.open: %v", err)
	}
	if first.Owner != "sandbox:b1" || first.State != terminals.Running || first.Cols != 100 || first.Rows != 30 {
		t.Fatalf("terminal %+v", first)
	}
	var again terminals.Info
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": "b1", "cols": 80, "rows": 24}, &again); err != nil || again.ID != first.ID {
		t.Fatalf("second pty.open opened another terminal: %v %+v", err, again)
	}
	owned := 0
	for _, in := range d.Terminals.List() {
		if in.Owner == "sandbox:b1" {
			owned++
		}
	}
	if owned != 1 {
		t.Fatalf("%d terminals for the bot", owned)
	}

	// Keystrokes reach the guest terminal through the ordinary terminal methods.
	if err := c.Call(ctx, "terminals.write", map[string]any{"id": first.ID, "data": []byte("hi")}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the echoed keystrokes", func() bool { return strings.Contains(c.terminalOutput(first.ID), "echo:hi") })

	// A command the bot runs shows up in the open terminal.
	ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := g.Exec(ectx, wire.ExecRequest{Command: "make \x1b[2Jtest"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the exec echo", func() bool {
		out := c.terminalOutput(first.ID)
		return strings.Contains(out, "\x1b[2m[bot]\x1b[0m $ make test\r\nran make test\r\n\x1b[2m(exit 0)\x1b[0m\r\n")
	})
}

func TestStoppingTheComputerClosesItsScreensAndTerminal(t *testing.T) {
	d := computerDaemon(t)
	c := newStreamClient(t, d)
	ctx := context.Background()
	bootComputer(t, d, "b1")

	var ref struct{ ID string }
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "b1"}, &ref); err != nil {
		t.Fatal(err)
	}
	var term terminals.Info
	if err := c.Call(ctx, "sandbox.pty.open", map[string]any{"botId": "b1", "cols": 80, "rows": 24}, &term); err != nil {
		t.Fatal(err)
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := d.Sandbox.Delete(dctx, "b1"); err != nil { // stops the VM
		t.Fatal(err)
	}
	waitFor(t, "the screen to close", func() bool {
		_, closed, why := c.screenData(ref.ID)
		return closed && why == "the computer stopped"
	})
	waitFor(t, "the terminal to exit", func() bool {
		for _, in := range d.Terminals.List() {
			if in.ID == term.ID {
				return in.State == terminals.Exited
			}
		}
		return true // already reaped
	})
	if _, ok := d.sandboxTerminal("b1"); ok {
		t.Fatal("a stopped computer still has a running terminal")
	}
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "b1"}, nil); ipcCode(err) != ipc.CodeNotFound {
		t.Fatalf("screen.open on the stopped computer: %v", err)
	}
	if st := d.Sandbox.Status("b1"); st.State != sandbox.StateOff {
		t.Fatalf("the computer was started again: %+v", st)
	}
}

func TestSandboxStreamMethodsAreForTheDesktopUserOnly(t *testing.T) {
	d := jarvisDaemon(t)
	for name, ctx := range map[string]context.Context{
		"bot":        agents.WithActor(context.Background(), "bot:b1"),
		"automation": events.WithChain(context.Background(), []string{"auto-1"}),
	} {
		calls := map[string]error{
			"viewer":       d.sandboxViewer(ctx, sandboxViewerParams{BotID: "b1", Watching: true}),
			"screen.input": d.sandboxScreenInput(ctx, screenInputParams{ID: "x", Data: []byte{0}}),
			"screen.close": d.closeSandboxScreen(ctx, screenRef{ID: "x"}),
		}
		_, calls["screen.open"] = d.openSandboxScreen(ctx, sandboxRef{BotID: "b1"})
		_, calls["pty.open"] = d.openSandboxPTY(ctx, sandboxPTYParams{BotID: "b1", Cols: 80, Rows: 24})
		for m, err := range calls {
			if ipcCode(err) != ipc.CodeDenied {
				t.Errorf("%s as %s: %v", m, name, err)
			}
		}
	}
}

// openTypedScreen opens b1's screen, completes the RFB handshake and holds the 'A' key down, which reaches the
// guest only while the user has taken over.
func openTypedScreen(t *testing.T, c *streamClient) string {
	t.Helper()
	ctx := context.Background()
	var ref struct{ ID string }
	if err := c.Call(ctx, "sandbox.screen.open", map[string]string{"botId": "b1"}, &ref); err != nil {
		t.Fatal(err)
	}
	handshake := append([]byte(fakeRFBGreeting), 1, 1)
	keyDown := []byte{4, 1, 0, 0, 0, 0, 0, 0x41}
	if err := c.Call(ctx, "sandbox.screen.input", map[string]any{"id": ref.ID, "data": append(handshake, keyDown...)}, nil); err != nil {
		time.Sleep(200 * time.Millisecond)
		got, closed, why := c.screenData(ref.ID)
		t.Fatalf("%v (screen sent %q, closed %v: %q)", err, got, closed, why)
	}
	waitFor(t, "the key down at the guest", func() bool {
		got, _, _ := c.screenData(ref.ID)
		return strings.HasSuffix(got, string(keyDown))
	})
	return ref.ID
}

var keyUpA = string([]byte{4, 0, 0, 0, 0, 0, 0, 0x41})

// Every way a takeover ends releases the keys the user still held, and a lease that ran out gives the computer
// back unless the bot itself asked the user to take over.
func TestEndingATakeoverReleasesHeldKeys(t *testing.T) {
	defer func(old time.Duration) { sandboxViewerTTL = old }(sandboxViewerTTL)
	sandboxViewerTTL = 300 * time.Millisecond
	d := computerDaemon(t)
	c := newStreamClient(t, d)
	ctx := context.Background()
	bootComputer(t, d, "b1")

	// Done (sandbox.release).
	if err := c.Call(ctx, "sandbox.takeover", map[string]string{"botId": "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	id := openTypedScreen(t, c)
	if err := c.Call(ctx, "sandbox.release", map[string]string{"botId": "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the key up after Done", func() bool { got, _, _ := c.screenData(id); return strings.HasSuffix(got, keyUpA) })

	// A viewer lease that ran out while the bot had asked for the takeover: the user keeps the computer.
	if err := c.Call(ctx, "sandbox.takeover", map[string]string{"botId": "b1"}, nil); err != nil {
		t.Fatal(err)
	}
	wctx, stopWait := context.WithCancel(ctx)
	w, err := d.handoffs().Begin(wctx, "b1", "log in", true)
	if err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { defer close(waited); _, _ = w.Wait(wctx) }()
	if err := c.Call(ctx, "sandbox.viewer", map[string]any{"botId": "b1", "watching": true}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * sandboxViewerTTL)
	if !d.Sandbox.TakenOver("b1") {
		t.Fatal("an expired lease took the computer back while the bot waited for the user")
	}
	stopWait()
	<-waited

	// The same lease running out with nothing pending: the computer goes back to the bot, keys released.
	id = openTypedScreen(t, c)
	if err := c.Call(ctx, "sandbox.viewer", map[string]any{"botId": "b1", "watching": true}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the abandoned takeover to end", func() bool { return !d.Sandbox.TakenOver("b1") })
	waitFor(t, "the key up after the lease ran out", func() bool { got, _, _ := c.screenData(id); return strings.HasSuffix(got, keyUpA) })
}

func TestPTYSizeDefaultsAndClamps(t *testing.T) {
	tests := []struct{ cols, rows, wantCols, wantRows int }{
		{0, 0, 120, 30},
		{-5, -1, 120, 30},
		{100, 24, 100, 24},
		{1, 1, 20, 5},
		{9999, 9999, 500, 200},
	}
	for _, tt := range tests {
		if c, r := ptySize(tt.cols, tt.rows); c != tt.wantCols || r != tt.wantRows {
			t.Errorf("ptySize(%d, %d) = %d, %d; want %d, %d", tt.cols, tt.rows, c, r, tt.wantCols, tt.wantRows)
		}
	}
}

func TestSandboxPTYOpensWithTheNormalizedSize(t *testing.T) {
	d := computerDaemon(t)
	c := newStreamClient(t, d)
	bootComputer(t, d, "b1")
	var info terminals.Info
	if err := c.Call(context.Background(), "sandbox.pty.open", map[string]any{"botId": "b1", "cols": 9999, "rows": 0}, &info); err != nil {
		t.Fatalf("pty.open: %v", err)
	}
	if info.Cols != 500 || info.Rows != 30 {
		t.Fatalf("terminal size %dx%d, want 500x30", info.Cols, info.Rows)
	}
}
