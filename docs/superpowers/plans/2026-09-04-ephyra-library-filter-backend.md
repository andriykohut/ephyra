# Library-Level Filtering — Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `library` filter to Library Overview, Watch Stats, Cleanup, and
Profiles — end to end through the Go backend (source → store → aggregate →
API) — so that `GET .../whatever?library=Movies` scopes every number in the
response to that Jellyfin library. Frontend wiring is a separate plan.

**Architecture:** Two mechanisms, matched to how each pipeline already works.
Library Overview and Profiles precompute everything at refresh time (no live
SQL filtering today), so the scheduler filters the source snapshot/events by
library *before* calling the existing pure aggregate functions, once per
library plus once for "All", then writes every scope in **one** transaction
per job run. Watch Stats is already filtered live via SQL `WHERE` at read
time (that's how `range`/`user` work today), so library becomes one more
denormalized column with no precompute change. Cleanup already stores
`library` per row — pure parameter wiring.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` (pure-Go, `CGO_ENABLED=0`),
`database/sql`, table-driven tests with SQLite fixtures under `testdata/`.

**Spec:** `docs/superpowers/specs/2026-09-04-ephyra-library-filter-design.md`
— this plan implements it task-by-task; read both together. Where this plan
narrows an implementation detail the spec left open (e.g. exact function
names), this plan is authoritative.

## Global Constraints

- `CGO_ENABLED=0` always — pure-Go SQLite only (`go test ./...` and
  `go build ./...` must both pass with it set).
- Migrations are forward-only, embedded, integer-prefixed filenames under
  `internal/store/migrations/`; a migration must never touch
  `schema_migrations` (the runner owns that table).
- `agg_*` tables are fully rewritten per refresh inside one transaction per
  job — never partially written, never read half-done.
- Every JSON list field serializes as `[]`, never `null` (`orEmpty` in
  `internal/store/reads.go`) — every new slice-returning read must run
  through it.
- `""` is the "All libraries" sentinel in every Go/SQL layer (matches how
  `user=""` already means "all users"). `"Unknown"` (capitalized) is a real
  bucket value, never the sentinel — it's what `LibraryItem.Library` already
  uses for an unresolved `TopParentId`.
- `make lint` (`go vet ./...` + `golangci-lint run`, v2) and `make test`
  (`CGO_ENABLED=0 go test ./...`) must stay green after every task.

---

## Task 1: Migration `0005_library_filter.sql`

**Files:**
- Create: `internal/store/migrations/0005_library_filter.sql`
- Test: `internal/store/migrations_test.go` (check if this file exists first —
  if migrations already have a generic "all migrations apply cleanly" test,
  extend it; otherwise this task's test lives in `internal/store/store_test.go`)

**Interfaces:**
- Produces: every table/column later tasks read and write. No Go code in this
  task — schema only. Later tasks assume these exact names.

This is the full schema change from the spec's Section 6, applied as one
migration. Every `agg_*`/`playback_events`-adjacent table gets a `library`
column; tables whose primary key already scopes to one item don't need it in
the PK, tables that aggregate across items do.

- [ ] **Step 1: Check how existing migrations are tested**

```bash
grep -rln "0004_tags\|ApplyMigrations\|runMigrations\|0003_profiles" internal/store/*_test.go
```

Read whatever file matches (likely `internal/store/store_test.go` or
`migrations_test.go`) to see the exact helper used to open a `Store` and
confirm it applies every migration. Reuse that helper — don't invent a new one.

- [ ] **Step 2: Write the migration file**

```sql
-- 0005_library_filter.sql
-- Adds a library dimension throughout: playback_events gets a resolved
-- library column (default 'Unknown', backfilled once by the watch job), and
-- every agg_* table that Library Overview / Watch Stats / Profiles write
-- gets a library scope column so a page can be filtered to one Jellyfin
-- library. '' is the "All libraries" sentinel everywhere; 'Unknown' is a
-- real bucket, never the sentinel. Forward-only; does not touch
-- schema_migrations.

ALTER TABLE playback_events ADD COLUMN library TEXT NOT NULL DEFAULT 'Unknown';

CREATE TABLE dim_library (name TEXT PRIMARY KEY);

DROP TABLE agg_totals;
CREATE TABLE agg_totals (
  library TEXT NOT NULL DEFAULT '',
  metric  TEXT NOT NULL,
  value   REAL NOT NULL,
  PRIMARY KEY (library, metric)
);

DROP TABLE agg_disk;
CREATE TABLE agg_disk (
  library   TEXT NOT NULL DEFAULT '',
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  bytes     INTEGER NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (library, dimension, bucket)
);

DROP TABLE agg_distribution;
CREATE TABLE agg_distribution (
  library   TEXT NOT NULL DEFAULT '',
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (library, dimension, bucket)
);

DROP TABLE agg_library_growth;
CREATE TABLE agg_library_growth (
  library     TEXT NOT NULL DEFAULT '',
  month       TEXT NOT NULL,
  added_items INTEGER NOT NULL,
  added_bytes INTEGER NOT NULL,
  cum_items   INTEGER NOT NULL,
  PRIMARY KEY (library, month)
);

DROP TABLE agg_library_tag_pairs;
CREATE TABLE agg_library_tag_pairs (
  library TEXT NOT NULL DEFAULT '',
  tag_a   TEXT NOT NULL,
  tag_b   TEXT NOT NULL,
  items   INTEGER NOT NULL,
  PRIMARY KEY (library, tag_a, tag_b)
);
CREATE INDEX ix_library_tag_pairs_a ON agg_library_tag_pairs (library, tag_a, items DESC);
CREATE INDEX ix_library_tag_pairs_b ON agg_library_tag_pairs (library, tag_b, items DESC);

ALTER TABLE watch_events_daily ADD COLUMN library TEXT NOT NULL DEFAULT '';
ALTER TABLE agg_played_core    ADD COLUMN library TEXT NOT NULL DEFAULT '';

DROP TABLE agg_watch_heatmap;
CREATE TABLE agg_watch_heatmap (
  user_id   TEXT NOT NULL,
  dow       INTEGER NOT NULL,
  hour      INTEGER NOT NULL,
  library   TEXT NOT NULL DEFAULT '',
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, dow, hour, library)
);

DROP TABLE agg_profile_summary;
CREATE TABLE agg_profile_summary (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  watch_sec INTEGER NOT NULL DEFAULT 0, plays INTEGER NOT NULL DEFAULT 0,
  distinct_titles INTEGER NOT NULL DEFAULT 0, days_active INTEGER NOT NULL DEFAULT 0,
  finished_pct REAL NOT NULL DEFAULT 0, bailed_pct REAL NOT NULL DEFAULT 0,
  rewatch_pct REAL NOT NULL DEFAULT 0,
  longest_binge_episodes INTEGER NOT NULL DEFAULT 0,
  longest_binge_series_name TEXT NOT NULL DEFAULT '',
  show_of_range_series_id TEXT NOT NULL DEFAULT '',
  show_of_range_series_name TEXT NOT NULL DEFAULT '',
  first_play TEXT NOT NULL DEFAULT '', last_play TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, range, library)
);

DROP TABLE agg_profile_completion;
CREATE TABLE agg_profile_completion (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL, bucket TEXT NOT NULL, count INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, library, scope, bucket)
);

DROP TABLE agg_profile_abandoned;
CREATE TABLE agg_profile_abandoned (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL, item_id TEXT NOT NULL, name TEXT NOT NULL,
  series_name TEXT NOT NULL DEFAULT '', bailed_count INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, library, scope, item_id)
);

DROP TABLE agg_profile_rewatch;
CREATE TABLE agg_profile_rewatch (
  user_id TEXT NOT NULL, library TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL,
  item_id TEXT NOT NULL, name TEXT NOT NULL, series_name TEXT NOT NULL DEFAULT '',
  watch_days INTEGER NOT NULL,
  PRIMARY KEY (user_id, library, scope, item_id)
);

DROP TABLE agg_profile_binge;
CREATE TABLE agg_profile_binge (
  user_id TEXT NOT NULL, library TEXT NOT NULL DEFAULT '', series_id TEXT NOT NULL,
  series_name TEXT NOT NULL, run_episodes INTEGER NOT NULL,
  run_start TEXT NOT NULL, run_end TEXT NOT NULL,
  PRIMARY KEY (user_id, library, series_id, run_start)
);

DROP TABLE agg_profile_taste;
CREATE TABLE agg_profile_taste (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  dim TEXT NOT NULL, key TEXT NOT NULL, watch_sec INTEGER NOT NULL, plays INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, library, dim, key)
);

DROP TABLE agg_taste_baseline;
CREATE TABLE agg_taste_baseline (
  library TEXT NOT NULL DEFAULT '', dim TEXT NOT NULL, key TEXT NOT NULL,
  watch_sec INTEGER NOT NULL,
  PRIMARY KEY (library, dim, key)
);

DROP TABLE agg_profile_tag_overlap;
CREATE TABLE agg_profile_tag_overlap (
  user_a TEXT NOT NULL, user_b TEXT NOT NULL, range TEXT NOT NULL,
  library TEXT NOT NULL DEFAULT '', cosine REAL NOT NULL, shared TEXT NOT NULL,
  PRIMARY KEY (user_a, user_b, range, library)
);
```

- [ ] **Step 3: Write a test that the migration applies cleanly**

If no generic "all migrations apply" test exists, add one:

```go
// internal/store/migrations_test.go
package store

import (
	"context"
	"testing"
)

func TestMigrations_ApplyCleanly(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatalf("open (runs all migrations): %v", err)
	}
	defer st.Close()

	for _, table := range []string{
		"dim_library", "agg_totals", "agg_disk", "agg_distribution",
		"agg_library_growth", "agg_library_tag_pairs", "agg_watch_heatmap",
		"agg_profile_summary", "agg_profile_completion", "agg_profile_abandoned",
		"agg_profile_rewatch", "agg_profile_binge", "agg_profile_taste",
		"agg_taste_baseline", "agg_profile_tag_overlap",
	} {
		var n int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, table,
		).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing after migrations", table)
		}
	}

	var col int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('playback_events') WHERE name = 'library'`,
	).Scan(&col); err != nil {
		t.Fatal(err)
	}
	if col != 1 {
		t.Error("playback_events.library column missing")
	}
}
```

(If `internal/store/store.go` doesn't expose `DB()`, check for the existing
accessor other tests use — `spine_test.go`'s `openStore` helper already calls
`st.DB()`, so it exists.)

- [ ] **Step 4: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestMigrations_ApplyCleanly -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/0005_library_filter.sql internal/store/migrations_test.go
git commit -m "$(cat <<'EOF'
feat(store): add library scope column to every agg_* table

Migration 0005 adds a library column (PK for tables that aggregate
across items) throughout, plus playback_events.library and a new
dim_library table. Schema-only; no reader/writer changes yet.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 2: Source — `LibrarySnapshot.SeriesCounts` per library

**Files:**
- Modify: `internal/source/source.go`
- Modify: `internal/source/file/queries.go`
- Modify: `internal/source/file/queries_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `source.LibrarySnapshot.SeriesCounts map[string]int` (library name
  → series count) — Task 6's `FilterLibrary` reads this.

`LibrarySnapshot.SeriesCount int` (global) becomes `SeriesCounts map[string]int`
so a library-filtered snapshot can report the right series count for its own
library. The existing fixture (`testdata/library.fixture.sql`) already has two
libraries — "Movies" (4 items, 0 series) and "Shows" (2 episodes, 1 series,
"Some Show") — so no fixture changes are needed.

- [ ] **Step 1: Update the failing test**

`internal/source/file/queries_test.go` currently asserts
`snap.SeriesCount != 1`. Change `TestDefaultQueryLibrary` (around line 27-29):

```go
	if len(snap.Items) != 6 || snap.SeriesCounts["Shows"] != 1 || len(snap.SeriesCounts) != 1 {
		t.Fatalf("items=%d seriesCounts=%v", len(snap.Items), snap.SeriesCounts)
	}
```

(Only "Shows" has a series in the fixture, so the map has exactly one entry.)

- [ ] **Step 2: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: compile error, `snap.SeriesCount` (old field) still referenced
elsewhere, or `SeriesCounts` undefined — either way, a failure that goes away
once both source files below are updated.

- [ ] **Step 3: `source.LibrarySnapshot.SeriesCounts`**

`internal/source/source.go`, in `LibrarySnapshot` (around line 61-67):

```go
type LibrarySnapshot struct {
	GeneratedAt time.Time
	Items       []LibraryItem
	SeriesCounts map[string]int // library name -> series count
	Users       []UserRef
	UserPlays   []UserPlay
}
```

- [ ] **Step 4: Extract the shared `folders` CTE and group the series-count query by library**

`internal/source/file/queries.go`: pull the `folders AS (...)` block out of
`libraryQuery` (currently inlined at lines 44-51) into its own constant, and
reuse it in both `libraryQuery` and the new series-count query:

```go
const foldersCTE = `
folders AS (
  SELECT phys.Id AS fid, COALESCE(coll.Name, phys.Name) AS lib
  FROM BaseItems phys
  LEFT JOIN BaseItems coll
    ON coll.Type = '` + collectionFolderType + `'
   AND lower(coll.Name) = lower(phys.Name)
  WHERE phys.Type IN ('` + folderType + `', '` + collectionFolderType + `')
)`

const libraryQuery = `
WITH pvs AS (
  SELECT ItemId,
         Codec         AS codec,
         Width         AS width,
         ColorTransfer AS color_transfer,
         DvProfile     AS dv_profile,
         ROW_NUMBER() OVER (PARTITION BY ItemId ORDER BY StreamIndex) AS rn
  FROM MediaStreamInfos
  WHERE StreamType = 1
),` + foldersCTE + `
SELECT
  i.Name,
  i.Type,
  COALESCE(i.Size, 0),
  COALESCE(i.RunTimeTicks, 0),
  COALESCE(i.DateCreated, ''),
  COALESCE(i.ProductionYear, 0),
  COALESCE(i.Genres, ''),
  COALESCE(i.Tags, ''),
  COALESCE(f.lib, 'Unknown'),
  COALESCE(i.Path, ''),
  v.codec, v.width, v.color_transfer, v.dv_profile,
  i.Id, COALESCE(i.SeriesId, ''), COALESCE(i.SeriesName, '')
FROM BaseItems i
LEFT JOIN pvs v     ON v.ItemId = i.Id AND v.rn = 1
LEFT JOIN folders f ON f.fid = i.TopParentId
WHERE i.Type IN (?, ?)
  AND COALESCE(i.IsVirtualItem, 0) = 0
  AND COALESCE(i.IsFolder, 0) = 0
`

const seriesCountQuery = `
WITH ` + foldersCTE + `
SELECT COALESCE(f.lib, 'Unknown'), count(*)
FROM BaseItems s
LEFT JOIN folders f ON f.fid = s.TopParentId
WHERE s.Type = ? AND COALESCE(s.IsVirtualItem, 0) = 0
GROUP BY f.lib
`
```

Replace the body at the end of `defaultQueryLibrary` (currently lines ~174-178):

```go
	snap.SeriesCounts = map[string]int{}
	srows, err := db.Query(seriesCountQuery, seriesType)
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	for srows.Next() {
		var lib string
		var n int
		if err := srows.Scan(&lib, &n); err != nil {
			srows.Close()
			return source.LibrarySnapshot{}, err
		}
		snap.SeriesCounts[lib] = n
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return source.LibrarySnapshot{}, err
	}
	return snap, nil
```

- [ ] **Step 5: Fix every other reference to `SeriesCount`**

```bash
grep -rn "SeriesCount\b" --include=*.go . | grep -v SeriesCounts
```

Every hit outside this task's two files needs updating too — expect at least
`internal/aggregate/library.go` (`totals["items.Series"] = float64(snap.SeriesCount)`,
around line 71) and its test fixtures in `internal/aggregate/library_test.go`.
Change the aggregate.go line to sum the map:

```go
	var seriesTotal int
	for _, n := range snap.SeriesCounts {
		seriesTotal += n
	}
	totals := map[string]float64{
		...
		"items.Series": float64(seriesTotal),
		...
	}
```

Update any in-code test snapshot in `library_test.go` that sets
`SeriesCount: N` to `SeriesCounts: map[string]int{"SomeLib": N}` instead —
grep for `SeriesCount:` to find them.

- [ ] **Step 6: Run the full build and the source/file + aggregate tests**

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./internal/source/... ./internal/aggregate/... -v
```

Expected: build succeeds, all tests PASS (including the updated
`TestDefaultQueryLibrary`).

- [ ] **Step 7: Commit**

```bash
git add internal/source/source.go internal/source/file/queries.go \
        internal/source/file/queries_test.go internal/aggregate/library.go \
        internal/aggregate/library_test.go
git commit -m "$(cat <<'EOF'
feat(source): resolve series count per library

LibrarySnapshot.SeriesCount becomes SeriesCounts map[string]int so a
library-filtered snapshot can report its own series count. Extracts
the shared folders CTE out of libraryQuery so the new grouped
series-count query can reuse it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 3: Source — `PlaybackEvent.Library` resolution

**Files:**
- Modify: `internal/source/source.go`
- Modify: `internal/source/file/playback.go`
- Modify: `internal/source/file/file_test.go`

**Interfaces:**
- Consumes: `foldersCTE`, `movieType`/`episodeType` constants from Task 2
  (same package, `internal/source/file`).
- Produces: `source.PlaybackEvent.Library string` — Task 4 persists it,
  Task 9/12 read it.

Each event's library is resolved from **its own** item id's `TopParentId` —
unlike tags, which inherit from the parent series for episodes. An episode
physically sits under the same library folder as its series, so there's no
inheritance step: library is a location, not a taste facet.

- [ ] **Step 1: Write the failing test**

`internal/source/file/file_test.go`, new test (the fixture already has Alpha/
Bravo/Delta in "Movies", the S1E1/S1E2 episodes in "Shows", and the
`deadbeef...` id that isn't in `BaseItems` at all):

```go
func TestPlaybackEvents_Library(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	events, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var sawMovie, sawEpisode, sawDeleted bool
	for _, e := range events {
		switch e.ItemID {
		case "0000000000000000000000000000000a", "0000000000000000000000000000000b", "0000000000000000000000000000000d":
			sawMovie = true
			if e.Library != "Movies" {
				t.Errorf("item %s library = %q, want Movies", e.ItemID, e.Library)
			}
		case "000000000000000000000000000000e1", "000000000000000000000000000000e2":
			sawEpisode = true
			if e.Library != "Shows" {
				t.Errorf("episode %s library = %q, want Shows", e.ItemID, e.Library)
			}
		case "deadbeefdeadbeefdeadbeefdeadbeef":
			sawDeleted = true
			if e.Library != "Unknown" {
				t.Errorf("deleted item library = %q, want Unknown", e.Library)
			}
		}
	}
	if !sawMovie || !sawEpisode || !sawDeleted {
		t.Fatalf("missing cases: movie=%v episode=%v deleted=%v", sawMovie, sawEpisode, sawDeleted)
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/source/file/... -run TestPlaybackEvents_Library -v
```

Expected: FAIL — `e.Library` is `""` for every case (field doesn't exist yet /
zero-valued), not `"Movies"`/`"Shows"`/`"Unknown"`.

- [ ] **Step 3: `source.PlaybackEvent.Library`**

`internal/source/source.go`, in `PlaybackEvent` (around line 72-86), add:

```go
	Library string // resolved folder name, or "Unknown" — see LibraryItem.Library
```

- [ ] **Step 4: Resolve it in `enrichPlaybackEvents`**

`internal/source/file/playback.go`: the `items` query (lines 82-87) needs
`TopParentId` and a join to `folders`; `itemInfo` gains a `library` field.

```go
	type itemInfo struct {
		name, seriesID, seriesName, genres, tags, library string
		runtimeTicks                                      int64
		year                                               int
	}
	items := map[string]itemInfo{}
	irows, err := jdb.Query(`
		WITH ` + foldersCTE + `
		SELECT lower(replace(i.Id,'-','')), COALESCE(i.Name,''),
		       lower(replace(COALESCE(i.SeriesId,''),'-','')), COALESCE(i.SeriesName,''),
		       COALESCE(i.RunTimeTicks,0), COALESCE(i.ProductionYear,0), COALESCE(i.Genres,''), COALESCE(i.Tags,''),
		       COALESCE(f.lib, 'Unknown')
		FROM BaseItems i
		LEFT JOIN folders f ON f.fid = i.TopParentId
		WHERE i.Type IN ('` + movieType + `', '` + episodeType + `')`)
	if err != nil {
		return err
	}
	for irows.Next() {
		var id string
		var info itemInfo
		if err := irows.Scan(&id, &info.name, &info.seriesID, &info.seriesName,
			&info.runtimeTicks, &info.year, &info.genres, &info.tags, &info.library); err != nil {
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

Then in the enrichment loop (around line 125-144), set the default before the
lookup and copy `info.library` when found:

```go
	for i := range events {
		events[i].Library = "Unknown"
		if n, ok := users[events[i].UserID]; ok {
			events[i].UserName = n
		}
		if info, ok := items[events[i].ItemID]; ok {
			if info.name != "" {
				events[i].ItemName = info.name
			}
			events[i].SeriesID = info.seriesID
			events[i].SeriesName = info.seriesName
			events[i].ItemRuntimeSec = info.runtimeTicks / 10_000_000
			events[i].ItemYear = info.year
			events[i].ItemGenres = splitGenres(info.genres)
			events[i].Library = info.library
			if events[i].ItemType == "episode" {
				events[i].ItemTags = splitTags(seriesTags[info.seriesID])
			} else {
				events[i].ItemTags = splitTags(info.tags)
			}
		}
	}
	return nil
}
```

- [ ] **Step 5: Run the test again**

```bash
CGO_ENABLED=0 go test ./internal/source/file/... -run TestPlaybackEvents_Library -v
```

Expected: PASS.

- [ ] **Step 6: Run the full package test suite**

```bash
CGO_ENABLED=0 go test ./internal/source/... -v
```

Expected: all PASS, including the pre-existing `TestPlaybackEvents_*` tests
(unaffected by this change).

- [ ] **Step 7: Commit**

```bash
git add internal/source/source.go internal/source/file/playback.go internal/source/file/file_test.go
git commit -m "$(cat <<'EOF'
feat(source): resolve library on every playback event

PlaybackEvent.Library is resolved from the event's own item id (never
inherited from a parent series, unlike tags — library is a location
every item already sits in directly). Unmatched items default to
"Unknown".

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 4: Store — `playback_events.library` round-trip

**Files:**
- Modify: `internal/store/spine.go`
- Modify: `internal/store/spine_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent.Library` (Task 3).
- Produces: `playback_events.library` persisted and read back — Task 5's
  backfill and Task 12/13's Profiles pipeline depend on this.

- [ ] **Step 1: Write the failing test**

`internal/store/spine_test.go`, extend the `ev()` helper's zero-value case (or
add a new small test) — simplest is a dedicated round-trip test:

```go
func TestAppendPlaybackEvents_Library(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)
	e.Library = "Movies"
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Library != "Movies" {
		t.Fatalf("got %+v", got)
	}

	// re-report with a different library (e.g. item moved) refreshes it
	e.Library = "Shows"
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e}); err != nil {
		t.Fatal(err)
	}
	got, err = st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Library != "Shows" {
		t.Fatalf("library not refreshed on conflict: %+v", got)
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestAppendPlaybackEvents_Library -v
```

Expected: FAIL — `got[0].Library` is `""`, `library` isn't inserted or read.

- [ ] **Step 3: Insert, refresh-on-conflict, and read `library`**

`internal/store/spine.go`, `AppendPlaybackEvents` (lines 72-93):

```go
	const q = `
		INSERT INTO playback_events
		  (at, user_id, item_id, item_type, method, play_duration_sec,
		   item_name, series_id, series_name, item_runtime_sec, item_year, item_genres, item_tags, library, dedup_hash)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dedup_hash) DO UPDATE SET
		  item_name        = excluded.item_name,
		  series_id        = excluded.series_id,
		  series_name      = excluded.series_name,
		  item_runtime_sec = excluded.item_runtime_sec,
		  item_year        = excluded.item_year,
		  item_genres      = excluded.item_genres,
		  item_tags        = excluded.item_tags,
		  library          = excluded.library`
	for _, e := range evs {
		if _, err := tx.ExecContext(ctx, q,
			formatSpineTime(e.At), e.UserID, e.ItemID, e.ItemType, e.Method, e.PlayDurationSec,
			e.ItemName, e.SeriesID, e.SeriesName,
			e.ItemRuntimeSec, e.ItemYear, joinGenres(e.ItemGenres), joinTags(e.ItemTags), e.Library, spineDedupHash(e),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
```

`ReadPlaybackEvents` (lines 100-125):

```go
func (s *Store) ReadPlaybackEvents(ctx context.Context) ([]source.PlaybackEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT at, user_id, item_id, item_type, method, play_duration_sec,
		       item_name, series_id, series_name, item_runtime_sec, item_year, item_genres, item_tags, library
		FROM playback_events ORDER BY at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []source.PlaybackEvent
	for rows.Next() {
		var e source.PlaybackEvent
		var atRaw, genres, tags string
		if err := rows.Scan(&atRaw, &e.UserID, &e.ItemID, &e.ItemType, &e.Method,
			&e.PlayDurationSec, &e.ItemName, &e.SeriesID, &e.SeriesName,
			&e.ItemRuntimeSec, &e.ItemYear, &genres, &tags, &e.Library); err != nil {
			return nil, err
		}
		e.At = parseSpineTime(atRaw)
		e.ItemGenres = splitGenres(genres)
		e.ItemTags = splitTags(tags)
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run the test again**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestAppendPlaybackEvents_Library -v
```

Expected: PASS.

- [ ] **Step 5: Run the whole store suite**

```bash
CGO_ENABLED=0 go test ./internal/store/... -v
```

Expected: all PASS (existing spine tests unaffected — they never set
`Library`, so it round-trips as `""` for them, which is fine since those
tests don't assert on it).

- [ ] **Step 6: Commit**

```bash
git add internal/store/spine.go internal/store/spine_test.go
git commit -m "$(cat <<'EOF'
feat(store): persist library on the playback spine

playback_events.library round-trips through AppendPlaybackEvents (with
refresh-on-conflict, like the other enrichable columns) and
ReadPlaybackEvents.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 5: One-time backfill of pre-existing "Unknown" spine rows

**Files:**
- Modify: `internal/source/source.go`
- Modify: `internal/source/file/playback.go`
- Modify: `internal/source/file/file_test.go`
- Modify: `internal/store/spine.go`
- Modify: `internal/store/spine_test.go`
- Modify: `internal/scheduler/scheduler.go`
- Modify: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `playback_events.library` (Task 4).
- Produces: `source.LibraryResolver` optional interface (mirrors the existing
  `MTimer` pattern), `FileSource.ResolveLibraries`, `Store.ItemsWithUnknownLibrary`,
  `Store.UpdatePlaybackLibraries` — used only by the scheduler wiring in this
  task; no later task depends on these directly.

Rows already in the spine before this feature shipped default to `'Unknown'`
(the migration's column default). `enrichPlaybackEvents` (Task 3) only
re-resolves events the Playback Reporting plugin **currently** reports — once
an old play ages out of the plugin's retention, it's never touched by that
path again. So a genuinely one-time backfill pass is needed: on first run
after this ships, resolve every spine row still at `'Unknown'` against the
current `jellyfin.db` copy directly, by item id, independent of what the
plugin currently reports.

- [ ] **Step 1: Write the failing test for `FileSource.ResolveLibraries`**

`internal/source/file/file_test.go`:

```go
func TestResolveLibraries(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	resolved, err := f.ResolveLibraries(context.Background(), []string{
		"0000000000000000000000000000000a", // Alpha, Movies
		"000000000000000000000000000000e1", // S1E1, Shows
		"nonexistentitemidxxxxxxxxxxxxxxx",  // not in BaseItems at all
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved["0000000000000000000000000000000a"] != "Movies" {
		t.Errorf("Alpha: %q", resolved["0000000000000000000000000000000a"])
	}
	if resolved["000000000000000000000000000000e1"] != "Shows" {
		t.Errorf("S1E1: %q", resolved["000000000000000000000000000000e1"])
	}
	if _, ok := resolved["nonexistentitemidxxxxxxxxxxxxxxx"]; ok {
		t.Error("unmatched id should not appear in the result map at all")
	}
}
```

- [ ] **Step 2: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: `f.ResolveLibraries` undefined.

- [ ] **Step 3: Implement `ResolveLibraries`**

`internal/source/source.go`, near `MTimer` (around line 94-98):

```go
// LibraryResolver is implemented by sources that can re-resolve a set of item
// ids' library names after the fact (the watch job's one-time backfill for
// spine rows the plugin no longer reports). A future APISource that can't do
// this simply doesn't implement it — the backfill becomes a no-op.
type LibraryResolver interface {
	ResolveLibraries(ctx context.Context, itemIDs []string) (map[string]string, error)
}
```

`internal/source/file/playback.go`, new function + method:

```go
func resolveLibraries(jdb *sql.DB, itemIDs []string) (map[string]string, error) {
	if len(itemIDs) == 0 {
		return map[string]string{}, nil
	}
	placeholders := make([]string, len(itemIDs))
	args := make([]any, len(itemIDs))
	for i, id := range itemIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `
		WITH ` + foldersCTE + `
		SELECT lower(replace(i.Id,'-','')), COALESCE(f.lib, 'Unknown')
		FROM BaseItems i
		LEFT JOIN folders f ON f.fid = i.TopParentId
		WHERE lower(replace(i.Id,'-','')) IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := jdb.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, lib string
		if err := rows.Scan(&id, &lib); err != nil {
			return nil, err
		}
		out[id] = lib
	}
	return out, rows.Err()
}
```

`internal/source/file/file.go`, new exported method on `*FileSource` (near
`PlaybackEvents`):

```go
// ResolveLibraries backfills library names for a set of item ids against the
// current jellyfin.db copy, independent of what the Playback Reporting
// plugin currently reports. Ids not found in BaseItems are simply absent
// from the result map — best effort, no error.
func (f *FileSource) ResolveLibraries(ctx context.Context, itemIDs []string) (map[string]string, error) {
	jdb, cleanup, err := f.openForRead(ctx, "library")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return resolveLibraries(jdb, itemIDs)
}
```

- [ ] **Step 4: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/source/file/... -run TestResolveLibraries -v
```

Expected: PASS.

- [ ] **Step 5: Write the failing store test for the backfill query helpers**

`internal/store/spine_test.go`:

```go
func TestItemsWithUnknownLibrary_And_UpdatePlaybackLibraries(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	e1 := ev(time.Date(2025, 1, 6, 20, 30, 0, 0, time.UTC), "u1", "m1", "movie", 3600)
	e1.Library = "Unknown"
	e2 := ev(time.Date(2025, 1, 6, 21, 30, 0, 0, time.UTC), "u1", "m2", "movie", 1800)
	e2.Library = "Movies" // already resolved, should not come back
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{e1, e2}); err != nil {
		t.Fatal(err)
	}

	unknown, err := st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 1 || unknown[0] != "m1" {
		t.Fatalf("unknown = %v, want [m1]", unknown)
	}

	if err := st.UpdatePlaybackLibraries(ctx, map[string]string{"m1": "Movies"}); err != nil {
		t.Fatal(err)
	}
	unknown, err = st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("still unknown after update: %v", unknown)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		if e.ItemID == "m1" && e.Library != "Movies" {
			t.Errorf("m1 library not updated: %q", e.Library)
		}
	}
}
```

- [ ] **Step 6: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: `st.ItemsWithUnknownLibrary` / `st.UpdatePlaybackLibraries` undefined.

- [ ] **Step 7: Implement the store helpers**

`internal/store/spine.go`, add:

```go
// ItemsWithUnknownLibrary returns distinct item ids whose spine rows are
// still at the 'Unknown' library sentinel — candidates for the watch job's
// one-time backfill pass.
func (s *Store) ItemsWithUnknownLibrary(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT item_id FROM playback_events WHERE library = 'Unknown'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// UpdatePlaybackLibraries sets library on every spine row for each item id in
// resolved. One transaction; a no-op for ids not present in resolved.
func (s *Store) UpdatePlaybackLibraries(ctx context.Context, resolved map[string]string) error {
	if len(resolved) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, lib := range resolved {
		if _, err := tx.ExecContext(ctx,
			`UPDATE playback_events SET library = ? WHERE item_id = ?`, lib, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 8: Run the store test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestItemsWithUnknownLibrary_And_UpdatePlaybackLibraries -v
```

Expected: PASS.

- [ ] **Step 9: Wire the backfill into `RunWatchOnce`**

Read `internal/scheduler/scheduler.go` around lines 153-179 first (`RunWatchOnce`)
to confirm line numbers haven't shifted from earlier tasks, then insert the
backfill pass right after `AppendPlaybackEvents` succeeds and before
`ReadPlaybackEvents`:

```go
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
```

- [ ] **Step 10: Write the scheduler-level test**

`internal/scheduler/scheduler_test.go` — read the existing
`TestRunWatchOnce_PopulatesWatchTables` (around line 191) first to match its
setup style (it already builds a `Scheduler` over `testsupport.TwoDBLayout`),
then add:

```go
func TestRunWatchOnce_BackfillsUnknownLibrary(t *testing.T) {
	sch, st := newScheduler(t) // reuse whatever helper the existing tests use
	ctx := context.Background()

	if err := sch.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sch.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-migration row: force one spine item back to Unknown, as
	// if it predated this feature.
	if _, err := st.DB().ExecContext(ctx,
		`UPDATE playback_events SET library = 'Unknown' WHERE item_id = '0000000000000000000000000000000a'`); err != nil {
		t.Fatal(err)
	}

	// A second watch run should resolve it via the backfill pass (the item
	// is still in the jellyfin.db fixture, so it's resolvable).
	if err := sch.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, err := st.ItemsWithUnknownLibrary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range unknown {
		if id == "0000000000000000000000000000000a" {
			t.Fatal("Alpha should have been backfilled to Movies")
		}
	}
}
```

If `newScheduler`/equivalent doesn't exist under that name, use whatever setup
helper `TestRunWatchOnce_PopulatesWatchTables` actually uses — read it first,
don't guess.

- [ ] **Step 11: Run it**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -run TestRunWatchOnce_BackfillsUnknownLibrary -v
```

Expected: PASS.

- [ ] **Step 12: Run the full scheduler + source + store suites**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... ./internal/source/... ./internal/store/... -v
```

Expected: all PASS.

- [ ] **Step 13: Commit**

```bash
git add internal/source/source.go internal/source/file/playback.go internal/source/file/file.go \
        internal/source/file/file_test.go internal/store/spine.go internal/store/spine_test.go \
        internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "$(cat <<'EOF'
feat(scheduler): backfill Unknown library on pre-existing spine rows

One-time pass on the watch job: resolve any spine row still at the
'Unknown' sentinel against the current jellyfin.db copy directly, by
item id, independent of what the Playback Reporting plugin currently
reports (which only covers events still inside its retention window).
Best effort — items no longer in Jellyfin stay Unknown.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 6: Aggregate — `FilterLibrary` + `DistinctItemLibraries`

**Files:**
- Modify: `internal/aggregate/library.go`
- Modify: `internal/aggregate/library_test.go`

**Interfaces:**
- Consumes: `source.LibrarySnapshot` (Task 2's `SeriesCounts`).
- Produces: `aggregate.FilterLibrary(snap, lib) source.LibrarySnapshot`,
  `aggregate.DistinctItemLibraries(items []source.LibraryItem) []string` —
  Task 8's scheduler wiring calls both.

`aggregate.Library(snap, loc)` itself stays untouched — these are new pure
helpers the *caller* uses to get a filtered snapshot before invoking it, so
the existing, already-tested `Library()` needs no changes at all.

- [ ] **Step 1: Write the failing test**

`internal/aggregate/library_test.go` — read the existing snapshot-building
test helper first (the file already builds an in-code `source.LibrarySnapshot`
with items across two libraries per the design doc's testing notes), then add:

```go
func TestFilterLibrary(t *testing.T) {
	snap := source.LibrarySnapshot{
		Items: []source.LibraryItem{
			{ID: "a", Library: "Movies", Type: "movie"},
			{ID: "b", Library: "Movies", Type: "movie"},
			{ID: "c", Library: "Shows", Type: "episode"},
		},
		SeriesCounts: map[string]int{"Shows": 2, "Movies": 0},
	}

	all := FilterLibrary(snap, "")
	if len(all.Items) != 3 {
		t.Fatalf("empty filter should return everything, got %d items", len(all.Items))
	}

	movies := FilterLibrary(snap, "Movies")
	if len(movies.Items) != 2 || movies.SeriesCount != 0 {
		t.Fatalf("movies: items=%d series=%d", len(movies.Items), movies.SeriesCount)
	}
	shows := FilterLibrary(snap, "Shows")
	if len(shows.Items) != 1 || shows.SeriesCount != 2 {
		t.Fatalf("shows: items=%d series=%d", len(shows.Items), shows.SeriesCount)
	}
}

func TestDistinctItemLibraries(t *testing.T) {
	items := []source.LibraryItem{
		{Library: "Movies"}, {Library: "Shows"}, {Library: "Movies"}, {Library: "Unknown"},
	}
	got := DistinctItemLibraries(items)
	want := map[string]bool{"Movies": true, "Shows": true, "Unknown": true}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected library %q", g)
		}
	}
}
```

- [ ] **Step 2: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: `FilterLibrary` / `DistinctItemLibraries` undefined.

- [ ] **Step 3: Implement both**

`internal/aggregate/library.go`, near the top (after imports):

```go
// FilterLibrary returns a snapshot containing only items in lib, with
// SeriesCounts narrowed to lib's own count. lib == "" returns snap unchanged
// (the "All libraries" scope). Used by the scheduler to compute a
// per-library LibraryAggregates by calling the unmodified Library() function
// once per library, rather than threading a filter through it.
func FilterLibrary(snap source.LibrarySnapshot, lib string) source.LibrarySnapshot {
	if lib == "" {
		return snap
	}
	out := snap
	out.Items = nil
	for _, it := range snap.Items {
		if it.Library == lib {
			out.Items = append(out.Items, it)
		}
	}
	out.SeriesCounts = map[string]int{lib: snap.SeriesCounts[lib]}
	return out
}

// DistinctItemLibraries returns the unique Library values across items, in no
// particular order.
func DistinctItemLibraries(items []source.LibraryItem) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if !seen[it.Library] {
			seen[it.Library] = true
			out = append(out, it.Library)
		}
	}
	return out
}
```

This matches Task 2 Step 5, which changed `library.go` to sum
`snap.SeriesCounts` into `totals["items.Series"]` — a filtered snapshot with
`SeriesCounts` narrowed to just `lib`'s own count naturally sums to the right
value for that library.

- [ ] **Step 4: Run the tests**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -run 'TestFilterLibrary|TestDistinctItemLibraries' -v
```

Expected: PASS.

- [ ] **Step 5: Run the whole aggregate suite**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/aggregate/library.go internal/aggregate/library_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): add FilterLibrary + DistinctItemLibraries helpers

Pure helpers the scheduler uses to compute a per-library
LibraryAggregates by calling the existing, unmodified Library()
function once per library rather than threading a filter through it.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 7: Store — per-library `WriteLibraryAggregates` + `ReadLibraryOverview`

**Files:**
- Modify: `internal/store/writes.go`
- Modify: `internal/store/reads.go`
- Modify: `internal/store/reads_cleanup_test.go` (existing call site of
  `WriteLibraryAggregates` needs its signature updated)
- Test: `internal/store/writes_test.go` (create if it doesn't exist — check
  first) or extend whatever file already covers `WriteLibraryAggregates`

**Interfaces:**
- Consumes: `aggregate.LibraryAggregates` (unchanged), `aggregate.CleanupRow`/
  `UserRow`/`CorePlayRow` (unchanged).
- Produces: `Store.WriteLibraryAggregates(ctx, scoped map[string]aggregate.LibraryAggregates, cleanup, users, core) error`,
  `Store.ReadLibraryOverview(ctx, library string) (LibraryOverview, error)` —
  Task 8's scheduler wiring and Task 17's API handler depend on these exact
  signatures.

One call writes every library's scope (including `""` for All) in a single
transaction with a single blanket `DELETE` per table — never one `Write*` call
per library, which would have each call's `DELETE` erase the previous
library's just-written rows.

- [ ] **Step 1: Find and read the existing test for `WriteLibraryAggregates`/`ReadLibraryOverview`**

```bash
grep -rln "WriteLibraryAggregates\|ReadLibraryOverview" internal/store/*_test.go
```

Read whatever test file(s) come back in full before writing anything — this
task rewrites their `WriteLibraryAggregates(...)` calls to the new signature.

- [ ] **Step 2: Write the failing round-trip test**

Add to whichever file Step 1 identified as the primary one (or create
`internal/store/writes_library_test.go` if none exists):

```go
func TestWriteAndReadLibraryAggregates_PerLibrary(t *testing.T) {
	ctx := context.Background()
	st := openStore(t) // reuse the existing helper from spine_test.go / similar

	scoped := map[string]aggregate.LibraryAggregates{
		"": {
			Totals:         map[string]float64{"items.total": 3},
			ItemsByLibrary: []aggregate.LabeledCount{{Label: "Movies", Count: 2}, {Label: "Shows", Count: 1}},
			GenresTop:      []aggregate.LabeledCount{{Label: "Drama", Count: 3}},
		},
		"Movies": {
			Totals:    map[string]float64{"items.total": 2},
			GenresTop: []aggregate.LabeledCount{{Label: "Drama", Count: 2}},
		},
		"Shows": {
			Totals:    map[string]float64{"items.total": 1},
			GenresTop: []aggregate.LabeledCount{{Label: "Drama", Count: 1}},
		},
	}
	if err := st.WriteLibraryAggregates(ctx, scoped, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	all, err := st.ReadLibraryOverview(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if all.Totals.Items != 3 || len(all.GenresTop) != 1 || all.GenresTop[0].Count != 3 {
		t.Fatalf("all: %+v", all)
	}

	movies, err := st.ReadLibraryOverview(ctx, "Movies")
	if err != nil {
		t.Fatal(err)
	}
	if movies.Totals.Items != 2 || movies.GenresTop[0].Count != 2 {
		t.Fatalf("movies: %+v", movies)
	}

	shows, err := st.ReadLibraryOverview(ctx, "Shows")
	if err != nil {
		t.Fatal(err)
	}
	if shows.Totals.Items != 1 || shows.GenresTop[0].Count != 1 {
		t.Fatalf("shows: %+v", shows)
	}

	var libs []string
	rows, err := st.DB().QueryContext(ctx, `SELECT name FROM dim_library ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		libs = append(libs, n)
	}
	if len(libs) != 2 || libs[0] != "Movies" || libs[1] != "Shows" {
		t.Fatalf("dim_library = %v", libs)
	}
}
```

- [ ] **Step 3: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: signature mismatch — `WriteLibraryAggregates` still takes a single
`aggregate.LibraryAggregates`, `ReadLibraryOverview` doesn't take a `library`
argument.

- [ ] **Step 4: Rewrite `WriteLibraryAggregates`**

`internal/store/writes.go` (currently lines 13-120):

```go
// WriteLibraryAggregates replaces every library-derived row in one
// transaction, so a reader never sees a half-written set — including every
// library's scope: scoped["" ] is "All libraries", every other key is one
// Jellyfin library. cleanup/users/core are not library-scoped (Cleanup and
// Watch Stats get their own filtering elsewhere).
func (s *Store) WriteLibraryAggregates(ctx context.Context, scoped map[string]aggregate.LibraryAggregates,
	cleanup []aggregate.CleanupRow, users []aggregate.UserRow, core []aggregate.CorePlayRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_totals`,
		`DELETE FROM agg_disk`,
		`DELETE FROM agg_distribution WHERE dimension IN ('genre','decade','library_items','tag')`,
		`DELETE FROM agg_library_growth`,
		`DELETE FROM agg_library_tag_pairs`,
		`DELETE FROM agg_cleanup`,
		`DELETE FROM dim_user`,
		`DELETE FROM dim_library`,
		`DELETE FROM agg_played_core`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for lib, a := range scoped {
		if lib != "" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO dim_library (name) VALUES (?)`, lib); err != nil {
				return err
			}
		}
		if err := insertTotals(ctx, tx, lib, a.Totals); err != nil {
			return err
		}

		disks := []struct {
			name string
			data []aggregate.DiskBucket
		}{
			{"resolution", a.DiskByResolution},
			{"codec", a.DiskByCodec},
			{"container", a.DiskByContainer},
			{"library", a.DiskByLibrary},
		}
		for _, d := range disks {
			for _, b := range d.data {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO agg_disk (library, dimension, bucket, bytes, items) VALUES (?,?,?,?,?)`,
					lib, d.name, b.Bucket, b.Bytes, b.Items); err != nil {
					return err
				}
			}
		}

		distros := []struct {
			name string
			data []aggregate.LabeledCount
		}{
			{"genre", a.GenresTop},
			{"decade", a.ByDecade},
			{"library_items", a.ItemsByLibrary},
			{"tag", a.TagsTop},
		}
		for _, d := range distros {
			for _, lc := range d.data {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO agg_distribution (library, dimension, bucket, items) VALUES (?,?,?,?)`,
					lib, d.name, lc.Label, lc.Count); err != nil {
					return err
				}
			}
		}

		for _, g := range a.Growth {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_library_growth (library, month, added_items, added_bytes, cum_items) VALUES (?,?,?,?,?)`,
				lib, g.Month, g.AddedItems, g.AddedBytes, g.CumItems); err != nil {
				return err
			}
		}

		for _, p := range a.TagPairs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_library_tag_pairs (library, tag_a, tag_b, items) VALUES (?,?,?,?)`,
				lib, p.A, p.B, p.Items); err != nil {
				return err
			}
		}
	}

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
			INSERT INTO agg_played_core (user_id, scope, item_id, name, play_count, last_played_at, library)
			VALUES (?,?,?,?,?,?,?)`,
			p.UserID, p.Scope, p.ItemID, p.Name, p.PlayCount, nullif(p.LastPlayedAt), p.Library,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func insertTotals(ctx context.Context, tx *sql.Tx, library string, totals map[string]float64) error {
	for k, v := range totals {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_totals (library, metric, value) VALUES (?,?,?)`, library, k, v); err != nil {
			return err
		}
	}
	return nil
}
```

(`agg_played_core`'s `library` column and `CorePlayRow.Library` don't exist
yet — that's Task 10. For now, add the column to the `INSERT` as shown, but
leave `CorePlayRow` unchanged and pass `""` instead of `p.Library` — **do
not** reference `p.Library` until Task 10 adds the field, or this task won't
compile.  Use `""` here; Task 10 changes it to `p.Library`.)

- [ ] **Step 5: Rewrite `ReadLibraryOverview`**

`internal/store/reads.go` (currently lines 56-141), add a `library string`
parameter and thread it through every query:

```go
func (s *Store) ReadLibraryOverview(ctx context.Context, library string) (LibraryOverview, error) {
	var ov LibraryOverview

	totals := map[string]float64{}
	rows, err := s.db.QueryContext(ctx, `SELECT metric, value FROM agg_totals WHERE library = ?`, library)
	if err != nil {
		return ov, err
	}
	for rows.Next() {
		var k string
		var v float64
		if err := rows.Scan(&k, &v); err != nil {
			rows.Close()
			return ov, err
		}
		totals[k] = v
	}
	rows.Close()
	ov.Totals.RuntimeSeconds = int64(totals["runtime_sec.total"])
	ov.Totals.Bytes = int64(totals["bytes.total"])
	ov.Totals.CountUHD = int64(totals["count.uhd"])
	ov.Totals.CountHDR = int64(totals["count.hdr"])
	ov.Totals.CountDV = int64(totals["count.dv"])
	ov.Totals.Series = int64(totals["items.Series"])
	ov.Totals.Items = int64(totals["items.total"])
	ov.Tags.Coverage.Tagged = int64(totals["tags.tagged_items"])
	ov.Tags.Coverage.Total = int64(totals["tags.total_items"])

	if ov.DiskByResolution, err = s.readDisk(ctx, library, "resolution"); err != nil {
		return ov, err
	}
	if ov.DiskByCodec, err = s.readDisk(ctx, library, "codec"); err != nil {
		return ov, err
	}
	if ov.DiskByContainer, err = s.readDisk(ctx, library, "container"); err != nil {
		return ov, err
	}
	if ov.DiskByLibrary, err = s.readDisk(ctx, library, "library"); err != nil {
		return ov, err
	}

	if ov.GenresTop, err = s.readDistro(ctx, library, "genre", false); err != nil {
		return ov, err
	}
	if ov.ByDecade, err = s.readDistro(ctx, library, "decade", true); err != nil {
		return ov, err
	}
	if ov.Totals.ItemsByLibrary, err = s.readDistro(ctx, library, "library_items", false); err != nil {
		return ov, err
	}
	if ov.Tags.Top, err = s.readDistro(ctx, library, "tag", false); err != nil {
		return ov, err
	}
	if ov.Tags.Pairs, err = s.readTagPairs(ctx, library); err != nil {
		return ov, err
	}

	gr, err := s.db.QueryContext(ctx,
		`SELECT month, added_items, added_bytes, cum_items FROM agg_library_growth WHERE library = ? ORDER BY month ASC`, library)
	if err != nil {
		return ov, err
	}
	defer gr.Close()
	for gr.Next() {
		var g aggregate.GrowthPoint
		if err := gr.Scan(&g.Month, &g.AddedItems, &g.AddedBytes, &g.CumItems); err != nil {
			return ov, err
		}
		ov.Growth = append(ov.Growth, g)
	}
	if err := gr.Err(); err != nil {
		return ov, err
	}

	ov.Totals.ItemsByLibrary = orEmpty(ov.Totals.ItemsByLibrary)
	ov.DiskByResolution = orEmpty(ov.DiskByResolution)
	ov.DiskByCodec = orEmpty(ov.DiskByCodec)
	ov.DiskByContainer = orEmpty(ov.DiskByContainer)
	ov.DiskByLibrary = orEmpty(ov.DiskByLibrary)
	ov.GenresTop = orEmpty(ov.GenresTop)
	ov.ByDecade = orEmpty(ov.ByDecade)
	ov.Growth = orEmpty(ov.Growth)
	ov.Tags.Top = orEmpty(ov.Tags.Top)
	ov.Tags.Pairs = orEmpty(ov.Tags.Pairs)
	return ov, nil
}

func (s *Store) readTagPairs(ctx context.Context, library string) ([]TagPairDTO, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT tag_a, tag_b, items FROM agg_library_tag_pairs WHERE library = ? ORDER BY items DESC, tag_a, tag_b`, library)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []TagPairDTO
	for r.Next() {
		var p TagPairDTO
		if err := r.Scan(&p.A, &p.B, &p.Items); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, r.Err()
}

func (s *Store) readDisk(ctx context.Context, library, dim string) ([]aggregate.DiskBucket, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT bucket, bytes, items FROM agg_disk WHERE library = ? AND dimension = ?`, library, dim)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []aggregate.DiskBucket
	for r.Next() {
		var b aggregate.DiskBucket
		if err := r.Scan(&b.Bucket, &b.Bytes, &b.Items); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Bucket < out[j].Bucket
	})
	return out, r.Err()
}

func (s *Store) readDistro(ctx context.Context, library, dim string, chrono bool) ([]aggregate.LabeledCount, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT bucket, items FROM agg_distribution WHERE library = ? AND dimension = ?`, library, dim)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []aggregate.LabeledCount
	for r.Next() {
		var lc aggregate.LabeledCount
		if err := r.Scan(&lc.Label, &lc.Count); err != nil {
			return nil, err
		}
		out = append(out, lc)
	}
	if chrono {
		sort.Slice(out, func(i, j int) bool {
			ui, uj := out[i].Label == "Unknown", out[j].Label == "Unknown"
			if ui != uj {
				return uj
			}
			return out[i].Label < out[j].Label
		})
	} else {
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Label < out[j].Label
		})
	}
	return out, r.Err()
}
```

- [ ] **Step 6: Fix the pre-existing `WriteLibraryAggregates` call site**

`internal/store/reads_cleanup_test.go` (around line 87-89):

```go
	if err := s.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{"": {Totals: map[string]float64{}}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
```

Search for any other call sites and fix them the same way:

```bash
grep -rln "WriteLibraryAggregates(ctx" internal/
```

- [ ] **Step 7: Run the new test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestWriteAndReadLibraryAggregates_PerLibrary -v
```

Expected: PASS.

- [ ] **Step 8: Run the whole store suite**

```bash
CGO_ENABLED=0 go test ./internal/store/... -v
```

Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/store/writes.go internal/store/reads.go internal/store/reads_cleanup_test.go
git commit -m "$(cat <<'EOF'
feat(store): write and read Library Overview aggregates per library

WriteLibraryAggregates now takes every scope (map[string]LibraryAggregates,
"" = All) and writes them in one transaction with one blanket DELETE per
table. ReadLibraryOverview takes a library parameter threaded through
every underlying query. Also writes dim_library.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 8: Scheduler — `RunLibraryOnce` per-library wiring

**Files:**
- Modify: `internal/scheduler/scheduler.go`
- Modify: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `aggregate.FilterLibrary`, `aggregate.DistinctItemLibraries`
  (Task 6), `Store.WriteLibraryAggregates` new signature (Task 7).
- Produces: nothing new for later tasks — this is where Task 6 and 7 actually
  get used.

- [ ] **Step 1: Read `RunLibraryOnce` and its existing test**

Read `internal/scheduler/scheduler.go` lines 82-120 and
`TestRunLibraryOnce_FullRun` / `TestRunLibraryOnce_PopulatesCleanupUsersCore`
in `scheduler_test.go` in full before editing — confirm exact current line
numbers (earlier tasks in this plan may have shifted other files, but this
one hasn't been touched yet).

- [ ] **Step 2: Write the failing test**

Add to `scheduler_test.go`, next to the existing `RunLibraryOnce` tests:

```go
func TestRunLibraryOnce_PerLibraryAggregates(t *testing.T) {
	sch, st := newScheduler(t) // match whatever helper the existing tests use
	ctx := context.Background()

	if err := sch.RunLibraryOnce(ctx); err != nil {
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
		t.Fatalf("movies=%d shows=%d, want 4/2 (per the fixture)", movies.Totals.Items, shows.Totals.Items)
	}
}
```

- [ ] **Step 3: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -run TestRunLibraryOnce_PerLibraryAggregates -v
```

Expected: FAIL — `movies`/`shows` reads return zero-valued (nothing written
under those library keys yet), since `RunLibraryOnce` still calls
`aggregate.Library(snap, time.Local)` once and `WriteLibraryAggregates` with
just `map[string]aggregate.LibraryAggregates{"": agg}`.

- [ ] **Step 4: Update `RunLibraryOnce`**

`internal/scheduler/scheduler.go`, replace the body from `snap, err :=
s.src.LibraryFacts(ctx)` through the `WriteLibraryAggregates` call:

```go
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
```

(`agg` is gone — the log line that used it now reads from `scoped[""]`.)

- [ ] **Step 5: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -run TestRunLibraryOnce_PerLibraryAggregates -v
```

Expected: PASS.

- [ ] **Step 6: Run the whole scheduler suite**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -v
```

Expected: all PASS, including the pre-existing `TestRunLibraryOnce_*` tests
(they only assert on `""`/All-scope behavior, which is unchanged).

- [ ] **Step 7: Run the full build + lint**

```bash
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
```

Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "$(cat <<'EOF'
feat(scheduler): compute Library Overview per library on refresh

RunLibraryOnce now computes All plus every distinct library's
LibraryAggregates up front (reusing the unmodified Library() pure
function via FilterLibrary) and writes them all in one call.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 9: Aggregate — `WatchDailyRow`/`HeatmapRow`.Library

**Files:**
- Modify: `internal/aggregate/rows.go`
- Modify: `internal/aggregate/watch.go`
- Modify: `internal/aggregate/watch_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent.Library` (Task 3).
- Produces: `WatchDailyRow.Library`, `HeatmapRow.Library` — Task 11's store
  writer/reader depend on both fields existing.

Library is denormalized onto both row types (no precompute multiplication
here — `Watch()` is still called exactly once per refresh). `HeatmapRow`
needs library in its grouping key because it aggregates across *all* items
for a `(user, dow, hour)` cell — the same reason `user_id` is already there.
`WatchDailyRow` doesn't need it in its key; it's a property of the item, like
`Name`.

- [ ] **Step 1: Write the failing test**

`internal/aggregate/watch_test.go` — read the existing test first to match
its event-building style, then add:

```go
func TestWatch_Library(t *testing.T) {
	events := []source.PlaybackEvent{
		{At: mustTime("2025-01-06T20:00:00Z"), UserID: "u1", ItemID: "m1", ItemType: "movie",
			Method: "DirectPlay", PlayDurationSec: 600, Library: "Movies"},
		{At: mustTime("2025-01-06T21:00:00Z"), UserID: "u1", ItemID: "e1", ItemType: "episode",
			Method: "DirectPlay", PlayDurationSec: 600, Library: "Shows"},
	}
	out := Watch(events)

	daily := map[string]WatchDailyRow{}
	for _, r := range out.Daily {
		daily[r.ItemID] = r
	}
	if daily["m1"].Library != "Movies" || daily["e1"].Library != "Shows" {
		t.Fatalf("daily libraries: m1=%q e1=%q", daily["m1"].Library, daily["e1"].Library)
	}

	// Same user, same hour, two different libraries -> two heatmap rows, not one.
	if len(out.Heatmap) != 2 {
		t.Fatalf("want 2 heatmap rows (split by library), got %d: %+v", len(out.Heatmap), out.Heatmap)
	}
	libs := map[string]bool{}
	for _, h := range out.Heatmap {
		libs[h.Library] = true
	}
	if !libs["Movies"] || !libs["Shows"] {
		t.Fatalf("heatmap libraries: %v", libs)
	}
}
```

(Use whatever time-parsing helper `watch_test.go` already has — grep for
`mustTime` or similar before assuming the name.)

- [ ] **Step 2: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -run TestWatch_Library -v
```

Expected: FAIL — `Library` field doesn't exist / heatmap collapses to 1 row.

- [ ] **Step 3: Add `Library` to both row types**

`internal/aggregate/rows.go`:

```go
// WatchDailyRow is one (day, user, title, method) fact from the plugin.
type WatchDailyRow struct {
	Day, UserID, ItemID, Scope, Name, SeriesID, SeriesName, Method, Library string
	Plays, WatchSec                                                        int64
}

// HeatmapRow is one (user, day-of-week, hour, library) cell, all of history.
type HeatmapRow struct {
	UserID, Library string
	DOW, Hour       int
	WatchSec, Plays int64
}
```

- [ ] **Step 4: Update `Watch()`**

`internal/aggregate/watch.go`: add `library` to `hKey` (line 20-23), set
`Library` on both row constructors:

```go
	type dKey struct{ day, user, item, method string }
	type hKey struct {
		user, library string
		dow, hour     int
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
				Library: e.Library,
			}
			daily[dk] = r
		}
		r.Plays++
		r.WatchSec += e.PlayDurationSec

		hk := hKey{e.UserID, e.Library, int(e.At.Weekday()), e.At.Hour()}
		h := heat[hk]
		if h == nil {
			h = &HeatmapRow{UserID: e.UserID, Library: e.Library, DOW: hk.dow, Hour: hk.hour}
			heat[hk] = h
		}
		h.Plays++
		h.WatchSec += e.PlayDurationSec
	}
```

And the heatmap sort (line 72-84) gains `Library` as a tiebreaker so output
order stays deterministic:

```go
	sort.Slice(out.Heatmap, func(i, j int) bool {
		a, b := out.Heatmap[i], out.Heatmap[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.DOW != b.DOW {
			return a.DOW < b.DOW
		}
		if a.Hour != b.Hour {
			return a.Hour < b.Hour
		}
		return a.Library < b.Library
	})
```

- [ ] **Step 5: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -run TestWatch_Library -v
```

Expected: PASS.

- [ ] **Step 6: Run the whole aggregate suite**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -v
```

Expected: all PASS (pre-existing `Watch()` tests don't set `Library`, so it
stays `""` for them — harmless, doesn't change row counts since those tests
don't have two libraries in play).

- [ ] **Step 7: Commit**

```bash
git add internal/aggregate/rows.go internal/aggregate/watch.go internal/aggregate/watch_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): denormalize library onto watch daily/heatmap rows

HeatmapRow gains Library in its grouping key (it aggregates across all
items per user/dow/hour, same reason user_id is already there);
WatchDailyRow carries it as a plain field, like Name.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 10: Source + Aggregate — `UserPlay.Library` / `CorePlayRow.Library`

**Files:**
- Modify: `internal/source/source.go`
- Modify: `internal/source/file/queries.go`
- Modify: `internal/source/file/queries_test.go`
- Modify: `internal/aggregate/rows.go`
- Modify: `internal/aggregate/coreplays.go`
- Modify: `internal/aggregate/coreplays_test.go`
- Modify: `internal/store/writes.go` (fix the `""` placeholder from Task 7
  Step 4 now that `CorePlayRow.Library` exists)

**Interfaces:**
- Consumes: `foldersCTE` (Task 2).
- Produces: `source.UserPlay.Library`, `aggregate.CorePlayRow.Library` —
  Task 11's `WriteLibraryAggregates`/`ReadWatchStats` (`readCore`) depend on
  the latter.

- [ ] **Step 1: Write the failing source test**

`internal/source/file/queries_test.go` — read the existing
`TestQueryLibrary_PlayedStateAndUsers` (around line 83) first, then add:

```go
func TestUserPlays_Library(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	byItem := map[string]source.UserPlay{}
	for _, p := range snap.UserPlays {
		byItem[p.ItemID] = p
	}
	// Bravo is a Movies-library title with PlayCount > 0 in the fixture.
	if bravo, ok := byItem["0000000000000000000000000000000b"]; !ok || bravo.Library != "Movies" {
		t.Fatalf("bravo userplay library = %+v", bravo)
	}
}
```

Check the fixture first (`testdata/library.fixture.sql`'s `UserData` insert,
if any) to confirm Bravo actually has `PlayCount > 0` there — if the fixture
doesn't give any item a play count yet, read `TestQueryLibrary_PlayedStateAndUsers`
to find which item id does, and use that one instead of guessing.

- [ ] **Step 2: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/source/file/... -run TestUserPlays_Library -v
```

Expected: FAIL (compile error — `Library` field doesn't exist on `UserPlay` yet).

- [ ] **Step 3: `source.UserPlay.Library`**

`internal/source/source.go`, in `UserPlay` (around line 54-58):

```go
type UserPlay struct {
	UserID, ItemID, Scope, Name, Library string // Scope: "movie" | "series"; ItemID canonical
	PlayCount                            int
	LastPlayedAt                         time.Time
}
```

- [ ] **Step 4: Resolve it in `userPlaysQuery`**

`internal/source/file/queries.go`, replace `userPlaysQuery` (lines 93-111):

```go
const userPlaysQuery = `
WITH ` + foldersCTE + `
SELECT uid, scope, pid, MAX(name) AS name, SUM(pc) AS play_count, MAX(lpd) AS last_played,
       MAX(library) AS library
FROM (
  SELECT lower(replace(ud.UserId,'-',''))  AS uid,
         CASE WHEN bi.Type = '` + episodeType + `' THEN 'series' ELSE 'movie' END AS scope,
         lower(replace(
           CASE WHEN bi.Type = '` + episodeType + `' AND COALESCE(bi.SeriesId,'') <> ''
                THEN bi.SeriesId ELSE bi.Id END, '-', '')) AS pid,
         CASE WHEN bi.Type = '` + episodeType + `' AND COALESCE(bi.SeriesName,'') <> ''
              THEN bi.SeriesName ELSE bi.Name END AS name,
         MAX(ud.PlayCount)      AS pc,
         MAX(ud.LastPlayedDate) AS lpd,
         COALESCE(f.lib, 'Unknown') AS library
  FROM UserData ud
  JOIN BaseItems bi ON bi.Id = ud.ItemId
  LEFT JOIN folders f ON f.fid = bi.TopParentId
  WHERE bi.Type IN ('` + movieType + `', '` + episodeType + `')
    AND COALESCE(ud.PlayCount,0) > 0
  GROUP BY uid, pid, ud.ItemId, library
)
GROUP BY uid, pid`
```

Find `readUserPlays` (the function that scans this query — grep for
`userPlaysQuery` in `queries.go` to find its scan site) and add `&up.Library`
(or the equivalent local variable) to the `Scan(...)` call, matching the new
`library` output column.

- [ ] **Step 5: Run the source test**

```bash
CGO_ENABLED=0 go test ./internal/source/file/... -run TestUserPlays_Library -v
```

Expected: PASS.

- [ ] **Step 6: Write the failing aggregate test**

`internal/aggregate/coreplays_test.go` — read the existing test first, then
add:

```go
func TestCorePlays_Library(t *testing.T) {
	plays := []source.UserPlay{
		{UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Alpha", Library: "Movies", PlayCount: 3},
	}
	out := CorePlays(plays)
	if len(out) != 1 || out[0].Library != "Movies" {
		t.Fatalf("got %+v", out)
	}
}
```

- [ ] **Step 7: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -run TestCorePlays_Library -v
```

Expected: FAIL (compile error — `CorePlayRow.Library` doesn't exist).

- [ ] **Step 8: `CorePlayRow.Library` + `CorePlays()`**

`internal/aggregate/rows.go`:

```go
type CorePlayRow struct {
	UserID, Scope, ItemID, Name, Library string
	PlayCount                            int64
	LastPlayedAt                         string // RFC3339, "" if none
}
```

`internal/aggregate/coreplays.go`, line 16-19:

```go
		byUser[p.UserID] = append(byUser[p.UserID], CorePlayRow{
			UserID: p.UserID, Scope: p.Scope, ItemID: p.ItemID, Name: p.Name, Library: p.Library,
			PlayCount: int64(p.PlayCount), LastPlayedAt: rfc3339(p.LastPlayedAt),
		})
```

- [ ] **Step 9: Fix Task 7's placeholder in `writes.go`**

`internal/store/writes.go`, in the `core` insert loop (added in Task 7 Step
4), change `""` to `p.Library`:

```go
	for _, p := range core {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_played_core (user_id, scope, item_id, name, play_count, last_played_at, library)
			VALUES (?,?,?,?,?,?,?)`,
			p.UserID, p.Scope, p.ItemID, p.Name, p.PlayCount, nullif(p.LastPlayedAt), p.Library,
		); err != nil {
			return err
		}
	}
```

- [ ] **Step 10: Run all four affected test packages**

```bash
CGO_ENABLED=0 go test ./internal/source/... ./internal/aggregate/... ./internal/store/... ./internal/scheduler/... -v
```

Expected: all PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/source/source.go internal/source/file/queries.go internal/source/file/queries_test.go \
        internal/aggregate/rows.go internal/aggregate/coreplays.go internal/aggregate/coreplays_test.go \
        internal/store/writes.go
git commit -m "$(cat <<'EOF'
feat(source,aggregate): resolve library on user-play rollups

UserPlay.Library (from userPlaysQuery, same folders CTE join) flows
through to CorePlayRow.Library, closing the placeholder left in
WriteLibraryAggregates's core-play insert.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 11: Store — Watch Stats library param (live SQL filter)

**Files:**
- Modify: `internal/store/writes_watch.go`
- Modify: `internal/store/reads_watch.go`
- Modify: `internal/store/writes_watch_test.go`
- Modify: `internal/store/reads_watch_test.go`

**Interfaces:**
- Consumes: `WatchDailyRow.Library`/`HeatmapRow.Library` (Task 9).
- Produces: `WatchStatsParams.Library string`, threaded into
  `ReadWatchStats` — Task 17's API handler depends on this field name.

- [ ] **Step 1: Write the failing write-side test**

`internal/store/writes_watch_test.go` — read the existing
`TestWriteWatchAggregatesRoundTrip` first, then add or extend it to assert
`library` round-trips:

```go
func TestWriteWatchAggregates_Library(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	daily := []aggregate.WatchDailyRow{
		{Day: "2025-01-06", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Alpha",
			Method: "DirectPlay", Library: "Movies", Plays: 1, WatchSec: 600},
	}
	heat := []aggregate.HeatmapRow{
		{UserID: "u1", Library: "Movies", DOW: 1, Hour: 20, Plays: 1, WatchSec: 600},
	}
	if err := st.WriteWatchAggregates(ctx, daily, heat); err != nil {
		t.Fatal(err)
	}
	var lib string
	if err := st.DB().QueryRowContext(ctx, `SELECT library FROM watch_events_daily WHERE item_id = 'm1'`).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if lib != "Movies" {
		t.Fatalf("watch_events_daily.library = %q", lib)
	}
	if err := st.DB().QueryRowContext(ctx, `SELECT library FROM agg_watch_heatmap WHERE user_id = 'u1'`).Scan(&lib); err != nil {
		t.Fatal(err)
	}
	if lib != "Movies" {
		t.Fatalf("agg_watch_heatmap.library = %q", lib)
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestWriteWatchAggregates_Library -v
```

Expected: FAIL — no `library` column populated (SQL error or empty scan,
depending on whether the migration's default `''` masks it — confirm it
actually fails before moving on; if it silently passes because `''` isn't
what's asserted, the assertion is wrong, not the implementation).

- [ ] **Step 3: Update `WriteWatchAggregates`**

`internal/store/writes_watch.go`:

```go
	for _, r := range daily {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO watch_events_daily
			  (day, user_id, item_id, scope, name, series_id, series_name, plays, watch_sec, method, library)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.Day, r.UserID, r.ItemID, r.Scope, r.Name, r.SeriesID, r.SeriesName, r.Plays, r.WatchSec, r.Method, r.Library,
		); err != nil {
			return err
		}
	}
	for _, r := range heat {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_watch_heatmap (user_id, dow, hour, library, watch_sec, plays)
			VALUES (?,?,?,?,?,?)`,
			r.UserID, r.DOW, r.Hour, r.Library, r.WatchSec, r.Plays,
		); err != nil {
			return err
		}
	}
```

- [ ] **Step 4: Run the write-side test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestWriteWatchAggregates_Library -v
```

Expected: PASS.

- [ ] **Step 5: Write the failing read-side test**

`internal/store/reads_watch_test.go` — read `TestReadWatchStats_RangeAndUserFilters`
in full first (it already builds a small dataset via direct SQL or via the
writer — match its exact setup pattern), then add a library-filter variant:

```go
func TestReadWatchStats_LibraryFilter(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	if err := st.WriteWatchAggregates(ctx,
		[]aggregate.WatchDailyRow{
			{Day: "2025-05-20", UserID: "u1", ItemID: "m1", Scope: "movie", Name: "Alpha",
				Method: "DirectPlay", Library: "Movies", Plays: 1, WatchSec: 600},
			{Day: "2025-05-20", UserID: "u1", ItemID: "e1", Scope: "episode", Name: "S1E1",
				Method: "DirectPlay", Library: "Shows", Plays: 1, WatchSec: 900},
		},
		[]aggregate.HeatmapRow{
			{UserID: "u1", Library: "Movies", DOW: 2, Hour: 20, Plays: 1, WatchSec: 600},
			{UserID: "u1", Library: "Shows", DOW: 2, Hour: 20, Plays: 1, WatchSec: 900},
		},
	); err != nil {
		t.Fatal(err)
	}

	all := mustRead(t, st, WatchStatsParams{Range: "all", Now: now})
	if all.Totals.WatchSeconds != 1500 {
		t.Fatalf("all: %d", all.Totals.WatchSeconds)
	}
	movies := mustRead(t, st, WatchStatsParams{Range: "all", Library: "Movies", Now: now})
	if movies.Totals.WatchSeconds != 600 {
		t.Fatalf("movies: %d", movies.Totals.WatchSeconds)
	}
	var heatSecMovies int64
	for _, h := range movies.Heatmap {
		heatSecMovies += h.WatchSec
	}
	if heatSecMovies != 600 {
		t.Fatalf("movies heatmap: %d", heatSecMovies)
	}
}
```

(Reuse `mustRead` from `reads_watch_test.go` — it already exists per the
design doc's ground-truth notes.)

- [ ] **Step 6: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: `WatchStatsParams` has no field `Library`.

- [ ] **Step 7: Add `Library` to `WatchStatsParams` and thread it through**

`internal/store/reads_watch.go`:

```go
type WatchStatsParams struct {
	Range   string
	User    string // canonical id, or "" for all
	Library string // "" for all
	Now     time.Time
}
```

In `ReadWatchStats` (lines 121-366), extend `scoped`/`scopedArgs` (lines
170-175):

```go
	cut := dayCutoff(p.Range, p.Now)
	rangeWhere, rangeArgs := "1=1", []any{}
	if cut != "" {
		rangeWhere, rangeArgs = "day >= ?", []any{cut}
	}
	scoped := rangeWhere
	scopedArgs := append([]any{}, rangeArgs...)
	if p.User != "" {
		scoped += " AND user_id = ?"
		scopedArgs = append(scopedArgs, p.User)
	}
	if p.Library != "" {
		scoped += " AND library = ?"
		scopedArgs = append(scopedArgs, p.Library)
	}
```

Extend `hWhere`/`hArgs` (lines 329-332):

```go
	hWhere, hArgs := "1=1", []any{}
	if p.User != "" {
		hWhere, hArgs = "user_id = ?", []any{p.User}
	}
	if p.Library != "" {
		if hWhere == "1=1" {
			hWhere, hArgs = "library = ?", []any{p.Library}
		} else {
			hWhere += " AND library = ?"
			hArgs = append(hArgs, p.Library)
		}
	}
```

And `readCore` (lines 391-417) gains a `library` parameter used in both its
branches:

```go
func (s *Store) readCore(ctx context.Context, ws *WatchStats, user, library string) error {
	var q string
	var args []any
	where := "1=1"
	if user != "" {
		where = "user_id = ?"
		args = append(args, user)
	}
	if library != "" {
		if where == "1=1" {
			where = "library = ?"
		} else {
			where += " AND library = ?"
		}
		args = append(args, library)
	}
	if user == "" {
		q = `SELECT scope, item_id, MAX(name), SUM(play_count), COALESCE(MAX(last_played_at),'')
		     FROM agg_played_core WHERE ` + where + ` GROUP BY item_id, scope
		     ORDER BY SUM(play_count) DESC, MAX(name) LIMIT 15`
	} else {
		q = `SELECT scope, item_id, name, play_count, COALESCE(last_played_at,'')
		     FROM agg_played_core WHERE ` + where + `
		     ORDER BY play_count DESC, name LIMIT 15`
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

Update its call site (`if err := s.readCore(ctx, &ws, p.User); err != nil`)
to `s.readCore(ctx, &ws, p.User, p.Library)`.

- [ ] **Step 8: Run the read-side test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestReadWatchStats_LibraryFilter -v
```

Expected: PASS.

- [ ] **Step 9: Run the whole store suite**

```bash
CGO_ENABLED=0 go test ./internal/store/... -v
```

Expected: all PASS, including the pre-existing `TestReadWatchStats_RangeAndUserFilters`.

- [ ] **Step 10: Commit**

```bash
git add internal/store/writes_watch.go internal/store/reads_watch.go \
        internal/store/writes_watch_test.go internal/store/reads_watch_test.go
git commit -m "$(cat <<'EOF'
feat(store): add library filter to Watch Stats reads

WatchStatsParams.Library threads through the same live-SQL-filter
pattern range/user already use — no precompute change, watch_events_daily
and agg_watch_heatmap were already maximally granular.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 12: Aggregate — `FilterEventsByLibrary` + `DistinctEventLibraries`

**Files:**
- Modify: `internal/aggregate/profile.go`
- Modify: `internal/aggregate/profile_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent.Library` (Task 3).
- Produces: `aggregate.FilterEventsByLibrary(events, lib) []source.PlaybackEvent`,
  `aggregate.DistinctEventLibraries(events) []string` — Task 14's scheduler
  wiring calls both.

Same trick as Task 6, but on events instead of a snapshot. `Profiles()` itself
stays completely unchanged — filtering happens at the boundary, before the
call.

- [ ] **Step 1: Write the failing test**

`internal/aggregate/profile_test.go` — read the existing event-building test
helper first, then add:

```go
func TestFilterEventsByLibrary(t *testing.T) {
	events := []source.PlaybackEvent{
		{UserID: "u1", ItemID: "m1", Library: "Movies"},
		{UserID: "u1", ItemID: "e1", Library: "Shows"},
		{UserID: "u2", ItemID: "m2", Library: "Movies"},
	}
	all := FilterEventsByLibrary(events, "")
	if len(all) != 3 {
		t.Fatalf("empty filter should return everything, got %d", len(all))
	}
	movies := FilterEventsByLibrary(events, "Movies")
	if len(movies) != 2 {
		t.Fatalf("movies: %d", len(movies))
	}
}

func TestDistinctEventLibraries(t *testing.T) {
	events := []source.PlaybackEvent{
		{Library: "Movies"}, {Library: "Shows"}, {Library: "Movies"},
	}
	got := DistinctEventLibraries(events)
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestProfiles_LibraryFilterExcludesUsersWithNoHistoryThere(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		{At: now.AddDate(0, 0, -1), UserID: "u1", ItemID: "m1", ItemType: "movie",
			PlayDurationSec: 600, ItemRuntimeSec: 6000, Library: "Movies"},
	}
	full := Profiles(events, now)
	if len(full.Summary) == 0 {
		t.Fatal("expected summary rows for u1 in the unfiltered call")
	}

	filtered := Profiles(FilterEventsByLibrary(events, "Shows"), now)
	if len(filtered.Summary) != 0 {
		t.Fatalf("u1 has no Shows history, expected no summary rows, got %+v", filtered.Summary)
	}
}
```

- [ ] **Step 2: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: `FilterEventsByLibrary` / `DistinctEventLibraries` undefined.

- [ ] **Step 3: Implement both**

`internal/aggregate/profile.go`, near the top (after the tuning constants):

```go
// FilterEventsByLibrary returns only the events whose Library matches lib.
// lib == "" returns events unchanged (the "All libraries" scope). Used by the
// scheduler to compute a per-library ProfileAggregates by calling the
// unmodified Profiles() function once per library.
func FilterEventsByLibrary(events []source.PlaybackEvent, lib string) []source.PlaybackEvent {
	if lib == "" {
		return events
	}
	var out []source.PlaybackEvent
	for _, e := range events {
		if e.Library == lib {
			out = append(out, e)
		}
	}
	return out
}

// DistinctEventLibraries returns the unique Library values across events, in
// no particular order.
func DistinctEventLibraries(events []source.PlaybackEvent) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if !seen[e.Library] {
			seen[e.Library] = true
			out = append(out, e.Library)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -run 'TestFilterEventsByLibrary|TestDistinctEventLibraries|TestProfiles_LibraryFilterExcludesUsersWithNoHistoryThere' -v
```

Expected: PASS.

- [ ] **Step 5: Run the whole aggregate suite**

```bash
CGO_ENABLED=0 go test ./internal/aggregate/... -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/aggregate/profile.go internal/aggregate/profile_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): add FilterEventsByLibrary + DistinctEventLibraries

Pure helpers the scheduler uses to compute a per-library
ProfileAggregates by calling the existing, unmodified Profiles()
function once per library. A user with no events in a given library
simply produces no rows for that library — verified explicitly, since
ReadProfile's null-handling depends on this being safe.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 13: Store — per-library `WriteProfileAggregates` + `ReadProfile`

**Files:**
- Modify: `internal/store/writes_profile.go`
- Modify: `internal/store/reads_profile.go`
- Modify: `internal/store/writes_profile_test.go`
- Modify: `internal/store/reads_profile_test.go`
- Modify: `internal/api/profile.go` (call site — see Task 17, but the
  `ReadProfile` signature change breaks its build now, so fix the call site
  in this task too, minimally, without adding the API param yet)

**Interfaces:**
- Consumes: `aggregate.ProfileAggregates` (unchanged).
- Produces: `Store.WriteProfileAggregates(ctx, scoped map[string]aggregate.ProfileAggregates) error`,
  `Store.ReadProfile(ctx, userID, rng, library string) (Profile, bool, error)`
  — Task 14's scheduler wiring and Task 17's API handler depend on these.

Same map-in-one-transaction shape as Task 7, applied to all eight
`agg_profile_*`/`agg_taste_baseline`/`agg_profile_tag_overlap` tables.

- [ ] **Step 1: Write the failing round-trip test**

`internal/store/writes_profile_test.go` — read
`TestWriteProfileAggregates_RewriteSemantics` in full first, then add:

```go
func TestWriteAndReadProfileAggregates_PerLibrary(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO dim_user (id, name) VALUES ('u1','Alice')`); err != nil {
		t.Fatal(err)
	}

	scoped := map[string]aggregate.ProfileAggregates{
		"": {
			Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", WatchSec: 1500, Plays: 2}},
		},
		"Movies": {
			Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", WatchSec: 600, Plays: 1}},
		},
	}
	if err := st.WriteProfileAggregates(ctx, scoped); err != nil {
		t.Fatal(err)
	}

	all, ok, err := st.ReadProfile(ctx, "u1", "all", "")
	if err != nil || !ok {
		t.Fatalf("all: ok=%v err=%v", ok, err)
	}
	if all.Summary.WatchSec != 1500 {
		t.Fatalf("all watch_sec = %d", all.Summary.WatchSec)
	}

	movies, ok, err := st.ReadProfile(ctx, "u1", "all", "Movies")
	if err != nil || !ok {
		t.Fatalf("movies: ok=%v err=%v", ok, err)
	}
	if movies.Summary.WatchSec != 600 {
		t.Fatalf("movies watch_sec = %d", movies.Summary.WatchSec)
	}

	// A library with no data for this user still returns ok=true, zero-valued.
	shows, ok, err := st.ReadProfile(ctx, "u1", "all", "Shows")
	if err != nil || !ok {
		t.Fatalf("shows: ok=%v err=%v", ok, err)
	}
	if shows.Summary.WatchSec != 0 {
		t.Fatalf("shows should be zero-valued, got %d", shows.Summary.WatchSec)
	}
}
```

- [ ] **Step 2: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: signature mismatches on both `WriteProfileAggregates` and
`ReadProfile`.

- [ ] **Step 3: Rewrite `WriteProfileAggregates`**

`internal/store/writes_profile.go` — same shape as Task 7's
`WriteLibraryAggregates`, one blanket `DELETE` per table, one outer loop over
the `scoped` map:

```go
func (s *Store) WriteProfileAggregates(ctx context.Context, scoped map[string]aggregate.ProfileAggregates) error {
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
		`DELETE FROM agg_profile_tag_overlap`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	for lib, a := range scoped {
		for _, r := range a.Summary {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_summary
				  (user_id, range, library, watch_sec, plays, distinct_titles, days_active,
				   finished_pct, bailed_pct, rewatch_pct, longest_binge_episodes,
				   longest_binge_series_name, show_of_range_series_id,
				   show_of_range_series_name, first_play, last_play)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.WatchSec, r.Plays, r.DistinctTitles, r.DaysActive,
				r.FinishedPct, r.BailedPct, r.RewatchPct, r.LongestBingeEpisodes,
				r.LongestBingeSeriesName, r.ShowOfRangeSeriesID, r.ShowOfRangeSeriesName,
				r.FirstPlay, r.LastPlay,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Completion {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_completion (user_id, range, library, scope, bucket, count)
				VALUES (?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.Scope, r.Bucket, r.Count,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Abandoned {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_abandoned
				  (user_id, range, library, scope, item_id, name, series_name, bailed_count)
				VALUES (?,?,?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.Scope, r.ItemID, r.Name, r.SeriesName, r.BailedCount,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Rewatch {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_rewatch
				  (user_id, library, scope, item_id, name, series_name, watch_days)
				VALUES (?,?,?,?,?,?,?)`,
				r.UserID, lib, r.Scope, r.ItemID, r.Name, r.SeriesName, r.WatchDays,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Binge {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_binge
				  (user_id, library, series_id, series_name, run_episodes, run_start, run_end)
				VALUES (?,?,?,?,?,?,?)`,
				r.UserID, lib, r.SeriesID, r.SeriesName, r.RunEpisodes, r.RunStart, r.RunEnd,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Taste {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_taste (user_id, range, library, dim, key, watch_sec, plays)
				VALUES (?,?,?,?,?,?,?)`,
				r.UserID, r.Range, lib, r.Dim, r.Key, r.WatchSec, r.Plays,
			); err != nil {
				return err
			}
		}
		for _, r := range a.Baseline {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_taste_baseline (library, dim, key, watch_sec) VALUES (?,?,?,?)`,
				lib, r.Dim, r.Key, r.WatchSec,
			); err != nil {
				return err
			}
		}
		for _, r := range a.TagOverlap {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agg_profile_tag_overlap (user_a, user_b, range, library, cosine, shared)
				VALUES (?,?,?,?,?,?)`,
				r.UserA, r.UserB, r.Range, lib, r.Cosine, joinTags(r.Shared),
			); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 4: Rewrite `ReadProfile` and its eight sub-readers**

`internal/store/reads_profile.go`, change the signature and every call site
within it:

```go
func (s *Store) ReadProfile(ctx context.Context, userID, rng, library string) (Profile, bool, error) {
	out := Profile{Range: rng, User: ProfileUser{ID: userID}}

	err := s.db.QueryRowContext(ctx, `SELECT name FROM dim_user WHERE id = ?`, userID).Scan(&out.User.Name)
	if err == sql.ErrNoRows {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, err
	}

	if err := s.readProfileSummary(ctx, &out, userID, rng, library); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileCompletion(ctx, &out, userID, rng, library); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileAbandoned(ctx, &out, userID, rng, library); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileRewatch(ctx, &out, userID, library); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileBinge(ctx, &out, userID, library); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileTaste(ctx, &out, userID, rng, library); err != nil {
		return Profile{}, false, err
	}
	if err := s.readProfileTagOverlap(ctx, &out, userID, rng, library); err != nil {
		return Profile{}, false, err
	}
	out.Taste.SignatureGenres = signatureGenres(out.Taste.Genre, out.Baseline.Genre)
	out.Taste.SignatureTags = signatureShares(out.Taste.Tag, out.Baseline.Tag, 2, 120)

	out.Completion = orEmpty(out.Completion)
	out.Abandoned = orEmpty(out.Abandoned)
	out.Rewatch = orEmpty(out.Rewatch)
	out.Binge = orEmpty(out.Binge)
	out.Taste.Genre = orEmpty(out.Taste.Genre)
	out.Taste.Decade = orEmpty(out.Taste.Decade)
	out.Taste.Length = orEmpty(out.Taste.Length)
	out.Taste.Tag = orEmpty(out.Taste.Tag)
	out.Taste.SignatureGenres = orEmpty(out.Taste.SignatureGenres)
	out.Taste.SignatureTags = orEmpty(out.Taste.SignatureTags)
	out.Baseline.Genre = orEmpty(out.Baseline.Genre)
	out.Baseline.Decade = orEmpty(out.Baseline.Decade)
	out.Baseline.Length = orEmpty(out.Baseline.Length)
	out.Baseline.Tag = orEmpty(out.Baseline.Tag)
	out.TagOverlap = orEmpty(out.TagOverlap)
	return out, true, nil
}

func (s *Store) readProfileSummary(ctx context.Context, out *Profile, userID, rng, library string) error {
	err := s.db.QueryRowContext(ctx, `
		SELECT watch_sec, plays, distinct_titles, days_active, finished_pct, bailed_pct,
		       show_of_range_series_id, show_of_range_series_name, first_play, last_play
		FROM agg_profile_summary WHERE user_id = ? AND range = ? AND library = ?`, userID, rng, library,
	).Scan(&out.Summary.WatchSec, &out.Summary.Plays, &out.Summary.DistinctTitles,
		&out.Summary.DaysActive, &out.Summary.FinishedPct, &out.Summary.BailedPct,
		&out.Summary.ShowOfRange.SeriesID, &out.Summary.ShowOfRange.SeriesName,
		&out.Summary.FirstPlay, &out.Summary.LastPlay)
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	var rp float64
	var lbe int64
	var lbs string
	e := s.db.QueryRowContext(ctx, `
		SELECT rewatch_pct, longest_binge_episodes, longest_binge_series_name
		FROM agg_profile_summary WHERE user_id = ? AND range = 'all' AND library = ?`, userID, library,
	).Scan(&rp, &lbe, &lbs)
	if e != nil && e != sql.ErrNoRows {
		return e
	}
	out.Summary.RewatchPct = rp
	out.Summary.LongestBinge = ProfileLongestBinge{Episodes: lbe, SeriesName: lbs}
	return nil
}

func (s *Store) readProfileCompletion(ctx context.Context, out *Profile, userID, rng, library string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, bucket, count FROM agg_profile_completion
		WHERE user_id = ? AND range = ? AND library = ? ORDER BY scope, bucket`, userID, rng, library)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c ProfileCompletionSlice
		if err := rows.Scan(&c.Scope, &c.Bucket, &c.Count); err != nil {
			return err
		}
		out.Completion = append(out.Completion, c)
	}
	return rows.Err()
}

func (s *Store) readProfileAbandoned(ctx context.Context, out *Profile, userID, rng, library string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, item_id, name, series_name, bailed_count FROM agg_profile_abandoned
		WHERE user_id = ? AND range = ? AND library = ? ORDER BY bailed_count DESC, item_id`, userID, rng, library)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a ProfileAbandoned
		if err := rows.Scan(&a.Scope, &a.ItemID, &a.Name, &a.SeriesName, &a.BailedCount); err != nil {
			return err
		}
		out.Abandoned = append(out.Abandoned, a)
	}
	return rows.Err()
}

func (s *Store) readProfileRewatch(ctx context.Context, out *Profile, userID, library string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, item_id, name, series_name, watch_days FROM agg_profile_rewatch
		WHERE user_id = ? AND library = ? ORDER BY watch_days DESC, item_id`, userID, library)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r ProfileRewatch
		if err := rows.Scan(&r.Scope, &r.ItemID, &r.Name, &r.SeriesName, &r.WatchDays); err != nil {
			return err
		}
		out.Rewatch = append(out.Rewatch, r)
	}
	return rows.Err()
}

func (s *Store) readProfileBinge(ctx context.Context, out *Profile, userID, library string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT series_id, series_name, run_episodes, run_start, run_end FROM agg_profile_binge
		WHERE user_id = ? AND library = ? ORDER BY run_episodes DESC, run_end DESC`, userID, library)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b ProfileBinge
		if err := rows.Scan(&b.SeriesID, &b.SeriesName, &b.RunEpisodes, &b.RunStart, &b.RunEnd); err != nil {
			return err
		}
		out.Binge = append(out.Binge, b)
	}
	return rows.Err()
}

func (s *Store) readProfileTaste(ctx context.Context, out *Profile, userID, rng, library string) error {
	trows, err := s.db.QueryContext(ctx, `
		SELECT dim, key, watch_sec, plays FROM agg_profile_taste
		WHERE user_id = ? AND range = ? AND library = ? ORDER BY dim, watch_sec DESC, key`, userID, rng, library)
	if err != nil {
		return err
	}
	defer trows.Close()
	for trows.Next() {
		var dim string
		var e TasteEntry
		if err := trows.Scan(&dim, &e.Key, &e.WatchSec, &e.Plays); err != nil {
			return err
		}
		switch dim {
		case "genre":
			out.Taste.Genre = append(out.Taste.Genre, e)
		case "decade":
			out.Taste.Decade = append(out.Taste.Decade, e)
		case "length":
			out.Taste.Length = append(out.Taste.Length, e)
		case "tag":
			out.Taste.Tag = append(out.Taste.Tag, e)
		}
	}
	if err := trows.Err(); err != nil {
		return err
	}

	brows, err := s.db.QueryContext(ctx, `
		SELECT dim, key, watch_sec FROM agg_taste_baseline WHERE library = ? ORDER BY dim, watch_sec DESC, key`, library)
	if err != nil {
		return err
	}
	defer brows.Close()
	for brows.Next() {
		var dim string
		var e BaselineEntry
		if err := brows.Scan(&dim, &e.Key, &e.WatchSec); err != nil {
			return err
		}
		switch dim {
		case "genre":
			out.Baseline.Genre = append(out.Baseline.Genre, e)
		case "decade":
			out.Baseline.Decade = append(out.Baseline.Decade, e)
		case "length":
			out.Baseline.Length = append(out.Baseline.Length, e)
		case "tag":
			out.Baseline.Tag = append(out.Baseline.Tag, e)
		}
	}
	return brows.Err()
}

func (s *Store) readProfileTagOverlap(ctx context.Context, out *Profile, userID, rng, library string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT CASE WHEN o.user_a = ? THEN o.user_b ELSE o.user_a END AS other,
		       COALESCE(d.name, ''), o.cosine, o.shared
		FROM agg_profile_tag_overlap o
		LEFT JOIN dim_user d
		  ON d.id = CASE WHEN o.user_a = ? THEN o.user_b ELSE o.user_a END
		WHERE (o.user_a = ? OR o.user_b = ?) AND o.range = ? AND o.library = ?
		ORDER BY o.cosine DESC, other`, userID, userID, userID, userID, rng, library)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e ProfileTagOverlap
		var shared string
		if err := rows.Scan(&e.User, &e.UserName, &e.Cosine, &shared); err != nil {
			return err
		}
		e.Shared = splitGenres(shared)
		out.TagOverlap = append(out.TagOverlap, e)
	}
	return rows.Err()
}
```

- [ ] **Step 5: Fix the `ReadProfile` call site in `internal/api/profile.go`**

Change `s.st.ReadProfile(ctx, r.PathValue("userID"), rng)` to
`s.st.ReadProfile(ctx, r.PathValue("userID"), rng, "")` for now — Task 17
wires the real `library` query param through. This keeps the build green
without doing Task 17's work early.

- [ ] **Step 6: Run the new test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestWriteAndReadProfileAggregates_PerLibrary -v
```

Expected: PASS.

- [ ] **Step 7: Run the whole store + api suites**

```bash
CGO_ENABLED=0 go test ./internal/store/... ./internal/api/... -v
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/store/writes_profile.go internal/store/reads_profile.go \
        internal/store/writes_profile_test.go internal/store/reads_profile_test.go \
        internal/api/profile.go
git commit -m "$(cat <<'EOF'
feat(store): write and read Profiles aggregates per library

WriteProfileAggregates takes every scope in one call/transaction, same
shape as WriteLibraryAggregates. ReadProfile takes a library parameter
threaded through all eight agg_profile_*/agg_taste_baseline/
agg_profile_tag_overlap sub-readers. A user with no history in a given
library returns ok=true with zero-valued fields, not a 404 — same
null-handling path that already covers "no plays in range".

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 14: Scheduler — `RunWatchOnce` per-library Profiles wiring

**Files:**
- Modify: `internal/scheduler/scheduler.go`
- Modify: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `aggregate.FilterEventsByLibrary`, `aggregate.DistinctEventLibraries`
  (Task 12), `Store.WriteProfileAggregates` new signature (Task 13).
- Produces: nothing new for later tasks.

`aggregate.Watch(history)` itself is untouched (still one call — Task 9
already denormalized library onto its output rows). Only the `Profiles(...)`
call becomes a per-library loop.

- [ ] **Step 1: Read the current `RunWatchOnce` end**

Read `internal/scheduler/scheduler.go` around the `aggregate.Profiles(history,
time.Now())` call (previously line 177, may have shifted after Task 5's
backfill insertion — find it fresh with `grep -n "aggregate.Profiles"
internal/scheduler/scheduler.go`).

- [ ] **Step 2: Write the failing test**

```go
func TestRunWatchOnce_PerLibraryProfiles(t *testing.T) {
	sch, st := newScheduler(t)
	ctx := context.Background()

	if err := sch.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := sch.RunWatchOnce(ctx); err != nil {
		t.Fatal(err)
	}

	// alice (11111111222233334444555555555555) has plays on Alpha/Bravo/Delta,
	// all in the Movies library per the fixture.
	all, ok, err := st.ReadProfile(ctx, "11111111222233334444555555555555", "all", "")
	if err != nil || !ok {
		t.Fatalf("all: ok=%v err=%v", ok, err)
	}
	movies, ok, err := st.ReadProfile(ctx, "11111111222233334444555555555555", "all", "Movies")
	if err != nil || !ok {
		t.Fatalf("movies: ok=%v err=%v", ok, err)
	}
	if movies.Summary.WatchSec != all.Summary.WatchSec {
		t.Fatalf("alice has only Movies plays, movies(%d) should equal all(%d)",
			movies.Summary.WatchSec, all.Summary.WatchSec)
	}
	shows, ok, err := st.ReadProfile(ctx, "11111111222233334444555555555555", "all", "Shows")
	if err != nil || !ok {
		t.Fatalf("shows: ok=%v err=%v", ok, err)
	}
	if shows.Summary.WatchSec != 0 {
		t.Fatalf("alice has no Shows plays, want 0, got %d", shows.Summary.WatchSec)
	}
}
```

- [ ] **Step 3: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -run TestRunWatchOnce_PerLibraryProfiles -v
```

Expected: FAIL — `movies`/`shows` reads are zero-valued since nothing is
written under those library keys yet.

- [ ] **Step 4: Update the `Profiles(...)` call site**

```go
	agg := aggregate.Watch(history)
	if err := s.st.WriteWatchAggregates(ctx, agg.Daily, agg.Heatmap); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}

	scopedProfiles := map[string]aggregate.ProfileAggregates{"": aggregate.Profiles(history, time.Now())}
	for _, lib := range aggregate.DistinctEventLibraries(history) {
		scopedProfiles[lib] = aggregate.Profiles(aggregate.FilterEventsByLibrary(history, lib), time.Now())
	}
	if err := s.st.WriteProfileAggregates(ctx, scopedProfiles); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
