package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/rayorole/orole-os-desktop/internal/credentials"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
	"golang.org/x/oauth2"
)

// credBlob is the single secret record for a server, stored as JSON in the OS
// credential store under mcp/<serverID>. Nothing in it is ever written to SQLite or logs.
type credBlob struct {
	Kind          string    `json:"kind"` // oauth | bearer
	AccessToken   string    `json:"accessToken"`
	RefreshToken  string    `json:"refreshToken,omitempty"`
	Expiry        time.Time `json:"expiry,omitempty"`
	ClientID      string    `json:"clientId,omitempty"`
	ClientSecret  string    `json:"clientSecret,omitempty"`
	AuthStyle     string    `json:"authStyle,omitempty"` // params | header
	TokenEndpoint string    `json:"tokenEndpoint,omitempty"`
	Resource      string    `json:"resource,omitempty"`
	Scope         string    `json:"scope,omitempty"`
}

func credKey(id string) string         { return "mcp/" + id }
func clientSecretKey(id string) string { return "mcp/" + id + "/client-secret" }

const refreshSkew = 30 * time.Second

// authSession owns one server's credentials: loading, refreshing (single-flight) and clearing.
type authSession struct {
	id        string
	creds     credentials.Store
	hc        *http.Client
	sourceURL func(context.Context) (string, error) // configured resource, not discovered metadata
	onChange  func(*credBlob)                       // non-secret bookkeeping (expiry) after a refresh

	mu     sync.Mutex
	blob   *credBlob
	loaded bool

	// Keep retired credentials for this session too: in-flight responses can
	// echo the token that was replaced or revoked while the request ran.
	secretMu sync.RWMutex
	secrets  []string
}

func newAuthSession(id string, creds credentials.Store, hc *http.Client, sourceURL func(context.Context) (string, error)) *authSession {
	return &authSession{id: id, creds: creds, hc: hc, sourceURL: sourceURL}
}

func (a *authSession) remember(secrets ...string) {
	a.secretMu.Lock()
	defer a.secretMu.Unlock()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		found := false
		for _, known := range a.secrets {
			if known == secret {
				found = true
				break
			}
		}
		if !found {
			a.secrets = append(a.secrets, secret)
		}
	}
	// Replace longer values first when a rotated token contains an old one.
	sort.Slice(a.secrets, func(i, j int) bool { return len(a.secrets[i]) > len(a.secrets[j]) })
}

func (a *authSession) scrub(s string) string {
	a.secretMu.RLock()
	defer a.secretMu.RUnlock()
	for _, secret := range a.secrets {
		s = strings.ReplaceAll(s, secret, "[REDACTED]")
	}
	return s
}

func (a *authSession) scrubJSON(raw []byte) []byte {
	a.secretMu.RLock()
	empty := len(a.secrets) == 0
	a.secretMu.RUnlock()
	if empty {
		return raw
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return []byte("null")
	}
	var scrubValue func(any) any
	scrubValue = func(value any) any {
		switch v := value.(type) {
		case string:
			return a.scrub(v)
		case []any:
			for i := range v {
				v[i] = scrubValue(v[i])
			}
		case map[string]any:
			out := make(map[string]any, len(v))
			for key, item := range v {
				out[a.scrub(key)] = scrubValue(item)
			}
			return out
		}
		return value
	}
	out, err := json.Marshal(scrubValue(value))
	if err != nil {
		return []byte("null")
	}
	return out
}

func (a *authSession) loadLocked() (*credBlob, error) {
	if a.loaded {
		return a.blob, nil
	}
	raw, err := a.creds.Get(credKey(a.id))
	if errors.Is(err, credentials.ErrNotFound) {
		a.loaded, a.blob = true, nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	var b credBlob
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("credential record unreadable: %w", err)
	}
	a.remember(b.AccessToken, b.RefreshToken, b.ClientSecret)
	a.loaded, a.blob = true, &b
	return a.blob, nil
}

func (a *authSession) saveLocked(b *credBlob) error {
	a.remember(b.AccessToken, b.RefreshToken, b.ClientSecret)
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	if err := a.creds.Set(credKey(a.id), raw); err != nil {
		return fmt.Errorf("credential store: %w", err)
	}
	a.blob, a.loaded = b, true
	if a.onChange != nil {
		a.onChange(b)
	}
	return nil
}

