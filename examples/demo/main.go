// Command demo is Weftbase in one command, with no model provider and
// no key: a node on http://127.0.0.1:7331 over a throwaway data
// directory, two agents of a make-believe kitchenware store, and a
// steady trickle of runs to look at in the Console.
//
//	go run ./examples/demo            # from the repository root
//	go run ./examples/demo -every 0   # the first batch only
//
// It shows both placements that exist:
//
//   - embedded: the support agent runs inside this process
//     (weftbase.App.Executor), as two releases;
//   - local: the refunds agent is its own binary (./build), compiled
//     twice and run by the node as supervised child processes
//     (weftbase.App.Local).
//
// Each agent has two AgentVersions, labelled prod and staging. Every
// run is admitted through the public API (POST /v1/runs), and every
// model is scripted (package shop): succeeded runs with parallel tool
// calls, a delegation to a subagent, a run that fails on a tool and
// one on the model, a cancelled run, conversations of several turns,
// and refunds that wait for a person (each parked call is an approval,
// decided through /v1/approvals): some approved, one denied, one left
// pending for you to decide.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/otel"

	"github.com/weftbase/weftbase"
	"github.com/weftbase/weftbase/client/exporter"
	"github.com/weftbase/weftbase/examples/demo/shop"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/worker/executor"
)

type config struct {
	addr    string        // where the node listens
	dir     string        // data directory; "" is a temporary one, removed on exit
	speed   float64       // scales the scripted latency (0: none)
	every   time.Duration // a new run this often after the first batch (0: never)
	console string        // a Console dev server to print a second sign-in link for
	out     io.Writer
}

func main() {
	cfg := config{out: os.Stdout}
	flag.StringVar(&cfg.addr, "addr", "127.0.0.1:7331", "listen `address` (the Console dev server proxies /v1 to :7331)")
	flag.StringVar(&cfg.dir, "data", "", "data `dir`ectory (default: a temporary one, removed on exit)")
	flag.Float64Var(&cfg.speed, "speed", 1, "scale of the scripted latency: 1 is realistic, 0 is none")
	flag.DurationVar(&cfg.every, "every", 4*time.Second, "start a new run this often after the first batch (0: never)")
	flag.StringVar(&cfg.console, "console", "http://localhost:3000", "also print a sign-in link for a Console dev server at this `url` (\"\": none)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, nil); err != nil {
		fmt.Fprintln(os.Stderr, "demo:", err)
		os.Exit(1)
	}
}

