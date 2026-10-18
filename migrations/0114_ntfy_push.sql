-- ntfy push notifications. The master switch and server URL live in the settings table under
-- `notifications.ntfy`; the topic and access token are secrets and live in Credential Manager.
-- The per-kind Push toggle is a field of the existing `notifications.prefs` JSON and reads as
-- off where it is missing, so existing preferences need no rewrite. Push starts switched off.
INSERT OR IGNORE INTO settings(key, value, updated_at)
VALUES ('notifications.ntfy', '{"enabled":false,"server":"https://ntfy.sh"}', CAST(strftime('%s','now') AS INTEGER));
