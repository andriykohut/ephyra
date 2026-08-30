// Package testsupport builds throwaway SQLite fixtures for tests.
package testsupport

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// LibraryFixtureDB builds a fresh SQLite database from testdata/library.fixture.sql
// in t.TempDir() and returns its path.
func LibraryFixtureDB(t testing.TB) string {
	t.Helper()
	return buildFixture(t, "library.fixture.sql")
}

// PlaybackFixtureDB builds a fresh playback_reporting.db from
// testdata/playback_reporting.fixture.sql and returns its path.
func PlaybackFixtureDB(t testing.TB) string {
	t.Helper()
	return buildFixture(t, "playback_reporting.fixture.sql")
}

// TwoDBLayout writes both fixtures into <tmp>/data/ as jellyfin.db and
// playback_reporting.db and returns <tmp> — a value for JELLYFIN_DATA_DIR.
func TwoDBLayout(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct{ sqlName, dbName string }{
		{"library.fixture.sql", "jellyfin.db"},
		{"playback_reporting.fixture.sql", "playback_reporting.db"},
	} {
		src, err := os.ReadFile(fixturePath(t, f.sqlName))
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dataDir, f.dbName))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(src)); err != nil {
			db.Close()
			t.Fatal(err)
		}
		db.Close()
	}
	return root
}

func buildFixture(t testing.TB, name string) string {
	t.Helper()
	sqlBytes, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	path := filepath.Join(t.TempDir(), name+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("exec fixture %s: %v", name, err)
	}
	return path
}

// fixturePath walks up from the test's working directory to the repo root
// (the directory with go.mod) and returns <root>/testdata/<name>.
func fixturePath(t testing.TB, name string) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "testdata", name)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (go.mod) from %s", dir)
		}
		dir = parent
	}
}
