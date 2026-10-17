# Orole OS design system

Acceptance reference for GitHub #7 and #26. This specifies the existing component and token contract, not a second UI framework.

## Direction and hierarchy

A compact personal desktop: quiet neutral surfaces, restrained status color, small readable typography, and a single atmospheric ASCII band on Home. The shell owns navigation and the route heading; feature views retain controls, breadcrumbs, counts and meaningful detail titles without duplicating the route title. Native caption controls retain MyGo's reserved inset.

Home combines the task composer, running-agent view and persistent widget grid. The sidebar owns workspace/project navigation; the footer owns daemon, integration, task and machine status. Notifications use a non-modal viewport-corner popover rather than a full-height sheet. Dialogs are for bounded decisions and forms, not ordinary navigation.

## One token layer

`frontend/src/index.css` imports Tailwind v4 and the bundled Inter and JetBrains Mono variable fonts. Its `@theme inline` maps semantic CSS variables to utilities. `frontend/src/themes/themes.css` supplies shared theme styles; `themes/builtin.ts` is the built-in UI/terminal palette registry. `hooks/useApplyTheme.ts` applies palette variables, `color-scheme`, the dark class and `data-density` to the document root and clears old palette overrides on changes.

| Purpose | Tokens / contract |
|---|---|
| Canvas and content | `background`, `foreground` |
| Cards and overlays | `card`, `card-foreground`, `popover`, `popover-foreground` |
| Emphasis and selection | `primary`, `primary-foreground`, `accent`, `accent-foreground` |
| Secondary content | `muted`, `muted-foreground`, `secondary`, `secondary-foreground` |
| Boundaries and focus | `border`, `input`, `ring`, `radius` and derived radii |
| Status | `success`, `warning`, `destructive`, `info` and corresponding foregrounds |
| Navigation | `sidebar`, `sidebar-foreground`, `sidebar-accent`, `sidebar-border`, `sidebar-ring` |
| Density | `row-h`, `row-pad`, `row-gap`, `side-row`; compact is the default |
| Typography | Inter for interface text; JetBrains Mono for code, terminal and technical metadata |

Built-ins: Orole Midnight, Orole Obsidian, Orole Light, Solarized Dark/Light, and Catppuccin Latte/Frappé/Macchiato/Mocha. Theme IDs, labels and terminal colors come from the same registry. Custom themes extend Midnight with the whitelist in `themes/custom.ts`: background, foreground, primary, primary-foreground, card, sidebar, success, warning, destructive, border and muted-foreground. Inputs must be a JSON object with recognized keys and validated color strings, not arbitrary CSS.

A custom theme is imported from a JSON file (Settings → Customization → Import theme, max 64 KiB) and then chosen from the theme select like any other.

## Components and interaction

Use the existing coss/Base UI primitives in `components/ui`; do not add a competing menu, dialog, tooltip or field implementation. Preserve upstream notices and record copied changes in [upstream attribution](../research/upstream-attribution.md).

