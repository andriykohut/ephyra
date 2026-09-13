package file

import (
	"database/sql"
	"slices"
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
	if len(snap.Items) != 6 || snap.SeriesCounts["Shows"] != 1 || len(snap.SeriesCounts) != 1 {
		t.Fatalf("items=%d seriesCounts=%v", len(snap.Items), snap.SeriesCounts)
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

func TestDefaultQueryLibrary_Tags(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string][]string{}
	for _, it := range snap.Items {
		byName[it.Name] = it.Tags
	}
	if got := byName["Alpha"]; len(got) != 3 || got[0] != "heist" || got[1] != "vault" || got[2] != "dystopia" {
		t.Fatalf("Alpha tags normalized wrong: %#v", got)
	}
	if got := byName["Charlie"]; len(got) != 2 || got[0] != "heist" || got[1] != "vault" {
		t.Fatalf("Charlie tags: %#v", got)
	}
	if got := byName["S1E1"]; len(got) != 0 {
		t.Fatalf("episode should have no own tags in the library snapshot: %#v", got)
	}
}

func TestQueryLibrary_PlayedStateAndUsers(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]source.LibraryItem{}
	for _, it := range snap.Items {
		byName[it.Name] = it
	}

	if byName["Alpha"].ID != "0000000000000000000000000000000a" {
		t.Errorf("Alpha.ID = %q", byName["Alpha"].ID)
	}
	if byName["S1E1"].SeriesID != "00000000000000000000000000000f0" &&
		byName["S1E1"].SeriesID != "000000000000000000000000000000f0" {
		t.Errorf("S1E1.SeriesID = %q", byName["S1E1"].SeriesID)
	}
	if byName["S1E1"].SeriesName != "Some Show" {
		t.Errorf("S1E1.SeriesName = %q", byName["S1E1"].SeriesName)
	}
	// Alpha: alice finished it -- the fixture's one credited item with a
	// played=1 tick (see UpsertPlayed/people aggregate coverage).
	if a := byName["Alpha"]; !a.Played || a.PlayCount != 2 || a.LastPlayedAt.IsZero() {
		t.Errorf("Alpha played-state: %+v", a)
	}
	// Bravo: alice 3 + an orphan-user (not in Users) 1 -> SUM 4; still resolves.
	if b := byName["Bravo"]; !b.Played || b.PlayCount != 4 || b.LastPlayedAt.IsZero() {
		t.Errorf("Bravo played-state: %+v", b)
	}
	// Delta: two CustomDataKey rows, one user. per-user MAX(PlayCount)=2, then SUM over users = 2.
	if d := byName["Delta"]; !d.Played || d.PlayCount != 2 {
		t.Errorf("Delta played-state: %+v", d)
	}

	if len(snap.Users) != 3 {
		t.Fatalf("want 3 users, got %d: %+v", len(snap.Users), snap.Users)
	}
	uname := map[string]string{}
	for _, u := range snap.Users {
		uname[u.ID] = u.Name
	}
	if uname["11111111222233334444555555555555"] != "alice" {
		t.Errorf("user map: %+v", uname)
	}

	var sawSeries bool
	for _, p := range snap.UserPlays {
		if p.Scope == "series" && p.Name == "Some Show" {
			sawSeries = true
		}
	}
	if !sawSeries {
		t.Errorf("expected a rolled-up series UserPlay for Some Show: %+v", snap.UserPlays)
	}
}

func TestUserPlays_Library(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	byItem := map[string]source.UserPlay{}
	for _, p := range snap.UserPlays {
		byItem[p.ItemID] = p
	}
	// Bravo is a Movies-library title with PlayCount > 0 in the fixture.
	if bravo, ok := byItem["0000000000000000000000000000000b"]; !ok || bravo.Library != "Movies" {
		t.Fatalf("bravo userplay library = %+v", bravo)
	}
}

func TestLibraryFactsCredits(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}

	byPerson := map[string][]source.Credit{}
	for _, c := range snap.Credits {
		byPerson[c.Person] = append(byPerson[c.Person], c)
	}

	if _, ok := byPerson["Dot Reyes"]; ok {
		t.Error("Producer leaked into credits; only Actor/GuestStar/Director are read")
	}
	// GuestStar folds into actor at read time so the aggregate never sees the
	// distinction.
	for _, c := range byPerson["Bo Quill"] {
		if c.Kind != "actor" {
			t.Errorf("Bo Quill kind = %q, want actor", c.Kind)
		}
	}
	var kinds []string
	for _, c := range byPerson["Ada Vex"] {
		kinds = append(kinds, c.Kind)
	}
	if !slices.Contains(kinds, "actor") || !slices.Contains(kinds, "director") {
		t.Errorf("Ada Vex kinds = %v, want both actor and director", kinds)
	}
	for _, c := range snap.Credits {
		if c.ItemID != source.CanonID(c.ItemID) {
			t.Errorf("item id %q is not canonical", c.ItemID)
		}
	}
}

func TestLibraryFactsPlayed(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Played) == 0 {
		t.Fatal("no per-user played rows")
	}
	seen := map[bool]bool{}
	for _, p := range snap.Played {
		seen[p.Played] = true
		if p.UserID != source.CanonID(p.UserID) || p.ItemID != source.CanonID(p.ItemID) {
			t.Errorf("non-canonical ids: %+v", p)
		}
	}
	if !seen[true] || !seen[false] {
		t.Errorf("want both played and unplayed rows, got %v", seen)
	}
}
