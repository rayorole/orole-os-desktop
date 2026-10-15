package wire

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Browser actions (BrowserRequest.Action).
const (
	BrowserOpen       = "open"
	BrowserClick      = "click"
	BrowserType       = "type"
	BrowserRead       = "read"
	BrowserScreenshot = "screenshot"
)

// Browser limits, enforced by the host tools and again by orole-guest.
const (
	MaxBrowserURL      = 2048
	MaxBrowserTarget   = 1000
	MaxBrowserTypeText = 64 << 10
	MaxBrowserPageText = 90 << 10 // bytes of page text returned by read; the engine clips tool results at 100 KiB
	MaxScreenshotBytes = 5 << 20
)

// TextTargetPrefix makes a click or type target match an element by its visible text instead of as a CSS selector.
const TextTargetPrefix = "text="

// PageTextTruncated is appended to BrowserResult.Text when read cut the page text at MaxBrowserPageText.
const PageTextTruncated = "\n[page text truncated at 90 KiB]"

// ValidateBrowser checks a browser request. The host tools call it before asking for approval and orole-guest
// calls it again before touching Chromium.
func ValidateBrowser(req BrowserRequest) error {
	switch req.Action {
	case BrowserOpen:
		return validURL(req.URL)
	case BrowserClick:
		return validTarget(req.Target)
	case BrowserType:
		if err := validTarget(req.Target); err != nil {
			return err
		}
		if len(req.Text) > MaxBrowserTypeText || !utf8.ValidString(req.Text) {
			return fmt.Errorf("text must be valid text of at most %d bytes", MaxBrowserTypeText)
		}
		return nil
	case BrowserRead, BrowserScreenshot:
		return nil
	}
	return fmt.Errorf("unknown browser action %q", req.Action)
}

func validURL(raw string) error {
	if strings.TrimSpace(raw) == "" || len(raw) > MaxBrowserURL {
		return fmt.Errorf("url is required (at most %d characters)", MaxBrowserURL)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be an absolute http or https address")
	}
	if u.User != nil {
		return errors.New("url must not contain a username or password")
	}
	return nil
}

func validTarget(target string) error {
	if len(target) > MaxBrowserTarget || !utf8.ValidString(target) || strings.ContainsFunc(target, unicode.IsControl) {
		return fmt.Errorf("target must be a CSS selector or text=<visible text> of at most %d characters", MaxBrowserTarget)
	}
	if value, _ := ParseTarget(target); value == "" {
		return errors.New("target is required: a CSS selector or text=<visible text>")
	}
	return nil
}

// ParseTarget splits a click or type target into the string to look for and whether it is matched by visible text.
// Surrounding whitespace is ignored; the "text=" prefix is case-sensitive and the text after it is trimmed. The
// validation here and the guest's element lookup both use it, so they cannot disagree.
func ParseTarget(target string) (value string, byText bool) {
	target = strings.TrimSpace(target)
	if rest, ok := strings.CutPrefix(target, TextTargetPrefix); ok {
		return strings.TrimSpace(rest), true
	}
	return target, false
}

// CapText cuts s to at most max bytes on a rune boundary and reports whether it cut.
func CapText(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}
