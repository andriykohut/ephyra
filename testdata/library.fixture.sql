-- Stand-in for Jellyfin 10.11's jellyfin.db (EF-Core), with just the columns
-- Ephyra reads. Rows exercise the awkward cases: two libraries, movies and
-- episodes, a series row and a virtual episode (both excluded), a NULL Size, an
-- item with no video stream, one of each HDR flavour, a path with no extension.
PRAGMA journal_mode = WAL;

CREATE TABLE BaseItems (
  Id             TEXT PRIMARY KEY,
  Type           TEXT,
  Name           TEXT,
  Path           TEXT,
  Size           INTEGER,
  RunTimeTicks   INTEGER,
  DateCreated    TEXT,
  ProductionYear INTEGER,
  Genres         TEXT,
  Tags           TEXT,
  TopParentId    TEXT,
  Width          INTEGER,
  Height         INTEGER,
  SeriesId       TEXT,
  SeriesName     TEXT,
  IsVirtualItem  INTEGER DEFAULT 0,
  IsFolder       INTEGER DEFAULT 0
);

CREATE TABLE MediaStreamInfos (
  ItemId        TEXT,
  StreamIndex   INTEGER,
  StreamType    INTEGER,          -- 1 = Video, 0 = Audio, 2 = Subtitle
  Codec         TEXT,
  Width         INTEGER,
  Height        INTEGER,
  ColorTransfer TEXT,
  DvProfile     INTEGER
);

-- library folders: physical Folder (TopParentId target, on-disk name) plus the
-- CollectionFolder Jellyfin shows in the UI (Title Case).
INSERT INTO BaseItems (Id, Type, Name, IsFolder) VALUES
 ('1F000000-0000-0000-0000-0000000000F1', 'MediaBrowser.Controller.Entities.Folder',           'movies', 1),
 ('1F000000-0000-0000-0000-0000000000F2', 'MediaBrowser.Controller.Entities.Folder',           'shows',  1),
 ('11111111-1111-1111-1111-111111111111', 'MediaBrowser.Controller.Entities.CollectionFolder', 'Movies', 1),
 ('22222222-2222-2222-2222-222222222222', 'MediaBrowser.Controller.Entities.CollectionFolder', 'Shows',  1);

