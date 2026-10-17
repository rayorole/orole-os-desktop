package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rayorole/orole-os-desktop/internal/credentials"
	"github.com/rayorole/orole-os-desktop/internal/events"
	"github.com/rayorole/orole-os-desktop/internal/policy"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

// Publisher delivers events to subscribers (ipc.Server satisfies it).
type Publisher interface {
	Publish(event string, data any)
}

// Options configures a Manager.
type Options struct {
	DB     *sql.DB
	Creds  credentials.Store
	Pub    Publisher      // optional
	Policy *policy.Engine // required for InvokeChecked
	Events *events.Bus    // optional daemon event bus with automation provenance
	Log    *slog.Logger
	// HTTP is the client for OAuth/discovery requests (default: 30s timeout). Each
	// MCP connection guards its transport using the configured server's trust,
	// without an overall timeout (streams are long-lived).
	HTTP    *http.Client
	Version string // reported to servers as client version

	BackoffBase    time.Duration // default 1s
	BackoffMax     time.Duration // default 60s
	ConnectTimeout time.Duration // default 30s
	CallTimeout    time.Duration // default 60s
}

// Manager owns the server registry and every connection. It is safe for concurrent use.
type Manager struct {
	db      *sql.DB
	creds   credentials.Store
	pub     Publisher
	policy  *policy.Engine
	events  *events.Bus
	log     *slog.Logger
	hc      *http.Client // discovery + OAuth
	version string

	backoffBase, backoffMax, connectTimeout, callTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	conns    map[string]*conn
	flows    map[string]*authFlow
	sessions map[string]*authSession

	teardownTimeout time.Duration // bounds each step of the background cleanup after Remove
	cleanup         sync.WaitGroup

	gen        atomic.Uint64 // see Generation
	statusMu   sync.Mutex
	lastStatus map[string]ServerState // last published mcp.status per server
	removed    map[string]struct{}    // ids removed this run (ids are never reused); see publishStatus

	permMu    sync.RWMutex
	perms     map[string]map[string]string // server id -> tool -> decision (see permissions)
	permEpoch uint64
}

// New builds a Manager. Call Start to load persisted servers and connect.
func New(o Options) *Manager {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second, CheckRedirect: oauthRedirect}
	}
	if o.Version == "" {
		o.Version = "0.0.0"
	}
	m := &Manager{db: o.DB, creds: o.Creds, pub: o.Pub, policy: o.Policy, events: o.Events, log: o.Log, hc: hc,
		version:         o.Version,
		teardownTimeout: 10 * time.Second,
		backoffBase:     or(o.BackoffBase, time.Second), backoffMax: or(o.BackoffMax, time.Minute),
		connectTimeout: or(o.ConnectTimeout, 30*time.Second), callTimeout: or(o.CallTimeout, 60*time.Second),
		conns: map[string]*conn{}, flows: map[string]*authFlow{}, sessions: map[string]*authSession{},
		lastStatus: map[string]ServerState{}, removed: map[string]struct{}{}, perms: map[string]map[string]string{}}
	return m
}

