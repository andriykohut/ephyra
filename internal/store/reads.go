package store

import (
	"context"
	"sort"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// orEmpty swaps a nil slice for an empty one so it marshals as [] rather than
// null. The SPA iterates these lists straight off the JSON response, and a null
// throws mid-render.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// LibraryOverview is the shape GET /api/library/overview returns (under "data").
type LibraryOverview struct {
	Totals struct {
		ItemsByLibrary []aggregate.LabeledCount `json:"items_by_library"`
		RuntimeSeconds int64                    `json:"runtime_seconds"`
		Bytes          int64                    `json:"bytes"`
		CountUHD       int64                    `json:"count_uhd"`
		CountHDR       int64                    `json:"count_hdr"`
		CountDV        int64                    `json:"count_dv"`
		Series         int64                    `json:"series"`
		Items          int64                    `json:"items"`
	} `json:"totals"`
	DiskByResolution []aggregate.DiskBucket   `json:"disk_by_resolution"`
	DiskByCodec      []aggregate.DiskBucket   `json:"disk_by_codec"`
	DiskByContainer  []aggregate.DiskBucket   `json:"disk_by_container"`
	DiskByLibrary    []aggregate.DiskBucket   `json:"disk_by_library"`
	GenresTop        []aggregate.LabeledCount `json:"genres_top"`
	ByDecade         []aggregate.LabeledCount `json:"by_decade"`
	Growth           []aggregate.GrowthPoint  `json:"growth"`
}

func (s *Store) ReadLibraryOverview(ctx context.Context) (LibraryOverview, error) {
	var ov LibraryOverview

	totals := map[string]float64{}
	rows, err := s.db.QueryContext(ctx, `SELECT metric, value FROM agg_totals`)
	if err != nil {
		return ov, err
	}
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return ov, err
		}
		totals[k] = v
	}
	rows.Close()
	ov.Totals.RuntimeSeconds = int64(totals["runtime_sec.total"])
	ov.Totals.Bytes = int64(totals["bytes.total"])
	ov.Totals.CountUHD = int64(totals["count.uhd"])
	ov.Totals.CountHDR = int64(totals["count.hdr"])
	ov.Totals.CountDV = int64(totals["count.dv"])
	ov.Totals.Series = int64(totals["items.Series"])
	ov.Totals.Items = int64(totals["items.total"])

	if ov.DiskByResolution, err = s.readDisk(ctx, "resolution"); err != nil {
		return ov, err
	}
	if ov.DiskByCodec, err = s.readDisk(ctx, "codec"); err != nil {
		return ov, err
	}
	if ov.DiskByContainer, err = s.readDisk(ctx, "container"); err != nil {
		return ov, err
	}
	if ov.DiskByLibrary, err = s.readDisk(ctx, "library"); err != nil {
		return ov, err
	}

	if ov.GenresTop, err = s.readDistro(ctx, "genre", false); err != nil {
		return ov, err
	}
	if ov.ByDecade, err = s.readDistro(ctx, "decade", true); err != nil {
		return ov, err
	}
	if ov.Totals.ItemsByLibrary, err = s.readDistro(ctx, "library_items", false); err != nil {
		return ov, err
	}

	gr, err := s.db.QueryContext(ctx,
		`SELECT month, added_items, added_bytes, cum_items FROM agg_library_growth ORDER BY month ASC`)
	if err != nil {
		return ov, err
	}
	defer gr.Close()
	for gr.Next() {
		var g aggregate.GrowthPoint
		if err := gr.Scan(&g.Month, &g.AddedItems, &g.AddedBytes, &g.CumItems); err != nil {
			return ov, err
		}
		ov.Growth = append(ov.Growth, g)
	}
	if err := gr.Err(); err != nil {
		return ov, err
	}

	ov.Totals.ItemsByLibrary = orEmpty(ov.Totals.ItemsByLibrary)
	ov.DiskByResolution = orEmpty(ov.DiskByResolution)
	ov.DiskByCodec = orEmpty(ov.DiskByCodec)
	ov.DiskByContainer = orEmpty(ov.DiskByContainer)
	ov.DiskByLibrary = orEmpty(ov.DiskByLibrary)
	ov.GenresTop = orEmpty(ov.GenresTop)
	ov.ByDecade = orEmpty(ov.ByDecade)
	ov.Growth = orEmpty(ov.Growth)
	return ov, nil
}

func (s *Store) readDisk(ctx context.Context, dim string) ([]aggregate.DiskBucket, error) {
	r, err := s.db.QueryContext(ctx, `SELECT bucket, bytes, items FROM agg_disk WHERE dimension = ?`, dim)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []aggregate.DiskBucket
	for r.Next() {
		var b aggregate.DiskBucket
		if err := r.Scan(&b.Bucket, &b.Bytes, &b.Items); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Bucket < out[j].Bucket
	})
	return out, r.Err()
}

func (s *Store) readDistro(ctx context.Context, dim string, chrono bool) ([]aggregate.LabeledCount, error) {
	r, err := s.db.QueryContext(ctx, `SELECT bucket, items FROM agg_distribution WHERE dimension = ?`, dim)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []aggregate.LabeledCount
	for r.Next() {
		var lc aggregate.LabeledCount
		if err := r.Scan(&lc.Label, &lc.Count); err != nil {
			return nil, err
		}
		out = append(out, lc)
	}
	if chrono {
		sort.Slice(out, func(i, j int) bool {
			ui, uj := out[i].Label == "Unknown", out[j].Label == "Unknown"
			if ui != uj {
				return uj
			}
			return out[i].Label < out[j].Label
		})
	} else {
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Label < out[j].Label
		})
	}
	return out, r.Err()
}

// IsStale reports whether a job's data should be shown with a "catching up"
// hint: no row at all, the last run failed, or it's older than 2x the interval.
func IsStale(m RefreshMeta, ok bool, interval time.Duration, now time.Time) bool {
	if !ok || !m.OK {
		return true
	}
	return now.Sub(m.LastRunAt) > 2*interval
}
