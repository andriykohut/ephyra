# Ephyra — Watch Stats & Cleanup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Watch Stats and Cleanup pages end to end — a `watch` refresh job that reads the Playback Reporting plugin DB, an extended `library` job that also materializes cleanup candidates and core play counts, two new JSON endpoints, and the two React pages that consume them (with a page-level user filter on Watch Stats).

**Architecture:** Unchanged from Plan 1. The `library` job (already reading `jellyfin.db`) gains three pure aggregation outputs — `agg_cleanup`, `dim_user`, `agg_played_core`. A new `watch` job reads `playback_reporting.db` plus a targeted read of the `jellyfin.db` copy for name/series enrichment, then `aggregate.Watch` rolls the events into `watch_events_daily` + `agg_watch_heatmap`. The API serves both pages entirely from these materialized tables.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` (pure Go, no CGO), stdlib `net/http` + `log/slog`; React 19 + Vite + TypeScript, Tailwind v4, TanStack Router + Query, Apache ECharts; Biome; Vitest 4; Docker (distroless).

**Spec:** `docs/superpowers/specs/2026-08-30-ephyra-watch-stats-and-cleanup-design.md` — read it alongside this plan. Parent spec: `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`. This plan implements the Plan-2 spec in full: §3 (schema notes), §4 (migration `0002`), §5 (source layer), §6 (aggregation), §7 (library-job additions), §8 (`watch` job), §9 (`/api/watch/stats` + `/api/cleanup`), §10 (`/watch` + `/cleanup` pages), §11 (docs), §12 (tests).

## Global Constraints

Copied from the spec. Every task inherits these.

- **No CGO.** `CGO_ENABLED=0` for every build. SQLite access is `modernc.org/sqlite` only.
- **Module path:** `github.com/andriykohut/ephyra`.
- **Reads only.** Ephyra never writes to Jellyfin or the mounted data dir. All Jellyfin DB access is against copies in `WORK_DIR`, opened read-only (Plan 1's `openForRead`).
- **Canonical IDs.** Every user/item/series id stored by Ephyra is **dashless lowercase** (`lower(replace(id,'-',''))`). The plugin DB is already that shape; `jellyfin.db` is dashed-uppercase and is normalized on read.
- **No timezone re-zoning for watch data.** `playback_reporting.db` timestamps are server-local wall time. `aggregate.Watch` buckets `day`/`dow`/`hour` off the literal parsed components and takes no `*time.Location`. (`aggregate.Library`'s `TZ` handling for the growth chart is unchanged — `jellyfin.db` stores UTC.)
- **Stored timestamps are RFC3339.** `agg_cleanup.added_at` / `.last_played_at` and `agg_played_core.last_played_at` are `t.UTC().Format(time.RFC3339)` (empty string / SQL NULL when the source time is zero). Range and staleness cutoffs are formatted the same way and compared lexically.
- **Response envelope:** `{ "data": …, "meta": { "generated_at": <RFC3339>, "stale": <bool> } }`. Errors: `{ "error": { "code": <string>, "message": <string> } }`. Error codes used here: `not_ready`, `plugin_unavailable` (informational only — Watch Stats returns `200` with `plugin_available:false`, never this code), `bad_request`, `internal`.
- **Staleness rule:** stale when the job's last `refresh_meta` row has `ok = 0`, OR `now - last_run_at > 2 × interval`. A plugin-absent `watch` job is `ok = 1` and **not** stale.
- **`agg_*` / `dim_*` / `watch_events_daily` tables are fully rewritten per job inside one transaction.** The API never sees a partial set.
- **TDD.** Each task: write the failing test → run it, see it fail → minimal implementation → run it, see it pass → commit. Small commits.

## Domain primer (the DBs this plan reads)

Verified 2026-08-30 against a real 10.11.11. Full detail in the spec §3; the essentials:

### `playback_reporting.db` (the Playback Reporting plugin)

```sql
CREATE TABLE PlaybackActivity (
  DateCreated    DATETIME NOT NULL,  -- 'YYYY-MM-DD HH:MM:SS.fffffff', SERVER-LOCAL wall time, no zone
  UserId         TEXT,               -- 32 hex, no dashes, lowercase
  ItemId         TEXT,               -- 32 hex, no dashes, lowercase
  ItemType       TEXT,               -- 'Movie' | 'Episode' (we keep only these)
  ItemName       TEXT,               -- movie title, or 'Series - s01e02 - Ep title'
  PlaybackMethod TEXT,               -- see method buckets below
  ClientName     TEXT, DeviceName TEXT,
  PlayDuration   INT                 -- SECONDS (0 is valid — instant stop)
);
CREATE TABLE UserList (UserId TEXT); -- ignore
```

No `RemoteAddress`. No primary key. Missing file, or `PlaybackActivity` absent → the plugin is not installed.

**`PlaybackMethod` → 5 buckets** (string match, done in `aggregate`):

| Plugin string | Bucket |
|---|---|
| `DirectPlay` | `DirectPlay` |
| `Transcode (v:direct a:direct)` | `Remux` |
| `Transcode (v:direct a:…)` (a not `direct`) | `AudioTranscode` |
| `Transcode (v:… …)` (v not `direct`) | `VideoTranscode` |
| anything else (incl. unparseable `Transcode (...)`) | `Other` |

### `jellyfin.db` — the tables this plan adds reads for

```sql
CREATE TABLE Users (
  Id TEXT PRIMARY KEY,      -- dashed uppercase
  Username TEXT NOT NULL, ...
);
CREATE TABLE UserData (     -- PK (ItemId, UserId, CustomDataKey)
  ItemId TEXT, UserId TEXT, CustomDataKey TEXT,
  LastPlayedDate TEXT NULL, -- 'YYYY-MM-DD HH:MM:SS.fff'
  PlayCount INTEGER NOT NULL,
  Played INTEGER NOT NULL,  -- 0 | 1 (1 = finished past Jellyfin's threshold)
  ...
);
```

- `UserData` can hold multiple rows per `(ItemId, UserId)` (differing `CustomDataKey`). Aggregate `GROUP BY ItemId, UserId` with `MAX` first.
- "Never watched" = `LastPlayedDate IS NULL` for **every** user. A partial play sets `LastPlayedDate` while `Played` stays `0`, so `Played` is not the test.
- Episode → series linkage is on `BaseItems`: `SeriesId`, `SeriesName` (populated in practice).

## File structure

```
internal/store/migrations/0002_watch_cleanup.sql   NEW — spec §4
internal/store/writes.go            MODIFY — WriteLibraryAggregates grows (+cleanup, +users, +coreplays)
internal/store/writes_watch.go      NEW — WriteWatchAggregates
internal/store/reads_cleanup.go     NEW — ReadCleanup + CSV row iterator
internal/store/reads_watch.go       NEW — ReadWatchStats
internal/store/reads_cleanup_test.go / reads_watch_test.go / writes_watch_test.go  NEW
internal/store/writes_test.go       MODIFY — assert the 3 new library-job tables round-trip

internal/source/source.go           MODIFY — LibraryItem +6 fields; UserRef, UserPlay; PlaybackEvent struct; ErrPluginUnavailable
internal/source/ids.go              NEW — CanonID
internal/source/ids_test.go         NEW
internal/source/file/queries.go     MODIFY — extend library query; add UserData/Users/UserPlays reads
internal/source/file/queries_test.go MODIFY
internal/source/file/playback.go    NEW — queryPlaybackEvents + enrichPlaybackEvents
internal/source/file/file.go        MODIFY — PlaybackEvents (two-DB open + enrichment)
internal/source/file/file_test.go   MODIFY

internal/aggregate/cleanup.go       NEW — Cleanup(snap) + Users(snap)
internal/aggregate/coreplays.go     NEW — CorePlays(plays)
internal/aggregate/watch.go         NEW — Watch(events) + methodBucket + isoWeekMonday
internal/aggregate/timefmt.go       NEW — rfc3339 / parse helpers shared by the new files
internal/aggregate/{cleanup,coreplays,watch}_test.go  NEW

internal/scheduler/scheduler.go     MODIFY — RunWatchOnce, watchMu, watch ticker, trigger wiring, generalized recordFailure
internal/scheduler/scheduler_test.go MODIFY

internal/api/api.go                 MODIFY — register GET /api/watch/stats + GET /api/cleanup
internal/api/watch.go               NEW — handleWatchStats
internal/api/cleanup.go             NEW — handleCleanup (+ CSV)
internal/api/watch_test.go / cleanup_test.go  NEW

testdata/library.fixture.sql        MODIFY — Users, UserData, SeriesId/SeriesName on episodes
testdata/playback_reporting.fixture.sql  NEW
internal/testsupport/fixtures.go    MODIFY — PlaybackFixtureDB + a two-DB layout helper
internal/testsupport/fixtures_test.go MODIFY

cmd/ephyra/main_test.go             NEW — binary smoke test (spec §12.4)

web/src/api/types.ts                MODIFY — WatchStats, Cleanup DTOs
web/src/api/queries.ts              MODIFY — watchStatsQuery, cleanupQuery
web/src/components/SegmentedControl.tsx / UserSelect.tsx / DataTable.tsx  NEW
web/src/charts/Heatmap.tsx / MethodBars.tsx  NEW
web/src/charts/theme.ts             MODIFY — method colours + heatmap ramp
web/src/routes/cleanup.tsx          REPLACE — real page
web/src/routes/watch.tsx            REPLACE — real page
web/src/routes/cleanup.test.tsx / watch.test.tsx  NEW

docs/schema-notes.md                MODIFY — spec §3 findings
README.md                           MODIFY — feature list, "Test against a real library" gains the plugin DB
CLAUDE.md                           MODIFY — watch job reads two DBs; canonical id form
```

---

## Task 1: Migration 0002 + store scaffolding for the new tables

**Files:**
- Create: `internal/store/migrations/0002_watch_cleanup.sql`
- Create: `internal/store/writes_watch.go`
- Modify: `internal/store/writes.go`
- Test: `internal/store/store_test.go` (add one test), `internal/store/writes_watch_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces:
  ```go
  // internal/aggregate — row types the store persists (defined here so store
  // imports only "aggregate", never "source", matching Plan 1).
  package aggregate

  type CleanupRow struct {
      ItemID, Scope, Name, Library string
      Bytes, Episodes              int64
      AddedAt                      string // RFC3339, "" if unknown
      LastPlayedAt                 string // RFC3339, "" = never
  }
  type UserRow struct{ ID, Name string }
  type CorePlayRow struct {
      UserID, Scope, ItemID, Name string
      PlayCount                   int64
      LastPlayedAt                string // RFC3339, "" if none
  }
  type WatchDailyRow struct {
      Day, UserID, ItemID, Scope, Name, SeriesID, SeriesName, Method string
      Plays, WatchSec                                                int64
  }
  type HeatmapRow struct {
      UserID         string
      DOW, Hour      int
      WatchSec, Plays int64
  }
  ```
  ```go
  // internal/store
  func (s *Store) WriteWatchAggregates(ctx context.Context, daily []aggregate.WatchDailyRow, heat []aggregate.HeatmapRow) error
  // WriteLibraryAggregates signature grows (Task 5 fills the bodies):
  func (s *Store) WriteLibraryAggregates(ctx context.Context, a aggregate.LibraryAggregates,
      cleanup []aggregate.CleanupRow, users []aggregate.UserRow, core []aggregate.CorePlayRow) error
  ```

- [ ] **Step 1: Create `internal/store/migrations/0002_watch_cleanup.sql`**

```sql
-- Redefines the three watch/cleanup tables from 0001 (created empty, never
-- written) and adds dim_user + agg_played_core. Forward-only. The migration
-- runner owns schema_migrations — do not touch it here.

CREATE TABLE dim_user (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL
);

CREATE TABLE agg_played_core (
  user_id        TEXT NOT NULL,
  scope          TEXT NOT NULL,   -- 'movie' | 'series'
  item_id        TEXT NOT NULL,
  name           TEXT NOT NULL,
  play_count     INTEGER NOT NULL,
  last_played_at TEXT,
  PRIMARY KEY (user_id, item_id)
);

DROP TABLE watch_events_daily;
CREATE TABLE watch_events_daily (
  day         TEXT NOT NULL,      -- 'YYYY-MM-DD', plugin server-local wall date
  user_id     TEXT NOT NULL,
  item_id     TEXT NOT NULL,
  scope       TEXT NOT NULL,      -- 'movie' | 'episode'
  name        TEXT NOT NULL,
  series_id   TEXT NOT NULL DEFAULT '',
  series_name TEXT NOT NULL DEFAULT '',
  plays       INTEGER NOT NULL,
  watch_sec   INTEGER NOT NULL,
  method      TEXT NOT NULL,      -- DirectPlay|Remux|AudioTranscode|VideoTranscode|Other
  PRIMARY KEY (day, user_id, item_id, method)
);

DROP TABLE agg_watch_heatmap;
CREATE TABLE agg_watch_heatmap (
  user_id   TEXT NOT NULL,
  dow       INTEGER NOT NULL,     -- 0=Sun .. 6=Sat, server-local
  hour      INTEGER NOT NULL,     -- 0..23, server-local
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, dow, hour)
);

DROP TABLE agg_cleanup;
CREATE TABLE agg_cleanup (
  item_id        TEXT PRIMARY KEY,
  scope          TEXT NOT NULL,   -- 'movie' | 'series'
  name           TEXT NOT NULL,
  library        TEXT NOT NULL,
  bytes          INTEGER NOT NULL,
  episodes       INTEGER NOT NULL,
  added_at       TEXT NOT NULL,
  last_played_at TEXT
);
```

- [ ] **Step 2: Add the `aggregate` row types.** Create `internal/aggregate/rows.go` with the six structs from the Interfaces block above (`CleanupRow`, `UserRow`, `CorePlayRow`, `WatchDailyRow`, `HeatmapRow`; `LibraryAggregates` already exists in `library.go`). No logic — just types.

- [ ] **Step 3: Write the failing test** — append to `internal/store/store_test.go`

```go
func TestMigration0002Redefinitions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// new tables exist
	for _, table := range []string{"dim_user", "agg_played_core"} {
		if _, err := s.DB().ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err != nil {
			t.Errorf("%s missing: %v", table, err)
		}
	}
	// redefined columns exist
	if _, err := s.DB().ExecContext(ctx,
		`SELECT series_id, series_name FROM watch_events_daily LIMIT 1`); err != nil {
		t.Errorf("watch_events_daily.series_* missing: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`SELECT user_id FROM agg_watch_heatmap LIMIT 1`); err != nil {
		t.Errorf("agg_watch_heatmap.user_id missing: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`SELECT scope, episodes FROM agg_cleanup LIMIT 1`); err != nil {
		t.Errorf("agg_cleanup.scope/episodes missing: %v", err)
	}
	// runner still owns schema_migrations: two rows applied
	var n int
	if err := s.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 migrations applied, got %d", n)
	}
}
```

- [ ] **Step 4: Run it, see it fail**

Run: `go test ./internal/store/ -run TestMigration0002 -v`
Expected: FAIL — `no such column: series_id` (0002 not present yet).

- [ ] **Step 5: The migration file from Step 1 makes it pass.** Run: `go test ./internal/store/ -run TestMigration0002 -v` → PASS.

- [ ] **Step 6: Write the failing test** — `internal/store/writes_watch_test.go`

```go
package store

import (
	"context"
	"testing"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func TestWriteWatchAggregatesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	daily := []aggregate.WatchDailyRow{
		{Day: "2025-01-06", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo",
			Method: "DirectPlay", Plays: 1, WatchSec: 3600},
		{Day: "2025-01-07", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo",
			Method: "VideoTranscode", Plays: 1, WatchSec: 1800},
	}
	heat := []aggregate.HeatmapRow{{UserID: "u1", DOW: 1, Hour: 20, WatchSec: 3600, Plays: 1}}

	if err := s.WriteWatchAggregates(ctx, daily, heat); err != nil {
		t.Fatal(err)
	}
	// rewrites: second call replaces, not appends
	if err := s.WriteWatchAggregates(ctx, daily[:1], heat); err != nil {
		t.Fatal(err)
	}

	var rows, hrows int
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM watch_events_daily`).Scan(&rows)
	s.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_watch_heatmap`).Scan(&hrows)
	if rows != 1 || hrows != 1 {
		t.Fatalf("after rewrite want 1/1 rows, got %d/%d", rows, hrows)
	}
}
```

- [ ] **Step 7: Run it, see it fail**

Run: `go test ./internal/store/ -run TestWriteWatchAggregates -v`
Expected: FAIL — `undefined: (*Store).WriteWatchAggregates`.

- [ ] **Step 8: Implement `internal/store/writes_watch.go`**

