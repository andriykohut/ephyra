# Ephyra — Foundations & Library Overview — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the Ephyra service spine and ship the Library Overview page end to end — a running container that reads a copy of Jellyfin's `library.db`, materializes library aggregates into its own SQLite, and serves them to an embedded React page.

**Architecture:** A single Go binary. A `Source` interface abstracts data acquisition; `FileSource` copies Jellyfin's SQLite files to a work dir (the mount is read-only) and queries the copy into a `LibrarySnapshot`. Pure `aggregate` functions turn the snapshot into rows; `store` writes them transactionally into Ephyra's own SQLite and reads them back as an API DTO. A `scheduler` runs the library job on a timer, skipping when `library.db`'s mtime is unchanged. A `net/http` API serves the JSON plus the embedded React SPA.

**Tech Stack:** Go 1.25, `modernc.org/sqlite` (pure Go, no CGO), stdlib `net/http` + `log/slog`; React 19 + Vite + TypeScript, Tailwind v4 + shadcn/ui, TanStack Router + Query, Apache ECharts; Biome; Vitest 3; Docker (distroless).

**Spec:** `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md` — read it alongside this plan. This plan implements §4, §5.1, §5.4 (interface only), §5.5, §5.6, §6 (schema; watch tables created but unused), §7 (library job only), §8.1, §8.2, §8.6, §8.7, §10 (shell + Library page), §11, §12, §13, §14, §15.

## Global Constraints

Copied verbatim from the spec. Every task inherits these.

- **No CGO.** `CGO_ENABLED=0` for every build. SQLite access is `modernc.org/sqlite` only.
- **Go module path:** `github.com/andrii/ephyra`. This is a local identifier for an application (never published as a library). If your GitHub username is not `andrii`, choose the final value in Task 1 Step 1 and use it consistently in `go.mod` and every import.
- **One artifact.** The frontend builds into the binary via `//go:embed`. One container, one config block, no runtime dependencies.
- **Reads only.** Ephyra never writes to Jellyfin or to the mounted Jellyfin data directory. All Jellyfin DB access is against copies in `WORK_DIR`, opened read-only.
- **Copy-before-read is the default.** `DIRECT_READ=true` is an opt-in escape hatch for read-write mounts.
- **Plugin-optional.** Nothing in this plan requires the Playback Reporting plugin. (Watch Stats arrives in Plan 2.)
- **Config is environment variables only.** An unset required var is a fatal startup error naming the var.
- **Response envelope:** every non-stream JSON response is `{ "data": ..., "meta": { "generated_at": <RFC3339>, "stale": <bool> } }`. Errors are `{ "error": { "code": <string>, "message": <string> } }`.
- **Staleness rule:** a job's data is stale when its last `refresh_meta` row has `ok = 0`, OR `now - last_run_at > 2 × interval`.
- **Timestamps** in API output are RFC3339 UTC (`time.Now().UTC().Format(time.RFC3339)`).
- **TDD.** Every task is: write the failing test → run it, see it fail → minimal implementation → run it, see it pass → commit. Small commits.

## Jellyfin schema primer (domain knowledge you need)

Jellyfin 10.11's item store is `library.db` (SQLite, WAL mode). The tables this plan reads:

- **`TypedBaseItems`** — one row per library item. ~100 columns; the ones we use:
  - `guid` BLOB — 16-byte item id.
  - `type` TEXT — a .NET class name. Movies are `MediaBrowser.Controller.Entities.Movies.Movie`, episodes `MediaBrowser.Controller.Entities.TV.Episode`, series `MediaBrowser.Controller.Entities.TV.Series`, library folders `MediaBrowser.Controller.Entities.CollectionFolder`.
  - `Name` TEXT, `Path` TEXT, `Container` TEXT (e.g. `mkv`).
  - `Size` BIGINT — file size in bytes (nullable; populated after a library scan).
  - `RunTimeTicks` BIGINT — runtime in 100-nanosecond ticks. Seconds = `RunTimeTicks / 10000000`.
  - `DateCreated` TEXT — when the item was added. Stored like `2024-03-01 12:00:00.0000000Z` or without the trailing `Z`; treat as UTC.
  - `ProductionYear` INT (nullable).
  - `Genres` TEXT — pipe-delimited, e.g. `Drama|Thriller` (nullable/empty). We use this column directly (not the normalized `ItemValues` table) to avoid its version-dependent `Type` enum.
  - `TopParentId` TEXT — the `guid` of the item's top-level library folder, as an un-dashed hex string.
  - `Width` INT, `Height` INT — primary video resolution, denormalized (nullable).
  - `IsVirtualItem` BIT — `1` for placeholder rows (missing episodes). Always exclude.
  - `IsFolder` BIT — `1` for folders/series/seasons.
- **`MediaStreams`** — one row per stream per item.
  - `ItemId` BLOB — matches `TypedBaseItems.guid`.
  - `StreamIndex` INT, `StreamType` TEXT (`Video` / `Audio` / `Subtitle` / …).
  - `Codec` TEXT (`h264`, `hevc`, `av1`, …).
  - `Width` INT, `Height` INT.
  - `ColorTransfer` TEXT — HDR marker: `smpte2084` = HDR10, `arib-std-b67` = HLG, otherwise SDR.
  - `DvProfile` INT (nullable) — Dolby Vision profile; `> 0` means Dolby Vision.

"Which library an item belongs to" = look up `TypedBaseItems` rows of type `CollectionFolder` to get `(guid, Name)`, then match `upper(hex(folder.guid)) = upper(replace(item.TopParentId,'-',''))`.

Exact column names/types vary slightly by Jellyfin build. **Task 2 pins them against the operator's real DB** and adjusts the fixture + queries if needed; the tests then guard behavior.

---

## File structure

```
go.mod
Makefile
.gitignore
.dockerignore
.golangci.yml
.github/workflows/ci.yml
Dockerfile
docker-compose.example.yml
README.md
scripts/dump-jellyfin-schema.sh          # operator runs against their live Jellyfin
docs/schema-notes.md                     # Task 2 output: real schema + deltas from baseline
cmd/ephyra/main.go                       # wiring + graceful shutdown
internal/buildinfo/buildinfo.go          # Version string, set via -ldflags
internal/config/config.go                # env parsing + validation
internal/config/config_test.go
internal/testsupport/fixtures.go         # builds fixture DBs from .sql into temp files
internal/store/store.go                  # open sqlite, run migrations, RefreshMeta helpers
internal/store/store_test.go
internal/store/migrations/0001_init.sql  # full v1 schema (spec §6)
internal/store/writes.go                 # WriteLibraryAggregates
internal/store/writes_test.go
internal/store/reads.go                  # ReadLibraryOverview + staleness
internal/store/reads_test.go
internal/source/source.go                # Source + MTimer interfaces; LibrarySnapshot, LibraryItem, PlaybackEvent
internal/source/file/file.go             # FileSource: copy-before-read, SourceMTime, LibraryFacts
internal/source/file/file_test.go
internal/source/file/queries.go          # SQL → LibrarySnapshot
internal/source/file/queries_test.go
internal/aggregate/buckets.go            # ResolutionBucket, CodecBucket, HDRBucket, Decade
internal/aggregate/buckets_test.go
internal/aggregate/library.go            # Library(LibrarySnapshot) LibraryAggregates
internal/aggregate/library_test.go
internal/scheduler/scheduler.go          # tickers, Trigger, per-job mutex, mtime skip, library job
internal/scheduler/scheduler_test.go
internal/api/api.go                      # ServeMux, envelope, writeJSON/writeError
internal/api/api_test.go
internal/api/health.go                   # GET /healthz
internal/api/library.go                  # GET /api/library/overview
internal/api/library_test.go
internal/api/refresh.go                  # POST /api/refresh (minimal; full wiring in Plan 3)
internal/api/static.go                   # serve embedded SPA + fallback
internal/api/static_test.go
testdata/library.fixture.sql             # schema + rows for the library fixture
web/embed.go                             # package web: //go:embed all:dist
web/dist/.gitkeep
web/package.json
web/tsconfig.json  web/tsconfig.node.json
web/vite.config.ts
web/biome.json
web/index.html
web/components.json                      # shadcn/ui config
web/src/main.tsx
web/src/router.tsx                       # TanStack Router: routeTree + router
web/src/index.css                        # Tailwind v4 entry + @theme tokens
web/src/lib/utils.ts                     # cn() helper (clsx + tailwind-merge)
web/src/lib/format.ts                    # fmtBytes / fmtDuration
web/src/api/client.ts
web/src/api/types.ts
web/src/api/queries.ts                   # TanStack Query options factories
web/src/components/ui/                    # shadcn primitives (button, card, table, sheet, skeleton, alert, sonner, tooltip, badge, separator)
web/src/components/AppShell.tsx
web/src/components/StatCard.tsx
web/src/components/Section.tsx            # titled panel wrapper for charts
web/src/components/StaleBanner.tsx
web/src/charts/echarts.ts                # registered ECharts core + modules + theme
web/src/charts/EChart.tsx                # React wrapper (resize-observed)
web/src/charts/theme.ts                  # shared light/dark ECharts theme from CSS tokens
web/src/routes/__root.tsx                # root route: AppShell + Outlet
web/src/routes/library.tsx               # /library route + LibraryOverview
web/src/routes/watch.tsx  web/src/routes/now.tsx  web/src/routes/cleanup.tsx  # placeholder routes
web/src/routes/library.test.tsx
web/src/test/setup.ts
```

---

## Task 1: Repo skeleton, config, build info, CI (Go)

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `.golangci.yml`, `.github/workflows/ci.yml`
- Create: `internal/buildinfo/buildinfo.go`
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `buildinfo.Version() string` — returns the build version (default `"dev"`).
  - `config.Config` struct with fields:
    ```go
    type Config struct {
        JellyfinURL     string        // JELLYFIN_URL (required)
        JellyfinAPIKey  string        // JELLYFIN_API_KEY (required)
        JellyfinDataDir string        // JELLYFIN_DATA_DIR (required unless Source=="api")
        Source          string        // SOURCE: "auto"|"file"|"api"  (default "auto")
        StorePath       string        // STORE_PATH  (default "/data/ephyra.db")
        WorkDir         string        // WORK_DIR    (default "/data/work")
        ListenAddr      string        // LISTEN_ADDR (default ":8080")
        RefreshLibrary  time.Duration // REFRESH_LIBRARY (default 30m)
        RefreshWatch    time.Duration // REFRESH_WATCH   (default 10m)
        LivePollInterval time.Duration// LIVE_POLL_INTERVAL (default 4s)
        DirectRead      bool          // DIRECT_READ (default false)
        StreamCapacity  int           // STREAM_CAPACITY (default 0 = unset)
        LogLevel        slog.Level    // LOG_LEVEL (default info)
    }
    func Load(getenv func(string) string) (Config, error)
    ```
  - `config.Load` returns a non-nil error listing every missing required var (joined), and parses durations with `time.ParseDuration`, `LogLevel` from `debug|info|warn|error`.

- [ ] **Step 1: Decide the module path.** Default `github.com/andrii/ephyra`. If your GitHub username differs, pick the final string now; use it in `go.mod` and every import in this plan.

- [ ] **Step 2: Create `go.mod`**

```
module github.com/andrii/ephyra

go 1.25
```

Then: `go get modernc.org/sqlite@latest` (adds the driver; it is used from Task 3 on).

- [ ] **Step 3: Create `internal/buildinfo/buildinfo.go`**

```go
package buildinfo

// version is overridden at build time with:
//   -ldflags "-X github.com/andrii/ephyra/internal/buildinfo.version=<v>"
var version = "dev"

// Version returns the build version string.
func Version() string { return version }
```

- [ ] **Step 4: Write the failing test** — `internal/config/config_test.go`

```go
package config

import (
	"log/slog"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{
		"JELLYFIN_URL":      "http://jellyfin:8096",
		"JELLYFIN_API_KEY":  "k",
		"JELLYFIN_DATA_DIR": "/jellyfin-data",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.StorePath != "/data/ephyra.db" || c.WorkDir != "/data/work" || c.ListenAddr != ":8080" {
		t.Fatalf("bad defaults: %+v", c)
	}
	if c.RefreshLibrary != 30*time.Minute || c.RefreshWatch != 10*time.Minute || c.LivePollInterval != 4*time.Second {
		t.Fatalf("bad duration defaults: %+v", c)
	}
	if c.Source != "auto" || c.DirectRead || c.LogLevel != slog.LevelInfo {
		t.Fatalf("bad misc defaults: %+v", c)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	_, err := Load(env(map[string]string{"SOURCE": "file"}))
	if err == nil {
		t.Fatal("expected error for missing required vars")
	}
	for _, want := range []string{"JELLYFIN_URL", "JELLYFIN_API_KEY", "JELLYFIN_DATA_DIR"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestLoadDataDirOptionalForAPISource(t *testing.T) {
	_, err := Load(env(map[string]string{
		"JELLYFIN_URL": "u", "JELLYFIN_API_KEY": "k", "SOURCE": "api",
	}))
	if err != nil {
		t.Fatalf("data dir should be optional when SOURCE=api: %v", err)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"JELLYFIN_URL": "u", "JELLYFIN_API_KEY": "k", "JELLYFIN_DATA_DIR": "/d",
		"REFRESH_LIBRARY": "5m", "LOG_LEVEL": "debug", "DIRECT_READ": "true",
		"STREAM_CAPACITY": "6", "LISTEN_ADDR": ":9000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshLibrary != 5*time.Minute || c.LogLevel != slog.LevelDebug || !c.DirectRead || c.StreamCapacity != 6 || c.ListenAddr != ":9000" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 5: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoad -v`
Expected: build failure — `undefined: Load`, `undefined: Config`.

- [ ] **Step 6: Implement `internal/config/config.go`**

```go
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	JellyfinURL      string
	JellyfinAPIKey   string
	JellyfinDataDir  string
	Source           string
	StorePath        string
	WorkDir          string
	ListenAddr       string
	RefreshLibrary   time.Duration
	RefreshWatch     time.Duration
	LivePollInterval time.Duration
	DirectRead       bool
	StreamCapacity   int
	LogLevel         slog.Level
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		JellyfinURL:      getenv("JELLYFIN_URL"),
		JellyfinAPIKey:   getenv("JELLYFIN_API_KEY"),
		JellyfinDataDir:  getenv("JELLYFIN_DATA_DIR"),
		Source:           def(getenv("SOURCE"), "auto"),
		StorePath:        def(getenv("STORE_PATH"), "/data/ephyra.db"),
		WorkDir:          def(getenv("WORK_DIR"), "/data/work"),
		ListenAddr:       def(getenv("LISTEN_ADDR"), ":8080"),
		RefreshLibrary:   30 * time.Minute,
		RefreshWatch:     10 * time.Minute,
		LivePollInterval: 4 * time.Second,
	}

	var errs []string
	req := func(name, val string) {
		if strings.TrimSpace(val) == "" {
			errs = append(errs, "missing required env "+name)
		}
	}
	req("JELLYFIN_URL", c.JellyfinURL)
	req("JELLYFIN_API_KEY", c.JellyfinAPIKey)
	if c.Source != "api" {
		req("JELLYFIN_DATA_DIR", c.JellyfinDataDir)
	}
	if c.Source != "auto" && c.Source != "file" && c.Source != "api" {
		errs = append(errs, "SOURCE must be one of auto|file|api, got "+c.Source)
	}

	dur := func(name string, dst *time.Duration) {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				return
			}
			*dst = d
		}
	}
	dur("REFRESH_LIBRARY", &c.RefreshLibrary)
	dur("REFRESH_WATCH", &c.RefreshWatch)
	dur("LIVE_POLL_INTERVAL", &c.LivePollInterval)

	if v := getenv("DIRECT_READ"); v != "" {
		c.DirectRead = v == "1" || strings.EqualFold(v, "true")
	}
	if v := getenv("STREAM_CAPACITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, "STREAM_CAPACITY: "+err.Error())
		}
		c.StreamCapacity = n
	}
	switch strings.ToLower(def(getenv("LOG_LEVEL"), "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		errs = append(errs, "LOG_LEVEL must be debug|info|warn|error")
	}

	if len(errs) > 0 {
		return Config{}, errors.New(strings.Join(errs, "; "))
	}
	return c, nil
}

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/config/ -v`
Expected: PASS (all four tests).

- [ ] **Step 8: Create `.gitignore`**

```
/web/dist/*
!/web/dist/.gitkeep
/web/node_modules
/ephyra
*.db
*.db-wal
*.db-shm
/dist
.DS_Store
```

- [ ] **Step 9: Create `Makefile`**

```make
GO ?= go
VERSION ?= dev
LDFLAGS := -s -w -X github.com/andrii/ephyra/internal/buildinfo.version=$(VERSION)

.PHONY: test lint web build run dev docker fixtures

test:
	CGO_ENABLED=0 $(GO) test ./...

lint:
	$(GO) vet ./...
	golangci-lint run

web:
	cd web && npm ci && npm run build

build: web
	CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o ephyra ./cmd/ephyra

run: build
	./ephyra

dev:
	@echo "terminal 1: cd web && npm run dev"
	@echo "terminal 2: go run ./cmd/ephyra"

docker:
	docker build -t ephyra:$(VERSION) .
```

- [ ] **Step 10: Create `.golangci.yml`**

