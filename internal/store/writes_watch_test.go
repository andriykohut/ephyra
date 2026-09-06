package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func TestWriteWatchAggregatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	daily := []aggregate.WatchDailyRow{
		{Day: "2025-01-06", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo",
			Method: "DirectPlay", Plays: 1, WatchSec: 3600},
		{Day: "2025-01-07", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo",
			Method: "VideoTranscode", Plays: 1, WatchSec: 1800},
	}
	heat := []aggregate.HeatmapRow{{UserID: "u1", DOW: 1, Hour: 20, WatchSec: 3600, Plays: 1}}

	if err := s.WriteWatchAggregates(ctx, daily, heat); err != nil {
		t.Fatal(err)
	}
	// rewrites: second call replaces, not appends
	if err := s.WriteWatchAggregates(ctx, daily[:1], heat); err != nil {
		t.Fatal(err)
	}

	var rows, hrows int
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM watch_events_daily`).Scan(&rows)
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_watch_heatmap`).Scan(&hrows)
	if rows != 1 || hrows != 1 {
		t.Fatalf("after rewrite want 1/1 rows, got %d/%d", rows, hrows)
	}
}

func TestWriteWatchAggregates_Library(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	daily := []aggregate.WatchDailyRow{
		{Day: "2025-01-06", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Alpha",
			Method: "DirectPlay", Library: "Movies", Plays: 1, WatchSec: 600},
	}
	heat := []aggregate.HeatmapRow{
		{UserID: "u1", Library: "Movies", DOW: 1, Hour: 20, Plays: 1, WatchSec: 600},
	}
	if err := st.WriteWatchAggregates(ctx, daily, heat); err != nil {
		t.Fatal(err)
	}
	var lib string
	if err := st.DB().QueryRowContext(ctx, `SELECT library FROM watch_events_daily WHERE item_id = 'm1'`).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if lib != "Movies" {
		t.Fatalf("watch_events_daily.library = %q", lib)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT library FROM agg_watch_heatmap WHERE user_id = 'u1'`).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if lib != "Movies" {
		t.Fatalf("agg_watch_heatmap.library = %q", lib)
	}
}
