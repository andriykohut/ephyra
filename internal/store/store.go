// Package store is Ephyra's own SQLite database: schema migrations plus typed
// read/write helpers. The API reads only from here, never from Jellyfin's files.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

type Store struct{ db *sql.DB }

// Open opens (creating if needed) the SQLite file at path and applies any
// pending migrations. Calling it again on the same file is a no-op.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	// modernc's driver is happiest with a single connection for a writer.
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		return err
	}

	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: bad version prefix", name)
		}
		if applied[v] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			v, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// RefreshMeta is the bookkeeping row a refresh job writes about its last run.
type RefreshMeta struct {
	Job             string
	LastRunAt       time.Time
	SourceMTime     time.Time // zero means none recorded
	DurationMS      int64
	OK              bool
	Skipped         bool
	PluginAvailable bool
	Error           string
}

const tsLayout = time.RFC3339Nano

func (s *Store) GetRefreshMeta(ctx context.Context, job string) (RefreshMeta, bool, error) {
	var (
		m                        RefreshMeta
		lastRun, srcMTime        string
		ok, skipped, pluginAvail int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT job, last_run_at, source_mtime, duration_ms, ok, skipped, plugin_available, error
		   FROM refresh_meta WHERE job = ?`, job,
	).Scan(&m.Job, &lastRun, &srcMTime, &m.DurationMS, &ok, &skipped, &pluginAvail, &m.Error)
	if err == sql.ErrNoRows {
		return RefreshMeta{}, false, nil
	}
	if err != nil {
		return RefreshMeta{}, false, err
	}
	m.LastRunAt, _ = time.Parse(tsLayout, lastRun)
	if srcMTime != "" {
		m.SourceMTime, _ = time.Parse(tsLayout, srcMTime)
	}
	m.OK, m.Skipped, m.PluginAvailable = ok == 1, skipped == 1, pluginAvail == 1
	return m, true, nil
}

func (s *Store) SetRefreshMeta(ctx context.Context, m RefreshMeta) error {
	srcMTime := ""
	if !m.SourceMTime.IsZero() {
		srcMTime = m.SourceMTime.UTC().Format(tsLayout)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO refresh_meta (job, last_run_at, source_mtime, duration_ms, ok, skipped, plugin_available, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job) DO UPDATE SET
		  last_run_at=excluded.last_run_at, source_mtime=excluded.source_mtime,
		  duration_ms=excluded.duration_ms, ok=excluded.ok, skipped=excluded.skipped,
		  plugin_available=excluded.plugin_available, error=excluded.error`,
		m.Job, m.LastRunAt.UTC().Format(tsLayout), srcMTime, m.DurationMS,
		b2i(m.OK), b2i(m.Skipped), b2i(m.PluginAvailable), m.Error,
	)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
