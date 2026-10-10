# Dependency and integration matrix

GitHub #7. Versions below are declared direct dependencies; `go.mod`, `go.sum`, `frontend/package.json` and `frontend/bun.lock` remain authoritative. Installation uses frozen frontend resolution and the Go module checksums.

## Runtime dependencies

| Capability | Dependency | Owner / boundary | Verification and limitations |
|---|---|---|---|
| Native desktop and generated bindings | MyGo v0.3.3, mygo-runtime ^0.3.3, WebView2 | Desktop only | NSIS-installed Windows app exercised; no Electron/Tauri; [MyGo research](../research/mygo-compatibility.md) |
| UI | React ^19.2.8, TypeScript ~6.0.2, Vite ^8.3.0, Bun | React rendering | Strict type checking; native-bound actions unavailable in browser preview |
| Tokens and primitives | Tailwind ^4.3.3, Base UI ^1.8.0, local coss-compatible components | Frontend | Single token/component contract; [design system](design-system.md) |
| UI state and layouts | Zustand ^5.0.15, react-grid-layout ^2.3.0, TanStack Virtual ^3.14.13 | Client drafts; daemon persists workspace data | No separate durable GUI database |
| Background and motion | ascii.rest ^0.1.0, Motion ^14.0.0 | One Home canvas; frontend motion policy | [ASCII lifecycle and reduced motion](../research/ascii-library.md) |
| Editor and terminal display | CodeMirror 6 packages, xterm.js ^6.0.0 | Frontend only | File writes and terminal lifetime belong to daemon |
| Windows terminal host | UserExistsError/conpty v0.1.4 | Daemon | PowerShell/cmd exercised; terminals survive GUI restart, not daemon exit/reboot |
| Local IPC | Microsoft/go-winio v0.6.2 | Desktop/CLI → daemon | Current-user pipe DACL, token, protocol negotiation, bounded queues |
| SQLite | modernc.org/sqlite v1.60.1 | Daemon | Forward-only embedded migrations, WAL, no integration secret values |
| Windows secret store | danieljoos/wincred v1.2.3 | Daemon/native Go only | Windows Credential Manager; no plaintext or frontend secret fallback |
| MCP | modelcontextprotocol/go-sdk v1.8.0, x/oauth2 v0.35.0 | Daemon | Real SDK Streamable HTTP; discovery, OAuth, policy and cancellation |
| SSH/SFTP | x/crypto and pkg/sftp (versions in go.mod) | Daemon machine transport | Independently supplied host fingerprint; pinned key; external-host acceptance still required |
| Git / GitHub | Installed Git and gh executables; GitHub REST API | Daemon | No token returned to React; mutations require Go policy |
| Audio and charts | @web-kits/audio ^0.2.0; attributed Dither Kit chart source | Frontend/native notification bridge | Real samples only; native acceptance is not proof of acoustic audibility |

## Integration prerequisites and acceptance boundaries

| Integration | Required configuration | Implemented path | Outstanding external acceptance |
|---|---|---|---|
| Local Windows | WebView2; installed desktop/daemon | Authenticated pipe, ConPTY, files, projects, settings, launcher | OS-dependent toast/tray behavior must be observed, not inferred |
| GitHub | Existing gh authentication or reviewed credential setup; Git for repository operations | Repositories/issues/PRs/actions; issue-to-agent provenance and explicit publication approvals | Successful push/PR workflow requires a reviewed real change and approval; never auto-merge |
| Generic MCP | User-configured endpoint and supported auth | Discovery, tools/resources/prompts, checked invocation, diagnostics | Server-specific schemas and permissions are authoritative |
| Orole Mail | https://mail.orole.be/api/mcp; administrator-registered OAuth public client | Typed capability-derived adapter and setup/application UI | Dynamic registration rejected; authenticated catalog/data/send acceptance blocked until authorized |
| Orole Drive | https://drive.orole.be/api/mcp; administrator-registered OAuth public client | Typed capability-derived file adapter and scoped content handoff | Dynamic registration rejected; live file capabilities not claimed before discovery |
| SSH machines | Host address, independently verified host fingerprint, OS-protected authentication, approved roots/commands | Transport separate from execution backend; native and remote files/commands | Real external Windows and Linux SSH hosts not configured for acceptance |
| Coding agents | Installed CLI and that CLI's valid authentication | Capability-detected Claude/Codex/OpenCode/Gemini/custom adapters | Actual Codex turn exercised; Claude authorization expired in recorded attempt; missing tools report unavailable |
| Declarative plugins | Registered MCP server/tool plus reviewed exact manifest | Fixed actions contribute commands/widgets; trust and per-call authorization are separate | No arbitrary JavaScript host or automatic tool execution |
| Signed release/update | Maintainer-held signing key (GitHub `release` environment), public key in `mygo.json`, tag-triggered `release.yml` draft releases | MyGo updater bridge and replacement/lifecycle barriers; [releasing](../development/releasing.md) | Update channel signed; installer not Authenticode-signed; end-to-end signed upgrade not yet verified |

## Required delivery order

Research and trust boundaries precede native bootstrap. Native bootstrap, IPC, database and credentials precede persisted integrations. Terminals/files/projects precede agent worktrees; agents and checked MCP/GitHub operations precede automations and plugin actions. Packaging/security/performance acceptance uses the actual integrated app, not browser mocks.

Issues #1–68 track the original milestones. PR #69 contains the integrated work; issue closure requires evidence for the issue's own criteria and does not authorize merging the PR or releasing unsigned artifacts as signed.

## Licensing and upgrades

See [upstream attribution](../research/upstream-attribution.md) for copied paths, commits and notices, and [reuse inventory](../research/reuse-inventory.md) for reuse/reimplementation decisions. Update one dependency deliberately, regenerate bindings when bound Go contracts change, and rerun affected native/browser behavior. A package's declared compatibility is not proof of a supported Orole desktop platform.
