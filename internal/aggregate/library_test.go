package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func mkItem(name, typ string, size, runtimeSec int64, date string, year int, genres []string, lib, container, codec, transfer string, width int, hasVideo bool, dv *int) source.LibraryItem {
	d, _ := time.Parse(time.RFC3339, date)
	return source.LibraryItem{
		Name: name, Type: typ, SizeBytes: size, RuntimeSec: runtimeSec,
		DateCreated: d, Year: year, Genres: genres, Library: lib, Container: container,
		VideoCodec: codec, ColorTransfer: transfer, Width: width, HasVideo: hasVideo, DvProfile: dv,
	}
}

func TestLibraryAggregates_Tags(t *testing.T) {
	mk := func(name string, tags []string) source.LibraryItem {
		return source.LibraryItem{Name: name, Type: "movie", Year: 2000, Tags: tags}
	}
	snap := source.LibrarySnapshot{Items: []source.LibraryItem{
		mk("A", []string{"heist", "vault", "dystopia"}),
		mk("B", []string{"heist", "vault"}),
		mk("C", []string{"heist", "vault"}),
		mk("D", []string{"heist", "dystopia"}),
		mk("E", nil),
	}}
	a := Library(snap, time.UTC)

	if a.TagCoverage.ItemsTagged != 4 || a.TagCoverage.ItemsTotal != 5 {
		t.Fatalf("coverage: %+v", a.TagCoverage)
	}
	if a.Totals["tags.tagged_items"] != 4 || a.Totals["tags.total_items"] != 5 {
		t.Fatalf("coverage totals: %v / %v", a.Totals["tags.tagged_items"], a.Totals["tags.total_items"])
	}
	if len(a.TagsTop) == 0 || a.TagsTop[0].Label != "heist" || a.TagsTop[0].Count != 4 {
		t.Fatalf("TagsTop head: %+v", a.TagsTop)
	}
	if len(a.TagPairs) != 1 || a.TagPairs[0].A != "heist" || a.TagPairs[0].B != "vault" || a.TagPairs[0].Items != 3 {
		t.Fatalf("TagPairs: %+v", a.TagPairs)
	}
}

