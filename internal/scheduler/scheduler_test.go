package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/source"
	"github.com/andrii/ephyra/internal/store"
)

type fakeSource struct {
	mtime    atomic.Pointer[time.Time]
	calls    atomic.Int32
	failNext atomic.Bool
	snap     source.LibrarySnapshot
}

func (f *fakeSource) Kind() string { return "fake" }

func (f *fakeSource) PlaybackEvents(context.Context, time.Time) ([]source.PlaybackEvent, error) {
	return nil, source.ErrNotImplemented
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
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	ov, _ := st.ReadLibraryOverview(context.Background())
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
