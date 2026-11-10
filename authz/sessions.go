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
//
// Sessions come in families. A link minted with a secret key (or
// printed at start) begins a family; a link a signed-in session mints
// (POST /v1/auth/links from the Console) belongs to that session's
// family, and so does the session it is exchanged for. A family shares
// one sign-in time, so MaxSessionAge counts from the family's first
// sign-in however many links it chains, and signing out of any of its
// sessions ends all of them and its unused links. The family is the
// rows' created_at (the family's sign-in time, exact to the
// nanosecond): a new family's time is made unique in its project and
// env when its link is minted.

// SessionTTL is how long a Console session lives without being used:
// every request it authenticates more than SessionRenewAfter after its
// last renewal pushes its end to SessionTTL from then (a sliding
// window), until MaxSessionAge after the family's sign-in, when it ends
// whatever its use.
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
	// Ref names the session in audit records ("ses_" and a prefix of
	// its id's hash); never the id.
	Ref string
}

// Sessions stores sign-in links and Console sessions.
type Sessions struct {
	db  store.Runner
	log *events.Log
	now func() time.Time
}

// Audit makes the sessions write their audited events (link created,
// session created and ended) to log.
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

// SessionRef is the audit name of the session id names: "ses_" and the
// first 16 hex digits of its hash.
func SessionRef(id string) string { return "ses_" + hex.EncodeToString(digest(id))[:16] }

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// pruneBefore is the cutoff of a prune at now: one second back,
// truncated to the second and written without a fraction, so a plain
// string comparison with the stored RFC 3339 times never deletes a row
// that has not ended (a fraction sorts before "Z").
func pruneBefore(now time.Time) string {
	return now.UTC().Add(-time.Second).Truncate(time.Second).Format(time.RFC3339)
}

// prune deletes the sessions and links that have ended, in tx.
func prune(tx store.Tx, now time.Time) error {
	cut := pruneBefore(now)
	if _, err := store.Exec(tx, `DELETE FROM console_sessions WHERE expires_at < ?`, cut); err != nil {
		return err
	}
	_, err := store.Exec(tx, `DELETE FROM console_links WHERE expires_at < ?`, cut)
	return err
}

// newFamily returns a sign-in time for a new family at now, unique in
// the project and env (moved back a nanosecond at a time on a clash).
func newFamily(tx store.Tx, project, env string, now time.Time) (string, error) {
	t := now.UTC()
	for {
		var n int
		born := stamp(t)
		if err := store.QueryRow(tx, `SELECT (SELECT COUNT(*) FROM console_sessions WHERE project_id = ? AND env = ? AND created_at = ?)
			+ (SELECT COUNT(*) FROM console_links WHERE project_id = ? AND env = ? AND created_at = ?)`,
			project, env, born, project, env, born).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return born, nil
		}
		t = t.Add(-time.Nanosecond)
	}
}

// CreateLink returns a one-time sign-in token for the scope's project
// and env, beginning a new family.
func (s *Sessions) CreateLink(ctx context.Context, sc tenant.Scope) (string, error) {
	tok, _, err := s.CreateLinkBy(ctx, sc, events.System, "")
	return tok, err
}

// ErrSessionEnded is a link asked for by a session that is no longer
// live.
var ErrSessionEnded = errors.New("authz: the Console session has ended")

