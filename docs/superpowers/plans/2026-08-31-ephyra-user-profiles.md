# User Profiles ("Wrapped") Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a fifth page, `/profile`, showing per-user completion vs abandonment, rewatch, binge runs, and a taste fingerprint — backed by a new append-only `playback_events` table that outlives the Playback Reporting plugin's retention; Watch Stats reads from the same spine.

**Architecture:** The `watch` job gains a step: after it fetches plugin rows (unchanged), it appends them to `playback_events` (idempotent `INSERT … ON CONFLICT`), reads the full accumulated history back, and feeds *that* to both `aggregate.Watch` (unchanged logic) and a new pure `aggregate.Profiles`. Profiles precomputes every panel for the four fixed ranges and writes materialized `agg_profile_*` tables. Two new read-only endpoints serve them. No new calls to Jellyfin.

**Tech Stack:** Go 1.25 (`CGO_ENABLED=0`, pure-Go SQLite via `modernc.org/sqlite`), `net/http` `ServeMux`; React 19 + Vite + TypeScript, Tailwind v4, TanStack Router + Query, ECharts; Vitest + Biome.

**Spec:** `docs/superpowers/specs/2026-08-31-ephyra-user-profiles-design.md` — read it alongside this plan.

## Global Constraints

- Go module `github.com/andriykohut/ephyra`, floor `go 1.25`. Always `CGO_ENABLED=0`.
- `make test` → `CGO_ENABLED=0 go test ./...`. `make lint` → `go vet ./...` + `golangci-lint run` (**golangci-lint v2**, `.golangci.yml` is v2 schema; binary often at `~/go/bin/golangci-lint`).
- Frontend: in `web/`, `npm run test` (Vitest), `npm run lint` → `biome ci .`, `npx tsc -b`, `npm run build`.
- **Never write to Jellyfin or the mounted directory.** Read-only, always.
- **Never serve 5xx for a page because a refresh failed.** A failed/absent refresh → `meta.stale=true` (or a documented `not_ready` 503 only when the *library* refresh has never run).
- Every JSON response is `{ "data": ..., "meta": { "generated_at": <RFC3339>, "stale": <bool> } }`; errors are `{ "error": { "code", "message" } }`. Unknown `/api/*` → 404 JSON.
- List fields serialize as `[]`, never `null` — run every returned slice through `store.orEmpty` before returning.
- Store migrations: embedded `internal/store/migrations/NNNN_*.sql`, forward-only, integer prefix. The runner owns `schema_migrations` — a migration must **not** `CREATE` or touch it.
- Canonical IDs everywhere in the store: dashless lowercase (`source.CanonID`). The plugin DB is already that shape; `jellyfin.db` is dashed-uppercase and normalized on read.
- **Watch timestamps are not re-zoned.** The plugin writes server-local wall time; bucket off the literal components. (Contrast `aggregate.Library`, whose `jellyfin.db` `DateCreated` is UTC.)
- Docs & comments: casual, deadpan. No exclamation marks, no "simply" / "powerful" / marketing voice.
- Commit after every task (frequent commits). DRY, YAGNI, TDD.

---

## File Structure

**Go — created:**
- `internal/store/migrations/0003_profiles.sql` — spine + `agg_profile_*` + `agg_taste_baseline` DDL.
- `internal/store/spine.go` — `AppendPlaybackEvents`, `ReadPlaybackEvents`, `SpineCoverage`, `spineDedupHash`.
- `internal/store/spine_test.go`.
- `internal/store/writes_profile.go` — `WriteProfileAggregates`.
- `internal/store/writes_profile_test.go`.
- `internal/store/reads_profile.go` — `ProfileList` / `Profile` DTOs + `ReadProfileList`, `ReadProfile`.
- `internal/store/reads_profile_test.go`.
- `internal/aggregate/profile.go` — `Profiles(events, now) ProfileAggregates` + panel helpers + threshold constants.
- `internal/aggregate/profile_test.go`.
- `internal/api/profile.go` — `handleProfileList`, `handleProfile`.
- `internal/api/profile_test.go`.

**Go — modified:**
- `internal/aggregate/rows.go` — add profile row structs + `ProfileAggregates`.
- `internal/source/source.go` — add `ItemRuntimeSec`, `ItemGenres`, `ItemYear` to `PlaybackEvent`.
- `internal/source/file/playback.go` — widen `enrichPlaybackEvents` query.
- `internal/scheduler/scheduler.go` — `RunWatchOnce`: empty-spine skip bypass, spine append, read-back, feed both aggregators.
- `internal/api/api.go` — register `GET /api/profile` and `GET /api/profile/{userID}`.
- `internal/api/smoke_test.go` (or wherever the smoke sweep lives) — hit the two new endpoints.
- `testdata/library.fixture.sql`, `testdata/playback_reporting.fixture.sql` — rows that make profile ratios deterministic for the source + smoke tests.

**Frontend — created:**
- `web/src/routes/profile.tsx` — `/profile` route, `ProfileView`, panel components.
- `web/src/routes/profile.test.tsx`.

**Frontend — modified:**
- `web/src/api/types.ts` — `ProfileList`, `Profile` types.
- `web/src/api/queries.ts` — `profileListQuery`, `profileQuery`.
- `web/src/router.tsx` — register `profileRoute`.
- `web/src/components/AppShell.tsx` — nav entry.

**Docs — modified:**
- `README.md` — one line on `playback_events` growth.
- `CHANGELOG.md` — the feature + the Watch Stats history-retention note.

---

## Task 1: Migration `0003_profiles.sql`

**Files:**
- Create: `internal/store/migrations/0003_profiles.sql`
- Test: `internal/store/store_test.go` (add a test function)

**Interfaces:**
- Consumes: the migration runner in `internal/store/store.go` (`Open` applies pending migrations in filename order).
- Produces: tables `playback_events`, `agg_profile_summary`, `agg_profile_completion`, `agg_profile_abandoned`, `agg_profile_rewatch`, `agg_profile_binge`, `agg_profile_taste`, `agg_taste_baseline`. `schema_migrations` now contains version `3`.

- [ ] **Step 1: Write the failing test**

Add to `internal/store/store_test.go`:

```go
func TestMigrate_0003_ProfileTables(t *testing.T) {
	st, err := Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	want := []string{
		"playback_events", "agg_profile_summary", "agg_profile_completion",
		"agg_profile_abandoned", "agg_profile_rewatch", "agg_profile_binge",
		"agg_profile_taste", "agg_taste_baseline",
	}
	for _, name := range want {
		var got string
		err := st.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&got)
		if err != nil {
			t.Fatalf("table %q missing: %v", name, err)
		}
	}

	var v int
	if err := st.DB().QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != 3 {
		t.Fatalf("schema_migrations max version = %d, want 3", v)
	}

	// the spine's unique index is what makes the append idempotent
	var idx int
	st.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='playback_events' AND sql LIKE '%dedup_hash%'`,
	).Scan(&idx)
	if idx == 0 {
		t.Fatal("expected a unique index on playback_events.dedup_hash")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestMigrate_0003_ProfileTables -v`
Expected: FAIL — `table "playback_events" missing`.

- [ ] **Step 3: Write the migration**

Create `internal/store/migrations/0003_profiles.sql`:

```sql
-- Adds the playback-event spine and the per-user profile aggregates. The spine
-- is append-only -- it is the only table Ephyra never rewrites -- and outlives
-- the Playback Reporting plugin's max-age setting. The agg_profile_* tables are
-- full-rewritten every watch run, like the other agg_* tables. Forward-only;
-- the migration runner owns schema_migrations, do not touch it here.

CREATE TABLE playback_events (
  at                TEXT NOT NULL,     -- plugin server-local wall clock, not re-zoned
  user_id           TEXT NOT NULL,     -- canonical
  item_id           TEXT NOT NULL,     -- canonical
  item_type         TEXT NOT NULL,     -- 'movie' | 'episode'
  method            TEXT NOT NULL,     -- raw plugin PlaybackMethod string
  play_duration_sec INTEGER NOT NULL,
  item_name         TEXT NOT NULL DEFAULT '',
  series_id         TEXT NOT NULL DEFAULT '',
  series_name       TEXT NOT NULL DEFAULT '',
  dedup_hash        TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_playback_events_dedup ON playback_events (dedup_hash);
CREATE INDEX ix_playback_events_user_item ON playback_events (user_id, item_id);
CREATE INDEX ix_playback_events_at ON playback_events (at);

CREATE TABLE agg_profile_summary (
  user_id                   TEXT NOT NULL,
  range                     TEXT NOT NULL,   -- '30d' | '90d' | '1y' | 'all'
  watch_sec                 INTEGER NOT NULL DEFAULT 0,
  plays                     INTEGER NOT NULL DEFAULT 0,
  distinct_titles           INTEGER NOT NULL DEFAULT 0,
  days_active               INTEGER NOT NULL DEFAULT 0,
  finished_pct              REAL    NOT NULL DEFAULT 0,
  bailed_pct                REAL    NOT NULL DEFAULT 0,
  rewatch_pct               REAL    NOT NULL DEFAULT 0,   -- meaningful only at range='all'
  longest_binge_episodes    INTEGER NOT NULL DEFAULT 0,   -- lifetime
  longest_binge_series_name TEXT    NOT NULL DEFAULT '',  -- lifetime
  show_of_range_series_id   TEXT    NOT NULL DEFAULT '',
  show_of_range_series_name TEXT    NOT NULL DEFAULT '',
  first_play                TEXT    NOT NULL DEFAULT '',
  last_play                 TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, range)
);

CREATE TABLE agg_profile_completion (
  user_id TEXT NOT NULL,
  range   TEXT NOT NULL,
  scope   TEXT NOT NULL,   -- 'movie' | 'episode'
  bucket  TEXT NOT NULL,   -- 'finished' | 'partial' | 'bailed' | 'unknown'
  count   INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, scope, bucket)
);

CREATE TABLE agg_profile_abandoned (
  user_id      TEXT NOT NULL,
  range        TEXT NOT NULL,
  scope        TEXT NOT NULL,   -- 'movie' | 'series'
  item_id      TEXT NOT NULL,
  name         TEXT NOT NULL,
  series_name  TEXT NOT NULL DEFAULT '',
  bailed_count INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, scope, item_id)
);

CREATE TABLE agg_profile_rewatch (
  user_id     TEXT NOT NULL,
  scope       TEXT NOT NULL,   -- 'movie' | 'episode'
  item_id     TEXT NOT NULL,
  name        TEXT NOT NULL,
  series_name TEXT NOT NULL DEFAULT '',
  watch_days  INTEGER NOT NULL,
  PRIMARY KEY (user_id, scope, item_id)
);

