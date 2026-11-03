// Package sqlite is the control store on one SQLite file: the Stage 0
// backend (one node, one writer, WAL). It owns the connections, the
// dialect and the migrations; the domain repositories write portable
// SQL over store.Tx and run on it unchanged.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/weftbase/weftbase/store"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB is the control database. Writes go through one connection
// (BEGIN IMMEDIATE), reads through a small pool.
type DB struct {
	w, r *sql.DB
	path string
}

var _ store.Runner = (*DB)(nil)

// Open opens (creating when missing) the control database at path and
// applies the migrations. ":memory:" opens a private in-memory
// database (tests).
func Open(ctx context.Context, path string) (*DB, error) {
	var d DB
	d.path = path
	if path == ":memory:" {
		h, err := sql.Open("sqlite", "file::memory:?_pragma=foreign_keys(ON)&_txlock=immediate")
		if err != nil {
			return nil, err
		}
		h.SetMaxOpenConns(1)
		d.w, d.r = h, h
	} else {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
		q := "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(ON)"
		file := "file:" + (&url.URL{Path: path}).EscapedPath()
		w, err := sql.Open("sqlite", file+q+"&_txlock=immediate")
		if err != nil {
			return nil, err
		}
		w.SetMaxOpenConns(1)
		r, err := sql.Open("sqlite", file+q)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		r.SetMaxOpenConns(4)
		d.w, d.r = w, r
	}
	ms, err := store.LoadMigrations(migrations, "migrations")
	if err == nil {
		err = refuseOld(ctx, d.w)
	}
	if err == nil {
		err = store.Migrate(ctx, d.w, store.SQLite, ms)
	}
	if err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("control database %s: %w", path, err)
	}
	return &d, nil
}

// refuseOld refuses a file written by the old server (its tables, no
// schema_migrations): the rebuild starts on a fresh data dir.
func refuseOld(ctx context.Context, db *sql.DB) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		return fmt.Errorf("%w: this database predates the rebuild; start a fresh data dir", store.ErrNewerSchema)
	}
	return nil
}

// Path is the file the database was opened at.
func (d *DB) Path() string { return d.path }

// SQL is the writer handle, for a repository test that needs raw SQL.
func (d *DB) SQL() *sql.DB { return d.w }

// Close closes both pools.
func (d *DB) Close() error {
	var errs []error
	if d.w != nil {
		errs = append(errs, d.w.Close())
	}
	if d.r != nil && d.r != d.w {
		errs = append(errs, d.r.Close())
	}
	return errors.Join(errs...)
}

// Ping checks the writer connection (readiness).
func (d *DB) Ping(ctx context.Context) error { return d.w.PingContext(ctx) }

// tx is a store.Tx over a *sql.Tx.
type tx struct {
	*sql.Tx
	ctx   context.Context
	after []func()
}

func (t *tx) Context() context.Context          { return t.ctx }
func (t *tx) Dialect() store.Dialect            { return store.SQLite }
func (t *tx) AfterCommit(fn func())             { t.after = append(t.after, fn) }
func (t *tx) run(fn func(store.Tx) error) error { return fn(t) }

// InTx runs fn in a write transaction (BEGIN IMMEDIATE).
func (d *DB) InTx(ctx context.Context, fn func(store.Tx) error) error {
	return d.do(ctx, d.w, nil, fn)
}

// Read runs fn in a read-only transaction on the reader pool.
func (d *DB) Read(ctx context.Context, fn func(store.Tx) error) error {
	if d.r == d.w {
		return d.do(ctx, d.w, nil, fn)
	}
	return d.do(ctx, d.r, &sql.TxOptions{ReadOnly: true}, fn)
}

func (d *DB) do(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(store.Tx) error) (err error) {
	sqlTx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	t := &tx{Tx: sqlTx, ctx: ctx}
	defer func() {
		if p := recover(); p != nil {
			_ = sqlTx.Rollback()
			panic(p)
		}
	}()
	if err := t.run(fn); err != nil {
		_ = sqlTx.Rollback()
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return err
	}
	for _, f := range t.after {
		f()
	}
	return nil
}

// IsUnique reports whether err is a UNIQUE or PRIMARY KEY violation.
func IsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
