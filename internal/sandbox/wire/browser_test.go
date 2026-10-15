package wire

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateBrowser(t *testing.T) {
	long := strings.Repeat("a", MaxBrowserURL)
	tests := []struct {
		name string
		req  BrowserRequest
		ok   bool
	}{
		{"open https", BrowserRequest{Action: BrowserOpen, URL: "https://example.com/a?b=c"}, true},
		{"open http", BrowserRequest{Action: BrowserOpen, URL: "http://192.168.1.1/"}, true},
		{"open file", BrowserRequest{Action: BrowserOpen, URL: "file:///etc/passwd"}, false},
		{"open javascript", BrowserRequest{Action: BrowserOpen, URL: "javascript:alert(1)"}, false},
		{"open chrome", BrowserRequest{Action: BrowserOpen, URL: "chrome://settings"}, false},
		{"open relative", BrowserRequest{Action: BrowserOpen, URL: "/path"}, false},
		{"open data", BrowserRequest{Action: BrowserOpen, URL: "data:text/html,<b>x</b>"}, false},
		{"open uppercase scheme", BrowserRequest{Action: BrowserOpen, URL: "HTTPS://Example.com/"}, true},
		{"open no host", BrowserRequest{Action: BrowserOpen, URL: "http:///x"}, false},
		{"open opaque", BrowserRequest{Action: BrowserOpen, URL: "http:example.com"}, false},
		{"open leading space", BrowserRequest{Action: BrowserOpen, URL: " https://example.com"}, false},
		{"open control char", BrowserRequest{Action: BrowserOpen, URL: "https://exa\x00mple.com"}, false},
		{"open userinfo", BrowserRequest{Action: BrowserOpen, URL: "https://user:pw@example.com/"}, false},
		{"open too long", BrowserRequest{Action: BrowserOpen, URL: "https://x.dev/" + long}, false},
		{"click css", BrowserRequest{Action: BrowserClick, Target: "#submit"}, true},
		{"click text", BrowserRequest{Action: BrowserClick, Target: "text=Sign in"}, true},
		{"click empty", BrowserRequest{Action: BrowserClick, Target: "  "}, false},
		{"click empty text", BrowserRequest{Action: BrowserClick, Target: "text= "}, false},
		{"click control char", BrowserRequest{Action: BrowserClick, Target: "a\x00b"}, false},
		{"click too long", BrowserRequest{Action: BrowserClick, Target: strings.Repeat("x", MaxBrowserTarget+1)}, false},
		{"type", BrowserRequest{Action: BrowserType, Target: "input[name=q]", Text: "orole\n"}, true},
		{"type too much", BrowserRequest{Action: BrowserType, Target: "#q", Text: strings.Repeat("x", MaxBrowserTypeText+1)}, false},
		{"type invalid utf8", BrowserRequest{Action: BrowserType, Target: "#q", Text: "\xff"}, false},
		{"read", BrowserRequest{Action: BrowserRead}, true},
		{"screenshot", BrowserRequest{Action: BrowserScreenshot}, true},
		{"unknown", BrowserRequest{Action: "eval"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateBrowser(tt.req); (err == nil) != tt.ok {
				t.Fatalf("ValidateBrowser(%+v) = %v, want ok=%v", tt.req, err, tt.ok)
			}
		})
	}
}

func TestCapTextKeepsWholeRunes(t *testing.T) {
	s := strings.Repeat("é", 10) // 20 bytes
	got, cut := CapText(s, 7)
	if !cut || got != "ééé" || !utf8.ValidString(got) {
		t.Fatalf("CapText = %q %v", got, cut)
	}
	if got, cut := CapText("short", 7); cut || got != "short" {
		t.Fatalf("short text changed: %q %v", got, cut)
	}
}

func TestParseTarget(t *testing.T) {
	tests := []struct {
		in    string
		value string
		text  bool
	}{
		{"#submit", "#submit", false},
		{"  #submit ", "#submit", false},
		{"text=Sign in", "Sign in", true},
		{"  text= Sign in ", "Sign in", true},
		{"TEXT=Sign in", "TEXT=Sign in", false},
		{"text=", "", true},
	}
	for _, tt := range tests {
		if v, b := ParseTarget(tt.in); v != tt.value || b != tt.text {
			t.Errorf("ParseTarget(%q) = %q, %v; want %q, %v", tt.in, v, b, tt.value, tt.text)
		}
	}
}
