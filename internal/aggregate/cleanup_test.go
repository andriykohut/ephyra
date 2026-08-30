package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func TestCleanup(t *testing.T) {
	mk := func(id, typ, name, lib string, bytes int64, added time.Time) source.LibraryItem {
		return source.LibraryItem{ID: id, Type: typ, Name: name, Library: lib, SizeBytes: bytes, DateCreated: added}
	}
	jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	e1 := mk("e1", "episode", "S1E1", "Shows", 1_200, jan)
	e1.SeriesID, e1.SeriesName = "s_show", "Some Show"
	e1.LastPlayedAt = time.Date(2025, 4, 10, 0, 0, 0, 0, time.UTC)
	e2 := mk("e2", "episode", "S1E2", "Shows", 1_300, feb) // newer add; never played
	e2.SeriesID, e2.SeriesName = "s_show", "Some Show"
	bravo := mk("m_bravo", "movie", "Bravo", "Movies", 4_000, jan)
	bravo.LastPlayedAt = time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	snap := source.LibrarySnapshot{Items: []source.LibraryItem{
		mk("m_alpha", "movie", "Alpha", "Movies", 8_000, jan), // never played
		bravo,
		e1,
		e2,
	}}

	rows := Cleanup(snap)
	byID := map[string]CleanupRow{}
	for _, r := range rows {
		byID[r.ItemID] = r
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (2 movies + 1 series), got %d: %+v", len(rows), rows)
	}
	if s := byID["s_show"]; s.Scope != "series" || s.Bytes != 2_500 || s.Episodes != 2 ||
		s.Name != "Some Show" || s.Library != "Shows" {
		t.Errorf("series row wrong: %+v", s)
	}
	if byID["s_show"].AddedAt != feb.Format(time.RFC3339) {
		t.Errorf("series added_at = %q want %q", byID["s_show"].AddedAt, feb.Format(time.RFC3339))
	}
	if byID["s_show"].LastPlayedAt != "2025-04-10T00:00:00Z" {
		t.Errorf("series last_played = %q", byID["s_show"].LastPlayedAt)
	}
	if byID["m_alpha"].LastPlayedAt != "" {
		t.Errorf("Alpha should read as never: %q", byID["m_alpha"].LastPlayedAt)
	}
	if byID["m_bravo"].LastPlayedAt != "2025-06-01T00:00:00Z" {
		t.Errorf("Bravo last_played = %q", byID["m_bravo"].LastPlayedAt)
	}
}
