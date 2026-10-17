package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const serverCols = `id,name,url,transport,adapter,enabled,auth_kind,oauth_client_id,scopes,token_header,created_at`

type rowScanner interface{ Scan(...any) error }

func scanServer(r rowScanner) (ServerConfig, error) {
	var s ServerConfig
	var en int
	var ak string
	err := r.Scan(&s.ID, &s.Name, &s.URL, &s.Transport, &s.Adapter, &en, &ak, &s.OAuthClientID, &s.Scopes, &s.TokenHeader, &s.CreatedAt)
	s.Enabled, s.AuthKind = en != 0, AuthKind(ak)
	return s, err
}

func (m *Manager) dbListServers(ctx context.Context) ([]ServerConfig, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+serverCols+` FROM mcp_servers ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerConfig{}
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (m *Manager) dbGetServer(ctx context.Context, id string) (ServerConfig, error) {
	s, err := scanServer(m.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM mcp_servers WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrNotFound
	}
	return s, err
}

func (m *Manager) dbInsertServer(ctx context.Context, s ServerConfig) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO mcp_servers(`+serverCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.Name, s.URL, s.Transport, s.Adapter, boolInt(s.Enabled), string(s.AuthKind), s.OAuthClientID, s.Scopes, s.TokenHeader, s.CreatedAt)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// saveCatalog persists the discovery cache.
func (m *Manager) saveCatalog(c *Catalog) error {
	info, _ := json.Marshal(map[string]string{"name": c.ServerName, "version": c.ServerVersion})
	caps := c.Capabilities
	if len(caps) == 0 {
		caps = json.RawMessage(`{}`)
	}
	tools, _ := json.Marshal(nonNil(c.Tools))
	var res, tpl []ResourceInfo
	for _, r := range c.Resources {
		if r.Template {
			tpl = append(tpl, r)
		} else {
			res = append(res, r)
		}
	}
	resj, _ := json.Marshal(nonNil(res))
	tplj, _ := json.Marshal(nonNil(tpl))
	pj, _ := json.Marshal(nonNil(c.Prompts))
	_, err := m.db.Exec(`INSERT INTO mcp_cache(server_id,fetched_at,protocol_version,server_info,capabilities,tools,resources,resource_templates,prompts)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET fetched_at=excluded.fetched_at, protocol_version=excluded.protocol_version,
		server_info=excluded.server_info, capabilities=excluded.capabilities, tools=excluded.tools, resources=excluded.resources,
		resource_templates=excluded.resource_templates, prompts=excluded.prompts`,
		c.ServerID, c.FetchedAt, c.ProtocolVersion, string(info), string(caps), string(tools), string(resj), string(tplj), string(pj))
	return err
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (m *Manager) loadCatalog(id string) (*Catalog, error) {
	var c Catalog
	var info, caps, tools, res, tpl, prompts string
	err := m.db.QueryRow(`SELECT fetched_at,protocol_version,server_info,capabilities,tools,resources,resource_templates,prompts FROM mcp_cache WHERE server_id=?`, id).
		Scan(&c.FetchedAt, &c.ProtocolVersion, &info, &caps, &tools, &res, &tpl, &prompts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.ServerID = id
	var si struct{ Name, Version string }
	_ = json.Unmarshal([]byte(info), &si)
	c.ServerName, c.ServerVersion = si.Name, si.Version
	c.Capabilities = json.RawMessage(caps)
	_ = json.Unmarshal([]byte(tools), &c.Tools)
	var r1, r2 []ResourceInfo
	_ = json.Unmarshal([]byte(res), &r1)
	_ = json.Unmarshal([]byte(tpl), &r2)
	c.Resources = append(r1, r2...)
	_ = json.Unmarshal([]byte(prompts), &c.Prompts)
	return &c, nil
}

// saveAuthState records the non-secret outcome of an authorization.
func (m *Manager) saveAuthState(id string, d *Discovery, reg clientReg, b *credBlob) error {
	_, err := m.db.Exec(`INSERT INTO mcp_auth_state(server_id,issuer,authorization_endpoint,token_endpoint,registration_endpoint,resource,registration_kind,client_id,scopes,authorized_at,access_expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server_id) DO UPDATE SET issuer=excluded.issuer, authorization_endpoint=excluded.authorization_endpoint,
		token_endpoint=excluded.token_endpoint, registration_endpoint=excluded.registration_endpoint, resource=excluded.resource,
		registration_kind=excluded.registration_kind, client_id=excluded.client_id, scopes=excluded.scopes, authorized_at=excluded.authorized_at,
		access_expires_at=excluded.access_expires_at`,
		id, d.AS.Issuer, d.AS.AuthorizationEndpoint, d.AS.TokenEndpoint, d.AS.RegistrationEndpoint, d.Resource, reg.kind, reg.clientID, b.Scope,
		time.Now().Unix(), b.Expiry.Unix())
	return err
}

func (m *Manager) loadAuthState(id string) (AuthDiag, bool) {
	var a AuthDiag
	err := m.db.QueryRow(`SELECT issuer,authorization_endpoint,token_endpoint,registration_kind,client_id,scopes,authorized_at FROM mcp_auth_state WHERE server_id=?`, id).
		Scan(&a.Issuer, &a.AuthEndpoint, &a.TokenEndpoint, &a.RegistrationKind, &a.ClientID, &a.Scopes, &a.AuthorizedAt)
	return a, err == nil
}

func (m *Manager) touchAuthExpiry(id string, exp time.Time) {
	_, _ = m.db.Exec(`UPDATE mcp_auth_state SET access_expires_at=? WHERE server_id=?`, exp.Unix(), id)
}

// loadToolPermissions reads the stored per-tool decisions of a server from SQLite.
func (m *Manager) loadToolPermissions(ctx context.Context, id string) (map[string]string, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT tool,decision FROM mcp_permissions WHERE server_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var t, d string
		if err := rows.Scan(&t, &d); err != nil {
			return nil, err
		}
		out[t] = d
	}
	return out, rows.Err()
}

// SetToolPermission stores allow|ask|deny for one tool; "" clears it.
func (m *Manager) SetToolPermission(ctx context.Context, id, tool, decision string) error {
	if _, err := m.dbGetServer(ctx, id); err != nil {
		return err
	}
	if !validToolName(tool) {
		return ErrNoSuchTool
	}
	if decision == "" {
		_, err := m.db.ExecContext(ctx, `DELETE FROM mcp_permissions WHERE server_id=? AND tool=?`, id, tool)
		m.invalidatePermissions(id)
		return err
	}
	if decision != "allow" && decision != "ask" && decision != "deny" {
		return ErrBadRequest
	}
	_, err := m.db.ExecContext(ctx, `INSERT INTO mcp_permissions(server_id,tool,decision) VALUES(?,?,?) ON CONFLICT(server_id,tool) DO UPDATE SET decision=excluded.decision`, id, tool, decision)
	m.invalidatePermissions(id)
	if err == nil {
		m.audit("mcp.permission.set", "mcp/"+id+"/"+tool, "allowed", decision)
	}
	return err
}
