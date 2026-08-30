// Package aggregate is the pure part: snapshots and events in, rows out. No I/O,
// no SQL, easy to test.
package aggregate

import (
	"sort"
	"time"

	"github.com/andrii/ephyra/internal/source"
)

type LabeledCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

type DiskBucket struct {
	Bucket string `json:"bucket"`
	Bytes  int64  `json:"bytes"`
	Items  int64  `json:"items"`
}

type GrowthPoint struct {
	Month      string `json:"month"`
	AddedItems int64  `json:"added_items"`
	AddedBytes int64  `json:"added_bytes"`
	CumItems   int64  `json:"cum_items"`
}

type LibraryAggregates struct {
	Totals           map[string]float64
	ItemsByLibrary   []LabeledCount
	DiskByResolution []DiskBucket
	DiskByCodec      []DiskBucket
	DiskByContainer  []DiskBucket
	DiskByLibrary    []DiskBucket
	GenresTop        []LabeledCount
	ByDecade         []LabeledCount
	Growth           []GrowthPoint
}

type diskAcc struct {
	bytes int64
	items int64
}

// Library rolls a snapshot into the aggregate rows the store persists. loc
// controls month bucketing for the growth chart; nil means UTC.
func Library(snap source.LibrarySnapshot, loc *time.Location) LibraryAggregates {
	if loc == nil {
		loc = time.UTC
	}
	totals := map[string]float64{
		"items.total":       0,
		"items.Movie":       0,
		"items.Episode":     0,
		"items.Series":      float64(snap.SeriesCount),
		"runtime_sec.total": 0,
		"bytes.total":       0,
		"count.uhd":         0,
		"count.hdr":         0,
		"count.dv":          0,
	}
	byLibraryCount := map[string]int64{}
	diskRes := map[string]*diskAcc{}
	diskCodec := map[string]*diskAcc{}
	diskContainer := map[string]*diskAcc{}
	diskLibrary := map[string]*diskAcc{}
	genre := map[string]int64{}
	decade := map[string]int64{}
	growthItems := map[string]int64{}
	growthBytes := map[string]int64{}

	add := func(m map[string]*diskAcc, k string, bytes int64) {
		a := m[k]
		if a == nil {
			a = &diskAcc{}
			m[k] = a
		}
		a.bytes += bytes
		a.items++
	}

	for _, it := range snap.Items {
		totals["items.total"]++
		switch it.Type {
		case "movie":
			totals["items.Movie"]++
		case "episode":
			totals["items.Episode"]++
		}
		totals["runtime_sec.total"] += float64(it.RuntimeSec)
		totals["bytes.total"] += float64(it.SizeBytes)

		res := ResolutionBucket(it.Width)
		codec := CodecBucket(it.VideoCodec)
		hdr := HDRBucket(it.ColorTransfer, it.DvProfile, it.HasVideo)
		if res == "4K" {
			totals["count.uhd"]++
		}
		if hdr == "HDR10" || hdr == "HLG" || hdr == "Dolby Vision" {
			totals["count.hdr"]++
		}
		if hdr == "Dolby Vision" {
			totals["count.dv"]++
		}

		byLibraryCount[it.Library]++
		add(diskRes, res, it.SizeBytes)
		add(diskCodec, codec, it.SizeBytes)
		container := it.Container
		if container == "" {
			container = "Unknown"
		}
		add(diskContainer, container, it.SizeBytes)
		add(diskLibrary, it.Library, it.SizeBytes)

		for _, g := range it.Genres {
			genre[g]++
		}
		decade[Decade(it.Year)]++

		if !it.DateCreated.IsZero() {
			m := it.DateCreated.In(loc).Format("2006-01")
			growthItems[m]++
			growthBytes[m] += it.SizeBytes
		}
	}

	out := LibraryAggregates{Totals: totals}
	out.ItemsByLibrary = labeledSortedDesc(byLibraryCount)
	out.DiskByResolution = diskSlice(diskRes)
	out.DiskByCodec = diskSlice(diskCodec)
	out.DiskByContainer = diskSlice(diskContainer)
	out.DiskByLibrary = diskSlice(diskLibrary)
	out.GenresTop = topN(labeledSortedDesc(genre), 15)
	out.ByDecade = decadeSlice(decade)
	out.Growth = growthSlice(growthItems, growthBytes)
	return out
}

func labeledSortedDesc(m map[string]int64) []LabeledCount {
	out := make([]LabeledCount, 0, len(m))
	for k, v := range m {
		out = append(out, LabeledCount{Label: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func topN(s []LabeledCount, n int) []LabeledCount {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func diskSlice(m map[string]*diskAcc) []DiskBucket {
	out := make([]DiskBucket, 0, len(m))
	for k, a := range m {
		out = append(out, DiskBucket{Bucket: k, Bytes: a.bytes, Items: a.items})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Bucket < out[j].Bucket
	})
	return out
}

func decadeSlice(m map[string]int64) []LabeledCount {
	out := make([]LabeledCount, 0, len(m))
	for k, v := range m {
		out = append(out, LabeledCount{Label: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		ui, uj := out[i].Label == "Unknown", out[j].Label == "Unknown"
		if ui != uj {
			return uj // Unknown sorts last
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func growthSlice(items, bytes map[string]int64) []GrowthPoint {
	months := make([]string, 0, len(items))
	for m := range items {
		months = append(months, m)
	}
	sort.Strings(months)
	var cum int64
	out := make([]GrowthPoint, 0, len(months))
	for _, m := range months {
		cum += items[m]
		out = append(out, GrowthPoint{Month: m, AddedItems: items[m], AddedBytes: bytes[m], CumItems: cum})
	}
	return out
}
