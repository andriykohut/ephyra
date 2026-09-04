# Ephyra — Library-level filtering

Status: design, approved in brainstorming 2026-09-04.

Jellyfin libraries (`CollectionFolder`s — "Movies", "TV Shows", etc.) already
resolve to a name on every item (`source.LibraryItem.Library`), but that name
only reaches two places today: Library Overview's `library_items`/`disk_by_library`
charts and Cleanup's per-row `library` column. Watch Stats and Profiles have no
library dimension at all. This adds a library filter to three pages — Library
Overview, Watch Stats & Cleanup, Profiles — so picking a library scopes
everything on the page to it. Now Playing stays out of scope (a live-session
view; a historical filter doesn't fit it).

No new page, no new nav entry. Nothing writes to Jellyfin.

## Scope

In:

- A shared library filter (`GET /api/libraries` + a `LibrarySelect` component)
  usable on Library Overview, Watch Stats, Cleanup, Profiles.
- Library Overview: selecting a library recomputes every chart (genre, decade,
  resolution, codec, tag cloud/coverage/pairs, growth, totals) for just that
  library, not only the two charts that already have library data.
- Watch Stats + Cleanup: filter by library, live at read time.
- Profiles: filter by library — summary, completion, abandoned, rewatch, binge,
  taste, baseline, tag overlap all scoped to the selected library.
- `playback_events` gains a resolved `library` column, backfilled once for
  pre-existing rows.
- Unmatched items bucket under the existing `"Unknown"` sentinel (already used
  for unresolved `TopParentId`s) rather than being dropped from filtered totals.

Out:

- Now Playing.
- Multi-library selection (one library at a time, or "All" — matches the
  existing single-select `UserSelect` pattern on Watch Stats).
- An `APISource` implementation of any of this.
- Renaming a Jellyfin library after the fact re-bucketing history under the new
  name — library identity is the display name, same as `agg_cleanup.library`
  today; a rename is a new bucket going forward. Not solved here.

## Section 1 — Data model

### Source layer

`internal/source/source.go`:

- `LibrarySnapshot.SeriesCount int` → `SeriesCounts map[string]int` (library
  name → series count). `Library`-scoped aggregation needs a per-library series
  count, not just a global one.
- `PlaybackEvent` gains `Library string` (resolved folder name, or `"Unknown"` —
  same values `LibraryItem.Library` already uses).

`internal/source/file/queries.go`:

- The series-count query (`queries.go:174-178`, currently
  `SELECT count(*) FROM BaseItems WHERE Type = ?`) becomes a `GROUP BY` on the
  same `folders` CTE the item query already joins against
  (`LEFT JOIN folders f ON f.fid = s.TopParentId`), producing one row per
  library. Scan into `snap.SeriesCounts`.
- The watch job's targeted `jellyfin.db`-copy read (where it resolves
  `item_name`/`series_name`/etc. per event) adds the same `folders` CTE, joined
  on the **event's own item id** — always the episode's `TopParentId`, never
  inherited from the series. This is unlike tags (`PlaybackEvent.ItemTags`, see
  `2026-09-01-ephyra-tags-design.md`), which *do* inherit from the parent series
  for episodes: tags are a taste facet the series owns, but library is a
  filesystem location every item — episode or series — already sits in
  directly, so there's no inheritance step needed. Unmatched → `"Unknown"`.

### Store: `playback_events`

New column, migration `0005` (see Section 6): `library TEXT NOT NULL DEFAULT 'Unknown'`.

`internal/store/spine.go`:
- `AppendPlaybackEvents`: insert `library`; add `library = excluded.library` to
  the `ON CONFLICT ... DO UPDATE SET` list (refreshes on re-report, like
  `item_genres` etc.).
- `ReadPlaybackEvents`: select + scan `library` into `PlaybackEvent.Library`.

### Backfill (one-time, on the watch job)

Historical rows default to `'Unknown'` from the migration. On the first watch
run after this ships, before processing new plugin rows, the watch job selects
distinct `item_id`s currently at `library = 'Unknown'`, resolves each against
the current `jellyfin.db` copy (same `folders` CTE), and `UPDATE`s matches.
Items no longer in Jellyfin stay `Unknown` — expected, no signal to resolve
them from. This runs once because after the first pass no row is spuriously
`'Unknown'` anymore (only genuinely-unresolvable ones are), so there's nothing
left to backfill on subsequent runs — no flag needed, the `WHERE library =
'Unknown'` query naturally does less each time until it's a no-op.

