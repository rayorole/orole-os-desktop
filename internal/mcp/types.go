// Package mcp is Orole OS's MCP client platform: a persisted registry of MCP servers,
// a connection manager (state machine, retry/backoff, discovery cache), OAuth 2.1
// authorization (RFC 9728 discovery, RFC 7591 registration or client-ID metadata
// documents, PKCE S256, RFC 8707 resource indicators, loopback redirect), and tool
// invocation with timeout/cancel.
//
// Everything a server returns (names, descriptions, results, errors) is untrusted
// data: it is sanitised for display and never interpreted as instructions. Secrets
// live only in internal/credentials under mcp/<serverID>.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Status is a connection's state.
type Status string

const (
	StatusDisabled     Status = "disabled"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusAuthRequired Status = "auth_required"
	StatusBackoff      Status = "backoff" // last attempt failed; retry scheduled
)

// AuthKind says how a server authenticates.
type AuthKind string

const (
	AuthNone   AuthKind = "none"
	AuthBearer AuthKind = "bearer"
	AuthOAuth  AuthKind = "oauth"
)

// Adapter names a typed adapter built on top of a server.
const (
	AdapterNone  = ""
	AdapterMail  = "mail"
	AdapterDrive = "drive"
)

// ServerConfig is a registered server (the persisted, non-secret part).
type ServerConfig struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Transport     string   `json:"transport"`
	Adapter       string   `json:"adapter"`
	Enabled       bool     `json:"enabled"`
	AuthKind      AuthKind `json:"authKind"`
	OAuthClientID string   `json:"oauthClientId"` // public id or https client-metadata URL; "" = dynamic registration
	Scopes        string   `json:"scopes"`        // space separated override; "" = what the server asks for
	TokenHeader   string   `json:"tokenHeader"`   // bearer kind: header carrying the token; "" = Authorization: Bearer
	CreatedAt     int64    `json:"createdAt"`
}

// AddParams creates a server. BearerToken and ClientSecret go to the credential
// store and are never persisted anywhere else.
type AddParams struct {
	Name          string   `json:"name"`
	URL           string   `json:"url"`
	Adapter       string   `json:"adapter"`
	AuthKind      AuthKind `json:"authKind"`
	OAuthClientID string   `json:"oauthClientId"`
	Scopes        string   `json:"scopes"`
	BearerToken   string   `json:"bearerToken,omitempty"`
	TokenHeader   string   `json:"tokenHeader,omitempty"` // send BearerToken in this header instead of Authorization: Bearer
	ClientSecret  string   `json:"clientSecret,omitempty"`
}

// ServerState is a server plus live status for list views.
type ServerState struct {
	ServerConfig
	Status     Status `json:"status"`
	LastError  string `json:"lastError,omitempty"`
	Authorized bool   `json:"authorized"` // credentials are stored
	ToolCount  int    `json:"toolCount"`
}

// ToolInfo is one discovered tool. All strings are sanitised.
type ToolInfo struct {
	Name         string          `json:"name"`
	Title        string          `json:"title,omitempty"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// Annotations are the server's claims. They are hints only.
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
	// Effect is our classification derived from the hints: read | write | destructive.
	Effect string `json:"effect"`
	// Permission is the stored per-tool decision ("allow"|"ask"|"deny") or "".
	Permission string `json:"permission,omitempty"`
}

// ResourceInfo is a discovered resource or resource template.
type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Template    bool   `json:"template,omitempty"`
}

// PromptArg is a prompt argument.
type PromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptInfo is a discovered prompt.
type PromptInfo struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Arguments   []PromptArg `json:"arguments,omitempty"`
}

// Catalog is the discovery cache for one server.
type Catalog struct {
	ServerID        string          `json:"serverId"`
	FetchedAt       int64           `json:"fetchedAt"`
	ProtocolVersion string          `json:"protocolVersion"`
	ServerName      string          `json:"serverName"`
	ServerVersion   string          `json:"serverVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	Tools           []ToolInfo      `json:"tools"`
	Resources       []ResourceInfo  `json:"resources"`
	Prompts         []PromptInfo    `json:"prompts"`
	// Skipped counts tools dropped because their name was unsafe.
	Skipped int `json:"skipped,omitempty"`
}

