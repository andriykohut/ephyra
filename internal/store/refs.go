package store

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// dim_jf_ref caches a name's Jellyfin id so profile charts can link into the
// web client. Peoples.Id and Genres.Id in jellyfin.db are not the API's ids
// for those entities -- only a lookup against the running server resolves
// them, and that lookup is a substring search, so a hit is worth keeping
// forever and a miss is worth not repeating on every refresh.

// ReadJFRefs returns every resolved name, keyed by "kind\x1fname" -> jf_id.
// Unresolved names (never looked up, or looked up and missed) are not
// included.
func (s *Store) ReadJFRefs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, name, jf_id FROM dim_jf_ref WHERE jf_id != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var kind, name, id string
		if err := rows.Scan(&kind, &name, &id); err != nil {
			return nil, err
		}
		out[kind+"\x1f"+name] = id
	}
	return out, rows.Err()
}

// DueJFRefs filters names down to the ones worth a Jellyfin lookup: never
// seen, or missed and past its backoff. A resolved name never comes back --
// jf_id, once set, is never rechecked.
func (s *Store) DueJFRefs(ctx context.Context, kind string, names []string, now time.Time) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT name, jf_id, checked_at, miss_count FROM dim_jf_ref WHERE kind = ?`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type seenRef struct {
		id        string
		checkedAt time.Time
		misses    int
	}
	seen := map[string]seenRef{}
	for rows.Next() {
		var name, id, checkedAt string
		var misses int
		if err := rows.Scan(&name, &id, &checkedAt, &misses); err != nil {
			return nil, err
		}
		t, _ := time.Parse(tsLayout, checkedAt)
		seen[name] = seenRef{id: id, checkedAt: t, misses: misses}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var due []string
	for _, name := range names {
		r, ok := seen[name]
		if !ok {
			due = append(due, name)
			continue
		}
		if r.id != "" {
			continue
		}
		// 1, 2, 4 ... days, capped at 64 -- a flat retry would bill every
		// permanently-unresolvable name a lookup forever. Clamped at 0 too:
		// a miss-less row (only reachable by a direct INSERT, since PutJFRef
		// refuses an empty id) would otherwise shift by -1 and panic.
		shift := min(max(r.misses-1, 0), 6)
		backoff := time.Duration(1<<shift) * 24 * time.Hour
		if now.Sub(r.checkedAt) >= backoff {
			due = append(due, name)
		}
	}
	return due, nil
}

// PutJFRef marks name resolved. jf_id, once set, is never overwritten by a
// later miss. id must not be empty -- an empty id is a miss and belongs in
// PutJFMiss; letting one in here would plant a miss_count == 0 row that
// DueJFRefs can't compute a backoff for.
func (s *Store) PutJFRef(ctx context.Context, kind, name, id string, now time.Time) error {
	if id == "" {
		return fmt.Errorf("PutJFRef: empty id for %s %q -- use PutJFMiss for a miss", kind, name)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dim_jf_ref (kind, name, jf_id, checked_at, miss_count)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT (kind, name) DO UPDATE SET jf_id = excluded.jf_id, checked_at = excluded.checked_at`,
		kind, name, id, now.UTC().Format(tsLayout))
	return err
}

// PutJFMiss bumps miss_count, which lengthens the backoff DueJFRefs applies
// to this name next.
func (s *Store) PutJFMiss(ctx context.Context, kind, name string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dim_jf_ref (kind, name, jf_id, checked_at, miss_count)
		VALUES (?, ?, '', ?, 1)
		ON CONFLICT (kind, name) DO UPDATE SET
		  checked_at = excluded.checked_at, miss_count = miss_count + 1`,
		kind, name, now.UTC().Format(tsLayout))
	return err
}

// ChartedNames is the resolver's worklist: every person and genre that
// actually appears in a profile chart, across every library scope and range.
func (s *Store) ChartedNames(ctx context.Context) (people, genres []string, err error) {
	people, err = s.distinctStrings(ctx, `SELECT DISTINCT person FROM agg_profile_people`)
	if err != nil {
		return nil, nil, err
	}
	genres, err = s.distinctStrings(ctx, `SELECT DISTINCT key FROM agg_profile_taste WHERE dim = 'genre'`)
	if err != nil {
		return nil, nil, err
	}
	return people, genres, nil
}

func (s *Store) distinctStrings(ctx context.Context, q string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// jfLinks holds what jfLink needs to build a URL: the Jellyfin base URL from
// config, and the server id. The server id has no config source -- it's only
// known once the resolver asks Jellyfin for it -- so this is primed at boot
// from whatever dim_jf_ref already has, and updated again whenever the
// resolver gets a fresh answer.
type jfLinks struct {
	mu       sync.RWMutex
	base     string
	serverID string
}

// SetJellyfinLinks sets the base URL and server id used to build web-client
// links. Safe to call from any goroutine.
func (s *Store) SetJellyfinLinks(baseURL, serverID string) {
	s.links.mu.Lock()
	defer s.links.mu.Unlock()
	s.links.base = strings.TrimRight(baseURL, "/")
	s.links.serverID = serverID
}

func (s *Store) jellyfinLinks() (base, serverID string) {
	s.links.mu.RLock()
	defer s.links.mu.RUnlock()
	return s.links.base, s.links.serverID
}

// jfLink builds a Jellyfin web-client URL. Items need no lookup -- BaseItems.Id
// is the API's item id -- but people and genres are synthesized id spaces, so
// those ids come from dim_jf_ref and are empty until resolved. base, serverID,
// or id empty returns "", never a half-built URL.
func jfLink(base, serverID, kind, id string) string {
	if base == "" || serverID == "" || id == "" {
		return ""
	}
	if kind == "genre" {
		return base + "/web/#/list?parentId=" + id + "&serverId=" + serverID
	}
	return base + "/web/#/details?id=" + id + "&serverId=" + serverID
}
