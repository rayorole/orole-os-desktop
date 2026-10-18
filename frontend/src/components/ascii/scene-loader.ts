import { load as packaged, type Piece } from "ascii.rest";

export type ScenePiece = Piece;

// One lazy loader per vendored scene (`pieces/*.js`, see pieces/LICENSE): the glob makes each file its own chunk, and
// nothing is fetched until the scene is asked for.
const vendored = import.meta.glob<ScenePiece>("./pieces/*.js");
const packagedScenes = packaged as Record<string, (() => Promise<ScenePiece>) | undefined>;

// A scene is a pure generator, so one loaded module serves every mount. The module registry already caches the import;
// this also keeps a single in-flight promise per scene.
const cache = new Map<string, Promise<ScenePiece>>();

/** The scene's module, imported on first use and kept; undefined for an id that is neither vendored nor packaged. */
export function loadScene(id: string): Promise<ScenePiece> | undefined {
  const hit = cache.get(id);
  if (hit) return hit;
  const loader = vendored[`./pieces/${id}.js`] ?? packagedScenes[id];
  if (!loader) return undefined;
  const pending = loader();
  cache.set(id, pending);
  // A failed import (for example a chunk that did not arrive) may be retried on the next call.
  pending.catch(() => cache.delete(id));
  return pending;
}
