package dictation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSettings) Get(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok, nil
}

func (s *memSettings) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[key] = value
	return nil
}

type recCall struct {
	spec    Spec
	samples int
}

type fakeRec struct {
	mu     sync.Mutex
	calls  []recCall
	closes int
	texts  []string // returned per call in order ("" past the end)
	delay  time.Duration
}

func (f *fakeRec) Load(context.Context, Spec) error { return nil }

func (f *fakeRec) Recognize(ctx context.Context, spec Spec, samples []float32) (string, error) {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recCall{spec, len(samples)})
	if i := len(f.calls) - 1; i < len(f.texts) {
		return f.texts[i], nil
	}
	return "", nil
}

func (f *fakeRec) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return nil
}

// fileServer serves fixed contents with Range support and records the Range header of every request.
type fileServer struct {
	*httptest.Server
	mu      sync.Mutex
	files   map[string][]byte
	ranges  map[string][]string
	noRange map[string]bool // answer 200 with the whole file
	hang    map[string]bool // send half, then wait for the client to go away
}

func newFileServer(t *testing.T) *fileServer {
	fs := &fileServer{files: map[string][]byte{}, ranges: map[string][]string{}, noRange: map[string]bool{}, hang: map[string]bool{}}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		body, ok := fs.files[r.URL.Path]
		fs.ranges[r.URL.Path] = append(fs.ranges[r.URL.Path], r.Header.Get("Range"))
		noRange, hang := fs.noRange[r.URL.Path], fs.hang[r.URL.Path]
		fs.mu.Unlock()
		switch {
		case !ok:
			http.NotFound(w, r)
		case hang:
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body[:len(body)/2])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case noRange:
			w.Write(body)
		default:
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
		}
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fileServer) add(name string, body []byte) File {
	fs.mu.Lock()
	fs.files["/"+name] = body
	fs.mu.Unlock()
	sum := sha256.Sum256(body)
	return File{Name: name, URL: fs.URL + "/" + name, Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])}
}

func (fs *fileServer) rangesOf(name string) []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.ranges["/"+name]...)
}

type auditLog struct {
	mu    sync.Mutex
	lines []string
}

func (a *auditLog) add(action, target, outcome, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = append(a.lines, strings.Join([]string{action, target, outcome, detail}, "|"))
}

