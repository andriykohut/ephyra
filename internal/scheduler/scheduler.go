// Package scheduler runs Ephyra's periodic refresh jobs: the library job and,
// since Plan 2, the watch job.
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/source"
	"github.com/andriykohut/ephyra/internal/store"
)

type Scheduler struct {
	st  *store.Store
	src source.Source
	cfg config.Config
	log *slog.Logger

	libMu   sync.Mutex
	watchMu sync.Mutex
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
	if err := s.RunWatchOnce(ctx); err != nil {
		s.log.Warn("startup watch refresh failed", "err", err)
	}
	lib := time.NewTicker(s.cfg.RefreshLibrary)
	defer lib.Stop()
	wat := time.NewTicker(s.cfg.RefreshWatch)
	defer wat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-lib.C:
			if err := s.RunLibraryOnce(ctx); err != nil {
				s.log.Warn("library refresh failed", "err", err)
			}
		case <-wat.C:
			if err := s.RunWatchOnce(ctx); err != nil {
				s.log.Warn("watch refresh failed", "err", err)
			}
		case job := <-s.trigger:
			if job == "library" || job == "all" {
				if err := s.RunLibraryOnce(ctx); err != nil {
					s.log.Warn("triggered library refresh failed", "err", err)
				}
			}
			if job == "watch" || job == "all" {
				if err := s.RunWatchOnce(ctx); err != nil {
					s.log.Warn("triggered watch refresh failed", "err", err)
				}
			}
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
		return s.recordFailure(ctx, "library", mt, start, err)
	}
	scoped := map[string]aggregate.LibraryAggregates{"": aggregate.Library(snap, time.Local)}
	for _, lib := range aggregate.DistinctItemLibraries(snap.Items) {
		scoped[lib] = aggregate.Library(aggregate.FilterLibrary(snap, lib), time.Local)
	}
	cleanup := aggregate.Cleanup(snap)
	users := aggregate.Users(snap)
	core := aggregate.CorePlays(snap.UserPlays)
	if err := s.st.WriteLibraryAggregates(ctx, scoped, cleanup, users, core); err != nil {
		return s.recordFailure(ctx, "library", mt, start, err)
	}
	s.log.Info("library refresh ok",
		"items", int64(scoped[""].Totals["items.total"]), "dur_ms", time.Since(start).Milliseconds())
	return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: true, Skipped: false,
	})
}

func (s *Scheduler) recordFailure(ctx context.Context, job string, mt, start time.Time, cause error) error {
	_ = s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: job, LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: false, Error: cause.Error(),
	})
	return cause
}

// RunWatchOnce refreshes the watch aggregates from the Playback Reporting plugin
// DB. mtime-skip like the library job. A missing plugin is not a failure: it
// records ok=true, plugin_available=false and writes no agg_watch_* rows.
func (s *Scheduler) RunWatchOnce(ctx context.Context) error {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	start := time.Now()

	var mt time.Time
	if mtimer, ok := s.src.(source.MTimer); ok {
		mt, _ = mtimer.SourceMTime("watch")
	}
	prev, hadPrev, _ := s.st.GetRefreshMeta(ctx, "watch")
	if hadPrev && prev.OK && !prev.SourceMTime.IsZero() && !mt.IsZero() &&
		mt.Equal(prev.SourceMTime) && !s.spineEmpty(ctx) {
		s.log.Info("watch refresh skipped (mtime unchanged)", "mtime", mt)
		return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
			Job: "watch", LastRunAt: time.Now().UTC(), SourceMTime: mt,
			DurationMS: time.Since(start).Milliseconds(), OK: true, Skipped: true,
			PluginAvailable: prev.PluginAvailable,
		})
	}

	events, err := s.src.PlaybackEvents(ctx, time.Time{})
	if errors.Is(err, source.ErrPluginUnavailable) {
		s.log.Info("watch refresh: Playback Reporting plugin not found")
		return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
			Job: "watch", LastRunAt: time.Now().UTC(), SourceMTime: mt,
			DurationMS: time.Since(start).Milliseconds(), OK: true, PluginAvailable: false,
		})
	}
	if err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}

	if err := s.st.AppendPlaybackEvents(ctx, events); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}

	if resolver, ok := s.src.(source.LibraryResolver); ok {
		unknown, err := s.st.ItemsWithUnknownLibrary(ctx)
		if err != nil {
			return s.recordFailure(ctx, "watch", mt, start, err)
		}
		if len(unknown) > 0 {
			resolved, err := resolver.ResolveLibraries(ctx, unknown)
			if err != nil {
				s.log.Warn("library backfill failed, will retry next run", "err", err)
			} else if err := s.st.UpdatePlaybackLibraries(ctx, resolved); err != nil {
				return s.recordFailure(ctx, "watch", mt, start, err)
			}
		}
	}

	history, err := s.st.ReadPlaybackEvents(ctx)
	if err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}

	agg := aggregate.Watch(history)
	if err := s.st.WriteWatchAggregates(ctx, agg.Daily, agg.Heatmap); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
	// TODO(task-14): compute and write per-library scopes; "" ("All libraries")
	// is a placeholder to keep the build green until that wiring lands.
	if err := s.st.WriteProfileAggregates(ctx, map[string]aggregate.ProfileAggregates{
		"": aggregate.Profiles(history, time.Now()),
	}); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
	s.log.Info("watch refresh ok", "events_seen", len(events), "history", len(history),
		"daily_rows", len(agg.Daily), "dur_ms", time.Since(start).Milliseconds())
	return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "watch", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: true, PluginAvailable: true,
	})
}

// spineEmpty reports whether playback_events has no rows -- used to force a full
// backfill on the first run after this feature ships, even when the plugin DB's
// mtime hasn't moved.
func (s *Scheduler) spineEmpty(ctx context.Context) bool {
	var n int
	err := s.st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&n)
	return err == nil && n == 0
}