CREATE TABLE agg_profile_binge (
  user_id      TEXT NOT NULL,
  series_id    TEXT NOT NULL,
  series_name  TEXT NOT NULL,
  run_episodes INTEGER NOT NULL,
  run_start    TEXT NOT NULL,
  run_end      TEXT NOT NULL,
  PRIMARY KEY (user_id, series_id, run_start)
);

CREATE TABLE agg_profile_taste (
  user_id   TEXT NOT NULL,
  range     TEXT NOT NULL,
  dim       TEXT NOT NULL,   -- 'genre' | 'decade' | 'length'
  key       TEXT NOT NULL,
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, dim, key)
);

CREATE TABLE agg_taste_baseline (
  dim       TEXT NOT NULL,
  key       TEXT NOT NULL,
  watch_sec INTEGER NOT NULL,
  PRIMARY KEY (dim, key)
);
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestMigrate_0003_ProfileTables -v`
Expected: PASS.

- [ ] **Step 5: Full store package + vet**

Run: `CGO_ENABLED=0 go test ./internal/store/... && go vet ./internal/store/...`
Expected: PASS (existing migration tests still green — 0003 is additive).

- [ ] **Step 6: Commit**

```bash
git add internal/store/migrations/0003_profiles.sql internal/store/store_test.go
git commit -m "feat(store): migration 0003 — playback_events spine + agg_profile_* tables"
```

---

## Task 2: Spine read/write helpers

**Files:**
- Create: `internal/store/spine.go`, `internal/store/spine_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent` (`internal/source/source.go`) — fields `At time.Time`, `UserID`, `ItemID`, `ItemName`, `ItemType`, `SeriesID`, `SeriesName`, `Method string`, `PlayDurationSec int64` (plus the three new fields from Task 3, which the spine does **not** persist).
- Produces:
  - `func (s *Store) AppendPlaybackEvents(ctx context.Context, evs []source.PlaybackEvent) error`
  - `func (s *Store) ReadPlaybackEvents(ctx context.Context) ([]source.PlaybackEvent, error)` — ordered by `at` ascending; `At` parsed back with `parseSpineTime`.
  - `func (s *Store) SpineCoverage(ctx context.Context) (first, last string, total int64, err error)` — `first`/`last` are `YYYY-MM-DD` (date part of `MIN(at)`/`MAX(at)`), `""` when the spine is empty.
  - `func spineDedupHash(ev source.PlaybackEvent) string` — `sha1` hex of `at|user_id|item_id|play_duration_sec` joined by `\x1f`, `at` formatted with `spineTimeLayout`.

- [ ] **Step 1: Write the failing tests**

Create `internal/store/spine_test.go`:

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ev(at time.Time, user, item, typ string, dur int64) source.PlaybackEvent {
	return source.PlaybackEvent{
		At: at, UserID: user, ItemID: item, ItemType: typ,
		Method: "DirectPlay", PlayDurationSec: dur,
		ItemName: item + "-name",
	}
}

func TestAppendPlaybackEvents_Dedup(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)

	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e, e}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("want 1 row after 3 appends of the same event, got %d", n)
	}
}

func TestAppendPlaybackEvents_RefreshesEnrichment(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	base := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "e9", "episode", 1200)
	base.ItemName, base.SeriesID, base.SeriesName = "old title", "", ""
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{base}); err != nil {
		t.Fatal(err)
	}
	upd := base
	upd.ItemName, upd.SeriesID, upd.SeriesName = "new title", "s1", "The Show"
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{upd}); err != nil {
		t.Fatal(err)
	}

	var name, sid, sname string
	var n int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&n)
	st.DB().QueryRowContext(ctx,
		`SELECT item_name, series_id, series_name FROM playback_events`,
	).Scan(&name, &sid, &sname)
	if n != 1 || name != "new title" || sid != "s1" || sname != "The Show" {
		t.Fatalf("n=%d name=%q sid=%q sname=%q", n, name, sid, sname)
	}
}

func TestReadPlaybackEvents_RoundTripOrdered(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	a := ev(time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC), "u1", "m2", "movie", 100)
	b := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{a, b}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ItemID != "m1" || got[1].ItemID != "m2" {
		t.Fatalf("order/content wrong: %+v", got)
	}
	if !got[0].At.Equal(b.At) || got[0].PlayDurationSec != 3600 || got[0].ItemType != "movie" {
		t.Fatalf("round-trip lost fields: %+v", got[0])
	}
}

func TestSpineCoverage(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	f, l, total, err := st.SpineCoverage(ctx)
	if err != nil || f != "" || l != "" || total != 0 {
		t.Fatalf("empty spine: f=%q l=%q total=%d err=%v", f, l, total, err)
	}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{
		ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 10),
		ev(time.Date(2025, 4, 9, 8, 0, 0, 0, time.UTC), "u1", "m2", "movie", 20),
	}); err != nil {
		t.Fatal(err)
	}
	f, l, total, err = st.SpineCoverage(ctx)
	if err != nil || f != "2025-01-06" || l != "2025-04-09" || total != 2 {
		t.Fatalf("f=%q l=%q total=%d err=%v", f, l, total, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run 'TestAppendPlaybackEvents|TestReadPlaybackEvents|TestSpineCoverage' -v`
Expected: FAIL — `st.AppendPlaybackEvents undefined`.

- [ ] **Step 3: Implement `spine.go`**

Create `internal/store/spine.go`:

```go
package store

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

// spineTimeLayout is how playback_events.at is stored: the plugin's wall-clock
// components, no zone. Kept parseable so ReadPlaybackEvents can hand aggregate
// a time.Time whose literal fields match what the plugin wrote.
const spineTimeLayout = "2006-01-02 15:04:05"

func formatSpineTime(t time.Time) string { return t.Format(spineTimeLayout) }

func parseSpineTime(s string) time.Time {
	t, err := time.Parse(spineTimeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// spineDedupHash identifies a plugin session by its immutable coordinates.
// Two genuinely distinct plays that share all four collapse to one row; that is
// accepted (see spec 4.3).
func spineDedupHash(ev source.PlaybackEvent) string {
	parts := strings.Join([]string{
		formatSpineTime(ev.At),
		ev.UserID,
		ev.ItemID,
		strconv.FormatInt(ev.PlayDurationSec, 10),
	}, "\x1f")
	sum := sha1.Sum([]byte(parts))
	return hex.EncodeToString(sum[:])
}

// AppendPlaybackEvents inserts events the spine has not seen and refreshes the
// enrichable columns (item_name / series_*) on the ones it has. Its own
// transaction; idempotent.
func (s *Store) AppendPlaybackEvents(ctx context.Context, evs []source.PlaybackEvent) error {
	if len(evs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	const q = `
		INSERT INTO playback_events
		  (at, user_id, item_id, item_type, method, play_duration_sec,
		   item_name, series_id, series_name, dedup_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dedup_hash) DO UPDATE SET
		  item_name   = excluded.item_name,
		  series_id   = excluded.series_id,
		  series_name = excluded.series_name`
	for _, e := range evs {
		if _, err := tx.ExecContext(ctx, q,
			formatSpineTime(e.At), e.UserID, e.ItemID, e.ItemType, e.Method, e.PlayDurationSec,
			e.ItemName, e.SeriesID, e.SeriesName, spineDedupHash(e),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadPlaybackEvents returns the whole spine, oldest first. The three
// library-fact fields (ItemRuntimeSec / ItemGenres / ItemYear) are NOT stored
// here — the caller re-enriches from the jellyfin.db copy.
func (s *Store) ReadPlaybackEvents(ctx context.Context) ([]source.PlaybackEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT at, user_id, item_id, item_type, method, play_duration_sec,
		       item_name, series_id, series_name
		FROM playback_events ORDER BY at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []source.PlaybackEvent
	for rows.Next() {
		var e source.PlaybackEvent
		var atRaw string
		if err := rows.Scan(&atRaw, &e.UserID, &e.ItemID, &e.ItemType, &e.Method,
			&e.PlayDurationSec, &e.ItemName, &e.SeriesID, &e.SeriesName); err != nil {
			return nil, err
		}
		e.At = parseSpineTime(atRaw)
		out = append(out, e)
	}
	return out, rows.Err()
}

// SpineCoverage is the min/max play date and total row count, for the API
// coverage block. Dates are YYYY-MM-DD; empty strings when the spine is empty.
func (s *Store) SpineCoverage(ctx context.Context) (first, last string, total int64, err error) {
	var f, l *string
	err = s.db.QueryRowContext(ctx,
		`SELECT substr(MIN(at),1,10), substr(MAX(at),1,10), COUNT(*) FROM playback_events`,
	).Scan(&f, &l, &total)
	if err != nil {
		return "", "", 0, err
	}
	if f != nil {
		first = *f
	}
	if l != nil {
		last = *l
	}
	return first, last, total, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run 'TestAppendPlaybackEvents|TestReadPlaybackEvents|TestSpineCoverage' -v`
Expected: PASS.

- [ ] **Step 5: Full package + vet + lint**

Run: `CGO_ENABLED=0 go test ./internal/store/... && go vet ./internal/store/... && golangci-lint run ./internal/store/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/spine.go internal/store/spine_test.go
git commit -m "feat(store): playback_events spine — append (idempotent), read, coverage"
```

---

## Task 3: Widen source enrichment with runtime / genres / year

**Files:**
- Modify: `internal/source/source.go` (add fields to `PlaybackEvent`)
- Modify: `internal/source/file/playback.go` (`enrichPlaybackEvents` query + scan)
- Modify: `testdata/library.fixture.sql` (only if the played items lack the columns — they already carry `RunTimeTicks` / `Genres` / `ProductionYear`, so likely no change; verify)
- Test: `internal/source/file/playback_test.go` (add a test; file may need creating — check for `queries_test.go` conventions in the same package)

**Interfaces:**
- Consumes: the existing `enrichPlaybackEvents(jdb *sql.DB, events []source.PlaybackEvent) error` in `internal/source/file/playback.go`, which already loops a `BaseItems` query for `Name` / `SeriesId` / `SeriesName`.
- Produces: `source.PlaybackEvent` gains `ItemRuntimeSec int64`, `ItemGenres []string`, `ItemYear int`. After `PlaybackEvents(ctx, _)` returns, every event whose `ItemID` resolves in `jellyfin.db` has these populated; unresolved (deleted) items keep the zero values.

- [ ] **Step 1: Write the failing test**

Create/append `internal/source/file/playback_test.go`:

