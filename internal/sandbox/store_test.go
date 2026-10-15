package sandbox

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/rayorole/orole-os-desktop/internal/database"
	"github.com/rayorole/orole-os-desktop/migrations"
)

// testDB opens a fresh database with every migration applied.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "t.db"), migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func TestStoreRoundTrip(t *testing.T) {
	s := NewStore(testDB(t))
	ctx := context.Background()
	if _, ok, err := s.Get(ctx, "b1"); err != nil || ok {
		t.Fatalf("Get on empty table = %v, %v", ok, err)
	}
	created := time.UnixMilli(1_700_000_000_000)
	r := Record{BotID: "b1", ImageVersion: "v1", OverlayPath: `C:\d\sandbox\bots\b1.qcow2`, Limits: Limits{MemoryMB: 4096, CPUs: 4, DiskGB: 20}, LastUsedAt: created, CreatedAt: created}
	if err := s.Upsert(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, "b1")
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if got.BotID != r.BotID || got.ImageVersion != "v1" || got.OverlayPath != r.OverlayPath || got.Limits != r.Limits ||
		!got.CreatedAt.Equal(created) || !got.LastUsedAt.Equal(created) {
		t.Fatalf("Get = %+v, want %+v", got, r)
	}

	// Upsert updates everything but created_at.
	r2 := r
	r2.ImageVersion, r2.Limits, r2.CreatedAt = "v2", DefaultLimits(), created.Add(time.Hour)
	if err := s.Upsert(ctx, r2); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "b1")
	if got.ImageVersion != "v2" || got.Limits != DefaultLimits() || !got.CreatedAt.Equal(created) {
		t.Fatalf("after second Upsert = %+v", got)
	}

	later := created.Add(2 * time.Hour)
	if err := s.Touch(ctx, "b1", later); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, "b1")
	if !got.LastUsedAt.Equal(later) {
		t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, later)
	}
	if err := s.Touch(ctx, "missing", later); err != nil {
		t.Fatalf("Touch on a missing row = %v, want nil", err)
	}

	if err := s.Delete(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get(ctx, "b1"); ok {
		t.Fatal("row still there after Delete")
	}
	if err := s.Delete(ctx, "b1"); err != nil {
		t.Fatalf("second Delete = %v, want nil", err)
	}
}

func TestStoreRejectsIncompleteRecords(t *testing.T) {
	s := NewStore(testDB(t))
	for _, r := range []Record{
		{ImageVersion: "v1", OverlayPath: "o"},
		{BotID: "b1", OverlayPath: "o"},
		{BotID: "b1", ImageVersion: "v1"},
	} {
		if err := s.Upsert(context.Background(), r); err == nil {
			t.Errorf("Upsert(%+v) = nil, want an error", r)
		}
	}
}

func TestStoreReportsCorruptLimits(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO sandboxes(bot_id,image_version,overlay_path,limits,last_used_at,created_at) VALUES('b1','v1','o','{not json',0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewStore(db).Get(context.Background(), "b1"); err == nil {
		t.Fatal("Get with corrupt limits = nil error")
	}
}

func TestStoreDefaultLimitsColumn(t *testing.T) {
	db := testDB(t)
	if _, err := db.Exec(`INSERT INTO sandboxes(bot_id,image_version,overlay_path,created_at) VALUES('b1','v1','o',5)`); err != nil {
		t.Fatal(err)
	}
	got, ok, err := NewStore(db).Get(context.Background(), "b1")
	if err != nil || !ok || got.Limits != (Limits{}) || !got.LastUsedAt.IsZero() {
		t.Fatalf("Get = %+v, %v, %v", got, ok, err)
	}
}
