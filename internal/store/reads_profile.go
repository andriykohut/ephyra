package store

import (
	"context"
	"database/sql"
	"sort"
)

// --- GET /api/profile ---

type ProfileListEntry struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	TotalWatchSec        int64   `json:"total_watch_sec"`
	TotalPlays           int64   `json:"total_plays"`
	FinishedPct          float64 `json:"finished_pct"`
	RewatchPct           float64 `json:"rewatch_pct"`
	LongestBingeEpisodes int64   `json:"longest_binge_episodes"`
	LastPlay             string  `json:"last_play"`
}

type ProfileList struct {
	PluginAvailable bool               `json:"plugin_available"`
	Coverage        WatchCoverage      `json:"coverage"`
	Users           []ProfileListEntry `json:"users"`
}

// --- GET /api/profile/{userID} ---

type ProfileUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProfileLongestBinge struct {
	Episodes   int64  `json:"episodes"`
	SeriesName string `json:"series_name"`
}
type ProfileShowOfRange struct {
	SeriesID   string `json:"series_id"`
	SeriesName string `json:"series_name"`
}
type ProfileSummary struct {
	WatchSec       int64               `json:"watch_sec"`
	Plays          int64               `json:"plays"`
	DistinctTitles int64               `json:"distinct_titles"`
	DaysActive     int64               `json:"days_active"`
	FinishedPct    float64             `json:"finished_pct"`
	BailedPct      float64             `json:"bailed_pct"`
	RewatchPct     float64             `json:"rewatch_pct"`
	LongestBinge   ProfileLongestBinge `json:"longest_binge"`
	ShowOfRange    ProfileShowOfRange  `json:"show_of_range"`
	FirstPlay      string              `json:"first_play"`
	LastPlay       string              `json:"last_play"`
}

type ProfileCompletionSlice struct {
	Scope  string `json:"scope"`
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
}
type ProfileAbandoned struct {
	Scope       string `json:"scope"`
	ItemID      string `json:"item_id"`
	Name        string `json:"name"`
	SeriesName  string `json:"series_name"`
	BailedCount int64  `json:"bailed_count"`
}
type ProfileRewatch struct {
	Scope      string `json:"scope"`
	ItemID     string `json:"item_id"`
	Name       string `json:"name"`
	SeriesName string `json:"series_name"`
	WatchDays  int64  `json:"watch_days"`
}
type ProfileBinge struct {
	SeriesID    string `json:"series_id"`
	SeriesName  string `json:"series_name"`
	RunEpisodes int64  `json:"run_episodes"`
	RunStart    string `json:"run_start"`
	RunEnd      string `json:"run_end"`
}
type TasteEntry struct {
	Key      string `json:"key"`
	WatchSec int64  `json:"watch_sec"`
	Plays    int64  `json:"plays"`
}
type BaselineEntry struct {
	Key      string `json:"key"`
	WatchSec int64  `json:"watch_sec"`
}
type ProfileTaste struct {
	Genre           []TasteEntry `json:"genre"`
	Decade          []TasteEntry `json:"decade"`
	Length          []TasteEntry `json:"length"`
	SignatureGenres []string     `json:"signature_genres"`
}
type ProfileBaseline struct {
	Genre  []BaselineEntry `json:"genre"`
	Decade []BaselineEntry `json:"decade"`
	Length []BaselineEntry `json:"length"`
}
type Profile struct {
	Range      string                   `json:"range"`
	User       ProfileUser              `json:"user"`
	Summary    ProfileSummary           `json:"summary"`
	Completion []ProfileCompletionSlice `json:"completion"`
	Abandoned  []ProfileAbandoned       `json:"abandoned"`
	Rewatch    []ProfileRewatch         `json:"rewatch"`
	Binge      []ProfileBinge           `json:"binge"`
	Taste      ProfileTaste             `json:"taste"`
	Baseline   ProfileBaseline          `json:"baseline"`
}

