# Ephyra — Watch Stats & Cleanup — Design

Status: approved for planning
Date: 2026-08-30
Parent spec: `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`
Implements: parent §5.2, §5.5 (watch half), §6 (watch + cleanup tables), §7
(`watch` job), §8.3, §8.4, §10 (`/watch` + `/cleanup` pages), §14 (watch +
cleanup tests). This is **Plan 2 of 3**.

---

## 1. Summary

Two pages, one new refresh job, no new job type.

- **Watch Stats** (`/watch`) — history from the Playback Reporting plugin's
  `playback_reporting.db`: watch time, trend, top titles, an active-users
  leaderboard, a day×hour heatmap, and a direct-play-vs-transcode weekly
  breakdown. A page-level **user filter** re-scopes every panel. When the plugin
  is missing or thin, an always-present "most played" panel built from Jellyfin's
  own play counters keeps the page useful.
- **Cleanup** (`/cleanup`) — a shortlist of movies and series to consider
  removing: never watched by anyone, or watched once and untouched for over a
  year. Sizes, an "reclaimable" total, CSV export. Reads only the core DB, so it
  works with the plugin absent. Never deletes anything.

Both follow the parent architecture unchanged: a scheduled job reads copies of
Jellyfin's SQLite files, pure functions in `aggregate` roll them into `agg_*`
rows, the API serves only from the store.

---

## 2. Goals & non-goals

### Goals

- `/api/watch/stats` and `/api/cleanup` per §9, served entirely from
  materialized store tables.
- Multi-user is first-class in Watch Stats: a filter that re-scopes every
  plugin-derived panel, plus a permanent leaderboard.
- Watch Stats degrades cleanly: plugin absent → `plugin_available:false` but the
  page still renders the user list and the core "most played" panel; range with
  no plays → a coverage-aware empty state, not a blank card.
- Cleanup has zero plugin dependency.
- The `watch` job and the extended `library` job stay within the parent's
  footprint and cadence.

### Non-goals

- No per-user Cleanup ("what has X not watched") — that's a recommendations
  feature, backlog. Cleanup is about shared disk and stays global.
