-- schema_migrations is created by the migration runner itself.

CREATE TABLE refresh_meta (
  job              TEXT PRIMARY KEY,
  last_run_at      TEXT NOT NULL,
  source_mtime     TEXT NOT NULL DEFAULT '',
  duration_ms      INTEGER NOT NULL DEFAULT 0,
  ok               INTEGER NOT NULL DEFAULT 0,
  skipped          INTEGER NOT NULL DEFAULT 0,
  plugin_available INTEGER NOT NULL DEFAULT 0,
  error            TEXT NOT NULL DEFAULT ''
);

CREATE TABLE agg_totals (
  metric TEXT PRIMARY KEY,
  value  REAL NOT NULL
);

CREATE TABLE agg_disk (
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  bytes     INTEGER NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (dimension, bucket)
);

CREATE TABLE agg_distribution (
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (dimension, bucket)
);

CREATE TABLE agg_library_growth (
  month        TEXT PRIMARY KEY,
  added_items  INTEGER NOT NULL,
  added_bytes  INTEGER NOT NULL,
  cum_items    INTEGER NOT NULL
);

CREATE TABLE watch_events_daily (
  day       TEXT NOT NULL,
  user_id   TEXT NOT NULL,
  item_id   TEXT NOT NULL,
  scope     TEXT NOT NULL,
  name      TEXT NOT NULL,
  plays     INTEGER NOT NULL,
  watch_sec INTEGER NOT NULL,
  method    TEXT NOT NULL,
  PRIMARY KEY (day, user_id, item_id, method)
);

CREATE TABLE agg_watch_heatmap (
  dow       INTEGER NOT NULL,
  hour      INTEGER NOT NULL,
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (dow, hour)
);

CREATE TABLE agg_cleanup (
  item_id        TEXT PRIMARY KEY,
  name           TEXT NOT NULL,
  library        TEXT NOT NULL,
  bytes          INTEGER NOT NULL,
  added_at       TEXT NOT NULL,
  last_played_at TEXT
);
