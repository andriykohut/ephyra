package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// seedPeopleRows inserts one agg_profile_people row per name, range "all",
// library "" -- enough for ReadProfilePeople to have something to join
// dim_jf_ref against.
func seedPeopleRows(t *testing.T, st *Store, userID string, names []string) {
	t.Helper()
	for _, name := range names {
		if _, err := st.DB().ExecContext(context.Background(), `
			INSERT INTO agg_profile_people (library, user_id, range, kind, person, watch_sec, plays)
			VALUES ('', ?, 'all', 'actor', ?, 100, 1)`, userID, name,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPeopleLinkOnlyWhenResolved(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.SetJellyfinLinks("http://jf.example", "sid1")
	seedPeopleRows(t, st, "u1", []string{"Ada Vex", "Ghost"})
	if err := st.PutJFRef(ctx, "person", "Ada Vex", "abc123", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadProfilePeople(ctx, "u1", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		switch p.Person {
		case "Ada Vex":
			if !strings.Contains(p.JFURL, "abc123") {
				t.Errorf("Ada Vex jf_url = %q, want the resolved id in it", p.JFURL)
			}
		case "Ghost":
			// Unresolved renders as plain text. Resolving ahead of the click,
			// rather than on it, is what keeps a dead link from ever rendering.
			if p.JFURL != "" {
				t.Errorf("Ghost jf_url = %q, want empty", p.JFURL)
			}
		}
	}
}

func TestPeopleLinkEmptyWithoutBaseURL(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	seedPeopleRows(t, st, "u1", []string{"Ada Vex"})
	if err := st.PutJFRef(ctx, "person", "Ada Vex", "abc123", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadProfilePeople(ctx, "u1", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].JFURL != "" {
		t.Fatalf("got %+v, want an empty jf_url with no base URL configured", got)
	}
}

func TestTopItemLinkNeedsNoResolution(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	st.SetJellyfinLinks("http://jf.example", "sid1")
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO agg_profile_top_items (library, user_id, range, scope, item_id, name, watch_sec, plays)
		VALUES ('', 'u1', 'all', 'movie', 'item-1', 'Movie', 100, 1)`,
	); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadProfileTopItems(ctx, "u1", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].JFURL, "item-1") {
		t.Fatalf("got %+v, want a link built straight from item_id, no dim_jf_ref lookup", got)
	}
}
