package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func sampleAggregates() aggregate.LibraryAggregates {
	return aggregate.LibraryAggregates{
		Totals: map[string]float64{
			"items.total": 6, "items.Movie": 4, "items.Episode": 2, "items.Series": 3,
			"runtime_sec.total": 33600, "bytes.total": 29_500_000_000,
			"count.uhd": 2, "count.hdr": 3, "count.dv": 1,
			"tags.tagged_items": 4, "tags.total_items": 6,
		},
		ItemsByLibrary: []aggregate.LabeledCount{{Label: "Movies", Count: 4}, {Label: "Shows", Count: 2}},
		DiskByResolution: []aggregate.DiskBucket{
			{Bucket: "4K", Bytes: 23_000_000_000, Items: 2},
			{Bucket: "1080p", Bytes: 5_300_000_000, Items: 3},
			{Bucket: "Unknown", Bytes: 0, Items: 1},
		},
		DiskByCodec:     []aggregate.DiskBucket{{Bucket: "HEVC", Bytes: 23_000_000_000, Items: 2}},
		DiskByContainer: []aggregate.DiskBucket{{Bucket: "mkv", Bytes: 25_500_000_000, Items: 4}},
		DiskByLibrary: []aggregate.DiskBucket{
			{Bucket: "Movies", Bytes: 27_000_000_000, Items: 4},
			{Bucket: "Shows", Bytes: 2_500_000_000, Items: 2},
		},
		GenresTop:   []aggregate.LabeledCount{{Label: "Drama", Count: 4}, {Label: "Comedy", Count: 1}},
		TagsTop:     []aggregate.LabeledCount{{Label: "heist", Count: 4}, {Label: "vault", Count: 3}},
		TagCoverage: aggregate.TagCoverage{ItemsTagged: 4, ItemsTotal: 6},
		TagPairs:    []aggregate.TagPair{{A: "heist", B: "vault", Items: 3}},
		ByDecade: []aggregate.LabeledCount{
			{Label: "1990s", Count: 1}, {Label: "2000s", Count: 1},
			{Label: "2010s", Count: 3}, {Label: "2020s", Count: 1},
		},
		Growth: []aggregate.GrowthPoint{
			{Month: "2024-01", AddedItems: 2, AddedBytes: 12_000_000_000, CumItems: 2},
			{Month: "2024-03", AddedItems: 4, AddedBytes: 17_500_000_000, CumItems: 6},
		},
	}
}

func TestWriteThenReadLibraryOverview(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WriteLibraryAggregates(ctx, sampleAggregates(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	ov, err := s.ReadLibraryOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Totals.RuntimeSeconds != 33600 || ov.Totals.Bytes != 29_500_000_000 || ov.Totals.CountHDR != 3 || ov.Totals.Series != 3 || ov.Totals.Items != 6 {
		t.Fatalf("totals: %+v", ov.Totals)
	}
	if len(ov.Totals.ItemsByLibrary) != 2 || ov.Totals.ItemsByLibrary[0].Label != "Movies" {
		t.Fatalf("items by library: %+v", ov.Totals.ItemsByLibrary)
	}
	if ov.DiskByResolution[0].Bucket != "4K" || ov.DiskByResolution[0].Bytes != 23_000_000_000 {
		t.Fatalf("disk res order: %+v", ov.DiskByResolution)
	}
	if ov.ByDecade[0].Label != "1990s" || ov.ByDecade[3].Label != "2020s" {
		t.Fatalf("decade order: %+v", ov.ByDecade)
	}
	if ov.Growth[0].Month != "2024-01" || ov.Growth[1].CumItems != 6 {
		t.Fatalf("growth: %+v", ov.Growth)
	}
	if len(ov.GenresTop) != 2 || ov.GenresTop[0].Label != "Drama" {
		t.Fatalf("genres: %+v", ov.GenresTop)
	}
	if ov.Tags.Coverage.Tagged != 4 || ov.Tags.Coverage.Total != 6 {
		t.Fatalf("tag coverage: %+v", ov.Tags.Coverage)
	}
	if len(ov.Tags.Top) == 0 || ov.Tags.Top[0].Label != "heist" {
		t.Fatalf("tag top: %+v", ov.Tags.Top)
	}
	if len(ov.Tags.Pairs) != 1 || ov.Tags.Pairs[0].A != "heist" || ov.Tags.Pairs[0].B != "vault" || ov.Tags.Pairs[0].Items != 3 {
		t.Fatalf("tag pairs: %+v", ov.Tags.Pairs)
	}

	repl := sampleAggregates()
	repl.Totals["bytes.total"] = 1
	repl.Growth = []aggregate.GrowthPoint{{Month: "2024-05", AddedItems: 1, AddedBytes: 1, CumItems: 1}}
	if err := s.WriteLibraryAggregates(ctx, repl, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	ov, _ = s.ReadLibraryOverview(ctx)
	if ov.Totals.Bytes != 1 || len(ov.Growth) != 1 || ov.Growth[0].Month != "2024-05" {
		t.Fatalf("replace failed: %+v", ov)
	}
}

func TestReadLibraryOverview_TagsEmpty(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WriteLibraryAggregates(ctx,
		aggregate.LibraryAggregates{Totals: map[string]float64{}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	ov, err := s.ReadLibraryOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Tags.Coverage.Tagged != 0 || ov.Tags.Coverage.Total != 0 {
		t.Fatalf("empty coverage: %+v", ov.Tags.Coverage)
	}
	if ov.Tags.Top == nil || ov.Tags.Pairs == nil {
		t.Fatalf("empty slices must be non-nil for []-serialization: %+v", ov.Tags)
	}
}

func TestWriteLibraryExtrasRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := aggregate.LibraryAggregates{Totals: map[string]float64{"items.total": 1}}
	cleanup := []aggregate.CleanupRow{
		{ItemID: "m1", Scope: "movie", Name: "Alpha", Library: "Movies", Bytes: 900, AddedAt: "2024-01-01T00:00:00Z"}, // never
		{ItemID: "s1", Scope: "series", Name: "Show", Library: "Shows", Bytes: 500, Episodes: 12,
			AddedAt: "2020-01-01T00:00:00Z", LastPlayedAt: "2023-06-01T00:00:00Z"},
	}
	users := []aggregate.UserRow{{ID: "u1", Name: "alice"}}
	core := []aggregate.CorePlayRow{{UserID: "u1", Scope: "movie", ItemID: "m1", Name: "Alpha", PlayCount: 3}}

	if err := s.WriteLibraryAggregates(ctx, base, cleanup, users, core); err != nil {
		t.Fatal(err)
	}

	var cn, un, corn int
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_cleanup`).Scan(&cn)
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM dim_user`).Scan(&un)
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_played_core`).Scan(&corn)
	if cn != 2 || un != 1 || corn != 1 {
		t.Fatalf("counts cleanup=%d users=%d core=%d", cn, un, corn)
	}
	// "" LastPlayedAt reads back as NULL
	var last *string
	if err := s.DB().QueryRowContext(ctx, `SELECT last_played_at FROM agg_cleanup WHERE item_id='m1'`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != nil {
		t.Fatalf("empty last_played_at should be NULL, got %q", *last)
	}
	// core last_played_at NULL too
	if err := s.DB().QueryRowContext(ctx, `SELECT last_played_at FROM agg_played_core WHERE item_id='m1'`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != nil {
		t.Fatalf("core last_played_at should be NULL, got %q", *last)
	}
}