func or(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Start loads persisted servers and connects every enabled one.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx, m.cancel = context.WithCancel(context.Background())
	servers, err := m.dbListServers(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range servers {
		c := newConn(m, s)
		m.conns[s.ID] = c
		if s.Enabled {
			c.start()
		}
	}
	return nil
}

// Close disconnects everything and aborts authorization flows.
func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	conns := make([]*conn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	flows := make([]*authFlow, 0, len(m.flows))
	for _, f := range m.flows {
		f.cancel()
		flows = append(flows, f)
	}
	m.mu.Unlock()
	for _, f := range flows {
		<-f.done
	}
	for _, c := range conns {
		c.stop()
	}
	finished := make(chan struct{})
	go func() { m.cleanup.Wait(); close(finished) }()
	select { // background cleanup after Remove; bounded so a stuck credential store cannot hold shutdown
	case <-finished:
	case <-time.After(m.teardownTimeout):
	}
}

func (m *Manager) publish(event string, data any) {
	if m.pub != nil {
		m.pub.Publish(event, data)
	}
}

func (m *Manager) publishToolEvent(ctx context.Context, id, tool string, success bool) {
	m.events.Publish(events.Event{Topic: events.TopicMCPToolCalled, Chain: events.ChainFrom(ctx),
		Data: map[string]any{"serverId": id, "tool": tool, "success": success}})
}

func (m *Manager) audit(action, target, outcome, detail string) {
	_, err := m.db.Exec(`INSERT INTO audit_events(at,actor,action,target,outcome,detail) VALUES(?,?,?,?,?,?)`,
		time.Now().Unix(), "mcp", action, target, outcome, untrusted.Line(detail, 500))
	if err != nil {
		m.log.Warn("audit write failed", "err", err)
	}
}

func (m *Manager) conn(id string) (*conn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.conns[id]
	if c == nil {
		return nil, ErrNotFound
	}
	return c, nil
}

// server returns a server's configuration. A live connection holds the current copy, so the
// registry is only queried for servers without one.
func (m *Manager) server(ctx context.Context, id string) (ServerConfig, error) {
	if c, err := m.conn(id); err == nil {
		return c.config(), nil
	}
	return m.dbGetServer(ctx, id)
}

func (m *Manager) authSession(id string) *authSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a := m.sessions[id]; a != nil {
		return a
	}
	a := newAuthSession(id, m.creds, m.hc, func(ctx context.Context) (string, error) {
		cfg, err := m.server(ctx, id)
		return cfg.URL, err
	})
	a.onChange = func(b *credBlob) { m.touchAuthExpiry(id, b.Expiry) }
	m.sessions[id] = a
	return a
}

// List returns every server with its live status.
func (m *Manager) List(ctx context.Context) ([]ServerState, error) {
	servers, err := m.dbListServers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ServerState, 0, len(servers))
	for _, s := range servers {
		out = append(out, m.state(s))
	}
	return out, nil
}

func (m *Manager) state(s ServerConfig) ServerState {
	st := ServerState{ServerConfig: s, Status: StatusDisabled}
	m.mu.Lock()
	c := m.conns[s.ID]
	m.mu.Unlock()
	if c != nil {
		st.Status, st.LastError, st.ToolCount = c.summary()
	}
	has, _, _, _ := m.authSession(s.ID).Snapshot()
	st.Authorized = has
	return st
}

// Get returns one server's state.
func (m *Manager) Get(ctx context.Context, id string) (ServerState, error) {
	s, err := m.dbGetServer(ctx, id)
	if err != nil {
		return ServerState{}, err
	}
	return m.state(s), nil
}

// ByAdapter returns the first enabled server bound to an adapter ("mail", "drive").
func (m *Manager) ByAdapter(ctx context.Context, adapter string) (ServerState, bool) {
	list, err := m.List(ctx)
	if err != nil {
		return ServerState{}, false
	}
	for _, s := range list {
		if s.Adapter == adapter && s.Enabled {
			return s, true
		}
	}
	return ServerState{}, false
}

