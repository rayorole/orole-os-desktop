package sandbox

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Asset is one pinned release file.
type Asset struct {
	Name   string // file name in the GitHub release
	SHA256 string // lowercase hex
	Size   int64  // bytes
}

// Pins names the QEMU build and guest image this build runs. Every download is checked against them.
type Pins struct {
	QEMUVersion  string // e.g. "20260811" (the qemu.weilnetz.de installer date)
	QEMUTag      string // e.g. "qemu-v20260811"
	QEMU         Asset  // zip: qemu-system-x86_64.exe, qemu-img.exe, DLLs, share/
	ImageVersion string // guest/VERSION, e.g. "0.1.0"
	ImageTag     string // e.g. "guest-v0.1.0"
	Image        []Asset
}

// Complete reports whether p names a full runtime; a build made before the releases were pinned has none.
func (p Pins) Complete() bool { return p.complete() }

// DownloadSize is the total of the pinned files in bytes: what installing the runtime downloads.
func (p Pins) DownloadSize() int64 {
	n := p.QEMU.Size
	for _, a := range p.Image {
		n += a.Size
	}
	return n
}

func (p Pins) complete() bool {
	return p.QEMUVersion != "" && p.QEMUTag != "" && p.QEMU.SHA256 != "" && p.ImageVersion != "" && p.ImageTag != "" && len(p.Image) > 0
}

const defaultReleaseBase = "https://github.com/rayorole/orole-os-desktop/releases/download"

// Files inside Paths.Image(version).
const (
	imageKernel  = "vmlinuz"
	imageInitrd  = "initrd"
	imageCmdline = "cmdline"
	imageBase    = "base.qcow2"
	completeMark = ".complete" // written last: a folder without it is not installed
)

// maxQEMUUnpacked bounds what the QEMU zip may expand to.
const maxQEMUUnpacked = 2 << 30

// ErrNoPins means this build was made before the runtime assets were published.
var ErrNoPins = errors.New("sandbox: this build of Orole has no pinned computer runtime")

// RuntimeState is what the setup screen shows while the runtime downloads.
type RuntimeState struct {
	Ready      bool   `json:"ready"`
	Installing bool   `json:"installing"`
	Step       string `json:"step,omitempty"` // "qemu" or "image"
	Done       int64  `json:"done"`
	Total      int64  `json:"total"`
	Error      string `json:"error,omitempty"`
}

// RuntimeConfig wires a Runtime. Zero values use production defaults.
type RuntimeConfig struct {
	Paths   Paths
	Pins    Pins
	BaseURL string       // default: GitHub releases of rayorole/orole-os-desktop
	HTTP    *http.Client // default: no whole-request timeout (the image is ~1.6 GB); IdleTimeout bounds stalls
	// IdleTimeout fails a download that receives no bytes (headers included) for this long. Default 2 minutes.
	IdleTimeout time.Duration
	OnChange    func(RuntimeState)
}

const defaultDownloadIdle = 2 * time.Minute

// errStalled is the cause a download's context is cancelled with when no bytes arrive for IdleTimeout.
var errStalled = errors.New("the download stalled")

// QEMUFiles are the installed QEMU executables and firmware folder.
type QEMUFiles struct{ System, Img, Share string }

// Runtime downloads, verifies and locates QEMU and the guest image.
type Runtime struct {
	cfg       RuntimeConfig
	installMu sync.Mutex // one install at a time

	mu       sync.Mutex
	state    RuntimeState
	running  bool
	lastSent int64
}

func NewRuntime(cfg RuntimeConfig) *Runtime {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultReleaseBase
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{} // no overall timeout: a slow link may need hours for the image
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultDownloadIdle
	}
	return &Runtime{cfg: cfg}
}

// Ready reports whether the pinned QEMU and image are installed.
func (r *Runtime) Ready() bool {
	p := r.cfg.Pins
	return p.complete() &&
		pathExists(filepath.Join(r.cfg.Paths.Runtime(p.QEMUVersion), completeMark)) &&
		pathExists(filepath.Join(r.cfg.Paths.Image(p.ImageVersion), completeMark))
}

func (r *Runtime) QEMU() (QEMUFiles, error) {
	if !r.Ready() {
		return QEMUFiles{}, ErrNotReady
	}
	dir := r.cfg.Paths.Runtime(r.cfg.Pins.QEMUVersion)
	return QEMUFiles{System: filepath.Join(dir, "qemu-system-x86_64.exe"), Img: filepath.Join(dir, "qemu-img.exe"), Share: filepath.Join(dir, "share")}, nil
}

