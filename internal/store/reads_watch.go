package store

import (
	"context"
	"sort"
	"time"
)

type WatchStatsParams struct {
	Range string
	User  string // canonical id, or "" for all
	Now   time.Time
}

type WatchUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type WatchCoverage struct {
	FirstPlay  string `json:"first_play"`
	LastPlay   string `json:"last_play"`
	TotalPlays int64  `json:"total_plays"`
}
type WatchTotals struct {
	WatchSeconds      int64   `json:"watch_seconds"`
	Plays             int64   `json:"plays"`
	ActiveUsers       int64   `json:"active_users"`
	DirectPlayPct     float64 `json:"direct_play_pct"`
	VideoTranscodePct float64 `json:"video_transcode_pct"`
}
type WatchTitle struct {
	ItemID   string `json:"item_id,omitempty"`
	SeriesID string `json:"series_id,omitempty"`
	Name     string `json:"name"`
	Plays    int64  `json:"plays"`
	WatchSec int64  `json:"watch_sec"`
}
type WatchEpisode struct {
	ItemID     string `json:"item_id"`
	Name       string `json:"name"`
	SeriesName string `json:"series_name"`
	Plays      int64  `json:"plays"`
	WatchSec   int64  `json:"watch_sec"`
}
type WatchActiveUser struct {
	UserID         string `json:"user_id"`
	Name           string `json:"name"`
	WatchSec       int64  `json:"watch_sec"`
	Plays          int64  `json:"plays"`
	DistinctTitles int64  `json:"distinct_titles"`
}
type WatchTrendPoint struct {
	Day      string `json:"day"`
	WatchSec int64  `json:"watch_sec"`
	Plays    int64  `json:"plays"`
}
type WatchHeatCell struct {
	DOW      int   `json:"dow"`
	Hour     int   `json:"hour"`
	WatchSec int64 `json:"watch_sec"`
	Plays    int64 `json:"plays"`
}
type WatchMethodWeek struct {
	Week           string `json:"week"`
	DirectPlay     int64  `json:"DirectPlay"`
	Remux          int64  `json:"Remux"`
	AudioTranscode int64  `json:"AudioTranscode"`
	VideoTranscode int64  `json:"VideoTranscode"`
	Other          int64  `json:"Other"`
}
type WatchCoreTitle struct {
	Scope        string `json:"scope"`
	ItemID       string `json:"item_id"`
	Name         string `json:"name"`
	PlayCount    int64  `json:"play_count"`
	LastPlayedAt string `json:"last_played_at"`
}

type WatchStats struct {
	PluginAvailable  bool              `json:"plugin_available"`
	Range            string            `json:"range"`
	User             string            `json:"user"`
	Users            []WatchUser       `json:"users"`
	Coverage         WatchCoverage     `json:"coverage"`
	Totals           WatchTotals       `json:"totals"`
	TopMovies        []WatchTitle      `json:"top_movies"`
	TopSeries        []WatchTitle      `json:"top_series"`
	TopEpisodes      []WatchEpisode    `json:"top_episodes"`
	ActiveUsers      []WatchActiveUser `json:"active_users"`
	Trend            []WatchTrendPoint `json:"trend"`
	Heatmap          []WatchHeatCell   `json:"heatmap"`
	PlayMethodWeekly []WatchMethodWeek `json:"play_method_weekly"`
	MostPlayedCore   []WatchCoreTitle  `json:"most_played_core"`
}

func dayCutoff(rng string, now time.Time) string {
	switch rng {
	case "30d":
		return now.AddDate(0, 0, -30).Format("2006-01-02")
	case "90d":
		return now.AddDate(0, 0, -90).Format("2006-01-02")
	case "1y":
		return now.AddDate(-1, 0, 0).Format("2006-01-02")
	default:
		return ""
	}
}

func weekly(rng string) bool { return rng == "1y" || rng == "all" }

// isoWeekMonday returns the Monday (YYYY-MM-DD) of the ISO week containing day.
func isoWeekMonday(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	wd := (int(t.Weekday()) + 6) % 7 // Mon=0 .. Sun=6
	return t.AddDate(0, 0, -wd).Format("2006-01-02")
}

