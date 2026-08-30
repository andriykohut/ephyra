# Jellyfin schema notes

What Ephyra's queries assume about Jellyfin 10.11's `library.db`. If your dump
(`scripts/dump-jellyfin-schema.sh`) disagrees, write the difference down here and
change `testdata/library.fixture.sql` and `internal/source/file/queries.go` to
match. The tests then keep everyone honest.

## TypedBaseItems (columns we touch)

`guid` BLOB, `type` TEXT, `Name` TEXT, `Path` TEXT, `Container` TEXT,
`Size` BIGINT, `RunTimeTicks` BIGINT, `DateCreated` TEXT, `ProductionYear` INT,
`Genres` TEXT (pipe-delimited), `TopParentId` TEXT (un-dashed hex guid),
`Width` INT, `Height` INT, `IsVirtualItem` BIT, `IsFolder` BIT

`type` values:
- Movie  = `MediaBrowser.Controller.Entities.Movies.Movie`
- Episode = `MediaBrowser.Controller.Entities.TV.Episode`
- Series = `MediaBrowser.Controller.Entities.TV.Series`
- CollectionFolder = `MediaBrowser.Controller.Entities.CollectionFolder`

## MediaStreams (columns we touch)

`ItemId` BLOB (= `TypedBaseItems.guid`), `StreamIndex` INT, `StreamType` TEXT,
`Codec` TEXT, `Width` INT, `Height` INT, `ColorTransfer` TEXT, `DvProfile` INT

HDR read: `ColorTransfer='smpte2084'` -> HDR10, `'arib-std-b67'` -> HLG,
`DvProfile > 0` -> Dolby Vision, else SDR.

## Things to check against a real dump

- [ ] `TypedBaseItems` actually has: `Size`, `Container`, `TopParentId`, `Genres`, `DateCreated`, `IsVirtualItem`
- [ ] `TopParentId` format — dashes? case? Adjust the `upper(replace(...,'-',''))` in queries.go
- [ ] `MediaStreams` has `ColorTransfer` and `DvProfile` (older builds may lack `DvProfile` — then it reads as NULL, which is fine)
- [ ] `DateCreated` string format matches one of the layouts in `parseJellyfinTime`
- [ ] missing/unaired episodes carry `IsVirtualItem = 1`
