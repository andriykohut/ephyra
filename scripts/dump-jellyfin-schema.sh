#!/usr/bin/env sh
# Dump Jellyfin's item-DB schema + a few sample rows, for comparing against
# docs/schema-notes.md. Run it where you can read the Jellyfin config dir.
#
#   JELLYFIN_DATA_DIR=/path/to/jellyfin/config ./scripts/dump-jellyfin-schema.sh [out.txt]
set -eu
DIR="${JELLYFIN_DATA_DIR:?set JELLYFIN_DATA_DIR}"
OUT="${1:-schema-dump.txt}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
: >"$OUT"

find_db() {
  for p in "$DIR/data/$1" "$DIR/data/data/$1"; do
    [ -f "$p" ] && { echo "$p"; return 0; }
  done
  return 1
}

for name in jellyfin.db library.db playback_reporting.db; do
  src=$(find_db "$name") || { echo "== $name: NOT FOUND" >>"$OUT"; continue; }
  for f in "$src" "$src-wal" "$src-shm"; do [ -f "$f" ] && cp "$f" "$TMP/"; done
  db="$TMP/$(basename "$src")"
  {
    echo "== $name  ($src) =="
    sqlite3 "$db" '.schema'
    echo
    echo "== $name: item types =="
    sqlite3 -header -column "$db" \
      "SELECT Type, count(*) FROM BaseItems GROUP BY Type ORDER BY 2 DESC LIMIT 20;" 2>/dev/null || true
    echo
  } >>"$OUT"
done
echo "wrote $OUT"
