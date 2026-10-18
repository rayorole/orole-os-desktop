package mcp

import (
	"context"
	"maps"
	"reflect"
)

// Change tracking and the permission cache.
//
// Consumers that derive something expensive from the server list and the catalogs (the gateway's
// tool index, a bot's tool specs) key their cache on Generation instead of re-reading the
// registry on every call. Generation moves whenever a server is added, edited, enabled, disabled,
// removed, changes status, or its catalog content changes, i.e. exactly when a status event is
// published or a server is removed. Per-tool permissions are not part of it: they only decorate
// Catalog results (see PermissionVersion), and policy decisions read the permissions table directly.

// Generation returns a counter that increases whenever the server list, a server's status or a
// catalog changed. A cache built after reading generation g is current for as long as Generation
// still returns g; read it before building so a concurrent change is never masked.
func (m *Manager) Generation() uint64 { return m.gen.Load() }

// PermissionVersion returns a counter that increases on every per-tool permission write (including
// failed ones) and every server removal. Permissions decorate Catalog results (a tool set to "deny"
// is not offered to bots), so caches of those results key on it next to Generation. It is separate
// from Generation because the gateway's tool index does not depend on permissions.
func (m *Manager) PermissionVersion() uint64 {
	m.permMu.RLock()
	defer m.permMu.RUnlock()
	return m.permEpoch
}

// publishStatus publishes mcp.status for st, but only when st differs from what was last
// published for that server (or force is set because the catalog behind it changed without
// changing st). Subscribers re-list everything on each event, so identical repeats are pure cost.
// A removed server publishes nothing more: its connection is still winding down in the background.
func (m *Manager) publishStatus(st ServerState, force bool) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	if _, gone := m.removed[st.ID]; gone {
		return
	}
	if last, ok := m.lastStatus[st.ID]; ok && last == st && !force {
		return
	}
	m.lastStatus[st.ID] = st
	m.gen.Add(1)
	m.publish("mcp.status", st)
}

// publishRemoved forgets the server's last status and publishes mcp.removed.
func (m *Manager) publishRemoved(id string) {
	m.statusMu.Lock()
	defer m.statusMu.Unlock()
	delete(m.lastStatus, id)
	m.removed[id] = struct{}{}
	m.gen.Add(1)
	m.publish("mcp.removed", map[string]string{"id": id})
}

// sameCatalogContent reports whether two catalogs list the same tools, resources and prompts
// (FetchedAt is ignored, nil and empty lists are equal).
func sameCatalogContent(a, b *Catalog) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ProtocolVersion == b.ProtocolVersion && a.ServerName == b.ServerName && a.ServerVersion == b.ServerVersion &&
		string(a.Capabilities) == string(b.Capabilities) &&
		reflect.DeepEqual(nonNil(a.Tools), nonNil(b.Tools)) &&
		reflect.DeepEqual(nonNil(a.Resources), nonNil(b.Resources)) &&
		reflect.DeepEqual(nonNil(a.Prompts), nonNil(b.Prompts))
}

// permissions returns the stored per-tool decisions of a server from memory, loading them on
// first use. The returned map is shared and must not be modified.
func (m *Manager) permissions(ctx context.Context, id string) (map[string]string, error) {
	m.permMu.RLock()
	p, ok := m.perms[id]
	epoch := m.permEpoch
	m.permMu.RUnlock()
	if ok {
		return p, nil
	}
	p, err := m.loadToolPermissions(ctx, id)
	if err != nil {
		return nil, err
	}
	m.permMu.Lock()
	// A write that landed while we were reading may have been missed by the query.
	if m.permEpoch == epoch {
		m.perms[id] = p
	}
	m.permMu.Unlock()
	return p, nil
}

// invalidatePermissions drops the cached decisions of one server.
func (m *Manager) invalidatePermissions(id string) {
	m.permMu.Lock()
	m.permEpoch++
	delete(m.perms, id)
	m.permMu.Unlock()
}

// ToolPermissions returns the stored per-tool decisions for a server.
func (m *Manager) ToolPermissions(ctx context.Context, id string) (map[string]string, error) {
	p, err := m.permissions(ctx, id)
	if err != nil {
		return nil, err
	}
	return maps.Clone(p), nil
}
