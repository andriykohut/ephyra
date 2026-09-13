package scheduler

import (
	"context"
	"time"
)

// refLookupsPerRun bounds how many unresolved people a single watch run will
// ask Jellyfin about. The charted-names set only grows, and each lookup is
// one HTTP round trip -- this keeps a large library from turning a 10-minute
// refresh into a person-by-person crawl.
const refLookupsPerRun = 200

// RefClient is the slice of *jellyfin.Client the resolver needs. nil (the
// zero value of *Scheduler.refc) means Jellyfin is not configured or not yet
// wired, and every name stays unlinked.
type RefClient interface {
	FindPerson(ctx context.Context, name string) (string, error)
	Genres(ctx context.Context) (map[string]string, error)
	ServerID(ctx context.Context) (string, error)
}

// SetRefClient wires the resolver's Jellyfin access after construction, so
// New's signature -- and every existing test that calls it -- stays put.
func (s *Scheduler) SetRefClient(c RefClient) { s.refc = c }

// resolveRefs fills the name -> Jellyfin-id cache for the names that
// actually charted, plus the server id every link needs. Best effort in
// every direction: an unresolved name renders as plain text, and the watch
// job's success never depends on Jellyfin answering.
func (s *Scheduler) resolveRefs(ctx context.Context) {
	cl := s.refc
	if cl == nil {
		return
	}
	now := time.Now()

	// The server id has no config source -- it's read from Jellyfin once and
	// cached in dim_jf_ref like any other ref, so a restart doesn't lose it.
	if due, err := s.st.DueJFRefs(ctx, "server", []string{""}, now); err != nil {
		s.log.Warn("ref resolve: due server", "err", err)
	} else if len(due) > 0 {
		if id, err := cl.ServerID(ctx); err == nil && id != "" {
			_ = s.st.PutJFRef(ctx, "server", "", id, now)
			// Also update the live link config so this run's links work
			// without waiting for a restart to read dim_jf_ref back.
			s.st.SetJellyfinLinks(s.cfg.JellyfinURL, id)
		}
	}

	people, genres, err := s.st.ChartedNames(ctx)
	if err != nil {
		s.log.Warn("ref resolve: charted names", "err", err)
		return
	}

	if due, err := s.st.DueJFRefs(ctx, "genre", genres, now); err != nil {
		s.log.Warn("ref resolve: due genres", "err", err)
	} else if len(due) > 0 {
		if byName, err := cl.Genres(ctx); err == nil {
			for _, name := range due {
				if id := byName[name]; id != "" {
					_ = s.st.PutJFRef(ctx, "genre", name, id, now)
				} else {
					_ = s.st.PutJFMiss(ctx, "genre", name, now)
				}
			}
		}
	}

	due, err := s.st.DueJFRefs(ctx, "person", people, now)
	if err != nil {
		s.log.Warn("ref resolve: due people", "err", err)
		return
	}
	for _, name := range due[:min(len(due), refLookupsPerRun)] {
		id, err := cl.FindPerson(ctx, name)
		if err != nil {
			s.log.Warn("ref resolve failed, will retry", "name", name, "err", err)
			return // Jellyfin is unhappy; stop rather than walk the rest into the same error
		}
		if id == "" {
			_ = s.st.PutJFMiss(ctx, "person", name, now)
			continue
		}
		_ = s.st.PutJFRef(ctx, "person", name, id, now)
	}
}
