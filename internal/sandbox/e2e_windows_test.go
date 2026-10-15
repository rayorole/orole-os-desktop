//go:build windows

package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

const e2eRepo = "rayorole/orole-os-desktop"

// TestRealGuestImageThroughTheManager boots the released NixOS guest image (DefaultPins) on this PC through the
// real Runtime install (hash checks, unzip), Manager and host backend (QEMU + WHPX). It needs WHP on, about
// 5 GB of free RAM and the gh CLI signed in (draft releases download with repository access). Assets are kept
// in OROLE_SANDBOX_E2E_ASSETS (default %TEMP%\orole-realboot\<tag>\<name>) and downloaded only when missing
// or not matching the pins. Run by hand:
//
//	$env:OROLE_SANDBOX_E2E = "1"
//	go test -run TestRealGuestImageThroughTheManager -v -timeout 30m ./internal/sandbox
//
// Lines starting with "E2E RESULT" are the measurements docs/architecture/bot-computer.md records.
func TestRealGuestImageThroughTheManager(t *testing.T) {
	if os.Getenv("OROLE_SANDBOX_E2E") == "" {
		t.Skip("set OROLE_SANDBOX_E2E=1 to boot the released guest image (downloads ~1.6 GB once)")
	}
	pins := DefaultPins
	if !pins.Complete() {
		t.Fatal("this build has no DefaultPins")
	}
	assets := os.Getenv("OROLE_SANDBOX_E2E_ASSETS")
	if assets == "" {
		assets = filepath.Join(os.TempDir(), "orole-realboot")
	}
	e2eFetchAssets(t, assets, pins.QEMUTag, []Asset{pins.QEMU})
	e2eFetchAssets(t, assets, pins.ImageTag, pins.Image)

	// The release server is a local stand-in for github.com serving the verified files at the same paths.
	srv := httptest.NewServer(http.FileServer(http.Dir(assets)))
	defer srv.Close()
	root := shortRoot(t)
	paths := Paths{Root: filepath.Join(root, "sandbox")}
	rt := NewRuntime(RuntimeConfig{Paths: paths, Pins: pins, BaseURL: srv.URL})
	start := time.Now()
	if err := rt.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("E2E RESULT install (local copy, verify, unzip) = %s", time.Since(start).Round(time.Millisecond))

	backend := NewHostBackend(rt, paths)
	if av := backend.Availability(context.Background()); !av.Ready {
		t.Skipf("this PC cannot run the computer: %+v", av)
	}
	m := NewManager(Config{Paths: paths, Image: rt.Image, BootTimeout: 3 * time.Minute, IdleTimeout: time.Hour}, backend, NewStore(testDB(t)))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = m.Shutdown(ctx)
	}()
	ctx := context.Background()
	var tokens []string

	ensure := func(botID, label string) *Guest {
		t.Helper()
		start := time.Now()
		g, err := m.Ensure(ctx, botID)
		if err != nil {
			log, _ := os.ReadFile(paths.Log(root, botID))
			t.Fatalf("%s: %v\n--- console (tail) ---\n%s", label, err, logTail(log, 6000))
		}
		tokens = append(tokens, g.token)
		t.Logf("E2E RESULT %s: boot to guest ready = %s", label, time.Since(start).Round(time.Millisecond))
		return g
	}
	run := func(g *Guest, cmd string) wire.ExecResult {
		t.Helper()
		res, err := g.Exec(ctx, wire.ExecRequest{Command: cmd, Timeout: 60})
		if err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		return res
	}
	waitFor := func(g *Guest, what, cmd, want string, limit time.Duration) string {
		t.Helper()
		start := time.Now()
		var out string
		for {
			res := run(g, cmd)
			out = res.Stdout + res.Stderr
			if strings.Contains(res.Stdout, want) {
				t.Logf("E2E RESULT %s up %s after guest ready", what, time.Since(start).Round(time.Millisecond))
				return res.Stdout
			}
			if time.Since(start) > limit {
				t.Fatalf("%s not up after %s: %q", what, limit, out)
			}
			time.Sleep(time.Second)
		}
	}

	g := ensure("e2e-bot", "first boot (fresh overlay)")
	t.Logf("E2E RESULT QEMU process after first boot: %s", qemuMemory(t, m, "e2e-bot"))

	if out := strings.TrimSpace(run(g, "id -un").Stdout); out != "bot" {
		t.Fatalf("id -un = %q, want bot", out)
	}
	if out := strings.TrimSpace(run(g, `python3 -c "print(1+1)"`).Stdout); out != "2" {
		t.Fatalf("python3: %q", out)
	}
	cdp := waitFor(g, "Chromium CDP (127.0.0.1:9222)", "curl -s http://127.0.0.1:9222/json/version || true", `"Browser"`, 90*time.Second)
	t.Logf("E2E RESULT chromium: %s", firstLine(cdp, `"Browser"`))
	ss := waitFor(g, "wayvnc (127.0.0.1:5900)", "ss -ltn", "127.0.0.1:5900", 60*time.Second)
	for _, line := range strings.Split(ss, "\n") {
		if strings.Contains(line, ":5900") && !strings.Contains(line, "127.0.0.1:5900") {
			t.Fatalf("wayvnc listens beyond guest loopback: %q", line)
		}
	}
	fonts := run(g, "fc-list | wc -l; fc-list : family | sort -u | head -5")
	if n, _ := strconv.Atoi(strings.TrimSpace(strings.SplitN(fonts.Stdout, "\n", 2)[0])); n == 0 {
		t.Fatalf("no fonts: %q %q", fonts.Stdout, fonts.Stderr)
	}
	t.Logf("E2E RESULT fonts: %s", strings.Join(strings.Fields(strings.ReplaceAll(fonts.Stdout, "\n", " | ")), " "))
	// Only root (orole-guest) reads the token and opens the channel; bot cannot.
	const tokenFile = "/sys/firmware/qemu_fw_cfg/by_name/opt/orole/token/raw"
	if out := strings.TrimSpace(run(g, "stat -c '%a %U' "+tokenFile+"; stat -L -c '%a %U' /dev/virtio-ports/"+guestPortName).Stdout); out != "400 root\n600 root" {
		t.Fatalf("token file and channel modes = %q, want 400 root and 600 root", out)
	}
	if res := run(g, "cat "+tokenFile); res.ExitCode == 0 || res.Stdout != "" {
		t.Fatalf("bot read the token file: %+v", res)
	}
	units := run(g, "systemctl is-active cage-tty1 wayvnc orole-guest | tr '\\n' ' '; systemd-analyze 2>&1 | head -1; df -h / | tail -1")
	t.Logf("E2E RESULT units, boot time, disk: %s", strings.Join(strings.Fields(units.Stdout), " "))
	if strings.Contains(units.Stdout, "inactive") || strings.Contains(units.Stdout, "failed") {
		t.Fatalf("a guest service is down: %q", units.Stdout)
	}
	tools := run(g, "python3 --version; node --version; uv --version; git --version; bash -c 'echo bash ok'")
	if tools.ExitCode != 0 {
		t.Fatalf("toolchain: %+v", tools)
	}
	t.Logf("E2E RESULT tools as bot: %s", strings.Join(strings.Fields(strings.ReplaceAll(tools.Stdout, "\n", " | ")), " "))
	t.Logf("E2E RESULT guest memory (free -m, used/total): %s",
		strings.TrimSpace(run(g, "free -m | awk '/Mem:/{print $3\" MB used of \"$2\" MB\"}'").Stdout))

	// The host's loopback (10.0.2.2 in QEMU user networking) is out of reach: nothing connects to a listener there.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
			accepted <- struct{}{}
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	hostRes := run(g, fmt.Sprintf("curl -sS -m 5 -o /dev/null http://10.0.2.2:%d/; echo exit=$?", port))
	select {
	case <-accepted:
		t.Fatalf("the guest reached the host's loopback: %+v", hostRes)
	case <-time.After(500 * time.Millisecond):
	}
	if strings.Contains(hostRes.Stdout, "exit=0") {
		t.Fatalf("curl to 10.0.2.2 succeeded: %+v", hostRes)
	}
	t.Logf("E2E RESULT host loopback 10.0.2.2:%d blocked: %s %s", port, strings.TrimSpace(hostRes.Stdout), strings.TrimSpace(hostRes.Stderr))
	inet := run(g, "curl -sS -I -m 20 -o /dev/null -w 'http=%{http_code}' https://example.com 2>&1; echo \" exit=$?\"")
	t.Logf("E2E RESULT internet (curl -sI https://example.com): %s", strings.Join(strings.Fields(inet.Stdout+" "+inet.Stderr), " "))

	// Files are written as bot, in its home.
	if _, err := g.WriteFile(ctx, "persist.txt", []byte("kept")); err != nil {
		t.Fatal(err)
	}
	if owner := strings.TrimSpace(run(g, "stat -c %U ~/persist.txt").Stdout); owner != "bot" {
		t.Fatalf("persist.txt owner %q, want bot", owner)
	}
	run(g, "sync")

	// Clean stop, then the same overlay boots again with the file.
	start = time.Now()
	e2eStop(t, m, "e2e-bot")
	t.Logf("E2E RESULT clean shutdown = %s", time.Since(start).Round(time.Millisecond))
	g = ensure("e2e-bot", "second boot (same overlay)")
	if f, err := g.ReadFile(ctx, "persist.txt"); err != nil || string(f.Content) != "kept" {
		t.Fatalf("persist.txt after a clean restart: %q %v", f.Content, err)
	}

	// Hard kill (what a crashed daemon's job object does), then the overlay still boots and kept a synced file.
	if _, err := g.WriteFile(ctx, "marker.txt", []byte("synced before the kill")); err != nil {
		t.Fatal(err)
	}
	run(g, "sync")
	e2eKill(t, m, "e2e-bot")
	g = ensure("e2e-bot", "boot after a hard kill")
	if f, err := g.ReadFile(ctx, "marker.txt"); err != nil || string(f.Content) != "synced before the kill" {
		t.Fatalf("marker.txt after a hard kill: %q %v", f.Content, err)
	}

	// Two bots run at once on the one read-only base image.
	baseBefore, _ := os.Stat(filepath.Join(paths.Image(pins.ImageVersion), imageBase))
	g2 := ensure("e2e-other", "second bot (shared base image)")
	if out := strings.TrimSpace(run(g2, "id -un; test -e ~/persist.txt && echo leaked || echo separate").Stdout); out != "bot\nseparate" {
		t.Fatalf("second bot: %q", out)
	}
	if out := strings.TrimSpace(run(g, "cat ~/marker.txt").Stdout); out != "synced before the kill" {
		t.Fatalf("first bot while the second runs: %q", out)
	}
	t.Logf("E2E RESULT QEMU processes with two bots: e2e-bot %s; e2e-other %s", qemuMemory(t, m, "e2e-bot"), qemuMemory(t, m, "e2e-other"))
	baseAfter, _ := os.Stat(filepath.Join(paths.Image(pins.ImageVersion), imageBase))
	if baseBefore == nil || baseAfter == nil || !baseAfter.ModTime().Equal(baseBefore.ModTime()) || baseAfter.Size() != baseBefore.Size() {
		t.Fatal("the shared base image changed")
	}
	for _, id := range []string{"e2e-bot", "e2e-other"} {
		if !pathExists(paths.Overlay(id)) {
			t.Fatalf("no overlay for %s", id)
		}
	}

	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	if err := m.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	cancel()

	// The per-boot tokens never reach a console log.
	for _, id := range []string{"e2e-bot", "e2e-other"} {
		log, err := os.ReadFile(paths.Log(root, id))
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range tokens {
			if strings.Contains(string(log), tok) {
				t.Fatalf("a guest token is in %s", paths.Log(root, id))
			}
		}
		t.Logf("E2E RESULT %s console log: %d bytes, no token", id, len(log))
	}
}

