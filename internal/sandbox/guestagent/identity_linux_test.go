//go:build linux

package guestagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

func TestSetIdentityWiresCredential(t *testing.T) {
	cmd := exec.Command("true")
	setProcessGroup(cmd)
	if err := setIdentity(cmd, &Identity{Name: "bot", UID: 1000, GID: 100, Groups: []uint32{100, 27}}); err != nil {
		t.Fatal(err)
	}
	a := cmd.SysProcAttr
	if a == nil || !a.Setpgid || a.Credential == nil {
		t.Fatalf("SysProcAttr = %+v", a)
	}
	if c := a.Credential; c.Uid != 1000 || c.Gid != 100 || !slices.Equal(c.Groups, []uint32{100, 27}) || c.NoSetGroups {
		t.Fatalf("Credential = %+v", c)
	}

	plain := exec.Command("true")
	if err := setIdentity(plain, nil); err != nil || plain.SysProcAttr != nil {
		t.Fatalf("nil identity changed the command: %+v %v", plain.SysProcAttr, err)
	}
}

func TestIdentityEnv(t *testing.T) {
	env := identityEnv([]string{"PATH=/bin", "USER=root"}, "/home/bot", &Identity{Name: "bot"})
	want := []string{"PATH=/bin", "USER=root", "HOME=/home/bot", "USER=bot", "LOGNAME=bot"}
	if !slices.Equal(env, want) {
		t.Fatalf("env = %q", env)
	}
	if env := identityEnv([]string{"PATH=/bin"}, "/h", nil); !slices.Equal(env, []string{"PATH=/bin", "HOME=/h"}) {
		t.Fatalf("env without identity = %q", env)
	}
}

// runAsNobody returns an identity for the nobody user and a home it can use; it skips unless the test runs as root.
func runAsNobody(t *testing.T) (*Identity, string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("needs root to switch users")
	}
	u, err := user.Lookup("nobody")
	if err != nil {
		t.Skip("no nobody user:", err)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	home, err := os.MkdirTemp("", "orole-runas-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.Chown(home, uid, gid); err != nil {
		t.Fatal(err)
	}
	return &Identity{Name: u.Username, UID: uint32(uid), GID: uint32(gid)}, home
}

func TestRunAsSwitchesUser(t *testing.T) {
	id, home := runAsNobody(t)
	a := New(Config{Token: "t", Home: home, RunAs: id})

	res, err := a.exec(t.Context(), wire.ExecRequest{Command: `id -un; id -u; echo "$HOME $USER $LOGNAME"; touch made`})
	if err != nil {
		t.Fatal(err)
	}
	want := id.Name + "\n" + strconv.Itoa(int(id.UID)) + "\n" + home + " " + id.Name + " " + id.Name + "\n"
	if res.Stdout != want || res.ExitCode != 0 {
		t.Fatalf("exec as %s: %+v, want stdout %q", id.Name, res, want)
	}
	assertOwner(t, filepath.Join(home, "made"), id.UID)

	p, err := startPTY(PTYSpec{Argv: []string{"/bin/sh"}, Dir: home, Env: identityEnv(os.Environ(), home, id), Cols: 80, Rows: 24, RunAs: id})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	io.WriteString(p, "echo uid=$(id -u) tty=$(stat -c %u $(tty))\n")
	u := strconv.Itoa(int(id.UID))
	readUntil(t, p, "uid="+u+" tty="+u)
}

// The helper's protocol and error mapping, as the current user (no root needed).
func TestFileHelperAsCurrentUser(t *testing.T) {
	a := New(Config{Token: "t", Home: t.TempDir()})
	ctx := t.Context()
	dir := t.TempDir()
	p := filepath.Join(dir, "x/y/f.txt")
	if err := a.writeFileAs(ctx, p, []byte("hello"), 0o640, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("written file: %v %v", st, err)
	}
	if b, err := a.readFileAs(ctx, p, nil); err != nil || string(b) != "hello" {
		t.Fatalf("read back %q %v", b, err)
	}
	if _, err := a.readFileAs(ctx, filepath.Join(dir, "missing"), nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := a.readFileAs(ctx, dir, nil); !errors.Is(err, errIsDir) {
		t.Errorf("dir: %v", err)
	}
	big := filepath.Join(dir, "big")
	if err := os.WriteFile(big, bytes.Repeat([]byte("z"), wire.MaxReadFile+100), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, err := a.readFileAs(ctx, big, nil); err != nil || len(b) != wire.MaxReadFile+1 {
		t.Errorf("big: len %d %v (want the cap plus one byte, which marks truncation)", len(b), err)
	}
	if os.Geteuid() != 0 { // root ignores file modes
		locked := filepath.Join(dir, "locked")
		if err := os.WriteFile(locked, []byte("s"), 0o000); err != nil {
			t.Fatal(err)
		}
		if _, err := a.readFileAs(ctx, locked, nil); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("locked read: %v", err)
		}
		if err := a.writeFileAs(ctx, locked, []byte("x"), 0o644, nil); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("locked write: %v", err)
		}
	}
}

// With RunAs, /files/* has the bot's permissions: new files are the bot's, root's files and symlinks to them are
// out of reach (403), and the HTTP statuses stay the same.
func TestFilesRunAsBot(t *testing.T) {
	id, home := runAsNobody(t)
	a := New(Config{Token: "t", Home: home, RunAs: id})
	rootDir, err := os.MkdirTemp("", "orole-rootonly-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(rootDir) })
	if err := os.Chmod(rootDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(rootDir, "rootfile")
	secret := filepath.Join(rootDir, "secret")
	if err := os.WriteFile(rootFile, []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rootFile, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}

	write := func(path string) int {
		return callStatus(t, a, "/files/write", wire.WriteFileRequest{Path: path, Content: []byte("x")})
	}
	read := func(path string) int { return callStatus(t, a, "/files/read", wire.ReadFileRequest{Path: path}) }
	if c := write("a/b/new.txt"); c != 200 {
		t.Fatalf("write in home: %d", c)
	}
	assertOwner(t, filepath.Join(home, "a"), id.UID)
	assertOwner(t, filepath.Join(home, "a/b"), id.UID)
	assertOwner(t, filepath.Join(home, "a/b/new.txt"), id.UID)
	for _, p := range []string{rootFile, "link", filepath.Join(rootDir, "new")} {
		if c := write(p); c != 403 {
			t.Errorf("write %s: %d, want 403", p, c)
		}
	}
	if b, _ := os.ReadFile(rootFile); string(b) != "root" {
		t.Errorf("root file changed: %q", b)
	}
	if _, err := os.Lstat(filepath.Join(rootDir, "new")); err == nil {
		t.Error("bot created a file in a root directory")
	}
	for p, want := range map[string]int{secret: 403, rootFile: 200, "missing": 404, "a": 400, "a/b/new.txt": 200} {
		if c := read(p); c != want {
			t.Errorf("read %s: %d, want %d", p, c, want)
		}
	}
}

func assertOwner(t *testing.T, p string, uid uint32) {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Sys().(*syscall.Stat_t).Uid; got != uid {
		t.Errorf("%s owned by %d, want %d", p, got, uid)
	}
}

func callStatus(t *testing.T, a *Agent, path string, body any) int {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "http://guest"+path, bytes.NewReader(b))
	req.Header.Set(wire.TokenHeader, a.cfg.Token)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec.Code
}
