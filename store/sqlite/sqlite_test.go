package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/weftbase/weftbase/store"
)

func TestOpenMigratesAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sub", "control.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('events', 'event_cursors', 'api_keys', 'console_links', 'console_sessions')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 5 {
		t.Errorf("tables after migrate = %d, want 5", tables)
	}
	if db.Path() != path {
		t.Errorf("Path = %q", db.Path())
	}
	if err := db.Ping(ctx); err != nil {
		t.Errorf("Ping: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// A second open applies nothing: one schema row per migration.
	ms, err := store.LoadMigrations(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(ms) {
		t.Errorf("schema_migrations rows = %d (%v), want %d", n, err, len(ms))
	}
}

func TestOpenRefusesAnOldDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE runs (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	_, err = Open(ctx, path)
	if !errors.Is(err, store.ErrNewerSchema) || !strings.Contains(err.Error(), "predates the rebuild") {
		t.Errorf("Open on a pre-rebuild file = %v, want ErrNewerSchema naming it", err)
	}
}

func TestOpenRefusesANewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "newer.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (99, 'future', 'now')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(ctx, path); !errors.Is(err, store.ErrNewerSchema) {
		t.Errorf("Open on a newer schema = %v, want ErrNewerSchema", err)
	}
}

func TestAfterCommitRunsOnlyOnCommit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	var ran []string
	if err := db.InTx(ctx, func(tx store.Tx) error {
		tx.AfterCommit(func() { ran = append(ran, "committed") })
		_, err := store.Exec(tx, `INSERT INTO event_cursors (consumer, seq, updated_at) VALUES (?, ?, ?)`, "c1", 1, "t")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	err = db.InTx(ctx, func(tx store.Tx) error {
		tx.AfterCommit(func() { ran = append(ran, "rolled back") })
		if _, err := store.Exec(tx, `INSERT INTO event_cursors (consumer, seq, updated_at) VALUES (?, ?, ?)`, "c2", 1, "t"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InTx = %v, want fn's error", err)
	}
	if strings.Join(ran, ",") != "committed" {
		t.Errorf("AfterCommit ran %v, want only the committed one", ran)
	}
	// The rolled-back row is gone; the committed one is there.
	var n int
	if err := db.Read(ctx, func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM event_cursors`).Scan(&n)
	}); err != nil || n != 1 {
		t.Errorf("rows = %d (%v), want 1", n, err)
	}

	// A panic rolls back and propagates.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("InTx swallowed a panic")
			}
		}()
		_ = db.InTx(ctx, func(tx store.Tx) error {
			tx.AfterCommit(func() { ran = append(ran, "panicked") })
			_, _ = store.Exec(tx, `INSERT INTO event_cursors (consumer, seq, updated_at) VALUES (?, ?, ?)`, "c3", 1, "t")
			panic("in tx")
		})
	}()
	if err := db.Read(ctx, func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM event_cursors`).Scan(&n)
	}); err != nil || n != 1 || len(ran) != 1 {
		t.Errorf("after a panic: rows %d, AfterCommit %v", n, ran)
	}
}

func TestIsUnique(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	ins := func() error {
		return db.InTx(ctx, func(tx store.Tx) error {
			_, err := store.Exec(tx, `INSERT INTO event_cursors (consumer, seq, updated_at) VALUES ('c', 1, 't')`)
			return err
		})
	}
	if err := ins(); err != nil {
		t.Fatal(err)
	}
	if err := ins(); !IsUnique(err) {
		t.Errorf("second insert = %v, want a unique violation", err)
	}
}

func TestLoadMigrations(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0002_two.sql": {Data: []byte("SELECT 2")},
		"m/0001_one.sql": {Data: []byte("SELECT 1")},
		"m/README":       {Data: []byte("ignored")},
	}
	ms, err := store.LoadMigrations(fsys, "m")
	if err != nil || len(ms) != 2 || ms[0].Name != "one" || ms[1].Version != 2 {
		t.Errorf("LoadMigrations = %+v, %v", ms, err)
	}
	fsys["m/0004_gap.sql"] = &fstest.MapFile{Data: []byte("SELECT 4")}
	if _, err := store.LoadMigrations(fsys, "m"); err == nil {
		t.Error("a gap in the versions loaded")
	}
}

func TestRebind(t *testing.T) {
	if got := store.Rebind(store.Postgres, `SELECT '?' FROM t WHERE a = ? AND b = ?`); got != `SELECT '?' FROM t WHERE a = $1 AND b = $2` {
		t.Errorf("Rebind = %q", got)
	}
	if got := store.Rebind(store.SQLite, `a = ?`); got != `a = ?` {
		t.Errorf("Rebind sqlite = %q", got)
	}
}
