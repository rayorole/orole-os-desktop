package mcp

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rayorole/orole-os-desktop/internal/untrusted"
)

// conn is one server's connection. State machine:
//
//	disabled -> connecting -> connected -> (lost) -> backoff -> connecting ...
//	                      \-> auth_required (waits for Authorize/Connect)
//	                      \-> backoff (exponential, capped, jittered)
type conn struct {
	m  *Manager
	id string

	mu          sync.Mutex
	cfg         ServerConfig
	status      Status
	lastErr     string
	lastErrAt   time.Time
	attempts    int
	nextRetry   time.Time
	connectedAt time.Time
	latency     time.Duration
	session     *sdk.ClientSession
	catalog     *Catalog
	discovery   *Discovery
	calls       map[string]context.CancelFunc

	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
}

func newConn(m *Manager, cfg ServerConfig) *conn {
	c := &conn{m: m, id: cfg.ID, cfg: cfg, status: StatusDisabled, calls: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1)}
	if cat, err := m.loadCatalog(cfg.ID); err == nil {
		c.catalog = cat // cached tools remain visible while offline; live() only trusts a session
	}
	return c
}

func (c *conn) config() ServerConfig { c.mu.Lock(); defer c.mu.Unlock(); return c.cfg }
func (c *conn) setConfig(cfg ServerConfig) {
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
}

func (c *conn) statusNow() Status { c.mu.Lock(); defer c.mu.Unlock(); return c.status }

func (c *conn) summary() (Status, string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	if c.catalog != nil {
		n = len(c.catalog.Tools)
	}
	return c.status, c.lastErr, n
}

// live returns the session and catalog only while connected.
func (c *conn) live() (*sdk.ClientSession, *Catalog) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status != StatusConnected || c.session == nil {
		return nil, c.catalog
	}
	return c.session, c.catalog
}

func (c *conn) setStatus(s Status) {
	c.mu.Lock()
	c.status = s
	cfg := c.cfg
	c.mu.Unlock()
	c.m.publishStatus(c.m.state(cfg), false)
}

func (c *conn) noteError(err error) {
	message := c.m.authSession(c.id).scrub(err.Error())
	c.mu.Lock()
	c.lastErr = untrusted.Line(message, 500)
	c.lastErrAt = time.Now()
	cfg := c.cfg
	c.mu.Unlock()
	c.m.publishStatus(c.m.state(cfg), false)
}

func (c *conn) noteLatency(d time.Duration) { c.mu.Lock(); c.latency = d; c.mu.Unlock() }

