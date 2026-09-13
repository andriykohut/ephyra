# Ephyra — Profile Overview — Design

Status: approved for planning
Date: 2026-09-13
Parent specs: `docs/superpowers/specs/2026-08-30-ephyra-dashboard-design.md`,
`docs/superpowers/specs/2026-08-31-ephyra-user-profiles-design.md`

---

## 1. Summary

`/profile` today is four analytical panels. This turns it into a profile: you land
on a page with an avatar, three headline numbers, a year-of-days activity strip,
an unbounded reverse-chronological list of everything you have played, and a set
of ranked charts — actors, directors, series, movies and episodes, genres. The
existing four panels move behind a second tab, unchanged.

Two of those charts need data Ephyra has never read. `jellyfin.db` carries
credits in `Peoples` / `PeopleBaseItemMap`, and it carries Jellyfin's own watched
tick in `UserData`. Both land in new upsert-only dimension tables so that the
charts survive deleting the media they were built from — the same property the
`playback_events` spine already has, extended to the things the spine joins
against.

Names in the charts link into Jellyfin. Items link directly; people and genres
need a name lookup against the HTTP API, cached, because Jellyfin's API invents
IDs for those that have nothing to do with the ones in the DB.

Non-negotiables carried from the parent specs: never write to Jellyfin; never
serve 5xx for a page because a refresh failed; the frontend ships in the binary;
no auth.

---

## 2. Goals & non-goals

### Goals

- `GET /api/profile/{userID}/overview?range=` and
  `GET /api/profile/{userID}/plays?before=…` behind a new default tab at
  `/profile/$user`.
- Credits and watched state materialised into `dim_credit` and `dim_played`,
  written by the library job, upsert-only, so a deleted item keeps its cast and
  its tick.
- Ranked people charts on two axes at once — hours watched and distinct titles —
  because they disagree, and the disagreement is the interesting part.
- Artwork on every chart and every row, proxied through Ephyra so the browser
  never sees the API key, with placeholders wherever Jellyfin has no image.
- Deep links into the Jellyfin web client for items, people and genres.
- The existing four panels keep working, byte-identical, behind a Stats tab.

### Non-goals

- No change to `aggregate.Profiles`, `aggregate.Watch`, or any `agg_profile_*`
  table that exists today.
- No auth, no per-user scoping. Every profile stays visible to every visitor.
- No custom date ranges — the same `30d|90d|1y|all` set as everywhere else.
- No writes to Jellyfin. No `SOURCE=api`.
- No SSE subscription from the profile page. Currently-watching comes from
  `Hub.Snapshot`, which never starts the poll loop.

---

## 3. Data flow

```
library job tick
  └─ Source.LibraryFacts(ctx)            ← WIDENED: + Credits, + Played
  └─ aggregate.Library(...)                unchanged
  └─ store.WriteLibraryAggregates(...)     unchanged
  └─ store.UpsertCredits(facts.Credits)  ← new txn, INSERT … ON CONFLICT, no DELETE
  └─ store.UpsertPlayed(facts.Played)    ← new txn, INSERT … ON CONFLICT, no DELETE

watch job tick
  └─ … spine append + aggregate.Watch + aggregate.Profiles   unchanged
  └─ credits := store.ReadCredits(ctx)
  └─ played  := store.ReadPlayed(ctx)
  └─ aggregate.People(all, credits, played, now)
       → agg_profile_people, agg_profile_top_items
  └─ resolveJellyfinRefs(ctx)            ← best-effort, capped, non-fatal
       → dim_jf_ref
```

The library job owns the two dimension tables because their data comes from
`LibraryFacts`; the watch job owns the aggregates because they need the spine. On
a cold start the watch job can win the race and produce empty people charts for
one cycle. It self-heals on the next run, and that is cheaper than coupling two
jobs that otherwise share nothing.

