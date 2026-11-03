// Package keys mints, verifies and revokes API keys.
//
// A key is wb_<kind>_<env>_<secret>: kind sk (secret, server-side,
// project-wide), rk (restricted), pk (publishable) or wk (worker); env
// test or live. Only the SHA-256 of the whole key is stored, with a
// short visible prefix for lists. Every mint and revoke writes an
// audited outbox event in the same transaction.
package keys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Kind is a key's kind.
type Kind string

const (
	Secret      Kind = "sk"
	Restricted  Kind = "rk"
	Publishable Kind = "pk"
	Worker      Kind = "wk"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case Secret, Restricted, Publishable, Worker:
		return true
	}
	return false
}

// Key is a stored key, without its secret.
type Key struct {
	ID         string     `json:"id"`
	ProjectID  string     `json:"project_id"`
	Env        string     `json:"env"`
	TenantID   string     `json:"tenant_id"`
	Kind       Kind       `json:"kind"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

// ErrInvalid is any key that does not verify: malformed, unknown or
// revoked. Callers never learn which.
var ErrInvalid = errors.New("keys: invalid key")

// Parse splits a key into its kind and env. ok is false for anything
// not shaped wb_<kind>_<env>_<secret>.
func Parse(key string) (kind Kind, env string, ok bool) {
	rest, found := strings.CutPrefix(key, "wb_")
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, "_", 3)
	if len(parts) != 3 || parts[2] == "" {
		return "", "", false
	}
	kind, env = Kind(parts[0]), parts[1]
	if !kind.Valid() || !tenant.ValidEnv(env) {
		return "", "", false
	}
	return kind, env, true
}

func hash(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// newSecret is a fresh key of kind and env: 160 random bits.
func newSecret(kind Kind, env string) string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return "wb_" + string(kind) + "_" + env + "_" + strings.ToLower(b32.EncodeToString(b))
}

// prefix is the part of a key a list shows: kind, env and four
// characters of the secret.
func prefix(key string) string {
	if i := strings.LastIndex(key, "_"); i >= 0 && len(key) > i+4 {
		return key[:i+5]
	}
	return key
}

// Service mints and verifies keys over the control store.
type Service struct {
	db  store.Runner
	log *events.Log
	now func() time.Time
}

// New returns the key service.
func New(db store.Runner, log *events.Log) *Service {
	return &Service{db: db, log: log, now: time.Now}
}

// Mint creates a key of kind in scope s (its project and env; a tenant
// other than ten_project binds the key to that tenant) and returns it
// with its secret, which is shown once and never stored.
func (s *Service) Mint(ctx context.Context, sc tenant.Scope, kind Kind, name string, actor events.Actor) (Key, string, error) {
	if !kind.Valid() {
		return Key{}, "", fmt.Errorf("keys: unknown kind %q", kind)
	}
	secret := newSecret(kind, sc.Env())
	k, err := s.insert(ctx, sc, kind, name, secret, actor)
	return k, secret, err
}

// Import registers an existing secret (the bootstrap key a dev server
// keeps in its data dir). Importing a key already stored returns it.
func (s *Service) Import(ctx context.Context, sc tenant.Scope, name, secret string, actor events.Actor) (Key, error) {
	kind, env, ok := Parse(secret)
	if !ok {
		return Key{}, ErrInvalid
	}
	if env != sc.Env() {
		return Key{}, fmt.Errorf("keys: a %s key cannot be imported into env %s", env, sc.Env())
	}
	if k, err := s.lookup(ctx, secret); err == nil {
		return k, nil
	}
	return s.insert(ctx, sc, kind, name, secret, actor)
}

func (s *Service) insert(ctx context.Context, sc tenant.Scope, kind Kind, name, secret string, actor events.Actor) (Key, error) {
	if sc.IsZero() {
		return Key{}, tenant.ErrZeroScope
	}
	k := Key{
		ID: "key_" + ulid.New(), ProjectID: sc.Project(), Env: sc.Env(), TenantID: sc.Tenant(),
		Kind: kind, Name: name, Prefix: prefix(secret), CreatedAt: s.now().UTC(),
	}
	err := s.db.InTx(ctx, func(tx store.Tx) error {
		if _, err := store.Exec(tx, `INSERT INTO api_keys
			(id, project_id, env, tenant_id, kind, name, prefix, secret_hash, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			k.ID, k.ProjectID, k.Env, k.TenantID, string(k.Kind), k.Name, k.Prefix, hash(secret),
			k.CreatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_, err := s.log.Emit(tx, sc, events.Event{
			Type: events.KeyMinted, ResourceKind: "api_key", ResourceID: k.ID, Actor: actor, Audited: true,
		}, map[string]any{"kind": k.Kind, "name": k.Name, "prefix": k.Prefix})
		return err
	})
	if err != nil {
		return Key{}, err
	}
	return k, nil
}

