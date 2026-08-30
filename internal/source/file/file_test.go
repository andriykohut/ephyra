package file

import (
	"context"
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

	var sawColin bool
	for _, e := range events {
		if e.UserID == "66666666777788889999aaaaaaaaaaaa" {
			if e.UserName != "bob" {
				t.Errorf("bob not resolved: %q", e.UserName)
			}
			sawColin = true
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
	if !sawColin {
		t.Error("no bob events")
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