```go
package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

// WriteWatchAggregates replaces every watch-derived row in one transaction.
func (s *Store) WriteWatchAggregates(ctx context.Context, daily []aggregate.WatchDailyRow, heat []aggregate.HeatmapRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{`DELETE FROM watch_events_daily`, `DELETE FROM agg_watch_heatmap`} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for _, r := range daily {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO watch_events_daily
			  (day, user_id, item_id, scope, name, series_id, series_name, plays, watch_sec, method)
			VALUES (?,?,?,?,?,?,?,?,?,?)`,
			r.Day, r.UserID, r.ItemID, r.Scope, r.Name, r.SeriesID, r.SeriesName, r.Plays, r.WatchSec, r.Method,
		); err != nil {
			return err
		}
	}
	for _, r := range heat {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_watch_heatmap (user_id, dow, hour, watch_sec, plays)
			VALUES (?,?,?,?,?)`,
			r.UserID, r.DOW, r.Hour, r.WatchSec, r.Plays,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 9: Grow `WriteLibraryAggregates`'s signature** in `internal/store/writes.go` — add the three params, and at the top of the transaction add `DELETE FROM agg_cleanup`, `DELETE FROM dim_user`, `DELETE FROM agg_played_core`, then `INSERT` loops:

```go
// signature:
func (s *Store) WriteLibraryAggregates(ctx context.Context, a aggregate.LibraryAggregates,
	cleanup []aggregate.CleanupRow, users []aggregate.UserRow, core []aggregate.CorePlayRow) error {
	// ... existing tx setup ...
	for _, q := range []string{
		`DELETE FROM agg_totals`,
		`DELETE FROM agg_disk`,
		`DELETE FROM agg_distribution WHERE dimension IN ('genre','decade','library_items')`,
		`DELETE FROM agg_library_growth`,
		`DELETE FROM agg_cleanup`,
		`DELETE FROM dim_user`,
		`DELETE FROM agg_played_core`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	// ... existing inserts ...
	for _, c := range cleanup {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_cleanup (item_id, scope, name, library, bytes, episodes, added_at, last_played_at)
			VALUES (?,?,?,?,?,?,?,?)`,
			c.ItemID, c.Scope, c.Name, c.Library, c.Bytes, c.Episodes, c.AddedAt, nullif(c.LastPlayedAt),
		); err != nil {
			return err
		}
	}
	for _, u := range users {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO dim_user (id, name) VALUES (?,?)`, u.ID, u.Name); err != nil {
			return err
		}
	}
	for _, p := range core {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_played_core (user_id, scope, item_id, name, play_count, last_played_at)
			VALUES (?,?,?,?,?,?)`,
			p.UserID, p.Scope, p.ItemID, p.Name, p.PlayCount, nullif(p.LastPlayedAt),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// nullif turns "" into a SQL NULL so IS NULL filters work.
func nullif(s string) any {
	if s == "" {
		return nil
	}
	return s
}
```

- [ ] **Step 10: Fix the existing callers.** `internal/store/writes_test.go` and `internal/api/api_test.go` (`TestLibraryOverview_OKAndStaleFlag`) and `internal/scheduler/scheduler.go` call `WriteLibraryAggregates(ctx, agg)`. Update each to `WriteLibraryAggregates(ctx, agg, nil, nil, nil)` for now (Task 5 wires the real slices in the scheduler). Add to `internal/store/writes_test.go` a `TestWriteLibraryExtrasRoundTrip` that passes one `CleanupRow`, one `UserRow`, one `CorePlayRow` and asserts each lands and a NULL `last_played_at` reads back as NULL.

- [ ] **Step 11: Run the store + api suites**

Run: `go test ./internal/store/ ./internal/api/ -v`
Expected: PASS.

- [ ] **Step 12: Commit**

```bash
git add internal/store internal/aggregate/rows.go internal/api/api_test.go internal/scheduler/scheduler.go
git commit -m "feat(store): migration 0002; watch + cleanup + dim_user write paths"
```

---

## Task 2: `CanonID` + source type additions

**Files:**
- Create: `internal/source/ids.go`, `internal/source/ids_test.go`
- Modify: `internal/source/source.go`

**Interfaces:**
- Produces:
  ```go
  // internal/source
  func CanonID(s string) string // "11111111-2222-..." -> "111111112222..."; trims, lowercases, drops '-'

  type LibraryItem struct {
      // ... Plan 1 fields unchanged ...
      ID           string
      SeriesID     string    // canonical; "" for movies
      SeriesName   string
      Played       bool
      PlayCount    int
      LastPlayedAt time.Time // zero if never
  }
  type UserRef  struct{ ID, Name string }        // canonical id
  type UserPlay struct {
      UserID, ItemID, Scope, Name string          // Scope: "movie" | "series"; ItemID is movie or series id, canonical
      PlayCount                    int
      LastPlayedAt                 time.Time
  }
  type LibrarySnapshot struct {
      // ... Plan 1 fields unchanged ...
      Users     []UserRef
      UserPlays []UserPlay
  }
  type PlaybackEvent struct {
      At              time.Time // parsed literal components (server-local)
      UserID, UserName string
      ItemID, ItemName string
      ItemType         string   // "movie" | "episode"
      SeriesID, SeriesName string
      Method           string   // RAW plugin string
      PlayDurationSec  int64
  }

  var ErrPluginUnavailable = errors.New("source: playback reporting plugin data not available")
  ```

- [ ] **Step 1: Write the failing test** — `internal/source/ids_test.go`

```go
package source

import "testing"

func TestCanonID(t *testing.T) {
	cases := map[string]string{
		"11111111-2222-3333-4444-555555555555": "11111111222233334444555555555555",
		"  00000000-0000-0000-0000-00000000000A ": "0000000000000000000000000000000a",
		"already1111111122223333444455555555555": "already1111111122223333444455555555555",
		"":                                       "",
	}
	for in, want := range cases {
		if got := CanonID(in); got != want {
			t.Errorf("CanonID(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it, see it fail** — `go test ./internal/source/ -run TestCanonID -v` → `undefined: CanonID`.

- [ ] **Step 3: Implement `internal/source/ids.go`**

```go
package source

import "strings"

// CanonID normalizes a Jellyfin GUID to Ephyra's canonical form: lowercase hex,
// no dashes. jellyfin.db uses dashed-uppercase; the Playback Reporting plugin
// uses dashless-lowercase. Everything Ephyra stores is canonical.
func CanonID(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}
```

- [ ] **Step 4: Extend `internal/source/source.go`** — add the six fields to `LibraryItem`, the `Users` / `UserPlays` fields to `LibrarySnapshot`, the `UserRef` and `UserPlay` types, replace `type PlaybackEvent struct{}` with the struct above, and add `ErrPluginUnavailable` next to `ErrNotImplemented`. Nothing else changes; existing code still compiles because every addition is new.

- [ ] **Step 5: Run it, see it pass** — `go test ./internal/source/... -v` → PASS (existing tests unaffected).

- [ ] **Step 6: Commit**

```bash
git add internal/source/ids.go internal/source/ids_test.go internal/source/source.go
git commit -m "feat(source): CanonID + watch/cleanup snapshot fields, PlaybackEvent"
```

---

## Task 3: Extend the library query + read played-state, users, user-plays

**Files:**
- Modify: `internal/source/file/queries.go`, `internal/source/file/queries_test.go`
- Modify: `testdata/library.fixture.sql`

**Interfaces:**
- Consumes: `CanonID`, the new `source` fields (Task 2).
- Produces: `defaultQueryLibrary(db)` now also fills, on every returned `LibraryItem`: `ID`, `SeriesID`, `SeriesName`, `Played`, `PlayCount`, `LastPlayedAt`; and on the snapshot: `Users []source.UserRef`, `UserPlays []source.UserPlay`.

- [ ] **Step 1: Extend `testdata/library.fixture.sql`.** Add the two columns to `BaseItems`, the `Users` and `UserData` tables, and rows. Append (do not disturb the existing rows):

```sql
-- episodes gain series linkage (added to the CREATE TABLE BaseItems column list:
--   SeriesId TEXT, SeriesName TEXT   -- put them just before IsVirtualItem)
-- and to the two episode INSERTs above: SeriesId='00000000-0000-0000-0000-0000000000F0',
--   SeriesName='Some Show'.

CREATE TABLE Users (
  Id       TEXT PRIMARY KEY,
  Username TEXT NOT NULL
);
INSERT INTO Users (Id, Username) VALUES
 ('11111111-2222-3333-4444-555555555555', 'alice'),
 ('66666666-7777-8888-9999-aaaaaaaaaaaa', 'bob'),
 ('00000000-0000-0000-0000-0000000000AA', 'never_watches');

CREATE TABLE UserData (
  ItemId         TEXT NOT NULL,
  UserId         TEXT NOT NULL,
  CustomDataKey  TEXT NOT NULL DEFAULT '',
  LastPlayedDate TEXT,
  PlayCount      INTEGER NOT NULL DEFAULT 0,
  Played         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (ItemId, UserId, CustomDataKey)
);
INSERT INTO UserData (ItemId, UserId, CustomDataKey, LastPlayedDate, PlayCount, Played) VALUES
 -- Bravo: alice finished it 3x, recently  -> not "never", not "stale"
 ('00000000-0000-0000-0000-00000000000B', '11111111-2222-3333-4444-555555555555', 'k1', '2025-06-01 20:00:00.000', 3, 1),
 -- Charlie: bob watched once, long ago  -> "stale"
 ('00000000-0000-0000-0000-00000000000C', '66666666-7777-8888-9999-aaaaaaaaaaaa', 'k1', '2023-01-05 21:00:00.000', 1, 1),
 -- Delta: two CustomDataKey rows for the same (item,user); MAX must win
 ('00000000-0000-0000-0000-00000000000D', '11111111-2222-3333-4444-555555555555', 'k1', '2025-05-01 10:00:00.000', 1, 0),
 ('00000000-0000-0000-0000-00000000000D', '11111111-2222-3333-4444-555555555555', 'k2', '2025-05-02 10:00:00.000', 2, 1),
 -- S1E1: bob, partial (Played=0 but LastPlayedDate set) -> series "Some Show" is NOT "never"
 ('00000000-0000-0000-0000-0000000000E1', '66666666-7777-8888-9999-aaaaaaaaaaaa', 'k1', '2025-04-10 22:00:00.000', 1, 0),
 -- a UserData row for a user absent from Users (must not crash the join)
 ('00000000-0000-0000-0000-00000000000B', '00000000-0000-0000-0000-0000000000BB', 'k1', '2025-02-02 02:00:00.000', 1, 1);
-- Alpha, S1E2: no UserData at all -> "never watched"
```

- [ ] **Step 2: Write the failing test** — append to `internal/source/file/queries_test.go`

```go
func TestQueryLibrary_PlayedStateAndUsers(t *testing.T) {
	db := openFixture(t) // existing helper: opens testsupport.LibraryFixtureDB
	defer db.Close()

	snap, err := defaultQueryLibrary(db)
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]source.LibraryItem{}
	for _, it := range snap.Items {
		byName[it.Name] = it
	}

	// ID is canonical
	if byName["Alpha"].ID != "0000000000000000000000000000000a" {
		t.Errorf("Alpha.ID = %q", byName["Alpha"].ID)
	}
	// episode carries canonical series linkage
	if byName["S1E1"].SeriesID != "00000000000000000000000000000f0" &&
		byName["S1E1"].SeriesID != "000000000000000000000000000000f0" {
		t.Errorf("S1E1.SeriesID = %q", byName["S1E1"].SeriesID)
	}
	if byName["S1E1"].SeriesName != "Some Show" {
		t.Errorf("S1E1.SeriesName = %q", byName["S1E1"].SeriesName)
	}
	// Alpha: no UserData -> never
	if a := byName["Alpha"]; a.Played || a.PlayCount != 0 || !a.LastPlayedAt.IsZero() {
		t.Errorf("Alpha played-state should be empty: %+v", a)
	}
	// Bravo: played 3x, recent
	if b := byName["Bravo"]; !b.Played || b.PlayCount != 3 || b.LastPlayedAt.IsZero() {
		t.Errorf("Bravo played-state: %+v", b)
	}
	// Delta: MAX across two CustomDataKey rows -> Played true, PlayCount 2 (SUM of per-user MAX = 2)
	if d := byName["Delta"]; !d.Played || d.PlayCount != 2 {
		t.Errorf("Delta played-state: %+v", d)
	}

	// Users: 3 rows, canonical ids
	if len(snap.Users) != 3 {
		t.Fatalf("want 3 users, got %d: %+v", len(snap.Users), snap.Users)
	}
	uname := map[string]string{}
	for _, u := range snap.Users {
		uname[u.ID] = u.Name
	}
	if uname["11111111222233334444555555555555"] != "alice" {
		t.Errorf("user map: %+v", uname)
	}

	// UserPlays: alice has movie plays; bob has a 'series' play for Some Show
	var sawSeries bool
	for _, p := range snap.UserPlays {
		if p.Scope == "series" && p.Name == "Some Show" {
			sawSeries = true
		}
	}
	if !sawSeries {
		t.Errorf("expected a rolled-up series UserPlay for Some Show: %+v", snap.UserPlays)
	}
}
```

> If `openFixture` / `defaultQueryLibrary` helpers differ in the current test file, adapt the call sites — the existing `queries_test.go` already opens the fixture DB and calls `defaultQueryLibrary`.

- [ ] **Step 3: Run it, see it fail** — `go test ./internal/source/file/ -run TestQueryLibrary_PlayedState -v` → FAIL (fields all zero / `snap.Users` nil).

- [ ] **Step 4: Extend `internal/source/file/queries.go`.**

4a. In `libraryQuery`, add three columns to the `SELECT` list (after `i.Path`): `i.Id`, `COALESCE(i.SeriesId,'')`, `COALESCE(i.SeriesName,'')`. Add matching scan targets in `defaultQueryLibrary` and set `it.ID = source.CanonID(rawID)`, `it.SeriesID = source.CanonID(rawSeriesID)` (empty stays empty — `CanonID("")==""`), `it.SeriesName = rawSeriesName`.

4b. Add the three helper queries as package consts and run them in `defaultQueryLibrary` after the item loop:

```go
const playedStateQuery = `
SELECT iid, MAX(played) AS played, SUM(pc) AS play_count, MAX(lpd) AS last_played
FROM (
  SELECT lower(replace(ItemId,'-','')) AS iid,
         lower(replace(UserId,'-','')) AS uid,
         MAX(Played)                   AS played,
         MAX(PlayCount)                AS pc,
         MAX(LastPlayedDate)           AS lpd
  FROM UserData
  GROUP BY iid, uid
)
GROUP BY iid`

const usersQuery = `SELECT lower(replace(Id,'-','')), COALESCE(Username,'') FROM Users`

const userPlaysQuery = `
SELECT uid, scope, pid, MAX(name) AS name, SUM(pc) AS play_count, MAX(lpd) AS last_played
FROM (
  SELECT lower(replace(ud.UserId,'-',''))  AS uid,
         CASE WHEN bi.Type = '` + episodeType + `' THEN 'series' ELSE 'movie' END AS scope,
         lower(replace(
           CASE WHEN bi.Type = '` + episodeType + `' AND COALESCE(bi.SeriesId,'') <> ''
                THEN bi.SeriesId ELSE bi.Id END, '-', '')) AS pid,
         CASE WHEN bi.Type = '` + episodeType + `' AND COALESCE(bi.SeriesName,'') <> ''
              THEN bi.SeriesName ELSE bi.Name END AS name,
         MAX(ud.PlayCount)      AS pc,
         MAX(ud.LastPlayedDate) AS lpd
  FROM UserData ud
  JOIN BaseItems bi ON bi.Id = ud.ItemId
  WHERE bi.Type IN ('` + movieType + `', '` + episodeType + `')
    AND COALESCE(ud.PlayCount,0) > 0
  GROUP BY uid, pid, ud.ItemId
)
GROUP BY uid, pid`
```

```go
// after the item rows.Next() loop, before the series-count query:

playState := map[string]struct {
	played    bool
	playCount int64
	last      time.Time
}{}
prows, err := db.Query(playedStateQuery)
if err != nil {
	return source.LibrarySnapshot{}, err
}
for prows.Next() {
	var iid string
	var played, pc int64
	var lpd sql.NullString
	if err := prows.Scan(&iid, &played, &pc, &lpd); err != nil {
		prows.Close()
		return source.LibrarySnapshot{}, err
	}
	playState[iid] = struct {
		played    bool
		playCount int64
		last      time.Time
	}{played == 1, pc, parseJellyfinTime(lpd.String)}
}
prows.Close()
if err := prows.Err(); err != nil {
	return source.LibrarySnapshot{}, err
}
for i := range snap.Items {
	if ps, ok := playState[snap.Items[i].ID]; ok {
		snap.Items[i].Played = ps.played
		snap.Items[i].PlayCount = int(ps.playCount)
		snap.Items[i].LastPlayedAt = ps.last
	}
}

urows, err := db.Query(usersQuery)
if err != nil {
	return source.LibrarySnapshot{}, err
}
for urows.Next() {
	var u source.UserRef
	if err := urows.Scan(&u.ID, &u.Name); err != nil {
		urows.Close()
		return source.LibrarySnapshot{}, err
	}
	snap.Users = append(snap.Users, u)
}
urows.Close()
if err := urows.Err(); err != nil {
	return source.LibrarySnapshot{}, err
}

pprows, err := db.Query(userPlaysQuery)
if err != nil {
	return source.LibrarySnapshot{}, err
}
for pprows.Next() {
	var p source.UserPlay
	var pc int64
	var lpd sql.NullString
	if err := pprows.Scan(&p.UserID, &p.Scope, &p.ItemID, &p.Name, &pc, &lpd); err != nil {
		pprows.Close()
		return source.LibrarySnapshot{}, err
	}
	p.PlayCount = int(pc)
	p.LastPlayedAt = parseJellyfinTime(lpd.String)
	snap.UserPlays = append(snap.UserPlays, p)
}
pprows.Close()
if err := pprows.Err(); err != nil {
	return source.LibrarySnapshot{}, err
}
```

> `parseJellyfinTime` already exists in this file and already handles the `2006-01-02 15:04:05.9999999` layout `UserData.LastPlayedDate` uses; `parseJellyfinTime("")` returns the zero time.

- [ ] **Step 5: Run it, see it pass** — `go test ./internal/source/file/ -v` → PASS (all, including the untouched Plan-1 query tests).

- [ ] **Step 6: Commit**

```bash
git add internal/source/file/queries.go internal/source/file/queries_test.go testdata/library.fixture.sql
git commit -m "feat(source): read played-state, users, and per-user play rollups from jellyfin.db"
```

---

## Task 4: `aggregate.Cleanup`, `aggregate.Users`, `aggregate.CorePlays`

**Files:**
- Create: `internal/aggregate/timefmt.go`, `internal/aggregate/cleanup.go`, `internal/aggregate/coreplays.go`
- Test: `internal/aggregate/cleanup_test.go`, `internal/aggregate/coreplays_test.go`

**Interfaces:**
- Consumes: `source.LibrarySnapshot`, `source.UserPlay`; the row types from Task 1.
- Produces:
  ```go
  func Cleanup(snap source.LibrarySnapshot) []CleanupRow   // one row per movie or series, sorted by ItemID
  func Users(snap source.LibrarySnapshot) []UserRow        // straight map of snap.Users, sorted by ID
  func CorePlays(plays []source.UserPlay) []CorePlayRow     // top 25 per user, sorted (UserID, -PlayCount, -LastPlayedAt, Name)
  func rfc3339(t time.Time) string                          // "" when zero, else t.UTC().Format(time.RFC3339)
  ```

- [ ] **Step 1: Create `internal/aggregate/timefmt.go`**

```go
package aggregate

import "time"

// rfc3339 renders a timestamp for the store; the zero time becomes "" (which the
// store writes as SQL NULL).
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
```

- [ ] **Step 2: Write the failing test** — `internal/aggregate/cleanup_test.go`

```go
package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func TestCleanup(t *testing.T) {
	mk := func(id, typ, name, lib string, bytes int64, added time.Time) source.LibraryItem {
		return source.LibraryItem{ID: id, Type: typ, Name: name, Library: lib, SizeBytes: bytes, DateCreated: added}
	}
	jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	snap := source.LibrarySnapshot{Items: []source.LibraryItem{
		mk("m_alpha", "movie", "Alpha", "Movies", 8_000, jan),        // never played
		func() source.LibraryItem { // Bravo: played
			it := mk("m_bravo", "movie", "Bravo", "Movies", 4_000, jan)
			it.LastPlayedAt = time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
			return it
		}(),
		func() source.LibraryItem {
			it := mk("e1", "episode", "S1E1", "Shows", 1_200, jan)
			it.SeriesID, it.SeriesName = "s_show", "Some Show"
			it.LastPlayedAt = time.Date(2025, 4, 10, 0, 0, 0, 0, time.UTC)
			return it
		}(),
		func() source.LibraryItem {
			it := mk("e2", "episode", "S1E2", "Shows", 1_300, feb) // newer add; never played
			it.SeriesID, it.SeriesName = "s_show", "Some Show"
			return it
		}(),
	}}

	rows := Cleanup(snap)
	byID := map[string]CleanupRow{}
	for _, r := range rows {
		byID[r.ItemID] = r
	}
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (2 movies + 1 series), got %d: %+v", len(rows), rows)
	}
	if s := byID["s_show"]; s.Scope != "series" || s.Bytes != 2_500 || s.Episodes != 2 ||
		s.Name != "Some Show" || s.Library != "Shows" {
		t.Errorf("series row wrong: %+v", s)
	}
	// series added_at = MAX(episode DateCreated) = feb
	if byID["s_show"].AddedAt != feb.Format(time.RFC3339) {
		t.Errorf("series added_at = %q want %q", byID["s_show"].AddedAt, feb.Format(time.RFC3339))
	}
	// series last_played = MAX over episodes = 2025-04-10 (E2 never played)
	if byID["s_show"].LastPlayedAt != "2025-04-10T00:00:00Z" {
		t.Errorf("series last_played = %q", byID["s_show"].LastPlayedAt)
	}
	if byID["m_alpha"].LastPlayedAt != "" {
		t.Errorf("Alpha should read as never: %q", byID["m_alpha"].LastPlayedAt)
	}
	if byID["m_bravo"].LastPlayedAt != "2025-06-01T00:00:00Z" {
		t.Errorf("Bravo last_played = %q", byID["m_bravo"].LastPlayedAt)
	}
}
```

- [ ] **Step 3: Run it, see it fail** — `go test ./internal/aggregate/ -run TestCleanup -v` → `undefined: Cleanup`.

- [ ] **Step 4: Implement `internal/aggregate/cleanup.go`**

```go
package aggregate

import (
	"sort"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

// Cleanup rolls a library snapshot into one row per movie or series. Episodes
// are grouped into their series; a movie is its own row. Never/stale is decided
// later at read time from last_played_at.
func Cleanup(snap source.LibrarySnapshot) []CleanupRow {
	type sAcc struct {
		name, library    string
		bytes, episodes  int64
		added, lastPlay  time.Time
	}
	var out []CleanupRow
	series := map[string]*sAcc{}

	for _, it := range snap.Items {
		switch it.Type {
		case "movie":
			out = append(out, CleanupRow{
				ItemID: it.ID, Scope: "movie", Name: it.Name, Library: it.Library,
				Bytes: it.SizeBytes, Episodes: 0,
				AddedAt: rfc3339(it.DateCreated), LastPlayedAt: rfc3339(it.LastPlayedAt),
			})
		case "episode":
			key := it.SeriesID
			name := it.SeriesName
			if key == "" { // no series linkage: treat the episode as its own "series" of 1
				key, name = it.ID, it.Name
			}
			a := series[key]
			if a == nil {
				a = &sAcc{name: name, library: it.Library}
				series[key] = a
			}
			a.bytes += it.SizeBytes
			a.episodes++
			if it.DateCreated.After(a.added) {
				a.added = it.DateCreated
			}
			if it.LastPlayedAt.After(a.lastPlay) {
				a.lastPlay = it.LastPlayedAt
			}
		}
	}
	for id, a := range series {
		out = append(out, CleanupRow{
			ItemID: id, Scope: "series", Name: a.name, Library: a.library,
			Bytes: a.bytes, Episodes: a.episodes,
			AddedAt: rfc3339(a.added), LastPlayedAt: rfc3339(a.lastPlay),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ItemID < out[j].ItemID })
	return out
}

// Users is a straight projection of the snapshot's user directory, sorted by id
// for a stable write order.
func Users(snap source.LibrarySnapshot) []UserRow {
	out := make([]UserRow, 0, len(snap.Users))
	for _, u := range snap.Users {
		out = append(out, UserRow{ID: u.ID, Name: u.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
```

- [ ] **Step 5: Write the failing test** — `internal/aggregate/coreplays_test.go`

```go
package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func TestCorePlays_TopNPerUserAndOrder(t *testing.T) {
	now := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	var plays []source.UserPlay
	// user A: 30 movies with descending play counts
	for i := 0; i < 30; i++ {
		plays = append(plays, source.UserPlay{
			UserID: "a", Scope: "movie", ItemID: string(rune('A' + i)), Name: "M" + string(rune('a'+i)),
			PlayCount: int64(30 - i), LastPlayedAt: now,
		})
	}
	// user B: one series
	plays = append(plays, source.UserPlay{UserID: "b", Scope: "series", ItemID: "s1", Name: "Show", PlayCount: 5, LastPlayedAt: now})

	rows := CorePlays(plays)

	var a, b int
	for _, r := range rows {
		switch r.UserID {
		case "a":
			a++
		case "b":
			b++
		}
	}
	if a != 25 {
		t.Fatalf("user a capped at 25, got %d", a)
	}
	if b != 1 {
		t.Fatalf("user b = %d", b)
	}
	// user a's first row is the highest play_count (30)
	for _, r := range rows {
		if r.UserID == "a" {
			if r.PlayCount != 30 {
				t.Fatalf("first a row play_count = %d, want 30", r.PlayCount)
			}
			break
		}
	}
}
```

- [ ] **Step 6: Run it, see it fail** — `undefined: CorePlays`.

- [ ] **Step 7: Implement `internal/aggregate/coreplays.go`**

```go
package aggregate

import (
	"sort"

	"github.com/andriykohut/ephyra/internal/source"
)

const corePlaysPerUser = 25

// CorePlays projects per-user play rollups into store rows, keeping the top
// corePlaysPerUser titles per user by play count.
func CorePlays(plays []source.UserPlay) []CorePlayRow {
	byUser := map[string][]CorePlayRow{}
	for _, p := range plays {
		byUser[p.UserID] = append(byUser[p.UserID], CorePlayRow{
			UserID: p.UserID, Scope: p.Scope, ItemID: p.ItemID, Name: p.Name,
			PlayCount: int64(p.PlayCount), LastPlayedAt: rfc3339(p.LastPlayedAt),
		})
	}
	users := make([]string, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	sort.Strings(users)

	var out []CorePlayRow
	for _, u := range users {
		rows := byUser[u]
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].PlayCount != rows[j].PlayCount {
				return rows[i].PlayCount > rows[j].PlayCount
			}
			if rows[i].LastPlayedAt != rows[j].LastPlayedAt {
				return rows[i].LastPlayedAt > rows[j].LastPlayedAt
			}
			return rows[i].Name < rows[j].Name
		})
		if len(rows) > corePlaysPerUser {
			rows = rows[:corePlaysPerUser]
		}
		out = append(out, rows...)
	}
	return out
}
```

- [ ] **Step 8: Run the aggregate suite** — `go test ./internal/aggregate/ -v` → PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/aggregate/timefmt.go internal/aggregate/cleanup.go internal/aggregate/coreplays.go internal/aggregate/cleanup_test.go internal/aggregate/coreplays_test.go
git commit -m "feat(aggregate): Cleanup, Users, CorePlays"
```

---

## Task 5: Wire cleanup / users / core-plays into the library job

**Files:**
- Modify: `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `aggregate.Cleanup/Users/CorePlays`, the grown `store.WriteLibraryAggregates` (Task 1).
- Produces: after a successful `library` refresh, `agg_cleanup`, `dim_user`, `agg_played_core` are populated. No API change yet.

- [ ] **Step 1: Write the failing test** — append to `internal/scheduler/scheduler_test.go`

Use the existing scheduler test harness (it already builds a `FileSource` against `testsupport.LibraryFixtureDB` and a real `store`). Add:

```go
func TestRunLibraryOnce_PopulatesCleanupAndUsers(t *testing.T) {
	sc, st := newLibrarySchedulerFixture(t) // existing helper in this file
	if err := sc.RunLibraryOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var cleanup, users, core int
	st.DB().QueryRow(`SELECT count(*) FROM agg_cleanup`).Scan(&cleanup)
	st.DB().QueryRow(`SELECT count(*) FROM dim_user`).Scan(&users)
	st.DB().QueryRow(`SELECT count(*) FROM agg_played_core`).Scan(&core)
	if cleanup == 0 || users != 3 || core == 0 {
		t.Fatalf("cleanup=%d users=%d core=%d (want >0 / 3 / >0)", cleanup, users, core)
	}
	// a series row exists
	var scope string
	if err := st.DB().QueryRow(`SELECT scope FROM agg_cleanup WHERE scope='series' LIMIT 1`).Scan(&scope); err != nil {
		t.Fatalf("no series row in agg_cleanup: %v", err)
	}
}
```

> If the file has no `newLibrarySchedulerFixture` helper, factor one out of the existing `TestRunLibraryOnce*` setup: it returns `(*Scheduler, *store.Store)` with the file source pointed at the fixture DB laid out as `<tmp>/data/jellyfin.db`.

- [ ] **Step 2: Run it, see it fail** — `agg_cleanup` count is 0 (scheduler still passes `nil` slices).

- [ ] **Step 3: Update `RunLibraryOnce` in `internal/scheduler/scheduler.go`**

```go
	snap, err := s.src.LibraryFacts(ctx)
	if err != nil {
		return s.recordFailure(ctx, "library", mt, start, err)
	}
	agg := aggregate.Library(snap, time.Local)
	cleanup := aggregate.Cleanup(snap)
	users := aggregate.Users(snap)
	core := aggregate.CorePlays(snap.UserPlays)
	if err := s.st.WriteLibraryAggregates(ctx, agg, cleanup, users, core); err != nil {
		return s.recordFailure(ctx, "library", mt, start, err)
	}
```

- [ ] **Step 4: Generalize `recordFailure`** (needed by Task 10's `watch` path) — change the signature to `recordFailure(ctx context.Context, job string, mt, start time.Time, cause error) error` and use `job` in the `RefreshMeta`. Update the one existing call site in `RunLibraryOnce` (Step 3 already passes `"library"`).

- [ ] **Step 5: Run the scheduler suite** — `go test ./internal/scheduler/ -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/scheduler
git commit -m "feat(scheduler): library job writes cleanup, users, core play counts"
```

---

## Task 6: `store.ReadCleanup` + `GET /api/cleanup`

**Files:**
- Create: `internal/store/reads_cleanup.go`, `internal/store/reads_cleanup_test.go`
- Create: `internal/api/cleanup.go`, `internal/api/cleanup_test.go`
- Modify: `internal/api/api.go` (register the route)

**Interfaces:**
- Produces:
  ```go
  // internal/store
  type CleanupParams struct {
      Mode  string    // "never" | "stale"
      Sort  string    // "size" | "added"
      Limit int       // page cap
      Now   time.Time // for the stale cutoff
  }
  type CleanupItem struct {
      ItemID, Scope, Name, Library string
      Bytes, Episodes              int64
      AddedAt                      string
      LastPlayedAt                 *string // nil = never
  }
  type CleanupResult struct {
      Mode             string
      ReclaimableBytes int64
      MatchCount       int64
      Truncated        bool
      Items            []CleanupItem
  }
  func (s *Store) ReadCleanup(ctx context.Context, p CleanupParams) (CleanupResult, error)
  // CSV streaming: same filter, no limit.
  func (s *Store) StreamCleanupCSV(ctx context.Context, mode string, now time.Time, w io.Writer) error
  ```

- [ ] **Step 1: Write the failing test** — `internal/store/reads_cleanup_test.go`

```go
package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func seedCleanup(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := []aggregate.CleanupRow{
		{ItemID: "a", Scope: "movie", Name: "Big Never", Library: "Movies", Bytes: 900, AddedAt: "2020-01-01T00:00:00Z"},
		{ItemID: "b", Scope: "movie", Name: "Small Never", Library: "Movies", Bytes: 100, AddedAt: "2024-01-01T00:00:00Z"},
		{ItemID: "c", Scope: "series", Name: "Stale Show", Library: "Shows", Bytes: 500, Episodes: 12,
			AddedAt: "2019-01-01T00:00:00Z", LastPlayedAt: "2023-06-01T00:00:00Z"},
		{ItemID: "d", Scope: "movie", Name: "Recent", Library: "Movies", Bytes: 700,
			AddedAt: "2025-01-01T00:00:00Z", LastPlayedAt: "2025-12-01T00:00:00Z"},
	}
	if err := s.WriteLibraryAggregates(ctx, aggregate.LibraryAggregates{Totals: map[string]float64{}}, rows, nil, nil); err != nil {
		t.Fatal(err)
	}
	return s, now
}

func TestReadCleanup_NeverAndStale(t *testing.T) {
	s, now := seedCleanup(t)
	ctx := context.Background()

	never, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if never.MatchCount != 2 || never.ReclaimableBytes != 1000 {
		t.Fatalf("never: count=%d bytes=%d", never.MatchCount, never.ReclaimableBytes)
	}
	if never.Items[0].ItemID != "a" { // size desc
		t.Fatalf("never sort=size: first=%s", never.Items[0].ItemID)
	}
	if never.Items[0].LastPlayedAt != nil {
		t.Fatalf("never item should have nil last_played_at")
	}

	stale, err := s.ReadCleanup(ctx, CleanupParams{Mode: "stale", Sort: "added", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if stale.MatchCount != 1 || stale.Items[0].ItemID != "c" {
		t.Fatalf("stale: %+v", stale)
	}
	if *stale.Items[0].LastPlayedAt != "2023-06-01T00:00:00Z" {
		t.Fatalf("stale last_played = %v", *stale.Items[0].LastPlayedAt)
	}

	// limit -> truncated
	lim, _ := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 1, Now: now})
	if !lim.Truncated || len(lim.Items) != 1 || lim.MatchCount != 2 || lim.ReclaimableBytes != 1000 {
		t.Fatalf("limit: %+v", lim)
	}

	// CSV: all matching rows, header + 2 data lines for never
	var b strings.Builder
	if err := s.StreamCleanupCSV(ctx, "never", now, &b); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "item_id,scope,name,library,bytes,episodes,added_at,last_played_at") {
		t.Fatalf("csv:\n%s", b.String())
	}
}
```

- [ ] **Step 2: Run it, see it fail** — `undefined: (*Store).ReadCleanup`.

- [ ] **Step 3: Implement `internal/store/reads_cleanup.go`**

```go
package store

import (
	"context"
	"encoding/csv"
	"io"
	"strconv"
	"time"
)

type CleanupParams struct {
	Mode  string
	Sort  string
	Limit int
	Now   time.Time
}

type CleanupItem struct {
	ItemID       string  `json:"item_id"`
	Scope        string  `json:"scope"`
	Name         string  `json:"name"`
	Library      string  `json:"library"`
	Bytes        int64   `json:"bytes"`
	Episodes     int64   `json:"episodes"`
	AddedAt      string  `json:"added_at"`
	LastPlayedAt *string `json:"last_played_at"`
}

type CleanupResult struct {
	Mode             string        `json:"mode"`
	ReclaimableBytes int64         `json:"reclaimable_bytes"`
	MatchCount       int64         `json:"match_count"`
	Truncated        bool          `json:"truncated"`
	Items            []CleanupItem `json:"items"`
}

// where returns the SQL predicate + args for a mode. staleCutoff is now-365d in
// RFC3339 so it compares lexically against the stored last_played_at.
func cleanupWhere(mode string, now time.Time) (string, []any) {
	switch mode {
	case "stale":
		cut := now.AddDate(-1, 0, 0).UTC().Format(time.RFC3339)
		return `last_played_at IS NOT NULL AND last_played_at < ?`, []any{cut}
	default: // "never"
		return `last_played_at IS NULL`, nil
	}
}

func (s *Store) ReadCleanup(ctx context.Context, p CleanupParams) (CleanupResult, error) {
	where, args := cleanupWhere(p.Mode, p.Now)
	res := CleanupResult{Mode: p.Mode}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(bytes),0) FROM agg_cleanup WHERE `+where, args...,
	).Scan(&res.MatchCount, &res.ReclaimableBytes); err != nil {
		return res, err
	}

	order := `bytes DESC, name ASC`
	if p.Sort == "added" {
		order = `added_at ASC, name ASC`
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, scope, name, library, bytes, episodes, added_at, last_played_at
		   FROM agg_cleanup WHERE `+where+` ORDER BY `+order+` LIMIT ?`,
		append(args, limit)...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	for rows.Next() {
		var it CleanupItem
		var last *string
		if err := rows.Scan(&it.ItemID, &it.Scope, &it.Name, &it.Library, &it.Bytes, &it.Episodes, &it.AddedAt, &last); err != nil {
			return res, err
		}
		it.LastPlayedAt = last
		res.Items = append(res.Items, it)
	}
	res.Truncated = res.MatchCount > int64(len(res.Items))
	return res, rows.Err()
}

func (s *Store) StreamCleanupCSV(ctx context.Context, mode string, now time.Time, w io.Writer) error {
	where, args := cleanupWhere(mode, now)
	rows, err := s.db.QueryContext(ctx,
		`SELECT item_id, scope, name, library, bytes, episodes, added_at, last_played_at
		   FROM agg_cleanup WHERE `+where+` ORDER BY bytes DESC, name ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"item_id", "scope", "name", "library", "bytes", "episodes", "added_at", "last_played_at"}); err != nil {
		return err
	}
	for rows.Next() {
		var id, scope, name, lib, added string
		var bytes, eps int64
		var last *string
		if err := rows.Scan(&id, &scope, &name, &lib, &bytes, &eps, &added, &last); err != nil {
			return err
		}
		lp := ""
		if last != nil {
			lp = *last
		}
		if err := cw.Write([]string{id, scope, name, lib, strconv.FormatInt(bytes, 10), strconv.FormatInt(eps, 10), added, lp}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}
```

- [ ] **Step 4: Run the store test** — `go test ./internal/store/ -run TestReadCleanup -v` → PASS.

- [ ] **Step 5: Write the failing API test** — `internal/api/cleanup_test.go`

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/store"
)

func seedCleanupAPI(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	rows := []aggregate.CleanupRow{
		{ItemID: "a", Scope: "movie", Name: "Big Never", Library: "Movies", Bytes: 900, AddedAt: "2020-01-01T00:00:00Z"},
		{ItemID: "c", Scope: "series", Name: "Stale Show", Library: "Shows", Bytes: 500, Episodes: 12,
			AddedAt: "2019-01-01T00:00:00Z", LastPlayedAt: "2023-06-01T00:00:00Z"},
	}
	if err := st.WriteLibraryAggregates(context.Background(),
		aggregate.LibraryAggregates{Totals: map[string]float64{}}, rows, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(context.Background(), store.RefreshMeta{
		Job: "library", LastRunAt: now.Add(-time.Minute), OK: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCleanup_JSON(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedCleanupAPI(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=never", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data store.CleanupResult `json:"data"`
		Meta Meta                `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Mode != "never" || env.Data.MatchCount != 1 || env.Data.ReclaimableBytes != 900 {
		t.Fatalf("bad data: %+v", env.Data)
	}
}

func TestCleanup_CSV(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedCleanupAPI(t, st, now)

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup?mode=stale&format=csv", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("content-type %q", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd != `attachment; filename="ephyra-cleanup-stale.csv"` {
		t.Fatalf("disposition %q", cd)
	}
	if !strings.Contains(rr.Body.String(), "Stale Show") {
		t.Fatalf("csv body:\n%s", rr.Body.String())
	}
}

func TestCleanup_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/cleanup", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
}
```

> Add `"strings"` to the test imports.

- [ ] **Step 6: Run it, see it fail** — 404 (route not registered).

- [ ] **Step 7: Implement `internal/api/cleanup.go`**

```go
package api

import (
	"net/http"
	"strconv"

	"github.com/andriykohut/ephyra/internal/store"
)

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	mode := q.Get("mode")
	if mode == "" {
		mode = "never"
	}
	if mode != "never" && mode != "stale" {
		writeError(w, http.StatusBadRequest, "bad_request", "mode must be never|stale")
		return
	}
	sortBy := q.Get("sort")
	if sortBy == "" {
		sortBy = "size"
	}
	if sortBy != "size" && sortBy != "added" {
		writeError(w, http.StatusBadRequest, "bad_request", "sort must be size|added")
		return
	}
	limit := 200
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1..1000")
			return
		}
		limit = n
	}

	m, ok, err := s.st.GetRefreshMeta(ctx, "library")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first library refresh has not completed")
		return
	}

	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ephyra-cleanup-`+mode+`.csv"`)
		if err := s.st.StreamCleanupCSV(ctx, mode, s.now(), w); err != nil {
			s.log.Error("cleanup csv", "err", err)
		}
		return
	}

	res, err := s.st.ReadCleanup(ctx, store.CleanupParams{Mode: mode, Sort: sortBy, Limit: limit, Now: s.now()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	stale := store.IsStale(m, ok, s.cfg.RefreshLibrary, s.now())
	writeJSON(w, http.StatusOK, res, s.meta(stale))
}
```

- [ ] **Step 8: Register the route** in `internal/api/api.go` `Handler()`:

```go
	mux.HandleFunc("GET /api/cleanup", s.handleCleanup)
```

- [ ] **Step 9: Run the api suite** — `go test ./internal/api/ -v` → PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/store/reads_cleanup.go internal/store/reads_cleanup_test.go internal/api/cleanup.go internal/api/cleanup_test.go internal/api/api.go
git commit -m "feat(api): GET /api/cleanup (json + csv)"
```

---

## Task 7: Frontend — `SegmentedControl`, `DataTable`, and the `/cleanup` page

**Files:**
- Create: `web/src/components/SegmentedControl.tsx`, `web/src/components/DataTable.tsx`
- Modify: `web/src/api/types.ts`, `web/src/api/queries.ts`
- Replace: `web/src/routes/cleanup.tsx`
- Create: `web/src/routes/cleanup.test.tsx`

**Interfaces:**
- Produces:
  ```ts
  // components/SegmentedControl.tsx
  export function SegmentedControl<T extends string>(props: {
    value: T; options: { value: T; label: string }[]; onChange: (v: T) => void; ariaLabel: string;
  }): JSX.Element

  // components/DataTable.tsx
  export interface Column<Row> { key: string; header: string; cell: (r: Row) => ReactNode; align?: "left" | "right"; }
  export function DataTable<Row>(props: { columns: Column<Row>[]; rows: Row[]; getKey: (r: Row) => string; empty?: ReactNode }): JSX.Element

  // api/types.ts
  export interface CleanupItem {
    item_id: string; scope: "movie" | "series" | "episode"; name: string; library: string;
    bytes: number; episodes: number; added_at: string; last_played_at: string | null;
  }
  export interface Cleanup {
    mode: "never" | "stale"; reclaimable_bytes: number; match_count: number; truncated: boolean; items: CleanupItem[];
  }

  // api/queries.ts
  export const cleanupQuery: (mode: "never" | "stale", sort: "size" | "added") => UseQueryOptions<Envelope<Cleanup>>
  ```

- [ ] **Step 1: `web/src/components/SegmentedControl.tsx`**

```tsx
import { cn } from "@/lib/utils";

export function SegmentedControl<T extends string>({
  value,
  options,
  onChange,
  ariaLabel,
}: {
  value: T;
  options: { value: T; label: string }[];
  onChange: (v: T) => void;
  ariaLabel: string;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      className="inline-flex rounded-[10px] border border-line bg-panel/60 p-0.5"
    >
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          onClick={() => onChange(o.value)}
          className={cn(
            "rounded-[8px] px-3 py-1.5 text-[12.5px] font-medium transition-colors",
            o.value === value ? "bg-cyan/15 text-cyan" : "text-muted hover:text-ink",
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}
```

- [ ] **Step 2: `web/src/components/DataTable.tsx`**

```tsx
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export interface Column<Row> {
  key: string;
  header: string;
  cell: (r: Row) => ReactNode;
  align?: "left" | "right";
}

export function DataTable<Row>({
  columns,
  rows,
  getKey,
  empty,
}: {
  columns: Column<Row>[];
  rows: Row[];
  getKey: (r: Row) => string;
  empty?: ReactNode;
}) {
  if (rows.length === 0 && empty) {
    return <div className="px-4 py-8 text-center text-[13px] text-muted">{empty}</div>;
  }
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-[13px]">
        <thead>
          <tr className="border-b border-line text-[11px] uppercase tracking-[0.12em] text-muted">
            {columns.map((c) => (
              <th
                key={c.key}
                className={cn("px-3 py-2 font-semibold", c.align === "right" ? "text-right" : "text-left")}
              >
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={getKey(r)} className="border-b border-line/50 last:border-0">
              {columns.map((c) => (
                <td
                  key={c.key}
                  className={cn(
                    "px-3 py-2.5 tabular-nums text-ink",
                    c.align === "right" ? "text-right" : "text-left",
                  )}
                >
                  {c.cell(r)}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
```

- [ ] **Step 3: Add DTOs to `web/src/api/types.ts`** — append `CleanupItem` and `Cleanup` from the Interfaces block.

- [ ] **Step 4: Add the query factory to `web/src/api/queries.ts`**

```ts
import type { Cleanup } from "./types";

export const cleanupQuery = (mode: "never" | "stale", sort: "size" | "added") =>
  queryOptions({
    queryKey: ["cleanup", mode, sort],
    queryFn: () => fetchEnvelope<Cleanup>(`/api/cleanup?mode=${mode}&sort=${sort}`),
    staleTime: 30 * 60 * 1000,
  });
```

- [ ] **Step 5: Write the failing test** — `web/src/routes/cleanup.test.tsx`

```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Cleanup, Envelope } from "@/api/types";

const { CleanupPage } = await import("./cleanup");

const sample: Envelope<Cleanup> = {
  data: {
    mode: "never",
    reclaimable_bytes: 3_000_000_000,
    match_count: 2,
    truncated: false,
    items: [
      { item_id: "s1", scope: "series", name: "Old Show", library: "Shows", bytes: 2_000_000_000, episodes: 24, added_at: "2020-01-01T00:00:00Z", last_played_at: null },
      { item_id: "m1", scope: "movie", name: "Unseen Film", library: "Movies", bytes: 1_000_000_000, episodes: 0, added_at: "2021-01-01T00:00:00Z", last_played_at: null },
    ],
  },
  meta: { generated_at: new Date().toISOString(), stale: false },
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}
afterEach(() => vi.restoreAllMocks());

test("renders the reclaimable header and the candidate rows", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(sample), { status: 200 }));
  render(wrap(<CleanupPage />));
  await waitFor(() => expect(screen.getByText(/Old Show/)).toBeInTheDocument());
  expect(screen.getByText(/24 eps/)).toBeInTheDocument();
  expect(screen.getByText(/never deletes/i)).toBeInTheDocument();
  expect(screen.getByText("3.0 GB")).toBeInTheDocument();
  const csv = screen.getByRole("link", { name: /export csv/i });
  expect(csv).toHaveAttribute("href", expect.stringContaining("format=csv"));
  expect(csv).toHaveAttribute("href", expect.stringContaining("mode=never"));
});

test("empty state", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ ...sample, data: { ...sample.data, items: [], match_count: 0, reclaimable_bytes: 0 } }), { status: 200 }),
  );
  render(wrap(<CleanupPage />));
  await waitFor(() => expect(screen.getByText(/it's all been watched/i)).toBeInTheDocument());
});
```

- [ ] **Step 6: Run it, see it fail** — `npx vitest run src/routes/cleanup.test.tsx` → module has no `CleanupPage` export.

- [ ] **Step 7: Replace `web/src/routes/cleanup.tsx`**

```tsx
import { useQuery } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";
import { cleanupQuery } from "@/api/queries";
import type { CleanupItem } from "@/api/types";
import { type Column, DataTable } from "@/components/DataTable";
import { Panel } from "@/components/Panel";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { fmtBytes } from "@/lib/format";
import { Route as rootRoute } from "./__root";

type Mode = "never" | "stale";
type Sort = "size" | "added";

const search = (s: Record<string, unknown>): { mode: Mode; sort: Sort } => ({
  mode: s.mode === "stale" ? "stale" : "never",
  sort: s.sort === "added" ? "added" : "size",
});

function fmtDate(iso: string): string {
  return iso ? iso.slice(0, 10) : "—";
}

const columns: Column<CleanupItem>[] = [
  {
    key: "name",
    header: "title",
    cell: (r) => (
      <span>
        <span className="text-muted">{r.scope === "series" ? "▤ " : "▸ "}</span>
        {r.name}
        {r.scope === "series" && <span className="text-muted"> · {r.episodes} eps</span>}
      </span>
    ),
  },
  { key: "library", header: "library", cell: (r) => <span className="text-muted">{r.library}</span> },
  { key: "bytes", header: "size", align: "right", cell: (r) => fmtBytes(r.bytes) },
  { key: "added", header: "added", align: "right", cell: (r) => <span className="text-muted">{fmtDate(r.added_at)}</span> },
  {
    key: "last",
    header: "last played",
    align: "right",
    cell: (r) => <span className="text-muted">{r.last_played_at ? fmtDate(r.last_played_at) : "—"}</span>,
  },
];

export function CleanupPage() {
  const { mode, sort } = Route.useSearch();
  const nav = Route.useNavigate();
  const q = useQuery(cleanupQuery(mode, sort));

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-baseline justify-between gap-4">
        <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">Cleanup</h1>
      </div>

      <Panel className="p-5">
        <div className="font-mono text-[28px] font-semibold tabular-nums text-cyan">
          {q.data ? fmtBytes(q.data.data.reclaimable_bytes) : "—"}
        </div>
        <p className="mt-1 text-[12.5px] text-muted">
          reclaimable · Ephyra never deletes anything — this is a shortlist you act on in Jellyfin
        </p>
      </Panel>

      <div className="flex flex-wrap items-center gap-3">
        <SegmentedControl
          ariaLabel="watched filter"
          value={mode}
          onChange={(v) => nav({ search: (s) => ({ ...s, mode: v }) })}
          options={[
            { value: "never", label: "Never watched" },
            { value: "stale", label: "Watched, then untouched 1y+" },
          ]}
        />
        <SegmentedControl
          ariaLabel="sort"
          value={sort}
          onChange={(v) => nav({ search: (s) => ({ ...s, sort: v }) })}
          options={[
            { value: "size", label: "Biggest" },
            { value: "added", label: "Longest resident" },
          ]}
        />
        <a
          className="rounded-[10px] border border-line px-3 py-1.5 text-[12.5px] text-ink hover:border-cyan hover:text-cyan"
          href={`/api/cleanup?mode=${mode}&sort=${sort}&format=csv`}
        >
          Export CSV
        </a>
      </div>

      {q.data?.meta.stale && <StaleBanner />}

      {q.isPending ? (
        <Skeleton className="h-[320px] w-full" />
      ) : q.isError ? (
        <Panel className="p-5">
          <p className="text-[13px] text-muted">{(q.error as Error).message}</p>
          <button
            type="button"
            onClick={() => q.refetch()}
            className="mt-3 rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
          >
            Try again
          </button>
        </Panel>
      ) : (
        <Panel className="p-1.5">
          <DataTable
            columns={columns}
            rows={q.data.data.items}
            getKey={(r) => r.item_id}
            empty={mode === "never" ? "Nothing matches — it's all been watched." : "Nothing's gone stale."}
          />
          {q.data.data.truncated && (
            <div className="px-3 py-2 text-[12px] text-muted">
              Showing {q.data.data.items.length} of {q.data.data.match_count} — export CSV for the full list.
            </div>
          )}
        </Panel>
      )}
    </div>
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/cleanup",
  validateSearch: search,
  component: CleanupPage,
});
```

- [ ] **Step 8: Run it, see it pass** — `npx vitest run src/routes/cleanup.test.tsx` → PASS. Then `npx tsc -b` and `npx biome ci .` → clean.

- [ ] **Step 9: Commit**

```bash
cd .. && git add web/src && git commit -m "feat(web): Cleanup page + SegmentedControl + DataTable"
```

---

## Task 8: `FileSource.PlaybackEvents` — read the plugin DB + enrich from jellyfin.db

**Files:**
- Create: `testdata/playback_reporting.fixture.sql`
- Modify: `internal/testsupport/fixtures.go`, `internal/testsupport/fixtures_test.go`
- Create: `internal/source/file/playback.go`
- Modify: `internal/source/file/file.go`, `internal/source/file/file_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent`, `source.ErrPluginUnavailable`, `CanonID`, `parseJellyfinTime`.
- Produces:
  ```go
  // internal/testsupport
  func PlaybackFixtureDB(t testing.TB) string          // builds testdata/playback_reporting.fixture.sql
  func TwoDBLayout(t testing.TB) string                 // returns a dir with data/jellyfin.db + data/playback_reporting.db from both fixtures

  // internal/source/file
  func (f *FileSource) PlaybackEvents(ctx context.Context, since time.Time) ([]source.PlaybackEvent, error)
  //   plugin file absent OR PlaybackActivity missing -> (nil, source.ErrPluginUnavailable)
  //   otherwise all rows, enriched with UserName / current Name / SeriesID / SeriesName where jellyfin.db resolves them
  ```

- [ ] **Step 1: Create `testdata/playback_reporting.fixture.sql`** — real plugin schema; ids are the dashless-lowercase forms of the library fixture's ids.

```sql
CREATE TABLE PlaybackActivity (
  DateCreated    DATETIME NOT NULL,
  UserId         TEXT,
  ItemId         TEXT,
  ItemType       TEXT,
  ItemName       TEXT,
  PlaybackMethod TEXT,
  ClientName     TEXT,
  DeviceName     TEXT,
  PlayDuration   INT
);
CREATE TABLE UserList (UserId TEXT);

-- alice = 11111111222233334444555555555555 ; bob = 66666666777788889999aaaaaaaaaaaa
-- Bravo movie id = 0000000000000000000000000000000b ; Alpha = ...000a ; Delta = ...000d
-- S1E1 = 000000000000000000000000000000e1 ; S1E2 = 000000000000000000000000000000e2
INSERT INTO PlaybackActivity (DateCreated, UserId, ItemId, ItemType, ItemName, PlaybackMethod, ClientName, DeviceName, PlayDuration) VALUES
 ('2025-01-06 20:10:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000b', 'Movie',   'Bravo', 'DirectPlay',                 'Jellyfin Media Player', 'Air',  3600),
 ('2025-01-07 21:30:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000b', 'Movie',   'Bravo', 'Transcode (v:h264 a:aac)',   'Jellyfin Web',          'FF',   1800),
 ('2025-01-08 00:20:00.000', '66666666777788889999aaaaaaaaaaaa', '000000000000000000000000000000e1', 'Episode', 'Some Show - s01e01 - Pilot', 'DirectPlay', 'JMP', 'TV', 1200),
 ('2025-01-08 23:50:00.000', '66666666777788889999aaaaaaaaaaaa', '000000000000000000000000000000e2', 'Episode', 'Some Show - s01e02 - Two',   'Transcode (v:direct a:aac)', 'JMP', 'TV', 1500),
 ('2025-01-09 12:00:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000a', 'Movie',   'Alpha', 'Transcode (v:direct a:direct)', 'JMP', 'Air', 600),
 ('2025-01-09 12:30:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000a', 'Movie',   'Alpha', 'DirectPlay',                'JMP', 'Air', 0),
 ('2025-01-10 19:00:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000d', 'Movie',   'Delta', 'Transcode (garbled)',       'JMP', 'Air', 700),
 ('2025-01-11 19:00:00.000', '11111111222233334444555555555555', 'deadbeefdeadbeefdeadbeefdeadbeef', 'Movie',   'Ghost Movie (deleted)', 'DirectPlay', 'JMP', 'Air', 900);
```

- [ ] **Step 2: Add fixture helpers to `internal/testsupport/fixtures.go`**

```go
// PlaybackFixtureDB builds a fresh playback_reporting.db from
// testdata/playback_reporting.fixture.sql and returns its path.
func PlaybackFixtureDB(t testing.TB) string {
	t.Helper()
	return buildFixture(t, "playback_reporting.fixture.sql")
}

// TwoDBLayout writes both fixtures into <tmp>/data/ as jellyfin.db and
// playback_reporting.db and returns <tmp> (a JELLYFIN_DATA_DIR).
func TwoDBLayout(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, dst := range map[string]string{
		"library.fixture.sql":            "jellyfin.db",
		"playback_reporting.fixture.sql": "playback_reporting.db",
	} {
		src, err := os.ReadFile(fixturePath(t, name))
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dataDir, dst))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(src)); err != nil {
			db.Close()
			t.Fatal(err)
		}
		db.Close()
	}
	return root
}
```

> Add `"os"` and `"path/filepath"` to the imports if not already present.

- [ ] **Step 3: Write the failing test** — append to `internal/source/file/file_test.go`

```go
func TestPlaybackEvents_ReadsAndEnriches(t *testing.T) {
	dataDir := testsupport.TwoDBLayout(t)
	f := newFS(t, dataDir)

	events, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("want 8 events, got %d", len(events))
	}

	byRaw := map[string][]source.PlaybackEvent{}
	for _, e := range events {
		byRaw[e.Method] = append(byRaw[e.Method], e)
	}

	// user name resolved from jellyfin.db
	var sawBob bool
	for _, e := range events {
		if e.UserID == "66666666777788889999aaaaaaaaaaaa" {
			if e.UserName != "bob" {
				t.Errorf("bob not resolved: %q", e.UserName)
			}
			sawBob = true
		}
	}
	if !sawBob {
		t.Error("no bob events")
	}

	// episode enriched with current series name
	for _, e := range events {
		if e.ItemType == "episode" && e.ItemID == "000000000000000000000000000000e1" {
			if e.SeriesName != "Some Show" || e.SeriesID == "" {
				t.Errorf("E1 series not resolved: %+v", e)
			}
		}
	}
	// deleted item keeps the plugin's name, no series
	for _, e := range events {
		if e.ItemID == "deadbeefdeadbeefdeadbeefdeadbeef" {
			if e.ItemName != "Ghost Movie (deleted)" || e.SeriesID != "" {
				t.Errorf("deleted item fallback wrong: %+v", e)
			}
		}
	}
	// At carries the literal wall-clock components (no zone shift)
	for _, e := range events {
		if e.ItemName == "Some Show - s01e02 - Two" {
			if e.At.Hour() != 23 || e.At.Day() != 8 {
				t.Errorf("At not literal: %v", e.At)
			}
		}
	}
}

func TestPlaybackEvents_PluginAbsent(t *testing.T) {
	// jellyfin.db only, no playback_reporting.db
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "data", "jellyfin.db"), "")
	f := newFS(t, dir)
	_, err := f.PlaybackEvents(context.Background(), time.Time{})
	if !errors.Is(err, source.ErrPluginUnavailable) {
		t.Fatalf("want ErrPluginUnavailable, got %v", err)
	}
}
```

> Add `"errors"` and the `testsupport` import to the test file.

- [ ] **Step 4: Run it, see it fail** — `PlaybackEvents` currently returns `source.ErrNotImplemented`.

- [ ] **Step 5: Implement `internal/source/file/playback.go`**

```go
package file

import (
	"database/sql"
	"strings"

	"github.com/andriykohut/ephyra/internal/source"
)

const playbackQuery = `
SELECT DateCreated,
       COALESCE(UserId,''), COALESCE(ItemId,''),
       COALESCE(ItemType,''), COALESCE(ItemName,''),
       COALESCE(PlaybackMethod,''), COALESCE(PlayDuration,0)
FROM PlaybackActivity
WHERE ItemType IN ('Movie','Episode')`

func queryPlaybackEvents(db *sql.DB) ([]source.PlaybackEvent, error) {
	rows, err := db.Query(playbackQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []source.PlaybackEvent
	for rows.Next() {
		var dateRaw, userID, itemID, itemType, itemName, method string
		var dur int64
		if err := rows.Scan(&dateRaw, &userID, &itemID, &itemType, &itemName, &method, &dur); err != nil {
			return nil, err
		}
		scope := "movie"
		if strings.EqualFold(itemType, "episode") {
			scope = "episode"
		}
		out = append(out, source.PlaybackEvent{
			At:              parseJellyfinTime(dateRaw),
			UserID:          source.CanonID(userID),
			ItemID:          source.CanonID(itemID),
			ItemName:        itemName,
			ItemType:        scope,
			Method:          method,
			PlayDurationSec: dur,
		})
	}
	return out, rows.Err()
}

func tableExists(db *sql.DB, name string) bool {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// enrichPlaybackEvents fills UserName / ItemName / SeriesID / SeriesName from a
// jellyfin.db copy. Best effort: rows it can't resolve keep their plugin values.
func enrichPlaybackEvents(jdb *sql.DB, events []source.PlaybackEvent) error {
	users := map[string]string{}
	urows, err := jdb.Query(`SELECT lower(replace(Id,'-','')), COALESCE(Username,'') FROM Users`)
	if err != nil {
		return err
	}
	for urows.Next() {
		var id, name string
		if err := urows.Scan(&id, &name); err != nil {
			urows.Close()
			return err
		}
		users[id] = name
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return err
	}

	type itemInfo struct{ name, seriesID, seriesName string }
	items := map[string]itemInfo{}
	irows, err := jdb.Query(`
		SELECT lower(replace(Id,'-','')), COALESCE(Name,''),
		       lower(replace(COALESCE(SeriesId,''),'-','')), COALESCE(SeriesName,'')
		FROM BaseItems
		WHERE Type IN ('` + movieType + `', '` + episodeType + `')`)
	if err != nil {
		return err
	}
	for irows.Next() {
		var id string
		var info itemInfo
		if err := irows.Scan(&id, &info.name, &info.seriesID, &info.seriesName); err != nil {
			irows.Close()
			return err
		}
		items[id] = info
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return err
	}

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
		}
	}
	return nil
}
```

- [ ] **Step 6: Implement `PlaybackEvents` in `internal/source/file/file.go`** (replace the `ErrNotImplemented` stub)

```go
func (f *FileSource) PlaybackEvents(ctx context.Context, _ time.Time) ([]source.PlaybackEvent, error) {
	pdb, pcleanup, err := f.openForRead(ctx, "watch")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, source.ErrPluginUnavailable
		}
		return nil, err
	}
	defer pcleanup()

	if !tableExists(pdb, "PlaybackActivity") {
		return nil, source.ErrPluginUnavailable
	}
	events, err := queryPlaybackEvents(pdb)
	if err != nil {
		return nil, err
	}

	jdb, jcleanup, err := f.openForRead(ctx, "library")
	if err != nil {
		f.log.Warn("watch enrichment skipped: jellyfin.db unavailable", "err", err)
		return events, nil
	}
	defer jcleanup()
	if err := enrichPlaybackEvents(jdb, events); err != nil {
		f.log.Warn("watch enrichment failed", "err", err)
	}
	return events, nil
}
```

> `file.go` already imports `errors` and `os` (used by `openForRead`). `openForRead(ctx, "watch")` already resolves `playback_reporting.db` and wraps a missing file as `fmt.Errorf("source db %s: %w", src, err)` where the wrapped `err` is `os.ErrNotExist` — so `errors.Is(err, os.ErrNotExist)` holds.

- [ ] **Step 7: Run it, see it pass** — `go test ./internal/source/file/ ./internal/testsupport/ -v` → PASS.

- [ ] **Step 8: Commit**

```bash
git add testdata/playback_reporting.fixture.sql internal/testsupport internal/source/file
git commit -m "feat(source): PlaybackEvents reads the plugin DB and enriches from jellyfin.db"
```

---

## Task 9: `aggregate.Watch` — events → daily rows + heatmap

**Files:**
- Create: `internal/aggregate/watch.go`, `internal/aggregate/watch_test.go`

**Interfaces:**
- Consumes: `[]source.PlaybackEvent`; the row types from Task 1.
- Produces:
  ```go
  type WatchAggregates struct {
      Daily   []WatchDailyRow
      Heatmap []HeatmapRow
  }
  func Watch(events []source.PlaybackEvent) WatchAggregates
  ```
  `methodBucket` stays unexported — `WatchDailyRow.Method` is already bucketed, so nothing outside `aggregate` needs it.

- [ ] **Step 1: Write the failing test** — `internal/aggregate/watch_test.go`

```go
package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func ev(day string, hour int, user, item, scope, method string, dur int64) source.PlaybackEvent {
	at, _ := time.Parse("2006-01-02 15:04", day+" "+pad(hour)+":00")
	return source.PlaybackEvent{At: at, UserID: user, ItemID: item, ItemType: scope, Method: method, PlayDurationSec: dur}
}
func pad(h int) string {
	if h < 10 {
		return "0" + string(rune('0'+h))
	}
	return string(rune('0'+h/10)) + string(rune('0'+h%10))
}

func TestWatch_DailyAndHeatmap(t *testing.T) {
	events := []source.PlaybackEvent{
		ev("2025-01-06", 20, "u1", "m1", "movie", "DirectPlay", 3600),
		ev("2025-01-06", 21, "u1", "m1", "movie", "DirectPlay", 600), // same day/user/item/method -> merges
		ev("2025-01-06", 0, "u1", "m2", "movie", "Transcode (v:h264 a:aac)", 1200),
		ev("2025-01-06", 23, "u1", "m3", "movie", "Transcode (v:direct a:direct)", 900),
		ev("2025-01-07", 9, "u2", "e1", "episode", "Transcode (v:direct a:aac)", 1500),
	}
	events[4].SeriesID, events[4].SeriesName = "s1", "Show"

	agg := Watch(events)

	// merged row
	var m1 *WatchDailyRow
	for i := range agg.Daily {
		if agg.Daily[i].ItemID == "m1" {
			m1 = &agg.Daily[i]
		}
	}
	if m1 == nil || m1.Plays != 2 || m1.WatchSec != 4200 || m1.Method != "DirectPlay" || m1.Day != "2025-01-06" {
		t.Fatalf("m1 row: %+v", m1)
	}

	buckets := map[string]string{}
	for _, r := range agg.Daily {
		buckets[r.ItemID] = r.Method
	}
	if buckets["m2"] != "VideoTranscode" || buckets["m3"] != "Remux" {
		t.Fatalf("method buckets: %+v", buckets)
	}
	// episode row carries series
	for _, r := range agg.Daily {
		if r.ItemID == "e1" && (r.SeriesID != "s1" || r.Scope != "episode") {
			t.Fatalf("e1 row: %+v", r)
		}
	}

	// heatmap: the two 2025-01-06 u1 events at 20:00 and 21:00 are distinct cells;
	// the 00:00 one is hour 0 same day (no zone shift).
	cells := map[int]int64{}
	for _, h := range agg.Heatmap {
		if h.UserID == "u1" {
			cells[h.Hour] += h.WatchSec
		}
	}
	if cells[0] != 1200 || cells[20] != 3600 || cells[21] != 600 || cells[23] != 900 {
		t.Fatalf("u1 heatmap cells: %+v", cells)
	}
}

func TestMethodBucket_Table(t *testing.T) {
	cases := map[string]string{
		"DirectPlay":                     "DirectPlay",
		"Transcode (v:direct a:direct)":  "Remux",
		"Transcode (v:direct a:aac)":     "AudioTranscode",
		"Transcode (v:h264 a:aac)":       "VideoTranscode",
		"Transcode (v:h264 a:direct)":    "VideoTranscode",
		"Transcode (garbled)":            "Other",
		"":                               "Other",
		"something else":                 "Other",
	}
	for in, want := range cases {
		if got := methodBucket(in); got != want {
			t.Errorf("methodBucket(%q) = %q want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it, see it fail** — `undefined: Watch`.

- [ ] **Step 3: Implement `internal/aggregate/watch.go`**

```go
package aggregate

import (
	"sort"
	"strings"

	"github.com/andriykohut/ephyra/internal/source"
)

type WatchAggregates struct {
	Daily   []WatchDailyRow
	Heatmap []HeatmapRow
}

// Watch rolls playback events into daily per-title rows and an all-time
// user×dow×hour heatmap. Timestamps are used as literal wall-clock (no zone
// shift): the plugin already writes server-local time.
func Watch(events []source.PlaybackEvent) WatchAggregates {
	type dKey struct{ day, user, item, method string }
	type hKey struct {
		user      string
		dow, hour int
	}
	daily := map[dKey]*WatchDailyRow{}
	heat := map[hKey]*HeatmapRow{}

	for _, e := range events {
		if e.At.IsZero() || e.UserID == "" || e.ItemID == "" {
			continue
		}
		day := e.At.Format("2006-01-02")
		mb := methodBucket(e.Method)
		dk := dKey{day, e.UserID, e.ItemID, mb}
		r := daily[dk]
		if r == nil {
			r = &WatchDailyRow{
				Day: day, UserID: e.UserID, ItemID: e.ItemID, Scope: e.ItemType,
				Name: e.ItemName, SeriesID: e.SeriesID, SeriesName: e.SeriesName, Method: mb,
			}
			daily[dk] = r
		}
		r.Plays++
		r.WatchSec += e.PlayDurationSec

		hk := hKey{e.UserID, int(e.At.Weekday()), e.At.Hour()}
		h := heat[hk]
		if h == nil {
			h = &HeatmapRow{UserID: e.UserID, DOW: hk.dow, Hour: hk.hour}
			heat[hk] = h
		}
		h.Plays++
		h.WatchSec += e.PlayDurationSec
	}

	out := WatchAggregates{}
	for _, r := range daily {
		out.Daily = append(out.Daily, *r)
	}
	sort.Slice(out.Daily, func(i, j int) bool {
		a, b := out.Daily[i], out.Daily[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.ItemID != b.ItemID {
			return a.ItemID < b.ItemID
		}
		return a.Method < b.Method
	})
	for _, h := range heat {
		out.Heatmap = append(out.Heatmap, *h)
	}
	sort.Slice(out.Heatmap, func(i, j int) bool {
		a, b := out.Heatmap[i], out.Heatmap[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.DOW != b.DOW {
			return a.DOW < b.DOW
		}
		return a.Hour < b.Hour
	})
	return out
}

// methodBucket collapses the plugin's free-text PlaybackMethod into five values.
func methodBucket(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.EqualFold(s, "DirectPlay") {
		return "DirectPlay"
	}
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "transcode") {
		return "Other"
	}
	v := paren(low, "v:")
	a := paren(low, "a:")
	if v == "" && a == "" {
		return "Other"
	}
	if v == "direct" && a == "direct" {
		return "Remux"
	}
	if v == "direct" {
		return "AudioTranscode"
	}
	return "VideoTranscode"
}

// paren pulls the token after key up to the next space or ')'.
func paren(s, key string) string {
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	if j := strings.IndexAny(rest, " )"); j >= 0 {
		return rest[:j]
	}
	return rest
}
```

- [ ] **Step 4: Run the aggregate suite** — `go test ./internal/aggregate/ -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate/watch.go internal/aggregate/watch_test.go
git commit -m "feat(aggregate): Watch — daily rows + heatmap, 5 method buckets"
```

---

## Task 10: `store.WriteWatchAggregates` wiring + scheduler `RunWatchOnce`

**Files:**
- Modify: `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`

(`WriteWatchAggregates` already exists from Task 1.)

**Interfaces:**
- Produces:
  ```go
  func (s *Scheduler) RunWatchOnce(ctx context.Context) error
  ```
  Behaviour:
  - mtime-skip vs `refresh_meta` job `"watch"` keyed on `playback_reporting.db`.
  - `src.PlaybackEvents(ctx, time.Time{})`:
    - `errors.Is(err, source.ErrPluginUnavailable)` → write `RefreshMeta{Job:"watch", OK:true, PluginAvailable:false}`, no `agg_watch_*` rows, return nil.
    - other error → `recordFailure(ctx, "watch", …)`.
  - else `aggregate.Watch(events)` → `store.WriteWatchAggregates` → `RefreshMeta{Job:"watch", OK:true, PluginAvailable:true}`.
  - `Run(ctx)` starts a `watch` ticker (`cfg.RefreshWatch`), runs `RunWatchOnce` once on startup, and the trigger `case` handles `job == "watch" || job == "all"`.

- [ ] **Step 1: Write the failing test** — append to `internal/scheduler/scheduler_test.go`

```go
func TestRunWatchOnce_PopulatesWatchTables(t *testing.T) {
	dataDir := testsupport.TwoDBLayout(t)
	sc, st := newSchedulerFor(t, dataDir) // helper: FileSource(dataDir) + fresh store
	if err := sc.RunWatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var daily, heat int
	st.DB().QueryRow(`SELECT count(*) FROM watch_events_daily`).Scan(&daily)
	st.DB().QueryRow(`SELECT count(*) FROM agg_watch_heatmap`).Scan(&heat)
	if daily == 0 || heat == 0 {
		t.Fatalf("daily=%d heat=%d", daily, heat)
	}
	m, ok, _ := st.GetRefreshMeta(context.Background(), "watch")
	if !ok || !m.OK || !m.PluginAvailable {
		t.Fatalf("watch meta: %+v ok=%v", m, ok)
	}
}

func TestRunWatchOnce_PluginAbsent(t *testing.T) {
	// jellyfin.db only
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	// minimal jellyfin.db so the library side is happy if touched
	seedEmptyJellyfinDB(t, filepath.Join(dir, "data", "jellyfin.db"))
	sc, st := newSchedulerFor(t, dir)

	if err := sc.RunWatchOnce(context.Background()); err != nil {
		t.Fatalf("plugin-absent should not error: %v", err)
	}
	m, ok, _ := st.GetRefreshMeta(context.Background(), "watch")
	if !ok || !m.OK || m.PluginAvailable {
		t.Fatalf("want ok=true plugin_available=false, got %+v", m)
	}
	var daily int
	st.DB().QueryRow(`SELECT count(*) FROM watch_events_daily`).Scan(&daily)
	if daily != 0 {
		t.Fatalf("no rows expected, got %d", daily)
	}
}
```

> Factor `newSchedulerFor(t, dataDir) (*Scheduler, *store.Store)` and `seedEmptyJellyfinDB` from the existing helpers. `seedEmptyJellyfinDB` just needs `CREATE TABLE PlaybackActivity`-free content — a `BaseItems`/`Users`/`UserData`/`MediaStreamInfos` skeleton; reuse `testsupport.LibraryFixtureDB`'s SQL is fine.

- [ ] **Step 2: Run it, see it fail** — `undefined: (*Scheduler).RunWatchOnce`.

- [ ] **Step 3: Implement `RunWatchOnce`** in `internal/scheduler/scheduler.go`

```go
// add to the struct:  watchMu sync.Mutex

func (s *Scheduler) RunWatchOnce(ctx context.Context) error {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	start := time.Now()

	var mt time.Time
	if mtimer, ok := s.src.(source.MTimer); ok {
		mt, _ = mtimer.SourceMTime("watch")
	}
	prev, hadPrev, _ := s.st.GetRefreshMeta(ctx, "watch")
	if hadPrev && prev.OK && !prev.SourceMTime.IsZero() && !mt.IsZero() && mt.Equal(prev.SourceMTime) {
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

	agg := aggregate.Watch(events)
	if err := s.st.WriteWatchAggregates(ctx, agg.Daily, agg.Heatmap); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
	s.log.Info("watch refresh ok", "events", len(events), "daily_rows", len(agg.Daily),
		"dur_ms", time.Since(start).Milliseconds())
	return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "watch", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: true, PluginAvailable: true,
	})
}
```

> `scheduler.go` already imports `errors`? It does not — add `"errors"` to the import block.

- [ ] **Step 4: Wire `RunWatchOnce` into `Run`** — add the startup call, the ticker, and the trigger case:

```go
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
```

- [ ] **Step 5: Run the scheduler suite** — `go test ./internal/scheduler/ -v` → PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/scheduler
git commit -m "feat(scheduler): watch job — RunWatchOnce, ticker, trigger, plugin-absent path"
```

---

## Task 11: `store.ReadWatchStats` + `GET /api/watch/stats`

**Files:**
- Create: `internal/store/reads_watch.go`, `internal/store/reads_watch_test.go`
- Create: `internal/api/watch.go`, `internal/api/watch_test.go`
- Modify: `internal/api/api.go`

**Interfaces:**
- Produces:
  ```go
  // internal/store
  type WatchStatsParams struct {
      Range string    // "30d" | "90d" | "1y" | "all"
      User  string    // canonical id, or "" for all
      Now   time.Time
  }
  type WatchStats struct {
      PluginAvailable bool               `json:"plugin_available"`
      Range           string             `json:"range"`
      User            string             `json:"user"`
      Users           []WatchUser        `json:"users"`
      Coverage        WatchCoverage      `json:"coverage"`
      Totals          WatchTotals        `json:"totals"`
      TopMovies       []WatchTitle       `json:"top_movies"`
      TopSeries       []WatchTitle       `json:"top_series"`
      TopEpisodes     []WatchEpisode     `json:"top_episodes"`
      ActiveUsers     []WatchActiveUser  `json:"active_users"`
      Trend           []WatchTrendPoint  `json:"trend"`
      Heatmap         []WatchHeatCell    `json:"heatmap"`
      PlayMethodWeekly []WatchMethodWeek `json:"play_method_weekly"`
      MostPlayedCore  []WatchCoreTitle   `json:"most_played_core"`
  }
  func (s *Store) ReadWatchStats(ctx context.Context, p WatchStatsParams) (WatchStats, error)
  ```
  (nested types spelled out in Step 3)

- [ ] **Step 1: Write the failing store test** — `internal/store/reads_watch_test.go`

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
)

func seedWatch(t *testing.T) (*Store, time.Time) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	// dim_user + agg_played_core via the library write
	users := []aggregate.UserRow{{ID: "u1", Name: "alice"}, {ID: "u2", Name: "bob"}}
	core := []aggregate.CorePlayRow{
		{UserID: "u1", Scope: "movie", ItemID: "m9", Name: "Fav Movie", PlayCount: 12, LastPlayedAt: "2025-01-20T00:00:00Z"},
		{UserID: "u2", Scope: "series", ItemID: "s9", Name: "Fav Show", PlayCount: 40},
	}
	if err := s.WriteLibraryAggregates(ctx, aggregate.LibraryAggregates{Totals: map[string]float64{}}, nil, users, core); err != nil {
		t.Fatal(err)
	}

	daily := []aggregate.WatchDailyRow{
		{Day: "2025-01-06", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo", Method: "DirectPlay", Plays: 3, WatchSec: 9000},
		{Day: "2025-01-20", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Bravo", Method: "VideoTranscode", Plays: 1, WatchSec: 1800},
		{Day: "2025-01-21", UserID: "u2", ItemID: "e1", Scope: "episode", Name: "Ep 1", SeriesID: "s1", SeriesName: "Some Show", Method: "DirectPlay", Plays: 2, WatchSec: 2400},
		{Day: "2024-06-01", UserID: "u1", ItemID: "m2", Scope: "movie", Name: "Old", Method: "DirectPlay", Plays: 1, WatchSec: 600}, // outside 30d
	}
	heat := []aggregate.HeatmapRow{
		{UserID: "u1", DOW: 1, Hour: 20, WatchSec: 9000, Plays: 3},
		{UserID: "u2", DOW: 2, Hour: 21, WatchSec: 2400, Plays: 2},
	}
	if err := s.WriteWatchAggregates(ctx, daily, heat); err != nil {
		t.Fatal(err)
	}
	return s, now
}

func TestReadWatchStats_RangeAndUserFilters(t *testing.T) {
	s, now := seedWatch(t)
	ctx := context.Background()

	all, err := s.ReadWatchStats(ctx, WatchStatsParams{Range: "all", User: "", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Users) != 2 || all.Coverage.TotalPlays != 7 || all.Coverage.FirstPlay != "2024-06-01" {
		t.Fatalf("coverage/users: %+v", all)
	}
	if all.Totals.Plays != 7 || all.Totals.ActiveUsers != 2 {
		t.Fatalf("all totals: %+v", all.Totals)
	}
	// direct_play_pct over "all": DirectPlay plays = 3+2+1 = 6 of 7
	if all.Totals.DirectPlayPct < 0.85 || all.Totals.DirectPlayPct > 0.86 {
		t.Fatalf("direct_play_pct = %v", all.Totals.DirectPlayPct)
	}

	// range=30d from 2025-02-01 -> cutoff 2025-01-02; drops the 2024-06 row
	r30 := mustRead(t, s, WatchStatsParams{Range: "30d", User: "", Now: now})
	if r30.Totals.Plays != 6 {
		t.Fatalf("30d plays = %d want 6", r30.Totals.Plays)
	}

	// user filter: u1 only
	u1 := mustRead(t, s, WatchStatsParams{Range: "all", User: "u1", Now: now})
	if u1.Totals.Plays != 5 { // 3 + 1 + 1(old)
		t.Fatalf("u1 plays = %d want 5", u1.Totals.Plays)
	}
	// active_users ignores the user filter -> still both
	if len(u1.ActiveUsers) != 2 {
		t.Fatalf("active_users should ignore user filter: %d", len(u1.ActiveUsers))
	}
	// heatmap follows the user filter
	if len(u1.Heatmap) != 1 || u1.Heatmap[0].DOW != 1 {
		t.Fatalf("u1 heatmap: %+v", u1.Heatmap)
	}
	// most_played_core follows user filter, ignores range
	if len(u1.MostPlayedCore) != 1 || u1.MostPlayedCore[0].Name != "Fav Movie" {
		t.Fatalf("u1 core: %+v", u1.MostPlayedCore)
	}
	// most_played_core for all users sums per item
	if len(all.MostPlayedCore) != 2 {
		t.Fatalf("all core rows: %+v", all.MostPlayedCore)
	}

	// top_series groups episodes by series
	if len(r30.TopSeries) != 1 || r30.TopSeries[0].Name != "Some Show" {
		t.Fatalf("top_series: %+v", r30.TopSeries)
	}
}

func mustRead(t *testing.T, s *Store, p WatchStatsParams) WatchStats {
	t.Helper()
	w, err := s.ReadWatchStats(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return w
}
```

- [ ] **Step 2: Run it, see it fail** — `undefined: (*Store).ReadWatchStats`.

- [ ] **Step 3: Implement `internal/store/reads_watch.go`**

```go
package store

import (
	"context"
	"sort"
	"time"
)

type WatchStatsParams struct {
	Range string
	User  string
	Now   time.Time
}

type WatchUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type WatchCoverage struct {
	FirstPlay  string `json:"first_play"`
	LastPlay   string `json:"last_play"`
	TotalPlays int64  `json:"total_plays"`
}
type WatchTotals struct {
	WatchSeconds      int64   `json:"watch_seconds"`
	Plays             int64   `json:"plays"`
	ActiveUsers       int64   `json:"active_users"`
	DirectPlayPct     float64 `json:"direct_play_pct"`
	VideoTranscodePct float64 `json:"video_transcode_pct"`
}
type WatchTitle struct {
	ItemID   string `json:"item_id,omitempty"`
	SeriesID string `json:"series_id,omitempty"`
	Name     string `json:"name"`
	Plays    int64  `json:"plays"`
	WatchSec int64  `json:"watch_sec"`
}
type WatchEpisode struct {
	ItemID     string `json:"item_id"`
	Name       string `json:"name"`
	SeriesName string `json:"series_name"`
	Plays      int64  `json:"plays"`
	WatchSec   int64  `json:"watch_sec"`
}
type WatchActiveUser struct {
	UserID         string `json:"user_id"`
	Name           string `json:"name"`
	WatchSec       int64  `json:"watch_sec"`
	Plays          int64  `json:"plays"`
	DistinctTitles int64  `json:"distinct_titles"`
}
type WatchTrendPoint struct {
	Day      string `json:"day"`
	WatchSec int64  `json:"watch_sec"`
	Plays    int64  `json:"plays"`
}
type WatchHeatCell struct {
	DOW      int   `json:"dow"`
	Hour     int   `json:"hour"`
	WatchSec int64 `json:"watch_sec"`
	Plays    int64 `json:"plays"`
}
type WatchMethodWeek struct {
	Week           string `json:"week"`
	DirectPlay     int64  `json:"DirectPlay"`
	Remux          int64  `json:"Remux"`
	AudioTranscode int64  `json:"AudioTranscode"`
	VideoTranscode int64  `json:"VideoTranscode"`
	Other          int64  `json:"Other"`
}
type WatchCoreTitle struct {
	Scope        string `json:"scope"`
	ItemID       string `json:"item_id"`
	Name         string `json:"name"`
	PlayCount    int64  `json:"play_count"`
	LastPlayedAt string `json:"last_played_at"`
}

type WatchStats struct {
	PluginAvailable  bool              `json:"plugin_available"`
	Range            string            `json:"range"`
	User             string            `json:"user"`
	Users            []WatchUser       `json:"users"`
	Coverage         WatchCoverage     `json:"coverage"`
	Totals           WatchTotals       `json:"totals"`
	TopMovies        []WatchTitle      `json:"top_movies"`
	TopSeries        []WatchTitle      `json:"top_series"`
	TopEpisodes      []WatchEpisode    `json:"top_episodes"`
	ActiveUsers      []WatchActiveUser `json:"active_users"`
	Trend            []WatchTrendPoint `json:"trend"`
	Heatmap          []WatchHeatCell   `json:"heatmap"`
	PlayMethodWeekly []WatchMethodWeek `json:"play_method_weekly"`
	MostPlayedCore   []WatchCoreTitle  `json:"most_played_core"`
}

// dayCutoff returns "YYYY-MM-DD" for the range, or "" for "all".
func dayCutoff(rng string, now time.Time) string {
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

func weekly(rng string) bool { return rng == "1y" || rng == "all" }

// isoWeekMonday returns the Monday (YYYY-MM-DD) of the ISO week containing day.
func isoWeekMonday(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	wd := (int(t.Weekday()) + 6) % 7 // Mon=0 .. Sun=6
	return t.AddDate(0, 0, -wd).Format("2006-01-02")
}

func (s *Store) ReadWatchStats(ctx context.Context, p WatchStatsParams) (WatchStats, error) {
	ws := WatchStats{Range: p.Range, User: p.User}
	if ws.User == "" {
		ws.User = "all"
	}

	// plugin_available from the watch job's meta
	if m, ok, err := s.GetRefreshMeta(ctx, "watch"); err == nil && ok {
		ws.PluginAvailable = m.OK && m.PluginAvailable
	}

	// users (always)
	urows, err := s.db.QueryContext(ctx, `SELECT id, name FROM dim_user ORDER BY name`)
	if err != nil {
		return ws, err
	}
	for urows.Next() {
		var u WatchUser
		if err := urows.Scan(&u.ID, &u.Name); err != nil {
			urows.Close()
			return ws, err
		}
		ws.Users = append(ws.Users, u)
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return ws, err
	}

	// coverage (always, unfiltered)
	var first, last *string
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(day), MAX(day), COALESCE(SUM(plays),0) FROM watch_events_daily`,
	).Scan(&first, &last, &ws.Coverage.TotalPlays); err != nil {
		return ws, err
	}
	if first != nil {
		ws.Coverage.FirstPlay = *first
	}
	if last != nil {
		ws.Coverage.LastPlay = *last
	}

	// most_played_core (user filter, no range)
	if err := s.readCore(ctx, &ws, p.User); err != nil {
		return ws, err
	}

	// everything else needs the daily table
	cut := dayCutoff(p.Range, p.Now)
	rangeArgs := []any{}
	rangeWhere := "1=1"
	if cut != "" {
		rangeWhere = "day >= ?"
		rangeArgs = append(rangeArgs, cut)
	}
	userWhere := ""
	userArgs := []any{}
	if p.User != "" {
		userWhere = " AND user_id = ?"
		userArgs = append(userArgs, p.User)
	}
	scoped := rangeWhere + userWhere
	scopedArgs := append(append([]any{}, rangeArgs...), userArgs...)

	// totals + method percentages
	var dp, vt int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(watch_sec),0), COALESCE(SUM(plays),0),
		       COUNT(DISTINCT user_id),
		       COALESCE(SUM(CASE WHEN method='DirectPlay'     THEN plays ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN method='VideoTranscode' THEN plays ELSE 0 END),0)
		FROM watch_events_daily WHERE `+scoped, scopedArgs...,
	).Scan(&ws.Totals.WatchSeconds, &ws.Totals.Plays, &ws.Totals.ActiveUsers, &dp, &vt); err != nil {
		return ws, err
	}
	if ws.Totals.Plays > 0 {
		ws.Totals.DirectPlayPct = float64(dp) / float64(ws.Totals.Plays)
		ws.Totals.VideoTranscodePct = float64(vt) / float64(ws.Totals.Plays)
	}

	// top movies
	if err := s.readTop(ctx, &ws.TopMovies, `
		SELECT item_id, MAX(name), SUM(plays), SUM(watch_sec)
		FROM watch_events_daily WHERE scope='movie' AND `+scoped+`
		GROUP BY item_id ORDER BY SUM(watch_sec) DESC, MAX(name) LIMIT 10`, scopedArgs, false); err != nil {
		return ws, err
	}
	// top series (episodes with a series_id)
	if err := s.readTop(ctx, &ws.TopSeries, `
		SELECT series_id, MAX(series_name), SUM(plays), SUM(watch_sec)
		FROM watch_events_daily WHERE scope='episode' AND series_id <> '' AND `+scoped+`
		GROUP BY series_id ORDER BY SUM(watch_sec) DESC, MAX(series_name) LIMIT 10`, scopedArgs, true); err != nil {
		return ws, err
	}
	// top episodes
	erows, err := s.db.QueryContext(ctx, `
		SELECT item_id, MAX(name), MAX(series_name), SUM(plays), SUM(watch_sec)
		FROM watch_events_daily WHERE scope='episode' AND `+scoped+`
		GROUP BY item_id ORDER BY SUM(watch_sec) DESC, MAX(name) LIMIT 10`, scopedArgs...)
	if err != nil {
		return ws, err
	}
	for erows.Next() {
		var e WatchEpisode
		if err := erows.Scan(&e.ItemID, &e.Name, &e.SeriesName, &e.Plays, &e.WatchSec); err != nil {
			erows.Close()
			return ws, err
		}
		ws.TopEpisodes = append(ws.TopEpisodes, e)
	}
	erows.Close()
	if err := erows.Err(); err != nil {
		return ws, err
	}

	// active users (range only, NOT the user filter) + name from dim_user
	arows, err := s.db.QueryContext(ctx, `
		SELECT w.user_id, COALESCE(d.name, w.user_id),
		       SUM(w.watch_sec), SUM(w.plays), COUNT(DISTINCT w.item_id)
		FROM watch_events_daily w
		LEFT JOIN dim_user d ON d.id = w.user_id
		WHERE `+rangeWhere+`
		GROUP BY w.user_id ORDER BY SUM(w.watch_sec) DESC`, rangeArgs...)
	if err != nil {
		return ws, err
	}
	for arows.Next() {
		var a WatchActiveUser
		if err := arows.Scan(&a.UserID, &a.Name, &a.WatchSec, &a.Plays, &a.DistinctTitles); err != nil {
			arows.Close()
			return ws, err
		}
		ws.ActiveUsers = append(ws.ActiveUsers, a)
	}
	arows.Close()
	if err := arows.Err(); err != nil {
		return ws, err
	}

	// trend (daily; rolled to ISO-week Monday when weekly(range))
	trows, err := s.db.QueryContext(ctx, `
		SELECT day, SUM(watch_sec), SUM(plays)
		FROM watch_events_daily WHERE `+scoped+`
		GROUP BY day ORDER BY day`, scopedArgs...)
	if err != nil {
		return ws, err
	}
	trend := map[string]*WatchTrendPoint{}
	var order []string
	for trows.Next() {
		var day string
		var wsec, plays int64
		if err := trows.Scan(&day, &wsec, &plays); err != nil {
			trows.Close()
			return ws, err
		}
		key := day
		if weekly(p.Range) {
			key = isoWeekMonday(day)
		}
		pt := trend[key]
		if pt == nil {
			pt = &WatchTrendPoint{Day: key}
			trend[key] = pt
			order = append(order, key)
		}
		pt.WatchSec += wsec
		pt.Plays += plays
	}
	trows.Close()
	if err := trows.Err(); err != nil {
		return ws, err
	}
	for _, k := range order {
		ws.Trend = append(ws.Trend, *trend[k])
	}

	// play_method_weekly (always weekly; range + user scoped)
	mrows, err := s.db.QueryContext(ctx, `
		SELECT day, method, SUM(plays)
		FROM watch_events_daily WHERE `+scoped+`
		GROUP BY day, method`, scopedArgs...)
	if err != nil {
		return ws, err
	}
	weeks := map[string]*WatchMethodWeek{}
	var weekOrder []string
	for mrows.Next() {
		var day, method string
		var plays int64
		if err := mrows.Scan(&day, &method, &plays); err != nil {
			mrows.Close()
			return ws, err
		}
		wk := isoWeekMonday(day)
		mw := weeks[wk]
		if mw == nil {
			mw = &WatchMethodWeek{Week: wk}
			weeks[wk] = mw
			weekOrder = append(weekOrder, wk)
		}
		switch method {
		case "DirectPlay":
			mw.DirectPlay += plays
		case "Remux":
			mw.Remux += plays
		case "AudioTranscode":
			mw.AudioTranscode += plays
		case "VideoTranscode":
			mw.VideoTranscode += plays
		default:
			mw.Other += plays
		}
	}
	mrows.Close()
	if err := mrows.Err(); err != nil {
		return ws, err
	}
	sort.Strings(weekOrder)
	for _, k := range weekOrder {
		ws.PlayMethodWeekly = append(ws.PlayMethodWeekly, *weeks[k])
	}

	// heatmap (user filter, NO range) from agg_watch_heatmap
	hWhere := "1=1"
	hArgs := []any{}
	if p.User != "" {
		hWhere = "user_id = ?"
		hArgs = append(hArgs, p.User)
	}
	hrows, err := s.db.QueryContext(ctx, `
		SELECT dow, hour, SUM(watch_sec), SUM(plays)
		FROM agg_watch_heatmap WHERE `+hWhere+`
		GROUP BY dow, hour ORDER BY dow, hour`, hArgs...)
	if err != nil {
		return ws, err
	}
	for hrows.Next() {
		var c WatchHeatCell
		if err := hrows.Scan(&c.DOW, &c.Hour, &c.WatchSec, &c.Plays); err != nil {
			hrows.Close()
			return ws, err
		}
		ws.Heatmap = append(ws.Heatmap, c)
	}
	hrows.Close()
	return ws, hrows.Err()
}

func (s *Store) readTop(ctx context.Context, dst *[]WatchTitle, q string, args []any, isSeries bool) error {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		var plays, wsec int64
		if err := rows.Scan(&id, &name, &plays, &wsec); err != nil {
			return err
		}
		wt := WatchTitle{Name: name, Plays: plays, WatchSec: wsec}
		if isSeries {
			wt.SeriesID = id
		} else {
			wt.ItemID = id
		}
		*dst = append(*dst, wt)
	}
	return rows.Err()
}

func (s *Store) readCore(ctx context.Context, ws *WatchStats, user string) error {
	var q string
	var args []any
	if user == "" {
		q = `SELECT scope, item_id, MAX(name), SUM(play_count), COALESCE(MAX(last_played_at),'')
		     FROM agg_played_core GROUP BY item_id, scope
		     ORDER BY SUM(play_count) DESC, MAX(name) LIMIT 15`
	} else {
		q = `SELECT scope, item_id, name, play_count, COALESCE(last_played_at,'')
		     FROM agg_played_core WHERE user_id = ?
		     ORDER BY play_count DESC, name LIMIT 15`
		args = append(args, user)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c WatchCoreTitle
		if err := rows.Scan(&c.Scope, &c.ItemID, &c.Name, &c.PlayCount, &c.LastPlayedAt); err != nil {
			return err
		}
		ws.MostPlayedCore = append(ws.MostPlayedCore, c)
	}
	return rows.Err()
}
```

- [ ] **Step 4: Run the store test** — `go test ./internal/store/ -run TestReadWatchStats -v` → PASS.

- [ ] **Step 5: Write the failing API test** — `internal/api/watch_test.go`

```go
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/store"
)

func TestWatchStats_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/watch/stats", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 before any library refresh, got %d", rr.Code)
	}
}

func TestWatchStats_PluginAbsentStillHasUsersAndCore(t *testing.T) {
	now := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()

	// library ran (so not_ready is cleared); no watch job
	if err := st.WriteLibraryAggregates(ctx, aggregate.LibraryAggregates{Totals: map[string]float64{}},
		nil,
		[]aggregate.UserRow{{ID: "u1", Name: "alice"}},
		[]aggregate.CorePlayRow{{UserID: "u1", Scope: "movie", ItemID: "m9", Name: "Fav", PlayCount: 9}},
	); err != nil {
		t.Fatal(err)
	}
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-time.Minute), OK: true})

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/watch/stats?range=30d", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data store.WatchStats `json:"data"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Data.PluginAvailable {
		t.Fatal("plugin should be reported absent")
	}
	if len(env.Data.Users) != 1 || len(env.Data.MostPlayedCore) != 1 {
		t.Fatalf("users/core missing when plugin absent: %+v", env.Data)
	}
	if len(env.Data.TopMovies) != 0 {
		t.Fatalf("plugin panels should be empty: %+v", env.Data.TopMovies)
	}
}

func TestWatchStats_BadRange(t *testing.T) {
	now := time.Now()
	s, st, _ := newTestServer(t, now)
	st.SetRefreshMeta(context.Background(), store.RefreshMeta{Job: "library", LastRunAt: now, OK: true})
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/watch/stats?range=lolyear", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rr.Code)
	}
}
```

- [ ] **Step 6: Run it, see it fail** — 404.

- [ ] **Step 7: Implement `internal/api/watch.go`**

```go
package api

import (
	"net/http"

	"github.com/andriykohut/ephyra/internal/store"
)

var validRanges = map[string]bool{"30d": true, "90d": true, "1y": true, "all": true}

func (s *Server) handleWatchStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	rng := q.Get("range")
	if rng == "" {
		rng = "30d"
	}
	if !validRanges[rng] {
		writeError(w, http.StatusBadRequest, "bad_request", "range must be 30d|90d|1y|all")
		return
	}
	user := q.Get("user")
	if user == "all" {
		user = ""
	}

	m, ok, err := s.st.GetRefreshMeta(ctx, "library")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first library refresh has not completed")
		return
	}

	data, err := s.st.ReadWatchStats(ctx, store.WatchStatsParams{Range: rng, User: user, Now: s.now()})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}

	// staleness: from the watch job's meta, but a plugin-absent job is fresh-empty
	stale := false
	if wm, wok, _ := s.st.GetRefreshMeta(ctx, "watch"); wok && wm.PluginAvailable {
		stale = store.IsStale(wm, wok, s.cfg.RefreshWatch, s.now())
	}
	_ = m
	writeJSON(w, http.StatusOK, data, s.meta(stale))
}
```

- [ ] **Step 8: Register the route** in `internal/api/api.go`:

```go
	mux.HandleFunc("GET /api/watch/stats", s.handleWatchStats)
