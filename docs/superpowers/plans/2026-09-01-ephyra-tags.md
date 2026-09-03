# Tags Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface freeform Jellyfin item tags (mostly TMDb keywords) as a tag section on Library Overview (coverage + cloud + co-occurrence explorer) and additions to the Profiles taste fingerprint (tag ShareBars + signature tags + two-user overlap).

**Architecture:** Tags travel the same path `Genres` already travels: read from `BaseItems.Tags` in `internal/source/file`, carried on `source.LibraryItem` / `source.PlaybackEvent`, rolled up by the pure `internal/aggregate` functions, persisted by `internal/store` (reusing the generic `agg_distribution` / `agg_totals` tables plus two small new tables), served by the existing `/api/library/overview` and `/api/profile/{userID}` handlers, rendered in `web/src/routes/{library,profile}.tsx`. Episode plays inherit their parent Series' tags. No new page, no new route.

**Tech Stack:** Go 1.25 (`CGO_ENABLED=0`, pure-Go `modernc.org/sqlite`); React 19 + Vite + TypeScript + Tailwind v4; Vitest; Biome.

**Spec:** `docs/superpowers/specs/2026-09-01-ephyra-tags-design.md` — read it alongside this plan.

## Global Constraints

- Go module `github.com/andriykohut/ephyra`, floor `go 1.25`. Always `CGO_ENABLED=0`.
- `make test` → `CGO_ENABLED=0 go test ./...`. `make lint` → `go vet ./...` + `golangci-lint run` (golangci-lint **v2**).
- Frontend: `cd web && npm run test` (Vitest), `npm run lint` (`biome ci .`), `npx tsc -b`.
- Migrations: forward-only, integer-prefixed by filename, the runner owns `schema_migrations` — a migration must **not** `CREATE` or touch it.
- `agg_*` tables are fully rewritten per refresh in one transaction. `playback_events` is the one append-only table — never rewritten, upsert-on-`dedup_hash`.
- Every store read runs slices through `orEmpty` so JSON lists serialize as `[]`, never `null`.
- All user/item/series ids in the store are canonical dashless-lowercase (`source.CanonID`). `jellyfin.db` ids are dashed-uppercase and normalized on read.
- Tag normalization: `strings.ToLower(strings.TrimSpace(x))`, drop empties, dedup within a single item. No fuzzy merging, no stop-list.
- Co-occurrence noise floor: `minSupport = 3` items per pair. Caps: tag cloud ≤ 120 tags, `TagPairs` ≤ 400 pairs.
- `signature_tags` support floor: a tag needs ≥ 2 plays and ≥ 120 watch-seconds on the user row to be eligible.
- Docs/comments: casual, deadpan. No exclamation marks, no "simply" / "powerful".

---

### Task 1: Migration 0004 + spine `item_tags` column

**Files:**
- Create: `internal/store/migrations/0004_tags.sql`
- Modify: `internal/store/spine.go` (add `joinTags`/`splitTags`; `AppendPlaybackEvents` INSERT + `ON CONFLICT`; `ReadPlaybackEvents` SELECT/scan)
- Test: `internal/store/spine_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent` (add field `ItemTags []string` in Task 3 — for this task, reference it; Go zero value is `nil`, which `joinTags(nil)` renders as `""`).
- Produces: `playback_events.item_tags TEXT NOT NULL DEFAULT ''`; tables `agg_library_tag_pairs(tag_a,tag_b,items)` and `agg_profile_tag_overlap(user_a,user_b,range,cosine,shared)`. `store.joinTags([]string) string` / `store.splitTags(string) []string`.

- [ ] **Step 1: Add the `ItemTags` field to `source.PlaybackEvent` now** (so this task compiles independently). In `internal/source/source.go`, in the `PlaybackEvent` struct after `ItemGenres []string`:

```go
	ItemGenres []string // nil when unknown
	ItemTags   []string // nil when unknown; for episodes these are the parent Series' tags
	ItemYear   int      // 0 when unknown
```

- [ ] **Step 2: Write the migration**

Create `internal/store/migrations/0004_tags.sql`:

```sql
-- Freeform item tags (TMDb keywords). The tag cloud reuses agg_distribution
-- (dimension 'tag') and coverage reuses agg_totals (tags.*_items); only these
-- two shapes are new. Both full-rewritten per refresh. Forward-only; the runner
-- owns schema_migrations, do not touch it here.

ALTER TABLE playback_events ADD COLUMN item_tags TEXT NOT NULL DEFAULT '';

CREATE TABLE agg_library_tag_pairs (
  tag_a TEXT NOT NULL,
  tag_b TEXT NOT NULL,   -- writer enforces tag_a < tag_b
  items INTEGER NOT NULL,
  PRIMARY KEY (tag_a, tag_b)
);
CREATE INDEX ix_library_tag_pairs_a ON agg_library_tag_pairs (tag_a, items DESC);
CREATE INDEX ix_library_tag_pairs_b ON agg_library_tag_pairs (tag_b, items DESC);

CREATE TABLE agg_profile_tag_overlap (
  user_a TEXT NOT NULL,
  user_b TEXT NOT NULL,   -- writer enforces user_a < user_b
  range  TEXT NOT NULL,
  cosine REAL NOT NULL,
  shared TEXT NOT NULL,   -- pipe-joined top shared tags
  PRIMARY KEY (user_a, user_b, range)
);
```

- [ ] **Step 3: Write the failing test**

In `internal/store/spine_test.go`, add:

```go
func TestSpine_ItemTagsRoundTripAndRefresh(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t) // use whatever the file's other tests use to get a *Store

	ev := source.PlaybackEvent{
		At: time.Date(2025, 1, 6, 20, 10, 0, 0, time.UTC),
		UserID: "u1", ItemID: "i1", ItemType: "movie", Method: "DirectPlay",
		PlayDurationSec: 3600, ItemTags: []string{"heist", "vault"},
	}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{ev}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadPlaybackEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].ItemTags) != 2 || got[0].ItemTags[0] != "heist" || got[0].ItemTags[1] != "vault" {
		t.Fatalf("tags round-trip: %+v", got)
	}

	// same dedup coordinates, new tags -> refreshed in place
	ev.ItemTags = []string{"heist", "vault", "dystopia"}
	if err := st.AppendPlaybackEvents(ctx, []source.PlaybackEvent{ev}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ReadPlaybackEvents(ctx)
	if len(got) != 1 || len(got[0].ItemTags) != 3 {
		t.Fatalf("tags refresh-on-conflict: %+v", got)
	}
}
```

If `spine_test.go` has no store-open helper, copy the pattern from `writes_profile_test.go` (`store.Open(ctx, t.TempDir()+"/s.db")`).

- [ ] **Step 4: Run it, expect failure**

Run: `go test ./internal/store/ -run TestSpine_ItemTagsRoundTripAndRefresh -v`
Expected: FAIL — `item_tags` column missing / `ItemTags` not scanned.

- [ ] **Step 5: Implement spine changes**

In `internal/store/spine.go`, next to `joinGenres` / `splitGenres`:

```go
func joinTags(ts []string) string { return strings.Join(ts, "|") }
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "|")
}
```

In `AppendPlaybackEvents`, the `const q`:
- add `item_tags` to the column list and one more `?` to `VALUES`;
- add `item_tags = excluded.item_tags` to the `ON CONFLICT(dedup_hash) DO UPDATE SET` list.

In the `tx.ExecContext(ctx, q, ...)` argument list, add `joinTags(e.ItemTags)` in the same position as the column (after `joinGenres(e.ItemGenres)`, before `spineDedupHash(e)`).

In `ReadPlaybackEvents`: add `item_tags` to the `SELECT` column list; add `var ... tags string` (extend the existing `var atRaw, genres string`); add `&tags` to `rows.Scan` in the matching position; after `e.ItemGenres = splitGenres(genres)` add `e.ItemTags = splitTags(tags)`.

- [ ] **Step 6: Run tests**

Run: `go test ./internal/store/ -run TestSpine -v`
Expected: PASS (new test + existing spine tests).

- [ ] **Step 7: Full store package + vet**

Run: `go test ./internal/store/... && go vet ./internal/store/...`
Expected: PASS. (Migration 0004 now auto-applies in every store test.)

- [ ] **Step 8: Commit**

```bash
git add internal/store/migrations/0004_tags.sql internal/store/spine.go internal/store/spine_test.go internal/source/source.go
git commit -m "$(cat <<'EOF'
feat(store): add item_tags to the playback spine + tag migration

0004_tags.sql adds playback_events.item_tags and the agg_library_tag_pairs /
agg_profile_tag_overlap tables. Spine upsert carries and refreshes item_tags
like item_genres.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 2: Source reads `BaseItems.Tags` into the library snapshot

**Files:**
- Modify: `internal/source/source.go` (add `Tags []string` to `LibraryItem`)
- Modify: `internal/source/file/queries.go` (`libraryQuery` SELECT, scan, `splitTags` helper)
- Modify: `testdata/library.fixture.sql` (`Tags` column + `UPDATE` rows)
- Modify: `docs/schema-notes.md` (document the `Tags` column)
- Test: `internal/source/file/queries_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `source.LibraryItem.Tags []string` (normalized, deduped). `file.splitTags(string) []string`.

- [ ] **Step 1: Add the struct field**

`internal/source/source.go`, in `LibraryItem` after `Genres []string`:

```go
	Genres        []string
	Tags          []string // normalized: lower(trim), empties dropped, deduped within the item
```

- [ ] **Step 2: Extend the fixture**

In `testdata/library.fixture.sql`:

- In `CREATE TABLE BaseItems (...)`, add a column after `Genres TEXT,`:

```sql
  Genres         TEXT,
  Tags           TEXT,
```

- After the episode `INSERT INTO BaseItems ... VALUES (...)` block (before the "excluded" inserts), add:

```sql
-- item tags (TMDb-keyword-ish). Alpha's raw value exercises splitTags:
-- mixed case, padding, and a duplicate all normalize away.
UPDATE BaseItems SET Tags = 'Heist| vault |dystopia|heist' WHERE Id = '00000000-0000-0000-0000-00000000000A';
UPDATE BaseItems SET Tags = 'heist|vault'                  WHERE Id = '00000000-0000-0000-0000-00000000000B';
UPDATE BaseItems SET Tags = 'heist|vault'                  WHERE Id = '00000000-0000-0000-0000-00000000000C';
UPDATE BaseItems SET Tags = 'heist|dystopia'              WHERE Id = '00000000-0000-0000-0000-00000000000D';
-- the "Some Show" series row; its episodes carry no Tags of their own
UPDATE BaseItems SET Tags = 'slow burn|dystopia'          WHERE Id = '00000000-0000-0000-0000-0000000000F0';
```

Resulting library-snapshot facts (episodes have NULL `Tags` — series inheritance is playback-only): `heist` on A,B,C,D; `vault` on A,B,C; `dystopia` on A,D. Tagged items = 4 of 6. Pair `heist|vault` = 3 (survives `minSupport`), `dystopia|heist` = 2 (cut), `dystopia|vault` = 1 (cut).

- [ ] **Step 3: Write the failing test**

In `internal/source/file/queries_test.go`, add:

```go
func TestLibraryFacts_Tags(t *testing.T) {
	f := newFS(t, filepath.Dir(filepath.Dir(testsupport.LibraryFixtureDB(t))))
	// If newFS needs a data dir laid out a certain way, mirror the existing
	// TestLibraryFacts setup in this file instead of the line above.
	snap, err := f.LibraryFacts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string][]string{}
	for _, it := range snap.Items {
		byName[it.Name] = it.Tags
	}
	if got := byName["Alpha"]; len(got) != 3 || got[0] != "heist" || got[1] != "vault" || got[2] != "dystopia" {
		t.Fatalf("Alpha tags normalized wrong: %#v", got)
	}
	if got := byName["Charlie"]; len(got) != 2 || got[0] != "heist" || got[1] != "vault" {
		t.Fatalf("Charlie tags: %#v", got)
	}
	if got := byName["S1E1"]; len(got) != 0 {
		t.Fatalf("episode should have no own tags in the library snapshot: %#v", got)
	}
}
```

Match the harness the sibling `TestLibraryFacts*` tests in this file already use to construct a `*FileSource` — reuse that exact setup rather than the sketch above.

- [ ] **Step 4: Run it, expect failure**

Run: `go test ./internal/source/file/ -run TestLibraryFacts_Tags -v`
Expected: FAIL — `Tags` always nil.

- [ ] **Step 5: Implement**

In `internal/source/file/queries.go`:

- Add the helper near `splitGenres`:

```go
func splitTags(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Split(s, "|") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}
```

- In `libraryQuery`, add `COALESCE(i.Tags, '')` immediately after `COALESCE(i.Genres, ''),` in the `SELECT` list.
- In `defaultQueryLibrary`, extend the scan `var` block: `name, typ, dateRaw, genres, library, itemPath string` → add `tags`. Add `&tags` to `rows.Scan(...)` in the same position as the SELECT column (right after `&genres`).
- In the `source.LibraryItem{...}` literal, add `Tags: splitTags(tags),` after `Genres: splitGenres(genres),`.

- [ ] **Step 6: Update `docs/schema-notes.md`**

In the `## BaseItems` section, change the field line to include `Tags`:

```
`ProductionYear` INT, `Genres` TEXT (pipe-delimited), `Tags` TEXT (pipe-delimited;
freeform, usually TMDb keywords, often sparse), `TopParentId` TEXT,
```

Add a bullet under that section:

```
- `Tags` is Jellyfin's freeform item-tag list. TMDb metadata agents write keyword
  lists here. Verified present as a pipe-delimited `BaseItems` column on 10.11;
  re-check on a real DB after any Jellyfin upgrade. Episodes generally have none —
  the watch path inherits the parent Series' tags.
```

- [ ] **Step 7: Run tests**

Run: `go test ./internal/source/... && go vet ./internal/source/...`
Expected: PASS. Existing `TestLibraryFacts` genre assertions (`queries_test.go:45`) untouched — `splitGenres` unchanged.

- [ ] **Step 8: Commit**

```bash
git add internal/source/source.go internal/source/file/queries.go internal/source/file/queries_test.go testdata/library.fixture.sql docs/schema-notes.md
git commit -m "$(cat <<'EOF'
feat(source): read BaseItems.Tags into the library snapshot

Adds LibraryItem.Tags (lowercased, trimmed, deduped). Fixture and schema notes
updated.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 3: Playback events carry item tags (movie self, episode ← series)

**Files:**
- Modify: `internal/source/file/playback.go` (`itemInfo` gains `tags`; item query; new series-tags query; enrich loop)
- Modify: `testdata/playback_reporting.fixture.sql` (only if more event coverage is wanted — the existing Alpha + E1 + E2 rows already suffice)
- Test: `internal/source/file/file_test.go`

**Interfaces:**
- Consumes: `source.LibraryItem` / `jellyfin.db` copy; `file.splitTags` (Task 2).
- Produces: `source.PlaybackEvent.ItemTags` populated — a movie's own tags, an episode's parent-Series tags.

- [ ] **Step 1: Write the failing test**

In `internal/source/file/file_test.go`, near the existing `ItemGenres` assertions, add:

```go
func TestPlaybackEvents_ItemTags(t *testing.T) {
	root := testsupport.TwoDBLayout(t)
	f := newFS(t, root)
	evs, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var sawMovie, sawEpisode bool
	for _, e := range evs {
		switch e.ItemName {
		case "Alpha": // movie -> its own tags
			sawMovie = true
			if len(e.ItemTags) != 3 || e.ItemTags[0] != "heist" {
				t.Fatalf("Alpha ItemTags: %#v", e.ItemTags)
			}
		}
		if e.ItemType == "episode" && e.SeriesName == "Some Show" {
			sawEpisode = true
			if len(e.ItemTags) != 2 || e.ItemTags[0] != "slow burn" || e.ItemTags[1] != "dystopia" {
				t.Fatalf("episode inherits series tags, got: %#v", e.ItemTags)
			}
		}
	}
	if !sawMovie || !sawEpisode {
		t.Fatalf("missing coverage: movie=%v episode=%v", sawMovie, sawEpisode)
	}
}
```

(`e.ItemName` for episodes is overwritten by the plugin's `ItemName`; match on `ItemType`/`SeriesName` for those, as above. If the enrich step leaves `ItemName` as the plugin string for Alpha too, match on `e.ItemID` == Alpha's canonical id instead.)

- [ ] **Step 2: Run it, expect failure**

Run: `go test ./internal/source/file/ -run TestPlaybackEvents_ItemTags -v`
Expected: FAIL — `ItemTags` nil.

- [ ] **Step 3: Implement in `internal/source/file/playback.go`**

- `type itemInfo struct` gains `tags string`:

```go
	type itemInfo struct {
		name, seriesID, seriesName, genres, tags string
		runtimeTicks                             int64
		year                                     int
	}
```

- The item query gains `COALESCE(Tags,'')`:

```go
	irows, err := jdb.Query(`
		SELECT lower(replace(Id,'-','')), COALESCE(Name,''),
		       lower(replace(COALESCE(SeriesId,''),'-','')), COALESCE(SeriesName,''),
		       COALESCE(RunTimeTicks,0), COALESCE(ProductionYear,0), COALESCE(Genres,''), COALESCE(Tags,'')
		FROM BaseItems
		WHERE Type IN ('` + movieType + `', '` + episodeType + `')`)
