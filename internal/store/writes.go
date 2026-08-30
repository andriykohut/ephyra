package store

import (
	"context"
	"database/sql"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WriteLibraryAggregates replaces every library-derived row in one transaction,
// so a reader never sees a half-written set.
func (s *Store) WriteLibraryAggregates(ctx context.Context, a aggregate.LibraryAggregates) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_totals`,
		`DELETE FROM agg_disk`,
		`DELETE FROM agg_distribution WHERE dimension IN ('genre','decade','library_items')`,
		`DELETE FROM agg_library_growth`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	if err := insertTotals(ctx, tx, a.Totals); err != nil {
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
				`INSERT INTO agg_disk (dimension, bucket, bytes, items) VALUES (?,?,?,?)`,
				d.name, b.Bucket, b.Bytes, b.Items); err != nil {
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
	}
	for _, d := range distros {
		for _, lc := range d.data {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_distribution (dimension, bucket, items) VALUES (?,?,?)`,
				d.name, lc.Label, lc.Count); err != nil {
				return err
			}
		}
	}

	for _, g := range a.Growth {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_library_growth (month, added_items, added_bytes, cum_items) VALUES (?,?,?,?)`,
			g.Month, g.AddedItems, g.AddedBytes, g.CumItems); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func insertTotals(ctx context.Context, tx *sql.Tx, totals map[string]float64) error {
	for k, v := range totals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agg_totals (metric, value) VALUES (?,?)`, k, v); err != nil {
			return err
		}
	}
	return nil
}
