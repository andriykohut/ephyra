# Ephyra — User Profiles ("Wrapped") — Design

Status: approved for planning
Date: 2026-08-31
Parent spec: `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`
Implements: parent backlog #3 (per-user profiles: completion vs abandonment,
rewatch, binge detection) on top of a minimal slice of backlog #4 (Ephyra's own
event history). Adds a fifth page, `/profile`.

---

## 1. Summary

A per-user profile page. Pick a user, pick a range, get four panels: completion
vs abandonment, rewatch vs first-watch, binge/marathon runs, and a taste
fingerprint (genre / decade / runtime-length, weighted by time actually
watched, shown against the library baseline).

To make any of this trustworthy over time, Ephyra stops living inside the
Playback Reporting plugin's retention window. A new append-only table,
`playback_events`, accumulates every plugin session Ephyra has ever seen. The
`watch` job writes to it each run; **Watch Stats moves onto it too**, so there
is one history of record instead of two read paths.

The shareable "year in review" card is a later view over the same materialized
rows — not in this build. Page first.

Non-negotiables carried from the parent spec: never write to Jellyfin; never
serve 5xx for a page because a refresh failed; the frontend ships in the binary;
no auth — the picker is open to anyone who can reach Ephyra.

---

## 2. Goals & non-goals

### Goals

- `GET /api/profile` (user list + headline numbers) and
  `GET /api/profile/{userID}?range=30d|90d|1y|all` (the four panels), served
  entirely from materialized `agg_profile_*` tables.
- `playback_events`: an append-only spine that outlives the plugin's max-age
  setting. Populated from the same `src.PlaybackEvents` call the `watch` job
  already makes — no new load on Jellyfin.
- Watch Stats reads from the spine. `aggregate.Watch` is unchanged; only the
  source of its input slice moves.
- Backfill-on-upgrade: the first run after this ships ingests whatever the
  plugin currently retains, then grows from there.
- The `watch` job stays within the parent's footprint: no new HTTP calls, one
  slowly-growing table, ~100–300 ms of extra aggregation once per interval at
  the high end.
- Degrades like Watch Stats: plugin absent → `plugin_available:false`, the
  picker still renders from `dim_user`, panels show the "enable the plugin"
  state; the spine keeps serving prior rows.

### Non-goals

- **No recap card UI** in this build. `/profile/$user/recap` is a later view
  over the same `agg_profile_*` rows.
- **No Now Playing samples in the spine** — bandwidth-over-time and
  recently-ended sessions stay parent backlog #4.
