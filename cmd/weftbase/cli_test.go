package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weftbase/weftbase/client/rest"

	"github.com/weftbase/weft/core"
	"github.com/weftbase/weft/core/mw"
	"github.com/weftbase/weft/core/wefttest"
	"github.com/weftbase/weft/otel"
	"github.com/weftbase/weftbase/client/discovery"
	"github.com/weftbase/weftbase/client/exporter"
)

// opened records what the command handed the browser opener: no test
// ever opens a browser.
var opened struct {
	mu    sync.Mutex
	links []string
}

func takeOpened() []string {
	opened.mu.Lock()
	defer opened.mu.Unlock()
	l := opened.links
	opened.links = nil
	return l
}

func TestMain(m *testing.M) {
	// `weftbase dev`'s helper process: this binary re-executed as the app.
	if mode := os.Getenv("WEFT_DEV_HELPER"); mode != "" {
		os.Exit(devHelper(mode))
	}
	// `weftbase mcp` over real stdio: this binary re-executed as the
	// command, its arguments after the test binary's name.
	if os.Getenv("WEFT_MCP_HELPER") == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	stdoutIsTTY = func() bool { return false }
	// Every discovery file the tests' servers write (and every read)
	// goes to a directory of this process's own, never the developer's
	// ./.weftbase, $XDG_RUNTIME_DIR/weftbase or user cache directory.
	discDir, err := os.MkdirTemp("", "weft-discovery-")
	if err != nil {
		panic(err)
	}
	discovery.Dirs = func() []string { return []string{discDir} }
	openURL = func(link string) error {
		opened.mu.Lock()
		defer opened.mu.Unlock()
		opened.links = append(opened.links, link)
		return nil
	}
	code := m.Run()
	_ = os.RemoveAll(discDir)
	os.Exit(code)
}

// TestUsage pins the dispatcher: no command and an unknown one are
// usage errors (exit 2, the usage on stderr), help is exit 0 on stdout,
// and a bad `weftbase dev` flag or --watch directory is a usage error.
func TestUsage(t *testing.T) {
	for _, c := range []struct {
		args       []string
		code       int
		out, errIn string
	}{
		{nil, 2, "", "a command is required"},
		{[]string{"nope"}, 2, "", `unknown command "nope"`},
		{[]string{"help"}, 0, "weftbase dev", ""},
		{[]string{"dev", "--bogus"}, 2, "", "flag provided but not defined: -bogus"},
		{[]string{"dev", "--no-watch"}, 2, "", "dev's loop runs a command"},
		{[]string{"dev", "--"}, 2, "", "dev's loop runs a command"},
		{[]string{"dev", "--watch", "/nonexistent-weft-dev-dir", "--", "true"}, 2, "", "--watch /nonexistent-weft-dev-dir: not a directory"},
		{[]string{"version", "extra"}, 2, "", "version takes no arguments"},
		{[]string{"runs", "--bogus"}, 2, "", "flag provided but not defined: -bogus"},
		{[]string{"runs", "-h"}, 0, "", "-since"},
		{[]string{"dev", "extra"}, 2, "", "dev takes no arguments"},
	} {
		var out, errb strings.Builder
		code := run(c.args, &out, &errb)
		if code != c.code || !strings.Contains(out.String(), c.out) || !strings.Contains(errb.String(), c.errIn) {
			t.Errorf("weft %q = exit %d\nstdout %q\nstderr %q\nwant exit %d, stdout ⊇ %q, stderr ⊇ %q",
				c.args, code, out.String(), errb.String(), c.code, c.out, c.errIn)
		}
	}
}

// TestParseArgs pins the interspersed positional: `weftbase export <id>
// --format jsonl` parses the flag after the id, and "--" ends the flags.
func TestParseArgs(t *testing.T) {
	fs := newFlags("t", io.Discard)
	f := fs.String("f", "", "")
	pos, err := parseArgs(fs, []string{"a", "-f", "x", "b"})
	if err != nil || *f != "x" || strings.Join(pos, ",") != "a,b" {
		t.Errorf("interspersed: %q %q %v", pos, *f, err)
	}
	fs = newFlags("t", io.Discard)
	f = fs.String("f", "", "")
	pos, err = parseArgs(fs, []string{"a", "--", "-f", "y"})
	if err != nil || *f != "" || strings.Join(pos, ",") != "a,-f,y" {
		t.Errorf("after --: %q %q %v", pos, *f, err)
	}
}