// Set replaces the stored credentials.
func (a *authSession) Set(b *credBlob) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.saveLocked(b)
}

// Clear deletes the stored credentials (tokens and pre-registered client secret).
func (a *authSession) Clear() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.blob, a.loaded = nil, true
	err1 := a.creds.Delete(credKey(a.id))
	err2 := a.creds.Delete(clientSecretKey(a.id))
	return errors.Join(err1, err2)
}

// Snapshot returns non-secret facts about the stored credentials.
func (a *authSession) Snapshot() (has, hasRefresh bool, expiry time.Time, kind string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := a.loadLocked()
	if err != nil || b == nil {
		return false, false, time.Time{}, ""
	}
	return b.AccessToken != "", b.RefreshToken != "", b.Expiry, b.Kind
}

// AccessToken returns a usable access token, refreshing it first if it is about to expire.
func (a *authSession) AccessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := a.loadLocked()
	if err != nil {
		return "", err
	}
	if b == nil || b.AccessToken == "" {
		return "", ErrNoCredentials
	}
	if b.Kind == "bearer" || b.Expiry.IsZero() || time.Until(b.Expiry) > refreshSkew {
		return b.AccessToken, nil
	}
	if b.RefreshToken == "" {
		return "", &AuthRequiredError{Reason: "access token expired and no refresh token is stored"}
	}
	if err := a.refreshLocked(ctx, b); err != nil {
		return "", err
	}
	return a.blob.AccessToken, nil
}

// Refresh forces a refresh after the server rejected stale. If another caller already
// replaced the token it returns nil without a second round trip.
func (a *authSession) Refresh(ctx context.Context, stale string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, err := a.loadLocked()
	if err != nil {
		return err
	}
	if b == nil || b.AccessToken == "" {
		return &AuthRequiredError{Reason: "no stored credentials"}
	}
	if b.Kind == "bearer" {
		return &AuthRequiredError{Reason: "the static bearer token was rejected by the server"}
	}
	if stale != "" && b.AccessToken != stale {
		return nil
	}
	if b.RefreshToken == "" {
		return &AuthRequiredError{Reason: "access token rejected and no refresh token is stored"}
	}
	return a.refreshLocked(ctx, b)
}

func (a *authSession) refreshLocked(ctx context.Context, b *credBlob) error {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.RefreshToken},
	}
	if b.Resource != "" {
		form.Set("resource", b.Resource)
	}
	source, err := a.sourceURL(ctx)
	if err != nil {
		return err
	}
	tr, err := oauthPostToken(ctx, a.hc, source, b.TokenEndpoint, b.ClientID, b.ClientSecret, b.AuthStyle, form)
	if err != nil {
		var te *tokenError
		if errors.As(err, &te) && (te.Code == "invalid_grant" || te.Code == "invalid_client" || te.Code == "unauthorized_client") {
			// The refresh token is dead: drop the tokens (keep client registration) so the UI asks to re-authorize.
			nb := *b
			nb.AccessToken, nb.RefreshToken, nb.Expiry = "", "", time.Time{}
			_ = a.saveLocked(&nb)
			return &AuthRequiredError{Reason: "refresh token rejected (" + te.Code + ")"}
		}
		return err
	}
	nb := *b
	applyToken(&nb, tr)
	return a.saveLocked(&nb)
}

func applyToken(b *credBlob, tr *tokenResponse) {
	b.AccessToken = tr.AccessToken
	if tr.RefreshToken != "" {
		b.RefreshToken = tr.RefreshToken
	}
	if tr.ExpiresIn > 0 {
		b.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	} else {
		b.Expiry = time.Time{}
	}
	if tr.Scope != "" {
		b.Scope = tr.Scope
	}
}

// tokenResponse is an RFC 6749 §5.1 response.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
}

// tokenError retains only a recognized OAuth identifier, never provider prose.
type tokenError struct {
	Status int
	Code   string
}

func oauthErrorCode(code string) string {
	switch code {
	case "invalid_request", "invalid_client", "invalid_grant", "unauthorized_client",
		"unsupported_grant_type", "invalid_scope", "access_denied",
		"unsupported_response_type", "server_error", "temporarily_unavailable",
		"invalid_target", "authorization_pending", "slow_down", "expired_token":
		return code
	default:
		return "unknown_error"
	}
}

