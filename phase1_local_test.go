package weftbase

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/wefttest"

	"github.com/weftbase/weftbase/client/wire"
	"github.com/weftbase/weftbase/registry"
	"github.com/weftbase/weftbase/sessions"
	"github.com/weftbase/weftbase/store"
)

// The Phase 1 gate with the local placement (plan §9 step 6, NEXT-STEPS
// §4): a real child process built from a fixture agent
// (worker/executor/local/testdata/refunds, wefttest scripted models)
// runs through the same Executor path as the embedded placement; a run
// parked on build A survives a new build B and a restart, and resumes
// on A's child (started again from the build store); work admitted on
// a node without a scheduler is reclaimed by the next node.

var (
	fixtureOnce sync.Once
	fixtureBins map[string]string
	fixtureErr  error
)

// fixture builds the refunds fixture as build tag ("A", "B"): two tags
// are two digests of one agent definition.
func fixture(t *testing.T, tag string) string {
	t.Helper()
	fixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wb-fixture")
		if err != nil {
			fixtureErr = err
			return
		}
		fixtureBins = map[string]string{}
		gobin, err := exec.LookPath("go")
		if err != nil {
			fixtureErr = fmt.Errorf("the go tool builds the fixture: %w", err)
			return
		}
		for _, tg := range []string{"A", "B"} {
			out := filepath.Join(dir, "refunds-"+tg)
			cmd := exec.Command(gobin, "build", "-o", out, "-ldflags", "-X main.build="+tg, "./worker/executor/local/testdata/refunds")
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				fixtureErr = fmt.Errorf("building the fixture: %v\n%s", err, b)
				return
			}
			fixtureBins[tg] = out
		}
	})
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	return fixtureBins[tag]
}

func refundLog(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func TestParkedLocalRunSurvivesANewBuild(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "refunds.log")
	// A build restored from the build store runs with the node's
	// environment only (per-build env is the deployment object's, later):
	// the log path rides the node's.
	t.Setenv("REFUND_LOG", log)
	quiet := LocalOutput(io.Discard)

	// Node 1 runs build A as a local child; a refund parks on it.
	app, ts, stop := startApp(t, dir)
	vsA, err := app.Local(fixture(t, "A"), quiet)
	if err != nil || len(vsA) != 1 || vsA[0].Agent != "refunds" {
		t.Fatalf("Local(A) = %+v, %v", vsA, err)
	}
	verA := vsA[0].ID
	_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "refund 42"}})
	parkedID := run["id"].(string)
	parked := waitRun(t, app, parkedID, sessions.StatusParked, sessions.StatusFailed)
	if parked.Status != sessions.StatusParked || parked.AgentVersion != verA || len(parked.Pending) != 1 || parked.Pending[0].Tool != "refund" {
		t.Fatalf("parked = %+v", parked)
	}
	stop()

	// Node 2 serves the API only: what it admits waits in the queue.
	app, ts, stop = startApp(t, dir, Roles(RoleAPI))
	res := decide(t, ts.URL, parkedID, "approve")
	if agv(res) != verA || res["resolved_via"] != registry.ViaPin {
		t.Fatalf("resume: %v", res)
	}
	resID := res["id"].(string)
	_, hello := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "hello"}})
	helloID := hello["id"].(string)
	if _, err := app.Local(fixture(t, "A")); err == nil {
		t.Error("an api-only node ran a local build")
	}
	stop()

	// Node 3: a new build B of the same agent is deployed and promoted.
	// A is restored from the build store without starting; the work
	// node 2 admitted is reclaimed and runs on A, its pin.
	app, ts, _ = startApp(t, dir)
	vsB, err := app.Local(fixture(t, "B"), quiet)
	if err != nil || len(vsB) != 1 {
		t.Fatalf("Local(B) = %+v, %v", vsB, err)
	}
	verB := vsB[0].ID
	if verB == verA || vsB[0].ManifestHash != vsA[0].ManifestHash || vsB[0].BuildDigest == vsA[0].BuildDigest {
		t.Fatalf("B = %+v, A = %+v: want one manifest, two builds, two versions", vsB[0], vsA[0])
	}
	if code, _ := postJSON(t, ts.URL+"/v1/agents/refunds/labels/prod", map[string]any{"version": verB}); code != 200 {
		t.Fatalf("promote B: %d", code)
	}
	done := waitRun(t, app, resID, sessions.StatusSucceeded, sessions.StatusFailed)
	if done.Status != sessions.StatusSucceeded || done.AgentVersion != verA || done.ResumeOf != parkedID {
		t.Fatalf("resume = %+v", done)
	}
	if h := waitRun(t, app, helloID, sessions.StatusSucceeded, sessions.StatusFailed); h.Status != sessions.StatusSucceeded || h.AgentVersion != verA {
		t.Fatalf("the reclaimed run = %+v", h)
	}
	if got := refundLog(t, log); !slices.Equal(got, []string{"A", "42"}) {
		t.Fatalf("refund log = %v, want the refund on A", got)
	}

	// A new run takes prod: B's child.
	code, fresh := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "refund 7"}})
	if code != 202 || agv(fresh) != verB || fresh["resolved_via"] != "label:prod" {
		t.Fatalf("new run: %d %v", code, fresh)
	}
	p7 := waitRun(t, app, fresh["id"].(string), sessions.StatusParked)
	r7 := decide(t, ts.URL, p7.ID, "approve")
	if d := waitRun(t, app, r7["id"].(string), sessions.StatusSucceeded, sessions.StatusFailed); d.Status != sessions.StatusSucceeded || d.AgentVersion != verB {
		t.Fatalf("B's resume = %+v", d)
	}
	if got := refundLog(t, log); !slices.Equal(got, []string{"A", "42", "B", "7"}) {
		t.Fatalf("refund log = %v", got)
	}
	assertEveryRunHasAVersion(t, app)
}