```

- [ ] **Step 9: Run api + store suites** — `go test ./internal/api/ ./internal/store/ -v` → PASS. Then `go test ./... && go vet ./... && golangci-lint run`.

- [ ] **Step 10: Commit**

```bash
git add internal/store/reads_watch.go internal/store/reads_watch_test.go internal/api/watch.go internal/api/watch_test.go internal/api/api.go
git commit -m "feat(api): GET /api/watch/stats — range/user scoping, coverage, core fallback"
```

---

## Task 12: Frontend — `UserSelect`, `Heatmap`, `MethodBars`, DTOs

**Files:**
- Create: `web/src/components/UserSelect.tsx`
- Create: `web/src/charts/Heatmap.tsx`, `web/src/charts/MethodBars.tsx`
- Modify: `web/src/charts/theme.ts`, `web/src/api/types.ts`, `web/src/api/queries.ts`

**Interfaces:**
- Produces:
  ```ts
  export function UserSelect(props: { value: string; users: { id: string; name: string }[]; onChange: (v: string) => void }): JSX.Element
  export function Heatmap(props: { cells: { dow: number; hour: number; watch_sec: number }[]; ariaLabel: string }): JSX.Element
  export function MethodBars(props: { weeks: WatchMethodWeek[] }): JSX.Element

  // api/types.ts — add WatchStats + nested types mirroring internal/store/reads_watch.go JSON tags
  // api/queries.ts
  export const watchStatsQuery: (range: WatchRange, user: string) => UseQueryOptions<Envelope<WatchStats>>
  ```

- [ ] **Step 1: `web/src/components/UserSelect.tsx`**

```tsx
export function UserSelect({
  value,
  users,
  onChange,
}: {
  value: string;
  users: { id: string; name: string }[];
  onChange: (v: string) => void;
}) {
  return (
    <select
      aria-label="user"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-[10px] border border-line bg-panel/60 px-3 py-1.5 text-[12.5px] text-ink"
    >
      <option value="all">All users</option>
      {users.map((u) => (
        <option key={u.id} value={u.id}>
          {u.name}
        </option>
      ))}
    </select>
  );
}
```

- [ ] **Step 2: Add DTOs to `web/src/api/types.ts`** — mirror the Go JSON exactly:

```ts
export type WatchRange = "30d" | "90d" | "1y" | "all";