```yaml
run:
  timeout: 3m
linters:
  enable:
    - errcheck
    - govet
    - staticcheck
    - ineffassign
    - unused
    - gofmt
    - misspell
```

- [ ] **Step 11: Create `.github/workflows/ci.yml`**

```yaml
name: CI
on:
  push: { branches: [main] }
  pull_request:
jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.25" }
      - run: CGO_ENABLED=0 go build ./...
      - run: CGO_ENABLED=0 go test ./...
      - run: go vet ./...
      - uses: golangci/golangci-lint-action@v6
        with: { version: latest }
```

- [ ] **Step 12: Commit**

```bash
git add go.mod go.sum Makefile .gitignore .golangci.yml .github internal/buildinfo internal/config
git commit -m "chore: repo skeleton, config loader, build info, CI"
```

---

## Task 2: Schema discovery + test fixtures

**Files:**
- Create: `scripts/dump-jellyfin-schema.sh`
- Create: `docs/schema-notes.md`
- Create: `testdata/library.fixture.sql`
- Create: `internal/testsupport/fixtures.go`
- Test: `internal/testsupport/fixtures_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `testsupport.LibraryFixtureDB(t testing.TB) string` — writes a fresh SQLite file to `t.TempDir()`, executes `testdata/library.fixture.sql` into it via `modernc.org/sqlite`, returns the path. Fails the test on any error.
  - `testdata/library.fixture.sql` — a self-contained SQL script: `CREATE TABLE` for `TypedBaseItems` and `MediaStreams` (only the columns this plan reads, matching the primer), plus `INSERT`s described below.

- [ ] **Step 1: Create `scripts/dump-jellyfin-schema.sh`** (operator convenience — not run in CI)

```bash
#!/usr/bin/env sh
# Run on the Jellyfin host. Points at the live data dir, copies the DBs, dumps schema.
# Usage: JELLYFIN_DATA_DIR=/path/to/jellyfin/config ./scripts/dump-jellyfin-schema.sh
set -eu
DIR="${JELLYFIN_DATA_DIR:?set JELLYFIN_DATA_DIR}"
OUT="${1:-schema-dump.txt}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
for db in library.db playback_reporting.db; do
  [ -f "$DIR/data/$db" ] || { echo "== $db: NOT FOUND" >>"$OUT"; continue; }
  cp "$DIR/data/$db" "$DIR/data/$db-wal" "$DIR/data/$db-shm" "$TMP/" 2>/dev/null || cp "$DIR/data/$db" "$TMP/"
  {
    echo "== $db schema =="
    sqlite3 "$TMP/$db" '.schema'
    echo
    echo "== $db: sample rows =="
    sqlite3 -header -column "$TMP/$db" \
      "SELECT type, count(*) FROM TypedBaseItems GROUP BY type ORDER BY 2 DESC LIMIT 20;" 2>/dev/null || true
  } >>"$OUT"
done
echo "wrote $OUT"
```

- [ ] **Step 2: Create `docs/schema-notes.md`** with the baseline below, and a checklist for the operator.

```markdown
# Jellyfin schema notes

Baseline assumed by Ephyra's queries (Jellyfin 10.11). If the operator's real
dump (`scripts/dump-jellyfin-schema.sh`) differs, record the delta here and
adjust `testdata/library.fixture.sql` + `internal/source/file/queries.go`.

## TypedBaseItems (columns used)
guid BLOB, type TEXT, Name TEXT, Path TEXT, Container TEXT, Size BIGINT,
RunTimeTicks BIGINT, DateCreated TEXT, ProductionYear INT, Genres TEXT,
TopParentId TEXT, Width INT, Height INT, IsVirtualItem BIT, IsFolder BIT

type values: Movie = 'MediaBrowser.Controller.Entities.Movies.Movie',
Episode = 'MediaBrowser.Controller.Entities.TV.Episode',
Series = 'MediaBrowser.Controller.Entities.TV.Series',
CollectionFolder = 'MediaBrowser.Controller.Entities.CollectionFolder'

## MediaStreams (columns used)
ItemId BLOB, StreamIndex INT, StreamType TEXT, Codec TEXT, Width INT,
Height INT, ColorTransfer TEXT, DvProfile INT

## Verification checklist (operator)
- [ ] `TypedBaseItems` has columns: Size, Container, TopParentId, Genres, DateCreated, IsVirtualItem
- [ ] `TopParentId` format: un-dashed hex? dashed? case? -> adjust the `replace()/upper()` in queries.go
- [ ] `MediaStreams` has `ColorTransfer` and `DvProfile` (older builds: `DvProfile` may be absent -> treat as always NULL)
- [ ] `DateCreated` string format -> confirm the parse layouts in aggregate/library.go cover it
- [ ] confirm virtual/missing episodes carry IsVirtualItem=1
```

- [ ] **Step 3: Create `testdata/library.fixture.sql`**

Schema mirrors the primer. Rows must exercise: two libraries; movies + episodes; a series row and a virtual episode (both must be excluded from aggregates); an item with `NULL` `Size`; an item with no video stream; HDR10, HLG, Dolby Vision, and SDR items; multiple genres; items across three decades and three calendar months.

```sql
PRAGMA journal_mode = WAL;

CREATE TABLE TypedBaseItems (
  guid BLOB PRIMARY KEY,
  type TEXT,
  Name TEXT,
  Path TEXT,
  Container TEXT,
  Size BIGINT,
  RunTimeTicks BIGINT,
  DateCreated TEXT,
  ProductionYear INT,
  Genres TEXT,
  TopParentId TEXT,
  Width INT,
  Height INT,
  IsVirtualItem INT DEFAULT 0,
  IsFolder INT DEFAULT 0
);

CREATE TABLE MediaStreams (
  ItemId BLOB,
  StreamIndex INT,
  StreamType TEXT,
  Codec TEXT,
  Width INT,
  Height INT,
  ColorTransfer TEXT,
  DvProfile INT
);

-- library folders
INSERT INTO TypedBaseItems (guid, type, Name, TopParentId, IsFolder) VALUES
 (x'11111111111111111111111111111111', 'MediaBrowser.Controller.Entities.CollectionFolder', 'Movies', NULL, 1),
 (x'22222222222222222222222222222222', 'MediaBrowser.Controller.Entities.CollectionFolder', 'Shows',  NULL, 1);

