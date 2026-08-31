# Jellyfin schema notes

What Ephyra's queries assume about Jellyfin's item store. Verified against a real
**Jellyfin 10.11.11** (linuxserver image) on 2026-08-30.

## Where the DB is

10.11 moved the item store from `library.db` to **`jellyfin.db`** (EF-Core). The
file sits at `<jellyfin-config>/data/jellyfin.db`, or one level deeper —
`<jellyfin-config>/data/data/jellyfin.db` — on the linuxserver image (its datadir
is `/config/data`, not `/config`). `FileSource.dbCandidates` checks both, and
also `library.db` for older installs.

`docs/../scripts/dump-jellyfin-schema.sh` dumps schema + sample rows for
comparison.

## BaseItems  (was TypedBaseItems)

`Id` TEXT (dashed-uppercase GUID, e.g. `0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9`),
`Type` TEXT, `Name` TEXT, `Path` TEXT, `Size` BIGINT, `RunTimeTicks` BIGINT,
`DateCreated` TEXT (`2025-04-13 16:51:00.4165068`, space-separated, no `Z`),
`ProductionYear` INT, `Genres` TEXT (pipe-delimited), `TopParentId` TEXT,
`Width` INT, `Height` INT, `IsVirtualItem` INT, `IsFolder` INT.

- No `Container` column — Ephyra derives it from `Path`'s extension.
- `Type` values unchanged: Movie = `MediaBrowser.Controller.Entities.Movies.Movie`,
  Episode = `...TV.Episode`, Series = `...TV.Series`,
  Folder = `...Entities.Folder`, CollectionFolder = `...Entities.CollectionFolder`.

## MediaStreamInfos  (was MediaStreams)

`ItemId` TEXT (= `BaseItems.Id`), `StreamIndex` INT, `StreamType` **INT enum**
(0 Audio, 1 Video, 2 Subtitle, 3 EmbeddedImage, 4 Data), `Codec` TEXT,
`Width`/`Height` INT, `ColorTransfer` TEXT (`smpte2084` = HDR10,
`arib-std-b67` = HLG), `DvProfile` INT (`> 0` = Dolby Vision),
`Hdr10PlusPresentFlag` INT (new; not used yet).

## Library attribution

`BaseItems.TopParentId` points at the **physical root `Folder`** (its `Name` is
the on-disk directory, e.g. `movies`). When a `CollectionFolder` shares that name
case-insensitively, Ephyra uses its display name (`Movies`). `AncestorIds` would
be the "proper" route to the CollectionFolder, but on the test DB a sizeable
fraction of items had no CollectionFolder ancestor row, so it can't be relied on.

## playback_reporting.db  (Watch Stats)

A separate SQLite file next to `jellyfin.db`, written by the Playback Reporting
plugin. Missing file, or a missing `PlaybackActivity` table, means the plugin
isn't installed — `FileSource.PlaybackEvents` returns `source.ErrPluginUnavailable`
and the watch job records `plugin_available=false` (not a failure).

```sql
CREATE TABLE PlaybackActivity (
  DateCreated    DATETIME NOT NULL,  -- 'YYYY-MM-DD HH:MM:SS.fffffff', SERVER-LOCAL wall time, no zone
  UserId         TEXT,               -- 32 hex, no dashes, lowercase
  ItemId         TEXT,               -- 32 hex, no dashes, lowercase
  ItemType       TEXT,               -- 'Movie' | 'Episode' (we keep only these)
  ItemName       TEXT,
  PlaybackMethod TEXT,               -- see buckets below
  ClientName TEXT, DeviceName TEXT,
  PlayDuration   INT                 -- SECONDS (0 is valid)
);
```

No `RemoteAddress`. No primary key.

### ID normalization

The plugin stores dashless-lowercase ids; `jellyfin.db` (`Users.Id`,
`BaseItems.Id`, `UserData.*`) stores dashed-uppercase. **Ephyra's canonical form
is dashless lowercase** (`source.CanonID` in Go, `lower(replace(id,'-',''))` in
SQL). Every id column in the store — `dim_user.id`, `watch_events_daily.*`,
`agg_watch_heatmap.user_id`, `agg_played_core.*`, `agg_cleanup.item_id` — is
canonical.

### `PlaybackMethod` → 5 buckets  (`aggregate.methodBucket`, string match)

| Plugin string | Bucket |
|---|---|
| `DirectPlay` | `DirectPlay` |
| `Transcode (v:direct a:direct)` | `Remux` |
| `Transcode (v:direct a:…)` (a not `direct`) | `AudioTranscode` |
| `Transcode (v:… …)` (v not `direct`) | `VideoTranscode` |
| anything else, incl. unparseable `Transcode (...)` | `Other` |

There is no `DirectStream`.

### Timestamps — no re-zoning

`jellyfin.db` `DateCreated` is UTC (Plan 1 shifts it into `TZ` for the growth
chart). `playback_reporting.db` `DateCreated` is server-local wall time, so
`aggregate.Watch` buckets `day`/`dow`/`hour` off the literal parsed components
and does **not** apply `TZ`. Operators run Jellyfin's host and the Ephyra
container in the same zone.

## Users / UserData  (Watch Stats + Cleanup)

