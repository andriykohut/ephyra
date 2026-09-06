-- 0005_library_filter.sql
-- Adds a library dimension throughout: playback_events gets a resolved
-- library column (default 'Unknown', backfilled once by the watch job), and
-- every agg_* table that Library Overview / Watch Stats / Profiles write
-- gets a library scope column so a page can be filtered to one Jellyfin
-- library. '' is the "All libraries" sentinel everywhere; 'Unknown' is a
-- real bucket, never the sentinel. Forward-only; does not touch
-- schema_migrations.

ALTER TABLE playback_events ADD COLUMN library TEXT NOT NULL DEFAULT 'Unknown';

CREATE TABLE dim_library (name TEXT PRIMARY KEY);

DROP TABLE agg_totals;
CREATE TABLE agg_totals (
  library TEXT NOT NULL DEFAULT '',
  metric  TEXT NOT NULL,
  value   REAL NOT NULL,
  PRIMARY KEY (library, metric)
);

DROP TABLE agg_disk;
CREATE TABLE agg_disk (
  library   TEXT NOT NULL DEFAULT '',
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  bytes     INTEGER NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (library, dimension, bucket)
);

DROP TABLE agg_distribution;
CREATE TABLE agg_distribution (
  library   TEXT NOT NULL DEFAULT '',
  dimension TEXT NOT NULL,
  bucket    TEXT NOT NULL,
  items     INTEGER NOT NULL,
  PRIMARY KEY (library, dimension, bucket)
);

DROP TABLE agg_library_growth;
CREATE TABLE agg_library_growth (
  library     TEXT NOT NULL DEFAULT '',
  month       TEXT NOT NULL,
  added_items INTEGER NOT NULL,
  added_bytes INTEGER NOT NULL,
  cum_items   INTEGER NOT NULL,
  PRIMARY KEY (library, month)
);

DROP TABLE agg_library_tag_pairs;
CREATE TABLE agg_library_tag_pairs (
  library TEXT NOT NULL DEFAULT '',
  tag_a   TEXT NOT NULL,
  tag_b   TEXT NOT NULL,
  items   INTEGER NOT NULL,
  PRIMARY KEY (library, tag_a, tag_b)
);
CREATE INDEX ix_library_tag_pairs_a ON agg_library_tag_pairs (library, tag_a, items DESC);
CREATE INDEX ix_library_tag_pairs_b ON agg_library_tag_pairs (library, tag_b, items DESC);

ALTER TABLE watch_events_daily ADD COLUMN library TEXT NOT NULL DEFAULT '';
ALTER TABLE agg_played_core    ADD COLUMN library TEXT NOT NULL DEFAULT '';

DROP TABLE agg_watch_heatmap;
CREATE TABLE agg_watch_heatmap (
  user_id   TEXT NOT NULL,
  dow       INTEGER NOT NULL,
  hour      INTEGER NOT NULL,
  library   TEXT NOT NULL DEFAULT '',
  watch_sec INTEGER NOT NULL,
  plays     INTEGER NOT NULL,
  PRIMARY KEY (user_id, dow, hour, library)
);

DROP TABLE agg_profile_summary;
CREATE TABLE agg_profile_summary (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  watch_sec INTEGER NOT NULL DEFAULT 0, plays INTEGER NOT NULL DEFAULT 0,
  distinct_titles INTEGER NOT NULL DEFAULT 0, days_active INTEGER NOT NULL DEFAULT 0,
  finished_pct REAL NOT NULL DEFAULT 0, bailed_pct REAL NOT NULL DEFAULT 0,
  rewatch_pct REAL NOT NULL DEFAULT 0,
  longest_binge_episodes INTEGER NOT NULL DEFAULT 0,
  longest_binge_series_name TEXT NOT NULL DEFAULT '',
  show_of_range_series_id TEXT NOT NULL DEFAULT '',
  show_of_range_series_name TEXT NOT NULL DEFAULT '',
  first_play TEXT NOT NULL DEFAULT '', last_play TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (user_id, range, library)
);

DROP TABLE agg_profile_completion;
CREATE TABLE agg_profile_completion (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL, bucket TEXT NOT NULL, count INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, library, scope, bucket)
);

DROP TABLE agg_profile_abandoned;
CREATE TABLE agg_profile_abandoned (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL, item_id TEXT NOT NULL, name TEXT NOT NULL,
  series_name TEXT NOT NULL DEFAULT '', bailed_count INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, library, scope, item_id)
);

DROP TABLE agg_profile_rewatch;
CREATE TABLE agg_profile_rewatch (
  user_id TEXT NOT NULL, library TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL,
  item_id TEXT NOT NULL, name TEXT NOT NULL, series_name TEXT NOT NULL DEFAULT '',
  watch_days INTEGER NOT NULL,
  PRIMARY KEY (user_id, library, scope, item_id)
);

DROP TABLE agg_profile_binge;
CREATE TABLE agg_profile_binge (
  user_id TEXT NOT NULL, library TEXT NOT NULL DEFAULT '', series_id TEXT NOT NULL,
  series_name TEXT NOT NULL, run_episodes INTEGER NOT NULL,
  run_start TEXT NOT NULL, run_end TEXT NOT NULL,
  PRIMARY KEY (user_id, library, series_id, run_start)
);

DROP TABLE agg_profile_taste;
CREATE TABLE agg_profile_taste (
  user_id TEXT NOT NULL, range TEXT NOT NULL, library TEXT NOT NULL DEFAULT '',
  dim TEXT NOT NULL, key TEXT NOT NULL, watch_sec INTEGER NOT NULL, plays INTEGER NOT NULL,
  PRIMARY KEY (user_id, range, library, dim, key)
);

DROP TABLE agg_taste_baseline;
CREATE TABLE agg_taste_baseline (
  library TEXT NOT NULL DEFAULT '', dim TEXT NOT NULL, key TEXT NOT NULL,
  watch_sec INTEGER NOT NULL,
  PRIMARY KEY (library, dim, key)
);

DROP TABLE agg_profile_tag_overlap;
CREATE TABLE agg_profile_tag_overlap (
  user_a TEXT NOT NULL, user_b TEXT NOT NULL, range TEXT NOT NULL,
  library TEXT NOT NULL DEFAULT '', cosine REAL NOT NULL, shared TEXT NOT NULL,
  PRIMARY KEY (user_a, user_b, range, library)
);

-- This migration drops and rebuilds every agg_* table above. Both jobs' mtime
-- skip would otherwise leave them empty until the underlying DB happens to
-- change, so blank the recorded mtimes to force exactly one full re-run.
UPDATE refresh_meta SET source_mtime = '' WHERE job IN ('library', 'watch');
