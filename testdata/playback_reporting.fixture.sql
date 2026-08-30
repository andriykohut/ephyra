-- Stand-in for the Playback Reporting plugin's playback_reporting.db. Ids are the
-- dashless-lowercase forms of the library fixture's dashed-uppercase ids.
CREATE TABLE PlaybackActivity (
  DateCreated    DATETIME NOT NULL,
  UserId         TEXT,
  ItemId         TEXT,
  ItemType       TEXT,
  ItemName       TEXT,
  PlaybackMethod TEXT,
  ClientName     TEXT,
  DeviceName     TEXT,
  PlayDuration   INT
);
CREATE TABLE UserList (UserId TEXT);

-- alice = 11111111222233334444555555555555 ; bob = 66666666777788889999aaaaaaaaaaaa
-- Bravo = 0000000000000000000000000000000b ; Alpha = ...000a ; Delta = ...000d
-- S1E1 = 000000000000000000000000000000e1 ; S1E2 = 000000000000000000000000000000e2
INSERT INTO PlaybackActivity (DateCreated, UserId, ItemId, ItemType, ItemName, PlaybackMethod, ClientName, DeviceName, PlayDuration) VALUES
 ('2025-01-06 20:10:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000b', 'Movie',   'Bravo', 'DirectPlay',                   'Jellyfin Media Player', 'Air', 3600),
 ('2025-01-07 21:30:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000b', 'Movie',   'Bravo', 'Transcode (v:h264 a:aac)',     'Jellyfin Web',          'FF',  1800),
 ('2025-01-08 00:20:00.000', '66666666777788889999aaaaaaaaaaaa', '000000000000000000000000000000e1', 'Episode', 'Some Show - s01e01 - Pilot', 'DirectPlay',              'JMP', 'TV',  1200),
 ('2025-01-08 23:50:00.000', '66666666777788889999aaaaaaaaaaaa', '000000000000000000000000000000e2', 'Episode', 'Some Show - s01e02 - Two',   'Transcode (v:direct a:aac)', 'JMP', 'TV', 1500),
 ('2025-01-09 12:00:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000a', 'Movie',   'Alpha', 'Transcode (v:direct a:direct)', 'JMP', 'Air', 600),
 ('2025-01-09 12:30:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000a', 'Movie',   'Alpha', 'DirectPlay',                   'JMP', 'Air', 0),
 ('2025-01-10 19:00:00.000', '11111111222233334444555555555555', '0000000000000000000000000000000d', 'Movie',   'Delta', 'Transcode (garbled)',          'JMP', 'Air', 700),
 ('2025-01-11 19:00:00.000', '11111111222233334444555555555555', 'deadbeefdeadbeefdeadbeefdeadbeef', 'Movie',   'Ghost Movie (deleted)', 'DirectPlay',        'JMP', 'Air', 900);
