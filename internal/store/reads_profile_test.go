package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func seedProfile(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dim_user (id, name) VALUES ('u1','alice'), ('u2','bob')`); err != nil {
		t.Fatal(err)
	}
	agg := aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{
			{UserID: "u1", Range: "all", WatchSec: 9000, Plays: 12, DistinctTitles: 5, DaysActive: 7,
				FinishedPct: 0.6, BailedPct: 0.1, RewatchPct: 0.25,
				LongestBingeEpisodes: 6, LongestBingeSeriesName: "The Show",
				FirstPlay: "2025-01-01", LastPlay: "2025-05-01"},
			{UserID: "u1", Range: "30d", WatchSec: 3000, Plays: 4, DistinctTitles: 3, DaysActive: 3,
				FinishedPct: 0.5, BailedPct: 0.25, FirstPlay: "2025-04-10", LastPlay: "2025-05-01"},
		},
		Completion: []aggregate.ProfileCompletionRow{
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "finished", Count: 2},
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "bailed", Count: 1},
		},
		Rewatch: []aggregate.ProfileRewatchRow{
			{UserID: "u1", Scope: "movie", ItemID: "m1", Name: "Alpha", WatchDays: 3},
		},
		Binge: []aggregate.ProfileBingeRow{
			{UserID: "u1", SeriesID: "s1", SeriesName: "The Show", RunEpisodes: 6, RunStart: "2025-02-01", RunEnd: "2025-02-01"},
		},
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "30d", Dim: "genre", Key: "Drama", WatchSec: 2400, Plays: 2},
			{UserID: "u1", Range: "30d", Dim: "genre", Key: "Comedy", WatchSec: 600, Plays: 1},
			{UserID: "u1", Range: "30d", Dim: "decade", Key: "1990", WatchSec: 3000, Plays: 3},
			{UserID: "u1", Range: "30d", Dim: "length", Key: "90-120m", WatchSec: 3000, Plays: 3},
		},
		Baseline: []aggregate.TasteBaselineRow{
			{Dim: "genre", Key: "Drama", WatchSec: 1000},
			{Dim: "genre", Key: "Comedy", WatchSec: 4000},
		},
	}
	if err := st.WriteProfileAggregates(ctx, agg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, RefreshMeta{Job: "watch", OK: true, PluginAvailable: true}); err != nil {
		t.Fatal(err)
	}
}

func TestReadProfileList(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)

	pl, err := st.ReadProfileList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pl.PluginAvailable || len(pl.Users) != 2 {
		t.Fatalf("list: %+v", pl)
	}
	if pl.Users[0].Name != "alice" || pl.Users[0].TotalPlays != 12 || pl.Users[0].RewatchPct != 0.25 {
		t.Fatalf("alice entry: %+v", pl.Users[0])
	}
	if pl.Users[1].Name != "bob" || pl.Users[1].TotalPlays != 0 {
		t.Fatalf("bob entry: %+v", pl.Users[1])
	}
}

func TestReadProfile_RangeScopedPlusLifetime(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)

	p, ok, err := st.ReadProfile(ctx, "u1", "30d")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.User.Name != "alice" || p.Summary.Plays != 4 {
		t.Fatalf("range-scoped summary wrong: %+v", p.Summary)
	}
	if p.Summary.RewatchPct != 0.25 || p.Summary.LongestBinge.Episodes != 6 {
		t.Fatalf("lifetime fields missing on 30d: %+v", p.Summary)
	}
	if len(p.Completion) != 2 || len(p.Rewatch) != 1 || len(p.Binge) != 1 {
		t.Fatalf("slices: comp=%d rw=%d binge=%d", len(p.Completion), len(p.Rewatch), len(p.Binge))
	}
	if len(p.Taste.SignatureGenres) == 0 || p.Taste.SignatureGenres[0] != "Drama" {
		t.Fatalf("signature genres: %+v", p.Taste.SignatureGenres)
	}
}

func TestReadProfile_UnknownUser(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)
	_, ok, err := st.ReadProfile(ctx, "nope", "all")
	if err != nil || ok {
		t.Fatalf("want ok=false err=nil, got ok=%v err=%v", ok, err)
	}
}