```go
package file

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/testsupport"
)

func TestPlaybackEvents_EnrichesItemFacts(t *testing.T) {
	root := testsupport.TwoDBLayout(t)
	src := New(config.Config{JellyfinDataDir: root, WorkDir: t.TempDir(), DirectRead: true}, testLogger())

	events, err := src.PlaybackEvents(context.Background(), timeZero())
	if err != nil {
		t.Fatal(err)
	}

	// Bravo (item id ...000b) in the fixtures: RunTimeTicks 60000000000 = 6000s,
	// ProductionYear 2001, Genres "Comedy".
	var bravo *struct{ rt int64; yr int; genres []string }
	for i := range events {
		if events[i].ItemID == "0000000000000000000000000000000b" {
			bravo = &struct{ rt int64; yr int; genres []string }{
				events[i].ItemRuntimeSec, events[i].ItemYear, events[i].ItemGenres,
			}
			break
		}
	}
	if bravo == nil {
		t.Fatal("no Bravo event")
	}
	if bravo.rt != 6000 || bravo.yr != 2001 || len(bravo.genres) != 1 || bravo.genres[0] != "Comedy" {
		t.Fatalf("bravo facts: %+v", *bravo)
	}

	// The deleted item (deadbeef...) stays zero-valued, not an error.
	for i := range events {
		if events[i].ItemID == "deadbeefdeadbeefdeadbeefdeadbeef" {
			if events[i].ItemRuntimeSec != 0 || events[i].ItemYear != 0 || len(events[i].ItemGenres) != 0 {
				t.Fatalf("deleted item should be zero-valued: %+v", events[i])
			}
		}
	}
}
```

If `testLogger()` / `timeZero()` helpers don't already exist in the package's test files, inline them: `slog.New(slog.NewTextHandler(io.Discard, nil))` and `time.Time{}`.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/source/file/ -run TestPlaybackEvents_EnrichesItemFacts -v`
Expected: FAIL — `events[i].ItemRuntimeSec undefined`.

- [ ] **Step 3: Add the fields to `PlaybackEvent`**

In `internal/source/source.go`, extend the struct (keep the existing comment style):

```go
// PlaybackEvent is one row from the Playback Reporting plugin, enriched (where
// jellyfin.db resolves it) with current names, series linkage, and the item
// facts aggregate.Profiles needs (runtime / genres / year).
type PlaybackEvent struct {
	At               time.Time
	UserID, UserName string
	ItemID, ItemName string
	ItemType         string
	SeriesID         string
	SeriesName       string
	Method           string
	PlayDurationSec  int64

	ItemRuntimeSec int64    // 0 when unknown or the item is gone
	ItemGenres     []string // nil when unknown
	ItemYear       int      // 0 when unknown
}
```

- [ ] **Step 4: Widen the enrichment query**

In `internal/source/file/playback.go`, in `enrichPlaybackEvents`, extend the `itemInfo` struct and the `BaseItems` query. Genres in `jellyfin.db` are a single pipe-delimited string column (`i.Genres`), same as the library query uses; reuse `splitGenres` from `queries.go`:

```go
	type itemInfo struct {
		name, seriesID, seriesName, genres string
		runtimeSec                         int64
		year                               int
	}
	items := map[string]itemInfo{}
	irows, err := jdb.Query(`
		SELECT lower(replace(Id,'-','')), COALESCE(Name,''),
		       lower(replace(COALESCE(SeriesId,''),'-','')), COALESCE(SeriesName,''),
		       COALESCE(RunTimeTicks,0), COALESCE(ProductionYear,0), COALESCE(Genres,'')
		FROM BaseItems
		WHERE Type IN ('` + movieType + `', '` + episodeType + `')`)
	if err != nil {
		return err
	}
	for irows.Next() {
		var id string
		var info itemInfo
		if err := irows.Scan(&id, &info.name, &info.seriesID, &info.seriesName,
			&info.runtimeSec, &info.year, &info.genres); err != nil {
			irows.Close()
			return err
		}
		items[id] = info
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return err
	}
```

Note `info.runtimeSec` still holds *ticks* at this point. In the apply loop, convert and set the new fields:

```go
	for i := range events {
		if n, ok := users[events[i].UserID]; ok {
			events[i].UserName = n
		}
		if info, ok := items[events[i].ItemID]; ok {
			if info.name != "" {
				events[i].ItemName = info.name
			}
			events[i].SeriesID = info.seriesID
			events[i].SeriesName = info.seriesName
			events[i].ItemRuntimeSec = info.runtimeSec / 10_000_000 // ticks -> seconds
			events[i].ItemYear = info.year
			events[i].ItemGenres = splitGenres(info.genres)
		}
	}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/source/file/ -run TestPlaybackEvents_EnrichesItemFacts -v`
Expected: PASS. If the fixture's Bravo row lacks a `Genres`/`ProductionYear` value, add it to `testdata/library.fixture.sql` (`00000000-0000-0000-0000-00000000000B` already has `2001` and `'Comedy'` per the current fixture — no change expected).

- [ ] **Step 6: Whole source tree + vet + lint**

Run: `CGO_ENABLED=0 go test ./internal/source/... && go vet ./internal/source/... && golangci-lint run ./internal/source/...`
Expected: PASS. `internal/scheduler` still compiles (it builds `source.PlaybackEvent` literals in tests — new fields are optional, zero values fine).

- [ ] **Step 7: Commit**

```bash
git add internal/source/source.go internal/source/file/playback.go internal/source/file/playback_test.go testdata/
git commit -m "feat(source): enrich playback events with runtime / genres / year"
```

---

## Task 4: Feed Watch Stats from the spine

**Files:**
- Modify: `internal/scheduler/scheduler.go` (`RunWatchOnce`)
- Test: `internal/scheduler/scheduler_test.go` (add tests; the `fakeSource` there already implements `PlaybackEvents` / `SourceMTime`)

**Interfaces:**
- Consumes: `store.AppendPlaybackEvents`, `store.ReadPlaybackEvents` (Task 2); `aggregate.Watch([]source.PlaybackEvent) aggregate.WatchAggregates` (unchanged); `store.WriteWatchAggregates` (unchanged).
- Produces: after `RunWatchOnce`, `playback_events` holds the union of every event ever returned by the source, and `watch_events_daily` / `agg_watch_heatmap` are rebuilt from that union — not from the latest source call alone.

- [ ] **Step 1: Write the failing tests**

Add to `internal/scheduler/scheduler_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/scheduler/ -run 'TestRunWatchOnce_SpineAccumulates|TestRunWatchOnce_EmptySpine' -v`
Expected: FAIL — `playback_events` count is 0 / mtime-skip returns early.

- [ ] **Step 3: Rework `RunWatchOnce`**

In `internal/scheduler/scheduler.go`, replace the body of `RunWatchOnce` from the mtime-skip block through the aggregate write. Add a helper first:

```go
func (s *Scheduler) spineEmpty(ctx context.Context) bool {
	var n int
	err := s.st.DB().QueryRowContext(ctx, `SELECT count(*) FROM playback_events`).Scan(&n)
	return err == nil && n == 0
}
```

Then in `RunWatchOnce`, change the skip guard to also require a non-empty spine:

```go
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
```

And replace the aggregate section:

```go
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
	history, err := s.st.ReadPlaybackEvents(ctx)
	if err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}

	agg := aggregate.Watch(history)
	if err := s.st.WriteWatchAggregates(ctx, agg.Daily, agg.Heatmap); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
	s.log.Info("watch refresh ok", "events_seen", len(events), "history", len(history),
		"daily_rows", len(agg.Daily), "dur_ms", time.Since(start).Milliseconds())
	return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "watch", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: true, PluginAvailable: true,
	})
```

(`aggregate.Profiles` is wired in Task 6 — not yet.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/scheduler/ -run 'TestRunWatchOnce' -v`
Expected: PASS — including the pre-existing `TestRunWatchOnce_PopulatesWatchTables` and `TestRunWatchOnce_PluginAbsent`.

- [ ] **Step 5: Whole repo test + vet + lint**

Run: `CGO_ENABLED=0 go test ./... && go vet ./... && golangci-lint run`
Expected: PASS. In particular the `internal/api` watch tests and any smoke test still pass — `watch_events_daily` shape is unchanged.

- [ ] **Step 6: Commit**

```bash
git add internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "feat(scheduler): watch job appends to the spine and aggregates the full history"
```

---

## Task 5: `aggregate.Profiles` — the pure aggregation

**Files:**
- Modify: `internal/aggregate/rows.go` (row structs + `ProfileAggregates`)
- Create: `internal/aggregate/profile.go`, `internal/aggregate/profile_test.go`

**Interfaces:**
- Consumes: `[]source.PlaybackEvent` with `At`, `UserID`, `ItemID`, `ItemName`, `ItemType` (`"movie"`/`"episode"`), `SeriesID`, `SeriesName`, `PlayDurationSec`, `ItemRuntimeSec`, `ItemGenres`, `ItemYear`.
- Produces:

```go
// in rows.go
type ProfileRanges = string // "30d" | "90d" | "1y" | "all"

type ProfileSummaryRow struct {
	UserID, Range              string
	WatchSec, Plays            int64
	DistinctTitles, DaysActive int64
	FinishedPct, BailedPct     float64
	RewatchPct                 float64 // only set on Range=="all"
	LongestBingeEpisodes       int64   // lifetime
	LongestBingeSeriesName     string  // lifetime
	ShowOfRangeSeriesID        string
	ShowOfRangeSeriesName      string
	FirstPlay, LastPlay        string  // YYYY-MM-DD
}

type ProfileCompletionRow struct {
	UserID, Range, Scope, Bucket string // Scope: movie|episode ; Bucket: finished|partial|bailed|unknown
	Count                        int64
}

type ProfileAbandonedRow struct {
	UserID, Range, Scope, ItemID, Name, SeriesName string // Scope: movie|series
	BailedCount                                    int64
}

type ProfileRewatchRow struct {
	UserID, Scope, ItemID, Name, SeriesName string
	WatchDays                               int64
}

type ProfileBingeRow struct {
	UserID, SeriesID, SeriesName string
	RunEpisodes                  int64
	RunStart, RunEnd             string // YYYY-MM-DD
}

type ProfileTasteRow struct {
	UserID, Range, Dim, Key string // Dim: genre|decade|length
	WatchSec, Plays         int64
}

type TasteBaselineRow struct {
	Dim, Key string
	WatchSec int64
}

type ProfileAggregates struct {
	Summary    []ProfileSummaryRow
	Completion []ProfileCompletionRow
	Abandoned  []ProfileAbandonedRow
	Rewatch    []ProfileRewatchRow
	Binge      []ProfileBingeRow
	Taste      []ProfileTasteRow
	Baseline   []TasteBaselineRow
}
```

- `func Profiles(events []source.PlaybackEvent, now time.Time) ProfileAggregates`

