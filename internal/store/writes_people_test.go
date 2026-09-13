package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func TestWritePeopleAggregatesIsLibraryScoped(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	row := aggregate.PeopleRow{
		UserID: "u1", Range: "all", Kind: "actor", Person: "Ada Vex",
		WatchSec: 100, Plays: 2, DistinctTitles: 1,
	}
	if err := st.WritePeopleAggregates(ctx, map[string]aggregate.PeopleAggregates{
		"":       {People: []aggregate.PeopleRow{row}},
		"Movies": {People: []aggregate.PeopleRow{row}},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := st.ReadProfilePeople(ctx, "u1", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("all-libraries rows = %d, want 1", len(all))
	}
	movies, err := st.ReadProfilePeople(ctx, "u1", "all", "Movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 {
		t.Fatalf("Movies rows = %d, want 1", len(movies))
	}
	none, err := st.ReadProfilePeople(ctx, "u1", "all", "Shows")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("Shows rows = %d, want 0", len(none))
	}
}

func TestReadProfilePeopleReturnsEmptySliceNotNil(t *testing.T) {
	st := openStore(t)
	got, err := st.ReadProfilePeople(context.Background(), "nobody", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("got nil; the SPA iterates this straight off the response")
	}
}
