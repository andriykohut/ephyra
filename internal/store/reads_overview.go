package store

import (
	"context"
	"database/sql"
	"time"
)

type ActivityDay struct {
	Day      string `json:"day"`
	WatchSec int64  `json:"watch_sec"`
}

type GenreDTO struct {
	Key      string `json:"key"`
	WatchSec int64  `json:"watch_sec"`
	Plays    int64  `json:"plays"`
	JFURL    string `json:"jf_url"`
}

type ProfileOverview struct {
	User     ProfileUser   `json:"user"`
	Since    string        `json:"since"`
	Plays    int64         `json:"plays"`
	WatchSec int64         `json:"watch_sec"`
	Activity []ActivityDay `json:"activity"`
	People   []PersonDTO   `json:"people"`
	Items    []TopItemDTO  `json:"items"`
	Genres   []GenreDTO    `json:"genres"`
}

// ReadProfileOverview composes the profile page's overview payload. Like
// ReadProfile, the bool reports whether userID exists in dim_user at all --
// distinct from a known user with an empty spine, which still gets zeroed
// panels rather than a 404.
func (s *Store) ReadProfileOverview(ctx context.Context, userID, rng, library string, now time.Time) (ProfileOverview, bool, error) {
	out := ProfileOverview{User: ProfileUser{ID: userID}}

	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM dim_user WHERE id = ?`, userID,
	).Scan(&out.User.Name)
	if err == sql.ErrNoRows {
		return ProfileOverview{}, false, nil
	}
	if err != nil {
		return ProfileOverview{}, false, err
	}

	err = s.db.QueryRowContext(ctx, `
		SELECT watch_sec, plays
		FROM agg_profile_summary WHERE user_id = ? AND range = ? AND library = ?`,
		userID, rng, library,
	).Scan(&out.WatchSec, &out.Plays)
	if err != nil && err != sql.ErrNoRows {
		return ProfileOverview{}, false, err
	}

	// "Watching since" is the spine's earliest play, not the selected range's --
	// range='all' is the only row whose first_play isn't cut off at the range
	// window (see aggregate.Profiles / summaryRow).
	err = s.db.QueryRowContext(ctx, `
		SELECT first_play FROM agg_profile_summary WHERE user_id = ? AND range = 'all' AND library = ?`,
		userID, library,
	).Scan(&out.Since)
	if err != nil && err != sql.ErrNoRows {
		return ProfileOverview{}, false, err
	}

	// The strip is a fixed year-of-days view, like a contribution graph -- it
	// is deliberately independent of rng and can't render more than this
	// anyway, so bound it rather than let the payload grow with the spine.
	q := `SELECT day, SUM(watch_sec) FROM watch_events_daily WHERE user_id = ? AND day >= ?`
	args := []any{userID, now.AddDate(0, 0, -365).Format("2006-01-02")}
	if library != "" {
		q += ` AND library = ?`
		args = append(args, library)
	}
	q += ` GROUP BY day ORDER BY day`
	arows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return ProfileOverview{}, false, err
	}
	defer arows.Close()
	for arows.Next() {
		var a ActivityDay
		if err := arows.Scan(&a.Day, &a.WatchSec); err != nil {
			return ProfileOverview{}, false, err
		}
		out.Activity = append(out.Activity, a)
	}
	if err := arows.Err(); err != nil {
		return ProfileOverview{}, false, err
	}

	out.People, err = s.ReadProfilePeople(ctx, userID, rng, library)
	if err != nil {
		return ProfileOverview{}, false, err
	}
	out.Items, err = s.ReadProfileTopItems(ctx, userID, rng, library)
	if err != nil {
		return ProfileOverview{}, false, err
	}

	base, serverID := s.jellyfinLinks()
	grows, err := s.db.QueryContext(ctx, `
		SELECT t.key, t.watch_sec, t.plays, COALESCE(r.jf_id, '')
		FROM agg_profile_taste t
		LEFT JOIN dim_jf_ref r ON r.kind = 'genre' AND r.name = t.key
		WHERE t.user_id = ? AND t.range = ? AND t.library = ? AND t.dim = 'genre'
		ORDER BY t.watch_sec DESC, t.key`, userID, rng, library)
	if err != nil {
		return ProfileOverview{}, false, err
	}
	defer grows.Close()
	for grows.Next() {
		var g GenreDTO
		var jfID string
		if err := grows.Scan(&g.Key, &g.WatchSec, &g.Plays, &jfID); err != nil {
			return ProfileOverview{}, false, err
		}
		g.JFURL = jfLink(base, serverID, "genre", jfID)
		out.Genres = append(out.Genres, g)
	}
	if err := grows.Err(); err != nil {
		return ProfileOverview{}, false, err
	}

	out.Activity = orEmpty(out.Activity)
	out.Genres = orEmpty(out.Genres)
	return out, true, nil
}
