# MCP connectivity: Orole Mail and Drive

Observed 2026-10-08 from the development workstation. No user credentials or login were available; no real tools were invoked. The code never substitutes fake data when an integration is unavailable.

## Live endpoint evidence

Commands used (POST body is an unauthenticated MCP initialize):

```sh
curl -i https://mail.orole.be/.well-known/oauth-protected-resource/api/mcp
curl -i https://drive.orole.be/.well-known/oauth-protected-resource/api/mcp
curl -i -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}' \
  https://mail.orole.be/api/mcp
```

Mail protected resource response (HTTP 200, application/json):

```json
{"resource":"https://mail.orole.be/api/mcp","authorization_servers":["https://mail.orole.be/api/auth"],"bearer_methods_supported":["header"],"dpop_signing_alg_values_supported":["EdDSA","ES256","ES512","PS256","RS256"],"scopes_supported":["mcp:read","mcp:write","mcp:send","mcp:trash"]}
```

Drive protected resource response (HTTP 200, application/json):

```json
{"resource":"https://drive.orole.be/api/mcp","authorization_servers":["https://drive.orole.be/api/auth"],"bearer_methods_supported":["header"],"dpop_signing_alg_values_supported":["EdDSA","ES256","ES512","PS256","RS256"],"scopes_supported":["mcp:read","mcp:write","mcp:share","mcp:trash"]}
```

Both unauthenticated MCP POSTs return HTTP 401. Raw relevant headers/body:

```text
Mail:
www-authenticate: Bearer resource_metadata="https://mail.orole.be/.well-known/oauth-protected-resource/api/mcp", scope="mcp:read mcp:write mcp:send mcp:trash"
{"jsonrpc":"2.0","error":{"code":-32000,"message":"missing authorization header"},"id":null}

Drive:
Www-Authenticate: Bearer resource_metadata="https://drive.orole.be/.well-known/oauth-protected-resource/api/mcp", scope="mcp:read"
{"jsonrpc":"2.0","error":{"code":-32000,"message":"missing authorization header"},"id":null}
```

Important distinction: Drive's resource metadata advertises four scopes, but its unauthenticated challenge requests only `mcp:read`. The client defaults to the challenge scope, not all advertised scopes. Configured scope overrides are explicit. The challenge scope is requested whole, never shortened: Apollo's names 71 scopes (about 1.6 KB), and cutting it left a half-written last scope that Apollo rejected as unknown. Only tokens outside the RFC 6749 scope-token characters are dropped.

## Authorization server discovery

