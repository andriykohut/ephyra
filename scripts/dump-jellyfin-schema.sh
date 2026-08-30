#!/usr/bin/env sh
# Run this on the Jellyfin host. It copies the DBs somewhere writable and dumps
# their schema so you can compare against docs/schema-notes.md.
#
#   JELLYFIN_DATA_DIR=/path/to/jellyfin/config ./scripts/dump-jellyfin-schema.sh [out.txt]
set -eu
DIR="${JELLYFIN_DATA_DIR:?set JELLYFIN_DATA_DIR}"
OUT="${1:-schema-dump.txt}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
: >"$OUT"
for db in library.db playback_reporting.db; do
  if [ ! -f "$DIR/data/$db" ]; then
    echo "== $db: NOT FOUND" >>"$OUT"
    continue
  fi
  cp "$DIR/data/$db" "$DIR/data/$db-wal" "$DIR/data/$db-shm" "$TMP/" 2>/dev/null || cp "$DIR/data/$db" "$TMP/"
  {
    echo "== $db schema =="
    sqlite3 "$TMP/$db" '.schema'
    echo
    echo "== $db: item types =="
    sqlite3 -header -column "$TMP/$db" \
      "SELECT type, count(*) FROM TypedBaseItems GROUP BY type ORDER BY 2 DESC LIMIT 20;" 2>/dev/null || true
    echo
  } >>"$OUT"
done
echo "wrote $OUT"
