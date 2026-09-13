package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/source"
	"github.com/andriykohut/ephyra/internal/store"
)

type fakeRefClient struct {
	people    map[string]string
	genres    map[string]string
	serverID  string
	findErr   error
	findCalls int
}

func (f *fakeRefClient) FindPerson(_ context.Context, name string) (string, error) {
	f.findCalls++
	if f.findErr != nil {
		return "", f.findErr
	}
	return f.people[name], nil
}

func (f *fakeRefClient) Genres(context.Context) (map[string]string, error) { return f.genres, nil }

func (f *fakeRefClient) ServerID(context.Context) (string, error) { return f.serverID, nil }

// schedWithPeopleAndGenre returns a scheduler whose watch job, once run, has
// charted one person ("Ada Vex") and one genre ("Drama") -- the resolver's
// worklist for the tests below.
func schedWithPeopleAndGenre(t *testing.T) (*Scheduler, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.UpsertCredits(ctx, []source.Credit{
		{ItemID: "m1", Person: "Ada Vex", Kind: "actor"},
	}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSource{events: []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie",
			Method: "DirectPlay", PlayDurationSec: 6000, ItemRuntimeSec: 6000, ItemYear: 1994,
			ItemGenres: []string{"Drama"}},
	}}
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sc, st
}

func TestResolveRefsNoopWithoutClient(t *testing.T) {
	sc, st := schedWithPeopleAndGenre(t)
	ctx := context.Background()

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	refs, err := st.ReadJFRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %v, want none resolved with no client wired", refs)
	}
	m, ok, _ := st.GetRefreshMeta(ctx, "watch")
	if !ok || !m.OK {
		t.Fatalf("watch job should still succeed with no ref client: %+v", m)
	}
}

func TestResolveRefsPopulatesPersonGenreAndServerRefs(t *testing.T) {
	sc, st := schedWithPeopleAndGenre(t)
	ctx := context.Background()
	fc := &fakeRefClient{
		people:   map[string]string{"Ada Vex": "person-1"},
		genres:   map[string]string{"Drama": "genre-1"},
		serverID: "server-1",
	}
	sc.SetRefClient(fc)

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	refs, err := st.ReadJFRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if refs["person\x1fAda Vex"] != "person-1" {
		t.Errorf("person ref = %v", refs)
	}
	if refs["genre\x1fDrama"] != "genre-1" {
		t.Errorf("genre ref = %v", refs)
	}
	if refs["server\x1f"] != "server-1" {
		t.Errorf("server ref = %v", refs)
	}
}

func TestResolveRefsFindPersonErrorLeavesWatchJobSuccessful(t *testing.T) {
	sc, st := schedWithPeopleAndGenre(t)
	ctx := context.Background()
	sc.SetRefClient(&fakeRefClient{findErr: errors.New("jellyfin unreachable")})

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatalf("resolver failure must not fail the watch job: %v", err)
	}
	m, ok, _ := st.GetRefreshMeta(ctx, "watch")
	if !ok || !m.OK {
		t.Fatalf("watch job should report success despite a FindPerson error: %+v", m)
	}
}

// TestResolveRefsCapsPersonLookupsPerRun seeds agg_profile_people directly --
// the aggregate.People step caps a single ranking at 50 rows, which would
// mask the resolver's own cap if this went through a real watch run instead.
func TestResolveRefsCapsPersonLookupsPerRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	for i := 0; i < refLookupsPerRun+5; i++ {
		if _, err := st.DB().ExecContext(ctx, `
			INSERT INTO agg_profile_people (library, user_id, range, kind, person)
			VALUES ('', 'u1', 'all', 'actor', ?)`, fmt.Sprintf("Person-%03d", i),
		); err != nil {
			t.Fatal(err)
		}
	}

	sc := New(st, &fakeSource{}, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	fc := &fakeRefClient{people: map[string]string{}, genres: map[string]string{}}
	sc.SetRefClient(fc)

	sc.resolveRefs(ctx)

	if fc.findCalls != refLookupsPerRun {
		t.Fatalf("FindPerson calls = %d, want the %d-per-run cap", fc.findCalls, refLookupsPerRun)
	}
}
