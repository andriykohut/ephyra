// Package aggregate is the pure part: snapshots and events in, rows out. No I/O,
// no SQL, easy to test.
package aggregate

import (
	"sort"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
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

type TagCoverage struct {
	ItemsTagged int64 `json:"tagged"`
	ItemsTotal  int64 `json:"total"`
}

type TagPair struct {
	A     string `json:"a"`
	B     string `json:"b"`
	Items int64  `json:"items"`
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
	TagsTop          []LabeledCount
	TagCoverage      TagCoverage
	TagPairs         []TagPair
}

// FilterLibrary returns a snapshot containing only items in lib, with
// SeriesCounts narrowed to lib's own count. lib == "" returns snap unchanged
// (the "All libraries" scope). Used by the scheduler to compute a
// per-library LibraryAggregates by calling the unmodified Library() function
// once per library, rather than threading a filter through it.
func FilterLibrary(snap source.LibrarySnapshot, lib string) source.LibrarySnapshot {
	if lib == "" {
		return snap
	}
	out := snap
	out.Items = nil
	for _, it := range snap.Items {
		if it.Library == lib {
			out.Items = append(out.Items, it)
		}
	}
	out.SeriesCounts = map[string]int{lib: snap.SeriesCounts[lib]}
	return out
}

// DistinctItemLibraries returns the unique Library values across items, in no
// particular order.
func DistinctItemLibraries(items []source.LibraryItem) []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if !seen[it.Library] {
			seen[it.Library] = true
			out = append(out, it.Library)
		}
	}
	return out
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
	var seriesTotal int
	for _, n := range snap.SeriesCounts {
		seriesTotal += n
	}
	totals := map[string]float64{
		"items.total":       0,
		"items.Movie":       0,
		"items.Episode":     0,
		"items.Series":      float64(seriesTotal),
		"runtime_sec.total": 0,
		"bytes.total":       0,
		"count.uhd":         0,
		"count.hdr":         0,
		"count.dv":          0,
		"tags.tagged_items": 0,
		"tags.total_items":  0,
	}
	byLibraryCount := map[string]int64{}
	diskRes := map[string]*diskAcc{}
	diskCodec := map[string]*diskAcc{}
	diskContainer := map[string]*diskAcc{}
	diskLibrary := map[string]*diskAcc{}
	genre := map[string]int64{}
	tag := map[string]int64{}
	tagPairs := map[[2]string]int64{}
	var taggedItems int64
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
		if len(it.Tags) > 0 {
			taggedItems++
		}
		for _, tg := range it.Tags {
			tag[tg]++
		}
		for i := 0; i < len(it.Tags); i++ {
			for j := i + 1; j < len(it.Tags); j++ {
				x, y := it.Tags[i], it.Tags[j]
				if x > y {
					x, y = y, x
				}
				tagPairs[[2]string{x, y}]++
			}
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

	totals["tags.tagged_items"] = float64(taggedItems)
	totals["tags.total_items"] = float64(len(snap.Items))
	out.TagCoverage = TagCoverage{ItemsTagged: taggedItems, ItemsTotal: int64(len(snap.Items))}
	out.TagsTop = topN(labeledSortedDesc(tag), 120)
	out.TagPairs = tagPairsSlice(tagPairs, 3, 400)
	return out
}

func tagPairsSlice(m map[[2]string]int64, minSupport int64, limit int) []TagPair {
	out := make([]TagPair, 0, len(m))
	for k, v := range m {
		if v < minSupport {
			continue
		}
		out = append(out, TagPair{A: k[0], B: k[1], Items: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Items != out[j].Items {
			return out[i].Items > out[j].Items
		}
		if out[i].A != out[j].A {
			return out[i].A < out[j].A
		}
		return out[i].B < out[j].B
	})
	if len(out) > limit {
		out = out[:limit]
	}
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