// Image resolves the installed guest image; it is the Manager's Config.Image.
func (r *Runtime) Image() (ImageSet, error) {
	if !r.Ready() {
		return ImageSet{}, ErrNotReady
	}
	dir := r.cfg.Paths.Image(r.cfg.Pins.ImageVersion)
	return ImageSet{Version: r.cfg.Pins.ImageVersion, Kernel: filepath.Join(dir, imageKernel),
		Initrd: filepath.Join(dir, imageInitrd), BaseImage: filepath.Join(dir, imageBase)}, nil
}

// installedImage resolves an installed image version by name, whether or not it is the pinned one: bots
// whose overlay was made from an older version keep booting it while its files exist.
func installedImage(p Paths, version string) (ImageSet, bool) {
	if version == "" || filepath.Base(version) != version || !filepath.IsLocal(version) {
		return ImageSet{}, false
	}
	dir := p.Image(version)
	for _, f := range []string{completeMark, imageKernel, imageInitrd, imageCmdline, imageBase} {
		if !pathExists(filepath.Join(dir, f)) {
			return ImageSet{}, false
		}
	}
	return ImageSet{Version: version, Kernel: filepath.Join(dir, imageKernel),
		Initrd: filepath.Join(dir, imageInitrd), BaseImage: filepath.Join(dir, imageBase)}, true
}

func (r *Runtime) State() RuntimeState {
	r.mu.Lock()
	s := r.state
	r.mu.Unlock()
	s.Ready = r.Ready()
	return s
}

// Start installs in the background with ctx (the daemon's lifetime, not a request's) unless an install
// is already running, and returns the state right after.
func (r *Runtime) Start(ctx context.Context) RuntimeState {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return r.State()
	}
	r.running = true
	r.state = RuntimeState{Installing: true}
	r.mu.Unlock()
	go func() {
		err := r.Install(ctx)
		r.mu.Lock()
		r.running = false
		r.state.Installing = false
		r.state.Error = ""
		if err != nil {
			r.state.Error = err.Error()
		}
		r.mu.Unlock()
		r.changed()
	}()
	r.changed()
	return r.State()
}

func (r *Runtime) changed() {
	if r.cfg.OnChange != nil {
		r.cfg.OnChange(r.State())
	}
}

// Install downloads whatever of the pinned runtime is missing. A partial or mismatched download leaves
// nothing behind.
func (r *Runtime) Install(ctx context.Context) error {
	if !r.cfg.Pins.complete() {
		return ErrNoPins
	}
	r.installMu.Lock()
	defer r.installMu.Unlock()
	r.sweepStaging()
	if err := r.installQEMU(ctx); err != nil {
		return fmt.Errorf("could not install QEMU: %w", err)
	}
	if err := r.installImage(ctx); err != nil {
		return fmt.Errorf("could not install the computer image: %w", err)
	}
	return nil
}

func (r *Runtime) installQEMU(ctx context.Context) error {
	p := r.cfg.Pins
	dest := r.cfg.Paths.Runtime(p.QEMUVersion)
	if pathExists(filepath.Join(dest, completeMark)) {
		return nil
	}
	stage, err := makeStage(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	archive := filepath.Join(stage, "download.zip")
	if err := r.fetch(ctx, "qemu", p.QEMUTag, p.QEMU, archive); err != nil {
		return err
	}
	files := filepath.Join(stage, "files")
	if err := extractZip(archive, files, maxQEMUUnpacked); err != nil {
		return err
	}
	for _, rel := range []string{"qemu-system-x86_64.exe", "qemu-img.exe", filepath.Join("share", "bios-256k.bin")} {
		if !pathExists(filepath.Join(files, rel)) {
			return fmt.Errorf("the QEMU archive is missing %s", rel)
		}
	}
	return finishStage(files, dest)
}

func (r *Runtime) installImage(ctx context.Context) error {
	p := r.cfg.Pins
	dest := r.cfg.Paths.Image(p.ImageVersion)
	if pathExists(filepath.Join(dest, completeMark)) {
		return nil
	}
	var names []string
	for _, a := range p.Image {
		names = append(names, a.Name)
	}
	for _, need := range []string{imageKernel, imageInitrd, imageCmdline, imageBase} {
		if !slices.Contains(names, need) {
			return fmt.Errorf("the pinned image has no %s", need)
		}
	}
	stage, err := makeStage(filepath.Dir(dest))
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	files := filepath.Join(stage, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return err
	}
	for _, a := range p.Image {
		if filepath.Base(a.Name) != a.Name || !filepath.IsLocal(a.Name) {
			return fmt.Errorf("unsafe asset name %q", a.Name)
		}
		if err := r.fetch(ctx, "image", p.ImageTag, a, filepath.Join(files, a.Name)); err != nil {
			return err
		}
	}
	return finishStage(files, dest)
}

// sweepStaging removes the staging folders an earlier install left when the daemon died mid-download. Only
// one install runs at a time (installMu), so none of them is in use.
func (r *Runtime) sweepStaging() {
	for _, parent := range []string{filepath.Dir(r.cfg.Paths.Runtime("x")), filepath.Dir(r.cfg.Paths.Image("x"))} {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".staging-") {
				_ = os.RemoveAll(filepath.Join(parent, e.Name()))
			}
		}
	}
}

