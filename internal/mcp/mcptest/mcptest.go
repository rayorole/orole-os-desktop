// Package mcptest is a fake MCP server plus OAuth 2.1 authorization server used by
// tests across Orole OS. It speaks real Streamable HTTP MCP (via the official SDK),
// RFC 9728 protected-resource metadata, RFC 8414/OIDC authorization-server metadata,
// PKCE S256, RFC 8707 resource indicators, refresh-token rotation, and optionally
// dynamic client registration and client-ID metadata documents.
//
// It is a test double: it proves our client against the protocol, NOT against the
// real Orole Mail/Drive servers (whose tool names are unverified until authorization).
package mcptest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configure the fake.
type Options struct {
	// DCR enables the registration endpoint (RFC 7591). When false it answers 403 like the real Orole servers.
	DCR bool
	// CIMD advertises client_id_metadata_document_supported and accepts https URL client IDs.
	CIMD bool
	// ClientID is a pre-registered public client that is always accepted.
	ClientID string
	// ClientSecret, when set, makes ClientID a confidential client (basic or post).
	ClientSecret string
	// NoIssParam omits RFC 9207 iss support.
	NoIssParam bool
	// Scopes the server asks for in its challenge. Default: mcp:read mcp:write.
	Scopes []string
	// AccessTTL is the lifetime of issued access tokens (default 1h).
	AccessTTL time.Duration
	// Setup registers tools/resources on the MCP server.
	Setup func(*sdk.Server)
	// OpenMCP disables authentication on the MCP endpoint.
	OpenMCP bool
	// ModernOnly serves only the stateless 2026-07-28 protocol, like Orole Drive and Mail,
	// which reject the legacy initialize handshake with UnsupportedProtocolVersion.
	ModernOnly bool
	// KeyHeader makes the MCP endpoint take its token from this header instead of
	// Authorization: Bearer, like Similarweb's api-key header.
	KeyHeader string
}

// AuthzRequest records one authorization request.
type AuthzRequest struct {
	ClientID, RedirectURI, State, Challenge, Method, Resource, Scope string
}

// Server is the running fake.
type Server struct {
	*httptest.Server
	MCPURL string // protected MCP endpoint
	Issuer string
	MCP    *sdk.Server
	opts   Options

	mu             sync.Mutex
	clients        map[string]bool
	codes          map[string]codeInfo
	access         map[string]tokenInfo
	refresh        map[string]tokenInfo
	Registrations  []map[string]any
	Authorizations []AuthzRequest
	TokenRequests  []url.Values
	MCPRequests    int
	failN          int
	Unauthorized   int // 401 responses issued by the MCP endpoint
	// LastTokenAuthHeader is the Authorization header of the last token request.
	LastTokenAuthHeader string
}

type codeInfo struct {
	clientID, redirect, challenge, resource, scope string
}
type tokenInfo struct {
	clientID, resource, scope string
	expires                   time.Time
}

// New starts a fake and registers cleanup with t.
func New(t interface {
	Cleanup(func())
	Helper()
}, o Options) *Server {
	t.Helper()
	if len(o.Scopes) == 0 {
		o.Scopes = []string{"mcp:read", "mcp:write"}
	}
	if o.AccessTTL == 0 {
		o.AccessTTL = time.Hour
	}
	s := &Server{opts: o, clients: map[string]bool{}, codes: map[string]codeInfo{}, access: map[string]tokenInfo{}, refresh: map[string]tokenInfo{}}
	if o.ClientID != "" {
		s.clients[o.ClientID] = true
	}
	var serverOpts *sdk.ServerOptions
	if o.ModernOnly {
		serverOpts = &sdk.ServerOptions{SupportedProtocolVersions: []string{modernProtocolVersion}}
	}
	s.MCP = sdk.NewServer(&sdk.Implementation{Name: "fake-orole", Version: "1.2.3"}, serverOpts)
	if o.Setup != nil {
		o.Setup(s.MCP)
	} else {
		DefaultTools(s.MCP)
	}
	mux := http.NewServeMux()
	s.Server = httptest.NewServer(mux)
	t.Cleanup(func() {
		s.Server.CloseClientConnections() // long-lived MCP SSE streams otherwise block httptest.Close
		s.Server.Close()
	})
	s.MCPURL = s.Server.URL + "/api/mcp"
	s.Issuer = s.Server.URL + "/api/auth"

	var handlerOpts *sdk.StreamableHTTPOptions
	if o.ModernOnly {
		handlerOpts = &sdk.StreamableHTTPOptions{Stateless: true}
	}
	var mcpHandler http.Handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return s.MCP }, handlerOpts)
	if o.ModernOnly {
		mcpHandler = rejectLegacyInitialize(mcpHandler)
	}
	mux.Handle("/api/mcp", s.bearer(mcpHandler))
	mux.HandleFunc("/.well-known/oauth-protected-resource/api/mcp", s.prm)
	// Exercise an HTML app-shell fallback; metadata lives under the issuer.
	mux.HandleFunc("/.well-known/oauth-authorization-server/api/auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!doctype html><html><body>app shell</body></html>")
	})
	mux.HandleFunc("/.well-known/openid-configuration/api/auth", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!doctype html><html><body>app shell</body></html>")
	})
	mux.HandleFunc("/api/auth/.well-known/openid-configuration", s.asMetadata)
	mux.HandleFunc("/api/auth/.well-known/oauth-authorization-server", s.asMetadata)
	mux.HandleFunc("/api/auth/oauth2/authorize", s.authorize)
	mux.HandleFunc("/api/auth/oauth2/token", s.token)
	mux.HandleFunc("/api/auth/oauth2/register", s.register)
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func rnd(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) prm(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{
		"resource": s.MCPURL, "authorization_servers": []string{s.Issuer},
		"bearer_methods_supported": []string{"header"}, "scopes_supported": s.opts.Scopes,
	})
}