export interface WatchMethodWeek {
  week: string;
  DirectPlay: number;
  Remux: number;
  AudioTranscode: number;
  VideoTranscode: number;
  Other: number;
}
export interface WatchStats {
  plugin_available: boolean;
  range: WatchRange;
  user: string;
  users: { id: string; name: string }[];
  coverage: { first_play: string; last_play: string; total_plays: number };
  totals: {
    watch_seconds: number;
    plays: number;
    active_users: number;
    direct_play_pct: number;
    video_transcode_pct: number;
  };
  top_movies: { item_id: string; name: string; plays: number; watch_sec: number }[];
  top_series: { series_id: string; name: string; plays: number; watch_sec: number }[];
  top_episodes: { item_id: string; name: string; series_name: string; plays: number; watch_sec: number }[];
  active_users: { user_id: string; name: string; watch_sec: number; plays: number; distinct_titles: number }[];
  trend: { day: string; watch_sec: number; plays: number }[];
  heatmap: { dow: number; hour: number; watch_sec: number; plays: number }[];
  play_method_weekly: WatchMethodWeek[];
  most_played_core: { scope: string; item_id: string; name: string; play_count: number; last_played_at: string }[];
}
```

- [ ] **Step 3: `web/src/api/queries.ts`** — add:

```ts
import type { WatchRange, WatchStats } from "./types";