func (s *Store) ReadWatchStats(ctx context.Context, p WatchStatsParams) (WatchStats, error) {
	ws := WatchStats{Range: p.Range, User: p.User}
	if ws.User == "" {
		ws.User = "all"
	}

	if m, ok, err := s.GetRefreshMeta(ctx, "watch"); err == nil && ok {
		ws.PluginAvailable = m.OK && m.PluginAvailable
	}

	urows, err := s.db.QueryContext(ctx, `SELECT id, name FROM dim_user ORDER BY name`)
	if err != nil {
		return ws, err
	}
	for urows.Next() {
		var u WatchUser
		if err := urows.Scan(&u.ID, &u.Name); err != nil {
			urows.Close()
			return ws, err
		}
		ws.Users = append(ws.Users, u)
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return ws, err
	}

	var first, last *string
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(day), MAX(day), COALESCE(SUM(plays),0) FROM watch_events_daily`,
	).Scan(&first, &last, &ws.Coverage.TotalPlays); err != nil {
		return ws, err
	}
	if first != nil {
		ws.Coverage.FirstPlay = *first
	}
	if last != nil {
		ws.Coverage.LastPlay = *last
	}

	if err := s.readCore(ctx, &ws, p.User); err != nil {
		return ws, err
	}

	cut := dayCutoff(p.Range, p.Now)
	rangeWhere, rangeArgs := "1=1", []any{}
	if cut != "" {
		rangeWhere, rangeArgs = "day >= ?", []any{cut}
	}
	scoped := rangeWhere
	scopedArgs := append([]any{}, rangeArgs...)
	if p.User != "" {
		scoped += " AND user_id = ?"
		scopedArgs = append(scopedArgs, p.User)
	}

	var dp, vt int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(watch_sec),0), COALESCE(SUM(plays),0),
		       COUNT(DISTINCT user_id),
		       COALESCE(SUM(CASE WHEN method='DirectPlay'     THEN plays ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN method='VideoTranscode' THEN plays ELSE 0 END),0)
		FROM watch_events_daily WHERE `+scoped, scopedArgs...,
	).Scan(&ws.Totals.WatchSeconds, &ws.Totals.Plays, &ws.Totals.ActiveUsers, &dp, &vt); err != nil {
		return ws, err
	}
	if ws.Totals.Plays > 0 {
		ws.Totals.DirectPlayPct = float64(dp) / float64(ws.Totals.Plays)
		ws.Totals.VideoTranscodePct = float64(vt) / float64(ws.Totals.Plays)
	}

	if err := s.readTop(ctx, &ws.TopMovies, `
		SELECT item_id, MAX(name), SUM(plays), SUM(watch_sec)
		FROM watch_events_daily WHERE scope='movie' AND `+scoped+`
		GROUP BY item_id ORDER BY SUM(watch_sec) DESC, MAX(name) LIMIT 10`, scopedArgs, false); err != nil {
		return ws, err
	}
	if err := s.readTop(ctx, &ws.TopSeries, `
		SELECT series_id, MAX(series_name), SUM(plays), SUM(watch_sec)
		FROM watch_events_daily WHERE scope='episode' AND series_id <> '' AND `+scoped+`
		GROUP BY series_id ORDER BY SUM(watch_sec) DESC, MAX(series_name) LIMIT 10`, scopedArgs, true); err != nil {
		return ws, err
	}
	erows, err := s.db.QueryContext(ctx, `
		SELECT item_id, MAX(name), MAX(series_name), SUM(plays), SUM(watch_sec)
		FROM watch_events_daily WHERE scope='episode' AND `+scoped+`
		GROUP BY item_id ORDER BY SUM(watch_sec) DESC, MAX(name) LIMIT 10`, scopedArgs...)
	if err != nil {
		return ws, err
	}
	for erows.Next() {
		var e WatchEpisode
		if err := erows.Scan(&e.ItemID, &e.Name, &e.SeriesName, &e.Plays, &e.WatchSec); err != nil {
			erows.Close()
			return ws, err
		}
		ws.TopEpisodes = append(ws.TopEpisodes, e)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return ws, err
	}

	arows, err := s.db.QueryContext(ctx, `
		SELECT w.user_id, COALESCE(d.name, w.user_id),
		       SUM(w.watch_sec), SUM(w.plays), COUNT(DISTINCT w.item_id)
		FROM watch_events_daily w
		LEFT JOIN dim_user d ON d.id = w.user_id
		WHERE `+rangeWhere+`
		GROUP BY w.user_id ORDER BY SUM(w.watch_sec) DESC`, rangeArgs...)
	if err != nil {
		return ws, err
	}
	for arows.Next() {
		var a WatchActiveUser
		if err := arows.Scan(&a.UserID, &a.Name, &a.WatchSec, &a.Plays, &a.DistinctTitles); err != nil {
			arows.Close()
			return ws, err
		}
		ws.ActiveUsers = append(ws.ActiveUsers, a)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return ws, err
	}

	trows, err := s.db.QueryContext(ctx, `
		SELECT day, SUM(watch_sec), SUM(plays)
		FROM watch_events_daily WHERE `+scoped+`
		GROUP BY day ORDER BY day`, scopedArgs...)
	if err != nil {
		return ws, err
	}
	trend := map[string]*WatchTrendPoint{}
	var trendOrder []string
	for trows.Next() {
		var day string
		var wsec, plays int64
		if err := trows.Scan(&day, &wsec, &plays); err != nil {
			trows.Close()
			return ws, err
		}
		key := day
		if weekly(p.Range) {
			key = isoWeekMonday(day)
		}
		pt := trend[key]
		if pt == nil {
			pt = &WatchTrendPoint{Day: key}
			trend[key] = pt
			trendOrder = append(trendOrder, key)
		}
		pt.WatchSec += wsec
		pt.Plays += plays
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return ws, err
	}
	for _, k := range trendOrder {
		ws.Trend = append(ws.Trend, *trend[k])
	}

	mrows, err := s.db.QueryContext(ctx, `
		SELECT day, method, SUM(plays)
		FROM watch_events_daily WHERE `+scoped+`
		GROUP BY day, method`, scopedArgs...)
	if err != nil {
		return ws, err
	}
	weeks := map[string]*WatchMethodWeek{}
	var weekOrder []string
	for mrows.Next() {
		var day, method string
		var plays int64
		if err := mrows.Scan(&day, &method, &plays); err != nil {
			mrows.Close()
			return ws, err
		}
		wk := isoWeekMonday(day)
		mw := weeks[wk]
		if mw == nil {
			mw = &WatchMethodWeek{Week: wk}
			weeks[wk] = mw
			weekOrder = append(weekOrder, wk)
		}
		switch method {
		case "DirectPlay":
			mw.DirectPlay += plays
		case "Remux":
			mw.Remux += plays
		case "AudioTranscode":
			mw.AudioTranscode += plays
		case "VideoTranscode":
			mw.VideoTranscode += plays
		default:
			mw.Other += plays
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return ws, err
	}
	sort.Strings(weekOrder)
	for _, k := range weekOrder {
		ws.PlayMethodWeekly = append(ws.PlayMethodWeekly, *weeks[k])
	}

	hWhere, hArgs := "1=1", []any{}
	if p.User != "" {
		hWhere, hArgs = "user_id = ?", []any{p.User}
	}
	hrows, err := s.db.QueryContext(ctx, `
		SELECT dow, hour, SUM(watch_sec), SUM(plays)
		FROM agg_watch_heatmap WHERE `+hWhere+`
		GROUP BY dow, hour ORDER BY dow, hour`, hArgs...)
	if err != nil {
		return ws, err
	}
	for hrows.Next() {
		var c WatchHeatCell
		if err := hrows.Scan(&c.DOW, &c.Hour, &c.WatchSec, &c.Plays); err != nil {
			hrows.Close()
			return ws, err
		}
		ws.Heatmap = append(ws.Heatmap, c)
	}
	hrows.Close()
	return ws, hrows.Err()
}