- No client/device breakdown in Watch Stats (parent backlog #5).
- No completion-vs-abandonment / rewatch analysis (parent backlog #3).
- No Ephyra-side event history — Watch Stats shows exactly what the plugin
  retains (parent backlog #4).
- No writes to Jellyfin. Cleanup identifies; the operator acts in Jellyfin.

---

## 3. Task #0 findings — the real plugin and core DBs

Verified 2026-08-30 against a real 10.11.11 (`playback_reporting.db` and
`jellyfin.db`). These supersede the parent spec's expected shapes and go into
`docs/schema-notes.md`.

### 3.1 `playback_reporting.db`

```sql
CREATE TABLE PlaybackActivity (
  DateCreated    DATETIME NOT NULL,   -- 'YYYY-MM-DD HH:MM:SS.fffffff', server local time, no zone marker
  UserId         TEXT,                -- 32 hex chars, NO dashes, lowercase
  ItemId         TEXT,                -- 32 hex chars, NO dashes, lowercase
  ItemType       TEXT,                -- 'Movie' | 'Episode' (others possible; we only keep these two)
  ItemName       TEXT,                -- movie title, or 'Series - s01e02 - Episode title'
  PlaybackMethod TEXT,                -- see 3.3
  ClientName     TEXT,
  DeviceName     TEXT,
  PlayDuration   INT                  -- SECONDS (not ticks)
);
CREATE TABLE UserList (UserId TEXT);  -- empty in practice; ignore
```

- **No `RemoteAddress` column** (the parent spec listed it as expected). No
  local/remote split is possible from history.
- No primary key; rows are individual play events. Use `rowid` if a stable key is
  ever needed (it isn't for aggregation).
- `PlayDuration` is whole seconds. Some rows are `0` (instant stop) — keep them;
  they count as a play with zero watch time.

### 3.2 ID formats — normalization

| Source | Column | Format |
|---|---|---|
| `playback_reporting.db` | `UserId`, `ItemId` | `11111111222233334444555555555555` (dashless, lowercase) |
| `jellyfin.db` | `Users.Id`, `BaseItems.Id`, `UserData.UserId`, `UserData.ItemId` | `11111111-2222-3333-4444-555555555555` (dashed, uppercase) |

Same GUIDs, different spelling. **Canonical form across Ephyra's store is dashless
lowercase** (`lower(replace(id, '-', ''))`). Every store column that holds a user
or item id — `dim_user.id`, `watch_events_daily.user_id`/`item_id`/`series_id`,
`agg_watch_heatmap.user_id`, `agg_played_core.*`, `agg_cleanup.item_id` — stores
the canonical form. Joins against `jellyfin.db` normalize the jellyfin side in
SQL.

### 3.3 `PlaybackMethod` values → 5 buckets

Real distinct values and the mapping:

| Plugin string | Bucket | Meaning |
|---|---|---|
| `DirectPlay` | `DirectPlay` | no work |
| `Transcode (v:direct a:direct)` | `Remux` | container swap only |
| `Transcode (v:direct a:aac)` (any `a:` other than `direct`) | `AudioTranscode` | cheap |
| `Transcode (v:h264 …)` (any `v:` other than `direct`) | `VideoTranscode` | the expensive one |
| anything else, incl. an unparseable `Transcode (...)` | `Other` | still a transcode, shape unknown |

There is **no `DirectStream`** value — drop it from the parent spec's enum.
Bucketing is string-prefix / substring matching, done in `aggregate` (pure), not
SQL.

### 3.4 Core played-state — `jellyfin.db`

```sql
CREATE TABLE UserData (
  ItemId TEXT, UserId TEXT, CustomDataKey TEXT,   -- PK (ItemId, UserId, CustomDataKey)
  IsFavorite INTEGER NOT NULL,
  LastPlayedDate TEXT NULL,                        -- 'YYYY-MM-DD HH:MM:SS.fff'
  PlayCount INTEGER NOT NULL,
  PlaybackPositionTicks INTEGER NOT NULL,
  Played INTEGER NOT NULL,                         -- 0 | 1 (1 = finished past Jellyfin's threshold)
  ...
);
CREATE TABLE Users (Id TEXT PRIMARY KEY, Username TEXT NOT NULL, LastLoginDate TEXT NULL, LastActivityDate TEXT NULL, ...);
```

- `UserData` has multiple rows per `(ItemId, UserId)` differing only by
  `CustomDataKey`. Aggregate with `GROUP BY ItemId, UserId` and
  `MAX(LastPlayedDate)`, `MAX(PlayCount)`, `MAX(Played)`.
- A partial play sets `LastPlayedDate` and `PlayCount>0` while `Played` stays `0`.
  "Never watched" therefore means **`LastPlayedDate IS NULL` for every user** —
  not `Played=0`.
- Episode → series linkage is on `BaseItems`: `SeriesId`, `SeriesName`,
  `SeasonId`. Present and populated.

### 3.5 Operator reality (informational, not a design constraint)

The test server's plugin history runs 2025-01-27 → 2025-04-06 (261 rows, one
user), then stops — the plugin was uninstalled and only reinstalled 2026-08-30.
Core `UserData` shows plays by two users continuing past that. Consequences the
design already handles: default `range=30d` shows nothing until fresh history
accrues (§9.2 coverage + empty state); the core "most played" panel (§7.3) is the
page's floor.

---

## 4. Store schema — migration `0002_watch_cleanup.sql`

Forward-only, integer-prefixed, does **not** touch `schema_migrations` (the
runner owns it). `watch_events_daily`, `agg_watch_heatmap`, `agg_cleanup` were
created empty in `0001` and have never been written, so redefining them by
drop+recreate is safe.

```sql
-- user directory, for the Watch Stats filter and name resolution
CREATE TABLE dim_user (
  id   TEXT PRIMARY KEY,   -- canonical (dashless lowercase)
  name TEXT NOT NULL
);

-- core play counts (Jellyfin's own counters), for the always-on "most played" panel
CREATE TABLE agg_played_core (
  user_id        TEXT NOT NULL,   -- canonical
  scope          TEXT NOT NULL,   -- 'movie' | 'series'
  item_id        TEXT NOT NULL,   -- canonical; movie id or series id
  name           TEXT NOT NULL,
  play_count     INTEGER NOT NULL,
  last_played_at TEXT,            -- MAX across the user's rows; NULL only if all NULL
  PRIMARY KEY (user_id, item_id)
);

-- daily watch facts (redefined: + series_id, series_name)
DROP TABLE watch_events_daily;
CREATE TABLE watch_events_daily (
  day         TEXT NOT NULL,      -- 'YYYY-MM-DD', plugin's server-local wall date (§5.3)
  user_id     TEXT NOT NULL,      -- canonical
  item_id     TEXT NOT NULL,      -- canonical
  scope       TEXT NOT NULL,      -- 'movie' | 'episode'
  name        TEXT NOT NULL,      -- current BaseItems name if resolved, else plugin ItemName
  series_id   TEXT NOT NULL DEFAULT '',   -- canonical; '' for movies / unresolved
  series_name TEXT NOT NULL DEFAULT '',
  plays       INTEGER NOT NULL,
  watch_sec   INTEGER NOT NULL,
  method      TEXT NOT NULL,      -- DirectPlay | Remux | AudioTranscode | VideoTranscode | Other
  PRIMARY KEY (day, user_id, item_id, method)
);

-- day x hour heatmap (redefined: + user_id in the PK)
DROP TABLE agg_watch_heatmap;
CREATE TABLE agg_watch_heatmap (
  user_id   TEXT NOT NULL,        -- canonical
  dow       INTEGER NOT NULL,     -- 0=Sun .. 6=Sat, server-local (§5.3)
  hour      INTEGER NOT NULL,     -- 0..23, server-local (§5.3)
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, dow, hour)
);

-- cleanup candidates (redefined: + scope, episodes; grain is movie|series)
DROP TABLE agg_cleanup;
CREATE TABLE agg_cleanup (
  item_id        TEXT PRIMARY KEY, -- canonical; movie id or series id
  scope          TEXT NOT NULL,    -- 'movie' | 'series'
  name           TEXT NOT NULL,
  library        TEXT NOT NULL,
  bytes          INTEGER NOT NULL, -- movie size, or SUM(episode sizes)
  episodes       INTEGER NOT NULL, -- 0 for movies
  added_at       TEXT NOT NULL,    -- movie DateCreated, or MAX(episode DateCreated)
  last_played_at TEXT              -- MAX(UserData.LastPlayedDate) over users x (episodes); NULL = never
);
```

`refresh_meta` is unchanged; the `watch` row's `plugin_available` column (already
present) is now actually written.

All `agg_*` / `dim_*` / `watch_events_daily` tables remain fully rewritten inside
one transaction per job, so the API never sees a partial set.

---

## 5. Source layer

### 5.1 `LibraryItem` / `LibrarySnapshot` additions

```go
type LibraryItem struct {
    // ... Plan 1 fields unchanged ...
    ID           string    // canonical (dashless lowercase)
    SeriesID     string    // canonical; "" for movies
    SeriesName   string
    Played       bool      // MAX(UserData.Played) across users
    PlayCount    int       // SUM(UserData.PlayCount) across users
    LastPlayedAt time.Time // MAX(UserData.LastPlayedDate) across users; zero if never
}

type UserPlay struct {
    UserID       string    // canonical
    ItemID       string    // canonical
    Scope        string    // "movie" | "series"  (episodes pre-rolled to their series)
    Name         string
    PlayCount    int
    LastPlayedAt time.Time
}

type UserRef struct {
    ID   string // canonical
    Name string
}

type LibrarySnapshot struct {
    // ... Plan 1 fields unchanged ...
    Users     []UserRef
    UserPlays []UserPlay // for agg_played_core; already rolled to movie|series grain
}
```

`FileSource.LibraryFacts` runs, against the same `jellyfin.db` copy it already
opens:
1. the Plan-1 item query, now also selecting `Id`, `SeriesId`, `SeriesName`;
2. a `UserData`-joined lookup for per-item aggregate played-state
   (`GROUP BY ItemId`, MAX of the three columns) folded into each `LibraryItem`;
3. `SELECT Id, Username FROM Users` → `[]UserRef` (id normalized);
4. a `UserData` → `BaseItems` join producing `[]UserPlay` at movie|series grain
   (`GROUP BY UserId, COALESCE(SeriesId, Id)`), for `agg_played_core`.

### 5.2 `PlaybackEvent` and enrichment

```go
type PlaybackEvent struct {
    At             time.Time // DateCreated parsed with its literal components (see 5.4)
    UserID         string    // canonical
    UserName       string    // resolved from jellyfin.db Users; "" if unknown
    ItemID         string    // canonical
    ItemName       string    // current BaseItems.Name if resolved, else plugin ItemName
    ItemType       string    // "movie" | "episode"
    SeriesID       string    // canonical; "" for movies or unresolved episodes
    SeriesName     string
    Method         string    // RAW plugin string; bucketing happens in aggregate
    PlayDurationSec int64
}
```

`FileSource.PlaybackEvents(ctx, since)`:
- opens the `playback_reporting.db` copy **and** the `jellyfin.db` copy (the
  `watch` job now copies both files);
- reads all `PlaybackActivity` rows with `ItemType IN ('Movie','Episode')`
  (`since` is accepted but ignored by `FileSource` — full history every run; it
  exists for a future `APISource`);
- resolves, from `jellyfin.db`: `UserName` via `Users`, and for the distinct
  played `ItemId`s their current `Name` / `SeriesId` / `SeriesName` via
  `BaseItems` (normalizing the jellyfin side in SQL);
- unresolved item (deleted media) → keep the plugin's `ItemName`, leave series
  fields empty;
- returns `[]PlaybackEvent`.

Enrichment is best-effort labelling: if the `jellyfin.db` copy is unavailable the
job still produces events with raw names and no series rollup, and logs a WARN.

### 5.3 Timestamps — no re-zoning for watch data

The two sources disagree on how they store time:

- `jellyfin.db` `DateCreated` (Plan 1's growth chart, and Cleanup's `added_at`) is
  **UTC**. Plan 1's `parseJellyfinTime` returns it as `.UTC()` and
  `aggregate.Library` shifts it into `TZ` for month bucketing. Unchanged.
- `playback_reporting.db` `DateCreated` is **server-local wall time, no zone
  marker** — a long-standing plugin behaviour.

So `PlaybackEvent.At` is parsed with `time.Parse` (layouts from Plan 1) and its
literal Y-M-D-h-m-s components are used as-is: `aggregate.Watch` buckets `day`,
`dow`, `hour` straight off `At.Date()` / `At.Weekday()` / `At.Hour()` and does
**not** apply `TZ`. Re-zoning a naive local timestamp would corrupt the
hour-of-day heatmap. The operator is expected to run Jellyfin's host and the
Ephyra container in the same timezone (they share a machine); `TZ` only aligns
Ephyra's *"now"* (for the `range` and `stale` cutoffs, §9) with that wall clock.

### 5.4 mtime skip

The `watch` job's `SourceMTime("watch")` keys off `playback_reporting.db` only
(already the case). The enrichment DB changing does not need to re-trigger a
`watch` refresh — stale labels are acceptable until the next real change.

---

## 6. Aggregation (pure)

New file `internal/aggregate/watch.go`:

```go
func Watch(events []source.PlaybackEvent) WatchAggregates
// WatchAggregates{ Daily []WatchDailyRow; Heatmap []HeatmapRow }
```

- No `loc` parameter — `PlaybackEvent.At` is already server-local wall time
  (§5.3). `day` = `At.Format("2006-01-02")`, `dow` = `At.Weekday()` (0=Sun),
  `hour` = `At.Hour()`, all off the literal components.
- `Daily`: group by `(day, user_id, item_id, method_bucket)` →
  `plays = len`, `watch_sec = Σ PlayDurationSec`. Carries `scope`, `name`,
  `series_id`, `series_name`.
- `Heatmap`: group by `(user_id, dow, hour)` → `plays`, `watch_sec`. All of
  history (no range concept at aggregation time).
- `method_bucket` per §3.3, table-driven.
- Weekly rollups (`play_method_weekly`'s `week` = `YYYY-Www`; `trend`'s Monday
  date for `1y`/`all`) are computed at API read time from `day`, not stored.

New file `internal/aggregate/cleanup.go`:

```go
func Cleanup(snap source.LibrarySnapshot) []CleanupRow
```

- One row per movie, and one per series (grouping that series' episodes).
- `bytes` = movie size or Σ episode sizes; `episodes` = 0 or count;
  `added_at` = movie `DateCreated` or MAX episode `DateCreated`;
  `last_played_at` = MAX `LastPlayedAt` over the movie / all its episodes (which
  is already MAX-across-users from §5.1); zero → stored NULL.
- A series with zero sized episodes still emits a row (`bytes=0`) — it shows up
  under "never watched" which is useful signal.

New file `internal/aggregate/coreplays.go`:

```go
func CorePlays(plays []source.UserPlay) []CorePlayRow
```

- Pass-through with a per-user top-N cut (N = 25; the API returns 15, the slack
  absorbs ties / future needs). Sort by `play_count` desc, then `last_played_at`
  desc, then `name`.

`aggregate.Library` (Plan 1) is untouched.

---

## 7. `library` job — additions

The existing job gains three writes, all inside the existing single transaction
in `store.WriteLibraryAggregates` (renamed conceptually to "library job write";
signature grows to take the extra slices):

1. **`agg_cleanup`** ← `aggregate.Cleanup(snap)`
2. **`dim_user`** ← `snap.Users` (straight insert, no aggregation)
3. **`agg_played_core`** ← `aggregate.CorePlays(snap.UserPlays)`

Each is `DELETE FROM` + re-`INSERT` within the txn. A failure rolls back the
whole library refresh, Plan-1 rows included — acceptable, the previous set stays
live and the page shows `stale`.

### 7.1 Why the core "most played" panel lives here

It is core-DB data (`UserData`), so the `library` job already has it open, and it
must be available when the `watch` job / plugin is entirely absent. `/api/watch/stats`
reads `agg_played_core` + `dim_user` regardless of `watch` job state.

---

## 8. `watch` job — wiring

- `scheduler` already starts a `watch` ticker (`REFRESH_WATCH`, default 10m) and
  runs it once on startup; today it calls `PlaybackEvents` which returns
  `ErrNotImplemented`. Plan 2 makes it real.
- Flow: `SourceMTime("watch")` skip check → `Source.PlaybackEvents(ctx, zero)` →
  `aggregate.Watch(events)` → `store.WriteWatchAggregates(daily, heatmap)`
  (one txn: clear + rewrite `watch_events_daily` and `agg_watch_heatmap`) →
  `refresh_meta` upsert with `plugin_available`.
- **Plugin-absent detection:** `playback_reporting.db` missing under both
  `data/` and `data/data/`, or `PlaybackActivity` absent → job writes
  `refresh_meta(job='watch', ok=1, plugin_available=0)` and **no** `agg_watch_*`
  rows. Not an error; the ticker keeps checking (a later install is picked up on
  the next tick).
- Staleness for `/api/watch/stats` follows the parent rule against the `watch`
  interval, but a plugin-absent job is "fresh and empty", not stale.

---

## 9. HTTP API

### 9.1 `GET /api/watch/stats`

Query params: `range` ∈ `30d|90d|1y|all` (default `30d`),
`user` = a canonical id or `all` (default `all`).

Envelope per parent §8.1. `data`:

```jsonc
{
  "plugin_available": true,
  "range": "30d",
  "user": "all",
  "users": [{ "id": "11111111…", "name": "alice" }],          // from dim_user, always present
  "coverage": { "first_play": "2025-01-27", "last_play": "2025-04-06", "total_plays": 261 },
  "totals": {
    "watch_seconds": 0, "plays": 0, "active_users": 0,
    "direct_play_pct": 0.0, "video_transcode_pct": 0.0
  },
  "top_movies":   [{ "item_id": "", "name": "", "plays": 0, "watch_sec": 0 }],      // ≤10
  "top_series":   [{ "series_id": "", "name": "", "plays": 0, "watch_sec": 0 }],    // ≤10
  "top_episodes": [{ "item_id": "", "name": "", "series_name": "", "plays": 0, "watch_sec": 0 }], // ≤10
  "active_users": [{ "user_id": "", "name": "", "watch_sec": 0, "plays": 0, "distinct_titles": 0 }],
  "trend":   [{ "day": "2026-08-01", "watch_sec": 0, "plays": 0 }],                 // for 1y/all: one point per ISO week, "day" = that week's Monday
  "heatmap": [{ "dow": 0, "hour": 0, "watch_sec": 0, "plays": 0 }],                 // ≤168
  "play_method_weekly": [{ "week": "2026-W31", "DirectPlay": 0, "Remux": 0, "AudioTranscode": 0, "VideoTranscode": 0, "Other": 0 }],
  "most_played_core": [{ "scope": "series", "item_id": "", "name": "", "play_count": 0, "last_played_at": "" }]  // ≤15
}
```

Scoping rules:

| Panel | `range` | `user` |
|---|---|---|
| `totals`, `top_*`, `trend`, `play_method_weekly` | yes | yes |
| `heatmap` | no (all-time) | yes |
| `active_users` | yes | **no** (always the full leaderboard) |
| `most_played_core` | no (lifetime counts) | yes |
| `users`, `coverage` | no | no |

- `range` → a `day >= ?` cutoff: `time.Now()` (container local, §5.3) minus 30 /
  90 / 365 days, formatted `YYYY-MM-DD`, lexical string compare against the
  stored `day`. `all` = no cutoff.
- `direct_play_pct` = `DirectPlay plays / Σ all-method plays` in scope;
  `video_transcode_pct` = `VideoTranscode / Σ` — `0.0` when denominator is `0`.
- `coverage` is computed over **all** `watch_events_daily` (ignores both params)
  so the frontend can explain a gap.
- **Plugin absent:** `plugin_available:false`; `coverage` all-zero /
  null-ish; `users` and `most_played_core` still populated; every other panel an
  empty array. Still HTTP `200`.
- **Not-ready** (no `library` refresh yet, so no `dim_user`): parent `not_ready`
  error, HTTP `503`.

### 9.2 `GET /api/cleanup`

Query params: `mode` ∈ `never|stale` (default `never`), `sort` ∈ `size|added`
(default `size`), `limit` int (default `200`, max `1000`), `format` ∈
`json|csv` (default `json`).

`data`:

```jsonc
{
  "mode": "never",
  "reclaimable_bytes": 0,               // Σ bytes over ALL rows matching mode, not just the page
  "truncated": false,                    // true when matches > limit
  "match_count": 0,
  "items": [{
    "item_id": "", "scope": "series", "name": "", "library": "",
    "bytes": 0, "episodes": 0, "added_at": "", "last_played_at": null
  }]
}
```

- `never` → `last_played_at IS NULL`.
- `stale` → `last_played_at IS NOT NULL AND last_played_at < :cutoff`, where
  `:cutoff` is `time.Now()` minus 365 days formatted to match the stored
  `'YYYY-MM-DD HH:MM:SS…'` shape (lexical compare; `UserData.LastPlayedDate` and
  the cutoff are both zero-padded, so it sorts correctly).
- `sort`: `size` → `bytes DESC`; `added` → `added_at ASC`. Tie-break `name ASC`.
- `format=csv` → `text/csv`, header row `item_id,scope,name,library,bytes,episodes,added_at,last_played_at`,
  **all** matching rows (ignores `limit`), `Content-Disposition: attachment;
  filename="ephyra-cleanup-<mode>.csv"`, no envelope. `last_played_at` empty for
  never.
- Served from `agg_cleanup` only; `meta.stale` from the `library` job's
  `refresh_meta`.

### 9.3 `POST /api/refresh?job=…`

Unchanged from parent §8.6 — `job=watch` and `job=all` now do real work.

---

## 10. Frontend

Stack unchanged from Plan 1 (React 19, TanStack Router + Query, ECharts
lazy-loaded, Tailwind v4 `@theme` tokens, hand-rolled components, Abyssal
theme). `/watch` and `/cleanup` are currently stub routes.

### 10.1 `/watch` — `WatchStats`

Typed route search params: `range` (`30d|90d|1y|all`, default `30d`), `user`
(`all` or an id, default `all`). Both drive the query key; both are in the URL.

Layout, top to bottom:

1. **Controls row** — `SegmentedControl` for range · `UserSelect` (`All users` +
   one option per `users[]`).
2. **Stat cards** (`StatCard` ×5) — Watch time · Plays · Active users ·
   Direct-play % · Video-transcode %.
3. **Watch-time trend** — full-width ECharts area; x = `trend[].day` (daily for
   `30d`/`90d`, one point per ISO week for `1y`/`all`).
4. **Two-up** — `Heatmap` (7×24, cell = `watch_sec`, tooltip `Sun 21:00 · 3.2 h`,
   scale label "all time") | `MethodBars` (weekly stacked, the 5 buckets).
5. **Three ranked lists** — Top series · Top movies · Top episodes.
   `DataTable`, shared plays↔hours toggle.
6. **Active-users leaderboard** — horizontal bars + a `DataTable` (watch time,
   plays, distinct titles). Caption: "all users, current range".
7. **Most played · Jellyfin counts** — `DataTable` from `most_played_core`.
   Caption: "From Jellyfin's own play counters, not the plugin — all-time, no
   watch time." Always shown.

States:

- Loading → per-panel `Skeleton`.
- `plugin_available:false` → panels 2–6 replaced by one card: what Playback
  Reporting is, and the catalog install steps (Dashboard → Plugins → Catalog →
  Playback Reporting → install → restart). Panels 1 and 7 and the user filter
  still render.
- `plugin_available:true` but the selected `range`/`user` is empty → panels 2–6
  show a shared empty state that reads `coverage`: e.g. "No plays in the last 30
  days. The plugin has history for 2025-01-27 → 2025-04-06 and anything since it
  was reinstalled." + a "View all" button (`range=all`).
- `StaleBanner` when `meta.stale`.

### 10.2 `/cleanup` — `Cleanup`

Typed search params: `mode` (`never|stale`, default `never`), `sort`
(`size|added`, default `size`).

- **Header** — large `reclaimable_bytes` (via `lib/format`), and the fixed line
  "Ephyra never deletes anything. This is a shortlist you act on in Jellyfin."
- **Controls** — `SegmentedControl` mode ("Never watched" / "Watched, then
  untouched 1y+") · `SegmentedControl` sort ("Biggest" / "Longest resident") ·
  **Export CSV** (anchor to `/api/cleanup?…&format=csv`).
- **Table** (`DataTable`) — Name (scope icon + `· 24 eps` for series) · Library ·
  Size · Added · Last played (`—` for never). Server already sorted; render as
  received.
- **Truncation** — when `truncated`, a footer: "Showing 200 of 431. Export CSV
  for the full list."

States: loading `Skeleton` table · empty ("Nothing matches — it's all been
watched." / "Nothing's gone stale.") · error + Retry · `StaleBanner`.

### 10.3 New components

Hand-rolled, in `web/src/components/` (no shadcn/ui):

| Component | Notes |
|---|---|
| `SegmentedControl` | generic, `value` / `options` / `onChange`; used 3× |
| `UserSelect` | styled `<select>`; `All users` sentinel |
| `Heatmap` | ECharts `heatmap` series, 7×24, theme-driven color ramp |
| `MethodBars` | ECharts stacked bar; fixed 5-series color mapping |
| `DataTable` | minimal column-config table; used by every ranked list + cleanup |

`charts/theme.ts` gains the 5 method colors and the heatmap ramp, sourced from
the existing CSS custom properties.

---

## 11. Config & docs

- **No new env vars.** `watch_events_daily.day` and the heatmap axes come from
  the plugin's own server-local timestamps and are **not** re-zoned by `TZ`
  (§5.3). `TZ` still matters for the `range`/`stale` cutoff "now" and for Plan
  1's growth chart — extend its doc line to say so.
- `REFRESH_WATCH` (exists, default `10m`) is now load-bearing.
- `docs/schema-notes.md` — add the §3 findings: `playback_reporting.db` schema,
  the id-format table, the `PlaybackMethod` → bucket mapping, `UserData` /
  `Users` shapes, episode→series linkage.
- `README.md` — "Test against a real library" gains a `playback_reporting.db`
  copy step (same `docker cp` pattern, next to `jellyfin.db`); the feature list
  flips Watch Stats and Cleanup from "stub" to shipped.
- `CLAUDE.md` — architecture note: `watch` job reads two DBs; canonical id form
  is dashless-lowercase.

---

## 12. Testing

Follows Plan 1: hand-built SQLite fixtures → golden values, pure `aggregate`
funcs, table-driven API tests, Vitest for components.

### 12.1 Fixtures

- Extend `testdata/library.fixture.sql`: add `Users` (3–4 rows), `UserData`
  (covering the edges below), and `SeriesId`/`SeriesName` on the existing episode
  rows.
- New `testdata/playback_reporting.fixture.sql` in the real schema (§3.1).
- `internal/testsupport`: add `PlaybackFixtureDB(t)` beside `LibraryFixtureDB(t)`;
  a helper that lays both files out under one `JELLYFIN_DATA_DIR` tree for the
  `watch` job / smoke test.

Edge cases to bake in:

- All five `PlaybackMethod` strings + one unparseable `Transcode (...)` → `Other`.
- A `PlayDuration = 0` event.
- Two users with plays; a third user present in `Users` with none.
- A plugin `UserId`/`ItemId` (dashless) that must normalize-match a dashed
  `jellyfin.db` id.
- A played `ItemId` absent from `BaseItems` → keeps plugin `ItemName`, no series.
- Episodes from ≥2 series, so top-series rollup and per-series byte sums are
  exercised.
- Events across ≥2 `day`s, ≥2 ISO weeks, and distinct `dow`×`hour` cells; events
  at `00:20` and `23:50` on one date to prove the literal components are used
  with no re-zoning (§5.3).
- Core: a never-touched movie; a series with some episodes played, some not; a
  movie last played > 1y ago (stale) and one < 1y (neither); a `(ItemId,UserId)`
  with two `CustomDataKey` rows where MAX matters; a partial play
  (`PlayCount=2, Played=0, LastPlayedDate` set) → **not** "never".

### 12.2 Go tests

- `aggregate.Watch` — daily + heatmap rows vs golden; an event at `00:20` and one
  at `23:50` on the same date land in `hour` 0 and 23 of the **same** `day` (no
  re-zoning, §5.3); method bucketing table-driven.
- `aggregate.Cleanup` — series byte sum, `added_at` = MAX, never/stale
  classification, zero-byte series still emitted.
- `aggregate.CorePlays` — per-user top-N ordering and tie-break; episode→series
  pre-roll respected.
- `source/file` — `PlaybackEvents` against both fixture DBs: `UserName` /
  `SeriesName` filled, normalization join, missing-item fallback, enrichment-DB
  absent → raw events + WARN.
- `store` — round-trip each new table; the library-job write clears + rewrites
  `agg_cleanup` / `agg_played_core` / `dim_user` in one txn; the watch-job write
  clears + rewrites `watch_events_daily` / `agg_watch_heatmap`; `0002` applies on
  top of `0001` with the runner still owning `schema_migrations`.
- `api` — table-driven:
  - `/api/watch/stats`: range cutoff, user filter, `active_users` ignoring the
    user filter, heatmap ignoring range, `plugin_available:false` shape (keeps
    `users` + `most_played_core`), empty-range shape (has `coverage`),
    `direct_play_pct` / `video_transcode_pct` math incl. zero denominator,
    `not_ready` before any library refresh.
  - `/api/cleanup`: `mode`, `sort`, `limit` + `truncated` + `match_count`,
    `reclaimable_bytes` = full-set sum, CSV content-type / filename / header row /
    full-set rows / empty `last_played_at` for never.

### 12.3 Frontend tests (Vitest + RTL)

- `/watch`: loading / error / data; range + user controls change the rendered
  query key; `plugin_available:false` install card; empty-range coverage copy +
  "View all"; heatmap cell intensity scales with value.
- `/cleanup`: loading / empty / data; mode + sort toggles; Export CSV anchor
  href carries the current `mode`; truncation footer.

### 12.4 Smoke

Extend the existing binary smoke test: lay out both fixture DBs, start with
`SOURCE=file`, hit `/api/watch/stats` (default + `range=all` + `user=<id>`) and
`/api/cleanup` (`json` + `csv`) → assert `200` + schema. Run once more with
`playback_reporting.db` removed → assert `plugin_available:false` and that
`/api/cleanup` is unaffected.

### 12.5 CI

Commands unchanged (`go test ./...`, `go vet`, `golangci-lint run`, `biome ci`,
`tsc -b`, `vitest run`, `docker buildx build`).

---

## 13. Build sequence (coarse; the plan expands this)

1. Migration `0002` + `store` read/write scaffolding for the new tables, with
   round-trip tests.
2. `source` types (`ID`, series, played-state on `LibraryItem`;
   `UserRef` / `UserPlay`; `PlaybackEvent`). Extend the Plan-1 library query;
   add the `UserData` / `Users` reads. Fixtures updated.
3. `aggregate.Cleanup` + `aggregate.CorePlays` + wire into the library-job write.
   `GET /api/cleanup` (json + csv).
4. `FileSource.PlaybackEvents` (two-DB open + enrichment). `aggregate.Watch`.
   `watch` job wiring + plugin-absent path. `store.WriteWatchAggregates`.
5. `GET /api/watch/stats` — all panels, scoping table, plugin-absent + empty
   shapes.
6. Frontend `/cleanup` — components (`SegmentedControl`, `DataTable`), page,
   states, CSV.
7. Frontend `/watch` — `UserSelect`, `Heatmap`, `MethodBars`, page, all panels,
   states.
8. Docs: `schema-notes.md`, `README.md`, `CLAUDE.md`. Smoke test extension.

---

## 14. Explicitly deferred

Stay in the parent backlog, not this plan:

- `APISource` playback path (plugin `submit_custom_query`) — #1.
- Per-user Cleanup / recommendations — #3.
- Ephyra-owned event history, bandwidth-over-time, recently-ended — #4.
- Client / device breakdown, local-vs-remote — #5.
- Completion vs abandonment, binge detection, rewatch — #3.
- Now Playing (SSE) — Plan 3.
