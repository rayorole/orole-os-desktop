package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/daemon/api"
	"github.com/rayorole/orole-os-desktop/internal/sandbox"
)

func TestForwardSandbox(t *testing.T) {
	for _, ev := range []string{"sandbox.status", "sandbox.screen.data", "sandbox.availability", "sandbox.runtime",
		"sandbox.setup", "sandbox.handoff"} {
		if !forwardSandbox(ev, json.RawMessage(`{"id":"s1","botId":"b1","data":"AQID"}`)) {
			t.Fatalf("%q must be routed", ev)
		}
		if !forwardSandbox(ev, json.RawMessage(`not json`)) {
			t.Fatalf("malformed %q is still consumed", ev)
		}
	}
	for _, ev := range []string{"sandbox.other", "terminal.output", ""} {
		if forwardSandbox(ev, json.RawMessage(`{}`)) {
			t.Fatalf("%q must not be routed", ev)
		}
	}
}

func TestScreenInputRejectsNonBase64(t *testing.T) {
	err := Sandbox{L: NewLink(t.TempDir())}.ScreenInput(t.Context(), "s1", "not base64!")
	if err == nil {
		t.Fatal("non-base64 input was sent")
	}
}

// recordedCall is one IPC call a fake call function saw.
type recordedCall struct {
	method string
	params any
}

func TestEnableAllowsTheDaemonForegroundFirst(t *testing.T) {
	var order []string
	call := func(_ context.Context, method string, _ any, out any) error {
		order = append(order, method)
		switch method {
		case "daemon.health":
			out.(*api.Health).PID = 4242
		case "sandbox.setup.enable":
			out.(*sandbox.SetupState).Running = true
		}
		return nil
	}
	allow := func(pid int) error {
		if pid != 4242 {
			t.Fatalf("allowed pid %d, want the daemon's 4242", pid)
		}
		order = append(order, "allow")
		return nil
	}
	st, err := enableSandbox(t.Context(), call, allow)
	if err != nil || !st.Running {
		t.Fatalf("enable = %+v, %v; want the daemon's running state", st, err)
	}
	want := []string{"daemon.health", "allow", "sandbox.setup.enable"}
	if len(order) != len(want) {
		t.Fatalf("calls %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("calls %v, want %v", order, want)
		}
	}
}

func TestEnableStillRunsWhenTheForegroundGrantFails(t *testing.T) {
	for name, tc := range map[string]struct {
		healthErr error
		pid       int
		allowErr  error
		wantAllow bool
	}{
		"health fails":  {healthErr: errors.New("down")},
		"no pid":        {pid: 0},
		"allow refused": {pid: 7, allowErr: errors.New("denied"), wantAllow: true},
	} {
		t.Run(name, func(t *testing.T) {
			var enabled, allowed bool
			call := func(_ context.Context, method string, _ any, out any) error {
				switch method {
				case "daemon.health":
					if tc.healthErr != nil {
						return tc.healthErr
					}
					out.(*api.Health).PID = tc.pid
				case "sandbox.setup.enable":
					enabled = true
				}
				return nil
			}
			allow := func(int) error { allowed = true; return tc.allowErr }
			if _, err := enableSandbox(t.Context(), call, allow); err != nil {
				t.Fatalf("enable: %v", err)
			}
			if !enabled {
				t.Fatal("sandbox.setup.enable was not called")
			}
			if allowed != tc.wantAllow {
				t.Fatalf("allow called = %v, want %v", allowed, tc.wantAllow)
			}
		})
	}
}

func TestScreenInputSplitsIntoChunks(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 2*sandbox.ScreenChunkMax+5)
	var got []recordedCall
	call := func(_ context.Context, method string, params any, _ any) error {
		got = append(got, recordedCall{method, params})
		return nil
	}
	if err := screenInput(t.Context(), call, "s1", base64.StdEncoding.EncodeToString(payload)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d calls, want 3", len(got))
	}
	var joined []byte
	for _, c := range got {
		if c.method != "sandbox.screen.input" {
			t.Fatalf("method %q", c.method)
		}
		p := c.params.(screenInputParams)
		if p.ID != "s1" || len(p.Data) == 0 || len(p.Data) > sandbox.ScreenChunkMax {
			t.Fatalf("chunk id %q len %d", p.ID, len(p.Data))
		}
		joined = append(joined, p.Data...)
	}
	if !bytes.Equal(joined, payload) {
		t.Fatal("chunks do not add up to the input")
	}
}

func TestScreenInputStopsAtTheFirstError(t *testing.T) {
	payload := bytes.Repeat([]byte{1}, sandbox.ScreenChunkMax+1)
	n := 0
	call := func(context.Context, string, any, any) error { n++; return errors.New("closed") }
	if err := screenInput(t.Context(), call, "s1", base64.StdEncoding.EncodeToString(payload)); err == nil {
		t.Fatal("error was lost")
	}
	if n != 1 {
		t.Fatalf("%d calls after a failure, want 1", n)
	}
}

func TestScreenInputEmptyIsANoOp(t *testing.T) {
	call := func(context.Context, string, any, any) error { t.Fatal("empty input was sent"); return nil }
	if err := screenInput(t.Context(), call, "s1", ""); err != nil {
		t.Fatal(err)
	}
}

func TestEnableBoundsTheHealthCall(t *testing.T) {
	old := enableHealthTimeout
	enableHealthTimeout = 20 * time.Millisecond
	t.Cleanup(func() { enableHealthTimeout = old })
	enabled := false
	call := func(ctx context.Context, method string, _ any, _ any) error {
		switch method {
		case "daemon.health":
			if _, ok := ctx.Deadline(); !ok {
				t.Error("daemon.health called without a deadline")
			}
			<-ctx.Done() // a daemon that never answers
			return ctx.Err()
		case "sandbox.setup.enable":
			if ctx.Err() != nil {
				t.Error("setup called with the expired health context")
			}
			enabled = true
		}
		return nil
	}
	if _, err := enableSandbox(t.Context(), call, func(int) error { t.Error("allow called"); return nil }); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !enabled {
		t.Fatal("sandbox.setup.enable was not called after the health call timed out")
	}
}
