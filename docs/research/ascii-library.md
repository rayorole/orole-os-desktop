# ascii.rest library

Original source review: `C:/code/projects/_upstream/ascii` (HEAD `bfd6ad8cebc2486be75cc26b4d0fe5065d5c15c9`).
Orole pins npm package `ascii.rest` 0.2.1 (`frontend/package.json`); 0.4.0 is the latest release.
Licence: MIT, Copyright (c) 2026 bas3line, for the whole repository including every piece
(https://github.com/bas3line/ascii/blob/main/LICENSE; the site's docs say it "can be used and changed in any project, free or paid"). The package is a dependency and its
MIT notice ships with it; 15 newer scenes are copied into the repository (see Catalogue and `upstream-attribution.md`).

## Piece contract (`src/types.ts`)

A piece module exports `meta` and a default function. `meta`: `name`, `category`, `note`, `cols`, `rows`, `fps`
(0 = still), optional `options`, `clock`, `palette` (up to 64 `#rrggbb`), `ground`, `cell` (1 or 2). The default
function takes options and returns `frame(t, env)`, which returns exactly `rows` lines of `cols` characters.
Coloured pieces (palette) draw on a `<canvas>`; the rest draw text into a `<pre>`.

## Catalogue

The original source checkout had 197 files (index/helpers included); README said 169/196 pieces depending on section. Categories:
scenes, shapes, space, physics, nature, creatures, objects, generative, effects, ui, data, type, logos, companies,
distros. Colour "scenes" in that checkout (13): alpine-dawn, aurora-fjord, deep-reef, desert-night, earthrise,
kyoto-dusk, marine-drive, misty-forest, night-coast, ocean-sunset, storm-plains, taj-dawn, varanasi-ghats.
Package 0.2.1 also includes [Tokyo rain](https://ascii.rest/tokyo-rain/) and
[Lantern lake](https://ascii.rest/lantern-lake/), both 200 × 100 square-cell colour scenes at 15 fps.

## Mounting and options (`src/mount.ts`)

`mount(el, piece, { fps, motion, ...pieceOptions })` returns a stop function. `fps` defaults to `meta.fps ?? 30`;
`fps: 0` draws once. A resize observer redraws canvases. Playback runs only while the element has been seen,
`document.hidden` is false, and `(motion || !prefers-reduced-motion)`: **with `prefers-reduced-motion: reduce` the
first frame is kept unless `motion: true` is passed** (the option exists for pages that already decided).

## How Orole uses it (`frontend/src/components/ascii/AsciiBackground.tsx`)

- `AsciiBand`: one `<canvas>` per window across the top of Home, cropped like `object-fit: cover` (scenes are 2:1),
  faded at the foot with a CSS mask; never one canvas per widget (`AGENTS.md`).
- `scenes.ts` is the list of 30 scenes (`SCENES`, pure data: Settings and the startup selection import it). Scenes load
  lazily by id through `scene-loader.ts#loadScene`: 15 scenes that are newer than the latest npm release
  (0.4.0 has none of them) are vendored in `components/ascii/pieces/` (MIT, `pieces/LICENSE`, see
  `upstream-attribution.md`) and imported per file (one chunk each); every other scene comes from `load[scene]()` of
  the `ascii.rest` package. One in-flight promise per scene is kept; a failed import is retried on the next request.
  The vendored scenes are volcano-night, snow-train, saturn-rise, salt-flat, prism-city, night-highway, lake-pichola,
  kite-festival, jungle-falls, jal-mahal, gangaur-ghat, edge-orbit, cloud-sea, command-city and amer-fort. All 30
  participate in random startup selection. No scene is fetched from ascii.rest at runtime.
- Settings > Customization > Background (`SceneGrid.tsx`): None plus the 30 scenes in a listbox with one tab stop,
  arrow keys/Home/End to move and Enter or Space to choose. Tiles show `previews/<scene>.png` (100 x 50, drawn at build
  time by `scripts/gen-scene-previews.mjs`, shown pixelated through `<img loading="lazy">`), so opening Settings runs
  no scene code and draws no canvas. The list is virtualised by row (`@tanstack/react-virtual`), keeping the row that
  holds the focus target mounted. None is `ascii.enabled = false` (the same as the Show background switch; the saved
  scene is kept); choosing a scene sets `ascii.scene` and turns the background on. The full scene is loaded only by
  the band, only for the scene in use.
- Orole passes `motion: true` always and applies its **own** policy first, `decideMotion`: disabled, static-only,
  paused, reduced-motion (Settings > Accessibility or the OS preference), pause when unfocused, intensity (scales
  fps, 1..30), low-power (<= 2 cores or discharging < 20%: capped at 6 fps). When the policy says do not animate
  it mounts with `fps: 0`, i.e. a still first frame. Unit tests: `motion.test.ts`.
- Decorative only: the band is `aria-hidden` and `pointer-events-none`.
- Settings > Customization > Background accepts PNG, JPEG, and WebP images up to 10 MB. Images are
  center-cropped to 2:1 and sampled to 200 × 100 RGB cells; only those samples and the filename are saved
  through the existing daemon-backed settings API (about 80 KB, below its 256 KiB per-value limit).
  The original file and its path are not retained. Browser preview uses the existing local settings storage.
- `custom-background.ts` converts those samples into a 64-colour, ordered-dithered dot piece, mounted through
  the same `ascii.rest` renderer as the built-in scenes. The picker shows the upload's converted result above the grid.
  Uploads are still images, share Home's crop/fade, opacity and intensity, and disable startup randomization.
  Replace image swaps the upload; Remove image returns to the saved built-in scene. Selecting a built-in
  scene or enabling random startup removes the upload. Invalid uploads leave the current background intact.

## Risks

Small, young, single-maintainer library; each scene is its own lazy chunk (the build emits one chunk per piece).
Pin the version and keep the motion policy in Orole's code. Scenes run on the UI thread: measured in Chromium, one
frame costs 1.2 to 8.7 ms for 29 scenes and about 21 ms for `edge-orbit` (headless, dev build), so at the default
12 fps the heaviest scene uses about a quarter of a core. Moving the generator to a worker would remove that.
Opening Settings used to load, run and screenshot every scene on the main thread (13 long tasks, 2.6 s blocked in a
production build); the thumbnails replaced that.
