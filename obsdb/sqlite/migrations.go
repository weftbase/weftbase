package sqlite

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The migrations are embedded SQL applied in order on Open, tracked in
// obsdb_migrations — the schema's own table, so one SQLite file can also
// hold thread's sessions (each module owns its migrations table). A file
// whose recorded version is ahead of this binary's highest fails Open
// with ErrNewerSchema: never run nothing and say nothing. A file written
// before the schema was squashed into 0001 (its obsdb_migrations has no
// epoch column) fails Open with ErrPredatesRebuild.

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrations maps version → file name; the list is derived from the
// embedded directory so a new .sql file is picked up by adding it.
var migrations = func() map[int]string {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		panic("sqlite: embedded migrations unreadable: " + err.Error())
	}
	out := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			panic("sqlite: migration " + name + " must start with its numeric version")
		}
		out[v] = name
	}
	return out
}()

// schemaEpoch marks the obsdb_migrations rows of the squashed history
// (0001 holds the whole schema): a file whose table lacks the column was
// written by the history before it.
const schemaEpoch = "rebuild"

func highestMigration() int {
	versions := sortedVersions()
	if len(versions) == 0 {
		return 0
	}
	return versions[len(versions)-1]
}

func sortedVersions() []int {
	versions := make([]int, 0, len(migrations))
	for v := range migrations {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	return versions
}

// ErrNewerSchema is returned by Open when the file's obsdb_migrations
// is ahead of this binary's highest migration: a database written by a
// newer weftbase fails loudly instead of silently ignoring tables it does
// not know.
var ErrNewerSchema = errors.New("sqlite: database schema is newer than this obsdb")

// ErrPredatesRebuild is returned by Open for a file written before the
// schema was squashed (an obsdb_migrations without the epoch column):
// there is no upgrade path from it.
var ErrPredatesRebuild = errors.New("sqlite: this database predates the rebuild; start a fresh data dir")

// migrate applies pending migrations, each in its own transaction, and
// refuses a schema ahead of this binary or from before the rebuild.
func migrate(db *sql.DB) error {
	var hasTable, hasEpoch int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'obsdb_migrations'`).Scan(&hasTable); err != nil {
		return err
	}
	if hasTable == 1 {
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('obsdb_migrations') WHERE name = 'epoch'`).Scan(&hasEpoch); err != nil {
			return err
		}
		if hasEpoch == 0 {
			return ErrPredatesRebuild
		}
	} else if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS obsdb_migrations (
			version INTEGER PRIMARY KEY, epoch TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	var current sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM obsdb_migrations`).Scan(&current); err != nil {
		return err
	}
	if current.Valid && int(current.Int64) > highestMigration() {
		return fmt.Errorf("%w: file has migration %d, this binary knows up to %d",
			ErrNewerSchema, current.Int64, highestMigration())
	}
	for _, v := range sortedVersions() {
		if current.Valid && int(current.Int64) >= v {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + migrations[v])
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		// Re-read the applied version inside the transaction: another
		// process may have migrated this file between the read above and
		// the write lock this Begin took (two first Opens on a fresh
		// file). The loser skips what the winner applied.
		var applied sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(version) FROM obsdb_migrations`).Scan(&applied); err != nil {
			_ = tx.Rollback()
			return err
		}
		if applied.Valid && int(applied.Int64) >= v {
			_ = tx.Rollback()
			continue
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("sqlite: migration %s: %w", migrations[v], err)
		}
		if _, err := tx.Exec(`INSERT INTO obsdb_migrations (version, epoch, applied_at) VALUES (?, ?, ?)`,
			v, schemaEpoch, time.Now().UTC().Format(time.RFC3339)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
