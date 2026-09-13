package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WritePeopleAggregates replaces every row in one transaction, same shape as
// WriteProfileAggregates: scoped[""] is "All libraries", every other key is
// one Jellyfin library.
func (s *Store) WritePeopleAggregates(ctx context.Context, scoped map[string]aggregate.PeopleAggregates) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_profile_people`,
		`DELETE FROM agg_profile_top_items`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for lib, a := range scoped {
		for _, r := range a.People {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_people
				  (library, user_id, range, kind, person, watch_sec, plays, distinct_titles,
				   watch_sec_played, plays_played, distinct_titles_played)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				lib, r.UserID, r.Range, r.Kind, r.Person, r.WatchSec, r.Plays, r.DistinctTitles,
				r.WatchSecPlayed, r.PlaysPlayed, r.DistinctTitlesPlayed,
			); err != nil {
				return err
			}
		}
		for _, r := range a.TopItems {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_top_items
				  (library, user_id, range, scope, item_id, name, series_name,
				   watch_sec, plays, watch_sec_played, plays_played)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				lib, r.UserID, r.Range, r.Scope, r.ItemID, r.Name, r.SeriesName,
				r.WatchSec, r.Plays, r.WatchSecPlayed, r.PlaysPlayed,
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