## Section 2 — Library Overview pipeline

`aggregate.Library(snap, loc)` stays unchanged — no filter parameter. Instead,
the **caller** filters the snapshot before invoking it:

```go
func filterLibrary(snap source.LibrarySnapshot, lib string) source.LibrarySnapshot {
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
    out.SeriesCount = snap.SeriesCounts[lib]
    return out
}
```

`internal/scheduler/scheduler.go:107` (`RunLibraryOnce`) changes from one call
to computing every scope up front, then **one** write call:

```go
libs := distinctLibraries(snap.Items) // unique it.Library values
scoped := map[string]aggregate.LibraryAggregates{"": aggregate.Library(snap, time.Local)}
for _, lib := range libs {
    scoped[lib] = aggregate.Library(filterLibrary(snap, lib), time.Local)
}
if err := s.st.WriteLibraryAggregates(ctx, scoped, cleanup, users, core); err != nil { ... }
```

`WriteLibraryAggregates` also takes `libs` (the same slice used to build
`scoped`) and, in the same transaction, `DELETE FROM dim_library` +
re-`INSERT`s it — same full-rewrite pattern as `dim_user`. This is what backs
`GET /api/libraries` (Section 7).

`WriteLibraryAggregates` changes shape: its first param becomes
`map[string]aggregate.LibraryAggregates` (library → that scope's result,
`""` = All). It keeps its existing single-transaction, single-blanket-DELETE
structure (`writes.go:21-34`) unchanged — still exactly one `DELETE FROM
agg_totals` etc., not one per library — and its insert loops (line 49-92) gain
an outer loop over the map, writing every row with its map key as the
`library` column. This preserves the "a reader never sees a half-written set"
guarantee the function already documents: one call, one transaction, every
library's rows land together. `cleanup`/`users`/`core` keep their existing
single (unscoped) shape — they're not part of the "recompute every chart per
library" scope (Cleanup gets its own param wiring in Section 5; `users`/`core`
back Watch Stats, covered in Section 3).

This reuses `Library()` and its existing tests completely unchanged — every
`LibraryAggregates` produced by a filtered snapshot is exactly as correct as
today's single unfiltered call, by construction.

### Reads

`internal/store/reads.go`: `ReadLibraryOverview(ctx)` becomes
`ReadLibraryOverview(ctx, library string)` (`""` = All, unchanged behavior).
Every query in it gains `WHERE library = ?` (or `AND library = ?` alongside
the existing `dimension`/`month` filters):

- The bare `SELECT metric, value FROM agg_totals` (line 60) → `WHERE library = ?`.
- `readDisk(ctx, dim)` (line 161) and `readDistro(ctx, dim, chrono)` (line 184)
  each gain a `library` parameter, added to their existing `WHERE dimension = ?`.
- The `agg_library_growth` query (line 113-114) gains `WHERE library = ?`.
- `readTagPairs` (line 143) gains `WHERE library = ?`.

Note: `ItemsByLibrary`/`DiskByLibrary` are still computed for every filtered
run (trivially: one row, 100%, for the selected library). The frontend ignores
them when a library filter is active rather than the backend suppressing them
(Section 8) — simpler than adding a "skip this pair of charts" special case to
the write loop.

## Section 3 — Watch Stats pipeline

Unlike Library Overview, `watch_events_daily`/`agg_watch_heatmap`/`agg_played_core`
are already maximally granular (per day/user/item, or per user/item) and
filtered live via SQL `WHERE` at read time — that's how `range`/`user` work
today (`reads_watch.go`). Library becomes another denormalized column, no
precompute multiplication needed.

`internal/aggregate/rows.go`: `WatchDailyRow` and `HeatmapRow` gain `Library
string`. `internal/aggregate/watch.go`:
- `dKey` (line 19) doesn't need `library` — it's a property of `item`, not part
  of what makes two events the same daily fact. Set `r.Library = e.Library`
  where the row is created (line 36-39), alongside `Name`/`SeriesID`.
