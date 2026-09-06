package aggregate

// Row types the store persists for the watch and cleanup jobs. They live in
// aggregate (not source) so the store depends only on this package, matching how
// LibraryAggregates already works.

// CleanupRow is one movie or one whole series. never/stale is decided at read
// time from LastPlayedAt.
type CleanupRow struct {
	ItemID, Scope, Name, Library string
	Bytes, Episodes              int64
	AddedAt                      string // RFC3339, "" if unknown
	LastPlayedAt                 string // RFC3339, "" = never
}

// UserRow is Jellyfin's user directory, for the Watch Stats filter.
type UserRow struct{ ID, Name string }

// CorePlayRow is a per-user play count from Jellyfin's own counters, for the
// always-on "most played" panel.
type CorePlayRow struct {
	UserID, Scope, ItemID, Name string
	PlayCount                   int64
	LastPlayedAt                string // RFC3339, "" if none
}

// WatchDailyRow is one (day, user, title, method) fact from the plugin.
type WatchDailyRow struct {
	Day, UserID, ItemID, Scope, Name, SeriesID, SeriesName, Method, Library string
	Plays, WatchSec                                                         int64
}

// HeatmapRow is one (user, day-of-week, hour, library) cell, all of history.
type HeatmapRow struct {
	UserID, Library string
	DOW, Hour       int
	WatchSec, Plays int64
}

// --- Profile rows (Plan 4). One agg_profile_* table each; full-rewritten per
// watch run. Range is one of "30d" | "90d" | "1y" | "all"; a few panels are
// lifetime-only and stored under "all".

type ProfileSummaryRow struct {
	UserID, Range              string
	WatchSec, Plays            int64
	DistinctTitles, DaysActive int64
	FinishedPct, BailedPct     float64
	RewatchPct                 float64 // only set on Range=="all"
	LongestBingeEpisodes       int64   // lifetime
	LongestBingeSeriesName     string  // lifetime
	ShowOfRangeSeriesID        string
	ShowOfRangeSeriesName      string
	FirstPlay, LastPlay        string // YYYY-MM-DD
}

type ProfileCompletionRow struct {
	UserID, Range, Scope, Bucket string // Scope: movie|episode ; Bucket: finished|partial|bailed|unknown
	Count                        int64
}

type ProfileAbandonedRow struct {
	UserID, Range, Scope, ItemID, Name, SeriesName string // Scope: movie|series
	BailedCount                                    int64
}

type ProfileRewatchRow struct {
	UserID, Scope, ItemID, Name, SeriesName string
	WatchDays                               int64
}

type ProfileBingeRow struct {
	UserID, SeriesID, SeriesName string
	RunEpisodes                  int64
	RunStart, RunEnd             string // YYYY-MM-DD
}

type ProfileTasteRow struct {
	UserID, Range, Dim, Key string // Dim: genre|decade|length|tag
	WatchSec, Plays         int64
}

type TasteBaselineRow struct {
	Dim, Key string
	WatchSec int64
}

// ProfileTagOverlapRow is one unordered user pair's tag-taste similarity for a
// range. UserA < UserB. Emitted only when both users have tagged watch history
// in range and share at least one tag.
type ProfileTagOverlapRow struct {
	UserA, UserB, Range string
	Cosine              float64
	Shared              []string
}

// ProfileAggregates is everything one Profiles() pass produces.
type ProfileAggregates struct {
	Summary    []ProfileSummaryRow
	Completion []ProfileCompletionRow
	Abandoned  []ProfileAbandonedRow
	Rewatch    []ProfileRewatchRow
	Binge      []ProfileBingeRow
	Taste      []ProfileTasteRow
	Baseline   []TasteBaselineRow
	TagOverlap []ProfileTagOverlapRow
}
