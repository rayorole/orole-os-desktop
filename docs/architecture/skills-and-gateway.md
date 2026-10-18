# Skills and the MCP gateway

Skills and MCP servers are configured once in Orole and reach every model in local structured
Oh My Pi, Codex and Claude Code tasks. The daemon owns the state; harnesses see it through
one stdio MCP server named `orole`. Optional native-directory sync also exposes them to
sessions started outside Orole. Legacy terminal-only and remote sessions do not receive
automatic per-session gateway injection.

```
Skills view ──MyGo──> daemon ──> <DataDir>/skills/<name>/SKILL.md (+ resource files, .registry.json)
                         │
                         ├─ sync ──> ~/.claude/skills, ~/.agents/skills, ~/.omp/agent/skills  (marker file per skill)
                         │           + one "orole" MCP entry in ~/.claude.json, ~/.codex/config.toml, ~/.omp/agent/mcp.json
                         │
harness ──stdio MCP──> orole-daemon mcp-gateway ──authenticated pipe──> daemon (skills, MCP manager, policy)
```

## Registry (`internal/skills`)

A skill is a directory `<DataDir>/skills/<name>/` with `SKILL.md` (YAML frontmatter `name` + `description`,
then Markdown) and optional resource files. `.registry.json` beside them holds each skill's `enabled` flag and
`source` (`orole` or `imported:<harness>`); a corrupt sidecar is reported, never silently reset.

- Names are slugs (`a-z0-9` and single hyphens, ≤ 64 chars, no Windows device names). Frontmatter `name` wins
  over the name argument of `skills.save`. A description is required.
- Caps: `SKILL.md` 256 KiB, 1 MiB per resource file, 200 files and 8 MiB per skill, depth 6, 500 skills.
- Resource paths are relative, slash-separated, with no `..`, drive letters, backslashes, hidden or device-named
  components. Reads `Lstat` every component and refuse links. Writes are temp-file + rename; directories are
  swapped, never half-written. Links and hidden files are never copied or hashed.
- Disabled skills are neither synced nor served nor listed to agents.

IPC (all audited, all publish `skills.changed {action,name}`): `skills.list` (no bodies), `skills.get`,
`skills.save`, `skills.delete`, `skills.setEnabled`, `skills.discover`, `skills.import`, `skills.syncState`,
`skills.sync`, and the skills.sh hub methods `skills.hub.browse`, `.search`, `.describe`, `.preview`, `.install`.


### skills.sh discovery (`internal/skills/hub.go`, Settings > Skills > Discover)

Discover lists the public [skills.sh](https://www.skills.sh) catalogue (Most installed, Trending, search) and adds a skill to
the registry. skills.sh lists skills as GitHub `owner/repo` + skill id; the skill itself lives in that repository.

- **Listing.** skills.sh's documented API (`/api/v1`) needs a Vercel OIDC token, which a desktop app cannot have, so
  browsing calls the unauthenticated JSON endpoints the site itself uses (`/api/skills/<all-time|trending>/<page>`,
  `/api/search?q=`). They are not a published contract. Every field is validated and a format change shows as an
  error in Discover, never as wrong data. Descriptions come from each skill page's `<meta name="description">`, are cached
  for an hour and are optional: a card without one still works.
- **Install.** The repository's public archive (`github.com/<owner>/<repo>/archive/HEAD.zip`, at most 128 MiB, to a temp
  file) is read, the skill directory is found (directory named like the skill id, else the `SKILL.md` whose frontmatter
  `name` is the id) and copied with the same caps and path rules as an import: no links, no hidden files, no `..`, 200 files,
  1 MiB per file, 8 MiB per skill. It never overwrites a registry skill. Source is recorded as
  `skills.sh:<owner>/<repo>/<skill>`; private repositories are not supported.
- **Topics.** skills.sh's categories exist only as HTML pages (`/topic/<id>`, eight of them, listed in `hubTopics`); there is
  no JSON for them. `skills.hub.topic` reads the skill links from the page, validates each, and has no install counts. A layout
  change yields an empty list or an error, not wrong data.
- **Review first.** A skill is instructions an agent follows. The UI adds only from a preview that shows the full
  `SKILL.md`, the resource files and a warning for script files (`.sh`, `.ps1`, `.py`, `.js` …). Nothing from skills.sh or
  GitHub is executed by Orole. Adding does not touch harness folders; that is the separate "Update agents" step.

