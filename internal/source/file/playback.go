package file

import (
	"database/sql"
	"strings"

	"github.com/andriykohut/ephyra/internal/source"
)

const playbackQuery = `
SELECT DateCreated,
       COALESCE(UserId,''), COALESCE(ItemId,''),
       COALESCE(ItemType,''), COALESCE(ItemName,''),
       COALESCE(PlaybackMethod,''), COALESCE(PlayDuration,0)
FROM PlaybackActivity
WHERE ItemType IN ('Movie','Episode')`

func queryPlaybackEvents(db *sql.DB) ([]source.PlaybackEvent, error) {
	rows, err := db.Query(playbackQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []source.PlaybackEvent
	for rows.Next() {
		var dateRaw, userID, itemID, itemType, itemName, method string
		var dur int64
		if err := rows.Scan(&dateRaw, &userID, &itemID, &itemType, &itemName, &method, &dur); err != nil {
			return nil, err
		}
		scope := "movie"
		if strings.EqualFold(itemType, "episode") {
			scope = "episode"
		}
		out = append(out, source.PlaybackEvent{
			At:              parseJellyfinTime(dateRaw),
			UserID:          source.CanonID(userID),
			ItemID:          source.CanonID(itemID),
			ItemName:        itemName,
			ItemType:        scope,
			Method:          method,
			PlayDurationSec: dur,
		})
	}
	return out, rows.Err()
}

func tableExists(db *sql.DB, name string) bool {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// resolveLibrariesBatchSize caps how many item ids go into a single IN (...)
// query. SQLite's SQLITE_MAX_VARIABLE_NUMBER (32766 on the version this
// project's modernc.org/sqlite ships) limits how many "?" placeholders one
// query can hold; this is a safe round number well under that.
const resolveLibrariesBatchSize = 500

// chunkIDs splits ids into slices of at most size elements each, preserving
// order. size <= 0 is treated as "one chunk" (no batching).
func chunkIDs(ids []string, size int) [][]string {
	if size <= 0 || len(ids) <= size {
		if len(ids) == 0 {
			return nil
		}
		return [][]string{ids}
	}
	var chunks [][]string
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

// resolveLibraries looks up library names for a set of item ids directly
// against a jellyfin.db copy, independent of what the Playback Reporting
// plugin currently reports. Ids not found in BaseItems are simply absent from
// the result map. This backs a residual backfill sweep (see ResolveLibraries
// in file.go), so itemIDs can run into the tens of thousands for a household
// with years of history -- queried in batches to stay under SQLite's
// host-parameter limit.
func resolveLibraries(jdb *sql.DB, itemIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, batch := range chunkIDs(itemIDs, resolveLibrariesBatchSize) {
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, id := range batch {
			placeholders[i] = "?"
			args[i] = id
		}
		q := `
			WITH ` + foldersCTE + `
			SELECT lower(replace(i.Id,'-','')), COALESCE(f.lib, 'Unknown')
			FROM BaseItems i
			LEFT JOIN folders f ON f.fid = i.TopParentId
			WHERE lower(replace(i.Id,'-','')) IN (` + strings.Join(placeholders, ",") + `)`
		rows, err := jdb.Query(q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, lib string
			if err := rows.Scan(&id, &lib); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = lib
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

// enrichPlaybackEvents fills UserName / ItemName / SeriesID / SeriesName from a
// jellyfin.db copy. Best effort: rows it can't resolve keep their plugin values.
func enrichPlaybackEvents(jdb *sql.DB, events []source.PlaybackEvent) error {
	users := map[string]string{}
	urows, err := jdb.Query(`SELECT lower(replace(Id,'-','')), COALESCE(Username,'') FROM Users`)
	if err != nil {
		return err
	}
	for urows.Next() {
		var id, name string
		if err := urows.Scan(&id, &name); err != nil {
			urows.Close()
			return err
		}
		users[id] = name
	}
	urows.Close()
	if err := urows.Err(); err != nil {
		return err
	}

	type itemInfo struct {
		name, seriesID, seriesName, genres, tags, library string
		runtimeTicks                                      int64
		year                                              int
	}
	items := map[string]itemInfo{}
	irows, err := jdb.Query(`
		WITH ` + foldersCTE + `
		SELECT lower(replace(i.Id,'-','')), COALESCE(i.Name,''),
		       lower(replace(COALESCE(i.SeriesId,''),'-','')), COALESCE(i.SeriesName,''),
		       COALESCE(i.RunTimeTicks,0), COALESCE(i.ProductionYear,0), COALESCE(i.Genres,''), COALESCE(i.Tags,''),
		       COALESCE(f.lib, 'Unknown')
		FROM BaseItems i
		LEFT JOIN folders f ON f.fid = i.TopParentId
		WHERE i.Type IN ('` + movieType + `', '` + episodeType + `')`)
	if err != nil {
		return err
	}
	for irows.Next() {
		var id string
		var info itemInfo
		if err := irows.Scan(&id, &info.name, &info.seriesID, &info.seriesName,
			&info.runtimeTicks, &info.year, &info.genres, &info.tags, &info.library); err != nil {
			irows.Close()
			return err
		}
		items[id] = info
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return err
	}

	// Episodes almost never carry their own Tags; inherit the parent Series'.
	seriesTags := map[string]string{}
	srows, err := jdb.Query(`SELECT lower(replace(Id,'-','')), COALESCE(Tags,'') FROM BaseItems WHERE Type = '` + seriesType + `'`)
	if err != nil {
		return err
	}
	for srows.Next() {
		var id, tags string
		if err := srows.Scan(&id, &tags); err != nil {
			srows.Close()
			return err
		}
		seriesTags[id] = tags
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return err
	}

	for i := range events {
		events[i].Library = "Unknown"
		if n, ok := users[events[i].UserID]; ok {
			events[i].UserName = n
		}
		if info, ok := items[events[i].ItemID]; ok {
			if info.name != "" {
				events[i].ItemName = info.name
			}
			events[i].SeriesID = info.seriesID
			events[i].SeriesName = info.seriesName
			events[i].ItemRuntimeSec = info.runtimeTicks / 10_000_000 // ticks -> seconds
			events[i].ItemYear = info.year
			events[i].ItemGenres = splitGenres(info.genres)
			events[i].Library = info.library
			if events[i].ItemType == "episode" {
				events[i].ItemTags = splitTags(seriesTags[info.seriesID])
			} else {
				events[i].ItemTags = splitTags(info.tags)
			}
		}
	}
	return nil
}
