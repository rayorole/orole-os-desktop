package guestagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// BrowserPage is the one visible Chromium tab orole-guest drives. cmd/orole-guest implements it with chromedp;
// tests use a fake.
type BrowserPage interface {
	Navigate(ctx context.Context, url string) error
	Click(ctx context.Context, target string) error
	Type(ctx context.Context, target, text string) error
	Info(ctx context.Context) (title, url string, err error)
	Text(ctx context.Context) (string, error)
	Screenshot(ctx context.Context) ([]byte, error)
}

const (
	browserOpenTimeout = 60 * time.Second
	browserStepTimeout = 20 * time.Second
)

// ErrNoBrowser marks a page that cannot reach Chromium (not started yet, or crashed).
var ErrNoBrowser = errors.New("the browser is not running")

// browserHandler serves POST /browser: one action at a time on the visible tab.
func browserHandler(page BrowserPage) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		var req wire.BrowserRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := wire.ValidateBrowser(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		timeout := browserStepTimeout
		if req.Action == wire.BrowserOpen {
			timeout = browserOpenTimeout
		}
		// Lock first so time spent waiting for an earlier action does not eat this one's budget.
		mu.Lock()
		defer mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		res, err := runBrowser(ctx, page, req)
		if err != nil {
			status := http.StatusBadGateway
			switch {
			case errors.Is(err, ErrNoBrowser):
				status = http.StatusServiceUnavailable
			case errors.Is(err, context.DeadlineExceeded):
				status = http.StatusGatewayTimeout
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// runBrowser performs one validated action and reports the page it left the tab on.
func runBrowser(ctx context.Context, page BrowserPage, req wire.BrowserRequest) (wire.BrowserResult, error) {
	var res wire.BrowserResult
	var err error
	switch req.Action {
	case wire.BrowserOpen:
		err = page.Navigate(ctx, req.URL)
	case wire.BrowserClick:
		err = page.Click(ctx, req.Target)
	case wire.BrowserType:
		err = page.Type(ctx, req.Target, req.Text)
	case wire.BrowserRead:
		var text string
		if text, err = page.Text(ctx); err == nil {
			capped, cut := wire.CapText(text, wire.MaxBrowserPageText)
			if cut {
				capped += wire.PageTextTruncated
			}
			res.Text = capped
		}
	case wire.BrowserScreenshot:
		res.PNG, err = page.Screenshot(ctx)
		if err == nil && len(res.PNG) > wire.MaxScreenshotBytes {
			err = fmt.Errorf("the screenshot is larger than %d bytes", wire.MaxScreenshotBytes)
		}
	}
	if err != nil {
		return wire.BrowserResult{}, err
	}
	if res.Title, res.URL, err = page.Info(ctx); err != nil {
		return wire.BrowserResult{}, err
	}
	return res, nil
}
