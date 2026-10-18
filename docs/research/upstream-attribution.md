
## Copied files (register)
Source: https://github.com/cosscom/shipyard @ c809c5eccfc31a386be6bb82b8b0839eaf7ec89b, MIT, Copyright (c) 2026 Sean Brydon.
| Upstream path | Orole path | Modification |
|---|---|---|
| app/src/components/ui/*.tsx | frontend/src/components/ui/*.tsx | import stubs for lib/focus-home and lib/platform; tooltip root uses coss/Base UI directly instead of Shipyard's global keyboard-focus timer; scroll-area scrollbar 4px wide with a 2px inset |
| app/src/lib/utils.ts | frontend/src/lib/utils.ts | none |
| app/src/index.css (tokens, density, base layer) | frontend/src/index.css | removed plugin @source, Tauri/sidebar-resize/worktree-tone rules; Orole additions appended |
| app/src/themes/builtin.ts (colours) | frontend/src/themes/builtin.ts | renamed Orole Midnight/Obsidian/Light; expanded to a shared UI/terminal registry with Solarized and Catppuccin palettes |
| shadcn/ui registry `message`, `bubble` (base-mira, installed with `shadcn@latest add`, MIT, Copyright (c) shadcn) | frontend/src/components/ui/message.tsx, bubble.tsx | `cn` import rewritten to `@/lib/utils` (the registry emitted a bare `cn` specifier) |
| HextaUI Prompt Input block, `https://hextaui.com/r/pro/prompt-input.json` (fetched 2026-10-12; `prompt-input.tsx`, `prompt-controls.tsx`, `effort-fill.tsx`), HextaUI Pro License (https://hextaui.com/legal/license), Copyright (c) Preet Suthar | frontend/src/components/blocks/prompt-input/{prompt-input.tsx,prompt-controls.tsx,effort-fill.tsx,context.ts,effort.ts} | agent-harness subset only (inset, body, textarea, tools, actions, submit, footer, select, effort slider with WebGL fill, rolling label); controlled by the caller instead of owning text/files/queue; ported to local Base UI Menu/Popover/Tooltip and lucide icons; dropped dictation, @ and / menus, queue, drafts, context ring, haptics, size morphing and the Sessions menu; effort thumb easing and shake use local tokens; the body uses Orole's shared composer frame (`composerFrame` in `assistant-ui/elements/surfaces.ts`) instead of its inset ring; file drop and paste now use the shared `lib/useFileDrop.ts` and `lib/attachmentFiles.ts` (a paste of files and no text is taken over, any text paste is left alone) |
| `@lobehub/icons-static-svg` 1.x `icons/github.svg`, `icons/apple.svg` (MIT) | frontend/src/assets/logos/github.svg, apple.svg | removed fixed `width`/`height`/`<title>` so CSS sizes them |
| `@ant-design/icons-svg` 4.6.0 `inline-svg/filled/windows.svg`, `inline-svg/outlined/linux.svg` (MIT) | frontend/src/assets/logos/windows.svg, linux.svg | added `fill="currentColor"`. Used for the New task page's repository and host badges; GitHub, Apple, Windows and Linux are trademarks of their owners and appear only to identify them |

Theme palette references: [Solarized](https://ethanschoonover.com/solarized/) and
[Catppuccin palette v1.8.0](https://github.com/catppuccin/palette/tree/v1.8.0).
Orole's semantic-token mappings are original; no third-party theme renderer is embedded.
Keyboard and tooltip compositions follow the [coss Kbd](https://coss.com/ui/docs/components/kbd)
and [coss Tooltip](https://coss.com/ui/docs/components/tooltip) contracts using the
already attributed local primitives. The custom `components/tip.tsx` layer was removed.

### Bot computer: QEMU and the guest image (downloaded at runtime, not in the repository)

Nothing here is committed to the repository. The daemon downloads both from this repository's GitHub releases and
pins every file by size and SHA-256 in `internal/sandbox/pins.go`. The releases `qemu-v20260811` and `guest-v0.1.3` are published (prereleases).

| Upstream | Orole artifact | Modification |
|---|---|---|
| QEMU for Windows, `qemu-w64-setup-20260811.exe` from https://qemu.weilnetz.de/w64/ (SHA-512 `5bcf9eed634e8575a37b74f445af41a2fe4106da512d0c30c368301d4c105037fdfab40a5287367a28a957624cddebbc8c07e16c88ab6634f554cdf3d16bf543`), GPL-2.0 (QEMU) and the licences of its bundled DLLs | release asset `qemu-v20260811/qemu-20260811-win64.zip` | none: a subset of the installer's files (`qemu-system-x86_64.exe`, `qemu-img.exe`, DLLs, `share/*.bin`, `share/*.rom`, the `COPYING*`, `LICENSE*` and `README*` files) repackaged by `.github/workflows/qemu-asset.yml`; the zip includes `SOURCE.txt` |
| NixOS / nixpkgs revision `55bfb4d7437bd4e09ab7cba58a43bd68eb310044` (`guest/flake.lock`; nixpkgs is MIT, packages keep their own licences: Linux GPL-2.0, Chromium BSD-3-Clause and others, cage MIT, wayvnc ISC) | release assets `guest-v0.1.1/{vmlinuz,initrd,cmdline,base.qcow2,SHA256SUMS}` built from `guest/` | configuration only (`guest/configuration.nix`, `guest/orole-guest.nix`); no package sources modified. `orole-guest` is Orole's own code |

**GPL source availability.** QEMU (GPL-2.0) and the guest image (Linux and other GPL packages) are distributed as
binaries, and their corresponding source is attached to the same releases, each with a `SOURCES.md` manifest
(component, version, licence, asset, upstream URL, SHA-256):

- `qemu-v20260811`: `qemu-src-e470268ff4.tar.xz` is the tree the binaries report (`v11.1.0-12130-ge470268ff4`, commit
  `e470268ff446ec56728215a987ed166d5ed8fb64` of the installer maintainer's https://repo.or.cz/qemu/ar7.git, not on
  gitlab.com/qemu-project/qemu) with its `roms/` firmware submodules and meson subprojects. Every bundled DLL was
  matched byte for byte against MSYS2 `mingw64` binary packages, and the MSYS2 source package of each copyleft one
  (GPL, LGPL, AGPL, MPL, dual licences included) is attached; `qemu-third-party-licenses.zip` holds the licence texts.
  Two DLLs could not be matched exactly: `libssp-0.dll` (MSYS2 gcc 13.1.0-3, no longer on repo.msys2.org; GCC 13.1.0
  tarball plus the MSYS2 recipe at that revision attached) and `brlapi-0.8.dll` (built by the installer maintainer,
  not an MSYS2 package; BRLTTY 6.4, the closest determinable release, attached). Built by
  `.github/workflows/qemu-sources.yml` (tag `sources-qemu-v20260811`).
- `guest-v0.1.3`: the sources of every copyleft (or licence-unknown) package in the image's runtime closure, by
  nixpkgs `meta.license` at revision `55bfb4d7437bd4e09ab7cba58a43bd68eb310044`, in `guest-v0.1.3-sources-partNN.tar.zst`
  parts, plus that nixpkgs tree and this repository at the tag (flake files and the `orole-guest` source). Permissive
  packages (Chromium and others) are listed with their upstream URL only. Built by
  `.github/workflows/guest-sources.yml` (tag `sources-guest-v0.1.3`), which checks that the evaluated system is the
  one in the release's `cmdline`.

A new `qemu-v*` or `guest-v*` release needs the matching `sources-*` tag pushed once it is published.

### DiceBear bot avatars

`@dicebear/core` and `@dicebear/styles` (npm, MIT) generate the bot avatars in `frontend/src/lib/botAvatar.ts`
from the `gaze` style. Gaze by DiceBear is licensed under CC0 1.0
([license](https://creativecommons.org/publicdomain/zero/1.0/)). Nothing is copied into the repository; avatars
are generated locally and no avatar is fetched from the network.

### Dictation engine and models (downloaded at runtime, not in the repository)

Nothing is copied into the repository; the daemon downloads these on the user's request and pins each file by URL,
size and SHA-256 in `internal/dictation/catalog.go`. The structs in `internal/dictation/engine` mirror the C API header
of sherpa-onnx v1.13.8 (`sherpa-onnx/c-api/c-api.h`); no code is copied from it.

| Artifact | Source | Licence |
|---|---|---|
| sherpa-onnx v1.13.8 C API + ONNX Runtime DLLs (`sherpa-onnx-v1.13.8-win-x64-shared-MT-Release-no-tts-lib.tar.bz2`) | https://github.com/k2-fsa/sherpa-onnx/releases/tag/v1.13.8 | Apache-2.0 (sherpa-onnx), MIT (ONNX Runtime) |
| Parakeet TDT 0.6B v3, int8 ONNX export | https://huggingface.co/csukuangfj/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8 @ 2bda32ec70b097a55adaa07d9a7173915b43cc78, from https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3 | CC-BY-4.0, NVIDIA (credited on the Voice settings page) |
| Whisper large-v3 turbo, int8 ONNX export | https://huggingface.co/csukuangfj/sherpa-onnx-whisper-turbo @ 2ca6ff69fc878651b770880507669577ac41c2ff, from https://huggingface.co/openai/whisper-large-v3-turbo | MIT, OpenAI |

### Dither Kit charts

Only the official `area-chart` registry component and its shared chart core were
installed with `@dither-kit/cli` 0.1.1. No Dither Kit buttons, avatars, or gradients
are used. Source: [Boring-Software-Inc/dither-kit](https://github.com/Boring-Software-Inc/dither-kit/commit/1e7faee9aa252e499651e6736ed65f7a07d9a6bd),
commit `1e7faee9aa252e499651e6736ed65f7a07d9a6bd`, author ripgrim / Boring-Software-Inc.
The `area-chart` and `core` registry versions are 0.1.0; installed files and source
hashes are recorded in `frontend/dither-kit.json`.

Copied paths: `frontend/src/components/dither-kit/*`, from the
[`area-chart`](https://tripwire.sh/r/area-chart.json) and
[`core`](https://tripwire.sh/r/core.json) registry payloads. Local changes:
`cartesian-canvas.tsx` paints once per data/context change when animation is
disabled, without an idle animation loop; `area.tsx` uses Vite's
`import.meta.env.DEV` guard. Other transformations are the official installer's
normal source transforms.

Upstream package metadata declares MIT, but the inspected repository contains no
standalone LICENSE text. This records the declaration, not a claim that an
upstream license notice was supplied. Retain supplied source notices.

### Oh My Pi logo

Source: [can1357/oh-my-pi](https://github.com/can1357/oh-my-pi/tree/cde91bb38674d365e8c3916d05d2292273bb8554),
commit `cde91bb38674d365e8c3916d05d2292273bb8554`.
The upstream [`assets/icon.svg`](https://github.com/can1357/oh-my-pi/blob/cde91bb38674d365e8c3916d05d2292273bb8554/assets/icon.svg)
(Git blob `f1ccf2a08a90ec4d7f6bb947dc62c5e995114422`) is bundled at
`frontend/src/assets/oh-my-pi.svg` and used by `features/agents/ProviderLogo.tsx`
for the `omp` provider across selectors and the sidebar.

Local changes: the three pale `#fafafa` foreground fills use `currentColor`
to remain visible in light and dark themes; a provenance comment was added.
Geometry, viewBox, orange accents, connector details, and original comments
are retained. The SVG contains only static rectangles and circles, with no
scripts, event handlers, external references, fonts, or runtime network requests.
The logo remains its owner's brand mark; no trademark rights are asserted.

License notice from the pinned upstream
[`LICENSE`](https://github.com/can1357/oh-my-pi/blob/cde91bb38674d365e8c3916d05d2292273bb8554/LICENSE):

```text
MIT License

Copyright (c) 2025 Mario Zechner
Copyright (c) 2025-2026 Can Bölük
Copyright (c) 2026 Stencil Labs, Inc.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### assistant-ui composer attachments

Source: [assistant-ui/assistant-ui](https://github.com/assistant-ui/assistant-ui/tree/9201447608271c64a8a9ca66c69e81d27a1d9fab),
commit `9201447608271c64a8a9ca66c69e81d27a1d9fab`,
`packages/ui/src/components/react/assistant-ui/elements/composer.tsx`.
The standalone `ComposerAttachments` and `ComposerAttachmentChip` subset is adapted
at `frontend/src/components/assistant-ui/elements/composer-attachments.tsx`, following
the [requested attachment element](https://www.assistant-ui.com/elements/composer-attachments).

Changes: coss buttons and semantic theme tokens replace catalog surface helpers;
pending/failed chips can be removed; indeterminate IPC uploads show a spinner rather
than fabricated percentage progress. The element itself imports no assistant-ui runtime; no cloud
storage or sample files were introduced. The full MIT notice, copyright (c) 2026 AgentbaseAI Inc., is
retained in the source header.

### assistant-ui message pair

Source: [assistant-ui/assistant-ui](https://github.com/assistant-ui/assistant-ui/tree/9201447608271c64a8a9ca66c69e81d27a1d9fab),
commit `9201447608271c64a8a9ca66c69e81d27a1d9fab`, the runtime composition of the
[message pair element](https://www.assistant-ui.com/elements/message-pair)
(`MessagePrimitive.Root` per message plus an `ActionBarPrimitive` row with
`autohide="not-last"`, so the newest reply keeps its actions visible).
Adapted at `frontend/src/components/assistant-ui/elements/message-pair.tsx`.

Changes: coss buttons and semantic theme tokens replace the catalog `paper` and `ghostButton`
surface helpers; keyboard focus inside a message counts as hover so older replies' actions are
reachable without a pointer; Regenerate is only rendered for the last reply (the only one the
daemon can regenerate) and Copy is supplied by the caller so reports copy too. The standalone
word-reveal simulation is not shipped: replies only ever stream from the daemon. The full MIT
notice, copyright (c) 2026 AgentbaseAI Inc., is retained in the source header.

### assistant-ui structured and search elements

Source: [assistant-ui/assistant-ui](https://github.com/assistant-ui/assistant-ui/tree/9201447608271c64a8a9ca66c69e81d27a1d9fab),
commit `9201447608271c64a8a9ca66c69e81d27a1d9fab`, `packages/ui/src/components/react/assistant-ui/`.

| Upstream path (`elements/` unless noted) | Orole path (`frontend/src/components/assistant-ui/`) | Modification |
|---|---|---|
| `directive-text.tsx` | `elements/directive-text.tsx` | added `renderIcon` (a chip's leading visual from the segment, used for bot avatars) and `chipClassName` options |
| `conversation-search.tsx` | `elements/conversation-search.tsx` | none |
| `data-table.tsx` | `elements/data-table.tsx` | chat-bubble surface; selectable; header strip for the caption and an `actions` slot; horizontal scroll instead of clipping; short columns stay on one line and prose columns get a minimum width; row separators and tighter padding; exported `tableClipboard` (tab-separated text + HTML table) |
| `chart.tsx`, `diagram.tsx`, `flow-graph.tsx`, `math-block.tsx`, `spec-sheet.tsx`, `comparison-card.tsx`, `timeline.tsx`, `job-progress.tsx` | `elements/<same name>.tsx` | chat-bubble surface (`bubble`) instead of the bordered card (`paper`) |
| `../utils/range.ts`, `../utils/href.ts` | `utils/range.ts`, `utils/href.ts` | none |

A source header was added to each element; `surfaces.ts` gained named re-exports (`paper`, `bubble`, `field`, `mono`,
`ghostButton`) and the borderless `bubble` surface that everything a bot shows in chat uses. The full MIT notice, copyright (c) 2026 AgentbaseAI Inc.,
is retained in each source header.

The chat view also depends on the MIT-licensed [`@assistant-ui/react`](https://github.com/assistant-ui/assistant-ui)
package (`useExternalStoreRuntime`, thread, composer, message and action bar primitives) as an
unmodified npm dependency; nothing from it is copied.

`elements/permission-grant.tsx`, `elements/image-generation.tsx`, `elements/todo-list.tsx`,
`elements/subagent-list.tsx` and `elements/canvas-split.tsx` are not copied: they are
written in Orole's own code from the published element documentation (props, anatomy, lifecycle table) at
`https://www.assistant-ui.com/elements/{permission-grant,image-generation,todo-list,subagent-list,canvas-split}.md`.
Orole deviations: `PermissionGrant` is adapted to a single daemon request
(`once`/`denied` only unless the request carries more, a `blocked` reason, a details disclosure), `ImageGeneration` omits the
fixed "1024 × 1024" label. `TodoList` keeps the documented counting
rules (failed counts toward the denominator only, cancelled toward neither) and the `maxVisible` fold, with the entrance
animation done in CSS (`tw-animate-css`). `SubagentList` replaces the documented `completedCount`/`progress` pair by a
per-row `status` (`running`/`needs-you`/`done`/`failed`) with an indeterminate shimmer bar while running, adds an `activity`
line, a `ReactNode` model slot (Orole draws the harness badge there) and clickable rows (`onSelect`, `selectedIndex`),
and renders as a `ul`. `CanvasSplitBody` takes any children (Orole renders the chat's markdown there) and the header's
saved/writing state is a text label with an icon.

### beui reasoning range slider

Source: [starc007/ui-components](https://github.com/starc007/ui-components/blob/cbc23255f962098a93a2c103d39e11f93b61d059/components/motion/range-slider.tsx),
commit `cbc23255f962098a93a2c103d39e11f93b61d059`, distributed through
[`range-slider.json`](https://beui.dev/r/range-slider.json).
Copied/adapted path: `frontend/src/components/ui/range-slider.tsx`.

Changes: a native range input supplies pointer/keyboard/accessibility behavior;
percentage positioning removes the shared gesture hook and ResizeObserver; the
rounded thumb and semantic color tokens fit the requested composer reference.
Tick geometry, spring-driven fill/handle and reduced-motion support are retained.
The full MIT notice, copyright (c) 2026 Saurabh Chauhan, is retained in the source.

### BeUI image-generation presentation

Source: [starc007/ui-components](https://github.com/starc007/ui-components/tree/cbc23255f962098a93a2c103d39e11f93b61d059),
commit `cbc23255f962098a93a2c103d39e11f93b61d059`,
[`components/agents/image-generation.tsx`](https://raw.githubusercontent.com/starc007/ui-components/cbc23255f962098a93a2c103d39e11f93b61d059/components/agents/image-generation.tsx).
Adapted path: `frontend/src/features/tasks/TaskImages.tsx`. The image card kept the
stable status/media/footer composition; Orole connects it to actual provider events and
daemon-verified raster artifacts. Demo timers, dithered synthetic pixels, unsupported
resolution/refinement stages, and regeneration controls were not copied. While a generation
runs, the card now renders the assistant-ui `ImageGeneration` element (see above) instead of
the former spinner placeholder; the notice below is retained for the composition that remains.

The upstream repository's pinned [`LICENSE`](https://raw.githubusercontent.com/starc007/ui-components/cbc23255f962098a93a2c103d39e11f93b61d059/LICENSE)
declares MIT, Copyright (c) 2026 Saurabh Chauhan. The full notice:

```text
MIT License

Copyright (c) 2026 Saurabh Chauhan

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Dependencies used without copying source
| Project | Version | Licence | Use |
|---|---|---|---|
| ascii.rest (https://github.com/bas3line/ascii @ bfd6ad8cebc2486be75cc26b4d0fe5065d5c15c9) | npm `^0.1.0` | MIT, (c) 2026 bas3line | animated scenes in `frontend/src/components/ascii/AsciiBackground.tsx` |
| MyGo (https://github.com/egoist/mygo) | v0.3.3 | see upstream `LICENSE` | desktop shell, bindings, packaging (`cmd/desktop`, `mygo.json`) |
| Microsoft/go-winio, UserExistsError/conpty, danieljoos/wincred, modernc.org/sqlite, google/uuid | see `go.mod` | upstream licences | named pipes, ConPTY, Credential Manager, SQLite, ids |
| @web-kits/audio (https://audio.raphaelsalaja.com/, https://github.com/raphaelsalaja/audio) | npm `0.2.0` | MIT, (c) 2026 Raphael Salaja | shared synthesized UI and in-app notification engine in `frontend/src/lib/audio.ts`; original cue definitions, no copied samples |
| @novnc/novnc (https://github.com/novnc/noVNC) | npm `1.7.0` (exact) | MPL-2.0 (core); vendored `pako` MIT, see the package's `LICENSE.txt` | the bot computer's screen viewer: RFB over `frontend/src/features/computer/screenChannel.ts` (Orole's own adapter). Bundled unmodified; MPL-2.0 source for these files is the upstream tag `v1.7.0` |
| @lobehub/icons-static-svg (https://github.com/lobehub/lobe-icons) | npm `1.95.1` | MIT (package declaration), LobeHub | Four unmodified package SVG imports (Claude, OpenAI, Gemini, OpenCode) in `features/agents/ProviderLogo.tsx`; no additional UI framework or remotely loaded logos. Brand marks remain their owners' trademarks. |

### Lobe Icons license notice

Source: https://raw.githubusercontent.com/lobehub/lobe-icons/master/LICENSE

```text
MIT License

Copyright (c) 2023 LobeHub

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## Original work
`resources/icon.png` and `cmd/desktop/window-icon.png` are drawn programmatically by `scripts/genicon` (original mark, no upstream artwork).

`frontend/src/features/chat/ComposerMenu.tsx` (the composer's `@`/`/` menu), the composer frame tokens in
`elements/surfaces.ts` and `frontend/src/features/chat/parts/ThinkingPanel.tsx` are Orole's own code. Their layout follows
the public demos of the AI SDK Agents "Mention menu", "Slash command menu" and "Thinking state" components
(https://www.aisdkagents.com/ai-components/); none of that (paid) source was copied.

`elements/agent-run-tree.tsx`, `elements/tool-call-card.tsx` and `elements/terminal-result.tsx` (the task feed's run tree,
tool call rows and shell command card) are Orole's own code. Their layout follows the public demos of the AI SDK Agents
"Agent run tree", "Tool call card" and "Terminal result" components (https://www.aisdkagents.com/ai-components/); none of
that (paid) source was copied.

The session rail beside a code task (`frontend/src/features/tasks/rail/`) and the shared `ContextWindow` in
`features/tasks/ComposerContext.tsx` are Orole's own code. Their layout follows the public demos of the AI SDK Agents
"Context window", "MCP server list" and "Agent list" components (https://www.aisdkagents.com/ai-components/); none of
that (paid) source was copied.
