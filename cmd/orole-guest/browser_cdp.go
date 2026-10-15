package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"github.com/rayorole/orole-os-desktop/internal/sandbox/guestagent"
	"github.com/rayorole/orole-os-desktop/internal/sandbox/wire"
)

// cdpEndpoint is Chromium's DevTools endpoint inside the guest (loopback only; the guest image starts Chromium with
// --remote-debugging-address=127.0.0.1 --remote-debugging-port=9222).
const cdpEndpoint = "http://127.0.0.1:9222"

// maxTrackedTabs bounds the tab contexts (each owns a websocket and a DevTools session) kept open at once.
const maxTrackedTabs = 8

// cdpPage drives the visible Chromium tab the user watches.
//
// Every page target it has attached to has one chromedp context in tabs, keyed by target id. Each action reads
// /json/list, drives the front page (reusing its context or attaching one), and cancels the contexts of targets that
// no longer exist. A context is cancelled only once its target is gone, because cancelling a context of a live tab
// makes chromedp close that tab in the user's visible Chromium. So at most maxTrackedTabs live tabs are tracked; a
// further one is refused with a clear error rather than leaking a connection per switch.
type cdpPage struct {
	endpoint string
	client   *http.Client

	mu      sync.Mutex
	alloc   context.Context
	tabs    map[string]*cdpTab
	pending *cdpAttach // an attach that outlived its request; folded into tabs by the next action
}

// cdpTab is the chromedp context attached to one page target.
type cdpTab struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// cdpAttach is an attach in flight. It has its own deadline: an attach that hangs is given up after attachTimeout
// so it cannot hold up every later action (each would otherwise wait for it until its own request ended).
type cdpAttach struct {
	tab      cdpTab
	done     chan struct{}
	err      error
	deadline time.Time
}

// attachTimeout bounds attaching to a tab, independent of the request that started it.
const attachTimeout = 20 * time.Second

var _ guestagent.BrowserPage = (*cdpPage)(nil)

func newCDPPage(endpoint string) *cdpPage {
	return &cdpPage{endpoint: endpoint, client: &http.Client{Timeout: 5 * time.Second}, tabs: map[string]*cdpTab{}}
}

