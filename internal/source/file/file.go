// Package file implements source.Source by copying Jellyfin's SQLite files to a
// work dir (the Jellyfin mount is read-only, and SQLite can't open a WAL
// database read-only without writing its -shm sidecar) and querying the copy.
package file

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
	"github.com/andriykohut/ephyra/internal/source"
	_ "modernc.org/sqlite"
)

type sqlDB = sql.DB
type snap = source.LibrarySnapshot

// queryLibrary turns an open *sql.DB (a read-only copy of library.db) into a
// snapshot. Task 5 replaces this stub with defaultQueryLibrary via an init().
var queryLibrary = func(*sqlDB) (snap, error) { return snap{}, nil }

type FileSource struct {
	dataDir    string
	workDir    string
	directRead bool
	log        *slog.Logger
}

func New(cfg config.Config, log *slog.Logger) *FileSource {
	return &FileSource{
		dataDir:    cfg.JellyfinDataDir,
		workDir:    cfg.WorkDir,
		directRead: cfg.DirectRead,
		log:        log,
	}
}

func (f *FileSource) Kind() string { return "file" }

// dbCandidates lists where the job's SQLite file might live, best guess first.
// 10.11 renamed the item store library.db -> jellyfin.db (EF-Core); the
// linuxserver image nests it one deeper (mount is /config, datadir is
// /config/data), so both data/ and data/data/ are checked.
func (f *FileSource) dbCandidates(job string) []string {
	names := []string{"jellyfin.db", "library.db"}
	if job == "watch" {
		names = []string{"playback_reporting.db"}
	}
	var out []string
	for _, dir := range []string{"data", filepath.Join("data", "data")} {
		for _, n := range names {
			out = append(out, filepath.Join(f.dataDir, dir, n))
		}
	}
	return out
}

// dbPath returns the first candidate that exists, or the best-guess candidate so
// error messages point somewhere sensible.
func (f *FileSource) dbPath(job string) string {
	cands := f.dbCandidates(job)
	for _, p := range cands {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return cands[0]
}

// SourceMTime returns the mod time of the job's database file, or the zero time
// (no error) if it doesn't exist.
func (f *FileSource) SourceMTime(job string) (time.Time, error) {
	fi, err := os.Stat(f.dbPath(job))
	if os.IsNotExist(err) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

func (f *FileSource) LibraryFacts(ctx context.Context) (source.LibrarySnapshot, error) {
	db, cleanup, err := f.openForRead(ctx, "library")
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	defer cleanup()

	s, err := queryLibrary(db)
	if err != nil {
		return source.LibrarySnapshot{}, err
	}
	s.GeneratedAt = time.Now().UTC()
	return s, nil
}

func (f *FileSource) PlaybackEvents(ctx context.Context, _ time.Time) ([]source.PlaybackEvent, error) {
	pdb, pcleanup, err := f.openForRead(ctx, "watch")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, source.ErrPluginUnavailable
		}
		return nil, err
	}
	defer pcleanup()

	if !tableExists(pdb, "PlaybackActivity") {
		return nil, source.ErrPluginUnavailable
	}
	events, err := queryPlaybackEvents(pdb)
	if err != nil {
		return nil, err
	}

	jdb, jcleanup, err := f.openForRead(ctx, "library")
	if err != nil {
		f.log.Warn("watch enrichment skipped: jellyfin.db unavailable", "err", err)
		return events, nil
	}
	defer jcleanup()
	if err := enrichPlaybackEvents(jdb, events); err != nil {
		f.log.Warn("watch enrichment failed", "err", err)
	}
	return events, nil
}

// ResolveLibraries backfills library names for a set of item ids against the
// current jellyfin.db copy, independent of what the Playback Reporting
// plugin currently reports. Ids not found in BaseItems are simply absent
// from the result map — best effort, no error.
func (f *FileSource) ResolveLibraries(ctx context.Context, itemIDs []string) (map[string]string, error) {
	jdb, cleanup, err := f.openForRead(ctx, "library")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return resolveLibraries(jdb, itemIDs)
}

// openForRead returns a read-only *sql.DB over a private copy of the job's
// database (default) or the live file (DIRECT_READ=true). cleanup closes the DB
// and, for the copy path, removes the copy.
func (f *FileSource) openForRead(_ context.Context, job string) (*sql.DB, func(), error) {
	src := f.dbPath(job)
	if _, err := os.Stat(src); err != nil {
		return nil, nil, fmt.Errorf("source db %s: %w", src, err)
	}

	if f.directRead {
		db, err := sql.Open("sqlite", "file:"+src+"?mode=ro&immutable=1")
		if err != nil {
			return nil, nil, err
		}
		return db, func() { db.Close() }, nil
	}

	dstDir := filepath.Join(f.workDir, job)
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return nil, nil, err
	}
	base := filepath.Base(src)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		from := src + suffix
		if _, err := os.Stat(from); err != nil {
			continue // sidecar may not exist
		}
		if err := copyFile(from, filepath.Join(dstDir, base+suffix)); err != nil {
			return nil, nil, err
		}
	}
	copyPath := filepath.Join(dstDir, base)
	db, err := sql.Open("sqlite", "file:"+copyPath)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		db.Close()
		for _, suffix := range []string{"", "-wal", "-shm"} {
			os.Remove(copyPath + suffix)
		}
	}
	return db, cleanup, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
