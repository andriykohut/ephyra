package store

import (
	"context"
	"database/sql"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// applyMigrationsUpTo runs the embedded migrations whose version prefix is <=
// maxVersion, in order, against db. It duplicates just enough of (*Store).migrate
// to let a test pause partway through the migration chain and inject
// pre-upgrade state before applying a specific later migration.
func applyMigrationsUpTo(t *testing.T, ctx context.Context, db *sql.DB, maxVersion int) {
	t.Helper()
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		t.Fatal(err)
	}
	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		applied[v] = true
	}
	rows.Close()
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			t.Fatal(err)
		}
		if v > maxVersion || applied[v] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			v, time.Now().UTC().Format(time.RFC3339)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigration0005ClearsRefreshMetaMtimeOnUpgrade simulates an existing
// install upgrading straight into migration 0005: refresh_meta already has
// "ok" rows recording the last-seen mtimes (matching CLAUDE.md's fear that a
// quiet stretch since last refresh means both jobs' mtime-skip would
// otherwise leave the freshly-dropped-and-recreated agg_* tables empty
// forever). After 0005 applies, both jobs' recorded mtimes must be cleared so
// the very next refresh does a full run instead of skipping.
func TestMigration0005ClearsRefreshMetaMtimeOnUpgrade(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/s.db"
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Pre-upgrade: migrations 1-4 only.
	applyMigrationsUpTo(t, ctx, db, 4)

	// Pre-existing refresh_meta rows, as if both jobs ran successfully before
	// the upgrade and the underlying source files haven't changed since.
	mt := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Format(tsLayout)
	for _, job := range []string{"library", "watch"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO refresh_meta (job, last_run_at, source_mtime, duration_ms, ok, skipped, plugin_available, error)
			VALUES (?, ?, ?, 1, 1, 0, 1, '')`,
			job, mt, mt,
		); err != nil {
			t.Fatal(err)
		}
	}
	// A non-empty spine, as a real install would have.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO playback_events (dedup_hash, user_id, item_id, item_type, method, at, play_duration_sec)
		VALUES ('h1', 'u1', 'i1', 'movie', 'DirectPlay', ?, 100)`, mt,
	); err != nil {
		t.Fatal(err)
	}

	// The upgrade: apply migration 0005.
	applyMigrationsUpTo(t, ctx, db, 5)

	s := &Store{db: db}
	for _, job := range []string{"library", "watch"} {
		m, ok, err := s.GetRefreshMeta(ctx, job)
		if err != nil || !ok {
			t.Fatalf("job %s: ok=%v err=%v", job, ok, err)
		}
		if !m.SourceMTime.IsZero() {
			t.Errorf("job %s: SourceMTime = %v, want zero after migration 0005", job, m.SourceMTime)
		}
	}

	// The spine row survived the ALTER TABLE, defaulted to 'Unknown'.
	var lib string
	if err := db.QueryRowContext(ctx, `SELECT library FROM playback_events WHERE item_id='i1'`).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if lib != "Unknown" {
		t.Errorf("spine row library = %q, want Unknown default", lib)
	}
}

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
	if n != 6 {
		t.Fatalf("want 6 applied migrations, got %d", n)
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
	if n != 6 {
		t.Fatalf("want 6 migrations applied, got %d", n)
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
	if v != 6 {
		t.Fatalf("schema_migrations max version = %d, want 6", v)
	}

	var idx int
	st.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='playback_events'
		 AND sql LIKE '%UNIQUE%' AND sql LIKE '%at, user_id, item_id%'`,
	).Scan(&idx)
	if idx == 0 {
		t.Fatal("expected a unique index on playback_events (at, user_id, item_id)")
	}
}

func TestMigrations_ApplyCleanly(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatalf("open (runs all migrations): %v", err)
	}
	defer st.Close()

	for _, table := range []string{
		"dim_library", "agg_totals", "agg_disk", "agg_distribution",
		"agg_library_growth", "agg_library_tag_pairs", "agg_watch_heatmap",
		"agg_profile_summary", "agg_profile_completion", "agg_profile_abandoned",
		"agg_profile_rewatch", "agg_profile_binge", "agg_profile_taste",
		"agg_taste_baseline", "agg_profile_tag_overlap",
	} {
		var n int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, table,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migrations", table)
		}
	}

	var col int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('playback_events') WHERE name = 'library'`,
	).Scan(&col); err != nil {
		t.Fatal(err)
	}
	if col != 1 {
		t.Error("playback_events.library column missing")
	}
}
