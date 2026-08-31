package aggregate

import (
	"sort"
	"strconv"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

// Tuning knobs. Deadpan defaults; revisit against real data before treating any
// of these as load-bearing.
const (
	completionFinished = 0.90 // watched/runtime at or above this -> "finished"
	completionBailed   = 0.25 // below this -> "bailed"
	bingeGapHours      = 4    // episodes more than this far apart start a new run
	profileTopN        = 10   // list length for abandoned / rewatch / binge panels
)

var profileRangeList = []string{"30d", "90d", "1y", "all"}

// rangeCutoff is the inclusive lower bound (YYYY-MM-DD) for a range, or "" for
// "all". Same shape as store.dayCutoff so the two stay in step.
func rangeCutoff(rng string, now time.Time) string {
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

// dailyRow is one (user, item, day) rollup with its completion verdict. Binge
// detection needs sub-day ordering, so it works off raw events instead.
type dailyRow struct {
	user, item, day, scope     string
	name, seriesID, seriesName string
	genres                     []string
	year                       int
	runtimeSec, watchedSec     int64
	plays                      int64
}

func (d dailyRow) verdict() string {
	if d.runtimeSec <= 0 {
		return "unknown"
	}
	r := float64(d.watchedSec) / float64(d.runtimeSec)
	switch {
	case r >= completionFinished:
		return "finished"
	case r < completionBailed:
		return "bailed"
	default:
		return "partial"
	}
}

func rollDaily(events []source.PlaybackEvent) []dailyRow {
	type key struct{ user, item, day string }
	m := map[key]*dailyRow{}
	var order []key
	for _, e := range events {
		if e.At.IsZero() || e.UserID == "" || e.ItemID == "" {
			continue
		}
		k := key{e.UserID, e.ItemID, e.At.Format("2006-01-02")}
		r := m[k]
		if r == nil {
			scope := "movie"
			if e.ItemType == "episode" {
				scope = "episode"
			}
			r = &dailyRow{
				user: k.user, item: k.item, day: k.day, scope: scope,
				name: e.ItemName, seriesID: e.SeriesID, seriesName: e.SeriesName,
				genres: e.ItemGenres, year: e.ItemYear, runtimeSec: e.ItemRuntimeSec,
			}
			m[k] = r
			order = append(order, k)
		}
		r.watchedSec += e.PlayDurationSec
		r.plays++
		if r.runtimeSec == 0 && e.ItemRuntimeSec > 0 {
			r.runtimeSec = e.ItemRuntimeSec
		}
	}
	out := make([]dailyRow, 0, len(order))
	for _, k := range order {
		out = append(out, *m[k])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].user != out[j].user {
			return out[i].user < out[j].user
		}
		if out[i].day != out[j].day {
			return out[i].day < out[j].day
		}
		return out[i].item < out[j].item
	})
	return out
}

func lengthBand(scope string, runtimeSec int64) string {
	if runtimeSec <= 0 {
		return "unknown"
	}
	if scope == "episode" {
		switch {
		case runtimeSec < 1800:
			return "<30m"
		case runtimeSec <= 3600:
			return "30-60m"
		default:
			return ">60m"
		}
	}
	switch {
	case runtimeSec < 5400:
		return "<90m"
	case runtimeSec <= 7200:
		return "90-120m"
	default:
		return ">120m"
	}
}

func decadeKey(year int) string {
	if year <= 0 {
		return "unknown"
	}
	return strconv.Itoa((year / 10) * 10)
}

// Profiles rolls the full playback history into every agg_profile_* row, for all
// four ranges. Pure: no I/O, no SQL. now is container-local (matches the watch
// stats cutoffs).
func Profiles(events []source.PlaybackEvent, now time.Time) ProfileAggregates {
	daily := rollDaily(events)
	var out ProfileAggregates

	for _, u := range distinctUsers(daily) {
		urows := filterUser(daily, u)
		runs := bingeRuns(u, events)

		for _, rng := range profileRangeList {
			scoped := filterSince(urows, rangeCutoff(rng, now))
			out.Completion = append(out.Completion, completionRows(u, rng, scoped)...)
			out.Abandoned = append(out.Abandoned, abandonedRows(u, rng, scoped)...)
			out.Taste = append(out.Taste, tasteRows(u, rng, scoped)...)

			sum := summaryRow(u, rng, scoped)
			if rng == "all" {
				sum.RewatchPct = rewatchPct(urows)
				if len(runs) > 0 {
					sum.LongestBingeEpisodes = runs[0].RunEpisodes
					sum.LongestBingeSeriesName = runs[0].SeriesName
				}
			}
			out.Summary = append(out.Summary, sum)
		}

		out.Rewatch = append(out.Rewatch, rewatchRows(u, urows)...)
		out.Binge = append(out.Binge, topBinge(runs)...)
	}
	out.Baseline = baselineRows(daily)

	sortProfileAggregates(&out)
	return out
}