`resolveJellyfinRefs` is the only step in the whole pipeline that makes an HTTP
call. It runs last, it is capped, its failure is logged and discarded, and it is
skipped when Jellyfin is unconfigured or unreachable.

---

## 4. Schema — migration `0007_profile_people.sql`

### 4.1 Dimension tables

Three new tables, all keyed on natural keys, all written with
`INSERT … ON CONFLICT DO UPDATE`, none ever `DELETE`d.

```sql
dim_credit(item_id, person, kind, list_order)     PK (item_id, person, kind)
dim_played(user_id, item_id, played)              PK (user_id, item_id)
dim_jf_ref(kind, name, jf_id, checked_at, miss_count)  PK (kind, name)
```

`kind` in `dim_credit` is `actor | director`; `GuestStar` folds into `actor` at
read time. `kind` in `dim_jf_ref` is `person | genre`.

**Why they are never deleted.** `playback_events` outlives the plugin's retention
window and retains `item_name` / `item_genres` verbatim once an item leaves
Jellyfin. A chart that joined the spine against a table reconciled with the
current library would lose that durability at the join: delete a film and its
cast stops counting, its genre slice shrinks, its rank vanishes. `UserData` makes
this concrete — it is `ON DELETE CASCADE` from `BaseItems`, so Jellyfin's watched
tick for a deleted item is already gone by the next refresh. Snapshotting it into
`dim_played` is the only way the tick survives the media.

The cost is monotonic growth: rows accumulate for items that no longer exist.
Today's real library is 40,525 credit rows, about 2 MB. **A future "prune orphaned
dimension rows" cleanup would silently destroy the durability this whole design
rests on.** The migration says so, and so does CLAUDE.md.

One gap, accepted: an item played and then deleted before any library run
captured its credits has no cast, permanently. The window is one refresh
interval.

### 4.2 Aggregate tables

```sql
agg_profile_people(
  library, user_id, range, kind, person,
  watch_sec, plays, distinct_titles,
  watch_sec_played, plays_played, distinct_titles_played
)  PK (library, user_id, range, kind, person)

agg_profile_top_items(
  library, user_id, range, scope, item_id, name, series_name,
  watch_sec, plays,
  watch_sec_played, plays_played
)  PK (library, user_id, range, scope, item_id)
```

`scope` is `series | movie | episode`. Both tables are full-rewritten per run like
every other `agg_*` table.

`library` carries the same meaning as everywhere since migration 0005: `''` is the
all-libraries sentinel, `'Unknown'` is a real bucket. The page has a library
filter; these tables would be the only thing on it that ignored the filter.

Each row carries the filtered and unfiltered metric side by side rather than
splitting into two row sets. Three extra integer columns buy the "finished only"
toggle for free and keep the payload single. Rows are capped at the union of the
top 50 by each metric per `(library, user_id, range, kind|scope)`, so the tables
cannot grow with library size.

Top genres reuses `agg_profile_taste` (`dim='genre'`) as it stands. No new table.

### 4.3 Index

```sql
CREATE INDEX ix_playback_events_user_at ON playback_events (user_id, at DESC);
```

The existing indexes lead on `at` (`ux_playback_events_session`) or on
`(user_id, item_id)`. Neither serves a user-scoped scan in `at` order, which is
what the recents cursor does on every page.

`library` stays out of the index and is a residual predicate. Adding it as a
second column would help the filtered case and hurt the far more common
all-libraries one, which passes no library at all.

---

## 5. Source reads

`LibraryFacts` returns two new slices, `Credits` and `Played`.

**Credits** — `PeopleBaseItemMap` joined to `Peoples`, filtered to
`PersonType IN ('Actor','GuestStar','Director')`, IDs canonicalised via
`source.CanonID`. Jellyfin mints one `Peoples` row per `(name, type)` pair, so the
join yields a real per-credit type rather than a global label on the person; the
same human appears as separate rows when they act and direct.

