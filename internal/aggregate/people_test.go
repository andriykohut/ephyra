package aggregate

import (
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func TestCreditIndexFallsBackToSeries(t *testing.T) {
	credits := []source.Credit{
		{ItemID: "series1", Person: "Ada Vex", Kind: "actor"},
		{ItemID: "ep1", Person: "Bo Quill", Kind: "actor"},
	}
	ix := newCreditIndex(credits)

	// ep1 has its own cast, so the series' is not consulted.
	own := ix.forEvent(source.PlaybackEvent{ItemID: "ep1", SeriesID: "series1", ItemType: "episode"})
	if len(own) != 1 || own[0].Person != "Bo Quill" {
		t.Fatalf("own credits = %+v, want just Bo Quill", own)
	}

	// ep2 has none. Roughly half the episodes in a real library are like this,
	// so without the fallback the people charts are movies-only.
	inherited := ix.forEvent(source.PlaybackEvent{ItemID: "ep2", SeriesID: "series1", ItemType: "episode"})
	if len(inherited) != 1 || inherited[0].Person != "Ada Vex" {
		t.Fatalf("inherited credits = %+v, want Ada Vex from the series", inherited)
	}
}

func TestCreditIndexMoviesDoNotInherit(t *testing.T) {
	ix := newCreditIndex([]source.Credit{{ItemID: "series1", Person: "Ada Vex", Kind: "actor"}})
	got := ix.forEvent(source.PlaybackEvent{ItemID: "movie1", ItemType: "movie"})
	if len(got) != 0 {
		t.Fatalf("movie inherited %+v; a movie has no parent to inherit from", got)
	}
}

func TestPeopleCountsSeriesOnceForBreadth(t *testing.T) {
	credits := []source.Credit{{ItemID: "series1", Person: "Ada Vex", Kind: "actor"}}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	var events []source.PlaybackEvent
	for i := range 20 { // 20 episodes of one series
		events = append(events, source.PlaybackEvent{
			At: now.Add(-time.Duration(i) * time.Hour), UserID: "u1",
			ItemID: fmt.Sprintf("ep%d", i), ItemType: "episode",
			SeriesID: "series1", SeriesName: "Series One", PlayDurationSec: 1200,
		})
	}
	got := People(events, credits, map[UserItem]bool{}, now)

	var ada *PeopleRow
	for i := range got.People {
		if got.People[i].Person == "Ada Vex" && got.People[i].Range == "all" {
			ada = &got.People[i]
		}
	}
	if ada == nil {
		t.Fatal("no all-range row for Ada Vex")
	}
	if ada.DistinctTitles != 1 {
		t.Errorf("distinct_titles = %d, want 1: a series counts once, not per episode",
			ada.DistinctTitles)
	}
	if ada.Plays != 20 {
		t.Errorf("plays = %d, want 20", ada.Plays)
	}
	if ada.WatchSec != 20*1200 {
		t.Errorf("watch_sec = %d, want %d", ada.WatchSec, 20*1200)
	}
}

func TestPeoplePlayedMetricsCountOnlyTickedItems(t *testing.T) {
	credits := []source.Credit{
		{ItemID: "m1", Person: "Ada Vex", Kind: "actor"},
		{ItemID: "m2", Person: "Ada Vex", Kind: "actor"},
	}
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		{At: now.Add(-time.Hour), UserID: "u1", ItemID: "m1", ItemType: "movie", PlayDurationSec: 6000},
		{At: now.Add(-2 * time.Hour), UserID: "u1", ItemID: "m2", ItemType: "movie", PlayDurationSec: 300},
	}
	played := map[UserItem]bool{{UserID: "u1", ItemID: "m1"}: true}

	got := People(events, credits, played, now)
	var found bool
	for _, r := range got.People {
		if r.Person != "Ada Vex" || r.Range != "all" {
			continue
		}
		found = true
		if r.WatchSec != 6300 {
			t.Errorf("watch_sec = %d, want 6300 (everything)", r.WatchSec)
		}
		if r.Plays != 2 {
			t.Errorf("plays = %d, want 2 (everything)", r.Plays)
		}
		if r.WatchSecPlayed != 6000 {
			t.Errorf("watch_sec_played = %d, want 6000 (only the ticked item)", r.WatchSecPlayed)
		}
		if r.PlaysPlayed != 1 {
			t.Errorf("plays_played = %d, want 1 (only the ticked item)", r.PlaysPlayed)
		}
		if r.DistinctTitlesPlayed != 1 {
			t.Errorf("distinct_titles_played = %d, want 1", r.DistinctTitlesPlayed)
		}
	}
	if !found {
		t.Fatal("no all-range row for Ada Vex")
	}
}

// peopleSet collects the all-range actor rows' Person names into a set, so two
// runs' surviving sets can be compared regardless of output order.
func peopleSet(agg PeopleAggregates) map[string]bool {
	out := map[string]bool{}
	for _, r := range agg.People {
		if r.Range == "all" && r.Kind == "actor" {
			out[r.Person] = true
		}
	}
	return out
}

func TestPeopleCapKeepsSameSetAcrossRepeatedRuns(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	const n = 60 // over peopleTopN, all tied on watch_sec and distinct_titles
	var credits []source.Credit
	var events []source.PlaybackEvent
	for i := range n {
		item := fmt.Sprintf("m%02d", i)
		person := fmt.Sprintf("Actor%02d", i)
		credits = append(credits, source.Credit{ItemID: item, Person: person, Kind: "actor"})
		events = append(events, source.PlaybackEvent{
			At: now.Add(-time.Duration(i) * time.Hour), UserID: "u1",
			ItemID: item, ItemType: "movie", PlayDurationSec: 1000,
		})
	}

	first := peopleSet(People(events, credits, map[UserItem]bool{}, now))
	second := peopleSet(People(events, credits, map[UserItem]bool{}, now))

	if len(first) != peopleTopN {
		t.Fatalf("kept %d actor rows, want the cap (%d)", len(first), peopleTopN)
	}
	if !maps.Equal(first, second) {
		t.Fatalf("cap picked a different subset across runs on tied input:\nrun1: %v\nrun2: %v",
			first, second)
	}
}

func TestTopItemsRollEpisodesIntoSeriesScope(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	events := []source.PlaybackEvent{
		{At: now.Add(-time.Hour), UserID: "u1", ItemID: "ep1", ItemName: "Pilot",
			ItemType: "episode", SeriesID: "s1", SeriesName: "Series One", PlayDurationSec: 1200},
		{At: now.Add(-2 * time.Hour), UserID: "u1", ItemID: "ep2", ItemName: "Second",
			ItemType: "episode", SeriesID: "s1", SeriesName: "Series One", PlayDurationSec: 1500},
	}
	got := People(events, nil, map[UserItem]bool{}, now)

	var series, episodes int
	for _, r := range got.TopItems {
		if r.Range != "all" {
			continue
		}
		switch r.Scope {
		case "series":
			series++
			if r.ItemID != "s1" || r.WatchSec != 2700 {
				t.Errorf("series row = %+v, want s1 with 2700s", r)
			}
		case "episode":
			episodes++
		}
	}
	if series != 1 {
		t.Errorf("series rows = %d, want 1", series)
	}
	if episodes != 2 {
		t.Errorf("episode rows = %d, want 2; episodes stay their own scope too", episodes)
	}
}