// ── the API clients, against a scripted server ─────────────────────

type orderIn struct {
	OrderID string `json:"order_id" jsonschema:"the order"`
}

// ordersAgent is the one agent definition the recorded run and its
// replay share.
func ordersAgent(m core.Model, opts ...core.Option) *core.Agent {
	lookup := core.Tool("lookup_order", "Look up an order.", func(context.Context, orderIn) (string, error) {
		return "order 42 shipped", nil
	})
	return core.New(m, append([]core.Option{core.Name("orders"), core.Instructions("You are a support agent."), lookup}, opts...)...)
}

// apiServer is setup B's server (newServer, the token wall on) under
// httptest, with two runs ingested through the otel pipeline: r_ok
// (orders: a tool call, then an answer) and r_fail (triage: the model
// fails), started apart. With child, a third run p (lead) delegates to
// a subagent: its child run p/0/c. It returns the server URL and r_ok's
// transcript.
func apiServer(t *testing.T, child bool) (string, []core.Message) {
	t.Helper()
	srv := newServer(t, tok)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = srv.Close() })

	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(ts.URL, tok), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	prov := []core.Option{core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider())}
	ok := ordersAgent(wefttest.Script(
		wefttest.ToolCalls(wefttest.Call{Name: "lookup_order", Args: `{"order_id":"42"}`, ID: "c1"}),
		wefttest.Say("Order 42 shipped."),
	), prov...)
	res, err := ok.Generate(ctx, core.RunID("r_ok"), core.Prompt("where is order 42?"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond) // a --since cutoff fits between the two
	fail := core.New(wefttest.Script(wefttest.Fail(errors.New("upstream down"))),
		append([]core.Option{core.Name("triage")}, prov...)...)
	if _, err := fail.Generate(ctx, core.RunID("r_fail"), core.Prompt("triage")); err == nil {
		t.Fatal("the failing run succeeded")
	}
	settle := map[string]string{"r_ok": `"status":"succeeded"`, "r_fail": `"status":"failed"`}
	if child {
		researcher := core.New(wefttest.Script(wefttest.Say("the carrier lost it")),
			append([]core.Option{core.Name("researcher")}, prov...)...)
		lead := core.New(wefttest.Script(
			wefttest.ToolCalls(wefttest.Call{Name: "research", Args: `{"prompt":"why?"}`, ID: "c"}),
			wefttest.Say("done"),
		), append([]core.Option{core.Name("lead"), core.Subagent("research", "Research.", researcher)}, prov...)...)
		if _, err := lead.Generate(ctx, core.RunID("p"), core.Prompt("go")); err != nil {
			t.Fatal(err)
		}
		settle["p"], settle["p/0/c"] = `"status":"succeeded"`, `"status":"succeeded"`
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	c := rest.New(ts.URL, tok)
	for id, status := range settle {
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			b, err := c.Get(ctx, rest.RunPath(id), nil)
			if err == nil && strings.Contains(string(b), status) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s never settled to %s: %s %v", id, status, b, err)
			}
		}
	}
	return ts.URL, res.Messages
}

// weftbase runs the command line and returns stdout, stderr and the exit
// code.
func cli(args ...string) (string, string, int) {
	var out, errb strings.Builder
	code := run(args, &out, &errb)
	return out.String(), errb.String(), code
}

