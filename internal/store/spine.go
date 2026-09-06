package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strconv"
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

// spineDedupHash identifies a plugin session by its immutable coordinates. Two
// genuinely distinct plays that share all four collapse to one row; that is
// accepted (see the design doc, section 4.3).
func spineDedupHash(ev source.PlaybackEvent) string {
	parts := strings.Join([]string{
		formatSpineTime(ev.At),
		ev.UserID,
		ev.ItemID,
		strconv.FormatInt(ev.PlayDurationSec, 10),
	}, "\x1f")
	sum := sha1.Sum([]byte(parts))
	return hex.EncodeToString(sum[:])
}

// AppendPlaybackEvents inserts events the spine has not seen and refreshes the
// enrichable columns (item_name / series_* / the library-fact snapshot) on the
// ones it has. Its own transaction; idempotent.
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
		   item_name, series_id, series_name, item_runtime_sec, item_year, item_genres, item_tags, library, dedup_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dedup_hash) DO UPDATE SET
		  item_name        = excluded.item_name,
		  series_id        = excluded.series_id,
		  series_name      = excluded.series_name,
		  item_runtime_sec = excluded.item_runtime_sec,
		  item_year        = excluded.item_year,
		  item_genres      = excluded.item_genres,
		  item_tags        = excluded.item_tags,
		  library          = excluded.library`
	for _, e := range evs {
		if _, err := tx.ExecContext(ctx, q,
			formatSpineTime(e.At), e.UserID, e.ItemID, e.ItemType, e.Method, e.PlayDurationSec,
			e.ItemName, e.SeriesID, e.SeriesName,
			e.ItemRuntimeSec, e.ItemYear, joinGenres(e.ItemGenres), joinTags(e.ItemTags), e.Library, spineDedupHash(e),
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
// one-time backfill pass.
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
