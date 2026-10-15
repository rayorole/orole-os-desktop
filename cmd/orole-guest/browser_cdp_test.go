package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
)

func TestPageTargetPicksTheFirstPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/list" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"id":"sw1","type":"service_worker","url":"https://x"},{"id":"P1","type":"page","url":"https://example.com"},{"id":"P2","type":"page","url":"about:blank"}]`))
	}))
	defer srv.Close()
	id, err := pageTarget(context.Background(), srv.Client(), srv.URL)
	if err != nil || id != "P1" {
		t.Fatalf("pageTarget = %q %v", id, err)
	}
}

func TestPageTargetWithoutChromiumIsNoBrowser(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	if _, err := pageTarget(context.Background(), http.DefaultClient, url); !errors.Is(err, guestagent.ErrNoBrowser) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveScriptQuotesTheTarget(t *testing.T) {
	hostile := `"); document.body.innerHTML = ""; ("`
	script := resolveScript("m", hostile, false)
	quoted, _ := json.Marshal(hostile)
	if !strings.HasSuffix(script, ", "+string(quoted)+", false, false)") {
		t.Fatalf("target must be passed as a JSON string literal: %s", script[len(script)-120:])
	}
	if s := resolveScript("m", "text=Sign in", false); !strings.HasSuffix(s, `, "Sign in", true, false)`) {
		t.Fatalf("text= targets match by visible text: %s", s[len(s)-40:])
	}
}

func TestResolveScriptAgreesWithValidationOnThePrefix(t *testing.T) {
	// Leading whitespace before text= still means text; the prefix is case-sensitive.
	if s := resolveScript("m", "  text= Sign in ", false); !strings.HasSuffix(s, `, "Sign in", true, false)`) {
		t.Fatalf("padded text= target: %s", s[len(s)-40:])
	}
	if s := resolveScript("m", "TEXT=Sign in", false); !strings.HasSuffix(s, `, "TEXT=Sign in", false, false)`) {
		t.Fatalf("upper-case prefix is a selector: %s", s[len(s)-40:])
	}
}

func TestPlanTabs(t *testing.T) {
	tests := []struct {
		name    string
		tracked []string
		pages   []string
		max     int
		want    tabPlan
	}{
		{"nothing yet", nil, []string{"P1"}, 8, tabPlan{Front: "P1", Attach: true}},
		{"reuse the front tab", []string{"P1"}, []string{"P1"}, 8, tabPlan{Front: "P1"}},
		{"new tab in front, old still open", []string{"P1"}, []string{"P2", "P1"}, 8, tabPlan{Front: "P2", Attach: true}},
		{"switch back to a tracked tab", []string{"P1", "P2"}, []string{"P1", "P2"}, 8, tabPlan{Front: "P1"}},
		{"attached tab closed", []string{"P1"}, []string{"P3"}, 8, tabPlan{Front: "P3", Cancel: []string{"P1"}, Attach: true}},
		{"closed tab cancelled, front reused", []string{"P1", "P2"}, []string{"P2"}, 8, tabPlan{Front: "P2", Cancel: []string{"P1"}}},
		{"no page left", []string{"P1"}, nil, 8, tabPlan{Cancel: []string{"P1"}, Attach: true}},
		{"too many live tabs", []string{"A", "B"}, []string{"C", "A", "B"}, 2, tabPlan{Front: "C", Attach: true, TooMany: true}},
		{"closed tabs make room", []string{"A", "B"}, []string{"C", "A"}, 2, tabPlan{Front: "C", Cancel: []string{"B"}, Attach: true}},
	}
	for _, tt := range tests {
		got := planTabs(tt.tracked, tt.pages, tt.max)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: planTabs = %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// browser_type refuses a password field: the script checks the resolved element and answers "password" without marking
// it, and the bot is told to ask the user to take over. Clicking one is still allowed.
func TestResolveScriptRefusesPasswordFieldsWhenTyping(t *testing.T) {
	typing := resolveScript("m", "#pw", true)
	if !strings.HasSuffix(typing, `, "#pw", false, true)`) {
		t.Fatalf("typing flag not passed: %s", typing[len(typing)-40:])
	}
	if !strings.Contains(typing, "if (typing && el instanceof HTMLInputElement && el.type === 'password') return 'password';") {
		t.Fatal("the script does not check for a password input")
	}
	if strings.Index(typing, "return 'password'") > strings.Index(typing, "setAttribute('data-orole-target'") {
		t.Fatal("a password field must be refused before it is marked")
	}
	if s := resolveScript("m", "#pw", false); !strings.HasSuffix(s, `, "#pw", false, false)`) {
		t.Fatalf("click must not refuse password fields: %s", s[len(s)-40:])
	}
	for _, c := range []struct {
		found   string
		wantSel bool
		wantErr error
		text    string
	}{
		{resolveFound, true, nil, ""},
		{resolvePassword, false, errPasswordField, "ask_user_to_take_over"},
		{resolveNone, false, nil, "nothing visible"},
		{"", false, nil, "nothing visible"},
	} {
		sel, err := resolved(c.found, "m", "#pw")
		if (sel != "") != c.wantSel || (c.wantErr != nil && !errors.Is(err, c.wantErr)) || (c.text != "" && (err == nil || !strings.Contains(err.Error(), c.text))) {
			t.Errorf("%q: %q %v", c.found, sel, err)
		}
	}
}

// A hung attach is given up at its own deadline instead of blocking every later action; a request that ends first
// leaves it pending; a finished attach always wins.
func TestWaitAttach(t *testing.T) {
	never := make(chan struct{})
	finished := make(chan struct{})
	close(finished)
	fired := make(chan time.Time, 1)
	fired <- time.Now()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := waitAttach(context.Background(), never, fired); got != attachExpired {
		t.Errorf("hung attach past its deadline: %v", got)
	}
	if got := waitAttach(cancelled, never, make(chan time.Time)); got != attachCancelled {
		t.Errorf("request ended first: %v", got)
	}
	fired <- time.Now()
	if got := waitAttach(cancelled, finished, fired); got != attachDone {
		t.Errorf("finished attach: %v", got)
	}
	if attachTimeout <= 0 || attachTimeout > 30*time.Second {
		t.Errorf("attachTimeout = %v", attachTimeout)
	}
}

// settle gives up an attach past its deadline, so the next action is not blocked, and unblocks immediately.
func TestSettleGivesUpAHungAttach(t *testing.T) {
	p := newCDPPage("http://127.0.0.1:1")
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &cdpAttach{tab: cdpTab{ctx: context.Background(), cancel: cancel}, done: make(chan struct{}), deadline: time.Now().Add(-time.Second)}
	p.pending = a
	start := time.Now()
	p.mu.Lock()
	err := p.settle(context.Background())
	pending := p.pending
	p.mu.Unlock()
	if !errors.Is(err, guestagent.ErrNoBrowser) || pending != nil || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v, pending %v, took %v", err, pending, time.Since(start))
	}
	// When the given-up attach finally fails, it is released without blocking anything.
	a.err = errors.New("attach failed")
	close(a.done)
}
