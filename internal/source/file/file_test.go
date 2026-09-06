package file

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"errors"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/source"
	"github.com/andriykohut/ephyra/internal/testsupport"
)

func newFS(t *testing.T, dataDir string) *FileSource {
	t.Helper()
	return New(config.Config{
		JellyfinDataDir: dataDir,
		WorkDir:         t.TempDir(),
	}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceMTime(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "data", "library.db")
	writeFile(t, dbPath, "x")
	want := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(dbPath, want, want); err != nil {
		t.Fatal(err)
	}

	f := newFS(t, dataDir)
	got, err := f.SourceMTime("library")
	if err != nil {
		t.Fatal(err)
	}
	if got.Unix() != want.Unix() {
		t.Fatalf("mtime: got %v want %v", got, want)
	}

	got, err = f.SourceMTime("watch") // absent -> zero, no error
	if err != nil || !got.IsZero() {
		t.Fatalf("absent watch db: got %v err %v", got, err)
	}
}

func TestLibraryFactsCopiesSidecars(t *testing.T) {
	dataDir := t.TempDir()
	base := filepath.Join(dataDir, "data")
	writeFile(t, filepath.Join(base, "library.db"), "main")
	writeFile(t, filepath.Join(base, "library.db-wal"), "wal")
	writeFile(t, filepath.Join(base, "library.db-shm"), "shm")

	f := newFS(t, dataDir)
	var sawCopy bool
	orig := queryLibrary
	t.Cleanup(func() { queryLibrary = orig })
	queryLibrary = func(_ *sqlDB) (snap, error) {
		if _, err := os.Stat(filepath.Join(f.workDir, "library", "library.db")); err == nil {
			sawCopy = true
		}
		return snap{}, nil
	}

	if _, err := f.LibraryFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawCopy {
		t.Fatal("expected library.db copy to exist while queryLibrary ran")
	}
	if _, err := os.Stat(filepath.Join(f.workDir, "library", "library.db")); !os.IsNotExist(err) {
		t.Fatalf("copy not cleaned up: %v", err)
	}
}

func TestPlaybackEvents_ReadsAndEnriches(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	events, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 {
		t.Fatalf("want 8 events, got %d", len(events))
	}

	var sawBob bool
	for _, e := range events {
		if e.UserID == "66666666777788889999aaaaaaaaaaaa" {
			if e.UserName != "bob" {
				t.Errorf("bob not resolved: %q", e.UserName)
			}
			sawBob = true
		}
		if e.ItemType == "episode" && e.ItemID == "000000000000000000000000000000e1" {
			if e.SeriesName != "Some Show" || e.SeriesID == "" {
				t.Errorf("E1 series not resolved: %+v", e)
			}
		}
		if e.ItemID == "deadbeefdeadbeefdeadbeefdeadbeef" {
			if e.ItemName != "Ghost Movie (deleted)" || e.SeriesID != "" {
				t.Errorf("deleted item fallback wrong: %+v", e)
			}
		}
		if e.ItemName == "Some Show - s01e02 - Two" {
			t.Errorf("E2 name should be replaced by BaseItems.Name, got %q", e.ItemName)
		}
		if e.ItemID == "000000000000000000000000000000e2" {
			if e.At.Hour() != 23 || e.At.Day() != 8 {
				t.Errorf("At not literal wall-clock: %v", e.At)
			}
			if e.ItemName != "S1E2" {
				t.Errorf("E2 name = %q, want S1E2 (from BaseItems)", e.ItemName)
			}
		}
	}
	if !sawBob {
		t.Error("no bob events")
	}
}

func TestPlaybackEvents_EnrichesItemFacts(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	events, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	// Bravo (item id ...000b): RunTimeTicks 60000000000 = 6000s,
	// ProductionYear 2001, Genres "Comedy".
	var sawBravo bool
	for _, e := range events {
		if e.ItemID == "0000000000000000000000000000000b" {
			sawBravo = true
			if e.ItemRuntimeSec != 6000 || e.ItemYear != 2001 ||
				len(e.ItemGenres) != 1 || e.ItemGenres[0] != "Comedy" {
				t.Fatalf("bravo facts: rt=%d yr=%d genres=%v", e.ItemRuntimeSec, e.ItemYear, e.ItemGenres)
			}
		}
		if e.ItemID == "deadbeefdeadbeefdeadbeefdeadbeef" {
			if e.ItemRuntimeSec != 0 || e.ItemYear != 0 || len(e.ItemGenres) != 0 {
				t.Fatalf("deleted item should be zero-valued: %+v", e)
			}
		}
	}
	if !sawBravo {
		t.Fatal("no Bravo event")
	}
}

func TestPlaybackEvents_ItemTags(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	events, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var sawMovie, sawEpisode bool
	for _, e := range events {
		if e.ItemID == "0000000000000000000000000000000a" { // Alpha, movie -> own tags
			sawMovie = true
			if len(e.ItemTags) != 3 || e.ItemTags[0] != "heist" || e.ItemTags[1] != "vault" || e.ItemTags[2] != "dystopia" {
				t.Fatalf("Alpha ItemTags: %#v", e.ItemTags)
			}
		}
		if e.ItemType == "episode" && e.SeriesName == "Some Show" { // -> parent series' tags
			sawEpisode = true
			if len(e.ItemTags) != 2 || e.ItemTags[0] != "slow burn" || e.ItemTags[1] != "dystopia" {
				t.Fatalf("episode inherits series tags, got: %#v", e.ItemTags)
			}
		}
	}
	if !sawMovie || !sawEpisode {
		t.Fatalf("missing coverage: movie=%v episode=%v", sawMovie, sawEpisode)
	}
}

func TestPlaybackEvents_PluginAbsent(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "data", "jellyfin.db"), "")
	f := newFS(t, dir)
	_, err := f.PlaybackEvents(context.Background(), time.Time{})
	if !errors.Is(err, source.ErrPluginUnavailable) {
		t.Fatalf("want ErrPluginUnavailable, got %v", err)
	}
}

