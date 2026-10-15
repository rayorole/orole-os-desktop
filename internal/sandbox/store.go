package sandbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Record is one row of table sandboxes.
type Record struct {
	BotID, ImageVersion, OverlayPath string
	Limits                           Limits
	LastUsedAt, CreatedAt            time.Time
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Get(ctx context.Context, botID string) (Record, bool, error) {
	var (
		r             Record
		limits        string
		last, created int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT bot_id, image_version, overlay_path, limits, last_used_at, created_at FROM sandboxes WHERE bot_id=?`, botID).
		Scan(&r.BotID, &r.ImageVersion, &r.OverlayPath, &limits, &last, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := json.Unmarshal([]byte(limits), &r.Limits); err != nil {
		return Record{}, false, fmt.Errorf("sandbox: limits of %s: %w", botID, err)
	}
	r.LastUsedAt, r.CreatedAt = fromMS(last), fromMS(created)
	return r, true, nil
}

// Upsert inserts r, or updates every column but created_at when the bot already has a row.
func (s *Store) Upsert(ctx context.Context, r Record) error {
	if r.BotID == "" || r.ImageVersion == "" || r.OverlayPath == "" {
		return errors.New("sandbox: a record needs a bot id, image version and overlay path")
	}
	limits, err := json.Marshal(r.Limits)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO sandboxes(bot_id, image_version, overlay_path, limits, last_used_at, created_at) VALUES (?,?,?,?,?,?)
ON CONFLICT(bot_id) DO UPDATE SET image_version=excluded.image_version, overlay_path=excluded.overlay_path,
  limits=excluded.limits, last_used_at=excluded.last_used_at`,
		r.BotID, r.ImageVersion, r.OverlayPath, string(limits), toMS(r.LastUsedAt), toMS(r.CreatedAt))
	return err
}

// Touch records the last use; a bot without a row is not an error.
func (s *Store) Touch(ctx context.Context, botID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sandboxes SET last_used_at=? WHERE bot_id=?`, toMS(at), botID)
	return err
}

func (s *Store) Delete(ctx context.Context, botID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sandboxes WHERE bot_id=?`, botID)
	return err
}

func toMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