func (a *auditLog) has(prefix string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, l := range a.lines {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

type env struct {
	t        *testing.T
	dir      string
	srv      *fileServer
	settings *memSettings
	rec      *fakeRec
	audit    *auditLog
	catalog  []Model
	engine   Engine
	svc      *Service
}

func randomBytes(t *testing.T, n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// newEnv serves a two-model catalog and the engine fixture. Models: "small" (whisper-like, chunked,
// language choice) and "other" (transducer).
func newEnv(t *testing.T) *env {
	t.Helper()
	srv := newFileServer(t)
	archive, err := os.ReadFile(filepath.Join("testdata", "engine.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{
		t: t, dir: t.TempDir(), srv: srv, settings: &memSettings{}, rec: &fakeRec{}, audit: &auditLog{},
		engine: Engine{
			Version: "9.9.9", Archive: srv.add("engine.tar.bz2", archive),
			Prefix: "eng/lib/", DLLs: []string{"api.dll", "runtime.dll"}, API: "api.dll",
		},
		catalog: []Model{
			{
				ID: "small", Name: "Small", Kind: KindWhisper, LanguageChoice: true, ChunkSeconds: 1,
				Encoder: "enc.bin", Decoder: "dec.bin", Tokens: "tok.txt",
				Files: []File{srv.add("small/enc.bin", randomBytes(t, 300<<10)), srv.add("small/dec.bin", randomBytes(t, 40<<10)), srv.add("small/tok.txt", []byte("a\nb\n"))},
			},
			{
				ID: "other", Name: "Other", Kind: KindTransducer,
				Encoder: "e.bin", Decoder: "d.bin", Joiner: "j.bin", Tokens: "t.txt",
				Files: []File{srv.add("other/e.bin", randomBytes(t, 20<<10)), srv.add("other/d.bin", []byte("dd")), srv.add("other/j.bin", []byte("jj")), srv.add("other/t.txt", []byte("tt"))},
			},
		},
	}
	// Files are named after their URL path; on disk they keep the base name.
	for i := range e.catalog {
		for k := range e.catalog[i].Files {
			e.catalog[i].Files[k].Name = filepath.Base(e.catalog[i].Files[k].Name)
		}
	}
	e.svc = e.open()
	return e
}

func (e *env) open() *Service {
	e.t.Helper()
	yes := true
	s, err := NewService(Config{
		Dir: e.dir, Settings: e.settings, Recognizer: e.rec, Audit: e.audit.add,
		Catalog: e.catalog, Engine: &e.engine, Supported: &yes,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { s.Close() })
	return s
}

func (e *env) model(id string) ModelStatus {
	for _, m := range e.svc.Status(context.Background()).Models {
		if m.ID == id {
			return m
		}
	}
	e.t.Fatalf("no model %s", id)
	return ModelStatus{}
}

func (e *env) waitState(id string, want ModelState) ModelStatus {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		m := e.model(id)
		if m.State == want {
			return m
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("%s: state %s (%s), want %s", id, m.State, m.Error, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) download(id string, want ModelState) ModelStatus {
	e.t.Helper()
	if _, err := e.svc.Download(context.Background(), id); err != nil {
		e.t.Fatal(err)
	}
	return e.waitState(id, want)
}

// installReady puts a model's files and the engine on disk without downloading them.
func (e *env) installReady(id string) {
	e.t.Helper()
	m, _ := e.svc.model(id)
	for _, f := range m.Files {
		writeFile(e.t, filepath.Join(e.svc.modelDir(id), f.Name), make([]byte, f.Size))
	}
	for _, d := range e.engine.DLLs {
		writeFile(e.t, filepath.Join(e.svc.engineDir(), d), []byte("dll"))
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func codeOf(err error) Code {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestDownloadInstallsEngineVerifiesAndUsesModel(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	st := e.svc.Status(ctx)
	if st.Ready || st.Model != "" || len(st.Models) != 2 || st.SampleRate != SampleRate || st.PartBytes != PartBytes {
		t.Fatalf("fresh status: %+v", st)
	}
	small := e.catalog[0]
	if m := e.model("small"); m.SizeBytes != small.SizeBytes() || m.DownloadBytes != small.SizeBytes()+e.engine.Archive.Size {
		t.Fatalf("sizes before engine: %+v", m)
	}

	e.download("small", StateReady)
	st = e.svc.Status(ctx)
	if !st.Ready || st.Model != "small" {
		t.Fatalf("after download: ready=%v model=%q", st.Ready, st.Model)
	}
	if b, _ := os.ReadFile(filepath.Join(e.svc.engineDir(), "api.dll")); string(b) != "api dll bytes" {
		t.Fatalf("engine dll = %q", b)
	}
	if exists(filepath.Join(e.svc.engineDir(), "other.lib")) || exists(e.svc.engineStaging()) || exists(e.svc.downloadDir("small")) {
		t.Fatal("extra engine file or leftover staging")
	}
	if m := e.model("other"); m.DownloadBytes != e.catalog[1].SizeBytes() {
		t.Fatalf("download size once the engine is installed: %+v", m)
	}
	if !e.audit.has(fmt.Sprintf("dictation.download|small|allowed|%d bytes", small.SizeBytes()+e.engine.Archive.Size)) {
		t.Fatalf("audit: %v", e.audit.lines)
	}

	// A second model does not take over the one in use, and the choice survives a restart.
	e.download("other", StateReady)
	if got := e.svc.Status(ctx).Model; got != "small" {
		t.Fatalf("model in use after second download = %q", got)
	}
	if ranges := e.srv.rangesOf("engine.tar.bz2"); len(ranges) != 1 {
		t.Fatalf("engine fetched %d times", len(ranges))
	}
	e.svc.Close()
	e.svc = e.open()
	if st := e.svc.Status(ctx); st.Model != "small" || !st.Ready {
		t.Fatalf("after restart: %+v", st)
	}
}

func TestDownloadResumesPartialFile(t *testing.T) {
	for _, tc := range []struct {
		name      string
		noRange   bool
		wantRange string
	}{
		{"server honours the range", false, "bytes=100000-"},
		{"server ignores the range", true, "bytes=100000-"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.installReady("other") // engine on disk; "other" is irrelevant here
			enc := e.catalog[0].Files[0]
			e.srv.mu.Lock()
			body := e.srv.files["/small/enc.bin"]
			e.srv.noRange["/small/enc.bin"] = tc.noRange
			e.srv.mu.Unlock()
			writeFile(t, filepath.Join(e.svc.downloadDir("small"), "enc.bin.part"), body[:100000])

			e.download("small", StateReady)
			if got := e.srv.rangesOf("small/enc.bin"); len(got) != 1 || got[0] != tc.wantRange {
				t.Fatalf("range requests = %q", got)
			}
			if got := e.srv.rangesOf("small/dec.bin"); len(got) != 1 || got[0] != "" {
				t.Fatalf("fresh file requested with %q", got)
			}
			b, _ := os.ReadFile(filepath.Join(e.svc.modelDir("small"), "enc.bin"))
			if int64(len(b)) != enc.Size || !bytes.Equal(b, body) {
				t.Fatal("resumed file differs")
			}
		})
	}
}

func TestDownloadFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		break_    func(e *env)
		msg       string
		class     string
		part      string // the failing file
		keepsPart bool
	}{
		{"hash mismatch", func(e *env) { e.catalog[0].Files[1].SHA256 = strings.Repeat("0", 64) }, msgCorrupted, "corrupted", "dec.bin", false},
		{"missing on server", func(e *env) { e.catalog[0].Files[2].URL = e.srv.URL + "/gone" }, "The download server answered 404 Not Found. Try again later.", "http_404", "tok.txt", false},
		// The watchdog also runs while earlier files download and while dec.bin's headers are awaited. It must
		// only fire in the hang, after dec.bin's first half is on disk: 100 ms was shorter than those normal
		// gaps on a busy runner, which failed with "stalled" before dec.bin.part existed.
		{"stalled", func(e *env) { e.srv.hang["/small/dec.bin"] = true; e.svc.stall = 2 * time.Second }, msgStalled, "stalled", "dec.bin", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.installReady("other")
			e.srv.mu.Lock()
			tc.break_(e)
			e.srv.mu.Unlock()
			m := e.download("small", StateFailed)
			if m.Error != tc.msg {
				t.Fatalf("error = %q", m.Error)
			}
			if got := exists(filepath.Join(e.svc.downloadDir("small"), tc.part+".part")); got != tc.keepsPart {
				t.Fatalf("part kept = %v", got)
			}
			if !e.audit.has("dictation.download|small|failed|" + tc.class) {
				t.Fatalf("audit: %v", e.audit.lines)
			}
			if exists(e.svc.modelDir("small")) || e.svc.Status(context.Background()).Model != "" {
				t.Fatal("failed model installed or selected")
			}
		})
	}
}

func TestCancelKeepsPartForResume(t *testing.T) {
	e := newEnv(t)
	e.installReady("other")
	e.srv.mu.Lock()
	e.srv.hang["/small/enc.bin"] = true
	half := len(e.srv.files["/small/enc.bin"]) / 2
	e.srv.mu.Unlock()
	if _, err := e.svc.Download(context.Background(), "small"); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(e.svc.downloadDir("small"), "enc.bin.part")
	deadline := time.Now().Add(10 * time.Second)
	for e.model("small").ReceivedBytes < int64(half) {
		if time.Now().After(deadline) {
			t.Fatal("download made no progress")
		}
		time.Sleep(5 * time.Millisecond)
	}
	st, err := e.svc.Cancel(context.Background(), "small")
	if err != nil {
		t.Fatal(err)
	}
	if st.Models[0].State != StateAbsent || st.Models[0].Error != "" {
		t.Fatalf("after cancel: %+v", st.Models[0])
	}
	if fi, err := os.Stat(part); err != nil || fi.Size() != int64(half) {
		t.Fatalf("part after cancel: %v %v", fi, err)
	}
	if !e.audit.has("dictation.download|small|canceled|") {
		t.Fatalf("audit: %v", e.audit.lines)
	}

	e.srv.mu.Lock()
	e.srv.hang["/small/enc.bin"] = false
	e.srv.mu.Unlock()
	e.download("small", StateReady)
	if got := e.srv.rangesOf("small/enc.bin"); got[len(got)-1] != fmt.Sprintf("bytes=%d-", half) {
		t.Fatalf("resume range = %q", got)
	}
}

func TestRemoveFreesModelAndReselects(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.download("small", StateReady)
	e.download("other", StateReady)
	closes := e.rec.closes

	st, err := e.svc.Remove(ctx, "small")
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "other" || !st.Ready || st.Models[0].State != StateAbsent || exists(e.svc.modelDir("small")) {
		t.Fatalf("after removing the model in use: %+v", st)
	}
	if e.rec.closes == closes {
		t.Fatal("recognizer not closed before deleting the model in use")
	}
	if !e.svc.engineInstalled() || st.Models[0].DownloadBytes != e.catalog[0].SizeBytes() {
		t.Fatal("engine removed while a model is left")
	}

	st, err = e.svc.Remove(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "" || st.Ready || exists(filepath.Join(e.dir, "engine")) {
		t.Fatalf("after removing the last model: %+v", st)
	}
	if st.Models[1].DownloadBytes != e.catalog[1].SizeBytes()+e.engine.Archive.Size {
		t.Fatalf("download size without engine: %+v", st.Models[1])
	}
	if !e.audit.has("dictation.remove|small|allowed|") || !e.audit.has("dictation.remove|other|allowed|") {
		t.Fatalf("audit: %v", e.audit.lines)
	}
	if _, err := e.svc.Remove(ctx, "nope"); codeOf(err) != CodeUnknown {
		t.Fatalf("remove unknown: %v", err)
	}
}

func TestPreferences(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.svc.Use(ctx, "other"); codeOf(err) != CodeNotReady {
		t.Fatalf("use before download: %v", err)
	}
	if _, err := e.svc.Use(ctx, "nope"); codeOf(err) != CodeUnknown {
		t.Fatalf("use unknown: %v", err)
	}
	if _, err := e.svc.SetLanguage(ctx, "xx"); codeOf(err) != CodeUnknown {
		t.Fatalf("unknown language: %v", err)
	}
	e.installReady("small")
	e.installReady("other")
	if _, err := e.svc.Use(ctx, "small"); err != nil {
		t.Fatal(err)
	}
	closes := e.rec.closes
	if _, err := e.svc.Use(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if e.rec.closes != closes+1 {
		t.Fatal("old model not released when switching")
	}
	if _, err := e.svc.SetLanguage(ctx, "de"); err != nil {
		t.Fatal(err)
	}
	e.svc.Close()
	e.svc = e.open()
	if st := e.svc.Status(ctx); st.Model != "other" || st.Language != "de" || !st.Ready {
		t.Fatalf("after restart: model=%q language=%q ready=%v", st.Model, st.Language, st.Ready)
	}

	// Values this build does not know are ignored.
	e.settings.Set(ctx, SettingKey, `{"model":"retired","language":"klingon"}`)
	e.svc.Close()
	e.svc = e.open()
	if st := e.svc.Status(ctx); st.Model != "" || st.Language != "" {
		t.Fatalf("invalid stored preferences kept: %+v", st)
	}
}

func TestStartupSweepsStaleEngineFiles(t *testing.T) {
	e := newEnv(t)
	e.svc.Close()
	writeFile(t, filepath.Join(e.dir, "downloads", "engine-9.9.9", "archive.part"), []byte("x"))
	writeFile(t, filepath.Join(e.dir, "engine", "0.0.1", "api.dll"), []byte("x"))
	writeFile(t, filepath.Join(e.dir, "downloads", "small", "enc.bin.part"), []byte("x"))
	e.svc = e.open()
	if exists(filepath.Join(e.dir, "downloads", "engine-9.9.9")) || exists(filepath.Join(e.dir, "engine", "0.0.1")) {
		t.Fatal("stale engine files kept")
	}
	if !exists(filepath.Join(e.dir, "downloads", "small", "enc.bin.part")) {
		t.Fatal("resumable model part deleted")
	}
}

func pcmBytes(samples int) []byte {
	b := make([]byte, samples*2)
	for i := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(i%2000-1000)))
	}
	return b
}

func pcm(samples int) string { return base64.StdEncoding.EncodeToString(pcmBytes(samples)) }

func TestUploadValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	up := func(id string, part int, data string) error {
		return e.svc.Upload(ctx, UploadRequest{ID: id, Part: part, PCMBase64: data})
	}
	big := pcm(PartBytes / 2)
	for _, tc := range []struct {
		name string
		run  func() error
		want Code
	}{
		{"short id", func() error { return up("short", 0, pcm(4)) }, CodeBadAudio},
		{"id with a slash", func() error { return up("abcd/efgh", 0, pcm(4)) }, CodeBadAudio},
		{"not base64", func() error { return up("rec-bad-b64", 0, "!!!") }, CodeBadAudio},
		{"odd byte count", func() error { return up("rec-odd-001", 0, base64.StdEncoding.EncodeToString([]byte{1, 2, 3})) }, CodeBadAudio},
		{"part over PartBytes", func() error { return up("rec-big-001", 0, pcm(PartBytes/2+1)) }, CodeBadAudio},
		{"first part not 0", func() error { return up("rec-order-1", 1, pcm(4)) }, CodeBadAudio},
		{"part skipped", func() error {
			if err := up("rec-order-2", 0, pcm(4)); err != nil {
				return err
			}
			return up("rec-order-2", 2, pcm(4))
		}, CodeBadAudio},
		{"too long", func() error {
			for part := 0; ; part++ {
				if err := up("rec-too-long", part, big); err != nil {
					return err
				}
			}
		}, CodeTooLong},
		{"in order", func() error {
			for part := range 3 {
				if err := up("rec-fine-01", part, pcm(1000)); err != nil {
					return err
				}
			}
			return e.svc.Discard(ctx, RecordingRef{ID: "rec-fine-01"})
		}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(tc.run()); got != tc.want {
				t.Fatalf("code = %q, want %q", got, tc.want)
			}
		})
	}

	// Failed uploads were dropped, so two recordings fit; a third is refused until one expires.
	if err := up("rec-busy-01", 0, pcm(4)); err != nil {
		t.Fatal(err)
	}
	if err := up("rec-busy-02", 0, pcm(4)); err != nil {
		t.Fatal(err)
	}
	if err := up("rec-busy-03", 0, pcm(4)); codeOf(err) != CodeBusy {
		t.Fatalf("third recording: %v", err)
	}
	later := time.Now().Add(UploadTTL + time.Second)
	e.svc.now = func() time.Time { return later }
	if err := up("rec-busy-03", 0, pcm(4)); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if err := up("rec-busy-01", 1, pcm(4)); codeOf(err) != CodeBadAudio {
		t.Fatalf("expired recording continued: %v", err)
	}
	if _, err := e.svc.Transcribe(ctx, RecordingRef{ID: "rec-busy-02"}); codeOf(err) != CodeBadAudio {
		t.Fatalf("expired recording transcribed: %v", err)
	}
}

func TestTranscribe(t *testing.T) {
	ctx := context.Background()
	upload := func(t *testing.T, s *Service, id string, samples int) {
		t.Helper()
		if err := s.Upload(ctx, UploadRequest{ID: id, PCMBase64: pcm(samples)}); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("not ready", func(t *testing.T) {
		e := newEnv(t)
		upload(t, e.svc, "rec-notready", 1600)
		_, err := e.svc.Transcribe(ctx, RecordingRef{ID: "rec-notready"})
		if codeOf(err) != CodeNotReady || err.Error() != "Choose and download a dictation model in Settings > Voice." {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no audio and missing upload", func(t *testing.T) {
		e := newEnv(t)
		upload(t, e.svc, "rec-empty-01", 0)
		if _, err := e.svc.Transcribe(ctx, RecordingRef{ID: "rec-empty-01"}); codeOf(err) != CodeNoAudio {
			t.Fatalf("empty: %v", err)
		}
		if _, err := e.svc.Transcribe(ctx, RecordingRef{ID: "rec-empty-01"}); codeOf(err) != CodeBadAudio {
			t.Fatalf("transcribed twice: %v", err)
		}
	})

	t.Run("chunked model joins texts in order", func(t *testing.T) {
		e := newEnv(t)
		e.installReady("small")
		if _, err := e.svc.Use(ctx, "small"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.SetLanguage(ctx, "fr"); err != nil {
			t.Fatal(err)
		}
		e.rec.texts = []string{"  bonjour ", "", "le monde\n", " ! "}
		samples := SampleRate*3 + SampleRate/2 // 3.5 s with 1 s chunks
		// Two parts, so the PCM is joined before recognition.
		first, second := pcmBytes(SampleRate), pcmBytes(samples-SampleRate)
		if err := e.svc.Upload(ctx, UploadRequest{ID: "rec-chunked", Part: 0, PCMBase64: base64.StdEncoding.EncodeToString(first)}); err != nil {
			t.Fatal(err)
		}
		if err := e.svc.Upload(ctx, UploadRequest{ID: "rec-chunked", Part: 1, PCMBase64: base64.StdEncoding.EncodeToString(second)}); err != nil {
			t.Fatal(err)
		}
		tr, err := e.svc.Transcribe(ctx, RecordingRef{ID: "rec-chunked"})
		if err != nil {
			t.Fatal(err)
		}
		all := append(first, second...)
		floats := make([]float32, samples)
		for i := range floats {
			floats[i] = float32(int16(binary.LittleEndian.Uint16(all[2*i:]))) / 32768
		}
		chunks := Chunks(floats, SampleRate, 1)
		if len(chunks) < 4 || len(e.rec.calls) != len(chunks) {
			t.Fatalf("recognizer calls = %d, chunks = %d", len(e.rec.calls), len(chunks))
		}
		total := 0
		for i, c := range e.rec.calls {
			total += c.samples
			if c.samples != len(chunks[i]) {
				t.Fatalf("call %d got %d samples, chunk has %d", i, c.samples, len(chunks[i]))
			}
			if c.spec.Language != "fr" || c.spec.Kind != KindWhisper || c.spec.Joiner != "" ||
				c.spec.Encoder != filepath.Join(e.svc.modelDir("small"), "enc.bin") || c.spec.EngineDir != e.svc.engineDir() {
				t.Fatalf("spec = %+v", c.spec)
			}
		}
		if total != samples {
			t.Fatalf("recognized %d samples of %d", total, samples)
		}
		if tr.Text != "bonjour le monde !" || tr.Model != "Small" {
			t.Fatalf("transcript = %+v", tr)
		}
	})

	t.Run("unchunked model gets one piece and no language", func(t *testing.T) {
		e := newEnv(t)
		e.installReady("other")
		if _, err := e.svc.Use(ctx, "other"); err != nil {
			t.Fatal(err)
		}
		e.svc.SetLanguage(ctx, "fr")
		e.rec.texts = []string{"hello"}
		upload(t, e.svc, "rec-whole-01", SampleRate*3)
		tr, err := e.svc.Transcribe(ctx, RecordingRef{ID: "rec-whole-01"})
		if err != nil || tr.Text != "hello" || len(e.rec.calls) != 1 || e.rec.calls[0].samples != SampleRate*3 || e.rec.calls[0].spec.Language != "" {
			t.Fatalf("transcript %+v err %v calls %+v", tr, err, e.rec.calls)
		}
	})

	t.Run("caller deadline does not cut it short, cancel does", func(t *testing.T) {
		e := newEnv(t)
		e.installReady("other")
		if _, err := e.svc.Use(ctx, "other"); err != nil {
			t.Fatal(err)
		}
		e.rec.delay = 100 * time.Millisecond
		upload(t, e.svc, "rec-deadline", 1600)
		short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
		defer cancel()
		if _, err := e.svc.Transcribe(short, RecordingRef{ID: "rec-deadline"}); err != nil {
			t.Fatalf("deadline: %v", err)
		}
		upload(t, e.svc, "rec-canceled", 1600)
		canceled, cancelNow := context.WithCancel(ctx)
		time.AfterFunc(10*time.Millisecond, cancelNow)
		if _, err := e.svc.Transcribe(canceled, RecordingRef{ID: "rec-canceled"}); codeOf(err) != CodeCanceled {
			t.Fatalf("cancel: %v", err)
		}
	})
}
