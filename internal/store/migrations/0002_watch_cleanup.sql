-- Redefines the three watch/cleanup tables from 0001 (created empty, never
-- written) and adds dim_user + agg_played_core. Forward-only. The migration
-- runner owns schema_migrations -- do not touch it here.

CREATE TABLE dim_user (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL
);

CREATE TABLE agg_played_core (
  user_id        TEXT NOT NULL,
  scope          TEXT NOT NULL,   -- 'movie' | 'series'
  item_id        TEXT NOT NULL,
  name           TEXT NOT NULL,
  play_count     INTEGER NOT NULL,
  last_played_at TEXT,
  PRIMARY KEY (user_id, item_id)
);

DROP TABLE watch_events_daily;
CREATE TABLE watch_events_daily (
  day         TEXT NOT NULL,      -- 'YYYY-MM-DD', plugin server-local wall date
  user_id     TEXT NOT NULL,
  item_id     TEXT NOT NULL,
  scope       TEXT NOT NULL,      -- 'movie' | 'episode'
  name        TEXT NOT NULL,
  series_id   TEXT NOT NULL DEFAULT '',
  series_name TEXT NOT NULL DEFAULT '',
  plays       INTEGER NOT NULL,
  watch_sec   INTEGER NOT NULL,
  method      TEXT NOT NULL,      -- DirectPlay|Remux|AudioTranscode|VideoTranscode|Other
  PRIMARY KEY (day, user_id, item_id, method)
);

DROP TABLE agg_watch_heatmap;
CREATE TABLE agg_watch_heatmap (
  user_id   TEXT NOT NULL,
  dow       INTEGER NOT NULL,     -- 0=Sun .. 6=Sat, server-local
  hour      INTEGER NOT NULL,     -- 0..23, server-local
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, dow, hour)
);

DROP TABLE agg_cleanup;
CREATE TABLE agg_cleanup (
  item_id        TEXT PRIMARY KEY,
  scope          TEXT NOT NULL,   -- 'movie' | 'series'
  name           TEXT NOT NULL,
  library        TEXT NOT NULL,
  bytes          INTEGER NOT NULL,
  episodes       INTEGER NOT NULL,
  added_at       TEXT NOT NULL,
  last_played_at TEXT
);
