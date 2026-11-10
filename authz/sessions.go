package authz

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
)

// Console sign-in: a one-time link token (printed by `weftbase dev`)
// is exchanged once for a session id the browser keeps in an HttpOnly
// cookie. Only hashes of both are stored.

// SessionTTL is how long a Console session lives without being used:
// every request it authenticates more than SessionRenewAfter after its
// last renewal pushes its end to SessionTTL from then (a sliding
// window), until MaxSessionAge after the sign-in, when it ends whatever
// its use.
const (
	SessionTTL        = 12 * time.Hour
	SessionRenewAfter = time.Hour
	MaxSessionAge     = 30 * 24 * time.Hour
)

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
	log *events.Log
	now func() time.Time
}

// Audit makes CreateLinkBy write its event to log.
func (s *Sessions) Audit(log *events.Log) { s.log = log }

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

// CreateLinkBy is CreateLink for a caller: it also returns when the
// link lapses and, when Audit was given a log, writes an audited
// console.link_created event naming who asked (never the token).
func (s *Sessions) CreateLinkBy(ctx context.Context, sc tenant.Scope, actor events.Actor) (string, time.Time, error) {
	if sc.IsZero() {
		return "", time.Time{}, tenant.ErrZeroScope
	}
	tok := token()
	now := s.now().UTC()
	exp := now.Add(LinkTTL)
	err := s.db.InTx(ctx, func(tx store.Tx) error {
		if _, err := store.Exec(tx, `INSERT INTO console_links (token_hash, project_id, env, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			digest(tok), sc.Project(), sc.Env(), now.Format(time.RFC3339Nano), exp.Format(time.RFC3339Nano)); err != nil {
			return err
		}
		if s.log == nil {
			return nil
		}
		_, err := s.log.Emit(tx, sc, events.Event{Type: events.ConsoleLinkCreated, ResourceKind: "console_link",
			ResourceID: "lnk_" + hex.EncodeToString(digest(tok))[:16], Actor: actor, Audited: true},
			map[string]any{"expires_at": exp})
		return err
	})
	return tok, exp, err
}

// Renew slides a live session's end to SessionTTL from now when its
// last renewal is more than SessionRenewAfter old, never past
// MaxSessionAge after the sign-in. renewed says the end moved (the
// caller sets the cookie again); exp is the session's end either way.
func (s *Sessions) Renew(ctx context.Context, id string) (exp time.Time, renewed bool, err error) {
	now := s.now().UTC()
	err = s.db.InTx(ctx, func(tx store.Tx) error {
		var created, expires string
		if err := store.QueryRow(tx, `SELECT created_at, expires_at FROM console_sessions WHERE id_hash = ?`, digest(id)).Scan(&created, &expires); err != nil {
			return store.NotFound(err)
		}
		exp, _ = time.Parse(time.RFC3339Nano, expires)
		born, _ := time.Parse(time.RFC3339Nano, created)
		next := now.Add(SessionTTL)
		if limit := born.Add(MaxSessionAge); next.After(limit) {
			next = limit
		}
		if !now.Before(exp) || next.Sub(exp) < SessionRenewAfter {
			return nil
		}
		if _, err := store.Exec(tx, `UPDATE console_sessions SET expires_at = ? WHERE id_hash = ?`, next.Format(time.RFC3339Nano), digest(id)); err != nil {
			return err
		}
		exp, renewed = next, true
		return nil
	})
	return exp, renewed, err
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