// run serves the demo until ctx ends. ready, when given, is called once
// the first batch of runs has played.
func run(ctx context.Context, cfg config, ready func(url, key string)) error {
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(cfg.out, "demo: "+format+"\n", args...) }
	speed := shop.Speed(cfg.speed)

	dir := cfg.dir
	if dir == "" {
		tmp, err := os.MkdirTemp("", "weftbase-demo-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		dir = tmp
	}

	// The refunds build, compiled twice: two digests, two versions.
	say("building the refunds agent (two releases)…")
	var builds []string
	for release := 1; release <= 2; release++ {
		bin, err := buildRefunds(ctx, dir, release)
		if err != nil {
			return err
		}
		builds = append(builds, bin)
	}

	// The node: the kernel behind one listener. The key is the secret
	// API key of the default project (what `weftbase dev` keeps in
	// <data>/bootstrap.key).
	key := newKey()
	logger := slog.New(slog.NewTextHandler(cfg.out, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app, err := weftbase.New(weftbase.DataDir(dir), weftbase.BootstrapKey(key), weftbase.Logger(logger))
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		_ = app.Shutdown(context.Background())
		return fmt.Errorf("%w (is `weftbase dev` or another demo running?)", err)
	}
	base := "http://" + ln.Addr().String()
	app.Serving(base)
	srv := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	nodeCtx, stopNode := context.WithCancel(context.Background())
	nodeDone := make(chan struct{})
	go func() { defer close(nodeDone); _ = app.Run(nodeCtx) }()

	// This process's own runs (the embedded agents) report to the node
	// like any app does: traces and log lines over OTLP.
	flush := exporter.Install(exporter.Remote(base, key), otel.Service("weftbase-demo"), otel.Slog(), otel.NoEnv())

	defer func() {
		flush()
		_ = srv.Close()
		stopNode()
		<-nodeDone
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := app.Shutdown(sctx); err != nil {
			say("shutdown: %v", err)
		}
	}()

	// Embedded placement: two releases of the support agent in this
	// process. (One release is just app.Agent(agent); a digest names
	// the build each one belongs to.)
	versions := map[string][]string{}
	for release := 1; release <= 2; release++ {
		ex, err := executor.NewEmbedded(digestOf("support release", release), []*core.Agent{shop.Support(release, speed)})
		if err != nil {
			return err
		}
		vs, err := app.Executor(ex)
		if err != nil {
			return err
		}
		versions["support"] = append(versions["support"], vs[0].ID)
	}
	// Local placement: each refunds binary runs as a child process.
	for _, bin := range builds {
		vs, err := app.Local(bin,
			weftbase.LocalEnv("DEMO_SPEED="+strconv.FormatFloat(cfg.speed, 'f', -1, 64)),
			weftbase.LocalOutput(cfg.out))
		if err != nil {
			return err
		}
		versions["refunds"] = append(versions["refunds"], vs[0].ID)
	}

	// Labels: the first release is prod, the second is staging.
	c := client{base: base, key: key}
	for agent, vs := range versions {
		for i, label := range []string{"prod", "staging"} {
			if err := c.post(ctx, "/v1/agents/"+agent+"/labels/"+label, map[string]any{"version": vs[i], "reason": "demo setup"}, nil); err != nil {
				return err
			}
		}
	}

	say("serving %s (data in %s)", base, dir)
	say("API key: %s", key)
	for _, origin := range []string{base, cfg.console} {
		if origin == "" {
			continue
		}
		tok, err := app.Sessions().CreateLink(ctx, tenant.Default())
		if err != nil {
			return err
		}
		say("sign in to the Console: %s/#signin=%s (one use, 15 min)", strings.TrimRight(origin, "/"), url.QueryEscape(tok))
	}

	say("playing the first batch of runs…")
	if err := c.playAll(ctx, speed, firstBatch()); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if cfg.every > 0 {
		say("first batch done; a new run every %s (Ctrl-C to stop)", cfg.every)
	} else {
		say("first batch done (Ctrl-C to stop)")
	}
	if ready != nil {
		ready(base, key)
	}

	var wg sync.WaitGroup
	defer wg.Wait()
	if cfg.every > 0 {
		tick := time.NewTicker(cfg.every)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				s := steady[i%len(steady)]
				wg.Go(func() {
					if err := c.play(ctx, speed, s); err != nil && ctx.Err() == nil {
						say("%v", err)
					}
				})
			}
		}
	}
	<-ctx.Done()
	return nil
}

// ── What gets played ─────────────────────────────────────────────────

// scene is one conversation: an agent, the version to run ("" is the
// prod label), the user's messages (each one is a run; more than one
// makes a session), and what happens to the run afterwards.
type scene struct {
	agent   string
	version string
	prompts []string
	then    string // "", "approve", "deny", "cancel", "leave pending"
}

func firstBatch() []scene {
	return []scene{
		{agent: "support", prompts: []string{shop.AskTracking}},
		{agent: "support", version: "label:staging", prompts: []string{shop.AskStock}},
		{agent: "support", prompts: []string{shop.AskCharge}},  // delegates to the billing subagent
		{agent: "support", prompts: []string{shop.AskAddress}}, // fails: the tool keeps erroring
		{agent: "support", prompts: []string{shop.AskOutage}},  // fails: the model call errors
		{agent: "support", prompts: []string{shop.AskExport}, then: "cancel"},
		{agent: "support", prompts: []string{shop.AskTracking, shop.AskReturn, shop.AskThanks}},
		{agent: "support", version: "label:staging", prompts: []string{shop.AskStock, shop.AskThanks}},
		{agent: "refunds", prompts: []string{shop.AskRefund}, then: "approve"},
		{agent: "refunds", prompts: []string{shop.AskRefund90}, then: "deny"},
		{agent: "refunds", prompts: []string{shop.AskRefund, shop.AskThanks}, then: "approve"}, // a conversation that waits on a person
		{agent: "refunds", version: "label:staging", prompts: []string{shop.AskRefund77}, then: "leave pending"},
	}
}

// steady is the rotation played after the first batch.
var steady = []scene{
	{agent: "support", prompts: []string{shop.AskTracking}},
	{agent: "refunds", prompts: []string{shop.AskRefund}, then: "approve"},
	{agent: "support", version: "label:staging", prompts: []string{shop.AskStock}},
	{agent: "support", prompts: []string{shop.AskCharge}},
	{agent: "support", prompts: []string{shop.AskAddress}},
	{agent: "support", prompts: []string{shop.AskTracking, shop.AskReturn}},
	{agent: "refunds", version: "label:staging", prompts: []string{shop.AskRefund77}, then: "deny"},
	{agent: "support", prompts: []string{shop.AskOutage}},
	{agent: "support", prompts: []string{shop.AskExport}, then: "cancel"},
}

