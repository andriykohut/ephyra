# Ephyra — Jellyfin Stats Dashboard — Design

Status: approved for planning
Date: 2026-08-30
Target Jellyfin: 10.11.11 (forward-compat plan for Jellyfin 12)

---

## 1. Summary

Ephyra is a single Go binary (with an embedded React SPA) that runs as its own
Docker Compose service beside a Jellyfin server and presents a stats dashboard:
four pages — **Library Overview**, **Watch Stats**, **Now Playing**, **Cleanup** —
plus the JSON/SSE API behind them.

It gets its data cheaply: bulk/historical data is read from **copies of Jellyfin's
SQLite files** (the item DB and the Playback Reporting plugin DB) on a schedule,
aggregated in Go, and written into Ephyra's own small SQLite store. Pages are
served entirely from that store. The only live surface, Now Playing, uses the
Jellyfin HTTP API and is pushed to browsers over Server-Sent Events.

Design principles:

- **Plugin-optional.** Only Watch Stats needs the Playback Reporting plugin; it
  degrades to a clear "enable this plugin" state. The other three pages work
  against core Jellyfin alone.
- **Cheapest path wins.** Direct file reads for bulk data; the HTTP API only for
  live sessions.
- **Source is pluggable.** A `Source` interface isolates *what data we need* from
  *how it is fetched*. v1 ships `FileSource` (copy + read SQLite). `APISource`
  (Jellyfin 12 / Postgres backend / remote deployments) is designed-in as
  backlog item #1 and implements the same interface.
- **One artifact.** The frontend builds into the binary. One container, one
  config block, no runtime dependencies.

---

## 2. Goals & non-goals

### v1 goals

- Four pages listed above, each with loading / error / empty / stale states.
- Idle memory footprint ~15–30 MB; near-zero standing load on Jellyfin.
- Runs from a single container image with a read-only bind mount of Jellyfin's
  data directory and one API key.
- Data layer abstracted so a future API-only / remote mode is an added
  implementation, not a rewrite.

### Non-goals (v1)

