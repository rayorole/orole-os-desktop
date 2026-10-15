package guestagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// The file helper is the agent's own binary started again, as Config.RunAs, with a hidden first argument. It
// does one file operation with that user's permissions: content travels on stdin (write) or stdout (read), and
// the exit code says what went wrong. The agent still enforces every size limit itself.
const (
	helperRead  = "__orole-file-read"  // <path>: the file on stdout (at most MaxReadFile+1 bytes)
	helperWrite = "__orole-file-write" // <path> <octal mode>: stdin into the file, parent directories created

	helperTimeout = 2 * time.Minute // a FIFO or a stuck network mount must not hold a request forever
)

// Helper exit codes; anything else is an internal error.
const (
	exitNotExist   = 3
	exitPermission = 4
	exitIsDir      = 5
	exitTooLarge   = 6
)

// RunFileHelper runs the file helper when args (os.Args[1:]) start with a helper argument and reports whether
// it did; the caller exits with the code. cmd/orole-guest and test binaries that re-exec themselves call it
// first thing.
func RunFileHelper(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, ok bool) {
	if len(args) == 0 || (args[0] != helperRead && args[0] != helperWrite) {
		return 0, false
	}
	err := runFileHelper(args, stdin, stdout)
	if err == nil {
		return 0, true
	}
	fmt.Fprintln(stderr, err)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return exitNotExist, true
	case errors.Is(err, fs.ErrPermission):
		return exitPermission, true
	case errors.Is(err, errIsDir):
		return exitIsDir, true
	case errors.Is(err, errTooLarge):
		return exitTooLarge, true
	}
	return 1, true
}

var errTooLarge = errors.New("file too large")

func runFileHelper(args []string, stdin io.Reader, stdout io.Writer) error {
	switch {
	case args[0] == helperRead && len(args) == 2:
		f, err := openForRead(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(stdout, io.LimitReader(f, wire.MaxReadFile+1))
		return err
	case args[0] == helperWrite && len(args) == 3:
		mode, err := strconv.ParseUint(args[2], 8, 32)
		if err != nil {
			return fmt.Errorf("bad mode %q", args[2])
		}
		content, err := io.ReadAll(io.LimitReader(stdin, wire.MaxWriteFile+1))
		if err != nil {
			return err
		}
		if len(content) > wire.MaxWriteFile {
			return errTooLarge
		}
		return writeFile(args[1], content, fs.FileMode(mode).Perm())
	}
	return fmt.Errorf("bad file helper arguments %q", args)
}

func (a *Agent) readFileAs(ctx context.Context, p string, id *Identity) ([]byte, error) {
	return a.runHelper(ctx, id, nil, wire.MaxReadFile+1, helperRead, p)
}

func (a *Agent) writeFileAs(ctx context.Context, p string, content []byte, mode fs.FileMode, id *Identity) error {
	_, err := a.runHelper(ctx, id, content, 0, helperWrite, p, strconv.FormatUint(uint64(mode.Perm()), 8))
	return err
}

// runHelper starts the helper as id (nil: as this process's user), feeds it stdin, and returns at most
// stdoutLimit bytes of its output; a helper that writes more is killed (the caller reports truncation).
func (a *Agent) runHelper(ctx context.Context, id *Identity, stdin []byte, stdoutLimit int64, args ...string) ([]byte, error) {
	exe := a.cfg.HelperExe
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return nil, helperStartError(err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(stdin)
	stderr := newHelperStderr()
	cmd.Stderr = stderr
	if err := setIdentity(cmd, id); err != nil {
		return nil, helperStartError(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, helperStartError(err)
	}
	if err := cmd.Start(); err != nil {
		return nil, helperStartError(err)
	}
	data, readErr := io.ReadAll(io.LimitReader(out, stdoutLimit+1))
	over := int64(len(data)) > stdoutLimit
	if over {
		_ = cmd.Process.Kill()
		data = data[:stdoutLimit]
	}
	waitErr := cmd.Wait()
	if over {
		return data, nil
	}
	if waitErr == nil && readErr != nil {
		waitErr = readErr
	}
	if waitErr == nil {
		return data, nil
	}
	msg := strings.TrimSpace(stderr.String())
	if len(msg) > 512 {
		msg = msg[:512]
	}
	var exit *exec.ExitError
	if !errors.As(waitErr, &exit) {
		return nil, fmt.Errorf("file helper: %w", waitErr)
	}
	switch exit.ExitCode() {
	case exitNotExist:
		return nil, helperError{msg, fs.ErrNotExist}
	case exitPermission:
		return nil, helperError{msg, fs.ErrPermission}
	case exitIsDir:
		return nil, errIsDir
	case exitTooLarge:
		return nil, errTooLarge
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("file helper: %w", ctx.Err())
	}
	return nil, fmt.Errorf("file helper: %s (%v)", msg, waitErr)
}

// helperStartError reports that the helper could not run at all. It deliberately does not wrap err: a missing
// helper binary (ENOENT) or a failed user switch (EPERM) is the agent's fault, a 500, not the requested file's
// 404 or 403.
func helperStartError(err error) error {
	return fmt.Errorf("file helper could not start: %v", err)
}

// helperStderrMax bounds the helper's stderr kept in memory; only its first 512 bytes reach the error message.
const helperStderrMax = 4 << 10

// newHelperStderr collects at most helperStderrMax bytes of the helper's stderr and drops the rest (reporting it
// written, so the helper never blocks on a full pipe).
func newHelperStderr() *capWriter { return newCapped(helperStderrMax).writer() }

// helperError carries the helper's own message and the class its exit code stands for.
type helperError struct {
	msg  string
	kind error
}

func (e helperError) Error() string { return e.msg }
func (e helperError) Unwrap() error { return e.kind }