**Played** — `(UserId, ItemId, Played)` from `UserData`, canonicalised.
`CustomDataKey` is part of that table's PK; the read rolls to one row per
`(user, item)` the way `playedStateQuery` already does.

All Actor / GuestStar / Director credits are kept, with no `ListOrder` cutoff. Bit
parts can therefore surface in a breadth ranking. Trimming to the top ~8 billed is
a one-line change once there is real data to pick a cutoff from.

---

## 6. Aggregation — `internal/aggregate/people.go`

```go
func People(
    events  []store.PlaybackEvent,
    credits []source.Credit,
    played  map[UserItem]bool,   // UserItem is a {UserID, ItemID} comparable key
    now     time.Time,
) PeopleAggregates
```

Pure — no I/O, no SQL — and tested with in-code snapshots like `library.go`.

`People` is library-agnostic. The scheduler scopes it exactly as it already scopes
`Profiles` — once over the whole history for `''`, then once per
`aggregate.DistinctEventLibraries` entry over
`aggregate.FilterEventsByLibrary(history, lib)` — and hands the map to
`store.WritePeopleAggregates(ctx, map[string]aggregate.PeopleAggregates)`.

Ranges are cut on the spine's `at` literal, which is server-local wall time and
**is not re-zoned**, matching `aggregate.Watch` and the parent spec's rule.

**Credit attribution.** A play credits its item's own `dim_credit` rows. If the
item has none and it is an episode, it credits the parent series' rows instead.
This fallback is load-bearing, not a nicety: on a real library 925/925 movies
carry credits but only 5,285/9,794 episodes do. Without it the people charts are
a movies-only view with a TV-shaped hole.

**Played filter.** Every metric is computed twice — once over all plays, once over
plays whose `(user_id, item_id)` is `played` in `dim_played`. The unfiltered pass
is what the toggle turns back on.

**Ranking.** Two orderings over the same rows: `watch_sec` descending, and
`distinct_titles` descending.

`distinct_titles` counts **series once, not per episode** — a series contributes
one to the count no matter how many of its episodes were played, and each movie
contributes one. Counting distinct `item_id` instead would make breadth a second,
noisier copy of hours, which is the opposite of why it exists. Credits attach per episode, so a single binge would
otherwise hand the top ten to one show's regular cast; breadth is the counterweight
and the page shows both rather than picking.

This never touches the completion verdict in `aggregate.Profiles`. The tick is
Jellyfin's opinion, the verdict is Ephyra's, and feeding one into the other would
make the completion panel circular.

---

## 7. API

`GET /api/profile/{userID}` is unchanged and backs the Stats tab.

### `GET /api/profile/{userID}/overview?range=30d|90d|1y|all&library=<name|all>`

