package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rayorole/orole-os-desktop/internal/credentials"
)

// ntfy (https://ntfy.sh) pushes a notification to the user's phone or PC app through an HTTP
// POST. The server URL and the master switch live in the settings table; the topic and the
// optional access token are secrets (on a public server the topic is the only thing keeping
// others from reading or posting) and live in the OS credential store.
const (
	// NtfySettingsKey is the settings key holding the server URL and the master switch.
	NtfySettingsKey = "notifications.ntfy"
	// DefaultNtfyServer is the public ntfy instance.
	DefaultNtfyServer = "https://ntfy.sh"

	ntfyTopicKey = "notifications/ntfy/topic"
	ntfyTokenKey = "notifications/ntfy/token"

	// MaxPushMessage keeps the message under ntfy's limit of 4096 bytes.
	MaxPushMessage = 4000
	maxTokenLen    = 512
	ntfyTimeout    = 10 * time.Second
	// ntfyInflight bounds concurrent deliveries so a burst of events cannot pile up goroutines.
	ntfyInflight = 8
	topicBytes   = 24 // 24 random bytes = 32 URL-safe characters
)

// NtfyView is the configuration as the settings page shows it. The token never leaves the
// credential store; TokenSet says whether one exists.
type NtfyView struct {
	Enabled  bool   `json:"enabled"`
	Server   string `json:"server"`
	Topic    string `json:"topic"`
	TokenSet bool   `json:"tokenSet"`
}

// NtfyUpdate replaces the master switch and server, and optionally the secrets.
type NtfyUpdate struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
	// Token nil keeps the stored token, "" removes it, anything else replaces it.
	Token *string `json:"token"`
	// RegenerateTopic discards the topic for a new random one.
	RegenerateTopic bool `json:"regenerateTopic"`
}

type ntfyStored struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"`
}

// ntfyPayload is the JSON publish body. JSON, not headers, so UTF-8 titles survive.
type ntfyPayload struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

// Ntfy sends notifications to an ntfy server.
type Ntfy struct {
	DB    *sql.DB
	Creds credentials.Store
	HTTP  *http.Client
	Log   *slog.Logger

	slots chan struct{}
	wg    sync.WaitGroup
}

// NewNtfy returns a sender over the settings in db and the secrets in creds.
func NewNtfy(db *sql.DB, creds credentials.Store, log *slog.Logger) *Ntfy {
	if log == nil {
		log = slog.Default()
	}
	return &Ntfy{
		DB: db, Creds: creds, Log: log,
		// Redirects are not followed: a POST would turn into a GET and the body, topic and
		// token would be lost or sent somewhere the user did not configure.
		HTTP:  &http.Client{Timeout: ntfyTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		slots: make(chan struct{}, ntfyInflight),
	}
}

// Wait blocks until every delivery started by Push has finished.
func (n *Ntfy) Wait() { n.wg.Wait() }

func (n *Ntfy) stored(ctx context.Context) (ntfyStored, error) {
	out := ntfyStored{Server: DefaultNtfyServer}
	var raw string
	err := n.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, NtfySettingsKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return ntfyStored{Server: DefaultNtfyServer}, fmt.Errorf("decode ntfy settings: %w", err)
	}
	if out.Server == "" {
		out.Server = DefaultNtfyServer
	}
	return out, nil
}

func (n *Ntfy) secret(key string) (string, error) {
	b, err := n.Creds.Get(key)
	if errors.Is(err, credentials.ErrNotFound) {
		return "", nil
	}
	return string(b), err
}

// Config returns the stored configuration with the topic and whether a token is set.
func (n *Ntfy) Config(ctx context.Context) (NtfyView, error) {
	st, err := n.stored(ctx)
	if err != nil {
		return NtfyView{}, err
	}
	topic, err := n.secret(ntfyTopicKey)
	if err != nil {
		return NtfyView{}, fmt.Errorf("read ntfy topic: %w", err)
	}
	token, err := n.secret(ntfyTokenKey)
	if err != nil {
		return NtfyView{}, fmt.Errorf("read ntfy token: %w", err)
	}
	return NtfyView{Enabled: st.Enabled, Server: st.Server, Topic: topic, TokenSet: token != ""}, nil
}

