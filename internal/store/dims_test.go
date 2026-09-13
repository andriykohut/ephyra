package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/source"
)

func TestUpsertCreditsRefreshesAndNeverDeletes(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	first := []source.Credit{
		{ItemID: "aaa", Person: "Ada Vex", Kind: "actor", ListOrder: 3},
		{ItemID: "bbb", Person: "Cy Marrow", Kind: "director"},
	}
	if err := st.UpsertCredits(ctx, first); err != nil {
		t.Fatal(err)
	}
	// A later refresh sees a re-billed Ada and no longer sees item bbb at all --
	// bbb was deleted from Jellyfin. Its credit must survive, because the play
	// that references it survives in the spine.
	second := []source.Credit{{ItemID: "aaa", Person: "Ada Vex", Kind: "actor", ListOrder: 0}}
	if err := st.UpsertCredits(ctx, second); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadCredits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d credits, want 2 (the deleted item's credit must be retained)", len(got))
	}
	for _, c := range got {
		if c.ItemID == "aaa" && c.ListOrder != 0 {
			t.Errorf("list_order = %d, want 0 (refreshed)", c.ListOrder)
		}
	}
}

func TestUpsertPlayedRefreshesAndNeverDeletes(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	if err := st.UpsertPlayed(ctx, []source.UserItemPlayed{
		{UserID: "u1", ItemID: "aaa", Played: true},
		{UserID: "u1", ItemID: "bbb", Played: true},
	}); err != nil {
		t.Fatal(err)
	}
	// Unmarked in Jellyfin, and bbb deleted entirely.
	if err := st.UpsertPlayed(ctx, []source.UserItemPlayed{
		{UserID: "u1", ItemID: "aaa", Played: false},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadPlayed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[aggregate.UserItem{UserID: "u1", ItemID: "aaa"}] {
		t.Error("aaa should have been un-played by the refresh")
	}
	if !got[aggregate.UserItem{UserID: "u1", ItemID: "bbb"}] {
		t.Error("bbb was deleted from Jellyfin; its tick must be retained")
	}
}
