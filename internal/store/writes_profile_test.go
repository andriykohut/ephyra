package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func TestWriteProfileAggregates_RewriteSemantics(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	first := aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 10}},
		Completion: []aggregate.ProfileCompletionRow{
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "finished", Count: 3},
		},
		Baseline: []aggregate.TasteBaselineRow{{Dim: "genre", Key: "Drama", WatchSec: 999}},
	}
	if err := st.WriteProfileAggregates(ctx, first); err != nil {
		t.Fatal(err)
	}

	second := aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 20}},
	}
	if err := st.WriteProfileAggregates(ctx, second); err != nil {
		t.Fatal(err)
	}

	var plays, comp, base int64
	st.DB().QueryRowContext(ctx, `SELECT plays FROM agg_profile_summary WHERE user_id='u1' AND range='all'`).Scan(&plays)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_profile_completion`).Scan(&comp)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_taste_baseline`).Scan(&base)
	if plays != 20 || comp != 0 || base != 0 {
		t.Fatalf("rewrite not clean: plays=%d comp=%d base=%d", plays, comp, base)
	}
}

func TestWriteProfile_TagOverlapAndTagTaste(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "all", Dim: "tag", Key: "heist", WatchSec: 3000, Plays: 2},
		},
		Baseline: []aggregate.TasteBaselineRow{
			{Dim: "tag", Key: "heist", WatchSec: 3000},
		},
		TagOverlap: []aggregate.ProfileTagOverlapRow{
			{UserA: "u1", UserB: "u2", Range: "all", Cosine: 0.62, Shared: []string{"heist", "vault"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var dim, key string
	var ws int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT dim, key, watch_sec FROM agg_profile_taste WHERE dim='tag'`).Scan(&dim, &key, &ws); err != nil {
		t.Fatal(err)
	}
	if key != "heist" || ws != 3000 {
		t.Fatalf("tag taste row: %s %d", key, ws)
	}

	var ua, ub, shared string
	var cos float64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT user_a, user_b, cosine, shared FROM agg_profile_tag_overlap`).Scan(&ua, &ub, &cos, &shared); err != nil {
		t.Fatal(err)
	}
	if ua != "u1" || ub != "u2" || cos < 0.61 || shared != "heist|vault" {
		t.Fatalf("overlap row: %s %s %v %q", ua, ub, cos, shared)
	}

	if err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{}); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_profile_tag_overlap`).Scan(&n)
	if n != 0 {
		t.Fatalf("expected wipe, got %d rows", n)
	}
}