- **No Jellyfin-credential login / per-user scoping** (parent backlog #2). Every
  profile is visible to every visitor, same posture as the rest of Ephyra.
- **No custom date ranges** — the same fixed `30d|90d|1y|all` set as Watch
  Stats.
- No movie-marathon detection, no env-configurable thresholds, no
  streaming/incremental aggregation (only if the transient-memory spike ever
  bites).
- No writes to Jellyfin. No `SOURCE=api`.

---

## 3. Data flow

```
watch job tick (mtime-gated as today, plus: bypassed while playback_events is empty)
  └─ src.PlaybackEvents(ctx, zero)          ← unchanged read of the plugin DB copy,
  │                                           enriched against the jellyfin.db copy
  │                                           (enrichment query WIDENED — see §5)
  └─ store.AppendPlaybackEvents(newEvents)  ← INSERT … ON CONFLICT(dedup_hash):
  │                                           immutable cols never change;
  │                                           item_name / series_* refreshed
  └─ all := store.ReadPlaybackEvents(ctx)   ← the full accumulated history
  └─ aggregate.Watch(all)         → watch_events_daily, agg_watch_heatmap   (logic unchanged)
  └─ aggregate.Profiles(all, now) → agg_profile_*, agg_taste_baseline       (new)
```

Three sequential transactions per run: (1) `AppendPlaybackEvents`, (2)
`WriteWatchAggregates` (its own txn, unchanged), (3) `WriteProfileAggregates`.
The append is idempotent (`ON CONFLICT`), and the derived tables are full
rewrites every run, so a crash between transactions self-heals — the next run
rebuilds the derived tables from whatever the spine holds.

API handlers read ONLY the `agg_*` / `*_daily` tables, plus `playback_events`'
own min/max/count for the `coverage` block.

`playback_events` is the exception to "every table is rewritten per refresh": it
is append-only and never DELETEd. It *is* the history.

---

## 4. `playback_events` — the spine (migration `0003_profiles.sql`)

| column | type | notes |
|---|---|---|
| `at` | TEXT | plugin's server-local wall-clock literal. **Not re-zoned** — matches `aggregate.Watch` / the parent's "watch timestamps are not re-zoned" rule. |
| `user_id` | TEXT | canonical (dashless lowercase) |
| `item_id` | TEXT | canonical |
| `item_type` | TEXT | `movie` \| `episode` |
| `method` | TEXT | raw plugin `PlaybackMethod` string; bucketed at aggregate time, as today |
| `play_duration_sec` | INTEGER | |
| `item_name` | TEXT | last-known name; the plugin's own value at ingest, refreshed from `jellyfin.db` on conflict. Retained verbatim once an item is deleted from Jellyfin. |
| `series_id` | TEXT | canonical; `''` for movies / unresolved. From `jellyfin.db` enrichment only. |
| `series_name` | TEXT | `''` for movies / unresolved |
| `item_runtime_sec` / `item_year` / `item_genres` | INT / INT / TEXT | library-fact snapshot from the `jellyfin.db` enrichment, refreshed on conflict while the item exists, retained verbatim once it's deleted. `item_genres` is pipe-joined. **Stored** (not re-derived at read time) because `ReadPlaybackEvents` feeds `aggregate.Profiles` directly and the source enrichment only covers rows the plugin still has. |
| `dedup_hash` | TEXT | `UNIQUE`. `sha1(at | "\x1f" | user_id | "\x1f" | item_id | "\x1f" | play_duration_sec)` |

Indexes: `(user_id, item_id)`, `(at)`.

### 4.1 Append semantics

```sql
INSERT INTO playback_events
  (at, user_id, item_id, item_type, method, play_duration_sec,
   item_name, series_id, series_name, item_runtime_sec, item_year, item_genres, dedup_hash)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(dedup_hash) DO UPDATE SET
  item_name        = excluded.item_name,
  series_id        = excluded.series_id,
  series_name      = excluded.series_name,
  item_runtime_sec = excluded.item_runtime_sec,
  item_year        = excluded.item_year,
  item_genres      = excluded.item_genres;
```

The `ON CONFLICT` clause keeps names fresh for items that still exist while
leaving deleted-item rows with their last good enrichment. Immutable columns
(`at`, `user_id`, `item_id`, `item_type`, `method`, `play_duration_sec`) are
never touched after first insert.

The append runs in its own transaction, before the derived-table rewrites. It
is idempotent, so a crash between transactions self-heals: the next run
re-appends (all no-ops) and rebuilds the derived tables.

### 4.2 Backfill on upgrade

The `watch` job's mtime-skip currently short-circuits the entire job when
`playback_reporting.db` is untouched. New rule: **the skip is also bypassed
while `SELECT EXISTS(SELECT 1 FROM playback_events)` is false.** So the first
run after this feature ships performs a full ingest of the plugin's current
retention even if the plugin DB has not changed since the last pre-upgrade run.
Once the spine has any row, normal mtime-skip resumes.

### 4.3 Dedup edge cases

- Two genuinely distinct plays by the same user of the same item, in the same
  clock-second, with identical `play_duration_sec` → collapse to one row.
  Accepted; vanishingly rare.
- The plugin editing a historical row's `play_duration_sec` post-hoc → the hash
  changes, producing a second row and a double-count for that one play.
  Believed not to happen; not handled. Flagged in §11 open questions.
- The plugin pruning old rows under its max-age setting → the spine keeps them.
  That is the entire point of the spine.

### 4.4 Growth

One row per plugin session, ~150–250 B with indexes. Typical home server (tens
of plays/day) → single-digit MB/year. Heavy multi-user (~200/day) → ~15–40
MB/year, ~73k rows/year. SQLite is comfortable into the millions. There is **no
prune knob** — pruning would defeat retention independence. The README gets one
line telling operators this table grows.

---

## 5. Source: widen the enrichment read

`aggregate.Profiles` needs per-watched-item runtime, genres, and release year.
These come from the `file` source's existing targeted read of the `jellyfin.db`
copy (`enrichPlaybackEvents` in `internal/source/file/playback.go`), extended to
also select, for the watched-item subset:

- `RunTimeTicks` → `ItemRuntimeSec` (÷ 10_000_000)
- genres → `ItemGenres []string`, reusing the library job's 10.11 genre-query
  shape
- `ProductionYear` → `ItemYear int`

New fields on `source.PlaybackEvent`: `ItemRuntimeSec int64`, `ItemGenres
[]string`, `ItemYear int`. `AppendPlaybackEvents` snapshots them into
`playback_events` and `ReadPlaybackEvents` hands them back — the watch job feeds
`aggregate.Profiles` from the spine read, and the source enrichment only covers
rows the plugin still reports, so re-deriving at read time would leave every
historical row blank. On conflict the snapshot is refreshed from the current
`jellyfin.db` copy; an item deleted from Jellyfin keeps its last-known values,
and a play the plugin recorded before Ephyra could resolve the item falls into
the `unknown` bucket (§6.1), same as any missing-runtime row.

No new HTTP calls to Jellyfin. The widened query hits the local copy only.

---

## 6. `aggregate.Profiles` — pure, no I/O

Signature: `Profiles(events []source.PlaybackEvent, now time.Time) ProfileAggregates`
where `now` is container-local (parent §5.3), used for the range cutoffs.
`ProfileAggregates` carries the row slices for every `agg_profile_*` table.

### 6.0 Roll to daily grain first

The plugin writes multiple rows per viewing (pause/resume = separate sessions).
`Profiles` first folds raw events to **(user, item, calendar-day)**, summing
`play_duration_sec` — the same grain as `watch_events_daily`. Every downstream
verdict is one-per-title-per-day. `at`-level ordering is kept only where §6.3
needs it (binge gap detection).

### 6.1 Completion vs abandonment

Per (user, item, day): `ratio = watched_sec / ItemRuntimeSec`.

| bucket | condition |
|---|---|
| `finished` | `ratio ≥ 0.90` |
| `partial` | `0.25 ≤ ratio < 0.90` |
| `bailed` | `ratio < 0.25` |
| `unknown` | `ItemRuntimeSec` is 0 / missing |

Thresholds are named constants in `internal/aggregate`
(`completionFinished = 0.90`, `completionBailed = 0.25`). Episodes use their own
runtime.

Output: `agg_profile_completion` (per user × range × scope × bucket → count) and
`agg_profile_abandoned` (per user × range: top-N titles by `bailed` count;
episodes rolled up to their series).

### 6.2 Rewatch vs first-watch

Per (user, movie) and per (user, episode): count distinct days carrying a
non-`bailed` verdict. Days after the first are rewatches.
`rewatch_pct = rewatch_days / total_non_bailed_days`.

Output: `agg_profile_rewatch` — top-N (user × scope × item) by distinct
non-`bailed` watch-days, **lifetime only** (stored once under `range='all'`,
like `most_played_core`).

### 6.3 Binge / marathon

Per (user, series): sort that user's episode events by `at`. A **run** is a
maximal chain of same-series episode watches with `< bingeGapHours` (= 4)
between consecutive `at` values. Run length = distinct episodes in the run.

Output: `agg_profile_binge` — top-N runs (user × series → run_episodes,
run_start, run_end), **lifetime only**. Plus, on `agg_profile_summary`:
`longest_binge_episodes` + `longest_binge_series_name`, and (per range)
`show_of_range` = series with the most `watched_sec` in the range window.

Movies are excluded. Movie-marathon detection is deferred.

### 6.4 Taste fingerprint

Over the user's (user, item, day) rows in the range window, **weighted by
`watched_sec`**, not play count:

- `dim = genre` — one contribution per genre on the item (multi-count)
- `dim = decade` — `ItemYear` floored to the decade; `unknown` when 0
- `dim = length` — movies: `<90m` / `90–120m` / `>120m`; episodes: `<30m` /
  `30–60m` / `>60m`

Output: `agg_profile_taste` (user × range × dim × key → watched_sec, plays).

Baseline: `agg_taste_baseline` (dim × key → watched_sec), computed in the same
`aggregate.Profiles` pass over **all users' events, all time**, so "you vs the
library" uses one consistent weighting. Signature genres = keys where the
user's share of their own total most exceeds the baseline's share.

### 6.5 Ranges

`Profiles` precomputes **every range-scoped panel for all four of `30d`, `90d`,
`1y`, `all`** using `now` minus 30 / 90 / 365 days (string-formatted, lexical
compare on the stored `day`, exactly as `watch/stats` does it). Each
`agg_profile_*` row that is range-scoped carries a `range` TEXT column. The API
does `WHERE user_id = ? AND range = ?` and nothing else. Row counts stay small:
per user, ~4 ranges × a handful of buckets / top-N lists.

Lifetime-only outputs (`agg_profile_rewatch`, `agg_profile_binge`, and the
`rewatch_pct` / `longest_binge_*` columns on `agg_profile_summary`) are written
once under `range='all'`.

---

## 7. Store — tables & functions

### 7.1 Migration `0003_profiles.sql` adds

- `playback_events` — §4.
- `agg_profile_summary` — `(user_id, range)` PK-ish; columns: `watch_sec`,
  `plays`, `distinct_titles` (movies + distinct series, episodes rolled up),
  `days_active` (distinct calendar days with a play in range), `finished_pct`, `bailed_pct`,
  `rewatch_pct` (only meaningful at `range='all'`), `longest_binge_episodes`,
  `longest_binge_series_name`, `show_of_range_series_id`,
  `show_of_range_series_name`, `first_play`, `last_play`.
- `agg_profile_completion` — `(user_id, range, scope, bucket, count)`.
- `agg_profile_abandoned` — `(user_id, range, scope, item_id, name,
  series_name, bailed_count)`.
- `agg_profile_rewatch` — `(user_id, scope, item_id, name, series_name,
  watch_days)`.
- `agg_profile_binge` — `(user_id, series_id, series_name, run_episodes,
  run_start, run_end)`.
- `agg_profile_taste` — `(user_id, range, dim, key, watch_sec, plays)`.
- `agg_taste_baseline` — `(dim, key, watch_sec)`.

Forward-only, integer-prefixed, no `CREATE` of `schema_migrations` (parent
rules). All list reads run through `orEmpty` so the API sees `[]`, never
`null`.

### 7.2 Functions

- `AppendPlaybackEvents(ctx, []source.PlaybackEvent) error` — the §4.1 upsert,
  its own transaction.
- `ReadPlaybackEvents(ctx) ([]source.PlaybackEvent, error)` — full history,
  ordered by `at`. Feeds both `aggregate.Watch` and `aggregate.Profiles`.
- `SpineCoverage(ctx) (first, last string, total int64, err error)` — for the
  API `coverage` block.
- `WriteProfileAggregates(ctx, ProfileAggregates) error` — one transaction:
  DELETE + re-INSERT every `agg_profile_*` and `agg_taste_baseline` row.
- `ReadProfileList(ctx) (...)`, `ReadProfile(ctx, userID, range) (...)` — the
  two API reads.

---

## 8. Scheduler — the `watch` job

`RunWatchOnce` changes as little as possible:

1. mtime-skip check, now also bypassed when the spine is empty (§4.2).
2. `events, err := s.src.PlaybackEvents(ctx, time.Time{})` — unchanged.
   `ErrPluginUnavailable` path is unchanged: record `plugin_available=false`,
   write nothing, the spine keeps its rows.
3. `AppendPlaybackEvents(ctx, events)` — txn 1.
4. `all, _ := ReadPlaybackEvents(ctx)` — the full accumulated history.
5. `WriteWatchAggregates(ctx, aggregate.Watch(all)...)` — txn 2, unchanged callee.
6. `WriteProfileAggregates(ctx, aggregate.Profiles(all, s.now()))` — txn 3.
7. `SetRefreshMeta` for `watch` as today. `stale` semantics unchanged.

Cost: one full-history load + two O(n) roll-to-daily passes + a 4-range
re-scan, per tick, mtime-gated. ~100–300 ms at the high end (few years of heavy
history); tens of ms otherwise. Transient memory: the history slice, ~15 MB per
year-of-heavy-history, freed on return. Within the parent's ~15–30 MB idle
footprint (this is a per-tick spike, not standing).

---

## 9. HTTP API

Envelope per parent §8.1. `meta.stale` from the `watch` job's `refresh_meta`.

### 9.1 `GET /api/profile`

No params. `data`:

```jsonc
{
  "plugin_available": true,
  "coverage": { "first_play": "2025-01-06", "last_play": "2026-08-30", "total_plays": 1421 },
  "users": [
    { "id": "11111111…", "name": "alice",
      "total_watch_sec": 0, "total_plays": 0,
      "finished_pct": 0.0, "rewatch_pct": 0.0,
      "longest_binge_episodes": 0, "last_play": "2026-08-29" }
  ]
}
```

`users` always present, from `dim_user` joined to `agg_profile_summary`
(`range='all'`); a user with no plays gets zeroed numbers. `coverage` from
`SpineCoverage`, over the whole spine.

Plugin absent: `plugin_available:false`, `coverage` zeroed, `users` still lists
everyone with zeroed numbers.

### 9.2 `GET /api/profile/{userID}`

Param: `range ∈ 30d|90d|1y|all` (default `30d`). `data`:

```jsonc
{
  "range": "30d",
  "user": { "id": "11111111…", "name": "alice" },
  "summary": {
    "watch_sec": 0, "plays": 0, "distinct_titles": 0, "days_active": 0,
    "finished_pct": 0.0, "bailed_pct": 0.0,
    "rewatch_pct": 0.0,                                  // lifetime
    "longest_binge": { "episodes": 0, "series_name": "" }, // lifetime
    "show_of_range": { "series_id": "", "series_name": "" },
    "first_play": "2025-01-06", "last_play": "2026-08-29"
  },
  "completion": [{ "scope": "movie", "bucket": "finished", "count": 0 }],
  "abandoned":  [{ "scope": "series", "item_id": "", "name": "", "series_name": "", "bailed_count": 0 }], // ≤10
  "rewatch":    [{ "scope": "movie", "item_id": "", "name": "", "series_name": "", "watch_days": 0 }],    // ≤10, lifetime
  "binge":      [{ "series_id": "", "series_name": "", "run_episodes": 0, "run_start": "", "run_end": "" }], // ≤10, lifetime
  "taste": {
    "genre":  [{ "key": "Drama", "watch_sec": 0, "plays": 0 }],
    "decade": [{ "key": "2010", "watch_sec": 0, "plays": 0 }],
    "length": [{ "key": "90-120m", "watch_sec": 0, "plays": 0 }],
    "signature_genres": ["Documentary", "Thriller"]
  },
  "baseline": {
    "genre":  [{ "key": "Drama", "watch_sec": 0 }],
    "decade": [{ "key": "2010", "watch_sec": 0 }],
    "length": [{ "key": "90-120m", "watch_sec": 0 }]
  }
}
```

- Unknown `userID` → parent 404 JSON (`{ "error": { "code": "not_found", … } }`).
- No `library` refresh yet (no `dim_user`) → parent `not_ready`, HTTP 503, same
  as Watch Stats.
- Plugin absent → 200 with `summary` zeroed and every list `[]`; the frontend
  shows the "enable the plugin" panel.

### 9.3 `POST /api/refresh?job=watch`

Unchanged. Now also rewrites `agg_profile_*`.

---

## 10. Frontend — `/profile`

New route, modelled on `web/src/routes/watch.tsx`:

- `createRoute` off `__root`; `validateSearch` → `{ user: string, range:
  "30d"|"90d"|"1y"|"all" }`, both in the URL.
- Reused components: `SegmentedControl` (range), `UserSelect` (fed from
  `/api/profile` `users`), `Panel`, `DataTable`, `StatCard`, `GenreBand`,
  `StaleBanner`, `Skeleton`, `NotFound`. Nav entry in `AppShell`.
- `web/src/api/queries.ts` + `types.ts`: `profileListQuery()`,
  `profileQuery(userID, range)`, and the DTO types.

Layout:

- Header: `StatCard` row — total watch time, finished %, rewatch %, longest
  binge (episodes + series).
- **Completion** `Panel` — stacked bar (finished / partial / bailed / unknown) +
  "most abandoned" `DataTable`.
- **Rewatch** `Panel` — big rewatch-rate figure + "most rewatched" `DataTable`.
- **Binge** `Panel` — longest-run hero `StatCard`, notable-runs list,
  "show of the last 30 days".
- **Taste** `Panel` — `GenreBand` (you vs `baseline`), decade histogram,
  signature-genres callout.

States mirror Watch Stats: pending → `Skeleton`; error → inline; empty (user
with no plays in range) → coverage-aware empty state, not a blank card; stale →
`StaleBanner`; plugin absent → the shared "enable Playback Reporting" panel with
the picker still usable.

Recap card: a later `/profile/$user/recap` view over the same data. Out of
scope here.

---

## 11. Testing

- **Fixtures.** Extend `testdata/playback_reporting.fixture.sql` and
  `testdata/library.fixture.sql` (matching `RunTimeTicks`, genres,
  `ProductionYear`) so ratios are deterministic. Cover: a `finished`, a
  `partial`, a `bailed`, an `unknown`-runtime row; a movie watched on two
  distinct days (rewatch); three episodes of one series with one > 4h gap that
  splits a run; multi-genre items for taste weighting.
- **`aggregate.Profiles`.** Golden tests in the `library_test.go` /
  `watch_test.go` style: in-code `[]source.PlaybackEvent` + a fixed `now` →
  expected `agg_profile_*` and `agg_taste_baseline` rows. Assert watched-sec
  weighting in taste, the range cutoffs, and that lifetime outputs ignore the
  range.
- **Store.** `AppendPlaybackEvents`: same tuple twice → one row; `ON CONFLICT`
  refreshes `item_name` / `series_id`; immutable columns unchanged. Empty spine
  → mtime-skip bypassed.
- **Scheduler.** `RunWatchOnce` twice with a new event added between runs →
  `watch_events_daily` and `agg_profile_*` reflect the **union**. Existing
  Watch Stats scheduler tests pass unchanged with input coming from the spine.
  Plugin-absent run leaves the spine intact and `agg_profile_*` from the last
  good run in place.
- **API.** `/api/profile` and `/api/profile/{user}` against a store seeded with
  golden rows: envelope, `range` param, unknown-user 404, `not_ready` 503,
  plugin-absent shape, list fields `[]` not `null`.
- **Smoke.** Add both endpoints to the binary smoke sweep (200 + schema).
- **Frontend.** Vitest component tests for `/profile`:
  loading/error/empty/data/stale/plugin-absent, the range control, the user
  picker, the completion stacked bar, the taste "you vs baseline" band.
- **CI.** No new steps; `go test ./...`, `go vet`, `golangci-lint run`,
  `biome ci web`, `vitest run` cover it.

---

## 12. Build sequence (coarse; the plan expands this)

1. Migration `0003`: `playback_events` + `agg_profile_*` + `agg_taste_baseline`.
2. `store`: `AppendPlaybackEvents`, `ReadPlaybackEvents`, `SpineCoverage`, with
   dedup / backfill tests.
3. Widen the `file` source enrichment read; new `source.PlaybackEvent` fields.
4. `RunWatchOnce`: spine append + read-back inside the derived-table txn; feed
   `aggregate.Watch` from the spine; empty-spine skip bypass. Watch Stats
   regression green.
5. `aggregate.Profiles` + `WriteProfileAggregates`, golden tests.
6. `GET /api/profile` + `GET /api/profile/{userID}` + DTOs + handler tests +
   smoke.
7. `/profile` route, queries, components, nav, Vitest.
8. README line about `playback_events` growth; CHANGELOG note that Watch Stats
   history now outlives the plugin's retention.

---

## 13. Open questions / to verify during implementation

- The Jellyfin 10.11 genre-query shape reused from the library job — confirm it
  works for the playback-item subset (it targets the same `BaseItems`).
- Completion thresholds (`0.90` / `0.25`) and `bingeGapHours` (`4`) — confirm
  the defaults read right on real data before locking the constants.
- Does the Playback Reporting plugin ever rewrite a historical row's
  `PlayDuration` after the fact? If so, §4.3's double-count edge is real and
  needs a stable event key instead of the duration-inclusive hash.
- `agg_taste_baseline` recomputed in `aggregate.Profiles` vs derived from the
  library job's genre/decade aggregates — design picks recompute, for one
  consistent watched-sec weighting. Confirm the cost is trivial (it is O(n)
  over the same slice).
- Transient memory: measure the history-slice size on the largest real DB
  available before deciding whether streaming aggregation is needed sooner than
  "if it bites".

---

## 14. Explicitly deferred (parent backlog, not this plan)

- The shareable recap card (`/profile/$user/recap`).
- Now Playing poll samples persisted into the spine → bandwidth-over-time,
  recently-ended sessions (parent backlog #4, the rest of it).
- Jellyfin-credential login + per-user scoping (parent backlog #2).
- `APISource` (parent backlog #1).
- Custom date ranges; movie-marathon detection; env-configurable thresholds;
  streaming/incremental aggregation.