func makeStage(parent string) (string, error) {
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, ".staging-")
}

// finishStage marks src complete and moves it to dest, replacing an incomplete leftover of the same
// version only. Other installed image versions are never touched: existing overlays still reference them.
func finishStage(src, dest string) error {
	if err := os.WriteFile(filepath.Join(src, completeMark), nil, 0o600); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(src, dest)
}

// fetch streams one release asset into dst, checking its size and SHA-256 against the pin. It fails when no
// bytes arrive for IdleTimeout, however long the whole transfer takes.
func (r *Runtime) fetch(ctx context.Context, step, tag string, a Asset, dst string) (err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(r.cfg.IdleTimeout, func() { cancel(errStalled) })
	defer idle.Stop()
	defer func() {
		if err != nil && errors.Is(context.Cause(ctx), errStalled) {
			err = fmt.Errorf("%s: %w (nothing received for %s)", a.Name, errStalled, r.cfg.IdleTimeout)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.BaseURL+"/"+tag+"/"+a.Name, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "orole-os")
	resp, err := r.cfg.HTTP.Do(req)
	if err != nil {
		return err
	}
	body := &idleReader{r: resp.Body, timer: idle, idle: r.cfg.IdleTimeout}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s answered %s", a.Name, req.URL.Host, resp.Status)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	r.progress(step, 0, a.Size, true)
	n, copyErr := io.Copy(io.MultiWriter(f, h, &progressWriter{r: r, step: step, total: a.Size}), io.LimitReader(body, a.Size+1))
	if err := f.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr == nil && n != a.Size {
		copyErr = fmt.Errorf("%s has %d bytes, expected %d", a.Name, n, a.Size)
	}
	if copyErr == nil && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(a.SHA256) {
		copyErr = fmt.Errorf("%s does not match its pinned SHA-256", a.Name)
	}
	if copyErr != nil {
		os.Remove(dst)
		return copyErr
	}
	r.progress(step, a.Size, a.Size, true)
	return nil
}

// idleReader restarts the stall timer whenever bytes arrive.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
	idle  time.Duration
}

func (i *idleReader) Read(p []byte) (int, error) {
	n, err := i.r.Read(p)
	if n > 0 {
		i.timer.Reset(i.idle)
	}
	return n, err
}

type progressWriter struct {
	r     *Runtime
	step  string
	total int64
	done  int64
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	w.r.progress(w.step, w.done, w.total, false)
	return len(p), nil
}

// progress records download progress and reports it every 8 MiB (and always when force is set).
func (r *Runtime) progress(step string, done, total int64, force bool) {
	r.mu.Lock()
	r.state.Step, r.state.Done, r.state.Total = step, done, total
	send := force || done-r.lastSent >= 8<<20
	if send {
		r.lastSent = done
	}
	r.mu.Unlock()
	if send {
		r.changed()
	}
}

// extractZip unpacks archive into dst, refusing paths outside dst and more than limit bytes in total.
func extractZip(archive, dst string, limit int64) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	var total int64
	for _, zf := range zr.File {
		name := filepath.FromSlash(strings.ReplaceAll(zf.Name, `\`, "/"))
		if !filepath.IsLocal(name) {
			return fmt.Errorf("unsafe path in the archive: %q", zf.Name)
		}
		target := filepath.Join(dst, name)
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if !zf.Mode().IsRegular() {
			return fmt.Errorf("unsupported entry in the archive: %q", zf.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		n, err := extractOne(zf, target, limit-total)
		if err != nil {
			return err
		}
		total += n
	}
	return nil
}

func extractOne(zf *zip.File, target string, room int64) (int64, error) {
	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(rc, room+1))
	if err := out.Close(); copyErr == nil {
		copyErr = err
	}
	if copyErr == nil && n > room {
		copyErr = errors.New("the archive expands beyond its size limit")
	}
	return n, copyErr
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
