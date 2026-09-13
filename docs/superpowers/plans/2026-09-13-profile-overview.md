# Profile Overview Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn `/profile` into a profile page — avatar, headline numbers, activity strip, an unbounded play history, and ranked charts for actors, directors, series, items and genres — with the four existing analytical panels moved behind a Stats tab.

**Architecture:** Two new upsert-only dimension tables (`dim_credit`, `dim_played`) carry credits and Jellyfin's watched tick out of `jellyfin.db`, written by the library job. A new pure `aggregate.People` joins them against the `playback_events` spine and materialises two new `agg_*` tables, scoped by library exactly as `Profiles` already is. Two new handlers serve the page; a generalised art proxy serves the images; a cached, backed-off resolver turns names into Jellyfin deep links.

**Tech Stack:** Go 1.25 (`modernc.org/sqlite`, `net/http` ServeMux), React 19 + Vite + TypeScript, TanStack Router/Query, Tailwind v4.

**Spec:** `docs/superpowers/specs/2026-09-13-ephyra-profile-overview-design.md`

## Global Constraints

- `CGO_ENABLED=0` always. Pure-Go SQLite. Module floor `go 1.25`.
- `make test` = `CGO_ENABLED=0 go test ./...`; `make lint` = `go vet ./...` + `golangci-lint run` (**must be v2**; often at `~/go/bin/golangci-lint`).
- Frontend: `npm run test` (Vitest), `npm run lint` (`biome ci .`), `npx tsc -b`.
- **Never write to Jellyfin.** Never serve 5xx for a page because a refresh failed.
- Every JSON response is `{ "data": …, "meta": { "generated_at", "stale" } }`; errors are `{ "error": { "code", "message" } }`.
- **List fields serialise as `[]`, never `null`** — run every slice through `store.orEmpty` before returning.
- All user/item/series IDs are canonical dashless lowercase (`source.CanonID`).
- **Watch timestamps are not re-zoned.** `playback_events.at` is a server-local wall-clock literal; bucket off its literal components and ignore `TZ`.
- `library`: `''` is the all-libraries sentinel, `'Unknown'` is a real bucket. Query param `library=all` maps to `''`.
- Migrations are forward-only, integer-prefixed, and **must not** `CREATE`/touch `schema_migrations`.
- Comments: load-bearing only. Docs and comments are casual and deadpan — no exclamation marks, no "simply", no marketing voice.

---

### Task 1: Migration 0007 — dimension tables, aggregates, index

**Files:**
- Create: `internal/store/migrations/0007_profile_people.sql`
- Test: `internal/store/store_test.go` (add case)

**Interfaces:**
- Consumes: nothing.
- Produces: tables `dim_credit`, `dim_played`, `dim_jf_ref`, `agg_profile_people`, `agg_profile_top_items`; index `ix_playback_events_user_at`.

- [ ] **Step 1: Write the failing test**

In `internal/store/store_test.go`:

```go
func TestMigration0007Tables(t *testing.T) {
	st := newTestStore(t)
	for _, name := range []string{
		"dim_credit", "dim_played", "dim_jf_ref",
		"agg_profile_people", "agg_profile_top_items",
	} {
		var got string
		err := st.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&got)
		if err != nil {
			t.Fatalf("table %s missing: %v", name, err)
		}
	}
	var idx string
	if err := st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type='index' AND name='ix_playback_events_user_at'`,
	).Scan(&idx); err != nil {
		t.Fatalf("index missing: %v", err)
	}
}
```

If `newTestStore` is named differently in that file, use whatever helper the existing tests use to get a migrated `*Store`.

- [ ] **Step 2: Run it and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestMigration0007Tables -v`
Expected: FAIL — `table dim_credit missing`.

- [ ] **Step 3: Write the migration**

`internal/store/migrations/0007_profile_people.sql`:

```sql
-- Credits, Jellyfin's watched tick, and the name -> Jellyfin-id cache, plus the
-- two aggregates the profile overview reads.
--
-- dim_credit / dim_played / dim_jf_ref are upsert-only and are NEVER deleted
-- from. They join playback_events, which outlives the plugin's retention and
-- keeps item_name / item_genres verbatim once an item leaves Jellyfin. A
-- dimension reconciled against the current library would throw that away at the
-- join: delete a film and its cast stops counting. UserData makes it concrete --
-- it is ON DELETE CASCADE from BaseItems, so Jellyfin's own tick for a deleted
-- item is already gone by the next refresh.
--
-- Pruning "orphaned" rows here would silently destroy that durability. Don't.
--
-- Forward-only; the migration runner owns schema_migrations, do not touch it here.

CREATE TABLE dim_credit (
  item_id    TEXT NOT NULL,
  person     TEXT NOT NULL,
  kind       TEXT NOT NULL,          -- 'actor' | 'director'; GuestStar folds into actor
  list_order INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (item_id, person, kind)
);
CREATE INDEX ix_dim_credit_item ON dim_credit (item_id);

CREATE TABLE dim_played (
  user_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  played  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, item_id)
);

CREATE TABLE dim_jf_ref (
  kind       TEXT NOT NULL,          -- 'person' | 'genre'
  name       TEXT NOT NULL,
  jf_id      TEXT NOT NULL DEFAULT '',
  checked_at TEXT NOT NULL DEFAULT '',
  miss_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, name)
);

CREATE TABLE agg_profile_people (
  library                TEXT NOT NULL DEFAULT '',
  user_id                TEXT NOT NULL,
  range                  TEXT NOT NULL,   -- '30d' | '90d' | '1y' | 'all'
  kind                   TEXT NOT NULL,   -- 'actor' | 'director'
  person                 TEXT NOT NULL,
  watch_sec              INTEGER NOT NULL DEFAULT 0,
  plays                  INTEGER NOT NULL DEFAULT 0,
  distinct_titles        INTEGER NOT NULL DEFAULT 0,
  watch_sec_played       INTEGER NOT NULL DEFAULT 0,
  plays_played           INTEGER NOT NULL DEFAULT 0,
  distinct_titles_played INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (library, user_id, range, kind, person)
);

CREATE TABLE agg_profile_top_items (
  library          TEXT NOT NULL DEFAULT '',
  user_id          TEXT NOT NULL,
  range            TEXT NOT NULL,
  scope            TEXT NOT NULL,        -- 'series' | 'movie' | 'episode'
  item_id          TEXT NOT NULL,
  name             TEXT NOT NULL DEFAULT '',
  series_name      TEXT NOT NULL DEFAULT '',
  watch_sec        INTEGER NOT NULL DEFAULT 0,
  plays            INTEGER NOT NULL DEFAULT 0,
  watch_sec_played INTEGER NOT NULL DEFAULT 0,
  plays_played     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (library, user_id, range, scope, item_id)
);

-- The recents cursor scans one user's history in `at` order. The existing
-- indexes lead on `at` or on (user_id, item_id); neither serves that. library
-- stays out on purpose -- it would help the filtered case and hurt the far more
-- common unfiltered one.
CREATE INDEX ix_playback_events_user_at ON playback_events (user_id, at DESC);
```

- [ ] **Step 4: Run it and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestMigration0007Tables -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/migrations/0007_profile_people.sql internal/store/store_test.go
git commit -m "feat(store): add credit, played and ref dimensions plus profile people aggregates"
```

---

### Task 2: Source — read credits and per-user watched state

**Files:**
- Modify: `internal/source/source.go` (add types + snapshot fields)
- Modify: `internal/source/file/queries.go` (two new queries)
- Modify: `internal/source/file/file.go` (run them in `LibraryFacts`)
- Modify: `testdata/library.fixture.sql`
- Test: `internal/source/file/queries_test.go`

**Interfaces:**
- Consumes: `source.CanonID`, the existing `LibraryFacts` plumbing.
- Produces:
  ```go
  type Credit struct {
      ItemID    string // canonical
      Person    string
      Kind      string // "actor" | "director"
      ListOrder int
  }
  type UserItemPlayed struct {
      UserID, ItemID string // canonical
      Played         bool
  }
  // LibrarySnapshot gains: Credits []Credit; Played []UserItemPlayed
  ```

> `LibraryItem.Played` already exists and means `MAX(UserData.Played)` **across users**. `UserItemPlayed` is per-user and is a different thing. Do not conflate them.

- [ ] **Step 1: Extend the fixture**

Append to `testdata/library.fixture.sql`. Match the existing fixture's ID style; the four items below must be ones the fixture already defines — substitute real IDs from the file. The episode with no credits whose series has them is the point of this fixture, so keep it.

```sql
CREATE TABLE IF NOT EXISTS Peoples (
  Id TEXT NOT NULL PRIMARY KEY,
  Name TEXT NOT NULL,
  PersonType TEXT NULL
);
CREATE TABLE IF NOT EXISTS PeopleBaseItemMap (
  ItemId TEXT NOT NULL,
  PeopleId TEXT NOT NULL,
  Role TEXT NOT NULL,
  ListOrder INTEGER NULL,
  SortOrder INTEGER NULL,
  PRIMARY KEY (ItemId, PeopleId, Role)
);

-- One row per (name, type), which is how Jellyfin actually stores people:
-- Ada Vex both acts and directs, so she is two rows.
INSERT INTO Peoples (Id, Name, PersonType) VALUES
  ('P0000001-0000-0000-0000-000000000001', 'Ada Vex',   'Actor'),
  ('P0000001-0000-0000-0000-000000000002', 'Ada Vex',   'Director'),
  ('P0000001-0000-0000-0000-000000000003', 'Bo Quill',  'GuestStar'),
  ('P0000001-0000-0000-0000-000000000004', 'Cy Marrow', 'Director'),
  ('P0000001-0000-0000-0000-000000000005', 'Dot Reyes', 'Producer');
