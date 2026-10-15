-- One row per bot that has (had) a computer: its image version, overlay disk and limits. Run state is in memory
-- only (internal/sandbox.Manager). No foreign key to bots: Manager.Delete removes the overlay file and the row
-- together, which a cascade could not do. Times are Unix ms.
CREATE TABLE sandboxes (
  bot_id        TEXT PRIMARY KEY,
  image_version TEXT NOT NULL,
  overlay_path  TEXT NOT NULL,
  limits        TEXT NOT NULL DEFAULT '{}',
  last_used_at  INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL
);
