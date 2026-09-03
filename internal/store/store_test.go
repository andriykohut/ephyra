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
	if n != 4 {
		t.Fatalf("want 4 applied migrations, got %d", n)
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

func TestMigration0002Redefinitions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, table := range []string{"dim_user", "agg_played_core"} {
		if _, err := s.DB().ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err != nil {
			t.Errorf("%s missing: %v", table, err)
		}
	}
	if _, err := s.DB().ExecContext(ctx,
		`SELECT series_id, series_name FROM watch_events_daily LIMIT 1`); err != nil {
		t.Errorf("watch_events_daily.series_* missing: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`SELECT user_id FROM agg_watch_heatmap LIMIT 1`); err != nil {
		t.Errorf("agg_watch_heatmap.user_id missing: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`SELECT scope, episodes FROM agg_cleanup LIMIT 1`); err != nil {
		t.Errorf("agg_cleanup.scope/episodes missing: %v", err)
	}
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("want 4 migrations applied, got %d", n)
	}
}

func TestMigrate_0003_ProfileTables(t *testing.T) {
	st, err := Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	want := []string{
		"playback_events", "agg_profile_summary", "agg_profile_completion",
		"agg_profile_abandoned", "agg_profile_rewatch", "agg_profile_binge",
		"agg_profile_taste", "agg_taste_baseline",
	}
	for _, name := range want {
		var got string
		err := st.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&got)
		if err != nil {
			t.Fatalf("table %q missing: %v", name, err)
		}
	}

	var v int
	if err := st.DB().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 4 {
		t.Fatalf("schema_migrations max version = %d, want 4", v)
	}

	var idx int
	st.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='playback_events' AND sql LIKE '%dedup_hash%'`,
	).Scan(&idx)
	if idx == 0 {
		t.Fatal("expected a unique index on playback_events.dedup_hash")
	}
}
