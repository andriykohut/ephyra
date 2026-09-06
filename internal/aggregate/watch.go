package aggregate

import (
	"sort"
	"strings"

	"github.com/andriykohut/ephyra/internal/source"
)

type WatchAggregates struct {
	Daily   []WatchDailyRow
	Heatmap []HeatmapRow
}

// Watch rolls playback events into daily per-title rows and an all-time
// user×dow×hour heatmap. Timestamps are used as literal wall-clock (no zone
// shift): the plugin already writes server-local time.
func Watch(events []source.PlaybackEvent) WatchAggregates {
	type dKey struct{ day, user, item, method string }
	type hKey struct {
		user, library string
		dow, hour     int
	}
	daily := map[dKey]*WatchDailyRow{}
	heat := map[hKey]*HeatmapRow{}

	for _, e := range events {
		if e.At.IsZero() || e.UserID == "" || e.ItemID == "" {
			continue
		}
		day := e.At.Format("2006-01-02")
		mb := methodBucket(e.Method)
		dk := dKey{day, e.UserID, e.ItemID, mb}
		r := daily[dk]
		if r == nil {
			r = &WatchDailyRow{
				Day: day, UserID: e.UserID, ItemID: e.ItemID, Scope: e.ItemType,
				Name: e.ItemName, SeriesID: e.SeriesID, SeriesName: e.SeriesName, Method: mb,
				Library: e.Library,
			}
			daily[dk] = r
		}
		r.Plays++
		r.WatchSec += e.PlayDurationSec

		hk := hKey{e.UserID, e.Library, int(e.At.Weekday()), e.At.Hour()}
		h := heat[hk]
		if h == nil {
			h = &HeatmapRow{UserID: e.UserID, Library: e.Library, DOW: hk.dow, Hour: hk.hour}
			heat[hk] = h
		}
		h.Plays++
		h.WatchSec += e.PlayDurationSec
	}

	out := WatchAggregates{}
	for _, r := range daily {
		out.Daily = append(out.Daily, *r)
	}
	sort.Slice(out.Daily, func(i, j int) bool {
		a, b := out.Daily[i], out.Daily[j]
		if a.Day != b.Day {
			return a.Day < b.Day
		}
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.ItemID != b.ItemID {
			return a.ItemID < b.ItemID
		}
		return a.Method < b.Method
	})
	for _, h := range heat {
		out.Heatmap = append(out.Heatmap, *h)
	}
	sort.Slice(out.Heatmap, func(i, j int) bool {
		a, b := out.Heatmap[i], out.Heatmap[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.DOW != b.DOW {
			return a.DOW < b.DOW
		}
		if a.Hour != b.Hour {
			return a.Hour < b.Hour
		}
		return a.Library < b.Library
	})
	return out
}

// methodBucket collapses the plugin's free-text PlaybackMethod into five values.
func methodBucket(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.EqualFold(s, "DirectPlay") {
		return "DirectPlay"
	}
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "transcode") {
		return "Other"
	}
	v := paren(low, "v:")
	a := paren(low, "a:")
	if v == "" && a == "" {
		return "Other"
	}
	if v == "direct" && a == "direct" {
		return "Remux"
	}
	if v == "direct" {
		return "AudioTranscode"
	}
	return "VideoTranscode"
}

// paren pulls the token after key up to the next space or ')'.
func paren(s, key string) string {
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	rest := s[i+len(key):]
	if j := strings.IndexAny(rest, " )"); j >= 0 {
		return rest[:j]
	}
	return rest
}
