package aggregate

import (
	"sort"

	"github.com/andriykohut/ephyra/internal/source"
)

const corePlaysPerUser = 25

// CorePlays projects per-user play rollups into store rows, keeping the top
// corePlaysPerUser titles per user by play count.
func CorePlays(plays []source.UserPlay) []CorePlayRow {
	byUser := map[string][]CorePlayRow{}
	for _, p := range plays {
		byUser[p.UserID] = append(byUser[p.UserID], CorePlayRow{
			UserID: p.UserID, Scope: p.Scope, ItemID: p.ItemID, Name: p.Name,
			PlayCount: int64(p.PlayCount), LastPlayedAt: rfc3339(p.LastPlayedAt),
		})
	}
	users := make([]string, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	sort.Strings(users)

	var out []CorePlayRow
	for _, u := range users {
		rows := byUser[u]
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].PlayCount != rows[j].PlayCount {
				return rows[i].PlayCount > rows[j].PlayCount
			}
			if rows[i].LastPlayedAt != rows[j].LastPlayedAt {
				return rows[i].LastPlayedAt > rows[j].LastPlayedAt
			}
			return rows[i].Name < rows[j].Name
		})
		if len(rows) > corePlaysPerUser {
			rows = rows[:corePlaysPerUser]
		}
		out = append(out, rows...)
	}
	return out
}
