# Database schema

SQLite via `modernc.org/sqlite` (pure Go, no cgo). File: `<data dir>/orole.db`. The SQL in `migrations/*.sql`
is authoritative; this page summarises it. Migrations present in this tree: `0001`, `0010`, `0020`, `0030`, `0050`, the chat series `0096`-`0111`, and `0112`.

## Connection and migrations

`internal/database`: DSN pragmas `busy_timeout(5000)`, `journal_mode(WAL)`, `foreign_keys(1)`,
`synchronous(NORMAL)`, `temp_store(MEMORY)`, `cache_size(-16000)`, `journal_size_limit(64MiB)`, plus
`_txlock=immediate` (every transaction takes the write lock at BEGIN, so read-then-write transactions wait on the busy
timeout rather than failing with `SQLITE_BUSY_SNAPSHOT`); up to 4 open and 4 idle connections. `DB.Close` runs
`PRAGMA optimize` and `wal_checkpoint(TRUNCATE)` before closing. `Migrate` creates `schema_migrations(version, applied_at)`, then
applies every embedded `NNNN_name.sql` (`migrations/embed.go`) whose numeric prefix is not recorded, in order,
**each in its own transaction**. Forward-only: there are no down migrations. Different slices use different number
prefixes (0010, 0020, ...) so concurrent work does not collide; `DB.Version()` is `MAX(version)`.

## Tables

