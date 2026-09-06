package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/source"
	"github.com/andriykohut/ephyra/internal/store"
)

type fakeSource struct {
	mtime    atomic.Pointer[time.Time]
	calls    atomic.Int32
	failNext atomic.Bool
	snap     source.LibrarySnapshot

	events       []source.PlaybackEvent
	pluginAbsent bool

	resolved   map[string]string // itemID -> library, backs ResolveLibraries
	resolveErr error             // if set, ResolveLibraries returns this instead
}

func (f *fakeSource) Kind() string { return "fake" }

// ResolveLibraries implements source.LibraryResolver so scheduler tests can
// exercise the one-time backfill wiring without a real jellyfin.db. Ids not
// present in resolved are simply absent from the result, like the real
// FileSource does for items no longer in BaseItems. If resolveErr is set, it
// is returned instead — used to exercise the backfill's non-fatal error path.
func (f *fakeSource) ResolveLibraries(_ context.Context, itemIDs []string) (map[string]string, error) {
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	out := map[string]string{}
	for _, id := range itemIDs {
		if lib, ok := f.resolved[id]; ok {
			out[id] = lib
		}
	}
	return out, nil
}

func (f *fakeSource) PlaybackEvents(context.Context, time.Time) ([]source.PlaybackEvent, error) {
	if f.pluginAbsent {
		return nil, source.ErrPluginUnavailable
	}
	return f.events, nil
}

func (f *fakeSource) LibraryFacts(context.Context) (source.LibrarySnapshot, error) {
	f.calls.Add(1)
	if f.failNext.Swap(false) {
		return source.LibrarySnapshot{}, errors.New("boom")
	}
	return f.snap, nil
}

func (f *fakeSource) SourceMTime(string) (time.Time, error) {
	if p := f.mtime.Load(); p != nil {
		return *p, nil
	}
	return time.Time{}, nil
}

func newSched(t *testing.T) (*Scheduler, *fakeSource, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := &fakeSource{snap: source.LibrarySnapshot{Items: []source.LibraryItem{
		{Name: "M", Type: "movie", SizeBytes: 1, RuntimeSec: 1, Year: 2020, Library: "Movies", Container: "mkv", Width: 1920, HasVideo: true},
	}}}
	ts := time.Unix(1000, 0)
	fs.mtime.Store(&ts)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sc, fs, st
}

func TestRunLibraryOnce_FullRun(t *testing.T) {
	sc, fs, st := newSched(t)
	if err := sc.RunLibraryOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.calls.Load() != 1 {
		t.Fatalf("LibraryFacts calls = %d", fs.calls.Load())
	}
	m, ok, _ := st.GetRefreshMeta(context.Background(), "library")
	if !ok || !m.OK || m.Skipped {
		t.Fatalf("refresh_meta: %+v ok=%v", m, ok)
	}
	ov, _ := st.ReadLibraryOverview(context.Background(), "")
	if ov.Totals.Items != 1 {
		t.Fatalf("overview not written: %+v", ov.Totals)
	}
}

func TestRunLibraryOnce_SkipWhenMtimeUnchanged(t *testing.T) {
	sc, fs, st := newSched(t)
	ctx := context.Background()
	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := fs.calls.Load()

	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if fs.calls.Load() != callsAfterFirst {
		t.Fatalf("expected skip, calls went %d -> %d", callsAfterFirst, fs.calls.Load())
	}
	m, _, _ := st.GetRefreshMeta(ctx, "library")
	if !m.Skipped || !m.OK {
		t.Fatalf("expected ok+skipped meta, got %+v", m)
	}
}