- Buttons use the shared variants and sizes. Icon-only actions have accessible names.
- Menus select commands/options; nested agent controls reuse menu/submenu primitives. The reasoning slider exposes only the selected harness's supported levels.
- Tooltips use `Tooltip`, `TooltipTrigger`, `TooltipPopup`. Keyboard hints use `Kbd` and `KbdGroup`, not textual approximations of keycaps.
- Dialog fields have associated labels and descriptions. Approval dialogs display the Go-owned action, actor and resource; frontend state never grants authority.
- Project **New task** is a page, not a modal. It renders the same composer as Home (`HomeComposerView`: attachments, project/system/worktree menus, the provider/model/reasoning picker) with the page's project and prompt preselected. Both entry points share launch logic and saved provider preferences. Enter starts, Shift+Enter inserts a newline, and Ctrl/Cmd+Enter remains supported; composition and held Enter do not launch tasks.
- Harness/model discovery is cached in memory per machine and shared across composers. Reopening uses cached choices immediately and shares any in-flight request. Results older than 45 seconds refresh on the next mount without clearing the picker. Each entry records the daemon's `connectEpoch` (incremented whenever the daemon becomes reachable again); an entry from an earlier epoch is stale and a late reply for it is ignored, even if no dialog was open at the time. Offline state still blocks launching.
- The **Issues** view lists GitHub issues for projects whose `origin` is on github.com, composes `Message`/`Bubble` for the thread and the coss `Textarea` for comments, and hands an issue to an agent through the reviewed launch sheet. See [GitHub](github.md#issues-view).
- CodeMirror owns code highlighting; xterm.js owns terminal rendering. Both consume the shared appearance settings rather than independently selected palettes.
- Charts use the integrated Dither Kit chart components and real daemon samples. Unknown samples remain gaps; empty data is not synthesized.

## Component contracts

These names identify product roles; not every role is a separately exported generic
component. Reuse the concrete implementations below rather than creating aliases
solely to match the handoff's names.

| Role | Existing implementation and caller contract |
|---|---|
| AppShell / TitleBar / Sidebar | `components/shell/AppShell.tsx` accepts the active view as children; shell stores own navigation and sidebar visibility. Native window chrome is not a React imitation. |
| Panel / Widget | `components/widgets/Widget.tsx`: `title`, optional `icon`, `count`, `children`, `onRemove`, and `status` (`ready`, `loading`, `error`, `offline`, `unconfigured`). `message` and `{label,onClick}` action explain non-ready states. The header is the grid drag handle; the body scrolls independently. Feature panels otherwise compose semantic layout elements and shared surfaces; there is no second generic Panel API. |
| DataTable | `components/ui/table.tsx` exports semantic Table/Header/Body/Row/Head/Cell composition. `Table` accepts native table props and `variant: default | card`; its wrapper provides horizontal scrolling. Feature stores own sorting/filtering/selection, not the table primitive. |
| Dialogs | `components/ui/dialog.tsx` supplies Base UI open/onOpenChange composition, popup, title, description and close controls. Use confirmation-specific composition for destructive actions and await Go results before reporting success. |
| ContextMenu | `components/ui/context-menu.tsx`: Root/Trigger/Popup and item primitives. Popup accepts position/anchor and portal options while preserving Base UI keyboard/focus behavior. Context actions must still call authorized services. |
| Empty / Loading / Error | Shared Empty/Skeleton/Alert primitives and `WidgetState`; feature-level `NeedsDesktop`, `NotConnected`, `NotImplemented` in `features/shared/States.tsx` describe distinct capability states. Error states preserve actionable retry/setup information. |
| CommandPalette | Existing command registry supplies stable IDs, labels, categories and run callbacks; provider results remain asynchronous data with explicit provider boundaries. Plugin contributions use the same registry. |

## Layout and state ownership

`WidgetGrid` is a 12-column CSS grid of @dnd-kit sortables in Home's centered column (two columns below ~900px container width, one on narrow windows). Each widget is `{id,type,size,tall?,config?}` in an ordered list: the heading drags it (pointer sensor with a 6px activation distance that ignores the heading's own controls; the move handle is the keyboard activator, with Space/arrows/Escape and spoken announcements), and its menu sets the size (Small 4, Medium 6, Large 8 or Full 12 columns, optionally Tall = two rows). New widgets start at their `WidgetDef.size`. Unknown plugin widgets preserve layout and show an explicit unavailable state with removal. The daemon owns saved workspace data; the browser preview must not pretend it can persist daemon data.

Rows always end at the column's right edge: `fillRows` (in `components/widgets/frame.ts`) replays CSS grid's sparse auto-placement for the two- and twelve-column layouts and widens each widget over the free cells to its right, so a Medium + Small row (10 of 12 columns) shows a wider Small instead of a ragged edge. The saved size is unchanged; only the rendered span (`--span-two` / `--span-wide`) grows.

Home's usage widgets (Needs you, Sessions, Tokens, Spend, All time, By agent, Latest sessions in `features/home/`) read one shared `Agents.usage` reply through `useUsageStats(range)`: one in-flight request per By agent range, refreshed on session events at most every 15 s, every 60 s while the window is visible, and when the daemon reconnects. The browser preview shows them offline, never sample data. Token counts use `compactNumber` (999, 1.00K, 1.88B) and spend `costLabel` (`≥ $586.17` plus an amber "Excludes …" chip while some models have no price) from `usageFormat.ts`. Every token headline (these widgets, a task's meter in chat, the run header in the task feed) counts input + cache write + output (`TokenTotals.work`, `Session.tokens`) and leaves cache reads out; hovering it shows the billed breakdown with cost (`features/agents/TokenBreakdown.tsx`). Needs you lists waiting tasks, held MCP actions and automation approvals; the automation approval poll is shared with the sidebar badge (`useAutomationApprovals`).

Every data surface distinguishes loading, empty, error, offline, setup-required and unsupported states. Retain visible retry/setup actions where applicable. Long histories/lists scroll or virtualize; keep primary controls usable at narrow widths. Do not hide authorization errors or replace unavailable integrations with sample results.

## Motion and accessibility

`components/ascii/motion.ts` centralizes background policy: pause/static settings, OS/application reduced motion, focus/visibility, intensity and low-power limits. Render one decorative, non-interactive canvas, never one per widget. Motion must not convey essential information. Maintain keyboard access, visible focus, labeled inputs and readable semantic foreground/background pairs; verify actual surfaces rather than inferring accessibility from primitive selection.

## Verification

Existing evidence and component ownership are recorded in [component structure](component-structure.md). `bun run --cwd frontend check` covers type checking, lint and unit behavior; `bun run --cwd frontend test:e2e` is the browser suite. Installed WebView2 acceptance is separate from preview-mode checks. Unverified platform/accessibility cases must stay explicitly unverified; this specification does not certify a complete WCAG audit.
