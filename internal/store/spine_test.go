package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ev(at time.Time, user, item, typ string, dur int64) source.PlaybackEvent {
	return source.PlaybackEvent{
		At: at, UserID: user, ItemID: item, ItemType: typ,
		Method: "DirectPlay", PlayDurationSec: dur,
		ItemName:       item + "-name",
		ItemRuntimeSec: 6000, ItemYear: 2001, ItemGenres: []string{"Comedy", "Drama"},
	}
}

func TestAppendPlaybackEvents_Dedup(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)

	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e, e}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 row after 3 appends of the same event, got %d", n)
	}
}

func TestAppendPlaybackEvents_RefreshesEnrichment(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	base := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "e9", "episode", 1200)
	base.ItemName, base.SeriesID, base.SeriesName = "old title", "", ""
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{base}); err != nil {
		t.Fatal(err)
	}
	upd := base
	upd.ItemName, upd.SeriesID, upd.SeriesName = "new title", "s1", "The Show"
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{upd}); err != nil {
		t.Fatal(err)
	}

	var name, sid, sname string
	var n int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&n)
	st.DB().QueryRowContext(ctx,
		`SELECT item_name, series_id, series_name FROM playback_events`,
	).Scan(&name, &sid, &sname)
	if n != 1 || name != "new title" || sid != "s1" || sname != "The Show" {
		t.Fatalf("n=%d name=%q sid=%q sname=%q", n, name, sid, sname)
	}
}

func TestReadPlaybackEvents_RoundTripOrdered(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	a := ev(time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC), "u1", "m2", "movie", 100)
	b := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{a, b}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ItemID != "m1" || got[1].ItemID != "m2" {
		t.Fatalf("order/content wrong: %+v", got)
	}
	if !got[0].At.Equal(b.At) || got[0].PlayDurationSec != 3600 || got[0].ItemType != "movie" {
		t.Fatalf("round-trip lost fields: %+v", got[0])
	}
	if got[0].ItemRuntimeSec != 6000 || got[0].ItemYear != 2001 ||
		len(got[0].ItemGenres) != 2 || got[0].ItemGenres[0] != "Comedy" {
		t.Fatalf("round-trip lost library facts: %+v", got[0])
	}
}

func TestSpine_ItemTagsRoundTripAndRefresh(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	e := source.PlaybackEvent{
		At:     time.Date(2025, 1, 6, 20, 10, 0, 0, time.UTC),
		UserID: "u1", ItemID: "i1", ItemType: "movie", Method: "DirectPlay",
		PlayDurationSec: 3600, ItemTags: []string{"heist", "vault"},
	}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].ItemTags) != 2 || got[0].ItemTags[0] != "heist" || got[0].ItemTags[1] != "vault" {
		t.Fatalf("tags round-trip: %+v", got)
	}

	e.ItemTags = []string{"heist", "vault", "dystopia"}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ReadPlaybackEvents(ctx)
	if len(got) != 1 || len(got[0].ItemTags) != 3 {
		t.Fatalf("tags refresh-on-conflict: %+v", got)
	}
}

func TestAppendPlaybackEvents_Library(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)
	e.Library = "Movies"
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Library != "Movies" {
		t.Fatalf("got %+v", got)
	}

	// re-report with a different library (e.g. item moved) refreshes it
	e.Library = "Shows"
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}
	got, err = st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Library != "Shows" {
		t.Fatalf("library not refreshed on conflict: %+v", got)
	}
}

func TestSpineCoverage(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	f, l, total, err := st.SpineCoverage(ctx)
	if err != nil || f != "" || l != "" || total != 0 {
		t.Fatalf("empty spine: f=%q l=%q total=%d err=%v", f, l, total, err)
	}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{
		ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 10),
		ev(time.Date(2025, 4, 9, 8, 0, 0, 0, time.UTC), "u1", "m2", "movie", 20),
	}); err != nil {
		t.Fatal(err)
	}
	f, l, total, err = st.SpineCoverage(ctx)
	if err != nil || f != "2025-01-06" || l != "2025-04-09" || total != 2 {
		t.Fatalf("f=%q l=%q total=%d err=%v", f, l, total, err)
	}
}

