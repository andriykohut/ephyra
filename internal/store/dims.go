package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/source"
)

// UpsertCredits refreshes what Jellyfin still knows and leaves everything else
// alone. No DELETE: a play in the spine outlives its item, and its cast has to
// outlive it too or the charts lose history when you delete a film.
func (s *Store) UpsertCredits(ctx context.Context, cs []source.Credit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range cs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dim_credit (item_id, person, kind, list_order)
			VALUES (?,?,?,?)
			ON CONFLICT (item_id, person, kind) DO UPDATE SET list_order = excluded.list_order`,
			c.ItemID, c.Person, c.Kind, c.ListOrder,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertPlayed mirrors UpsertCredits. UserData is ON DELETE CASCADE from
// BaseItems, so this snapshot is the only copy of the tick that survives the
// item.
func (s *Store) UpsertPlayed(ctx context.Context, ps []source.UserItemPlayed) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range ps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dim_played (user_id, item_id, played)
			VALUES (?,?,?)
			ON CONFLICT (user_id, item_id) DO UPDATE SET played = excluded.played`,
			p.UserID, p.ItemID, p.Played,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadCredits returns every credit ever seen, including ones for items no
// longer in Jellyfin.
func (s *Store) ReadCredits(ctx context.Context) ([]source.Credit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id, person, kind, list_order FROM dim_credit`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []source.Credit
	for rows.Next() {
		var c source.Credit
		if err := rows.Scan(&c.ItemID, &c.Person, &c.Kind, &c.ListOrder); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return orEmpty(out), rows.Err()
}

// ReadPlayed returns the watched tick for every (user, item) pair ever seen.
// The map carries only true entries, so a missing key and a false key mean the
// same thing to callers.
func (s *Store) ReadPlayed(ctx context.Context) (map[aggregate.UserItem]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, item_id, played FROM dim_played`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[aggregate.UserItem]bool{}
	for rows.Next() {
		var ui aggregate.UserItem
		var played bool
		if err := rows.Scan(&ui.UserID, &ui.ItemID, &played); err != nil {
			return nil, err
		}
		if played {
			out[ui] = true
		}
	}
	return out, rows.Err()
}