func validateAdd(p *AddParams) error {
	p.Name = untrusted.Line(p.Name, 80)
	p.URL = strings.TrimSpace(p.URL)
	if p.Name == "" {
		return fmt.Errorf("%w: name required", ErrBadRequest)
	}
	if err := checkEndpointURL(p.URL); err != nil {
		return err
	}
	if u, _ := url.Parse(p.URL); u.Fragment != "" {
		return fmt.Errorf("%w: URL must not contain a fragment", ErrBadRequest)
	}
	switch p.Adapter {
	case AdapterNone, AdapterMail, AdapterDrive:
	default:
		return fmt.Errorf("%w: unknown adapter %q", ErrBadRequest, untrusted.Line(p.Adapter, 32))
	}
	if p.AuthKind == "" {
		p.AuthKind = AuthOAuth
	}
	switch p.AuthKind {
	case AuthNone, AuthOAuth:
		if p.BearerToken != "" || p.TokenHeader != "" {
			return fmt.Errorf("%w: bearer token or token header given but auth kind is %s", ErrBadRequest, p.AuthKind)
		}
	case AuthBearer:
		if strings.TrimSpace(p.BearerToken) == "" {
			return fmt.Errorf("%w: bearer token required", ErrBadRequest)
		}
		if p.TokenHeader != "" && (!tokenHeaderPattern.MatchString(p.TokenHeader) || reservedTokenHeader(p.TokenHeader)) {
			return fmt.Errorf("%w: token header must be a plain header name that HTTP or MCP does not already use (Authorization, Host, Mcp-Session-Id, ...)", ErrBadRequest)
		}
	default:
		return fmt.Errorf("%w: unknown auth kind", ErrBadRequest)
	}
	p.OAuthClientID = strings.TrimSpace(p.OAuthClientID)
	if len(p.OAuthClientID) > 512 || len(p.Scopes) > 512 {
		return fmt.Errorf("%w: client id or scopes too long", ErrBadRequest)
	}
	p.Scopes = strings.Join(strings.Fields(p.Scopes), " ")
	return nil
}

// tokenHeaderPattern is an RFC 9110 field name restricted to letters, digits and hyphens.
var tokenHeaderPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)

// reservedTokenHeader reports headers the HTTP transport or the MCP client sets itself;
// Authorization is the plain bearer case, chosen by leaving the token header empty.
func reservedTokenHeader(name string) bool {
	low := strings.ToLower(name)
	return strings.HasPrefix(low, "proxy-") || strings.HasPrefix(low, "sec-") || strings.HasPrefix(low, "mcp-") || slices.Contains([]string{
		"authorization", "host", "content-length", "content-type", "content-encoding", "accept", "accept-encoding",
		"transfer-encoding", "connection", "keep-alive", "upgrade", "te", "trailer", "expect", "cookie",
		"origin", "referer", "user-agent", "last-event-id", "forwarded", "via",
	}, low)
}

// Add registers and (if enabled, the default) connects a server.
func (m *Manager) Add(ctx context.Context, p AddParams) (ServerState, error) {
	if err := validateAdd(&p); err != nil {
		return ServerState{}, err
	}
	existing, err := m.dbListServers(ctx)
	if err != nil {
		return ServerState{}, err
	}
	for _, s := range existing {
		if s.URL == p.URL {
			return ServerState{}, fmt.Errorf("%w: a server with this URL is already registered (%q)", ErrBadRequest, s.Name)
		}
	}
	cfg := ServerConfig{ID: uuid.NewString(), Name: p.Name, URL: p.URL, Transport: "streamable-http", Adapter: p.Adapter,
		Enabled: true, AuthKind: p.AuthKind, OAuthClientID: p.OAuthClientID, Scopes: p.Scopes, TokenHeader: p.TokenHeader, CreatedAt: time.Now().Unix()}
	// Secrets first: if the credential store refuses, no row is created.
	if p.AuthKind == AuthBearer {
		if err := m.authSession(cfg.ID).Set(&credBlob{Kind: "bearer", AccessToken: strings.TrimSpace(p.BearerToken)}); err != nil {
			return ServerState{}, err
		}
	}
	if p.ClientSecret != "" {
		m.authSession(cfg.ID).remember(p.ClientSecret)
		if err := m.creds.Set(clientSecretKey(cfg.ID), []byte(p.ClientSecret)); err != nil {
			_ = m.authSession(cfg.ID).Clear()
			return ServerState{}, fmt.Errorf("credential store: %w", err)
		}
	}
	if err := m.dbInsertServer(ctx, cfg); err != nil {
		_ = m.authSession(cfg.ID).Clear()
		return ServerState{}, err
	}
	m.audit("mcp.server.add", "mcp/"+cfg.ID, "allowed", cfg.Name+" "+cfg.URL)
	c := newConn(m, cfg)
	m.mu.Lock()
	m.conns[cfg.ID] = c
	m.mu.Unlock()
	c.start()
	st := m.state(cfg)
	m.publishStatus(st, false)
	return st, nil
}

