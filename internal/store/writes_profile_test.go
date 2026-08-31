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