export const watchStatsQuery = (range: WatchRange, user: string) =>
  queryOptions({
    queryKey: ["watch-stats", range, user],
    queryFn: () => fetchEnvelope<WatchStats>(`/api/watch/stats?range=${range}&user=${user}`),
    staleTime: 10 * 60 * 1000,
  });
```

- [ ] **Step 4: `web/src/charts/theme.ts`** — add exported constants used by both charts:

```ts
export const methodColors: Record<string, string> = {
  DirectPlay: "#4fe0d8",
  Remux: "#57c7a3",
  AudioTranscode: "#7ba7e6",
  VideoTranscode: "#9b7bff",
  Other: "#8a9bb8",
};
// heatmap ramp: transparent -> cyan -> violet
export const heatRamp = ["rgba(79,224,216,0.05)", "rgba(79,224,216,0.55)", "rgba(155,123,255,0.9)"];
```

- [ ] **Step 5: `web/src/charts/MethodBars.tsx`**

```tsx
import { lazy, Suspense } from "react";
import type { WatchMethodWeek } from "@/api/types";
import { Skeleton } from "@/components/Skeleton";
import { methodColors } from "./theme";

const EChart = lazy(() => import("./EChart").then((m) => ({ default: m.EChart })));
const ORDER = ["DirectPlay", "Remux", "AudioTranscode", "VideoTranscode", "Other"] as const;

