package authz

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

type fixture struct {
	db       *sqlite.DB
	log      *events.Log
	keys     *keys.Service
	sessions *Sessions
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := events.New(db)
	return fixture{db: db, log: log, keys: keys.New(db, log), sessions: NewSessions(db)}
}

func TestKeyAuthentication(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	a := NewAuthenticator(f.keys, f.sessions)
	k, secret, err := f.keys.Mint(ctx, tenant.Default(), keys.Secret, "ci", events.System)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/runs", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	p, sc, err := a.Authenticate(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != Key || p.ID != k.ID || p.KeyKind != keys.Secret || !p.AllTenants || sc != tenant.Default() {
		t.Errorf("principal %+v scope %v", p, sc)
	}
	if !Can(p, RunsDelete).Allow {
		t.Error("a secret key may not delete runs")
	}

	// A wrong, malformed or revoked key is ErrInvalid; no credential is
	// ErrNoCredential.
	for _, bad := range []string{"wb_sk_test_nope", "not-a-key", secret + "x"} {
		req.Header.Set("Authorization", "Bearer "+bad)
		if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrInvalid) {
			t.Errorf("Authenticate(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if err := f.keys.Revoke(ctx, tenant.Default(), k.ID, events.System); err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrInvalid) {
		t.Errorf("revoked key = %v, want ErrInvalid", err)
	}
	req.Header.Del("Authorization")
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrNoCredential) {
		t.Errorf("no credential = %v, want ErrNoCredential", err)
	}

	// A key in the URL is never read.
	req = httptest.NewRequest("GET", "/v1/runs?token="+secret, nil)
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrNoCredential) {
		t.Errorf("a key in the URL = %v, want ErrNoCredential", err)
	}

	// Mint and revoke were audited outbox events.
	evs, err := f.log.List(ctx, events.ForScope(tenant.Default(), true), 0, 10)
	if err != nil || len(evs) != 2 || evs[0].Type != events.KeyMinted || evs[1].Type != events.KeyRevoked || !evs[0].Audited {
		t.Errorf("events = %+v, %v", evs, err)
	}
}

func TestCanByKeyKind(t *testing.T) {
	for _, c := range []struct {
		p     Principal
		a     Action
		allow bool
	}{
		{Principal{Kind: Key, KeyKind: keys.Worker}, IngestWrite, true},
		{Principal{Kind: Key, KeyKind: keys.Worker}, RunsRead, false},
		{Principal{Kind: Key, KeyKind: keys.Publishable}, MetaRead, true},
		{Principal{Kind: Key, KeyKind: keys.Publishable}, RunsRead, false},
		{Principal{Kind: Key, KeyKind: keys.Restricted}, RunsRead, false},
		{Principal{Kind: Key, KeyKind: keys.Restricted}, AuthSession, true},
		{Principal{Kind: Operator}, RunsDelete, true},
		{Principal{Kind: Local}, IngestWrite, true},
		{Principal{}, Public, true},
		{Principal{}, MetaRead, false},
	} {
		if got := Can(c.p, c.a); got.Allow != c.allow || (!got.Allow && got.Reason == "") {
			t.Errorf("Can(%+v, %s) = %+v, want allow %v (a refusal says why)", c.p, c.a, got, c.allow)
		}
	}
}