func TestPlaybackEvents_Library(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	events, err := f.PlaybackEvents(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	var sawMovie, sawEpisode, sawDeleted bool
	for _, e := range events {
		switch e.ItemID {
		case "0000000000000000000000000000000a", "0000000000000000000000000000000b", "0000000000000000000000000000000d":
			sawMovie = true
			if e.Library != "Movies" {
				t.Errorf("item %s library = %q, want Movies", e.ItemID, e.Library)
			}
		case "000000000000000000000000000000e1", "000000000000000000000000000000e2":
			sawEpisode = true
			if e.Library != "Shows" {
				t.Errorf("episode %s library = %q, want Shows", e.ItemID, e.Library)
			}
		case "deadbeefdeadbeefdeadbeefdeadbeef":
			sawDeleted = true
			if e.Library != "Unknown" {
				t.Errorf("deleted item library = %q, want Unknown", e.Library)
			}
		}
	}
	if !sawMovie || !sawEpisode || !sawDeleted {
		t.Fatalf("missing cases: movie=%v episode=%v deleted=%v", sawMovie, sawEpisode, sawDeleted)
	}
}

func TestResolveLibraries(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))

	resolved, err := f.ResolveLibraries(context.Background(), []string{
		"0000000000000000000000000000000a", // Alpha, Movies
		"000000000000000000000000000000e1", // S1E1, Shows
		"nonexistentitemidxxxxxxxxxxxxxxx", // not in BaseItems at all
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved["0000000000000000000000000000000a"] != "Movies" {
		t.Errorf("Alpha: %q", resolved["0000000000000000000000000000000a"])
	}
	if resolved["000000000000000000000000000000e1"] != "Shows" {
		t.Errorf("S1E1: %q", resolved["000000000000000000000000000000e1"])
	}
	if _, ok := resolved["nonexistentitemidxxxxxxxxxxxxxxx"]; ok {
		t.Error("unmatched id should not appear in the result map at all")
	}
}

func TestChunkIDs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		size int
		want [][]string
	}{
		{"empty", nil, 3, nil},
		{"under size", []string{"a", "b"}, 3, [][]string{{"a", "b"}}},
		{"exact multiple", []string{"a", "b", "c", "d"}, 2, [][]string{{"a", "b"}, {"c", "d"}}},
		{"remainder", []string{"a", "b", "c", "d", "e"}, 2, [][]string{{"a", "b"}, {"c", "d"}, {"e"}}},
		{"size <= 0 means one chunk", []string{"a", "b", "c"}, 0, [][]string{{"a", "b", "c"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chunkIDs(tc.in, tc.size)
			if len(got) != len(tc.want) {
				t.Fatalf("chunkIDs(%v, %d) = %v, want %v", tc.in, tc.size, got, tc.want)
			}
			for i := range got {
				if len(got[i]) != len(tc.want[i]) {
					t.Fatalf("chunk %d: got %v want %v", i, got[i], tc.want[i])
				}
				for j := range got[i] {
					if got[i][j] != tc.want[i][j] {
						t.Fatalf("chunk %d elem %d: got %q want %q", i, j, got[i][j], tc.want[i][j])
					}
				}
			}
		})
	}
}

// TestResolveLibraries_BatchesLargeInput proves resolveLibraries chunks its
// IN (...) query instead of building one placeholder per id: with an input
// spanning several resolveLibrariesBatchSize-sized batches, every batch's
// results must still land in the single merged output map, real items
// scattered across batch boundaries included.
func TestResolveLibraries_BatchesLargeInput(t *testing.T) {
	f := newFS(t, testsupport.TwoDBLayout(t))
	jdb, cleanup, err := f.openForRead(context.Background(), "library")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	const n = resolveLibrariesBatchSize*2 + 200 // spans 3 batches
	ids := make([]string, 0, n+2)
	// Real ids placed near the start and end so they land in different batches.
	ids = append(ids, "0000000000000000000000000000000a") // Alpha, Movies
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("nonexistentpadding%013d", i))
	}
	ids = append(ids, "000000000000000000000000000000e1") // S1E1, Shows

	resolved, err := resolveLibraries(jdb, ids)
	if err != nil {
		t.Fatal(err)
	}
	if resolved["0000000000000000000000000000000a"] != "Movies" {
		t.Errorf("Alpha (first batch): %q", resolved["0000000000000000000000000000000a"])
	}
	if resolved["000000000000000000000000000000e1"] != "Shows" {
		t.Errorf("S1E1 (last batch): %q", resolved["000000000000000000000000000000e1"])
	}
	if len(resolved) != 2 {
		t.Errorf("expected only the 2 real ids to resolve, got %d: %v", len(resolved), resolved)
	}
}