export function MethodBars({ weeks }: { weeks: WatchMethodWeek[] }) {
  const option = {
    tooltip: { trigger: "axis" as const, axisPointer: { type: "shadow" as const } },
    legend: { top: 0, left: 0, textStyle: { fontSize: 10 } },
    grid: { left: 4, right: 8, top: 28, bottom: 20, containLabel: true },
    xAxis: { type: "category" as const, data: weeks.map((w) => w.week) },
    yAxis: { type: "value" as const },
    series: ORDER.map((k) => ({
      name: k,
      type: "bar" as const,
      stack: "m",
      data: weeks.map((w) => w[k]),
      itemStyle: { color: methodColors[k] },
      barMaxWidth: 26,
    })),
  };
  return (
    <Suspense fallback={<Skeleton className="h-[240px]" />}>
      <EChart ariaLabel="Play method by week" option={option} />
    </Suspense>
  );
}
```

- [ ] **Step 6: `web/src/charts/Heatmap.tsx`**

```tsx
import { lazy, Suspense } from "react";
import { Skeleton } from "@/components/Skeleton";
import { heatRamp } from "./theme";

const EChart = lazy(() => import("./EChart").then((m) => ({ default: m.EChart })));
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

export function Heatmap({
  cells,
  ariaLabel,
}: {
  cells: { dow: number; hour: number; watch_sec: number }[];
  ariaLabel: string;
}) {
  const data = cells.map((c) => [c.hour, c.dow, Math.round(c.watch_sec / 3600)]);
  const max = Math.max(1, ...data.map((d) => d[2] as number));
  const option = {
    tooltip: {
      formatter: (p: { value: [number, number, number] }) =>
        `${DOW[p.value[1]]} ${String(p.value[0]).padStart(2, "0")}:00 · ${p.value[2]} h`,
    },
    grid: { left: 4, right: 8, top: 8, bottom: 24, containLabel: true },
    xAxis: { type: "category" as const, data: Array.from({ length: 24 }, (_, i) => i), splitArea: { show: true } },
    yAxis: { type: "category" as const, data: DOW, splitArea: { show: true } },
    visualMap: { min: 0, max, show: false, inRange: { color: heatRamp } },
    series: [{ type: "heatmap" as const, data, progressive: 0, itemStyle: { borderColor: "transparent" } }],
  };
  return (
    <Suspense fallback={<Skeleton className="h-[240px]" />}>
      <EChart ariaLabel={ariaLabel} option={option} />
    </Suspense>
  );
}
```

- [ ] **Step 7: `npx tsc -b && npx biome ci .`** → clean. (No dedicated unit test for these — they're exercised by Task 13's page test with the EChart mock.)

- [ ] **Step 8: Commit**

```bash
cd .. && git add web/src && git commit -m "feat(web): UserSelect, Heatmap, MethodBars, watch DTOs"
```

---

## Task 13: Frontend — the `/watch` page

**Files:**
- Replace: `web/src/routes/watch.tsx`
- Create: `web/src/routes/watch.test.tsx`

**Interfaces:**
- Consumes: `watchStatsQuery`, `SegmentedControl`, `UserSelect`, `DataTable`, `Heatmap`, `MethodBars`, `StatCard`, `Panel`, `StaleBanner`, `Skeleton`.
- Produces: `export function WatchStats()` and the route.

- [ ] **Step 1: Write the failing test** — `web/src/routes/watch.test.tsx`

```tsx
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, WatchStats as WS } from "@/api/types";