func TestRunLibraryOnce_ChangedMtimeReRuns(t *testing.T) {
	sc, fs, st := newSched(t)
	ctx := context.Background()
	_ = sc.RunLibraryOnce(ctx)
	c1 := fs.calls.Load()

	newMt := time.Unix(2000, 0)
	fs.mtime.Store(&newMt)
	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if fs.calls.Load() != c1+1 {
		t.Fatalf("expected re-run, calls %d -> %d", c1, fs.calls.Load())
	}
	m, _, _ := st.GetRefreshMeta(ctx, "library")
	if m.Skipped {
		t.Fatal("should not be skipped after mtime change")
	}
}

func TestRunLibraryOnce_ErrorRecorded(t *testing.T) {
	sc, fs, st := newSched(t)
	fs.failNext.Store(true)
	if err := sc.RunLibraryOnce(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	m, ok, _ := st.GetRefreshMeta(context.Background(), "library")
	if !ok || m.OK || m.Error == "" {
		t.Fatalf("error not recorded: %+v", m)
	}
}

func TestRun_StartupRunsLibraryOnce(t *testing.T) {
	sc, fs, _ := newSched(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sc.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for fs.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("startup did not run library job")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestRunLibraryOnce_PopulatesCleanupUsersCore(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	e := source.LibraryItem{ID: "e1", Name: "S1E1", Type: "episode", SizeBytes: 10, Library: "Shows",
		SeriesID: "s1", SeriesName: "Some Show"}
	m := source.LibraryItem{ID: "m1", Name: "Alpha", Type: "movie", SizeBytes: 20, Library: "Movies"}
	fs := &fakeSource{snap: source.LibrarySnapshot{
		Items:     []source.LibraryItem{m, e},
		Users:     []source.UserRef{{ID: "u1", Name: "alice"}, {ID: "u2", Name: "bob"}, {ID: "u3", Name: "z"}},
		UserPlays: []source.UserPlay{{UserID: "u1", Scope: "movie", ItemID: "m1", Name: "Alpha", PlayCount: 3}},
	}}
	ts := time.Unix(1000, 0)
	fs.mtime.Store(&ts)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var cleanup, users, core int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_cleanup`).Scan(&cleanup)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM dim_user`).Scan(&users)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_played_core`).Scan(&core)
	if cleanup != 2 || users != 3 || core != 1 {
		t.Fatalf("cleanup=%d users=%d core=%d (want 2/3/1)", cleanup, users, core)
	}
	var scope string
	if err := st.DB().QueryRowContext(ctx, `SELECT scope FROM agg_cleanup WHERE scope='series' LIMIT 1`).Scan(&scope); err != nil {
		t.Fatalf("no series row in agg_cleanup: %v", err)
	}
}

func TestRunLibraryOnce_PerLibraryAggregates(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	var items []source.LibraryItem
	for i := 0; i < 4; i++ {
		items = append(items, source.LibraryItem{
			ID: "m" + string(rune('1'+i)), Name: "Movie", Type: "movie", SizeBytes: 1, Library: "Movies",
		})
	}
	for i := 0; i < 2; i++ {
		items = append(items, source.LibraryItem{
			ID: "s" + string(rune('1'+i)), Name: "Show", Type: "movie", SizeBytes: 1, Library: "Shows",
		})
	}
	fs := &fakeSource{snap: source.LibrarySnapshot{Items: items}}
	ts := time.Unix(1000, 0)
	fs.mtime.Store(&ts)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}

	all, err := st.ReadLibraryOverview(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	movies, err := st.ReadLibraryOverview(ctx, "Movies")
	if err != nil {
		t.Fatal(err)
	}
	shows, err := st.ReadLibraryOverview(ctx, "Shows")
	if err != nil {
		t.Fatal(err)
	}
	if movies.Totals.Items+shows.Totals.Items != all.Totals.Items {
		t.Fatalf("movies(%d)+shows(%d) != all(%d)", movies.Totals.Items, shows.Totals.Items, all.Totals.Items)
	}
	if movies.Totals.Items != 4 || shows.Totals.Items != 2 {
		t.Fatalf("movies=%d shows=%d, want 4/2", movies.Totals.Items, shows.Totals.Items)
	}
}

func TestRunWatchOnce_PopulatesWatchTables(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fs := &fakeSource{events: []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 3600},
		{At: time.Date(2025, 1, 7, 21, 0, 0, 0, time.UTC), UserID: "u2", ItemID: "e1", ItemType: "episode", SeriesID: "s1", SeriesName: "Show", Method: "Transcode (v:h264 a:aac)", PlayDurationSec: 1500},
	}}
	ts := time.Unix(1000, 0)
	fs.mtime.Store(&ts)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var daily, heat int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM watch_events_daily`).Scan(&daily)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_watch_heatmap`).Scan(&heat)
	if daily != 2 || heat != 2 {
		t.Fatalf("daily=%d heat=%d", daily, heat)
	}
	m, ok, _ := st.GetRefreshMeta(ctx, "watch")
	if !ok || !m.OK || !m.PluginAvailable {
		t.Fatalf("watch meta: %+v ok=%v", m, ok)
	}
}

func TestRunWatchOnce_PluginAbsent(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fs := &fakeSource{pluginAbsent: true}
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatalf("plugin-absent should not error: %v", err)
	}
	m, ok, _ := st.GetRefreshMeta(ctx, "watch")
	if !ok || !m.OK || m.PluginAvailable {
		t.Fatalf("want ok=true plugin_available=false, got %+v", m)
	}
	var daily int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM watch_events_daily`).Scan(&daily)
	if daily != 0 {
		t.Fatalf("no rows expected, got %d", daily)
	}
}