**Design notes for the implementer (from spec §6):**
- Roll raw events to `(user, item, day)` first, summing `PlayDurationSec`. `day` = `ev.At.Format("2006-01-02")` (no zone shift).
- Completion verdict per rolled row: `ratio = watched / ItemRuntimeSec`; `finished` ≥ `completionFinished` (0.90), `bailed` < `completionBailed` (0.25), `partial` between, `unknown` when `ItemRuntimeSec <= 0`.
- Ranges: for each of `30d|90d|1y|all`, cutoff = `now.AddDate(0,0,-30)` / `-90` / `AddDate(-1,0,0)` / none; compare `day >= cutoffStr` lexically (same as `store.dayCutoff`).
- Rewatch (lifetime): per `(user, scope, item)`, `watch_days` = count of distinct days with a non-`bailed` verdict; days beyond the first are rewatches. `RewatchPct` = `Σ max(watch_days-1,0)` / `Σ watch_days` over the user's titles.
- Binge (lifetime): per `(user, seriesID)`, order that user's *episode* events by `At`; new run when the gap to the previous episode event exceeds `bingeGapHours` (4). Run length = count of distinct `ItemID` in the run. Emit runs with `RunEpisodes >= 2`; keep the top `profileTopN` per user by `RunEpisodes` then recency. `LongestBinge*` on the summary is the single max per user.
- Taste: per range, per rolled row, add `watched` to `(genre, g)` for each `g in ItemGenres`, to `(decade, floor(year/10)*10)` (`"unknown"` when `ItemYear<=0`), and to `(length, band)` where band is by `ItemRuntimeSec` — movies: `<90m` (`<5400`), `90-120m` (`<=7200`), `>120m`; episodes: `<30m` (`<1800`), `30-60m` (`<=3600`), `>60m`. `unknown` length when `ItemRuntimeSec<=0`.
- Baseline: same accumulation over **all** users, range `all`, into `TasteBaselineRow`.
- `profileTopN = 10` for abandoned / rewatch / binge lists.
- Deterministic output ordering (sort every slice) so golden tests are stable.

- [ ] **Step 1: Write the failing tests**

Create `internal/aggregate/profile_test.go`:

```go
package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func pe(day string, user, item, typ string, dur, runtime int64) source.PlaybackEvent {
	t, _ := time.Parse("2006-01-02 15:04", day)
	return source.PlaybackEvent{
		At: t, UserID: user, ItemID: item, ItemName: item, ItemType: typ,
		PlayDurationSec: dur, ItemRuntimeSec: runtime, Method: "DirectPlay",
	}
}

func findCompletion(rows []ProfileCompletionRow, user, rng, scope, bucket string) int64 {
	for _, r := range rows {
		if r.UserID == user && r.Range == rng && r.Scope == scope && r.Bucket == bucket {
			return r.Count
		}
	}
	return -1
}

func TestProfiles_CompletionBuckets(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		pe("2025-05-20 20:00", "u1", "mFin", "movie", 5760, 6000), // ratio .96 -> finished
		pe("2025-05-21 20:00", "u1", "mPar", "movie", 3000, 6000), // ratio .5  -> partial
		pe("2025-05-22 20:00", "u1", "mBal", "movie", 300, 6000),  // ratio .05 -> bailed
		pe("2025-05-23 20:00", "u1", "mUnk", "movie", 1200, 0),    // no runtime -> unknown
	}
	agg := Profiles(events, now)

	for bucket, want := range map[string]int64{"finished": 1, "partial": 1, "bailed": 1, "unknown": 1} {
		if got := findCompletion(agg.Completion, "u1", "30d", "movie", bucket); got != want {
			t.Fatalf("30d movie %s = %d, want %d", bucket, got, want)
		}
	}
	// two sessions same title same day sum before the verdict
	events2 := []source.PlaybackEvent{
		pe("2025-05-20 20:00", "u2", "m1", "movie", 3000, 6000),
		pe("2025-05-20 21:30", "u2", "m1", "movie", 3000, 6000), // together .of 1.0 -> finished
	}
	agg2 := Profiles(events2, now)
	if got := findCompletion(agg2.Completion, "u2", "30d", "movie", "finished"); got != 1 {
		t.Fatalf("summed same-day sessions should be one finished verdict, got %d", got)
	}
}

func TestProfiles_RewatchLifetime(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		pe("2024-01-01 20:00", "u1", "m1", "movie", 6000, 6000),
		pe("2024-06-01 20:00", "u1", "m1", "movie", 6000, 6000), // rewatch #1
		pe("2025-05-01 20:00", "u1", "m1", "movie", 6000, 6000), // rewatch #2
		pe("2025-05-02 20:00", "u1", "m2", "movie", 6000, 6000), // first-watch only
	}
	agg := Profiles(events, now)

	var rw *ProfileRewatchRow
	for i := range agg.Rewatch {
		if agg.Rewatch[i].ItemID == "m1" {
			rw = &agg.Rewatch[i]
		}
	}
	if rw == nil || rw.WatchDays != 3 {
		t.Fatalf("m1 rewatch row: %+v", rw)
	}
	var sumAll *ProfileSummaryRow
	for i := range agg.Summary {
		if agg.Summary[i].UserID == "u1" && agg.Summary[i].Range == "all" {
			sumAll = &agg.Summary[i]
		}
	}
	// 2 rewatch days out of 4 total watch days = 0.5
	if sumAll == nil || sumAll.RewatchPct < 0.49 || sumAll.RewatchPct > 0.51 {
		t.Fatalf("rewatch_pct: %+v", sumAll)
	}
}

func TestProfiles_BingeRuns(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	ep := func(ts, item string) source.PlaybackEvent {
		e := pe(ts, "u1", item, "episode", 1400, 1500)
		e.SeriesID, e.SeriesName = "s1", "The Show"
		return e
	}
	events := []source.PlaybackEvent{
		ep("2025-05-01 20:00", "e1"),
		ep("2025-05-01 20:30", "e2"),
		ep("2025-05-01 21:00", "e3"), // run of 3
		ep("2025-05-02 09:00", "e4"), // >4h gap -> new run of 1 (dropped, <2)
	}
	agg := Profiles(events, now)

	if len(agg.Binge) != 1 || agg.Binge[0].RunEpisodes != 3 || agg.Binge[0].SeriesName != "The Show" {
		t.Fatalf("binge rows: %+v", agg.Binge)
	}
	for _, s := range agg.Summary {
		if s.UserID == "u1" && s.Range == "all" && s.LongestBingeEpisodes != 3 {
			t.Fatalf("longest binge on summary: %+v", s)
		}
	}
}

func TestProfiles_TasteWeightedByWatchSec(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	a := pe("2025-05-01 20:00", "u1", "mLong", "movie", 6000, 6600)
	a.ItemGenres, a.ItemYear = []string{"Drama"}, 1994
	b := pe("2025-05-02 20:00", "u1", "mShort", "movie", 60, 6000) // barely watched
	b.ItemGenres, b.ItemYear = []string{"Comedy"}, 2015
	agg := Profiles([]source.PlaybackEvent{a, b}, now)

	var drama, comedy int64
	for _, r := range agg.Taste {
		if r.UserID == "u1" && r.Range == "all" && r.Dim == "genre" {
			switch r.Key {
			case "Drama":
				drama = r.WatchSec
			case "Comedy":
				comedy = r.WatchSec
			}
		}
	}
	if drama != 6000 || comedy != 60 {
		t.Fatalf("taste weighted by seconds: drama=%d comedy=%d", drama, comedy)
	}
	// decade + length dims present
	var haveDecade, haveLength bool
	for _, r := range agg.Taste {
		if r.Dim == "decade" && r.Key == "1990" {
			haveDecade = true
		}
		if r.Dim == "length" {
			haveLength = true
		}
	}
	if !haveDecade || !haveLength {
		t.Fatalf("missing decade/length dims: %+v", agg.Taste)
	}
	if len(agg.Baseline) == 0 {
		t.Fatal("baseline not populated")
	}
}

func TestProfiles_RangeFiltering(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		pe("2025-05-25 20:00", "u1", "recent", "movie", 6000, 6000), // in 30d
		pe("2025-01-10 20:00", "u1", "older", "movie", 6000, 6000),  // in 1y, not 30d
	}
	agg := Profiles(events, now)

	if got := findCompletion(agg.Completion, "u1", "30d", "movie", "finished"); got != 1 {
		t.Fatalf("30d finished = %d, want 1", got)
	}
	if got := findCompletion(agg.Completion, "u1", "1y", "movie", "finished"); got != 2 {
		t.Fatalf("1y finished = %d, want 2", got)
	}
	if got := findCompletion(agg.Completion, "u1", "all", "movie", "finished"); got != 2 {
		t.Fatalf("all finished = %d, want 2", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -run TestProfiles -v`
Expected: FAIL — `undefined: Profiles`.

- [ ] **Step 3: Add the row types to `rows.go`**

Append the structs from the **Interfaces** block above to `internal/aggregate/rows.go`, keeping the file's existing comment tone.

- [ ] **Step 4: Implement `profile.go`**

Create `internal/aggregate/profile.go`. Implement per the design notes above. Skeleton to fill in (all helpers must be real code, no TODOs):