func (c *conn) wakeUp() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) trackCall(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	c.calls[id] = cancel
	c.mu.Unlock()
}
func (c *conn) untrackCall(id string) { c.mu.Lock(); delete(c.calls, id); c.mu.Unlock() }
func (c *conn) cancelCall(id string) bool {
	c.mu.Lock()
	cancel := c.calls[id]
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (c *conn) markAuthRequired(err error) {
	c.noteError(err)
	c.setStatus(StatusAuthRequired)
}

// start launches the connection loop if it is not running.
func (c *conn) start() {
	c.mu.Lock()
	if c.running || !c.cfg.Enabled {
		c.mu.Unlock()
		return
	}
	parent := c.m.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	c.running, c.cancel, c.done = true, cancel, make(chan struct{})
	done := c.done
	c.mu.Unlock()
	go func() {
		defer close(done)
		c.run(ctx)
	}()
}

// stop ends the loop, closes the session and waits for the goroutine.
func (c *conn) stop() {
	c.mu.Lock()
	cancel, done, sess := c.cancel, c.done, c.session
	running := c.running
	c.mu.Unlock()
	if running {
		cancel()
		if sess != nil {
			_ = sess.Close()
		}
		<-done
	}
	c.mu.Lock()
	c.running, c.session, c.attempts = false, nil, 0
	c.nextRetry = time.Time{}
	c.status = StatusDisabled
	cfg := c.cfg
	c.mu.Unlock()
	c.m.publishStatus(c.m.state(cfg), false)
}

// reconnect drops the session so the loop dials again with current credentials.
func (c *conn) reconnect() {
	c.mu.Lock()
	sess := c.session
	c.attempts = 0
	c.mu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
	c.wakeUp()
}

func (c *conn) run(ctx context.Context) {
	for ctx.Err() == nil {
		c.setStatus(StatusConnecting)
		sess, hc, err := c.dial(ctx)
		if err == nil {
			stopClose := context.AfterFunc(ctx, func() { _ = sess.Close() })
			c.onConnected(ctx, sess)
			waitErr := sess.Wait()
			stopClose()
			hc.CloseIdleConnections()
			c.onLost(waitErr, ctx)
			if ctx.Err() != nil {
				return
			}
			// A dropped live session retries quickly at the base delay.
			if !c.sleep(ctx, c.m.backoffBase) {
				return
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrReauthRequired) || errors.Is(err, ErrNoCredentials) {
			c.mu.Lock()
			c.attempts = 0
			c.mu.Unlock()
			c.markAuthRequired(err)
			select { // wait for a flow to finish or the user to retry
			case <-c.wake:
			case <-ctx.Done():
				return
			}
			continue
		}
		c.mu.Lock()
		c.attempts++
		n := c.attempts
		c.mu.Unlock()
		c.noteError(err)
		delay := c.m.backoffFor(n)
		c.mu.Lock()
		c.nextRetry = time.Now().Add(delay)
		c.mu.Unlock()
		c.setStatus(StatusBackoff)
		if !c.sleep(ctx, delay) {
			return
		}
		c.mu.Lock()
		c.nextRetry = time.Time{}
		c.mu.Unlock()
	}
}

// backoffFor returns base*2^(n-1) capped at max, with +-20% jitter.
func (m *Manager) backoffFor(n int) time.Duration {
	d := m.backoffBase
	for i := 1; i < n && d < m.backoffMax; i++ {
		d *= 2
	}
	d = min(d, m.backoffMax)
	j := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(d) * j)
}

// sleep waits d, returning early (true) on wake and false if ctx ended.
func (c *conn) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-c.wake:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *conn) dial(ctx context.Context) (*sdk.ClientSession, *http.Client, error) {
	cfg := c.config()
	network, err := newOAuthNetwork(cfg.URL)
	if err != nil {
		return nil, nil, err
	}
	hc, err := network.streamClient(c.m.hc)
	if err != nil {
		return nil, nil, err
	}
	handler := sdkHandler{a: c.m.authSession(cfg.ID)}
	if cfg.AuthKind == AuthBearer && cfg.TokenHeader != "" {
		u, err := url.Parse(cfg.URL)
		if err != nil {
			return nil, nil, err
		}
		hc.Transport = &tokenHeaderTransport{base: hc.Transport, scheme: u.Scheme, host: u.Host, header: cfg.TokenHeader, a: handler.a}
		handler.tokenHeader = cfg.TokenHeader
	}
	dctx, cancel := context.WithTimeout(ctx, c.m.connectTimeout)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "orole-os", Title: "Orole OS", Version: c.m.version}, &sdk.ClientOptions{
		Capabilities:               &sdk.ClientCapabilities{},
		ToolListChangedHandler:     func(context.Context, *sdk.ToolListChangedRequest) { c.refreshAsync() },
		ResourceListChangedHandler: func(context.Context, *sdk.ResourceListChangedRequest) { c.refreshAsync() },
		PromptListChangedHandler:   func(context.Context, *sdk.PromptListChangedRequest) { c.refreshAsync() },
	})
	connect := func(version string) (*sdk.ClientSession, error) {
		tr := &sdk.StreamableClientTransport{
			Endpoint:     cfg.URL,
			HTTPClient:   hc,
			OAuthHandler: handler,
			MaxRetries:   2,
		}
		return client.Connect(dctx, tr, &sdk.ClientSessionOptions{ProtocolVersion: version})
	}
	// Start with the legacy initialize handshake: the SDK default first sends the 2026-07-28
	// server/discover probe, and Mobbin's notifications/initialized hung after that fallback.
	// Servers that only speak the stateless 2026-07-28 protocol (Orole Drive and Mail) reject
	// initialize with UnsupportedProtocolVersion; only then connect with server/discover.
	sess, err := connect(legacyProtocolVersion)
	if werr := (*jsonrpc.Error)(nil); errors.As(err, &werr) && werr.Code == sdk.CodeUnsupportedProtocolVersion {
		sess, err = connect(modernProtocolVersion)
	}
	if err != nil {
		hc.CloseIdleConnections()
		return nil, nil, classifyConnectErr(err)
	}
	return sess, hc, nil
}