// ReadProfileList is every user in dim_user with their lifetime headline
// numbers. Present even when the plugin is gone (zeros).
func (s *Store) ReadProfileList(ctx context.Context) (ProfileList, error) {
	var pl ProfileList

	if m, ok, err := s.GetRefreshMeta(ctx, "watch"); err == nil && ok {
		pl.PluginAvailable = m.OK && m.PluginAvailable
	}
	if f, l, total, err := s.SpineCoverage(ctx); err == nil {
		pl.Coverage = WatchCoverage{FirstPlay: f, LastPlay: l, TotalPlays: total}
	} else {
		return pl, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.name,
		       COALESCE(p.watch_sec, 0), COALESCE(p.plays, 0),
		       COALESCE(p.finished_pct, 0), COALESCE(p.rewatch_pct, 0),
		       COALESCE(p.longest_binge_episodes, 0), COALESCE(p.last_play, '')
		FROM dim_user d
		LEFT JOIN agg_profile_summary p ON p.user_id = d.id AND p.range = 'all'
		ORDER BY d.name`)
	if err != nil {
		return pl, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ProfileListEntry
		if err := rows.Scan(&e.ID, &e.Name, &e.TotalWatchSec, &e.TotalPlays,
			&e.FinishedPct, &e.RewatchPct, &e.LongestBingeEpisodes, &e.LastPlay); err != nil {
			return pl, err
		}
		pl.Users = append(pl.Users, e)
	}
	if err := rows.Err(); err != nil {
		return pl, err
	}
	pl.Users = orEmpty(pl.Users)
	return pl, nil
}

// ReadProfile is one user's panels for a range. ok is false when userID is not
// in dim_user. Range-scoped panels honour rng; rewatch / binge / the summary's
// rewatch_pct + longest_binge are lifetime and always come from the 'all' row.
func (s *Store) ReadProfile(ctx context.Context, userID, rng string) (Profile, bool, error) {
	out := Profile{Range: rng, User: ProfileUser{ID: userID}}

	err := s.db.QueryRowContext(ctx, `SELECT name FROM dim_user WHERE id = ?`, userID).Scan(&out.User.Name)
	if err == sql.ErrNoRows {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, err
	}

	if err := s.readProfileSummary(ctx, &out, userID, rng); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileCompletion(ctx, &out, userID, rng); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileAbandoned(ctx, &out, userID, rng); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileRewatch(ctx, &out, userID); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileBinge(ctx, &out, userID); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileTaste(ctx, &out, userID, rng); err != nil {
		return Profile{}, false, err
	}
	out.Taste.SignatureGenres = signatureGenres(out.Taste.Genre, out.Baseline.Genre)

	out.Completion = orEmpty(out.Completion)
	out.Abandoned = orEmpty(out.Abandoned)
	out.Rewatch = orEmpty(out.Rewatch)
	out.Binge = orEmpty(out.Binge)
	out.Taste.Genre = orEmpty(out.Taste.Genre)
	out.Taste.Decade = orEmpty(out.Taste.Decade)
	out.Taste.Length = orEmpty(out.Taste.Length)
	out.Taste.SignatureGenres = orEmpty(out.Taste.SignatureGenres)
	out.Baseline.Genre = orEmpty(out.Baseline.Genre)
	out.Baseline.Decade = orEmpty(out.Baseline.Decade)
	out.Baseline.Length = orEmpty(out.Baseline.Length)
	return out, true, nil
}

func (s *Store) readProfileSummary(ctx context.Context, out *Profile, userID, rng string) error {
	err := s.db.QueryRowContext(ctx, `
		SELECT watch_sec, plays, distinct_titles, days_active, finished_pct, bailed_pct,
		       show_of_range_series_id, show_of_range_series_name, first_play, last_play
		FROM agg_profile_summary WHERE user_id = ? AND range = ?`, userID, rng,
	).Scan(&out.Summary.WatchSec, &out.Summary.Plays, &out.Summary.DistinctTitles,
		&out.Summary.DaysActive, &out.Summary.FinishedPct, &out.Summary.BailedPct,
		&out.Summary.ShowOfRange.SeriesID, &out.Summary.ShowOfRange.SeriesName,
		&out.Summary.FirstPlay, &out.Summary.LastPlay)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// Lifetime fields always come from the 'all' row, so every response carries them.
	var rp float64
	var lbe int64
	var lbs string
	e := s.db.QueryRowContext(ctx, `
		SELECT rewatch_pct, longest_binge_episodes, longest_binge_series_name
		FROM agg_profile_summary WHERE user_id = ? AND range = 'all'`, userID,
	).Scan(&rp, &lbe, &lbs)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	out.Summary.RewatchPct = rp
	out.Summary.LongestBinge = ProfileLongestBinge{Episodes: lbe, SeriesName: lbs}
	return nil
}

func (s *Store) readProfileCompletion(ctx context.Context, out *Profile, userID, rng string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, bucket, count FROM agg_profile_completion
		WHERE user_id = ? AND range = ? ORDER BY scope, bucket`, userID, rng)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c ProfileCompletionSlice
		if err := rows.Scan(&c.Scope, &c.Bucket, &c.Count); err != nil {
			return err
		}
		out.Completion = append(out.Completion, c)
	}
	return rows.Err()
}

