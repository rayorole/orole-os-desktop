package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

// ASMetadata is the subset of RFC 8414 / OIDC discovery metadata we act on.
type ASMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorizationEndpoint"`
	TokenEndpoint                 string   `json:"tokenEndpoint"`
	RegistrationEndpoint          string   `json:"registrationEndpoint,omitempty"`
	RevocationEndpoint            string   `json:"revocationEndpoint,omitempty"`
	ScopesSupported               []string `json:"scopesSupported,omitempty"`
	CodeChallengeMethods          []string `json:"codeChallengeMethods,omitempty"`
	TokenEndpointAuthMethods      []string `json:"tokenEndpointAuthMethods,omitempty"`
	GrantTypes                    []string `json:"grantTypes,omitempty"`
	IssParameterSupported         bool     `json:"issParameterSupported"`
	ClientIDMetadataDocumentOK    bool     `json:"clientIdMetadataDocumentSupported"`
	DPoPSigningAlgValuesSupported []string `json:"dpopSigningAlgValuesSupported,omitempty"`
}

// DiscoveryAttempt records one metadata URL we tried.
type DiscoveryAttempt struct {
	Kind   string `json:"kind"` // challenge | prm | as
	URL    string `json:"url"`
	Result string `json:"result"` // ok | <reason>
}

// Discovery is the outcome of OAuth discovery for an MCP endpoint (no login involved).
type Discovery struct {
	ResourceURL    string `json:"resourceUrl"`
	RequiresAuth   bool   `json:"requiresAuth"`
	ProbeStatus    int    `json:"probeStatus"`
	ChallengeScope string `json:"challengeScope,omitempty"`
	ChallengeError string `json:"challengeError,omitempty"`
	PRMURL         string `json:"protectedResourceMetadataUrl,omitempty"`
	Resource       string `json:"resource,omitempty"`
	// BearerMethods/PRMScopes come from the protected-resource metadata.
	BearerMethods   []string           `json:"bearerMethods,omitempty"`
	PRMScopes       []string           `json:"prmScopes,omitempty"`
	AuthServers     []string           `json:"authorizationServers,omitempty"`
	ASMetadataURL   string             `json:"authorizationServerMetadataUrl,omitempty"`
	AS              *ASMetadata        `json:"authorizationServer,omitempty"`
	Registration    []string           `json:"registrationMethods,omitempty"` // dcr, cimd
	RequestedScopes []string           `json:"requestedScopes,omitempty"`
	Attempts        []DiscoveryAttempt `json:"attempts,omitempty"`
}

