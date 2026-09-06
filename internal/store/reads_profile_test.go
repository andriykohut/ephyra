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
	agg := map[string]aggregate.ProfileAggregates{"": {
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
	}}
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

func TestReadProfileList_DoesNotFanOutAcrossLibraries(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dim_user (id, name) VALUES ('u1','alice')`); err != nil {
		t.Fatal(err)
	}

	// Two library scopes for the same user in one write call. agg_profile_summary's
	// PK is (user_id, range, library), so a naive `range = 'all'` join with no
	// library predicate would match both rows and duplicate the user in the list.
	if err := st.WriteProfileAggregates(ctx, map[string]aggregate.ProfileAggregates{
		"": {
			Summary: []aggregate.ProfileSummaryRow{
				{UserID: "u1", Range: "all", WatchSec: 9000, Plays: 12, RewatchPct: 0.25, LastPlay: "2025-05-01"},
			},
		},
		"Movies": {
			Summary: []aggregate.ProfileSummaryRow{
				{UserID: "u1", Range: "all", WatchSec: 3000, Plays: 2, LastPlay: "2025-05-02"},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	pl, err := st.ReadProfileList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var u1Count int
	for _, u := range pl.Users {
		if u.ID == "u1" {
			u1Count++
			if u.TotalPlays != 12 || u.TotalWatchSec != 9000 {
				t.Fatalf("u1 entry should reflect the '' (All) scope, got %+v", u)
			}
		}
	}
	if u1Count != 1 {
		t.Fatalf("u1 appeared %d times in the list, want 1: %+v", u1Count, pl.Users)
	}
}

func TestReadProfile_RangeScopedPlusLifetime(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)

	p, ok, err := st.ReadProfile(ctx, "u1", "30d", "")
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

func TestReadProfile_TagsAndOverlap(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dim_user (id, name) VALUES ('u1','alice'), ('u2','bob')`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, RefreshMeta{Job: "watch", OK: true, PluginAvailable: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteProfileAggregates(ctx, map[string]aggregate.ProfileAggregates{"": {
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 5, WatchSec: 9000}},
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "all", Dim: "tag", Key: "heist", WatchSec: 8000, Plays: 5},
			{UserID: "u1", Range: "all", Dim: "tag", Key: "cameo", WatchSec: 30, Plays: 1},
		},
		Baseline: []aggregate.TasteBaselineRow{
			{Dim: "tag", Key: "heist", WatchSec: 1000},
			{Dim: "tag", Key: "cameo", WatchSec: 1000},
		},
		TagOverlap: []aggregate.ProfileTagOverlapRow{
			{UserA: "u1", UserB: "u2", Range: "all", Cosine: 0.5, Shared: []string{"heist"}},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	p, ok, err := st.ReadProfile(ctx, "u1", "all", "")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if len(p.Taste.Tag) == 0 || p.Taste.Tag[0].Key != "heist" {
		t.Fatalf("taste.tag: %+v", p.Taste.Tag)
	}
	if len(p.Taste.SignatureTags) != 1 || p.Taste.SignatureTags[0] != "heist" {
		t.Fatalf("signature_tags (cameo should be below the floor): %+v", p.Taste.SignatureTags)
	}
	if len(p.TagOverlap) != 1 || p.TagOverlap[0].User != "u2" || p.TagOverlap[0].UserName != "bob" ||
		len(p.TagOverlap[0].Shared) != 1 || p.TagOverlap[0].Shared[0] != "heist" {
		t.Fatalf("tag_overlap: %+v", p.TagOverlap)
	}

	p2, _, _ := st.ReadProfile(ctx, "u2", "all", "")
	if p2.TagOverlap == nil {
		t.Fatalf("tag_overlap must be [] not nil")
	}
	if len(p2.TagOverlap) != 1 || p2.TagOverlap[0].User != "u1" {
		t.Fatalf("same pair surfaced from u2's side: %+v", p2.TagOverlap)
	}
}

func TestReadProfile_UnknownUser(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)
	_, ok, err := st.ReadProfile(ctx, "nope", "all", "")
	if err != nil || ok {
		t.Fatalf("want ok=false err=nil, got ok=%v err=%v", ok, err)
	}
}