// cdpTarget is one entry of Chromium's /json/list.
type cdpTarget struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// pageTargets returns the ids of the page targets, front tab first. An endpoint that cannot be reached is
// guestagent.ErrNoBrowser.
func pageTargets(ctx context.Context, client *http.Client, endpoint string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", guestagent.ErrNoBrowser, err)
	}
	defer resp.Body.Close()
	var list []cdpTarget
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("%w: %v", guestagent.ErrNoBrowser, err)
	}
	var ids []string
	for _, t := range list {
		if t.Type == "page" {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

// pageTarget returns the id of the first page target (the visible tab), or "" when Chromium has none.
func pageTarget(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	ids, err := pageTargets(ctx, client, endpoint)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[0], nil
}

// tabPlan is what to do with the tracked tab contexts given the page targets Chromium lists.
type tabPlan struct {
	Front   string   // the front page target, "" when there is none
	Cancel  []string // tracked targets that no longer exist: their contexts are safe to cancel
	Attach  bool     // the front target has no context (or there is no page and one must be opened)
	TooMany bool     // attaching would exceed max live tracked tabs
}

// planTabs is the pure bookkeeping behind tabContext. tracked are the ids that have a live context.
func planTabs(tracked, pages []string, max int) tabPlan {
	exists := make(map[string]bool, len(pages))
	for _, id := range pages {
		exists[id] = true
	}
	var plan tabPlan
	if len(pages) > 0 {
		plan.Front = pages[0]
	}
	live, front := 0, false
	for _, id := range tracked {
		if !exists[id] {
			plan.Cancel = append(plan.Cancel, id)
			continue
		}
		live++
		front = front || id == plan.Front
	}
	plan.Attach = !front
	plan.TooMany = plan.Attach && live >= max
	return plan
}

// tabContext returns the chromedp context of the visible tab and the id of its target, attaching first when needed.
// The request context bounds the lookup and the attach; it is never made the tab's deadline.
func (p *cdpPage) tabContext(ctx context.Context) (context.Context, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.settle(ctx); err != nil {
		return nil, "", err
	}
	pages, err := pageTargets(ctx, p.client, p.endpoint)
	if err != nil {
		return nil, "", err
	}
	tracked := make([]string, 0, len(p.tabs))
	for id, t := range p.tabs {
		if t.ctx.Err() != nil { // the connection died: nothing live to protect
			t.cancel()
			delete(p.tabs, id)
			continue
		}
		tracked = append(tracked, id)
	}
	plan := planTabs(tracked, pages, maxTrackedTabs)
	for _, id := range plan.Cancel {
		p.tabs[id].cancel() // the target is gone, so this closes nothing
		delete(p.tabs, id)
	}
	if !plan.Attach {
		return p.tabs[plan.Front].ctx, plan.Front, nil
	}
	if plan.TooMany {
		return nil, "", fmt.Errorf("too many browser tabs are open (at most %d are driven); close some and try again", maxTrackedTabs)
	}
	if p.alloc == nil {
		// The path-less ws:// URL makes chromedp look up the browser's websocket address on /json/version.
		p.alloc, _ = chromedp.NewRemoteAllocator(context.Background(), strings.Replace(p.endpoint, "http://", "ws://", 1)+"/")
	}
	var opts []chromedp.ContextOption
	if plan.Front != "" {
		opts = append(opts, chromedp.WithTargetID(target.ID(plan.Front)))
	}
	tab, cancel := chromedp.NewContext(p.alloc, opts...)
	a := &cdpAttach{tab: cdpTab{ctx: tab, cancel: cancel}, done: make(chan struct{}), deadline: time.Now().Add(attachTimeout)}
	go func() {
		a.err = chromedp.Run(tab)
		close(a.done)
	}()
	p.pending = a
	if err := p.settle(ctx); err != nil {
		return nil, "", err
	}
	return tab, targetID(tab), nil
}

// settle waits for an in-flight attach, bounded by ctx and by the attach's own deadline, and files it (see file). A
// late attach that outlived its request is filed here by the next action instead of being abandoned. One that is past
// its deadline is given up: it no longer blocks actions, and is filed by a goroutine if it ever finishes (its context
// is not cancelled meanwhile, as that could close the user's tab). Callers hold p.mu.
func (p *cdpPage) settle(ctx context.Context) error {
	a := p.pending
	if a == nil {
		return nil
	}
	timer := time.NewTimer(time.Until(a.deadline))
	defer timer.Stop()
	switch waitAttach(ctx, a.done, timer.C) {
	case attachCancelled:
		return ctx.Err()
	case attachExpired:
		p.pending = nil
		go func() {
			<-a.done
			p.mu.Lock()
			defer p.mu.Unlock()
			_ = p.file(a)
		}()
		return fmt.Errorf("%w: Chromium did not let the browser attach to its tab within %s; try again", guestagent.ErrNoBrowser, attachTimeout)
	}
	p.pending = nil
	return p.file(a)
}

// file stores a finished attach in tabs when it worked and releases it when it failed (nothing is attached then, so
// cancelling closes no tab). Callers hold p.mu.
func (p *cdpPage) file(a *cdpAttach) error {
	if a.err != nil {
		a.tab.cancel()
		return fmt.Errorf("%w: %v", guestagent.ErrNoBrowser, a.err)
	}
	t := a.tab
	p.tabs[targetID(t.ctx)] = &t
	return nil
}

// How waiting for an attach ended.
type attachWait int

const (
	attachDone      attachWait = iota // the attach finished (worked or failed)
	attachCancelled                   // the request ended first; the attach stays pending for the next action
	attachExpired                     // the attach's own deadline passed
)

// waitAttach waits for done, the request ctx or the attach deadline (expired), whichever comes first; a finished
// attach wins a tie.
func waitAttach(ctx context.Context, done <-chan struct{}, expired <-chan time.Time) attachWait {
	select {
	case <-done:
		return attachDone
	default:
	}
	select {
	case <-done:
		return attachDone
	case <-ctx.Done():
		return attachCancelled
	case <-expired:
		return attachExpired
	}
}

// targetID is the id of the page target a tab context is attached to.
func targetID(tab context.Context) string {
	return string(chromedp.FromContext(tab).Target.TargetID)
}

// runOn executes actions on tab, bounded by ctx without ever cancelling the tab itself.
func runOn(ctx, tab context.Context, actions ...chromedp.Action) error {
	runCtx, cancel := context.WithCancel(tab)
	defer cancel()
	defer context.AfterFunc(ctx, cancel)()
	if err := chromedp.Run(runCtx, actions...); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if tab.Err() != nil { // the connection to Chromium was lost mid-action
			return fmt.Errorf("%w: %v", guestagent.ErrNoBrowser, err)
		}
		return err
	}
	return nil
}

// run executes actions on the visible tab.
func (p *cdpPage) run(ctx context.Context, actions ...chromedp.Action) error {
	tab, _, err := p.tabContext(ctx)
	if err != nil {
		return err
	}
	return runOn(ctx, tab, actions...)
}

var errTabChanged = errors.New("the visible browser tab changed during the action; try again")

// onFront checks that id is still the visible tab, so a two-step action does not wait on a tab nobody sees.
func (p *cdpPage) onFront(ctx context.Context, id string) error {
	front, err := pageTarget(ctx, p.client, p.endpoint)
	if err != nil {
		return err
	}
	if front != id {
		return errTabChanged
	}
	return nil
}

func (p *cdpPage) Navigate(ctx context.Context, url string) error {
	return p.run(ctx, chromedp.Navigate(url))
}

// Click and Type resolve and act on the same tab context, captured once.
func (p *cdpPage) Click(ctx context.Context, target string) error {
	tab, id, err := p.tabContext(ctx)
	if err != nil {
		return err
	}
	sel, err := resolveOn(ctx, tab, target, false)
	if err != nil {
		return err
	}
	if err := p.onFront(ctx, id); err != nil {
		return err
	}
	return runOn(ctx, tab, chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible))
}