// asWire is the RFC 8414 wire form.
type asWire struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	RevocationEndpoint            string   `json:"revocation_endpoint"`
	ScopesSupported               []string `json:"scopes_supported"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	GrantTypesSupported           []string `json:"grant_types_supported"`
	IssParam                      bool     `json:"authorization_response_iss_parameter_supported"`
	CIMD                          bool     `json:"client_id_metadata_document_supported"`
	DPoP                          []string `json:"dpop_signing_alg_values_supported"`
}

const maxMetadataBytes = 1 << 20

// isLoopbackHost reports whether host (with optional port) is loopback.
func isLoopbackHost(hostport string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// checkEndpointURL requires https, or http on loopback, and no embedded credentials.
func checkEndpointURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %q", ErrUnsafeURL, untrusted.Line(raw, 200))
	}
	if u.User != nil {
		return fmt.Errorf("%w: URL contains credentials", ErrUnsafeURL)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Host) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrUnsafeURL, untrusted.Line(raw, 200))
}

// getJSON GETs a metadata document. Non-200 and non-JSON responses (a SPA that
// answers 200 text/html for unknown paths is common) are reported, not fatal.
func getJSON(ctx context.Context, hc *http.Client, rawURL string, out any) error {
	if err := checkEndpointURL(rawURL); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mt != "application/json" && !strings.HasSuffix(mt, "+json") {
		return fmt.Errorf("not JSON (content-type %q)", untrusted.Line(mt, 60))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetadataBytes)).Decode(out); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	return nil
}

func sameURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// Discover probes mcpURL without credentials and walks protected-resource and
// authorization-server metadata (RFC 9728, RFC 8414, OIDC Discovery). It never logs
// in. If the endpoint answers the probe without demanding authorization,
// RequiresAuth is false and nothing else is fetched.
func Discover(ctx context.Context, hc *http.Client, mcpURL string) (*Discovery, error) {
	network, err := newOAuthNetwork(mcpURL)
	if err != nil {
		return &Discovery{ResourceURL: mcpURL}, err
	}
	return network.discover(ctx, hc, mcpURL)
}

func (network *oauthNetwork) discover(ctx context.Context, hc *http.Client, mcpURL string) (*Discovery, error) {
	d := &Discovery{ResourceURL: mcpURL}
	hc, err := network.client(hc)
	if err != nil {
		return d, err
	}
	defer hc.CloseIdleConnections()
	// 1. Unauthenticated probe: an initialize request that any conforming server answers 401 to.
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"orole-os-probe","version":"0"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, mcpURL, bytes.NewReader(body))
	if err != nil {
		return d, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return d, fmt.Errorf("probe %s: %w", mcpURL, err)
	}
	resp.Body.Close()
	d.ProbeStatus = resp.StatusCode

	var challengeURL string
	if resp.StatusCode != http.StatusUnauthorized {
		if resp.StatusCode/100 == 2 {
			d.RequiresAuth = false
			return d, nil
		}
		if resp.StatusCode != http.StatusForbidden {
			return d, fmt.Errorf("probe %s: unexpected HTTP %d", mcpURL, resp.StatusCode)
		}
	}
	d.RequiresAuth = true
	chs, _ := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	for _, c := range chs {
		if c.Scheme != "bearer" {
			continue
		}
		if challengeURL == "" {
			challengeURL = c.Params["resource_metadata"]
		}
		if d.ChallengeScope == "" {
			// Never cut this like other display strings: it is sent back verbatim as the
			// authorization request's scope, and a truncated last token is an unknown scope.
			d.ChallengeScope = strings.Join(scopeTokens(c.Params["scope"]), " ")
		}
		if d.ChallengeError == "" {
			d.ChallengeError = untrusted.Line(c.Params["error"], 64)
		}
	}

	// 2. Protected resource metadata: header URL, then path-inserted, then root (RFC 9728 §3, MCP spec).
	ru, err := url.Parse(mcpURL)
	if err != nil {
		return d, err
	}
	type cand struct{ url, resource string }
	var cands []cand
	if challengeURL != "" {
		cands = append(cands, cand{challengeURL, mcpURL})
		d.Attempts = append(d.Attempts, DiscoveryAttempt{Kind: "challenge", URL: untrusted.Line(challengeURL, 300), Result: "ok"})
	}
	pi := *ru
	pi.RawQuery = ""
	pi.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(ru.Path, "/")
	cands = append(cands, cand{pi.String(), mcpURL})
	root := *ru
	root.RawQuery = ""
	root.Path = "/.well-known/oauth-protected-resource"
	rootRes := *ru
	rootRes.Path, rootRes.RawQuery = "", ""
	cands = append(cands, cand{root.String(), rootRes.String()})

	var prm struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		ScopesSupported        []string `json:"scopes_supported"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
	}
	found := false
	for _, c := range cands {
		prm.Resource, prm.AuthorizationServers, prm.ScopesSupported, prm.BearerMethodsSupported = "", nil, nil, nil
		err := getJSON(ctx, hc, c.url, &prm)
		if err == nil && !sameURL(prm.Resource, c.resource) {
			err = fmt.Errorf("resource %q does not match %q (RFC 9728 §3.3)", untrusted.Line(prm.Resource, 200), c.resource)
		}
		if err == nil && len(prm.AuthorizationServers) == 0 {
			err = errors.New("no authorization_servers")
		}
		if err == nil {
			for _, as := range prm.AuthorizationServers {
				if e := network.checkEndpoint(ctx, as); e != nil {
					err = e
					break
				}
			}
		}
		res := "ok"
		if err != nil {
			res = untrusted.Line(err.Error(), 200)
		}
		d.Attempts = append(d.Attempts, DiscoveryAttempt{Kind: "prm", URL: untrusted.Line(c.url, 300), Result: res})
		if errors.Is(err, ErrUnsafeURL) {
			return d, err
		}
		if err == nil {
			d.PRMURL = c.url
			d.Resource = prm.Resource
			d.AuthServers = prm.AuthorizationServers
			d.PRMScopes = prm.ScopesSupported
			d.BearerMethods = prm.BearerMethodsSupported
			found = true
			break
		}
	}
	if !found {
		return d, errors.New("no usable protected resource metadata (see attempts)")
	}

	// 3. Authorization server metadata for the first listed server.
	issuer := d.AuthServers[0]
	var meta asWire
	for _, mu := range asMetadataURLs(issuer) {
		meta = asWire{}
		err := getJSON(ctx, hc, mu, &meta)
		if err == nil && !sameURL(meta.Issuer, issuer) {
			err = fmt.Errorf("issuer %q does not match %q", untrusted.Line(meta.Issuer, 200), issuer)
		}
		if err == nil && (meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "") {
			err = errors.New("missing authorization_endpoint or token_endpoint")
		}
		if err == nil {
			for _, e := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint, meta.RegistrationEndpoint, meta.RevocationEndpoint} {
				if e != "" {
					if ce := network.checkEndpoint(ctx, e); ce != nil {
						err = ce
						break
					}
				}
			}
		}
		res := "ok"
		if err != nil {
			res = untrusted.Line(err.Error(), 200)
		}
		d.Attempts = append(d.Attempts, DiscoveryAttempt{Kind: "as", URL: mu, Result: res})
		if errors.Is(err, ErrUnsafeURL) {
			return d, err
		}
		if err == nil {
			d.ASMetadataURL = mu
			d.AS = &ASMetadata{
				Issuer: meta.Issuer, AuthorizationEndpoint: meta.AuthorizationEndpoint, TokenEndpoint: meta.TokenEndpoint,
				RegistrationEndpoint: meta.RegistrationEndpoint, RevocationEndpoint: meta.RevocationEndpoint,
				ScopesSupported: meta.ScopesSupported, CodeChallengeMethods: meta.CodeChallengeMethodsSupported,
				TokenEndpointAuthMethods: meta.TokenEndpointAuthMethods, GrantTypes: meta.GrantTypesSupported,
				IssParameterSupported: meta.IssParam, ClientIDMetadataDocumentOK: meta.CIMD, DPoPSigningAlgValuesSupported: meta.DPoP,
			}
			break
		}
	}
	if d.AS == nil {
		return d, errors.New("no usable authorization server metadata (see attempts)")
	}
	if !slices.Contains(d.AS.CodeChallengeMethods, "S256") {
		return d, errors.New("authorization server does not advertise PKCE S256; refusing to continue")
	}
	if d.AS.RegistrationEndpoint != "" {
		d.Registration = append(d.Registration, "dcr")
	}
	if d.AS.ClientIDMetadataDocumentOK {
		d.Registration = append(d.Registration, "cimd")
	}
	// Scopes: what the server's challenge asks for, else PRM, never more than that.
	switch {
	case d.ChallengeScope != "":
		d.RequestedScopes = strings.Fields(d.ChallengeScope)
	case len(d.PRMScopes) > 0:
		d.RequestedScopes = slices.Clone(d.PRMScopes)
	}
	return d, nil
}