```

Add `&info.tags` to the `irows.Scan(...)` call in the matching position.

- After the item loop, add a series-tags lookup:

```go
	seriesTags := map[string]string{}
	srows, err := jdb.Query(`SELECT lower(replace(Id,'-','')), COALESCE(Tags,'') FROM BaseItems WHERE Type = '` + seriesType + `'`)
	if err != nil {
		return err
	}
	for srows.Next() {
		var id, tags string
		if err := srows.Scan(&id, &tags); err != nil {
			srows.Close()
			return err
		}
		seriesTags[id] = tags
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}
```

- In the `for i := range events` enrich loop, inside `if info, ok := items[events[i].ItemID]; ok {`, after `events[i].ItemGenres = splitGenres(info.genres)`:

```go
			if events[i].ItemType == "episode" {
				events[i].ItemTags = splitTags(seriesTags[info.seriesID])
			} else {
				events[i].ItemTags = splitTags(info.tags)
			}
```

(`events[i].ItemType` is already `"movie"`/`"episode"` from `queryPlaybackEvents`. `info.seriesID` is the normalized parent id for episodes, `""` for movies.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/source/... && go vet ./internal/source/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/source/file/playback.go internal/source/file/file_test.go
git commit -m "$(cat <<'EOF'
feat(source): playback events carry item tags, episodes inherit series tags

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 4: `aggregate.Library` — tag frequency, coverage, co-occurrence

**Files:**
- Modify: `internal/aggregate/library.go` (types, `LibraryAggregates` fields, `Library()` walk, `tagPairsSlice`)
- Test: `internal/aggregate/library_test.go`

**Interfaces:**
- Consumes: `source.LibraryItem.Tags`.
- Produces: `aggregate.LibraryAggregates` gains `TagsTop []LabeledCount`, `TagCoverage TagCoverage`, `TagPairs []TagPair`; also sets `Totals["tags.tagged_items"]` and `Totals["tags.total_items"]`. New types `TagCoverage{ItemsTagged, ItemsTotal int64}`, `TagPair{A, B string; Items int64}`.

- [ ] **Step 1: Write the failing test**

In `internal/aggregate/library_test.go`, add:

```go
func TestLibraryAggregates_Tags(t *testing.T) {
	mk := func(name string, tags []string) source.LibraryItem {
		return source.LibraryItem{Name: name, Type: "movie", Year: 2000, Tags: tags}
	}
	snap := source.LibrarySnapshot{Items: []source.LibraryItem{
		mk("A", []string{"heist", "vault", "dystopia"}),
		mk("B", []string{"heist", "vault"}),
		mk("C", []string{"heist", "vault"}),
		mk("D", []string{"heist", "dystopia"}),
		mk("E", nil),
	}}
	a := Library(snap, time.UTC)

	if a.TagCoverage.ItemsTagged != 4 || a.TagCoverage.ItemsTotal != 5 {
		t.Fatalf("coverage: %+v", a.TagCoverage)
	}
	if a.Totals["tags.tagged_items"] != 4 || a.Totals["tags.total_items"] != 5 {
		t.Fatalf("coverage totals: %v / %v", a.Totals["tags.tagged_items"], a.Totals["tags.total_items"])
	}
	if len(a.TagsTop) == 0 || a.TagsTop[0].Label != "heist" || a.TagsTop[0].Count != 4 {
		t.Fatalf("TagsTop head: %+v", a.TagsTop)
	}
	// only heist|vault (3 items) clears minSupport=3; dystopia pairs (<=2) are dropped
	if len(a.TagPairs) != 1 || a.TagPairs[0].A != "heist" || a.TagPairs[0].B != "vault" || a.TagPairs[0].Items != 3 {
		t.Fatalf("TagPairs: %+v", a.TagPairs)
	}
}
```

- [ ] **Step 2: Run it, expect failure**

Run: `go test ./internal/aggregate/ -run TestLibraryAggregates_Tags -v`
Expected: FAIL — fields don't exist.

- [ ] **Step 3: Implement in `internal/aggregate/library.go`**

- Add types after `GrowthPoint`:

```go
type TagCoverage struct {
	ItemsTagged int64 `json:"tagged"`
	ItemsTotal  int64 `json:"total"`
}

type TagPair struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Items int64  `json:"items"`
}
```

- `LibraryAggregates` gains three fields:

```go
	GenresTop        []LabeledCount
	ByDecade         []LabeledCount
	Growth           []GrowthPoint
	TagsTop          []LabeledCount
	TagCoverage      TagCoverage
	TagPairs         []TagPair
```

- In `Library()`, add to the initial `totals` map literal:

```go
		"count.dv":          0,
		"tags.tagged_items": 0,
		"tags.total_items":  0,
```

- Declare accumulators next to `genre := map[string]int64{}`:

```go
	tag := map[string]int64{}
	tagPairs := map[[2]string]int64{}
	var taggedItems int64
```

- Inside `for _, it := range snap.Items`, after the `for _, g := range it.Genres { genre[g]++ }` loop:

```go
		if len(it.Tags) > 0 {
			taggedItems++
		}
		for _, tg := range it.Tags {
			tag[tg]++
		}
		for i := 0; i < len(it.Tags); i++ {
			for j := i + 1; j < len(it.Tags); j++ {
				x, y := it.Tags[i], it.Tags[j]
				if x > y {
					x, y = y, x
				}
				tagPairs[[2]string{x, y}]++
			}
		}
```

- After the loop, near `out.GenresTop = topN(...)`:

```go
	totals["tags.tagged_items"] = float64(taggedItems)
	totals["tags.total_items"] = float64(len(snap.Items))
	out.TagCoverage = TagCoverage{ItemsTagged: taggedItems, ItemsTotal: int64(len(snap.Items))}
	out.TagsTop = topN(labeledSortedDesc(tag), 120)
	out.TagPairs = tagPairsSlice(tagPairs, 3, 400)
