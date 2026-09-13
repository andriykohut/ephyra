package store

import (
	"context"
	"testing"
	"time"
)

// TestJFLinkRequiresServerID guards against a half-built "...&serverId=" URL:
// before the resolver's first /System/Info call, serverID is still "" even
// though base and id are already known.
func TestJFLinkRequiresServerID(t *testing.T) {
	if got := jfLink("http://jf.example", "", "item", "i1"); got != "" {
		t.Fatalf("jfLink with empty serverID = %q, want empty", got)
	}
	if got := jfLink("http://jf.example", "sid1", "item", "i1"); got == "" {
		t.Fatal("jfLink with base, serverID and id all set should build a URL")
	}
}

func TestJFMissBacksOff(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	day0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	if err := st.PutJFMiss(ctx, "person", "Ghost", day0); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueJFRefs(ctx, "person", []string{"Ghost"}, day0.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Error("a miss should not retry within the first day")
	}

	due, err = st.DueJFRefs(ctx, "person", []string{"Ghost"}, day0.Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatal("first retry is due after a day")
	}
	if err := st.PutJFMiss(ctx, "person", "Ghost", day0.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Second miss doubles the wait. A flat retry would bill every permanently
	// unresolvable name a lookup forever, and that set only grows.
	due, err = st.DueJFRefs(ctx, "person", []string{"Ghost"}, day0.Add(26*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Error("second wait should be two days, not one")
	}
}

func TestJFHitIsNeverRechecked(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	day0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	if err := st.PutJFRef(ctx, "person", "Ada Vex", "abc123", day0); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueJFRefs(ctx, "person", []string{"Ada Vex"}, day0.AddDate(5, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatal("a resolved name should never be looked up again")
	}
}

func TestDueJFRefsIncludesNamesNeverLookedUp(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	due, err := st.DueJFRefs(ctx, "person", []string{"Nobody"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0] != "Nobody" {
		t.Fatalf("due = %v, want [Nobody]", due)
	}
}

func TestReadJFRefsOmitsMissesAndUnseenNames(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Now()

	if err := st.PutJFRef(ctx, "person", "Ada Vex", "abc123", now); err != nil {
		t.Fatal(err)
	}
	if err := st.PutJFMiss(ctx, "person", "Ghost", now); err != nil {
		t.Fatal(err)
	}

	refs, err := st.ReadJFRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if refs["person\x1fAda Vex"] != "abc123" {
		t.Fatalf("refs = %v, want the resolved id keyed by kind\\x1fname", refs)
	}
	if _, ok := refs["person\x1fGhost"]; ok {
		t.Fatal("a missed name should not appear in ReadJFRefs")
	}
	if _, ok := refs["person\x1fNobody"]; ok {
		t.Fatal("a name with no dim_jf_ref row at all should not appear in ReadJFRefs")
	}
}

// TestDueJFRefsHandlesZeroMissCountEmptyIDRowWithoutPanicking guards a shape
// PutJFRef itself now refuses to create (see TestPutJFRefRejectsEmptyID) --
// planted directly here since that's the only way it can still occur.
func TestDueJFRefsHandlesZeroMissCountEmptyIDRowWithoutPanicking(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Now()

	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO dim_jf_ref (kind, name, jf_id, checked_at, miss_count)
		VALUES ('person', 'Weird', '', ?, 0)`, now.UTC().Format(tsLayout),
	); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueJFRefs(ctx, "person", []string{"Weird"}, now.Add(2*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("due = %v, want [Weird] once its (clamped, zero-shift) backoff has passed", due)
	}
}

func TestPutJFRefRejectsEmptyID(t *testing.T) {
	st := openStore(t)
	if err := st.PutJFRef(context.Background(), "person", "Ghost", "", time.Now()); err == nil {
		t.Fatal("want an error for an empty id -- an empty id is a miss, not a ref")
	}
}

func TestChartedNamesReadsPeopleAndGenreDims(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO agg_profile_people (library, user_id, range, kind, person)
		VALUES ('', 'u1', 'all', 'actor', 'Ada Vex')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO agg_profile_taste (library, user_id, range, dim, key, watch_sec, plays)
		VALUES ('', 'u1', 'all', 'genre', 'Drama', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO agg_profile_taste (library, user_id, range, dim, key, watch_sec, plays)
		VALUES ('', 'u1', 'all', 'tag', 'not-a-genre', 0, 0)`); err != nil {
		t.Fatal(err)
	}

	people, genres, err := st.ChartedNames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || people[0] != "Ada Vex" {
		t.Fatalf("people = %v, want [Ada Vex]", people)
	}
	if len(genres) != 1 || genres[0] != "Drama" {
		t.Fatalf("genres = %v, want [Drama] -- the 'tag' dim row must not leak in", genres)
	}
}
