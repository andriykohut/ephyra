package store

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"time"
)

type CleanupParams struct {
	Mode  string
	Sort  string
	Limit int
	Now   time.Time
}

type CleanupItem struct {
	ItemID       string  `json:"item_id"`
	Scope        string  `json:"scope"`
	Name         string  `json:"name"`
	Library      string  `json:"library"`
	Bytes        int64   `json:"bytes"`
	Episodes     int64   `json:"episodes"`
	AddedAt      string  `json:"added_at"`
	LastPlayedAt *string `json:"last_played_at"`
}

type CleanupResult struct {
	Mode             string        `json:"mode"`
	ReclaimableBytes int64         `json:"reclaimable_bytes"`
	MatchCount       int64         `json:"match_count"`
	Truncated        bool          `json:"truncated"`
	Items            []CleanupItem `json:"items"`
}

// cleanupWhere returns the SQL predicate + args for a mode. The stale cutoff is
// now-365d in RFC3339 so it compares lexically against the stored value.
func cleanupWhere(mode string, now time.Time) (string, []any) {
	if mode == "stale" {
		cut := now.AddDate(-1, 0, 0).UTC().Format(time.RFC3339)
		return `last_played_at IS NOT NULL AND last_played_at < ?`, []any{cut}
	}
	return `last_played_at IS NULL`, nil
}

func (s *Store) ReadCleanup(ctx context.Context, p CleanupParams) (CleanupResult, error) {
	where, args := cleanupWhere(p.Mode, p.Now)
	res := CleanupResult{Mode: p.Mode}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(bytes),0) FROM agg_cleanup WHERE `+where, args...,
	).Scan(&res.MatchCount, &res.ReclaimableBytes); err != nil {
		return res, err
	}

	order := `bytes DESC, name ASC`
	if p.Sort == "added" {
		order = `added_at ASC, name ASC`
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, scope, name, library, bytes, episodes, added_at, last_played_at
		   FROM agg_cleanup WHERE `+where+` ORDER BY `+order+` LIMIT ?`,
		append(append([]any{}, args...), limit)...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var it CleanupItem
		var last *string
		if err := rows.Scan(&it.ItemID, &it.Scope, &it.Name, &it.Library, &it.Bytes, &it.Episodes, &it.AddedAt, &last); err != nil {
			return res, err
		}
		it.LastPlayedAt = last
		res.Items = append(res.Items, it)
	}
	res.Truncated = res.MatchCount > int64(len(res.Items))
	return res, rows.Err()
}

// StreamCleanupCSV writes all matching rows (no limit) as CSV.
func (s *Store) StreamCleanupCSV(ctx context.Context, mode string, now time.Time, w io.Writer) error {
	where, args := cleanupWhere(mode, now)
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, scope, name, library, bytes, episodes, added_at, last_played_at
		   FROM agg_cleanup WHERE `+where+` ORDER BY bytes DESC, name ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"item_id", "scope", "name", "library", "bytes", "episodes", "added_at", "last_played_at"}); err != nil {
		return err
	}
	for rows.Next() {
		var id, scope, name, lib, added string
		var bytes, eps int64
		var last *string
		if err := rows.Scan(&id, &scope, &name, &lib, &bytes, &eps, &added, &last); err != nil {
			return err
		}
		lp := ""
		if last != nil {
			lp = *last
		}
		if err := cw.Write([]string{id, scope, name, lib, strconv.FormatInt(bytes, 10), strconv.FormatInt(eps, 10), added, lp}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}
