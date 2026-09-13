-- Credits, Jellyfin's watched tick, and the name -> Jellyfin-id cache, plus the
-- two aggregates the profile overview reads.
--
-- dim_credit / dim_played / dim_jf_ref are upsert-only and are NEVER deleted
-- from. They join playback_events, which outlives the plugin's retention and
-- keeps item_name / item_genres verbatim once an item leaves Jellyfin. A
-- dimension reconciled against the current library would throw that away at the
-- join: delete a film and its cast stops counting. UserData makes it concrete --
-- it is ON DELETE CASCADE from BaseItems, so Jellyfin's own tick for a deleted
-- item is already gone by the next refresh.
--
-- Pruning "orphaned" rows here would silently destroy that durability. Don't.
--
-- Forward-only; the migration runner owns schema_migrations, do not touch it here.

CREATE TABLE dim_credit (
  item_id    TEXT NOT NULL,
  person     TEXT NOT NULL,
  kind       TEXT NOT NULL,          -- 'actor' | 'director'; GuestStar folds into actor
  list_order INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (item_id, person, kind)
);
CREATE INDEX ix_dim_credit_item ON dim_credit (item_id);

CREATE TABLE dim_played (
  user_id TEXT NOT NULL,
  item_id TEXT NOT NULL,
  played  INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, item_id)
);

CREATE TABLE dim_jf_ref (
  kind       TEXT NOT NULL,          -- 'person' | 'genre' | 'server' (server: name = '')
  name       TEXT NOT NULL,
  jf_id      TEXT NOT NULL DEFAULT '',
  checked_at TEXT NOT NULL DEFAULT '',
  miss_count INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (kind, name)
);

CREATE TABLE agg_profile_people (
  library                TEXT NOT NULL DEFAULT '',
  user_id                TEXT NOT NULL,
  range                  TEXT NOT NULL,   -- '30d' | '90d' | '1y' | 'all'
  kind                   TEXT NOT NULL,   -- 'actor' | 'director'
  person                 TEXT NOT NULL,
  watch_sec              INTEGER NOT NULL DEFAULT 0,
  plays                  INTEGER NOT NULL DEFAULT 0,
  distinct_titles        INTEGER NOT NULL DEFAULT 0,
  watch_sec_played       INTEGER NOT NULL DEFAULT 0,
  plays_played           INTEGER NOT NULL DEFAULT 0,
  distinct_titles_played INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (library, user_id, range, kind, person)
);

CREATE TABLE agg_profile_top_items (
  library          TEXT NOT NULL DEFAULT '',
  user_id          TEXT NOT NULL,
  range            TEXT NOT NULL,
  scope            TEXT NOT NULL,        -- 'series' | 'movie' | 'episode'
  item_id          TEXT NOT NULL,
  name             TEXT NOT NULL DEFAULT '',
  series_name      TEXT NOT NULL DEFAULT '',
  watch_sec        INTEGER NOT NULL DEFAULT 0,
  plays            INTEGER NOT NULL DEFAULT 0,
  watch_sec_played INTEGER NOT NULL DEFAULT 0,
  plays_played     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (library, user_id, range, scope, item_id)
);

-- The recents cursor scans one user's history in `at` order. The existing
-- indexes lead on `at` or on (user_id, item_id); neither serves that. library
-- stays out on purpose -- it would help the filtered case and hurt the far more
-- common unfiltered one.
CREATE INDEX ix_playback_events_user_at ON playback_events (user_id, at DESC);

-- Both jobs' mtime-skip would otherwise leave dim_credit/dim_played/agg_profile_people
-- empty on an existing install until the underlying DB happens to change (see 0005).
UPDATE refresh_meta SET source_mtime = '' WHERE job IN ('library', 'watch');
