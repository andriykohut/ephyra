// Package scheduler runs Ephyra's periodic refresh jobs. In Plan 1 that's just
// the library job.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/andrii/ephyra/internal/aggregate"
	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/source"
	"github.com/andrii/ephyra/internal/store"
)

type Scheduler struct {
	st  *store.Store
	src source.Source
	cfg config.Config
	log *slog.Logger

	libMu   sync.Mutex
	trigger chan string
}

func New(st *store.Store, src source.Source, cfg config.Config, log *slog.Logger) *Scheduler {
	return &Scheduler{st: st, src: src, cfg: cfg, log: log, trigger: make(chan string, 8)}
}

// Trigger asks for an out-of-band refresh. It never blocks; if a refresh is
// already queued the request is dropped.
func (s *Scheduler) Trigger(job string) {
	select {
	case s.trigger <- job:
	default:
	}
}

// Run blocks until ctx is done: one startup refresh, then the ticker plus any
// manual triggers.
func (s *Scheduler) Run(ctx context.Context) {
	if err := s.RunLibraryOnce(ctx); err != nil {
		s.log.Warn("startup library refresh failed", "err", err)
	}
	lib := time.NewTicker(s.cfg.RefreshLibrary)
	defer lib.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-lib.C:
			if err := s.RunLibraryOnce(ctx); err != nil {
				s.log.Warn("library refresh failed", "err", err)
			}
		case job := <-s.trigger:
			if job == "library" || job == "all" {
				if err := s.RunLibraryOnce(ctx); err != nil {
					s.log.Warn("triggered library refresh failed", "err", err)
				}
			}
			// "watch" arrives in Plan 2
		}
	}
}

// RunLibraryOnce refreshes the library aggregates unless the source file's mtime
// is unchanged since the last successful run, in which case it just records a
// skip. Safe to call concurrently; it serializes on a mutex.
func (s *Scheduler) RunLibraryOnce(ctx context.Context) error {
	s.libMu.Lock()
	defer s.libMu.Unlock()
	start := time.Now()

	var mt time.Time
	if mtimer, ok := s.src.(source.MTimer); ok {
		mt, _ = mtimer.SourceMTime("library")
	}
	prev, hadPrev, _ := s.st.GetRefreshMeta(ctx, "library")
	if hadPrev && !prev.SourceMTime.IsZero() && !mt.IsZero() && mt.Equal(prev.SourceMTime) {
		s.log.Info("library refresh skipped (mtime unchanged)", "mtime", mt)
		return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
			Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
			DurationMS: time.Since(start).Milliseconds(), OK: true, Skipped: true,
		})
	}

	snap, err := s.src.LibraryFacts(ctx)
	if err != nil {
		return s.recordFailure(ctx, mt, start, err)
	}
	agg := aggregate.Library(snap, time.Local)
	if err := s.st.WriteLibraryAggregates(ctx, agg); err != nil {
		return s.recordFailure(ctx, mt, start, err)
	}
	s.log.Info("library refresh ok",
		"items", int64(agg.Totals["items.total"]), "dur_ms", time.Since(start).Milliseconds())
	return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: true, Skipped: false,
	})
}

func (s *Scheduler) recordFailure(ctx context.Context, mt, start time.Time, cause error) error {
	_ = s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: false, Error: cause.Error(),
	})
	return cause
}
