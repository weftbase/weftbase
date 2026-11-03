package clickhouse

// The schema version a fresh database reaches, and the obsdb_migrations
// rows it holds (one per embedded migration): the one place a new
// migration bumps — every test that pins them reads these.
const (
	WantSchemaVersion = 1
	WantSchemaThrough = "0001 the whole schema (the rebuild's squash)"
)

// WantMigrationRows is one obsdb_migrations row per embedded migration.
var WantMigrationRows = len(migrations)
