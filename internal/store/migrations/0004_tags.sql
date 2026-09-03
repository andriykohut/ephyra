-- Freeform item tags (TMDb keywords). The tag cloud reuses agg_distribution
-- (dimension 'tag') and coverage reuses agg_totals (tags.*_items); only these
-- two shapes are new. Both full-rewritten per refresh. Forward-only; the runner
-- owns schema_migrations, do not touch it here.

ALTER TABLE playback_events ADD COLUMN item_tags TEXT NOT NULL DEFAULT '';

CREATE TABLE agg_library_tag_pairs (
  tag_a TEXT NOT NULL,
  tag_b TEXT NOT NULL,   -- writer enforces tag_a < tag_b
  items INTEGER NOT NULL,
  PRIMARY KEY (tag_a, tag_b)
);
CREATE INDEX ix_library_tag_pairs_a ON agg_library_tag_pairs (tag_a, items DESC);
CREATE INDEX ix_library_tag_pairs_b ON agg_library_tag_pairs (tag_b, items DESC);

CREATE TABLE agg_profile_tag_overlap (
  user_a TEXT NOT NULL,
  user_b TEXT NOT NULL,   -- writer enforces user_a < user_b
  range  TEXT NOT NULL,
  cosine REAL NOT NULL,
  shared TEXT NOT NULL,   -- pipe-joined top shared tags
  PRIMARY KEY (user_a, user_b, range)
);
