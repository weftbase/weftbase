package keys_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

func service(t *testing.T) (*keys.Service, *events.Log) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l := events.New(db)
	return keys.New(db, l), l
}

func TestMintVerifyRevoke(t *testing.T) {
	ctx := context.Background()
	svc, l := service(t)
	k, secret, err := svc.Mint(ctx, tenant.Default(), keys.Secret, "dev", events.System)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "wb_sk_test_") || !strings.HasPrefix(secret, k.Prefix) {
		t.Fatalf("secret %q prefix %q", secret, k.Prefix)
	}
	got, err := svc.Verify(ctx, secret)
	if err != nil || got.ID != k.ID {
		t.Fatalf("verify: %+v %v", got, err)
	}
	if _, err := svc.Verify(ctx, secret+"x"); !errors.Is(err, keys.ErrInvalid) {
		t.Fatalf("a wrong secret verified: %v", err)
	}
	if err := svc.Revoke(ctx, tenant.Default(), k.ID, events.System); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(ctx, secret); !errors.Is(err, keys.ErrInvalid) {
		t.Fatalf("a revoked key verified: %v", err)
	}
	evs, _ := l.List(ctx, events.ForScope(tenant.Default(), false), 0, 10)
	if len(evs) != 2 || evs[0].Type != events.KeyMinted || evs[1].Type != events.KeyRevoked || !evs[0].Audited {
		t.Fatalf("events %+v", evs)
	}
}

func TestRevokeOtherEnvIsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := service(t)
	k, _, _ := svc.Mint(ctx, tenant.Default(), keys.Secret, "dev", events.System)
	live, _ := tenant.NewScope(tenant.DefaultProject, "live", tenant.ProjectTenant)
	if err := svc.Revoke(ctx, live, k.ID, events.System); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestImportIsIdempotent(t *testing.T) {
	ctx := context.Background()
	svc, _ := service(t)
	const secret = "wb_sk_test_abcdefghijklmnopqrstuvwxyz234567"
	a, err := svc.Import(ctx, tenant.Default(), "bootstrap", secret, events.System)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Import(ctx, tenant.Default(), "bootstrap", secret, events.System)
	if err != nil || a.ID != b.ID {
		t.Fatalf("second import %+v %v", b, err)
	}
	if _, _, ok := keys.Parse("wb_xx_test_abc"); ok {
		t.Fatal("parsed an unknown kind")
	}
}