```

- Add the helper near `growthSlice`:

```go
func tagPairsSlice(m map[[2]string]int64, minSupport int64, limit int) []TagPair {
	out := make([]TagPair, 0, len(m))
	for k, v := range m {
		if v < minSupport {
			continue
		}
		out = append(out, TagPair{A: k[0], B: k[1], Items: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Items != out[j].Items {
			return out[i].Items > out[j].Items
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/aggregate/ -run TestLibrary -v`
Expected: PASS (new + existing `TestLibraryAggregates`).

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate/library.go internal/aggregate/library_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): library tag frequency, coverage, and co-occurrence pairs

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 5: `aggregate` profile — `tag` taste dimension

**Files:**
- Modify: `internal/aggregate/profile.go` (`dailyRow.tags`, `rollDaily`, `tasteRows`, `baselineRows`)
- Modify: `internal/aggregate/rows.go` (comment on `ProfileTasteRow.Dim`)
- Test: `internal/aggregate/profile_test.go`

**Interfaces:**
- Consumes: `source.PlaybackEvent.ItemTags`.
- Produces: `agg.Taste` / `agg.Baseline` now contain `Dim == "tag"` rows (`ProfileTasteRow` / `TasteBaselineRow`, unchanged shapes).

- [ ] **Step 1: Write the failing test**

In `internal/aggregate/profile_test.go`, add a helper and test:

```go
func peTags(day, user, item, typ string, dur, runtime int64, tags []string) source.PlaybackEvent {
	e := pe(day, user, item, typ, dur, runtime)
	e.ItemTags = tags
	return e
}

func findTaste(rows []ProfileTasteRow, user, rng, dim, key string) (int64, int64) {
	for _, r := range rows {
		if r.UserID == user && r.Range == rng && r.Dim == dim && r.Key == key {
			return r.WatchSec, r.Plays
		}
	}
	return -1, -1
}

func TestProfiles_TagTasteDimension(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		peTags("2025-05-20 20:00", "u1", "m1", "movie", 3000, 6000, []string{"heist", "vault"}),
		peTags("2025-05-21 20:00", "u1", "e1", "episode", 1200, 1800, []string{"slow burn"}), // series tags already on the event
	}
	agg := Profiles(events, now)

	if ws, plays := findTaste(agg.Taste, "u1", "all", "tag", "heist"); ws != 3000 || plays != 1 {
		t.Fatalf("tag taste heist: ws=%d plays=%d", ws, plays)
	}
	if ws, _ := findTaste(agg.Taste, "u1", "all", "tag", "slow burn"); ws != 1200 {
		t.Fatalf("episode tag taste: ws=%d", ws)
	}
	var baseHeist int64 = -1
	for _, b := range agg.Baseline {
		if b.Dim == "tag" && b.Key == "heist" {
			baseHeist = b.WatchSec
		}
	}
	if baseHeist != 3000 {
		t.Fatalf("tag baseline heist: %d", baseHeist)
	}
}
```

- [ ] **Step 2: Run it, expect failure**

Run: `go test ./internal/aggregate/ -run TestProfiles_TagTasteDimension -v`
Expected: FAIL — no `tag` rows.

- [ ] **Step 3: Implement**

`internal/aggregate/profile.go`:

- `dailyRow` gains a field:

```go
	genres                     []string
	tags                       []string
```

- In `rollDaily`, the `r == nil` branch's `&dailyRow{...}` literal: add `tags: e.ItemTags,` next to `genres: e.ItemGenres,`.
- In `tasteRows`, inside `for _, r := range rows`, after the genre loop:

```go
		for _, tg := range r.tags {
			add("tag", tg, r.watchedSec, r.plays)
		}
```

- In `baselineRows`, inside `for _, r := range rows`, after the genre loop:

```go
		for _, tg := range r.tags {
			m[key{"tag", tg}] += r.watchedSec
		}
```

`internal/aggregate/rows.go`: update the `ProfileTasteRow` comment:

```go
type ProfileTasteRow struct {
	UserID, Range, Dim, Key string // Dim: genre|decade|length|tag
	WatchSec, Plays         int64
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/aggregate/... -v`
Expected: PASS (existing taste/decade/length tests unaffected).

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate/profile.go internal/aggregate/rows.go internal/aggregate/profile_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): add 'tag' dimension to profile taste + baseline

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 6: `aggregate` profile — two-user tag overlap

**Files:**
- Modify: `internal/aggregate/rows.go` (`ProfileTagOverlapRow`, `ProfileAggregates.TagOverlap`)
- Modify: `internal/aggregate/profile.go` (`tagOverlapRows`, `cosineAndShared`, call in `Profiles`, sort block)
- Test: `internal/aggregate/profile_test.go`

**Interfaces:**
- Consumes: the per-user/range `dailyRow.tags` + `watchedSec` from Task 5.
- Produces: `ProfileAggregates.TagOverlap []ProfileTagOverlapRow` where `ProfileTagOverlapRow{UserA, UserB, Range string; Cosine float64; Shared []string}`, `UserA < UserB`, one row per unordered user pair per range where both users have tagged watch history in range.

- [ ] **Step 1: Write the failing test**

In `internal/aggregate/profile_test.go`, add:

```go
func TestProfiles_TagOverlap(t *testing.T) {
	now := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		// u1 and u2 both watch identical tag mass -> cosine 1
		peTags("2025-05-20 20:00", "u1", "m1", "movie", 100, 200, []string{"heist"}),
		peTags("2025-05-20 20:00", "u2", "m2", "movie", 100, 200, []string{"heist"}),
		// u3 watches something disjoint
		peTags("2025-05-20 20:00", "u3", "m3", "movie", 100, 200, []string{"romance"}),
		// u4 has no tags at all
		pe("2025-05-20 20:00", "u4", "m4", "movie", 100, 200),
	}
	agg := Profiles(events, now)

	get := func(a, b, rng string) (ProfileTagOverlapRow, bool) {
		for _, r := range agg.TagOverlap {
			if r.UserA == a && r.UserB == b && r.Range == rng {
				return r, true
			}
		}
		return ProfileTagOverlapRow{}, false
	}

	r, ok := get("u1", "u2", "all")
	if !ok || r.Cosine < 0.999 || len(r.Shared) != 1 || r.Shared[0] != "heist" {
		t.Fatalf("u1/u2 overlap: %+v ok=%v", r, ok)
	}
	if _, ok := get("u1", "u3", "all"); ok {
		t.Fatalf("u1/u3 share no tags -> cosine 0 -> no row expected")
	}
	if _, ok := get("u1", "u4", "all"); ok {
		t.Fatalf("u4 has no tag vector -> no row expected")
	}
}
```

- [ ] **Step 2: Run it, expect failure**

Run: `go test ./internal/aggregate/ -run TestProfiles_TagOverlap -v`
Expected: FAIL — `agg.TagOverlap` undefined.

- [ ] **Step 3: Implement**

`internal/aggregate/rows.go`:

```go
type ProfileTagOverlapRow struct {
	UserA, UserB, Range string
	Cosine              float64
	Shared              []string
}
```

Add to `ProfileAggregates`:

```go
	Taste      []ProfileTasteRow
	Baseline   []TasteBaselineRow
	TagOverlap []ProfileTagOverlapRow
```

`internal/aggregate/profile.go` — add `"math"` to imports, then:

```go
func tagOverlapRows(daily []dailyRow, now time.Time) []ProfileTagOverlapRow {
	users := distinctUsers(daily) // sorted
	var out []ProfileTagOverlapRow
	for _, rng := range profileRangeList {
		scoped := filterSince(daily, rangeCutoff(rng, now))
		vec := map[string]map[string]int64{}
		for _, r := range scoped {
			for _, tg := range r.tags {
				m := vec[r.user]
				if m == nil {
					m = map[string]int64{}
					vec[r.user] = m
				}
				m[tg] += r.watchedSec
			}
		}
		for i := 0; i < len(users); i++ {
			for j := i + 1; j < len(users); j++ {
				va, vb := vec[users[i]], vec[users[j]]
				if len(va) == 0 || len(vb) == 0 {
					continue
				}
				cos, shared := cosineAndShared(va, vb)
				if cos == 0 {
					continue
				}
				out = append(out, ProfileTagOverlapRow{
					UserA: users[i], UserB: users[j], Range: rng, Cosine: cos, Shared: shared,
				})
			}
		}
	}
	return out
}

func cosineAndShared(a, b map[string]int64) (float64, []string) {
	var dot, na, nb float64
	var sumA, sumB int64
	for t, av := range a {
		na += float64(av) * float64(av)
		sumA += av
		if bv, ok := b[t]; ok {
			dot += float64(av) * float64(bv)
		}
	}
	for _, bv := range b {
		nb += float64(bv) * float64(bv)
		sumB += bv
	}
	if na == 0 || nb == 0 || dot == 0 {
		return 0, nil
	}
	cos := dot / (math.Sqrt(na) * math.Sqrt(nb))

	type sc struct {
		tag   string
		score float64
	}
	var xs []sc
	for t, av := range a {
		bv, ok := b[t]
		if !ok {
			continue
		}
		xs = append(xs, sc{t, math.Min(float64(av)/float64(sumA), float64(bv)/float64(sumB))})
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].score != xs[j].score {
			return xs[i].score > xs[j].score
		}
		return xs[i].tag < xs[j].tag
	})
	var shared []string
	for _, x := range xs {
		shared = append(shared, x.tag)
		if len(shared) == 8 {
			break
		}
	}
	return cos, shared
}
```

In `Profiles()`, after `out.Baseline = baselineRows(daily)`:

```go
	out.TagOverlap = tagOverlapRows(daily, now)
```

In `sortProfileAggregates`, add after the `a.Baseline` sort:

```go
	sort.Slice(a.TagOverlap, func(i, j int) bool {
		x, y := a.TagOverlap[i], a.TagOverlap[j]
		switch {
		case x.UserA != y.UserA:
			return x.UserA < y.UserA
		case x.UserB != y.UserB:
			return x.UserB < y.UserB
		default:
			return x.Range < y.Range
		}
	})
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/aggregate/... && go vet ./internal/aggregate/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/aggregate/profile.go internal/aggregate/rows.go internal/aggregate/profile_test.go
git commit -m "$(cat <<'EOF'
feat(aggregate): per-user-pair tag-vector cosine overlap

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 7: Store + API for library tags

**Files:**
- Modify: `internal/store/writes.go` (`WriteLibraryAggregates`: `agg_distribution` `'tag'`, `agg_library_tag_pairs` wipe+insert; totals already covered)
- Modify: `internal/store/reads.go` (`LibraryOverview.Tags`, `TagPairDTO`, coverage from totals, `readDistro("tag")`, `readTagPairs`, `orEmpty`)
- Test: `internal/store/reads_test.go`, `internal/store/writes_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: `aggregate.LibraryAggregates.{TagsTop, TagPairs, Totals}`.
- Produces: `/api/library/overview` `data.tags` = `{ coverage:{tagged,total}, top:[{label,count}], pairs:[{a,b,items}] }`. `store.TagPairDTO{A,B string; Items int64}`.

- [ ] **Step 1: Write the failing tests**

In `internal/store/writes_test.go`, extend `sampleAggregates()` (add fields to the returned literal):

```go
		GenresTop: []aggregate.LabeledCount{{Label: "Drama", Count: 4}, {Label: "Comedy", Count: 1}},
		TagsTop:   []aggregate.LabeledCount{{Label: "heist", Count: 4}, {Label: "vault", Count: 3}},
		TagCoverage: aggregate.TagCoverage{ItemsTagged: 4, ItemsTotal: 6},
		TagPairs:  []aggregate.TagPair{{A: "heist", B: "vault", Items: 3}},
```

and add `"tags.tagged_items": 4, "tags.total_items": 6` to that literal's `Totals` map.

In `internal/store/reads_test.go`, add (adapt to the file's existing store-open + write pattern):

```go
func TestReadLibraryOverview_Tags(t *testing.T) {
	ctx := context.Background()
	st := openStore(t) // match the helper used by other tests in this file
	if err := st.WriteLibraryAggregates(ctx, sampleAggregates(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	ov, err := st.ReadLibraryOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Tags.Coverage.Tagged != 4 || ov.Tags.Coverage.Total != 6 {
		t.Fatalf("coverage: %+v", ov.Tags.Coverage)
	}
	if len(ov.Tags.Top) == 0 || ov.Tags.Top[0].Label != "heist" {
		t.Fatalf("top: %+v", ov.Tags.Top)
	}
	if len(ov.Tags.Pairs) != 1 || ov.Tags.Pairs[0].A != "heist" || ov.Tags.Pairs[0].B != "vault" || ov.Tags.Pairs[0].Items != 3 {
		t.Fatalf("pairs: %+v", ov.Tags.Pairs)
	}
}

func TestReadLibraryOverview_TagsEmpty(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := st.WriteLibraryAggregates(ctx, aggregate.LibraryAggregates{Totals: map[string]float64{}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	ov, err := st.ReadLibraryOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Tags.Coverage.Tagged != 0 || ov.Tags.Coverage.Total != 0 {
		t.Fatalf("empty coverage: %+v", ov.Tags.Coverage)
	}
	if ov.Tags.Top == nil || ov.Tags.Pairs == nil {
		t.Fatalf("empty slices must be non-nil for []-serialization: %+v", ov.Tags)
	}
}
```

- [ ] **Step 2: Run, expect failure**

Run: `go test ./internal/store/ -run 'TestReadLibraryOverview_Tags' -v`
Expected: FAIL — `ov.Tags` undefined.

- [ ] **Step 3: Implement writes** — `internal/store/writes.go`, in `WriteLibraryAggregates`:

- Wipe list: add `` `DELETE FROM agg_library_tag_pairs`, `` and change the distribution wipe to include `'tag'`:

```go
		`DELETE FROM agg_distribution WHERE dimension IN ('genre','decade','library_items','tag')`,
		`DELETE FROM agg_library_growth`,
		`DELETE FROM agg_library_tag_pairs`,
```

- `distros` slice: add a row:

```go
		{"genre", a.GenresTop},
		{"decade", a.ByDecade},
		{"library_items", a.ItemsByLibrary},
		{"tag", a.TagsTop},
```

- After the `a.Growth` insert loop, add:

```go
	for _, p := range a.TagPairs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agg_library_tag_pairs (tag_a, tag_b, items) VALUES (?,?,?)`,
			p.A, p.B, p.Items); err != nil {
			return err
		}
	}
```

(`insertTotals` already writes every `a.Totals` key, so `tags.*_items` persist with no change.)

- [ ] **Step 4: Implement reads** — `internal/store/reads.go`:

- In the `LibraryOverview` struct, after `Growth`:

```go
	Growth []aggregate.GrowthPoint `json:"growth"`
	Tags   struct {
		Coverage struct {
			Tagged int64 `json:"tagged"`
			Total  int64 `json:"total"`
		} `json:"coverage"`
		Top   []aggregate.LabeledCount `json:"top"`
		Pairs []TagPairDTO             `json:"pairs"`
	} `json:"tags"`
```

- Add the DTO near the top of the file:

```go
type TagPairDTO struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Items int64  `json:"items"`
}
```

- In `ReadLibraryOverview`, after the block that reads `totals[...]` into `ov.Totals.*`:

```go
	ov.Tags.Coverage.Tagged = int64(totals["tags.tagged_items"])
	ov.Tags.Coverage.Total = int64(totals["tags.total_items"])
```

- After `ov.GenresTop, err = s.readDistro(ctx, "genre", false)` group, add:

```go
	if ov.Tags.Top, err = s.readDistro(ctx, "tag", false); err != nil {
		return ov, err
	}
	if ov.Tags.Pairs, err = s.readTagPairs(ctx); err != nil {
		return ov, err
	}
```

- Add the reader near `readDistro`:

```go
func (s *Store) readTagPairs(ctx context.Context) ([]TagPairDTO, error) {
	r, err := s.db.QueryContext(ctx,
		`SELECT tag_a, tag_b, items FROM agg_library_tag_pairs ORDER BY items DESC, tag_a, tag_b`)
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
```

- In the `orEmpty` block at the end of `ReadLibraryOverview`:

```go
	ov.Growth = orEmpty(ov.Growth)
	ov.Tags.Top = orEmpty(ov.Tags.Top)
	ov.Tags.Pairs = orEmpty(ov.Tags.Pairs)
```

- [ ] **Step 5: Run store tests**

Run: `go test ./internal/store/... && go vet ./internal/store/...`
Expected: PASS.

- [ ] **Step 6: Add the API assertion** — `internal/api/api_test.go`:

Extend `sampleAgg()` in `internal/api/testhelpers_test.go`:

```go
func sampleAgg() aggregate.LibraryAggregates {
	return aggregate.LibraryAggregates{
		Totals: map[string]float64{
			"items.total": 6, "items.Series": 3,
			"runtime_sec.total": 100, "bytes.total": 200,
			"tags.tagged_items": 4, "tags.total_items": 6,
		},
		TagsTop:     []aggregate.LabeledCount{{Label: "heist", Count: 4}},
		TagCoverage: aggregate.TagCoverage{ItemsTagged: 4, ItemsTotal: 6},
		TagPairs:    []aggregate.TagPair{{A: "heist", B: "vault", Items: 3}},
	}
}
```

Add a test after `TestLibraryOverview_EmptyListsSerializeAsArrays`:

```go
func TestLibraryOverview_TagsInPayload(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	ctx := context.Background()
	if err := st.WriteLibraryAggregates(ctx, sampleAgg(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	st.SetRefreshMeta(ctx, store.RefreshMeta{Job: "library", LastRunAt: now.Add(-time.Minute), OK: true})

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/library/overview", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data struct {
			Tags struct {
				Coverage struct{ Tagged, Total int64 } `json:"coverage"`
				Top      []struct{ Label string }      `json:"top"`
				Pairs    []struct{ A, B string }       `json:"pairs"`
			} `json:"tags"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Tags.Coverage.Tagged != 4 || env.Data.Tags.Coverage.Total != 6 {
		t.Fatalf("coverage: %+v", env.Data.Tags.Coverage)
	}
	if len(env.Data.Tags.Top) != 1 || env.Data.Tags.Top[0].Label != "heist" || len(env.Data.Tags.Pairs) != 1 {
		t.Fatalf("tags payload: %+v", env.Data.Tags)
	}
}
```

- [ ] **Step 7: Run store + api**

Run: `go test ./internal/store/... ./internal/api/... && go vet ./internal/store/... ./internal/api/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/store/writes.go internal/store/reads.go internal/store/writes_test.go internal/store/reads_test.go internal/api/testhelpers_test.go internal/api/api_test.go
git commit -m "$(cat <<'EOF'
feat(store,api): persist and serve library tag cloud, coverage, co-occurrence

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 8: Store writes for `agg_profile_tag_overlap`

**Files:**
- Modify: `internal/store/writes_profile.go` (wipe list, insert loop; `dim='tag'` taste/baseline flow unchanged)
- Test: `internal/store/writes_profile_test.go`

**Interfaces:**
- Consumes: `aggregate.ProfileAggregates.{Taste, Baseline, TagOverlap}`.
- Produces: rows in `agg_profile_tag_overlap` (`shared` pipe-joined via `joinTags`); `dim='tag'` rows in `agg_profile_taste` / `agg_taste_baseline`.

- [ ] **Step 1: Write the failing test**

In `internal/store/writes_profile_test.go`, add:

```go
func TestWriteProfile_TagOverlapAndTagTaste(t *testing.T) {
	ctx := context.Background()
	st := openProfileStore(t) // match this file's existing helper

	err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "all", Dim: "tag", Key: "heist", WatchSec: 3000, Plays: 2},
		},
		Baseline: []aggregate.TasteBaselineRow{
			{Dim: "tag", Key: "heist", WatchSec: 3000},
		},
		TagOverlap: []aggregate.ProfileTagOverlapRow{
			{UserA: "u1", UserB: "u2", Range: "all", Cosine: 0.62, Shared: []string{"heist", "vault"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var dim, key string
	var ws int64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT dim, key, watch_sec FROM agg_profile_taste WHERE dim='tag'`).Scan(&dim, &key, &ws); err != nil {
		t.Fatal(err)
	}
	if key != "heist" || ws != 3000 {
		t.Fatalf("tag taste row: %s %d", key, ws)
	}

	var ua, ub, shared string
	var cos float64
	if err := st.DB().QueryRowContext(ctx,
		`SELECT user_a, user_b, cosine, shared FROM agg_profile_tag_overlap`).Scan(&ua, &ub, &cos, &shared); err != nil {
		t.Fatal(err)
	}
	if ua != "u1" || ub != "u2" || cos < 0.61 || shared != "heist|vault" {
		t.Fatalf("overlap row: %s %s %v %q", ua, ub, cos, shared)
	}

	// second write wipes the prior overlap rows
	if err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{}); err != nil {
		t.Fatal(err)
	}
	var n int
	st.DB().QueryRowContext(ctx, `SELECT count(*) FROM agg_profile_tag_overlap`).Scan(&n)
	if n != 0 {
		t.Fatalf("expected wipe, got %d rows", n)
	}
}
```

- [ ] **Step 2: Run, expect failure**

Run: `go test ./internal/store/ -run TestWriteProfile_TagOverlapAndTagTaste -v`
Expected: FAIL — nothing writes `agg_profile_tag_overlap`.

- [ ] **Step 3: Implement** — `internal/store/writes_profile.go`:

- Add to the wipe loop list:

```go
		`DELETE FROM agg_profile_taste`,
		`DELETE FROM agg_taste_baseline`,
		`DELETE FROM agg_profile_tag_overlap`,
```

- After the `a.Baseline` insert loop, add:

```go
	for _, r := range a.TagOverlap {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agg_profile_tag_overlap (user_a, user_b, range, cosine, shared)
			VALUES (?,?,?,?,?)`,
			r.UserA, r.UserB, r.Range, r.Cosine, joinTags(r.Shared),
		); err != nil {
			return err
		}
	}
```

(`joinTags` is defined in `spine.go`, same package.)

- [ ] **Step 4: Run tests**

Run: `go test ./internal/store/... && go vet ./internal/store/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/store/writes_profile.go internal/store/writes_profile_test.go
git commit -m "$(cat <<'EOF'
feat(store): persist agg_profile_tag_overlap; wipe it per refresh

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 9: Store reads + API for profile tags

**Files:**
- Modify: `internal/store/reads_profile.go` (DTO fields, `readProfileTaste` `tag` branch, `signatureShares` generalization, `readProfileTagOverlap`, `ReadProfile` wiring, `orEmpty`)
- Test: `internal/store/reads_profile_test.go`, `internal/api/profile_test.go`

**Interfaces:**
- Consumes: `agg_profile_taste`/`agg_taste_baseline` `dim='tag'` rows; `agg_profile_tag_overlap`.
- Produces: `/api/profile/{userID}` `data.taste.tag`, `data.taste.signature_tags`, `data.baseline.tag`, `data.tag_overlap` (`[]{user, user_name, cosine, shared}`), always `[]` not `null`.

- [ ] **Step 1: Write the failing tests**

In `internal/store/reads_profile_test.go`, add (adapt helper names to the file):

```go
func TestReadProfile_TagsAndOverlap(t *testing.T) {
	ctx := context.Background()
	st := openProfileStore(t)

	// dim_user rows for name resolution
	if err := st.WriteLibraryAggregates(ctx, aggregate.LibraryAggregates{Totals: map[string]float64{}}, nil,
		[]aggregate.UserRow{{ID: "u1", Name: "alice"}, {ID: "u2", Name: "bob"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 5, WatchSec: 9000}},
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "all", Dim: "tag", Key: "heist", WatchSec: 8000, Plays: 5},
			{UserID: "u1", Range: "all", Dim: "tag", Key: "cameo", WatchSec: 30, Plays: 1}, // below floor
		},
		Baseline: []aggregate.TasteBaselineRow{
			{Dim: "tag", Key: "heist", WatchSec: 1000},
			{Dim: "tag", Key: "cameo", WatchSec: 1000},
		},
		TagOverlap: []aggregate.ProfileTagOverlapRow{
			{UserA: "u1", UserB: "u2", Range: "all", Cosine: 0.5, Shared: []string{"heist"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	p, ok, err := st.ReadProfile(ctx, "u1", "all")
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if len(p.Taste.Tag) == 0 || p.Taste.Tag[0].Key != "heist" {
		t.Fatalf("taste.tag: %+v", p.Taste.Tag)
	}
	// heist is way over baseline share and clears the 2-play/120s floor; cameo doesn't
	if len(p.Taste.SignatureTags) != 1 || p.Taste.SignatureTags[0] != "heist" {
		t.Fatalf("signature_tags: %+v", p.Taste.SignatureTags)
	}
	if len(p.TagOverlap) != 1 || p.TagOverlap[0].User != "u2" || p.TagOverlap[0].UserName != "bob" ||
		len(p.TagOverlap[0].Shared) != 1 || p.TagOverlap[0].Shared[0] != "heist" {
		t.Fatalf("tag_overlap: %+v", p.TagOverlap)
	}

	// solo user: no overlap rows
	p2, _, _ := st.ReadProfile(ctx, "u2", "all")
	if p2.TagOverlap == nil {
		t.Fatalf("tag_overlap must be [] not nil")
	}
	if len(p2.TagOverlap) != 1 || p2.TagOverlap[0].User != "u1" {
		// u2 appears as user_b in the single row; reading as u2 should surface u1
		t.Fatalf("expected the same pair surfaced from u2's side: %+v", p2.TagOverlap)
	}
}
```

- [ ] **Step 2: Run, expect failure**

Run: `go test ./internal/store/ -run TestReadProfile_TagsAndOverlap -v`
Expected: FAIL — `p.Taste.Tag` undefined.

- [ ] **Step 3: Implement DTO changes** — `internal/store/reads_profile.go`:

```go
type ProfileTaste struct {
	Genre           []TasteEntry `json:"genre"`
	Decade          []TasteEntry `json:"decade"`
	Length          []TasteEntry `json:"length"`
	Tag             []TasteEntry `json:"tag"`
	SignatureGenres []string     `json:"signature_genres"`
	SignatureTags   []string     `json:"signature_tags"`
}
type ProfileBaseline struct {
	Genre  []BaselineEntry `json:"genre"`
	Decade []BaselineEntry `json:"decade"`
	Length []BaselineEntry `json:"length"`
	Tag    []BaselineEntry `json:"tag"`
}
type ProfileTagOverlap struct {
	User     string   `json:"user"`
	UserName string   `json:"user_name"`
	Cosine   float64  `json:"cosine"`
	Shared   []string `json:"shared"`
}
```

Add to `Profile`:

```go
	Taste      ProfileTaste             `json:"taste"`
	Baseline   ProfileBaseline          `json:"baseline"`
	TagOverlap []ProfileTagOverlap      `json:"tag_overlap"`
```

- [ ] **Step 4: Implement `tag` branch + overlap reader + signature floor**

In `readProfileTaste`, add a `case "tag":` to both switches:

```go
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
```

```go
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
```

Generalize `signatureGenres` → `signatureShares` with a support floor; keep `signatureGenres` as a zero-floor wrapper:

```go
func signatureGenres(userGenre []TasteEntry, baseGenre []BaselineEntry) []string {
	return signatureShares(userGenre, baseGenre, 0, 0)
}

// signatureShares picks up to three keys where the user's watch-time share most
// exceeds the baseline share. minPlays / minSec drop keys with too little
// support to be called a signature.
func signatureShares(user []TasteEntry, base []BaselineEntry, minPlays, minSec int64) []string {
	var userTotal, baseTotal int64
	for _, g := range user {
		userTotal += g.WatchSec
	}
	for _, g := range base {
		baseTotal += g.WatchSec
	}
	if userTotal == 0 {
		return nil
	}
	baseShare := map[string]float64{}
	if baseTotal > 0 {
		for _, g := range base {
			baseShare[g.Key] = float64(g.WatchSec) / float64(baseTotal)
		}
	}
	type scored struct {
		key   string
		delta float64
		share float64
	}
	var xs []scored
	for _, g := range user {
		if g.Plays < minPlays || g.WatchSec < minSec {
			continue
		}
		us := float64(g.WatchSec) / float64(userTotal)
		xs = append(xs, scored{key: g.Key, delta: us - baseShare[g.Key], share: us})
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].delta != xs[j].delta {
			return xs[i].delta > xs[j].delta
		}
		if xs[i].share != xs[j].share {
			return xs[i].share > xs[j].share
		}
		return xs[i].key < xs[j].key
	})
	var out []string
	for _, x := range xs {
		if x.delta <= 0 {
			break
		}
		out = append(out, x.key)
		if len(out) == 3 {
			break
		}
	}
	return out
}
```

Add the overlap reader:

```go
func (s *Store) readProfileTagOverlap(ctx context.Context, out *Profile, userID, rng string) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT CASE WHEN o.user_a = ? THEN o.user_b ELSE o.user_a END AS other,
		       COALESCE(d.name, ''), o.cosine, o.shared
		FROM agg_profile_tag_overlap o
		LEFT JOIN dim_user d
		  ON d.id = CASE WHEN o.user_a = ? THEN o.user_b ELSE o.user_a END
		WHERE (o.user_a = ? OR o.user_b = ?) AND o.range = ?
		ORDER BY o.cosine DESC, other`, userID, userID, userID, userID, rng)
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
		e.Shared = splitGenres(shared) // pipe-split; store.splitGenres handles ""
		out.TagOverlap = append(out.TagOverlap, e)
	}
	return rows.Err()
}
```

In `ReadProfile`, after `if err := s.readProfileTaste(...)`:

```go
	if err := s.readProfileTagOverlap(ctx, &out, userID, rng); err != nil {
		return Profile{}, false, err
	}
	out.Taste.SignatureGenres = signatureGenres(out.Taste.Genre, out.Baseline.Genre)
	out.Taste.SignatureTags = signatureShares(out.Taste.Tag, out.Baseline.Tag, 2, 120)
```

In the `orEmpty` block:

```go
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
```

- [ ] **Step 5: Run store tests**

Run: `go test ./internal/store/... && go vet ./internal/store/...`
Expected: PASS.

- [ ] **Step 6: API assertion** — `internal/api/profile_test.go`:

Extend `seedForProfile` with tag rows, then add:

```go
func TestHandleProfile_TagsInPayload(t *testing.T) {
	now := time.Date(2025, 5, 2, 0, 0, 0, 0, time.UTC)
	s, st, _ := newTestServer(t, now)
	seedForProfile(t, st, now)

	ctx := context.Background()
	if err := st.WriteProfileAggregates(ctx, aggregate.ProfileAggregates{
		Summary: []aggregate.ProfileSummaryRow{{UserID: "u1", Range: "all", Plays: 5, WatchSec: 9000}},
		Taste: []aggregate.ProfileTasteRow{
			{UserID: "u1", Range: "all", Dim: "tag", Key: "heist", WatchSec: 8000, Plays: 5},
		},
		Baseline: []aggregate.TasteBaselineRow{{Dim: "tag", Key: "heist", WatchSec: 1000}},
	}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/profile/u1?range=all", nil))
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body)
	}
	var env struct {
		Data struct {
			Taste struct {
				Tag           []struct{ Key string } `json:"tag"`
				SignatureTags []string               `json:"signature_tags"`
			} `json:"taste"`
			Baseline struct {
				Tag []struct{ Key string } `json:"tag"`
			} `json:"baseline"`
			TagOverlap []any `json:"tag_overlap"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Taste.Tag) != 1 || env.Data.Taste.Tag[0].Key != "heist" {
		t.Fatalf("taste.tag: %+v", env.Data.Taste.Tag)
	}
	if len(env.Data.Taste.SignatureTags) != 1 || env.Data.Taste.SignatureTags[0] != "heist" {
		t.Fatalf("signature_tags: %+v", env.Data.Taste.SignatureTags)
	}
	if env.Data.TagOverlap == nil {
		t.Fatalf("tag_overlap must serialize as [] not null")
	}
}
```

Also add `Tag: []aggregate...` is not needed in `seedForProfile`; the test writes its own aggregates. But confirm the existing `TestHandleProfile_OK` still passes (it will — new fields default to `[]`).

- [ ] **Step 7: Run store + api**

Run: `go test ./internal/store/... ./internal/api/... && go vet ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/store/reads_profile.go internal/store/reads_profile_test.go internal/api/profile_test.go
git commit -m "$(cat <<'EOF'
feat(store,api): serve profile tag taste, signature tags, two-user overlap

signatureGenres generalized to signatureShares with a play/second support
floor; signature_tags uses 2 plays / 120s.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 10: Frontend — Library Overview tag section

**Files:**
- Modify: `web/src/api/types.ts` (`LibraryOverview.tags`)
- Modify: `web/src/routes/library.tsx` (`TagSection` component + render)
- Test: `web/src/routes/library.test.tsx`

**Interfaces:**
- Consumes: `GET /api/library/overview` `data.tags`.
- Produces: a `tags` section under the charts — coverage line, clickable tag cloud, co-occurrence list.

- [ ] **Step 1: Extend the type**

`web/src/api/types.ts`, in `interface LibraryOverview`, after `growth: GrowthPoint[];`:

```ts
  growth: GrowthPoint[];
  tags: {
    coverage: { tagged: number; total: number };
    top: LabeledCount[];
    pairs: { a: string; b: string; items: number }[];
  };
```

- [ ] **Step 2: Write the failing tests**

`web/src/routes/library.test.tsx` — add `tags` to the `sample` fixture's `data`:

```ts
    growth: [{ month: "2024-01", added_items: 40, added_bytes: 1e9, cum_items: 8000 }],
    tags: {
      coverage: { tagged: 812, total: 2140 },
      top: [
        { label: "heist", count: 143 },
        { label: "slow burn", count: 96 },
      ],
      pairs: [{ a: "heist", b: "vault", items: 11 }],
    },
```

Add tests:

```ts
test("renders the tag section: coverage line and cloud", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(sample), { status: 200 }),
  );
  render(wrap(<LibraryOverview />));
  expect(await screen.findByText(/812/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "heist" })).toBeInTheDocument();
});

test("clicking a tag shows its co-occurring tags", async () => {
  vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify(sample), { status: 200 }),
  );
  const user = userEvent.setup(); // import from "@testing-library/user-event"
  render(wrap(<LibraryOverview />));
  await user.click(await screen.findByRole("button", { name: "heist" }));
  expect(await screen.findByText(/vault/)).toBeInTheDocument();
});

test("zero coverage renders a single line, no cloud", async () => {
  const empty = {
    ...sample,
    data: { ...sample.data, tags: { coverage: { tagged: 0, total: 0 }, top: [], pairs: [] } },
  };
  vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify(empty), { status: 200 }));
  render(wrap(<LibraryOverview />));
  expect(await screen.findByText(/no tags in this library/i)).toBeInTheDocument();
});
```

(If `@testing-library/user-event` is not already a dep, check `web/package.json`; `watch.test.tsx` / `profile.test.tsx` likely already use it. If not, use `fireEvent.click` from `@testing-library/react` instead.)

- [ ] **Step 3: Run, expect failure**

Run: `cd web && npx vitest run src/routes/library.test.tsx`
Expected: FAIL — no tag section.

- [ ] **Step 4: Implement `TagSection`** in `web/src/routes/library.tsx`

Add above `LibraryOverview`:

```tsx
function TagSection({ tags }: { tags: LibraryOverview["tags"] }) {
  const [sel, setSel] = useState<string | null>(null);
  const { tagged, total } = tags.coverage;

  if (total === 0 || tagged === 0) {
    return (
      <Panel className="p-4">
        <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
          tags
        </div>
        <p className="font-mono text-[12px] text-muted">No tags in this library.</p>
      </Panel>
    );
  }

  const pct = Math.round((tagged / total) * 100);
  const max = tags.top[0]?.count ?? 1;
  const size = (c: number) => 11 + Math.round(Math.sqrt(c / max) * 15); // 11..26px

  const neighbours = sel
    ? tags.pairs
        .filter((p) => p.a === sel || p.b === sel)
        .map((p) => ({ tag: p.a === sel ? p.b : p.a, items: p.items }))
        .sort((x, y) => y.items - x.items)
    : [];

  return (
    <Panel className="p-4">
      <div className="mb-3 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
        tags
      </div>
      <p className="font-mono text-[12px] text-muted">
        <span className="font-medium text-ink">{fmtInt(tagged)}</span> / {fmtInt(total)} items tagged
        ({pct}%)
      </p>

      <div className="mt-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
        {tags.top.map((tg) => (
          <button
            key={tg.label}
            type="button"
            onClick={() => setSel((s) => (s === tg.label ? null : tg.label))}
            title={`${tg.label} · ${fmtInt(tg.count)}`}
            style={{ fontSize: `${size(tg.count)}px` }}
            className={`font-mono leading-tight transition-colors ${
              sel === tg.label ? "text-cyan" : "text-muted hover:text-ink"
            }`}
          >
            {tg.label}
          </button>
        ))}
      </div>

      <div className="mt-4 font-mono text-[12px]">
        {!sel ? (
          <p className="text-muted">Pick a tag to see what it rides along with.</p>
        ) : neighbours.length === 0 ? (
          <p className="text-muted">
            <span className="text-ink">{sel}</span> — nothing co-occurs above the noise floor.
          </p>
        ) : (
          <p className="text-muted">
            <span className="text-ink">{sel}</span> →{" "}
            {neighbours.map((n, i) => (
              <span key={n.tag}>
                {i > 0 && " · "}
                {n.tag} <span className="text-ink">({fmtInt(n.items)})</span>
              </span>
            ))}
          </p>
        )}
      </div>
    </Panel>
  );
}
```

Add `useState` to the existing `react` import: `import { lazy, type ReactNode, Suspense, useState } from "react";`. `LibraryOverview` is already imported as a type name in this file? It is `LibraryOverview` the component — the type is `LibraryOverview` from `@/api/types`. The file currently imports only `DiskBucket, LabeledCount` from types. Change to `import type { DiskBucket, LabeledCount, LibraryOverview } from "@/api/types";` and note the local component is also named `LibraryOverview` — rename the **type** usage via alias to avoid the clash: `import type { ..., LibraryOverview as LibraryOverviewData } from "@/api/types";` and use `LibraryOverviewData["tags"]` in `TagSection`'s prop type.

Render it: right after the closing `</Suspense>` in `LibraryOverview`'s JSX, before the outer `</div>`:

```tsx
      </Suspense>

      <TagSection tags={data.tags} />
    </div>
```

- [ ] **Step 5: Run frontend checks**

Run: `cd web && npx vitest run src/routes/library.test.tsx && npx tsc -b && npm run lint`
Expected: PASS / clean.

- [ ] **Step 6: Commit**

```bash
git add web/src/api/types.ts web/src/routes/library.tsx web/src/routes/library.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): tag section on Library Overview — coverage, cloud, co-occurrence

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 11: Frontend — Profiles taste-fingerprint additions

**Files:**
- Modify: `web/src/api/types.ts` (`Profile.taste.tag`, `signature_tags`, `Profile.baseline.tag`, `Profile.tag_overlap`)
- Modify: `web/src/routes/profile.tsx` (`ShareBars` label prop; tag bars + signature line + overlap sub-block in the taste `Section`)
- Test: `web/src/routes/profile.test.tsx`

**Interfaces:**
- Consumes: `GET /api/profile/{userID}` `data.taste.tag` / `data.taste.signature_tags` / `data.baseline.tag` / `data.tag_overlap`.
- Produces: extra rows in the existing "taste fingerprint" panel.

- [ ] **Step 1: Extend the types**

`web/src/api/types.ts`, in `interface Profile`:

```ts
  taste: {
    genre: TasteEntry[];
    decade: TasteEntry[];
    length: TasteEntry[];
    tag: TasteEntry[];
    signature_genres: string[];
    signature_tags: string[];
  };
  baseline: {
    genre: { key: string; watch_sec: number }[];
    decade: { key: string; watch_sec: number }[];
    length: { key: string; watch_sec: number }[];
    tag: { key: string; watch_sec: number }[];
  };
  tag_overlap: { user: string; user_name: string; cosine: number; shared: string[] }[];
```

- [ ] **Step 2: Write the failing tests**

`web/src/routes/profile.test.tsx` — in the profile fixture used by the render test, extend `taste` / `baseline` and add `tag_overlap`:

```ts
    taste: {
      genre: [{ key: "Drama", watch_sec: 100, plays: 2 }],
      decade: [],
      length: [],
      tag: [{ key: "heist", watch_sec: 120, plays: 3 }],
      signature_genres: ["Drama"],
      signature_tags: ["heist"],
    },
    baseline: {
      genre: [{ key: "Drama", watch_sec: 100 }],
      decade: [],
      length: [],
      tag: [{ key: "heist", watch_sec: 50 }],
    },
    tag_overlap: [{ user: "u2", user_name: "bob", cosine: 0.62, shared: ["heist", "vault"] }],
```

Add assertions to the existing "renders ... taste" test (or a new test):

```ts
  expect(screen.getByText("heist")).toBeInTheDocument();           // tag ShareBars row
  expect(screen.getByText(/bob/)).toBeInTheDocument();             // overlap row
  expect(screen.getByText(/62%/)).toBeInTheDocument();
```

And a solo-user case:

```ts
test("hides the overlap block when tag_overlap is empty", async () => {
  // mock profile response with tag_overlap: []
  // expect: no "overlap with others" heading
});
```

- [ ] **Step 3: Run, expect failure**

Run: `cd web && npx vitest run src/routes/profile.test.tsx`
Expected: FAIL — no tag bars / overlap.

- [ ] **Step 4: Implement**

`web/src/routes/profile.tsx`:

- `ShareBars` gets a `label` prop for its empty-state copy:

```tsx
function ShareBars({
  user,
  baseline,
  label = "data",
}: {
  user: { key: string; watch_sec: number }[];
  baseline: { key: string; watch_sec: number }[];
  label?: string;
}) {
```

and:

```tsx
  if (rows.length === 0)
    return <p className="font-mono text-[12px] text-muted">No {label} data for this user.</p>;
```

- In the `<Section title="taste fingerprint">` block, after the existing `signature_genres` `<p>` and before the decade `<div>`, insert:

```tsx
          <div className="mt-4 mb-2 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
            tags
          </div>
          <ShareBars user={d.taste.tag} baseline={d.baseline.tag} label="tag" />
          {d.taste.signature_tags.length > 0 && (
            <p className="mt-3 font-mono text-[11.5px] text-muted">
              also leans <span className="text-ink">{d.taste.signature_tags.join(", ")}</span>
            </p>
          )}

          {d.tag_overlap.length > 0 && (
            <div className="mt-4">
              <div className="mb-2 text-[11px] font-semibold uppercase tracking-[0.16em] text-muted">
                overlap with others
              </div>
              <div className="flex flex-col gap-1.5">
                {d.tag_overlap.map((o) => (
                  <div key={o.user} className="grid grid-cols-[90px_1fr] items-center gap-3">
                    <span className="truncate font-mono text-[12px] text-ink" title={o.user_name}>
                      {o.user_name}
                    </span>
                    <span className="relative block h-[14px] rounded bg-panel/60">
                      <span
                        className="absolute inset-y-0 left-0 rounded bg-violet/70"
                        style={{ width: `${Math.min(100, o.cosine * 100)}%` }}
                        title={`${Math.round(o.cosine * 100)}% · ${o.shared.join(", ")}`}
                      />
                    </span>
                  </div>
                ))}
              </div>
              <p className="mt-1 font-mono text-[11px] text-muted">
                bar = tag-taste similarity · hover for shared tags
              </p>
            </div>
          )}
```

- The `62%` text asserted in the test comes from the `title` attr — if the test queries visible text, add a visible `{Math.round(o.cosine * 100)}%` span next to the name instead. Adjust the test or the markup so they agree (prefer a visible percentage: put `<span className="font-mono text-[11px] text-muted">{Math.round(o.cosine * 100)}%</span>` in the name cell).

- [ ] **Step 5: Run frontend checks**

Run: `cd web && npx vitest run src/routes/profile.test.tsx && npx tsc -b && npm run lint`
Expected: PASS / clean.

- [ ] **Step 6: Commit**

```bash
git add web/src/api/types.ts web/src/routes/profile.tsx web/src/routes/profile.test.tsx
git commit -m "$(cat <<'EOF'
feat(web): tag bars, signature tags, and two-user overlap on Profiles

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

### Task 12: Full verification + manual pass

**Files:** none (verification only). If anything fails, fix in the owning task's files and re-commit.

- [ ] **Step 1: Whole Go suite + lint**

Run: `make test && make lint`
Expected: all packages PASS; `go vet` + `golangci-lint run` clean.

- [ ] **Step 2: Whole frontend suite + typecheck + lint + build**

Run: `cd web && npm run test && npx tsc -b && npm run lint && npm run build`
Expected: PASS / clean / build succeeds.

- [ ] **Step 3: Full binary build**

Run: `make build`
Expected: `web/` builds then `./ephyra` builds.

- [ ] **Step 4: Manual pass against the fixture**

```sh
mkdir -p /tmp/jf/data && sqlite3 /tmp/jf/data/jellyfin.db < testdata/library.fixture.sql
sqlite3 /tmp/jf/data/playback_reporting.db < testdata/playback_reporting.fixture.sql
JELLYFIN_URL=x JELLYFIN_API_KEY=x JELLYFIN_DATA_DIR=/tmp/jf \
  STORE_PATH=/tmp/ephyra.db WORK_DIR=/tmp/ephyra-work ./ephyra
```

Then `curl -s localhost:8097/api/library/overview | jq '.data.tags'` and
`curl -s 'localhost:8097/api/profile' | jq` → pick a user id →
`curl -s 'localhost:8097/api/profile/<id>?range=all' | jq '.data.taste.tag, .data.tag_overlap'`.
Confirm: coverage `{tagged:4,total:6}`, `heist` leads the cloud, one `heist|vault` pair; a user with Alpha plays shows `heist` in `taste.tag`.

- [ ] **Step 5: Manual pass against a real Jellyfin DB** (maintainer, per spec's pre-implementation check)

Follow README's "Test against a real library". Confirm `BaseItems.Tags` exists and is pipe-delimited; eyeball the real tagged-item ratio; sanity-check the tag section and a profile's tag fingerprint in the browser at `:5173`. If `Tags` is absent as a `BaseItems` column on the real DB (data only in `ItemValues`), that is a follow-up: adjust the two reads in `internal/source/file` to pull `ItemValues` types 5/6; nothing downstream changes.

- [ ] **Step 6: Final commit if any fixes were made**

```bash
git add -A && git commit -m "$(cat <<'EOF'
chore: verification fixes for the tags feature

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_013a8VeN6ANgmBke8A5uewcS
EOF
)"
```

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| §1 Ingestion — `LibraryItem.Tags`, `splitTags` | Task 2 |
| §1 Ingestion — `PlaybackEvent.ItemTags`, episode←series | Task 3 |
| §1 Ingestion — migration `0004`, spine `item_tags` + refresh | Task 1 |
| §1 Ingestion — `dailyRow.tags` | Task 5 |
| §2 Aggregation — `tasteRows` / `baselineRows` `tag` dim | Task 5 |
| §2 Aggregation — `TagOverlap` cosine + shared | Task 6 |
| §2 Aggregation — library `TagsTop` / `TagCoverage` / `TagPairs` | Task 4 |
| §3 Storage — `agg_distribution` `'tag'`, `agg_totals` coverage, `agg_library_tag_pairs` | Task 7 |
| §3 Storage — `agg_profile_tag_overlap` write + wipe | Task 8 |
| §3 Storage — spine backfill via `ON CONFLICT` | Task 1 |
| §4 API — `/api/library/overview` `tags` object | Task 7 |
| §4 API — `/api/profile/{userID}` `taste.tag` / `baseline.tag` / `signature_tags` / `tag_overlap` | Task 9 |
| §4 API — `signatureShares` generalization + floor | Task 9 |
| §5 Frontend — types | Tasks 10, 11 |
| §5 Frontend — Library Overview tag section | Task 10 |
| §5 Frontend — Profiles taste additions | Task 11 |
| §6 Edge cases — sparse coverage single-line degrade | Tasks 7 (empty test), 10 (zero-coverage test), 11 (empty overlap test) |
| §6 Edge cases — signature/overlap support floor | Tasks 6, 9 |
| §6 Edge cases — co-occurrence `minSupport` | Task 4 |
| §6 Edge cases — episode with tagless series → nil | Task 3 (series lookup returns "" → `splitTags("")` → nil) |
| §7 Testing — aggregate / source / store / api / frontend | Tasks 2–11 (each carries its own) |
| §7 Testing — schema-notes update | Task 2 |
| Build order §10 | Task 12 |

No gaps.

**Placeholder scan:** No "TBD"/"TODO"/"handle edge cases". Every code step has literal code. The two "match the file's existing helper" notes (Tasks 1, 7, 8, 9 store-open; Task 10 `user-event`) are explicit instructions to mirror a named sibling, not vague hand-waves — the fallback is also given.

**Type consistency:**
- `source.PlaybackEvent.ItemTags []string` — added Task 1 Step 1, consumed Tasks 3/5.
- `file.splitTags` (Task 2) vs `store.splitTags`/`store.joinTags` (Task 1) — same names, different packages, both intentional; store's pair is `joinTags`/`splitTags`, file's is `splitTags` only.
- `aggregate.TagCoverage{ItemsTagged, ItemsTotal int64}`, `aggregate.TagPair{A, B string; Items int64}` — Task 4, consumed Task 7.
- `aggregate.ProfileTagOverlapRow{UserA, UserB, Range string; Cosine float64; Shared []string}` — Task 6, consumed Task 8.
- `store.TagPairDTO{A, B string; Items int64}` — Task 7; JSON `a`/`b`/`items`.
- `store.ProfileTagOverlap{User, UserName string; Cosine float64; Shared []string}` — Task 9; JSON `user`/`user_name`/`cosine`/`shared`.
- `signatureShares(user []TasteEntry, base []BaselineEntry, minPlays, minSec int64) []string` — Task 9; `signatureGenres` becomes a `0,0` wrapper.
- Frontend `LibraryOverview["tags"]` shape (Task 10) matches `store` JSON tags (`coverage.tagged/total`, `top`, `pairs.a/b/items`).
- Frontend `Profile.taste.tag` / `baseline.tag` / `signature_tags` / `tag_overlap` (Task 11) match `store` `Profile` JSON.
- `aggregate.Library` sets `Totals["tags.tagged_items"]` / `["tags.total_items"]`; `store.ReadLibraryOverview` reads those exact keys (Task 7). Consistent.
