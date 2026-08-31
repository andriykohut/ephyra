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
		name, seriesID, seriesName, genres string
		runtimeTicks                       int64
		year                               int
	}
	items := map[string]itemInfo{}
	irows, err := jdb.Query(`
		SELECT lower(replace(Id,'-','')), COALESCE(Name,''),
		       lower(replace(COALESCE(SeriesId,''),'-','')), COALESCE(SeriesName,''),
		       COALESCE(RunTimeTicks,0), COALESCE(ProductionYear,0), COALESCE(Genres,'')
		FROM BaseItems
		WHERE Type IN ('` + movieType + `', '` + episodeType + `')`)
	if err != nil {
		return err
	}
	for irows.Next() {
		var id string
		var info itemInfo
		if err := irows.Scan(&id, &info.name, &info.seriesID, &info.seriesName,
			&info.runtimeTicks, &info.year, &info.genres); err != nil {
			irows.Close()
			return err
		}
		items[id] = info
	}
	irows.Close()
	if err := irows.Err(); err != nil {
		return err
	}

	for i := range events {
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
		}
	}
	return nil
}
