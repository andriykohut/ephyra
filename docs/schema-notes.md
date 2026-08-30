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

## playback_reporting.db  (Plan 2)

Still a separate SQLite file next to `jellyfin.db`. On the test system it held
very little — the plugin may need enabling, or just not have much history yet.

## Not yet verified

- Older Jellyfin (10.10 and earlier) with `library.db` — `queries.go` targets the
  10.11 schema only. If someone runs it against `library.db` the queries will
  fail on unknown tables; needs a schema-version branch or the `APISource`.
- `UserData` (Cleanup, Plan 2): `ItemId`/`UserId`/`Played`/`PlayCount`/
  `LastPlayedDate` — present, not wired up.