```sql
CREATE TABLE Users    (Id TEXT PRIMARY KEY, Username TEXT NOT NULL, ...);
CREATE TABLE UserData (ItemId TEXT, UserId TEXT, CustomDataKey TEXT,  -- PK all three
                       LastPlayedDate TEXT NULL, PlayCount INT NOT NULL,
                       Played INT NOT NULL, ...);
```

- `UserData` can have multiple rows per `(ItemId, UserId)` (differing
  `CustomDataKey`). Aggregate `GROUP BY ItemId, UserId` with `MAX` first.
- "Never watched" (Cleanup) = `LastPlayedDate IS NULL` for **every** user. A
  partial play sets `LastPlayedDate` while `Played` stays `0`, so `Played` is not
  the test.
- Episode → series linkage is `BaseItems.SeriesId` / `SeriesName` (populated in
  practice).

## /Sessions  (Now Playing)

Now Playing doesn't touch SQLite. `internal/jellyfin` talks to Jellyfin's HTTP
API directly — `GET /Sessions?ActiveWithinSeconds=960` on the poll,
`GET /System/Info` for the header, `GET /Items/{id}/Images/{kind}` for art.
Shapes below checked against **10.11.11** on 2026-08-31. `internal/jellyfin`
unmarshals only the fields listed here; `encoding/json` drops the rest.

### Auth

`Authorization: MediaBrowser Token=<api-key>` on every request. `X-Emby-Token:
<key>` also works on 10.11.11, but Ephyra uses the `Authorization` form.

### `GET /Sessions`

Returns every active session, idle ones included (app open, nothing playing).
Ephyra keeps a session only when `NowPlayingItem != null`, `PlayState != null`,
**and** `NowPlayingItem.MediaType == "Video"` — audio sessions and idle apps fall
out. (A playing item always carries a `PlayState`; the check is there so the
normalizer can dereference it without a guard.)

Per-session fields Ephyra reads:

- `Id` — session id.
- `UserName` — already on the session, so no `/Users` call. (`UserId` is
  dashless-lowercase like the store, but Now Playing doesn't persist it.)
- `Client`, `DeviceName` — free-text client / device labels.
- `RemoteEndPoint` — bare IP string, no port, no brackets. `net.ParseIP` plus the
  loopback / private / link-local checks classify local vs remote.
- `PlayState.PlayMethod` — `DirectPlay` | `DirectStream` | `Transcode`. It lives
  on `PlayState`, **not** at the top level.
- `PlayState.IsPaused` (bool), `PlayState.PositionTicks` (int),
  `PlayState.AudioStreamIndex` (int, which audio stream is playing).
- `TranscodingInfo` — present only while transcoding; **absent on direct-play**.
  See below.
- `NowPlayingItem` — `Id`, `Name`, `Type` (`Movie` | `Episode`), `MediaType`,
  `Container`, `RunTimeTicks` (int), `SeriesName` (`""` for movies),
  `IndexNumber` / `ParentIndexNumber` (int, → `S<parent>E<index>`),
  `ProductionYear`, `ImageTags.Primary`, `ParentBackdropItemId` +
  `ParentBackdropImageTags[]`, and `MediaStreams[]` (`Type` `Video` / `Audio` /
  `Subtitle`, `Index`, `Codec`, `Width`, `Height`, `BitRate`, `Channels`,
  `ChannelLayout`, `VideoRangeType`, `IsDefault`).

**Ticks are 100 ns.** `PositionTicks` and `RunTimeTicks` ÷ 1e7 = seconds.

**No overall bitrate on direct-play.** Sum the playing video stream's `BitRate`
and the selected audio stream's `BitRate` (audio picked by
`PlayState.AudioStreamIndex`, else the default audio stream).

### `TranscodingInfo`  (transcode sessions only)

`Bitrate`, `Container`, `VideoCodec`, `AudioCodec`, `IsVideoDirect`,
`IsAudioDirect` (bools), `TranscodeReasons` (string slice, passed through raw),
`CompletionPercentage` (float — how far ahead of the player the transcoder has
buffered), `HardwareAccelerationType` (`""` / `"none"` when software). The
`hevc→h264` / `mkv→ts` strings in the DTO come from diffing these against the
source streams.

**All three JSON fixtures are hand-built stand-ins.**
`testdata/sessions.directplay.json`, `testdata/sessions.transcode.json`, and
`testdata/systeminfo.json` mirror the real 10.11 payload shape — field names,
types, tick magnitudes, the lot — but nothing in them came from a real server.
Every user, title, series, server name, path, and IP is invented, and no live
transcode was ever captured. Same status as the `*.fixture.sql` files: right
shape, made-up contents.

### `GET /System/Info`

Ephyra reads `ServerName` and `Version`; the rest of the response is ignored.
`hub.Prime` fetches it once at startup for the first frame's header, and the poll
loop refreshes it on a 10-minute TTL. A failure here is not fatal — Now Playing
shows its degraded state until a poll succeeds.

### `GET /Items/{id}/Images/{kind}`

`kind` is `Primary` or `Backdrop`, with an optional `?tag=`. Ephyra proxies this
through `/api/now-playing/art/{itemId}` so the API key never reaches the browser;
the upstream body and `Content-Type` stream straight back, with a small LRU in
front.

## Not yet verified

- Older Jellyfin (10.10 and earlier) with `library.db` — `queries.go` targets the
  10.11 schema only. If someone runs it against `library.db` the queries will
  fail on unknown tables; needs a schema-version branch or the `APISource`.
