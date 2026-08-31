package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func pe(day string, user, item, typ string, dur, runtime int64) source.PlaybackEvent {
	t, _ := time.Parse("2006-01-02 15:04", day)
	return source.PlaybackEvent{
		At: t, UserID: user, ItemID: item, ItemName: item, ItemType: typ,
		PlayDurationSec: dur, ItemRuntimeSec: runtime, Method: "DirectPlay",
	}
}

func findCompletion(rows []ProfileCompletionRow, user, rng, scope, bucket string) int64 {
	for _, r := range rows {
		if r.UserID == user && r.Range == rng && r.Scope == scope && r.Bucket == bucket {
			return r.Count
		}
	}
	return -1
}

func TestProfiles_CompletionBuckets(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		pe("2025-05-20 20:00", "u1", "mFin", "movie", 5760, 6000), // ratio .96 -> finished
		pe("2025-05-21 20:00", "u1", "mPar", "movie", 3000, 6000), // ratio .5  -> partial
		pe("2025-05-22 20:00", "u1", "mBal", "movie", 300, 6000),  // ratio .05 -> bailed
		pe("2025-05-23 20:00", "u1", "mUnk", "movie", 1200, 0),    // no runtime -> unknown
	}
	agg := Profiles(events, now)

	for bucket, want := range map[string]int64{"finished": 1, "partial": 1, "bailed": 1, "unknown": 1} {
		if got := findCompletion(agg.Completion, "u1", "30d", "movie", bucket); got != want {
			t.Fatalf("30d movie %s = %d, want %d", bucket, got, want)
		}
	}
	// two sessions same title same day sum before the verdict
	events2 := []source.PlaybackEvent{
		pe("2025-05-20 20:00", "u2", "m1", "movie", 3000, 6000),
		pe("2025-05-20 21:30", "u2", "m1", "movie", 3000, 6000), // together 1.0 -> finished
	}
	agg2 := Profiles(events2, now)
	if got := findCompletion(agg2.Completion, "u2", "30d", "movie", "finished"); got != 1 {
		t.Fatalf("summed same-day sessions should be one finished verdict, got %d", got)
	}
}

func TestProfiles_RewatchLifetime(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		pe("2024-01-01 20:00", "u1", "m1", "movie", 6000, 6000),
		pe("2024-06-01 20:00", "u1", "m1", "movie", 6000, 6000), // rewatch #1
		pe("2025-05-01 20:00", "u1", "m1", "movie", 6000, 6000), // rewatch #2
		pe("2025-05-02 20:00", "u1", "m2", "movie", 6000, 6000), // first-watch only
	}
	agg := Profiles(events, now)

	var rw *ProfileRewatchRow
	for i := range agg.Rewatch {
		if agg.Rewatch[i].ItemID == "m1" {
			rw = &agg.Rewatch[i]
		}
	}
	if rw == nil || rw.WatchDays != 3 {
		t.Fatalf("m1 rewatch row: %+v", rw)
	}
	var sumAll *ProfileSummaryRow
	for i := range agg.Summary {
		if agg.Summary[i].UserID == "u1" && agg.Summary[i].Range == "all" {
			sumAll = &agg.Summary[i]
		}
	}
	// 2 rewatch days out of 4 total watch days = 0.5
	if sumAll == nil || sumAll.RewatchPct < 0.49 || sumAll.RewatchPct > 0.51 {
		t.Fatalf("rewatch_pct: %+v", sumAll)
	}
}

func TestProfiles_BingeRuns(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ep := func(ts, item string) source.PlaybackEvent {
		e := pe(ts, "u1", item, "episode", 1400, 1500)
		e.SeriesID, e.SeriesName = "s1", "The Show"
		return e
	}
	events := []source.PlaybackEvent{
		ep("2025-05-01 20:00", "e1"),
		ep("2025-05-01 20:30", "e2"),
		ep("2025-05-01 21:00", "e3"), // run of 3
		ep("2025-05-02 09:00", "e4"), // >4h gap -> new run of 1 (dropped, <2)
	}
	agg := Profiles(events, now)

	if len(agg.Binge) != 1 || agg.Binge[0].RunEpisodes != 3 || agg.Binge[0].SeriesName != "The Show" {
		t.Fatalf("binge rows: %+v", agg.Binge)
	}
	for _, s := range agg.Summary {
		if s.UserID == "u1" && s.Range == "all" && s.LongestBingeEpisodes != 3 {
			t.Fatalf("longest binge on summary: %+v", s)
		}
	}
}

func TestProfiles_TasteWeightedByWatchSec(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	a := pe("2025-05-01 20:00", "u1", "mLong", "movie", 6000, 6600)
	a.ItemGenres, a.ItemYear = []string{"Drama"}, 1994
	b := pe("2025-05-02 20:00", "u1", "mShort", "movie", 60, 6000) // barely watched
	b.ItemGenres, b.ItemYear = []string{"Comedy"}, 2015
	agg := Profiles([]source.PlaybackEvent{a, b}, now)

	var drama, comedy int64
	for _, r := range agg.Taste {
		if r.UserID == "u1" && r.Range == "all" && r.Dim == "genre" {
			switch r.Key {
			case "Drama":
				drama = r.WatchSec
			case "Comedy":
				comedy = r.WatchSec
			}
		}
	}
	if drama != 6000 || comedy != 60 {
		t.Fatalf("taste weighted by seconds: drama=%d comedy=%d", drama, comedy)
	}
	var haveDecade, haveLength bool
	for _, r := range agg.Taste {
		if r.Dim == "decade" && r.Key == "1990" {
			haveDecade = true
		}
		if r.Dim == "length" {
			haveLength = true
		}
	}
	if !haveDecade || !haveLength {
		t.Fatalf("missing decade/length dims: %+v", agg.Taste)
	}
	if len(agg.Baseline) == 0 {
		t.Fatal("baseline not populated")
	}
}

func TestProfiles_RangeFiltering(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		pe("2025-05-25 20:00", "u1", "recent", "movie", 6000, 6000), // in 30d
		pe("2025-01-10 20:00", "u1", "older", "movie", 6000, 6000),  // in 1y, not 30d
	}
	agg := Profiles(events, now)

	if got := findCompletion(agg.Completion, "u1", "30d", "movie", "finished"); got != 1 {
		t.Fatalf("30d finished = %d, want 1", got)
	}
	if got := findCompletion(agg.Completion, "u1", "1y", "movie", "finished"); got != 2 {
		t.Fatalf("1y finished = %d, want 2", got)
	}
	if got := findCompletion(agg.Completion, "u1", "all", "movie", "finished"); got != 2 {
		t.Fatalf("all finished = %d, want 2", got)
	}
}
