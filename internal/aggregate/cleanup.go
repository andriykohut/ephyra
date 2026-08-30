package aggregate

import (
	"sort"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

// Cleanup rolls a library snapshot into one row per movie or series. Episodes
// are grouped into their series; a movie is its own row. never/stale is decided
// later at read time from last_played_at.
func Cleanup(snap source.LibrarySnapshot) []CleanupRow {
	type sAcc struct {
		name, library   string
		bytes, episodes int64
		added, lastPlay time.Time
	}
	var out []CleanupRow
	series := map[string]*sAcc{}

	for _, it := range snap.Items {
		switch it.Type {
		case "movie":
			out = append(out, CleanupRow{
				ItemID: it.ID, Scope: "movie", Name: it.Name, Library: it.Library,
				Bytes: it.SizeBytes, Episodes: 0,
				AddedAt: rfc3339(it.DateCreated), LastPlayedAt: rfc3339(it.LastPlayedAt),
			})
		case "episode":
			key, name := it.SeriesID, it.SeriesName
			if key == "" { // no series linkage: the episode is its own one-item "series"
				key, name = it.ID, it.Name
			}
			a := series[key]
			if a == nil {
				a = &sAcc{name: name, library: it.Library}
				series[key] = a
			}
			a.bytes += it.SizeBytes
			a.episodes++
			if it.DateCreated.After(a.added) {
				a.added = it.DateCreated
			}
			if it.LastPlayedAt.After(a.lastPlay) {
				a.lastPlay = it.LastPlayedAt
			}
		}
	}
	for id, a := range series {
		out = append(out, CleanupRow{
			ItemID: id, Scope: "series", Name: a.name, Library: a.library,
			Bytes: a.bytes, Episodes: a.episodes,
			AddedAt: rfc3339(a.added), LastPlayedAt: rfc3339(a.lastPlay),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
}

// Users is a straight projection of the snapshot's user directory, sorted by id
// for a stable write order.
func Users(snap source.LibrarySnapshot) []UserRow {
	out := make([]UserRow, 0, len(snap.Users))
	for _, u := range snap.Users {
		out = append(out, UserRow{ID: u.ID, Name: u.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
