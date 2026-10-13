package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/credentials"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

// AuthStart is returned when an authorization flow has begun. The daemon never
// opens a browser itself: it publishes "mcp.auth.url" and the desktop shell opens URL.
type AuthStart struct {
	ServerID    string `json:"serverId"`
	ServerName  string `json:"serverName"`
	URL         string `json:"url"`
	RedirectURI string `json:"redirectUri"`
	Kind        string `json:"registrationKind"`
	ExpiresAt   int64  `json:"expiresAt"` // unix ms
}

// AuthDone is published as "mcp.auth.done" when a flow finishes.
type AuthDone struct {
	ServerID string `json:"serverId"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

// AuthTimeout bounds how long the loopback listener waits for the browser.
const AuthTimeout = 5 * time.Minute

type clientReg struct {
	kind     string // preregistered | cimd | dcr
	clientID string
	secret   string
	style    string // params | header
}

type authFlow struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// StartAuth runs OAuth discovery, registers/identifies the client, opens a loopback
// listener and returns the authorization URL. Completion is asynchronous: the flow
// stores tokens in the credential store, publishes mcp.auth.done and reconnects.
func (m *Manager) StartAuth(ctx context.Context, id string) (AuthStart, error) {
	cfg, err := m.server(ctx, id)
	if err != nil {
		return AuthStart{}, err
	}
	if cfg.AuthKind != AuthOAuth {
		return AuthStart{}, fmt.Errorf("%w: server uses auth kind %q, not oauth", ErrBadRequest, cfg.AuthKind)
	}
	disc, derr := Discover(ctx, m.hc, cfg.URL)
	m.setDiscovery(id, disc)
	if derr != nil {
		return AuthStart{}, fmt.Errorf("authorization discovery failed: %w", derr)
	}
	if !disc.RequiresAuth {
		return AuthStart{}, fmt.Errorf("%w: the server did not ask for authorization", ErrBadRequest)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0") // loopback only, one-shot, closed when the flow ends
	if err != nil {
		return AuthStart{}, fmt.Errorf("loopback listener: %w", err)
	}
	redirect := "http://" + ln.Addr().String() + "/callback"

	reg, err := m.resolveClient(ctx, cfg, disc, redirect)
	if err != nil {
		ln.Close()
		return AuthStart{}, err
	}

	scopes := strings.Fields(cfg.Scopes)
	if len(scopes) == 0 {
		scopes = slices.Clone(disc.RequestedScopes)
	}
	if slices.Contains(disc.AS.ScopesSupported, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access") // ask for a refresh token when the server offers one
	}
	state, err1 := randomToken(32)
	verifier, err2 := randomToken(32)
	if err := errors.Join(err1, err2); err != nil {
		ln.Close()
		return AuthStart{}, err
	}
	au, err := url.Parse(disc.AS.AuthorizationEndpoint)
	if err != nil {
		ln.Close()
		return AuthStart{}, err
	}
	q := au.Query()
	q.Set("response_type", "code")
	q.Set("client_id", reg.clientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", pkceChallenge(verifier))
	q.Set("code_challenge_method", "S256")
	q.Set("resource", disc.Resource) // RFC 8707: bind the token to this MCP server
	if len(scopes) > 0 {
		q.Set("scope", strings.Join(scopes, " "))
	}
	au.RawQuery = q.Encode()

	fctx, cancel := context.WithTimeout(context.Background(), AuthTimeout)
	flow := &authFlow{cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if old := m.flows[id]; old != nil {
		old.cancel()
	}
	m.flows[id] = flow
	m.mu.Unlock()

	start := AuthStart{ServerID: id, ServerName: cfg.Name, URL: au.String(), RedirectURI: redirect, Kind: reg.kind,
		ExpiresAt: time.Now().Add(AuthTimeout).UnixMilli()}
	go m.runFlow(fctx, flow, ln, cfg, disc, reg, redirect, state, verifier, scopes)
	m.publish("mcp.auth.url", start)
	m.audit("mcp.auth.start", "mcp/"+id, "allowed", "registration="+reg.kind)
	return start, nil
}

// CancelAuth aborts a pending flow.
func (m *Manager) CancelAuth(id string) {
	m.mu.Lock()
	f := m.flows[id]
	m.mu.Unlock()
	if f != nil {
		f.cancel()
		<-f.done
	}
}

func (m *Manager) runFlow(ctx context.Context, flow *authFlow, ln net.Listener, cfg ServerConfig, disc *Discovery, reg clientReg, redirect, state, verifier string, scopes []string) {
	defer close(flow.done)
	defer flow.cancel()
	defer func() {
		m.mu.Lock()
		if m.flows[cfg.ID] == flow {
			delete(m.flows, cfg.ID)
		}
		m.mu.Unlock()
	}()
	as := m.authSession(cfg.ID)
	as.remember(reg.secret, verifier)
	fail := func(err error) {
		err = errors.New(as.scrub(err.Error()))
		m.log.Warn("mcp authorization failed", "server", cfg.ID, "err", err)
		m.audit("mcp.auth.finish", "mcp/"+cfg.ID, "error", untrusted.Line(err.Error(), 300))
		m.setError(cfg.ID, "authorization failed: "+err.Error())
		m.publish("mcp.auth.done", AuthDone{ServerID: cfg.ID, OK: false, Error: untrusted.Line(err.Error(), 300)})
	}

	code, err := awaitCallback(ctx, ln, state, disc.AS.Issuer, disc.AS.IssParameterSupported)
	if err != nil {
		fail(err)
		return
	}
	as.remember(code)
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"code_verifier": {verifier},
		"resource":      {disc.Resource},
	}
	ectx, ecancel := context.WithTimeout(ctx, 30*time.Second)
	defer ecancel()
	tr, err := oauthPostToken(ectx, m.hc, cfg.URL, disc.AS.TokenEndpoint, reg.clientID, reg.secret, reg.style, form)
	if err != nil {
		fail(err)
		return
	}
	b := &credBlob{Kind: "oauth", ClientID: reg.clientID, ClientSecret: reg.secret, AuthStyle: reg.style,
		TokenEndpoint: disc.AS.TokenEndpoint, Resource: disc.Resource, Scope: strings.Join(scopes, " ")}
	applyToken(b, tr)
	if err := as.Set(b); err != nil {
		fail(err)
		return
	}
	if err := m.saveAuthState(cfg.ID, disc, reg, b); err != nil {
		m.log.Warn("mcp auth state not saved", "err", err)
	}
	m.audit("mcp.auth.finish", "mcp/"+cfg.ID, "allowed", "registration="+reg.kind+" scopes="+untrusted.Line(as.scrub(b.Scope), 200))
	m.publish("mcp.auth.done", AuthDone{ServerID: cfg.ID, OK: true})
	m.Connect(cfg.ID)
}

// awaitCallback serves GET /callback on ln until a valid authorization response
// arrives (or ctx ends) and returns the code. State and RFC 9207 iss are verified.
func awaitCallback(ctx context.Context, ln net.Listener, state, issuer string, issRequired bool) (string, error) {
	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	port := ln.Addr().(*net.TCPAddr).Port
	wantHost := fmt.Sprintf("127.0.0.1:%d", port)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Host != wantHost { // Host check defeats DNS rebinding
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		var res result
		switch {
		case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1:
			http.Error(w, "state mismatch", http.StatusBadRequest) // not our flow: keep waiting
			return
		case q.Get("error") != "":
			res.err = fmt.Errorf("authorization server returned error %s", oauthErrorCode(q.Get("error")))
		case q.Get("code") == "":
			res.err = errors.New("authorization response has no code")
		case issRequired && q.Get("iss") == "":
			res.err = errors.New("authorization server advertises RFC 9207 iss support but sent no iss parameter")
		case q.Get("iss") != "" && !sameURL(q.Get("iss"), issuer):
			res.err = fmt.Errorf("authorization response issuer %q does not match %q (possible mix-up attack)", untrusted.Line(q.Get("iss"), 200), issuer)
		default:
			res.code = q.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		msg := "Authorization response received. You can close this tab and return to Orole OS for the result."
		if res.err != nil {
			msg = "Authorization failed. Return to Orole OS for details."
		}
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Orole OS</title><body style=\"font-family:system-ui;margin:3rem\"><p>%s</p>", html.EscapeString(msg))
		select {
		case ch <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer func() {
		sctx, c := context.WithTimeout(context.Background(), 2*time.Second)
		defer c()
		srv.Shutdown(sctx)
	}()
	select {
	case r := <-ch:
		return r.code, r.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("timed out waiting for the browser authorization")
		}
		return "", errors.New("authorization canceled")
	}
}

// OroleClientMetadataURL is Orole OS's published OAuth client ID metadata document
// (served from orole-directory's public/oauth/orole-os.json). Its redirect_uris allow
// http://127.0.0.1/callback on any port, so it fits the loopback callback used here.
const OroleClientMetadataURL = "https://orole.be/oauth/orole-os.json"

// resolveClient picks how Orole OS identifies itself to the authorization server,
// in the MCP authorization spec's order of preference:
//
//  1. a configured client ID: an https client-ID metadata document URL when the server
//     supports those (CIMD), otherwise a pre-registered client (optional secret in the credential store);
//  2. Orole OS's own client metadata document when the server supports CIMD;
//  3. dynamic client registration (RFC 7591) when a registration endpoint is advertised;
//  4. otherwise ErrNoRegistration, with what the user can do about it.
func (m *Manager) resolveClient(ctx context.Context, cfg ServerConfig, disc *Discovery, redirect string) (clientReg, error) {
	as := disc.AS
	if cfg.OAuthClientID != "" {
		if u, err := url.Parse(cfg.OAuthClientID); err == nil && u.Scheme == "https" && u.Path != "" && u.Path != "/" && as.ClientIDMetadataDocumentOK {
			return clientReg{kind: "cimd", clientID: cfg.OAuthClientID, style: "params"}, nil
		}
		secret := ""
		if raw, err := m.creds.Get(clientSecretKey(cfg.ID)); err == nil {
			secret = string(raw)
			m.authSession(cfg.ID).remember(secret)
		} else if !errors.Is(err, credentials.ErrNotFound) {
			return clientReg{}, fmt.Errorf("credential store: %w", err)
		}
		style := "params"
		if secret != "" && !slices.Contains(as.TokenEndpointAuthMethods, "client_secret_post") && slices.Contains(as.TokenEndpointAuthMethods, "client_secret_basic") {
			style = "header"
		}
		return clientReg{kind: "preregistered", clientID: cfg.OAuthClientID, secret: secret, style: style}, nil
	}
	if as.ClientIDMetadataDocumentOK {
		return clientReg{kind: "cimd", clientID: OroleClientMetadataURL, style: "params"}, nil
	}
	if as.RegistrationEndpoint != "" {
		return m.registerClient(ctx, as, redirect, cfg)
	}
	return clientReg{}, fmt.Errorf("%w (the server supports neither dynamic registration nor client ID metadata documents: register a client on the server and set its client ID here)", ErrNoRegistration)
}

// registerClient performs RFC 7591 dynamic client registration as a public native client.
func (m *Manager) registerClient(ctx context.Context, as *ASMetadata, redirect string, cfg ServerConfig) (clientReg, error) {
	network, err := newOAuthNetwork(cfg.URL)
	if err != nil {
		return clientReg{}, err
	}
	hc, err := network.client(m.hc)
	if err != nil {
		return clientReg{}, err
	}
	defer hc.CloseIdleConnections()
	body, _ := json.Marshal(map[string]any{
		"client_name":                "Orole OS",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, as.RegistrationEndpoint, bytes.NewReader(body))
	if err != nil {
		return clientReg{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return clientReg{}, fmt.Errorf("client registration: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &e)
		return clientReg{}, fmt.Errorf("client registration refused: HTTP %d %s %s", resp.StatusCode, untrusted.Line(e.Error, 64), untrusted.Line(e.Desc, 200))
	}
	var rr struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		AuthMethod   string `json:"token_endpoint_auth_method"`
	}
	if err := json.Unmarshal(raw, &rr); err != nil || rr.ClientID == "" {
		return clientReg{}, errors.New("client registration returned no client_id")
	}
	style := "params"
	if rr.AuthMethod == "client_secret_basic" {
		style = "header"
	}
	return clientReg{kind: "dcr", clientID: rr.ClientID, secret: rr.ClientSecret, style: style}, nil
}
