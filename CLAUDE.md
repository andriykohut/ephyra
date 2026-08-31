# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Ephyra is a stats dashboard for Jellyfin: one Go binary with a React SPA baked in.
It reads *copies* of Jellyfin's SQLite files on a schedule and serves rolled-up
aggregates. It never writes to Jellyfin or to the mounted directory.

**Five pages live.** Library Overview; Watch Stats & Cleanup; Now Playing;
Profiles (per-user completion / rewatch / binge / taste, backed by an
append-only `playback_events` spine that outlives the plugin's retention). The
design specs and implementation plans live in `docs/superpowers/`.

## Commands

Go (module `github.com/andriykohut/ephyra`, floor `go 1.25`):

- `make test` → `CGO_ENABLED=0 go test ./...`
- One package / one test: `go test ./internal/aggregate/ -run TestLibraryAggregates -v`
- `make lint` → `go vet ./...` + `golangci-lint run`. **golangci-lint must be v2**
  — `.golangci.yml` is v2 schema. If it isn't on `PATH`, it's usually at
  `~/go/bin/golangci-lint` (installed via `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`).
- `make build` → builds `web/` then the binary (`./ephyra`). Frontend must build first.
- `make dist [VERSION=vX.Y.Z]` → cross-compiled release tarballs into `./dist/`
  for linux/darwin × amd64/arm64, plus `SHA256SUMS`. Same set `release.yml`
  attaches to the GitHub release; `./dist` is gitignored.
- Always `CGO_ENABLED=0` (pure-Go SQLite via `modernc.org/sqlite`).

Frontend (in `web/`):

- `npm run test` (Vitest) · one file: `npx vitest run src/routes/library.test.tsx`
- `npm run lint` → `biome ci .` · `npx tsc -b` (type-check) · `npm run build`

Dev — two processes:

```sh
cd web && npm run dev      # :5173, proxies /api and /healthz to :8097
go run ./cmd/ephyra        # :8097  (needs a Jellyfin DB to read — see below)
```

Run against the checked-in fixture instead of a real Jellyfin:

```sh
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/jellyfin.db < testdata/library.fixture.sql
JELLYFIN_URL=x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf \
  STORE_PATH=/tmp/ephyra.db WORK_DIR=/tmp/ephyra-work go run ./cmd/ephyra
```

## Architecture

**Data pipeline (one direction, no live reads):**

```
scheduler tick / POST /api/refresh
  └─ Source.LibraryFacts(ctx)
       FileSource: stat the item DB (jellyfin.db, under data/ or data/data/);
       if mtime == refresh_meta.source_mtime → skip, just bump the meta.
       Otherwise copy it + -wal/-shm into WORK_DIR, query the copy, delete it.
  └─ aggregate.Library(snapshot, loc)   ← pure, no I/O, no SQL
  └─ store.WriteLibraryAggregates(...)  ← one txn: DELETE + re-INSERT the agg_* rows
API handlers read ONLY from store's agg_* tables, never from Jellyfin.
```

- `internal/source` — the `Source` interface (`LibraryFacts`, `PlaybackEvents`,
  `Kind`) plus the plain structs it returns. `internal/source/file` is the only
  implementation. This is the pluggability seam: an `APISource` for Jellyfin 12 /
  Postgres / remote is designed-in but not built — `SOURCE=api` returns an error
  today. `MTimer` is the optional file-source extra the scheduler uses for the
  skip check.
- `internal/aggregate` — pure functions. `buckets.go` classifies raw values
  (resolution/codec/HDR/decade); `library.go` walks a snapshot once into
  `LibraryAggregates`. Tested with in-code snapshots and golden values.
- `internal/store` — Ephyra's own SQLite (`STORE_PATH`). Migrations are embedded
  (`migrations/*.sql`), forward-only, integer-versioned by filename prefix; the
  runner owns `schema_migrations`, so a migration must **not** `CREATE` it.
  `agg_*` tables are fully rewritten per refresh; the API always sees a complete
  set. `refresh_meta` drives staleness. The one exception is `playback_events`:
  append-only, never rewritten — the watch job upserts new plugin rows into it
  (idempotent on `dedup_hash`), reads the whole history back, and feeds *that*
  to both `aggregate.Watch` and `aggregate.Profiles`.
- `internal/scheduler` — one ticker per job + a manual-trigger channel, per-job
  mutex, mtime-skip. Runs each job once on startup.
- `internal/api` — `net/http` `ServeMux` (method patterns). Every JSON response is
  `{ "data": ..., "meta": { "generated_at": <RFC3339>, "stale": <bool> } }`;
  errors are `{ "error": { "code", "message" } }`. **Staleness** = no
  `refresh_meta` row, or last run failed, or older than 2× the interval. Unknown
  `/api/*` paths return 404 JSON; every other GET falls through to the SPA
  (`index.html`). List fields serialize as `[]`, never `null` — the SPA iterates
  them straight off the response, so the `store` reads run every slice through
  `orEmpty` before returning.
- `internal/jellyfin` / `internal/live` — the Now Playing live path, off to the
  side of the pipeline above. `internal/jellyfin` is a thin read-only HTTP client
  (not a `Source`). `internal/live` holds the SSE hub whose poll loop runs
  **only** while a browser has `/now` open — zero standing load otherwise.
  `hub.Prime` is non-fatal: Ephyra boots even with the Jellyfin API down.
- `cmd/ephyra/main.go` — wiring + graceful shutdown. Picks the source, fails fast
  with a clear message if the item DB isn't reachable.

**Frontend (`web/`):** React 19 + Vite + TypeScript, Tailwind v4, TanStack Router
+ Query, Apache ECharts.

- Built to `web/dist/`, embedded by `web/embed.go` (`//go:embed all:dist`) and
  served by the Go binary. **`web/dist/.gitkeep` must stay tracked** — without it
  `go build ./...` fails on a clean checkout with "pattern all:dist: no matching
  files found". Build output under `web/dist/` is gitignored.
- Design tokens are Tailwind v4 `@theme` custom properties in `src/index.css`.
  Components are hand-rolled in `src/components/` (no shadcn/ui). `src/charts/`
  wraps ECharts; its theme reads the CSS custom properties at runtime, and the
  chart chunk is lazy-loaded so the page paints before it arrives.
- The `Mark` component and `web/public/{favicon,icon}.svg` are the logo; brand
  assets and rules are in `docs/brand/`.

## Jellyfin schema

`internal/source/file/queries.go` targets Jellyfin **10.11**'s `jellyfin.db`
(EF-Core: `BaseItems` / `MediaStreamInfos` / `Users` / `UserData`), verified
against a real DB on 2026-08-30. `docs/schema-notes.md` records the layout and the
gotchas (integer `StreamType` enum, `TopParentId` points at the physical folder
not the `CollectionFolder`, no `Container` column — derived from `Path`). Older
`library.db` installs (10.10 and earlier) are not handled. Re-run README's "Test
against a real library" after any Jellyfin upgrade. `testdata/library.fixture.sql`
+ `testdata/playback_reporting.fixture.sql` are hand-built stand-ins;
`internal/testsupport` loads them (`LibraryFixtureDB`, `PlaybackFixtureDB`,
`TwoDBLayout`).

**Watch job reads two DBs.** `Source.PlaybackEvents` opens the Playback Reporting
plugin's `playback_reporting.db` for the events and does a targeted read of the
`jellyfin.db` copy for user names + current title/series. Plugin absent →
`source.ErrPluginUnavailable`, job records `plugin_available=false`, writes no
`agg_watch_*` rows (not a failure).

**Canonical IDs.** Every user/item/series id in Ephyra's store is dashless
lowercase (`source.CanonID`). The plugin DB is already that shape; `jellyfin.db`
is dashed-uppercase and is normalized on read.

**Watch timestamps are not re-zoned.** `playback_reporting.db` writes server-local
wall time; `aggregate.Watch` buckets `day`/`dow`/`hour` off the literal
components and ignores `TZ` (unlike `aggregate.Library`, whose `jellyfin.db`
`DateCreated` is UTC).

## Conventions

- Documentation and code comments: casual and deadpan. No exclamation marks, no
  "simply" / "powerful" / marketing voice.
- The `ephyra` name and mark are carved out of the MIT license (see `README.md`
  and `NOTICES.md`); bundled fonts are SIL OFL 1.1 (`web/public/OFL.txt` ships
  in the build).
