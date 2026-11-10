package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/weftbase/weftbase/store"
)

// 0005 backfills a data directory written before it: each run's chain
// (the first run of its park-and-resume chain), its session and turn
// from the metadata the ledger stamped, and the one spelling cancelled.
func TestMigration0005Backfills(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(OFF)")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := store.LoadMigrations(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	var upTo4 []store.Migration
	for _, m := range ms {
		if m.Version <= 4 {
			upTo4 = append(upTo4, m)
		}
	}
	if err := store.Migrate(ctx, raw, store.SQLite, upTo4); err != nil {
		t.Fatal(err)
	}
	insert := func(id, status, metadata, resumeOf string) {
		t.Helper()
		if _, err := raw.Exec(`INSERT INTO runs (id, project_id, env, tenant_id, agent, agent_version, resolved_via, status, input, metadata,
			resume_of, resumed_by, decisions, checkpoint, pending, command_id, worker, error, cancel_cause, cancel_requested,
			input_tokens, output_tokens, created_at, updated_at) VALUES (?, 'p', 'test', 't', 'a', 'agv_x', 'latest', ?, '{}', ?, ?, '', '', '', '', '', '', '', '', 0, 0, 0, '2026', '2026')`,
			id, status, metadata, resumeOf); err != nil {
			t.Fatal(err)
		}
	}
	insert("run_a", "parked", `{"weft.session.id":"s1","weft.turn":"2"}`, "")
	insert("run_b", "parked", `{"weft.session.id":"s1","weft.turn":"2"}`, "run_a")
	insert("run_c", "succeeded", `{"weft.session.id":"s1","weft.turn":"2"}`, "run_b")
	insert("run_d", "canceled", "null", "")
	insert("run_e", "succeeded", "{}", "run_gone") // its parked run was deleted
	_ = raw.Close()

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	want := map[string][4]string{
		"run_a": {"run_a", "s1", "2", "parked"},
		"run_b": {"run_a", "s1", "2", "parked"},
		"run_c": {"run_a", "s1", "2", "succeeded"},
		"run_d": {"run_d", "", "0", "cancelled"},
		"run_e": {"run_e", "", "0", "succeeded"},
	}
	for id, w := range want {
		var got [4]string
		if err := db.SQL().QueryRowContext(ctx, `SELECT chain_id, session_id, CAST(turn AS TEXT), status FROM runs WHERE id = ?`, id).
			Scan(&got[0], &got[1], &got[2], &got[3]); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("%s = %v, want %v", id, got, w)
		}
	}
}
