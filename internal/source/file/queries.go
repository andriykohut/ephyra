package file

import (
	"database/sql"
	"strings"
	"time"

	"github.com/andrii/ephyra/internal/source"
)

func init() { queryLibrary = defaultQueryLibrary }

const (
	movieType            = "MediaBrowser.Controller.Entities.Movies.Movie"
	episodeType          = "MediaBrowser.Controller.Entities.TV.Episode"
	seriesType           = "MediaBrowser.Controller.Entities.TV.Series"
	collectionFolderType = "MediaBrowser.Controller.Entities.CollectionFolder"
)

// libraryQuery pulls movies and episodes joined to their primary video stream
// (lowest StreamIndex) and to the name of their top-level library folder.
// TopParentId is matched to hex(folder.guid) after stripping dashes and
// upper-casing, since Jellyfin builds vary on that column's formatting.
const libraryQuery = `
WITH pvs AS (
  SELECT ItemId,
         Codec         AS codec,
         Width         AS width,
         ColorTransfer AS color_transfer,
         DvProfile     AS dv_profile,
         ROW_NUMBER() OVER (PARTITION BY ItemId ORDER BY StreamIndex) AS rn
  FROM MediaStreams
  WHERE StreamType = 'Video'
),
folders AS (
  SELECT upper(hex(guid)) AS fid, Name AS lib
  FROM TypedBaseItems
  WHERE type = ?
)
SELECT
  i.Name,
  i.type,
  COALESCE(i.Size, 0),
  COALESCE(i.RunTimeTicks, 0),
  COALESCE(i.DateCreated, ''),
  COALESCE(i.ProductionYear, 0),
  COALESCE(i.Genres, ''),
  COALESCE(f.lib, 'Unknown'),
  COALESCE(i.Container, ''),
  v.codec, v.width, v.color_transfer, v.dv_profile
FROM TypedBaseItems i
LEFT JOIN pvs v     ON v.ItemId = i.guid AND v.rn = 1
LEFT JOIN folders f ON f.fid = upper(replace(COALESCE(i.TopParentId, ''), '-', ''))
WHERE i.type IN (?, ?)
  AND COALESCE(i.IsVirtualItem, 0) = 0
  AND COALESCE(i.IsFolder, 0) = 0
`

func defaultQueryLibrary(db *sql.DB) (source.LibrarySnapshot, error) {
	rows, err := db.Query(libraryQuery, collectionFolderType, movieType, episodeType)
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	defer rows.Close()

	var snap source.LibrarySnapshot
	for rows.Next() {
		var (
			name, typ, dateRaw, genres, library, container string
			size, ticks                                    int64
			year                                           int
			codec, colorTransfer                           sql.NullString
			width, dvProfile                               sql.NullInt64
		)
		if err := rows.Scan(&name, &typ, &size, &ticks, &dateRaw, &year, &genres, &library, &container,
			&codec, &width, &colorTransfer, &dvProfile); err != nil {
			return source.LibrarySnapshot{}, err
		}
		it := source.LibraryItem{
			Name:          name,
			Type:          shortType(typ),
			SizeBytes:     size,
			RuntimeSec:    ticks / 10_000_000,
			DateRaw:       dateRaw,
			DateCreated:   parseJellyfinTime(dateRaw),
			Year:          year,
			Genres:        splitGenres(genres),
			Library:       library,
			Container:     container,
			VideoCodec:    codec.String,
			Width:         int(width.Int64),
			HasVideo:      width.Valid || codec.Valid,
			ColorTransfer: colorTransfer.String,
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

	if err := db.QueryRow(
		`SELECT count(*) FROM TypedBaseItems WHERE type = ? AND COALESCE(IsVirtualItem,0)=0`, seriesType,
	).Scan(&snap.SeriesCount); err != nil {
		return source.LibrarySnapshot{}, err
	}
	return snap, nil
}

func shortType(t string) string {
	if t == movieType {
		return "movie"
	}
	return "episode"
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

// Jellyfin writes DateCreated in a few shapes depending on version. Try the
// likely ones; give up quietly (zero time) rather than erroring a whole refresh.
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