func TestConsoleSessions(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f.sessions.now = func() time.Time { return now }
	a := NewAuthenticator(f.keys, f.sessions)

	link, err := f.sessions.CreateLink(ctx, tenant.Default())
	if err != nil {
		t.Fatal(err)
	}
	id, exp, err := f.sessions.Exchange(ctx, link, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(SessionTTL)) {
		t.Errorf("expiry %v", exp)
	}
	// The link is single-use.
	if _, _, err := f.sessions.Exchange(ctx, link, "operator"); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("second exchange = %v, want ErrLinkUsed", err)
	}
	if _, _, err := f.sessions.Exchange(ctx, "unknown", "operator"); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("unknown link = %v, want ErrLinkUsed", err)
	}

	req := httptest.NewRequest("GET", "/v1/runs", nil)
	req.Header.Set("Cookie", SessionCookie+"="+id)
	p, sc, err := a.Authenticate(ctx, req)
	if err != nil || p.Kind != Operator || p.ID != "operator" || sc != tenant.Default() {
		t.Fatalf("cookie session = %+v %v %v", p, sc, err)
	}
	// A bearer wins over the cookie.
	req.Header.Set("Authorization", "Bearer wb_sk_test_nope")
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad bearer beside a good cookie = %v, want ErrInvalid", err)
	}
	req.Header.Del("Authorization")

	// The session expires.
	now = now.Add(SessionTTL)
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrInvalid) {
		t.Errorf("expired session = %v, want ErrInvalid", err)
	}
	now = now.Add(-time.Minute)
	if _, _, err := a.Authenticate(ctx, req); err != nil {
		t.Errorf("live session = %v", err)
	}
	// Sign-out ends it.
	if err := f.sessions.End(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrInvalid) {
		t.Errorf("ended session = %v, want ErrInvalid", err)
	}

	// A link expires unused.
	link, err = f.sessions.CreateLink(ctx, tenant.Default())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(LinkTTL)
	if _, _, err := f.sessions.Exchange(ctx, link, "operator"); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("expired link = %v, want ErrLinkUsed", err)
	}
}

func TestOpenAdmitsLocal(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	req := httptest.NewRequest("GET", "/v1/runs", nil)
	if _, _, err := NewAuthenticator(f.keys, f.sessions).Authenticate(ctx, req); !errors.Is(err, ErrNoCredential) {
		t.Errorf("closed authenticator without a credential = %v", err)
	}
	a := NewAuthenticator(f.keys, f.sessions, Open())
	if !a.IsOpen() {
		t.Error("IsOpen false")
	}
	p, sc, err := a.Authenticate(ctx, req)
	if err != nil || p.Kind != Local || sc != tenant.Default() || !p.AllTenants {
		t.Errorf("open = %+v %v %v", p, sc, err)
	}
	// Open never downgrades a bad credential to Local.
	req.Header.Set("Authorization", "Bearer wb_sk_test_nope")
	if _, _, err := a.Authenticate(ctx, req); !errors.Is(err, ErrInvalid) {
		t.Errorf("open with a bad key = %v, want ErrInvalid", err)
	}
	ctx2 := With(ctx, p, sc)
	if p2, sc2, ok := From(ctx2); !ok || p2 != p || sc2 != sc {
		t.Errorf("From(With) = %+v %v %v", p2, sc2, ok)
	}
	if _, _, ok := From(ctx); ok {
		t.Error("From on a bare context reported a principal")
	}
}

func TestImportBootstrapKey(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const secret = "wb_sk_test_abcdefghijklmnopqrstuvwxyz234567"
	k1, err := f.keys.Import(ctx, tenant.Default(), "bootstrap", secret, events.System)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := f.keys.Import(ctx, tenant.Default(), "bootstrap", secret, events.System)
	if err != nil || k2.ID != k1.ID {
		t.Errorf("re-import = %+v %v, want the same key", k2, err)
	}
	if _, err := f.keys.Import(ctx, tenant.Default(), "x", "wb_sk_live_abcdefghijklmnop", events.System); err == nil {
		t.Error("a live key imported into the test env")
	}
	p, _, err := NewAuthenticator(f.keys, f.sessions).AuthenticateKey(ctx, secret)
	if err != nil || p.ID != k1.ID {
		t.Errorf("bootstrap key = %+v %v", p, err)
	}
	ks, err := f.keys.List(ctx, tenant.Default())
	if err != nil || len(ks) != 1 || ks[0].Prefix != "wb_sk_test_abcd" {
		t.Errorf("List = %+v %v", ks, err)
	}
}