- `hKey` (line 20-23) **does** need `library` added — the heatmap aggregates
  across *all* items for a `(user, dow, hour)` cell, so filtering by library
  after the fact requires the rows to already be split by library, the same
  reason `user_id` is already in `hKey`. Add `library` to `hKey` and to
  `HeatmapRow`.

`CorePlayRow` (from `aggregate.CorePlays`, `internal/aggregate/coreplays.go`)
gains `Library string`, sourced from the item's `Library` in `snap.UserPlays`
— check whether `UserPlay` needs a `Library` field too (it currently doesn't;
add it, resolved the same way as `LibraryItem.Library` since `userPlaysQuery`
already joins `BaseItems`).

Migration `0005` adds `library TEXT NOT NULL DEFAULT ''` to `watch_events_daily`
and `agg_played_core`, and rebuilds `agg_watch_heatmap`'s primary key to
`(user_id, dow, hour, library)`.

`internal/store/writes_watch.go` / `writes.go` (the `agg_played_core` insert):
add `library` to each `INSERT`'s column list, sourced from the row.

`internal/store/reads_watch.go`:
- `WatchStatsParams` gains `Library string`.
- `scoped`/`scopedArgs` (line 170-175) gains `AND library = ?` when
  `p.Library != ""`, same shape as the existing `user_id` clause.
- `hWhere`/`hArgs` (line 329-332) gains the same.
- `readCore` (line 391) gains a `library` param, added to its `WHERE` when set.
- The `Users` list (line 131, `dim_user`) is **not** library-filtered — the
  user dropdown always lists everyone; a user with no plays in the selected
  library just shows zeroed panels, exactly like a user with no plays in a
  30-day range today.

## Section 4 — Profiles pipeline

`aggregate.Profiles(events, now)` stays unchanged, same trick as Library
Overview: filter `events` by `e.Library` *before* calling it, not inside it.

```go
func filterEventsByLibrary(events []source.PlaybackEvent, lib string) []source.PlaybackEvent {
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
```

`internal/scheduler/scheduler.go:177` changes the same way Section 2's write
does — compute every scope, one write call:

```go
libs := distinctLibraries(history) // unique e.Library values
scoped := map[string]aggregate.ProfileAggregates{"": aggregate.Profiles(history, time.Now())}
for _, lib := range libs {
    scoped[lib] = aggregate.Profiles(filterEventsByLibrary(history, lib), time.Now())
}
if err := s.st.WriteProfileAggregates(ctx, scoped); err != nil { ... }
```

`WriteProfileAggregates`'s param becomes `map[string]aggregate.ProfileAggregates`
(library → result). Same reasoning as `WriteLibraryAggregates`: one blanket
`DELETE` per table (unchanged from `writes_profile.go:18-31`), one transaction,
an outer loop over the map in each of the eight insert loops, each row written
with its map key as `library`. A per-library loop calling the writer
repeatedly would have each call's blanket `DELETE FROM agg_profile_summary`
(etc.) erase the previous library's just-written rows — the map-in, single-call
shape avoids that. Every `agg_profile_*` table plus `agg_taste_baseline` and
`agg_profile_tag_overlap` gets `library` added to its primary key in migration
`0005`.

**Why this is safe despite `distinctUsers`/`bingeRuns` being per-call:** a user
with zero events in library X simply never appears in that library's
`Profiles()` output — no summary/completion/etc. rows get written for
`(user, *, library=X)`. This is not a new failure mode: `ReadProfile`'s "user
not found" check is `SELECT name FROM dim_user WHERE id = ?` only
(`reads_profile.go:172`), completely independent of whether any
`agg_profile_summary` row exists. Every subsequent read already tolerates a
missing row via `sql.ErrNoRows` → zero-valued fields (`readProfileSummary`,
line 231). So a user with no plays in the selected library gets back a
correctly zeroed profile, not a 404 — the exact same code path that already
handles "no plays in the last 30 days" today.

`internal/store/reads_profile.go`: `ReadProfile(ctx, userID, rng)` becomes
`ReadProfile(ctx, userID, rng, library string)`. Every `readProfile*` helper
adds `AND library = ?` to its `WHERE` (all eight). `readProfileTagOverlap`'s
`WHERE (o.user_a = ? OR o.user_b = ?) AND o.range = ?` gains `AND o.library = ?`
— tag overlap becomes "this user vs others, computed from only this library's
tag vectors," which is the intended meaning (both users' vectors were built
from the same library-filtered event set — see the baseline note just below).

