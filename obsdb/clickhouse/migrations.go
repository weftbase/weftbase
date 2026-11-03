package clickhouse

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	ch "github.com/ClickHouse/clickhouse-go/v2"
)

// The migrations are embedded SQL applied in order on Open, tracked in
// obsdb_migrations — the same table name and numbering rule as the
// SQLite backend (S3.6), so one ClickHouse database's schema is
// versioned exactly like one SQLite file's. A database whose recorded
// version is ahead of this binary's highest fails Open with
// ErrNewerSchema: never run nothing and say nothing. A database written
// before the schema was squashed into 0001 (its obsdb_migrations has no
// epoch column) fails Open with ErrPredatesRebuild.

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrations maps version → file name; derived from the embedded
// directory so a new .sql file is picked up by adding it.
var migrations = func() map[int]string {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		panic("clickhouse: embedded migrations unreadable: " + err.Error())
	}
	out := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			panic("clickhouse: migration " + name + " must start with its numeric version")
		}
		out[v] = name
	}
	return out
}()

func sortedVersions() []int {
	versions := make([]int, 0, len(migrations))
	for v := range migrations {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	return versions
}

func highestMigration() int {
	v := sortedVersions()
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

// schemaEpoch marks the obsdb_migrations rows of the squashed history
// (0001 holds the whole schema): a database whose table lacks the
// column was written by the history before it.
const schemaEpoch = "rebuild"

// ErrNewerSchema is returned by Open when the database's
// obsdb_migrations is ahead of this binary's highest migration: a
// database written by a newer weftbase fails loudly instead of silently
// ignoring tables it does not know.
var ErrNewerSchema = errors.New("clickhouse: database schema is newer than this obsdb")

// ErrPredatesRebuild is returned by Open for a database written before
// the schema was squashed (an obsdb_migrations without the epoch
// column): there is no upgrade path from it.
var ErrPredatesRebuild = errors.New("clickhouse: this database predates the rebuild; start a fresh data dir")

// migrate creates obsdb_migrations when missing, refuses a schema ahead
// of this binary or from before the rebuild, and applies pending
// migrations in order. ClickHouse DDL is idempotent by construction
// here (every statement is CREATE ... IF NOT EXISTS), so two racing
// Opens converge without the re-read dance the SQLite runner needs.
func migrate(ctx context.Context, conn ch.Conn) error {
	var tables, epochs uint64
	if err := conn.QueryRow(ctx, `SELECT count() FROM system.tables
		WHERE database = currentDatabase() AND name = 'obsdb_migrations'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		if err := conn.QueryRow(ctx, `SELECT count() FROM system.columns
			WHERE database = currentDatabase() AND table = 'obsdb_migrations' AND name = 'epoch'`).Scan(&epochs); err != nil {
			return err
		}
		if epochs == 0 {
			return ErrPredatesRebuild
		}
	}
	if err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS obsdb_migrations (
		version UInt32,
		name String,
		epoch String,
		applied_at DateTime DEFAULT now()
	) ENGINE = MergeTree ORDER BY version`); err != nil {
		return err
	}
	var current *uint32
	if err := conn.QueryRow(ctx, `SELECT max(version) FROM obsdb_migrations`).Scan(&current); err != nil {
		return err
	}
	if current != nil && int(*current) > highestMigration() {
		return fmt.Errorf("%w: database has migration %d, this binary knows up to %d",
			ErrNewerSchema, *current, highestMigration())
	}
	for _, v := range sortedVersions() {
		if current != nil && int(*current) >= v {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + migrations[v])
		if err != nil {
			return err
		}
		for _, stmt := range splitStatements(string(body)) {
			if err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("clickhouse: migration %s: %w", migrations[v], err)
			}
		}
		if err := conn.Exec(ctx, `INSERT INTO obsdb_migrations (version, name, epoch) VALUES (?, ?, ?)`,
			uint32(v), migrations[v], schemaEpoch); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements splits a migration body into single statements: the
// runner sends one query per Exec. Only whole-line `--` comments are
// stripped (the migration files carry no inline comment markers), then
// semicolons separate statements; the OTLP shapes contain no string
// literals with semicolons.
func splitStatements(body string) []string {
	var out []string
	var cur strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		for {
			idx := strings.Index(line, ";")
			if idx < 0 {
				cur.WriteString(line)
				cur.WriteString("\n")
				break
			}
			cur.WriteString(line[:idx])
			if stmt := strings.TrimSpace(cur.String()); stmt != "" {
				out = append(out, stmt)
			}
			cur.Reset()
			line = line[idx+1:]
		}
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}