const (
	legacyProtocolVersion = "2025-06-18"
	modernProtocolVersion = "2026-07-28"
)

func classifyConnectErr(err error) error {
	var are *AuthRequiredError
	if errors.As(err, &are) {
		return are
	}
	return err
}

func (c *conn) onConnected(ctx context.Context, sess *sdk.ClientSession) {
	start := time.Now()
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := sess.Ping(pctx, nil); err == nil {
		c.noteLatency(time.Since(start))
	}
	cancel()
	c.mu.Lock()
	c.session = sess
	c.connectedAt = time.Now()
	c.attempts = 0
	c.lastErr = ""
	c.nextRetry = time.Time{}
	c.mu.Unlock()
	discoveryCtx, cancelDiscovery := context.WithTimeout(ctx, c.m.connectTimeout)
	defer cancelDiscovery()
	_, err := c.refreshCatalog(discoveryCtx)
	if ctx.Err() != nil {
		return // stopped or removed meanwhile: nothing to report
	}
	if err != nil {
		c.noteError(errors.New("connected, but discovery failed: " + err.Error()))
	}
	c.setStatus(StatusConnected)
	c.m.audit("mcp.connect", "mcp/"+c.id, "allowed", "")
}

func (c *conn) onLost(err error, ctx context.Context) {
	c.mu.Lock()
	c.session = nil
	c.mu.Unlock()
	if ctx.Err() != nil {
		return
	}
	msg := "connection closed by server"
	if err != nil {
		msg = "connection lost: " + untrusted.Line(err.Error(), 300)
	}
	c.noteError(errors.New(msg))
	c.setStatus(StatusBackoff)
}

func (c *conn) refreshAsync() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = c.refreshCatalog(ctx)
	}()
}

func (c *conn) refreshCatalog(ctx context.Context) (*Catalog, error) {
	c.mu.Lock()
	sess := c.session
	c.mu.Unlock()
	if sess == nil {
		return nil, ErrNotConnected
	}
	cat, err := buildCatalog(ctx, c.id, sess)
	if cat != nil && (err == nil || len(cat.Tools) > 0) {
		c.mu.Lock()
		changed := !sameCatalogContent(c.catalog, cat)
		c.catalog = cat
		cfg := c.cfg
		c.mu.Unlock()
		if serr := c.m.saveCatalog(cat); serr != nil {
			c.m.log.Warn("mcp catalog not cached", "server", c.id, "err", serr)
		}
		// Same tool count but different tools must still reach the gateway and the bots.
		c.m.publishStatus(c.m.state(cfg), changed)
	}
	return cat, err
}

func (c *conn) fillDiagnostics(d *Diagnostics) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d.LastError = c.lastErr
	if !c.lastErrAt.IsZero() {
		d.LastErrorAt = c.lastErrAt.Unix()
	}
	d.Attempts = c.attempts
	if !c.nextRetry.IsZero() {
		d.NextRetryAt = c.nextRetry.UnixMilli()
	}
	if !c.connectedAt.IsZero() && c.status == StatusConnected {
		d.ConnectedAt = c.connectedAt.Unix()
	}
	d.LatencyMs = c.latency.Milliseconds()
	d.Discovery = c.discovery
	if c.catalog != nil {
		d.ProtocolVersion, d.ServerName, d.ServerVersion, d.Capabilities = c.catalog.ProtocolVersion, c.catalog.ServerName, c.catalog.ServerVersion, c.catalog.Capabilities
	}
}