### Built-in skills (bots only)

Skills embedded in the daemon (`internal/skills/builtin/<name>/`, `go:embed`) are installed by `Store.EnsureBuiltin` at
daemon start with source `builtin`. They are written for Chat bots, which have no shell or file access, so they are
**never synced to harness directories, never listed in the harness prompt index, and never served by the gateway**
(`Store.EnabledForHarnesses`, `Hub.SkillList/SkillGet`). Every bot can read them with `orole_get_skill` without a grant in
`BotConfig.Skills`; the Skills view lists them as "Built in (bots)" and the enable switch turns one off for all bots.

`.registry.json` keeps `builtin: {name: content hash}` for every built-in skill ever installed. A newer shipped version
replaces the installed copy only when that copy still matches the recorded hash; a user edit is kept, a deleted skill
stays deleted, and a same-named skill from another source is never touched. Currently shipped: `skill-creator`, the
authoring guide that teaches a bot to draft a skill with the user and offer it with `propose_skill` (the in-chat Add card
and its rules are in [chat](chat.md#skill-cards-propose_skill)).

## Harness directories and sync

| Harness | Skills dir written | MCP entry | Notes |
|---|---|---|---|
| Claude Code | `~/.claude/skills` (`$CLAUDE_CONFIG_DIR/skills`) | `mcpServers.orole` in `~/.claude.json` | JSON merge |
| Codex | `~/.agents/skills` (reads `~/.codex/skills` too) | `[mcp_servers.orole]` in `$CODEX_HOME/config.toml` | managed block between `# >>> orole managed block` markers |
| Oh My Pi | `~/.omp/agent/skills` (`$PI_CODING_AGENT_DIR`) | `mcpServers.orole` in `~/.omp/agent/mcp.json` | OMP also reads the Claude/Codex locations and keeps the first same-named server |

A harness is "available" when its CLI is on `PATH` or its config directory exists. Sync never touches an
uninstalled harness.

- **Ownership.** Each synced skill directory carries `.orole-managed` (name + content hash). Orole overwrites or
  removes only directories with a valid marker. A same-named skill without it is the user's: it is left alone and
  reported in `SyncState.error` ("import it to manage it with Orole"). Importing it makes the registry copy; if the
  native copy is byte-identical, the next sync adopts it (writes the marker). A differing one stays untouched.
- **Never through links.** The target skills directory must be a real directory; a link or junction there, or in
  place of a skill, is refused. Removal uses `RemoveAll`, which unlinks without following. Discovery and import read
  a skill directory that is itself a link (skills installers create them) but never follow links inside it.
- **Gateway entry.** `command` = the running daemon executable, `args` = `["mcp-gateway"]`, `env` =
  `{OROLE_HOME: <DataDir>}`. States: `installed`, `outdated` (present, different), `missing`, `unsupported` (no
  executable known). JSON files are parsed strictly (invalid JSON, a non-object root, duplicate keys: refuse and
  report, file untouched); other keys, key order and number precision are preserved byte-for-byte per value. The first
  change to an existing file writes `<file>.orole-backup` once (the pre-Orole original). The write re-checks that the
  file did not change meanwhile (harnesses rewrite their configs) and is atomic. TOML: an unmanaged
  `mcp_servers.orole` or a lone/duplicate marker is refused; CRLF files stay CRLF.
- Sync is idempotent: an up-to-date skill or config is not rewritten.

`skills.discover` lists skills in the harness directories that the registry does not hold (Orole's own marked
copies excluded); `skills.import` copies one (with resource files, same caps) into the registry. The harness
directory is only read.

### How the Skills screen explains this

The screen shows one line, "Used by", with a dot per installed agent (up to date / needs update) and a single
"Update agents" button when any agent has drifted. The popover behind the help button explains the three facts: skills live
in Orole, every agent started from Orole reads enabled skills through the gateway, and Claude Code / Codex / Oh My Pi
additionally read a folder that "Update" fills. Counts, folders and gateway state are in that popover only.

## Gateway (`internal/gateway`, `orole-daemon mcp-gateway`)

`orole-daemon mcp-gateway` is a stdio MCP server (modelcontextprotocol/go-sdk). It connects with the same
authenticated pipe client as the CLI and hooks (`OROLE_HOME` selects the data directory,
`OROLE_GATEWAY_SESSION` the agent session). It holds no secrets and no authority.

- Tools `orole_list_skills` and `orole_get_skill {name, file?}` serve **enabled** registry skills: instructions and
  resource files (UTF-8 text, 1 MiB cap, same traversal rules).
- Every tool of every **enabled and connected** MCP server in `internal/mcp` is proxied as `<server>__<tool>` with the
  server's input/output schema and annotation hints. The prefix is a slug of the server name (≤ 24 chars, numeric
  suffix on collision); names are capped at 64 characters (hash suffix). The tool description says the output is
  untrusted.
- The tool list follows the daemon: `mcp.status`, `mcp.removed` and `skills.changed` events (plus a slow safety
  poll) trigger a re-list; changed definitions send `notifications/tools/list_changed`. Until the daemon is
  reachable only the two skill tools exist.
- The daemon caches the tool index and routing table keyed on `Manager.Generation()`, which moves exactly when a
  server is added, edited, enabled, disabled, removed, changes status or changes its catalog content. Per-tool
  permissions are not part of the index: they decorate catalogs only, are cached per server (dropped on every write
  and on removal), and the policy engine reads them straight from SQLite on each call, so a cached index never
  widens access. `mcp.status` is published only when a server's state differs from the last one published.
- Removing a server (`mcp.servers.remove`) returns as soon as the registry row is deleted: that row decides, and a
  failed delete changes nothing (the connection keeps running). Cancelling a pending sign-in, closing the session
  (an HTTP DELETE to the remote server) and deleting the stored credentials then run in the background, each
  limited to 10 s, because any of them can stall on a remote server or the OS credential store. A removed server
  publishes no further `mcp.status`. If a step fails or times out the daemon writes an `mcp.server.cleanup` audit
  row and publishes `mcp.cleanup.failed` (`{serverId, name, error}`), which the desktop shows as a warning.
- IPC: `gateway.tools` (request `{sessionId, etag?}`, answer `{etag, tools}` or `{etag, notModified: true}` when the
  caller's etag is current, so the 30 s safety poll costs no tool bodies), `gateway.call`, `gateway.skills`,
  `gateway.skill`.

### Security model

- `gateway.call` runs `MCP.InvokeChecked` — the same path as the UI's own tool calls. The policy engine decides
  (`mcp.tool.call` on `mcp/<server>/<tool>`, actor `agent:<sessionID>`, `agent:external` for a harness Orole did not
  launch); calls that need approval appear in the existing approval UI (`policy.pending`) and block until the user
  answers; policy audits every decision. Per-tool `deny` permissions and policy rules apply unchanged. The gateway
  cannot bypass them: it has no other way to reach a server.
- The server's own annotations only choose how Orole asks; they never grant permission.
- MCP results are untrusted data: sanitised and size-limited by the manager, forwarded as text with `_meta`
  `orole/untrusted: true`; binary payloads are summarised, not forwarded. Denials and errors reach the model as tool
  errors, not protocol errors.
- The pipe is per-user and token-authenticated. A caller that can reach `gateway.call` could already call `mcp.call`
  as the user, so the session id attributes calls (policy rules, audit); it is not a boundary between same-user
  processes.
- Skills are instructions the user chose; skill text is not sanitised beyond UTF-8/size checks, and the prompt index
  puts only names and one-line descriptions into a system prompt.

## Agent launches

`Manager.launch` (every start, resume and restart of a **structured, local** session) fills
`LaunchSpec.Gateway = {Command: Host.GatewayExe, Args: ["mcp-gateway"], Env: {OROLE_HOME, OROLE_GATEWAY_SESSION}}` and
`LaunchSpec.SkillsPrompt` (`Store.PromptIndex()`: names and descriptions of enabled skills plus "call
`orole_get_skill`"; at most 60 entries and 300 characters per description). `Host.GatewayExe` is the daemon's own
executable. Adapters decide how to register it (a harness-specific `--mcp-config` or equivalent) and append the
prompt to their system/developer instructions. Remote machines cannot reach the daemon pipe and get neither.

## Verification

The gateway integration tests exercise real authenticated IPC, MCP tool discovery, central
skill reads, policy denial/approval, audit attribution and server disablement. An isolated
live smoke also ran the installed OMP harness through the real daemon and stdio gateway:
it read a skill created only in the daemon registry and returned its verification phrase.
The smoke did not sync or alter the user's native harness configuration.
