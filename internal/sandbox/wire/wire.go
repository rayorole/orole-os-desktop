// Package wire holds the request and response types of the protocol between the daemon and orole-guest, the
// agent inside a bot's VM. The transport is HTTP/2 without TLS (h2c) over the VM's one byte stream; every
// request carries TokenHeader. See docs/architecture/bot-computer.md.
package wire

const (
	TokenHeader = "X-Orole-Token" // per-boot secret; the guest answers 401 without it
	PTYHeader   = "X-Orole-Pty"   // response header of POST /pty: the terminal id for /pty/resize
	ExitTrailer = "X-Orole-Exit"  // response trailer of POST /pty: the shell's exit code

	DefaultExecTimeout = 120      // seconds
	MaxExecTimeout     = 600      // seconds
	MaxExecOutput      = 1 << 20  // stdout+stderr bytes kept per command
	MaxReadFile        = 4 << 20  // bytes returned by /files/read
	MaxWriteFile       = 16 << 20 // bytes accepted by /files/write

	Home = "/home/bot" // relative guest paths resolve here
)

type Health struct {
	Version string `json:"version"`
	Ready   bool   `json:"ready"`
}

type ExecRequest struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds; see ClampTimeout
	Cwd     string `json:"cwd,omitempty"`
}

type ExecResult struct {
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exitCode"`
	TimedOut  bool   `json:"timedOut,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type ReadFileRequest struct {
	Path string `json:"path"`
}

type ReadFileResult struct {
	Content   []byte `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
}

type WriteFileRequest struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
	Mode    uint32 `json:"mode,omitempty"` // permission bits; 0 means 0644
}

type WriteFileResult struct {
	Bytes int `json:"bytes"`
}

type ResizeRequest struct {
	ID   string `json:"id"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type BrowserRequest struct {
	Action string `json:"action"` // open, click, type, read, screenshot
	URL    string `json:"url,omitempty"`
	Target string `json:"target,omitempty"`
	Text   string `json:"text,omitempty"`
}

type BrowserResult struct {
	Text  string `json:"text,omitempty"`
	URL   string `json:"url,omitempty"`
	Title string `json:"title,omitempty"`
	PNG   []byte `json:"png,omitempty"`
}

// ClampTimeout returns the exec timeout in seconds that the guest applies to a requested value.
func ClampTimeout(seconds int) int {
	switch {
	case seconds <= 0:
		return DefaultExecTimeout
	case seconds > MaxExecTimeout:
		return MaxExecTimeout
	}
	return seconds
}