// ── Playing a scene over /v1 ─────────────────────────────────────────

type client struct{ base, key string }

// runStatus is the part of a run's status (GET /v1/runs/{id}/status)
// the demo reads.
type runStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (c client) playAll(ctx context.Context, speed shop.Speed, scenes []scene) error {
	errs := make([]error, len(scenes))
	var wg sync.WaitGroup
	for i, s := range scenes {
		wg.Go(func() { errs[i] = c.play(ctx, speed, s) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// play runs a scene's messages one after the other. Several messages
// share a session id (the weft.session.id metadata key), and each run
// gets the conversation so far as its input.
func (c client) play(ctx context.Context, speed shop.Speed, s scene) error {
	var session string
	if len(s.prompts) > 1 {
		session = "ses_" + newID()
	}
	var history []core.Message
	for turn, prompt := range s.prompts {
		history = append(history, core.User(prompt))
		req := map[string]any{"agent": s.agent, "version": s.version, "input": map[string]any{"messages": history}}
		if session != "" {
			req["metadata"] = map[string]string{"weft.session.id": session, "weft.turn": strconv.Itoa(turn + 1)}
		}
		var run runStatus
		if err := c.post(ctx, "/v1/runs", req, &run); err != nil {
			return err
		}
		if s.then == "cancel" {
			if _, err := c.wait(ctx, run.ID, "running"); err != nil {
				return err
			}
			pause(ctx, speed, 2500*time.Millisecond)
			return c.post(ctx, "/v1/runs/"+run.ID+"/cancel", map[string]any{"reason": "the customer closed the chat"}, nil)
		}
		done, err := c.wait(ctx, run.ID, "succeeded", "failed", "canceled", "parked")
		if err != nil {
			return err
		}
		if done.Status == "parked" {
			// The run waits for a person: each call it parked on is an
			// approval. Deciding the last one resumes the run, as a
			// new run on the same AgentVersion.
			if s.then == "leave pending" {
				return nil
			}
			pause(ctx, speed, 5*time.Second)
			var inbox struct {
				Approvals []struct {
					ID         string `json:"id"`
					ArgsSHA256 string `json:"args_sha256"`
				} `json:"approvals"`
			}
			if err := c.get(ctx, "/v1/approvals?status=pending&run="+run.ID, &inbox); err != nil {
				return err
			}
			var decided struct {
				ResumedBy string `json:"resumed_by"`
			}
			for _, a := range inbox.Approvals {
				// args_sha256 binds the decision to the arguments shown.
				decision := map[string]any{"decision": s.then, "args_sha256": a.ArgsSHA256}
				if s.then == "deny" {
					decision["reason"] = "outside the 30-day refund window"
				}
				if err := c.post(ctx, "/v1/approvals/"+a.ID+"/decision", decision, &decided); err != nil {
					return err
				}
			}
			if _, err := c.wait(ctx, decided.ResumedBy, "succeeded", "failed", "canceled"); err != nil {
				return err
			}
		}
		history = append(history, core.Assistant(shop.Reply(prompt)))
	}
	return nil
}

// wait polls a run's status until it is one of want.
func (c client) wait(ctx context.Context, id string, want ...string) (runStatus, error) {
	for {
		var run runStatus
		if err := c.get(ctx, "/v1/runs/"+id+"/status", &run); err != nil {
			return run, err
		}
		for _, w := range want {
			if run.Status == w {
				return run, nil
			}
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, "GET", path, nil, out)
}

func (c client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, "POST", path, body, out)
}

func (c client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

// ── Small helpers ────────────────────────────────────────────────────

// buildRefunds compiles ./build as one release of the refunds agent.
func buildRefunds(ctx context.Context, dir string, release int) (string, error) {
	out := filepath.Join(dir, "bin", fmt.Sprintf("refunds-r%d", release))
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "-ldflags", fmt.Sprintf("-X main.release=%d", release),
		"github.com/weftbase/weftbase/examples/demo/build")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building the refunds agent (run the demo from inside the weftbase repository, with the go tool on PATH): %w\n%s", err, b)
	}
	return out, nil
}

func pause(ctx context.Context, speed shop.Speed, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(float64(d) * float64(speed))):
	}
}

func digestOf(what string, release int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s %d", what, release))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

func newKey() string { return "wb_sk_test_" + randomString(20) }
func newID() string  { return randomString(10) }