// CreateLinkBy is CreateLink for a caller: it also returns when the
// link lapses and, when Audit was given a log, writes an audited
// console.link_created event naming who asked (never the token).
// fromSession, when not empty, is the session id of the signed-in
// caller: the link joins its family (its sign-in time, its cap, its
// sign-out), and the link lapses no later than the family's cap.
func (s *Sessions) CreateLinkBy(ctx context.Context, sc tenant.Scope, actor events.Actor, fromSession string) (string, time.Time, error) {
	if sc.IsZero() {
		return "", time.Time{}, tenant.ErrZeroScope
	}
	tok := token()
	now := s.now().UTC()
	exp := now.Add(LinkTTL)
	err := s.db.InTx(ctx, func(tx store.Tx) error {
		if err := prune(tx, now); err != nil {
			return err
		}
		var born string
		if fromSession != "" {
			var expires string
			if err := store.QueryRow(tx, `SELECT created_at, expires_at FROM console_sessions WHERE id_hash = ? AND project_id = ? AND env = ?`,
				digest(fromSession), sc.Project(), sc.Env()).Scan(&born, &expires); err != nil {
				if errors.Is(store.NotFound(err), store.ErrNotFound) {
					return ErrSessionEnded
				}
				return err
			}
			if t, _ := time.Parse(time.RFC3339Nano, expires); !now.Before(t) {
				return ErrSessionEnded
			}
			if b, _ := time.Parse(time.RFC3339Nano, born); exp.After(b.Add(MaxSessionAge)) {
				exp = b.Add(MaxSessionAge)
			}
		} else {
			var err error
			if born, err = newFamily(tx, sc.Project(), sc.Env(), now); err != nil {
				return err
			}
		}
		if _, err := store.Exec(tx, `INSERT INTO console_links (token_hash, project_id, env, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			digest(tok), sc.Project(), sc.Env(), born, stamp(exp)); err != nil {
			return err
		}
		if s.log == nil {
			return nil
		}
		_, err := s.log.Emit(tx, sc, events.Event{Type: events.ConsoleLinkCreated, ResourceKind: "console_link",
			ResourceID: "lnk_" + hex.EncodeToString(digest(tok))[:16], Actor: actor, Audited: true},
			map[string]any{"expires_at": exp, "from_session": fromSession != ""})
		return err
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// Renew slides a live session's end to SessionTTL from now when its
// last renewal is more than SessionRenewAfter old, never past
// MaxSessionAge after the family's sign-in. renewed says the end moved
// (the caller sets the cookie again); exp is the session's end either
// way.
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
		if _, err := store.Exec(tx, `UPDATE console_sessions SET expires_at = ? WHERE id_hash = ?`, stamp(next), digest(id)); err != nil {
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
// expiry. The session joins the link's family: its end is never past
// MaxSessionAge after the family's sign-in. Audited
// (console.session_created) when Audit was given a log.
func (s *Sessions) Exchange(ctx context.Context, link, operator string) (string, time.Time, error) {
	id := token()
	now := s.now().UTC()
	exp := now.Add(SessionTTL)
	err := s.db.InTx(ctx, func(tx store.Tx) error {
		var project, env, born, expires string
		err := store.QueryRow(tx, `SELECT project_id, env, created_at, expires_at FROM console_links WHERE token_hash = ? AND used_at IS NULL`, digest(link)).
			Scan(&project, &env, &born, &expires)
		if err != nil {
			if errors.Is(store.NotFound(err), store.ErrNotFound) {
				return ErrLinkUsed
			}
			return err
		}
		if t, _ := time.Parse(time.RFC3339Nano, expires); !now.Before(t) {
			return ErrLinkUsed
		}
		b, _ := time.Parse(time.RFC3339Nano, born)
		if limit := b.Add(MaxSessionAge); exp.After(limit) {
			exp = limit
		}
		if !now.Before(exp) {
			return ErrLinkUsed
		}
		if _, err := store.Exec(tx, `UPDATE console_links SET used_at = ? WHERE token_hash = ?`, stamp(now), digest(link)); err != nil {
			return err
		}
		if _, err := store.Exec(tx, `INSERT INTO console_sessions (id_hash, project_id, env, operator, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			digest(id), project, env, operator, born, stamp(exp)); err != nil {
			return err
		}
		if err := prune(tx, now); err != nil {
			return err
		}
		return s.audit(tx, project, env, events.ConsoleSessionCreated, SessionRef(id),
			events.Actor{Kind: string(Operator), ID: operator, Credential: SessionRef(id), Assurance: "session"},
			map[string]any{"expires_at": exp, "signed_in_at": b})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return id, exp, nil
}

// audit writes an audited console event in tx when Audit was given a
// log.
func (s *Sessions) audit(tx store.Tx, project, env, typ, ref string, actor events.Actor, data map[string]any) error {
	if s.log == nil {
		return nil
	}
	sc, err := tenant.NewScope(project, env, tenant.ProjectTenant)
	if err != nil {
		return err
	}
	_, err = s.log.Emit(tx, sc, events.Event{Type: typ, ResourceKind: "console_session", ResourceID: ref, Actor: actor, Audited: true}, data)
	return err
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
	out.Ref = SessionRef(id)
	return out, err
}

// End signs out: it ends the session id names and its whole family
// (every session and unused link that shares its sign-in), audited
// (console.session_ended, by actor). Ending a session that is gone is
// not an error. ended is how many sessions ended.
func (s *Sessions) End(ctx context.Context, id string, actor events.Actor) (ended int, err error) {
	now := s.now().UTC()
	err = s.db.InTx(ctx, func(tx store.Tx) error {
		var project, env, born string
		if err := store.QueryRow(tx, `SELECT project_id, env, created_at FROM console_sessions WHERE id_hash = ?`, digest(id)).
			Scan(&project, &env, &born); err != nil {
			if errors.Is(store.NotFound(err), store.ErrNotFound) {
				return nil
			}
			return err
		}
		res, err := store.Exec(tx, `DELETE FROM console_sessions WHERE project_id = ? AND env = ? AND created_at = ?`, project, env, born)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ended = int(n)
		if _, err := store.Exec(tx, `DELETE FROM console_links WHERE project_id = ? AND env = ? AND created_at = ? AND used_at IS NULL`, project, env, born); err != nil {
			return err
		}
		if err := prune(tx, now); err != nil {
			return err
		}
		return s.audit(tx, project, env, events.ConsoleSessionEnded, SessionRef(id), actor,
			map[string]any{"sessions_ended": ended, "reason": "sign_out"})
	})
	return ended, err
}

// EndAll ends every Console session of the scope's project and env and
// every unused sign-in link, audited (console.session_ended, by actor,
// reason end_all). ended is how many sessions ended.
func (s *Sessions) EndAll(ctx context.Context, sc tenant.Scope, actor events.Actor) (ended int, err error) {
	if sc.IsZero() {
		return 0, tenant.ErrZeroScope
	}
	err = s.db.InTx(ctx, func(tx store.Tx) error {
		res, err := store.Exec(tx, `DELETE FROM console_sessions WHERE project_id = ? AND env = ?`, sc.Project(), sc.Env())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		ended = int(n)
		if _, err := store.Exec(tx, `DELETE FROM console_links WHERE project_id = ? AND env = ? AND used_at IS NULL`, sc.Project(), sc.Env()); err != nil {
			return err
		}
		return s.audit(tx, sc.Project(), sc.Env(), events.ConsoleSessionEnded, "ses_all", actor,
			map[string]any{"sessions_ended": ended, "reason": "end_all"})
	})
	return ended, err
}
