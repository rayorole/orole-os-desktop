package wire

import (
	"encoding/json"
	"testing"
)

func TestClampTimeout(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, DefaultExecTimeout},
		{-5, DefaultExecTimeout},
		{1, 1},
		{600, 600},
		{601, MaxExecTimeout},
		{100000, MaxExecTimeout},
	} {
		if got := ClampTimeout(tc.in); got != tc.want {
			t.Errorf("ClampTimeout(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// The guest and the host are built separately; the JSON names are the protocol and must not drift.
func TestJSONNames(t *testing.T) {
	for _, tc := range []struct {
		v    any
		want string
	}{
		{ExecRequest{Command: "ls", Timeout: 5, Cwd: "w"}, `{"command":"ls","timeout":5,"cwd":"w"}`},
		{ExecResult{Stdout: "o", Stderr: "e", ExitCode: 2, TimedOut: true, Truncated: true}, `{"stdout":"o","stderr":"e","exitCode":2,"timedOut":true,"truncated":true}`},
		{ReadFileRequest{Path: "a"}, `{"path":"a"}`},
		{ReadFileResult{Content: []byte("hi")}, `{"content":"aGk="}`},
		{WriteFileRequest{Path: "a", Content: []byte("hi"), Mode: 0o600}, `{"path":"a","content":"aGk=","mode":384}`},
		{WriteFileResult{Bytes: 2}, `{"bytes":2}`},
		{ResizeRequest{ID: "p", Cols: 80, Rows: 24}, `{"id":"p","cols":80,"rows":24}`},
		{Health{Version: "1", Ready: true}, `{"version":"1","ready":true}`},
		{BrowserRequest{Action: "open", URL: "u"}, `{"action":"open","url":"u"}`},
	} {
		b, err := json.Marshal(tc.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != tc.want {
			t.Errorf("%T = %s, want %s", tc.v, b, tc.want)
		}
	}
}