func TestItemsWithUnknownLibrary_And_UpdatePlaybackLibraries(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e1 := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)
	e1.Library = "Unknown"
	e2 := ev(time.Date(2025, 1, 6, 21, 30, 0, 0, time.UTC), "u1", "m2", "movie", 1800)
	e2.Library = "Movies" // already resolved, should not come back
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e1, e2}); err != nil {
		t.Fatal(err)
	}

	unknown, err := st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != "m1" {
		t.Fatalf("unknown = %v, want [m1]", unknown)
	}

	if err := st.UpdatePlaybackLibraries(ctx, map[string]string{"m1": "Movies"}); err != nil {
		t.Fatal(err)
	}
	unknown, err = st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("still unknown after update: %v", unknown)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.ItemID == "m1" && e.Library != "Movies" {
			t.Errorf("m1 library not updated: %q", e.Library)
		}
	}
}

// The Playback Reporting plugin updates an in-progress session's row in place:
// DateCreated / UserId / ItemId stay put while PlayDuration grows on every
// write. The spine has to see that as one play, not one per refresh.
func TestAppendPlaybackEvents_GrowingDurationStaysOneRow(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	at := time.Date(2026, 9, 5, 21, 51, 50, 0, time.UTC)

	for _, dur := range []int64{570, 1164, 1751, 2359, 2891, 2957} {
		e := ev(at, "u1", "m1", "movie", dur)
		if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
			t.Fatal(err)
		}
	}

	var n int
	var dur int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*), COALESCE(MAX(play_duration_sec), 0) FROM playback_events`,
	).Scan(&n, &dur); err != nil {
		t.Fatal(err)
	}
	if n != 1 || dur != 2957 {
		t.Fatalf("got %d rows / %ds, want 1 row / 2957s", n, dur)
	}
}

// A store that shipped with the duration-in-the-hash key accumulated one row
// per refresh for every session that was still open. The migration has to
// collapse those back to one row apiece, keeping the longest duration.
func TestMigration_CollapsesDuplicateSessions(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/legacy.db"

	// A v5 install: everything up to 0005 applied and stamped, so the Open()
	// below runs 0006 and nothing else.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	applyMigrationsUpTo(t, ctx, db, 5)

	const ins = `INSERT INTO playback_events
	  (at, user_id, item_id, item_type, method, play_duration_sec, item_name, dedup_hash)
	  VALUES (?,?,?,?,?,?,?,?)`
	for i, dur := range []int64{570, 1164, 1751, 2359, 2891, 2957} {
		if _, err := db.Exec(ins, "2026-09-05 21:51:50", "u1", "m1", "movie", "DirectPlay",
			dur, "Rose of Nevada", fmt.Sprintf("hash-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// An unrelated session must survive untouched.
	if _, err := db.Exec(ins, "2026-09-04 18:11:46", "u1", "e2", "episode", "DirectPlay",
		3499, "Troy", "hash-other"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open (runs 0006): %v", err)
	}
	t.Cleanup(func() { st.Close() })

	var n int
	var dur int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*), COALESCE(MAX(play_duration_sec), 0) FROM playback_events WHERE item_id = 'm1'`,
	).Scan(&n, &dur); err != nil {
		t.Fatal(err)
	}
	if n != 1 || dur != 2957 {
		t.Fatalf("collapsed session: got %d rows / %ds, want 1 row / 2957s", n, dur)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM playback_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("spine has %d rows, want 2 (the collapsed session plus the untouched one)", n)
	}

	// And the migrated rows must keep deduping against fresh plugin reads.
	at := time.Date(2026, 9, 5, 21, 51, 50, 0, time.UTC)
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{ev(at, "u1", "m1", "movie", 3000)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM playback_events WHERE item_id = 'm1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("re-append after migration made %d rows, want 1", n)
	}
}
