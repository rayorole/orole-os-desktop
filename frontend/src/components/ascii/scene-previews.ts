// Thumbnails drawn at build time by `bun scripts/gen-scene-previews.mjs` (100 x 50 PNG, about 8 KiB each). `no-inline`
// keeps them as files the browser fetches when an <img> is shown, never base64 inside a script.
const urls = import.meta.glob<string>("./previews/*.png", { eager: true, query: "?no-inline", import: "default" });

/** URL of a scene's thumbnail; undefined for an id the generator has not drawn. */
export const scenePreview = (id: string): string | undefined => urls[`./previews/${id}.png`];