func distinctUsers(rows []dailyRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if !seen[r.user] {
			seen[r.user] = true
			out = append(out, r.user)
		}
	}
	sort.Strings(out)
	return out
}

func filterUser(rows []dailyRow, user string) []dailyRow {
	var out []dailyRow
	for _, r := range rows {
		if r.user == user {
			out = append(out, r)
		}
	}
	return out
}

func filterSince(rows []dailyRow, cutoff string) []dailyRow {
	if cutoff == "" {
		return rows
	}
	var out []dailyRow
	for _, r := range rows {
		if r.day >= cutoff {
			out = append(out, r)
		}
	}
	return out
}

func completionRows(user, rng string, rows []dailyRow) []ProfileCompletionRow {
	type key struct{ scope, bucket string }
	counts := map[key]int64{}
	for _, r := range rows {
		counts[key{r.scope, r.verdict()}]++
	}
	var out []ProfileCompletionRow
	for k, n := range counts {
		out = append(out, ProfileCompletionRow{
			UserID: user, Range: rng, Scope: k.scope, Bucket: k.bucket, Count: n,
		})
	}
	return out
}

func abandonedRows(user, rng string, rows []dailyRow) []ProfileAbandonedRow {
	type agg struct {
		scope, itemID, name, seriesName string
		bailed                          int64
	}
	m := map[string]*agg{}
	for _, r := range rows {
		if r.verdict() != "bailed" {
			continue
		}
		scope, id, name, sname := "movie", r.item, r.name, ""
		if r.scope == "episode" {
			if r.seriesID == "" {
				continue
			}
			scope, id, name, sname = "series", r.seriesID, r.seriesName, r.seriesName
		}
		a := m[scope+"\x1f"+id]
		if a == nil {
			a = &agg{scope: scope, itemID: id, name: name, seriesName: sname}
			m[scope+"\x1f"+id] = a
		}
		a.bailed++
	}
	var out []ProfileAbandonedRow
	for _, a := range m {
		out = append(out, ProfileAbandonedRow{
			UserID: user, Range: rng, Scope: a.scope, ItemID: a.itemID,
			Name: a.name, SeriesName: a.seriesName, BailedCount: a.bailed,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BailedCount != out[j].BailedCount {
			return out[i].BailedCount > out[j].BailedCount
		}
		return out[i].ItemID < out[j].ItemID
	})
	if len(out) > profileTopN {
		out = out[:profileTopN]
	}
	return out
}

func tasteRows(user, rng string, rows []dailyRow) []ProfileTasteRow {
	type key struct{ dim, k string }
	type acc struct{ watch, plays int64 }
	m := map[key]*acc{}
	add := func(dim, k string, watch, plays int64) {
		a := m[key{dim, k}]
		if a == nil {
			a = &acc{}
			m[key{dim, k}] = a
		}
		a.watch += watch
		a.plays += plays
	}
	for _, r := range rows {
		for _, g := range r.genres {
			add("genre", g, r.watchedSec, r.plays)
		}
		add("decade", decadeKey(r.year), r.watchedSec, r.plays)
		add("length", lengthBand(r.scope, r.runtimeSec), r.watchedSec, r.plays)
	}
	var out []ProfileTasteRow
	for k, a := range m {
		out = append(out, ProfileTasteRow{
			UserID: user, Range: rng, Dim: k.dim, Key: k.k,
			WatchSec: a.watch, Plays: a.plays,
		})
	}
	return out
}

func summaryRow(user, rng string, rows []dailyRow) ProfileSummaryRow {
	s := ProfileSummaryRow{UserID: user, Range: rng}
	movies := map[string]bool{}
	series := map[string]bool{}
	days := map[string]bool{}
	showSec := map[string]int64{}
	showName := map[string]string{}
	var finished, bailed, verdicts int64
	for _, r := range rows {
		s.WatchSec += r.watchedSec
		s.Plays += r.plays
		days[r.day] = true
		switch r.scope {
		case "episode":
			if r.seriesID != "" {
				series[r.seriesID] = true
				showSec[r.seriesID] += r.watchedSec
				showName[r.seriesID] = r.seriesName
			}
		default:
			movies[r.item] = true
		}
		switch r.verdict() {
		case "finished":
			finished++
			verdicts++
		case "bailed":
			bailed++
			verdicts++
		case "partial":
			verdicts++
		}
		if s.FirstPlay == "" || r.day < s.FirstPlay {
			s.FirstPlay = r.day
		}
		if r.day > s.LastPlay {
			s.LastPlay = r.day
		}
	}
	s.DistinctTitles = int64(len(movies) + len(series))
	s.DaysActive = int64(len(days))
	if verdicts > 0 {
		s.FinishedPct = float64(finished) / float64(verdicts)
		s.BailedPct = float64(bailed) / float64(verdicts)
	}
	var bestSec int64 = -1
	for sid, sec := range showSec {
		if sec > bestSec || (sec == bestSec && sid < s.ShowOfRangeSeriesID) {
			bestSec = sec
			s.ShowOfRangeSeriesID = sid
			s.ShowOfRangeSeriesName = showName[sid]
		}
	}
	return s
}

// watchDaysByTitle returns distinct non-bailed watch-days per (scope,item) for a
// user's whole history.
func watchDaysByTitle(rows []dailyRow) map[string]*ProfileRewatchRow {
	m := map[string]*ProfileRewatchRow{}
	for _, r := range rows {
		if r.verdict() == "bailed" {
			continue
		}
		scope, id, name, sname := r.scope, r.item, r.name, r.seriesName
		k := scope + "\x1f" + id
		rw := m[k]
		if rw == nil {
			rw = &ProfileRewatchRow{Scope: scope, ItemID: id, Name: name, SeriesName: sname}
			m[k] = rw
		}
		rw.WatchDays++
	}
	return m
}

func rewatchRows(user string, rows []dailyRow) []ProfileRewatchRow {
	var out []ProfileRewatchRow
	for _, rw := range watchDaysByTitle(rows) {
		if rw.WatchDays < 2 {
			continue // not a rewatch
		}
		r := *rw
		r.UserID = user
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].WatchDays != out[j].WatchDays {
			return out[i].WatchDays > out[j].WatchDays
		}
		return out[i].ItemID < out[j].ItemID
	})
	if len(out) > profileTopN {
		out = out[:profileTopN]
	}
	return out
}

