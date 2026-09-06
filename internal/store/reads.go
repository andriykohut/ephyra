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

type TagPairDTO struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Items int64  `json:"items"`
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
	Tags             struct {
		Coverage struct {
			Tagged int64 `json:"tagged"`
			Total  int64 `json:"total"`
		} `json:"coverage"`
		Top   []aggregate.LabeledCount `json:"top"`
		Pairs []TagPairDTO             `json:"pairs"`
	} `json:"tags"`
}

// ReadLibraries returns every known library name, sorted by item count desc
// then name — "Unknown" included if any item resolved to it. Backs the
// filter dropdown on every library-aware page.
func (s *Store) ReadLibraries(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.name, COALESCE(t.items, 0) AS items
		FROM dim_library d
		LEFT JOIN agg_distribution t ON t.library = '' AND t.dimension = 'library_items' AND t.bucket = d.name
		ORDER BY items DESC, d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var items int64
		if err := rows.Scan(&name, &items); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return orEmpty(out), rows.Err()
}

func (s *Store) ReadLibraryOverview(ctx context.Context, library string) (LibraryOverview, error) {
	var ov LibraryOverview

	totals := map[string]float64{}
	rows, err := s.db.QueryContext(ctx, `SELECT metric, value FROM agg_totals WHERE library = ?`, library)
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
	ov.Tags.Coverage.Tagged = int64(totals["tags.tagged_items"])
	ov.Tags.Coverage.Total = int64(totals["tags.total_items"])

	if ov.DiskByResolution, err = s.readDisk(ctx, library, "resolution"); err != nil {
		return ov, err
	}
	if ov.DiskByCodec, err = s.readDisk(ctx, library, "codec"); err != nil {
		return ov, err
	}
	if ov.DiskByContainer, err = s.readDisk(ctx, library, "container"); err != nil {
		return ov, err
	}
	if ov.DiskByLibrary, err = s.readDisk(ctx, library, "library"); err != nil {
		return ov, err
	}

	if ov.GenresTop, err = s.readDistro(ctx, library, "genre", false); err != nil {
		return ov, err
	}
	if ov.ByDecade, err = s.readDistro(ctx, library, "decade", true); err != nil {
		return ov, err
	}
	if ov.Totals.ItemsByLibrary, err = s.readDistro(ctx, library, "library_items", false); err != nil {
		return ov, err
	}
	if ov.Tags.Top, err = s.readDistro(ctx, library, "tag", false); err != nil {
		return ov, err
	}
	if ov.Tags.Pairs, err = s.readTagPairs(ctx, library); err != nil {
		return ov, err
	}

	gr, err := s.db.QueryContext(ctx,
		`SELECT month, added_items, added_bytes, cum_items FROM agg_library_growth WHERE library = ? ORDER BY month ASC`, library)
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
	ov.Tags.Top = orEmpty(ov.Tags.Top)
	ov.Tags.Pairs = orEmpty(ov.Tags.Pairs)
	return ov, nil
}

func (s *Store) readTagPairs(ctx context.Context, library string) ([]TagPairDTO, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT tag_a, tag_b, items FROM agg_library_tag_pairs WHERE library = ? ORDER BY items DESC, tag_a, tag_b`, library)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []TagPairDTO
	for r.Next() {
		var p TagPairDTO
		if err := r.Scan(&p.A, &p.B, &p.Items); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, r.Err()
}

func (s *Store) readDisk(ctx context.Context, library, dim string) ([]aggregate.DiskBucket, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT bucket, bytes, items FROM agg_disk WHERE library = ? AND dimension = ?`, library, dim)
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

func (s *Store) readDistro(ctx context.Context, library, dim string, chrono bool) ([]aggregate.LabeledCount, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT bucket, items FROM agg_distribution WHERE library = ? AND dimension = ?`, library, dim)
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