// TestOneExecutorPathForEveryPlacement: an embedded agent and a local
// build attach to one hub through worker.Executor and run side by side.
func TestOneExecutorPathForEveryPlacement(t *testing.T) {
	app, ts, _ := startApp(t, t.TempDir(), BuildDigest(digestA))
	if err := app.Agent(core.New(wefttest.Script(wefttest.Say("in process")), core.Name("support"))); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Local(fixture(t, "A"), LocalOutput(io.Discard)); err != nil {
		t.Fatal(err)
	}
	var placements []string
	for _, w := range app.hub.Router().Workers() {
		placements = append(placements, w.Registration.Placement)
	}
	slices.Sort(placements)
	if !slices.Equal(placements, []string{wire.PlacementEmbedded, wire.PlacementLocal}) {
		t.Fatalf("placements = %v", placements)
	}
	for _, agent := range []string{"support", "refunds"} {
		_, run := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": agent, "input": map[string]any{"prompt": "hello"}})
		if r := waitRun(t, app, run["id"].(string), sessions.StatusSucceeded, sessions.StatusFailed); r.Status != sessions.StatusSucceeded {
			t.Fatalf("%s: %+v", agent, r)
		}
	}
	// Cancel and steer reach a local child too.
	_, slow := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "wait"}})
	id := slow["id"].(string)
	waitRun(t, app, id, sessions.StatusRunning)
	msg := map[string]any{"role": "user", "content": []map[string]any{{"type": "text", "text": "and be quick"}}}
	if code, body := postJSON(t, ts.URL+"/v1/runs/"+id+"/steer", map[string]any{"messages": []any{msg}}); code != 202 {
		t.Fatalf("steer a local run: %d %v", code, body)
	}
	waitRun(t, app, id, sessions.StatusSucceeded)
	_, slow = postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "wait"}})
	id = slow["id"].(string)
	waitRun(t, app, id, sessions.StatusRunning)
	if code, _ := postJSON(t, ts.URL+"/v1/runs/"+id+"/cancel", map[string]any{}); code != 202 {
		t.Fatalf("cancel a local run: %d", code)
	}
	if r := waitRun(t, app, id, sessions.StatusCancelled, sessions.StatusSucceeded); r.Status == sessions.StatusCancelled && r.CancelCause != wire.CauseUser {
		t.Fatalf("canceled = %+v", r)
	}
	// A crash ends the child's run worker_lost and writes
	// worker.disconnected; the build restarts and serves again.
	_, crash := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "crash"}})
	lost := waitRun(t, app, crash["id"].(string), sessions.StatusFailed, sessions.StatusSucceeded)
	if !strings.HasPrefix(lost.Error, sessions.ErrWorkerLost) {
		t.Fatalf("crashed = %+v", lost)
	}
	_, again := postJSON(t, ts.URL+"/v1/runs", map[string]any{"agent": "refunds", "input": map[string]any{"prompt": "hello"}})
	if r := waitRun(t, app, again["id"].(string), sessions.StatusSucceeded, sessions.StatusFailed); r.Status != sessions.StatusSucceeded {
		t.Fatalf("after the restart = %+v", r)
	}
	var n int
	if err := app.control.Read(context.Background(), func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM events WHERE type = 'worker.disconnected'`).Scan(&n)
	}); err != nil || n == 0 {
		t.Errorf("worker.disconnected events = %d (%v)", n, err)
	}
	assertEveryRunHasAVersion(t, app)
}

func assertEveryRunHasAVersion(t *testing.T, app *App) {
	t.Helper()
	var bare int
	if err := app.control.Read(context.Background(), func(tx store.Tx) error {
		return store.QueryRow(tx, `SELECT COUNT(*) FROM runs r LEFT JOIN agent_versions v ON v.id = r.agent_version
			WHERE v.id IS NULL OR r.resolved_via = ''`).Scan(&bare)
	}); err != nil || bare != 0 {
		t.Errorf("runs without an AgentVersion: %d (%v)", bare, err)
	}
}