func rewatchPct(rows []dailyRow) float64 {
	var rewatch, total int64
	for _, rw := range watchDaysByTitle(rows) {
		total += rw.WatchDays
		if rw.WatchDays > 1 {
			rewatch += rw.WatchDays - 1
		}
	}
	if total == 0 {
		return 0
	}
	return float64(rewatch) / float64(total)
}

// bingeRuns finds a user's same-series episode runs, longest first. A run breaks
// when consecutive episode events are more than bingeGapHours apart.
func bingeRuns(user string, events []source.PlaybackEvent) []ProfileBingeRow {
	bySeries := map[string][]source.PlaybackEvent{}
	for _, e := range events {
		if e.UserID != user || e.ItemType != "episode" || e.SeriesID == "" || e.At.IsZero() {
			continue
		}
		bySeries[e.SeriesID] = append(bySeries[e.SeriesID], e)
	}

	var out []ProfileBingeRow
	for sid, evs := range bySeries {
		sort.Slice(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
		start := 0
		flush := func(lo, hi int) {
			eps := map[string]bool{}
			for _, e := range evs[lo:hi] {
				eps[e.ItemID] = true
			}
			out = append(out, ProfileBingeRow{
				UserID: user, SeriesID: sid, SeriesName: evs[lo].SeriesName,
				RunEpisodes: int64(len(eps)),
				RunStart:    evs[lo].At.Format("2006-01-02"),
				RunEnd:      evs[hi-1].At.Format("2006-01-02"),
			})
		}
		for i := 1; i < len(evs); i++ {
			if evs[i].At.Sub(evs[i-1].At) > bingeGapHours*time.Hour {
				flush(start, i)
				start = i
			}
		}
		if len(evs) > 0 {
			flush(start, len(evs))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].RunEpisodes != out[j].RunEpisodes {
			return out[i].RunEpisodes > out[j].RunEpisodes
		}
		if out[i].RunEnd != out[j].RunEnd {
			return out[i].RunEnd > out[j].RunEnd
		}
		return out[i].SeriesID < out[j].SeriesID
	})
	return out
}

