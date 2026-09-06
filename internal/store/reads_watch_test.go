package store

import (
	"context"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func seedWatch(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	users := []aggregate.UserRow{{ID: "u1", Name: "alice"}, {ID: "u2", Name: "bob"}}
	core := []aggregate.CorePlayRow{
		{UserID: "u1", Scope: "movie", ItemID: "m9", Name: "Fav Movie", PlayCount: 12, LastPlayedAt: "2025-01-20T00:00:00Z"},
		{UserID: "u2", Scope: "series", ItemID: "s9", Name: "Fav Show", PlayCount: 40},
	}
	if err := s.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, nil, users, core); err != nil {
		t.Fatal(err)
	}

	daily := []aggregate.WatchDailyRow{
		{Day: "2025-01-06", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo", Method: "DirectPlay", Plays: 3, WatchSec: 9000},
		{Day: "2025-01-20", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo", Method: "VideoTranscode", Plays: 1, WatchSec: 1800},
		{Day: "2025-01-21", UserID: "u2", ItemID: "e1", Scope: "episode", Name: "Ep 1", SeriesID: "s1", SeriesName: "Some Show", Method: "DirectPlay", Plays: 2, WatchSec: 2400},
		{Day: "2024-06-01", UserID: "u1", ItemID: "m2", Scope: "movie", Name: "Old", Method: "DirectPlay", Plays: 1, WatchSec: 600},
	}
	heat := []aggregate.HeatmapRow{
		{UserID: "u1", DOW: 1, Hour: 20, WatchSec: 9000, Plays: 3},
		{UserID: "u2", DOW: 2, Hour: 21, WatchSec: 2400, Plays: 2},
	}
	if err := s.WriteWatchAggregates(ctx, daily, heat); err != nil {
		t.Fatal(err)
	}
	return s, now
}

func mustRead(t *testing.T, s *Store, p WatchStatsParams) WatchStats {
	t.Helper()
	w, err := s.ReadWatchStats(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestReadWatchStats_RangeAndUserFilters(t *testing.T) {
	s, now := seedWatch(t)

	all := mustRead(t, s, WatchStatsParams{Range: "all", User: "", Now: now})
	if len(all.Users) != 2 || all.Coverage.TotalPlays != 7 || all.Coverage.FirstPlay != "2024-06-01" {
		t.Fatalf("coverage/users: %+v", all)
	}
	if all.Totals.Plays != 7 || all.Totals.ActiveUsers != 2 {
		t.Fatalf("all totals: %+v", all.Totals)
	}
	if all.Totals.DirectPlayPct < 0.85 || all.Totals.DirectPlayPct > 0.86 {
		t.Fatalf("direct_play_pct = %v", all.Totals.DirectPlayPct)
	}
	if len(all.MostPlayedCore) != 2 {
		t.Fatalf("all core rows: %+v", all.MostPlayedCore)
	}

	r30 := mustRead(t, s, WatchStatsParams{Range: "30d", User: "", Now: now})
	if r30.Totals.Plays != 6 {
		t.Fatalf("30d plays = %d want 6", r30.Totals.Plays)
	}
	if len(r30.TopSeries) != 1 || r30.TopSeries[0].Name != "Some Show" {
		t.Fatalf("top_series: %+v", r30.TopSeries)
	}

	u1 := mustRead(t, s, WatchStatsParams{Range: "all", User: "u1", Now: now})
	if u1.Totals.Plays != 5 {
		t.Fatalf("u1 plays = %d want 5", u1.Totals.Plays)
	}
	if len(u1.ActiveUsers) != 2 {
		t.Fatalf("active_users should ignore user filter: %d", len(u1.ActiveUsers))
	}
	if len(u1.Heatmap) != 1 || u1.Heatmap[0].DOW != 1 {
		t.Fatalf("u1 heatmap: %+v", u1.Heatmap)
	}
	if len(u1.MostPlayedCore) != 1 || u1.MostPlayedCore[0].Name != "Fav Movie" {
		t.Fatalf("u1 core: %+v", u1.MostPlayedCore)
	}
}

func TestReadWatchStats_LibraryFilter(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := st.WriteWatchAggregates(ctx,
		[]aggregate.WatchDailyRow{
			{Day: "2025-05-20", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Alpha",
				Method: "DirectPlay", Library: "Movies", Plays: 1, WatchSec: 600},
			{Day: "2025-05-20", UserID: "u1", ItemID: "e1", Scope: "episode", Name: "S1E1",
				Method: "DirectPlay", Library: "Shows", Plays: 1, WatchSec: 900},
			{Day: "2025-05-21", UserID: "u2", ItemID: "e2", Scope: "episode", Name: "S1E2",
				Method: "DirectPlay", Library: "Shows", Plays: 1, WatchSec: 300},
		},
		[]aggregate.HeatmapRow{
			{UserID: "u1", Library: "Movies", DOW: 2, Hour: 20, Plays: 1, WatchSec: 600},
			{UserID: "u1", Library: "Shows", DOW: 2, Hour: 20, Plays: 1, WatchSec: 900},
			{UserID: "u2", Library: "Shows", DOW: 3, Hour: 21, Plays: 1, WatchSec: 300},
		},
	); err != nil {
		t.Fatal(err)
	}

	all := mustRead(t, st, WatchStatsParams{Range: "all", Now: now})
	if all.Totals.WatchSeconds != 1800 {
		t.Fatalf("all: %d", all.Totals.WatchSeconds)
	}
	if len(all.ActiveUsers) != 2 {
		t.Fatalf("all active_users should include both users: %+v", all.ActiveUsers)
	}
	movies := mustRead(t, st, WatchStatsParams{Range: "all", Library: "Movies", Now: now})
	if movies.Totals.WatchSeconds != 600 {
		t.Fatalf("movies: %d", movies.Totals.WatchSeconds)
	}
	var heatSecMovies int64
	for _, h := range movies.Heatmap {
		heatSecMovies += h.WatchSec
	}
	if heatSecMovies != 600 {
		t.Fatalf("movies heatmap: %d", heatSecMovies)
	}
	// active_users must narrow to the library filter: only u1 watched Movies.
	if len(movies.ActiveUsers) != 1 || movies.ActiveUsers[0].UserID != "u1" {
		t.Fatalf("movies active_users should narrow to u1 only: %+v", movies.ActiveUsers)
	}

	// active_users still ignores the user filter even with a library set: u2
	// never watched Movies, but asking for user=u2 must not narrow the panel
	// to u2 -- it must still show whoever actually watched Movies (u1).
	moviesAsU2 := mustRead(t, st, WatchStatsParams{Range: "all", Library: "Movies", User: "u2", Now: now})
	if len(moviesAsU2.ActiveUsers) != 1 || moviesAsU2.ActiveUsers[0].UserID != "u1" {
		t.Fatalf("active_users should ignore user filter but honor library: %+v", moviesAsU2.ActiveUsers)
	}
}

func TestReadWatchStats_PluginMetaAbsent(t *testing.T) {
	s, now := seedWatch(t)
	w := mustRead(t, s, WatchStatsParams{Range: "30d", Now: now})
	if w.PluginAvailable {
		t.Fatalf("no watch refresh_meta -> plugin should read as unavailable")
	}
}
