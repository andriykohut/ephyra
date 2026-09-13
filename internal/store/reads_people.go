package store

import "context"

type PersonDTO struct {
	Person               string `json:"person"`
	Kind                 string `json:"kind"`
	WatchSec             int64  `json:"watch_sec"`
	Plays                int64  `json:"plays"`
	DistinctTitles       int64  `json:"distinct_titles"`
	WatchSecPlayed       int64  `json:"watch_sec_played"`
	PlaysPlayed          int64  `json:"plays_played"`
	DistinctTitlesPlayed int64  `json:"distinct_titles_played"`
	JFURL                string `json:"jf_url"`
}

type TopItemDTO struct {
	Scope          string `json:"scope"`
	ItemID         string `json:"item_id"`
	Name           string `json:"name"`
	SeriesName     string `json:"series_name"`
	WatchSec       int64  `json:"watch_sec"`
	Plays          int64  `json:"plays"`
	WatchSecPlayed int64  `json:"watch_sec_played"`
	PlaysPlayed    int64  `json:"plays_played"`
	JFURL          string `json:"jf_url"`
}

// ReadProfilePeople is one user's people ranking for a range, library-scoped.
// jf_url is populated from dim_jf_ref and stays "" until the resolver has
// found that person on the configured Jellyfin server.
func (s *Store) ReadProfilePeople(ctx context.Context, userID, rng, library string) ([]PersonDTO, error) {
	base, serverID := s.jellyfinLinks()
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.kind, p.person, p.watch_sec, p.plays, p.distinct_titles,
		       p.watch_sec_played, p.plays_played, p.distinct_titles_played,
		       COALESCE(r.jf_id, '')
		FROM agg_profile_people p
		LEFT JOIN dim_jf_ref r ON r.kind = 'person' AND r.name = p.person
		WHERE p.user_id = ? AND p.range = ? AND p.library = ?
		ORDER BY p.watch_sec DESC`, userID, rng, library)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonDTO
	for rows.Next() {
		var p PersonDTO
		var jfID string
		if err := rows.Scan(&p.Kind, &p.Person, &p.WatchSec, &p.Plays, &p.DistinctTitles,
			&p.WatchSecPlayed, &p.PlaysPlayed, &p.DistinctTitlesPlayed, &jfID); err != nil {
			return nil, err
		}
		p.JFURL = jfLink(base, serverID, "person", jfID)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return orEmpty(out), nil
}

// ReadProfileTopItems is one user's top-items ranking for a range,
// library-scoped. jf_url needs no dim_jf_ref lookup -- item_id already is
// the API's item id.
func (s *Store) ReadProfileTopItems(ctx context.Context, userID, rng, library string) ([]TopItemDTO, error) {
	base, serverID := s.jellyfinLinks()
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, item_id, name, series_name, watch_sec, plays,
		       watch_sec_played, plays_played
		FROM agg_profile_top_items
		WHERE user_id = ? AND range = ? AND library = ?
		ORDER BY watch_sec DESC`, userID, rng, library)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopItemDTO
	for rows.Next() {
		var t TopItemDTO
		if err := rows.Scan(&t.Scope, &t.ItemID, &t.Name, &t.SeriesName, &t.WatchSec, &t.Plays,
			&t.WatchSecPlayed, &t.PlaysPlayed); err != nil {
			return nil, err
		}
		t.JFURL = jfLink(base, serverID, "item", t.ItemID)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return orEmpty(out), nil
}