-- movies (TopParentId = the Movies folder Id, verbatim)
INSERT INTO BaseItems
 (Id, Type, Name, Path, Size, RunTimeTicks, DateCreated, ProductionYear, Genres, TopParentId, Width, Height) VALUES
 ('00000000-0000-0000-0000-00000000000A', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Alpha',   '/data/video/movies/Alpha (1994)/Alpha.mkv',   8000000000,  72000000000, '2024-01-05 10:00:00.0000000', 1994, 'Drama|Thriller', '1F000000-0000-0000-0000-0000000000F1', 3840, 2160),
 ('00000000-0000-0000-0000-00000000000B', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Bravo',   '/data/video/movies/Bravo (2001)/Bravo.mp4',   4000000000,  60000000000, '2024-01-20 10:00:00.0000000', 2001, 'Comedy',         '1F000000-0000-0000-0000-0000000000F1', 1920, 1080),
 ('00000000-0000-0000-0000-00000000000C', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Charlie', '/data/video/movies/Charlie (2019)/Charlie',   NULL,        90000000000, '2024-02-10 10:00:00.0000000', 2019, 'Drama',          '1F000000-0000-0000-0000-0000000000F1', 1280, 720),
 ('00000000-0000-0000-0000-00000000000D', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Delta',   '/data/video/movies/Delta (2022)/Delta.mkv',   15000000000, 80000000000, '2024-03-02 10:00:00.0000000', 2022, 'Sci-Fi|Drama',   '1F000000-0000-0000-0000-0000000000F1', 3840, 2160);

-- episodes (Shows library) — linked to the "Some Show" series row below
INSERT INTO BaseItems
 (Id, Type, Name, Path, Size, RunTimeTicks, DateCreated, ProductionYear, Genres, TopParentId, Width, Height, SeriesId, SeriesName) VALUES
 ('00000000-0000-0000-0000-0000000000E1', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E1', '/data/video/shows/Show/S01/S1E1.mkv', 1200000000, 18000000000, '2024-02-15 10:00:00.0000000', 2020, 'Drama', '1F000000-0000-0000-0000-0000000000F2', 1920, 1080, '00000000-0000-0000-0000-0000000000F0', 'Some Show'),
 ('00000000-0000-0000-0000-0000000000E2', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E2', '/data/video/shows/Show/S01/S1E2.MKV', 1300000000, 18000000000, '2024-03-16 10:00:00.0000000', 2020, 'Drama', '1F000000-0000-0000-0000-0000000000F2', 1920, 1080, '00000000-0000-0000-0000-0000000000F0', 'Some Show');

-- excluded: a series row, and a virtual (missing) episode
INSERT INTO BaseItems (Id, Type, Name, TopParentId, IsFolder) VALUES
 ('00000000-0000-0000-0000-0000000000F0', 'MediaBrowser.Controller.Entities.TV.Series', 'Some Show', '1F000000-0000-0000-0000-0000000000F2', 1);
INSERT INTO BaseItems (Id, Type, Name, TopParentId, IsVirtualItem, Size, RunTimeTicks) VALUES
 ('00000000-0000-0000-0000-0000000000F9', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E99 (missing)', '1F000000-0000-0000-0000-0000000000F2', 1, 999999999, 18000000000);

-- item tags (TMDb-keyword-ish). Alpha's raw value exercises splitTags:
-- mixed case, padding, and a duplicate all normalize away. Episodes E1/E2 carry
-- no Tags of their own -- the watch path inherits them from the series row.
UPDATE BaseItems SET Tags = 'Heist| vault |dystopia|heist' WHERE Id = '00000000-0000-0000-0000-00000000000A';
UPDATE BaseItems SET Tags = 'heist|vault'                  WHERE Id = '00000000-0000-0000-0000-00000000000B';
UPDATE BaseItems SET Tags = 'heist|vault'                  WHERE Id = '00000000-0000-0000-0000-00000000000C';
UPDATE BaseItems SET Tags = 'heist|dystopia'              WHERE Id = '00000000-0000-0000-0000-00000000000D';
UPDATE BaseItems SET Tags = 'slow burn|dystopia'          WHERE Id = '00000000-0000-0000-0000-0000000000F0';

-- video + audio streams
INSERT INTO MediaStreamInfos (ItemId, StreamIndex, StreamType, Codec, Width, Height, ColorTransfer, DvProfile) VALUES
 ('00000000-0000-0000-0000-00000000000A', 0, 1, 'hevc', 3840, 2160, 'smpte2084', NULL),    -- Alpha: HDR10
 ('00000000-0000-0000-0000-00000000000A', 1, 0, 'eac3', NULL, NULL, NULL, NULL),
 ('00000000-0000-0000-0000-00000000000B', 0, 1, 'h264', 1920, 1080, 'bt709', NULL),        -- Bravo: SDR
 ('00000000-0000-0000-0000-00000000000D', 0, 1, 'hevc', 3840, 2160, 'arib-std-b67', NULL), -- Delta: HLG
 ('00000000-0000-0000-0000-0000000000E1', 0, 1, 'av1',  1920, 1080, 'smpte2084', 8),       -- S1E1: Dolby Vision
 ('00000000-0000-0000-0000-0000000000E2', 0, 1, 'h264', 1920, 1080, '', NULL);             -- S1E2: SDR (blank transfer)
-- Charlie deliberately has no video stream row.

-- Users + per-user played state (Plan 2). Ids are dashed-uppercase like real
-- jellyfin.db; the source layer normalizes to dashless-lowercase.
CREATE TABLE Users (
  Id       TEXT PRIMARY KEY,
  Username TEXT NOT NULL
);
INSERT INTO Users (Id, Username) VALUES
 ('11111111-2222-3333-4444-555555555555', 'alice'),
 ('66666666-7777-8888-9999-aaaaaaaaaaaa', 'bob'),
 ('00000000-0000-0000-0000-0000000000AA', 'never_watches');

CREATE TABLE UserData (
  ItemId         TEXT NOT NULL,
  UserId         TEXT NOT NULL,
  CustomDataKey  TEXT NOT NULL DEFAULT '',
  LastPlayedDate TEXT,
  PlayCount      INTEGER NOT NULL DEFAULT 0,
  Played         INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (ItemId, UserId, CustomDataKey)
);
INSERT INTO UserData (ItemId, UserId, CustomDataKey, LastPlayedDate, PlayCount, Played) VALUES
 -- Bravo: alice finished it 3x, recently  -> not "never", not "stale"
 ('00000000-0000-0000-0000-00000000000B', '11111111-2222-3333-4444-555555555555', 'k1', '2025-06-01 20:00:00.000', 3, 1),
 -- Charlie: bob watched once, long ago  -> "stale"
 ('00000000-0000-0000-0000-00000000000C', '66666666-7777-8888-9999-aaaaaaaaaaaa', 'k1', '2023-01-05 21:00:00.000', 1, 1),
 -- Delta: two CustomDataKey rows for the same (item,user); MAX must win
 ('00000000-0000-0000-0000-00000000000D', '11111111-2222-3333-4444-555555555555', 'k1', '2025-05-01 10:00:00.000', 1, 0),
 ('00000000-0000-0000-0000-00000000000D', '11111111-2222-3333-4444-555555555555', 'k2', '2025-05-02 10:00:00.000', 2, 1),
 -- S1E1: bob, partial (Played=0 but LastPlayedDate set) -> series "Some Show" is NOT "never"
 ('00000000-0000-0000-0000-0000000000E1', '66666666-7777-8888-9999-aaaaaaaaaaaa', 'k1', '2025-04-10 22:00:00.000', 1, 0),
 -- a UserData row for a user absent from Users (must not crash the join)
 ('00000000-0000-0000-0000-00000000000B', '00000000-0000-0000-0000-0000000000BB', 'k1', '2025-02-02 02:00:00.000', 1, 1);
-- Alpha, S1E2: no UserData at all -> "never watched"
