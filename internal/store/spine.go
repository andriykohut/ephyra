package store

import (
	"context"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func joinGenres(gs []string) string { return strings.Join(gs, "|") }
func splitGenres(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "|")
}

func joinTags(ts []string) string { return strings.Join(ts, "|") }
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "|")
}

// spineTimeLayout is how playback_events.at is stored: the plugin's wall-clock
// components, no zone. Kept parseable so ReadPlaybackEvents can hand aggregate
// a time.Time whose literal fields match what the plugin wrote.
const spineTimeLayout = "2006-01-02 15:04:05"

func formatSpineTime(t time.Time) string { return t.Format(spineTimeLayout) }

func parseSpineTime(s string) time.Time {
	t, err := time.Parse(spineTimeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// AppendPlaybackEvents inserts events the spine has not seen and refreshes the
// mutable columns on the ones it has: the enrichment (item_name / series_* /
// the library-fact snapshot) plus play_duration_sec and method, which the
// plugin rewrites in place while a session is still open. Its own transaction;
// idempotent.
//
// A play is identified by (at, user_id, item_id) -- what the plugin keys its
// own row on. Two genuinely distinct plays by one user of one item in the same
// clock-second collapse into one row; accepted, and vanishingly rare.
func (s *Store) AppendPlaybackEvents(ctx context.Context, evs []source.PlaybackEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	const q = `
		INSERT INTO playback_events
		  (at, user_id, item_id, item_type, method, play_duration_sec,
		   item_name, series_id, series_name, item_runtime_sec, item_year, item_genres, item_tags, library)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(at, user_id, item_id) DO UPDATE SET
		  method            = excluded.method,
		  play_duration_sec = excluded.play_duration_sec,
		  item_name         = excluded.item_name,
		  series_id         = excluded.series_id,
		  series_name       = excluded.series_name,
		  item_runtime_sec  = excluded.item_runtime_sec,
		  item_year         = excluded.item_year,
		  item_genres       = excluded.item_genres,
		  item_tags         = excluded.item_tags,
		  library           = excluded.library`
	for _, e := range evs {
		if _, err := tx.ExecContext(ctx, q,
			formatSpineTime(e.At), e.UserID, e.ItemID, e.ItemType, e.Method, e.PlayDurationSec,
			e.ItemName, e.SeriesID, e.SeriesName,
			e.ItemRuntimeSec, e.ItemYear, joinGenres(e.ItemGenres), joinTags(e.ItemTags), e.Library,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadPlaybackEvents returns the whole spine, oldest first, including the
// library-fact snapshot (runtime / year / genres) captured at ingest and
// refreshed whenever the plugin re-reports the play while the item still exists.
func (s *Store) ReadPlaybackEvents(ctx context.Context) ([]source.PlaybackEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT at, user_id, item_id, item_type, method, play_duration_sec,
		       item_name, series_id, series_name, item_runtime_sec, item_year, item_genres, item_tags, library
		FROM playback_events ORDER BY at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []source.PlaybackEvent
	for rows.Next() {
		var e source.PlaybackEvent
		var atRaw, genres, tags string
		if err := rows.Scan(&atRaw, &e.UserID, &e.ItemID, &e.ItemType, &e.Method,
			&e.PlayDurationSec, &e.ItemName, &e.SeriesID, &e.SeriesName,
			&e.ItemRuntimeSec, &e.ItemYear, &genres, &tags, &e.Library); err != nil {
			return nil, err
		}
		e.At = parseSpineTime(atRaw)
		e.ItemGenres = splitGenres(genres)
		e.ItemTags = splitTags(tags)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ItemsWithUnknownLibrary returns distinct item ids whose spine rows are
// still at the 'Unknown' library sentinel — candidates for the watch job's
// residual backfill sweep. Items that never resolve (e.g. deleted from
// Jellyfin) stay candidates and get re-queried on every subsequent run, not
// just once.
func (s *Store) ItemsWithUnknownLibrary(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT item_id FROM playback_events WHERE library = 'Unknown'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UpdatePlaybackLibraries sets library on every spine row for each item id in
// resolved. One transaction; a no-op for ids not present in resolved.
func (s *Store) UpdatePlaybackLibraries(ctx context.Context, resolved map[string]string) error {
	if len(resolved) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, lib := range resolved {
		if _, err := tx.ExecContext(ctx,
			`UPDATE playback_events SET library = ? WHERE item_id = ?`, lib, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type PlayRow struct {
	At              string `json:"at"`
	ItemID          string `json:"item_id"`
	ItemType        string `json:"item_type"`
	Name            string `json:"name"`
	SeriesName      string `json:"series_name"`
	PlayDurationSec int64  `json:"play_duration_sec"`
	Played          bool   `json:"played"`
	JFURL           string `json:"jf_url"`
}

// PlayCursor's At alone is not unique -- two plays can share a timestamp --
// so RowID breaks the tie.
type PlayCursor struct {
	At    string `json:"at"`
	RowID int64  `json:"row_id"`
}

// ReadPlays is the one handler path that reads playback_events directly
// instead of an agg_* table: the play history is a raw list, not a rollup,
// so there is nothing to pre-aggregate. Pages newest-first via a keyset
// cursor on (at, rowid) rather than OFFSET, since the spine only grows.
// jf_url needs no dim_jf_ref lookup -- item_id already is the API's item id.
func (s *Store) ReadPlays(ctx context.Context, userID, library string,
	before *PlayCursor, limit int,
) ([]PlayRow, *PlayCursor, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	base, serverID := s.jellyfinLinks()
	q := `
		SELECT pe.rowid, pe.at, pe.item_id, pe.item_type, pe.item_name,
		       pe.series_name, pe.play_duration_sec,
		       COALESCE(dp.played, 0)
		FROM playback_events pe
		LEFT JOIN dim_played dp ON dp.user_id = pe.user_id AND dp.item_id = pe.item_id
		WHERE pe.user_id = ?`
	args := []any{userID}
	if library != "" {
		q += ` AND pe.library = ?`
		args = append(args, library)
	}
	if before != nil {
		// Tuple comparison, not `at < ?`: two plays can share a timestamp, and
		// the rowid is what breaks the tie in both the filter and the order.
		q += ` AND (pe.at < ? OR (pe.at = ? AND pe.rowid < ?))`
		args = append(args, before.At, before.At, before.RowID)
	}
	q += ` ORDER BY pe.at DESC, pe.rowid DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var out []PlayRow
	var rowIDs []int64
	for rows.Next() {
		var r PlayRow
		var rowID int64
		if err := rows.Scan(&rowID, &r.At, &r.ItemID, &r.ItemType, &r.Name,
			&r.SeriesName, &r.PlayDurationSec, &r.Played); err != nil {
			return nil, nil, err
		}
		r.JFURL = jfLink(base, serverID, "item", r.ItemID)
		out = append(out, r)
		rowIDs = append(rowIDs, rowID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	var next *PlayCursor
	if len(out) > limit {
		out = out[:limit]
		next = &PlayCursor{At: out[limit-1].At, RowID: rowIDs[limit-1]}
	}
	return orEmpty(out), next, nil
}

// SpineCoverage is the min/max play date and total row count, for the API
// coverage block. Dates are YYYY-MM-DD; empty strings when the spine is empty.
func (s *Store) SpineCoverage(ctx context.Context) (first, last string, total int64, err error) {
	var f, l *string
	err = s.db.QueryRowContext(ctx,
		`SELECT substr(MIN(at),1,10), substr(MAX(at),1,10), COUNT(*) FROM playback_events`,
	).Scan(&f, &l, &total)
	if err != nil {
		return "", "", 0, err
	}
	if f != nil {
		first = *f
	}
	if l != nil {
		last = *l
	}
	return first, last, total, nil
}
