package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WriteProfileAggregates replaces every profile-derived row in one
// transaction, so a reader never sees a half-written set -- including every
// library's scope: scoped[""] is "All libraries", every other key is one
// Jellyfin library. Same shape as WriteLibraryAggregates.
func (s *Store) WriteProfileAggregates(ctx context.Context, scoped map[string]aggregate.ProfileAggregates) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_profile_summary`,
		`DELETE FROM agg_profile_completion`,
		`DELETE FROM agg_profile_abandoned`,
		`DELETE FROM agg_profile_rewatch`,
		`DELETE FROM agg_profile_binge`,
		`DELETE FROM agg_profile_taste`,
		`DELETE FROM agg_taste_baseline`,
		`DELETE FROM agg_profile_tag_overlap`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for lib, a := range scoped {
		for _, r := range a.Summary {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_summary
				  (user_id, range, library, watch_sec, plays, distinct_titles, days_active,
				   finished_pct, bailed_pct, rewatch_pct, longest_binge_episodes,
				   longest_binge_series_name, show_of_range_series_id,
				   show_of_range_series_name, first_play, last_play)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.WatchSec, r.Plays, r.DistinctTitles, r.DaysActive,
				r.FinishedPct, r.BailedPct, r.RewatchPct, r.LongestBingeEpisodes,
				r.LongestBingeSeriesName, r.ShowOfRangeSeriesID, r.ShowOfRangeSeriesName,
				r.FirstPlay, r.LastPlay,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Completion {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_completion (user_id, range, library, scope, bucket, count)
				VALUES (?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.Scope, r.Bucket, r.Count,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Abandoned {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_abandoned
				  (user_id, range, library, scope, item_id, name, series_name, bailed_count)
				VALUES (?,?,?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.Scope, r.ItemID, r.Name, r.SeriesName, r.BailedCount,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Rewatch {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_rewatch
				  (user_id, library, scope, item_id, name, series_name, watch_days)
				VALUES (?,?,?,?,?,?,?)`,
				r.UserID, lib, r.Scope, r.ItemID, r.Name, r.SeriesName, r.WatchDays,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Binge {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_binge
				  (user_id, library, series_id, series_name, run_episodes, run_start, run_end)
				VALUES (?,?,?,?,?,?,?)`,
				r.UserID, lib, r.SeriesID, r.SeriesName, r.RunEpisodes, r.RunStart, r.RunEnd,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Taste {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_taste (user_id, range, library, dim, key, watch_sec, plays)
				VALUES (?,?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.Dim, r.Key, r.WatchSec, r.Plays,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Baseline {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_taste_baseline (library, dim, key, watch_sec) VALUES (?,?,?,?)`,
				lib, r.Dim, r.Key, r.WatchSec,
			); err != nil {
				return err
			}
		}
		for _, r := range a.TagOverlap {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_tag_overlap (user_a, user_b, range, library, cosine, shared)
				VALUES (?,?,?,?,?,?)`,
				r.UserA, r.UserB, r.Range, lib, r.Cosine, joinTags(r.Shared),
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
