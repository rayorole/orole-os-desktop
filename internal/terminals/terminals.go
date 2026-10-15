// Package terminals manages interactive pseudoterminal sessions behind a
// platform-neutral Backend (ConPTY on Windows, native PTY on Linux).
//
// Sessions survive GUI restarts. Native sessions end with the daemon. Opt-in
// Linux tmux sessions can be enumerated/restored after daemon restart while
// their isolated tmux server lives; no backend restores processes after reboot.
package terminals

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Backend starts pseudoterminal processes.
type Backend interface {
	Start(spec StartSpec) (Process, error)
	// Shells lists shells detected on this machine.
	Shells() []Shell
}

// Process is a running pty-attached process.
type Process interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows int) error
	Wait(ctx context.Context) (exitCode int, err error)
	Pid() int
	Close() error
}

// StartSpec describes a process to start.
type StartSpec struct {
	Command    string // full command line
	Cwd        string
	Env        []string
	Cols, Rows int
}

// Shell is a selectable shell.
type Shell struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// State of a session.
type State string

const (
	Running State = "running"
	Exited  State = "exited"
)

// Info is a session snapshot.
type Info struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Shell        string     `json:"shell"`
	Cwd          string     `json:"cwd"`
	Cols         int        `json:"cols"`
	Rows         int        `json:"rows"`
	Pid          int        `json:"pid"`
	State        State      `json:"state"`
	ExitCode     *int       `json:"exitCode,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
	Owner        string     `json:"owner,omitempty"`        // e.g. "agent:<id>" for agent-owned terminals
	PersistentID string     `json:"persistentId,omitempty"` // reconnectable tmux session, if opted in
}

// Sink receives live output and state changes.
//
// Output events are coalesced per session and delivered in order. data holds
// the stream bytes [seq-len(data), seq) and is owned by the receiver. If the
// consumer was too slow for the per-session output budget the oldest
// undelivered bytes are dropped, so an event may start past the end of the
// previous one; such a consumer must call Attach to resynchronize. Delivered
// events never straddle an Attachment's Seq.
type Sink interface {
	Output(id string, seq uint64, data []byte)
	Changed(info Info)
}

// RemovalSink is optionally implemented by a Sink to learn that the manager
// forgot an exited session on its own (retention limit or TTL). Sessions
// removed through Close are not reported.
type RemovalSink interface {
	Removed(id string)
}

// Output pacing. A terminal can produce far more than a GUI can draw, so
// output is coalesced and rate-capped per session instead of being forwarded
// read by read.
const (
	// ScrollbackBytes is the per-session history bound.
	ScrollbackBytes = 1 << 20

	outputFlushDelay  = 12 * time.Millisecond // coalescing window after an event
	outputFlushMax    = 64 << 10              // largest single output event
	outputPendingMax  = 256 << 10             // undelivered bytes kept before the oldest are dropped
	outputBytesPerSec = 2 << 20               // sustained per-session event payload rate

	sessionLimit = 64               // concurrently running sessions
	exitedMax    = 16               // exited sessions kept for Attach
	exitedTTL    = 30 * time.Minute // how long an exited session stays attachable
)

// ErrClosed is returned by Wait when the session was closed before it exited.
var ErrClosed = errors.New("terminals: session closed")

// Manager owns all sessions.
type Manager struct {
	be        Backend
	sink      Sink
	max       int // running sessions; exited sessions do not count
	maxExited int
	exitedTTL time.Duration

	mu      sync.RWMutex
	sess    map[string]*Session
	running int        // sessions that count against max
	exited  []*Session // exited sessions still attachable, oldest first
}

// NewManager creates a manager.
func NewManager(be Backend, sink Sink) *Manager {
	return &Manager{be: be, sink: sink, max: sessionLimit, maxExited: exitedMax, exitedTTL: exitedTTL, sess: map[string]*Session{}}
}

// Shells lists detected shells.
func (m *Manager) Shells() []Shell { return m.be.Shells() }

// Session is one terminal.
type Session struct {
	m *Manager
	p Process

	mu         sync.Mutex
	info       Info
	sb         scrollback  // most recent output
	emitted    uint64      // stream offset below which output was delivered or dropped
	nextFlush  time.Time   // earliest time of the next output event
	timer      *time.Timer // trailing flush
	timerArmed bool

	emitMu sync.Mutex // orders sink.Output calls

	done     chan struct{} // closed once the exit is recorded
	gone     chan struct{} // closed by Close/Shutdown
	goneOnce sync.Once

	// guarded by m.mu
	counted   bool // counts against Manager.max
	reapTimer *time.Timer
}

func (m *Manager) newSession(p Process, info Info) *Session {
	return &Session{m: m, p: p, info: info, sb: scrollback{max: ScrollbackBytes},
		done: make(chan struct{}), gone: make(chan struct{}), counted: true}
}

// CreateOptions configures a new session.
type CreateOptions struct {
	Shell       string // Shell.ID, or "" for default
	Cwd         string
	Cols        int
	Rows        int
	Title       string
	Owner       string
	Command     string // optional explicit command line overriding Shell
	Persistence string // "" for daemon lifetime; "tmux" for Linux tmux server lifetime
}

// Create starts a session. At most 64 sessions run at once; exited sessions do not count.
func (m *Manager) Create(o CreateOptions) (Info, error) {
	if o.Cols <= 0 {
		o.Cols = 120
	}
	if o.Rows <= 0 {
		o.Rows = 30
	}
	cmd := o.Command
	shellName := o.Shell
	if cmd == "" {
		shells := m.be.Shells()
		var sel *Shell
		for i := range shells {
			if (o.Shell == "" && i == 0) || shells[i].ID == o.Shell {
				sel = &shells[i]
				break
			}
		}
		if sel == nil {
			return Info{}, fmt.Errorf("terminals: unknown or unavailable shell %q", o.Shell)
		}
		cmd, shellName = sel.Command, sel.ID
	}
	spec := StartSpec{Command: cmd, Cwd: o.Cwd, Cols: o.Cols, Rows: o.Rows}
	m.mu.Lock()
	if m.running >= m.max {
		m.mu.Unlock()
		return Info{}, errors.New("terminals: session limit reached")
	}
	m.running++ // reserved until registered or the start fails
	m.mu.Unlock()
	var p Process
	var err error
	switch o.Persistence {
	case "":
		p, err = m.be.Start(spec)
	case "tmux":
		be, ok := m.be.(persistentBackend)
		if !ok {
			err = errors.New("terminals: tmux persistence is unavailable on this platform")
			break
		}
		p, err = be.StartPersistent(spec)
	default:
		err = fmt.Errorf("terminals: unknown persistence %q", o.Persistence)
	}
	if err != nil {
		m.mu.Lock()
		m.running--
		m.mu.Unlock()
		return Info{}, err
	}
	info := Info{ID: uuid.NewString(), Title: o.Title, Shell: shellName, Cwd: o.Cwd,
		Cols: o.Cols, Rows: o.Rows, Pid: p.Pid(), State: Running, StartedAt: time.Now().UTC(), Owner: o.Owner}
	if persistent, ok := p.(persistentProcess); ok {
		info.PersistentID = persistent.PersistentID()
	}
	if info.Title == "" {
		info.Title = shellName
	}
	s := m.newSession(p, info)
	m.mu.Lock()
	m.sess[info.ID] = s
	m.mu.Unlock()
	go s.pump()
	return info, nil
}

// AdoptOptions describes a process started outside this manager (the bot computer's guest PTY) that the manager
// should own from now on.
type AdoptOptions struct {
	Title string
	Shell string // label shown as the shell, e.g. "orole-guest"
	Owner string // set by trusted daemon code only, e.g. "sandbox:<botID>"
	Cols  int
	Rows  int
}

// Adopt registers an already running process as a session, so Write, Resize, Attach, Close and the output events
// work for it like for any terminal. It counts against the session cap. The manager owns p from here on: when Adopt
// fails it closes p.
func (m *Manager) Adopt(p Process, o AdoptOptions) (Info, error) {
	if o.Cols <= 0 {
		o.Cols = 120
	}
	if o.Rows <= 0 {
		o.Rows = 30
	}
	m.mu.Lock()
	if m.running >= m.max {
		m.mu.Unlock()
		_ = p.Close()
		return Info{}, errors.New("terminals: session limit reached")
	}
	m.running++
	m.mu.Unlock()
	info := Info{ID: uuid.NewString(), Title: o.Title, Shell: o.Shell, Cols: o.Cols, Rows: o.Rows, Pid: p.Pid(),
		State: Running, StartedAt: time.Now().UTC(), Owner: o.Owner}
	if info.Title == "" {
		info.Title = o.Shell
	}
	s := m.newSession(p, info)
	m.mu.Lock()
	m.sess[info.ID] = s
	m.mu.Unlock()
	go s.pump()
	return info, nil
}

// Inject adds bytes to a running session's output as if its process had written them; they are never written to
// the process. The bot computer uses it to show commands the bot ran in the user's Computer terminal.
func (m *Manager) Inject(id string, data []byte) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	running := s.info.State == Running
	s.mu.Unlock()
	if !running {
		return errors.New("terminals: session has exited")
	}
	if len(data) > 0 {
		s.push(data)
	}
	return nil
}

// readLoop copies pty output into the scrollback. The read buffer is reused,
// so a read allocates nothing; delivery to the sink is paced by push/flush.
func (s *Session) readLoop() {
	chunk := make([]byte, 32<<10)
	for {
		n, err := s.p.Read(chunk)
		if n > 0 {
			s.push(chunk[:n])
		}
		if err != nil {
			return
		}
	}
}

// push records data and delivers it now if the session is idle (so a
// keystroke echo is not delayed), otherwise leaves it to the trailing flush.
func (s *Session) push(data []byte) {
	s.mu.Lock()
	s.sb.write(data)
	if s.sb.end-s.emitted > outputPendingMax {
		s.emitted = s.sb.end - outputPendingMax // drop-oldest: the consumer re-attaches
	}
	ready := !time.Now().Before(s.nextFlush)
	if !ready {
		s.armLocked()
	}
	s.mu.Unlock()
	if ready {
		s.flush(false)
	}
}

// armLocked schedules the trailing flush at nextFlush. s.mu must be held.
func (s *Session) armLocked() {
	if s.timerArmed {
		return
	}
	s.timerArmed = true
	d := max(time.Until(s.nextFlush), 0)
	if s.timer == nil {
		s.timer = time.AfterFunc(d, s.onTimer)
	} else {
		s.timer.Reset(d)
	}
}

func (s *Session) onTimer() {
	s.mu.Lock()
	s.timerArmed = false
	s.mu.Unlock()
	s.flush(false)
}

// flush delivers up to outputFlushMax pending bytes as one event and reports
// whether it sent anything. Unless final, it respects the pacing window and
// re-arms the timer while more bytes remain.
func (s *Session) flush(final bool) bool {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.mu.Lock()
	now := time.Now()
	pending := s.sb.end - s.emitted
	if pending == 0 {
		s.mu.Unlock()
		return false
	}
	if !final && now.Before(s.nextFlush) {
		s.armLocked()
		s.mu.Unlock()
		return false
	}
	n := min(pending, outputFlushMax)
	var arr [4][]byte
	segs, from := s.sb.view(arr[:0], s.emitted, s.emitted+n)
	to := from + uint64(segmentsLen(segs))
	s.emitted = to
	if !final {
		spacing := time.Duration(n) * time.Second / outputBytesPerSec
		s.nextFlush = now.Add(max(outputFlushDelay, spacing))
		if s.sb.end > s.emitted {
			s.armLocked()
		}
	}
	s.mu.Unlock()
	data := copySegments(segs)
	s.unpin()
	if s.m.sink != nil {
		s.m.sink.Output(s.info.ID, to, data)
	}
	return true
}

// unpin releases a view taken with scrollback.view once its bytes were copied.
func (s *Session) unpin() {
	s.mu.Lock()
	s.sb.unpin()
	s.mu.Unlock()
}

func (s *Session) pump() {
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		s.readLoop()
	}()
	// A pty's output pipe stays open after the child exits, so process exit,
	// not read EOF, is the end-of-session signal. Give the reader a moment to
	// drain trailing output, then close the pty, which unblocks Read.
	code, _ := s.p.Wait(context.Background())
	time.Sleep(150 * time.Millisecond)
	s.p.Close()
	<-readDone
	for s.flush(true) { // trailing output precedes the exit event
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.info.State, s.info.ExitCode, s.info.EndedAt = Exited, &code, &now
	info := s.info
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
	s.m.sessionExited(s)
	close(s.done)
	if s.m.sink != nil {
		s.m.sink.Changed(info)
	}
}

// releaseLocked takes s out of the running count and the exited list. m.mu must be held.
func (m *Manager) releaseLocked(s *Session) {
	if s.counted {
		s.counted = false
		m.running--
	}
	if s.reapTimer != nil {
		s.reapTimer.Stop()
		s.reapTimer = nil
	}
	for i, e := range m.exited {
		if e == s {
			m.exited = append(m.exited[:i], m.exited[i+1:]...)
			break
		}
	}
}

// sessionExited frees s's slot under the session cap and keeps it attachable
// for a bounded time: at most maxExited sessions, each for exitedTTL.
func (m *Manager) sessionExited(s *Session) {
	id := s.info.ID
	var evicted []string
	m.mu.Lock()
	if s.counted {
		s.counted = false
		m.running--
	}
	if m.sess[id] == s {
		m.exited = append(m.exited, s)
		s.reapTimer = time.AfterFunc(m.exitedTTL, func() { m.reap(s) })
		for len(m.exited) > m.maxExited {
			old := m.exited[0]
			m.exited = m.exited[1:]
			delete(m.sess, old.info.ID)
			if old.reapTimer != nil {
				old.reapTimer.Stop()
				old.reapTimer = nil
			}
			evicted = append(evicted, old.info.ID)
		}
	}
	m.mu.Unlock()
	for _, id := range evicted {
		m.notifyRemoved(id)
	}
}

// reap forgets an exited session whose TTL ran out.
func (m *Manager) reap(s *Session) {
	m.mu.Lock()
	ok := m.sess[s.info.ID] == s
	if ok {
		delete(m.sess, s.info.ID)
		m.releaseLocked(s)
	}
	m.mu.Unlock()
	if ok {
		m.notifyRemoved(s.info.ID)
	}
}

func (m *Manager) notifyRemoved(id string) {
	if rs, ok := m.sink.(RemovalSink); ok {
		rs.Removed(id)
	}
}

func (m *Manager) get(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sess[id]
	if !ok {
		return nil, fmt.Errorf("terminals: no session %q", id)
	}
	return s, nil
}

// List returns all sessions.
func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.sess))
	for _, s := range m.sess {
		s.mu.Lock()
		out = append(out, s.info)
		s.mu.Unlock()
	}
	return out
}

// Wait blocks until the session has exited and returns its final Info. It
// returns ErrClosed if the session was closed first, and an error if the
// session is unknown (never created, or already forgotten).
func (m *Manager) Wait(ctx context.Context, id string) (Info, error) {
	s, err := m.get(id)
	if err != nil {
		return Info{}, err
	}
	final := func() Info {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.info
	}
	select {
	case <-s.done:
		return final(), nil
	default:
	}
	select {
	case <-s.done:
		return final(), nil
	case <-s.gone:
		return Info{}, ErrClosed
	case <-ctx.Done():
		return Info{}, ctx.Err()
	}
}

// Write sends input (keystrokes, pasted text, or Ctrl-C as "\x03").
func (m *Manager) Write(id string, data []byte) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	running := s.info.State == Running
	s.mu.Unlock()
	if !running {
		return errors.New("terminals: session has exited")
	}
	_, err = s.p.Write(data)
	return err
}

// Resize changes the terminal size.
func (m *Manager) Resize(id string, cols, rows int) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	if cols < 1 || rows < 1 || cols > 1000 || rows > 1000 {
		return errors.New("terminals: invalid size")
	}
	if err := s.p.Resize(cols, rows); err != nil {
		return err
	}
	s.mu.Lock()
	s.info.Cols, s.info.Rows = cols, rows
	s.mu.Unlock()
	return nil
}

// Attachment is a scrollback snapshot plus the sequence number it ends at.
// Live Output events with seq <= Seq are already contained in Data; events
// are never split across Seq, so the first event the client applies starts at
// or after it. Bytes the manager has accepted but not yet delivered are not
// in Data; they arrive in the next Output event.
type Attachment struct {
	Info Info   `json:"info"`
	Data []byte `json:"data"`
	Seq  uint64 `json:"seq"`
}

// Attach returns the scrollback so a (re)connecting client can restore the
// screen. The session lock covers only the snapshot of page headers; the
// bytes are copied after it is released, so a flooding PTY reader is not
// stalled behind a ~1 MiB copy.
func (m *Manager) Attach(id string) (Attachment, error) {
	s, err := m.get(id)
	if err != nil {
		return Attachment{}, err
	}
	var arr [4][]byte
	s.mu.Lock()
	segs, _ := s.sb.view(arr[:0], 0, s.emitted)
	info, seq := s.info, s.emitted
	s.mu.Unlock()
	data := copySegments(segs)
	s.unpin()
	return Attachment{Info: info, Data: data, Seq: seq}, nil
}

// Close kills the process (if running) and forgets the session.
func (m *Manager) Close(id string) error {
	s, err := m.get(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.sess, id)
	m.releaseLocked(s)
	m.mu.Unlock()
	s.goneOnce.Do(func() { close(s.gone) })
	if persistent, ok := s.p.(persistentProcess); ok {
		if err := persistent.Terminate(); err != nil {
			_ = s.p.Close()
			return err
		}
	}
	return s.p.Close()
}

// Shutdown closes native sessions but only detaches persistent tmux clients.
// Use Close to explicitly terminate a persistent session.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	sessions := m.sess
	m.sess = map[string]*Session{}
	for _, s := range sessions {
		m.releaseLocked(s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.goneOnce.Do(func() { close(s.gone) })
		_ = s.p.Close()
	}
}
