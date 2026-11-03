package weftbase

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/worker/executor"
)

// The Phase 1 gate (plan §9 step 6): every run carries an
// AgentVersion; an embedded agent runs end to end through the
// Executor; a parked run survives a new build and resumes on its pin;
// a restart claims queued items again and lets leases expire.

const (
	digestA = "sha256:aaaa"
	digestB = "sha256:bbbb"
)

// startApp runs an app over dir until the test ends (or stop).
func startApp(t *testing.T, dir string, opts ...Option) (*App, *httptest.Server, func()) {
	t.Helper()
	app, err := New(append([]Option{DataDir(dir), BootstrapKey(bootstrap), Drain(50 * time.Millisecond)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	ts := httptest.NewServer(app.Handler())
	var once sync.Once
	stop := func() {
		once.Do(func() {
			ts.Close()
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Run: %v", err)
			}
			if err := app.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return app, ts, stop
}

func waitRun(t *testing.T, app *App, id string, want ...string) sessions.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		run, err := app.Ledger().Get(context.Background(), tenant.Default(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if run.Status == w {
				return run
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s (%s), want %v", id, run.Status, run.Error, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runEvents(t *testing.T, app *App, id string) []string {
	t.Helper()
	sel := events.ForScope(tenant.Default(), false)
	sel.ResourceKind, sel.ResourceID = "run", id
	evs, err := app.Events().List(context.Background(), sel, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func postJSON(t *testing.T, url string, v any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	code, body := call(t, "POST", url, bootstrap, "application/json", b)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return code, out
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	code, body := call(t, "GET", url, bootstrap, "", nil)
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
	return code, out
}

type orderIn struct {
	Order int `json:"order"`
}

// refunds is one agent definition: built twice from this function, it
// has the same manifest (and so, on one digest, the same version).
func refunds(model core.Model, refunded *atomic.Int32) *core.Agent {
	refund := core.Tool("refund", "Refund an order.", func(_ context.Context, in orderIn) (string, error) {
		refunded.Add(1)
		return fmt.Sprintf("refunded %d", in.Order), nil
	}, core.RequireApproval())
	return core.New(model, core.Name("refunds"), core.Instructions("Refund orders."), refund)
}

func TestEmbeddedRunEndToEnd(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), BuildDigest(digestA))
	agent := core.New(wefttest.Script(wefttest.Say("hello"), wefttest.Say("again")), core.Name("support"))
	if err := app.Agent(agent); err != nil {
		t.Fatal(err)
	}

	code, agents := getJSON(t, ts.URL+"/v1/agents")
	if code != 200 || len(agents["agents"].([]any)) != 1 {
		t.Fatalf("GET /v1/agents: %d %v", code, agents)
	}
	code, vs := getJSON(t, ts.URL+"/v1/agents/support/versions")
	versions, _ := vs["versions"].([]any)
	if code != 200 || len(versions) != 1 {
		t.Fatalf("GET versions: %d %v", code, vs)
	}
	v := versions[0].(map[string]any)
	if v["build_digest"] != digestA || !strings.HasPrefix(v["id"].(string), "agv_") || v["number"].(float64) != 1 {
		t.Errorf("version = %v", v)
	}
	if code, m := getJSON(t, ts.URL+"/v1/manifests/"+v["manifest_hash"].(string)); code != 200 || m["agent"] != "support" {
		t.Errorf("the registered manifest does not resolve: %d %v", code, m)
	}
	if code, _ := getJSON(t, ts.URL+"/v1/agents/nobody/versions"); code != 404 {
		t.Errorf("unknown agent's versions: %d", code)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "support"}); code != 400 {
		t.Errorf("a run without input: %d", code)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "nobody", "input": map[string]any{"prompt": "hi"}}); code != 404 {
		t.Errorf("a run of an unknown agent: %d", code)
	}

	code, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "support", "input": map[string]any{"prompt": "hi"}})
	if code != 202 || run["status"] != "queued" || run["agent_version"] != v["id"] || run["resolved_via"] != registry.ViaLatest {
		t.Fatalf("POST /v1/runs: %d %v", code, run)
	}
	id := run["id"].(string)
	done := waitRun(t, app, id, sessions.StatusSucceeded, sessions.StatusFailed)
	if done.Status != sessions.StatusSucceeded || done.Usage.OutputTokens != 5 || done.StartedAt == nil {
		t.Fatalf("run = %+v", done)
	}
	if got := strings.Join(runEvents(t, app, id), ","); got != "run.queued,run.started,run.finished" {
		t.Errorf("events = %s", got)
	}
	code, st := getJSON(t, ts.URL+"/v1/runs/"+id+"/status")
	if code != 200 || st["status"] != "succeeded" {
		t.Errorf("GET status: %d %v", code, st)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/runs/"+id+"/cancel", map[string]any{}); code != 409 {
		t.Errorf("cancel of a finished run: %d, want 409", code)
	}

	// Promote, then a run by label.
	code, p := postJSON(t, ts.URL+"/v1/agents/support/labels/prod", map[string]any{"version": v["id"], "reason": "ship"})
	if code != 200 || p["id"] != v["id"] {
		t.Fatalf("promote: %d %v", code, p)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/agents/support/labels/prod", map[string]any{"version": v["id"]}); code != 409 {
		t.Errorf("promote with a stale expected: %d, want 409", code)
	}
	code, run = postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "support", "version": "label:prod", "input": map[string]any{"prompt": "hi"}})
	if code != 202 || run["resolved_via"] != "label:prod" {
		t.Fatalf("run by label: %d %v", code, run)
	}
	waitRun(t, app, run["id"].(string), sessions.StatusSucceeded)

	// Every run carries an AgentVersion that exists.
	var bare int
	if err := app.control.Read(context.Background(), func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM runs r LEFT JOIN agent_versions v ON v.id = r.agent_version
			WHERE v.id IS NULL OR r.resolved_via = ''`).Scan(&bare)
	}); err != nil || bare != 0 {
		t.Errorf("runs without an AgentVersion: %d (%v)", bare, err)
	}
}

func TestCancelARunningRun(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), BuildDigest(digestA))
	started := make(chan struct{}, 1)
	wait := core.Tool("wait", "Wait.", func(ctx context.Context, _ struct{}) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	})
	agent := core.New(wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "wait"}), wefttest.Say("never")), core.Name("slow"), wait)
	if err := app.Agent(agent); err != nil {
		t.Fatal(err)
	}
	code, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "slow", "input": map[string]any{"prompt": "go"}})
	if code != 202 {
		t.Fatalf("admit: %d %v", code, run)
	}
	id := run["id"].(string)
	<-started
	waitRun(t, app, id, sessions.StatusRunning)
	code, c := postJSON(t, ts.URL+"/v1/runs/"+id+"/cancel", map[string]any{"reason": "stop"})
	if code != 202 || c["cancel_requested"] != true {
		t.Fatalf("cancel: %d %v", code, c)
	}
	done := waitRun(t, app, id, sessions.StatusCanceled, sessions.StatusFailed, sessions.StatusSucceeded)
	if done.Status != sessions.StatusCanceled || done.CancelCause != wire.CauseUser {
		t.Fatalf("after cancel = %+v", done)
	}
	if got := strings.Join(runEvents(t, app, id), ","); got != "run.queued,run.started,run.canceled" {
		t.Errorf("events = %s", got)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/runs/"+id+"/cancel", nil); code != 409 {
		t.Errorf("second cancel: %d, want 409", code)
	}
}

// TestParkedRunSurvivesANewBuild: a run parks on an approval under
// build A; the node restarts as build B (a new AgentVersion of the same
// agent, promoted to prod). The resume pins A's version: it waits while
// nothing serves A, runs once A's build is attached again, and new runs
// go to B.
func TestParkedRunSurvivesANewBuild(t *testing.T) {
	dir := t.TempDir()
	var refundedA, refundedB atomic.Int32
	script := func() core.Model {
		return wefttest.Script(wefttest.ToolCalls(wefttest.Call{Name: "refund", Args: `{"order":42}`}), wefttest.Say("refunded"))
	}

	app, ts, stop := startApp(t, dir, BuildDigest(digestA))
	if err := app.Agent(refunds(script(), &refundedA)); err != nil {
		t.Fatal(err)
	}
	_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "refund 42"}})
	parkedID := run["id"].(string)
	verA := run["agent_version"].(string)
	parked := waitRun(t, app, parkedID, sessions.StatusParked, sessions.StatusFailed)
	if parked.Status != sessions.StatusParked || len(parked.Pending) != 1 || parked.Pending[0].Tool != "refund" || refundedA.Load() != 0 {
		t.Fatalf("parked = %+v (refunded %d)", parked, refundedA.Load())
	}
	stop()

	// The new build: same agent definition, another digest.
	app, ts, _ = startApp(t, dir, BuildDigest(digestB))
	modelB := script()
	if err := app.Agent(refunds(modelB, &refundedB)); err != nil {
		t.Fatal(err)
	}
	code, vs := getJSON(t, ts.URL+"/v1/agents/refunds/versions")
	versions, _ := vs["versions"].([]any)
	if code != 200 || len(versions) != 2 {
		t.Fatalf("versions after the new build: %d %v", code, vs)
	}
	verB := versions[0].(map[string]any)["id"].(string)
	if verB == verA {
		t.Fatal("the new build did not make a new version")
	}
	if code, _ := postJSON(t, ts.URL+"/v1/agents/refunds/labels/prod", map[string]any{"version": verB}); code != 200 {
		t.Fatalf("promote B: %d", code)
	}

	code, res := postJSON(t, ts.URL+"/v1/runs/"+parkedID+"/resume",
		map[string]any{"decisions": []map[string]any{{"call_id": parked.Pending[0].CallID, "decision": "approve"}}})
	if code != 202 || res["agent_version"] != verA || res["resolved_via"] != registry.ViaPin || res["resume_of"] != parkedID {
		t.Fatalf("resume: %d %v", code, res)
	}
	resID := res["id"].(string)
	time.Sleep(300 * time.Millisecond) // claim loop polls; nothing serves A
	if r := waitRun(t, app, resID, sessions.StatusQueued, sessions.StatusSucceeded, sessions.StatusFailed); r.Status != sessions.StatusQueued {
		t.Fatalf("the resume ran with no worker of its version: %+v", r)
	}

	// A new run takes prod (B) and runs on the new build.
	code, fresh := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "refund 7"}})
	if code != 202 || fresh["agent_version"] != verB || fresh["resolved_via"] != "label:prod" {
		t.Fatalf("new run: %d %v", code, fresh)
	}
	waitRun(t, app, fresh["id"].(string), sessions.StatusParked)

	// Build A comes back (a second executor on this node): the resume
	// runs there, on its pin, and the approved call runs.
	modelA := wefttest.Script(wefttest.Say("refunded"))
	exA, err := executor.NewEmbedded(digestA, []*core.Agent{refunds(modelA, &refundedA)})
	if err != nil {
		t.Fatal(err)
	}
	vsA, err := app.Executor(exA)
	if err != nil || len(vsA) != 1 || vsA[0].ID != verA {
		t.Fatalf("re-attached A = %+v, %v", vsA, err)
	}
	done := waitRun(t, app, resID, sessions.StatusSucceeded, sessions.StatusFailed)
	if done.Status != sessions.StatusSucceeded || refundedA.Load() != 1 || refundedB.Load() != 0 {
		t.Fatalf("resume = %+v; refunded on A %d, on B %d", done, refundedA.Load(), refundedB.Load())
	}
	if got := strings.Join(runEvents(t, app, parkedID), ","); got != "run.queued,run.started,run.parked" {
		t.Errorf("parked run events = %s", got)
	}
	if got := strings.Join(runEvents(t, app, resID), ","); got != "run.queued,run.started,run.finished" {
		t.Errorf("resume events = %s", got)
	}
	if code, _ := postJSON(t, ts.URL+"/v1/runs/"+parkedID+"/resume", map[string]any{}); code != 409 {
		t.Errorf("second resume: %d, want 409", code)
	}
}

// stubExec accepts every command and never answers again: a worker
// that went away (or a node that crashed) with the run in its hands.
type stubExec struct {
	reg        wire.Registration
	dispatched atomic.Int32
}

func newStub(name string) *stubExec {
	body := []byte(`{"weft":1,"agents":[{"name":"` + name + `"}]}`)
	return &stubExec{reg: wire.Registration{Placement: wire.PlacementRemote, Build: wire.Build{Digest: "sha256:5ab"},
		Agents: []wire.AgentRegistration{{Name: name, Manifest: body, ManifestHash: registry.ManifestHash(body)}}}}
}

func (s *stubExec) Register(context.Context) (wire.Registration, error) { return s.reg, nil }
func (s *stubExec) Dispatch(_ context.Context, c wire.Command) (wire.Ack, error) {
	s.dispatched.Add(1)
	return wire.Ack{CommandID: c.CommandID, State: wire.AckAccepted}, nil
}
func (s *stubExec) Cancel(context.Context, wire.Cancel) error { return nil }
func (s *stubExec) Events(ctx context.Context) iter.Seq2[wire.Frame, error] {
	return func(func(wire.Frame, error) bool) { <-ctx.Done() }
}
func (s *stubExec) Steer(_ context.Context, st wire.Steer) (wire.Ack, error) {
	return wire.Ack{CommandID: st.CommandID, State: wire.AckRejected, Error: "stub"}, nil
}
func (s *stubExec) Drain(context.Context, time.Time) error { return nil }

// TestRestartClaimsAgain: close and reopen the kernel over one data
// dir. A run queued for a version nobody served is claimed and runs
// after the restart; a run that was in a (now gone) worker's hands is
// claimed again only once its lease expired, and is then marked lost,
// never sent twice.
func TestRestartClaimsAgain(t *testing.T) {
	dir := t.TempDir()
	ttl := leaseTTL(400 * time.Millisecond)
	app, ts, stop := startApp(t, dir, BuildDigest(digestA), ttl)
	gone := newStub("slow")
	if _, err := app.Executor(gone); err != nil {
		t.Fatal(err)
	}
	// "later" is registered (a pushed build) but nothing serves it yet.
	agentLater := core.New(wefttest.Script(wefttest.Say("ran after the restart")), core.Name("later"))
	m, err := core.Manifest(agentLater)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Registry().RegisterIn(context.Background(), tenant.Default(), digestB, wire.BuildBinary,
		[]registry.AgentSpec{{Name: "later", Manifest: m, ManifestHash: registry.ManifestHash(m)}}); err != nil {
		t.Fatal(err)
	}
	_, inHand := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "slow", "input": map[string]any{"prompt": "x"}})
	_, queued := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "later", "input": map[string]any{"prompt": "y"}})
	deadline := time.Now().Add(5 * time.Second)
	for gone.dispatched.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if gone.dispatched.Load() != 1 {
		t.Fatal("the stub never got its command")
	}
	stop()

	app, _, _ = startApp(t, dir, BuildDigest(digestB), ttl)
	back := newStub("slow") // the same version is served again
	if _, err := app.Executor(back); err != nil {
		t.Fatal(err)
	}
	if err := app.Agent(agentLater); err != nil {
		t.Fatal(err)
	}
	if r := waitRun(t, app, queued["id"].(string), sessions.StatusSucceeded, sessions.StatusFailed); r.Status != sessions.StatusSucceeded {
		t.Fatalf("the queued run after the restart = %+v", r)
	}
	lost := waitRun(t, app, inHand["id"].(string), sessions.StatusFailed, sessions.StatusSucceeded)
	if !strings.HasPrefix(lost.Error, sessions.ErrWorkerLost) || back.dispatched.Load() != 0 {
		t.Fatalf("the in-hand run = %+v; re-dispatched %d times", lost, back.dispatched.Load())
	}
	if got := strings.Join(runEvents(t, app, inHand["id"].(string)), ","); got != "run.queued,run.failed" {
		t.Errorf("lost run events = %s", got)
	}
}
