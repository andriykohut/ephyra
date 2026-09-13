package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func insertWatchDay(t *testing.T, st *Store, day, userID string, watchSec int64) {
	t.Helper()
	_, err := st.DB().ExecContext(context.Background(), `
		INSERT INTO watch_events_daily
		  (day, user_id, item_id, scope, name, plays, watch_sec, method)
		VALUES (?, ?, 'i1', 'movie', 'Movie', 1, ?, 'DirectPlay')`,
		day, userID, watchSec)
	if err != nil {
		t.Fatal(err)
	}
}

func insertUser(t *testing.T, st *Store, id, name string) {
	t.Helper()
	if _, err := st.DB().ExecContext(context.Background(),
		`INSERT INTO dim_user (id, name) VALUES (?, ?)`, id, name,
	); err != nil {
		t.Fatal(err)
	}
}

func TestReadProfileOverviewActivityExcludesDaysOlderThanAYear(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	insertUser(t, st, "u1", "alice")

	insertWatchDay(t, st, "2026-09-01", "u1", 100) // within the last 365 days
	insertWatchDay(t, st, "2024-01-01", "u1", 200) // well outside it

	ov, ok, err := st.ReadProfileOverview(ctx, "u1", "30d", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a known user")
	}

	var foundRecent bool
	for _, a := range ov.Activity {
		if a.Day == "2024-01-01" {
			t.Fatalf("activity included a day older than 365 days: %+v", ov.Activity)
		}
		if a.Day == "2026-09-01" {
			foundRecent = true
		}
	}
	if !foundRecent {
		t.Fatalf("activity missing the in-window day: %+v", ov.Activity)
	}
}

// TestWatchingSinceIsRangeIndependent covers the case where a user's history
// predates the selected range: agg_profile_summary.first_play is computed
// per-range and gets cut off at the range window for every range but "all",
// so "since" must always read the "all" row instead of the requested one.
func TestWatchingSinceIsRangeIndependent(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	insertUser(t, st, "u1", "alice")

	insertSummary := func(rng, firstPlay string) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO agg_profile_summary (user_id, range, library, first_play)
			VALUES ('u1', ?, '', ?)`, rng, firstPlay,
		); err != nil {
			t.Fatal(err)
		}
	}
	insertSummary("all", "2019-03-01")
	insertSummary("30d", "2026-09-01") // range-cut: the earliest play within the last 30 days

	ov, ok, err := st.ReadProfileOverview(ctx, "u1", "30d", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a known user")
	}
	if ov.Since != "2019-03-01" {
		t.Fatalf("Since = %q on range=30d, want the all-time first play 2019-03-01", ov.Since)
	}
}

func TestReadProfileOverviewReportsUnknownUser(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	_, ok, err := st.ReadProfileOverview(ctx, "ghost", "30d", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false for a user with no dim_user row")
	}
}

func TestGenreLinkOnlyWhenResolved(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	insertUser(t, st, "u1", "alice")
	st.SetJellyfinLinks("http://jf.example", "sid1")
	for _, key := range []string{"Drama", "Noir"} {
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO agg_profile_taste (user_id, range, library, dim, key, watch_sec, plays)
			VALUES ('u1', 'all', '', 'genre', ?, 100, 1)`, key,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutJFRef(ctx, "genre", "Drama", "genre-1", time.Now()); err != nil {
		t.Fatal(err)
	}

	ov, ok, err := st.ReadProfileOverview(ctx, "u1", "all", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected a known user")
	}
	for _, g := range ov.Genres {
		switch g.Key {
		case "Drama":
			if !strings.Contains(g.JFURL, "genre-1") {
				t.Errorf("Drama jf_url = %q, want the resolved id in it", g.JFURL)
			}
		case "Noir":
			if g.JFURL != "" {
				t.Errorf("Noir jf_url = %q, want empty", g.JFURL)
			}
		}
	}
}