- No writes to Jellyfin, ever. Cleanup identifies candidates; it never deletes.
- No authentication system. LAN-only or reverse-proxy-auth is assumed.
- No real-time updates for anything except Now Playing.
- No historical retention beyond what the Playback Reporting plugin itself keeps
  (Ephyra does not yet persist its own event history — see backlog #4).
- No multi-server support.

---

## 3. Constraints & context

- **Deployment:** Docker Compose on the same host as Jellyfin (TrueNAS + Arcane,
  but nothing TrueNAS-specific is required). Jellyfin's config/data directory is
  bind-mounted **read-only** into the Ephyra container.
- **Jellyfin 10.11.11.** The item store is still SQLite (`library.db`). Some
  data (users, auth, API keys, activity log, display preferences) has migrated to
  an EF-Core `jellyfin.db` over the 10.9 → 10.11 arc. Exact table/column names
  are pinned by Task #0 (§5.5), not assumed here.
- **Playback Reporting plugin** is installed; it maintains its own
  `playback_reporting.db` SQLite file, independent of the core DB migration.
- **Jellyfin 12** completes the EF-Core migration and adds an *optional* Postgres
  backend. Direct file reads cannot be guaranteed there. Handled by `APISource`
  (backlog #1); §5.4 defines the interface boundary so this drops in cleanly.

---

## 4. Architecture overview

### Components

| Component | Responsibility |
|---|---|
| `Source` (interface) | `LibraryFacts(ctx) (LibrarySnapshot, error)` and `PlaybackEvents(ctx, since) ([]PlaybackEvent, error)` — abstracts data acquisition |
| `FileSource` | v1 `Source` impl: copy Jellyfin DB files to a work dir, open read-only, run queries, return rows as typed structs |
| `jellyfin.Client` | Thin HTTP client: `GET /Sessions`, `GET /Users`, `GET /System/Info` only. `X-Emby-Token` auth |
| `store` | Ephyra's own SQLite: schema migrations + materialized aggregate tables. The only thing the API reads from |
| `aggregate` | Pure functions: `LibrarySnapshot` / `[]PlaybackEvent` → rows for each `agg_*` table |
| `scheduler` | One ticker per job (`library` 30 m, `watch` 10 m) + a manual-trigger channel; per-job mutex; mtime-based skip |
| `live` | SSE hub: fan-out to connected browsers; single upstream `/Sessions` poll loop that runs only while ≥1 client is connected; snapshot diffing; upstream-failure resilience |
| `api` | HTTP handlers: JSON endpoints over `store`, the SSE stream over `live`, `POST /api/refresh`, `/healthz` |
| `web` | React SPA, built to static assets, embedded via `//go:embed`, served at `/` with SPA fallback |

### Request flow

```
browser ──GET /api/*──▶ api ──▶ store (SQLite SELECT, ms)         ──▶ JSON
browser ──GET /api/now-playing/stream──▶ api ──▶ live hub          ──▶ text/event-stream
browser ──static assets /──▶ api ──▶ embed.FS                        ──▶ HTML/JS/CSS
```

### Refresh flow

```
scheduler tick / POST /api/refresh
  └▶ job mutex acquired
     └▶ Source.LibraryFacts() | Source.PlaybackEvents()
        (FileSource: mtime unchanged since last OK run? -> skip, record skip)
        └▶ aggregate.*  (pure)
           └▶ store: BEGIN; replace agg_* rows for this job; upsert refresh_meta; COMMIT
```

---

## 5. Data sources & access strategy

### 5.1 Jellyfin item DB

- Expected path inside the container: `${JELLYFIN_DATA_DIR}/data/library.db`
  (plus `-wal`, `-shm` sidecars). `JELLYFIN_DATA_DIR` is the read-only mount of
  Jellyfin's config directory (`/config` for both the official and linuxserver
  images).
- Feeds: Library Overview (all charts/tiles) and Cleanup (item list + sizes +
  core played-state).
- Accessed via the **copy-before-read** mechanism in §5.6.

### 5.2 Playback Reporting plugin DB

- Expected path: `${JELLYFIN_DATA_DIR}/data/playback_reporting.db`.
- One table of interest (name/columns pinned by Task #0; expected shape):
  `PlaybackActivity(DateCreated, UserId, ItemId, ItemType, ItemName,
  PlaybackMethod, ClientName, DeviceName, PlayDuration, RemoteAddress)`.
- Feeds Watch Stats only.
- Also accessed via copy-before-read (§5.6). It is small; the copy is cheap.
- **Plugin-absent detection:** if the file is missing, or the expected table is
  absent, the `watch` job records `plugin_available=false` in `refresh_meta` and
  writes no `agg_watch_*` rows. `GET /api/watch/stats` then returns
  `{ plugin_available: false }`.

### 5.3 Jellyfin HTTP API (live only)

- `GET /Sessions` — polled by the `live` subsystem only while an SSE client is
  connected (§9). Fields used per session: `UserName`, `NowPlayingItem`
  (`Name`, `SeriesName`, `IndexNumber`, `ParentIndexNumber`, `Type`,
  `RunTimeTicks`, media bitrate), `PlayState` (`PositionTicks`, `IsPaused`),
  `TranscodingInfo` (`Bitrate`, `TranscodeReasons`, `IsVideoDirect`,
  `IsAudioDirect`, `VideoCodec`, `AudioCodec`, `Container`), `Client`,
  `DeviceName`, `RemoteEndPoint`, `PlayMethod`.
- `GET /Users` — id → display name map for Watch Stats and Now Playing (cached,
  refreshed hourly and on cache miss).
- `GET /System/Info` — server name + version for the header and for source
  selection (§5.4). Cached 10 minutes.

### 5.4 `Source` interface & Jellyfin 12 forward-compat

```go
type Source interface {
    LibraryFacts(ctx context.Context) (LibrarySnapshot, error)
    PlaybackEvents(ctx context.Context, since time.Time) ([]PlaybackEvent, error)
    Kind() string // "file" | "api" — for logging and diagnostics
}
```

- `LibrarySnapshot` and `PlaybackEvent` are plain Go structs with everything the
  `aggregate` package needs. Neither the interface nor these types mention
  SQLite, files, or HTTP.
- v1 wires `FileSource`. Selection logic at startup:
  1. If `SOURCE=api` is set → `APISource` (errors in v1: "not yet implemented").
  2. Else if the DB files are readable → `FileSource`.
  3. Else → fatal error with a message naming the expected paths and the
     `SOURCE=api` option.
- `APISource` (backlog #1) will implement `LibraryFacts` via `/Items` +
  `/Items/Counts` paging and `PlaybackEvents` via the plugin's
  `POST /user_usage_stats/submit_custom_query` (server-side SQL, one round trip).

### 5.5 Task #0 — schema-discovery spike (blocks all aggregation work)

Before any aggregation code is written, connect to the operator's real
`library.db` and `playback_reporting.db` and record, in a committed
`docs/schema-notes.md`:

- Item table name and columns for: id, type, `Path`, `Container`, `Size`
  (bytes), `RunTimeTicks`, `DateCreated`, `ProductionYear` / `PremiereDate`,
  parent/library ancestry (`TopParentId` or equivalent).
- `MediaStreams` table: columns for `Type` (Video/Audio/Subtitle), `Codec`,
  `Width`, `Height`, `BitRate`, `Channels`, `Language`, HDR/Dolby-Vision
  indicators (`VideoRange`, `VideoRangeType`, `ColorTransfer`, `DvProfile`…).
- Genre/tag storage (`ItemValues` or a column) and the join.
- Played-state storage in 10.11: `UserDatas` table (in `library.db` or
  `jellyfin.db`?) — columns `played`, `playCount`, `lastPlayedDate`,
  `playbackPositionTicks`, `UserId`, item key.
- Playback Reporting: exact table name, exact column names, and the **distinct
  values** actually present in `PlaybackMethod`.

Every query shape in §8 is written against these notes.

### 5.6 Copy-before-read & change detection

Rationale: the mount is `:ro`, and SQLite cannot open a WAL-mode database
read-only without writing a `-shm` sidecar. Copying also gives a point-in-time
consistent view and puts zero locking pressure on Jellyfin.

Mechanism, per refresh job:

1. `stat` the source DB file. If its mtime is **unchanged** since the last
   successful (non-skipped) run of this job → record a skip in `refresh_meta`
   (`ok=true`, `skipped=true`) and return. Libraries change rarely, so most
   ticks do no work.
2. Otherwise copy `<db>`, `<db>-wal`, `<db>-shm` (whichever exist) into
   `${WORK_DIR}/<job>/` (on Ephyra's writable volume).
3. Open the **copy** with `modernc.org/sqlite`, `?_pragma=busy_timeout(5000)`,
   read queries only. WAL is replayed on open, yielding a consistent snapshot.
4. Run the job's queries; hand rows to `aggregate`.
5. Delete the copy (keep on failure for debugging; overwrite next run).

Escape hatch: `DIRECT_READ=true` skips the copy and opens the source with
`?immutable=1` — valid only when the operator mounts read-write or accepts the
risk of a torn read. Default `false`.

Copy cost note: bounded by `library.db` size (tens to a few hundred MB typical).
At the 30-minute cadence, with the mtime skip, steady-state copying is rare.
`playback_reporting.db` is small and copied whenever it changes (roughly every
time a playback ends).

---

## 6. Local store schema (Ephyra's SQLite)

One file at `${STORE_PATH}` (default `/data/ephyra.db`), `modernc.org/sqlite`,
WAL mode, migrations run at startup (embedded `.sql` files, forward-only,
tracked in a `schema_migrations` table).

All `agg_*` tables are fully rewritten inside a single transaction per job, so
the API never observes a half-written set.

| Table | Columns |
|---|---|
| `schema_migrations` | `version INTEGER PRIMARY KEY`, `applied_at TEXT` |
| `refresh_meta` | `job TEXT PRIMARY KEY` (`library`\|`watch`), `last_run_at TEXT`, `source_mtime TEXT`, `duration_ms INTEGER`, `ok INTEGER`, `skipped INTEGER`, `plugin_available INTEGER`, `error TEXT` |
| `agg_totals` | `metric TEXT PRIMARY KEY`, `value REAL` — e.g. `items.Movie`, `items.Episode`, `runtime_sec.total`, `bytes.total`, `count.uhd`, `count.hdr`, `count.dv` |
| `agg_disk` | `dimension TEXT`, `bucket TEXT`, `bytes INTEGER`, `items INTEGER`, PK (`dimension`,`bucket`); `dimension` ∈ `resolution`\|`codec`\|`container`\|`library` |
| `agg_distribution` | `dimension TEXT`, `bucket TEXT`, `items INTEGER`, PK (`dimension`,`bucket`); `dimension` ∈ `genre`\|`decade`\|`resolution`\|`hdr`\|`audio_channels` |
| `agg_library_growth` | `month TEXT PRIMARY KEY` (`YYYY-MM`), `added_items INTEGER`, `added_bytes INTEGER`, `cum_items INTEGER` |
| `watch_events_daily` | `day TEXT` (`YYYY-MM-DD`), `user_id TEXT`, `item_id TEXT`, `scope TEXT` (`movie`\|`series`\|`episode`), `name TEXT`, `plays INTEGER`, `watch_sec INTEGER`, `method TEXT` (`DirectPlay`\|`DirectStream`\|`Transcode`\|`Other`), PK (`day`,`user_id`,`item_id`,`method`) |
| `agg_watch_heatmap` | `dow INTEGER` (0=Sun…6), `hour INTEGER` (0–23), `watch_sec INTEGER`, `plays INTEGER`, PK (`dow`,`hour`) |
| `agg_cleanup` | `item_id TEXT PRIMARY KEY`, `name TEXT`, `library TEXT`, `bytes INTEGER`, `added_at TEXT`, `last_played_at TEXT` (NULL = never) |

**Watch tables.** One `watch` refresh writes both of the above over the full
plugin history. `watch_events_daily` is the single source for every range-aware
panel in `GET /api/watch/stats?range=` — totals, watch-time trend, top movies
/ series / episodes, active users, and direct-vs-transcode weekly — each built
at read time with `WHERE day >= ?` + `GROUP BY`. It is small (day × user ×
item × method rows; hundreds to a few thousand for years of history), so no
stored per-panel aggregates are needed. `agg_watch_heatmap` is the one exception:
hour-of-day is lost at daily grain, so it is stored directly and reflects **all
recorded history** (the "when we watch" panel is a habits view and is not
range-filtered — noted in §8.3).

---

## 7. Refresh & scheduling

- `scheduler` starts one `time.Ticker` per job: `library` every
  `REFRESH_LIBRARY` (default 30m), `watch` every `REFRESH_WATCH` (default 10m).
- A buffered trigger channel carries manual requests from `POST /api/refresh`.
- Each job holds a `sync.Mutex`. If a job is already running, a manual trigger
  for it returns `202` with `{ "queued": false, "running": true }`; the ticker
  simply skips that tick.
- On startup, both jobs run once immediately (respecting the mtime skip, so a
  restart with an unchanged library does no real work and pages stay warm from
  the persisted `agg_*` rows).
- Every run writes `refresh_meta`: timestamps, duration, `ok`, `skipped`,
  `error`, and (for `watch`) `plugin_available`.

**Staleness rule** (drives the `stale` flag in API responses): a job's data is
stale when its last `refresh_meta` row has `ok=0`, **or**
`now - last_run_at > 2 × interval`.

---

## 8. HTTP API contracts

### 8.1 Common envelope & errors

Every non-stream JSON response is wrapped:

```json
{
  "data": { ... },
  "meta": { "generated_at": "2026-08-30T12:00:00Z", "stale": false }
}
```

Errors: `{ "error": { "code": "string", "message": "human readable" } }` with an
appropriate HTTP status. Codes used: `plugin_unavailable`, `not_ready`
(first refresh has not completed), `upstream_unavailable` (Jellyfin API down,
Now Playing only), `internal`.

### 8.2 `GET /api/library/overview`

`data`:

```json
{
  "totals": {
    "items_by_library": [{"library": "Movies", "count": 1240}, ...],
    "runtime_seconds": 3628800,
    "bytes": 41231686041600,
    "count_uhd": 210, "count_hdr": 180, "count_dv": 44
  },
  "disk_by_resolution": [{"bucket": "4K", "bytes": 0, "items": 0}, ...],
  "disk_by_codec":      [{"bucket": "HEVC", "bytes": 0, "items": 0}, ...],
  "disk_by_container":  [{"bucket": "mkv", "bytes": 0, "items": 0}, ...],
  "disk_by_library":    [{"bucket": "Movies", "bytes": 0, "items": 0}, ...],
  "genres_top":  [{"bucket": "Drama", "items": 0}, ... up to 15],
  "by_decade":   [{"bucket": "1990s", "items": 0}, ...],
  "growth":      [{"month": "2025-01", "added_items": 0, "added_bytes": 0, "cum_items": 0}, ...]
}
```

Resolution buckets: `4K` (width ≥ 3200), `1080p` (≥ 1400), `720p` (≥ 1000),
`SD` (else), `Unknown` (no video stream). Codec buckets: normalized
(`h264`→`H.264`, `hevc`/`h265`→`HEVC`, `av1`→`AV1`, else raw uppercased).

### 8.3 `GET /api/watch/stats?range=30d|90d|1y|all` (default `30d`)

If `plugin_available=false`: `200` with
`{ "data": { "plugin_available": false }, "meta": {...} }`.

Otherwise `data`:

```json
{
  "plugin_available": true,
  "range": "30d",
  "totals": { "watch_seconds": 0, "active_users": 0, "plays": 0, "direct_play_pct": 0.0 },
  "top_movies":   [{"item_id": "", "name": "", "plays": 0, "watch_sec": 0}, ... 10],
  "top_series":   [ ... 10 ],
  "top_episodes": [ ... 10 ],
  "active_users": [{"user_id": "", "name": "", "watch_sec": 0, "plays": 0, "distinct_titles": 0}, ...],
  "trend":   [{"day": "2026-08-01", "watch_sec": 0, "plays": 0}, ...],
  "heatmap": [{"dow": 0, "hour": 0, "watch_sec": 0, "plays": 0}, ... up to 168],
  "play_method_weekly": [{"week": "2026-W31", "DirectPlay": 0, "DirectStream": 0, "Transcode": 0, "Other": 0}, ...]
}
```

All panels honor `range` via `watch_events_daily` (§6). The `heatmap`
panel is all-time (see §6) and ignores `range`. `direct_play_pct` =
`DirectPlay / (all methods)` over the range.

### 8.4 `GET /api/cleanup?watched=never|stale&sort=size|added&limit=200&format=json|csv`

Defaults: `watched=never`, `sort=size` (desc), `limit=200`.

- `never`: items with no core played-state (`played=0` and `playCount=0` for all
  users) **and**, when the plugin is available, no row in `PlaybackActivity`.
- `stale`: fully played (`played=1` for ≥1 user) with `last_played_at` older
  than 1 year.

`data`:

```json
{
  "mode": "never",
  "reclaimable_bytes": 0,
  "items": [{"item_id": "", "name": "", "library": "", "bytes": 0, "added_at": "", "last_played_at": null}, ...]
}
```

`format=csv` streams `text/csv` with the same columns and a
`Content-Disposition` attachment header; ignores the envelope.

### 8.5 Now Playing

`GET /api/now-playing` — one-shot JSON snapshot (initial paint, and fallback
where `EventSource` is unavailable):

```json
{
  "data": {
    "server": { "name": "NAS", "version": "10.11.11" },
    "degraded": false,
    "summary": { "streams": 2, "transcodes": 1, "outbound_bitrate": 18400000 },
    "sessions": [{
      "session_id": "", "user": "", "title": "The Bear", "subtitle": "S03E01 · Tomorrow",
      "type": "Episode",
      "play_method": "Transcode", "transcode_reasons": ["VideoCodecNotSupported"],
      "video_codec": "hevc→h264", "audio_codec": "eac3→aac", "container": "mkv→ts",
      "bitrate": 9200000, "progress_pct": 34.2, "position_sec": 615, "runtime_sec": 1800,
      "client": "Jellyfin Web", "device": "Firefox", "is_remote": false, "paused": false
    }]
  },
  "meta": { "generated_at": "...", "stale": false }
}
```

`GET /api/now-playing/stream` — `text/event-stream`:

- On connect: one `event: snapshot` with the payload above (`data` only).
- Then `event: update` with the same shape whenever the diff vs the previous
  poll is non-empty (session added/removed, pause toggled, method changed,
  progress moved ≥ 1 %).
- `: heartbeat` comment every 15 s.
- `event: degraded` `{ "degraded": true }` when upstream polling is failing;
  `event: update` resumes normal payloads on recovery.

`is_remote` = `RemoteEndPoint` IP is outside RFC1918 / loopback / ULA / link-local.

### 8.6 `POST /api/refresh?job=library|watch|all`

`202` `{ "data": { "library": {"queued": true}, "watch": {"running": true} } }`
(per-job status). Result is observed via the next `GET` of the relevant page.

### 8.7 `GET /healthz`

`200 {"status":"ok"}` when the process is up and `store` answers `SELECT 1`;
`503` otherwise. No auth, no envelope.

---

## 9. Live subsystem design

- **Hub:** holds a `map[clientID]chan Event`. `Subscribe` adds a channel and
  returns it plus an unsubscribe func; `Publish` non-blocking-sends to each
  (drops to a per-client ring buffer of size 4 if the client is slow; a client
  that overflows is disconnected and left to `EventSource` auto-reconnect).
- **Poll loop:** a single goroutine. Started when subscriber count goes 0 → 1,
  stopped (ticker + goroutine torn down) when it returns to 0. Interval
  `LIVE_POLL_INTERVAL` (default 4s).
- Each tick: `GET /Sessions` with a 3s timeout → normalize to the payload shape
  → diff against the last snapshot → if changed, `Publish(update)` and store as
  new snapshot. New subscribers always get the stored snapshot immediately (or a
  fresh fetch if none yet).
- **Upstream failure:** keep serving the last snapshot; publish one `degraded`
  event; retry every tick with capped backoff (4s → 30s). Never tear down the
  hub or panic.
- The one-shot `GET /api/now-playing` reuses the hub's stored snapshot if it is
  < `LIVE_POLL_INTERVAL` old, else does a direct fetch (this is what a first
  page load hits before the stream connects).
- **Client:** opens `EventSource` on mount; closes it on `visibilitychange` →
  `hidden`, reopens on `visible`; relies on built-in reconnect otherwise.

---

## 10. Frontend

### 10.1 Stack & tooling

- React + Vite + **TypeScript**.
- **Mantine** (`@mantine/core`, `@mantine/hooks`) + **`@mantine/charts`**
  (Recharts under the hood) for layout, tables, tabs, controls, and charts.
- **TanStack Query** for fetching/caching. `staleTime` per endpoint matched to
  its refresh interval; background refetch on window focus; the envelope's
  `meta.stale` surfaces a small "data is catching up" banner.
- `react-router` for client-side routing.
- **Biome** for lint + format (replaces ESLint + Prettier). CI runs
  `biome ci .`.
- **Vitest** + React Testing Library for component tests.

### 10.2 App shell & routes

Mantine `AppShell`: left nav (Library / Watch / Now Playing / Cleanup), header
with server name + version and a "last refreshed · Refresh now" control that
calls `POST /api/refresh?job=all`. Nav collapses to a burger below `sm`.

| Route | Component | Data |
|---|---|---|
| `/` | redirect → `/library` | — |
| `/library` | `LibraryOverview` | `GET /api/library/overview` |
| `/watch` | `WatchStats` (range SegmentedControl: 30d / 90d / 1y / all) | `GET /api/watch/stats?range=` |
| `/now` | `NowPlaying` | `EventSource /api/now-playing/stream`; `GET /api/now-playing` for first paint / fallback |
| `/cleanup` | `Cleanup` (watched=never/stale toggle, sort control, "Export CSV") | `GET /api/cleanup?…` |

### 10.3 Per-page components & states

Every page renders: **loading** (Mantine `Skeleton`), **error** (message +
Retry), **empty** (contextual copy), **data**. Additional:

- `WatchStats` handles `plugin_available: false` with a dedicated panel: what the
  plugin is, and copy-paste steps to install it from Jellyfin's plugin catalog.
- `Cleanup` shows `reclaimable_bytes` prominently and a note that it never
  deletes anything.
- `NowPlaying` empty state = "Nothing playing right now"; shows a `degraded`
  ribbon when the stream reports upstream trouble.

### 10.4 Charts & the heatmap

- `@mantine/charts`: `BarChart` (disk by resolution/codec, genres, decades,
  active users), `AreaChart` (library growth — monthly bars + cumulative line;
  play-method weekly stacked), `LineChart` (watch-time trend).
- **Heatmap** (`WhenWeWatch`): a custom ~40-line component — CSS grid, 7 rows ×
  24 cols, cell background interpolated on `watch_sec` (light→accent), Mantine
  `Tooltip` per cell (`Sun 21:00 · 3.2 h`). No library.
- Ranked lists (top movies/series/episodes, active users) are Mantine `Table`
  with a plays ↔ hours `SegmentedControl`, not charts.

### 10.5 Build & embedding; dev proxy

- `web/` is its own package (`package.json`, Vite config). `vite build` outputs
  `web/dist/`.
- Go embeds it: `//go:embed all:web/dist` in a `web` package that exposes an
  `fs.FS`. The `api` server serves it at `/`, falling back to `index.html` for
  any non-`/api`, non-`/healthz` path (SPA routing).
- Dev: `vite dev` on `:5173` with a proxy for `/api` → `http://localhost:8080`
  (the Go server run locally). A `make dev` target runs both.
- The Go build depends on `web/dist` existing; `make build` runs the Vite build
  first. CI and the Dockerfile enforce the order.

---

## 11. Configuration

Environment variables only. Unset required var → fatal at startup with a message
naming the var.

| Var | Required | Default | Purpose |
|---|---|---|---|
| `JELLYFIN_URL` | yes | — | e.g. `http://jellyfin:8096` (compose service DNS) |
| `JELLYFIN_API_KEY` | yes | — | `X-Emby-Token` for `/Sessions`, `/Users`, `/System/Info` |
| `JELLYFIN_DATA_DIR` | yes (unless `SOURCE=api`) | — | read-only mount of Jellyfin's config dir; DBs read from `${JELLYFIN_DATA_DIR}/data/` |
| `SOURCE` | no | auto | `file` \| `api` \| `auto` (auto = `file` if DBs readable, else fatal) |
| `STORE_PATH` | no | `/data/ephyra.db` | Ephyra's SQLite |
| `WORK_DIR` | no | `/data/work` | scratch dir for DB copies (must be writable) |
| `LISTEN_ADDR` | no | `:8080` | HTTP listen address |
| `REFRESH_LIBRARY` | no | `30m` | library job interval (Go duration) |
| `REFRESH_WATCH` | no | `10m` | watch job interval |
| `LIVE_POLL_INTERVAL` | no | `4s` | `/Sessions` poll cadence while an SSE client is connected |
| `DIRECT_READ` | no | `false` | skip copy-before-read; open source DBs with `immutable=1` |
| `STREAM_CAPACITY` | no | unset | optional denominator for the concurrent-streams gauge |
| `LOG_LEVEL` | no | `info` | `debug` \| `info` \| `warn` \| `error`; `slog` JSON to stdout |
| `TZ` | no | `UTC` | affects day/week/heatmap bucketing (documented) |

---

## 12. Packaging & deployment

### Dockerfile (3 stages)

1. `node:22-alpine` — `npm ci` + `vite build` in `web/` → `web/dist`.
2. `golang:1.25-alpine` — copy source + `web/dist`, `CGO_ENABLED=0 go build
   -ldflags="-s -w"` → static `/ephyra`.
3. `gcr.io/distroless/static-debian12:nonroot` — copy the binary; `USER
   nonroot`; `ENTRYPOINT ["/ephyra"]`. Multi-arch `linux/amd64,linux/arm64` via
   buildx. Published to `ghcr.io/<owner>/ephyra`.

### `docker-compose.example.yml`

```yaml
services:
  ephyra:
    image: ghcr.io/<owner>/ephyra:latest
    environment:
      JELLYFIN_URL: http://jellyfin:8096
      JELLYFIN_API_KEY: ${EPHYRA_JELLYFIN_API_KEY}
      JELLYFIN_DATA_DIR: /jellyfin-data
    volumes:
      - /mnt/pool/apps/jellyfin/config:/jellyfin-data:ro
      - ephyra-data:/data
    ports:
      - "8080:8080"
    restart: unless-stopped
volumes:
  ephyra-data:
```

### Expected footprint

- Idle RAM ~15–30 MB; brief spike during a `library` refresh that actually
  copies + scans (seconds).
- Load on Jellyfin: **zero** standing HTTP load. One `GET /Sessions` per
  `LIVE_POLL_INTERVAL` *only while* someone has Now Playing open. `GET /Users`
  hourly. `GET /System/Info` every 10 min. No API calls for Library / Watch /
  Cleanup.

---

## 13. Observability & operations

- `slog` JSON to stdout. One line per refresh run: job, `skipped`, `duration_ms`,
  row counts, `ok`/error. One line on SSE poll-loop start/stop. WARN on upstream
  failure + recovery.
- `/healthz` for the container healthcheck.
- Prometheus `/metrics` is backlog #8 (refresh durations, last-success
  timestamps, SSE client gauge, upstream error counter).
- Failure behavior: a failed refresh keeps the previous `agg_*` rows; the page
  renders with `meta.stale=true` and a banner. The app never serves 5xx for a
  page just because a refresh failed.

---

## 14. Testing strategy

- **Aggregation (the core):** checked-in fixture SQLite files
  (`testdata/library.fixture.db`, `testdata/playback_reporting.fixture.db`) —
  tiny, hand-built to match the Task #0 schema notes, covering edge cases (no
  video stream, missing `Size`, multi-user played-state, unknown
  `PlaybackMethod`, item with zero genres). `aggregate` functions are pure:
  fixture rows in → `agg_*` rows compared to golden values.
- **API handlers:** run against a `store` seeded from golden `agg_*` rows; assert
  status, envelope, and body schema per endpoint (table-driven). A fake `Source`
  covers the `plugin_available=false` and `not_ready` paths.
- **Live hub:** fake `jellyfin.Client`; assert the poll loop starts on first
  subscribe and stops on last unsubscribe; assert snapshot diffing emits
  `update` only on real changes; assert `degraded` on upstream error and
  recovery after.
- **copy-before-read:** unit test the mtime-skip decision and the sidecar copy
  set with a temp dir.
- **Frontend:** Vitest component tests for each page's loading/error/empty/data
  states, the range control, and the heatmap cell scaling.
- **Smoke:** compose-free integration test that starts the binary with
  `SOURCE=file` pointed at the fixtures + a stub `/Sessions` HTTP server, then
  hits every endpoint and asserts `200` + schema.
- **CI:** `go test ./...`, `go vet`, `golangci-lint run`, `biome ci web`,
  `vitest run`, `docker buildx build`.

---

## 15. Repository layout

```
ephyra/
├── cmd/ephyra/main.go            # wire config → store → source → scheduler → live → api; ListenAndServe
├── internal/
│   ├── config/                   # env parsing + validation
│   ├── source/                   # Source interface + shared types (LibrarySnapshot, PlaybackEvent)
│   │   ├── file/                 # FileSource: copy-before-read, SQLite queries
│   │   └── api/                  # APISource stub (backlog #1)
│   ├── jellyfin/                 # HTTP client: Sessions, Users, SystemInfo
│   ├── aggregate/                # pure: snapshots/events → agg_* rows
│   ├── store/                    # our SQLite: migrations/*.sql, typed upserts, reads
│   ├── scheduler/               # tickers, manual trigger, per-job mutex, mtime skip
│   ├── live/                     # SSE hub + poll loop
│   └── api/                      # handlers, envelope, SSE, static serving
├── web/                          # React + Vite + TS app (own package.json)
│   ├── src/{pages,components,api,charts}/
│   └── dist/                     # build output, embedded (gitignored)
├── testdata/                     # fixture DBs + golden JSON
├── docs/
│   ├── schema-notes.md           # produced by Task #0
│   └── superpowers/specs/        # this file
├── Dockerfile
├── docker-compose.example.yml
├── Makefile                      # dev, build, test, lint, docker
└── go.mod                        # module github.com/<owner>/ephyra
```

---

## 16. Backlog (post-v1)

1. **`APISource`** — Jellyfin 12 / Postgres backend / remote deployments. Same
   `Source` interface; `/Items` + `/Items/Counts` for library, plugin
   `submit_custom_query` for playback.
2. **Jellyfin-credential login + per-user scoping** — non-admins see only their
   own profile page; admins see everything. Validates against Jellyfin auth.
3. **Per-user profile pages**; completion vs abandonment (watched duration vs
   runtime); binge/marathon detection; new-watch vs rewatch.
4. **Ephyra's own event history** — persist Now Playing poll samples and derived
   session records, enabling bandwidth-over-time, recently-ended sessions, and
   trends beyond the plugin's retention.
5. **Local vs remote streaming** breakdown + optional offline geo-IP; client /
   device deep-dive.
6. **Library health** — duplicates / multi-version detection, missing
   metadata/artwork counts, bitrate outliers; audio channel/language deep-dive.
7. **Webhook-triggered refresh** — consume the Jellyfin Webhook plugin instead of
   mtime polling for near-immediate library updates.
8. **Server page** (uptime, scheduled tasks, disk free) + Prometheus `/metrics`.

---

## 17. Open questions / to verify during implementation

- **Task #0 output.** Exact table/column names in 10.11.11 for the item store,
  `MediaStreams`, genre storage, and `UserDatas` (and whether `UserDatas` lives
  in `library.db` or `jellyfin.db`). All §8 query shapes depend on this.
- **`PlaybackMethod` distinct values** actually written by the installed plugin
  version → the `agg_play_method` bucket mapping.
- **`library.db` size on the operator's system** → confirms the copy-before-read
  cost assumption; if it is multi-GB, consider tightening the mtime skip or
  documenting a longer `REFRESH_LIBRARY`.
- **Per-item `Size`** population: confirm episodes/movies carry a byte size after
  a normal scan; define fallback (sum of media part sizes, or exclude from disk
  charts with a counted "unknown" bucket).
- **`<owner>`** GitHub namespace for the module path and GHCR image — set once at
  repo creation.

---

## 18. Build sequence (coarse; detailed plan follows separately)

1. Task #0 schema-discovery spike → `docs/schema-notes.md`.
2. Repo skeleton: module, `config`, `store` + migrations, `Makefile`, CI.
3. `source` interface + types; `FileSource` copy-before-read + one query
   (`agg_totals`) end to end, with fixtures.
4. Remaining `library` aggregations + `GET /api/library/overview`.
5. `watch` aggregations (`watch_events_daily` + `agg_watch_heatmap`) +
   `GET /api/watch/stats`; plugin-absent path.
6. `cleanup` aggregation + `GET /api/cleanup` (+ CSV).
7. `jellyfin.Client` + `live` hub + SSE + `GET /api/now-playing[/stream]`.
8. `scheduler` (tickers, manual trigger, startup run) + `POST /api/refresh`.
9. React app: shell, four pages, charts, heatmap, states; Biome + Vitest.
10. Embed, SPA fallback, Dockerfile, compose example, smoke test.
11. Docs: README (setup, API-key creation, compose), CHANGELOG.
