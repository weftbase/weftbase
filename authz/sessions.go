package authz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Console sign-in: a one-time link token (printed by `weftbase dev`)
// is exchanged once for a session id the browser keeps in an HttpOnly
// cookie. Only hashes of both are stored.

// SessionTTL is how long a Console session lives.
const SessionTTL = 12 * time.Hour

// LinkTTL is how long a sign-in link stays valid.
const LinkTTL = 15 * time.Minute

// Session is a Console session.
type Session struct {
	ProjectID string
	Env       string
	Operator  string
	ExpiresAt time.Time
}

// Sessions stores sign-in links and Console sessions.
type Sessions struct {
	db  store.Runner
	now func() time.Time
}

// NewSessions returns the session store.
func NewSessions(db store.Runner) *Sessions { return &Sessions{db: db, now: time.Now} }

func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func digest(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// CreateLink returns a one-time sign-in token for the scope's project
// and env.
func (s *Sessions) CreateLink(ctx context.Context, sc tenant.Scope) (string, error) {
	if sc.IsZero() {
		return "", tenant.ErrZeroScope
	}
	tok := token()
	now := s.now().UTC()
	err := s.db.InTx(ctx, func(tx store.Tx) error {
		_, err := store.Exec(tx, `INSERT INTO console_links (token_hash, project_id, env, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			digest(tok), sc.Project(), sc.Env(), now.Format(time.RFC3339Nano), now.Add(LinkTTL).Format(time.RFC3339Nano))
		return err
	})
	return tok, err
}

// ErrLinkUsed is a sign-in token that is unknown, used or expired.
var ErrLinkUsed = errors.New("authz: sign-in link unknown, used or expired")

// Exchange spends a sign-in token and returns a new session id and its
// expiry.
func (s *Sessions) Exchange(ctx context.Context, link, operator string) (string, time.Time, error) {
	id := token()
	now := s.now().UTC()
	exp := now.Add(SessionTTL)
	err := s.db.InTx(ctx, func(tx store.Tx) error {
		var project, env, expires string
		err := store.QueryRow(tx, `SELECT project_id, env, expires_at FROM console_links WHERE token_hash = ? AND used_at IS NULL`, digest(link)).
			Scan(&project, &env, &expires)
		if err != nil {
			if errors.Is(store.NotFound(err), store.ErrNotFound) {
				return ErrLinkUsed
			}
			return err
		}
		if t, _ := time.Parse(time.RFC3339Nano, expires); !now.Before(t) {
			return ErrLinkUsed
		}
		if _, err := store.Exec(tx, `UPDATE console_links SET used_at = ? WHERE token_hash = ?`, now.Format(time.RFC3339Nano), digest(link)); err != nil {
			return err
		}
		_, err = store.Exec(tx, `INSERT INTO console_sessions (id_hash, project_id, env, operator, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			digest(id), project, env, operator, now.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano))
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return id, exp, nil
}

// Lookup returns the live session an id names.
func (s *Sessions) Lookup(ctx context.Context, id string) (Session, error) {
	var out Session
	err := s.db.Read(ctx, func(tx store.Tx) error {
		var expires string
		if err := store.QueryRow(tx, `SELECT project_id, env, operator, expires_at FROM console_sessions WHERE id_hash = ?`, digest(id)).
			Scan(&out.ProjectID, &out.Env, &out.Operator, &expires); err != nil {
			return store.NotFound(err)
		}
		out.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
		if !s.now().Before(out.ExpiresAt) {
			return store.ErrNotFound
		}
		return nil
	})
	return out, err
}

// End deletes a session (sign-out).
func (s *Sessions) End(ctx context.Context, id string) error {
	return s.db.InTx(ctx, func(tx store.Tx) error {
		_, err := store.Exec(tx, `DELETE FROM console_sessions WHERE id_hash = ?`, digest(id))
		return err
	})
}