// e2eFetchAssets makes dir\<tag>\<name> match the pins, downloading with gh what is missing or different.
func e2eFetchAssets(t *testing.T, dir, tag string, want []Asset) {
	t.Helper()
	tagDir := filepath.Join(dir, tag)
	for _, a := range want {
		p := filepath.Join(tagDir, a.Name)
		if e2eMatches(p, a) == nil {
			continue
		}
		t.Logf("downloading %s/%s (%d MB)", tag, a.Name, a.Size>>20)
		cmd := exec.Command("gh", "release", "download", tag, "--repo", e2eRepo, "--pattern", a.Name, "--dir", tagDir, "--clobber")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("gh release download %s %s: %v\n%s", tag, a.Name, err, out)
		}
		if err := e2eMatches(p, a); err != nil {
			t.Fatalf("%s/%s does not match DefaultPins: %v", tag, a.Name, err)
		}
	}
}

func e2eMatches(p string, a Asset) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != a.Size {
		return fmt.Errorf("%d bytes, pinned %d", n, a.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("sha256 %s, pinned %s", got, a.SHA256)
	}
	return nil
}

// e2eStop stops one bot the way the idle reaper does (power off through the agent, then QMP, then kill).
func e2eStop(t *testing.T, m *Manager, botID string) {
	t.Helper()
	m.mu.Lock()
	inst := m.vms[botID]
	if inst == nil || inst.state != StateRunning {
		m.mu.Unlock()
		t.Fatalf("%s is not running", botID)
	}
	m.beginStopLocked(inst)
	m.mu.Unlock()
	m.finishStop(inst, errStopped)
	if st := m.Status(botID); st.State != StateOff {
		t.Fatalf("after stop: %+v", st)
	}
}

