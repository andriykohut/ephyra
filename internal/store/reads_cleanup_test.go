package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func seedCleanup(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []aggregate.CleanupRow{
		{ItemID: "a", Scope: "movie", Name: "Big Never", Library: "Movies", Bytes: 900, AddedAt: "2020-01-01T00:00:00Z"},
		{ItemID: "b", Scope: "movie", Name: "Small Never", Library: "Movies", Bytes: 100, AddedAt: "2024-01-01T00:00:00Z"},
		{ItemID: "c", Scope: "series", Name: "Stale Show", Library: "Shows", Bytes: 500, Episodes: 12,
			AddedAt: "2019-01-01T00:00:00Z", LastPlayedAt: "2023-06-01T00:00:00Z"},
		{ItemID: "d", Scope: "movie", Name: "Recent", Library: "Movies", Bytes: 700,
			AddedAt: "2025-01-01T00:00:00Z", LastPlayedAt: "2025-12-01T00:00:00Z"},
	}
	if err := s.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, rows, nil, nil); err != nil {
		t.Fatal(err)
	}
	return s, now
}

func TestReadCleanup_NeverAndStale(t *testing.T) {
	s, now := seedCleanup(t)
	ctx := context.Background()

	never, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if never.MatchCount != 2 || never.ReclaimableBytes != 1000 {
		t.Fatalf("never: count=%d bytes=%d", never.MatchCount, never.ReclaimableBytes)
	}
	if never.Items[0].ItemID != "a" {
		t.Fatalf("never sort=size: first=%s", never.Items[0].ItemID)
	}
	if never.Items[0].LastPlayedAt != nil {
		t.Fatalf("never item should have nil last_played_at")
	}

	stale, err := s.ReadCleanup(ctx, CleanupParams{Mode: "stale", Sort: "added", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if stale.MatchCount != 1 || stale.Items[0].ItemID != "c" {
		t.Fatalf("stale: %+v", stale)
	}
	if *stale.Items[0].LastPlayedAt != "2023-06-01T00:00:00Z" {
		t.Fatalf("stale last_played = %v", *stale.Items[0].LastPlayedAt)
	}

	lim, _ := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 1, Now: now})
	if !lim.Truncated || len(lim.Items) != 1 || lim.MatchCount != 2 || lim.ReclaimableBytes != 1000 {
		t.Fatalf("limit: %+v", lim)
	}

	var b strings.Builder
	if err := s.StreamCleanupCSV(ctx, "never", "", now, &b); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "item_id,scope,name,library,bytes,episodes,added_at,last_played_at") {
		t.Fatalf("csv:\n%s", b.String())
	}
}

func TestReadCleanup_LibraryFilter(t *testing.T) {
	// seedCleanup's "never" matches ("a","b") are both in the "Movies" library,
	// so filtering by that library wouldn't actually narrow anything. Seed our
	// own rows spanning two libraries within the same mode so the filter's
	// narrowing effect is actually exercised.
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []aggregate.CleanupRow{
		{ItemID: "a", Scope: "movie", Name: "Movie Never A", Library: "Movies", Bytes: 900, AddedAt: "2020-01-01T00:00:00Z"},
		{ItemID: "b", Scope: "movie", Name: "Movie Never B", Library: "Movies", Bytes: 100, AddedAt: "2024-01-01T00:00:00Z"},
		{ItemID: "e", Scope: "series", Name: "Show Never E", Library: "Shows", Bytes: 500, Episodes: 5, AddedAt: "2021-01-01T00:00:00Z"},
	}
	if err := s.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, rows, nil, nil); err != nil {
		t.Fatal(err)
	}

	all, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if all.MatchCount != 3 {
		t.Fatalf("unfiltered: want 3 matches across both libraries, got %d", all.MatchCount)
	}

	movies, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Library: "Movies", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if movies.MatchCount != 2 {
		t.Fatalf("Movies filter: want 2 matches (narrowed from 3), got %d", movies.MatchCount)
	}
	for _, it := range movies.Items {
		if it.Library != "Movies" {
			t.Fatalf("item %s has library %q, want Movies", it.ItemID, it.Library)
		}
	}

	shows, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Library: "Shows", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if shows.MatchCount != 1 || len(shows.Items) != 1 || shows.Items[0].ItemID != "e" {
		t.Fatalf("Shows filter: want exactly item e, got %+v", shows)
	}
}

func TestReadCleanup_EmptyItemsSerializeAsArray(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	res, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items == nil {
		t.Fatal("Items is nil; want an empty slice so it marshals as []")
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("items not []: %s", b)
	}
}
