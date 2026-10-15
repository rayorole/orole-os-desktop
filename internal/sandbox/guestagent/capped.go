package guestagent

import (
	"bytes"
	"sync"
)

// capped is a byte budget shared by a command's stdout and stderr: writes beyond it are dropped (and reported as
// written, so the command never blocks on a full pipe).
type capped struct {
	mu   sync.Mutex
	left int
	cut  bool
}

func newCapped(n int) *capped { return &capped{left: n} }

func (c *capped) writer() *capWriter { return &capWriter{c: c} }

func (c *capped) truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cut
}

type capWriter struct {
	c   *capped
	buf bytes.Buffer
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	n := min(len(p), w.c.left)
	w.buf.Write(p[:n])
	w.c.left -= n
	if n < len(p) {
		w.c.cut = true
	}
	return len(p), nil
}

func (w *capWriter) String() string {
	w.c.mu.Lock()
	defer w.c.mu.Unlock()
	return w.buf.String()
}