| Table | Migration | Purpose |
|---|---|---|
| `settings(key PK, value JSON, updated_at)` | 0001 | preferences |
| `machines(id, name, kind local\|remote, os, transport, address, fingerprint, created_at)` | 0001 | machine registry (remote use is planned) |
| `projects(id, name, machine_id, path, remote_url, last_opened, UNIQUE(machine_id,path))` + `worktree_root`, `created_at` | 0001, 0010 | registered repositories |
| `worktrees(id, project_id FK, name, path, branch, base_ref, is_main, created_at)` | 0010 | Orole metadata per git worktree; git is the source of truth for existence |
| `mcp_servers(id, name, url, transport, adapter, enabled, auth_kind none\|bearer\|oauth, created_at)` + `oauth_client_id`, `scopes` | 0001, 0030 | configured MCP servers (**no secrets**) |
| `mcp_permissions(server_id FK, tool, decision allow\|ask\|deny)` | 0001 | per-tool decisions |
| `mcp_auth_state(server_id PK/FK, issuer, endpoints, registration_kind, client_id, scopes, authorized_at, access_expires_at)` | 0030 | non-secret record of the last OAuth discovery |
| `mcp_cache(server_id PK/FK, fetched_at, tools, resources, prompts, ...)` | 0030 | last discovered capabilities; server-controlled, untrusted display data |
| `policy_rules(id, actor, action, resource, max_effect, decision, note, created_at, UNIQUE(...))` | 0030 | generic permission rules; most specific wins, deny > ask > allow |
| `audit_events(id, at, actor, action, target, outcome allowed\|denied\|error, detail)` + index on `at` | 0001 | audit log |
| `notifications(id, at, source, title, body, read, dismissed)` + `level`, `kind`, `target`, `ref`, `needs_you` + partial index on `ref` where `needs_you=1` | 0001, 0050, 0108 | notification center; `kind` is `approval \| failed \| finished \| reply \| message`, `target` what a click opens (`task:<id>`, `bot:<id>`, `automation:<id>`), `ref` the wait an approval reports, open while `needs_you=1`. `dismissed` rows predate 0108 and stay hidden |
| `file_roots(id, provider, path, label, created_at, UNIQUE(provider,path))` | 0050 | approved roots for the files providers; paths stored fully resolved |
| `agent_sessions(id, kind, status, ..., data JSON)`, `agent_events`, `agent_journal`, `agent_runs` | 0020 | agent runtime state and an at-most-once orchestration journal |
| `agent_sessions.data.permissionMode` | 0100 | renamed to `manual \| auto \| yolo` (`default`/empty → `manual`, `acceptEdits` → `auto`; `plan` unchanged) |
| `agent_usage(session_id FK cascade, provider, model, bucket_start, input, output, cache_read, cache_write, reasoning, reported_cost_usd, has_reported_cost)` + index on `bucket_start`; index `agent_sessions(updated_at)` | 0104 | billed tokens per session × model × 15-minute UTC bucket (`bucket_start` = Unix seconds, a multiple of 900, so each bucket lies in one local day in any zone); cost is priced at query time unless every report in the bucket carried the harness's own cost |
| `automations.created_by`; `automation_proposals(id, bot_id, name, replaces, base_version, rationale, definition, status, automation_id, created_at, decided_at)` + index `(bot_id, status)` | 0105 | who created an automation (`user` or `bot:<id>`, set on insert only); automations a bot drafted in chat, inert until the user approves the card (`pending \| approved \| rejected \| replaced`) |
| `automation_signals(bot_id FK cascade, key, label, count, days, last_day, first_at, last_at, offered_at, offers, dismissed_until)`, primary key `(bot_id, key)` | 0106 | requests a bot noticed the user repeating (same tool call on separate days) that it may offer to automate; rate-limited and silenced once answered |
| `messages.finished_at` | 0107 | Unix ms when a chat message reached a final status (`complete \| incomplete \| error \| canceled`), set once; 0 while pending/running and for messages finished before 0107 (the UI's "Thought for" duration) |
| `messages.call_id` + partial index `idx_messages_call(call_id, seq) WHERE call_id <> ''` | 0110 | the Jarvis call a spoken turn belongs to; `''` for every other message (and calls before 0110). Such messages are in the bot's model history and memory but excluded from chat paging, previews and chat events; `jarvis.calls.transcript` reads them by call id |
| `mcp_servers.token_header` | 0113 | header that carries a `bearer` server's static token: `''` sends `Authorization: Bearer <token>`, a name such as `api-key` (Similarweb) sends `<name>: <token>` instead; set when the server is added. The token stays in Credential Manager |
| `settings` key `notifications.ntfy` (`{"enabled", "server"}`), seeded off | 0114 | ntfy push master switch and server URL. The per-kind Push toggle is the `push` field of `notifications.prefs` (missing = off). The topic and access token are in Credential Manager, never here |

Tables for agents, MCP, policy and notifications exist ahead of the daemon services that use them; see
`daemon.md` for what is actually registered.

## Jarvis (migrations 0109, 0111)

| Table | Purpose |
|---|---|
| `jarvis_rules(id, bot_id FK cascade, trigger, target, repeat, urgent, quiet_override, note <= 280, expires_at, created_at)` + index on `trigger` | when a bot phones the owner: `trigger` is `task_finished \| task_failed \| automation_finished \| automation_failed \| notification \| at_time \| bot_reported`, `target` a task or automation id, `any`, a notification `kind[:source]`, `HH:MM`, or for `bot_reported` the name of a bot the rule's bot handed work to (or `any`); a rule with `repeat=0` is deleted after it fires; times are Unix ms. `bot_reported` came with 0111, which rebuilt the table (SQLite cannot widen a CHECK constraint) and kept every rule |
| `jarvis_calls(id, bot_id, direction outbound\|inbound, rule_id, reason, status, provider_call_id, conversation_id, message_id, started_at, answered_at, ended_at, est_cost_micros, created_at)` + indexes on `created_at` and `(bot_id, created_at)` | one row per phone call (status `dialing \| in_progress \| completed \| no_answer \| voicemail \| failed \| rejected`); `message_id` is the chat message holding the call card; `est_cost_micros` is an estimate (1 USD = 1,000,000) |

The global phone settings are JSON under the `jarvis` settings key and the daemon's last scheduler tick under
`jarvis.state` (neither writable through `settings.set`); per-bot Jarvis options live in the bot's config JSON. Twilio,
ElevenLabs and relay credentials are in Credential Manager, never here. See `jarvis.md`.

## Bot computers (migration 0112)

| Table | Purpose |
|---|---|
| `sandboxes(bot_id PK, image_version, overlay_path, limits JSON, last_used_at, created_at)` | one row per bot that has a computer (a local VM): the guest image version its overlay was made from, the overlay disk path, per-bot limits (`{"memoryMB","cpus","diskGB"}`, zero = default) and times in Unix ms. Run state is in memory only. No foreign key to `bots`: `sandbox.Manager.Delete` removes the overlay file and the row together. See `bot-computer.md`. |

## Secrets

Never stored here. OAuth tokens, bearer tokens and PATs live in Windows Credential Manager
(`docs/architecture/credentials.md`); `mcp_servers.auth_kind` only says which kind applies.

## Adding a migration

New file `migrations/NNNN_<slice>.sql` with a unique, higher prefix; never edit an applied file; test through
`database.Open` on a temp dir (`internal/database/database_test.go` shows the pattern). Update this page.

## Chat tables (migrations 0096-0101)

| Table | Purpose |
|---|---|
| `bots`, `messages` (+ `messages_fts`), `chat_attachments`, `conversation_summaries` | 0096: bots as contacts, one continuous conversation each (`UNIQUE(bot_id, seq)`), FTS5 over message text, content-addressed attachment references, rolling summaries |
| `memory_scopes`, `memory_entries` (+ `memory_fts`), `bot_memory_access`, `memory_consolidation`, `skill_proposals`, `knowledge_files`, `knowledge_chunks` (+ `knowledge_fts`) | 0097: scoped core/fact memory with validity windows, per-bot scope grants, consolidation cursor, inert skill proposals, knowledge indexing |
| `idx_messages_active_status` | 0101: partial index on `messages(status)` for `pending`/`running`, used by the startup `markInterrupted` |
| `llm_manifests` | 0098: declarative provider manifests with digest-based trust (same shape as `plugins`) |

Provider API keys are never stored here (`llm/<provider>/key` in Credential Manager). See `chat.md`.
