// Package authz turns a request's credential into a Principal and the
// tenant.Scope it acts in, and decides what a principal may do.
//
// Credentials, in order: an API key in `Authorization: Bearer wb_…`
// (keys verified by hash), then the Console's session cookie. A key in
// the URL is never read. Only this package (and the tenant packages)
// build a tenant.Scope: a handler cannot invent a tenant.
package authz

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// PrincipalKind says what authenticated.
type PrincipalKind string

const (
	// Key: an API key.
	Key PrincipalKind = "key"
	// Operator: a Console session.
	Operator PrincipalKind = "operator"
	// Local: no credential, admitted by an Open authenticator (the
	// embedded placement on loopback, tests).
	Local PrincipalKind = "local"
)

// Principal is who is asking.
type Principal struct {
	Kind PrincipalKind `json:"kind"`
	// ID is the key id, the operator's name, or "local".
	ID string `json:"id"`
	// KeyKind is the key's kind for a Key principal.
	KeyKind keys.Kind `json:"key_kind,omitempty"`
	// Credential names what it authenticated with, for audit: the key
	// id, or the session's ref (SessionRef: "ses_" and a prefix of its
	// id's hash, never the id).
	Credential string `json:"credential,omitempty"`
	// Assurance is how strongly the identity is known.
	Assurance string `json:"assurance"`
	// AllTenants: a project-level principal reads every tenant of its
	// project and env.
	AllTenants bool `json:"all_tenants"`
}

// Errors Authenticate returns. Both mean 401.
var (
	ErrNoCredential = errors.New("authz: no credential")
	ErrInvalid      = errors.New("authz: invalid credential")
)

// SessionCookie is the Console session cookie's name.
const SessionCookie = "wb_session"

// Authenticator verifies credentials.
type Authenticator struct {
	keys     *keys.Service
	sessions *Sessions
	open     bool
}

// Option configures an Authenticator.
type Option func(*Authenticator)

// Open admits a request with no credential as the Local principal in
// the default scope. The embedded placement uses it behind the host
// app's own guards; a served node never does.
func Open() Option { return func(a *Authenticator) { a.open = true } }

// NewAuthenticator returns an authenticator over the key service and
// the Console sessions.
func NewAuthenticator(k *keys.Service, s *Sessions, opts ...Option) *Authenticator {
	a := &Authenticator{keys: k, sessions: s}
	for _, o := range opts {
		o(a)
	}
	return a
}

// IsOpen reports whether the authenticator admits requests without a
// credential.
func (a *Authenticator) IsOpen() bool { return a.open }

// BearerToken reads the Authorization bearer; never the URL.
func BearerToken(r *http.Request) string {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(tok)
	}
	return ""
}

// Authenticate returns the request's principal and scope.
func (a *Authenticator) Authenticate(ctx context.Context, r *http.Request) (Principal, tenant.Scope, error) {
	if tok := BearerToken(r); tok != "" {
		return a.AuthenticateKey(ctx, tok)
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" && a.sessions != nil {
		s, err := a.sessions.Lookup(ctx, c.Value)
		if err != nil {
			return Principal{}, tenant.Scope{}, ErrInvalid
		}
		sc, err := tenant.NewScope(s.ProjectID, s.Env, tenant.ProjectTenant)
		if err != nil {
			return Principal{}, tenant.Scope{}, ErrInvalid
		}
		return Principal{Kind: Operator, ID: s.Operator, Credential: s.Ref, Assurance: "session", AllTenants: true}, sc, nil
	}
	if a.open {
		return Principal{Kind: Local, ID: "local", Assurance: "local", AllTenants: true}, tenant.Default(), nil
	}
	return Principal{}, tenant.Scope{}, ErrNoCredential
}

// AuthenticateKey verifies an API key.
func (a *Authenticator) AuthenticateKey(ctx context.Context, secret string) (Principal, tenant.Scope, error) {
	k, err := a.keys.Verify(ctx, secret)
	if err != nil {
		if errors.Is(err, keys.ErrInvalid) {
			return Principal{}, tenant.Scope{}, ErrInvalid
		}
		return Principal{}, tenant.Scope{}, err
	}
	sc, err := tenant.NewScope(k.ProjectID, k.Env, k.TenantID)
	if err != nil {
		return Principal{}, tenant.Scope{}, ErrInvalid
	}
	return Principal{
		Kind: Key, ID: k.ID, KeyKind: k.Kind, Credential: k.ID, Assurance: "key",
		AllTenants: k.TenantID == tenant.ProjectTenant,
	}, sc, nil
}

type ctxKey struct{}

type identity struct {
	p Principal
	s tenant.Scope
}

// With returns ctx carrying the principal and scope.
func With(ctx context.Context, p Principal, s tenant.Scope) context.Context {
	return context.WithValue(ctx, ctxKey{}, identity{p, s})
}

// From returns the principal and scope ctx carries; ok is false when
// the request was never authenticated.
func From(ctx context.Context) (Principal, tenant.Scope, bool) {
	id, ok := ctx.Value(ctxKey{}).(identity)
	return id.p, id.s, ok
}