func (s *Server) asMetadata(w http.ResponseWriter, r *http.Request) {
	m := map[string]any{
		"issuer": s.Issuer, "authorization_endpoint": s.Issuer + "/oauth2/authorize", "token_endpoint": s.Issuer + "/oauth2/token",
		"response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"},
		"scopes_supported":                      append([]string{"openid", "offline_access"}, s.opts.Scopes...),
		"client_id_metadata_document_supported": s.opts.CIMD,
	}
	if !s.opts.NoIssParam {
		m["authorization_response_iss_parameter_supported"] = true
	}
	if s.opts.DCR {
		m["registration_endpoint"] = s.Issuer + "/oauth2/register"
	}
	writeJSON(w, 200, m)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.opts.DCR || r.Method != http.MethodPost {
		writeJSON(w, 403, map[string]string{"error": "access_denied", "error_description": "Client registration is disabled"})
		return
	}
	var req map[string]any
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_client_metadata"})
		return
	}
	id := "dcr-" + rnd(8)
	s.mu.Lock()
	s.clients[id] = true
	s.Registrations = append(s.Registrations, req)
	s.mu.Unlock()
	req["client_id"] = id
	writeJSON(w, 201, req)
}

func (s *Server) knownClient(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients[id] {
		return true
	}
	if s.opts.CIMD {
		if u, err := url.Parse(id); err == nil && u.Scheme == "https" && u.Path != "" {
			return true
		}
	}
	return false
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	a := AuthzRequest{q.Get("client_id"), q.Get("redirect_uri"), q.Get("state"), q.Get("code_challenge"), q.Get("code_challenge_method"), q.Get("resource"), q.Get("scope")}
	s.mu.Lock()
	s.Authorizations = append(s.Authorizations, a)
	s.mu.Unlock()
	if q.Get("response_type") != "code" || !s.knownClient(a.ClientID) || a.RedirectURI == "" || a.Challenge == "" || a.Method != "S256" {
		http.Error(w, "invalid_request", http.StatusBadRequest)
		return
	}
	ru, err := url.Parse(a.RedirectURI)
	if err != nil || ru.Hostname() != "127.0.0.1" {
		http.Error(w, "redirect_uri must be loopback", http.StatusBadRequest)
		return
	}
	code := rnd(16)
	s.mu.Lock()
	s.codes[code] = codeInfo{a.ClientID, a.RedirectURI, a.Challenge, a.Resource, a.Scope}
	s.mu.Unlock()
	rq := ru.Query()
	rq.Set("code", code)
	rq.Set("state", a.State)
	if !s.opts.NoIssParam {
		rq.Set("iss", s.Issuer)
	}
	ru.RawQuery = rq.Encode()
	http.Redirect(w, r, ru.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	s.mu.Lock()
	s.TokenRequests = append(s.TokenRequests, r.PostForm)
	s.LastTokenAuthHeader = r.Header.Get("Authorization")
	s.mu.Unlock()

	clientID := r.PostForm.Get("client_id")
	if u, p, ok := r.BasicAuth(); ok {
		clientID, _ = url.QueryUnescape(u)
		if s.opts.ClientSecret != "" && p != url.QueryEscape(s.opts.ClientSecret) {
			writeJSON(w, 401, map[string]string{"error": "invalid_client"})
			return
		}
	} else if s.opts.ClientSecret != "" && clientID == s.opts.ClientID && r.PostForm.Get("client_secret") != s.opts.ClientSecret {
		writeJSON(w, 401, map[string]string{"error": "invalid_client"})
		return
	}
	bad := func(code, desc string) {
		writeJSON(w, 400, map[string]string{"error": code, "error_description": desc})
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.mu.Lock()
		ci, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code")) // single use
		s.mu.Unlock()
		if !ok {
			bad("invalid_grant", "unknown or reused code")
			return
		}
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		switch {
		case base64.RawURLEncoding.EncodeToString(sum[:]) != ci.challenge:
			bad("invalid_grant", "PKCE verification failed")
		case r.PostForm.Get("redirect_uri") != ci.redirect:
			bad("invalid_grant", "redirect_uri mismatch")
		case clientID != ci.clientID:
			bad("invalid_grant", "client mismatch")
		case r.PostForm.Get("resource") != s.MCPURL || ci.resource != s.MCPURL:
			bad("invalid_target", "resource indicator missing or wrong")
		default:
			s.issue(w, ci.clientID, ci.resource, ci.scope)
		}
	case "refresh_token":
		s.mu.Lock()
		ti, ok := s.refresh[r.PostForm.Get("refresh_token")]
		delete(s.refresh, r.PostForm.Get("refresh_token")) // rotation
		s.mu.Unlock()
		switch {
		case !ok:
			bad("invalid_grant", "unknown refresh token")
		case clientID != ti.clientID:
			bad("invalid_grant", "client mismatch")
		case r.PostForm.Get("resource") != s.MCPURL:
			bad("invalid_target", "resource indicator missing or wrong")
		default:
			s.issue(w, ti.clientID, ti.resource, ti.scope)
		}
	default:
		bad("unsupported_grant_type", "")
	}
}

func (s *Server) issue(w http.ResponseWriter, clientID, resource, scope string) {
	at, rt := "at-"+rnd(16), "rt-"+rnd(16)
	s.mu.Lock()
	s.access[at] = tokenInfo{clientID, resource, scope, time.Now().Add(s.opts.AccessTTL)}
	s.refresh[rt] = tokenInfo{clientID, resource, scope, time.Time{}}
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"access_token": at, "token_type": "Bearer", "refresh_token": rt, "expires_in": int(s.opts.AccessTTL.Seconds()), "scope": scope})
}

