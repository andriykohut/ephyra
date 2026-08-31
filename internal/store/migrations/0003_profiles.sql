-- Adds the playback-event spine and the per-user profile aggregates. The spine
-- is append-only -- it is the only table Ephyra never rewrites -- and outlives
-- the Playback Reporting plugin's max-age setting. The agg_profile_* tables are
-- full-rewritten every watch run, like the other agg_* tables. Forward-only;
-- the migration runner owns schema_migrations, do not touch it here.

CREATE TABLE playback_events (
  at                TEXT NOT NULL,     -- plugin server-local wall clock, not re-zoned
  user_id           TEXT NOT NULL,     -- canonical
  item_id           TEXT NOT NULL,     -- canonical
  item_type         TEXT NOT NULL,     -- 'movie' | 'episode'
  method            TEXT NOT NULL,     -- raw plugin PlaybackMethod string
  play_duration_sec INTEGER NOT NULL,
  item_name         TEXT NOT NULL DEFAULT '',
  series_id         TEXT NOT NULL DEFAULT '',
  series_name       TEXT NOT NULL DEFAULT '',
  item_runtime_sec  INTEGER NOT NULL DEFAULT 0,   -- library-fact snapshot, refreshed on conflict
  item_year         INTEGER NOT NULL DEFAULT 0,
  item_genres       TEXT NOT NULL DEFAULT '',     -- pipe-joined, like jellyfin.db's Genres column
  dedup_hash        TEXT NOT NULL
);
CREATE UNIQUE INDEX ux_playback_events_dedup ON playback_events (dedup_hash);
CREATE INDEX ix_playback_events_user_item ON playback_events (user_id, item_id);
CREATE INDEX ix_playback_events_at ON playback_events (at);

CREATE TABLE agg_profile_summary (
  user_id                   TEXT NOT NULL,
  range                     TEXT NOT NULL,   -- '30d' | '90d' | '1y' | 'all'
  watch_sec                 INTEGER NOT NULL DEFAULT 0,
  plays                     INTEGER NOT NULL DEFAULT 0,
  distinct_titles           INTEGER NOT NULL DEFAULT 0,
  days_active               INTEGER NOT NULL DEFAULT 0,
  finished_pct              REAL    NOT NULL DEFAULT 0,
  bailed_pct                REAL    NOT NULL DEFAULT 0,
  rewatch_pct               REAL    NOT NULL DEFAULT 0,   -- meaningful only at range='all'
  longest_binge_episodes    INTEGER NOT NULL DEFAULT 0,   -- lifetime
  longest_binge_series_name TEXT    NOT NULL DEFAULT '',  -- lifetime
  show_of_range_series_id   TEXT    NOT NULL DEFAULT '',
  show_of_range_series_name TEXT    NOT NULL DEFAULT '',
  first_play                TEXT    NOT NULL DEFAULT '',
  last_play                 TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, range)
);

CREATE TABLE agg_profile_completion (
  user_id TEXT NOT NULL,
  range   TEXT NOT NULL,
  scope   TEXT NOT NULL,   -- 'movie' | 'episode'
  bucket  TEXT NOT NULL,   -- 'finished' | 'partial' | 'bailed' | 'unknown'
  count   INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, scope, bucket)
);

CREATE TABLE agg_profile_abandoned (
  user_id      TEXT NOT NULL,
  range        TEXT NOT NULL,
  scope        TEXT NOT NULL,   -- 'movie' | 'series'
  item_id      TEXT NOT NULL,
  name         TEXT NOT NULL,
  series_name  TEXT NOT NULL DEFAULT '',
  bailed_count INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, scope, item_id)
);

CREATE TABLE agg_profile_rewatch (
  user_id     TEXT NOT NULL,
  scope       TEXT NOT NULL,   -- 'movie' | 'episode'
  item_id     TEXT NOT NULL,
  name        TEXT NOT NULL,
  series_name TEXT NOT NULL DEFAULT '',
  watch_days  INTEGER NOT NULL,
  PRIMARY KEY (user_id, scope, item_id)
);

CREATE TABLE agg_profile_binge (
  user_id      TEXT NOT NULL,
  series_id    TEXT NOT NULL,
  series_name  TEXT NOT NULL,
  run_episodes INTEGER NOT NULL,
  run_start    TEXT NOT NULL,
  run_end      TEXT NOT NULL,
  PRIMARY KEY (user_id, series_id, run_start)
);

CREATE TABLE agg_profile_taste (
  user_id   TEXT NOT NULL,
  range     TEXT NOT NULL,
  dim       TEXT NOT NULL,   -- 'genre' | 'decade' | 'length'
  key       TEXT NOT NULL,
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, dim, key)
);

CREATE TABLE agg_taste_baseline (
  dim       TEXT NOT NULL,
  key       TEXT NOT NULL,
  watch_sec INTEGER NOT NULL,
  PRIMARY KEY (dim, key)
);