-- MOVIES (TopParentId = un-dashed upper hex of the Movies folder guid)
INSERT INTO TypedBaseItems
 (guid, type, Name, Container, Size, RunTimeTicks, DateCreated, ProductionYear, Genres, TopParentId, Width, Height) VALUES
 (x'0000000000000000000000000000000A', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Alpha',   'mkv', 8000000000,  72000000000, '2024-01-05 10:00:00.0000000Z', 1994, 'Drama|Thriller', '11111111111111111111111111111111', 3840, 2160),
 (x'0000000000000000000000000000000B', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Bravo',   'mp4', 4000000000,  60000000000, '2024-01-20 10:00:00.0000000Z', 2001, 'Comedy',         '11111111111111111111111111111111', 1920, 1080),
 (x'0000000000000000000000000000000C', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Charlie', 'mkv', NULL,        90000000000, '2024-02-10 10:00:00.0000000Z', 2019, 'Drama',          '11111111111111111111111111111111', 1280, 720),
 (x'0000000000000000000000000000000D', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Delta',   'mkv', 15000000000, 80000000000, '2024-03-02 10:00:00.0000000Z', 2022, 'Sci-Fi|Drama',   '11111111111111111111111111111111', 3840, 2160);

-- EPISODES (Shows library)
INSERT INTO TypedBaseItems
 (guid, type, Name, Container, Size, RunTimeTicks, DateCreated, ProductionYear, Genres, TopParentId, Width, Height) VALUES
 (x'000000000000000000000000000000E1', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E1', 'mkv', 1200000000, 18000000000, '2024-02-15 10:00:00.0000000Z', 2020, 'Drama', '22222222222222222222222222222222', 1920, 1080),
 (x'000000000000000000000000000000E2', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E2', 'mkv', 1300000000, 18000000000, '2024-03-16 10:00:00.0000000Z', 2020, 'Drama', '22222222222222222222222222222222', 1920, 1080);

-- excluded: a series, and a virtual (missing) episode
INSERT INTO TypedBaseItems (guid, type, Name, TopParentId, IsFolder) VALUES
 (x'000000000000000000000000000000F0', 'MediaBrowser.Controller.Entities.TV.Series', 'Some Show', '22222222222222222222222222222222', 1);
INSERT INTO TypedBaseItems (guid, type, Name, TopParentId, IsVirtualItem, Size, RunTimeTicks) VALUES
 (x'000000000000000000000000000000F9', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E99 (missing)', '22222222222222222222222222222222', 1, 999999999, 18000000000);

-- video streams
INSERT INTO MediaStreams (ItemId, StreamIndex, StreamType, Codec, Width, Height, ColorTransfer, DvProfile) VALUES
 (x'0000000000000000000000000000000A', 0, 'Video', 'hevc', 3840, 2160, 'smpte2084', NULL),   -- Alpha: HDR10
 (x'0000000000000000000000000000000A', 1, 'Audio', 'eac3', NULL, NULL, NULL, NULL),
 (x'0000000000000000000000000000000B', 0, 'Video', 'h264', 1920, 1080, 'bt709', NULL),       -- Bravo: SDR
 (x'0000000000000000000000000000000D', 0, 'Video', 'hevc', 3840, 2160, 'arib-std-b67', NULL),-- Delta: HLG
 (x'000000000000000000000000000000E1', 0, 'Video', 'av1',  1920, 1080, 'smpte2084', 8),      -- S1E1: Dolby Vision (DvProfile>0)
 (x'000000000000000000000000000000E2', 0, 'Video', 'h264', 1920, 1080, '', NULL);            -- S1E2: SDR (blank transfer)
-- note: 'Charlie' has NO video stream row on purpose (Unknown resolution/codec/hdr)
```

- [ ] **Step 4: Write the failing test** — `internal/testsupport/fixtures_test.go`

```go
package testsupport

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestLibraryFixtureDB(t *testing.T) {
	path := LibraryFixtureDB(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var movies int
	if err := db.QueryRow(
		`SELECT count(*) FROM TypedBaseItems WHERE type = ?`,
		"MediaBrowser.Controller.Entities.Movies.Movie",
	).Scan(&movies); err != nil {
		t.Fatal(err)
	}
	if movies != 4 {
		t.Fatalf("want 4 movie rows, got %d", movies)
	}

	var streams int
	if err := db.QueryRow(`SELECT count(*) FROM MediaStreams WHERE StreamType='Video'`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if streams != 5 {
		t.Fatalf("want 5 video streams, got %d", streams)
	}
}
```

- [ ] **Step 5: Run test to verify it fails**

Run: `go test ./internal/testsupport/ -v`
Expected: build failure — `undefined: LibraryFixtureDB`.

- [ ] **Step 6: Implement `internal/testsupport/fixtures.go`**

```go
// Package testsupport builds throwaway SQLite fixtures for tests.
package testsupport

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// LibraryFixtureDB creates a fresh SQLite database from testdata/library.fixture.sql
// in t.TempDir() and returns its path.
func LibraryFixtureDB(t testing.TB) string {
	t.Helper()
	return buildFixture(t, "library.fixture.sql")
}

func buildFixture(t testing.TB, name string) string {
	t.Helper()
	sqlBytes, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("exec fixture %s: %v", name, err)
	}
	return path
}

// fixturePath walks up from the test's working directory to the repo root
// (identified by go.mod) and joins testdata/<name>.
func fixturePath(t testing.TB, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", name)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root (go.mod) from %s", dir)
		}
		dir = parent
	}
}
```

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./internal/testsupport/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add scripts docs/schema-notes.md testdata internal/testsupport
git commit -m "test: Jellyfin schema notes, library fixture, fixture builder"
```

---

## Task 3: Store + migrations

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/migrations/0001_init.sql`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `config.Config` (only `StorePath`).
- Produces:
  ```go
  type Store struct { /* holds *sql.DB */ }
  func Open(ctx context.Context, path string) (*Store, error) // opens sqlite, applies migrations
  func (s *Store) DB() *sql.DB
  func (s *Store) Close() error

  type RefreshMeta struct {
      Job             string
      LastRunAt       time.Time
      SourceMTime     time.Time // zero = none recorded
      DurationMS      int64
      OK              bool
      Skipped         bool
      PluginAvailable bool
      Error           string
  }
  func (s *Store) GetRefreshMeta(ctx context.Context, job string) (RefreshMeta, bool, error)
  func (s *Store) SetRefreshMeta(ctx context.Context, m RefreshMeta) error
  ```
- Migrations are embedded (`//go:embed migrations/*.sql`), applied in filename order, each wrapped in a transaction, tracked by integer version in `schema_migrations`. Re-running `Open` is a no-op.

- [ ] **Step 1: Create `internal/store/migrations/0001_init.sql`** — the full v1 schema (spec §6). Watch/cleanup tables are created now so later plans need no migration.

```sql
CREATE TABLE schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE refresh_meta (
  job              TEXT PRIMARY KEY,
  last_run_at      TEXT NOT NULL,
  source_mtime     TEXT NOT NULL DEFAULT '',
  duration_ms      INTEGER NOT NULL DEFAULT 0,
  ok               INTEGER NOT NULL DEFAULT 0,
  skipped          INTEGER NOT NULL DEFAULT 0,
  plugin_available INTEGER NOT NULL DEFAULT 0,
  error            TEXT NOT NULL DEFAULT ''
);

CREATE TABLE agg_totals (
  metric TEXT PRIMARY KEY,
  value  REAL NOT NULL
);

CREATE TABLE agg_disk (
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  bytes     INTEGER NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (dimension, bucket)
);

CREATE TABLE agg_distribution (
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (dimension, bucket)
);

CREATE TABLE agg_library_growth (
  month        TEXT PRIMARY KEY,
  added_items  INTEGER NOT NULL,
  added_bytes  INTEGER NOT NULL,
  cum_items    INTEGER NOT NULL
);

CREATE TABLE watch_events_daily (
  day             TEXT NOT NULL,
  user_id         TEXT NOT NULL,
  item_id         TEXT NOT NULL,
  scope           TEXT NOT NULL,
  name            TEXT NOT NULL,
  plays           INTEGER NOT NULL,
  watch_sec       INTEGER NOT NULL,
  method          TEXT NOT NULL,
  PRIMARY KEY (day, user_id, item_id, method)
);

CREATE TABLE agg_watch_heatmap (
  dow       INTEGER NOT NULL,
  hour      INTEGER NOT NULL,
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (dow, hour)
);

CREATE TABLE agg_cleanup (
  item_id        TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  library        TEXT NOT NULL,
  bytes          INTEGER NOT NULL,
  added_at       TEXT NOT NULL,
  last_played_at TEXT
);
```

- [ ] **Step 2: Write the failing test** — `internal/store/store_test.go`

```go
package store

import (
	"context"
	"testing"
	"time"
)

func TestOpenAppliesMigrationsIdempotently(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/s.db"

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s1.DB().QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 applied migration, got %d", n)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path) // reopen: must not error, must not re-apply
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	for _, table := range []string{"refresh_meta", "agg_totals", "agg_disk", "agg_distribution", "agg_library_growth", "watch_events_daily", "agg_watch_heatmap", "agg_cleanup"} {
		if _, err := s2.DB().ExecContext(ctx, `SELECT 1 FROM `+table+` LIMIT 1`); err != nil {
			t.Errorf("table %s not usable: %v", table, err)
		}
	}
}

func TestRefreshMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, ok, err := s.GetRefreshMeta(ctx, "library"); err != nil || ok {
		t.Fatalf("expected no row, got ok=%v err=%v", ok, err)
	}

	mt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	in := RefreshMeta{
		Job: "library", LastRunAt: mt, SourceMTime: mt.Add(-time.Hour),
		DurationMS: 1234, OK: true, Skipped: false, PluginAvailable: false,
	}
	if err := s.SetRefreshMeta(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetRefreshMeta(ctx, "library")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.LastRunAt.Equal(in.LastRunAt) || !got.SourceMTime.Equal(in.SourceMTime) || got.DurationMS != 1234 || !got.OK {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	in.DurationMS = 5 // upsert
	if err := s.SetRefreshMeta(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetRefreshMeta(ctx, "library")
	if got.DurationMS != 5 {
		t.Fatalf("upsert failed, duration=%d", got.DurationMS)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/store/ -v`
Expected: build failure — `undefined: Open`.

- [ ] **Step 4: Implement `internal/store/store.go`**

```go
// Package store is Ephyra's own SQLite database: schema migrations plus typed
// read/write helpers. The API reads only from here.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // modernc + single-writer: serialize
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		return err
	}
	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: bad version prefix", name)
		}
		if applied[v] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			v, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

type RefreshMeta struct {
	Job             string
	LastRunAt       time.Time
	SourceMTime     time.Time
	DurationMS      int64
	OK              bool
	Skipped         bool
	PluginAvailable bool
	Error           string
}

const tsLayout = time.RFC3339Nano

func (s *Store) GetRefreshMeta(ctx context.Context, job string) (RefreshMeta, bool, error) {
	var (
		m                        RefreshMeta
		lastRun, srcMTime        string
		ok, skipped, pluginAvail int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT job, last_run_at, source_mtime, duration_ms, ok, skipped, plugin_available, error
		   FROM refresh_meta WHERE job = ?`, job,
	).Scan(&m.Job, &lastRun, &srcMTime, &m.DurationMS, &ok, &skipped, &pluginAvail, &m.Error)
	if err == sql.ErrNoRows {
		return RefreshMeta{}, false, nil
	}
	if err != nil {
		return RefreshMeta{}, false, err
	}
	m.LastRunAt, _ = time.Parse(tsLayout, lastRun)
	if srcMTime != "" {
		m.SourceMTime, _ = time.Parse(tsLayout, srcMTime)
	}
	m.OK, m.Skipped, m.PluginAvailable = ok == 1, skipped == 1, pluginAvail == 1
	return m, true, nil
}

func (s *Store) SetRefreshMeta(ctx context.Context, m RefreshMeta) error {
	srcMTime := ""
	if !m.SourceMTime.IsZero() {
		srcMTime = m.SourceMTime.UTC().Format(tsLayout)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO refresh_meta (job, last_run_at, source_mtime, duration_ms, ok, skipped, plugin_available, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job) DO UPDATE SET
		  last_run_at=excluded.last_run_at, source_mtime=excluded.source_mtime,
		  duration_ms=excluded.duration_ms, ok=excluded.ok, skipped=excluded.skipped,
		  plugin_available=excluded.plugin_available, error=excluded.error`,
		m.Job, m.LastRunAt.UTC().Format(tsLayout), srcMTime, m.DurationMS,
		b2i(m.OK), b2i(m.Skipped), b2i(m.PluginAvailable), m.Error,
	)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/store/ -v`
Expected: PASS (both tests).

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(store): sqlite open + embedded migrations + refresh_meta helpers"
```

---

## Task 4: Source types + FileSource file-plumbing

**Files:**
- Create: `internal/source/source.go`
- Create: `internal/source/file/file.go`
- Test: `internal/source/file/file_test.go`

**Interfaces:**
- Consumes: `config.Config` (`JellyfinDataDir`, `WorkDir`, `DirectRead`).
- Produces:
  ```go
  // internal/source
  type LibraryItem struct {
      Type          string    // "movie" | "episode"
      SizeBytes     int64
      RuntimeSec    int64
      DateCreated   time.Time // parsed; zero if unparseable
      DateRaw       string    // original string, for diagnostics
      Year          int
      Genres        []string
      Library       string    // resolved folder name, or "Unknown"
      Container     string
      VideoCodec    string    // raw, e.g. "hevc"; "" if no video stream
      Width         int       // primary video stream width; 0 if none
      HasVideo      bool
      ColorTransfer string    // raw, e.g. "smpte2084"; "" if none/blank
      DvProfile     *int      // nil unless present and > 0-capable
  }
  type LibrarySnapshot struct {
      GeneratedAt time.Time
      Items       []LibraryItem
      SeriesCount int
  }
  type PlaybackEvent struct{} // filled in Plan 2

  type Source interface {
      LibraryFacts(ctx context.Context) (LibrarySnapshot, error)
      PlaybackEvents(ctx context.Context, since time.Time) ([]PlaybackEvent, error)
      Kind() string // "file" | "api"
  }

  // Optional; only FileSource implements it. job is "library" | "watch".
  type MTimer interface {
      SourceMTime(job string) (time.Time, error)
  }
  ```
  ```go
  // internal/source/file
  func New(cfg config.Config, log *slog.Logger) *FileSource
  func (f *FileSource) Kind() string
  func (f *FileSource) SourceMTime(job string) (time.Time, error) // stats data/library.db or data/playback_reporting.db; (time.Time{}, nil) if absent
  func (f *FileSource) LibraryFacts(ctx context.Context) (source.LibrarySnapshot, error)
  func (f *FileSource) PlaybackEvents(ctx context.Context, since time.Time) ([]source.PlaybackEvent, error) // returns (nil, source.ErrNotImplemented) in Plan 1
  ```
- `LibraryFacts` (this task): ensure `WorkDir/library` exists, copy `data/library.db` + any `-wal`/`-shm` sidecars there (unless `DirectRead`), then call `queryLibrary(db)` (added in Task 5 — for now a package-level `var queryLibrary = func(*sql.DB) (source.LibrarySnapshot, error){ return source.LibrarySnapshot{}, nil }` stub), then remove the copy on success.

- [ ] **Step 1: Create `internal/source/source.go`** with the types and interfaces above, plus:

```go
package source

import "errors"

var ErrNotImplemented = errors.New("source: not implemented in this build")
```

- [ ] **Step 2: Write the failing test** — `internal/source/file/file_test.go`

```go
package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"log/slog"

	"github.com/andrii/ephyra/internal/config"
)

func newFS(t *testing.T, dataDir string) *FileSource {
	t.Helper()
	return New(config.Config{
		JellyfinDataDir: dataDir,
		WorkDir:         t.TempDir(),
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMTime(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "data", "library.db")
	writeFile(t, dbPath, "x")
	want := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dbPath, want, want); err != nil {
		t.Fatal(err)
	}

	f := newFS(t, dataDir)
	got, err := f.SourceMTime("library")
	if err != nil {
		t.Fatal(err)
	}
	if got.Unix() != want.Unix() {
		t.Fatalf("mtime: got %v want %v", got, want)
	}

	// absent playback db -> zero time, no error
	got, err = f.SourceMTime("watch")
	if err != nil || !got.IsZero() {
		t.Fatalf("absent watch db: got %v err %v", got, err)
	}
}

func TestLibraryFactsCopiesSidecars(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "data")
	writeFile(t, filepath.Join(base, "library.db"), "main")
	writeFile(t, filepath.Join(base, "library.db-wal"), "wal")
	writeFile(t, filepath.Join(base, "library.db-shm"), "shm")

	f := newFS(t, dataDir)
	var sawCopy bool
	orig := queryLibrary
	t.Cleanup(func() { queryLibrary = orig })
	queryLibrary = func(_ *sqlDB) (snap, error) {
		// the copy must exist at call time
		if _, err := os.Stat(filepath.Join(f.workDir, "library", "library.db")); err == nil {
			sawCopy = true
		}
		return snap{}, nil
	}

	if _, err := f.LibraryFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawCopy {
		t.Fatal("expected library.db copy to exist during queryLibrary")
	}
	// cleaned up afterwards
	if _, err := os.Stat(filepath.Join(f.workDir, "library", "library.db")); !os.IsNotExist(err) {
		t.Fatalf("copy not cleaned up: %v", err)
	}
}
```

> Note: the test refers to `sqlDB` and `snap` aliases so the plumbing test does not depend on Task 5's real signature. Define in `file.go`:
> `type sqlDB = sql.DB` and `type snap = source.LibrarySnapshot`.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/source/file/ -v`
Expected: build failure — `undefined: New`, `undefined: FileSource`.

- [ ] **Step 4: Implement `internal/source/file/file.go`**

```go
// Package file implements source.Source by copying Jellyfin's SQLite files to a
// work directory (the Jellyfin mount is read-only) and querying the copy.
package file

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/source"
	_ "modernc.org/sqlite"
)

type sqlDB = sql.DB
type snap = source.LibrarySnapshot

// queryLibrary turns an open *sql.DB (a read-only copy of library.db) into a
// snapshot. Task 5 replaces this stub with defaultQueryLibrary via an init().
var queryLibrary = func(*sqlDB) (snap, error) { return snap{}, nil }

type FileSource struct {
	dataDir    string
	workDir    string
	directRead bool
	log        *slog.Logger
}

func New(cfg config.Config, log *slog.Logger) *FileSource {
	return &FileSource{
		dataDir:    cfg.JellyfinDataDir,
		workDir:    cfg.WorkDir,
		directRead: cfg.DirectRead,
		log:        log,
	}
}

func (f *FileSource) Kind() string { return "file" }

func (f *FileSource) dbPath(job string) string {
	name := "library.db"
	if job == "watch" {
		name = "playback_reporting.db"
	}
	return filepath.Join(f.dataDir, "data", name)
}

func (f *FileSource) SourceMTime(job string) (time.Time, error) {
	fi, err := os.Stat(f.dbPath(job))
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

func (f *FileSource) LibraryFacts(ctx context.Context) (source.LibrarySnapshot, error) {
	db, cleanup, err := f.openForRead(ctx, "library")
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	defer cleanup()

	s, err := queryLibrary(db)
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	s.GeneratedAt = time.Now().UTC()
	return s, nil
}

func (f *FileSource) PlaybackEvents(context.Context, time.Time) ([]source.PlaybackEvent, error) {
	return nil, source.ErrNotImplemented
}

// openForRead returns a read-only *sql.DB over either a private copy of the job's
// database (default) or the live file (DIRECT_READ=true). cleanup closes the DB
// and, for the copy path, deletes the copy.
func (f *FileSource) openForRead(ctx context.Context, job string) (*sql.DB, func(), error) {
	src := f.dbPath(job)
	if _, err := os.Stat(src); err != nil {
		return nil, nil, fmt.Errorf("source db %s: %w", src, err)
	}

	if f.directRead {
		db, err := sql.Open("sqlite", "file:"+src+"?mode=ro&immutable=1&_pragma=busy_timeout(5000)")
		if err != nil {
			return nil, nil, err
		}
		return db, func() { db.Close() }, nil
	}

	dstDir := filepath.Join(f.workDir, job)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return nil, nil, err
	}
	base := filepath.Base(src)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		from := src + suffix
		if _, err := os.Stat(from); err != nil {
			continue // sidecar may not exist
		}
		if err := copyFile(from, filepath.Join(dstDir, base+suffix)); err != nil {
			return nil, nil, err
		}
	}
	copyPath := filepath.Join(dstDir, base)
	db, err := sql.Open("sqlite", "file:"+copyPath+"?_pragma=busy_timeout(5000)&_pragma=query_only(true)")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		db.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(copyPath + suffix)
		}
	}
	return db, cleanup, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/source/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/source
git commit -m "feat(source): Source interface + FileSource copy-before-read plumbing"
```

---

## Task 5: FileSource library queries

**Files:**
- Create: `internal/source/file/queries.go`
- Test: `internal/source/file/queries_test.go`

**Interfaces:**
- Consumes: `*sql.DB` over a copy of `library.db`; the `source.LibrarySnapshot` / `source.LibraryItem` types.
- Produces:
  ```go
  func defaultQueryLibrary(db *sql.DB) (source.LibrarySnapshot, error)
  ```
  and an `init()` that assigns `queryLibrary = defaultQueryLibrary`.
- Behavior: one query for items joined to their primary video stream and resolved library name; one `COUNT(*)` for series. Maps `type` → `"movie"`/`"episode"`. Splits `Genres` on `|`. `DvProfile` scanned into `*int`; set to `nil` when NULL or `<= 0`.

- [ ] **Step 1: Write the failing test** — `internal/source/file/queries_test.go`

> This test identifies items by `Name`. In Task 4 you defined `source.LibraryItem`; confirm it has `Name string` and `DateCreated time.Time` (both listed in the Task 4 type note) and that `defaultQueryLibrary` populates them.

```go
package file

import (
	"database/sql"
	"testing"
	"time"

	"github.com/andrii/ephyra/internal/source"
	"github.com/andrii/ephyra/internal/testsupport"
)

func openFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", testsupport.LibraryFixtureDB(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustTime(s string) time.Time { ts, _ := time.Parse(time.RFC3339, s); return ts }

func TestDefaultQueryLibrary(t *testing.T) {
	snap, err := defaultQueryLibrary(openFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Items) != 6 || snap.SeriesCount != 1 {
		t.Fatalf("items=%d series=%d", len(snap.Items), snap.SeriesCount)
	}
	items := map[string]source.LibraryItem{}
	libCount := map[string]int{}
	for _, it := range snap.Items {
		items[it.Name] = it
		libCount[it.Library]++
	}
	if libCount["Movies"] != 4 || libCount["Shows"] != 2 {
		t.Fatalf("library attribution: %v", libCount)
	}
	alpha := items["Alpha"]
	if alpha.Type != "movie" || alpha.VideoCodec != "hevc" || alpha.ColorTransfer != "smpte2084" ||
		alpha.Width != 3840 || !alpha.HasVideo || alpha.SizeBytes != 8_000_000_000 ||
		alpha.RuntimeSec != 7200 || alpha.Year != 1994 ||
		len(alpha.Genres) != 2 || alpha.Genres[0] != "Drama" || alpha.Genres[1] != "Thriller" {
		t.Fatalf("Alpha wrong: %+v", alpha)
	}
	if !alpha.DateCreated.Equal(mustTime("2024-01-05T10:00:00Z")) {
		t.Fatalf("Alpha DateCreated: %v", alpha.DateCreated)
	}
	charlie := items["Charlie"]
	if charlie.HasVideo || charlie.Width != 0 || charlie.VideoCodec != "" || charlie.SizeBytes != 0 {
		t.Fatalf("Charlie should have no video and 0 size: %+v", charlie)
	}
	if dv := items["S1E1"].DvProfile; dv == nil || *dv != 8 {
		t.Fatalf("S1E1 DvProfile: %v", dv)
	}
	if items["S1E2"].DvProfile != nil {
		t.Fatalf("S1E2 DvProfile should be nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/source/file/ -run TestDefaultQueryLibrary -v`
Expected: build failure — `undefined: defaultQueryLibrary` (queries.go does not exist yet).

- [ ] **Step 3: Implement `internal/source/file/queries.go`**

```go
package file

import (
	"database/sql"
	"strings"
	"time"

	"github.com/andrii/ephyra/internal/source"
)

func init() { queryLibrary = defaultQueryLibrary }

const movieType = "MediaBrowser.Controller.Entities.Movies.Movie"
const episodeType = "MediaBrowser.Controller.Entities.TV.Episode"
const seriesType = "MediaBrowser.Controller.Entities.TV.Series"
const collectionFolderType = "MediaBrowser.Controller.Entities.CollectionFolder"

const libraryQuery = `
WITH pvs AS (
  SELECT ItemId,
         Codec         AS codec,
         Width         AS width,
         Height        AS height,
         ColorTransfer AS color_transfer,
         DvProfile     AS dv_profile,
         ROW_NUMBER() OVER (PARTITION BY ItemId ORDER BY StreamIndex) AS rn
  FROM MediaStreams
  WHERE StreamType = 'Video'
),
folders AS (
  SELECT upper(hex(guid)) AS fid, Name AS lib
  FROM TypedBaseItems
  WHERE type = ?
)
SELECT
  i.Name,
  i.type,
  COALESCE(i.Size, 0),
  COALESCE(i.RunTimeTicks, 0),
  COALESCE(i.DateCreated, ''),
  COALESCE(i.ProductionYear, 0),
  COALESCE(i.Genres, ''),
  COALESCE(f.lib, 'Unknown'),
  COALESCE(i.Container, ''),
  v.codec, v.width, v.color_transfer, v.dv_profile
FROM TypedBaseItems i
LEFT JOIN pvs v     ON v.ItemId = i.guid AND v.rn = 1
LEFT JOIN folders f ON f.fid = upper(replace(COALESCE(i.TopParentId, ''), '-', ''))
WHERE i.type IN (?, ?)
  AND COALESCE(i.IsVirtualItem, 0) = 0
  AND COALESCE(i.IsFolder, 0) = 0
`

func defaultQueryLibrary(db *sql.DB) (source.LibrarySnapshot, error) {
	rows, err := db.Query(libraryQuery, collectionFolderType, movieType, episodeType)
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	defer rows.Close()

	var snap source.LibrarySnapshot
	for rows.Next() {
		var (
			name, typ, dateRaw, genres, library, container string
			size, ticks                                    int64
			year                                           int
			codec, colorTransfer                           sql.NullString
			width                                          sql.NullInt64
			dvProfile                                      sql.NullInt64
		)
		if err := rows.Scan(&name, &typ, &size, &ticks, &dateRaw, &year, &genres, &library, &container,
			&codec, &width, &colorTransfer, &dvProfile); err != nil {
			return source.LibrarySnapshot{}, err
		}
		it := source.LibraryItem{
			Name:          name,
			Type:          shortType(typ),
			SizeBytes:     size,
			RuntimeSec:    ticks / 10_000_000,
			DateRaw:       dateRaw,
			DateCreated:   parseJellyfinTime(dateRaw),
			Year:          year,
			Genres:        splitGenres(genres),
			Library:       library,
			Container:     container,
			VideoCodec:    codec.String,
			Width:         int(width.Int64),
			HasVideo:      width.Valid || codec.Valid,
			ColorTransfer: colorTransfer.String,
		}
		if dvProfile.Valid && dvProfile.Int64 > 0 {
			p := int(dvProfile.Int64)
			it.DvProfile = &p
		}
		snap.Items = append(snap.Items, it)
	}
	if err := rows.Err(); err != nil {
		return source.LibrarySnapshot{}, err
	}

	if err := db.QueryRow(
		`SELECT count(*) FROM TypedBaseItems WHERE type = ? AND COALESCE(IsVirtualItem,0)=0`, seriesType,
	).Scan(&snap.SeriesCount); err != nil {
		return source.LibrarySnapshot{}, err
	}
	return snap, nil
}

func shortType(t string) string {
	if t == movieType {
		return "movie"
	}
	return "episode"
}

func splitGenres(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, g := range strings.Split(s, "|") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

var jellyfinTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.9999999Z",
	"2006-01-02 15:04:05Z",
	"2006-01-02 15:04:05.9999999",
	"2006-01-02 15:04:05",
}

func parseJellyfinTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range jellyfinTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/source/... -v`
Expected: PASS. Also run `go test ./internal/source/file/ -run TestLibraryFactsCopiesSidecars` — still green (the stub override is per-test).

- [ ] **Step 5: Commit**

```bash
git add internal/source
git commit -m "feat(source): FileSource library query -> LibrarySnapshot"
```

---

## Task 6: Library aggregation (buckets + Library fn)

**Files:**
- Create: `internal/aggregate/buckets.go`
- Test: `internal/aggregate/buckets_test.go`
- Create: `internal/aggregate/library.go`
- Test: `internal/aggregate/library_test.go`

**Interfaces:**
- Consumes: `source.LibrarySnapshot`.
- Produces:
  ```go
  func ResolutionBucket(width int) string   // "4K"|"1080p"|"720p"|"SD"|"Unknown"
  func CodecBucket(raw string) string       // "H.264"|"HEVC"|"AV1"|"VP9"|"MPEG-2"|"VC-1"|"Unknown"|<UPPER raw>
  func HDRBucket(colorTransfer string, dvProfile *int, hasVideo bool) string // "Dolby Vision"|"HDR10"|"HLG"|"SDR"|"Unknown"
  func Decade(year int) string              // "1990s" ... ; "Unknown" if year<=0

  type LabeledCount struct { Label string; Count int64 }
  type DiskBucket   struct { Bucket string; Bytes int64; Items int64 }
  type GrowthPoint  struct { Month string; AddedItems int64; AddedBytes int64; CumItems int64 }

  type LibraryAggregates struct {
      Totals           map[string]float64 // keys: items.total, items.Movie, items.Episode, items.Series,
                                          //       runtime_sec.total, bytes.total, count.uhd, count.hdr, count.dv
      ItemsByLibrary   []LabeledCount     // sorted by Count desc, then Label asc
      DiskByResolution []DiskBucket
      DiskByCodec      []DiskBucket
      DiskByContainer  []DiskBucket
      DiskByLibrary    []DiskBucket
      GenresTop        []LabeledCount     // top 15 by Count desc, then Label asc
      ByDecade         []LabeledCount     // chronological (oldest decade first), "Unknown" last
      Growth           []GrowthPoint      // chronological by Month (YYYY-MM); CumItems running total
  }

  func Library(snap source.LibrarySnapshot, loc *time.Location) LibraryAggregates
  ```
- Rules: iterate items once, accumulate into maps, then materialize sorted slices. Disk `*Bytes` sums `SizeBytes`; `*Items` counts rows (including size-unknown rows). `count.hdr` counts items whose `HDRBucket` is `HDR10`, `HLG`, or `Dolby Vision`. `count.uhd` counts `ResolutionBucket == "4K"`. Growth buckets by `DateCreated` month in `loc` (skip items with zero `DateCreated`, counting them into a synthetic... no — skip them entirely and do not let them affect `CumItems`); `CumItems` is the running total of `AddedItems` across the sorted months.

- [ ] **Step 1: Write the failing test** — `internal/aggregate/buckets_test.go`

```go
package aggregate

import "testing"

func TestResolutionBucket(t *testing.T) {
	cases := map[int]string{4096: "4K", 3840: "4K", 3200: "4K", 1920: "1080p", 1400: "1080p", 1280: "720p", 1000: "720p", 720: "SD", 1: "SD", 0: "Unknown"}
	for w, want := range cases {
		if got := ResolutionBucket(w); got != want {
			t.Errorf("ResolutionBucket(%d) = %q, want %q", w, got, want)
		}
	}
}

func TestCodecBucket(t *testing.T) {
	cases := map[string]string{"h264": "H.264", "AVC": "H.264", "hevc": "HEVC", "h265": "HEVC", "av1": "AV1", "vp9": "VP9", "": "Unknown", "weirdcodec": "WEIRDCODEC"}
	for in, want := range cases {
		if got := CodecBucket(in); got != want {
			t.Errorf("CodecBucket(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHDRBucket(t *testing.T) {
	p := 7
	zero := 0
	if got := HDRBucket("smpte2084", &p, true); got != "Dolby Vision" {
		t.Errorf("DV precedence: %q", got)
	}
	if got := HDRBucket("smpte2084", nil, true); got != "HDR10" {
		t.Errorf("HDR10: %q", got)
	}
	if got := HDRBucket("arib-std-b67", nil, true); got != "HLG" {
		t.Errorf("HLG: %q", got)
	}
	if got := HDRBucket("bt709", nil, true); got != "SDR" {
		t.Errorf("SDR: %q", got)
	}
	if got := HDRBucket("", nil, true); got != "SDR" {
		t.Errorf("blank transfer w/ video = SDR: %q", got)
	}
	if got := HDRBucket("", nil, false); got != "Unknown" {
		t.Errorf("no video = Unknown: %q", got)
	}
	if got := HDRBucket("smpte2084", &zero, true); got != "HDR10" {
		t.Errorf("DvProfile 0 is not DV: %q", got)
	}
}

func TestDecade(t *testing.T) {
	for in, want := range map[int]string{1994: "1990s", 2000: "2000s", 2019: "2010s", 2022: "2020s", 0: "Unknown", -5: "Unknown"} {
		if got := Decade(in); got != want {
			t.Errorf("Decade(%d) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/aggregate/ -run 'Bucket|Decade' -v`
Expected: build failure — undefined functions.

- [ ] **Step 3: Implement `internal/aggregate/buckets.go`**

```go
package aggregate

import (
	"fmt"
	"strings"
)

func ResolutionBucket(width int) string {
	switch {
	case width >= 3200:
		return "4K"
	case width >= 1400:
		return "1080p"
	case width >= 1000:
		return "720p"
	case width > 0:
		return "SD"
	default:
		return "Unknown"
	}
}

func CodecBucket(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "h264", "avc":
		return "H.264"
	case "hevc", "h265":
		return "HEVC"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "mpeg2video":
		return "MPEG-2"
	case "vc1":
		return "VC-1"
	case "":
		return "Unknown"
	default:
		return strings.ToUpper(strings.TrimSpace(raw))
	}
}

func HDRBucket(colorTransfer string, dvProfile *int, hasVideo bool) string {
	if dvProfile != nil && *dvProfile > 0 {
		return "Dolby Vision"
	}
	switch strings.ToLower(strings.TrimSpace(colorTransfer)) {
	case "smpte2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	}
	if !hasVideo {
		return "Unknown"
	}
	return "SDR"
}

func Decade(year int) string {
	if year <= 0 {
		return "Unknown"
	}
	return fmt.Sprintf("%ds", (year/10)*10)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/aggregate/ -run 'Bucket|Decade' -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test** — `internal/aggregate/library_test.go`

```go
package aggregate

import (
	"testing"
	"time"

	"github.com/andrii/ephyra/internal/source"
)

func mkItem(name, typ string, size, runtimeSec int64, date string, year int, genres []string, lib, container, codec, transfer string, width int, hasVideo bool, dv *int) source.LibraryItem {
	d, _ := time.Parse(time.RFC3339, date)
	return source.LibraryItem{
		Name: name, Type: typ, SizeBytes: size, RuntimeSec: runtimeSec,
		DateCreated: d, Year: year, Genres: genres, Library: lib, Container: container,
		VideoCodec: codec, ColorTransfer: transfer, Width: width, HasVideo: hasVideo, DvProfile: dv,
	}
}

func TestLibraryAggregates(t *testing.T) {
	dv := 8
	snap := source.LibrarySnapshot{
		SeriesCount: 3,
		Items: []source.LibraryItem{
			mkItem("Alpha", "movie", 8_000_000_000, 7200, "2024-01-05T10:00:00Z", 1994, []string{"Drama", "Thriller"}, "Movies", "mkv", "hevc", "smpte2084", 3840, true, nil),
			mkItem("Bravo", "movie", 4_000_000_000, 6000, "2024-01-20T10:00:00Z", 2001, []string{"Comedy"}, "Movies", "mp4", "h264", "bt709", 1920, true, nil),
			mkItem("Charlie", "movie", 0, 9000, "2024-02-10T10:00:00Z", 2019, []string{"Drama"}, "Movies", "mkv", "", "", 0, false, nil),
			mkItem("Delta", "movie", 15_000_000_000, 8000, "2024-03-02T10:00:00Z", 2022, []string{"Sci-Fi", "Drama"}, "Movies", "mkv", "hevc", "arib-std-b67", 3840, true, nil),
			mkItem("S1E1", "episode", 1_200_000_000, 1800, "2024-02-15T10:00:00Z", 2020, []string{"Drama"}, "Shows", "mkv", "av1", "smpte2084", 1920, true, &dv),
			mkItem("S1E2", "episode", 1_300_000_000, 1800, "2024-03-16T10:00:00Z", 2020, []string{"Drama"}, "Shows", "mkv", "h264", "", 1920, true, nil),
		},
	}
	a := Library(snap, time.UTC)

	if a.Totals["items.total"] != 6 || a.Totals["items.Movie"] != 4 || a.Totals["items.Episode"] != 2 || a.Totals["items.Series"] != 3 {
		t.Fatalf("counts: %+v", a.Totals)
	}
	if a.Totals["runtime_sec.total"] != 7200+6000+9000+8000+1800+1800 {
		t.Fatalf("runtime: %v", a.Totals["runtime_sec.total"])
	}
	if a.Totals["bytes.total"] != 8e9+4e9+0+15e9+1.2e9+1.3e9 {
		t.Fatalf("bytes: %v", a.Totals["bytes.total"])
	}
	if a.Totals["count.uhd"] != 2 { // Alpha, Delta
		t.Fatalf("uhd: %v", a.Totals["count.uhd"])
	}
	if a.Totals["count.hdr"] != 3 { // Alpha HDR10, Delta HLG, S1E1 DV
		t.Fatalf("hdr: %v", a.Totals["count.hdr"])
	}
	if a.Totals["count.dv"] != 1 {
		t.Fatalf("dv: %v", a.Totals["count.dv"])
	}

	if got := findDisk(a.DiskByCodec, "HEVC"); got.Bytes != 8e9+15e9 || got.Items != 2 {
		t.Fatalf("disk HEVC: %+v", got)
	}
	if got := findDisk(a.DiskByResolution, "Unknown"); got.Items != 1 || got.Bytes != 0 { // Charlie
		t.Fatalf("disk res Unknown: %+v", got)
	}
	if got := findDisk(a.DiskByLibrary, "Shows"); got.Items != 2 || got.Bytes != 2.5e9 {
		t.Fatalf("disk lib Shows: %+v", got)
	}

	if got := findLabel(a.GenresTop, "Drama"); got.Count != 4 {
		t.Fatalf("genre Drama: %+v", got)
	}
	if a.ByDecade[0].Label != "1990s" || a.ByDecade[len(a.ByDecade)-1].Label == "Unknown" && len(a.ByDecade) < 2 {
		t.Fatalf("decade order: %+v", a.ByDecade)
	}

	// growth: Jan 2 items, Feb 2, Mar 2 -> cum 2,4,6
	if len(a.Growth) != 3 {
		t.Fatalf("growth points: %+v", a.Growth)
	}
	if a.Growth[0].Month != "2024-01" || a.Growth[0].AddedItems != 2 || a.Growth[0].CumItems != 2 {
		t.Fatalf("growth[0]: %+v", a.Growth[0])
	}
	if a.Growth[2].Month != "2024-03" || a.Growth[2].CumItems != 6 {
		t.Fatalf("growth[2]: %+v", a.Growth[2])
	}
	if a.Growth[0].AddedBytes != 12e9 { // Alpha 8e9 + Bravo 4e9
		t.Fatalf("growth[0] bytes: %v", a.Growth[0].AddedBytes)
	}

	if a.ItemsByLibrary[0].Label != "Movies" || a.ItemsByLibrary[0].Count != 4 {
		t.Fatalf("items by library: %+v", a.ItemsByLibrary)
	}
}

func findDisk(s []DiskBucket, b string) DiskBucket {
	for _, x := range s {
		if x.Bucket == b {
			return x
		}
	}
	return DiskBucket{}
}
func findLabel(s []LabeledCount, l string) LabeledCount {
	for _, x := range s {
		if x.Label == l {
			return x
		}
	}
	return LabeledCount{}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `go test ./internal/aggregate/ -run TestLibraryAggregates -v`
Expected: build failure — `undefined: Library`, `undefined: DiskBucket`, etc.

- [ ] **Step 7: Implement `internal/aggregate/library.go`**

```go
package aggregate

import (
	"sort"
	"time"

	"github.com/andrii/ephyra/internal/source"
)

type LabeledCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}
type DiskBucket struct {
	Bucket string `json:"bucket"`
	Bytes  int64  `json:"bytes"`
	Items  int64  `json:"items"`
}
type GrowthPoint struct {
	Month      string `json:"month"`
	AddedItems int64  `json:"added_items"`
	AddedBytes int64  `json:"added_bytes"`
	CumItems   int64  `json:"cum_items"`
}

type LibraryAggregates struct {
	Totals           map[string]float64
	ItemsByLibrary   []LabeledCount
	DiskByResolution []DiskBucket
	DiskByCodec      []DiskBucket
	DiskByContainer  []DiskBucket
	DiskByLibrary    []DiskBucket
	GenresTop        []LabeledCount
	ByDecade         []LabeledCount
	Growth           []GrowthPoint
}

type diskAcc struct {
	bytes int64
	items int64
}

func Library(snap source.LibrarySnapshot, loc *time.Location) LibraryAggregates {
	if loc == nil {
		loc = time.UTC
	}
	totals := map[string]float64{
		"items.total": 0, "items.Movie": 0, "items.Episode": 0,
		"items.Series": float64(snap.SeriesCount),
		"runtime_sec.total": 0, "bytes.total": 0,
		"count.uhd": 0, "count.hdr": 0, "count.dv": 0,
	}
	byLibraryCount := map[string]int64{}
	diskRes := map[string]*diskAcc{}
	diskCodec := map[string]*diskAcc{}
	diskContainer := map[string]*diskAcc{}
	diskLibrary := map[string]*diskAcc{}
	genre := map[string]int64{}
	decade := map[string]int64{}
	growthItems := map[string]int64{}
	growthBytes := map[string]int64{}

	add := func(m map[string]*diskAcc, k string, bytes int64) {
		a := m[k]
		if a == nil {
			a = &diskAcc{}
			m[k] = a
		}
		a.bytes += bytes
		a.items++
	}

	for _, it := range snap.Items {
		totals["items.total"]++
		switch it.Type {
		case "movie":
			totals["items.Movie"]++
		case "episode":
			totals["items.Episode"]++
		}
		totals["runtime_sec.total"] += float64(it.RuntimeSec)
		totals["bytes.total"] += float64(it.SizeBytes)

		res := ResolutionBucket(it.Width)
		codec := CodecBucket(it.VideoCodec)
		hdr := HDRBucket(it.ColorTransfer, it.DvProfile, it.HasVideo)
		if res == "4K" {
			totals["count.uhd"]++
		}
		if hdr == "HDR10" || hdr == "HLG" || hdr == "Dolby Vision" {
			totals["count.hdr"]++
		}
		if hdr == "Dolby Vision" {
			totals["count.dv"]++
		}

		byLibraryCount[it.Library]++
		add(diskRes, res, it.SizeBytes)
		add(diskCodec, codec, it.SizeBytes)
		container := it.Container
		if container == "" {
			container = "Unknown"
		}
		add(diskContainer, container, it.SizeBytes)
		add(diskLibrary, it.Library, it.SizeBytes)

		for _, g := range it.Genres {
			genre[g]++
		}
		decade[Decade(it.Year)]++

		if !it.DateCreated.IsZero() {
			m := it.DateCreated.In(loc).Format("2006-01")
			growthItems[m]++
			growthBytes[m] += it.SizeBytes
		}
	}

	out := LibraryAggregates{Totals: totals}
	out.ItemsByLibrary = labeledSortedDesc(byLibraryCount)
	out.DiskByResolution = diskSlice(diskRes)
	out.DiskByCodec = diskSlice(diskCodec)
	out.DiskByContainer = diskSlice(diskContainer)
	out.DiskByLibrary = diskSlice(diskLibrary)
	out.GenresTop = topN(labeledSortedDesc(genre), 15)
	out.ByDecade = decadeSlice(decade)
	out.Growth = growthSlice(growthItems, growthBytes)
	return out
}

func labeledSortedDesc(m map[string]int64) []LabeledCount {
	out := make([]LabeledCount, 0, len(m))
	for k, v := range m {
		out = append(out, LabeledCount{Label: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func topN(s []LabeledCount, n int) []LabeledCount {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func diskSlice(m map[string]*diskAcc) []DiskBucket {
	out := make([]DiskBucket, 0, len(m))
	for k, a := range m {
		out = append(out, DiskBucket{Bucket: k, Bytes: a.bytes, Items: a.items})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Bucket < out[j].Bucket
	})
	return out
}

func decadeSlice(m map[string]int64) []LabeledCount {
	out := make([]LabeledCount, 0, len(m))
	for k, v := range m {
		out = append(out, LabeledCount{Label: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		ui, uj := out[i].Label == "Unknown", out[j].Label == "Unknown"
		if ui != uj {
			return uj // Unknown sorts last
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func growthSlice(items, bytes map[string]int64) []GrowthPoint {
	months := make([]string, 0, len(items))
	for m := range items {
		months = append(months, m)
	}
	sort.Strings(months)
	var cum int64
	out := make([]GrowthPoint, 0, len(months))
	for _, m := range months {
		cum += items[m]
		out = append(out, GrowthPoint{Month: m, AddedItems: items[m], AddedBytes: bytes[m], CumItems: cum})
	}
	return out
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/aggregate/ -v`
Expected: PASS (all four tests).

- [ ] **Step 9: Commit**

```bash
git add internal/aggregate
git commit -m "feat(aggregate): library buckets + Library() pure aggregation"
```

---

## Task 7: Store writes + reads for Library Overview

**Files:**
- Modify: `internal/store/writes.go` (create)
- Test: `internal/store/writes_test.go` (create)
- Modify: `internal/store/reads.go` (create)
- Test: `internal/store/reads_test.go` (create)

**Interfaces:**
- Consumes: `aggregate.LibraryAggregates`, `aggregate.{LabeledCount,DiskBucket,GrowthPoint}`.
- Produces:
  ```go
  func (s *Store) WriteLibraryAggregates(ctx context.Context, a aggregate.LibraryAggregates) error
  // one transaction: DELETE FROM agg_totals; agg_disk; agg_distribution; agg_library_growth
  // (only rows for library dimensions) then re-INSERT.

  type LibraryOverview struct {
      Totals struct {
          ItemsByLibrary []aggregate.LabeledCount `json:"items_by_library"`
          RuntimeSeconds int64 `json:"runtime_seconds"`
          Bytes          int64 `json:"bytes"`
          CountUHD       int64 `json:"count_uhd"`
          CountHDR       int64 `json:"count_hdr"`
          CountDV        int64 `json:"count_dv"`
          Series         int64 `json:"series"`
          Items          int64 `json:"items"`
      } `json:"totals"`
      DiskByResolution []aggregate.DiskBucket   `json:"disk_by_resolution"`
      DiskByCodec      []aggregate.DiskBucket   `json:"disk_by_codec"`
      DiskByContainer  []aggregate.DiskBucket   `json:"disk_by_container"`
      DiskByLibrary    []aggregate.DiskBucket   `json:"disk_by_library"`
      GenresTop        []aggregate.LabeledCount `json:"genres_top"`
      ByDecade         []aggregate.LabeledCount `json:"by_decade"`
      Growth           []aggregate.GrowthPoint  `json:"growth"`
  }
  func (s *Store) ReadLibraryOverview(ctx context.Context) (LibraryOverview, error)

  // IsStale reports staleness per the Global Constraints rule.
  func IsStale(m RefreshMeta, ok bool, interval time.Duration, now time.Time) bool
  ```
- `agg_disk` stores 4 dimensions (`resolution`,`codec`,`container`,`library`); `agg_distribution` stores `genre` and `decade`. `ItemsByLibrary` is persisted as `agg_distribution` dimension `library_items`. Reads reconstruct the DTO and re-sort deterministically (disk by bytes desc, labeled by count desc, decade chronological, growth by month asc).

- [ ] **Step 1: Write the failing test** — `internal/store/writes_test.go`

```go
package store

import (
	"context"
	"testing"

	"github.com/andrii/ephyra/internal/aggregate"
)

func sampleAggregates() aggregate.LibraryAggregates {
	return aggregate.LibraryAggregates{
		Totals: map[string]float64{
			"items.total": 6, "items.Movie": 4, "items.Episode": 2, "items.Series": 3,
			"runtime_sec.total": 33600, "bytes.total": 29_500_000_000,
			"count.uhd": 2, "count.hdr": 3, "count.dv": 1,
		},
		ItemsByLibrary: []aggregate.LabeledCount{{Label: "Movies", Count: 4}, {Label: "Shows", Count: 2}},
		DiskByResolution: []aggregate.DiskBucket{{Bucket: "4K", Bytes: 23_000_000_000, Items: 2}, {Bucket: "1080p", Bytes: 5_300_000_000, Items: 3}, {Bucket: "Unknown", Bytes: 0, Items: 1}},
		DiskByCodec:      []aggregate.DiskBucket{{Bucket: "HEVC", Bytes: 23_000_000_000, Items: 2}},
		DiskByContainer:  []aggregate.DiskBucket{{Bucket: "mkv", Bytes: 25_500_000_000, Items: 4}},
		DiskByLibrary:    []aggregate.DiskBucket{{Bucket: "Movies", Bytes: 27_000_000_000, Items: 4}, {Bucket: "Shows", Bytes: 2_500_000_000, Items: 2}},
		GenresTop:        []aggregate.LabeledCount{{Label: "Drama", Count: 4}, {Label: "Comedy", Count: 1}},
		ByDecade:         []aggregate.LabeledCount{{Label: "1990s", Count: 1}, {Label: "2000s", Count: 1}, {Label: "2010s", Count: 3}, {Label: "2020s", Count: 1}},
		Growth:           []aggregate.GrowthPoint{{Month: "2024-01", AddedItems: 2, AddedBytes: 12e9, CumItems: 2}, {Month: "2024-03", AddedItems: 4, AddedBytes: 17.5e9, CumItems: 6}},
	}
}

func TestWriteThenReadLibraryOverview(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.WriteLibraryAggregates(ctx, sampleAggregates()); err != nil {
		t.Fatal(err)
	}
	ov, err := s.ReadLibraryOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Totals.RuntimeSeconds != 33600 || ov.Totals.Bytes != 29_500_000_000 || ov.Totals.CountHDR != 3 || ov.Totals.Series != 3 || ov.Totals.Items != 6 {
		t.Fatalf("totals: %+v", ov.Totals)
	}
	if len(ov.Totals.ItemsByLibrary) != 2 || ov.Totals.ItemsByLibrary[0].Label != "Movies" {
		t.Fatalf("items by library: %+v", ov.Totals.ItemsByLibrary)
	}
	if ov.DiskByResolution[0].Bucket != "4K" || ov.DiskByResolution[0].Bytes != 23_000_000_000 {
		t.Fatalf("disk res order: %+v", ov.DiskByResolution)
	}
	if ov.ByDecade[0].Label != "1990s" || ov.ByDecade[3].Label != "2020s" {
		t.Fatalf("decade order: %+v", ov.ByDecade)
	}
	if ov.Growth[0].Month != "2024-01" || ov.Growth[1].CumItems != 6 {
		t.Fatalf("growth: %+v", ov.Growth)
	}
	if len(ov.GenresTop) != 2 || ov.GenresTop[0].Label != "Drama" {
		t.Fatalf("genres: %+v", ov.GenresTop)
	}

	// second write fully replaces (no duplicate rows, values updated)
	repl := sampleAggregates()
	repl.Totals["bytes.total"] = 1
	repl.Growth = []aggregate.GrowthPoint{{Month: "2024-05", AddedItems: 1, AddedBytes: 1, CumItems: 1}}
	if err := s.WriteLibraryAggregates(ctx, repl); err != nil {
		t.Fatal(err)
	}
	ov, _ = s.ReadLibraryOverview(ctx)
	if ov.Totals.Bytes != 1 || len(ov.Growth) != 1 || ov.Growth[0].Month != "2024-05" {
		t.Fatalf("replace failed: %+v", ov)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestWriteThenReadLibraryOverview -v`
Expected: build failure — `undefined: WriteLibraryAggregates`.

- [ ] **Step 3: Implement `internal/store/writes.go`**

```go
package store

import (
	"context"
	"database/sql"

	"github.com/andrii/ephyra/internal/aggregate"
)

func (s *Store) WriteLibraryAggregates(ctx context.Context, a aggregate.LibraryAggregates) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range []string{
		`DELETE FROM agg_totals`,
		`DELETE FROM agg_disk`,
		`DELETE FROM agg_distribution WHERE dimension IN ('genre','decade','library_items')`,
		`DELETE FROM agg_library_growth`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	if err := insertTotals(ctx, tx, a.Totals); err != nil {
		return err
	}
	dims := []struct {
		name string
		data []aggregate.DiskBucket
	}{
		{"resolution", a.DiskByResolution},
		{"codec", a.DiskByCodec},
		{"container", a.DiskByContainer},
		{"library", a.DiskByLibrary},
	}
	for _, d := range dims {
		for _, b := range d.data {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_disk (dimension, bucket, bytes, items) VALUES (?,?,?,?)`,
				d.name, b.Bucket, b.Bytes, b.Items); err != nil {
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
	}
	for _, d := range distros {
		for _, lc := range d.data {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO agg_distribution (dimension, bucket, items) VALUES (?,?,?)`,
				d.name, lc.Label, lc.Count); err != nil {
				return err
			}
		}
	}
	for _, g := range a.Growth {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_library_growth (month, added_items, added_bytes, cum_items) VALUES (?,?,?,?)`,
			g.Month, g.AddedItems, g.AddedBytes, g.CumItems); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertTotals(ctx context.Context, tx *sql.Tx, totals map[string]float64) error {
	for k, v := range totals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agg_totals (metric, value) VALUES (?,?)`, k, v); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Implement `internal/store/reads.go`**

```go
package store

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/andrii/ephyra/internal/aggregate"
)

type LibraryOverview struct {
	Totals struct {
		ItemsByLibrary []aggregate.LabeledCount `json:"items_by_library"`
		RuntimeSeconds int64                    `json:"runtime_seconds"`
		Bytes          int64                    `json:"bytes"`
		CountUHD       int64                    `json:"count_uhd"`
		CountHDR       int64                    `json:"count_hdr"`
		CountDV        int64                    `json:"count_dv"`
		Series         int64                    `json:"series"`
		Items          int64                    `json:"items"`
	} `json:"totals"`
	DiskByResolution []aggregate.DiskBucket   `json:"disk_by_resolution"`
	DiskByCodec      []aggregate.DiskBucket   `json:"disk_by_codec"`
	DiskByContainer  []aggregate.DiskBucket   `json:"disk_by_container"`
	DiskByLibrary    []aggregate.DiskBucket   `json:"disk_by_library"`
	GenresTop        []aggregate.LabeledCount `json:"genres_top"`
	ByDecade         []aggregate.LabeledCount `json:"by_decade"`
	Growth           []aggregate.GrowthPoint  `json:"growth"`
}

func (s *Store) ReadLibraryOverview(ctx context.Context) (LibraryOverview, error) {
	var ov LibraryOverview

	totals := map[string]float64{}
	rows, err := s.db.QueryContext(ctx, `SELECT metric, value FROM agg_totals`)
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

	disk := func(dim string) ([]aggregate.DiskBucket, error) {
		r, err := s.db.QueryContext(ctx, `SELECT bucket, bytes, items FROM agg_disk WHERE dimension = ?`, dim)
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
	if ov.DiskByResolution, err = disk("resolution"); err != nil {
		return ov, err
	}
	if ov.DiskByCodec, err = disk("codec"); err != nil {
		return ov, err
	}
	if ov.DiskByContainer, err = disk("container"); err != nil {
		return ov, err
	}
	if ov.DiskByLibrary, err = disk("library"); err != nil {
		return ov, err
	}

	distro := func(dim string, chrono bool) ([]aggregate.LabeledCount, error) {
		r, err := s.db.QueryContext(ctx, `SELECT bucket, items FROM agg_distribution WHERE dimension = ?`, dim)
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
	if ov.GenresTop, err = distro("genre", false); err != nil {
		return ov, err
	}
	if ov.ByDecade, err = distro("decade", true); err != nil {
		return ov, err
	}
	if ov.Totals.ItemsByLibrary, err = distro("library_items", false); err != nil {
		return ov, err
	}

	gr, err := s.db.QueryContext(ctx, `SELECT month, added_items, added_bytes, cum_items FROM agg_library_growth ORDER BY month ASC`)
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
	return ov, gr.Err()
}

// IsStale implements the Global Constraints staleness rule.
func IsStale(m RefreshMeta, ok bool, interval time.Duration, now time.Time) bool {
	if !ok {
		return true
	}
	if !m.OK {
		return true
	}
	return now.Sub(m.LastRunAt) > 2*interval
}

var _ = sql.ErrNoRows
```

- [ ] **Step 5: Write the failing test for `IsStale`** — append to `internal/store/reads_test.go`

```go
package store

import (
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	interval := 30 * time.Minute

	if !IsStale(RefreshMeta{}, false, interval, now) {
		t.Error("no row -> stale")
	}
	if !IsStale(RefreshMeta{OK: false, LastRunAt: now}, true, interval, now) {
		t.Error("last run failed -> stale")
	}
	if IsStale(RefreshMeta{OK: true, LastRunAt: now.Add(-20 * time.Minute)}, true, interval, now) {
		t.Error("fresh -> not stale")
	}
	if !IsStale(RefreshMeta{OK: true, LastRunAt: now.Add(-61 * time.Minute)}, true, interval, now) {
		t.Error("older than 2x interval -> stale")
	}
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/store/ -v`
Expected: PASS (all tests: migrations, refresh meta, write/read overview, IsStale).

- [ ] **Step 7: Commit**

```bash
git add internal/store
git commit -m "feat(store): library aggregate writes/reads + staleness rule"
```

---

## Task 8: Scheduler (library job)

**Files:**
- Create: `internal/scheduler/scheduler.go`
- Test: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `*store.Store`, `source.Source` (+ optional `source.MTimer`), `config.Config` (`RefreshLibrary`, `TZ` via `time.Local`), `*slog.Logger`.
- Produces:
  ```go
  type Scheduler struct { /* ... */ }
  func New(st *store.Store, src source.Source, cfg config.Config, log *slog.Logger) *Scheduler
  func (s *Scheduler) Run(ctx context.Context)     // blocks: runs startup jobs, then tickers, until ctx done
  func (s *Scheduler) Trigger(job string)          // non-blocking; "library" | "watch" | "all"
  func (s *Scheduler) RunLibraryOnce(ctx context.Context) error // exported for tests / POST /api/refresh path
  ```
- `RunLibraryOnce`:
  1. If `src` implements `MTimer`: `mt, _ := src.SourceMTime("library")`. Read `prev, ok := st.GetRefreshMeta("library")`. If `ok && !prev.SourceMTime.IsZero() && mt.Equal(prev.SourceMTime)` → write `RefreshMeta{Job:"library", LastRunAt: now, SourceMTime: mt, OK:true, Skipped:true, DurationMS:<elapsed>}` and return nil (skip).
  2. Else: `snap, err := src.LibraryFacts(ctx)`; on error write `RefreshMeta{..., OK:false, Error: err.Error()}` and return err.
  3. `agg := aggregate.Library(snap, time.Local)`; `st.WriteLibraryAggregates(ctx, agg)`.
  4. Write `RefreshMeta{Job:"library", LastRunAt: now, SourceMTime: mt, OK:true, Skipped:false, DurationMS:<elapsed>}`.
  5. A per-job `sync.Mutex` guards the whole method; `Trigger` and the ticker both call it.

- [ ] **Step 1: Write the failing test** — `internal/scheduler/scheduler_test.go`

```go
package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/source"
	"github.com/andrii/ephyra/internal/store"
)

type fakeSource struct {
	mtime    atomic.Pointer[time.Time]
	calls    atomic.Int32
	failNext atomic.Bool
	snap     source.LibrarySnapshot
}

func (f *fakeSource) Kind() string { return "fake" }
func (f *fakeSource) PlaybackEvents(context.Context, time.Time) ([]source.PlaybackEvent, error) {
	return nil, source.ErrNotImplemented
}
func (f *fakeSource) LibraryFacts(context.Context) (source.LibrarySnapshot, error) {
	f.calls.Add(1)
	if f.failNext.Swap(false) {
		return source.LibrarySnapshot{}, errors.New("boom")
	}
	return f.snap, nil
}
func (f *fakeSource) SourceMTime(job string) (time.Time, error) {
	if p := f.mtime.Load(); p != nil {
		return *p, nil
	}
	return time.Time{}, nil
}

func newSched(t *testing.T) (*Scheduler, *fakeSource, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := &fakeSource{snap: source.LibrarySnapshot{Items: []source.LibraryItem{
		{Name: "M", Type: "movie", SizeBytes: 1, RuntimeSec: 1, Year: 2020, Library: "Movies", Container: "mkv", Width: 1920, HasVideo: true},
	}}}
	set := func(ts time.Time) { fs.mtime.Store(&ts) }
	set(time.Unix(1000, 0))
	sc := New(st, fs, config.Config{RefreshLibrary: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sc, fs, st
}

func TestRunLibraryOnce_FullRun(t *testing.T) {
	sc, fs, st := newSched(t)
	if err := sc.RunLibraryOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.calls.Load() != 1 {
		t.Fatalf("LibraryFacts calls = %d", fs.calls.Load())
	}
	m, ok, _ := st.GetRefreshMeta(context.Background(), "library")
	if !ok || !m.OK || m.Skipped {
		t.Fatalf("refresh_meta: %+v ok=%v", m, ok)
	}
	ov, _ := st.ReadLibraryOverview(context.Background())
	if ov.Totals.Items != 1 {
		t.Fatalf("overview not written: %+v", ov.Totals)
	}
}

func TestRunLibraryOnce_SkipWhenMtimeUnchanged(t *testing.T) {
	sc, fs, st := newSched(t)
	ctx := context.Background()
	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := fs.calls.Load()

	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if fs.calls.Load() != callsAfterFirst {
		t.Fatalf("expected skip (no LibraryFacts call), calls went %d -> %d", callsAfterFirst, fs.calls.Load())
	}
	m, _, _ := st.GetRefreshMeta(ctx, "library")
	if !m.Skipped || !m.OK {
		t.Fatalf("expected ok+skipped meta, got %+v", m)
	}
}

func TestRunLibraryOnce_ChangedMtimeReRuns(t *testing.T) {
	sc, fs, st := newSched(t)
	ctx := context.Background()
	_ = sc.RunLibraryOnce(ctx)
	c1 := fs.calls.Load()

	newMt := time.Unix(2000, 0)
	fs.mtime.Store(&newMt)
	if err := sc.RunLibraryOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if fs.calls.Load() != c1+1 {
		t.Fatalf("expected re-run, calls %d -> %d", c1, fs.calls.Load())
	}
	m, _, _ := st.GetRefreshMeta(ctx, "library")
	if m.Skipped {
		t.Fatal("should not be skipped after mtime change")
	}
}

func TestRunLibraryOnce_ErrorRecorded(t *testing.T) {
	sc, fs, st := newSched(t)
	fs.failNext.Store(true)
	err := sc.RunLibraryOnce(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	m, ok, _ := st.GetRefreshMeta(context.Background(), "library")
	if !ok || m.OK || m.Error == "" {
		t.Fatalf("error not recorded: %+v", m)
	}
}

func TestRun_StartupRunsLibraryOnce(t *testing.T) {
	sc, fs, _ := newSched(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sc.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for fs.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("startup did not run library job")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/scheduler/ -v`
Expected: build failure — `undefined: New`, `undefined: Scheduler`.

- [ ] **Step 3: Implement `internal/scheduler/scheduler.go`**

```go
// Package scheduler runs Ephyra's periodic refresh jobs.
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/andrii/ephyra/internal/aggregate"
	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/source"
	"github.com/andrii/ephyra/internal/store"
)

type Scheduler struct {
	st  *store.Store
	src source.Source
	cfg config.Config
	log *slog.Logger

	libMu   sync.Mutex
	trigger chan string
}

func New(st *store.Store, src source.Source, cfg config.Config, log *slog.Logger) *Scheduler {
	return &Scheduler{st: st, src: src, cfg: cfg, log: log, trigger: make(chan string, 8)}
}

func (s *Scheduler) Trigger(job string) {
	select {
	case s.trigger <- job:
	default: // channel full: a refresh is already queued, drop
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	if err := s.RunLibraryOnce(ctx); err != nil {
		s.log.Warn("startup library refresh failed", "err", err)
	}
	lib := time.NewTicker(s.cfg.RefreshLibrary)
	defer lib.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-lib.C:
			if err := s.RunLibraryOnce(ctx); err != nil {
				s.log.Warn("library refresh failed", "err", err)
			}
		case job := <-s.trigger:
			if job == "library" || job == "all" {
				if err := s.RunLibraryOnce(ctx); err != nil {
					s.log.Warn("triggered library refresh failed", "err", err)
				}
			}
			// "watch" handled in Plan 2
		}
	}
}

func (s *Scheduler) RunLibraryOnce(ctx context.Context) error {
	s.libMu.Lock()
	defer s.libMu.Unlock()
	start := time.Now()

	var mt time.Time
	if mtimer, ok := s.src.(source.MTimer); ok {
		mt, _ = mtimer.SourceMTime("library")
	}
	prev, hadPrev, _ := s.st.GetRefreshMeta(ctx, "library")
	if hadPrev && !prev.SourceMTime.IsZero() && !mt.IsZero() && mt.Equal(prev.SourceMTime) {
		s.log.Info("library refresh skipped (mtime unchanged)", "mtime", mt)
		return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
			Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
			DurationMS: time.Since(start).Milliseconds(), OK: true, Skipped: true,
		})
	}

	snap, err := s.src.LibraryFacts(ctx)
	if err != nil {
		_ = s.st.SetRefreshMeta(ctx, store.RefreshMeta{
			Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
			DurationMS: time.Since(start).Milliseconds(), OK: false, Error: err.Error(),
		})
		return err
	}
	agg := aggregate.Library(snap, time.Local)
	if err := s.st.WriteLibraryAggregates(ctx, agg); err != nil {
		_ = s.st.SetRefreshMeta(ctx, store.RefreshMeta{
			Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
			DurationMS: time.Since(start).Milliseconds(), OK: false, Error: err.Error(),
		})
		return err
	}
	s.log.Info("library refresh ok",
		"items", int64(agg.Totals["items.total"]), "dur_ms", time.Since(start).Milliseconds())
	return s.st.SetRefreshMeta(ctx, store.RefreshMeta{
		Job: "library", LastRunAt: time.Now().UTC(), SourceMTime: mt,
		DurationMS: time.Since(start).Milliseconds(), OK: true, Skipped: false,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/scheduler/ -v`
Expected: PASS (all six tests).

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler
git commit -m "feat(scheduler): library job with mtime-skip, tickers, trigger"
```

---

## Task 9: HTTP API — envelope, health, library endpoint, minimal refresh

**Files:**
- Create: `internal/api/api.go`
- Test: `internal/api/api_test.go`
- Create: `internal/api/health.go`
- Create: `internal/api/library.go`
- Test: `internal/api/library_test.go`
- Create: `internal/api/refresh.go`

**Interfaces:**
- Consumes: `*store.Store`, `config.Config`, `*slog.Logger`, and a `Triggerer` (satisfied by `*scheduler.Scheduler`):
  ```go
  type Triggerer interface{ Trigger(job string) }
  ```
- Produces:
  ```go
  type Server struct { /* ... */ }
  type Deps struct {
      Store   *store.Store
      Cfg     config.Config
      Log     *slog.Logger
      Trigger Triggerer
      Static  http.Handler // from Task 10; pass nil in tests
      Now     func() time.Time // injectable clock; defaults to time.Now
  }
  func New(d Deps) *Server
  func (s *Server) Handler() http.Handler   // *http.ServeMux with all routes

  // envelope helpers (unexported except for tests in-package)
  func writeJSON(w http.ResponseWriter, status int, data any, meta Meta)
  func writeError(w http.ResponseWriter, status int, code, msg string)
  type Meta struct { GeneratedAt string `json:"generated_at"`; Stale bool `json:"stale"` }
  ```
- Routes registered on the mux:
  - `GET /healthz` → `health.go`
  - `GET /api/library/overview` → `library.go`
  - `POST /api/refresh` → `refresh.go`
  - `/` (catch-all) → `Static` handler if non-nil, else 404 JSON.
- `GET /api/library/overview`: `m, ok := store.GetRefreshMeta("library")`. If `!ok` → `503` `{"error":{"code":"not_ready","message":"first refresh has not completed"}}`. Else `ov := store.ReadLibraryOverview()`, `stale := store.IsStale(m, ok, cfg.RefreshLibrary, now)`, `writeJSON(200, ov, Meta{now.UTC().RFC3339, stale})`.
- `POST /api/refresh`: parse `?job=` (default `all`; must be `library|watch|all`), call `Trigger(job)`, respond `202` with envelope `data = {"job": job, "queued": true}`.

- [ ] **Step 1: Write the failing test** — `internal/api/api_test.go`

```go
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/store"
)

type fakeTrigger struct{ got []string }

func (f *fakeTrigger) Trigger(job string) { f.got = append(f.got, job) }

func newTestServer(t *testing.T, now time.Time) (*Server, *store.Store, *fakeTrigger) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/s.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ft := &fakeTrigger{}
	s := New(Deps{
		Store: st, Cfg: config.Config{RefreshLibrary: 30 * time.Minute},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Trigger: ft,
		Now: func() time.Time { return now },
	})
	return s, st, ft
}

func TestHealthz(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestLibraryOverview_NotReady(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (%s)", rr.Code, rr.Body)
	}
	var env struct {
		Error struct{ Code string } `json:"error"`
	}
	json.Unmarshal(rr.Body.Bytes(), &env)
	if env.Error.Code != "not_ready" {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

func TestLibraryOverview_OKAndStaleFlag(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()

	// seed aggregates + a fresh refresh_meta
	if err := st.WriteLibraryAggregates(ctx, sampleAgg()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-10 * time.Minute), OK: true}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data struct {
			Totals struct {
				Items int64 `json:"items"`
			} `json:"totals"`
		} `json:"data"`
		Meta Meta `json:"meta"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Totals.Items != 6 || env.Meta.Stale || env.Meta.GeneratedAt == "" {
		t.Fatalf("bad envelope: %+v", env)
	}

	// make it stale
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-2 * time.Hour), OK: true})
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	json.Unmarshal(rr.Body.Bytes(), &env)
	if !env.Meta.Stale {
		t.Fatal("expected stale=true")
	}
}

func TestRefreshEndpoint(t *testing.T) {
	s, _, ft := newTestServer(t, time.Now())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/refresh?job=library", nil))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rr.Code)
	}
	if len(ft.got) != 1 || ft.got[0] != "library" {
		t.Fatalf("trigger not called: %v", ft.got)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/refresh?job=bogus", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("bad job should be 400, got %d", rr.Code)
	}
}
```

Add a small helper file `internal/api/testhelpers_test.go`:

```go
package api

import "github.com/andrii/ephyra/internal/aggregate"

func sampleAgg() aggregate.LibraryAggregates {
	return aggregate.LibraryAggregates{
		Totals: map[string]float64{"items.total": 6, "items.Series": 3, "runtime_sec.total": 100, "bytes.total": 200},
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -v`
Expected: build failure — `undefined: New`, `undefined: Server`.

- [ ] **Step 3: Implement `internal/api/api.go`**

```go
// Package api serves Ephyra's JSON API and the embedded SPA.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/store"
)

type Triggerer interface{ Trigger(job string) }

type Deps struct {
	Store   *store.Store
	Cfg     config.Config
	Log     *slog.Logger
	Trigger Triggerer
	Static  http.Handler
	Now     func() time.Time
}

type Server struct {
	st      *store.Store
	cfg     config.Config
	log     *slog.Logger
	trigger Triggerer
	static  http.Handler
	now     func() time.Time
}

func New(d Deps) *Server {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Server{st: d.Store, cfg: d.Cfg, log: d.Log, trigger: d.Trigger, static: d.Static, now: now}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/library/overview", s.handleLibraryOverview)
	mux.HandleFunc("POST /api/refresh", s.handleRefresh)
	mux.HandleFunc("/", s.handleRoot)
	return withLogging(s.log, mux)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.static != nil && r.Method == http.MethodGet {
		s.static.ServeHTTP(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "no such route")
}

type Meta struct {
	GeneratedAt string `json:"generated_at"`
	Stale       bool   `json:"stale"`
}

func (s *Server) meta(stale bool) Meta {
	return Meta{GeneratedAt: s.now().UTC().Format(time.RFC3339), Stale: stale}
}

func writeJSON(w http.ResponseWriter, status int, data any, meta Meta) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": meta})
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}

func withLogging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if r.URL.Path != "/healthz" {
			log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "dur_ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
```

- [ ] **Step 4: Implement `internal/api/health.go`**

```go
package api

import (
	"net/http"
)

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DB().PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "unhealthy", err.Error())
		return
	}
	if _, err := s.st.DB().ExecContext(r.Context(), "SELECT 1"); err != nil {
		writeError(w, http.StatusServiceUnavailable, "unhealthy", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
```

- [ ] **Step 5: Implement `internal/api/library.go`**

```go
package api

import (
	"net/http"

	"github.com/andrii/ephyra/internal/store"
)

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
	ov, err := s.st.ReadLibraryOverview(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	stale := store.IsStale(m, ok, s.cfg.RefreshLibrary, s.now())
	writeJSON(w, http.StatusOK, ov, s.meta(stale))
}
```

- [ ] **Step 6: Implement `internal/api/refresh.go`**

```go
package api

import "net/http"

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	job := r.URL.Query().Get("job")
	if job == "" {
		job = "all"
	}
	switch job {
	case "library", "watch", "all":
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "job must be library|watch|all")
		return
	}
	if s.trigger != nil {
		s.trigger.Trigger(job)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job, "queued": true}, s.meta(false))
}
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/api/ -v`
Expected: PASS (all tests in `api_test.go`).

- [ ] **Step 8: Commit**

```bash
git add internal/api
git commit -m "feat(api): envelope, /healthz, /api/library/overview, /api/refresh"
```

---

## Task 10: Static embedding + main wiring + graceful shutdown

**Files:**
- Create: `web/dist/.gitkeep` (empty)
- Create: `web/embed.go`
- Create: `internal/api/static.go`
- Test: `internal/api/static_test.go`
- Create: `cmd/ephyra/main.go`

**Interfaces:**
- Produces:
  ```go
  // package web
  //go:embed all:dist
  var distFS embed.FS
  func DistFS() (fs.FS, error) // returns fs.Sub(distFS, "dist")

  // package api
  func NewStaticHandler(dist fs.FS) http.Handler
  // serves files from dist; for any path that does not resolve to a file and
  // is not under /api or /healthz, serves dist/index.html (SPA fallback).
  // If dist has no index.html (frontend not built), returns 503 text.
  ```
- `cmd/ephyra/main.go`: `slog` JSON handler at `cfg.LogLevel` → `config.Load(os.Getenv)` → `os.MkdirAll(filepath.Dir(cfg.StorePath))` + `os.MkdirAll(cfg.WorkDir)` → `store.Open` → build source: `file.New(cfg, log)` when `cfg.Source != "api"` and the library DB is reachable (`file.New` + a `SourceMTime("library")` probe); if `cfg.Source == "api"` → `log.Error("api source not implemented in this build"); os.Exit(1)` → `scheduler.New` → `go sched.Run(ctx)` → `web.DistFS()` → `api.New(Deps{... Static: api.NewStaticHandler(dist)})` → `http.Server{Addr: cfg.ListenAddr, Handler: srv.Handler()}` → `ListenAndServe` in a goroutine; on `SIGINT`/`SIGTERM` cancel ctx and `server.Shutdown(5s)`.

- [ ] **Step 1: Create `web/dist/.gitkeep`** (empty file) and **`web/embed.go`**

```go
// Package web embeds the built frontend.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the built SPA file system rooted at the dist directory.
func DistFS() (fs.FS, error) { return fs.Sub(distFS, "dist") }
```

- [ ] **Step 2: Write the failing test** — `internal/api/static_test.go`

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestStaticHandler_ServesAssetsAndSPAFallback(t *testing.T) {
	dist := fstest.MapFS{
		"index.html":       {Data: []byte("<!doctype html><title>Ephyra</title>")},
		"assets/app.js":    {Data: []byte("console.log(1)")},
	}
	h := NewStaticHandler(dist)

	// real asset
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if rr.Code != 200 || rr.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %q", rr.Code, rr.Body.String())
	}

	// unknown client route -> index.html
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library", nil))
	if rr.Code != 200 || rr.Body.String() == "" || rr.Body.String()[0] != '<' {
		t.Fatalf("spa fallback: %d %q", rr.Code, rr.Body.String())
	}
}

func TestStaticHandler_NoBuild(t *testing.T) {
	h := NewStaticHandler(fstest.MapFS{".gitkeep": {Data: []byte{}}})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when unbuilt, got %d", rr.Code)
	}
}
```

- [ ] **Step 3: Implement `internal/api/static.go`**

```go
package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

func NewStaticHandler(dist fs.FS) http.Handler {
	_, hasIndex := statFile(dist, "index.html")
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hasIndex {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("frontend not built (run `make web`)"))
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if _, ok := statFile(dist, p); ok {
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA fallback
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/index.html"
		fileServer.ServeHTTP(w, r2)
	})
}

func statFile(fsys fs.FS, name string) (fs.FileInfo, bool) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return nil, false
	}
	return fi, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/ -run TestStaticHandler -v`
Expected: PASS.

- [ ] **Step 5: Implement `cmd/ephyra/main.go`**

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andrii/ephyra/internal/api"
	"github.com/andrii/ephyra/internal/buildinfo"
	"github.com/andrii/ephyra/internal/config"
	"github.com/andrii/ephyra/internal/scheduler"
	"github.com/andrii/ephyra/internal/source"
	"github.com/andrii/ephyra/internal/source/file"
	"github.com/andrii/ephyra/internal/store"
	"github.com/andrii/ephyra/web"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		// logger not configured yet; use default
		slog.Error("config", "err", err)
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	log.Info("starting", "version", buildinfo.Version(), "source", cfg.Source)

	if err := os.MkdirAll(filepath.Dir(cfg.StorePath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.StorePath)
	if err != nil {
		return err
	}
	defer st.Close()

	var src source.Source
	switch cfg.Source {
	case "api":
		return errors.New("SOURCE=api is not implemented in this build; use the file source")
	default:
		fsrc := file.New(cfg, log)
		if _, err := fsrc.SourceMTime("library"); err != nil {
			return errors.New("cannot read Jellyfin library DB at " + cfg.JellyfinDataDir + "/data/library.db: " + err.Error())
		}
		src = fsrc
	}

	sched := scheduler.New(st, src, cfg, log)
	go sched.Run(ctx)

	dist, err := web.DistFS()
	if err != nil {
		return err
	}
	srv := api.New(api.Deps{
		Store: st, Cfg: cfg, Log: log, Trigger: sched,
		Static: api.NewStaticHandler(dist),
	})
	httpServer := &http.Server{Addr: cfg.ListenAddr, Handler: srv.Handler()}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdownCtx)
}
```

- [ ] **Step 6: Verify the binary builds and boots against the fixture**

```bash
CGO_ENABLED=0 go build ./...
# build a throwaway data dir from the fixture:
mkdir -p /tmp/ephyra-demo/data
go run ./internal/testsupport/cmd 2>/dev/null || true   # (skip; use sqlite3 if available)
```

Manual smoke (optional, needs `sqlite3`):

```bash
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/library.db < testdata/library.fixture.sql
JELLYFIN_URL=http://x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf \
  STORE_PATH=/tmp/ephyra/ephyra.db WORK_DIR=/tmp/ephyra/work LISTEN_ADDR=:8899 \
  go run ./cmd/ephyra &
sleep 1
curl -s localhost:8899/healthz
curl -s localhost:8899/api/library/overview | head -c 400
kill %1
```

Expected: `/healthz` → `{"status":"ok"}`; `/api/library/overview` → envelope with `data.totals.items == 6` after the startup refresh.

- [ ] **Step 7: Commit**

```bash
git add web/embed.go web/dist/.gitkeep internal/api/static.go internal/api/static_test.go cmd/ephyra
git commit -m "feat: embed SPA + SPA fallback + main wiring + graceful shutdown"
```

---

## Task 11: Frontend — scaffold, shell, Library Overview page

> **Run the `frontend-design` skill before this task** to set the visual direction
> (palette, typeface, density, motion, chart aesthetic), and the `dataviz` skill
> for chart encodings/theme. The code below is the *structure*; the design pass
> fills in the token values, the typeface, and the chart theme.

**Stack:** React 19 + Vite (latest) + TypeScript · Tailwind CSS v4 (`@tailwindcss/vite`) ·
shadcn/ui on Radix · TanStack Router (code-based) + TanStack Query v5 ·
Apache ECharts (tree-shaken, custom theme) · Biome · Vitest 3 + Testing Library.

**Files:** (all under `web/`)
- Create: `package.json`, `tsconfig.json`, `tsconfig.node.json`, `vite.config.ts`, `biome.json`, `components.json`, `index.html`, `.gitignore`
- Create: `src/index.css`, `src/lib/utils.ts`, `src/lib/format.ts`, `src/test/setup.ts`
- Create: `src/api/types.ts`, `src/api/client.ts`, `src/api/queries.ts`
- Create: `src/charts/echarts.ts`, `src/charts/theme.ts`, `src/charts/EChart.tsx`
- Create: `src/components/ui/*` (via shadcn CLI), `src/components/AppShell.tsx`, `src/components/StatCard.tsx`, `src/components/Section.tsx`, `src/components/StaleBanner.tsx`
- Create: `src/routes/__root.tsx`, `src/routes/library.tsx`, `src/routes/watch.tsx`, `src/routes/now.tsx`, `src/routes/cleanup.tsx`, `src/router.tsx`, `src/main.tsx`
- Test: `src/routes/library.test.tsx`
- Modify: `.github/workflows/ci.yml` (add `web` job)

**Interfaces:**
- Consumes: `GET /api/library/overview` (envelope from Task 9).
- Produces: a Vite build to `web/dist/` (embedded by Task 10). Scripts: `npm run build`, `npm run test`, `npm run lint`.

- [ ] **Step 1: Scaffold + install**

```bash
cd web
npm create vite@latest . -- --template react-ts   # accept overwrite into current dir
npm pkg delete scripts.lint
npm install @tanstack/react-query @tanstack/react-router echarts class-variance-authority clsx tailwind-merge lucide-react
npm install -D tailwindcss @tailwindcss/vite @biomejs/biome vitest @vitest/ui jsdom \
  @testing-library/react @testing-library/jest-dom @testing-library/user-event
```

Delete Vite's default `src/App.tsx`, `src/App.css`, `src/index.css`, `src/assets`, `public/vite.svg` — this task replaces them.

- [ ] **Step 2: `web/package.json` scripts + `type`**

```jsonc
{
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc -b && vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "lint": "biome ci ."
  }
}
```

Pin (approximate current majors; `npm install` resolves exact): `react` ^19, `react-dom` ^19,
`vite` ^7, `@tailwindcss/vite` ^4, `tailwindcss` ^4, `@tanstack/react-router` ^1,
`@tanstack/react-query` ^5, `echarts` ^5, `@biomejs/biome` ^2, `vitest` ^3.

- [ ] **Step 3: `web/vite.config.ts`**

```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import path from "node:path";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(__dirname, "src") } },
  build: { outDir: "dist", emptyOutDir: true },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://localhost:8080",
      "/healthz": "http://localhost:8080",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    css: true,
  },
});
```

- [ ] **Step 4: `web/tsconfig.json`** (+ keep Vite's `tsconfig.node.json`)

```jsonc
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noEmit": true,
    "skipLibCheck": true,
    "baseUrl": ".",
    "paths": { "@/*": ["src/*"] },
    "types": ["vitest/globals", "@testing-library/jest-dom"]
  },
  "include": ["src"]
}
```

- [ ] **Step 5: `web/biome.json`**

```json
{
  "$schema": "https://biomejs.dev/schemas/2.0.0/schema.json",
  "vcs": { "enabled": true, "clientKind": "git", "useIgnoreFile": true },
  "files": { "includes": ["src/**"] },
  "linter": { "enabled": true, "rules": { "recommended": true, "a11y": { "recommended": true } } },
  "formatter": { "enabled": true, "indentStyle": "space", "indentWidth": 2, "lineWidth": 100 },
  "assist": { "actions": { "source": { "organizeImports": "on" } } }
}
```

- [ ] **Step 6: `web/src/index.css`** — Tailwind v4 entry + design tokens

> The `frontend-design` pass sets the actual token values, the typeface `@font-face`,
> and dark-mode overrides. This is the scaffold with placeholder-neutral values.

```css
@import "tailwindcss";

@theme {
  --font-sans: "InterVariable", ui-sans-serif, system-ui, sans-serif;
  --color-bg: oklch(0.99 0 0);
  --color-surface: oklch(1 0 0);
  --color-border: oklch(0.92 0.004 260);
  --color-fg: oklch(0.20 0.02 260);
  --color-muted: oklch(0.55 0.02 260);
  --color-accent: oklch(0.62 0.19 300);
  --color-accent-fg: oklch(0.99 0 0);
  --radius: 0.75rem;
}

@media (prefers-color-scheme: dark) {
  @theme {
    --color-bg: oklch(0.17 0.01 260);
    --color-surface: oklch(0.21 0.012 260);
    --color-border: oklch(0.30 0.012 260);
    --color-fg: oklch(0.95 0.01 260);
    --color-muted: oklch(0.68 0.02 260);
  }
}

html, body, #root { height: 100%; }
body { background: var(--color-bg); color: var(--color-fg); font-family: var(--font-sans); }
```

- [ ] **Step 7: `web/src/lib/utils.ts` and `web/src/lib/format.ts`**

```ts
// utils.ts
import { type ClassValue, clsx } from "clsx";
import { twMerge } from "tailwind-merge";
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}
```

```ts
// format.ts
const GB = 1024 ** 3;
export function fmtBytes(n: number): string {
  if (n >= 1024 ** 4) return `${(n / 1024 ** 4).toFixed(1)} TB`;
  if (n >= GB) return `${(n / GB).toFixed(0)} GB`;
  return `${(n / 1024 ** 2).toFixed(0)} MB`;
}
export function fmtDuration(sec: number): string {
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  return d > 0 ? `${d}d ${h}h` : `${h}h`;
}
```

- [ ] **Step 8: shadcn/ui**

```bash
cd web
npx shadcn@latest init      # style: default; base color: neutral; CSS vars: yes; alias @/components
npx shadcn@latest add card skeleton alert table button sheet tooltip sonner separator badge scroll-area
```

Commit `components.json` and the generated `src/components/ui/*`. If `init` rewrites
`src/index.css`, re-apply the `@theme` block from Step 6 on top of what it generates.

- [ ] **Step 9: `web/src/api/types.ts`**

```ts
export interface Meta { generated_at: string; stale: boolean }
export interface Envelope<T> { data: T; meta: Meta }

export interface LabeledCount { label: string; count: number }
export interface DiskBucket { bucket: string; bytes: number; items: number }
export interface GrowthPoint { month: string; added_items: number; added_bytes: number; cum_items: number }

export interface LibraryOverview {
  totals: {
    items_by_library: LabeledCount[];
    runtime_seconds: number;
    bytes: number;
    count_uhd: number;
    count_hdr: number;
    count_dv: number;
    series: number;
    items: number;
  };
  disk_by_resolution: DiskBucket[];
  disk_by_codec: DiskBucket[];
  disk_by_container: DiskBucket[];
  disk_by_library: DiskBucket[];
  genres_top: LabeledCount[];
  by_decade: LabeledCount[];
  growth: GrowthPoint[];
}
```

- [ ] **Step 10: `web/src/api/client.ts`**

```ts
import type { Envelope } from "./types";

export class ApiRequestError extends Error {
  code: string;
  status: number;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

export async function fetchEnvelope<T>(path: string): Promise<Envelope<T>> {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new ApiRequestError(
      res.status,
      body?.error?.code ?? "http_error",
      body?.error?.message ?? `request failed (${res.status})`,
    );
  }
  return body as Envelope<T>;
}
```

- [ ] **Step 11: `web/src/api/queries.ts`**

```ts
import { queryOptions } from "@tanstack/react-query";
import { fetchEnvelope } from "./client";
import type { LibraryOverview } from "./types";

export const libraryOverviewQuery = () =>
  queryOptions({
    queryKey: ["library-overview"],
    queryFn: () => fetchEnvelope<LibraryOverview>("/api/library/overview"),
    staleTime: 30 * 60 * 1000,
  });
```

- [ ] **Step 12: ECharts wrapper**

`web/src/charts/theme.ts`:
```ts
// Colors read from CSS custom properties at init time so the ECharts theme
// tracks the Tailwind @theme tokens (and dark mode).
function cssVar(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

export function ephyraEChartsTheme() {
  const fg = cssVar("--color-fg", "#222");
  const muted = cssVar("--color-muted", "#888");
  const border = cssVar("--color-border", "#e5e5e5");
  const accent = cssVar("--color-accent", "#8b5cf6");
  return {
    color: [accent, "#22d3ee", "#f59e0b", "#34d399", "#f472b6", "#60a5fa"],
    textStyle: { fontFamily: "var(--font-sans)", color: fg },
    grid: { left: 8, right: 16, top: 24, bottom: 8, containLabel: true },
    categoryAxis: {
      axisLine: { lineStyle: { color: border } },
      axisTick: { show: false },
      axisLabel: { color: muted },
      splitLine: { show: false },
    },
    valueAxis: {
      axisLine: { show: false },
      axisTick: { show: false },
      axisLabel: { color: muted },
      splitLine: { lineStyle: { color: border } },
    },
    tooltip: {
      backgroundColor: cssVar("--color-surface", "#fff"),
      borderColor: border,
      textStyle: { color: fg },
    },
  };
}
```

`web/src/charts/echarts.ts`:
```ts
import * as echarts from "echarts/core";
import { BarChart, LineChart, HeatmapChart } from "echarts/charts";
import {
  GridComponent,
  TooltipComponent,
  LegendComponent,
  VisualMapComponent,
  DatasetComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import { ephyraEChartsTheme } from "./theme";

echarts.use([
  BarChart, LineChart, HeatmapChart,
  GridComponent, TooltipComponent, LegendComponent, VisualMapComponent, DatasetComponent,
  CanvasRenderer,
]);

let registered = false;
export function ensureTheme() {
  if (!registered) {
    echarts.registerTheme("ephyra", ephyraEChartsTheme());
    registered = true;
  }
}
export { echarts };
```

`web/src/charts/EChart.tsx`:
```tsx
import { useEffect, useRef } from "react";
import type { EChartsCoreOption } from "echarts/core";
import { echarts, ensureTheme } from "./echarts";

export function EChart({ option, height = 260, className }: {
  option: EChartsCoreOption;
  height?: number;
  className?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const chart = useRef<echarts.ECharts | null>(null);

  useEffect(() => {
    if (!ref.current) return;
    ensureTheme();
    chart.current = echarts.init(ref.current, "ephyra");
    const ro = new ResizeObserver(() => chart.current?.resize());
    ro.observe(ref.current);
    return () => {
      ro.disconnect();
      chart.current?.dispose();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    chart.current?.setOption(option, true);
  }, [option]);

  return <div ref={ref} style={{ height }} className={className} role="img" />;
}
```

- [ ] **Step 13: Shell + small components**

`web/src/components/AppShell.tsx`:
```tsx
import { Link, useRouterState } from "@tanstack/react-router";
import { Library, PlayCircle, BarChart3, Trash2 } from "lucide-react";
import { cn } from "@/lib/utils";

const nav = [
  { to: "/library", label: "Library", icon: Library },
  { to: "/watch", label: "Watch Stats", icon: BarChart3 },
  { to: "/now", label: "Now Playing", icon: PlayCircle },
  { to: "/cleanup", label: "Cleanup", icon: Trash2 },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const path = useRouterState({ select: (s) => s.location.pathname });
  return (
    <div className="grid min-h-full grid-cols-[220px_1fr] max-md:grid-cols-1">
      <aside className="border-r border-[--color-border] p-4 max-md:hidden">
        <div className="mb-6 px-2 text-lg font-bold tracking-tight">Ephyra</div>
        <nav className="flex flex-col gap-1">
          {nav.map(({ to, label, icon: Icon }) => (
            <Link
              key={to}
              to={to}
              className={cn(
                "flex items-center gap-2 rounded-[--radius] px-3 py-2 text-sm text-[--color-muted] hover:bg-[--color-surface]",
                path.startsWith(to) && "bg-[--color-surface] font-medium text-[--color-fg]",
              )}
            >
              <Icon size={16} />
              {label}
            </Link>
          ))}
        </nav>
      </aside>
      <main className="p-6 max-md:p-4">{children}</main>
    </div>
  );
}
```

`web/src/components/StatCard.tsx`:
```tsx
import { Card, CardContent } from "@/components/ui/card";

export function StatCard({ label, value }: { label: string; value: string }) {
  return (
    <Card>
      <CardContent className="p-4">
        <div className="text-xs font-semibold uppercase tracking-wide text-[--color-muted]" aria-label={label}>
          {label}
        </div>
        <div className="mt-1 text-2xl font-bold tabular-nums">{value}</div>
      </CardContent>
    </Card>
  );
}
```

`web/src/components/Section.tsx`:
```tsx
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader className="pb-2">
        <CardTitle className="text-sm font-semibold">{title}</CardTitle>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}
```

`web/src/components/StaleBanner.tsx`:
```tsx
import { Alert, AlertDescription } from "@/components/ui/alert";

export function StaleBanner() {
  return (
    <Alert role="status" className="mb-4">
      <AlertDescription>Data is catching up — showing the last successful refresh.</AlertDescription>
    </Alert>
  );
}
```

- [ ] **Step 14: Routes + router + entry**

`web/src/routes/__root.tsx`:
```tsx
import { createRootRoute, Outlet } from "@tanstack/react-router";
import { AppShell } from "@/components/AppShell";

export const Route = createRootRoute({
  component: () => (
    <AppShell>
      <Outlet />
    </AppShell>
  ),
});
```

`web/src/routes/watch.tsx`, `now.tsx`, `cleanup.tsx` — placeholder pattern (repeat per file, changing `path` and title):
```tsx
import { createRoute } from "@tanstack/react-router";
import { Route as rootRoute } from "./__root";

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/watch",
  component: () => (
    <div>
      <h2 className="text-xl font-bold">Watch Stats</h2>
      <p className="mt-2 text-[--color-muted]">Available in a later build.</p>
    </div>
  ),
});
```

`web/src/routes/library.tsx`:
```tsx
import { createRoute } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { Route as rootRoute } from "./__root";
import { libraryOverviewQuery } from "@/api/queries";
import { StatCard } from "@/components/StatCard";
import { Section } from "@/components/Section";
import { StaleBanner } from "@/components/StaleBanner";
import { EChart } from "@/charts/EChart";
import { Skeleton } from "@/components/ui/skeleton";
import { Alert, AlertTitle, AlertDescription } from "@/components/ui/alert";
import { fmtBytes, fmtDuration } from "@/lib/format";

const GB = 1024 ** 3;

function LibraryOverview() {
  const q = useQuery(libraryOverviewQuery());

  if (q.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-48" />
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
          {Array.from({ length: 6 }).map((_, i) => (
            <Skeleton key={i} className="h-20" />
          ))}
        </div>
        <Skeleton className="h-64" />
      </div>
    );
  }
  if (q.isError) {
    return (
      <Alert variant="destructive">
        <AlertTitle>Could not load library stats</AlertTitle>
        <AlertDescription>{(q.error as Error).message}</AlertDescription>
      </Alert>
    );
  }

  const { data, meta } = q.data;
  const t = data.totals;
  const bar = (rows: { name: string; value: number }[], unit: string) => ({
    tooltip: { trigger: "axis", valueFormatter: (v: number) => `${v} ${unit}` },
    xAxis: { type: "category", data: rows.map((r) => r.name) },
    yAxis: { type: "value" },
    series: [{ type: "bar", data: rows.map((r) => r.value), barMaxWidth: 40, itemStyle: { borderRadius: [4, 4, 0, 0] } }],
  });

  return (
    <div className="space-y-6">
      <h2 className="text-xl font-bold tracking-tight">Library Overview</h2>
      {meta.stale && <StaleBanner />}

      <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
        <StatCard label="Total items" value={String(t.items)} />
        <StatCard label="Series" value={String(t.series)} />
        <StatCard label="Runtime" value={fmtDuration(t.runtime_seconds)} />
        <StatCard label="On disk" value={fmtBytes(t.bytes)} />
        <StatCard label="4K / HDR" value={`${t.count_uhd} / ${t.count_hdr}`} />
        <StatCard label="Dolby Vision" value={String(t.count_dv)} />
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Section title="Disk by resolution">
          <EChart option={bar(data.disk_by_resolution.map((d) => ({ name: d.bucket, value: Math.round(d.bytes / GB) })), "GB")} />
        </Section>
        <Section title="Disk by codec">
          <EChart option={bar(data.disk_by_codec.map((d) => ({ name: d.bucket, value: Math.round(d.bytes / GB) })), "GB")} />
        </Section>
        <Section title="Top genres">
          <EChart option={bar(data.genres_top.map((g) => ({ name: g.label, value: g.count })), "items")} />
        </Section>
        <Section title="Titles by decade">
          <EChart option={bar(data.by_decade.map((d) => ({ name: d.label, value: d.count })), "items")} />
        </Section>
        <div className="md:col-span-2">
          <Section title="Library growth">
            <EChart
              height={300}
              option={{
                tooltip: { trigger: "axis" },
                legend: { data: ["Added", "Cumulative"] },
                xAxis: { type: "category", data: data.growth.map((g) => g.month) },
                yAxis: [{ type: "value" }, { type: "value" }],
                series: [
                  { name: "Added", type: "bar", data: data.growth.map((g) => g.added_items), barMaxWidth: 24 },
                  { name: "Cumulative", type: "line", yAxisIndex: 1, smooth: true, data: data.growth.map((g) => g.cum_items) },
                ],
              }}
            />
          </Section>
        </div>
      </div>
    </div>
  );
}

export const Route = createRoute({
  getParentRoute: () => rootRoute,
  path: "/library",
  component: LibraryOverview,
});
```

`web/src/router.tsx`:
```tsx
import { createRouter, createRoute, redirect } from "@tanstack/react-router";
import { Route as rootRoute } from "./routes/__root";
import { Route as libraryRoute } from "./routes/library";
import { Route as watchRoute } from "./routes/watch";
import { Route as nowRoute } from "./routes/now";
import { Route as cleanupRoute } from "./routes/cleanup";

const indexRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/library" });
  },
});

const routeTree = rootRoute.addChildren([indexRoute, libraryRoute, watchRoute, nowRoute, cleanupRoute]);

export const router = createRouter({ routeTree, defaultPreload: "intent" });

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
```

`web/src/main.tsx`:
```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { Toaster } from "@/components/ui/sonner";
import { router } from "./router";
import "./index.css";

const qc = new QueryClient({
  defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: true } },
});

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
      <Toaster />
    </QueryClientProvider>
  </React.StrictMode>,
);
```

- [ ] **Step 15: `web/src/test/setup.ts`**

```ts
import "@testing-library/jest-dom/vitest";
import { vi } from "vitest";

class RO {
  observe() {}
  unobserve() {}
  disconnect() {}
}
vi.stubGlobal("ResizeObserver", RO);

if (!window.matchMedia) {
  window.matchMedia = () =>
    ({
      matches: false,
      addEventListener() {},
      removeEventListener() {},
      addListener() {},
      removeListener() {},
      dispatchEvent() {
        return false;
      },
    }) as unknown as MediaQueryList;
}
```

- [ ] **Step 16: Write the failing test** — `web/src/routes/library.test.tsx`

> ECharts needs a real canvas; in jsdom we mock the `EChart` component so page
> tests exercise data → props, not canvas rendering.

```tsx
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, expect, test, vi } from "vitest";
import type { Envelope, LibraryOverview as LO } from "@/api/types";

vi.mock("@/charts/EChart", () => ({
  EChart: ({ option }: { option: unknown }) => (
    <div data-testid="echart" data-series={JSON.stringify((option as any).series?.length ?? 0)} />
  ),
}));

// import AFTER the mock
const { Route } = await import("./library");
const LibraryOverview = Route.options.component as React.ComponentType;

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>;
}

const sample: Envelope<LO> = {
  data: {
    totals: {
      items_by_library: [{ label: "Movies", count: 4 }],
      runtime_seconds: 36000, bytes: 29_500_000_000,
      count_uhd: 2, count_hdr: 3, count_dv: 1, series: 3, items: 6,
    },
    disk_by_resolution: [{ bucket: "4K", bytes: 23e9, items: 2 }],
    disk_by_codec: [{ bucket: "HEVC", bytes: 23e9, items: 2 }],
    disk_by_container: [{ bucket: "mkv", bytes: 25e9, items: 4 }],
    disk_by_library: [{ bucket: "Movies", bytes: 27e9, items: 4 }],
    genres_top: [{ label: "Drama", count: 4 }],
    by_decade: [{ label: "1990s", count: 1 }],
    growth: [{ month: "2024-01", added_items: 2, added_bytes: 12e9, cum_items: 2 }],
  },
  meta: { generated_at: "2026-08-30T12:00:00Z", stale: false },
};

afterEach(() => vi.restoreAllMocks());

test("renders totals + charts after load", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(sample), { status: 200 }));
  render(wrap(<LibraryOverview />));
  await waitFor(() => expect(screen.getByLabelText("Total items")).toBeInTheDocument());
  expect(screen.getByLabelText("Total items").parentElement).toHaveTextContent("6");
  expect(screen.getAllByTestId("echart").length).toBeGreaterThanOrEqual(5);
  expect(screen.queryByRole("status")).not.toBeInTheDocument();
});

test("stale banner when meta.stale", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ ...sample, meta: { ...sample.meta, stale: true } }), { status: 200 }),
  );
  render(wrap(<LibraryOverview />));
  await waitFor(() => expect(screen.getByRole("status")).toBeInTheDocument());
});

test("error state on 503", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ error: { code: "not_ready", message: "first refresh has not completed" } }), { status: 503 }),
  );
  render(wrap(<LibraryOverview />));
  await waitFor(() => expect(screen.getByText(/first refresh/i)).toBeInTheDocument());
});
```

- [ ] **Step 17: Run test to verify it fails**

Run: `cd web && npm run test`
Expected: FAIL — `./library` has no usable `Route.options.component` / imports unresolved until Steps 9–14 land. (If you built Steps 9–14 first, it fails only on assertions.)

- [ ] **Step 18: Implement Steps 9–14 as written; run test to pass**

Run: `cd web && npm run test`
Expected: PASS (3 tests). Then `npm run lint` (Biome) clean and `npm run build` emits `web/dist/index.html` + hashed assets. Manually `npm run dev`, open `http://localhost:5173/library` with the Go server running — the page renders against live data.

- [ ] **Step 19: Add the `web` CI job** — append to `.github/workflows/ci.yml` under `jobs:`

```yaml
  web:
    runs-on: ubuntu-latest
    defaults: { run: { working-directory: web } }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: "22" }
      - run: npm ci
      - run: npm run lint
      - run: npm run test
      - run: npm run build
```

- [ ] **Step 20: Commit**

```bash
git add web .github/workflows/ci.yml
git commit -m "feat(web): Tailwind v4 + shadcn/ui shell, TanStack Router, ECharts, Library Overview"
```

---

## Task 12: Dockerfile, compose, README, docker CI

**Files:**
- Create: `Dockerfile`, `.dockerignore`, `docker-compose.example.yml`, `README.md`
- Modify: `.github/workflows/ci.yml` (add `docker` job)

**Interfaces:** none (packaging).

- [ ] **Step 1: Create `Dockerfile`**

```dockerfile
# syntax=docker/dockerfile:1

FROM node:22-alpine AS web
WORKDIR /app/web
COPY web/package.json web/package-lock.json* ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /app/web/dist ./web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/andrii/ephyra/internal/buildinfo.version=${VERSION}" \
    -o /ephyra ./cmd/ephyra

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /ephyra /ephyra
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/ephyra"]
```

- [ ] **Step 2: Create `.dockerignore`**

```
.git
web/node_modules
web/dist
*.db
*.db-wal
*.db-shm
docs
```

- [ ] **Step 3: Create `docker-compose.example.yml`** (from spec §12)

```yaml
services:
  ephyra:
    image: ghcr.io/OWNER/ephyra:latest
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

- [ ] **Step 4: Create `README.md`** — sections:
  - **What it is:** one paragraph (from spec §1). Note "v1 Plan 1 ships Library Overview; Watch Stats / Now Playing / Cleanup arrive in later plans."
  - **Prerequisites:** Jellyfin 10.11 on the same Docker host; the ability to bind-mount Jellyfin's config dir read-only; a Jellyfin API key.
  - **Create a Jellyfin API key:** Jellyfin → Dashboard → API Keys → +.
  - **Run:** the compose snippet; replace `OWNER`, the mount path, set `EPHYRA_JELLYFIN_API_KEY`. Browse to `http://<host>:8080`.
  - **Configuration:** the full env-var table from spec §11.
  - **How it reads data:** copy-before-read explanation; the `DIRECT_READ` note; the mtime-skip.
  - **Verify the schema (recommended once):** run `scripts/dump-jellyfin-schema.sh` on the Jellyfin host, compare with `docs/schema-notes.md`, adjust if the operator's build differs.
  - **Development:** `make dev` (two terminals: `cd web && npm run dev` on :5173 proxying to Go on :8080; `go run ./cmd/ephyra`). `make test`, `make build`, `make docker`.
  - **Architecture:** link to `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`.

- [ ] **Step 5: Add the `docker` CI job** — append to `.github/workflows/ci.yml`

```yaml
  docker:
    runs-on: ubuntu-latest
    needs: [go, web]
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/build-push-action@v6
        with:
          context: .
          push: false
          platforms: linux/amd64
          build-args: VERSION=${{ github.sha }}
```

- [ ] **Step 6: Build and smoke-test the image**

```bash
docker build -t ephyra:dev --build-arg VERSION=dev .
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/library.db < testdata/library.fixture.sql
docker run --rm -p 8899:8080 \
  -e JELLYFIN_URL=http://x -e JELLYFIN_API_KEY=x -e JELLYFIN_DATA_DIR=/jellyfin-data \
  -v /tmp/jf:/jellyfin-data:ro -v ephyra-demo:/data \
  --name ephyra-demo ephyra:dev &
sleep 2
curl -s localhost:8899/api/library/overview | head -c 300
curl -s localhost:8899/ | head -c 80        # index.html
docker rm -f ephyra-demo
```

Expected: `/api/library/overview` envelope with `data.totals.items == 6`; `/` returns the built `index.html`.

- [ ] **Step 7: Commit**

```bash
git add Dockerfile .dockerignore docker-compose.example.yml README.md .github/workflows/ci.yml
git commit -m "build: Dockerfile (distroless), compose example, README, docker CI"
```

---

## Self-Review

### 1. Spec coverage (Plan 1's slice)

| Spec section | Covered by |
|---|---|
| §4 components (Source, FileSource, store, scheduler, api, web) | Tasks 3–11 |
| §5.1 item DB read | Tasks 4–5 |
| §5.4 `Source` interface + JF12 boundary | Task 4 (`Source`, `MTimer`, `ErrNotImplemented`); `SOURCE=api` rejected in Task 10 |
| §5.5 schema-discovery spike | Task 2 (`scripts/dump-jellyfin-schema.sh`, `docs/schema-notes.md`) |
| §5.6 copy-before-read + mtime skip | Task 4 (`openForRead`), Task 8 (skip policy) |
| §6 store schema (all tables) | Task 3 `0001_init.sql` (watch/cleanup tables created, unused) |
| §7 scheduling (library job, startup run, manual trigger, per-job mutex) | Task 8 |
| §8.1 envelope + error codes (`not_ready`, `internal`) | Task 9 |
| §8.2 `GET /api/library/overview` (all tiles + charts) | Tasks 6–9, 11 |
| §8.6 `POST /api/refresh` | Task 9 (minimal; full button wiring is Plan 3, noted) |
| §8.7 `GET /healthz` | Task 9 |
| §10 frontend (shell, routes, Library page, loading/error/stale states, ECharts) | Task 11 |
| §11 config (all vars) | Task 1 |
| §12 packaging (3-stage Dockerfile, compose, footprint) | Task 12 |
| §13 observability (`slog` JSON, per-refresh log line, `/healthz`) | Tasks 8–10 |
| §14 testing (fixture DBs, pure-fn golden tests, fake Source, httptest, smoke) | Tasks 2, 5–11 |
| §15 repo layout | matches the File structure section |

**Deferred to later plans (by design, not gaps):** Watch Stats (§8.3, Plan 2), Cleanup (§8.4, Plan 2), Now Playing / SSE / `jellyfin.Client` (§8.5, §9, Plan 3), `APISource` implementation (§backlog #1), header "server name/version" + Refresh-now button UI (Plan 3), `heatmap` panel (Plan 2). `watch_events_daily` / `agg_watch_heatmap` / `agg_cleanup` tables exist now (Task 3) so no migration is needed later.

**Not in the spec, added by the plan (justified):** `internal/testsupport` fixture builder (test infra); `IsStale` as an exported pure function (testability); `MTimer` optional interface (keeps `Source` at two methods while letting the scheduler own skip policy — noted in Task 4/8); `withLogging` middleware (satisfies §13's request logging).

### 2. Placeholder scan

No `TBD`/`TODO`/"implement later"/"add error handling"/"similar to Task N". Every code step carries full code; every test step carries real assertions and a concrete run command with expected output. The Task 5 test note offers a "recommended alternative" — the plan instructs the executor to use it, and both variants are fully written (not a placeholder).

### 3. Type consistency

- `source.LibraryItem` fields (`Name`, `Type`, `SizeBytes`, `RuntimeSec`, `DateCreated`, `DateRaw`, `Year`, `Genres`, `Library`, `Container`, `VideoCodec`, `Width`, `HasVideo`, `ColorTransfer`, `DvProfile`) — defined in Task 4, populated in Task 5, consumed in Task 6. `Name` and `DateCreated` are called out in Task 5 as additions to the Task 4 list; the executor adds them there.
- `aggregate.LibraryAggregates` / `LabeledCount` / `DiskBucket` / `GrowthPoint` — defined Task 6, consumed Task 7 (`WriteLibraryAggregates`) and Task 9 test helper. JSON tags on `LabeledCount`/`DiskBucket`/`GrowthPoint` live in `aggregate` and flow through `store.LibraryOverview` to the API body; `web/src/api/types.ts` mirrors them (`label`/`count`, `bucket`/`bytes`/`items`, `month`/`added_items`/`added_bytes`/`cum_items`, and the `totals.*` snake_case keys).
- `store.RefreshMeta` — defined Task 3, used Tasks 7–9. `store.IsStale(m, ok, interval, now)` — defined Task 7, used Task 9.
- `store.Store.WriteLibraryAggregates` / `ReadLibraryOverview` — defined Task 7, used Tasks 8–9.
- `scheduler.Scheduler.Trigger(job string)` — defined Task 8, matches `api.Triggerer` (Task 9) and the `*scheduler.Scheduler` passed as `Deps.Trigger` in Task 10.
- `api.Deps` / `api.New` / `api.Server.Handler` — defined Task 9, used Task 10. `api.NewStaticHandler(fs.FS)` — defined Task 10, used Task 10 `main`.
- `web.DistFS() (fs.FS, error)` — defined Task 10, used Task 10 `main`.
- Envelope shape identical across Task 9 (`writeJSON`), Task 9 tests, Task 11 `fetchEnvelope`, Task 11 tests.

### 4. Known adjustment points (verify during execution, already flagged in-task)

- Real `library.db` column names/`type` strings vs the baseline — Task 2 checklist; adjust `testdata/library.fixture.sql` + `internal/source/file/queries.go` together, tests then guard behavior.
- `TopParentId` encoding (dashes/case) — Task 2; the query uses `upper(replace(...,'-',''))` to be tolerant.
- `DateCreated` string format — `parseJellyfinTime` tries six layouts; add one if the operator's dump shows another.
- Frontend deps float to current majors (`npm install` at scaffold time). If TanStack Router's route-tree API or a shadcn component path shifts under the installed version, the Vitest render test and `tsc -b` fail fast. ECharts option shapes in `library.tsx` are plain objects — adjust against the installed `echarts` major if a series type is renamed.

---

## Execution note

`web/package-lock.json` does not exist until the first `npm install` in Task 11. The Dockerfile's `COPY web/package-lock.json*` tolerates its absence; commit the lock file generated in Task 11 Step 7 so CI (`npm ci`) and the image build are reproducible.