// scopeTokens splits a space-delimited scope value (RFC 6749 §3.3) and keeps only the
// tokens made of the allowed characters (%x21 / %x23-5B / %x5D-7E), so the untrusted
// value carries no control or invisible characters into the UI or the authorization URL.
func scopeTokens(raw string) []string {
	var out []string
	for _, tok := range strings.Fields(raw) {
		if !strings.ContainsFunc(tok, func(r rune) bool { return r < 0x21 || r > 0x7e || r == '"' || r == '\\' }) {
			out = append(out, tok)
		}
	}
	return out
}

// asMetadataURLs lists where an issuer's metadata may live. The MCP spec order is
// RFC 8414 path insertion, OIDC path insertion, OIDC path appending; we also try
// RFC 8414 path appending because real deployments (e.g. better-auth under /api/auth)
// publish there while answering 200 text/html on the inserted paths.
func asMetadataURLs(issuer string) []string {
	u, err := url.Parse(issuer)
	if err != nil {
		return nil
	}
	base := *u
	base.RawQuery, base.Fragment = "", ""
	p := strings.Trim(u.Path, "/")
	var out []string
	add := func(path string) {
		c := base
		c.Path = path
		out = append(out, c.String())
	}
	if p == "" {
		add("/.well-known/oauth-authorization-server")
		add("/.well-known/openid-configuration")
		return out
	}
	add("/.well-known/oauth-authorization-server/" + p)
	add("/.well-known/openid-configuration/" + p)
	add("/" + p + "/.well-known/openid-configuration")
	add("/" + p + "/.well-known/oauth-authorization-server")
	return out
}

// OAuth POST bodies contain credentials and must not be replayed to redirect
// targets. Discovery GET redirects remain bounded and may not downgrade TLS or
// redirect a public server into the loopback callback surface.
func oauthRedirect(req *http.Request, via []*http.Request) error {
	allowLocal := len(via) > 0 && isLoopbackHost(via[0].URL.Host)
	return oauthRedirectWithTrust(req, via, allowLocal)
}

func oauthRedirectWithTrust(req *http.Request, via []*http.Request, allowLocal bool) error {
	if len(via) >= 5 {
		return errors.New("too many metadata redirects")
	}
	if err := checkEndpointURL(req.URL.String()); err != nil {
		return err
	}
	if len(via) > 0 {
		first := via[0]
		if first.Method != http.MethodGet && first.Method != http.MethodHead {
			return errors.New("refusing redirect of credential-bearing POST")
		}
		if first.URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("refusing HTTPS downgrade")
		}
		if !allowLocal && isLoopbackHost(req.URL.Host) {
			return errors.New("refusing remote metadata redirect to loopback")
		}
	}
	return nil
}
