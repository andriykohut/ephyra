# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Ephyra is a stats dashboard for Jellyfin: one Go binary with a React SPA baked in.
It reads *copies* of Jellyfin's SQLite files on a schedule and serves rolled-up
aggregates. It never writes to Jellyfin or to the mounted directory.

This is **Plan 1 of 3** (Library Overview). The design spec and implementation
plans live in `docs/superpowers/`. `watch_events_daily`, `agg_watch_heatmap`, and
`agg_cleanup` exist in the schema but are unused until Plan 2 (Watch Stats &
Cleanup); Plan 3 is Now Playing (SSE).

## Commands

Go (module `github.com/andriykohut/ephyra`, floor `go 1.25`):

- `make test` → `CGO_ENABLED=0 go test ./...`
- One package / one test: `go test ./internal/aggregate/ -run TestLibraryAggregates -v`
- `make lint` → `go vet ./...` + `golangci-lint run`. **golangci-lint must be v2**
  — `.golangci.yml` is v2 schema. If it isn't on `PATH`, it's usually at
  `~/go/bin/golangci-lint` (installed via `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`).
- `make build` → builds `web/` then the binary (`./ephyra`). Frontend must build first.
- Always `CGO_ENABLED=0` (pure-Go SQLite via `modernc.org/sqlite`).

Frontend (in `web/`):

- `npm run test` (Vitest) · one file: `npx vitest run src/routes/library.test.tsx`
- `npm run lint` → `biome ci .` · `npx tsc -b` (type-check) · `npm run build`

Dev — two processes:

```sh
cd web && npm run dev      # :5173, proxies /api and /healthz to :8080
go run ./cmd/ephyra        # :8080  (needs a data/library.db to read — see below)
```

Run against the checked-in fixture instead of a real Jellyfin:

```sh
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/library.db < testdata/library.fixture.sql
JELLYFIN_URL=x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf \
  STORE_PATH=/tmp/ephyra.db WORK_DIR=/tmp/ephyra-work go run ./cmd/ephyra
```

## Architecture

**Data pipeline (one direction, no live reads):**

```
scheduler tick / POST /api/refresh
  └─ Source.LibraryFacts(ctx)
       FileSource: stat library.db; if mtime == refresh_meta.source_mtime → skip,
       just bump the meta. Otherwise copy library.db + -wal/-shm into WORK_DIR,
       query the copy, delete it.
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
  set. `refresh_meta` drives staleness.
- `internal/scheduler` — one ticker per job + a manual-trigger channel, per-job
  mutex, mtime-skip. Runs each job once on startup.
- `internal/api` — `net/http` `ServeMux` (method patterns). Every JSON response is
  `{ "data": ..., "meta": { "generated_at": <RFC3339>, "stale": <bool> } }`;
  errors are `{ "error": { "code", "message" } }`. **Staleness** = no
  `refresh_meta` row, or last run failed, or older than 2× the interval. Unknown
  `/api/*` paths return 404 JSON; every other GET falls through to the SPA
  (`index.html`).
- `cmd/ephyra/main.go` — wiring + graceful shutdown. Picks the source, fails fast
  with a clear message if `library.db` isn't reachable.

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

The queries in `internal/source/file/queries.go` assume a particular `library.db`
layout, documented in `docs/schema-notes.md`. That layout has **not** been
verified against a real Jellyfin yet — run `scripts/dump-jellyfin-schema.sh` on a
Jellyfin host and reconcile before trusting the numbers. `testdata/library.fixture.sql`
is a hand-built stand-in shaped like the assumed schema;
`internal/testsupport.LibraryFixtureDB(t)` builds it into a temp DB per test.

## Conventions

- Documentation and code comments: casual and deadpan. No exclamation marks, no
  "simply" / "powerful" / marketing voice.
- The `ephyra` name and mark are carved out of the MIT license (see `README.md`
  and `NOTICES.md`); bundled fonts are SIL OFL 1.1 (`web/public/OFL.txt` ships
  in the build).
