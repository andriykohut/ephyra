-- Re-keys the spine on the session's real coordinates.
--
-- The Playback Reporting plugin updates an in-progress session's row in place:
-- DateCreated / UserId / ItemId stay put while PlayDuration grows on every
-- write. dedup_hash covered play_duration_sec, so each refresh that caught a
-- longer duration minted a fresh row -- a film left paused counted as six
-- plays, with a watch time of 570+1164+1751+... instead of 2957s. The identity
-- of a play is (at, user_id, item_id); the hash was never anything else.
--
-- Forward-only; the migration runner owns schema_migrations, do not touch it here.

-- Collapse the rows that already piled up, keeping the longest duration of each
-- session -- the plugin's last word on it. Leans on SQLite's bare-column rule:
-- in a MAX() aggregate query the non-aggregated columns come from the row that
-- supplied the maximum.
DELETE FROM playback_events
WHERE rowid NOT IN (
  SELECT keep FROM (
    SELECT rowid AS keep, MAX(play_duration_sec)
    FROM playback_events
    GROUP BY at, user_id, item_id
  )
);

DROP INDEX ux_playback_events_dedup;
ALTER TABLE playback_events DROP COLUMN dedup_hash;
CREATE UNIQUE INDEX ux_playback_events_session ON playback_events (at, user_id, item_id);