`agg_taste_baseline` under a library filter reflects only that library's
watch-time distribution (it's written once per `Profiles()` call, same as
everything else in that call) — so signature detection
(`signatureShares`, comparing user share vs. baseline share) stays internally
consistent: a library-filtered profile compares against a library-filtered
baseline, not the global one.

## Section 5 — Cleanup

`agg_cleanup.library` already exists per-row (`internal/store/writes.go:94-102`).
Pure param wiring, no schema change:

- `internal/store/reads_cleanup.go`: `CleanupParams` gains `Library string`;
  its query gains `AND library = ?` when set.
- `internal/api/cleanup.go`: reads `q.Get("library")`, passes through to
  `CleanupParams` and to `StreamCleanupCSV` (CSV export honors the same filter).

## Section 6 — Storage: migration `0005_library_filter.sql`

Forward-only, does not touch `schema_migrations`. `agg_*` tables here are
always fully rewritten per refresh (per-job DELETE+re-INSERT), so `DROP TABLE`
+ `CREATE TABLE` to change a primary key is safe, matching the pattern already
used in `0002_watch_cleanup.sql`.

```sql
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

`''` is the "All libraries" sentinel throughout — matches how `user=""` already
means "all users" in `WatchStatsParams`. `'Unknown'` (capitalized, matching
`LibraryItem.Library`'s existing convention) is a real bucket value, never the
sentinel.

## Section 7 — API

- **New** `GET /api/libraries` → `{"data": ["Movies", "TV Shows", "Unknown"], ...}`,
  sorted by item count desc then name, sourced from `dim_library` (written by
  the library job alongside `dim_user`, same full-rewrite pattern).
- `GET /api/library/overview?library=` — omitted or `all` = unfiltered (today's
  behavior byte-for-byte). Any other value reads the `library`-scoped rows;
  an unrecognized name yields a valid, all-zero response (same leniency as an
  unrecognized `user` id today), not a 400.
- `GET /api/watch/stats?range=&user=&library=`
- `GET /api/profile/{userID}?range=&library=`
- `GET /api/cleanup?mode=&sort=&limit=&format=&library=`

`library=all` maps to `""` server-side, mirroring `user=all` in
`internal/api/watch.go:24-26`.

## Section 8 — Frontend

### `LibrarySelect`

New shared component, `web/src/components/`, styled like the existing
`UserSelect` on Watch Stats: single-select, "All Libraries" first, then names
from `GET /api/libraries` (one shared long-`staleTime` query — the library list
changes only on a library refresh), `Unknown` sorted last regardless of count.

### Wiring

- `web/src/api/queries.ts`: each affected query gains a `library` param,
  threaded into its query key exactly like `range`/`user` already are
  (`['watch-stats', range, user, library]`, etc.) so react-query treats a
  library change as a normal refetch, not a special case.
- `library.tsx`, `watch.tsx`, `cleanup.tsx`, `profile.tsx`: add `<LibrarySelect>`
  near the existing filters (`UserSelect` on Watch Stats, the mode/sort controls
  on Cleanup, the range `<select>` on Profiles).
- Library Overview only: `ItemsByLibrary`/`DiskByLibrary` charts keep rendering
  the **unfiltered** ("All") data regardless of the page's library filter —
  they're how you discover/pick a library in the first place, so filtering them
  to themselves would just show one trivial 100% bar. Every other chart on the
  page follows the filter normally.

## Section 9 — Edge cases

- **Unmatched items** ("Unknown" library) are a first-class filter option
  everywhere, not silently dropped — sums under "All" always equal the sum
  across every listed library option, "Unknown" included.
- **Historical playback events pre-migration** default to `Unknown` until the
  one-time backfill resolves what it can (Section 1). Items later deleted from
  Jellyfin stay `Unknown` permanently — there's nothing left to resolve them
  against.
- **A user with no plays in the selected library** gets a correctly zero-valued
  Profile/Watch Stats response, not a 404 — falls out of existing null-handling
  (Section 4), no special-casing needed.
- **A library with zero items** (edge case, e.g. an empty Jellyfin library)
  still appears in `GET /api/libraries` once any item has ever resolved to it;
  if literally nothing has, it simply won't be a filter option — nothing to
  filter to.
- **Cardinality**: `agg_profile_*` row counts scale ×(libraries+1); for a
  typical home-server library count (2–10) this stays trivially small for
  SQLite. Not treated as a performance concern; revisit only if real usage
  shows otherwise.

## Section 10 — Testing

### `internal/source/file`

- `testdata/library.fixture.sql`: add a second `CollectionFolder` and items
  under it (fixture currently likely single-library — confirm and extend).
- Assert `LibrarySnapshot.SeriesCounts` is per-library, sums to the old global
  total.
- Assert `PlaybackEvent.Library` resolves per event; an event for a deleted
  item → `"Unknown"`.

### `internal/aggregate`

- `library_test.go`: `filterLibrary` + `Library()` on a filtered snapshot
  matches hand-computed per-library expectations; sum of every library's
  `Totals["items.total"]` equals the unfiltered total.
- `watch_test.go`: `WatchDailyRow.Library`/`HeatmapRow.Library` set correctly;
  two libraries' events don't collide in the heatmap (`hKey` now includes
  library).
- `profile_test.go`: `filterEventsByLibrary` + `Profiles()` on a filtered event
  set matches hand-computed expectations; a user with events only in library A
  produces no rows when filtered to library B.

### `internal/store`

- `WriteLibraryAggregates`/`WriteProfileAggregates` signature changes
  (map-of-scopes, plus `libs` for the former) — every existing call site,
  including `reads_cleanup_test.go`'s `WriteLibraryAggregates(ctx,
  aggregate.LibraryAggregates{...}, nil, nil, nil)`, updates to pass
  `map[string]aggregate.LibraryAggregates{"": {...}}` (and `nil` for `libs`
  where a test doesn't care about `dim_library`).
- Migration `0005` applies cleanly on top of `0004`.
- Round-trip every new `library`-keyed table through its writer + reader with
  at least two distinct library values plus `""` (All).
- `TestReadWatchStats_RangeAndUserFilters`-style test extended with a library
  dimension: filtering by library narrows totals correctly, `""`/`all` matches
  today's unfiltered numbers exactly.
- `ReadProfile` for a user with zero rows in the requested library returns
  `ok=true` with zero-valued fields, not `ok=false`.
- Cleanup CSV export honors `library`.

### `internal/api`

- Each of the four endpoints: `library=all`/omitted matches today's byte-for-byte
  response; a real library name narrows results; an unrecognized name returns
  a valid all-zero/empty response, not 400.
- `GET /api/libraries` returns the expected sorted list, including `Unknown`
  when applicable.

### Frontend

- `LibrarySelect.test.tsx`: renders options from the fetched list, `all` is
  default/first.
- One test per page: selecting a library changes the relevant query key and
  triggers a refetch; Library Overview's `ItemsByLibrary`/`DiskByLibrary`
  charts stay on "All" data when a library filter is active.

## Build order

1. Migration `0005` + store read/write scaffolding for every touched table,
   tests (empty-safe, matches `0004`'s pattern).
2. Source: `SeriesCounts` map, `PlaybackEvent.Library`, watch-job library
   resolution + backfill pass, fixtures + tests.
3. Library Overview: `filterLibrary`, scheduler loop, `WriteLibraryAggregates`
   library param, store reads gain `library`, tests.
4. Watch Stats: `Library` field on `WatchDailyRow`/`HeatmapRow`/`CorePlayRow`,
   `hKey` change, `ReadWatchStats` library param, tests.
5. Profiles: `filterEventsByLibrary`, scheduler loop, `WriteProfileAggregates`
   library param, `ReadProfile` library param across all eight sub-readers,
   tests.
6. Cleanup: `CleanupParams.Library`, CSV export, tests.
7. API: `GET /api/libraries`, `library=` param on the four endpoints, tests.
8. Frontend: `LibrarySelect`, query-key wiring across four pages, Library
   Overview's "charts stay on All" exception, tests.
9. `make lint test`, then `make build`, then a manual pass against a real
   multi-library DB copy per README's "Test against a real library".
