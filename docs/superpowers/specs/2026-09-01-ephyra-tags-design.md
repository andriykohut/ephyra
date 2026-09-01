# Ephyra — Tags

Status: design, approved in brainstorming 2026-09-01.

Freeform Jellyfin item tags (in this library, mostly TMDb keywords) become two
new read-only views:

- **Library Overview** gains a `tags` section: coverage headline, a tag cloud,
  and a co-occurrence explorer.
- **Profiles** grows its existing `taste fingerprint` section: a tag `ShareBars`
  block, `signature_tags`, and a two-user overlap list.

No new page, no new nav entry. Nothing writes to Jellyfin. Tags ride the same
rails `Genres` already rides through the pipeline.

## Scope

In:

- Read `BaseItems.Tags` into the library snapshot and the playback spine.
- Episode plays inherit their parent `Series`' tags (episodes carry none).
- Library-side aggregates: tag frequency, tag coverage, tag co-occurrence pairs.
- Profile-side aggregates: `dim='tag'` taste rows + baseline (reuses existing
  `agg_profile_taste` / `agg_taste_baseline`), `signature_tags`, and a new
  per-user-pair tag-vector cosine overlap.
- API: extend `/api/library` and `/api/profiles/{user}` payloads. No new routes.
- Frontend: one new section on Library Overview, additions to the existing taste
  section on Profiles.

Out (deferred, with a named escape hatch where relevant):

- Per-`(user, series)` binge damping on tag weight. Known fix if TV-heavy
  fingerprints collapse to one show's keyword list; localized change to
  `tasteRows` / `baselineRows`. Not built until real output shows the problem.
- Fuzzy tag merging / normalization beyond lowercase+trim+dedup.
- Stop-list for noisy TMDb keywords (`duringcreditsstinger`, etc.).
- `Genres` + `Studios` in the tag namespace — this is freeform `Tags` only.
- True per-user (rather than vs-baseline) "signature" computation for widget C —
  v1 matches the existing `signature_genres` semantics (user share vs baseline).
- An `APISource` implementation of any of this.

## Weighting decision (load-bearing)

An episode play contributes its **series' tags**, weighted by that play's
`play_duration_sec`, exactly as a movie contributes its own tags weighted by its
play duration. No completion gate, no per-series damping in v1.

Rationale: it is the only option where TV-heavy users (which the Profiles data
skews toward) get a fingerprint at all; the weighting primitive
(`play_duration_sec` per event, series id already resolved) already exists; and
the fingerprint-vs-baseline widget is *meant* to surface "you watched a pile of
X". If the first real look shows every TV watcher's fingerprint collapsing to
their most-binged show's keywords, add per-`(user, series)` damping then.

## Pre-implementation check (first task, not a blocker)

Confirm against a real Jellyfin 10.11 `jellyfin.db` copy:

- `BaseItems.Tags` exists and is pipe-delimited (same shape as `Genres`).
- Rough tagged-item ratio (informs nothing structural, just sets expectations —
  the design already assumes sparse/patchy coverage).

Use `scripts/dump-jellyfin-schema.sh` or a throwaway `sqlite3` query on a copy.
Then add the `Tags` column to `docs/schema-notes.md` under "BaseItems".

If `Tags` turns out to live only in `ItemValues` (type 5 = Tag, 6 = InheritedTag)
rather than a `BaseItems` column, only the source read changes — pull distinct
`Value` for those types keyed by `ItemId`. Everything downstream is unaffected.

## Section 1 — Ingestion (source + spine)

### Library snapshot

`source.LibraryItem` gains:

```go
Tags []string // normalized: lower(trim(x)), empties dropped, deduped within the item
```

`internal/source/file/queries.go`: the item `SELECT` (currently `COALESCE(i.Genres, '')`)
adds `COALESCE(i.Tags, '')`. Scan into a string, split on `|`.

