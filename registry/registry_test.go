package registry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/store/sqlite"
	"github.com/weftbase/weftbase/tenant"
)

func open(t *testing.T) (*registry.Registry, *events.Log, *sqlite.DB) {
	t.Helper()
	db, err := sqlite.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := events.New(db)
	return registry.New(db, log), log, db
}

func spec(name, body string) registry.AgentSpec {
	return registry.AgentSpec{Name: name, Manifest: []byte(body), ManifestHash: registry.ManifestHash([]byte(body))}
}

func TestRegisterVersionsAndLabels(t *testing.T) {
	ctx := context.Background()
	reg, log, db := open(t)
	s := tenant.Default()

	v1, err := reg.RegisterIn(ctx, s, "sha256:aaa", "embedded", []registry.AgentSpec{spec("support", `{"a":1}`), spec("billing", `{"b":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(v1) != 2 || v1[0].Number != 1 || v1[1].Number != 1 || v1[0].ContentHash != registry.ContentHash("sha256:aaa", v1[0].ManifestHash) {
		t.Fatalf("first registration = %+v", v1)
	}
	// The same registration again creates nothing.
	again, err := reg.RegisterIn(ctx, s, "sha256:aaa", "embedded", []registry.AgentSpec{spec("support", `{"a":1}`)})
	if err != nil || again[0].ID != v1[0].ID {
		t.Fatalf("re-registration = %+v, %v", again, err)
	}
	// A new build of the same manifest is a new version, numbered 2.
	v2, err := reg.RegisterIn(ctx, s, "sha256:bbb", "embedded", []registry.AgentSpec{spec("support", `{"a":1}`)})
	if err != nil || v2[0].Number != 2 || v2[0].ID == v1[0].ID {
		t.Fatalf("new build = %+v, %v", v2, err)
	}
	// A bad hash and a bad digest are refused.
	bad := spec("support", `{"a":2}`)
	bad.ManifestHash = "nope"
	if _, err := reg.RegisterIn(ctx, s, "sha256:ccc", "", []registry.AgentSpec{bad}); !errors.Is(err, registry.ErrHashMismatch) {
		t.Errorf("bad hash = %v", err)
	}
	if _, err := reg.RegisterIn(ctx, s, "md5:x", "", []registry.AgentSpec{spec("x", "{}")}); !errors.Is(err, registry.ErrInvalid) {
		t.Errorf("bad digest = %v", err)
	}

	// Unlabelled: "" resolves the newest version.
	resolve := func(sel string) (registry.Version, string, error) {
		var v registry.Version
		var via string
		err := db.InTx(ctx, func(tx store.Tx) error {
			var err error
			v, via, err = reg.Resolve(tx, s, "support", sel)
			return err
		})
		return v, via, err
	}
	if v, via, err := resolve(""); err != nil || v.ID != v2[0].ID || via != registry.ViaLatest {
		t.Errorf("resolve latest = %s %s %v", v.ID, via, err)
	}
	// Promote prod to v1: "" and label:prod resolve it; the event is audited.
	p := registry.PromoteRequest{Agent: "support", Label: "prod", Version: v1[0].ID, Reason: "ship", Actor: events.Actor{Kind: "key", ID: "k1"}}
	if err := reg.PromoteIn(ctx, s, p); err != nil {
		t.Fatal(err)
	}
	if v, via, err := resolve(""); err != nil || v.ID != v1[0].ID || via != "label:prod" {
		t.Errorf("resolve prod = %s %s %v", v.ID, via, err)
	}
	if v, via, err := resolve(v2[0].ID); err != nil || v.ID != v2[0].ID || via != registry.ViaPin {
		t.Errorf("resolve pin = %s %s %v", v.ID, via, err)
	}
	if _, _, err := resolve("label:staging"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown label = %v", err)
	}
	if _, _, err := resolve(v1[1].ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another agent's version = %v", err)
	}
	// A stale expected is a conflict; the right one moves it.
	p.Version, p.Expected = v2[0].ID, ""
	if err := reg.PromoteIn(ctx, s, p); !errors.Is(err, store.ErrConflict) {
		t.Errorf("stale expected = %v", err)
	}
	p.Expected = v1[0].ID
	if err := reg.PromoteIn(ctx, s, p); err != nil {
		t.Fatal(err)
	}
	evs, err := log.List(ctx, events.Selector{Project: s.Project(), Env: s.Env(), Tenant: s.Tenant()}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	var moved int
	for _, e := range evs {
		types = append(types, e.Type)
		if e.Type == events.LabelMoved {
			moved++
			if !e.Audited || e.Actor.ID != "k1" {
				t.Errorf("label.moved = %+v", e)
			}
		}
	}
	if moved != 2 {
		t.Errorf("events = %v, want two label.moved", types)
	}

	agents, err := reg.Agents(ctx, s)
	if err != nil || len(agents) != 2 || agents[1].Name != "support" || agents[1].Labels["prod"] != v2[0].ID || agents[1].Versions != 2 {
		t.Errorf("Agents = %+v, %v", agents, err)
	}
	vs, err := reg.Versions(ctx, s, "support")
	if err != nil || len(vs) != 2 || vs[0].Number != 2 || len(vs[0].Labels) != 1 {
		t.Errorf("Versions = %+v, %v", vs, err)
	}
	if _, err := reg.Versions(ctx, s, "nobody"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Versions of an unknown agent = %v", err)
	}
	m, err := reg.Manifest(ctx, s, v1[0].ManifestHash)
	if err != nil || string(m.Body) != `{"a":1}` || m.Agent != "support" {
		t.Errorf("Manifest = %+v, %v", m, err)
	}
	// Another project's scope sees nothing.
	other, _ := tenant.NewScope("prj_other", "test", tenant.ProjectTenant)
	if as, err := reg.Agents(ctx, other); err != nil || len(as) != 0 {
		t.Errorf("other project's agents = %+v, %v", as, err)
	}
	if _, err := reg.Manifest(ctx, other, v1[0].ManifestHash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("other project's manifest = %v", err)
	}
	if _, err := reg.Agents(ctx, tenant.Scope{}); !errors.Is(err, tenant.ErrZeroScope) {
		t.Errorf("zero scope = %v", err)
	}
}