```

Then map them, using the fixture's own movie / series / episode IDs:
- movie #1 → Ada Vex (Actor, ListOrder 0), Cy Marrow (Director, 0), Dot Reyes (Producer, 0)
- series #1 → Ada Vex (Actor, 0), Bo Quill (GuestStar, 1)
- episode #1 of series #1 → Ada Vex (Actor, 0)  *(has its own credits)*
- episode #2 of series #1 → **no rows at all**  *(must inherit series #1's credits)*

Add `UserData` rows so at least one `(user, item)` pair is `Played = 1` and one is `Played = 0`.

- [ ] **Step 2: Write the failing test**

In `internal/source/file/queries_test.go`:

```go
func TestLibraryFactsCredits(t *testing.T) {
	snap := factsFromFixture(t) // use whatever helper the neighbouring tests use

	byPerson := map[string][]source.Credit{}
	for _, c := range snap.Credits {
		byPerson[c.Person] = append(byPerson[c.Person], c)
	}

	if _, ok := byPerson["Dot Reyes"]; ok {
		t.Error("Producer leaked into credits; only Actor/GuestStar/Director are read")
	}
	// GuestStar folds into actor at read time so the aggregate never sees the
	// distinction.
	for _, c := range byPerson["Bo Quill"] {
		if c.Kind != "actor" {
			t.Errorf("Bo Quill kind = %q, want actor", c.Kind)
		}
	}
	var kinds []string
	for _, c := range byPerson["Ada Vex"] {
		kinds = append(kinds, c.Kind)
	}
	if !slices.Contains(kinds, "actor") || !slices.Contains(kinds, "director") {
		t.Errorf("Ada Vex kinds = %v, want both actor and director", kinds)
	}
	for _, c := range snap.Credits {
		if c.ItemID != source.CanonID(c.ItemID) {
			t.Errorf("item id %q is not canonical", c.ItemID)
		}
	}
}

func TestLibraryFactsPlayed(t *testing.T) {
	snap := factsFromFixture(t)
	if len(snap.Played) == 0 {
		t.Fatal("no per-user played rows")
	}
	seen := map[bool]bool{}
	for _, p := range snap.Played {
		seen[p.Played] = true
		if p.UserID != source.CanonID(p.UserID) || p.ItemID != source.CanonID(p.ItemID) {
			t.Errorf("non-canonical ids: %+v", p)
		}
	}
	if !seen[true] || !seen[false] {
		t.Errorf("want both played and unplayed rows, got %v", seen)
	}
}
```

- [ ] **Step 3: Run and watch them fail**

Run: `CGO_ENABLED=0 go test ./internal/source/file/ -run 'TestLibraryFacts(Credits|Played)' -v`
Expected: FAIL — `snap.Credits` undefined.

- [ ] **Step 4: Add the types**

In `internal/source/source.go`, next to `UserPlay`:

```go
// Credit is one person attached to one item. Jellyfin keeps a separate Peoples
// row per (name, type), so the type here is the credit's own, not a global label
// on the person -- the same human is separate rows when they act and direct.
type Credit struct {
	ItemID    string // canonical
	Person    string
	Kind      string // "actor" | "director"
	ListOrder int
}

// UserItemPlayed is Jellyfin's watched tick for one (user, item). Distinct from
// LibraryItem.Played, which is MAX across users.
type UserItemPlayed struct {
	UserID, ItemID string // canonical
	Played         bool
}
```

And on `LibrarySnapshot`:

```go
	Credits []Credit
	Played  []UserItemPlayed
```

- [ ] **Step 5: Add the queries**

In `internal/source/file/queries.go`:

```go
// creditsQuery reads people per item. GuestStar folds into actor here so
// nothing downstream has to know the distinction existed. Producer, Writer and
// Composer are dropped -- they are not what the charts rank.
const creditsQuery = `
SELECT lower(replace(m.ItemId,'-','')) AS iid,
       p.Name,
       CASE WHEN p.PersonType = 'Director' THEN 'director' ELSE 'actor' END AS kind,
       COALESCE(m.ListOrder, 0)
FROM PeopleBaseItemMap m
JOIN Peoples p ON p.Id = m.PeopleId
WHERE p.PersonType IN ('Actor','GuestStar','Director')
  AND COALESCE(p.Name,'') <> ''`

// userPlayedQuery is playedStateQuery's per-user sibling: one row per
// (user, item), MAX over CustomDataKey so duplicate rows can't disagree.
const userPlayedQuery = `
SELECT lower(replace(UserId,'-','')) AS uid,
       lower(replace(ItemId,'-','')) AS iid,
       MAX(Played)                   AS played
FROM UserData
GROUP BY uid, iid`
```

- [ ] **Step 6: Run them in `LibraryFacts`**

In `internal/source/file/file.go`, inside `LibraryFacts`, following the shape of the existing query calls. `dim_credit` is `(item_id, person, kind)`-keyed, so collapse duplicate `Role` rows by keeping the lowest `ListOrder`:

```go
	rows, err := db.QueryContext(ctx, creditsQuery)
	if err != nil {
		return source.LibrarySnapshot{}, fmt.Errorf("credits: %w", err)
	}
	defer rows.Close()
	// A person can hold several roles on one item (two characters, or actor and
	// guest star). dim_credit is keyed without the role, so keep the top billing.
	best := map[source.Credit]int{}
	for rows.Next() {
		var c source.Credit
		var order int
		if err := rows.Scan(&c.ItemID, &c.Person, &c.Kind, &order); err != nil {
			return source.LibrarySnapshot{}, err
		}
		if prev, ok := best[c]; !ok || order < prev {
			best[c] = order
		}
	}
	if err := rows.Err(); err != nil {
		return source.LibrarySnapshot{}, err
	}
	for c, order := range best {
		c.ListOrder = order
		snap.Credits = append(snap.Credits, c)
	}
```

`snap.Played` is a straightforward scan of `userPlayedQuery` into `[]source.UserItemPlayed`.

- [ ] **Step 7: Run and watch them pass**

Run: `CGO_ENABLED=0 go test ./internal/source/... -v`
Expected: PASS, existing tests included.

- [ ] **Step 8: Commit**

```bash
git add internal/source testdata/library.fixture.sql
git commit -m "feat(source): read credits and per-user watched state from jellyfin.db"
```

---

### Task 3: Store — upsert and read the two dimensions

**Files:**
- Create: `internal/store/dims.go`
- Test: `internal/store/dims_test.go`

**Interfaces:**
- Consumes: `source.Credit`, `source.UserItemPlayed`.
- Produces:
  ```go
  func (s *Store) UpsertCredits(ctx context.Context, cs []source.Credit) error
  func (s *Store) UpsertPlayed(ctx context.Context, ps []source.UserItemPlayed) error
  func (s *Store) ReadCredits(ctx context.Context) ([]source.Credit, error)
  func (s *Store) ReadPlayed(ctx context.Context) (map[aggregate.UserItem]bool, error)
  ```
  `aggregate.UserItem` is defined in Task 5. Until then, have `ReadPlayed` return `map[[2]string]bool` and change it in Task 5 — or do Task 5 first. Prefer defining `aggregate.UserItem` up front in this task to avoid the churn.

- [ ] **Step 1: Write the failing test**

`internal/store/dims_test.go`:

```go
func TestUpsertCreditsRefreshesAndNeverDeletes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	first := []source.Credit{
		{ItemID: "aaa", Person: "Ada Vex", Kind: "actor", ListOrder: 3},
		{ItemID: "bbb", Person: "Cy Marrow", Kind: "director"},
	}
	if err := st.UpsertCredits(ctx, first); err != nil {
		t.Fatal(err)
	}
	// A later refresh sees a re-billed Ada and no longer sees item bbb at all --
	// bbb was deleted from Jellyfin. Its credit must survive, because the play
	// that references it survives in the spine.
	second := []source.Credit{{ItemID: "aaa", Person: "Ada Vex", Kind: "actor", ListOrder: 0}}
	if err := st.UpsertCredits(ctx, second); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadCredits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d credits, want 2 (the deleted item's credit must be retained)", len(got))
	}
	for _, c := range got {
		if c.ItemID == "aaa" && c.ListOrder != 0 {
			t.Errorf("list_order = %d, want 0 (refreshed)", c.ListOrder)
		}
	}
}