```

- [ ] **Step 5: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -run TestRunWatchOnce_PerLibraryProfiles -v
```

Expected: PASS.

- [ ] **Step 6: Run the whole scheduler suite + full build**

```bash
CGO_ENABLED=0 go test ./internal/scheduler/... -v
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
```

Expected: all PASS, clean build, clean vet.

- [ ] **Step 7: Commit**

```bash
git add internal/scheduler/scheduler.go internal/scheduler/scheduler_test.go
git commit -m "$(cat <<'EOF'
feat(scheduler): compute Profiles per library on refresh

RunWatchOnce now computes All plus every distinct library's
ProfileAggregates (reusing the unmodified Profiles() pure function via
FilterEventsByLibrary) and writes them all in one call. Watch()
itself is unchanged — its output already carries library per row.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 15: Store — Cleanup library param + CSV export

**Files:**
- Modify: `internal/store/reads_cleanup.go`
- Modify: `internal/store/reads_cleanup_test.go`
- Modify: `internal/api/cleanup.go` (call sites — kept in this task since it's
  a 2-line change; the query-param parsing itself is Task 17)

**Interfaces:**
- Consumes: `agg_cleanup.library` (already exists, no schema change).
- Produces: `CleanupParams.Library string` — Task 17's API handler depends on
  this field name.

- [ ] **Step 1: Read the current implementation**

`internal/store/reads_cleanup.go` (35 lines shown here for reference — read
the live file too in case it drifted, but this is the exact current shape):

```go
type CleanupParams struct {
	Mode  string
	Sort  string
	Limit int
	Now   time.Time
}

