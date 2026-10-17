package app

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/egoist/mygo"
	"github.com/rayorole/orole-os-desktop/internal/mcp"
	"github.com/rayorole/orole-os-desktop/internal/policy"
)

type MCP struct{ L *Link }
type Policy struct{ L *Link }
type ApprovalQueue struct {
	Items []policy.Pending `json:"items"`
}

var (
	MCPStatus   = mygo.NewEvent[mcp.ServerState]("mcp:status")
	MCPAuthURL  = mygo.NewEvent[mcp.AuthStart]("mcp:auth-url")
	MCPAuthDone = mygo.NewEvent[mcp.AuthDone]("mcp:auth-done")
	// MCPCleanupFailed: a server was removed, but closing its connection or deleting its credentials did not finish.
	MCPCleanupFailed = mygo.NewEvent[mcp.CleanupFailed]("mcp:cleanup-failed")
	PolicyPending    = mygo.NewEvent[ApprovalQueue]("policy:pending")
)

func forwardMCP(event string, data json.RawMessage) {
	switch event {
	case "mcp.status":
		var v mcp.ServerState
		if json.Unmarshal(data, &v) == nil {
			MCPStatus.Broadcast(v)
		}
	case "mcp.auth.url":
		var v mcp.AuthStart
		if json.Unmarshal(data, &v) == nil {
			// Only open daemon-issued web authorization URLs; never arbitrary shell schemes.
			if u, err := url.Parse(v.URL); err == nil && (u.Scheme == "https" || (u.Scheme == "http" && u.Hostname() == "127.0.0.1")) {
				mygo.Shell.OpenExternal(v.URL)
			}
			MCPAuthURL.Broadcast(v)
		}
	case "mcp.auth.done":
		var v mcp.AuthDone
		if json.Unmarshal(data, &v) == nil {
			MCPAuthDone.Broadcast(v)
		}
	case "mcp.cleanup.failed":
		var v mcp.CleanupFailed
		if json.Unmarshal(data, &v) == nil {
			MCPCleanupFailed.Broadcast(v)
		}
	case "policy.pending":
		var v ApprovalQueue
		if json.Unmarshal(data, &v) == nil {
			PolicyPending.Broadcast(v)
		}
	}
}

func (s MCP) List(ctx context.Context) ([]mcp.ServerState, error) {
	var out []mcp.ServerState
	err := s.L.call(ctx, "mcp.servers.list", nil, &out)
	return out, err
}
func (s MCP) Add(ctx context.Context, p mcp.AddParams) (mcp.ServerState, error) {
	var out mcp.ServerState
	err := s.L.call(ctx, "mcp.servers.add", p, &out)
	return out, err
}
func (s MCP) Update(ctx context.Context, id string, p mcp.UpdateParams) (mcp.ServerState, error) {
	var out mcp.ServerState
	err := s.L.call(ctx, "mcp.servers.update", map[string]any{"id": id, "update": p}, &out)
	return out, err
}
func (s MCP) Remove(ctx context.Context, id string) error {
	return s.L.call(ctx, "mcp.servers.remove", map[string]string{"id": id}, nil)
}
func (s MCP) Enable(ctx context.Context, id string, enabled bool) (mcp.ServerState, error) {
	var out mcp.ServerState
	err := s.L.call(ctx, "mcp.servers.enable", map[string]any{"id": id, "enabled": enabled}, &out)
	return out, err
}
func (s MCP) Connect(ctx context.Context, id string) error {
	return s.L.call(ctx, "mcp.connect", map[string]string{"id": id}, nil)
}
func (s MCP) Tools(ctx context.Context, id string) ([]mcp.ToolInfo, error) {
	var out []mcp.ToolInfo
	err := s.L.call(ctx, "mcp.tools", map[string]string{"id": id}, &out)
	return out, err
}
func (s MCP) Catalog(ctx context.Context, id string) (mcp.Catalog, error) {
	var out mcp.Catalog
	err := s.L.call(ctx, "mcp.catalog", map[string]string{"id": id}, &out)
	return out, err
}
func (s MCP) RefreshCatalog(ctx context.Context, id string) (mcp.Catalog, error) {
	var out mcp.Catalog
	err := s.L.call(ctx, "mcp.catalog.refresh", map[string]string{"id": id}, &out)
	return out, err
}
func (s MCP) Diagnostics(ctx context.Context, id string) (mcp.Diagnostics, error) {
	var out mcp.Diagnostics
	err := s.L.call(ctx, "mcp.diagnostics", map[string]string{"id": id}, &out)
	return out, err
}
func (s MCP) DiscoverAuth(ctx context.Context, id string) (mcp.Discovery, error) {
	var out mcp.Discovery
	err := s.L.call(ctx, "mcp.auth.discover", map[string]string{"id": id}, &out)
	return out, err
}
func (s MCP) StartAuth(ctx context.Context, id string) (mcp.AuthStart, error) {
	var out mcp.AuthStart
	err := s.L.call(ctx, "mcp.auth.start", map[string]string{"id": id}, &out)
	return out, err
}
func (s MCP) CancelAuth(ctx context.Context, id string) error {
	return s.L.call(ctx, "mcp.auth.cancel", map[string]string{"id": id}, nil)
}
func (s MCP) SignOut(ctx context.Context, id string) error {
	return s.L.call(ctx, "mcp.auth.signout", map[string]string{"id": id}, nil)
}
func (s MCP) SetToolPermission(ctx context.Context, id, tool, decision string) error {
	return s.L.call(ctx, "mcp.permissions.set", map[string]string{"id": id, "tool": tool, "decision": decision}, nil)
}
func (s MCP) Call(ctx context.Context, id, tool string, arguments map[string]any, callID string, timeoutMs int64) (mcp.CallResult, error) {
	var out mcp.CallResult
	err := s.L.call(ctx, "mcp.call", map[string]any{"id": id, "tool": tool, "arguments": arguments, "callId": callID, "timeoutMs": timeoutMs}, &out)
	return out, err
}
func (s MCP) Cancel(ctx context.Context, callID string) (bool, error) {
	var out bool
	err := s.L.call(ctx, "mcp.cancel", map[string]string{"callId": callID}, &out)
	return out, err
}
func (s Policy) Pending(ctx context.Context) ([]policy.Pending, error) {
	var out []policy.Pending
	err := s.L.call(ctx, "policy.pending", nil, &out)
	return out, err
}
func (s Policy) Resolve(ctx context.Context, id string, allow, remember bool) error {
	return s.L.call(ctx, "policy.resolve", map[string]any{"id": id, "allow": allow, "remember": remember}, nil)
}
func (s Policy) Rules(ctx context.Context) ([]policy.Rule, error) {
	var out []policy.Rule
	err := s.L.call(ctx, "policy.rules.list", nil, &out)
	return out, err
}
func (s Policy) AddRule(ctx context.Context, rule policy.Rule) (policy.Rule, error) {
	var out policy.Rule
	err := s.L.call(ctx, "policy.rules.add", rule, &out)
	return out, err
}
func (s Policy) DeleteRule(ctx context.Context, id int64) error {
	return s.L.call(ctx, "policy.rules.delete", map[string]int64{"id": id}, nil)
}
