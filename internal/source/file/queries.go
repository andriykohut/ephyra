package file

import (
	"database/sql"
	"path"
	"strings"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func init() { queryLibrary = defaultQueryLibrary }

// Jellyfin's .NET entity class names. Unchanged across the library.db ->
// jellyfin.db (EF-Core) move in 10.11.
const (
	movieType            = "MediaBrowser.Controller.Entities.Movies.Movie"
	episodeType          = "MediaBrowser.Controller.Entities.TV.Episode"
	seriesType           = "MediaBrowser.Controller.Entities.TV.Series"
	folderType           = "MediaBrowser.Controller.Entities.Folder"
	collectionFolderType = "MediaBrowser.Controller.Entities.CollectionFolder"
)

// libraryQuery pulls movies and episodes joined to their primary video stream
// (lowest StreamIndex) and to their library name. jellyfin.db (10.11+):
// BaseItems / MediaStreamInfos, Id and TopParentId are dashed-uppercase GUID
// strings that match directly, StreamType is an int enum (1 = Video).
//
// TopParentId points at the physical root Folder (name = the on-disk directory,
// e.g. "movies"). When a CollectionFolder shares that name case-insensitively we
// use its display name ("Movies") instead. AncestorIds would be the "proper"
// route but it's incomplete in practice.
const libraryQuery = `
WITH pvs AS (
  SELECT ItemId,
         Codec         AS codec,
         Width         AS width,
         ColorTransfer AS color_transfer,
         DvProfile     AS dv_profile,
         ROW_NUMBER() OVER (PARTITION BY ItemId ORDER BY StreamIndex) AS rn
  FROM MediaStreamInfos
  WHERE StreamType = 1
),
folders AS (
  SELECT phys.Id AS fid, COALESCE(coll.Name, phys.Name) AS lib
  FROM BaseItems phys
  LEFT JOIN BaseItems coll
    ON coll.Type = '` + collectionFolderType + `'
   AND lower(coll.Name) = lower(phys.Name)
  WHERE phys.Type IN ('` + folderType + `', '` + collectionFolderType + `')
)
SELECT
  i.Name,
  i.Type,
  COALESCE(i.Size, 0),
  COALESCE(i.RunTimeTicks, 0),
  COALESCE(i.DateCreated, ''),
  COALESCE(i.ProductionYear, 0),
  COALESCE(i.Genres, ''),
  COALESCE(i.Tags, ''),
  COALESCE(f.lib, 'Unknown'),
  COALESCE(i.Path, ''),
  v.codec, v.width, v.color_transfer, v.dv_profile,
  i.Id, COALESCE(i.SeriesId, ''), COALESCE(i.SeriesName, '')
FROM BaseItems i
LEFT JOIN pvs v     ON v.ItemId = i.Id AND v.rn = 1
LEFT JOIN folders f ON f.fid = i.TopParentId
WHERE i.Type IN (?, ?)
  AND COALESCE(i.IsVirtualItem, 0) = 0
  AND COALESCE(i.IsFolder, 0) = 0
`

// playedStateQuery rolls UserData to one row per item: played by anyone,
// summed play count (per-user MAX first, so duplicate CustomDataKey rows don't
// inflate), and the newest LastPlayedDate.
const playedStateQuery = `
SELECT iid, MAX(played) AS played, SUM(pc) AS play_count, MAX(lpd) AS last_played
FROM (
  SELECT lower(replace(ItemId,'-','')) AS iid,
         lower(replace(UserId,'-','')) AS uid,
         MAX(Played)                   AS played,
         MAX(PlayCount)                AS pc,
         MAX(LastPlayedDate)           AS lpd
  FROM UserData
  GROUP BY iid, uid
)
GROUP BY iid`

const usersQuery = `SELECT lower(replace(Id,'-','')), COALESCE(Username,'') FROM Users`

// userPlaysQuery rolls UserData to one row per (user, movie|series): episodes
// fold into their series, movies stay themselves.
const userPlaysQuery = `
SELECT uid, scope, pid, MAX(name) AS name, SUM(pc) AS play_count, MAX(lpd) AS last_played
FROM (
  SELECT lower(replace(ud.UserId,'-',''))  AS uid,
         CASE WHEN bi.Type = '` + episodeType + `' THEN 'series' ELSE 'movie' END AS scope,
         lower(replace(
           CASE WHEN bi.Type = '` + episodeType + `' AND COALESCE(bi.SeriesId,'') <> ''
                THEN bi.SeriesId ELSE bi.Id END, '-', '')) AS pid,
         CASE WHEN bi.Type = '` + episodeType + `' AND COALESCE(bi.SeriesName,'') <> ''
              THEN bi.SeriesName ELSE bi.Name END AS name,
         MAX(ud.PlayCount)      AS pc,
         MAX(ud.LastPlayedDate) AS lpd
  FROM UserData ud
  JOIN BaseItems bi ON bi.Id = ud.ItemId
  WHERE bi.Type IN ('` + movieType + `', '` + episodeType + `')
    AND COALESCE(ud.PlayCount,0) > 0
  GROUP BY uid, pid, ud.ItemId
)
GROUP BY uid, pid`

func defaultQueryLibrary(db *sql.DB) (source.LibrarySnapshot, error) {
	rows, err := db.Query(libraryQuery, movieType, episodeType)
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	defer rows.Close()

	var snap source.LibrarySnapshot
	for rows.Next() {
		var (
			name, typ, dateRaw, genres, tags, library, itemPath string
			rawID, rawSeriesID, seriesName                      string
			size, ticks                                         int64
			year                                                int
			codec, colorTransfer                                sql.NullString
			width, dvProfile                                    sql.NullInt64
		)
		if err := rows.Scan(&name, &typ, &size, &ticks, &dateRaw, &year, &genres, &tags, &library, &itemPath,
			&codec, &width, &colorTransfer, &dvProfile, &rawID, &rawSeriesID, &seriesName); err != nil {
			return source.LibrarySnapshot{}, err
		}
		it := source.LibraryItem{
			ID:            source.CanonID(rawID),
			Name:          name,
			Type:          shortType(typ),
			SizeBytes:     size,
			RuntimeSec:    ticks / 10_000_000,
			DateRaw:       dateRaw,
			DateCreated:   parseJellyfinTime(dateRaw),
			Year:          year,
			Genres:        splitGenres(genres),
			Tags:          splitTags(tags),
			Library:       library,
			Container:     containerFromPath(itemPath),
			VideoCodec:    codec.String,
			Width:         int(width.Int64),
			HasVideo:      width.Valid || codec.Valid,
			ColorTransfer: colorTransfer.String,
			SeriesID:      source.CanonID(rawSeriesID),
			SeriesName:    seriesName,
		}
		if dvProfile.Valid && dvProfile.Int64 > 0 {
			p := int(dvProfile.Int64)
			it.DvProfile = &p
		}
		snap.Items = append(snap.Items, it)
	}
	if err := rows.Err(); err != nil {
		return source.LibrarySnapshot{}, err
	}

	if err := foldPlayedState(db, &snap); err != nil {
		return source.LibrarySnapshot{}, err
	}
	if err := readUsers(db, &snap); err != nil {
		return source.LibrarySnapshot{}, err
	}
	if err := readUserPlays(db, &snap); err != nil {
		return source.LibrarySnapshot{}, err
	}

	if err := db.QueryRow(
		`SELECT count(*) FROM BaseItems WHERE Type = ? AND COALESCE(IsVirtualItem,0)=0`, seriesType,
	).Scan(&snap.SeriesCount); err != nil {
		return source.LibrarySnapshot{}, err
	}
	return snap, nil
}

type playState struct {
	played    bool
	playCount int64
	last      time.Time
}

func foldPlayedState(db *sql.DB, snap *source.LibrarySnapshot) error {
	rows, err := db.Query(playedStateQuery)
	if err != nil {
		return err
	}
	byItem := map[string]playState{}
	for rows.Next() {
		var iid string
		var played, pc int64
		var lpd sql.NullString
		if err := rows.Scan(&iid, &played, &pc, &lpd); err != nil {
			rows.Close()
			return err
		}
		byItem[iid] = playState{played == 1, pc, parseJellyfinTime(lpd.String)}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range snap.Items {
		if ps, ok := byItem[snap.Items[i].ID]; ok {
			snap.Items[i].Played = ps.played
			snap.Items[i].PlayCount = int(ps.playCount)
			snap.Items[i].LastPlayedAt = ps.last
		}
	}
	return nil
}

func readUsers(db *sql.DB, snap *source.LibrarySnapshot) error {
	rows, err := db.Query(usersQuery)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u source.UserRef
		if err := rows.Scan(&u.ID, &u.Name); err != nil {
			return err
		}
		snap.Users = append(snap.Users, u)
	}
	return rows.Err()
}

func readUserPlays(db *sql.DB, snap *source.LibrarySnapshot) error {
	rows, err := db.Query(userPlaysQuery)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p source.UserPlay
		var pc int64
		var lpd sql.NullString
		if err := rows.Scan(&p.UserID, &p.Scope, &p.ItemID, &p.Name, &pc, &lpd); err != nil {
			return err
		}
		p.PlayCount = int(pc)
		p.LastPlayedAt = parseJellyfinTime(lpd.String)
		snap.UserPlays = append(snap.UserPlays, p)
	}
	return rows.Err()
}

func shortType(t string) string {
	if t == movieType {
		return "movie"
	}
	return "episode"
}

// jellyfin.db has no Container column; take it from the file extension.
func containerFromPath(p string) string {
	return strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))
}

func splitGenres(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, g := range strings.Split(s, "|") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// splitTags is splitGenres plus lowercasing and dedup — item tags are freeform
// and case-inconsistent, genres are a controlled vocabulary.
func splitTags(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range strings.Split(s, "|") {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// Jellyfin writes DateCreated a few ways depending on version. Try the likely
// ones; give up quietly (zero time) rather than erroring a whole refresh.
var jellyfinTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.9999999Z",
	"2006-01-02 15:04:05Z",
	"2006-01-02 15:04:05.9999999",
	"2006-01-02 15:04:05",
}

func parseJellyfinTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range jellyfinTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