vi.mock("@/charts/EChart", () => ({ EChart: () => <div data-testid="echart" /> }));

const { WatchStats } = await import("./watch");

const base: WS = {
  plugin_available: true,
  range: "30d",
  user: "all",
  users: [
    { id: "u1", name: "alice" },
    { id: "u2", name: "bob" },
  ],
  coverage: { first_play: "2025-01-01", last_play: "2025-01-31", total_plays: 42 },
  totals: { watch_seconds: 36000, plays: 20, active_users: 2, direct_play_pct: 0.8, video_transcode_pct: 0.1 },
  top_movies: [{ item_id: "m1", name: "Bravo", plays: 5, watch_sec: 9000 }],
  top_series: [{ series_id: "s1", name: "Some Show", plays: 8, watch_sec: 12000 }],
  top_episodes: [{ item_id: "e1", name: "Pilot", series_name: "Some Show", plays: 2, watch_sec: 2400 }],
  active_users: [{ user_id: "u1", name: "alice", watch_sec: 30000, plays: 15, distinct_titles: 6 }],
  trend: [{ day: "2025-01-06", watch_sec: 9000, plays: 3 }],
  heatmap: [{ dow: 1, hour: 20, watch_sec: 9000, plays: 3 }],
  play_method_weekly: [{ week: "2025-01-06", DirectPlay: 10, Remux: 1, AudioTranscode: 2, VideoTranscode: 3, Other: 0 }],
  most_played_core: [{ scope: "movie", item_id: "m9", name: "Fav Movie", play_count: 12, last_played_at: "2025-01-20T00:00:00Z" }],
};

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}
afterEach(() => vi.restoreAllMocks());