Generalize `splitGenres` (`queries.go:264`) into a shared helper — rename to
`splitPipe(s string) []string` doing lowercase + trim + drop-empty + dedup, and
have both genres and tags call it. (Genres are already effectively that shape;
the lowercase change is cosmetically visible in `queries_test.go:45` which
expects `"Drama"` / `"Thriller"` — update the expectation, or keep a
genre-specific splitter that does not lowercase and add a `splitTags` that does.
Prefer the latter to avoid churning genre output: `splitTags` lowercases,
`splitGenres` stays as-is.)

### Playback spine

`source.PlaybackEvent` gains:

```go
ItemTags []string // nil when unknown; for episodes these are the parent Series' tags
```

`internal/source/file/playback.go`: the targeted `jellyfin.db` read
(`playback.go:77`+, currently selecting `Genres` among the per-item facts) adds
`Tags`. Enrichment logic:

- Movie: `ItemTags = splitTags(row.Tags)` from the item's own `BaseItems` row.
- Episode: `ItemTags` comes from the parent `Series` `BaseItems` row, looked up
  by the series id the enrichment already resolves (`seriesID` in the scan at
  `playback.go:77`). If the series is unresolved / gone, `ItemTags` is nil.

This means the episode-facts query needs the series row's `Tags`. If the current
code already fetches a series row for `seriesName`, add `Tags` to that select;
otherwise add a small `SELECT Id, Tags FROM BaseItems WHERE Id IN (<series ids>)`
pass and map it in.

### aggregate plumbing

`internal/aggregate/profile.go` `dailyRow` (line 39) gains `tags []string`.
`rollDaily` (line 64) populates it from `e.ItemTags` when creating the row,
parallel to `genres: e.ItemGenres`.

## Section 2 — Aggregation (pure functions)

### Profiles — `internal/aggregate/profile.go`

**`tasteRows` (line 269):** inside `for _, r := range rows`, add

```go
for _, t := range r.tags {
    add("tag", t, r.watchedSec, r.plays)
}
```

Produces widget A (top tags you watch, per range) directly, and feeds widget B.

**`baselineRows` (line 474):** inside its `for _, r := range rows`, add

```go
for _, t := range r.tags {
    m[key{"tag", t}] += r.watchedSec
}
```

Baseline is "what all users watched", aggregated — same as the existing genre
baseline. `dim='tag'` rows flow through the existing `agg_profile_taste` /
`agg_taste_baseline` writes and reads with no schema change.

**Widget D — `TagOverlap`:** new pure function.

```go
func TagOverlap(events []source.PlaybackEvent, now time.Time) []ProfileTagOverlapRow
```

- Reuse `rollDaily` + the same range windowing `Profiles` uses (`profileRangeList`,
  `rangeStart`).
- Per `(range)`, build each user's tag vector `map[string]int64` (tag → summed
  `watchedSec`, via the same episode-inherits-series-tags rows).
- For every unordered user pair `(a, b)` with `a < b` where **both** vectors are
  non-empty: `cosine = dot(a,b) / (‖a‖·‖b‖)`. `shared` = tags present in both,
  ranked by `min(shareA, shareB)` where `shareX = vecX[tag] / sum(vecX)`, top 8.
- Emit `ProfileTagOverlapRow{UserA, UserB, Range, Cosine, Shared []string}`.
  Solo user, or a user with no tagged watch history → no rows for that pair.

Wire into whichever pass owns profile aggregation (the watch/profiles job) next
to the `Profiles(...)` call; add `TagOverlap []ProfileTagOverlapRow` to
`ProfileAggregates` (`rows.go:88`).

### Library — `internal/aggregate/library.go`

`LibraryAggregates` (line 30) gains:

```go
TagsTop     []LabeledCount   // tag -> item count, desc, capped 120
TagCoverage TagCoverage      // { ItemsTagged, ItemsTotal int64 }
TagPairs    []TagPair        // { A, B string; Items int64 }, A < B, desc, capped 400
```