```go
package aggregate

import (
	"sort"
	"strconv"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

const (
	completionFinished = 0.90
	completionBailed   = 0.25
	bingeGapHours      = 4
	profileTopN        = 10
)

var profileRangeList = []string{"30d", "90d", "1y", "all"}

func rangeCutoff(rng string, now time.Time) string {
	switch rng {
	case "30d":
		return now.AddDate(0, 0, -30).Format("2006-01-02")
	case "90d":
		return now.AddDate(0, 0, -90).Format("2006-01-02")
	case "1y":
		return now.AddDate(-1, 0, 0).Format("2006-01-02")
	default:
		return ""
	}
}

// dailyRow is one (user, item, day) rollup with its completion verdict.
type dailyRow struct {
	user, item, day, scope    string
	name, seriesID, seriesName string
	genres                    []string
	year                      int
	runtimeSec, watchedSec    int64
}

func (d dailyRow) verdict() string {
	if d.runtimeSec <= 0 {
		return "unknown"
	}
	r := float64(d.watchedSec) / float64(d.runtimeSec)
	switch {
	case r >= completionFinished:
		return "finished"
	case r < completionBailed:
		return "bailed"
	default:
		return "partial"
	}
}

func rollDaily(events []source.PlaybackEvent) []dailyRow {
	type key struct{ user, item, day string }
	m := map[key]*dailyRow{}
	var order []key
	for _, e := range events {
		if e.At.IsZero() || e.UserID == "" || e.ItemID == "" {
			continue
		}
		k := key{e.UserID, e.ItemID, e.At.Format("2006-01-02")}
		r := m[k]
		if r == nil {
			scope := "movie"
			if e.ItemType == "episode" {
				scope = "episode"
			}
			r = &dailyRow{
				user: k.user, item: k.item, day: k.day, scope: scope,
				name: e.ItemName, seriesID: e.SeriesID, seriesName: e.SeriesName,
				genres: e.ItemGenres, year: e.ItemYear, runtimeSec: e.ItemRuntimeSec,
			}
			m[k] = r
			order = append(order, k)
		}
		r.watchedSec += e.PlayDurationSec
		if r.runtimeSec == 0 && e.ItemRuntimeSec > 0 {
			r.runtimeSec = e.ItemRuntimeSec
		}
	}
	out := make([]dailyRow, 0, len(order))
	for _, k := range order {
		out = append(out, *m[k])
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].user != out[j].user {
			return out[i].user < out[j].user
		}
		if out[i].day != out[j].day {
			return out[i].day < out[j].day
		}
		return out[i].item < out[j].item
	})
	return out
}

func lengthBand(scope string, runtimeSec int64) string {
	if runtimeSec <= 0 {
		return "unknown"
	}
	if scope == "episode" {
		switch {
		case runtimeSec < 1800:
			return "<30m"
		case runtimeSec <= 3600:
			return "30-60m"
		default:
			return ">60m"
		}
	}
	switch {
	case runtimeSec < 5400:
		return "<90m"
	case runtimeSec <= 7200:
		return "90-120m"
	default:
		return ">120m"
	}
}

func decadeKey(year int) string {
	if year <= 0 {
		return "unknown"
	}
	return strconv.Itoa((year / 10) * 10)
}

// Profiles rolls the full playback history into every agg_profile_* row, for all
// four ranges. Pure: no I/O, no SQL.
func Profiles(events []source.PlaybackEvent, now time.Time) ProfileAggregates {
	daily := rollDaily(events)
	var out ProfileAggregates

	users := distinctUsers(daily)
	for _, u := range users {
		urows := filterUser(daily, u)
		for _, rng := range profileRangeList {
			cut := rangeCutoff(rng, now)
			scoped := filterSince(urows, cut)
			out.Completion = append(out.Completion, completionRows(u, rng, scoped)...)
			out.Abandoned = append(out.Abandoned, abandonedRows(u, rng, scoped)...)
			out.Taste = append(out.Taste, tasteRows(u, rng, scoped)...)
			out.Summary = append(out.Summary, summaryRow(u, rng, scoped))
		}
		// lifetime panels
		out.Rewatch = append(out.Rewatch, rewatchRows(u, urows)...)
		runs := bingeRuns(u, events) // needs sub-day timestamps -> raw events, filtered to u
		out.Binge = append(out.Binge, topBinge(runs)...)
		fillLifetimeSummary(&out, u, urows, runs)
	}
	out.Baseline = baselineRows(daily)

	sortProfileAggregates(&out)
	return out
}
```

Implement the remaining helpers (`distinctUsers`, `filterUser`, `filterSince`, `completionRows`, `abandonedRows`, `tasteRows`, `summaryRow`, `rewatchRows`, `bingeRuns`, `topBinge`, `fillLifetimeSummary`, `baselineRows`, `sortProfileAggregates`) as small pure functions in the same file. Key points the tests pin down:
- `completionRows` emits one row per `(scope, bucket)` with a count; buckets with zero count may be omitted.
- `summaryRow.FinishedPct` = finished verdicts / total verdicts in scope+range (all scopes combined); `BailedPct` likewise. `DistinctTitles` = distinct `item` for movies + distinct `seriesID` (non-empty) for episodes. `DaysActive` = distinct `day`. `ShowOfRange*` = series with max `watchedSec` in the scoped rows.
- `rewatchRows` / `RewatchPct`: over lifetime rows (`cut==""`), `watch_days` per `(scope,item)` counting distinct days where `verdict != "bailed"`. `RewatchPct` written only on the `all` summary row (set it in `fillLifetimeSummary`).
- `bingeRuns` groups a user's `episode` events by `SeriesID`, sorts by `At`, splits when `cur.At.Sub(prev.At) > bingeGapHours*time.Hour`, counts distinct `ItemID`. `RunStart`/`RunEnd` are the run's first/last `At` as `YYYY-MM-DD`.
- `topBinge` keeps runs with `RunEpisodes >= 2`, sorts by `RunEpisodes` desc then `RunEnd` desc, caps at `profileTopN`.
- `fillLifetimeSummary` sets `LongestBingeEpisodes` / `LongestBingeSeriesName` from the single longest run (0 / "" if none), and back-patches `RewatchPct` onto the already-appended `all` summary row for that user.
- `baselineRows` accumulates `watchedSec` by `(dim,key)` across **all** `daily` rows.
- `sortProfileAggregates` sorts every slice by its natural key for stable golden output.

- [ ] **Step 5: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -run TestProfiles -v`
Expected: PASS (all five).

- [ ] **Step 6: Full aggregate package + vet + lint**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/... && go vet ./internal/aggregate/... && golangci-lint run ./internal/aggregate/...`
Expected: PASS. Existing `TestWatch_*` unaffected.

- [ ] **Step 7: Commit**

```bash
git add internal/aggregate/rows.go internal/aggregate/profile.go internal/aggregate/profile_test.go
git commit -m "feat(aggregate): Profiles — completion, rewatch, binge, taste across 4 ranges"
```

---

## Task 6: Persist profile aggregates + wire into the watch job

**Files:**
- Create: `internal/store/writes_profile.go`, `internal/store/writes_profile_test.go`
- Modify: `internal/scheduler/scheduler.go` (`RunWatchOnce` — add the profile write)
- Modify: `internal/scheduler/scheduler_test.go` (assert `agg_profile_*` populated / retained)

**Interfaces:**
- Consumes: `aggregate.ProfileAggregates` (Task 5).
- Produces: `func (s *Store) WriteProfileAggregates(ctx context.Context, a aggregate.ProfileAggregates) error` — one transaction: `DELETE FROM` each of the seven tables, then re-`INSERT` every row.

- [ ] **Step 1: Write the failing tests**

Create `internal/store/writes_profile_test.go`:

```go
package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func TestWriteProfileAggregates_RewriteSemantics(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)

	first := aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 10}},
		Completion: []aggregate.ProfileCompletionRow{
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "finished", Count: 3},
		},
		Baseline: []aggregate.TasteBaselineRow{{Dim: "genre", Key: "Drama", WatchSec: 999}},
	}
	if err := st.WriteProfileAggregates(ctx, first); err != nil {
		t.Fatal(err)
	}

	second := aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 20}},
	}
	if err := st.WriteProfileAggregates(ctx, second); err != nil {
		t.Fatal(err)
	}

	var plays, comp, base int64
	st.DB().QueryRowContext(ctx, `SELECT plays FROM agg_profile_summary WHERE user_id='u1' AND range='all'`).Scan(&plays)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_profile_completion`).Scan(&comp)
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_taste_baseline`).Scan(&base)
	if plays != 20 || comp != 0 || base != 0 {
		t.Fatalf("rewrite not clean: plays=%d comp=%d base=%d", plays, comp, base)
	}
}
```

Add to `internal/scheduler/scheduler_test.go`:

```go
func TestRunWatchOnce_PopulatesProfileTables(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	fs := &fakeSource{events: []source.PlaybackEvent{
		{At: time.Date(2025, 1, 6, 20, 0, 0, 0, time.UTC), UserID: "u1", ItemID: "m1", ItemType: "movie", Method: "DirectPlay", PlayDurationSec: 6000, ItemRuntimeSec: 6000},
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestWriteProfileAggregates -v && CGO_ENABLED=0 go test ./internal/scheduler/ -run TestRunWatchOnce_PopulatesProfileTables -v`
Expected: FAIL — `WriteProfileAggregates undefined`.

- [ ] **Step 3: Implement `writes_profile.go`**

Create `internal/store/writes_profile.go`:

```go
package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WriteProfileAggregates replaces every profile-derived row in one transaction,
// same pattern as WriteWatchAggregates.
func (s *Store) WriteProfileAggregates(ctx context.Context, a aggregate.ProfileAggregates) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_profile_summary`,
		`DELETE FROM agg_profile_completion`,
		`DELETE FROM agg_profile_abandoned`,
		`DELETE FROM agg_profile_rewatch`,
		`DELETE FROM agg_profile_binge`,
		`DELETE FROM agg_profile_taste`,
		`DELETE FROM agg_taste_baseline`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for _, r := range a.Summary {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_summary
			  (user_id, range, watch_sec, plays, distinct_titles, days_active,
			   finished_pct, bailed_pct, rewatch_pct, longest_binge_episodes,
			   longest_binge_series_name, show_of_range_series_id,
			   show_of_range_series_name, first_play, last_play)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.UserID, r.Range, r.WatchSec, r.Plays, r.DistinctTitles, r.DaysActive,
			r.FinishedPct, r.BailedPct, r.RewatchPct, r.LongestBingeEpisodes,
			r.LongestBingeSeriesName, r.ShowOfRangeSeriesID, r.ShowOfRangeSeriesName,
			r.FirstPlay, r.LastPlay,
		); err != nil {
			return err
		}
	}
	for _, r := range a.Completion {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_completion (user_id, range, scope, bucket, count)
			VALUES (?,?,?,?,?)`,
			r.UserID, r.Range, r.Scope, r.Bucket, r.Count,
		); err != nil {
			return err
		}
	}
	for _, r := range a.Abandoned {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_abandoned
			  (user_id, range, scope, item_id, name, series_name, bailed_count)
			VALUES (?,?,?,?,?,?,?)`,
			r.UserID, r.Range, r.Scope, r.ItemID, r.Name, r.SeriesName, r.BailedCount,
		); err != nil {
			return err
		}
	}
	for _, r := range a.Rewatch {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_rewatch
			  (user_id, scope, item_id, name, series_name, watch_days)
			VALUES (?,?,?,?,?,?)`,
			r.UserID, r.Scope, r.ItemID, r.Name, r.SeriesName, r.WatchDays,
		); err != nil {
			return err
		}
	}
	for _, r := range a.Binge {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_binge
			  (user_id, series_id, series_name, run_episodes, run_start, run_end)
			VALUES (?,?,?,?,?,?)`,
			r.UserID, r.SeriesID, r.SeriesName, r.RunEpisodes, r.RunStart, r.RunEnd,
		); err != nil {
			return err
		}
	}
	for _, r := range a.Taste {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_taste (user_id, range, dim, key, watch_sec, plays)
			VALUES (?,?,?,?,?,?)`,
			r.UserID, r.Range, r.Dim, r.Key, r.WatchSec, r.Plays,
		); err != nil {
			return err
		}
	}
	for _, r := range a.Baseline {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_taste_baseline (dim, key, watch_sec) VALUES (?,?,?)`,
			r.Dim, r.Key, r.WatchSec,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 4: Wire into `RunWatchOnce`**

In `internal/scheduler/scheduler.go`, right after the successful `WriteWatchAggregates` call, before `SetRefreshMeta`:

```go
	if err := s.st.WriteProfileAggregates(ctx, aggregate.Profiles(history, s.now())); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
```

If `Scheduler` has no `now()` helper, use `time.Now()` directly (match whatever `RunLibraryOnce` does — grep for `time.Now()` in the file; the codebase uses it directly in this file).

- [ ] **Step 5: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestWriteProfileAggregates -v && CGO_ENABLED=0 go test ./internal/scheduler/ -run TestRunWatchOnce -v`
Expected: PASS.

- [ ] **Step 6: Whole repo + vet + lint**

Run: `CGO_ENABLED=0 go test ./... && go vet ./... && golangci-lint run`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store/writes_profile.go internal/store/writes_profile_test.go internal/scheduler/
git commit -m "feat: persist agg_profile_* and run Profiles in the watch job"
```

---

## Task 7: Store reads for the two endpoints

**Files:**
- Create: `internal/store/reads_profile.go`, `internal/store/reads_profile_test.go`

**Interfaces:**
- Consumes: the `agg_profile_*` / `agg_taste_baseline` tables, `dim_user`, `refresh_meta` (`GetRefreshMeta`), `SpineCoverage` (Task 2), `orEmpty` (`internal/store/reads.go`).
- Produces:

```go
type ProfileListEntry struct {
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	TotalWatchSec        int64   `json:"total_watch_sec"`
	TotalPlays           int64   `json:"total_plays"`
	FinishedPct          float64 `json:"finished_pct"`
	RewatchPct           float64 `json:"rewatch_pct"`
	LongestBingeEpisodes int64   `json:"longest_binge_episodes"`
	LastPlay             string  `json:"last_play"`
}
type ProfileList struct {
	PluginAvailable bool               `json:"plugin_available"`
	Coverage        WatchCoverage      `json:"coverage"` // reuse the type from reads_watch.go
	Users           []ProfileListEntry `json:"users"`
}

type ProfileUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type ProfileSummary struct {
	WatchSec       int64   `json:"watch_sec"`
	Plays          int64   `json:"plays"`
	DistinctTitles int64   `json:"distinct_titles"`
	DaysActive     int64   `json:"days_active"`
	FinishedPct    float64 `json:"finished_pct"`
	BailedPct      float64 `json:"bailed_pct"`
	RewatchPct     float64 `json:"rewatch_pct"`
	LongestBinge   struct {
		Episodes   int64  `json:"episodes"`
		SeriesName string `json:"series_name"`
	} `json:"longest_binge"`
	ShowOfRange struct {
		SeriesID   string `json:"series_id"`
		SeriesName string `json:"series_name"`
	} `json:"show_of_range"`
	FirstPlay string `json:"first_play"`
	LastPlay  string `json:"last_play"`
}
type ProfileCompletionSlice struct {
	Scope  string `json:"scope"`
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
}
type ProfileAbandoned struct {
	Scope      string `json:"scope"`
	ItemID     string `json:"item_id"`
	Name       string `json:"name"`
	SeriesName string `json:"series_name"`
	BailedCount int64 `json:"bailed_count"`
}
type ProfileRewatch struct {
	Scope      string `json:"scope"`
	ItemID     string `json:"item_id"`
	Name       string `json:"name"`
	SeriesName string `json:"series_name"`
	WatchDays  int64  `json:"watch_days"`
}
type ProfileBinge struct {
	SeriesID    string `json:"series_id"`
	SeriesName  string `json:"series_name"`
	RunEpisodes int64  `json:"run_episodes"`
	RunStart    string `json:"run_start"`
	RunEnd      string `json:"run_end"`
}
type TasteEntry struct {
	Key      string `json:"key"`
	WatchSec int64  `json:"watch_sec"`
	Plays    int64  `json:"plays"`
}
type BaselineEntry struct {
	Key      string `json:"key"`
	WatchSec int64  `json:"watch_sec"`
}
type ProfileTaste struct {
	Genre           []TasteEntry `json:"genre"`
	Decade          []TasteEntry `json:"decade"`
	Length          []TasteEntry `json:"length"`
	SignatureGenres []string     `json:"signature_genres"`
}
type ProfileBaseline struct {
	Genre  []BaselineEntry `json:"genre"`
	Decade []BaselineEntry `json:"decade"`
	Length []BaselineEntry `json:"length"`
}
type Profile struct {
	Range      string                   `json:"range"`
	User       ProfileUser              `json:"user"`
	Summary    ProfileSummary           `json:"summary"`
	Completion []ProfileCompletionSlice `json:"completion"`
	Abandoned  []ProfileAbandoned       `json:"abandoned"`
	Rewatch    []ProfileRewatch         `json:"rewatch"`
	Binge      []ProfileBinge           `json:"binge"`
	Taste      ProfileTaste             `json:"taste"`
	Baseline   ProfileBaseline          `json:"baseline"`
}
```

- `func (s *Store) ReadProfileList(ctx context.Context) (ProfileList, error)`
- `func (s *Store) ReadProfile(ctx context.Context, userID, rng string) (Profile, bool, error)` — the `bool` is `false` when `userID` is not in `dim_user` (→ handler 404).

**Behaviour:**
- `ReadProfileList`: `PluginAvailable` from `GetRefreshMeta(ctx,"watch")` (`m.OK && m.PluginAvailable`); `Coverage` from `SpineCoverage`; `Users` = `dim_user` LEFT JOIN `agg_profile_summary` on `range='all'`, ordered by `name`. Missing summary → zeros. Every slice through `orEmpty`.
- `ReadProfile`: verify `userID` in `dim_user` (→ name, `ok`). `Summary` from `agg_profile_summary` where `range=?` (zero value if absent). `Completion` from `agg_profile_completion` where `user_id=? AND range=?`. `Abandoned`, `Taste` likewise on `range`. `Rewatch`, `Binge` ignore range (lifetime). `RewatchPct` / `LongestBinge` on the summary come from the stored row's columns — but note those are only populated on the `range='all'` row; when `rng != "all"`, do a second small read of the `all` row for `rewatch_pct` / `longest_binge_*` so every response carries them. `Baseline` from `agg_taste_baseline` split by `dim`. `SignatureGenres`: compute in Go — for each genre in `Taste.Genre`, `userShare = watch_sec / Σ user genre watch_sec`, `baseShare = baseline watch_sec / Σ baseline genre watch_sec`; take up to 3 genres with the largest positive `userShare - baseShare`, `userShare` tie-break. Empty when the user has no genre data.

- [ ] **Step 1: Write the failing tests**

Create `internal/store/reads_profile_test.go`:

```go
package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func seedProfile(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dim_user (id, name) VALUES ('u1','alice'), ('u2','bob')`); err != nil {
		t.Fatal(err)
	}
	agg := aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{
			{UserID: "u1", Range: "all", WatchSec: 9000, Plays: 12, DistinctTitles: 5, DaysActive: 7,
				FinishedPct: 0.6, BailedPct: 0.1, RewatchPct: 0.25,
				LongestBingeEpisodes: 6, LongestBingeSeriesName: "The Show",
				FirstPlay: "2025-01-01", LastPlay: "2025-05-01"},
			{UserID: "u1", Range: "30d", WatchSec: 3000, Plays: 4, DistinctTitles: 3, DaysActive: 3,
				FinishedPct: 0.5, BailedPct: 0.25, FirstPlay: "2025-04-10", LastPlay: "2025-05-01"},
		},
		Completion: []aggregate.ProfileCompletionRow{
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "finished", Count: 2},
			{UserID: "u1", Range: "30d", Scope: "movie", Bucket: "bailed", Count: 1},
		},
		Rewatch: []aggregate.ProfileRewatchRow{
			{UserID: "u1", Scope: "movie", ItemID: "m1", Name: "Alpha", WatchDays: 3},
		},
		Binge: []aggregate.ProfileBingeRow{
			{UserID: "u1", SeriesID: "s1", SeriesName: "The Show", RunEpisodes: 6, RunStart: "2025-02-01", RunEnd: "2025-02-01"},
		},
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "30d", Dim: "genre", Key: "Drama", WatchSec: 2400, Plays: 2},
			{UserID: "u1", Range: "30d", Dim: "genre", Key: "Comedy", WatchSec: 600, Plays: 1},
			{UserID: "u1", Range: "30d", Dim: "decade", Key: "1990", WatchSec: 3000, Plays: 3},
			{UserID: "u1", Range: "30d", Dim: "length", Key: "90-120m", WatchSec: 3000, Plays: 3},
		},
		Baseline: []aggregate.TasteBaselineRow{
			{Dim: "genre", Key: "Drama", WatchSec: 1000},
			{Dim: "genre", Key: "Comedy", WatchSec: 4000},
		},
	}
	if err := st.WriteProfileAggregates(ctx, agg); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, RefreshMeta{Job: "watch", OK: true, PluginAvailable: true}); err != nil {
		t.Fatal(err)
	}
}

func TestReadProfileList(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)

	pl, err := st.ReadProfileList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pl.PluginAvailable || len(pl.Users) != 2 {
		t.Fatalf("list: %+v", pl)
	}
	// alice first (ordered by name), with her 'all' headline numbers
	if pl.Users[0].Name != "alice" || pl.Users[0].TotalPlays != 12 || pl.Users[0].RewatchPct != 0.25 {
		t.Fatalf("alice entry: %+v", pl.Users[0])
	}
	// bob has no summary -> zeros, not missing
	if pl.Users[1].Name != "bob" || pl.Users[1].TotalPlays != 0 {
		t.Fatalf("bob entry: %+v", pl.Users[1])
	}
}

func TestReadProfile_RangeScopedPlusLifetime(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)

	p, ok, err := st.ReadProfile(ctx, "u1", "30d")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if p.User.Name != "alice" || p.Summary.Plays != 4 {
		t.Fatalf("range-scoped summary wrong: %+v", p.Summary)
	}
	// lifetime fields still present on a 30d response
	if p.Summary.RewatchPct != 0.25 || p.Summary.LongestBinge.Episodes != 6 {
		t.Fatalf("lifetime fields missing on 30d: %+v", p.Summary)
	}
	if len(p.Completion) != 2 || len(p.Rewatch) != 1 || len(p.Binge) != 1 {
		t.Fatalf("slices: comp=%d rw=%d binge=%d", len(p.Completion), len(p.Rewatch), len(p.Binge))
	}
	// signature genres: Drama is 80% of user watch vs 20% of baseline -> signature
	if len(p.Taste.SignatureGenres) == 0 || p.Taste.SignatureGenres[0] != "Drama" {
		t.Fatalf("signature genres: %+v", p.Taste.SignatureGenres)
	}
}