test("renders panels from data", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: base, meta: { generated_at: "x", stale: false } } satisfies Envelope<WS>), { status: 200 }));
  render(wrap(<WatchStats />));
  await waitFor(() => expect(screen.getByRole("heading", { name: /watch/i })).toBeInTheDocument());
  expect(screen.getByText("Some Show")).toBeInTheDocument();
  expect(screen.getByText(/Fav Movie/)).toBeInTheDocument();
  expect(screen.getByText(/Jellyfin's own play counters/i)).toBeInTheDocument();
  expect((await screen.findAllByTestId("echart")).length).toBeGreaterThanOrEqual(2);
});

test("plugin-absent shows install card but keeps users + core", async () => {
  const absent = { ...base, plugin_available: false, top_movies: [], top_series: [], top_episodes: [], trend: [], heatmap: [], play_method_weekly: [], active_users: [], totals: { ...base.totals, plays: 0 } };
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: absent, meta: { generated_at: "x", stale: false } }), { status: 200 }));
  render(wrap(<WatchStats />));
  await waitFor(() => expect(screen.getByText(/Playback Reporting/i)).toBeInTheDocument());
  expect(screen.getByText(/Fav Movie/)).toBeInTheDocument(); // core panel still there
});

test("empty range shows coverage-aware message", async () => {
  const empty = { ...base, top_movies: [], top_series: [], top_episodes: [], trend: [], heatmap: [], play_method_weekly: [], active_users: [], totals: { ...base.totals, plays: 0, watch_seconds: 0 } };
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ data: empty, meta: { generated_at: "x", stale: false } }), { status: 200 }));
  render(wrap(<WatchStats />));
  await waitFor(() => expect(screen.getByText(/no plays in this range/i)).toBeInTheDocument());
  expect(screen.getByText(/2025-01-01/)).toBeInTheDocument(); // coverage dates surfaced
});
```

- [ ] **Step 2: Run it, see it fail** — no `WatchStats` export.

- [ ] **Step 3: Replace `web/src/routes/watch.tsx`**

```tsx
import { useQuery } from "@tanstack/react-query";
import { createRoute } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { watchStatsQuery } from "@/api/queries";
import type { WatchRange, WatchStats as WS } from "@/api/types";
import { Heatmap } from "@/charts/Heatmap";
import { MethodBars } from "@/charts/MethodBars";
import { type Column, DataTable } from "@/components/DataTable";
import { Panel } from "@/components/Panel";
import { SegmentedControl } from "@/components/SegmentedControl";
import { Skeleton } from "@/components/Skeleton";
import { StaleBanner } from "@/components/StaleBanner";
import { StatCard } from "@/components/StatCard";
import { UserSelect } from "@/components/UserSelect";
import { fmtInt, fmtRuntime } from "@/lib/format";
import { Route as rootRoute } from "./__root";

const RANGES: { value: WatchRange; label: string }[] = [
  { value: "30d", label: "30 days" },
  { value: "90d", label: "90 days" },
  { value: "1y", label: "1 year" },
  { value: "all", label: "All" },
];

const search = (s: Record<string, unknown>): { range: WatchRange; user: string } => ({
  range: (["30d", "90d", "1y", "all"].includes(s.range as string) ? s.range : "30d") as WatchRange,
  user: typeof s.user === "string" && s.user ? s.user : "all",
});

function pct(x: number) {
  return `${Math.round(x * 100)}%`;
}
function hours(sec: number) {
  return `${(sec / 3600).toFixed(1)} h`;
}

function ChartPanel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Panel className="p-4">
      <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">{title}</div>
      {children}
    </Panel>
  );
}

const titleCols: Column<WS["top_movies"][number]>[] = [
  { key: "name", header: "title", cell: (r) => r.name },
  { key: "plays", header: "plays", align: "right", cell: (r) => fmtInt(r.plays) },
  { key: "hrs", header: "watch", align: "right", cell: (r) => hours(r.watch_sec) },
];

export function WatchStats() {
  const { range, user } = Route.useSearch();
  const nav = Route.useNavigate();
  const q = useQuery(watchStatsQuery(range, user));

  const controls = (
    <div className="flex flex-wrap items-center gap-3">
      <SegmentedControl
        ariaLabel="range"
        value={range}
        onChange={(v) => nav({ search: (s) => ({ ...s, range: v }) })}
        options={RANGES}
      />
      <UserSelect
        value={user}
        users={q.data?.data.users ?? []}
        onChange={(v) => nav({ search: (s) => ({ ...s, user: v }) })}
      />
    </div>
  );

  if (q.isPending) {
    return (
      <div className="flex flex-col gap-6">
        <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">Watch Stats</h1>
        <Skeleton className="h-[420px] w-full" />
      </div>
    );
  }
  if (q.isError) {
    return (
      <Panel className="p-5">
        <p className="text-[13px] text-muted">{(q.error as Error).message}</p>
      </Panel>
    );
  }

  const d = q.data.data;
  const hasPlays = d.totals.plays > 0;

  const coreTable = (
    <ChartPanel title="most played · jellyfin counts">
      <DataTable
        columns={[
          { key: "name", header: "title", cell: (r) => r.name },
          { key: "pc", header: "plays", align: "right", cell: (r) => fmtInt(r.play_count) },
        ]}
        rows={d.most_played_core}
        getKey={(r) => r.item_id}
        empty="No play counts recorded yet."
      />
      <p className="mt-2 text-[11px] text-muted">
        From Jellyfin's own play counters, not the plugin — all-time, no watch time.
      </p>
    </ChartPanel>
  );

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-baseline justify-between gap-4">
        <h1 className="font-display text-[clamp(24px,4vw,32px)] font-bold tracking-[-0.03em] text-ink">Watch Stats</h1>
      </div>
      {controls}
      {q.data.meta.stale && <StaleBanner />}

      {!d.plugin_available ? (
        <Panel className="p-5">
          <div className="font-display text-lg font-semibold text-ink">Playback Reporting not detected</div>
          <p className="mt-1 text-[13px] leading-relaxed text-muted">
            Watch Stats needs the Playback Reporting plugin. In Jellyfin: Dashboard → Plugins → Catalog →
            Playback Reporting → install, then restart Jellyfin. Ephyra picks it up on the next refresh.
          </p>
        </Panel>
      ) : !hasPlays ? (
        <Panel className="p-5">
          <div className="font-display text-lg font-semibold text-ink">No plays in this range</div>
          <p className="mt-1 text-[13px] text-muted">
            The plugin has history from {d.coverage.first_play || "—"} to {d.coverage.last_play || "—"} (
            {fmtInt(d.coverage.total_plays)} plays total).
          </p>
          <button
            type="button"
            onClick={() => nav({ search: (s) => ({ ...s, range: "all" }) })}
            className="mt-3 rounded-[10px] border border-line px-3 py-1.5 text-[13px] text-ink hover:border-cyan hover:text-cyan"
          >
            View all
          </button>
        </Panel>
      ) : (
        <>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
            <StatCard label="watch time" value={fmtRuntime(d.totals.watch_seconds)} />
            <StatCard label="plays" value={fmtInt(d.totals.plays)} />
            <StatCard label="active users" value={fmtInt(d.totals.active_users)} />
            <StatCard label="direct play" value={pct(d.totals.direct_play_pct)} />
            <StatCard label="video transcode" value={pct(d.totals.video_transcode_pct)} mote />
          </div>

          <div className="grid gap-3 md:grid-cols-2">
            <ChartPanel title="when we watch">
              <Heatmap cells={d.heatmap} ariaLabel="Watch time by day and hour" />
            </ChartPanel>
            <ChartPanel title="play method by week">
              <MethodBars weeks={d.play_method_weekly} />
            </ChartPanel>
          </div>

          <div className="grid gap-3 md:grid-cols-3">
            <ChartPanel title="top series">
              <DataTable columns={titleCols} rows={d.top_series} getKey={(r) => r.series_id} empty="—" />
            </ChartPanel>
            <ChartPanel title="top movies">
              <DataTable columns={titleCols} rows={d.top_movies} getKey={(r) => r.item_id} empty="—" />
            </ChartPanel>
            <ChartPanel title="top episodes">
              <DataTable
                columns={titleCols}
                rows={d.top_episodes}
                getKey={(r) => r.item_id}
                empty="—"
              />
            </ChartPanel>
          </div>

          <ChartPanel title="active users">
            <DataTable
              columns={[
                { key: "name", header: "user", cell: (r) => r.name },
                { key: "hrs", header: "watch", align: "right", cell: (r) => hours(r.watch_sec) },
                { key: "plays", header: "plays", align: "right", cell: (r) => fmtInt(r.plays) },
                { key: "titles", header: "titles", align: "right", cell: (r) => fmtInt(r.distinct_titles) },
              ]}
              rows={d.active_users}
              getKey={(r) => r.user_id}
              empty="—"
            />
          </ChartPanel>
        </>
      )}

      {coreTable}
    </div>
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/watch",
  validateSearch: search,
  component: WatchStats,
});
```

- [ ] **Step 4: Run it, see it pass** — `npx vitest run src/routes/watch.test.tsx` → PASS. Then `npx vitest run`, `npx tsc -b`, `npx biome ci .`, `npm run build` → all clean. `touch dist/.gitkeep` after the build (Plan-1 gotcha: `vite build` wipes it).

- [ ] **Step 5: Commit**

```bash
cd .. && git add web && git commit -m "feat(web): Watch Stats page — panels, user filter, plugin-absent + empty states"
```

---

## Task 14: Docs, smoke test, finish

**Files:**
- Modify: `docs/schema-notes.md`, `README.md`, `CLAUDE.md`
- Create: `cmd/ephyra/main_test.go`

- [ ] **Step 1: `docs/schema-notes.md`** — add a section for the plugin + the new `jellyfin.db` reads, from spec §3: `playback_reporting.db` schema (no `RemoteAddress`, `PlayDuration` seconds, server-local timestamps), the id-format table (plugin dashless-lower vs core dashed-upper; canonical = dashless-lower), the `PlaybackMethod` → 5-bucket mapping, `UserData` (`GROUP BY ItemId,UserId` + `MAX`; never = `LastPlayedDate IS NULL` for all users), `Users`, episode→series via `BaseItems.SeriesId`.

- [ ] **Step 2: `README.md`** — in "What you ship / build" flip Watch Stats and Cleanup from "stub" to shipped (keep Now Playing as the last stub); in "Test against a real library" add a `playback_reporting.db` copy line next to `jellyfin.db` (same `docker cp` pattern); note the Watch Stats page needs the Playback Reporting plugin and degrades cleanly without it.

- [ ] **Step 3: `CLAUDE.md`** — under "Architecture" note: the `watch` job reads two DB copies (`playback_reporting.db` for events + `jellyfin.db` for name/series/user enrichment); the canonical id form in the store is dashless-lowercase; `watch_events_daily`/heatmap are **not** re-zoned by `TZ` (plugin writes server-local). Update the "Plan 1 of 3" line — Plan 2 done, Plan 3 (Now Playing) pending.

- [ ] **Step 4: Write the smoke test** — `cmd/ephyra/main_test.go`

```go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/api"
	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/scheduler"
	"github.com/andriykohut/ephyra/internal/source/file"
	"github.com/andriykohut/ephyra/internal/store"
	"github.com/andriykohut/ephyra/internal/testsupport"
	"log/slog"
	"io"
)

// bootServer wires the real store+scheduler+api against a data dir and runs both
// jobs once, synchronously.
func bootServer(t *testing.T, dataDir string) http.Handler {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "ephyra.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{
		JellyfinDataDir: dataDir,
		WorkDir:         t.TempDir(),
		RefreshLibrary:  30 * time.Minute,
		RefreshWatch:    10 * time.Minute,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	src := file.New(cfg, log)
	sc := scheduler.New(st, src, cfg, log)
	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatalf("library refresh: %v", err)
	}
	if err := sc.RunWatchOnce(ctx); err != nil {
		t.Fatalf("watch refresh: %v", err)
	}
	return api.New(api.Deps{Store: st, Cfg: cfg, Log: log, Trigger: sc}).Handler()
}

func get(t *testing.T, h http.Handler, path string) *http.Response {
	t.Helper()
	rr := newRecorder()
	h.ServeHTTP(rr, mustReq(t, path))
	return rr.Result()
}

func TestSmoke_WithPlugin(t *testing.T) {
	h := bootServer(t, testsupport.TwoDBLayout(t))

	for _, p := range []string{
		"/api/watch/stats", "/api/watch/stats?range=all", "/api/watch/stats?range=all&user=11111111222233334444555555555555",
		"/api/cleanup", "/api/cleanup?mode=stale",
	} {
		res := get(t, h, p)
		if res.StatusCode != 200 {
			t.Fatalf("%s -> %d", p, res.StatusCode)
		}
		var env map[string]json.RawMessage
		if err := json.NewDecoder(res.Body).Decode(&env); err != nil {
			t.Fatalf("%s: bad json: %v", p, err)
		}
		if _, ok := env["data"]; !ok {
			t.Fatalf("%s: no data key", p)
		}
	}
	// CSV
	res := get(t, h, "/api/cleanup?format=csv")
	if res.Header.Get("Content-Type") != "text/csv" {
		t.Fatalf("csv content-type %q", res.Header.Get("Content-Type"))
	}
}

func TestSmoke_PluginAbsent(t *testing.T) {
	// jellyfin.db only
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "data"), 0o755)
	lib := testsupport.LibraryFixtureDB(t)
	b, _ := os.ReadFile(lib)
	os.WriteFile(filepath.Join(dir, "data", "jellyfin.db"), b, 0o644)

	h := bootServer(t, dir)
	res := get(t, h, "/api/watch/stats")
	if res.StatusCode != 200 {
		t.Fatalf("watch stats -> %d", res.StatusCode)
	}
	var env struct {
		Data struct {
			PluginAvailable bool `json:"plugin_available"`
		} `json:"data"`
	}
	json.NewDecoder(res.Body).Decode(&env)
	if env.Data.PluginAvailable {
		t.Fatal("plugin should be absent")
	}
	if get(t, h, "/api/cleanup").StatusCode != 200 {
		t.Fatal("cleanup should be unaffected by the missing plugin")
	}
}
```

> `newRecorder` / `mustReq` are 3-line helpers (`httptest.NewRecorder`, `httptest.NewRequest(http.MethodGet, path, nil)`) — inline them or add to this file. `LibraryFixtureDB` returns a built `.db` file path; copying its bytes gives a usable `jellyfin.db` (it has no `PlaybackActivity`). If reading a live SQLite file mid-WAL is flaky in CI, instead point `bootServer` at a `TwoDBLayout`-style dir built from only the library fixture SQL.

- [ ] **Step 5: Full gate**

```
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./... && go vet ./... && golangci-lint run
cd web && npx tsc -b && npx biome ci . && npx vitest run && npm run build && touch dist/.gitkeep
```
All green.

- [ ] **Step 6: Commit**

```bash
git add docs/schema-notes.md README.md CLAUDE.md cmd/ephyra/main_test.go web/dist/.gitkeep
git commit -m "docs + smoke test for watch stats & cleanup"
```

- [ ] **Step 7: Finish the branch.** Use `superpowers:finishing-a-development-branch` — verify the full suite is green on `plan-2-watch-stats-cleanup`, present the merge menu, and (on the user's choice) merge to `main` or open a PR against `main`.

---

## Self-review notes

- **Spec coverage:** §3 → Task 14 (schema-notes) + primer; §4 → Task 1; §5.1 → Tasks 2–3; §5.2 → Task 8; §5.3 (no re-zoning) → Task 9 (`Watch` takes no `loc`) + `watch_test.go` literal-component assertions; §6 → Tasks 4, 9; §7 → Task 5; §8 → Task 10; §9.1 → Task 11; §9.2 → Task 6; §10.1 → Task 13; §10.2 → Task 7; §10.3 → Tasks 7, 12; §11 → Task 14; §12 → tests in every task + Task 14 smoke.
- **Types:** `WatchDailyRow`/`HeatmapRow`/`CleanupRow`/`CorePlayRow`/`UserRow` defined once in Task 1 (`internal/aggregate/rows.go`), consumed unchanged in Tasks 4, 9, 10. Store DTOs (`WatchStats` et al.) defined once in Task 11 and mirrored field-for-field in the TS `WatchStats` in Task 12.
- **`WriteLibraryAggregates` signature change** ripples to three callers — Task 1 Step 10 updates the tests/api, Task 5 Step 3 updates the scheduler. Any task run out of order must apply Task 1 Step 10 first (it's called out there).
- **Plan-1 gotcha carried forward:** `web/dist/.gitkeep` must be re-`touch`ed after every `npm run build` before committing (Tasks 13, 14).
