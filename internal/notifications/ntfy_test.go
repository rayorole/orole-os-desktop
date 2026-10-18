package notifications

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/credentials"
)

type received struct {
	auth, contentType, path string
	body                    ntfyPayload
}

// ntfyServer records every publish it gets and answers with status.
func ntfyServer(t *testing.T, status int) (*httptest.Server, func() []received) {
	t.Helper()
	var mu sync.Mutex
	var got []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p ntfyPayload
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		mu.Lock()
		got = append(got, received{auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), path: r.URL.Path, body: p})
		mu.Unlock()
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = w.Write([]byte(`{"code":40301,"error":"forbidden: no access to this topic"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), got...)
	}
}

func newNtfy(t *testing.T) (*Ntfy, *Store) {
	t.Helper()
	db := open(t, filepath.Join(t.TempDir(), "orole.db")).DB
	return NewNtfy(db, credentials.NewMemory(), slog.New(slog.NewTextHandler(io.Discard, nil))), New(db)
}

func enable(t *testing.T, n *Ntfy, server string, token *string) NtfyView {
	t.Helper()
	v, err := n.Update(ctx, NtfyUpdate{Enabled: true, Server: server, Token: token})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPushPostsJSONWithKindPriorityTagsAndAuth(t *testing.T) {
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	token := "tk_secret"
	view := enable(t, n, srv.URL+"/", &token)

	cases := []struct {
		kind     Kind
		priority int
		tag      string
	}{
		{Approval, 4, "warning"}, {Failed, 4, "x"}, {Finished, 3, "white_check_mark"}, {Reply, 3, "speech_balloon"}, {Message, 3, "bell"},
	}
	for _, c := range cases {
		if err := n.Deliver(ctx, Notification{Kind: c.kind, Title: "Dépanneur ✓ 日本語", Body: "body"}); err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
	}
	reqs := got()
	if len(reqs) != len(cases) {
		t.Fatalf("got %d requests", len(reqs))
	}
	for i, c := range cases {
		r := reqs[i]
		if r.path != "/" || r.contentType != "application/json" || r.auth != "Bearer tk_secret" {
			t.Fatalf("%s: path %q type %q auth %q", c.kind, r.path, r.contentType, r.auth)
		}
		if r.body.Topic != view.Topic || r.body.Title != "Dépanneur ✓ 日本語" || r.body.Message != "body" || r.body.Priority != c.priority || len(r.body.Tags) != 1 || r.body.Tags[0] != c.tag {
			t.Fatalf("%s: %+v", c.kind, r.body)
		}
	}
}

func TestPushSendsNoAuthorizationWithoutToken(t *testing.T) {
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	enable(t, n, srv.URL, nil)
	if err := n.Deliver(ctx, Notification{Kind: Message, Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if r := got()[0]; r.auth != "" || r.body.Message == "" {
		t.Fatalf("auth %q, message %q (an empty body still needs text)", r.auth, r.body.Message)
	}
}

func TestPushClickLinksOnlyToViewsTheDeepLinkParserAccepts(t *testing.T) {
	for target, want := range map[string]string{
		"task:t1": "orole://open/agents", "automation:a1": "orole://open/automations",
		"bot:b1": "", "computer:b1": "", "": "",
	} {
		if got := pushClick(target); got != want {
			t.Errorf("%q: %q, want %q", target, got, want)
		}
	}
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	enable(t, n, srv.URL, nil)
	_ = n.Deliver(ctx, Notification{Kind: Reply, Title: "t", Target: "bot:b1"})
	if raw, _ := json.Marshal(got()[0].body); strings.Contains(string(raw), "click") {
		t.Fatalf("click must be omitted without a link: %s", raw)
	}
}

func TestPushTruncatesLongMessagesOnACharacterBoundary(t *testing.T) {
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	enable(t, n, srv.URL, nil)
	if err := n.Deliver(ctx, Notification{Kind: Message, Title: "t", Body: strings.Repeat("日", 5000)}); err != nil {
		t.Fatal(err)
	}
	msg := got()[0].body.Message
	if len(msg) > MaxPushMessage || !utf8.ValidString(msg) || !strings.HasSuffix(msg, "…") {
		t.Fatalf("len %d valid %v", len(msg), utf8.ValidString(msg))
	}
	if short := truncateBytes("short", 10); short != "short" {
		t.Fatalf("short text changed: %q", short)
	}
}

func TestPushIsSkippedWhileSwitchedOff(t *testing.T) {
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	enable(t, n, srv.URL, nil)
	if _, err := n.Update(ctx, NtfyUpdate{Enabled: false, Server: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := n.Deliver(ctx, Notification{Kind: Failed, Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if len(got()) != 0 {
		t.Fatal("nothing may be sent while push is off")
	}
}

func TestPushFailureIsAnErrorWithoutTopicOrToken(t *testing.T) {
	srv, _ := ntfyServer(t, 403)
	n, _ := newNtfy(t)
	token := "tk_secret"
	view := enable(t, n, srv.URL, &token)
	err := n.Deliver(ctx, Notification{Kind: Failed, Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "no access to this topic") {
		t.Fatalf("error %v", err)
	}
	if strings.Contains(err.Error(), view.Topic) || strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks a secret: %v", err)
	}
	// An unreachable server fails too, and also names neither secret.
	srv.Close()
	err = n.Deliver(ctx, Notification{Kind: Failed, Title: "t"})
	if err == nil || strings.Contains(err.Error(), view.Topic) || strings.Contains(err.Error(), token) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestPushDoesNotFollowRedirects(t *testing.T) {
	other, got := ntfyServer(t, 200)
	redirect := httptest.NewServer(http.RedirectHandler(other.URL, http.StatusTemporaryRedirect))
	t.Cleanup(redirect.Close)
	n, _ := newNtfy(t)
	token := "tk_secret"
	enable(t, n, redirect.URL, &token)
	if err := n.Deliver(ctx, Notification{Kind: Failed, Title: "t"}); err == nil {
		t.Fatal("a redirect must be reported as a failure")
	}
	if len(got()) != 0 {
		t.Fatal("the redirect target must not receive the topic or token")
	}
}

func TestPushRunsInTheBackground(t *testing.T) {
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	enable(t, n, srv.URL, nil)
	n.Push(Notification{Kind: Approval, Title: "t"})
	n.Wait()
	if len(got()) != 1 {
		t.Fatalf("got %d", len(got()))
	}
	// A failing server never surfaces: Push returns nothing and does not panic.
	srv.Close()
	n.Push(Notification{Kind: Approval, Title: "t"})
	n.Wait()
}

func TestSendTestWorksBeforePushIsSwitchedOn(t *testing.T) {
	srv, got := ntfyServer(t, 200)
	n, _ := newNtfy(t)
	if err := n.SendTest(ctx); err == nil {
		t.Fatal("no topic yet: the test must say push needs enabling")
	}
	view := enable(t, n, srv.URL, nil)
	if _, err := n.Update(ctx, NtfyUpdate{Enabled: false, Server: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := n.SendTest(ctx); err != nil {
		t.Fatal(err)
	}
	if r := got(); len(r) != 1 || r[0].body.Topic != view.Topic || r[0].body.Title != "Orole test push" {
		t.Fatalf("%+v", r)
	}
}

func TestConfigDefaultsAndTopicLifecycle(t *testing.T) {
	n, _ := newNtfy(t)
	v, err := n.Config(ctx)
	if err != nil || v.Enabled || v.Server != DefaultNtfyServer || v.Topic != "" || v.TokenSet {
		t.Fatalf("defaults (seeded by the migration): %+v %v", v, err)
	}
	// Saving with push off creates no topic; switching on does, once.
	if v, _ = n.Update(ctx, NtfyUpdate{Server: ""}); v.Topic != "" || v.Server != DefaultNtfyServer {
		t.Fatalf("off: %+v", v)
	}
	first := enable(t, n, "", nil)
	if len(first.Topic) < 30 || !strings.HasPrefix(first.Topic, "orole-") || strings.Trim(first.Topic, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		t.Fatalf("topic %q", first.Topic)
	}
	if again := enable(t, n, "", nil); again.Topic != first.Topic {
		t.Fatal("enabling again must keep the topic")
	}
	regen, err := n.Update(ctx, NtfyUpdate{Enabled: true, Server: "", RegenerateTopic: true})
	if err != nil || regen.Topic == first.Topic || len(regen.Topic) < 30 {
		t.Fatalf("regenerate: %+v %v", regen, err)
	}
}

func TestTokenIsStoredInTheCredentialStoreNeverTheDatabase(t *testing.T) {
	n, _ := newNtfy(t)
	token := "tk_secret"
	v := enable(t, n, "", &token)
	if !v.TokenSet {
		t.Fatal("token not reported as set")
	}
	if raw, _ := json.Marshal(v); strings.Contains(string(raw), token) {
		t.Fatalf("the view must never carry the token: %s", raw)
	}
	var settings string
	if err := n.DB.QueryRow(`SELECT group_concat(value) FROM settings`).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(settings, token) || strings.Contains(settings, v.Topic) {
		t.Fatal("secrets reached the settings table")
	}
	// nil keeps it, "" removes it.
	if v, _ = n.Update(ctx, NtfyUpdate{Enabled: true}); !v.TokenSet {
		t.Fatal("nil token must keep the stored one")
	}
	empty := " "
	if v, _ = n.Update(ctx, NtfyUpdate{Enabled: true, Token: &empty}); v.TokenSet {
		t.Fatal("empty token must remove it")
	}
}

func TestUpdateRejectsBadServerAndToken(t *testing.T) {
	n, _ := newNtfy(t)
	for _, server := range []string{"ntfy.sh", "ftp://ntfy.sh", "https://", "https://u:p@ntfy.sh", "https://ntfy.sh/?x=1", "https://ntfy.sh/#f"} {
		if _, err := n.Update(ctx, NtfyUpdate{Enabled: true, Server: server}); err == nil {
			t.Errorf("accepted server %q", server)
		}
	}
	for _, token := range []string{"two words", "line\nbreak", strings.Repeat("a", maxTokenLen+1)} {
		if _, err := n.Update(ctx, NtfyUpdate{Enabled: true, Token: &token}); err == nil {
			t.Errorf("accepted token %q", token)
		}
	}
	if got, err := NormalizeServer(" http://localhost:2586/ "); err != nil || got != "http://localhost:2586" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestPushPreferencePersistsPerKindAndDefaultsOff(t *testing.T) {
	s := store(t)
	p, err := s.Prefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range Kinds {
		if p.For(k).Push {
			t.Fatalf("%s pushes by default", k)
		}
	}
	p.Kinds[Failed] = KindPref{InApp: true, Sound: true, Push: true}
	p.Kinds[Reply] = KindPref{InApp: false, Sound: false, Push: true}
	if _, err := s.SetPrefs(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := s.Prefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.For(Failed).Push || !got.For(Reply).Push || got.For(Finished).Push || got.For(Approval).Push {
		t.Fatalf("persisted prefs: %+v", got)
	}
	// Preferences saved before the Push field existed read as off.
	if _, err := s.DB.Exec(`UPDATE settings SET value=? WHERE key=?`, `{"kinds":{"failed":{"inApp":true,"sound":false}}}`, PrefsKey); err != nil {
		t.Fatal(err)
	}
	if old, _ := s.Prefs(ctx); old.For(Failed).Push || old.For(Failed).Sound {
		t.Fatalf("legacy prefs: %+v", old.For(Failed))
	}
}

func TestNotifyReportsPushPerKindEvenWhenHiddenInApp(t *testing.T) {
	s := store(t)
	p := DefaultPrefs()
	p.Kinds[Failed] = KindPref{InApp: true, Push: true}
	p.Kinds[Reply] = KindPref{InApp: false, Push: true}
	if _, err := s.SetPrefs(ctx, p); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Notify(ctx, Input{Source: "task", Kind: Failed, Title: "t", Level: Error})
	if !r.Stored || !r.Push {
		t.Fatalf("failed: %+v", r)
	}
	r, _ = s.Notify(ctx, Input{Source: "bot", Kind: Reply, Title: "t"})
	if r.Stored || !r.Push {
		t.Fatalf("a hidden kind is not stored but may still push: %+v", r)
	}
	r, _ = s.Notify(ctx, Input{Source: "task", Kind: Finished, Title: "t"})
	if !r.Stored || r.Push {
		t.Fatalf("finished must not push: %+v", r)
	}
	// A duplicate open approval is not a new event: no second push.
	p.Kinds[Approval] = KindPref{InApp: true, Push: true}
	_, _ = s.SetPrefs(ctx, p)
	in := Input{Source: "task", Kind: Approval, Title: "t", Ref: "task:t1"}
	if r, _ = s.Notify(ctx, in); !r.Push {
		t.Fatalf("first approval: %+v", r)
	}
	if r, _ = s.Notify(ctx, in); r.Push || r.Stored {
		t.Fatalf("repeated approval: %+v", r)
	}
}