func topBinge(runs []ProfileBingeRow) []ProfileBingeRow {
	var out []ProfileBingeRow
	for _, r := range runs {
		if r.RunEpisodes >= 2 {
			out = append(out, r)
		}
	}
	if len(out) > profileTopN {
		out = out[:profileTopN]
	}
	return out
}

func baselineRows(rows []dailyRow) []TasteBaselineRow {
	type key struct{ dim, k string }
	m := map[key]int64{}
	for _, r := range rows {
		for _, g := range r.genres {
			m[key{"genre", g}] += r.watchedSec
		}
		m[key{"decade", decadeKey(r.year)}] += r.watchedSec
		m[key{"length", lengthBand(r.scope, r.runtimeSec)}] += r.watchedSec
	}
	var out []TasteBaselineRow
	for k, sec := range m {
		out = append(out, TasteBaselineRow{Dim: k.dim, Key: k.k, WatchSec: sec})
	}
	return out
}

func sortProfileAggregates(a *ProfileAggregates) {
	sort.Slice(a.Summary, func(i, j int) bool {
		if a.Summary[i].UserID != a.Summary[j].UserID {
			return a.Summary[i].UserID < a.Summary[j].UserID
		}
		return a.Summary[i].Range < a.Summary[j].Range
	})
	sort.Slice(a.Completion, func(i, j int) bool {
		x, y := a.Completion[i], a.Completion[j]
		switch {
		case x.UserID != y.UserID:
			return x.UserID < y.UserID
		case x.Range != y.Range:
			return x.Range < y.Range
		case x.Scope != y.Scope:
			return x.Scope < y.Scope
		default:
			return x.Bucket < y.Bucket
		}
	})
	sort.Slice(a.Abandoned, func(i, j int) bool {
		x, y := a.Abandoned[i], a.Abandoned[j]
		switch {
		case x.UserID != y.UserID:
			return x.UserID < y.UserID
		case x.Range != y.Range:
			return x.Range < y.Range
		case x.BailedCount != y.BailedCount:
			return x.BailedCount > y.BailedCount
		default:
			return x.ItemID < y.ItemID
		}
	})
	sort.Slice(a.Rewatch, func(i, j int) bool {
		x, y := a.Rewatch[i], a.Rewatch[j]
		switch {
		case x.UserID != y.UserID:
			return x.UserID < y.UserID
		case x.WatchDays != y.WatchDays:
			return x.WatchDays > y.WatchDays
		default:
			return x.ItemID < y.ItemID
		}
	})
	sort.Slice(a.Binge, func(i, j int) bool {
		x, y := a.Binge[i], a.Binge[j]
		switch {
		case x.UserID != y.UserID:
			return x.UserID < y.UserID
		case x.RunEpisodes != y.RunEpisodes:
			return x.RunEpisodes > y.RunEpisodes
		case x.RunEnd != y.RunEnd:
			return x.RunEnd > y.RunEnd
		default:
			return x.SeriesID < y.SeriesID
		}
	})
	sort.Slice(a.Taste, func(i, j int) bool {
		x, y := a.Taste[i], a.Taste[j]
		switch {
		case x.UserID != y.UserID:
			return x.UserID < y.UserID
		case x.Range != y.Range:
			return x.Range < y.Range
		case x.Dim != y.Dim:
			return x.Dim < y.Dim
		default:
			return x.Key < y.Key
		}
	})
	sort.Slice(a.Baseline, func(i, j int) bool {
		if a.Baseline[i].Dim != a.Baseline[j].Dim {
			return a.Baseline[i].Dim < a.Baseline[j].Dim
		}
		return a.Baseline[i].Key < a.Baseline[j].Key
	})
}