with

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

Computed in the single existing `snap.Items` walk in `Library(...)`:

- `TagsTop`: `map[string]int64` tag → count; `ItemsTagged++` for any item with
  ≥1 tag; `ItemsTotal` = len(items) (or the same denominator the existing
  `Totals["items"]` uses — match it).
- `TagPairs`: for each item, for each unordered pair of its distinct tags,
  `pairs[{a,b}]++`. After the walk, drop pairs with `Items < minSupport`
  (`minSupport = 3`), sort desc by `Items` then `(A, B)`, cap at 400.
- `TagsTop` sort desc by count then label, cap at 120.

Coverage lives only in `TagCoverage` (the typed struct field), not in the
`Totals` map. It is persisted to its own single-row table and the API reads it
from there — see Section 3.

## Section 3 — Storage: migration `0004_tags.sql`

```sql
ALTER TABLE playback_events ADD COLUMN item_tags TEXT NOT NULL DEFAULT '';

CREATE TABLE agg_library_tags (
  tag   TEXT NOT NULL PRIMARY KEY,
  items INTEGER NOT NULL
);

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

Plus a single-row coverage table (`COUNT(*)` on `agg_library_tags` would give
distinct tag count, not tagged-item count, so the two counts are stored
explicitly):

```sql
CREATE TABLE agg_library_tag_coverage (
  tagged INTEGER NOT NULL,
  total  INTEGER NOT NULL
);
```

All four tables are forward-only, integer-prefixed, and do not touch
`schema_migrations`. All are rewritten per refresh like the other `agg_*`.

### Writes

- `internal/store/writes.go` (library aggregates txn): add
  `DELETE FROM agg_library_tags`, `agg_library_tag_pairs`,
  `agg_library_tag_coverage` to the existing wipe, then insert from
  `LibraryAggregates.TagsTop` / `TagPairs` / `TagCoverage`.
- `internal/store/writes_profile.go`: add `DELETE FROM agg_profile_tag_overlap`
  to the list at line 24, then insert from `ProfileAggregates.TagOverlap`.
  `agg_profile_taste` / `agg_taste_baseline` inserts already loop over
  `a.Taste` / `a.Baseline` and will carry `dim='tag'` rows with no change.
- The watch job writes no `agg_*` when `plugin_available=false` — unchanged; tag
  overlap simply is not written in that case.

### Spine backfill

The watch job's upsert on `dedup_hash` conflict already refreshes
`item_genres` / `item_runtime_sec` / `item_year`. Add `item_tags` to that
`ON CONFLICT ... DO UPDATE SET` list. Existing rows backfill on the next watch
run; no data migration.

## Section 4 — API

### `GET /api/library`

`data` gains:

```jsonc
"tags": {
  "coverage": { "tagged": 812, "total": 2140 },
  "top":   [ { "label": "based on novel or book", "count": 143 }, ... ],  // <=120, desc
  "pairs": [ { "a": "heist", "b": "vault", "items": 11 }, ... ]           // <=400, desc
}
```

Co-occurrence is resolved client-side from `pairs`; no per-tag endpoint.
All arrays through `orEmpty`. `coverage` is always present (zeroes for an
untagged library).

### `GET /api/profiles/{user}?range=`

- `taste` gains `tag: TasteEntry[]` (parallel to `genre` / `decade` / `length`).
- `baseline` gains `tag: BaselineEntry[]`.
- `taste` gains `signature_tags: string[]` next to `signature_genres`, computed
  by `signatureGenres(...)` reused on the tag dim (rename the helper to
  `signatureShares` or add a thin `signatureTags` wrapper). **Support floor:**
  skip a tag whose user row has `< 2` plays or `< 120` watch-seconds before
  scoring, so a trivial sample cannot become a signature. (The genre version has
  only `delta > 0`; the floor is tag-specific and lives in the wrapper or as a
  param.)
- new key `tag_overlap`:

```jsonc
"tag_overlap": [
  { "user": "bob", "user_name": "Bob", "cosine": 0.62,
    "shared": ["slow-burn", "dystopia", "one-location"] },
  ...
]   // this user vs every other user with history in the requested range, desc by cosine
```

Reader (`internal/store/reads_profile.go`): `readProfileTaste` gains a `tag`
branch in both the `agg_profile_taste` and `agg_taste_baseline` scans; new
`readTagOverlap` selects from `agg_profile_tag_overlap` where
`user_a = ? OR user_b = ?` and `range = ?`, normalizing so the *other* user is
reported, joining `Users` (already available in the profile read) for
`user_name`, splitting `shared` on `|`. All new slices through `orEmpty`.

`meta` / staleness unchanged. No new routes.

## Section 5 — Frontend

### `web/src/api/types.ts`

Add `tags` to the library response type; add `tag` to `taste` / `baseline`,
`signature_tags: string[]` to `taste`, and `tag_overlap: TagOverlap[]` to the
profile response type.

### Library Overview — `web/src/routes/library.tsx`

New `<section>` after the "added over time" `ChartPanel` (around line 209),
heading `tags`, plain DOM (no ECharts — paints immediately, unaffected by the
lazy chart chunk):

- **Coverage line** — `812 / 2,140 items tagged (38%)` in the existing
  mono/muted caption style. If `coverage.total === 0` or `coverage.tagged === 0`:
  render only `No tags in this library.` and nothing else in the section.
- **Tag cloud** — `tags.top`, font-size by `count` on a sqrt scale, ~5 discrete
  steps, clamped; flex-wrap; tier drives `text-ink` vs `text-muted`. Each tag is
  a `<button>`; clicking sets `selectedTag` state.
- **Co-occurrence panel** — reads `selectedTag`. From `tags.pairs`, take pairs
  where `a === selectedTag || b === selectedTag`, map to the other side + count,
  sort desc, show as a ranked list: `heist → one-last-job (7) · vault (5) · ...`.
  No selection → a hint line (`pick a tag`). Selected tag with no qualifying
  pairs → `no co-occurring tags above the noise floor`.

### Profiles — `web/src/routes/profile.tsx`

Extend the existing `taste fingerprint` `<Section>` (line 332):

- Under the genre `<ShareBars>`, a second `<ShareBars user={d.taste.tag}
  baseline={d.baseline.tag} />` labeled `tags`. `ShareBars` is unchanged; if it
  hard-codes a "No genre data" string, generalize that copy or pass a label
  prop. Empty `d.taste.tag` → `ShareBars` shows its empty state.
- `signature_tags` rendered alongside the existing `signature_genres` "leans …"
  line: `also leans slow-burn, dystopia`.
- New sub-block `overlap with others` — from `d.tag_overlap`: one row per other
  user, `Bob — 62% · slow-burn, dystopia, one-location`, with a bar whose width
  is `cosine`. Sorted desc (server already does). `d.tag_overlap.length === 0`
  (solo user / no shared history) → the sub-block is not rendered.
- All of this is inside the range-switched query, so it tracks 30d/90d/1y/all
  automatically.

## Section 6 — Edge cases

- **Sparse coverage is the expected case.** Every widget degrades to one honest
  line, never an empty frame. The coverage headline is always shown when any
  tags exist, so a 12%-tagged library reads as 12%-tagged.
- **Signature / overlap support floor** — `signature_tags`: `< 2` plays or
  `< 120` watch-seconds on the user row disqualifies a tag. `TagOverlap`: a
  user whose in-range tag vector is empty produces no pair rows.
- **Co-occurrence noise floor** — `minSupport = 3` items per pair, applied in
  `aggregate.Library` before the cap.
- **TMDb keyword quirks** — `duringcreditsstinger`, `based on novel or book`,
  etc. pass through untouched in v1. Spaces in tags are fine everywhere (map
  keys, JSON strings, pipe-join is safe — tags never contain `|`; if one does,
  `splitTags` drops the empty fragments and the halves survive as separate
  tags, acceptable).
- **Episode with a resolved series that itself has no tags** → `ItemTags` nil,
  contributes nothing. Not an error.
- **`plugin_available=false`** → no `agg_profile_tag_overlap` rows, no
  `dim='tag'` taste rows (no events at all); Library-side tag widgets still work
  from the library snapshot.

## Section 7 — Testing

### `internal/aggregate`

- Extend in-code snapshot/event fixtures with tags.
- `Library`: tag frequency counts; coverage ratio (tagged vs total); pair
  counting; `minSupport` cutoff drops a 2-item pair, keeps a 3-item pair;
  `TagsTop` / `TagPairs` caps and sort order.
- `tasteRows` / `baselineRows`: `dim='tag'` rows present with expected
  `WatchSec` / `Plays`.
- Episode inherits series tags: an episode event with a series whose row has
  tags produces tag taste rows; a movie event uses its own tags.
- `TagOverlap`: two users with hand-computable vectors → expected `cosine`
  (within epsilon) and `shared` order; a third solo user → no pair rows
  involving them.

### `internal/source/file`

- `testdata/library.fixture.sql`: add `Tags` values to some `BaseItems` rows,
  including a `Series` row with tags whose `Episode` child has none, and at
  least one item with no tags.
- Assert `LibraryItem.Tags` parsed + normalized (lowercase, trimmed, deduped,
  no empties).
- Assert `PlaybackEvent.ItemTags`: movie → own tags; episode → parent series'
  tags; unresolved series → nil.
- Update `queries_test.go:45` only if `splitGenres` behavior changes (it should
  not, per Section 1's "keep `splitGenres`, add `splitTags`" decision).

### `internal/store`

- Migration `0004` applies on top of `0003`.
- Round-trip `item_tags` through the spine upsert incl. refresh-on-conflict.
- Round-trip `agg_library_tags`, `agg_library_tag_pairs`,
  `agg_library_tag_coverage`, `agg_profile_tag_overlap`.
- `dim='tag'` rows round-trip through `agg_profile_taste` / `agg_taste_baseline`.
- Every new slice comes back `[]`, never `null`.

### `internal/api`

- `/api/library` response includes `tags` with `coverage` / `top` / `pairs`;
  empty-library shape has `coverage:{tagged:0,total:0}` and `[]` arrays.
- `/api/profiles/{user}` includes `taste.tag`, `baseline.tag`,
  `taste.signature_tags`, `tag_overlap`; solo-user response has
  `tag_overlap: []`.

### Frontend

- `library.test.tsx`: cloud renders tags; clicking a tag filters the
  co-occurrence list; `coverage.total === 0` renders the single-line message and
  no cloud.
- `profile.test.tsx`: tag `ShareBars` renders; `signature_tags` line shows;
  overlap list renders rows with bars; `tag_overlap: []` hides the sub-block.

## Build order

1. Pre-implementation check + `docs/schema-notes.md` update.
2. Migration `0004` + store read/write scaffolding + tests (tables empty-safe).
3. Source: `splitTags`, `LibraryItem.Tags`, `PlaybackEvent.ItemTags` (incl.
   episode→series), fixtures + tests.
4. `aggregate.Library` tag outputs + tests.
5. `aggregate` profile: `dailyRow.tags`, `tasteRows` / `baselineRows` `tag`
   dim, `TagOverlap` + tests.
6. Wire aggregates → store writes; job wiring for `TagOverlap`.
7. API: extend library + profile payloads, `signatureTags` wrapper with floor,
   `readTagOverlap` + tests.
8. Frontend types + Library Overview section + tests.
9. Frontend Profiles taste-section additions + tests.
10. `make lint test`, then `make build`, then a manual pass against a real DB
    copy per README's "Test against a real library".