func TestReadProfile_UnknownUser(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	seedProfile(t, st)
	_, ok, err := st.ReadProfile(ctx, "nope", "all")
	if err != nil || ok {
		t.Fatalf("want ok=false err=nil, got ok=%v err=%v", ok, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestReadProfile -v`
Expected: FAIL — `ReadProfileList undefined`.

- [ ] **Step 3: Implement `reads_profile.go`**

Create the file with the DTOs from **Interfaces** and the two read methods. Follow the row-scan style of `reads_watch.go` (explicit `QueryContext` loops, `rows.Close()` on each path, `orEmpty` on every slice before returning). For `ReadProfile` when `rng != "all"`, run:

```go
	var rp float64
	var lbe int64
	var lbs string
	_ = s.db.QueryRowContext(ctx,
		`SELECT rewatch_pct, longest_binge_episodes, longest_binge_series_name
		   FROM agg_profile_summary WHERE user_id=? AND range='all'`, userID,
	).Scan(&rp, &lbe, &lbs)
	out.Summary.RewatchPct = rp
	out.Summary.LongestBinge.Episodes = lbe
	out.Summary.LongestBinge.SeriesName = lbs
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestReadProfile -v`
Expected: PASS.

- [ ] **Step 5: Full package + vet + lint**

Run: `CGO_ENABLED=0 go test ./internal/store/... && go vet ./internal/store/... && golangci-lint run ./internal/store/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/reads_profile.go internal/store/reads_profile_test.go
git commit -m "feat(store): ReadProfileList + ReadProfile (range-scoped panels + lifetime fields)"
```

---

## Task 8: HTTP endpoints

**Files:**
- Create: `internal/api/profile.go`, `internal/api/profile_test.go`
- Modify: `internal/api/api.go` (register routes)
- Modify: the smoke test (grep for a test that starts the binary / hits every endpoint — likely `internal/api/*smoke*` or `cmd/ephyra`; add the two paths)

**Interfaces:**
- Consumes: `store.ReadProfileList`, `store.ReadProfile` (Task 7); `s.st.GetRefreshMeta`; `store.IsStale`; `writeJSON` / `writeError` / `s.meta` (`internal/api/api.go`); `validRanges` (`internal/api/watch.go`).
- Produces: `GET /api/profile` and `GET /api/profile/{userID}` per spec §9.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/profile_test.go` — follow the seeding style of `internal/api/watch_test.go` (grep it for how it builds a `*store.Store` + `Server`). Cover:

```go
// - GET /api/profile with no library refresh -> 200 with plugin_available:false
//   is NOT required; but /api/profile/{user} with no dim_user row -> ... actually
//   /api/profile itself never 503s. /api/profile/{user} 404s an unknown user and
//   503 not_ready only when GetRefreshMeta(ctx,"library") is absent.
func TestHandleProfileList_OK(t *testing.T) { /* seed dim_user + agg_profile_summary, GET /api/profile, assert envelope + users[] */ }
func TestHandleProfile_OK(t *testing.T)      { /* seed, GET /api/profile/u1?range=30d, assert data.summary + data.range=="30d" */ }
func TestHandleProfile_BadRange(t *testing.T) { /* ?range=weekly -> 400 bad_request */ }
func TestHandleProfile_UnknownUser(t *testing.T) { /* GET /api/profile/ghost -> 404 not_found JSON */ }
func TestHandleProfile_NotReady(t *testing.T) { /* no library refresh_meta -> 503 not_ready */ }
```

Write these out fully in the style of `watch_test.go` (table or individual `httptest` calls against `s.Handler()`), asserting status codes and JSON body keys.

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/api/ -run TestHandleProfile -v`
Expected: FAIL — route returns 404 (not registered) / handler undefined.

- [ ] **Step 3: Implement `profile.go`**

```go
package api

import (
	"net/http"

	"github.com/andriykohut/ephyra/internal/store"
)

func (s *Server) handleProfileList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := s.st.ReadProfileList(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	stale := false
	if wm, wok, _ := s.st.GetRefreshMeta(ctx, "watch"); wok && wm.PluginAvailable {
		stale = store.IsStale(wm, wok, s.cfg.RefreshWatch, s.now())
	}
	writeJSON(w, http.StatusOK, data, s.meta(stale))
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "30d"
	}
	if !validRanges[rng] {
		writeError(w, http.StatusBadRequest, "bad_request", "range must be 30d|90d|1y|all")
		return
	}

	if _, ok, err := s.st.GetRefreshMeta(ctx, "library"); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	} else if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first library refresh has not completed")
		return
	}

	userID := r.PathValue("userID")
	data, ok, err := s.st.ReadProfile(ctx, userID, rng)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "no such user")
		return
	}

	stale := false
	if wm, wok, _ := s.st.GetRefreshMeta(ctx, "watch"); wok && wm.PluginAvailable {
		stale = store.IsStale(wm, wok, s.cfg.RefreshWatch, s.now())
	}
	writeJSON(w, http.StatusOK, data, s.meta(stale))
}
```

- [ ] **Step 4: Register the routes**

In `internal/api/api.go` `Handler()`, next to the other `GET /api/*` lines:

```go
	mux.HandleFunc("GET /api/profile", s.handleProfileList)
	mux.HandleFunc("GET /api/profile/{userID}", s.handleProfile)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/api/ -run TestHandleProfile -v`
Expected: PASS.

- [ ] **Step 6: Extend the smoke sweep**

Find the smoke/integration test that hits every endpoint (grep: `grep -rn "api/watch/stats" --include=*_test.go`). Add `/api/profile` and `/api/profile/<a seeded user id>?range=all` to its list, asserting `200` + that the body has a `data` key. If the smoke test runs against the fixtures with no users, `/api/profile` still returns `200` (empty `users: []`), so at minimum assert that.

- [ ] **Step 7: Whole repo + vet + lint**

Run: `CGO_ENABLED=0 go test ./... && go vet ./... && golangci-lint run`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/api/profile.go internal/api/profile_test.go internal/api/api.go internal/api/*smoke*
git commit -m "feat(api): GET /api/profile and GET /api/profile/{userID}"
```

---

## Task 9: Frontend — the `/profile` page

**Files:**
- Modify: `web/src/api/types.ts`, `web/src/api/queries.ts`, `web/src/router.tsx`, `web/src/components/AppShell.tsx`
- Create: `web/src/routes/profile.tsx`, `web/src/routes/profile.test.tsx`

**Interfaces:**
- Consumes: `fetchEnvelope` (`web/src/api/client.ts`); components `SegmentedControl`, `UserSelect`, `Panel`, `DataTable` (`type Column`), `StatCard`, `GenreBand`, `StaleBanner`, `Skeleton`; `Route` from `web/src/routes/__root.tsx`; `fmtInt` / `fmtRuntime` (`web/src/lib/format.ts`).
- Produces: `web/src/routes/profile.tsx` exports `Route` (path `/profile`, `validateSearch` → `{ user: string; range: WatchRange }`) and `ProfileView` (props `{ user, range, onUser, onRange }`) for the test to render directly, mirroring `WatchStatsView` in `web/src/routes/watch.tsx`.

- [ ] **Step 1: Add the types**

In `web/src/api/types.ts`, after the `WatchStats` block:

```ts
export interface ProfileListEntry {
  id: string;
  name: string;
  total_watch_sec: number;
  total_plays: number;
  finished_pct: number;
  rewatch_pct: number;
  longest_binge_episodes: number;
  last_play: string;
}
export interface ProfileList {
  plugin_available: boolean;
  coverage: { first_play: string; last_play: string; total_plays: number };
  users: ProfileListEntry[];
}

export interface Profile {
  range: WatchRange;
  user: { id: string; name: string };
  summary: {
    watch_sec: number;
    plays: number;
    distinct_titles: number;
    days_active: number;
    finished_pct: number;
    bailed_pct: number;
    rewatch_pct: number;
    longest_binge: { episodes: number; series_name: string };
    show_of_range: { series_id: string; series_name: string };
    first_play: string;
    last_play: string;
  };
  completion: { scope: string; bucket: string; count: number }[];
  abandoned: {
    scope: string;
    item_id: string;
    name: string;
    series_name: string;
    bailed_count: number;
  }[];
  rewatch: {
    scope: string;
    item_id: string;
    name: string;
    series_name: string;
    watch_days: number;
  }[];
  binge: {
    series_id: string;
    series_name: string;
    run_episodes: number;
    run_start: string;
    run_end: string;
  }[];
  taste: {
    genre: { key: string; watch_sec: number; plays: number }[];
    decade: { key: string; watch_sec: number; plays: number }[];
    length: { key: string; watch_sec: number; plays: number }[];
    signature_genres: string[];
  };
  baseline: {
    genre: { key: string; watch_sec: number }[];
    decade: { key: string; watch_sec: number }[];
    length: { key: string; watch_sec: number }[];
  };
}
```

- [ ] **Step 2: Add the queries**

In `web/src/api/queries.ts`:

```ts
export const profileListQuery = () =>
  queryOptions({
    queryKey: ["profile-list"],
    queryFn: () => fetchEnvelope<ProfileList>("/api/profile"),
    staleTime: 10 * 60 * 1000,
  });

export const profileQuery = (userID: string, range: WatchRange) =>
  queryOptions({
    queryKey: ["profile", userID, range],
    queryFn: () => fetchEnvelope<Profile>(`/api/profile/${userID}?range=${range}`),
    enabled: userID !== "",
    staleTime: 10 * 60 * 1000,
  });
```

Add `Profile`, `ProfileList` to the `import type { ... }` line at the top.

- [ ] **Step 3: Write the failing component test**

Create `web/src/routes/profile.test.tsx` in the style of `web/src/routes/cleanup.test.tsx` (a `wrap()` with a `QueryClient`, `vi.spyOn(globalThis, "fetch")`). Because `/profile` makes **two** requests (list + detail), route by URL:

```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, Profile, ProfileList } from "@/api/types";
import { ProfileView } from "./profile";

const list: Envelope<ProfileList> = {
  data: {
    plugin_available: true,
    coverage: { first_play: "2025-01-01", last_play: "2025-05-01", total_plays: 120 },
    users: [
      {
        id: "u1",
        name: "alice",
        total_watch_sec: 9000,
        total_plays: 12,
        finished_pct: 0.6,
        rewatch_pct: 0.25,
        longest_binge_episodes: 6,
        last_play: "2025-05-01",
      },
    ],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

const detail: Envelope<Profile> = {
  data: {
    range: "30d",
    user: { id: "u1", name: "alice" },
    summary: {
      watch_sec: 9000,
      plays: 12,
      distinct_titles: 5,
      days_active: 7,
      finished_pct: 0.6,
      bailed_pct: 0.1,
      rewatch_pct: 0.25,
      longest_binge: { episodes: 6, series_name: "The Show" },
      show_of_range: { series_id: "s1", series_name: "The Show" },
      first_play: "2025-01-01",
      last_play: "2025-05-01",
    },
    completion: [
      { scope: "movie", bucket: "finished", count: 3 },
      { scope: "movie", bucket: "bailed", count: 1 },
    ],
    abandoned: [
      { scope: "movie", item_id: "m9", name: "Half-Watched", series_name: "", bailed_count: 2 },
    ],
    rewatch: [
      { scope: "movie", item_id: "m1", name: "Alpha", series_name: "", watch_days: 3 },
    ],
    binge: [
      {
        series_id: "s1",
        series_name: "The Show",
        run_episodes: 6,
        run_start: "2025-02-01",
        run_end: "2025-02-01",
      },
    ],
    taste: {
      genre: [{ key: "Drama", watch_sec: 6000, plays: 4 }],
      decade: [{ key: "1990", watch_sec: 6000, plays: 4 }],
      length: [{ key: "90-120m", watch_sec: 6000, plays: 4 }],
      signature_genres: ["Drama"],
    },
    baseline: {
      genre: [{ key: "Drama", watch_sec: 1000 }],
      decade: [{ key: "1990", watch_sec: 1000 }],
      length: [{ key: "90-120m", watch_sec: 1000 }],
    },
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}
const noop = () => {};
afterEach(() => vi.restoreAllMocks());

test("renders the picked user's panels", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    const body = url.includes("/api/profile/") ? detail : list;
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });

  render(wrap(<ProfileView user="u1" range="30d" onUser={noop} onRange={noop} />));

  await waitFor(() => expect(screen.getByText(/Alpha/)).toBeInTheDocument());
  expect(screen.getByText(/The Show/)).toBeInTheDocument();
  expect(screen.getByText(/Half-Watched/)).toBeInTheDocument();
  expect(screen.getByText(/Drama/)).toBeInTheDocument();
});

test("plugin-absent shows the enable-plugin notice", async () => {
  vi.spyOn(globalThis, "fetch").mockImplementation((input) => {
    const url = String(input);
    const body = url.includes("/api/profile/")
      ? detail
      : { ...list, data: { ...list.data, plugin_available: false } };
    return Promise.resolve(new Response(JSON.stringify(body), { status: 200 }));
  });
  render(wrap(<ProfileView user="u1" range="30d" onUser={noop} onRange={noop} />));
  await waitFor(() =>
    expect(screen.getByText(/playback reporting/i)).toBeInTheDocument(),
  );
});
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `cd web && npx vitest run src/routes/profile.test.tsx`
Expected: FAIL — cannot resolve `./profile`.

- [ ] **Step 5: Implement `profile.tsx`**

Create `web/src/routes/profile.tsx` modelled on `web/src/routes/watch.tsx`:
- `RANGES` array + `validateSearch` identical shape to `watch.tsx` but default `user: ""` (empty until the list resolves; on first data, if `user===""`, call `onUser(users[0].id)`).
- `ProfileView({ user, range, onUser, onRange })`: `useQuery(profileListQuery())` for the picker + `useQuery(profileQuery(user, range))` for the panels.
- Controls: `<SegmentedControl>` (range) + `<UserSelect value={user} users={list.users} onChange={onUser} />`.
- If `list.data.plugin_available === false`: render a `<Panel>` with the copy "Watch Stats and profiles need the Playback Reporting plugin." (match the wording the Watch Stats page uses — grep `web/src/routes/watch.tsx` for it and reuse) and still show the picker.
- Header: four `<StatCard>` — total watch (`fmtRuntime(summary.watch_sec)`), finished % (`Math.round(summary.finished_pct*100)%`), rewatch % (`summary.rewatch_pct`), longest binge (`${summary.longest_binge.episodes} eps · ${summary.longest_binge.series_name}`).
- **Completion** `<Panel>`: a simple stacked bar built from `completion` (group by bucket, sum count across scopes) — inline divs with width `%`, no chart lib needed — plus a `<DataTable>` of `abandoned` (columns: title = `name` or `series_name`, `bailed_count`).
- **Rewatch** `<Panel>`: big number `Math.round(summary.rewatch_pct*100)%` + `<DataTable>` of `rewatch` (columns: `name`, `watch_days`).
- **Binge** `<Panel>`: `<StatCard>` longest run + `<DataTable>` of `binge` (columns: `series_name`, `run_episodes`, `run_start`).
- **Taste** `<Panel>`: reuse `<GenreBand>` if its props fit (grep `web/src/components/GenreBand.tsx` for the shape); otherwise inline bars comparing `taste.genre` vs `baseline.genre` by share. Show `taste.signature_genres.join(", ")` as a caption. Add a small decade bar list from `taste.decade`.
- Loading → `<Skeleton>`; `q.isError` → inline error text; `meta.stale` → `<StaleBanner>`.
- `ProfilePage()` wrapper + `export const Route = createRoute({ getParentRoute: () => rootRoute, path: "/profile", validateSearch, component: ProfilePage })` exactly like `watch.tsx`'s tail.

- [ ] **Step 6: Register the route + nav**

`web/src/router.tsx`:

```ts
import { Route as profileRoute } from "./routes/profile";
// ...
const routeTree = rootRoute.addChildren([
  indexRoute,
  libraryRoute,
  watchRoute,
  profileRoute,
  nowRoute,
  cleanupRoute,
]);
```

`web/src/components/AppShell.tsx` — add to `NAV` (pick an icon already imported from `lucide-react`, e.g. `User`; add it to the import):

```ts
  { to: "/profile", label: "Profiles", icon: User },
```

- [ ] **Step 7: Run the test + typecheck + lint + build**

Run:
```
cd web && npx vitest run src/routes/profile.test.tsx && npx tsc -b && npm run lint && npm run build
```
Expected: all PASS. `tsc -b` clean, `biome ci .` clean, `vite build` succeeds.

- [ ] **Step 8: Full frontend test run**

Run: `cd web && npm run test`
Expected: PASS (existing route tests unaffected).

- [ ] **Step 9: Commit**

```bash
git add web/src/
git commit -m "feat(web): /profile page — completion, rewatch, binge, taste"
```

---

## Task 10: Docs

**Files:**
- Modify: `README.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: README — the growth note**

In `README.md`, in the configuration / storage area (near where `STORE_PATH` / the `/data` volume is described), add one deadpan line:

```
Ephyra keeps its own copy of playback history in `playback_events` so profile
and watch stats survive the Playback Reporting plugin's retention window. It's
one row per play — single-digit MB a year on a home server, tens of MB on a busy
multi-user one. Nothing prunes it; that's deliberate.
```

- [ ] **Step 2: CHANGELOG**

Add an entry under the current unreleased section:

```
- Profiles page (`/profile`): per-user completion vs abandonment, rewatch,
  binge runs, and a taste fingerprint.
- Watch Stats history now outlives the Playback Reporting plugin's max-age
  setting — Ephyra accumulates playback events in its own store. Existing
  installs backfill from whatever the plugin currently retains on the first
  refresh after upgrading.
```

- [ ] **Step 3: Verify the whole thing once more**

Run:
```
CGO_ENABLED=0 go test ./... && go vet ./... && golangci-lint run && cd web && npm run test && npx tsc -b && npm run lint && npm run build && cd .. && make build
```
Expected: all green; `./ephyra` builds.

- [ ] **Step 4: Commit**

```bash
git add README.md CHANGELOG.md
git commit -m "docs: profiles page + playback_events retention note"
```

---

## Self-Review

**1. Spec coverage**

| Spec section | Task |
|---|---|
| §3 data flow (append → read-back → both aggregators, 3 txns) | 4, 6 |
| §4 `playback_events` schema + indexes + dedup hash | 1, 2 |
| §4.1 `INSERT … ON CONFLICT` enrichment refresh | 2 |
| §4.2 backfill on upgrade (empty-spine skip bypass) | 4 |
| §4.3 dedup edge cases | 2 (hash design + test) |
| §4.4 growth / no prune knob | 10 (README) |
| §5 widen source enrichment (runtime/genres/year) | 3 |
| §6.0 roll to daily grain | 5 (`rollDaily`) |
| §6.1 completion buckets + thresholds | 5 |
| §6.2 rewatch (lifetime) + `rewatch_pct` | 5 |
| §6.3 binge runs + `bingeGapHours` + show-of-range | 5 |
| §6.4 taste (watch_sec-weighted) + baseline + signature genres | 5, 7 (signature calc) |
| §6.5 four fixed ranges, `range` column, lifetime exceptions | 5 |
| §7.1 migration `0003` (all 8 tables) | 1 |
| §7.2 store functions | 2, 6, 7 |
| §8 `RunWatchOnce` changes | 4, 6 |
| §9.1 `GET /api/profile` | 7, 8 |
| §9.2 `GET /api/profile/{userID}` + range param + 404 + not_ready | 7, 8 |
| §9.3 `POST /api/refresh?job=watch` also rewrites profiles | 6 (via `RunWatchOnce`) |
| §10 `/profile` frontend | 9 |
| §11 testing (fixtures, golden, scheduler regression, API, smoke, vitest) | 3, 4, 5, 6, 7, 8, 9 |
| §12 build sequence | mirrored by task order |
| CHANGELOG / README notes | 10 |

No gaps.

**2. Placeholder scan**

Task 5 Step 4 leaves helper *bodies* to the implementer but names every helper, its inputs, and the exact behaviour each test pins — and the five tests fully constrain the output. Task 8 Step 1 and Task 9 Step 5 describe components rather than pasting every line; both give the exact prop shapes, the components to reuse, and a runnable test as the spec. These are right-sized, not placeholders — no "TODO", no "handle edge cases", no undefined types.

**3. Type consistency**

- `source.PlaybackEvent` new fields — `ItemRuntimeSec int64`, `ItemGenres []string`, `ItemYear int` — defined in Task 3, consumed in Task 5 with those exact names/types.
- `aggregate.ProfileAggregates` + row structs — defined in Task 5's Interfaces, written in Task 6's `WriteProfileAggregates` column-by-column, matching field names (`LongestBingeEpisodes`, `ShowOfRangeSeriesID`, …).
- `store.WatchCoverage` — reused (not redefined) by `store.ProfileList` in Task 7.
- `validRanges` (map in `internal/api/watch.go`) — reused by `handleProfile` in Task 8, not redeclared.
- `store.ReadProfile` returns `(Profile, bool, error)` in both Task 7's definition and Task 8's call site.
- Frontend `Profile` / `ProfileList` TS interfaces (Task 9 Step 1) mirror the Go JSON tags from Task 7 field-by-field (`total_watch_sec`, `longest_binge.episodes`, `signature_genres`).
- `ProfileView` prop shape `{ user, range, onUser, onRange }` — defined in Task 9's Interfaces, used by its own test in Step 3.

Consistent.
