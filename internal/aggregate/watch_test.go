package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func at(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }

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
