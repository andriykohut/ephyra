package store

import (
	"context"
	"database/sql"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WriteLibraryAggregates replaces every library-derived row in one
// transaction, so a reader never sees a half-written set — including every
// library's scope: scoped[""] is "All libraries", every other key is one
// Jellyfin library. cleanup/users/core are not library-scoped (Cleanup and
// Watch Stats get their own filtering elsewhere).
func (s *Store) WriteLibraryAggregates(ctx context.Context, scoped map[string]aggregate.LibraryAggregates,
	cleanup []aggregate.CleanupRow, users []aggregate.UserRow, core []aggregate.CorePlayRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_totals`,
		`DELETE FROM agg_disk`,
		`DELETE FROM agg_distribution WHERE dimension IN ('genre','decade','library_items','tag')`,
		`DELETE FROM agg_library_growth`,
		`DELETE FROM agg_library_tag_pairs`,
		`DELETE FROM agg_cleanup`,
		`DELETE FROM dim_user`,
		`DELETE FROM dim_library`,
		`DELETE FROM agg_played_core`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for lib, a := range scoped {
		if lib != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO dim_library (name) VALUES (?)`, lib); err != nil {
				return err
			}
		}
		if err := insertTotals(ctx, tx, lib, a.Totals); err != nil {
			return err
		}

		disks := []struct {
			name string
			data []aggregate.DiskBucket
		}{
			{"resolution", a.DiskByResolution},
			{"codec", a.DiskByCodec},
			{"container", a.DiskByContainer},
			{"library", a.DiskByLibrary},
		}
		for _, d := range disks {
			for _, b := range d.data {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO agg_disk (library, dimension, bucket, bytes, items) VALUES (?,?,?,?,?)`,
					lib, d.name, b.Bucket, b.Bytes, b.Items); err != nil {
					return err
				}
			}
		}

		distros := []struct {
			name string
			data []aggregate.LabeledCount
		}{
			{"genre", a.GenresTop},
			{"decade", a.ByDecade},
			{"library_items", a.ItemsByLibrary},
			{"tag", a.TagsTop},
		}
		for _, d := range distros {
			for _, lc := range d.data {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO agg_distribution (library, dimension, bucket, items) VALUES (?,?,?,?)`,
					lib, d.name, lc.Label, lc.Count); err != nil {
					return err
				}
			}
		}

		for _, g := range a.Growth {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_library_growth (library, month, added_items, added_bytes, cum_items) VALUES (?,?,?,?,?)`,
				lib, g.Month, g.AddedItems, g.AddedBytes, g.CumItems); err != nil {
				return err
			}
		}

		for _, p := range a.TagPairs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_library_tag_pairs (library, tag_a, tag_b, items) VALUES (?,?,?,?)`,
				lib, p.A, p.B, p.Items); err != nil {
				return err
			}
		}
	}

	for _, c := range cleanup {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_cleanup (item_id, scope, name, library, bytes, episodes, added_at, last_played_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			c.ItemID, c.Scope, c.Name, c.Library, c.Bytes, c.Episodes, c.AddedAt, nullif(c.LastPlayedAt),
		); err != nil {
			return err
		}
	}
	for _, u := range users {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dim_user (id, name) VALUES (?,?)`, u.ID, u.Name); err != nil {
			return err
		}
	}
	for _, p := range core {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_played_core (user_id, scope, item_id, name, play_count, last_played_at, library)
			VALUES (?,?,?,?,?,?,?)`,
			p.UserID, p.Scope, p.ItemID, p.Name, p.PlayCount, nullif(p.LastPlayedAt), p.Library,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// nullif turns "" into a SQL NULL so `IS NULL` filters work as intended.
func nullif(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func insertTotals(ctx context.Context, tx *sql.Tx, library string, totals map[string]float64) error {
	for k, v := range totals {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_totals (library, metric, value) VALUES (?,?,?)`, library, k, v); err != nil {
			return err
		}
	}
	return nil
}