func TestLibraryAggregates(t *testing.T) {
	dv := 8
	snap := source.LibrarySnapshot{
		SeriesCounts: map[string]int{"Shows": 3},
		Items: []source.LibraryItem{
			mkItem("Alpha", "movie", 8_000_000_000, 7200, "2024-01-05T10:00:00Z", 1994, []string{"Drama", "Thriller"}, "Movies", "mkv", "hevc", "smpte2084", 3840, true, nil),
			mkItem("Bravo", "movie", 4_000_000_000, 6000, "2024-01-20T10:00:00Z", 2001, []string{"Comedy"}, "Movies", "mp4", "h264", "bt709", 1920, true, nil),
			mkItem("Charlie", "movie", 0, 9000, "2024-02-10T10:00:00Z", 2019, []string{"Drama"}, "Movies", "mkv", "", "", 0, false, nil),
			mkItem("Delta", "movie", 15_000_000_000, 8000, "2024-03-02T10:00:00Z", 2022, []string{"Sci-Fi", "Drama"}, "Movies", "mkv", "hevc", "arib-std-b67", 3840, true, nil),
			mkItem("S1E1", "episode", 1_200_000_000, 1800, "2024-02-15T10:00:00Z", 2020, []string{"Drama"}, "Shows", "mkv", "av1", "smpte2084", 1920, true, &dv),
			mkItem("S1E2", "episode", 1_300_000_000, 1800, "2024-03-16T10:00:00Z", 2020, []string{"Drama"}, "Shows", "mkv", "h264", "", 1920, true, nil),
		},
	}
	a := Library(snap, time.UTC)

	if a.Totals["items.total"] != 6 || a.Totals["items.Movie"] != 4 || a.Totals["items.Episode"] != 2 || a.Totals["items.Series"] != 3 {
		t.Fatalf("counts: %+v", a.Totals)
	}
	if a.Totals["runtime_sec.total"] != 7200+6000+9000+8000+1800+1800 {
		t.Fatalf("runtime: %v", a.Totals["runtime_sec.total"])
	}
	if a.Totals["bytes.total"] != 8e9+4e9+0+15e9+1.2e9+1.3e9 {
		t.Fatalf("bytes: %v", a.Totals["bytes.total"])
	}
	if a.Totals["count.uhd"] != 2 {
		t.Fatalf("uhd: %v", a.Totals["count.uhd"])
	}
	if a.Totals["count.hdr"] != 3 {
		t.Fatalf("hdr: %v", a.Totals["count.hdr"])
	}
	if a.Totals["count.dv"] != 1 {
		t.Fatalf("dv: %v", a.Totals["count.dv"])
	}

	if got := findDisk(a.DiskByCodec, "HEVC"); got.Bytes != 8e9+15e9 || got.Items != 2 {
		t.Fatalf("disk HEVC: %+v", got)
	}
	if got := findDisk(a.DiskByResolution, "Unknown"); got.Items != 1 || got.Bytes != 0 {
		t.Fatalf("disk res Unknown: %+v", got)
	}
	if got := findDisk(a.DiskByLibrary, "Shows"); got.Items != 2 || got.Bytes != 2.5e9 {
		t.Fatalf("disk lib Shows: %+v", got)
	}

	if got := findLabel(a.GenresTop, "Drama"); got.Count != 5 { // Alpha, Charlie, Delta, S1E1, S1E2
		t.Fatalf("genre Drama: %+v", got)
	}
	if a.ByDecade[0].Label != "1990s" {
		t.Fatalf("decade order: %+v", a.ByDecade)
	}

	if len(a.Growth) != 3 {
		t.Fatalf("growth points: %+v", a.Growth)
	}
	if a.Growth[0].Month != "2024-01" || a.Growth[0].AddedItems != 2 || a.Growth[0].CumItems != 2 {
		t.Fatalf("growth[0]: %+v", a.Growth[0])
	}
	if a.Growth[2].Month != "2024-03" || a.Growth[2].CumItems != 6 {
		t.Fatalf("growth[2]: %+v", a.Growth[2])
	}
	if a.Growth[0].AddedBytes != 12e9 {
		t.Fatalf("growth[0] bytes: %v", a.Growth[0].AddedBytes)
	}

	if a.ItemsByLibrary[0].Label != "Movies" || a.ItemsByLibrary[0].Count != 4 {
		t.Fatalf("items by library: %+v", a.ItemsByLibrary)
	}
}

func findDisk(s []DiskBucket, b string) DiskBucket {
	for _, x := range s {
		if x.Bucket == b {
			return x
		}
	}
	return DiskBucket{}
}

func findLabel(s []LabeledCount, l string) LabeledCount {
	for _, x := range s {
		if x.Label == l {
			return x
		}
	}
	return LabeledCount{}
}

func TestFilterLibrary(t *testing.T) {
	snap := source.LibrarySnapshot{
		Items: []source.LibraryItem{
			{ID: "a", Library: "Movies", Type: "movie"},
			{ID: "b", Library: "Movies", Type: "movie"},
			{ID: "c", Library: "Shows", Type: "episode"},
		},
		SeriesCounts: map[string]int{"Shows": 2, "Movies": 0},
	}

	all := FilterLibrary(snap, "")
	if len(all.Items) != 3 {
		t.Fatalf("empty filter should return everything, got %d items", len(all.Items))
	}

	movies := FilterLibrary(snap, "Movies")
	if len(movies.Items) != 2 || movies.SeriesCounts["Movies"] != 0 {
		t.Fatalf("movies: items=%d series=%d", len(movies.Items), movies.SeriesCounts["Movies"])
	}
	shows := FilterLibrary(snap, "Shows")
	if len(shows.Items) != 1 || shows.SeriesCounts["Shows"] != 2 {
		t.Fatalf("shows: items=%d series=%d", len(shows.Items), shows.SeriesCounts["Shows"])
	}
}

func TestDistinctItemLibraries(t *testing.T) {
	items := []source.LibraryItem{
		{Library: "Movies"}, {Library: "Shows"}, {Library: "Movies"}, {Library: "Unknown"},
	}
	got := DistinctItemLibraries(items)
	want := map[string]bool{"Movies": true, "Shows": true, "Unknown": true}
	if len(got) != 3 {
		t.Fatalf("got %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected library %q", g)
		}
	}
}
