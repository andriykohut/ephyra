package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WriteWatchAggregates replaces every watch-derived row in one transaction.
func (s *Store) WriteWatchAggregates(ctx context.Context, daily []aggregate.WatchDailyRow, heat []aggregate.HeatmapRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{`DELETE FROM watch_events_daily`, `DELETE FROM agg_watch_heatmap`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, r := range daily {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO watch_events_daily
			  (day, user_id, item_id, scope, name, series_id, series_name, plays, watch_sec, method)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			r.Day, r.UserID, r.ItemID, r.Scope, r.Name, r.SeriesID, r.SeriesName, r.Plays, r.WatchSec, r.Method,
		); err != nil {
			return err
		}
	}
	for _, r := range heat {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_watch_heatmap (user_id, dow, hour, watch_sec, plays)
			VALUES (?,?,?,?,?)`,
			r.UserID, r.DOW, r.Hour, r.WatchSec, r.Plays,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