func (e *tokenError) Error() string {
	return fmt.Sprintf("token endpoint: HTTP %d %s", e.Status, oauthErrorCode(e.Code))
}

// postToken performs a token-endpoint request with the client's authentication method.
func postToken(ctx context.Context, hc *http.Client, endpoint, clientID, clientSecret, style string, form url.Values) (*tokenResponse, error) {
	if err := checkEndpointURL(endpoint); err != nil {
		return nil, err
	}
	form = cloneValues(form)
	if clientID != "" && (clientSecret == "" || style != "header") {
		form.Set("client_id", clientID)
	}
	if clientSecret != "" && style != "header" {
		form.Set("client_secret", clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if clientSecret != "" && style == "header" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(url.QueryEscape(clientID)+":"+url.QueryEscape(clientSecret))))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			// Descriptions are deliberately not retained: providers can echo secrets.
		}
		_ = json.Unmarshal(body, &e)
		return nil, &tokenError{Status: resp.StatusCode, Code: oauthErrorCode(e.Error)}
	}
	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, errors.New("token endpoint returned invalid JSON")
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token endpoint response has no access_token")
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "bearer") {
		return nil, errors.New("unsupported token_type (only Bearer is implemented)")
	}
	return &tr, nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// sdkHandler adapts an authSession to the MCP SDK's transport hook. It never starts an
// interactive flow: a rejected token is refreshed once, anything else surfaces as
// AuthRequiredError so the connection manager can show "authorization required".
type sdkHandler struct {
	a *authSession
	// tokenHeader is set for a bearer server whose static token travels in this header
	// (see tokenHeaderTransport) rather than as Authorization: Bearer.
	tokenHeader string
}

var _ auth.OAuthHandler = sdkHandler{}

type tokenSourceFunc func() (*oauth2.Token, error)

func (f tokenSourceFunc) Token() (*oauth2.Token, error) { return f() }

func (h sdkHandler) TokenSource(ctx context.Context) (oauth2.TokenSource, error) {
	if has, _, _, _ := h.a.Snapshot(); !has || h.tokenHeader != "" {
		return nil, nil
	}
	return tokenSourceFunc(func() (*oauth2.Token, error) {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		tok, err := h.a.AccessToken(c)
		if err != nil {
			return nil, err
		}
		return &oauth2.Token{AccessToken: tok, TokenType: "Bearer"}, nil
	}), nil
}

func (h sdkHandler) Authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	scope := ""
	if v := resp.Header.Get("WWW-Authenticate"); v != "" {
		if i := strings.Index(v, `scope="`); i >= 0 {
			rest := v[i+len(`scope="`):]
			if j := strings.IndexByte(rest, '"'); j >= 0 {
				scope = untrusted.Line(h.a.scrub(rest[:j]), 256)
			}
		}
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		if h.tokenHeader != "" {
			return &AuthRequiredError{Reason: "the server rejected the key sent in the " + h.tokenHeader + " header"}
		}
		stale := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if stale == "" {
			return &AuthRequiredError{Reason: "server requires authorization", Scope: scope}
		}
		return h.a.Refresh(ctx, stale)
	case http.StatusForbidden:
		if strings.Contains(resp.Header.Get("WWW-Authenticate"), "insufficient_scope") {
			return &AuthRequiredError{Reason: "insufficient scope; re-authorize to grant more", Scope: scope}
		}
		return nil // retried once by the transport, then surfaced as an ordinary HTTP error
	}
	return nil
}

// tokenHeaderTransport adds a bearer server's static token in its configured header. Only
// requests to the server's own origin get it: a redirect to another host never carries it.
type tokenHeaderTransport struct {
	base         http.RoundTripper
	scheme, host string
	header       string
	a            *authSession
}

func (t *tokenHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != t.scheme || !strings.EqualFold(req.URL.Host, t.host) {
		return t.base.RoundTrip(req)
	}
	tok, err := t.a.AccessToken(req.Context())
	if errors.Is(err, ErrNoCredentials) {
		return t.base.RoundTrip(req) // the server's 401 then reports authorization required
	}
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	r := req.Clone(req.Context())
	r.Header.Set(t.header, tok)
	return t.base.RoundTrip(r)
}

func (t *tokenHeaderTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