const selectKey = `SELECT id, project_id, env, tenant_id, kind, name, prefix, created_at, revoked_at, last_used_at FROM api_keys`

func scanKey(row interface{ Scan(...any) error }) (Key, error) {
	var k Key
	var kind, created string
	var revoked, used *string
	if err := row.Scan(&k.ID, &k.ProjectID, &k.Env, &k.TenantID, &kind, &k.Name, &k.Prefix, &created, &revoked, &used); err != nil {
		return Key{}, store.NotFound(err)
	}
	k.Kind = Kind(kind)
	k.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	if revoked != nil {
		t, _ := time.Parse(time.RFC3339Nano, *revoked)
		k.RevokedAt = &t
	}
	if used != nil {
		t, _ := time.Parse(time.RFC3339Nano, *used)
		k.LastUsedAt = &t
	}
	return k, nil
}

func (s *Service) lookup(ctx context.Context, secret string) (Key, error) {
	var k Key
	err := s.db.Read(ctx, func(tx store.Tx) error {
		var err error
		k, err = scanKey(store.QueryRow(tx, selectKey+` WHERE secret_hash = ?`, hash(secret)))
		return err
	})
	return k, err
}

// Verify returns the live key a secret names, or ErrInvalid. The
// lookup is by hash, so no secret is ever compared byte by byte.
func (s *Service) Verify(ctx context.Context, secret string) (Key, error) {
	kind, env, ok := Parse(secret)
	if !ok {
		return Key{}, ErrInvalid
	}
	k, err := s.lookup(ctx, secret)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Key{}, ErrInvalid
		}
		return Key{}, err
	}
	if k.RevokedAt != nil || k.Kind != kind || k.Env != env {
		return Key{}, ErrInvalid
	}
	return k, nil
}

// List lists the keys of a scope's project and env, newest first.
func (s *Service) List(ctx context.Context, sc tenant.Scope) ([]Key, error) {
	if sc.IsZero() {
		return nil, tenant.ErrZeroScope
	}
	var out []Key
	err := s.db.Read(ctx, func(tx store.Tx) error {
		rows, err := store.Query(tx, selectKey+` WHERE project_id = ? AND env = ? ORDER BY created_at DESC`, sc.Project(), sc.Env())
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			k, err := scanKey(rows)
			if err != nil {
				return err
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return out, err
}

// Revoke revokes a key of the scope's project and env. A key of another
// project or env is ErrNotFound.
func (s *Service) Revoke(ctx context.Context, sc tenant.Scope, id string, actor events.Actor) error {
	return s.db.InTx(ctx, func(tx store.Tx) error {
		res, err := store.Exec(tx, `UPDATE api_keys SET revoked_at = ? WHERE id = ? AND project_id = ? AND env = ? AND revoked_at IS NULL`,
			s.now().UTC().Format(time.RFC3339Nano), id, sc.Project(), sc.Env())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		_, err = s.log.Emit(tx, sc, events.Event{
			Type: events.KeyRevoked, ResourceKind: "api_key", ResourceID: id, Actor: actor, Audited: true,
		}, nil)
		return err
	})
}
