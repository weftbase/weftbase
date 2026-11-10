package authz

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
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
	if _, err := f.sessions.End(ctx, id, events.System); err != nil {
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

// A Console session slides with its use: renewed once an hour has
// passed since the last renewal, never past MaxSessionAge.
func TestConsoleSessionSlides(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := start
	f.sessions.now = func() time.Time { return now }
	link, exp, err := f.sessions.CreateLinkBy(ctx, tenant.Default(), events.System, "")
	if err != nil || !exp.Equal(start.Add(LinkTTL)) {
		t.Fatalf("CreateLinkBy: %v %v", exp, err)
	}
	id, first, err := f.sessions.Exchange(ctx, link, "operator")
	if err != nil {
		t.Fatal(err)
	}
	// Within the hour nothing moves.
	now = start.Add(30 * time.Minute)
	if exp, renewed, err := f.sessions.Renew(ctx, id); err != nil || renewed || !exp.Equal(first) {
		t.Errorf("renew after 30 min = %v %v %v, want the first end, not renewed", exp, renewed, err)
	}
	// Later it slides: 12 h from the use.
	now = start.Add(11 * time.Hour)
	exp, renewed, err := f.sessions.Renew(ctx, id)
	if err != nil || !renewed || !exp.Equal(now.Add(SessionTTL)) {
		t.Fatalf("renew after 11 h = %v %v %v", exp, renewed, err)
	}
	now = start.Add(20 * time.Hour) // past the first end, inside the renewed one
	if _, err := f.sessions.Lookup(ctx, id); err != nil {
		t.Errorf("the renewed session at 20 h: %v", err)
	}
	// A session left alone for 12 h is over, and is not revived.
	now = now.Add(SessionTTL + time.Hour)
	if _, renewed, _ := f.sessions.Renew(ctx, id); renewed {
		t.Error("an expired session was renewed")
	}
	if _, err := f.sessions.Lookup(ctx, id); err == nil {
		t.Error("an expired session is live")
	}
	// The cap: no end past MaxSessionAge after the sign-in.
	now = start
	link, _, _ = f.sessions.CreateLinkBy(ctx, tenant.Default(), events.System, "")
	id, _, _ = f.sessions.Exchange(ctx, link, "operator")
	for now.Before(start.Add(MaxSessionAge - time.Hour)) {
		now = now.Add(6 * time.Hour)
		exp, _, _ = f.sessions.Renew(ctx, id)
	}
	if exp.After(start.Add(MaxSessionAge)) {
		t.Errorf("the session's end %v is past the cap %v", exp, start.Add(MaxSessionAge))
	}
}

// TestSessionFamily: a link a session mints, and the session it is
// traded for, belong to that session's family. They keep the family's
// sign-in time, so the 30-day cap is not reset by chaining links, and
// signing out of any of them ends the whole family (its unused links
// too) and nothing else. Sign-in and sign-out are audited with the
// session's ref, never its id; ended sessions and links are pruned.
func TestSessionFamily(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.sessions.Audit(f.log)
	start := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	now := start
	f.sessions.now = func() time.Time { return now }

	link, _ := f.sessions.CreateLink(ctx, tenant.Default())
	parent, _, err := f.sessions.Exchange(ctx, link, "operator")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := f.sessions.CreateLink(ctx, tenant.Default()) // another family, same instant
	stranger, _, err := f.sessions.Exchange(ctx, other, "operator")
	if err != nil {
		t.Fatal(err)
	}

	// 29 days on (the parent kept alive by use), the parent mints a link.
	for now.Before(start.Add(29 * 24 * time.Hour)) {
		now = now.Add(6 * time.Hour)
		if _, _, err := f.sessions.Renew(ctx, parent); err != nil {
			t.Fatal(err)
		}
	}
	chained, lexp, err := f.sessions.CreateLinkBy(ctx, tenant.Default(), events.System, parent)
	if err != nil {
		t.Fatal(err)
	}
	if lexp.After(start.Add(MaxSessionAge)) {
		t.Errorf("a chained link lapses at %v, past the family's cap", lexp)
	}
	child, cexp, err := f.sessions.Exchange(ctx, chained, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if !cexp.Equal(now.Add(SessionTTL)) {
		t.Errorf("child ends %v, want %v", cexp, now.Add(SessionTTL))
	}
	// Near the cap the child cannot outlive it: renewing stops there.
	var exp time.Time
	for now.Before(start.Add(MaxSessionAge - time.Hour)) {
		now = now.Add(time.Hour)
		if exp, _, err = f.sessions.Renew(ctx, child); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.sessions.Renew(ctx, parent); err != nil {
			t.Fatal(err)
		}
	}
	if !exp.Equal(start.Add(MaxSessionAge)) {
		t.Errorf("the child's end %v, want the family's cap %v (a chain must not reset it)", exp, start.Add(MaxSessionAge))
	}
	pending, _, err := f.sessions.CreateLinkBy(ctx, tenant.Default(), events.System, child)
	if err != nil {
		t.Fatal(err)
	}

	// Signing out of the parent ends the child and the unused link, not
	// the stranger.
	if n, err := f.sessions.End(ctx, parent, events.Actor{Kind: "operator", ID: "operator", Credential: SessionRef(parent)}); err != nil || n != 2 {
		t.Fatalf("End = %d, %v; want 2 sessions", n, err)
	}
	if _, err := f.sessions.Lookup(ctx, child); err == nil {
		t.Error("the child outlived its family's sign-out")
	}
	if _, _, err := f.sessions.Exchange(ctx, pending, "operator"); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("a link of the ended family = %v, want ErrLinkUsed", err)
	}
	if _, _, err := f.sessions.CreateLinkBy(ctx, tenant.Default(), events.System, parent); !errors.Is(err, ErrSessionEnded) {
		t.Errorf("a link from an ended session = %v, want ErrSessionEnded", err)
	}

	// Audit: created and ended, by ref.
	evs, err := f.log.List(ctx, events.ForScope(tenant.Default(), true), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var created, ended int
	for _, e := range evs {
		switch e.Type {
		case events.ConsoleSessionCreated:
			created++
			if !e.Audited || e.ResourceID == "" || e.Actor.Credential != e.ResourceID {
				t.Errorf("created event = %+v", e)
			}
		case events.ConsoleSessionEnded:
			ended++
			if e.ResourceID != SessionRef(parent) {
				t.Errorf("ended event = %+v", e)
			}
		}
		for _, id := range []string{parent, child, stranger} {
			if strings.Contains(string(e.Data), id) || strings.Contains(e.ResourceID, id) || e.Actor.Credential == id {
				t.Errorf("an event carries a session id: %+v", e)
			}
		}
	}
	if created != 3 || ended != 1 {
		t.Errorf("events: %d created, %d ended; want 3 and 1", created, ended)
	}

	// Prune: ended rows go at the next write.
	now = start.Add(MaxSessionAge + time.Hour)
	if _, err := f.sessions.CreateLink(ctx, tenant.Default()); err != nil {
		t.Fatal(err)
	}
	var sessions, links int
	_ = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM console_sessions`).Scan(&sessions)
	_ = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM console_links`).Scan(&links)
	if sessions != 0 || links != 1 {
		t.Errorf("after prune: %d sessions, %d links; want 0 and 1 (the new link)", sessions, links)
	}
}

// TestEndAll: every session and unused link of the project and env ends.
func TestEndAll(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	var ids []string
	for range 3 {
		link, _ := f.sessions.CreateLink(ctx, tenant.Default())
		id, _, err := f.sessions.Exchange(ctx, link, "operator")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	unused, _ := f.sessions.CreateLink(ctx, tenant.Default())
	if n, err := f.sessions.EndAll(ctx, tenant.Default(), events.System); err != nil || n != 3 {
		t.Fatalf("EndAll = %d, %v", n, err)
	}
	for _, id := range ids {
		if _, err := f.sessions.Lookup(ctx, id); err == nil {
			t.Error("a session outlived EndAll")
		}
	}
	if _, _, err := f.sessions.Exchange(ctx, unused, "operator"); !errors.Is(err, ErrLinkUsed) {
		t.Errorf("an unused link after EndAll = %v", err)
	}
}
