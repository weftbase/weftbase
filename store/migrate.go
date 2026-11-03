package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Migration is one numbered schema step: NNNN_name.sql in a
// backend's migrations directory.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// LoadMigrations reads every NNNN_name.sql file of dir in fsys, in
// version order. A gap or a duplicate version is an error.
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var ms []Migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		num, rest, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("store: migration %s: want NNNN_name.sql", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("store: migration %s: %w", name, err)
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		ms = append(ms, Migration{Version: v, Name: rest, SQL: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Version < ms[j].Version })
	for i, m := range ms {
		if m.Version != i+1 {
			return nil, fmt.Errorf("store: migration versions must run 1..n without gaps; found %d at position %d", m.Version, i+1)
		}
	}
	return ms, nil
}

// Migrate applies the migrations db has not seen, each in its own
// transaction, recording them in schema_migrations. A database whose
// recorded version is past the newest migration is refused with
// ErrNewerSchema: it was written by a newer build.
func Migrate(ctx context.Context, db *sql.DB, d Dialect, ms []Migration) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	var cur int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&cur); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if cur > len(ms) {
		return fmt.Errorf("%w: the control database is at version %d, this build knows %d", ErrNewerSchema, cur, len(ms))
	}
	for _, m := range ms[cur:] {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migration %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, Rebind(d, `INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, CURRENT_TIMESTAMP)`), m.Version, m.Name); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