Sources:
- [Mail OAuth metadata](https://mail.orole.be/api/auth/.well-known/oauth-authorization-server)
- [Mail OIDC metadata](https://mail.orole.be/api/auth/.well-known/openid-configuration)
- [Drive OAuth metadata](https://drive.orole.be/api/auth/.well-known/oauth-authorization-server)
- [Drive OIDC metadata](https://drive.orole.be/api/auth/.well-known/openid-configuration)

Both path-inserted well-known locations returned HTML error pages rather than usable JSON; the live manager smoke recorded **HTTP 404** for both Mail inserted paths. The path-appended OIDC and OAuth locations returned HTTP 200 application/json. Our discovery tries RFC 8414 insertion, OIDC insertion, OIDC appending, then OAuth appending, retaining attempts in diagnostics and rejecting mismatched issuers/resources, unsafe schemes, and missing PKCE S256 support. The fake also tests a separate deployment edge case: an HTML app shell returned with HTTP 200.

Exact relevant key/value extracts from both JSON documents (substitute mail/drive in endpoint host):

```json
{
  "issuer": "https://mail.orole.be/api/auth",
  "authorization_endpoint": "https://mail.orole.be/api/auth/oauth2/authorize",
  "token_endpoint": "https://mail.orole.be/api/auth/oauth2/token",
  "revocation_endpoint": "https://mail.orole.be/api/auth/oauth2/revoke",
  "response_types_supported": ["code"],
  "response_modes_supported": ["query"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "token_endpoint_auth_methods_supported": ["none", "client_secret_basic", "client_secret_post", "private_key_jwt"],
  "code_challenge_methods_supported": ["S256"],
  "authorization_response_iss_parameter_supported": true,
  "client_id_metadata_document_supported": true,
  "dpop_signing_alg_values_supported": ["EdDSA", "ES256", "ES512", "PS256", "RS256"]
}
```

Mail AS scopes: `openid profile offline_access mcp:read mcp:write mcp:send mcp:trash`.
Drive AS scopes: `openid profile offline_access mcp:read mcp:write mcp:share mcp:trash`.
Neither metadata document includes `registration_endpoint`. Mail and Drive registration probes with native client metadata and `http://127.0.0.1:53682/callback` were rejected without creating clients:

```text
POST https://mail.orole.be/api/auth/oauth2/register
HTTP/1.1 403 Forbidden
{"error":"access_denied","error_description":"Client registration is disabled"}

POST https://drive.orole.be/api/auth/oauth2/register
HTTP/1.1 403 Forbidden
{"error":"access_denied","error_description":"Client registration is disabled"}
```

DCR is therefore **disabled on both live servers**. A configured pre-registered public client ID or HTTPS client-ID metadata-document URL is needed. DPoP is advertised as supported, not verified mandatory. This implementation supports Bearer tokens, not DPoP or private-key JWT client authentication.

### Redirect URI and user action

The daemon listens only on `127.0.0.1:<random>/callback`, for five minutes, and closes the listener on completion/cancellation. It verifies state, Host, PKCE, resource indicator, and RFC 9207 issuer. It emits `mcp.auth.url`; the desktop opens that URL using `mygo.Shell.OpenExternal`, never a daemon shell command.

Live redirect acceptance remains **unverified**: metadata does not describe redirect matching rules, and login/client registration is unavailable. The server's client registration must permit native loopback redirects at arbitrary ports per RFC 8252, or an administrator must provide a compatible client. Do not assume a wildcard or specific URI is accepted merely because it appears in an example.

To authorize:
1. In Settings > Integrations, quick-add Orole Mail or Orole Drive (or add the URL as a custom connector).
2. Leave the client ID empty. Both servers advertise CIMD, so Orole OS identifies itself with its published client metadata document `https://orole.be/oauth/orole-os.json` (source: `public/oauth/orole-os.json` in `rayorole/orole-directory`). That document is a `native` public client (`token_endpoint_auth_method: none`, `authorization_code` + `refresh_token`, `code`) whose `redirect_uris` is `http://127.0.0.1/callback`; the servers' better-auth provider matches loopback redirects on any port. A configured client ID or document URL still takes priority. Order: configured client → Orole's CIMD (when advertised) → DCR → `ErrNoRegistration`.
3. Select scopes explicitly if desired; `offline_access` is added when advertised.
4. Click Authorize, authenticate in the system browser, review consent, and allow the redirect back to Orole OS.
5. Inspect discovered tool schemas and permissions before using Mail/Drive actions.

Changing the CIMD document's redirect URIs or grants changes what every CIMD server accepts from Orole OS; keep it in sync with `OroleClientMetadataURL` in `internal/mcp/oauth_flow.go`.

## Library decision

Selected official [modelcontextprotocol/go-sdk v1.8.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0), module minimum Go 1.25.0. `go version` on this workstation reported `go1.27.1 windows/amd64`; `go build ./internal/mcp/` succeeded with this dependency.

Verified source interfaces:
- `mcp.StreamableClientTransport` exposes `Endpoint`, `HTTPClient`, `OAuthHandler`, retry/event-size controls.
- `auth.OAuthHandler` has `TokenSource(context.Context) (oauth2.TokenSource,error)` and `Authorize(context.Context,*http.Request,*http.Response) error`; transport retries a 401/403 once after successful authorization.
- Client sessions support tools/resources/templates/prompts discovery, cancellation through context, and negotiated protocol/server capability diagnostics.
- Built-in AuthorizationCodeHandler supports PKCE/DCR/CIMD, including fallback on 4xx metadata responses. Our custom `auth.OAuthHandler` integrates OS credential persistence, refresh-on-401, explicit interactive initiation, diagnostics, and bounded non-JSON fallback without opening a browser from a background connection.

## Implementation and trust boundaries

- SQLite stores server registrations, non-secret auth diagnostics, tool/resource/prompt metadata cache, per-tool permissions, generic policy rules, and audit decisions. No access token, refresh token, bearer token, authorization code, PKCE verifier, or client secret is written to SQLite.
- `internal/credentials` stores a token bundle under `mcp/<serverID>`; pre-registered client secrets use `mcp/<serverID>/client-secret`. Refresh rotates and persists the bundle, including on HTTP 401. Static bearer credentials use the same protected store.
- Manager supports persistent registry, enable/remove/reconnect, exponential bounded jittered backoff, auth-required suspension, discovery cache, timeout/cancel, diagnostics.
- Live MCP initialization, request POSTs and session GET/SSE streams use the same guarded destination policy as OAuth discovery, scoped to each configured server URL. Public servers cannot redirect or resolve to private, loopback, link-local or other nonpublic addresses; every new TCP connection validates the entire DNS answer and dials only a checked numeric address. Proxy and custom TLS dial bypasses are disabled. Local trust requires an explicitly configured `localhost` or loopback/private/link-local IP, never a public hostname that happens to resolve locally; HTTP remains loopback-only. Streams have no overall HTTP client timeout, while DNS/dial and connection initialization remain bounded. Each connection closes its idle transport connections on failure or session termination.
- `policy.Engine.Check(ctx, Request)` returns allow/deny; an ask decision publishes a pending approval and blocks until resolution, timeout, or cancellation. Rules and MCP per-tool permissions are stored. Read-only server annotations are hints, not authority: generic MCP calls still ask without an explicit user rule. Known internal reads allow; writes/destructive actions ask; unclassified actions deny. Every checked decision is audited.
- Text reaching UI/approval prompts is untrusted, bounded and stripped of control/bidi/invisible content. Server instructions are not adopted. Mail bodies render as text; tool descriptions/results are data, not instructions.
- Mail/Drive adapters derive capabilities from runtime tool names plus schemas. Candidate alias mappings in adapter code and test tools are protocol fixtures, **not verified real Orole tool names**. Unmatched schemas return Unsupported; no configured server returns NotConfigured. Send/trash require Go policy approval and affirmative server output, not merely an HTTP 200.
- Automation MCP invocation uses the same checked manager, preserves event provenance, and does not claim durable MCP idempotency-key support.

## Verified versus blocked

Verified live: resource metadata; HTTP 401 challenges; resolved AS authorize/token endpoints; advertised S256, offline access, refresh, issuer parameter, CIMD; Mail and Drive registration disabled. Verified locally: official SDK package compilation on Go 1.27.1; MCP, policy, untrusted-text, Mail, Drive, and shared adapter behavior tests passed with `-race`. Coverage includes real Streamable HTTP + OAuth/DCR, refresh on 401, PKCE/resource binding, lifecycle/backoff/cancel, tools/resources/templates/prompts cache, blocking approval, SQLite secret scans, typed capabilities, server mutation confirmation, safe previews, and per-tool denials through typed adapters.

Blocked on user authorization/client registration: real server session negotiation, actual tools/resources/prompts and schemas, accepted loopback redirect rules, token issuance/refresh, Mail send/trash and Drive data operations. Local `httptest` OAuth + real SDK Streamable HTTP tests exercise those mechanics without claiming live integration success.

### Exercised live manager smoke

`OROLE_MCP_LIVE=1 go test -v -run '^TestLiveMailDiscovery$' -timeout=60s ./internal/mcp` passed. It added the actual Mail endpoint to a temporary SQLite registry, reached `auth_required`, performed discovery, and emitted these diagnostics (selected fields):

```json
{
  "status": "auth_required",
  "authorized": false,
  "toolCount": 0,
  "authorizationServerMetadataUrl": "https://mail.orole.be/api/auth/.well-known/openid-configuration",
  "issuer": "https://mail.orole.be/api/auth",
  "authorizationEndpoint": "https://mail.orole.be/api/auth/oauth2/authorize",
  "tokenEndpoint": "https://mail.orole.be/api/auth/oauth2/token",
  "registrationMethods": ["cimd"],
  "codeChallengeMethods": ["S256"],
  "hasCredentials": false
}
```

The authorization-start attempt stopped with the explicit missing-client-registration error before opening a browser. No credentials were stored. The user must configure a compatible registered client ID or hosted HTTPS metadata document, then click Authorize and consent in their browser. Live tools remain unverified.

## Desktop surfaces and typed APIs

Settings exposes registry controls, OAuth client/CIMD configuration, tools/schema/annotation inspection, per-tool permissions, resources/prompts, and diagnostics. The global approval dialog subscribes before fetching the initial queue, handles expiration, and disables remembering destructive approvals. Mail provides folder/message lists, virtualization, pagination/search, plain-text threads, and capability-gated compose/reply with review. Drive provides list/grid, breadcrumbs, search/pagination and bounded text/Markdown/JSON/CSV or inline raster previews. Dashboard widgets show real data or setup/offline states, never fixture data.

Typed desktop services: `MCP`, `Policy`, `Mail`, `Drive`. IPC groups: `mcp.servers.*`, `mcp.auth.*`, `mcp.connect`, `mcp.tools`, `mcp.catalog*`, `mcp.call`, `mcp.cancel`, `mcp.diagnostics`, `mcp.permissions.set`, `policy.pending/resolve/rules.*`, `mail.*`, `drive.*`. Static bearer tokens are accepted by `MCP.Add`/`MCP.Update` and stored only in Credential Manager. `AddParams.tokenHeader` sends the token in that header instead of `Authorization: Bearer` (Similarweb takes API keys only as `api-key: <key>`; as a bearer token it expects an OAuth JWT); the header is added only to requests for the server's own origin, so a redirect elsewhere never carries it.

### Scoped Drive handoff and discovered actions

Drive's selected-file panel exposes **Move to trash** only when a compatible
trash tool was discovered. A review dialog precedes the existing Go policy gate;
the UI updates only after the server confirms the selected file identity.

**Send to an agent** accepts complete, unchanged plain text, Markdown, JSON or CSV
snapshots up to 64 KiB. Unsafe controls, unsupported MIME types, images, remote
URLs, empty content and truncated/oversized previews cannot be shared. The
desktop sends server/file identifiers, a server-generated content digest, a
recipient revision and the user's task—not file bytes or credentials. The daemon
reads that exact server/file, rejects a changed digest, and holds the bounded
snapshot only for this direct delivery attempt (at most two minutes).

`drive.recipients` lists idle local structured sessions and true continuations
supported by the existing agent manager; terminal-only, busy, permission-blocked
and remote sessions are not offered. `drive.share` separately requires explicit
Go-side `drive.share` consent, even under wildcard allow rules; it cannot be
remembered. Its hashed resource binds the server, file, recipient revision, exact
content digest and user task. The normal agent prompt authorization still runs.
Before delivery, the daemon rechecks source assignment/configuration/schema,
Drive read permissions and sharing denials; `PromptScoped` checks the recipient
again after approvals and serializes continuation against other launches.
Successful delivery uses the real manager's prompt/continuation path, not an MCP
grant, URL, credential, mount or retained access token.

This is one-shot content disclosure, **not revocable provider access**. Expiry or
denial prevents an unsent handoff; it cannot erase content already delivered.
Agent transcripts and providers may retain sent content. Approval/audit metadata
contains no filename or raw snapshot. A failed process send may have partially
delivered content, so the UI asks the user to inspect the transcript before
manually retrying; there are no automatic retries or reusable grants.

The integrated Windows Go race suite and frontend check passed these regressions.
The focused reproduction commands (no external Mail/Drive credentials) are:

```powershell
go test -race ./internal/drive ./internal/policy ./internal/daemon -run 'TestOnlyCompleteUnchangedBoundedTextHasShareDigest|TestRequireOnce|TestDriveScopedHandoff' -count=1
go test -race ./internal/agents -run '^TestScopedPromptRevalidatesAfterAuthorization$' -count=1
```

The daemon smoke creates temporary SQLite state and an in-process official SDK
Streamable HTTP fixture on loopback with a single bounded `read_file` tool. It
drives actual policy queues and the real agent manager via a test-only adapter;
it checks sharing/agent denial without prompt side effects, exact delivery,
expired approvals, changed recipients, revoked Drive permission, stale content
and unsupported/oversized text. The separate agents test uses the existing
process-backed CLI fixture for real continuation. UI tests interact with coss
controls for capability restrictions, no-agent state, review and explicit trash
confirmation. Production OAuth/server schemas remain independently unverified.


Frontend checks passed all 115 tests, including the selected-file sharing/trash cases.
No authenticated production Mail/Drive UI state has been exercised.
