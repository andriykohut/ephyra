package aggregate

import (
	"cmp"
	"slices"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

const peopleTopN = 50

// UserItem keys a per-user, per-item fact -- Jellyfin's watched tick today,
// more later.
type UserItem struct {
	UserID, ItemID string
}

type creditIndex struct{ byItem map[string][]source.Credit }

func newCreditIndex(credits []source.Credit) *creditIndex {
	ix := &creditIndex{byItem: map[string][]source.Credit{}}
	for _, c := range credits {
		ix.byItem[c.ItemID] = append(ix.byItem[c.ItemID], c)
	}
	return ix
}

// forEvent credits an episode to the parent series when the episode carries no
// cast of its own. On a real library 925/925 movies have credits but only
// 5285/9794 episodes do.
func (ix *creditIndex) forEvent(ev source.PlaybackEvent) []source.Credit {
	if own := ix.byItem[ev.ItemID]; len(own) > 0 {
		return own
	}
	if ev.SeriesID != "" {
		return ix.byItem[ev.SeriesID]
	}
	return nil
}

type PeopleRow struct {
	UserID, Range, Kind, Person                       string
	WatchSec, Plays, DistinctTitles                   int64
	WatchSecPlayed, PlaysPlayed, DistinctTitlesPlayed int64
}

type TopItemRow struct {
	UserID, Range, Scope, ItemID, Name, SeriesName string // Scope: "series" | "movie" | "episode"
	WatchSec, Plays, WatchSecPlayed, PlaysPlayed   int64
}

type PeopleAggregates struct {
	People   []PeopleRow
	TopItems []TopItemRow
}

type peopleKey struct{ user, rng, kind, person string }

type peopleAcc struct {
	watchSec, plays             int64
	watchSecPlayed, playsPlayed int64
	titles, titlesPlayed        map[string]struct{}
}

type topItemKey struct{ user, rng, scope, item string }

type topItemAcc struct {
	watchSec, plays, watchSecPlayed, playsPlayed int64
	name, seriesName                             string
}

// People ranks people by hours and by breadth for every profile range. Pure:
// no I/O, no SQL. now is container-local, matching Profiles.
func People(events []source.PlaybackEvent, credits []source.Credit, played map[UserItem]bool, now time.Time) PeopleAggregates {
	ix := newCreditIndex(credits)
	peopleAccs := map[peopleKey]*peopleAcc{}
	itemAccs := map[topItemKey]*topItemAcc{}

	for _, ev := range events {
		if ev.UserID == "" || ev.ItemID == "" || ev.At.IsZero() {
			continue
		}
		day := ev.At.Format("2006-01-02")
		// Title key for breadth: a series counts once, not per episode.
		titleKey := ev.SeriesID
		if titleKey == "" {
			titleKey = ev.ItemID
		}
		isPlayed := played[UserItem{UserID: ev.UserID, ItemID: ev.ItemID}]

		scope := "movie"
		if ev.ItemType == "episode" {
			scope = "episode"
		}

		seen := map[string]bool{} // dedupe (kind, person) credits on this one event
		for _, c := range ix.forEvent(ev) {
			dk := c.Kind + "\x1f" + c.Person
			if seen[dk] {
				continue
			}
			seen[dk] = true

			for _, rng := range profileRangeList {
				if day < rangeCutoff(rng, now) {
					continue
				}
				k := peopleKey{ev.UserID, rng, c.Kind, c.Person}
				a := peopleAccs[k]
				if a == nil {
					a = &peopleAcc{titles: map[string]struct{}{}, titlesPlayed: map[string]struct{}{}}
					peopleAccs[k] = a
				}
				a.watchSec += ev.PlayDurationSec
				a.plays++
				a.titles[titleKey] = struct{}{}
				if isPlayed {
					a.watchSecPlayed += ev.PlayDurationSec
					a.playsPlayed++
					a.titlesPlayed[titleKey] = struct{}{}
				}
			}
		}

		for _, rng := range profileRangeList {
			if day < rangeCutoff(rng, now) {
				continue
			}
			accumulateTopItem(itemAccs, topItemKey{ev.UserID, rng, scope, ev.ItemID},
				ev.ItemName, ev.SeriesName, ev.PlayDurationSec, isPlayed)
			// Episodes also roll up into their series' own scope, so a show's
			// total sits next to its individual episodes.
			if ev.SeriesID != "" {
				accumulateTopItem(itemAccs, topItemKey{ev.UserID, rng, "series", ev.SeriesID},
					ev.SeriesName, ev.SeriesName, ev.PlayDurationSec, isPlayed)
			}
		}
	}

	var out PeopleAggregates
	for k, a := range peopleAccs {
		out.People = append(out.People, PeopleRow{
			UserID: k.user, Range: k.rng, Kind: k.kind, Person: k.person,
			WatchSec: a.watchSec, Plays: a.plays, DistinctTitles: int64(len(a.titles)),
			WatchSecPlayed: a.watchSecPlayed, PlaysPlayed: a.playsPlayed,
			DistinctTitlesPlayed: int64(len(a.titlesPlayed)),
		})
	}
	out.People = cappedPeopleRows(out.People)
	sortPeopleRows(out.People)

	for k, a := range itemAccs {
		out.TopItems = append(out.TopItems, TopItemRow{
			UserID: k.user, Range: k.rng, Scope: k.scope, ItemID: k.item,
			Name: a.name, SeriesName: a.seriesName,
			WatchSec: a.watchSec, Plays: a.plays,
			WatchSecPlayed: a.watchSecPlayed, PlaysPlayed: a.playsPlayed,
		})
	}
	out.TopItems = cappedTopItemRows(out.TopItems)
	sortTopItemRows(out.TopItems)
	return out
}

func accumulateTopItem(accs map[topItemKey]*topItemAcc, k topItemKey, name, seriesName string, watchSec int64, isPlayed bool) {
	a := accs[k]
	if a == nil {
		a = &topItemAcc{name: name, seriesName: seriesName}
		accs[k] = a
	}
	a.watchSec += watchSec
	a.plays++
	if isPlayed {
		a.watchSecPlayed += watchSec
		a.playsPlayed++
	}
}

func cappedPeopleRows(rows []PeopleRow) []PeopleRow {
	type gkey struct{ user, rng, kind string }
	var order []gkey
	groups := map[gkey][]PeopleRow{}
	for _, r := range rows {
		k := gkey{r.UserID, r.Range, r.Kind}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []PeopleRow
	for _, k := range order {
		g := groups[k]
		// Stable order in, so topUnion's tie-break (incoming index) is
		// deterministic instead of riding map iteration order -- otherwise a
		// tied group re-rolls a different arbitrary 50 on every refresh.
		slices.SortFunc(g, func(a, b PeopleRow) int { return cmp.Compare(a.Person, b.Person) })
		out = append(out, topUnion(g, peopleTopN,
			func(r PeopleRow) int64 { return r.WatchSec },
			func(r PeopleRow) int64 { return r.DistinctTitles },
		)...)
	}
	return out
}

func cappedTopItemRows(rows []TopItemRow) []TopItemRow {
	type gkey struct{ user, rng, scope string }
	var order []gkey
	groups := map[gkey][]TopItemRow{}
	for _, r := range rows {
		k := gkey{r.UserID, r.Range, r.Scope}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	var out []TopItemRow
	for _, k := range order {
		g := groups[k]
		// Same determinism fix as cappedPeopleRows: sort on a stable key
		// before topUnion, not after.
		slices.SortFunc(g, func(a, b TopItemRow) int { return cmp.Compare(a.ItemID, b.ItemID) })
		out = append(out, topUnion(g, peopleTopN,
			func(r TopItemRow) int64 { return r.WatchSec },
			func(r TopItemRow) int64 { return r.Plays },
		)...)
	}
	return out
}

func sortTopItemRows(rows []TopItemRow) {
	slices.SortFunc(rows, func(a, b TopItemRow) int {
		if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Range, b.Range); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Scope, b.Scope); c != 0 {
			return c
		}
		if c := cmp.Compare(b.WatchSec, a.WatchSec); c != 0 {
			return c
		}
		return cmp.Compare(a.ItemID, b.ItemID)
	})
}

func sortPeopleRows(rows []PeopleRow) {
	slices.SortFunc(rows, func(a, b PeopleRow) int {
		if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Range, b.Range); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		if c := cmp.Compare(b.WatchSec, a.WatchSec); c != 0 {
			return c
		}
		return cmp.Compare(a.Person, b.Person)
	})
}

// topUnion keeps the best n by each metric. A single binge would otherwise hand
// every slot to one show's regular cast, which is why breadth is ranked at all;
// capping by hours alone would throw the breadth ranking away before it is read.
func topUnion[T any](rows []T, n int, metrics ...func(T) int64) []T {
	keep := map[int]bool{}
	for _, m := range metrics {
		idx := make([]int, len(rows))
		for i := range idx {
			idx[i] = i
		}
		slices.SortStableFunc(idx, func(a, b int) int { return cmp.Compare(m(rows[b]), m(rows[a])) })
		for _, i := range idx[:min(n, len(idx))] {
			keep[i] = true
		}
	}
	out := make([]T, 0, len(keep))
	for i, r := range rows {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}