func TestRunWatchOnce_SpineAccumulatesAcrossRuns(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fs := &fakeSource{events: []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 3600},
	}}
	mt1 := time.Unix(1000, 0)
	fs.mtime.Store(&mt1)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// second run: a NEW event, and the source no longer reports the first one
	fs.events = []source.PlaybackEvent{
		{At: time.Date(2025, 1, 7, 21, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m2", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 1200},
	}
	mt2 := time.Unix(2000, 0)
	fs.mtime.Store(&mt2)
	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var spine, daily int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&spine)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM watch_events_daily`).Scan(&daily)
	if spine != 2 || daily != 2 {
		t.Fatalf("spine=%d daily=%d, want 2/2 (history is the union)", spine, daily)
	}
}

func TestRunWatchOnce_EmptySpineBypassesMtimeSkip(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// Simulate "already ran once pre-upgrade": a watch refresh_meta row with the
	// current mtime, but an empty spine.
	mt := time.Unix(1000, 0)
	if err := st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "watch", LastRunAt: time.Now().UTC(), SourceMTime: mt, OK: true, PluginAvailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSource{events: []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 3600},
	}}
	fs.mtime.Store(&mt) // unchanged mtime -> would normally skip
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var spine int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&spine)
	if spine != 1 {
		t.Fatalf("empty spine should have forced a backfill despite unchanged mtime, spine=%d", spine)
	}
}

func TestRunWatchOnce_BackfillsUnknownLibrary(t *testing.T) {
	ctx := context.Background()
	sc, fs, st := newSched(t)

	fs.events = []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 3600, Library: "Unknown"},
	}
	fs.resolved = map[string]string{"m1": "Movies"}

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	unknown, err := st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("expected backfill to resolve m1, still unknown: %v", unknown)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.ItemID == "m1" && e.Library != "Movies" {
			t.Errorf("m1 library not backfilled by resolver: %q", e.Library)
		}
	}
}

func TestRunWatchOnce_LibraryBackfillErrorIsNonFatal(t *testing.T) {
	ctx := context.Background()
	sc, fs, st := newSched(t)

	fs.events = []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 3600, Library: "Unknown"},
	}
	fs.resolveErr = errors.New("jellyfin.db unreachable")

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatalf("resolver error should not fail the watch run: %v", err)
	}
	m, ok, _ := st.GetRefreshMeta(ctx, "watch")
	if !ok || !m.OK {
		t.Fatalf("watch meta should still record ok=true: %+v ok=%v", m, ok)
	}

	// The row should still be Unknown (unresolved), ready to retry next run.
	unknown, err := st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != "m1" {
		t.Fatalf("unknown = %v, want [m1] left for the next run's retry", unknown)
	}
}

func TestRunWatchOnce_PopulatesProfileTables(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fs := &fakeSource{events: []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 6000, ItemRuntimeSec: 6000, ItemYear: 1994, ItemGenres: []string{"Drama"}},
	}}
	mt := time.Unix(1000, 0)
	fs.mtime.Store(&mt)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var summ int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_profile_summary WHERE user_id='u1'`).Scan(&summ)
	if summ != 4 { // one row per range
		t.Fatalf("agg_profile_summary rows for u1 = %d, want 4", summ)
	}
	// The library facts must survive the spine round-trip: a fully-watched movie
	// with a known runtime lands in "finished", not "unknown".
	var bucket string
	st.DB().QueryRowContext(ctx, `SELECT bucket FROM agg_profile_completion WHERE user_id='u1' AND range='all' AND scope='movie'`).Scan(&bucket)
	if bucket != "finished" {
		t.Fatalf("completion bucket = %q, want finished (runtime lost through the spine?)", bucket)
	}

	// plugin goes away on the next run -> profile rows stay put
	fs.pluginAbsent = true
	mt2 := time.Unix(2000, 0)
	fs.mtime.Store(&mt2)
	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_profile_summary WHERE user_id='u1'`).Scan(&summ)
	if summ != 4 {
		t.Fatalf("profile rows should survive a plugin-absent run, got %d", summ)
	}
}

func TestRunWatchOnce_PerLibraryProfiles(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// alice has plays only in the Movies library; she has none in Shows.
	fs := &fakeSource{
		snap: source.LibrarySnapshot{Users: []source.UserRef{{ID: "alice", Name: "Alice"}}},
		events: []source.PlaybackEvent{
			{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "alice", ItemID: "m1", ItemType: "movie",
				Method: "DirectPlay", PlayDurationSec: 3600, ItemRuntimeSec: 3600, ItemYear: 1994,
				ItemGenres: []string{"Drama"}, Library: "Movies"},
			{At: time.Date(2025, 1, 7, 20, 0, 0, 0, time.UTC), UserID: "alice", ItemID: "m2", ItemType: "movie",
				Method: "DirectPlay", PlayDurationSec: 1800, ItemRuntimeSec: 3600, ItemYear: 2001,
				ItemGenres: []string{"Comedy"}, Library: "Movies"},
		},
	}
	mt := time.Unix(1000, 0)
	fs.mtime.Store(&mt)
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour, RefreshWatch: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	all, ok, err := st.ReadProfile(ctx, "alice", "all", "")
	if err != nil || !ok {
		t.Fatalf("all: ok=%v err=%v", ok, err)
	}
	if all.Summary.WatchSec != 5400 {
		t.Fatalf("all watch_sec = %d, want 5400", all.Summary.WatchSec)
	}

	movies, ok, err := st.ReadProfile(ctx, "alice", "all", "Movies")
	if err != nil || !ok {
		t.Fatalf("movies: ok=%v err=%v", ok, err)
	}
	if movies.Summary.WatchSec != all.Summary.WatchSec {
		t.Fatalf("alice has only Movies plays, movies(%d) should equal all(%d)",
			movies.Summary.WatchSec, all.Summary.WatchSec)
	}

	shows, ok, err := st.ReadProfile(ctx, "alice", "all", "Shows")
	if err != nil || !ok {
		t.Fatalf("shows: ok=%v err=%v", ok, err)
	}
	if shows.Summary.WatchSec != 0 {
		t.Fatalf("alice has no Shows plays, want 0, got %d", shows.Summary.WatchSec)
	}
}
