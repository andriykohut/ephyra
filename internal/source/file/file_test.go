package file

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrii/ephyra/internal/config"
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
