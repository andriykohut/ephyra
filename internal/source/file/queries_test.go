package file

import (
	"database/sql"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
	"github.com/andriykohut/ephyra/internal/testsupport"
)

func openFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", testsupport.LibraryFixtureDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustTime(s string) time.Time { ts, _ := time.Parse(time.RFC3339, s); return ts }

func TestDefaultQueryLibrary(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Items) != 6 || snap.SeriesCount != 1 {
		t.Fatalf("items=%d series=%d", len(snap.Items), snap.SeriesCount)
	}
	items := map[string]source.LibraryItem{}
	libCount := map[string]int{}
	for _, it := range snap.Items {
		items[it.Name] = it
		libCount[it.Library]++
	}
	if libCount["Movies"] != 4 || libCount["Shows"] != 2 {
		t.Fatalf("library attribution: %v", libCount)
	}
	alpha := items["Alpha"]
	if alpha.Type != "movie" || alpha.VideoCodec != "hevc" || alpha.ColorTransfer != "smpte2084" ||
		alpha.Width != 3840 || !alpha.HasVideo || alpha.SizeBytes != 8_000_000_000 ||
		alpha.RuntimeSec != 7200 || alpha.Year != 1994 ||
		len(alpha.Genres) != 2 || alpha.Genres[0] != "Drama" || alpha.Genres[1] != "Thriller" {
		t.Fatalf("Alpha wrong: %+v", alpha)
	}
	if !alpha.DateCreated.Equal(mustTime("2024-01-05T10:00:00Z")) {
		t.Fatalf("Alpha DateCreated: %v", alpha.DateCreated)
	}
	charlie := items["Charlie"]
	if charlie.HasVideo || charlie.Width != 0 || charlie.VideoCodec != "" || charlie.SizeBytes != 0 {
		t.Fatalf("Charlie should have no video and 0 size: %+v", charlie)
	}
	if dv := items["S1E1"].DvProfile; dv == nil || *dv != 8 {
		t.Fatalf("S1E1 DvProfile: %v", dv)
	}
	if items["S1E2"].DvProfile != nil {
		t.Fatalf("S1E2 DvProfile should be nil")
	}
}
