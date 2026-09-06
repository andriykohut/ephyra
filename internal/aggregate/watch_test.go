package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func at(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }

// mustTime parses an RFC3339 timestamp, panicking on failure. No such helper
// existed in this file before this test; added here for tests that read more
// naturally from a literal timestamp string than from at()'s y/m/d/h args.
func mustTime(s string) time.Time {
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return tm
}

func TestWatch_DailyAndHeatmap(t *testing.T) {
	e := func(tm time.Time, user, item, scope, method string, dur int64) source.PlaybackEvent {
		return source.PlaybackEvent{At: tm, UserID: user, ItemID: item, ItemType: scope, Method: method, PlayDurationSec: dur}
	}
	events := []source.PlaybackEvent{
		e(at(2025, 1, 6, 20), "u1", "m1", "movie", "DirectPlay", 3600),
		e(at(2025, 1, 6, 21), "u1", "m1", "movie", "DirectPlay", 600), // merges with above
		e(at(2025, 1, 6, 0), "u1", "m2", "movie", "Transcode (v:h264 a:aac)", 1200),
		e(at(2025, 1, 6, 23), "u1", "m3", "movie", "Transcode (v:direct a:direct)", 900),
		e(at(2025, 1, 7, 9), "u2", "e1", "episode", "Transcode (v:direct a:aac)", 1500),
	}
	events[4].SeriesID, events[4].SeriesName = "s1", "Show"

	agg := Watch(events)

	var m1 *WatchDailyRow
	buckets := map[string]string{}
	for i := range agg.Daily {
		buckets[agg.Daily[i].ItemID] = agg.Daily[i].Method
		if agg.Daily[i].ItemID == "m1" {
			m1 = &agg.Daily[i]
		}
	}
	if m1 == nil || m1.Plays != 2 || m1.WatchSec != 4200 || m1.Method != "DirectPlay" || m1.Day != "2025-01-06" {
		t.Fatalf("m1 row: %+v", m1)
	}
	if buckets["m2"] != "VideoTranscode" || buckets["m3"] != "Remux" {
		t.Fatalf("method buckets: %+v", buckets)
	}
	for _, r := range agg.Daily {
		if r.ItemID == "e1" && (r.SeriesID != "s1" || r.Scope != "episode") {
			t.Fatalf("e1 row: %+v", r)
		}
	}

	cells := map[int]int64{}
	for _, h := range agg.Heatmap {
		if h.UserID == "u1" {
			cells[h.Hour] += h.WatchSec
		}
	}
	if cells[0] != 1200 || cells[20] != 3600 || cells[21] != 600 || cells[23] != 900 {
		t.Fatalf("u1 heatmap cells: %+v", cells)
	}
}

func TestWatch_Library(t *testing.T) {
	events := []source.PlaybackEvent{
		{At: mustTime("2025-01-06T20:00:00Z"), UserID: "u1", ItemID: "m1", ItemType: "movie",
			Method: "DirectPlay", PlayDurationSec: 600, Library: "Movies"},
		// Same hour as the event above (20:xx) so this actually exercises the
		// (user, dow, hour) collision the comment below describes — without
		// Library in hKey these two would merge into one heatmap row.
		{At: mustTime("2025-01-06T20:30:00Z"), UserID: "u1", ItemID: "e1", ItemType: "episode",
			Method: "DirectPlay", PlayDurationSec: 600, Library: "Shows"},
	}
	out := Watch(events)

	daily := map[string]WatchDailyRow{}
	for _, r := range out.Daily {
		daily[r.ItemID] = r
	}
	if daily["m1"].Library != "Movies" || daily["e1"].Library != "Shows" {
		t.Fatalf("daily libraries: m1=%q e1=%q", daily["m1"].Library, daily["e1"].Library)
	}

	// Same user, same hour, two different libraries -> two heatmap rows, not one.
	if len(out.Heatmap) != 2 {
		t.Fatalf("want 2 heatmap rows (split by library), got %d: %+v", len(out.Heatmap), out.Heatmap)
	}
	libs := map[string]bool{}
	for _, h := range out.Heatmap {
		libs[h.Library] = true
	}
	if !libs["Movies"] || !libs["Shows"] {
		t.Fatalf("heatmap libraries: %v", libs)
	}
}

func TestMethodBucket_Table(t *testing.T) {
	cases := map[string]string{
		"DirectPlay":                    "DirectPlay",
		"Transcode (v:direct a:direct)": "Remux",
		"Transcode (v:direct a:aac)":    "AudioTranscode",
		"Transcode (v:h264 a:aac)":      "VideoTranscode",
		"Transcode (v:h264 a:direct)":   "VideoTranscode",
		"Transcode (garbled)":           "Other",
		"":                              "Other",
		"something else":                "Other",
	}
	for in, want := range cases {
		if got := methodBucket(in); got != want {
			t.Errorf("methodBucket(%q) = %q want %q", in, got, want)
		}
	}
}