// bearer guards the MCP endpoint like the real servers do.
func (s *Server) bearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.MCPRequests++
		s.mu.Unlock()
		s.mu.Lock()
		fail := s.failN > 0
		if fail {
			s.failN--
		}
		s.mu.Unlock()
		if fail {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		if s.opts.OpenMCP {
			next.ServeHTTP(w, r)
			return
		}
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.opts.KeyHeader != "" {
			tok = r.Header.Get(s.opts.KeyHeader)
		}
		s.mu.Lock()
		ti, ok := s.access[tok]
		valid := ok && tok != "" && time.Now().Before(ti.expires) && ti.resource == s.MCPURL
		if !valid {
			s.Unauthorized++
		}
		s.mu.Unlock()
		if !valid {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/api/mcp", scope="%s"`, s.Server.URL, strings.Join(s.opts.Scopes, " ")))
			writeJSON(w, 401, map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32000, "message": "missing or invalid authorization"}, "id": nil})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// InvalidateAccessTokens makes every issued access token be rejected (server-side
// expiry/revocation) while refresh tokens stay valid.
func (s *Server) InvalidateAccessTokens() {
	s.mu.Lock()
	s.access = map[string]tokenInfo{}
	s.mu.Unlock()
}

// RevokeAll invalidates access AND refresh tokens.
func (s *Server) RevokeAll() {
	s.mu.Lock()
	s.access = map[string]tokenInfo{}
	s.refresh = map[string]tokenInfo{}
	s.mu.Unlock()
}

// Counts returns request counters under the lock.
func (s *Server) Counts() (tokenReqs, authz, unauthorized int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.TokenRequests), len(s.Authorizations), s.Unauthorized
}

// Browser plays the user: it opens authURL (the authorization endpoint), which
// redirects to the client's loopback callback, and returns the callback response status.
func (s *Server) Browser(ctx context.Context, authURL string) (int, error) {
	hc := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// FailMCP makes the next n MCP requests answer 503 (simulates an outage).
func (s *Server) FailMCP(n int) {
	s.mu.Lock()
	s.failN = n
	s.mu.Unlock()
}

// AddStaticToken accepts tok as a long-lived bearer token (e.g. a personal access token).
func (s *Server) AddStaticToken(tok string) {
	s.mu.Lock()
	s.access[tok] = tokenInfo{resource: s.MCPURL, expires: time.Now().Add(100 * 365 * 24 * time.Hour)}
	s.mu.Unlock()
}

const modernProtocolVersion = "2026-07-28"

// rejectLegacyInitialize answers a legacy initialize the way the TypeScript SDK's
// createMcpHandler({legacy: "reject"}) does: HTTP 400 with UnsupportedProtocolVersion
// (-32022) naming the supported versions. Everything else reaches next.
func rejectLegacyInitialize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &msg) == nil && msg.Method == "initialize" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{
				"code": sdk.CodeUnsupportedProtocolVersion, "message": "Unsupported protocol version: " + msg.Params.ProtocolVersion,
				"data": map[string]any{"supported": []string{modernProtocolVersion}, "requested": msg.Params.ProtocolVersion},
			}})
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}