// UpdateParams changes editable settings; nil fields stay as they are.
type UpdateParams struct {
	Name          *string `json:"name,omitempty"`
	OAuthClientID *string `json:"oauthClientId,omitempty"`
	Scopes        *string `json:"scopes,omitempty"`
	ClientSecret  *string `json:"clientSecret,omitempty"` // "" clears
	BearerToken   *string `json:"bearerToken,omitempty"`
}

// Update edits a server.
func (m *Manager) Update(ctx context.Context, id string, p UpdateParams) (ServerState, error) {
	cfg, err := m.dbGetServer(ctx, id)
	if err != nil {
		return ServerState{}, err
	}
	if p.Name != nil {
		cfg.Name = untrusted.Line(*p.Name, 80)
		if cfg.Name == "" {
			return ServerState{}, fmt.Errorf("%w: name required", ErrBadRequest)
		}
	}
	if p.OAuthClientID != nil {
		cfg.OAuthClientID = strings.TrimSpace(*p.OAuthClientID)
		if len(cfg.OAuthClientID) > 512 {
			return ServerState{}, fmt.Errorf("%w: client id too long", ErrBadRequest)
		}
	}
	if p.Scopes != nil {
		cfg.Scopes = strings.Join(strings.Fields(*p.Scopes), " ")
		if len(cfg.Scopes) > 512 {
			return ServerState{}, fmt.Errorf("%w: scopes too long", ErrBadRequest)
		}
	}
	if p.ClientSecret != nil {
		m.authSession(id).remember(*p.ClientSecret)
		if *p.ClientSecret == "" {
			_ = m.creds.Delete(clientSecretKey(id))
		} else if err := m.creds.Set(clientSecretKey(id), []byte(*p.ClientSecret)); err != nil {
			return ServerState{}, fmt.Errorf("credential store: %w", err)
		}
	}
	if p.BearerToken != nil {
		if cfg.AuthKind != AuthBearer || strings.TrimSpace(*p.BearerToken) == "" {
			return ServerState{}, fmt.Errorf("%w: bearer token only applies to bearer servers and must not be empty", ErrBadRequest)
		}
		if err := m.authSession(id).Set(&credBlob{Kind: "bearer", AccessToken: strings.TrimSpace(*p.BearerToken)}); err != nil {
			return ServerState{}, err
		}
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE mcp_servers SET name=?, oauth_client_id=?, scopes=? WHERE id=?`, cfg.Name, cfg.OAuthClientID, cfg.Scopes, id); err != nil {
		return ServerState{}, err
	}
	if c, err := m.conn(id); err == nil {
		c.setConfig(cfg)
		if p.BearerToken != nil {
			c.reconnect()
		}
	}
	st := m.state(cfg)
	m.publishStatus(st, false)
	return st, nil
}

// Remove deletes a server. The registry row decides: once it is gone the server is disconnected for
// every caller, so Remove returns right after deleting it, and a failed delete changes nothing.
// Cancelling a pending sign-in, closing the session (an HTTP DELETE to the remote server) and deleting
// stored credentials (OS credential store, possibly behind a token refresh) can each stall, so they run
// in the background with a timeout per step; a step that fails is reported as mcp.cleanup.failed.
func (m *Manager) Remove(ctx context.Context, id string) error {
	cfg, err := m.dbGetServer(ctx, id)
	if err != nil {
		return err
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id=?`, id); err != nil {
		return err
	}
	m.mu.Lock()
	c := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	m.invalidatePermissions(id)
	m.audit("mcp.server.remove", "mcp/"+id, "allowed", "")
	m.publishRemoved(id)
	m.teardown(cfg, c)
	return nil
}