func TestUpsertPlayedRefreshesAndNeverDeletes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if err := st.UpsertPlayed(ctx, []source.UserItemPlayed{
		{UserID: "u1", ItemID: "aaa", Played: true},
		{UserID: "u1", ItemID: "bbb", Played: true},
	}); err != nil {
		t.Fatal(err)
	}
	// Unmarked in Jellyfin, and bbb deleted entirely.
	if err := st.UpsertPlayed(ctx, []source.UserItemPlayed{
		{UserID: "u1", ItemID: "aaa", Played: false},
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadPlayed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[aggregate.UserItem{UserID: "u1", ItemID: "aaa"}] {
		t.Error("aaa should have been un-played by the refresh")
	}
	if !got[aggregate.UserItem{UserID: "u1", ItemID: "bbb"}] {
		t.Error("bbb was deleted from Jellyfin; its tick must be retained")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run 'TestUpsert(Credits|Played)' -v`
Expected: FAIL — undefined `UpsertCredits`.

- [ ] **Step 3: Implement**

`internal/store/dims.go`:

```go
package store

import (
	"context"

	"github.com/andriykohut/ephyra/internal/aggregate"
	"github.com/andriykohut/ephyra/internal/source"
)

// UpsertCredits refreshes what Jellyfin still knows and leaves everything else
// alone. No DELETE: a play in the spine outlives its item, and its cast has to
// outlive it too or the charts lose history when you delete a film.
func (s *Store) UpsertCredits(ctx context.Context, cs []source.Credit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range cs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dim_credit (item_id, person, kind, list_order)
			VALUES (?,?,?,?)
			ON CONFLICT (item_id, person, kind) DO UPDATE SET list_order = excluded.list_order`,
			c.ItemID, c.Person, c.Kind, c.ListOrder,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpsertPlayed mirrors UpsertCredits. UserData is ON DELETE CASCADE from
// BaseItems, so this snapshot is the only copy of the tick that survives the
// item.
func (s *Store) UpsertPlayed(ctx context.Context, ps []source.UserItemPlayed) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range ps {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dim_played (user_id, item_id, played)
			VALUES (?,?,?)
			ON CONFLICT (user_id, item_id) DO UPDATE SET played = excluded.played`,
			p.UserID, p.ItemID, p.Played,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

`ReadCredits` selects all four columns into `[]source.Credit`. `ReadPlayed` selects `user_id, item_id, played` into `map[aggregate.UserItem]bool`, storing only `true` entries so a missing key and a false key mean the same thing to callers.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run 'TestUpsert(Credits|Played)' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/dims.go internal/store/dims_test.go
git commit -m "feat(store): upsert-only credit and watched-state dimensions"
```

---

### Task 4: Scheduler — write the dimensions from the library job

**Files:**
- Modify: `internal/scheduler/scheduler.go` (library job body)
- Test: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `Store.UpsertCredits`, `Store.UpsertPlayed`, `LibrarySnapshot.Credits/.Played`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Extend the existing library-job test so its fake source returns a snapshot with `Credits` and `Played` populated, then assert both tables have rows after a run:

```go
func TestLibraryJobWritesDimensions(t *testing.T) {
	// build the scheduler the way the neighbouring library-job tests do,
	// with a source whose LibraryFacts returns Credits and Played
	// ... run one library job ...
	var credits, played int
	if err := st.DB().QueryRow(`SELECT count(*) FROM dim_credit`).Scan(&credits); err != nil {
		t.Fatal(err)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM dim_played`).Scan(&played); err != nil {
		t.Fatal(err)
	}
	if credits == 0 || played == 0 {
		t.Fatalf("dim_credit=%d dim_played=%d, want both non-zero", credits, played)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/scheduler/ -run TestLibraryJobWritesDimensions -v`
Expected: FAIL — both counts zero.

- [ ] **Step 3: Wire it**

In the library job, after `WriteLibraryAggregates` succeeds:

```go
	if err := s.st.UpsertCredits(ctx, snap.Credits); err != nil {
		return s.recordFailure(ctx, "library", mt, start, err)
	}
	if err := s.st.UpsertPlayed(ctx, snap.Played); err != nil {
		return s.recordFailure(ctx, "library", mt, start, err)
	}
```

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/scheduler/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler
git commit -m "feat(scheduler): write credit and watched dimensions on library refresh"
```

---
### Task 5: `aggregate.People` — credit attribution

**Files:**
- Create: `internal/aggregate/people.go`
- Test: `internal/aggregate/people_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent`, `source.Credit`.
- Produces:
  ```go
  type UserItem struct{ UserID, ItemID string }

  // creditIndex answers "who is credited on this play", series fallback included.
  type creditIndex struct{ /* unexported */ }
  func newCreditIndex(credits []source.Credit) *creditIndex
  func (ix *creditIndex) forEvent(ev source.PlaybackEvent) []source.Credit
  ```

- [ ] **Step 1: Write the failing test**

`internal/aggregate/people_test.go`:

```go
func TestCreditIndexFallsBackToSeries(t *testing.T) {
	credits := []source.Credit{
		{ItemID: "series1", Person: "Ada Vex", Kind: "actor"},
		{ItemID: "ep1", Person: "Bo Quill", Kind: "actor"},
	}
	ix := newCreditIndex(credits)

	// ep1 has its own cast, so the series' is not consulted.
	own := ix.forEvent(source.PlaybackEvent{ItemID: "ep1", SeriesID: "series1", ItemType: "episode"})
	if len(own) != 1 || own[0].Person != "Bo Quill" {
		t.Fatalf("own credits = %+v, want just Bo Quill", own)
	}

	// ep2 has none. Roughly half the episodes in a real library are like this,
	// so without the fallback the people charts are movies-only.
	inherited := ix.forEvent(source.PlaybackEvent{ItemID: "ep2", SeriesID: "series1", ItemType: "episode"})
	if len(inherited) != 1 || inherited[0].Person != "Ada Vex" {
		t.Fatalf("inherited credits = %+v, want Ada Vex from the series", inherited)
	}
}

func TestCreditIndexMoviesDoNotInherit(t *testing.T) {
	ix := newCreditIndex([]source.Credit{{ItemID: "series1", Person: "Ada Vex", Kind: "actor"}})
	got := ix.forEvent(source.PlaybackEvent{ItemID: "movie1", ItemType: "movie"})
	if len(got) != 0 {
		t.Fatalf("movie inherited %+v; a movie has no parent to inherit from", got)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -run TestCreditIndex -v`
Expected: FAIL — undefined `newCreditIndex`.

- [ ] **Step 3: Implement**

`internal/aggregate/people.go`:

```go
package aggregate

import "github.com/andriykohut/ephyra/internal/source"

// UserItem keys the watched-tick lookup.
type UserItem struct{ UserID, ItemID string }

type creditIndex struct{ byItem map[string][]source.Credit }

func newCreditIndex(credits []source.Credit) *creditIndex {
	ix := &creditIndex{byItem: map[string][]source.Credit{}}
	for _, c := range credits {
		ix.byItem[c.ItemID] = append(ix.byItem[c.ItemID], c)
	}
	return ix
}

// forEvent credits an episode to the parent series when the episode carries no
// cast of its own. On a real library 925/925 movies have credits but only
// 5285/9794 episodes do.
func (ix *creditIndex) forEvent(ev source.PlaybackEvent) []source.Credit {
	if own := ix.byItem[ev.ItemID]; len(own) > 0 {
		return own
	}
	if ev.SeriesID != "" {
		return ix.byItem[ev.SeriesID]
	}
	return nil
}
```

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -run TestCreditIndex -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate/people.go internal/aggregate/people_test.go
git commit -m "feat(aggregate): credit attribution with series fallback"
```

---

### Task 6: `aggregate.People` — people rankings

**Files:**
- Modify: `internal/aggregate/people.go`
- Test: `internal/aggregate/people_test.go`

**Interfaces:**
- Consumes: `creditIndex`, `UserItem`, `rangeCutoff` / `profileRangeList` from `profile.go`.
- Produces:
  ```go
  type PeopleRow struct {
      UserID, Range, Kind, Person string
      WatchSec, Plays, DistinctTitles                   int64
      WatchSecPlayed, PlaysPlayed, DistinctTitlesPlayed int64
  }
  type PeopleAggregates struct {
      People   []PeopleRow
      TopItems []TopItemRow // Task 7
  }
  func People(events []source.PlaybackEvent, credits []source.Credit,
              played map[UserItem]bool, now time.Time) PeopleAggregates
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestPeopleCountsSeriesOnceForBreadth(t *testing.T) {
	credits := []source.Credit{{ItemID: "series1", Person: "Ada Vex", Kind: "actor"}}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	var events []source.PlaybackEvent
	for i := range 20 { // 20 episodes of one series
		events = append(events, source.PlaybackEvent{
			At: now.Add(-time.Duration(i) * time.Hour), UserID: "u1",
			ItemID: fmt.Sprintf("ep%d", i), ItemType: "episode",
			SeriesID: "series1", SeriesName: "Series One", PlayDurationSec: 1200,
		})
	}
	got := People(events, credits, map[UserItem]bool{}, now)

	var ada *PeopleRow
	for i := range got.People {
		if got.People[i].Person == "Ada Vex" && got.People[i].Range == "all" {
			ada = &got.People[i]
		}
	}
	if ada == nil {
		t.Fatal("no all-range row for Ada Vex")
	}
	if ada.DistinctTitles != 1 {
		t.Errorf("distinct_titles = %d, want 1: a series counts once, not per episode",
			ada.DistinctTitles)
	}
	if ada.Plays != 20 {
		t.Errorf("plays = %d, want 20", ada.Plays)
	}
	if ada.WatchSec != 20*1200 {
		t.Errorf("watch_sec = %d, want %d", ada.WatchSec, 20*1200)
	}
}

func TestPeoplePlayedMetricsCountOnlyTickedItems(t *testing.T) {
	credits := []source.Credit{
		{ItemID: "m1", Person: "Ada Vex", Kind: "actor"},
		{ItemID: "m2", Person: "Ada Vex", Kind: "actor"},
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		{At: now.Add(-time.Hour), UserID: "u1", ItemID: "m1", ItemType: "movie", PlayDurationSec: 6000},
		{At: now.Add(-2 * time.Hour), UserID: "u1", ItemID: "m2", ItemType: "movie", PlayDurationSec: 300},
	}
	played := map[UserItem]bool{{UserID: "u1", ItemID: "m1"}: true}

	got := People(events, credits, played, now)
	for _, r := range got.People {
		if r.Person != "Ada Vex" || r.Range != "all" {
			continue
		}
		if r.WatchSec != 6300 {
			t.Errorf("watch_sec = %d, want 6300 (everything)", r.WatchSec)
		}
		if r.WatchSecPlayed != 6000 {
			t.Errorf("watch_sec_played = %d, want 6000 (only the ticked item)", r.WatchSecPlayed)
		}
		if r.DistinctTitlesPlayed != 1 {
			t.Errorf("distinct_titles_played = %d, want 1", r.DistinctTitlesPlayed)
		}
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -run TestPeople -v`
Expected: FAIL — undefined `People`.

- [ ] **Step 3: Implement**

Walk events once per range. For each event, take `ix.forEvent(ev)`, dedupe the credits by `(person, kind)`, and accumulate into a map keyed by `(user, range, kind, person)`.

The title key for breadth is `ev.SeriesID` when non-empty, otherwise `ev.ItemID` — that is what makes a series count once. Track it in a `map[string]struct{}` per accumulator and take `len` at the end.

Range cutoffs reuse `rangeCutoff(rng, now)` and `profileRangeList` from `profile.go`. Cut on the event's literal `At`; it is server-local wall time and is not re-zoned.

Cap with a helper both this task and Task 7 use:

```go
// topUnion keeps the best n by each metric. A single binge would otherwise hand
// every slot to one show's regular cast, which is why breadth is ranked at all;
// capping by hours alone would throw the breadth ranking away before it is read.
func topUnion[T any](rows []T, n int, metrics ...func(T) int64) []T {
	keep := map[int]bool{}
	for _, m := range metrics {
		idx := make([]int, len(rows))
		for i := range idx {
			idx[i] = i
		}
		slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(m(rows[b]), m(rows[a])) })
		for _, i := range idx[:min(n, len(idx))] {
			keep[i] = true
		}
	}
	out := make([]T, 0, len(keep))
	for i, r := range rows {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}
```

Apply it per `(user, range, kind)` group with `n = 50`, ranking on `WatchSec` and `DistinctTitles`.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate
git commit -m "feat(aggregate): rank people by hours and by breadth"
```

---

### Task 7: `aggregate.People` — top items

**Files:**
- Modify: `internal/aggregate/people.go`
- Test: `internal/aggregate/people_test.go`

**Interfaces:**
- Produces:
  ```go
  type TopItemRow struct {
      UserID, Range, Scope, ItemID, Name, SeriesName string
      WatchSec, Plays, WatchSecPlayed, PlaysPlayed   int64
  }
  ```
  `Scope` is `"series" | "movie" | "episode"`. Populates `PeopleAggregates.TopItems`.

- [ ] **Step 1: Write the failing test**

```go
func TestTopItemsRollEpisodesIntoSeriesScope(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		{At: now.Add(-time.Hour), UserID: "u1", ItemID: "ep1", ItemName: "Pilot",
			ItemType: "episode", SeriesID: "s1", SeriesName: "Series One", PlayDurationSec: 1200},
		{At: now.Add(-2 * time.Hour), UserID: "u1", ItemID: "ep2", ItemName: "Second",
			ItemType: "episode", SeriesID: "s1", SeriesName: "Series One", PlayDurationSec: 1500},
	}
	got := People(events, nil, map[UserItem]bool{}, now)

	var series, episodes int
	for _, r := range got.TopItems {
		if r.Range != "all" {
			continue
		}
		switch r.Scope {
		case "series":
			series++
			if r.ItemID != "s1" || r.WatchSec != 2700 {
				t.Errorf("series row = %+v, want s1 with 2700s", r)
			}
		case "episode":
			episodes++
		}
	}
	if series != 1 {
		t.Errorf("series rows = %d, want 1", series)
	}
	if episodes != 2 {
		t.Errorf("episode rows = %d, want 2; episodes stay their own scope too", episodes)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -run TestTopItems -v`
Expected: FAIL — `got.TopItems` empty.

- [ ] **Step 3: Implement**

In the same per-range walk, accumulate three scopes: `movie` keyed by `ItemID`, `episode` keyed by `ItemID`, `series` keyed by `SeriesID` (skip events with no `SeriesID`). Carry `Name` (`ItemName`, or `SeriesName` for the series scope) and `SeriesName`. Apply `topUnion` per `(user, range, scope)` with `n = 50` on `WatchSec` and `Plays`.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/aggregate/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate
git commit -m "feat(aggregate): rank top series, movies and episodes"
```

---

### Task 8: Store — write and read the two new aggregates

**Files:**
- Create: `internal/store/writes_people.go`, `internal/store/reads_people.go`
- Test: `internal/store/writes_people_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) WritePeopleAggregates(ctx context.Context, scoped map[string]aggregate.PeopleAggregates) error
  func (s *Store) ReadProfilePeople(ctx context.Context, userID, rng, library string) ([]PersonDTO, error)
  func (s *Store) ReadProfileTopItems(ctx context.Context, userID, rng, library string) ([]TopItemDTO, error)
  ```
  DTO JSON tags: `person`, `kind`, `watch_sec`, `plays`, `distinct_titles`, `watch_sec_played`, `plays_played`, `distinct_titles_played`, `jf_url`; and `scope`, `item_id`, `name`, `series_name`, `watch_sec`, `plays`, `watch_sec_played`, `plays_played`, `jf_url`.

- [ ] **Step 1: Write the failing test**

```go
func TestWritePeopleAggregatesIsLibraryScoped(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	row := aggregate.PeopleRow{
		UserID: "u1", Range: "all", Kind: "actor", Person: "Ada Vex",
		WatchSec: 100, Plays: 2, DistinctTitles: 1,
	}
	if err := st.WritePeopleAggregates(ctx, map[string]aggregate.PeopleAggregates{
		"":       {People: []aggregate.PeopleRow{row}},
		"Movies": {People: []aggregate.PeopleRow{row}},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := st.ReadProfilePeople(ctx, "u1", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("all-libraries rows = %d, want 1", len(all))
	}
	movies, err := st.ReadProfilePeople(ctx, "u1", "all", "Movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 {
		t.Fatalf("Movies rows = %d, want 1", len(movies))
	}
	none, err := st.ReadProfilePeople(ctx, "u1", "all", "Shows")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("Shows rows = %d, want 0", len(none))
	}
}

func TestReadProfilePeopleReturnsEmptySliceNotNil(t *testing.T) {
	st := newTestStore(t)
	got, err := st.ReadProfilePeople(context.Background(), "nobody", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("got nil; the SPA iterates this straight off the response")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run 'TestWritePeople|TestReadProfilePeople' -v`
Expected: FAIL — undefined `WritePeopleAggregates`.

- [ ] **Step 3: Implement**

`WritePeopleAggregates` follows `WriteProfileAggregates` exactly: one transaction, `DELETE FROM agg_profile_people` and `DELETE FROM agg_profile_top_items` first, then insert every scope's rows with `lib` as the leading column.

Both reads order by `watch_sec DESC` and end with `return orEmpty(out), nil`. `jf_url` is left `""` here; Task 17 fills it.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): persist and read the profile people aggregates"
```

---

### Task 9: Scheduler — run `People` in the watch job, library-scoped

**Files:**
- Modify: `internal/scheduler/scheduler.go:199-210`
- Test: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: `aggregate.People`, `Store.WritePeopleAggregates`, `Store.ReadCredits`, `Store.ReadPlayed`, `aggregate.FilterEventsByLibrary`, `aggregate.DistinctEventLibraries`.

- [ ] **Step 1: Write the failing test**

```go
func TestWatchJobWritesPeopleAggregates(t *testing.T) {
	// Same setup as the existing watch-job tests, plus: seed dim_credit with a
	// credit for an item the fake plugin source reports a play for.
	// ... run one watch job ...
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM agg_profile_people`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no people rows after a watch run")
	}
	var libs int
	if err := st.DB().QueryRow(
		`SELECT count(DISTINCT library) FROM agg_profile_people`).Scan(&libs); err != nil {
		t.Fatal(err)
	}
	if libs < 1 {
		t.Fatal("expected at least the '' all-libraries scope")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/scheduler/ -run TestWatchJobWritesPeople -v`
Expected: FAIL — zero rows.

- [ ] **Step 3: Wire it**

Immediately after the `WriteProfileAggregates` block:

```go
	credits, err := s.st.ReadCredits(ctx)
	if err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
	played, err := s.st.ReadPlayed(ctx)
	if err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
	scopedPeople := map[string]aggregate.PeopleAggregates{
		"": aggregate.People(history, credits, played, now),
	}
	for _, lib := range aggregate.DistinctEventLibraries(history) {
		if lib == "" {
			continue
		}
		scopedPeople[lib] = aggregate.People(
			aggregate.FilterEventsByLibrary(history, lib), credits, played, now)
	}
	if err := s.st.WritePeopleAggregates(ctx, scopedPeople); err != nil {
		return s.recordFailure(ctx, "watch", mt, start, err)
	}
```

On a cold start this can run before the library job has written any credits, leaving the people charts empty for one cycle. It self-heals next run; coupling the two jobs to avoid that is not worth it.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/scheduler/ -v && make lint`
Expected: PASS, clean lint.

- [ ] **Step 5: Commit**

```bash
git add internal/scheduler
git commit -m "feat(scheduler): aggregate people per library on watch refresh"
```

---
### Task 10: Store — keyset read of the play history

**Files:**
- Modify: `internal/store/spine.go`
- Test: `internal/store/spine_test.go`

**Interfaces:**
- Produces:
  ```go
  type PlayRow struct {
      At              string `json:"at"`
      ItemID          string `json:"item_id"`
      ItemType        string `json:"item_type"`
      Name            string `json:"name"`
      SeriesName      string `json:"series_name"`
      PlayDurationSec int64  `json:"play_duration_sec"`
      Played          bool   `json:"played"`
      JFURL           string `json:"jf_url"`
  }
  type PlayCursor struct {
      At    string `json:"at"`
      RowID int64  `json:"row_id"`
  }
  func (s *Store) ReadPlays(ctx context.Context, userID, library string,
      before *PlayCursor, limit int) ([]PlayRow, *PlayCursor, error)
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestReadPlaysPagesBackwardsWithoutGapsOrRepeats(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// Two events share a timestamp, which is why the cursor carries rowid too --
	// an at-only cursor either skips one or serves it twice.
	seedPlaybackEvents(t, st, []source.PlaybackEvent{
		{At: mustAt("2026-09-01 10:00:00"), UserID: "u1", ItemID: "a", PlayDurationSec: 60},
		{At: mustAt("2026-09-01 11:00:00"), UserID: "u1", ItemID: "b", PlayDurationSec: 60},
		{At: mustAt("2026-09-01 11:00:00"), UserID: "u1", ItemID: "c", PlayDurationSec: 60},
		{At: mustAt("2026-09-01 12:00:00"), UserID: "u1", ItemID: "d", PlayDurationSec: 60},
		{At: mustAt("2026-09-01 09:00:00"), UserID: "u2", ItemID: "e", PlayDurationSec: 60},
	})

	var seen []string
	var cur *PlayCursor
	for range 10 {
		rows, next, err := st.ReadPlays(ctx, "u1", "", cur, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			seen = append(seen, r.ItemID)
		}
		if next == nil {
			break
		}
		cur = next
	}

	want := []string{"d", "c", "b", "a"} // newest first; u2's row never appears
	if !slices.Equal(seen, want) {
		t.Fatalf("paged %v, want %v", seen, want)
	}
}

func TestReadPlaysEndsWithNilCursor(t *testing.T) {
	st := newTestStore(t)
	seedPlaybackEvents(t, st, []source.PlaybackEvent{
		{At: mustAt("2026-09-01 10:00:00"), UserID: "u1", ItemID: "a", PlayDurationSec: 60},
	})
	_, next, err := st.ReadPlays(context.Background(), "u1", "", nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("cursor = %+v, want nil at the end of history", next)
	}
}
```

Write `seedPlaybackEvents` and `mustAt` as small helpers in the test file if the package has no equivalent.

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestReadPlays -v`
Expected: FAIL — undefined `ReadPlays`.

- [ ] **Step 3: Implement**

Ask for `limit+1` rows and use the extra one to decide whether there is a next page, rather than counting separately:

```go
func (s *Store) ReadPlays(ctx context.Context, userID, library string,
	before *PlayCursor, limit int,
) ([]PlayRow, *PlayCursor, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `
		SELECT pe.rowid, pe.at, pe.item_id, pe.item_type, pe.item_name,
		       pe.series_name, pe.play_duration_sec,
		       COALESCE(dp.played, 0)
		FROM playback_events pe
		LEFT JOIN dim_played dp ON dp.user_id = pe.user_id AND dp.item_id = pe.item_id
		WHERE pe.user_id = ?`
	args := []any{userID}
	if library != "" {
		q += ` AND pe.library = ?`
		args = append(args, library)
	}
	if before != nil {
		// Tuple comparison, not `at < ?`: two plays can share a timestamp, and
		// the rowid is what breaks the tie in both the filter and the order.
		q += ` AND (pe.at < ? OR (pe.at = ? AND pe.rowid < ?))`
		args = append(args, before.At, before.At, before.RowID)
	}
	q += ` ORDER BY pe.at DESC, pe.rowid DESC LIMIT ?`
	args = append(args, limit+1)
	// ... scan; if len(rows) > limit, trim to limit and build the cursor from
	// the last kept row; otherwise return a nil cursor ...
}
```

Return `orEmpty(out)`.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/store/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(store): keyset pagination over the play history"
```

---

### Task 11: API — `GET /api/profile/{userID}/plays`

**Files:**
- Create: `internal/api/profile_plays.go`
- Modify: `internal/api/api.go:56` (route)
- Test: `internal/api/profile_plays_test.go`

**Interfaces:**
- Consumes: `Store.ReadPlays`, `writeJSON`, `writeError`, `s.meta`, `s.profileStale`.
- Produces: response `{"data": {"plays": [...], "next_cursor": {...}|null}, "meta": {...}}`.

- [ ] **Step 1: Write the failing test**

```go
func TestPlaysHandlerRejectsBadLimit(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/plays?limit=abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPlaysHandlerSerialisesEmptyListNotNull(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/nobody/plays", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"plays":[]`) {
		t.Fatalf("body = %s, want an empty array", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"next_cursor":null`) {
		t.Fatalf("body = %s, want a null cursor", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/api/ -run TestPlaysHandler -v`
Expected: FAIL — 404, the route does not exist.

- [ ] **Step 3: Implement**

`internal/api/profile_plays.go`:

```go
func (s *Server) handleProfilePlays(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "bad_request", "limit must be 1..200")
			return
		}
		limit = n
	}

	var cur *store.PlayCursor
	if at := r.URL.Query().Get("before"); at != "" {
		id, err := strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "before_id must accompany before")
			return
		}
		cur = &store.PlayCursor{At: at, RowID: id}
	}

	library := r.URL.Query().Get("library")
	if library == "all" {
		library = ""
	}

	rows, next, err := s.st.ReadPlays(ctx, r.PathValue("userID"), library, cur, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"plays": rows, "next_cursor": next,
	}, s.meta(s.profileStale(ctx)))
}
```

Register at `internal/api/api.go`, next to the other profile routes:

```go
	mux.HandleFunc("GET /api/profile/{userID}/plays", s.handleProfilePlays)
```

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/api/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "feat(api): paginated play history endpoint"
```

---

### Task 12: API — `GET /api/profile/{userID}/overview`

**Files:**
- Create: `internal/api/profile_overview.go`, `internal/store/reads_overview.go`
- Modify: `internal/api/api.go` (route)
- Test: `internal/api/profile_overview_test.go`

**Interfaces:**
- Produces:
  ```go
  type Overview struct {
      User       ProfileUserDTO   `json:"user"`
      Since      string           `json:"since"`
      Plays      int64            `json:"plays"`
      WatchSec   int64            `json:"watch_sec"`
      Activity   []ActivityDay    `json:"activity"`
      NowPlaying *live.SessionDTO `json:"now_playing"`
      People     []PersonDTO      `json:"people"`
      Items      []TopItemDTO     `json:"items"`
      Genres     []GenreDTO       `json:"genres"`
  }
  type ActivityDay struct {
      Day      string `json:"day"`
      WatchSec int64  `json:"watch_sec"`
  }
  ```
  Use whatever the Now Playing code already calls a session DTO rather than inventing a type.

- [ ] **Step 1: Write the failing test**

```go
func TestOverviewRejectsBadRange(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview?range=7d", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestOverviewWithEmptySpineStillServes200(t *testing.T) {
	srv := newTestServer(t) // no playback events at all
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an empty spine is a page state, not an error", rec.Code)
	}
	for _, field := range []string{`"people":[]`, `"items":[]`, `"genres":[]`, `"activity":[]`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("body missing %s: %s", field, rec.Body.String())
		}
	}
}

func TestOverviewNowPlayingNullWhenLiveUnconfigured(t *testing.T) {
	srv := newTestServer(t) // Deps.Live == nil
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec,
		httptest.NewRequest("GET", "/api/profile/u1/overview", nil))
	if !strings.Contains(rec.Body.String(), `"now_playing":null`) {
		t.Fatalf("body = %s, want a null now_playing", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/api/ -run TestOverview -v`
Expected: FAIL — 404.

- [ ] **Step 3: Implement the store read**

`ReadProfileOverview(ctx, userID, rng, library)` composes:
- headline numbers and `since` from `agg_profile_summary` for `(user, rng, library)`,
- `activity` from `SELECT day, SUM(watch_sec) FROM watch_events_daily WHERE user_id = ? [AND library = ?] GROUP BY day ORDER BY day`,
- `people` from `ReadProfilePeople`, `items` from `ReadProfileTopItems`,
- `genres` from `agg_profile_taste WHERE dim = 'genre'`.

Every slice through `orEmpty`.

- [ ] **Step 4: Implement the handler**

Validate `range` against `validRanges` exactly as `handleProfile` does, map `library=all` to `""`, then:

```go
	if s.live != nil {
		if snap := s.live.Snapshot(ctx); snap != nil {
			out.NowPlaying = sessionForUser(snap, userID) // nil when this user has none
		}
	}
```

`Hub.Snapshot` never starts the poll loop, which is why the profile page can show a live row without putting Now Playing's polling behind every profile view.

- [ ] **Step 5: Register the route**

```go
	mux.HandleFunc("GET /api/profile/{userID}/overview", s.handleProfileOverview)
```

- [ ] **Step 6: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/api/ ./internal/store/ -v && make lint`
Expected: PASS, clean lint.

- [ ] **Step 7: Commit**

```bash
git add internal/api internal/store
git commit -m "feat(api): profile overview endpoint"
```

---

### Task 13: Jellyfin client — person/user images, person search, genres, server id

**Files:**
- Modify: `internal/jellyfin/client.go`
- Test: `internal/jellyfin/client_test.go`

**Interfaces:**
- Produces:
  ```go
  func (c *Client) PersonImage(ctx context.Context, name string) (io.ReadCloser, string, error)
  func (c *Client) UserImage(ctx context.Context, userID string) (io.ReadCloser, string, error)
  func (c *Client) FindPerson(ctx context.Context, name string) (string, error) // "" = no exact match
  func (c *Client) Genres(ctx context.Context) (map[string]string, error)       // name -> id
  func (c *Client) ServerID(ctx context.Context) (string, error)
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestFindPersonRequiresAnExactNameMatch(t *testing.T) {
	// searchTerm is a substring match on the server: "Cranston" comes back with
	// both of these. Taking the first hit would link to the wrong person.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[
			{"Name":"Cranston Johnson","Id":"wrongid"},
			{"Name":"Bryan Cranston","Id":"rightid"}
		]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "key")

	got, err := c.FindPerson(context.Background(), "Bryan Cranston")
	if err != nil {
		t.Fatal(err)
	}
	if got != "rightid" {
		t.Fatalf("id = %q, want rightid", got)
	}
}

func TestFindPersonReturnsEmptyWhenNoExactMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Cranston Johnson","Id":"wrongid"}]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "key")

	got, err := c.FindPerson(context.Background(), "Bryan Cranston")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("id = %q, want empty", got)
	}
}

func TestPersonImageEscapesTheName(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/jpeg")
	}))
	defer srv.Close()
	c := New(srv.URL, "key")

	rc, _, err := c.PersonImage(context.Background(), "A$AP Rocky")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if gotPath != "/Persons/A$AP Rocky/Images/Primary" {
		t.Fatalf("path = %q", gotPath)
	}
}
```

Use the constructor the package actually exports; `New(base, key)` above is a placeholder for whatever `client.go` defines.

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/jellyfin/ -run 'TestFindPerson|TestPersonImage' -v`
Expected: FAIL — undefined `FindPerson`.

- [ ] **Step 3: Implement**

Follow `Client.Image`'s shape: `url.PathEscape` the name, `maxWidth=480&quality=85`, `Authorization: MediaBrowser Token=` header, non-2xx becomes an error carrying the path but never the base URL in user-facing text.

```go
// FindPerson resolves a name to Jellyfin's own person id. searchTerm is a
// substring match, so only an exact name counts -- "Cranston" also returns
// "Cranston Johnson". Empty string means no match, which is not an error.
func (c *Client) FindPerson(ctx context.Context, name string) (string, error) {
	var out struct {
		Items []struct{ Name, Id string }
	}
	q := url.Values{"searchTerm": {name}, "limit": {"20"}}
	if err := c.getJSON(ctx, "/Persons?"+q.Encode(), &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		if it.Name == name {
			return it.Id, nil
		}
	}
	return "", nil
}
```

`Genres` does one `GET /Genres?limit=1000` and returns name→id. `ServerID` reads `/System/Info` `.Id`.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/jellyfin/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jellyfin
git commit -m "feat(jellyfin): person and user images, exact person lookup, genres"
```

---

### Task 14: Generalise the art proxy

**Files:**
- Rename/modify: `internal/api/nowplaying_art.go` → `internal/api/art.go`
- Modify: `internal/api/api.go` (routes), `internal/live/hub.go` (passthroughs)
- Modify: `web/src/` wherever `/api/now-playing/art/` is referenced
- Test: `internal/api/art_test.go`

**Interfaces:**
- Produces routes `GET /api/art/item/{itemId}`, `GET /api/art/person?name=`, `GET /api/art/user/{userId}`. `GET /api/now-playing/art/{itemId}` is **removed**.

- [ ] **Step 1: Write the failing test**

```go
func TestArtCacheEvictsOnBytesNotCount(t *testing.T) {
	c := newArtCache(1000) // 1000-byte budget
	c.put("a", "image/jpeg", make([]byte, 600))
	c.put("b", "image/jpeg", make([]byte, 600))
	if _, _, ok := c.get("a"); ok {
		t.Error("a should have been evicted; two 600-byte entries exceed the budget")
	}
	if _, _, ok := c.get("b"); !ok {
		t.Error("b should still be cached")
	}
}

func TestArtProxyCachesMisses(t *testing.T) {
	var upstream int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	// build a Server whose live client points at srv
	// ... request /api/art/person?name=Nobody twice ...
	if upstream != 1 {
		t.Fatalf("upstream hits = %d, want 1: over half the people in a real library "+
			"have no image, and re-asking on every render is the whole cost", upstream)
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/api/ -run TestArt -v`
Expected: FAIL — `newArtCache` takes an entry count; misses are not cached.

- [ ] **Step 3: Implement**

Change `artCache` to hold a byte budget: keep a running `bytes` total, add `len(body)` on put, subtract on evict, and evict from the back until `bytes <= cap`. Construct with `newArtCache(128 << 20)` in `New`. Add a `miss bool` to `artEntry`; a cached miss serves 404 with the same `Cache-Control`.

Split the handler into one `serveArtFrom(w, r, key string, fetch func() (io.ReadCloser, string, error))` and three thin handlers over it. Add `Hub.PersonImage` / `Hub.UserImage` passthroughs beside the existing `Hub.Image`.

- [ ] **Step 4: Update the frontend references**

`grep -rn "now-playing/art" web/src` and repoint each to `/api/art/item/`.

- [ ] **Step 5: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/... -v && cd web && npm run test && npx tsc -b`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/api internal/live web/src
git commit -m "feat(api): one art proxy for items, people and avatars"
```

---

### Task 15: The link resolver

**Files:**
- Create: `internal/store/refs.go`, `internal/scheduler/refs.go`
- Modify: `internal/scheduler/scheduler.go` (new optional dependency), `cmd/ephyra/main.go` (wire it)
- Test: `internal/store/refs_test.go`, `internal/scheduler/refs_test.go`

**Interfaces:**
- Produces:
  ```go
  func (s *Store) ReadJFRefs(ctx context.Context) (map[string]string, error) // "kind\x1fname" -> id
  func (s *Store) DueJFRefs(ctx context.Context, kind string, names []string, now time.Time) ([]string, error)
  func (s *Store) PutJFRef(ctx context.Context, kind, name, id string, now time.Time) error
  func (s *Store) PutJFMiss(ctx context.Context, kind, name string, now time.Time) error
  func (s *Store) ChartedNames(ctx context.Context) (people, genres []string, err error)
  ```
  Resolved refs are never re-checked. Misses back off: retry after `1 << min(miss_count-1, 6)` days, so 1, 2, 4 … capped at 64, and effectively at the 90-day ceiling in the spec.

**The Scheduler has no Jellyfin client today** — it holds only `st`, `src`, `cfg`, `log`.
Give it one, optional, so an unconfigured or unreachable Jellyfin leaves the
resolver inert rather than breaking the constructor for every existing caller:

```go
// RefClient is the slice of *jellyfin.Client the resolver needs. nil means
// Jellyfin is not configured, and every name stays unlinked.
type RefClient interface {
	FindPerson(ctx context.Context, name string) (string, error)
	Genres(ctx context.Context) (map[string]string, error)
}

// SetRefClient wires the resolver's Jellyfin access after construction, so
// New's signature -- and every test that calls it -- stays put.
func (s *Scheduler) SetRefClient(c RefClient) { s.refc = c }
```

Call it from `cmd/ephyra/main.go` only where a Jellyfin client is already built for
the live subsystem. `resolveRefs` returns immediately when `s.refc == nil`.

- [ ] **Step 1: Write the failing test**

```go
func TestJFMissBacksOff(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	day0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	if err := st.PutJFMiss(ctx, "person", "Ghost", day0); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueJFRefs(ctx, "person", []string{"Ghost"}, day0.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Error("a miss should not retry within the first day")
	}

	due, _ = st.DueJFRefs(ctx, "person", []string{"Ghost"}, day0.Add(25*time.Hour))
	if len(due) != 1 {
		t.Fatal("first retry is due after a day")
	}
	if err := st.PutJFMiss(ctx, "person", "Ghost", day0.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Second miss doubles the wait. A flat retry would bill every permanently
	// unresolvable name a lookup forever, and that set only grows.
	due, _ = st.DueJFRefs(ctx, "person", []string{"Ghost"}, day0.Add(26*time.Hour))
	if len(due) != 0 {
		t.Error("second wait should be two days, not one")
	}
}

func TestJFHitIsNeverRechecked(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	day0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	if err := st.PutJFRef(ctx, "person", "Ada Vex", "abc123", day0); err != nil {
		t.Fatal(err)
	}
	due, err := st.DueJFRefs(ctx, "person", []string{"Ada Vex"}, day0.AddDate(5, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatal("a resolved name should never be looked up again")
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestJF -v`
Expected: FAIL — undefined `PutJFMiss`.

- [ ] **Step 3: Implement the store side**

`DueJFRefs` returns names with no row, plus rows where `jf_id = ''` and `checked_at` is older than the backoff. `PutJFMiss` upserts with `miss_count = miss_count + 1`.

- [ ] **Step 4: Implement the resolver**

`internal/scheduler/refs.go`:

```go
const refLookupsPerRun = 200

// resolveRefs fills the name -> Jellyfin-id cache for the names that actually
// charted. Best effort in every direction: an unresolved name renders as plain
// text, and the watch job's success never depends on Jellyfin answering.
func (s *Scheduler) resolveRefs(ctx context.Context) {
	cl := s.refc
	if cl == nil {
		return
	}
	people, genres, err := s.st.ChartedNames(ctx)
	if err != nil {
		s.log.Warn("ref resolve: charted names", "err", err)
		return
	}
	now := time.Now()

	if due, err := s.st.DueJFRefs(ctx, "genre", genres, now); err == nil && len(due) > 0 {
		if byName, err := cl.Genres(ctx); err == nil {
			for _, name := range due {
				if id := byName[name]; id != "" {
					_ = s.st.PutJFRef(ctx, "genre", name, id, now)
				} else {
					_ = s.st.PutJFMiss(ctx, "genre", name, now)
				}
			}
		}
	}

	due, err := s.st.DueJFRefs(ctx, "person", people, now)
	if err != nil {
		s.log.Warn("ref resolve: due people", "err", err)
		return
	}
	for _, name := range due[:min(len(due), refLookupsPerRun)] {
		id, err := cl.FindPerson(ctx, name)
		if err != nil {
			s.log.Warn("ref resolve failed, will retry", "name", name, "err", err)
			return // Jellyfin is unhappy; stop rather than walk the rest into the same error
		}
		if id == "" {
			_ = s.st.PutJFMiss(ctx, "person", name, now)
			continue
		}
		_ = s.st.PutJFRef(ctx, "person", name, id, now)
	}
}
```

`ChartedNames` reads `SELECT DISTINCT person FROM agg_profile_people` and `SELECT DISTINCT key FROM agg_profile_taste WHERE dim = 'genre'`.

Add a scheduler test that a nil `refc` makes the watch job succeed with no
lookups, and one that a `FindPerson` returning an error still leaves the job
reporting success.

- [ ] **Step 5: Call it from the watch job**

As the last statement before `SetRefreshMeta`:

```go
	s.resolveRefs(ctx)
```

- [ ] **Step 6: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/... -v && make lint`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/store internal/scheduler
git commit -m "feat: resolve chart names to jellyfin ids with backoff on misses"
```

---

### Task 16: Fill `jf_url` in the overview payload

**Files:**
- Modify: `internal/store/reads_people.go`, `internal/store/reads_overview.go`
- Modify: `internal/config/config.go` if the Jellyfin base URL is not already reachable from the API layer
- Test: `internal/store/reads_people_test.go`

**Interfaces:**
- Consumes: `dim_jf_ref`, `Client.ServerID`, `cfg.JellyfinURL`.
- Produces: `PersonDTO.JFURL`, `TopItemDTO.JFURL`, `GenreDTO.JFURL`, and `PlayRow.JFURL`, populated or `""`.

- [ ] **Step 1: Write the failing test**

```go
func TestPeopleLinkOnlyWhenResolved(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedPeopleRows(t, st, "u1", []string{"Ada Vex", "Ghost"})
	if err := st.PutJFRef(ctx, "person", "Ada Vex", "abc123", time.Now()); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadProfilePeople(ctx, "u1", "all", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range got {
		switch p.Person {
		case "Ada Vex":
			if !strings.Contains(p.JFURL, "abc123") {
				t.Errorf("Ada Vex jf_url = %q, want the resolved id in it", p.JFURL)
			}
		case "Ghost":
			// Unresolved renders as plain text. Resolving ahead of the click,
			// rather than on it, is what keeps a dead link from ever rendering.
			if p.JFURL != "" {
				t.Errorf("Ghost jf_url = %q, want empty", p.JFURL)
			}
		}
	}
}
```

- [ ] **Step 2: Run and watch it fail**

Run: `CGO_ENABLED=0 go test ./internal/store/ -run TestPeopleLink -v`
Expected: FAIL — `jf_url` always empty.

- [ ] **Step 3: Implement**

`LEFT JOIN dim_jf_ref ON kind = 'person' AND name = person` in the people read, likewise `'genre'` for genres. Build the URL in one place:

```go
// jfLink builds a Jellyfin web-client URL. Items need no lookup -- BaseItems.Id
// is the API's item id -- but people and genres are synthesized id spaces, so
// those come from dim_jf_ref and are empty until resolved.
func jfLink(base, serverID, kind, id string) string {
	if base == "" || id == "" {
		return ""
	}
	if kind == "genre" {
		return base + "/web/#/list?parentId=" + id + "&serverId=" + serverID
	}
	return base + "/web/#/details?id=" + id + "&serverId=" + serverID
}
```

Items pass their own `item_id` straight in.

- [ ] **Step 4: Run and watch it pass**

Run: `CGO_ENABLED=0 go test ./internal/... -v && make lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store internal/config
git commit -m "feat(store): link charts into the jellyfin web client"
```

---

### Task 17: Backend checkpoint

- [ ] **Step 1: Full suite**

Run: `make test && make lint`
Expected: everything green.

- [ ] **Step 2: Run it against the fixture**

```bash
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/jellyfin.db < testdata/library.fixture.sql
JELLYFIN_URL=x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf \
  STORE_PATH=/tmp/ephyra.db WORK_DIR=/tmp/ephyra-work go run ./cmd/ephyra
```

Then, in another shell:

```bash
curl -s 'localhost:8097/api/profile' | head -c 400
curl -s 'localhost:8097/api/profile/<userID>/overview?range=all' | head -c 800
curl -s 'localhost:8097/api/profile/<userID>/plays?limit=5' | head -c 800
```

Expected: envelopes with `data` / `meta`, `[]` rather than `null` for empty lists, and `now_playing: null` (Jellyfin is not reachable at `JELLYFIN_URL=x`, which is the point — the page still serves).

- [ ] **Step 3: Commit anything the checkpoint fixed**

---
### Task 18: DESIGN GATE — stop here

**This task writes no code. Do not start Task 19 until it is closed.**

The backend is complete and exercisable through `curl` at this point; the visual
treatment of the overview is the user's decision, not the implementer's.

- [ ] **Step 1: Build the options**

Prepare two or three concrete visual directions for the overview, each covering:
- the header block — avatar, the three numbers, the activity strip
- one row of the play history — poster size, how `Series · S01E04 — Title` sets, where the relative time and duration sit
- one people chart — tile grid with faces versus a dense ranked list, and how the hours and breadth rankings sit next to each other
- what a missing image looks like, since roughly half the people in a real library have none
- where the "finished only" toggle and the library filter live

Work within the existing `@theme` tokens in `web/src/index.css` and the hand-rolled
components in `web/src/components/`. Match the density of the existing pages.

- [ ] **Step 2: Show them to the user and get a decision**

Static mockups are enough. Do not implement all of them.

- [x] **Step 3: Write the choice into this plan** — done, below.

---

## The chosen direction — "Ledger"

Decided 2026-09-13. Tasks 19-22 implement **this** and nothing else.

**The read:** dense and typographic, at the same density as Watch Stats and
Cleanup. Ranking is carried by number and bar; portraits are small enough that
the library's patchy artwork never dominates. Nothing on this page should look
unlike the rest of Ephyra.

**Page chrome**, one row above everything: `Overview | Stats` tabs on the left,
then the library filter, then the range selector, then — pushed right — a
`Finished only` pill. That pill **defaults on**.

**Header**, one `Panel`, all on a single row that wraps at phone width:
- 60px round avatar, gradient cyan→violet with the initial when Jellyfin has no
  image for the user.
- Display name in `--font-display` at 24px.
- Three numbers inline, each a mono value over an uppercase label: **plays**,
  **watched**, **since**.
- The activity strip right-aligned in the same panel: 9px cells, 2px gaps,
  four steps from `#141c33` to full `--color-cyan`.

**Body**, a two-column split — recents at `minmax(0, 340px)`, charts filling the
rest — collapsing to one column under 820px with recents first.

**Recents column.** 30×44 poster, title, a muted subtitle, and a right-aligned
block holding the date over the duration. A finished play gets a cyan `✓` before
its duration. A live session pins to the top of the list with a `--color-mote`
tinted row and a `live` pill. An item gone from Jellyfin says "no longer in the
library" in its subtitle and keeps its watch time.

**Charts grid**, two across. Actors get **two adjacent panels** — `by hours` in
cyan, `by breadth` in violet — so the two rankings can be read against each
other; that disagreement is the point. Directors, series, top items and genres
each get one panel.

**A ranked row** is: mono rank number (16px wide, right-aligned) · 28px round
face chip · name · mono tabular value. Under each row, a bar inset to the name's
left edge. A resolved name is a link with a cyan bottom-border; an unresolved one
is plain text with no border and no hover.

**Faces: yes, with an initials fallback.** The chip shows the proxied portrait
when there is one and a `#1b2647` circle with muted initials when there is not.
Roughly half the people in a real library land in the fallback, so it is a normal
state and must look deliberate, never like a broken image. Genres and series have
no chip at all — the row starts at the name.

---

### Task 19: Frontend — route split and tabs

**Files:**
- Create: `web/src/routes/profile.overview.tsx`, `web/src/routes/profile.stats.tsx`
- Modify: `web/src/routes/profile.tsx` (becomes the shell), `web/src/router.tsx` or wherever routes register
- Test: `web/src/routes/profile.test.tsx`

**Interfaces:**
- Produces: route `/profile/$user` with search params `{ range, tab, library }`; `tab` is `"overview" | "stats"`, defaulting to `"overview"`.

- [ ] **Step 1: Write the failing test**

```tsx
it("defaults to the overview tab", async () => {
  renderProfileAt("/profile/u1");
  expect(await screen.findByRole("tab", { name: /overview/i })).toHaveAttribute(
    "aria-selected",
    "true",
  );
});

it("keeps the four analytical panels reachable under the stats tab", async () => {
  renderProfileAt("/profile/u1?tab=stats");
  expect(await screen.findByText(/completion/i)).toBeInTheDocument();
});

it("redirects the old query-param URL to the path form", async () => {
  renderProfileAt("/profile?user=u1&range=90d");
  await waitFor(() => expect(currentPath()).toBe("/profile/u1"));
  expect(currentSearch().range).toBe("90d");
});
```

Follow whatever render/router harness `profile.test.tsx` already uses rather than introducing a new one.

- [ ] **Step 2: Run and watch it fail**

Run: `cd web && npx vitest run src/routes/profile.test.tsx`
Expected: FAIL — no such route.

- [ ] **Step 3: Implement**

Move the four panels into `profile.stats.tsx` **unchanged** — no edits beyond the
imports and the component name. Add the shell with the tab control and the
redirect. Keep the existing `SegmentedControl` for the range.

- [ ] **Step 4: Run and watch it pass**

Run: `cd web && npm run test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "refactor(web): split the profile page into overview and stats tabs"
```

---

### Task 20: Frontend — header and activity strip

**Files:**
- Create: `web/src/components/Avatar.tsx`, `web/src/components/Art.tsx`, `web/src/components/ActivityStrip.tsx`
- Modify: `web/src/routes/profile.overview.tsx`, `web/src/api/queries.ts`, `web/src/api/types.ts`
- Test: `web/src/routes/profile.overview.test.tsx`

**Interfaces:**
- Consumes: `GET /api/profile/{userID}/overview`.
- Produces: `<Art src kind fallback />`, `<Avatar userId name />`, `<ActivityStrip days />`.

- [ ] **Step 1: Write the failing test**

```tsx
it("shows a placeholder instead of a broken image when art 404s", async () => {
  render(<Art src="/api/art/person?name=Ghost" fallback="Ghost" />);
  fireEvent.error(screen.getByRole("img"));
  expect(await screen.findByText("G")).toBeInTheDocument();
});

it("renders the three headline numbers", async () => {
  renderOverview({ plays: 412, watch_sec: 360000, since: "2024-02-11 20:14:00" });
  expect(await screen.findByText("412")).toBeInTheDocument();
  expect(screen.getByText(/100h/)).toBeInTheDocument();
  expect(screen.getByText(/2024/)).toBeInTheDocument();
});
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd web && npx vitest run src/routes/profile.overview.test.tsx`
Expected: FAIL — no such module.

- [ ] **Step 3: Implement**

`Art` renders an `<img>` and swaps to a typographic block on `onError`. Use the
`@theme` tokens; do not introduce new colours. Reuse `fmtInt` / `fmtRuntime` from
`web/src/lib/format.ts`.

`ActivityStrip` reuses the Watch Stats heatmap's cell scale so the two pages agree
on what "a lot" looks like.

- [ ] **Step 4: Run and watch it pass**

Run: `cd web && npm run test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): profile header, avatar and activity strip"
```

---

### Task 21: Frontend — the play history list

**Files:**
- Create: `web/src/components/PlayList.tsx`
- Modify: `web/src/routes/profile.overview.tsx`, `web/src/api/queries.ts`
- Test: `web/src/components/PlayList.test.tsx`

**Interfaces:**
- Consumes: `GET /api/profile/{userID}/plays`, `useInfiniteQuery`.

- [ ] **Step 1: Write the failing test**

```tsx
it("loads the next page when the sentinel scrolls into view", async () => {
  const fetchSpy = stubPlays([
    { plays: [play("a"), play("b")], next_cursor: { at: "2026-09-01 10:00:00", row_id: 7 } },
    { plays: [play("c")], next_cursor: null },
  ]);
  render(<PlayList userId="u1" range="all" library="" />);

  expect(await screen.findByText("a")).toBeInTheDocument();
  triggerIntersection();
  expect(await screen.findByText("c")).toBeInTheDocument();
  expect(fetchSpy).toHaveBeenCalledTimes(2);
});

it("stops asking once the cursor comes back null", async () => {
  const fetchSpy = stubPlays([{ plays: [play("a")], next_cursor: null }]);
  render(<PlayList userId="u1" range="all" library="" />);
  await screen.findByText("a");
  triggerIntersection();
  triggerIntersection();
  expect(fetchSpy).toHaveBeenCalledTimes(1);
});
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd web && npx vitest run src/components/PlayList.test.tsx`
Expected: FAIL — no such module.

- [ ] **Step 3: Implement**

`getNextPageParam: (last) => last.data.next_cursor ?? undefined`. Episodes render
as `Series · S01E04 — Title`, movies as the title alone. The list ignores the range
selector — it is a history, not a window — and the component should carry no
`range` prop once that is settled; drop it from the test above if it is not used.

- [ ] **Step 4: Run and watch it pass**

Run: `cd web && npm run test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): infinite play history list"
```

---

### Task 22: Frontend — the ranked charts

**Files:**
- Create: `web/src/components/RankGrid.tsx`
- Modify: `web/src/routes/profile.overview.tsx`
- Test: `web/src/components/RankGrid.test.tsx`

**Interfaces:**
- Consumes: the `people`, `items` and `genres` arrays from the overview payload.

- [ ] **Step 1: Write the failing test**

```tsx
it("switches metric when the finished-only toggle flips", async () => {
  const rows = [
    { person: "Ada Vex", kind: "actor", watch_sec: 9000, watch_sec_played: 100,
      distinct_titles: 3, distinct_titles_played: 1, plays: 5, plays_played: 1, jf_url: "" },
  ];
  render(<RankGrid rows={rows} metric="watch_sec" playedOnly={false} />);
  expect(await screen.findByText(/2h 30m/)).toBeInTheDocument();

  render(<RankGrid rows={rows} metric="watch_sec" playedOnly={true} />);
  expect(await screen.findByText(/1m 40s/)).toBeInTheDocument();
});

it("renders an unresolved name as plain text, not a link", async () => {
  render(<RankGrid rows={[{ person: "Ghost", kind: "actor", jf_url: "" } as never]}
                   metric="watch_sec" playedOnly={false} />);
  expect(screen.queryByRole("link", { name: "Ghost" })).toBeNull();
  expect(await screen.findByText("Ghost")).toBeInTheDocument();
});
```

- [ ] **Step 2: Run and watch it fail**

Run: `cd web && npx vitest run src/components/RankGrid.test.tsx`
Expected: FAIL — no such module.

- [ ] **Step 3: Implement**

One `RankGrid` serves actors, directors, series, items and genres; the differences
are the image source and the label. The actor and director sections render it
twice — once on `watch_sec`, once on `distinct_titles` — because a single binge
would otherwise hand every slot to one show's regular cast.

The toggle is local state in `profile.overview.tsx`, defaults on, and changes no
request: both metrics are already in the payload.

- [ ] **Step 4: Run and watch it pass**

Run: `cd web && npm run test && npx tsc -b && npm run lint`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src
git commit -m "feat(web): ranked people, item and genre charts"
```

---

### Task 23: Documentation

**Files:**
- Modify: `docs/schema-notes.md`, `CLAUDE.md`, `README.md`

- [ ] **Step 1: `docs/schema-notes.md`**

Add a **People** section recording, with the numbers from the spec's §12: the
one-row-per-`(name, type)` shape of `Peoples`; that `Peoples.Id` is not the API's
person id while `BaseItems.Id` is the API's item id; the credit coverage split
between movies and episodes; and `GET /Persons/{name}/Images/Primary`. Add to the
`UserData` section that it is `ON DELETE CASCADE` from `BaseItems`.

- [ ] **Step 2: `CLAUDE.md`**

Two additions:
- Under the `internal/api` bullet: `GET /api/profile/{userID}/plays` reads
  `playback_events` directly. It is the one handler that does not read a
  pre-rolled `agg_*` table.
- Under the `internal/store` bullet: `dim_credit`, `dim_played` and `dim_jf_ref`
  join `playback_events` as tables that are upsert-only and never rewritten.
  Pruning rows for items Jellyfin no longer has would break the charts' durability
  across deletions, which is the reason they exist.

- [ ] **Step 3: `README.md`**

Add a credit-coverage check to "Test against a real library":

```sql
SELECT b.Type, count(*) items,
       sum(CASE WHEN EXISTS(SELECT 1 FROM PeopleBaseItemMap m WHERE m.ItemId = b.Id)
                THEN 1 ELSE 0 END) with_credits
FROM BaseItems b
WHERE b.Type LIKE '%Movie' OR b.Type LIKE '%Episode'
GROUP BY b.Type;
```

Low episode coverage is expected and is what the series fallback handles.

- [ ] **Step 4: Verify and commit**

Run: `make test && make lint && cd web && npm run test && npm run lint && npx tsc -b`

```bash
git add docs CLAUDE.md README.md
git commit -m "docs: people schema, the direct-spine read, and the never-pruned dimensions"
```

---

## Self-review notes

Checked against the spec, section by section.

- §4.1 dimension tables → Task 1 (schema) + Task 3 (semantics, with the
  deletion-durability tests that are the point of them).
- §4.2 aggregates → Task 1 + Task 8, library-scoped.
- §4.3 index → Task 1.
- §5 source reads → Task 2, GuestStar folding and the Producer exclusion both
  tested.
- §6 aggregation → Tasks 5–7. Attribution fallback, the played filter, both
  orderings and the cap each have a test.
- §7 API → Tasks 11 and 12, both with the `library=all` mapping.
- §8 art proxy → Task 14, byte budget and negative cache both tested.
- §9 deep links → Tasks 13, 15, 16. Exact-name matching and the backoff are
  tested; the 200-per-run cap is a constant in Task 15.
- §10 frontend → Tasks 19–22, gated on Task 18.
- §11 degradation → the empty-spine and unconfigured-live cases are in Task 12;
  the art-down case is Task 14's placeholder test; the unresolved-name case is
  Task 16.
- §13 testing → distributed across the tasks rather than collected at the end.
- §14 docs → Task 23.

Known soft spots for the implementer, stated rather than hidden:

- Task 2's fixture edit needs real IDs out of `testdata/library.fixture.sql`. The
  plan names the four items by role, not by ID, because the IDs are not in front
  of me.
- Task 3 references `aggregate.UserItem`, which Task 5 defines. Define it in
  Task 3 and let Task 5 use it, or run Task 5 first.
- Task 12's `sessionForUser` and the Now Playing session DTO should reuse whatever
  `internal/live` already exports; do not add a parallel type.
- Task 16 assumes the Jellyfin base URL is reachable from the store layer. If it
  is not, thread it through rather than reading the environment twice.
- Task 10's implementation sketch stops at the scan loop. The query and the
  cursor rule are given in full; the scan is the ordinary `rows.Next()` shape used
  everywhere else in `internal/store`.