// GenerateTopic returns "orole-" and 32 random URL-safe characters: a valid ntfy topic
// (letters, digits, - and _, up to 64) nobody can guess.
func GenerateTopic() (string, error) {
	b := make([]byte, topicBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "orole-" + base64.RawURLEncoding.EncodeToString(b), nil
}

// NormalizeServer validates a server URL and returns it without a trailing slash. An empty
// value means the public server. Plain http is allowed for a server on the user's own network.
func NormalizeServer(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultNtfyServer, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("%w: the ntfy server must be an http(s) URL such as %s", ErrInvalid, DefaultNtfyServer)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func validToken(t string) bool {
	return t != "" && len(t) <= maxTokenLen && !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// Update applies u. Turning push on creates the topic when there is none.
func (n *Ntfy) Update(ctx context.Context, u NtfyUpdate) (NtfyView, error) {
	server, err := NormalizeServer(u.Server)
	if err != nil {
		return NtfyView{}, err
	}
	if u.Token != nil {
		t := strings.TrimSpace(*u.Token)
		switch {
		case t == "":
			err = n.Creds.Delete(ntfyTokenKey)
		case !validToken(t):
			return NtfyView{}, fmt.Errorf("%w: the access token must be one word of at most %d characters", ErrInvalid, maxTokenLen)
		default:
			err = n.Creds.Set(ntfyTokenKey, []byte(t))
		}
		if err != nil {
			return NtfyView{}, fmt.Errorf("store ntfy token: %w", err)
		}
	}
	topic, err := n.secret(ntfyTopicKey)
	if err != nil {
		return NtfyView{}, fmt.Errorf("read ntfy topic: %w", err)
	}
	if u.RegenerateTopic || (u.Enabled && topic == "") {
		if topic, err = GenerateTopic(); err != nil {
			return NtfyView{}, err
		}
		if err := n.Creds.Set(ntfyTopicKey, []byte(topic)); err != nil {
			return NtfyView{}, fmt.Errorf("store ntfy topic: %w", err)
		}
	}
	b, err := json.Marshal(ntfyStored{Enabled: u.Enabled, Server: server})
	if err != nil {
		return NtfyView{}, err
	}
	if _, err := n.DB.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		NtfySettingsKey, string(b), time.Now().Unix()); err != nil {
		return NtfyView{}, err
	}
	return n.Config(ctx)
}

// pushStyle maps a kind to its ntfy priority (4 = high, 3 = default) and emoji tag.
func pushStyle(k Kind) (priority int, tag string) {
	switch k {
	case Approval:
		return 4, "warning"
	case Failed:
		return 4, "x"
	case Finished:
		return 3, "white_check_mark"
	case Reply:
		return 3, "speech_balloon"
	default:
		return 3, "bell"
	}
}

// pushClick returns the orole:// link a tap on the push opens. The deep-link parser accepts
// only whole views (internal/applications.ParseDeepLink), not single items, so a task or
// flow opens its list; bots and computers have no allowed view and get no link.
func pushClick(target string) string {
	switch kind, _, _ := strings.Cut(target, ":"); kind {
	case "task":
		return "orole://open/agents"
	case "automation":
		return "orole://open/automations"
	}
	return ""
}

// truncateBytes cuts s to at most max bytes on a character boundary, marking the cut.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const mark = "…"
	cut := max - len(mark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + mark
}

func buildPayload(topic string, nt Notification) ntfyPayload {
	priority, tag := pushStyle(nt.Kind)
	msg := truncateBytes(nt.Body, MaxPushMessage)
	if msg == "" {
		msg = "Open Orole to see it."
	}
	return ntfyPayload{Topic: topic, Title: nt.Title, Message: msg, Priority: priority, Tags: []string{tag}, Click: pushClick(nt.Target)}
}

// post publishes one payload. Errors never contain the topic or the token.
func (n *Ntfy) post(ctx context.Context, server, token string, p ntfyPayload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := n.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil
	}
	var e struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &e)
	if e.Error == "" {
		return fmt.Errorf("ntfy answered %s", resp.Status)
	}
	return fmt.Errorf("ntfy answered %s: %s", resp.Status, truncateBytes(e.Error, 200))
}

// target loads what a delivery needs; ok is false when push is off.
func (n *Ntfy) target(ctx context.Context) (server, topic, token string, ok bool, err error) {
	st, err := n.stored(ctx)
	if err != nil || !st.Enabled {
		return "", "", "", false, err
	}
	if topic, err = n.secret(ntfyTopicKey); err != nil {
		return "", "", "", false, fmt.Errorf("read ntfy topic: %w", err)
	}
	if topic == "" {
		return "", "", "", false, errors.New("push is on but has no topic")
	}
	if token, err = n.secret(ntfyTokenKey); err != nil {
		return "", "", "", false, fmt.Errorf("read ntfy token: %w", err)
	}
	return st.Server, topic, token, true, nil
}

// Deliver sends nt when push is switched on. The caller has already checked the kind's toggle.
func (n *Ntfy) Deliver(ctx context.Context, nt Notification) error {
	server, topic, token, ok, err := n.target(ctx)
	if err != nil || !ok {
		return err
	}
	return n.post(ctx, server, token, buildPayload(topic, nt))
}

// Push delivers nt in the background and never blocks or fails the notification: a failure
// is logged, and a burst beyond the in-flight limit is dropped.
func (n *Ntfy) Push(nt Notification) {
	select {
	case n.slots <- struct{}{}:
	default:
		n.Log.Warn("push notification dropped: too many deliveries in flight", "kind", nt.Kind)
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		defer func() { <-n.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), ntfyTimeout)
		defer cancel()
		if err := n.Deliver(ctx, nt); err != nil {
			n.Log.Warn("push notification not sent", "kind", nt.Kind, "err", err)
		}
	}()
}

// SendTest sends one sample push with the saved configuration, whether or not push is
// switched on, so the user can check the server, topic and token first.
func (n *Ntfy) SendTest(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ntfyTimeout)
	defer cancel()
	st, err := n.stored(ctx)
	if err != nil {
		return err
	}
	topic, err := n.secret(ntfyTopicKey)
	if err != nil {
		return fmt.Errorf("read ntfy topic: %w", err)
	}
	if topic == "" {
		return fmt.Errorf("%w: switch push on first so a topic is created", ErrInvalid)
	}
	token, err := n.secret(ntfyTokenKey)
	if err != nil {
		return fmt.Errorf("read ntfy token: %w", err)
	}
	return n.post(ctx, st.Server, token, buildPayload(topic, Notification{Kind: Message, Title: "Orole test push", Body: "Push notifications are working."}))
}

// NtfyTestResult is what the Send test button reports: success, or why the push failed.
type NtfyTestResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}