// e2eKill kills one bot's QEMU and waits until the manager has noticed.
func e2eKill(t *testing.T, m *Manager, botID string) {
	t.Helper()
	m.mu.Lock()
	inst := m.vms[botID]
	m.mu.Unlock()
	if inst == nil {
		t.Fatalf("%s is not running", botID)
	}
	if err := inst.vm.Kill(); err != nil {
		t.Fatal(err)
	}
	<-inst.vm.Done()
	deadline := time.Now().Add(10 * time.Second)
	for m.Status(botID).State != StateOff {
		if time.Now().After(deadline) {
			t.Fatalf("manager did not notice the kill: %+v", m.Status(botID))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st := m.Status(botID); !strings.Contains(st.Error, ErrCrashed.Error()) {
		t.Fatalf("a killed VM should read as crashed: %+v", st)
	}
}

// qemuMemory reports the working set and private bytes of a running bot's QEMU process.
func qemuMemory(t *testing.T, m *Manager, botID string) string {
	t.Helper()
	m.mu.Lock()
	inst := m.vms[botID]
	m.mu.Unlock()
	if inst == nil {
		return "not running"
	}
	qv, ok := inst.vm.(*qemuVM)
	if !ok {
		return "unknown"
	}
	ep, ok := qv.proc.(*execProcess)
	if !ok {
		return "unknown"
	}
	pid := ep.cmd.Process.Pid
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("$p = Get-Process -Id %d; '{0} {1}' -f $p.WorkingSet64, $p.PrivateMemorySize64", pid)).Output()
	if err != nil {
		return fmt.Sprintf("pid %d: %v", pid, err)
	}
	f := strings.Fields(string(out))
	if len(f) != 2 {
		return strings.TrimSpace(string(out))
	}
	ws, _ := strconv.ParseInt(f[0], 10, 64)
	priv, _ := strconv.ParseInt(f[1], 10, 64)
	return fmt.Sprintf("pid %d working set %d MB, private %d MB", pid, ws>>20, priv>>20)
}

func firstLine(s, containing string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, containing) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func logTail(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return string(b)
}
