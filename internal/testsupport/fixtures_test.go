package testsupport

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLibraryFixtureDB(t *testing.T) {
	path := LibraryFixtureDB(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var movies int
	if err := db.QueryRow(
		`SELECT count(*) FROM BaseItems WHERE Type = ?`,
		"MediaBrowser.Controller.Entities.Movies.Movie",
	).Scan(&movies); err != nil {
		t.Fatal(err)
	}
	if movies != 4 {
		t.Fatalf("want 4 movie rows, got %d", movies)
	}

	var streams int
	if err := db.QueryRow(`SELECT count(*) FROM MediaStreamInfos WHERE StreamType = 1`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if streams != 5 {
		t.Fatalf("want 5 video streams, got %d", streams)
	}
}
