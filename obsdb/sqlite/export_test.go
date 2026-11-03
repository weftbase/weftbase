package sqlite

// The schema version a fresh file reaches: the one place a new migration
// bumps — every test that pins the version reads it.
const (
	WantSchemaVersion = 1
	WantSchemaThrough = "0001 the whole schema (the rebuild's squash)"
)
