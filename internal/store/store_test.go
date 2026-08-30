package store

import (
	"context"
	"testing"
	"time"
)

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/s.db"

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s1.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 applied migration, got %d", n)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path) // reopen: must not error, must not re-apply
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	for _, table := range []string{
		"refresh_meta", "agg_totals", "agg_disk", "agg_distribution",
		"agg_library_growth", "watch_events_daily", "agg_watch_heatmap", "agg_cleanup",
	} {
		if _, err := s2.DB().ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err != nil {
			t.Errorf("table %s not usable: %v", table, err)
		}
	}
}

func TestRefreshMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, ok, err := s.GetRefreshMeta(ctx, "library"); err != nil || ok {
		t.Fatalf("expected no row, got ok=%v err=%v", ok, err)
	}

	mt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	in := RefreshMeta{
		Job: "library", LastRunAt: mt, SourceMTime: mt.Add(-time.Hour),
		DurationMS: 1234, OK: true, Skipped: false, PluginAvailable: false,
	}
	if err := s.SetRefreshMeta(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetRefreshMeta(ctx, "library")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.LastRunAt.Equal(in.LastRunAt) || !got.SourceMTime.Equal(in.SourceMTime) || got.DurationMS != 1234 || !got.OK {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	in.DurationMS = 5 // upsert
	if err := s.SetRefreshMeta(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetRefreshMeta(ctx, "library")
	if got.DurationMS != 5 {
		t.Fatalf("upsert failed, duration=%d", got.DurationMS)
	}
}
