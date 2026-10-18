/**
 * Colour scenes shared by Home, startup selection, and the settings picker. Pure data: Settings and the startup
 * selection import this, so it must not pull in a scene. Each id has a thumbnail in `previews/<id>.png`
 * (`bun scripts/gen-scene-previews.mjs`) and loads through `scene-loader.ts`.
 */
export const SCENES: { id: string; label: string }[] = [
  { id: "night-coast", label: "Night coast" },
  { id: "aurora-fjord", label: "Aurora fjord" },
  { id: "alpine-dawn", label: "Alpine dawn" },
  { id: "deep-reef", label: "Deep reef" },
  { id: "desert-night", label: "Desert night" },
  { id: "earthrise", label: "Earthrise" },
  { id: "kyoto-dusk", label: "Kyoto dusk" },
  { id: "marine-drive", label: "Marine drive" },
  { id: "misty-forest", label: "Misty forest" },
  { id: "ocean-sunset", label: "Ocean sunset" },
  { id: "storm-plains", label: "Storm plains" },
  { id: "tokyo-rain", label: "Tokyo rain" },
  { id: "lantern-lake", label: "Lantern lake" },
  { id: "taj-dawn", label: "Taj dawn" },
  { id: "varanasi-ghats", label: "Varanasi ghats" },
  { id: "volcano-night", label: "Volcano night" },
  { id: "snow-train", label: "Snow train" },
  { id: "saturn-rise", label: "Saturn rise" },
  { id: "salt-flat", label: "Salt flat" },
  { id: "prism-city", label: "Prism city" },
  { id: "night-highway", label: "Night highway" },
  { id: "lake-pichola", label: "Lake Pichola" },
  { id: "kite-festival", label: "Kite festival" },
  { id: "jungle-falls", label: "Jungle falls" },
  { id: "jal-mahal", label: "Jal Mahal" },
  { id: "gangaur-ghat", label: "Gangaur ghat" },
  { id: "edge-orbit", label: "Edge orbit" },
  { id: "cloud-sea", label: "Cloud sea" },
  { id: "command-city", label: "Command city" },
  { id: "amer-fort", label: "Amer fort" },
];