func (p *cdpPage) Type(ctx context.Context, target, text string) error {
	tab, id, err := p.tabContext(ctx)
	if err != nil {
		return err
	}
	sel, err := resolveOn(ctx, tab, target, true)
	if err != nil {
		return err
	}
	if err := p.onFront(ctx, id); err != nil {
		return err
	}
	return runOn(ctx, tab, chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible), chromedp.SendKeys(sel, text, chromedp.ByQuery))
}

func (p *cdpPage) Info(ctx context.Context) (string, string, error) {
	var title, url string
	err := p.run(ctx, chromedp.Title(&title), chromedp.Location(&url))
	return title, url, err
}

func (p *cdpPage) Text(ctx context.Context) (string, error) {
	var text string
	err := p.run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text))
	return text, err
}

func (p *cdpPage) Screenshot(ctx context.Context) ([]byte, error) {
	var png []byte
	err := p.run(ctx, chromedp.CaptureScreenshot(&png))
	return png, err
}

// errPasswordField is what browser_type answers for a password field: the bot never types passwords.
var errPasswordField = errors.New("that is a password field, and you never type passwords: call ask_user_to_take_over and ask the user to type it themselves")

// resolveOn marks the element target names, with a fresh random marker, on tab and returns a selector for it.
// typing refuses a password field (errPasswordField).
func resolveOn(ctx, tab context.Context, target string, typing bool) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	marker := hex.EncodeToString(raw[:])
	var found string
	if err := runOn(ctx, tab, chromedp.Evaluate(resolveScript(marker, target, typing), &found)); err != nil {
		return "", err
	}
	return resolved(found, marker, target)
}

// resolved turns resolveJS's answer into the marked element's selector or an error.
func resolved(found, marker, target string) (string, error) {
	switch found {
	case resolveFound:
		return `[data-orole-target="` + marker + `"]`, nil
	case resolvePassword:
		return "", errPasswordField
	}
	return "", fmt.Errorf("nothing visible on the page matches %q", target)
}

// Answers of resolveJS.
const (
	resolveFound    = "found"
	resolveNone     = "none"
	resolvePassword = "password"
)

// resolveJS finds a visible element by CSS selector, or by its visible text (exact, then partial match), marks it
// with data-orole-target=<marker> and scrolls it into view. Its arguments are the marker and target (JSON strings) and
// two booleans: match by text, and refuse a password field (for typing). It answers "found", "none" or "password"
// (a password input when typing; the element is then not marked).
const resolveJS = `(function (marker, target, byText, typing) {
  document.querySelectorAll('[data-orole-target]').forEach(function (e) { e.removeAttribute('data-orole-target'); });
  function visible(el) { var r = el.getBoundingClientRect(); var s = getComputedStyle(el); return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none'; }
  var el = null;
  if (!byText) { try { el = document.querySelector(target); } catch (e) { el = null; } if (el && !visible(el)) el = null; }
  if (!el) {
    var want = target.trim().toLowerCase(), partial = null;
    var nodes = document.querySelectorAll('a,button,input,textarea,select,label,summary,[role],[onclick],[contenteditable]');
    for (var i = 0; i < nodes.length; i++) {
      var n = nodes[i];
      if (!visible(n)) continue;
      var label = (n.innerText || n.value || n.getAttribute('aria-label') || n.getAttribute('placeholder') || '').trim().toLowerCase();
      if (label === want) { el = n; break; }
      if (!partial && label && label.indexOf(want) >= 0) partial = n;
    }
    el = el || partial;
  }
  if (!el) return 'none';
  if (typing && el instanceof HTMLInputElement && el.type === 'password') return 'password';
  el.setAttribute('data-orole-target', marker);
  el.scrollIntoView({block: 'center', inline: 'center'});
  return 'found';
})(%s, %s, %t, %t)`

// resolveScript builds resolveJS for target. wire.ParseTarget decides, exactly as request validation does, whether
// the target is matched by visible text ("text=" prefix) or as a CSS selector (falling back to text if no match).
// typing makes the script refuse a password field.
func resolveScript(marker, target string, typing bool) string {
	value, byText := wire.ParseTarget(target)
	quoted, _ := json.Marshal(value) // also escapes U+2028/U+2029
	quotedMarker, _ := json.Marshal(marker)
	return fmt.Sprintf(resolveJS, quotedMarker, quoted, byText, typing)
}