func (s *Store) readTop(ctx context.Context, dst *[]WatchTitle, q string, args []any, isSeries bool) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var plays, wsec int64
		if err := rows.Scan(&id, &name, &plays, &wsec); err != nil {
			return err
		}
		wt := WatchTitle{Name: name, Plays: plays, WatchSec: wsec}
		if isSeries {
			wt.SeriesID = id
		} else {
			wt.ItemID = id
		}
		*dst = append(*dst, wt)
	}
	return rows.Err()
}

func (s *Store) readCore(ctx context.Context, ws *WatchStats, user string) error {
	var q string
	var args []any
	if user == "" {
		q = `SELECT scope, item_id, MAX(name), SUM(play_count), COALESCE(MAX(last_played_at),'')
		     FROM agg_played_core GROUP BY item_id, scope
		     ORDER BY SUM(play_count) DESC, MAX(name) LIMIT 15`
	} else {
		q = `SELECT scope, item_id, name, play_count, COALESCE(last_played_at,'')
		     FROM agg_played_core WHERE user_id = ?
		     ORDER BY play_count DESC, name LIMIT 15`
		args = append(args, user)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c WatchCoreTitle
		if err := rows.Scan(&c.Scope, &c.ItemID, &c.Name, &c.PlayCount, &c.LastPlayedAt); err != nil {
			return err
		}
		ws.MostPlayedCore = append(ws.MostPlayedCore, c)
	}
	return rows.Err()
}