type CleanupItem struct {
	ItemID, Scope, Name, Library string
	Bytes, Episodes              int64
	AddedAt                      string
	LastPlayedAt                 *string
}

func cleanupWhere(mode string, now time.Time) (string, []any) {
	if mode == "stale" {
		cut := now.AddDate(-1, 0, 0).UTC().Format(time.RFC3339)
		return `last_played_at IS NOT NULL AND last_played_at < ?`, []any{cut}
	}
	return `last_played_at IS NULL`, nil
}

func (s *Store) ReadCleanup(ctx context.Context, p CleanupParams) (CleanupResult, error) { ... }

// StreamCleanupCSV writes all matching rows (no limit) as CSV.
func (s *Store) StreamCleanupCSV(ctx context.Context, mode string, now time.Time, w io.Writer) error { ... }
```

`StreamCleanupCSV` takes `mode`/`now` **positionally**, not a `CleanupParams`
— it gains a new positional `library` parameter, not a struct field.

- [ ] **Step 2: Write the failing test**

`internal/store/reads_cleanup_test.go` — read `TestReadCleanup_NeverAndStale`
and `seedCleanup` in full first (to reuse the same seeding helper), then add:

```go
func TestReadCleanup_LibraryFilter(t *testing.T) {
	s, now := seedCleanup(t) // reuse the existing helper; check what libraries it seeds
	ctx := context.Background()

	all, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if all.MatchCount == 0 {
		t.Fatal("seedCleanup should produce at least one match")
	}

	// Pick whatever library the first item actually has, from all.Items[0].
	lib := all.Items[0].Library
	filtered, err := s.ReadCleanup(ctx, CleanupParams{Mode: "never", Sort: "size", Limit: 10, Library: lib, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range filtered.Items {
		if it.Library != lib {
			t.Fatalf("item %s has library %q, want %q", it.ItemID, it.Library, lib)
		}
	}
	if filtered.MatchCount == 0 {
		t.Fatal("expected at least one match for the seeded library")
	}
}
```

(Check `CleanupItem`/whatever the response row type is actually called —
grep `reads_cleanup.go` for the exported item type before assuming `Items[0].Library`
is the right accessor.)

- [ ] **Step 3: Run it, confirm it fails to compile**

```bash
CGO_ENABLED=0 go build ./... 2>&1 | head -20
```

Expected: `CleanupParams` has no field `Library`.

- [ ] **Step 4: Add `Library` to `CleanupParams`, `cleanupWhere`, and `StreamCleanupCSV`**

```go
type CleanupParams struct {
	Mode    string
	Sort    string
	Limit   int
	Library string // "" for all
	Now     time.Time
}

// cleanupWhere returns the SQL predicate + args for a mode and an optional
// library filter. The stale cutoff is now-365d in RFC3339 so it compares
// lexically against the stored value.
func cleanupWhere(mode, library string, now time.Time) (string, []any) {
	var where string
	var args []any
	if mode == "stale" {
		cut := now.AddDate(-1, 0, 0).UTC().Format(time.RFC3339)
		where, args = `last_played_at IS NOT NULL AND last_played_at < ?`, []any{cut}
	} else {
		where, args = `last_played_at IS NULL`, nil
	}
	if library != "" {
		where += " AND library = ?"
		args = append(args, library)
	}
	return where, args
}
```

`ReadCleanup` (line 47-48): change `cleanupWhere(p.Mode, p.Now)` to
`cleanupWhere(p.Mode, p.Library, p.Now)`. Nothing else in `ReadCleanup`
changes — `where`/`args` already flow into every query unchanged.

`StreamCleanupCSV` gains a `library` parameter:

```go
func (s *Store) StreamCleanupCSV(ctx context.Context, mode, library string, now time.Time, w io.Writer) error {
	where, args := cleanupWhere(mode, library, now)
	...
```

(the rest of the function body is unchanged — only the `cleanupWhere` call's
arguments differ).

- [ ] **Step 5: Wire the call sites so the build stays green**

`internal/api/cleanup.go`: `ReadCleanup(ctx, store.CleanupParams{Mode: mode,
Sort: sortBy, Limit: limit, Now: s.now()})` → add `Library: ""` for now (Task
17 reads the real query param). `StreamCleanupCSV(ctx, mode, s.now(), w)` →
`StreamCleanupCSV(ctx, mode, "", s.now(), w)`.

- [ ] **Step 6: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/store/... -run TestReadCleanup_LibraryFilter -v
```

Expected: PASS.

- [ ] **Step 7: Run the whole store + api suites**

```bash
CGO_ENABLED=0 go test ./internal/store/... ./internal/api/... -v
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/store/reads_cleanup.go internal/store/reads_cleanup_test.go internal/api/cleanup.go
git commit -m "$(cat <<'EOF'
feat(store): add library filter to Cleanup reads

CleanupParams.Library filters agg_cleanup, which already stores
library per row — pure parameter wiring, no schema change.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 16: API — `GET /api/libraries`

**Files:**
- Create: `internal/api/libraries.go`
- Modify: `internal/api/api.go` (route registration)
- Create: `internal/api/libraries_test.go`

**Interfaces:**
- Consumes: `dim_library` (Task 7).
- Produces: new store method `Store.ReadLibraries(ctx) ([]string, error)` and
  the `GET /api/libraries` route — Task 17's frontend-facing contract; no Go
  code depends on this beyond the route itself.

- [ ] **Step 1: Read `internal/api/api.go`'s route table and `internal/api/library.go` for the handler pattern to match**

Both already read earlier in this plan's research — `handleLibraryOverview`
in `internal/api/library.go` is the template: check `refresh_meta`, call a
store read, `writeJSON`.

- [ ] **Step 2: Write the failing test**

`internal/api/libraries_test.go` — read `internal/api/api_test.go`'s test
server setup helper first (it already seeds a store and builds a `*Server`
for other endpoint tests), then:

```go
func TestHandleLibraries(t *testing.T) {
	srv, st := newTestServer(t) // match whatever helper api_test.go uses
	ctx := context.Background()
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO dim_library (name) VALUES ('Movies'), ('Shows')`); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	srv.ServeHTTP(rec, req) // or however api_test.go dispatches requests

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 2 {
		t.Fatalf("data = %v", body.Data)
	}
}
```

- [ ] **Step 3: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/api/... -run TestHandleLibraries -v
```

Expected: FAIL — 404 (no route registered yet).

- [ ] **Step 4: Add `Store.ReadLibraries`**

`internal/store/reads.go`, near `ReadLibraryOverview`:

```go
// ReadLibraries returns every known library name, sorted by item count desc
// then name — "Unknown" included if any item resolved to it. Backs the
// filter dropdown on every library-aware page.
func (s *Store) ReadLibraries(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.name, COALESCE(t.items, 0) AS items
		FROM dim_library d
		LEFT JOIN agg_distribution t ON t.library = '' AND t.dimension = 'library_items' AND t.bucket = d.name
		ORDER BY items DESC, d.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		var items int64
		if err := rows.Scan(&name, &items); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return orEmpty(out), rows.Err()
}
```

- [ ] **Step 5: Add the handler**

`internal/api/libraries.go`:

```go
package api

import "net/http"

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	libs, err := s.st.ReadLibraries(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, libs, s.meta(false))
}
```

(`stale` is always `false` here — the library list itself has no independent
staleness concept beyond the library refresh, which `library/overview`
already surfaces.)

- [ ] **Step 6: Register the route**

`internal/api/api.go` — find the `mux.HandleFunc("GET /api/library/overview",
...)` registration and add a sibling line:

```go
	mux.HandleFunc("GET /api/libraries", s.handleLibraries)
```

- [ ] **Step 7: Run the test**

```bash
CGO_ENABLED=0 go test ./internal/api/... -run TestHandleLibraries -v
```

Expected: PASS.

- [ ] **Step 8: Run the whole api + store suites**

```bash
CGO_ENABLED=0 go test ./internal/api/... ./internal/store/... -v
```

Expected: all PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/api/libraries.go internal/api/libraries_test.go internal/api/api.go internal/store/reads.go
git commit -m "$(cat <<'EOF'
feat(api): add GET /api/libraries

Returns every known library name (from dim_library), sorted by item
count desc then name — backs the filter dropdown on every
library-aware page.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Task 17: API — `library=` param on the four endpoints

**Files:**
- Modify: `internal/api/library.go`
- Modify: `internal/api/watch.go`
- Modify: `internal/api/profile.go`
- Modify: `internal/api/cleanup.go`
- Modify: `internal/api/library_test.go` / equivalent (check existing test
  filenames — `library.go`'s test may be inline in `api_test.go`)
- Modify: `internal/api/watch_test.go`
- Modify: `internal/api/profile_test.go`
- Modify: `internal/api/cleanup_test.go`

**Interfaces:**
- Consumes: `ReadLibraryOverview(ctx, library)` (Task 7),
  `WatchStatsParams.Library` (Task 11), `ReadProfile(ctx, userID, rng, library)`
  (Task 13), `CleanupParams.Library` (Task 15).
- Produces: the public API contract this whole plan exists to deliver.
  `library=all` or omitted maps to `""`.

- [ ] **Step 1: Write the failing test for `library/overview`**

Find (or create) the test file covering `handleLibraryOverview` — grep
`api_test.go` for `handleLibraryOverview` or `/api/library/overview` first.
Add:

```go
func TestHandleLibraryOverview_LibraryParam(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := context.Background()
	if err := st.WriteLibraryAggregates(ctx,
		map[string]aggregate.LibraryAggregates{
			"":       {Totals: map[string]float64{"items.total": 6}},
			"Movies": {Totals: map[string]float64{"items.total": 4}},
		}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: time.Now(), OK: true}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ q string; want int64 }{
		{"", 6}, {"?library=all", 6}, {"?library=Movies", 4}, {"?library=Nonexistent", 0},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/library/overview"+tc.q, nil)
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", tc.q, rec.Code, rec.Body.String())
		}
		var body struct {
			Data struct {
				Totals struct {
					Items int64 `json:"items"`
				} `json:"totals"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Data.Totals.Items != tc.want {
			t.Errorf("%s: items = %d, want %d", tc.q, body.Data.Totals.Items, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
CGO_ENABLED=0 go test ./internal/api/... -run TestHandleLibraryOverview_LibraryParam -v
```

Expected: FAIL — `library=Movies` still returns 6 (unfiltered), since the
handler doesn't read the param yet.

- [ ] **Step 3: Wire `library/overview`**

`internal/api/library.go`:

```go
func (s *Server) handleLibraryOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	m, ok, err := s.st.GetRefreshMeta(ctx, "library")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "first refresh has not completed")
		return
	}
	library := r.URL.Query().Get("library")
	if library == "all" {
		library = ""
	}
	ov, err := s.st.ReadLibraryOverview(ctx, library)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	stale := store.IsStale(m, ok, s.cfg.RefreshLibrary, s.now())
	writeJSON(w, http.StatusOK, ov, s.meta(stale))
}
```

- [ ] **Step 4: Run the overview test**

```bash
CGO_ENABLED=0 go test ./internal/api/... -run TestHandleLibraryOverview_LibraryParam -v
```

Expected: PASS.

- [ ] **Step 5: Wire `watch/stats`**

`internal/api/watch.go`:

```go
	user := q.Get("user")
	if user == "all" {
		user = ""
	}
	library := q.Get("library")
	if library == "all" {
		library = ""
	}
	...
	data, err := s.st.ReadWatchStats(ctx, store.WatchStatsParams{Range: rng, User: user, Library: library, Now: s.now()})
```

- [ ] **Step 6: Wire `profile/{userID}`**

`internal/api/profile.go`:

```go
	library := r.URL.Query().Get("library")
	if library == "all" {
		library = ""
	}
	data, ok, err := s.st.ReadProfile(ctx, r.PathValue("userID"), rng, library)
```

- [ ] **Step 7: Wire `cleanup`**

`internal/api/cleanup.go`:

```go
	library := q.Get("library")
	if library == "all" {
		library = ""
	}
	...
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="ephyra-cleanup-`+mode+`.csv"`)
		if err := s.st.StreamCleanupCSV(ctx, mode, library, s.now(), w); err != nil {
			s.log.Error("cleanup csv", "err", err)
		}
		return
	}

	res, err := s.st.ReadCleanup(ctx, store.CleanupParams{Mode: mode, Sort: sortBy, Limit: limit, Library: library, Now: s.now()})
```

- [ ] **Step 8: Write and run tests for the remaining three endpoints**

Add analogous `library=`/`all`/unrecognized-name cases to
`internal/api/watch_test.go`, `internal/api/profile_test.go`,
`internal/api/cleanup_test.go`, following Step 1's pattern (seed two scopes,
assert the right one comes back, `all`/omitted matches the unfiltered scope,
an unrecognized name returns a valid empty/zero response rather than a 400).

```bash
CGO_ENABLED=0 go test ./internal/api/... -run 'LibraryParam' -v
```

Expected: PASS for all four.

- [ ] **Step 9: Run the whole api suite + full build + lint**

```bash
CGO_ENABLED=0 go test ./... -v
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go vet ./...
golangci-lint run
```

Expected: everything green.

- [ ] **Step 10: Commit**

```bash
git add internal/api/library.go internal/api/watch.go internal/api/profile.go internal/api/cleanup.go \
        internal/api/library_test.go internal/api/watch_test.go internal/api/profile_test.go internal/api/cleanup_test.go
git commit -m "$(cat <<'EOF'
feat(api): add library= query param to overview/watch/profile/cleanup

library=all or omitted matches today's unfiltered behavior
byte-for-byte; a real library name narrows results; an unrecognized
name returns a valid empty/zero response, not 400 — same leniency as
an unrecognized user id today. This completes the backend half of
library-level filtering.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

- [ ] **Step 11: Manual smoke test against the fixture**

```bash
mkdir -p /tmp/jf-libfilter/data && sqlite3 /tmp/jf-libfilter/data/jellyfin.db < testdata/library.fixture.sql
JELLYFIN_URL=x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf-libfilter \
  STORE_PATH=/tmp/ephyra-libfilter.db WORK_DIR=/tmp/ephyra-libfilter-work go run ./cmd/ephyra &
sleep 2
curl -s -X POST localhost:8097/api/refresh
sleep 1
curl -s localhost:8097/api/libraries | python3 -m json.tool
curl -s 'localhost:8097/api/library/overview?library=Movies' | python3 -m json.tool | head -20
curl -s 'localhost:8097/api/library/overview?library=Shows' | python3 -m json.tool | head -20
kill %1
```

Expected: `/api/libraries` returns `["Movies","Shows"]` (order may vary by
item count); the two overview calls show different `totals.items` (4 vs 2)
and different genre/decade breakdowns.

---

## Backend plan complete

At this point: `GET /api/libraries`, and `library=` on
`/api/library/overview`, `/api/watch/stats`, `/api/profile/{userID}`, and
`/api/cleanup` are all live and tested end-to-end. The frontend plan
(`LibrarySelect` component + wiring into the four pages) is a separate plan,
written after this one is reviewed/executed, since it depends on this exact
API contract.
