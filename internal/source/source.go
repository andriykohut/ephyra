// Package source is where library facts and playback events come from, minus the
// question of how. FileSource reads copies of Jellyfin's SQLite files; a future
// APISource will go over HTTP for Jellyfin 12 / Postgres / remote setups.
package source

import (
	"context"
	"errors"
	"time"
)

// ErrNotImplemented is returned by methods a given Source doesn't do yet.
var ErrNotImplemented = errors.New("source: not implemented in this build")

// ErrPluginUnavailable means the Playback Reporting plugin's data isn't there
// (file missing, or the PlaybackActivity table absent). Not a failure — the
// watch job records plugin_available=false and moves on.
var ErrPluginUnavailable = errors.New("source: playback reporting plugin data not available")

// LibraryItem is one movie or episode, flattened to the fields aggregation needs.
// Values are raw (raw codec string, raw colour transfer); classification into
// buckets happens in the aggregate package.
type LibraryItem struct {
	ID            string // canonical (dashless lowercase)
	Name          string
	Type          string // "movie" | "episode"
	SizeBytes     int64
	RuntimeSec    int64
	DateCreated   time.Time // parsed; zero if unparseable
	DateRaw       string    // original string, for diagnostics
	Year          int
	Genres        []string
	Tags          []string // normalized: lower(trim), empties dropped, deduped within the item
	Library       string   // resolved folder name, or "Unknown"
	Container     string
	VideoCodec    string // raw, e.g. "hevc"; "" if no video stream
	Width         int    // primary video stream width; 0 if none
	HasVideo      bool
	ColorTransfer string // raw, e.g. "smpte2084"; "" if none/blank
	DvProfile     *int   // nil unless present and > 0

	SeriesID     string    // canonical; "" for movies
	SeriesName   string    // "" for movies
	Played       bool      // MAX(UserData.Played) across users
	PlayCount    int       // SUM(UserData.PlayCount) across users
	LastPlayedAt time.Time // MAX(UserData.LastPlayedDate) across users; zero if never
}

// UserRef is one Jellyfin user, for the Watch Stats filter.
type UserRef struct{ ID, Name string } // ID canonical

// UserPlay is one user's play rollup for a movie or a whole series, from
// Jellyfin's own UserData counters.
type UserPlay struct {
	UserID, ItemID, Scope, Name string // Scope: "movie" | "series"; ItemID canonical (movie or series id)
	PlayCount                   int
	LastPlayedAt                time.Time
}

// LibrarySnapshot is everything a library refresh pulled from Jellyfin.
type LibrarySnapshot struct {
	GeneratedAt time.Time
	Items       []LibraryItem
	SeriesCount int
	Users       []UserRef
	UserPlays   []UserPlay
}

// PlaybackEvent is one row from the Playback Reporting plugin, enriched (where
// jellyfin.db resolves it) with current names, series linkage, and the item
// facts aggregate.Profiles needs (runtime / genres / year).
type PlaybackEvent struct {
	At               time.Time // parsed literal components (server-local wall time)
	UserID, UserName string    // UserID canonical
	ItemID, ItemName string    // ItemID canonical
	ItemType         string    // "movie" | "episode"
	SeriesID         string    // canonical; "" for movies / unresolved
	SeriesName       string
	Method           string // RAW plugin string; bucketed in aggregate
	PlayDurationSec  int64

	ItemRuntimeSec int64    // 0 when unknown or the item is gone
	ItemGenres     []string // nil when unknown
	ItemTags       []string // nil when unknown; for episodes these are the parent Series' tags
	ItemYear       int      // 0 when unknown
}

type Source interface {
	LibraryFacts(ctx context.Context) (LibrarySnapshot, error)
	PlaybackEvents(ctx context.Context, since time.Time) ([]PlaybackEvent, error)
	Kind() string // "file" | "api"
}

// MTimer is implemented by sources backed by files. job is "library" | "watch".
// It lets the scheduler skip a refresh when the underlying file hasn't changed.
type MTimer interface {
	SourceMTime(job string) (time.Time, error)
}
