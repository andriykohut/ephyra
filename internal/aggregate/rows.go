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
	Day, UserID, ItemID, Scope, Name, SeriesID, SeriesName, Method string
	Plays, WatchSec                                                int64
}

// HeatmapRow is one (user, day-of-week, hour) cell, all of history.
type HeatmapRow struct {
	UserID          string
	DOW, Hour       int
	WatchSec, Plays int64
}
