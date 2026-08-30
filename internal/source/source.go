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

// LibraryItem is one movie or episode, flattened to the fields aggregation needs.
// Values are raw (raw codec string, raw colour transfer); classification into
// buckets happens in the aggregate package.
type LibraryItem struct {
	Name          string
	Type          string // "movie" | "episode"
	SizeBytes     int64
	RuntimeSec    int64
	DateCreated   time.Time // parsed; zero if unparseable
	DateRaw       string    // original string, for diagnostics
	Year          int
	Genres        []string
	Library       string // resolved folder name, or "Unknown"
	Container     string
	VideoCodec    string // raw, e.g. "hevc"; "" if no video stream
	Width         int    // primary video stream width; 0 if none
	HasVideo      bool
	ColorTransfer string // raw, e.g. "smpte2084"; "" if none/blank
	DvProfile     *int   // nil unless present and > 0
}

// LibrarySnapshot is everything a library refresh pulled from Jellyfin.
type LibrarySnapshot struct {
	GeneratedAt time.Time
	Items       []LibraryItem
	SeriesCount int
}

// PlaybackEvent is filled in Plan 2 (Watch Stats).
type PlaybackEvent struct{}

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
