-- Hand-built stand-in for Jellyfin's library.db, with just the columns Ephyra
-- reads. Rows are chosen to exercise the awkward cases: two libraries, movies
-- and episodes, a series row and a virtual episode (both excluded), a NULL
-- Size, an item with no video stream, and one of each HDR flavour.
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

-- movies (TopParentId = un-dashed upper hex of the Movies folder guid)
INSERT INTO TypedBaseItems
 (guid, type, Name, Container, Size, RunTimeTicks, DateCreated, ProductionYear, Genres, TopParentId, Width, Height) VALUES
 (x'0000000000000000000000000000000A', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Alpha',   'mkv', 8000000000,  72000000000, '2024-01-05 10:00:00.0000000Z', 1994, 'Drama|Thriller', '11111111111111111111111111111111', 3840, 2160),
 (x'0000000000000000000000000000000B', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Bravo',   'mp4', 4000000000,  60000000000, '2024-01-20 10:00:00.0000000Z', 2001, 'Comedy',         '11111111111111111111111111111111', 1920, 1080),
 (x'0000000000000000000000000000000C', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Charlie', 'mkv', NULL,        90000000000, '2024-02-10 10:00:00.0000000Z', 2019, 'Drama',          '11111111111111111111111111111111', 1280, 720),
 (x'0000000000000000000000000000000D', 'MediaBrowser.Controller.Entities.Movies.Movie', 'Delta',   'mkv', 15000000000, 80000000000, '2024-03-02 10:00:00.0000000Z', 2022, 'Sci-Fi|Drama',   '11111111111111111111111111111111', 3840, 2160);

-- episodes (Shows library)
INSERT INTO TypedBaseItems
 (guid, type, Name, Container, Size, RunTimeTicks, DateCreated, ProductionYear, Genres, TopParentId, Width, Height) VALUES
 (x'000000000000000000000000000000E1', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E1', 'mkv', 1200000000, 18000000000, '2024-02-15 10:00:00.0000000Z', 2020, 'Drama', '22222222222222222222222222222222', 1920, 1080),
 (x'000000000000000000000000000000E2', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E2', 'mkv', 1300000000, 18000000000, '2024-03-16 10:00:00.0000000Z', 2020, 'Drama', '22222222222222222222222222222222', 1920, 1080);

-- excluded: a series row, and a virtual (missing) episode
INSERT INTO TypedBaseItems (guid, type, Name, TopParentId, IsFolder) VALUES
 (x'000000000000000000000000000000F0', 'MediaBrowser.Controller.Entities.TV.Series', 'Some Show', '22222222222222222222222222222222', 1);
INSERT INTO TypedBaseItems (guid, type, Name, TopParentId, IsVirtualItem, Size, RunTimeTicks) VALUES
 (x'000000000000000000000000000000F9', 'MediaBrowser.Controller.Entities.TV.Episode', 'S1E99 (missing)', '22222222222222222222222222222222', 1, 999999999, 18000000000);

-- video + audio streams
INSERT INTO MediaStreams (ItemId, StreamIndex, StreamType, Codec, Width, Height, ColorTransfer, DvProfile) VALUES
 (x'0000000000000000000000000000000A', 0, 'Video', 'hevc', 3840, 2160, 'smpte2084', NULL),    -- Alpha: HDR10
 (x'0000000000000000000000000000000A', 1, 'Audio', 'eac3', NULL, NULL, NULL, NULL),
 (x'0000000000000000000000000000000B', 0, 'Video', 'h264', 1920, 1080, 'bt709', NULL),        -- Bravo: SDR
 (x'0000000000000000000000000000000D', 0, 'Video', 'hevc', 3840, 2160, 'arib-std-b67', NULL), -- Delta: HLG
 (x'000000000000000000000000000000E1', 0, 'Video', 'av1',  1920, 1080, 'smpte2084', 8),       -- S1E1: Dolby Vision
 (x'000000000000000000000000000000E2', 0, 'Video', 'h264', 1920, 1080, '', NULL);             -- S1E2: SDR (blank transfer)
-- Charlie deliberately has no video stream row.
