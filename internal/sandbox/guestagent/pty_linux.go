//go:build linux

package guestagent

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// startPTY starts argv on a new pseudo-terminal, as s.RunAs when set.
func startPTY(s PTYSpec) (PTY, error) {
	if len(s.Argv) == 0 {
		return nil, errors.New("no shell configured")
	}
	cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	f, err := startOnPTY(cmd, s)
	if err != nil {
		return nil, err
	}
	p := &linuxPTY{f: f, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		p.code = cmd.ProcessState.ExitCode()
		close(p.done)
	}()
	return p, nil
}

// startOnPTY is pty.StartWithSize plus running as s.RunAs: the shell gets its own session with the terminal as
// its controlling tty, and the terminal belongs to the user it runs as (as login does), so tools that check
// their tty (mesg, ssh, sudo-like prompts) work.
func startOnPTY(cmd *exec.Cmd, s PTYSpec) (*os.File, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, err
	}
	defer tty.Close()
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(s.Cols), Rows: uint16(s.Rows)}); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	if s.RunAs != nil {
		if err := tty.Chown(int(s.RunAs.UID), -1); err != nil {
			_ = ptmx.Close()
			return nil, err
		}
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := setIdentity(cmd, s.RunAs); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = ptmx.Close()
		return nil, err
	}
	return ptmx, nil
}

type linuxPTY struct {
	f    *os.File
	cmd  *exec.Cmd
	once sync.Once
	done chan struct{}
	code int
}

func (p *linuxPTY) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if errors.Is(err, syscall.EIO) { // Linux reports a terminal whose shell exited as EIO
		err = io.EOF
	}
	return n, err
}

func (p *linuxPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *linuxPTY) Resize(cols, rows int) error {
	return pty.Setsize(p.f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

// Close kills the shell's process group, closes the terminal, and kills the shell if it is still there 2 s later.
func (p *linuxPTY) Close() error {
	p.once.Do(func() {
		// The shell leads its session. A login shell on a tty is interactive and may have job control, which puts
		// background jobs in their own process groups; those survive the group kill below until the VM stops, as
		// do processes that left the session (setsid). Killing the shell's group closes the terminal slave, which
		// is what ends a blocked master Read (closing the master file does not interrupt it). ESRCH (group
		// already gone) is fine.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_ = p.f.Close()
		go func() {
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				_ = p.cmd.Process.Kill()
			}
		}()
	})
	return nil
}

func (p *linuxPTY) Wait() (int, error) {
	<-p.done
	return p.code, nil
}