// ContentBlock is one part of a tool result. Text is sanitised and size-limited.
type ContentBlock struct {
	Type      string `json:"type"` // text | image | audio | resource | resource_link
	Text      string `json:"text,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
	URI       string `json:"uri,omitempty"`
	Bytes     int    `json:"bytes,omitempty"`     // size of binary payload (not forwarded)
	Truncated bool   `json:"truncated,omitempty"` // text was cut at the size limit
}

// CallResult is a tool result. It is untrusted data.
type CallResult struct {
	IsError    bool            `json:"isError"`
	Content    []ContentBlock  `json:"content"`
	Structured json.RawMessage `json:"structured,omitempty"`
	LatencyMs  int64           `json:"latencyMs"`
	Untrusted  bool            `json:"untrusted"` // always true: render as data
}

// CallOptions tune one invocation.
type CallOptions struct {
	Timeout time.Duration // 0 = manager default
	CallID  string        // client-chosen id usable with CancelCall
	Actor   string        // policy actor for checked calls
	// BeforeCall revalidates caller-owned authority immediately before dispatch.
	// InvokeChecked runs it after policy approval. It cannot undo remote effects
	// that were already dispatched before authority was revoked.
	BeforeCall func(context.Context) error
}

// AuthDiag is the non-secret OAuth picture of a server.
type AuthDiag struct {
	Kind             AuthKind `json:"kind"`
	HasCredentials   bool     `json:"hasCredentials"`
	HasRefreshToken  bool     `json:"hasRefreshToken"`
	AccessExpiresAt  int64    `json:"accessExpiresAt,omitempty"` // unix
	Issuer           string   `json:"issuer,omitempty"`
	AuthEndpoint     string   `json:"authorizationEndpoint,omitempty"`
	TokenEndpoint    string   `json:"tokenEndpoint,omitempty"`
	RegistrationKind string   `json:"registrationKind,omitempty"`
	ClientID         string   `json:"clientId,omitempty"`
	Scopes           string   `json:"scopes,omitempty"`
	AuthorizedAt     int64    `json:"authorizedAt,omitempty"`
	FlowActive       bool     `json:"flowActive"`
}

// Diagnostics explains the current state of a server connection.
type Diagnostics struct {
	Server          ServerState     `json:"server"`
	LastError       string          `json:"lastError,omitempty"`
	LastErrorAt     int64           `json:"lastErrorAt,omitempty"`
	Attempts        int             `json:"attempts"`
	NextRetryAt     int64           `json:"nextRetryAt,omitempty"` // unix ms
	ConnectedAt     int64           `json:"connectedAt,omitempty"` // unix
	LatencyMs       int64           `json:"latencyMs"`             // last ping/call round trip
	ProtocolVersion string          `json:"protocolVersion,omitempty"`
	ServerName      string          `json:"serverName,omitempty"`
	ServerVersion   string          `json:"serverVersion,omitempty"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	Tools           int             `json:"tools"`
	Resources       int             `json:"resources"`
	Prompts         int             `json:"prompts"`
	CatalogAt       int64           `json:"catalogAt,omitempty"`
	Auth            AuthDiag        `json:"auth"`
	Discovery       *Discovery      `json:"discovery,omitempty"` // latest auth discovery result
}

// Errors.
var (
	ErrNotFound       = errors.New("mcp: no such server")
	ErrDisabled       = errors.New("mcp: server is disabled")
	ErrNotConnected   = errors.New("mcp: server is not connected")
	ErrNoSuchTool     = errors.New("mcp: server has no such tool")
	ErrNoCredentials  = errors.New("mcp: no credentials stored")
	ErrReauthRequired = errors.New("mcp: authorization required")
	ErrNoRegistration = errors.New("mcp: the authorization server offers no way to register this client")
	ErrUnsafeURL      = errors.New("mcp: server URL must be https (or http on loopback)")
	ErrBadRequest     = errors.New("mcp: bad request")
)

// AuthRequiredError reports that the server demands (new) authorization.
type AuthRequiredError struct {
	Reason string
	Scope  string
}

func (e *AuthRequiredError) Error() string { return "mcp: authorization required: " + e.Reason }
func (e *AuthRequiredError) Is(t error) bool {
	return t == ErrReauthRequired
}