// teardown finishes a Remove in the background: it ends the pending sign-in and the connection, then
// deletes the stored credentials. It touches only state of the removed server, so a step that outlives
// its timeout is left to finish on its own and is reported as unconfirmed.
func (m *Manager) teardown(cfg ServerConfig, c *conn) {
	m.cleanup.Add(1)
	go func() {
		defer m.cleanup.Done()
		var failed []string
		if ok, _ := m.within(func() error {
			m.CancelAuth(cfg.ID)
			if c != nil {
				c.stop()
			}
			return nil
		}); !ok {
			failed = append(failed, "closing the connection timed out")
		}
		switch ok, err := m.within(m.authSession(cfg.ID).Clear); {
		case !ok:
			failed = append(failed, "removing the stored credentials timed out; they may still be in the credential store")
		case err != nil:
			failed = append(failed, "removing the stored credentials failed: "+err.Error())
		}
		m.mu.Lock()
		delete(m.sessions, cfg.ID)
		m.mu.Unlock()
		if len(failed) == 0 {
			return
		}
		detail := untrusted.Line(strings.Join(failed, "; "), 300)
		m.log.Warn("mcp server removed, cleanup incomplete", "server", cfg.ID, "problems", detail)
		m.audit("mcp.server.cleanup", "mcp/"+cfg.ID, "error", detail)
		m.publish("mcp.cleanup.failed", CleanupFailed{ServerID: cfg.ID, Name: cfg.Name, Error: detail})
	}()
}

// within runs fn and waits at most teardownTimeout for it (ok=false on timeout). fn keeps running after
// a timeout.
func (m *Manager) within(fn func() error) (ok bool, err error) {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	t := time.NewTimer(m.teardownTimeout)
	defer t.Stop()
	select {
	case err := <-done:
		return true, err
	case <-t.C:
		return false, nil
	}
}