Three headline numbers — total plays, total hours, and "watching since" (the
spine's earliest `at` for this user) — plus the activity strip, the five charts,
and `now_playing`.

`now_playing` comes from `Hub.Snapshot` filtered to this user, and is `null` when
live is unconfigured or Jellyfin is down. The activity strip is
`SELECT day, SUM(watch_sec) … WHERE user_id = ? GROUP BY day` over
`watch_events_daily`, which is already keyed per `(day, user_id, …)` — no new
table.

Reads `agg_*` / `*_daily`, LEFT JOINed against `dim_jf_ref` for the deep links
(genres here, people and items via `ReadProfilePeople` / `ReadProfileTopItems`).
People rows carry all six metrics and a `jf_url` that is `""` until resolution
lands.

### `GET /api/profile/{userID}/plays?before=<at>&before_id=<rowid>&limit=50&library=<name|all>`

Keyset pagination over `playback_events`, `ORDER BY at DESC, rowid DESC`, with
`WHERE user_id = ? AND (at, rowid) < (?, ?)`, plus `AND library = ?` when a
library is selected, LEFT JOINed against `dim_played` for the played tick.
Returns `next_cursor`, null at the end. `limit` is clamped to 200.

Both endpoints take `library` with the existing convention: `all` (or absent)
means the `''` sentinel, as `handleProfile` already does.

**This is a deliberate exception to "handlers read ONLY from store's `agg_*`
tables".** The spine is the history; a paginated history cannot be pre-rolled
without either capping it or rewriting a slab every refresh. It is an indexed
keyset read, not a scan. The exception is recorded here and in CLAUDE.md so the
next person finds it stated rather than discovers it.

Every list field serialises as `[]`, never `null`, via `orEmpty`, as everywhere
else.

---

## 8. Art proxy

`internal/api/nowplaying_art.go` generalises to `internal/api/art.go`:

| route | upstream |
|---|---|
| `GET /api/art/item/{itemId}?kind=primary\|backdrop&tag=` | `/Items/{id}/Images/{kind}` |
| `GET /api/art/person?name=<name>` | `/Persons/{name}/Images/Primary` |
| `GET /api/art/user/{userId}` | `/Users/{id}/Images/Primary` |

Person art takes the name as a **query parameter**. Jellyfin serves
`/Persons/{name}/Images/Primary` by name, and there is no usable ID to key on
(§12); person names contain characters that make a path segment a trap.

`/api/now-playing/art/{itemId}` is migrated to `/api/art/item/{itemId}` and
dropped. One binary, one frontend, no external callers.

Two changes to the cache, both forced by this page:

- **Byte budget instead of an entry count.** The LRU holds 32 entries. One profile
  renders roughly 50 posters and 40 faces, so 32 thrashes on every scroll. A
  ~128 MB budget at 25–80 KB per resized image is several thousand tiles, and it
  bounds memory by the thing that actually costs memory.
- **Negative caching.** Better than half the people in a real library have no
  image. Without caching the miss, every render re-asks Jellyfin for the same
  404s. Misses are cached and served as 404 with the same `Cache-Control`.

---

## 9. Deep links

`serverId` comes from `/System/Info` → `.Id`, fetched at startup — the hub already
primes that endpoint — and cached for the process lifetime.

| target | URL |
|---|---|
| movie / episode / series | `{JELLYFIN_URL}/web/#/details?id={item_id}&serverId={sid}` |
| person | `{JELLYFIN_URL}/web/#/details?id={jf_person_id}&serverId={sid}` |
| genre | `{JELLYFIN_URL}/web/#/list?parentId={jf_genre_id}&serverId={sid}` |

Items link with no lookup at all: `BaseItems.Id` canonicalised is the API's item
ID (§12). People and genres are synthesized ID spaces and need one.

Genres use `#/list` rather than `#/details` because it lands on the browsable full
list instead of a curated shelf.

**Resolution.** `resolveJellyfinRefs` collects the distinct names that landed in
`agg_profile_people` and the genre keys in `agg_profile_taste`, drops the ones
already resolved in `dim_jf_ref` and the misses that are not yet due for a retry,
and resolves what is left: `GET /Persons?searchTerm=<name>&limit=20` per person,
one `GET /Genres` for all of them.

`dim_jf_ref` carries a `miss_count`, and a miss is retried on exponential backoff
— 1d, 2d, 4d, … capped at 90d. A flat retry would mean every permanently
unresolvable name bills a lookup forever, and that set only ever grows; backoff
makes the cost of a name that will never resolve converge instead of accumulate.

`searchTerm` is a substring match — `Cranston` returns both *Bryan Cranston* and
*Cranston Johnson* — so the resolver takes the **exact** name match among the
results or records a miss. Never the first hit.

Capped at 200 lookups per run, so a first run on a large library spreads over a
few cycles instead of hammering Jellyfin.

**Measured cost** (real 12.0.3 server, 33,582 people). One lookup is ~65 ms and
~400 bytes. The cache is keyed by name globally, so overlap between users and
between the four ranges costs nothing. A realistic cold start is 500–800 unique
names — warm in three or four ticks, about 50 seconds of Jellyfin time in total,
once. Steady state is single-digit calls per day, because a call happens only when
a name that has never charted before enters a top 50; the 30d chart rolling over
resolves to cache hits. For comparison, Now Playing polls `/Sessions` 900 times an
hour while a browser has `/now` open.

Bulk-fetching every person instead was measured and rejected: 34 pages at ~615 ms
and ~318 KB each is 21 seconds and ~11 MB, against ~200 KB for the per-name path.

A name renders as a link only when `dim_jf_ref` has a hit; otherwise it is plain
text. A newly-charted person is unlinked for one cycle. Resolving ahead of the
click rather than on it is what keeps a dead link from ever rendering.

`dim_jf_ref` is upsert-only like the others, which means a cached link can rot:
delete the last item an actor appeared in, Jellyfin drops the person, and the link
404s in Jellyfin's own UI. Re-verifying every cached name each refresh to catch
that is not worth the calls.

---

## 10. Frontend

Route becomes `/profile/$user` with `?range` and `?tab`; `/profile?user=…`
redirects into it.

`profile.tsx` splits: the four panels move to `profile.stats.tsx` untouched,
`profile.overview.tsx` is new. New components in `src/components/`:

- `PlayList` — `useInfiniteQuery` over `/plays` with an intersection-observer
  sentinel.
- `RankGrid` — image tile, name, metric bar; renders the two people orderings
  side by side.
- `ActivityStrip` — the year-of-days strip, reusing the Watch Stats heatmap
  machinery.
- `Art` — renders a placeholder on error rather than a broken image.
- `Avatar`.

Placeholders are typographic blocks in the existing `@theme` tokens — initials for
people, title initials for items — so a library with no artwork still looks
deliberate rather than broken.

The "finished only" toggle defaults on and flips which of the six metrics the
charts read. It changes no request; both metrics are already in the payload.

The overview needs no ECharts at all, so it paints without ever pulling the lazy
chart chunk.

---

## 11. Degradation

Each of these is a state the page renders, not an error.

| condition | behaviour |
|---|---|
| plugin absent, spine empty | the existing "enable the plugin" state |
| Jellyfin API down | art 502s → every tile is a placeholder; no now-playing row; no new link resolution. Page fully usable. |
| `JELLYFIN_URL` unset | as above, and the resolver never runs |
| library has no credit metadata | people charts show an empty state naming the cause; other charts unaffected |
| item deleted from Jellyfin | play, cast, genre and tick all retained; artwork falls back to a placeholder |
| person unresolvable | name renders as plain text |

---

## 12. Verified against a real server

Checked 2026-09-13 against a live Jellyfin **12.0.3** and a **10.11.11** DB copy.
These are the facts the design leans on; re-check them after a Jellyfin upgrade.

- `Peoples(Id, Name, PersonType)` holds **one row per (name, type)**. Taika
  Waititi has six. `PeopleBaseItemMap(ItemId, PeopleId, Role, ListOrder,
  SortOrder)`; `Role` is the character name.
- 58,845 credit rows total; 40,525 for Actor / GuestStar / Director; 6,511 items
  with any credit.
- Credit coverage: Movie **925/925**, Episode **5,285/9,794**, Series **301/303**.
- **`Peoples.Id` is not the API's person ID.** `A$AP Rocky` is
  `EDEE2BFA-5383-41EE-972A-8C42C327F37D` in the DB and `83f0beb8…` over the API,
  on the same server. Not an MD5 of the name; the derivation was not found and is
  not needed.
- **`BaseItems.Id` canonicalised *is* the API's item ID.** Confirmed by looking the
  same GUID up both ways and getting the same film.
- `GET /Persons/{name}/Images/Primary` returns a real image, keyed by name, no ID
  required. Roughly half the people in a real library have no image.
- `searchTerm` is a substring match, not an exact one.
- `ImageInfos` holds only user avatars — one row per user with a profile image.
  Person and poster art are not in the DB at all.
- `UserData(ItemId, UserId, Played, PlayCount, PlaybackPositionTicks,
  LastPlayedDate, IsFavorite)` is **`ON DELETE CASCADE` from `BaseItems`**. Real
  distribution: 8,027 played, 4,031 not.
- Web client routes that work on 12.0.3: `#/details?id=<personId>&serverId=`,
  `#/details?id=<genreId>`, `#/list?parentId=<genreId>`.

---

## 13. Testing

- `testdata/library.fixture.sql` gains `Peoples`, `PeopleBaseItemMap` and richer
  `UserData` rows, **including an episode with no credits whose series has them**.
  That fixture is what locks the attribution fallback.
- Golden tests for `aggregate.People` with in-code snapshots, matching
  `library_test.go`: attribution fallback, GuestStar folding, the played filter,
  the two orderings, the top-50 union cap.
- Store tests: `dim_credit` / `dim_played` upsert-refreshes-but-never-deletes;
  keyset pagination across a page boundary and at the end of history; and both new
  agg tables round-tripping under a library scope and under the `''` sentinel.
- Handler tests: the art proxy's negative cache, and `/overview` with an empty
  spine.
- Resolver tests: exact-name match wins over a substring hit; a miss is recorded
  and its retry backs off rather than repeating each run; an HTTP failure does not
  fail the job.
- Frontend: `profile.overview.test.tsx` alongside the existing `profile.test.tsx`.

---

## 14. Docs

- `docs/schema-notes.md` gains a **People** section — the per-`(name, type)` row
  shape, the two ID spaces, the coverage numbers, the by-name image endpoint — and
  a note on `UserData`'s cascade.
- `CLAUDE.md` gains the `/plays` exception to the agg-only read rule, and the fact
  that `dim_credit`, `dim_played` and `dim_jf_ref` join `playback_events` as tables
  that are never rewritten and must never be pruned.
- `README.md`'s "Test against a real library" gains the credit-coverage check.

---

## 15. Known gaps, left deliberately

Recorded at merge. Each of these was decided against during the build, not
overlooked.

**"No longer in the library" is not shown.** The spine keeps a deleted item's
play, name and watch time, and the charts keep counting it — that durability is
the point of §4.1. But nothing on the page says an item is gone, because there is
no signal for it. It was briefly inferred from a poster 404 and removed: a 404
also means an item with no artwork, a transient failure, or Jellyfin being down,
and in that last case every row would announce that its item had been deleted.
A real signal needs a per-item presence table — `dim_credit` only covers items
that have credits, and there is no `dim_item`.

**The genres chart ignores the "finished only" toggle.** `agg_profile_taste` has
no played metrics, so `GenreDTO` cannot carry them and the panel cannot move when
the toggle flips. The panel says "all plays" rather than pretending. Fixing it
properly means adding played metrics to `aggregate.Profiles`, which backs the
Stats tab and which this work deliberately left alone.

**Episode rows read `Series — Title`, not `Series · S01E04 — Title`.**
`playback_events` stores no season or episode number; the plugin does not report
them and the spine never captured them. Only the live now-playing row, which
comes from the Jellyfin API rather than the spine, can show the full form.

**The activity strip's "today" is the browser's date.** The `day` keys it matches
are server-local wall dates, deliberately not re-zoned (see the parent spec). A
browser far enough ahead of the server renders the newest column blank and shifts
every cell by one weekday. Correcting it means sending the server's date in the
payload; for a self-hosted viewer, usually in the server's own zone, that was not
judged worth the extra field.

**`ReadPlays` clamps an out-of-range `limit` to 50** rather than to 200 as §7
says. Unreachable through the handler, which rejects anything outside 1..200
before the store sees it.