func (s *Store) readProfileAbandoned(ctx context.Context, out *Profile, userID, rng string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, item_id, name, series_name, bailed_count FROM agg_profile_abandoned
		WHERE user_id = ? AND range = ? ORDER BY bailed_count DESC, item_id`, userID, rng)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a ProfileAbandoned
		if err := rows.Scan(&a.Scope, &a.ItemID, &a.Name, &a.SeriesName, &a.BailedCount); err != nil {
			return err
		}
		out.Abandoned = append(out.Abandoned, a)
	}
	return rows.Err()
}

func (s *Store) readProfileRewatch(ctx context.Context, out *Profile, userID string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, item_id, name, series_name, watch_days FROM agg_profile_rewatch
		WHERE user_id = ? ORDER BY watch_days DESC, item_id`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r ProfileRewatch
		if err := rows.Scan(&r.Scope, &r.ItemID, &r.Name, &r.SeriesName, &r.WatchDays); err != nil {
			return err
		}
		out.Rewatch = append(out.Rewatch, r)
	}
	return rows.Err()
}

func (s *Store) readProfileBinge(ctx context.Context, out *Profile, userID string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT series_id, series_name, run_episodes, run_start, run_end FROM agg_profile_binge
		WHERE user_id = ? ORDER BY run_episodes DESC, run_end DESC`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b ProfileBinge
		if err := rows.Scan(&b.SeriesID, &b.SeriesName, &b.RunEpisodes, &b.RunStart, &b.RunEnd); err != nil {
			return err
		}
		out.Binge = append(out.Binge, b)
	}
	return rows.Err()
}

// readProfileTaste fills out.Taste.{Genre,Decade,Length} for the range and
// out.Baseline.* library-wide.
func (s *Store) readProfileTaste(ctx context.Context, out *Profile, userID, rng string) error {
	trows, err := s.db.QueryContext(ctx, `
		SELECT dim, key, watch_sec, plays FROM agg_profile_taste
		WHERE user_id = ? AND range = ? ORDER BY dim, watch_sec DESC, key`, userID, rng)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var dim string
		var e TasteEntry
		if err := trows.Scan(&dim, &e.Key, &e.WatchSec, &e.Plays); err != nil {
			return err
		}
		switch dim {
		case "genre":
			out.Taste.Genre = append(out.Taste.Genre, e)
		case "decade":
			out.Taste.Decade = append(out.Taste.Decade, e)
		case "length":
			out.Taste.Length = append(out.Taste.Length, e)
		}
	}
	if err := trows.Err(); err != nil {
		return err
	}

	brows, err := s.db.QueryContext(ctx, `
		SELECT dim, key, watch_sec FROM agg_taste_baseline ORDER BY dim, watch_sec DESC, key`)
	if err != nil {
		return err
	}
	defer brows.Close()
	for brows.Next() {
		var dim string
		var e BaselineEntry
		if err := brows.Scan(&dim, &e.Key, &e.WatchSec); err != nil {
			return err
		}
		switch dim {
		case "genre":
			out.Baseline.Genre = append(out.Baseline.Genre, e)
		case "decade":
			out.Baseline.Decade = append(out.Baseline.Decade, e)
		case "length":
			out.Baseline.Length = append(out.Baseline.Length, e)
		}
	}
	return brows.Err()
}

// signatureGenres picks up to three genres where the user's watch-time share
// most exceeds the library baseline's share.
func signatureGenres(userGenre []TasteEntry, baseGenre []BaselineEntry) []string {
	var userTotal, baseTotal int64
	for _, g := range userGenre {
		userTotal += g.WatchSec
	}
	for _, g := range baseGenre {
		baseTotal += g.WatchSec
	}
	if userTotal == 0 {
		return nil
	}
	baseShare := map[string]float64{}
	if baseTotal > 0 {
		for _, g := range baseGenre {
			baseShare[g.Key] = float64(g.WatchSec) / float64(baseTotal)
		}
	}

	type scored struct {
		key   string
		delta float64
		share float64
	}
	var xs []scored
	for _, g := range userGenre {
		us := float64(g.WatchSec) / float64(userTotal)
		xs = append(xs, scored{key: g.Key, delta: us - baseShare[g.Key], share: us})
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].delta != xs[j].delta {
			return xs[i].delta > xs[j].delta
		}
		if xs[i].share != xs[j].share {
			return xs[i].share > xs[j].share
		}
		return xs[i].key < xs[j].key
	})

	var out []string
	for _, x := range xs {
		if x.delta <= 0 {
			break
		}
		out = append(out, x.key)
		if len(out) == 3 {
			break
		}
	}
	return out
}