// SetEnabled turns a server on or off; disabling closes the connection.
func (m *Manager) SetEnabled(ctx context.Context, id string, enabled bool) (ServerState, error) {
	cfg, err := m.dbGetServer(ctx, id)
	if err != nil {
		return ServerState{}, err
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE mcp_servers SET enabled=? WHERE id=?`, boolInt(enabled), id); err != nil {
		return ServerState{}, err
	}
	cfg.Enabled = enabled
	c, err := m.conn(id)
	if err != nil {
		return ServerState{}, err
	}
	c.setConfig(cfg)
	if enabled {
		c.start()
	} else {
		c.stop()
	}
	m.audit("mcp.server.enable", "mcp/"+id, "allowed", fmt.Sprint(enabled))
	st := m.state(cfg)
	m.publishStatus(st, false)
	return st, nil
}

// Connect asks a disabled-retry/backoff/auth-required server to try again now.
func (m *Manager) Connect(id string) error {
	c, err := m.conn(id)
	if err != nil {
		return err
	}
	if !c.config().Enabled {
		return ErrDisabled
	}
	c.start()
	c.reconnect()
	return nil
}

// SignOut deletes stored tokens and disconnects; the registration stays.
func (m *Manager) SignOut(ctx context.Context, id string) error {
	cfg, err := m.dbGetServer(ctx, id)
	if err != nil {
		return err
	}
	m.CancelAuth(id)
	if err := m.authSession(id).Clear(); err != nil {
		return err
	}
	_, _ = m.db.ExecContext(ctx, `DELETE FROM mcp_auth_state WHERE server_id=?`, id)
	if c, err := m.conn(id); err == nil {
		c.reconnect()
	}
	m.audit("mcp.auth.signout", "mcp/"+id, "allowed", "")
	m.publishStatus(m.state(cfg), false)
	return nil
}

func (m *Manager) setDiscovery(id string, d *Discovery) {
	if c, err := m.conn(id); err == nil {
		c.mu.Lock()
		c.discovery = d
		c.mu.Unlock()
	}
}

func (m *Manager) setError(id, msg string) {
	if c, err := m.conn(id); err == nil {
		c.noteError(errors.New(msg))
	}
}

// DiscoverAuth runs OAuth discovery for a server (no login) and returns the resolved endpoints.
func (m *Manager) DiscoverAuth(ctx context.Context, id string) (*Discovery, error) {
	cfg, err := m.dbGetServer(ctx, id)
	if err != nil {
		return nil, err
	}
	d, derr := Discover(ctx, m.hc, cfg.URL)
	m.setDiscovery(id, d)
	return d, derr
}

// Tools returns the cached tool catalog with stored per-tool permissions merged in.
func (m *Manager) Tools(ctx context.Context, id string) ([]ToolInfo, error) {
	cat, err := m.Catalog(ctx, id)
	if err != nil {
		return nil, err
	}
	return cat.Tools, nil
}

// Catalog returns the live catalog if connected, else the SQLite cache (possibly empty), with the
// stored per-tool permissions merged in.
func (m *Manager) Catalog(ctx context.Context, id string) (*Catalog, error) {
	cat, err := m.baseCatalog(ctx, id)
	if err != nil {
		return nil, err
	}
	return m.withPermissions(ctx, cat)
}

// baseCatalog is Catalog without permissions: the returned catalog is shared and immutable.
func (m *Manager) baseCatalog(ctx context.Context, id string) (*Catalog, error) {
	c, err := m.conn(id)
	if err != nil {
		// No connection: only a registered server may be answered from the SQLite cache.
		if _, err := m.dbGetServer(ctx, id); err != nil {
			return nil, err
		}
	} else if _, cat := c.live(); cat != nil {
		return cat, nil
	}
	cat, err := m.loadCatalog(id)
	if err != nil {
		return nil, err
	}
	if cat == nil {
		cat = &Catalog{ServerID: id, Tools: []ToolInfo{}, Resources: []ResourceInfo{}, Prompts: []PromptInfo{}}
	}
	return cat, nil
}

// Copy only the mutable permission-bearing slice; cached metadata remains immutable.
func (m *Manager) withPermissions(ctx context.Context, cat *Catalog) (*Catalog, error) {
	perms, err := m.permissions(ctx, cat.ServerID)
	if err != nil {
		return nil, err
	}
	out := *cat
	out.Tools = make([]ToolInfo, len(cat.Tools))
	copy(out.Tools, cat.Tools)
	for i := range out.Tools {
		out.Tools[i].Permission = perms[out.Tools[i].Name]
	}
	return &out, nil
}

// RefreshCatalog re-lists tools/resources/prompts from a connected server.
func (m *Manager) RefreshCatalog(ctx context.Context, id string) (*Catalog, error) {
	c, err := m.conn(id)
	if err != nil {
		return nil, err
	}
	return c.refreshCatalog(ctx)
}

// Diagnostics reports everything known about a server's connection and authorization.
func (m *Manager) Diagnostics(ctx context.Context, id string) (Diagnostics, error) {
	cfg, err := m.dbGetServer(ctx, id)
	if err != nil {
		return Diagnostics{}, err
	}
	d := Diagnostics{Server: m.state(cfg)}
	if c, err := m.conn(id); err == nil {
		c.fillDiagnostics(&d)
	}
	cat, _ := m.Catalog(ctx, id)
	if cat != nil {
		d.Tools, d.Resources, d.Prompts, d.CatalogAt = len(cat.Tools), len(cat.Resources), len(cat.Prompts), cat.FetchedAt
		if d.ProtocolVersion == "" {
			d.ProtocolVersion, d.ServerName, d.ServerVersion, d.Capabilities = cat.ProtocolVersion, cat.ServerName, cat.ServerVersion, cat.Capabilities
		}
	}
	a, _ := m.loadAuthState(id)
	a.Kind = cfg.AuthKind
	has, hasRefresh, exp, _ := m.authSession(id).Snapshot()
	a.HasCredentials, a.HasRefreshToken = has, hasRefresh
	if !exp.IsZero() {
		a.AccessExpiresAt = exp.Unix()
	}
	m.mu.Lock()
	a.FlowActive = m.flows[id] != nil
	m.mu.Unlock()
	d.Auth = a
	return d, nil
}
