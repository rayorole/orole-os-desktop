package guestagent_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// fakePage is a BrowserPage that records what it was asked to do.
type fakePage struct {
	mu         sync.Mutex
	calls      []string
	title, url string
	text       string
	png        []byte
	err        error
}

func (f *fakePage) record(c string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.err
}
func (f *fakePage) Navigate(_ context.Context, u string) error { return f.record("navigate " + u) }
func (f *fakePage) Click(_ context.Context, target string) error {
	return f.record("click " + target)
}
func (f *fakePage) Type(_ context.Context, target, text string) error {
	return f.record("type " + target + " " + text)
}
func (f *fakePage) Info(context.Context) (string, string, error) {
	return f.title, f.url, f.record("info")
}
func (f *fakePage) Text(context.Context) (string, error) { return f.text, f.record("text") }
func (f *fakePage) Screenshot(context.Context) ([]byte, error) {
	return f.png, f.record("screenshot")
}

func browserAgent(t *testing.T, page guestagent.BrowserPage) *guestagent.Agent {
	a, _ := newAgent(t, func(c *guestagent.Config) { c.Browser = page })
	return a
}

func TestBrowserRejectsInvalidRequestsBeforeTouchingChromium(t *testing.T) {
	for _, body := range []string{
		`{"action":"open","url":"file:///etc/passwd"}`,
		`{"action":"open","url":"javascript:alert(1)"}`,
		`{"action":"click","target":""}`,
		`{"action":"type","target":"text=","text":"x"}`,
		`{"action":"eval"}`,
		`not json`,
	} {
		page := &fakePage{}
		a := browserAgent(t, page)
		req := httptest.NewRequest("POST", "http://guest/browser", strings.NewReader(body))
		req.Header.Set(wire.TokenHeader, token)
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
		if len(page.calls) != 0 {
			t.Errorf("%s: page was used: %v", body, page.calls)
		}
	}
}

func TestBrowserOpenNavigatesAndReportsThePage(t *testing.T) {
	page := &fakePage{title: "Example", url: "https://example.com/"}
	a := browserAgent(t, page)
	res := decode[wire.BrowserResult](t, call(t, a, "POST", "/browser", token, wire.BrowserRequest{Action: "open", URL: "https://example.com"}))
	if res.Title != "Example" || res.URL != "https://example.com/" {
		t.Fatalf("result: %+v", res)
	}
	if strings.Join(page.calls, "|") != "navigate https://example.com|info" {
		t.Fatalf("calls: %v", page.calls)
	}
}

func TestBrowserReadCapsThePageText(t *testing.T) {
	page := &fakePage{title: "Big", url: "https://example.com/big", text: strings.Repeat("é", wire.MaxBrowserPageText)}
	a := browserAgent(t, page)
	res := decode[wire.BrowserResult](t, call(t, a, "POST", "/browser", token, wire.BrowserRequest{Action: "read"}))
	if !strings.HasSuffix(res.Text, wire.PageTextTruncated) || len(res.Text) > wire.MaxBrowserPageText+len(wire.PageTextTruncated) || !utf8.ValidString(res.Text) {
		t.Fatalf("text not capped: %d bytes", len(res.Text))
	}
}

func TestBrowserScreenshotOverTheLimitIsRefused(t *testing.T) {
	page := &fakePage{png: make([]byte, wire.MaxScreenshotBytes+1)}
	a := browserAgent(t, page)
	if rec := call(t, a, "POST", "/browser", token, wire.BrowserRequest{Action: "screenshot"}); rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestBrowserErrorsMapToStatusCodes(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want int
	}{
		{guestagent.ErrNoBrowser, http.StatusServiceUnavailable},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{errors.New(`nothing on the page matches "#nope"`), http.StatusBadGateway},
	} {
		a := browserAgent(t, &fakePage{err: tt.err})
		if rec := call(t, a, "POST", "/browser", token, wire.BrowserRequest{Action: "click", Target: "#nope"}); rec.Code != tt.want {
			t.Errorf("%v: status %d, want %d", tt.err, rec.Code, tt.want)
		}
	}
}
