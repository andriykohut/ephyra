package store

import (
	"context"
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
		ItemName: item + "-name",
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
