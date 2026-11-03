// Package store is the control store's contract: transactions, error
// kinds and the migration runner every backend shares.
//
// The control store holds tenants, keys, the registry, the run ledger,
// the queue, approvals, audit, budgets, webhooks and the events outbox.
// Its backends live in store/sqlite (one node) and, later,
// store/postgres (many nodes). Domains declare their repository
// interfaces against [Tx]; backends implement them. The package imports
// the standard library only.
package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Error kinds every repository returns, wrapped with context. Callers
// test them with errors.Is.
var (
	// ErrNotFound: no such row in the caller's scope. A row of another
	// tenant is also ErrNotFound, never a distinct error.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict: a uniqueness or compare-and-swap check failed.
	ErrConflict = errors.New("store: conflict")
	// ErrNewerSchema: the database was written by a newer build, or
	// predates the rebuild; the server refuses to start on it.
	ErrNewerSchema = errors.New("store: unsupported schema")
)

// Dialect names a backend's SQL dialect. Repositories write queries
// with "?" placeholders; [Rebind] turns them into the dialect's.
type Dialect int

const (
	SQLite Dialect = iota
	Postgres
)

// Tx is one transaction on the control store. Every state change and
// its outbox row are written through the same Tx (events.Log.Emit).
type Tx interface {
	// Context is the context the transaction was begun with.
	Context() context.Context
	// Dialect is the backend's SQL dialect.
	Dialect() Dialect
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// AfterCommit runs fn once the transaction commits (never on a
	// rollback): the outbox signals its hub this way.
	AfterCommit(fn func())
}

// Runner begins transactions. InTx commits when fn returns nil and
// rolls back otherwise, returning fn's error.
type Runner interface {
	InTx(ctx context.Context, fn func(Tx) error) error
	// Read runs fn on a read-only view (a plain connection on SQLite,
	// a read-only transaction on Postgres).
	Read(ctx context.Context, fn func(Tx) error) error
}

// Rebind rewrites "?" placeholders for the dialect: Postgres takes
// $1, $2…; SQLite keeps "?". Question marks inside single-quoted
// literals are left alone.
func Rebind(d Dialect, query string) string {
	if d != Postgres {
		return query
	}
	var b strings.Builder
	n, quoted := 0, false
	for _, r := range query {
		switch {
		case r == '\'':
			quoted = !quoted
			b.WriteRune(r)
		case r == '?' && !quoted:
			n++
			b.WriteString("$" + strconv.Itoa(n))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Exec runs a "?" query in tx, rebound for its dialect.
func Exec(tx Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(tx.Context(), Rebind(tx.Dialect(), query), args...)
}

// Query runs a "?" query in tx, rebound for its dialect.
func Query(tx Tx, query string, args ...any) (*sql.Rows, error) {
	return tx.QueryContext(tx.Context(), Rebind(tx.Dialect(), query), args...)
}

// QueryRow runs a "?" query in tx, rebound for its dialect.
func QueryRow(tx Tx, query string, args ...any) *sql.Row {
	return tx.QueryRowContext(tx.Context(), Rebind(tx.Dialect(), query), args...)
}

// NotFound maps sql.ErrNoRows to ErrNotFound and leaves any other
// error as it is.
func NotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