// TestRunsCommand pins `weftbase runs`: one row per run, newest first, from
// GET /v1/runs; --agent and --failed filter on the server, --since and
// --limit cut the paging (pinned across a one-run page, so the cursor
// is followed), --json prints server's rows; the token comes from
// --token or WEFTBASE_TOKEN and a missing one is server's 401.
func TestRunsCommand(t *testing.T) {
	url, _ := apiServer(t, false)
	t.Setenv("WEFTBASE_TOKEN", "")
	t.Setenv("WEFTBASE_URL", "")

	out, errb, code := cli("runs", "--url", url, "--token", tok)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], "ID") ||
		!strings.HasPrefix(lines[1], "r_fail") || !strings.Contains(lines[1], "triage") || !strings.Contains(lines[1], "failed") ||
		!strings.HasPrefix(lines[2], "r_ok") || !strings.Contains(lines[2], "orders") || !strings.Contains(lines[2], "succeeded") {
		t.Errorf("weftbase runs = exit %d\n%s%s", code, out, errb)
	}
	// STOPPED BY is the last column: what ended the run (U3.2) — r_ok's
	// model answered; r_fail's model failed, which no control decided.
	// PROMPT sits before it (U6): the prompt preview's first 40 runes.
	// UX-S5a-05: DUR, COST and ERROR — a failed run is triaged from
	// the table — and STARTED carries its zone.
	if !regexp.MustCompile(`STARTED +STEPS +DUR +COST +PROMPT +STOPPED BY +ERROR$`).MatchString(strings.TrimSpace(lines[0])) {
		t.Errorf("header = %q, want STEPS, DUR, COST, PROMPT, STOPPED BY, ERROR", lines[0])
	}
	if !regexp.MustCompile(` [+-]\d{4} +2 +\S+ +\$0 +where is order 42\? +final_answer +-$`).MatchString(strings.TrimSpace(lines[2])) {
		t.Errorf("r_ok's columns = %q, want its zone, 2 steps, a duration, $0 (scripted), its prompt, final_answer, no error", lines[2])
	}
	for _, c := range []struct{ in, want string }{
		{"", "-"},
		{"  line one\nline two", "line one"},
		{"a\tb", "a b"},
		{strings.Repeat("é", 41), strings.Repeat("é", 39) + "…"},
		{strings.Repeat("x", 40), strings.Repeat("x", 40)},
		// Untrusted text never reaches the terminal raw: an OSC title
		// set, a screen clear, a carriage return that would overwrite
		// the row — every control character is a space.
		{"\x1b]0;owned\x07hi\x1b[2J there\rX\nnext", "]0;owned hi [2J there X"},
	} {
		if got := (runRow{Prompt: c.in}).prompt(); got != c.want {
			t.Errorf("prompt cell of %q = %q, want %q", c.in, got, c.want)
		}
	}
	// The other free-text cells follow the same rule.
	if got := cell("bot\x1b[31m\r"); got != "bot [31m" {
		t.Errorf("cell = %q, want control characters as spaces", got)
	}
	if got := (runRow{StoppedBy: "cancel", CancelCause: "x\x1b[2Jy"}).stoppedBy(); got != "cancel (x [2Jy)" {
		t.Errorf("stopped by = %q, want control characters as spaces", got)
	}
	col := func(line, name string) string {
		i := strings.Index(lines[0], name)
		if i < 0 || i >= len(line) {
			return ""
		}
		return strings.Fields(line[i:])[0]
	}
	if got := col(lines[1], "STOPPED BY"); got != "-" {
		t.Errorf("r_fail's stopped by column = %q, want - (a model error: no control decided)", got)
	}
	if i := strings.Index(lines[0], "ERROR"); i < 0 || !strings.Contains(lines[1][i:], "upstream down") {
		t.Errorf("r_fail's error column = %q, want its error", lines[1])
	}
	if !strings.Contains(errb, "weftbase open <id>") {
		t.Errorf("stderr %q, want the next step named", errb)
	}
	for _, c := range []struct {
		r    runRow
		want string
	}{
		{runRow{}, "-"},
		{runRow{ErrorType: "max_steps"}, "max_steps"},
		{runRow{Err: "weft: run failed at step 0: model stream: mw: giving up after 2 retries: server error 500: overloaded", ErrorType: "model"},
			"model: giving up after 2 retries: server error 500: overloa…"},
		{runRow{Err: strings.Repeat("x", 70)}, strings.Repeat("x", 59) + "…"},
		{runRow{Err: "bad\x1b[2J\nsecond line"}, "bad [2J"},
	} {
		if got := c.r.errorCell(); got != c.want {
			t.Errorf("error cell of %+v = %q, want %q", c.r, got, c.want)
		}
	}

	t.Setenv("WEFTBASE_URL", url) // the env mirrors --url
	t.Setenv("WEFTBASE_TOKEN", tok)
	rows := func(args ...string) []string {
		t.Helper()
		out, errb, code := cli(append([]string{"runs", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("weftbase runs --json %v = exit %d %s", args, code, errb)
		}
		var rs []runRow
		if err := json.Unmarshal([]byte(out), &rs); err != nil {
			t.Fatalf("--json: %v\n%s", err, out)
		}
		var ids []string
		for _, r := range rs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{nil, "r_fail,r_ok"},
		{[]string{"--failed"}, "r_fail"},
		// --status takes the API's words (I3): sent as status=, the
		// read-time ones filtered by server.
		{[]string{"--status", "failed"}, "r_fail"},
		{[]string{"--status", "failed", "--failed"}, "r_fail"},
		{[]string{"--status", "succeeded"}, "r_ok"},
		{[]string{"--status", "parked"}, ""},
		{[]string{"--status", "cancelled"}, ""},
		// P8-fix-b #14: cancelled is a failed run's word — --failed
		// beside it narrows nothing more, never a usage error.
		{[]string{"--status", "cancelled", "--failed"}, ""},
		{[]string{"--agent", "orders"}, "r_ok"},
		{[]string{"--since", "1h"}, "r_fail,r_ok"},
		{[]string{"--since", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, ""},
		{[]string{"--limit", "1"}, "r_fail"},
	} {
		if got := strings.Join(rows(c.args...), ","); got != c.want {
			t.Errorf("weftbase runs %v = %q, want %q", c.args, got, c.want)
		}
	}
	// A word outside them is a usage error (exit 2) naming the words,
	// before any request; so is --failed beside another status.
	for _, args := range [][]string{{"runs", "--status", "bogus"}, {"runs", "--status", "parked", "--failed"}} {
		if _, errb, code := cli(args...); code != 2 || !strings.Contains(errb, "--") {
			t.Errorf("weft %v = exit %d %q, want a usage error", args, code, errb)
		}
	}
	if _, errb, _ := cli("runs", "--status", "bogus"); !strings.Contains(errb, "running, succeeded, failed, interrupted, parked or cancelled") {
		t.Errorf("--status bogus stderr = %q, want the allowed words", errb)
	}
	// --since between the two runs keeps the newer one only.
	out, _, _ = cli("runs", "--json")
	var both []runRow
	if err := json.Unmarshal([]byte(out), &both); err != nil || len(both) != 2 || !both[0].Started.After(both[1].Started) {
		t.Fatalf("rows %s: %v, want two runs started apart", out, err)
	}
	// --json carries server's stopped_by (U3.2), omitted when none was
	// recorded: r_ok's final_answer, nothing on r_fail.
	if both[1].ID != "r_ok" || both[1].StoppedBy != "final_answer" || both[0].StoppedBy != "" ||
		!strings.Contains(out, `"stopped_by": "final_answer"`) || strings.Count(out, `"stopped_by"`) != 1 || strings.Contains(out, `"cancel_cause"`) {
		t.Errorf("--json stopped_by = %q / %q, want final_answer on r_ok alone and no cancel_cause\n%s", both[1].StoppedBy, both[0].StoppedBy, out)
	}
	cutoff := both[1].Started.Add(both[0].Started.Sub(both[1].Started) / 2).Format(time.RFC3339Nano)
	if got := strings.Join(rows("--since", cutoff), ","); got != "r_fail" {
		t.Errorf("--since %s (between the runs) = %q, want r_fail", cutoff, got)
	}

	// Truncation is visible: a limit that hid runs says so on stderr,
	// --json too; one that hid nothing is silent.
	if _, errb, _ := cli("runs", "--limit", "1"); !strings.HasPrefix(errb, "weftbase: showing the newest 1; --limit 0 lists all\n") {
		t.Errorf("--limit 1 stderr = %q, want the notice", errb)
	}
	if _, errb, _ := cli("runs", "--limit", "2", "--json"); errb != "" {
		t.Errorf("--limit 2 (nothing hidden) stderr = %q", errb)
	}
	defer func(n int) { runsPageSize = n }(runsPageSize)
	runsPageSize = 1
	if got := strings.Join(rows(), ","); got != "r_fail,r_ok" {
		t.Errorf("paged one run at a time = %q, want both", got)
	}
	// The limit lands on a page's last row: the cursor says more exist.
	if out, errb, _ := cli("runs", "--limit", "1", "--json"); !strings.Contains(out, `"r_fail"`) || errb != "weftbase: showing the newest 1; --limit 0 lists all\n" {
		t.Errorf("paged --limit 1 = %q, stderr %q, want r_fail and the notice", out, errb)
	}
	if got := strings.Join(rows("--since", "1h"), ","); got != "r_fail,r_ok" {
		t.Errorf("paged --since = %q, want both", got)
	}

	if out, errb, code := cli("runs", "--since", time.Now().Add(time.Hour).UTC().Format(time.RFC3339)); code != 0 || out != "" || errb != "weftbase: no runs match\n" {
		t.Errorf("no match = exit %d %q %q", code, out, errb)
	}
	if _, errb, code := cli("runs", "--since", "yesterday"); code != 2 || !strings.Contains(errb, "--since") {
		t.Errorf("bad --since = exit %d %q, want 2", code, errb)
	}
	// A canceled run carries its cause on both outputs (U3-fix): the
	// table's STOPPED BY cell names it, --json carries stopped_by and
	// cancel_cause.
	runsPageSize = 50
	p, err := exporter.Start(context.Background(), exporter.Remote(url, tok), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancelCause(context.Background())
	cancel(core.CancelCause("weftbase.cancel"))
	canceled := core.New(wefttest.Script(wefttest.Say("never")), core.Name("support"),
		core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider()))
	if _, err := canceled.Generate(cctx, core.RunID("r_cancel"), core.Prompt("stop")); !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled run = %v, want context.Canceled", err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := rest.New(url, tok)
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		b, err := c.Get(context.Background(), rest.RunPath("r_cancel"), nil)
		if err == nil && strings.Contains(string(b), `"cancel_cause":"weftbase.cancel"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("r_cancel never settled with its cause: %s %v", b, err)
		}
	}
	out, errb, code = cli("runs", "--agent", "support")
	lines = strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 || !strings.HasPrefix(lines[1], "r_cancel") ||
		!strings.Contains(lines[1], " canceled (weftbase.cancel) ") {
		t.Errorf("weftbase runs --agent support = exit %d, want r_cancel's STOPPED BY \"canceled (weftbase.cancel)\"\n%s%s", code, out, errb)
	}
	out, _, _ = cli("runs", "--agent", "support", "--json")
	var cr []runRow
	if err := json.Unmarshal([]byte(out), &cr); err != nil || len(cr) != 1 ||
		cr[0].StoppedBy != "canceled" || cr[0].CancelCause != "weftbase.cancel" ||
		!strings.Contains(out, `"stopped_by": "canceled"`) || !strings.Contains(out, `"cancel_cause": "weftbase.cancel"`) {
		t.Errorf("--json for r_cancel = %v\n%s, want stopped_by canceled and cancel_cause weftbase.cancel", err, out)
	}

	t.Setenv("WEFTBASE_TOKEN", "")
	if _, errb, code := cli("runs"); code != 1 || !strings.Contains(errb, "401") || !strings.Contains(errb, "WEFTBASE_TOKEN") {
		t.Errorf("no token = exit %d %q, want 1 and server's 401", code, errb)
	}
	if _, errb, code := cli("runs", "--url", "http://127.0.0.1:1"); code != 1 || !strings.Contains(errb, "weftbase not reachable at http://127.0.0.1:1") {
		t.Errorf("unreachable = exit %d %q", code, errb)
	}
}

// TestOpenCommand pins `weftbase open <id>`: the run is checked against
// GET /v1/runs/<id>, its page <url>/runs/<id> printed (never a
// credential) and handed to the browser only with --open; an unknown
// run is server's 404; a child id travels escaped (GET
// /v1/runs/p%2F0%2Fc).
func TestOpenCommand(t *testing.T) {
	url, _ := apiServer(t, true)
	t.Setenv("WEFTBASE_URL", url)
	t.Setenv("WEFTBASE_TOKEN", "")
	takeOpened()
	out, errb, code := cli("open", "r_ok", "--token", tok)
	if want := url + "/runs/r_ok\n"; code != 0 || out != want {
		t.Errorf("weftbase open = exit %d %q %q, want the bare %q", code, out, errb, want)
	}
	if links := takeOpened(); len(links) != 0 {
		t.Errorf("without --open the browser got %q", links)
	}
	out, _, code = cli("open", "--open", "--token", tok, "r_ok")
	if code != 0 || out != url+"/runs/r_ok\n" {
		t.Errorf("--open = exit %d, stdout %q, want the link", code, out)
	}
	if links := takeOpened(); len(links) != 1 || links[0] != url+"/runs/r_ok" {
		t.Errorf("--open handed the browser %q, want the link", links)
	}
	// A child run's id carries slashes: escaped on the wire and in the link.
	out, errb, code = cli("open", "p/0/c", "--token", tok)
	if want := url + "/runs/p%2F0%2Fc\n"; code != 0 || out != want {
		t.Errorf("child id = exit %d %q %q, want %q", code, out, errb, want)
	}
	// An id no kind knows names what was tried (UX-S5a-06).
	if _, errb, code := cli("open", "p/9/nope", "--token", tok); code != 1 || !strings.Contains(errb, "p/9/nope is no run, public id, session, trace this server knows") {
		t.Errorf("unknown child = exit %d %q, want the kinds tried", code, errb)
	}
	// A trace id opens the trace's page.
	b, err := rest.New(url, tok).Get(context.Background(), rest.RunPath("r_ok"), nil)
	var row struct {
		TraceID string `json:"trace_id"`
	}
	if err != nil || json.Unmarshal(b, &row) != nil || row.TraceID == "" {
		t.Fatalf("r_ok's trace id: %v %s", err, b)
	}
	if out, errb, code := cli("open", row.TraceID, "--token", tok); code != 0 || out != url+"/traces/"+row.TraceID+"\n" {
		t.Errorf("a trace id = exit %d %q %q, want its trace page", code, out, errb)
	}
	// No id: the server root, checked with the key.
	if out, errb, code := cli("open", "--token", tok); code != 0 || out != url+"/\n" {
		t.Errorf("no id = exit %d %q %q, want the root", code, out, errb)
	}
	if _, errb, code := cli("open", "a", "b"); code != 2 || !strings.Contains(errb, "at most one id") {
		t.Errorf("two ids = exit %d %q", code, errb)
	}
	if got := rest.RunLink("http://h", "p/1/c"); got != "http://h/runs/p%2F1%2Fc" {
		t.Errorf("a child id's link = %s", got)
	}
}

// TestClientRedirectAndRedaction pins the API client's two guards: a
// redirect is an error naming its target, never followed (the bearer
// would travel with it); a password in --url never reaches stderr.
func TestClientRedirectAndRedaction(t *testing.T) {
	var followed bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redir.Close()
	_, errb, code := cli("runs", "--url", redir.URL, "--token", tok)
	if code != 1 || !strings.Contains(errb, "302, a redirect to "+target.URL) || !strings.Contains(errb, "not followed") || followed {
		t.Errorf("redirect = exit %d %q (followed %v), want an error, not followed", code, errb, followed)
	}
	_, errb, code = cli("runs", "--url", "http://user:s3cret@127.0.0.1:1")
	if code != 1 || strings.Contains(errb, "s3cret") || !strings.Contains(errb, "weftbase not reachable at http://user:xxxxx@127.0.0.1:1") {
		t.Errorf("unreachable with a password = exit %d %q, want it redacted", code, errb)
	}
}

// TestRunsCursorThatDoesNotMove: a server whose cursor repeats ends
// the listing instead of looping.
func TestRunsCursorThatDoesNotMove(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"total":9,"runs":[{"id":"r1","agent":"a","status":"succeeded","started":"2026-10-08T09:00:00Z","steps":1}],"next_before":"2026-10-08T09:00:00Z","next_before_id":"r1"}`)
	}))
	defer ts.Close()
	out, errb, code := cli("runs", "--url", ts.URL, "--limit", "0")
	if code != 0 || calls != 2 || strings.Count(out, "r1") != 2 || !strings.Contains(errb, "cursor did not move") {
		t.Errorf("a stuck cursor = exit %d after %d calls\n%s%s", code, calls, out, errb)
	}
	calls = 0
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"total":0,"runs":[],"next_before":"2026-10-08T09:00:00Z","next_before_id":null}`)
	}))
	defer empty.Close()
	if _, _, code := cli("runs", "--url", empty.URL); code != 0 || calls != 1 {
		t.Errorf("an empty page with a cursor = exit %d after %d calls, want one call", code, calls)
	}
}

// TestExportCommand: `weftbase export <id>` writes GET
// /v1/runs/<id>/export to stdout, json by default, jsonl and otlp on
// --format; any other format, no id, and an unknown flag are usage
// errors; an unknown run is the server's 404.
func TestExportCommand(t *testing.T) {
	url, _ := apiServer(t, false)
	t.Setenv("WEFTBASE_URL", url)
	t.Setenv("WEFTBASE_TOKEN", tok)

	out, errb, code := cli("export", "r_ok")
	if code != 0 || !strings.Contains(out, `"format":"weft.run.export/1"`) {
		t.Errorf("export to stdout = exit %d %.200q %q", code, out, errb)
	}
	if out, _, code := cli("export", "r_ok", "--format", "jsonl"); code != 0 || strings.Count(out, "\n") < 3 {
		t.Errorf("jsonl = exit %d %.200q", code, out)
	}
	if out, _, code := cli("export", "r_ok", "--format", "otlp"); code != 0 || !strings.Contains(out, `"traces"`) {
		t.Errorf("otlp = exit %d %.200q", code, out)
	}
	for _, args := range [][]string{
		{"export", "r_ok", "--format", "wefttest"},
		{"export", "r_ok", "--format", "xml"},
		{"export", "r_ok", "--wefttest", "dir"},
		{"export"},
	} {
		if _, errb, code := cli(args...); code != 2 {
			t.Errorf("weft %q = exit %d %q, want a usage error", args, code, errb)
		}
	}
	if _, errb, code := cli("export", "nope"); code != 1 || !strings.Contains(errb, "404") {
		t.Errorf("unknown run = exit %d %q", code, errb)
	}
}

// TestRunsJSONCarriesProblems pins `weftbase runs --json` (U4-fix): the rows
// are server's, so a failed run's error_type and a run's problem
// counters ride them — a Fallback answering both steps after the
// primary's failed try (attempts_retried 2, fallbacks 2, keyed on the
// attempt's weft.attempt.layer), a call to an unknown tool (tool_errors
// 1) beside a ModelRetry repair (no tool error) — and a clean field is
// omitted.
func TestRunsJSONCarriesProblems(t *testing.T) {
	srv := newServer(t, tok)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = srv.Close() })
	ctx := context.Background()
	p, err := exporter.Start(ctx, exporter.Remote(ts.URL, tok), otel.NoGlobal())
	if err != nil {
		t.Fatal(err)
	}
	prov := []core.Option{core.TracerProvider(p.TracerProvider()), core.LoggerProvider(p.LoggerProvider())}
	fix := core.Tool("fix", "Asks for a repair.", func(context.Context, orderIn) (string, error) {
		return "", core.ModelRetry("order_id must be numeric")
	})
	backup := wefttest.Script(
		wefttest.ToolCalls(wefttest.Call{Name: "nope", Args: `{}`, ID: "c1"}, wefttest.Call{Name: "fix", Args: `{"order_id":"x"}`, ID: "c2"}),
		wefttest.Say("done"))
	primary := wefttest.Script(wefttest.Fail(errors.New("503: down")))
	prob := core.New(primary, append([]core.Option{core.Name("orders"), fix, core.WrapModel(mw.Fallback(backup))}, prov...)...)
	if _, err := prob.Generate(ctx, core.RunID("r_prob"), core.Prompt("go")); err != nil {
		t.Fatal(err)
	}
	down := core.New(wefttest.Script(wefttest.Fail(errors.New("upstream down"))), append([]core.Option{core.Name("triage")}, prov...)...)
	if _, err := down.Generate(ctx, core.RunID("r_fail"), core.Prompt("triage")); err == nil {
		t.Fatal("the failing run succeeded")
	}
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEFTBASE_URL", ts.URL)
	t.Setenv("WEFTBASE_TOKEN", tok)
	var rows []map[string]any
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		out, errb, code := cli("runs", "--json")
		if code != 0 {
			t.Fatalf("weftbase runs --json = exit %d %s", code, errb)
		}
		rows = nil
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("--json: %v\n%s", err, out)
		}
		if len(rows) == 2 && rows[0]["status"] == "failed" && rows[1]["tool_errors"] != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rows never settled: %s", out)
		}
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[r["id"].(string)] = r
	}
	if r := byID["r_fail"]; r["error_type"] != "model_error" {
		t.Errorf("r_fail error_type = %v, want model_error", r["error_type"])
	}
	want := map[string]any{"attempts_retried": float64(2), "fallbacks": float64(2), "tool_errors": float64(1)}
	r := byID["r_prob"]
	for k, v := range want {
		if r[k] != v {
			t.Errorf("r_prob %s = %v, want %v", k, r[k], v)
		}
	}
	for _, k := range []string{"error_type", "denied", "truncated", "repaired", "log_warn", "log_error"} {
		if v, ok := r[k]; ok {
			t.Errorf("r_prob carries %s = %v; a clean field is omitted", k, v)
		}
	}
}

// TestUsageWords (UX-S5a-11): a usage error names what it was given in
// words — "none", or the arguments quoted — never a Go slice, and -h
// spells every flag --name, as the usage lines and the docs do.
func TestUsageWords(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"export"}, "export needs exactly one run id (got none)"},
		{[]string{"export", "a", "b"}, `export needs exactly one run id (got "a" "b")`},
		{[]string{"open", "a", "b"}, `(got "a" "b")`},
		{[]string{"runs", "x"}, `runs takes no arguments (got "x")`},
		{[]string{"vacuum", "x"}, `vacuum takes no arguments (got "x")`},
		{[]string{"doctor", "x"}, `doctor takes no arguments (got "x")`},
	} {
		_, errb, code := cli(c.args...)
		if code != 2 || !strings.Contains(errb, c.want) || strings.Contains(errb, "[]") {
			t.Errorf("weft %v = %d %q, want %q", c.args, code, errb, c.want)
		}
	}
	// weftbase dev -h (the server alone) names the dev loop's flags too.
	if _, errb, _ := cli("dev", "-h"); !strings.Contains(errb, "--watch dir") || !strings.Contains(errb, "--no-watch") {
		t.Errorf("weftbase dev -h does not list --watch and --no-watch:\n%s", errb)
	}
	for _, cmd := range []string{"open", "runs", "export", "doctor", "vacuum", "mcp", "dev", "signin", "signout"} {
		_, errb, _ := cli(cmd, "-h")
		for _, l := range strings.Split(errb, "\n") {
			if strings.HasPrefix(l, "  -") && !strings.HasPrefix(l, "  --") {
				t.Errorf("weft %s -h: %q spells a flag with one dash", cmd, l)
			}
		}
		if !strings.Contains(errb, "  --") {
			t.Errorf("weft %s -h lists no --flag:\n%s", cmd, errb)
		}
	}
}
