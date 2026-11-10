//go:build livecheck

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/weftbase/weftbase/client/discovery"
	"github.com/weftbase/weftbase/client/rest"
)

// TestMCPLiveCheck is `make mcp-check` (plan I4.2): the phase 6 gate's
// third clause — "`weftbase mcp` answers 'why did run X fail'" — proven end
// to end with no model and no fake: the real `weft` binary built, a real
// `weftbase dev` on a free loopback port over a scratch SQLite file, the
// record contract's fixtures (real recorded pipeline output) POSTed to
// its OTLP ingest, then `weftbase mcp` spawned and driven over stdio by the
// MCP SDK's client, the way Claude Code or Cursor drives it — once with
// --url and WEFTBASE_TOKEN, once with no flag at all (the discovery
// file the scratch server wrote). Each tool's summary line is printed;
// the last line is "MCP CHECK PASS". docs/mcp-recipe.md's worked
// examples are this output.
//
// It never touches the developer's discovery file or server: both
// processes run in a scratch directory with XDG_RUNTIME_DIR,
// XDG_CACHE_HOME and HOME pointed into it (where a starting server
// writes dev.json and where the reader looks), WEFTBASE_URL/TOKEN
// unset, and WEFTBASE_DISCOVERY=off only on the --url spawn — the server
// must write its file (none is written under off) and the no-flag spawn
// must read it, both inside scratch; the developer's own discovery
// files are compared before and after.
// Behind the livecheck tag: it starts a server on a port, so `make test`
// never runs it.
func TestMCPLiveCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	before := userDiscovery()
	defer func() {
		if after := userDiscovery(); after != before {
			t.Errorf("the developer's discovery files changed:\nbefore %s\nafter  %s", before, after)
		}
	}()
	scratch := t.TempDir()
	home := filepath.Join(scratch, "home")
	run := filepath.Join(scratch, "run")
	for _, d := range []string{home, run, filepath.Join(scratch, "cache")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := mcpCheckEnv(scratch, home)

	// 1. The binary, built as a user would `go install` it.
	bin := filepath.Join(scratch, "weftbase")
	if b, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "../../cmd/weftbase").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	say("built %s", "weftbase")

	// 2. A real server on a free loopback port.
	addr := freeAddr(t)
	data := filepath.Join(scratch, "data")
	server := exec.Command(bin, "dev", "--data", data, "--addr", addr)
	server.Dir, server.Env = home, env // discovery on: it writes scratch/run/weft/dev.json
	var devOut syncBuffer
	server.Stdout, server.Stderr = &devOut, &devOut
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = server.Wait(); close(exited) }()
	stop := func() {
		select {
		case <-exited:
			return
		default:
		}
		_ = server.Process.Signal(os.Interrupt)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = server.Process.Kill()
			<-exited
		}
	}
	defer func() {
		stop()
		if t.Failed() {
			t.Logf("server output:\n%s", devOut.String())
		}
	}()
	base := "http://" + addr
	token := waitServer(t, ctx, base, filepath.Join(data, "bootstrap.key"), exited, &devOut)
	say("weftbase %s (data <scratch>/data, the bootstrap key from <data>/bootstrap.key, never printed)", base)
	if _, err := os.Stat(filepath.Join(run, "weftbase", "dev.json")); err != nil {
		t.Fatalf("the scratch server wrote no discovery file in the scratch XDG_RUNTIME_DIR: %v", err)
	}

	// 3. The record contract's fixtures through the OTLP ingest. Every
	// generated case numbers its ids from 1 (run a110…1, trace 7ace…1,
	// span 5ba0…1), so all but failed get a case digit in the id's
	// prefix; stock_genai's derived ids and session_turns' session ids
	// are their own.
	cases := []struct {
		name, digit string
	}{{"failed", ""}, {"parked", "2"}, {"interrupted", "3"}, {"tool_calls", "4"}, {"session_turns", "5"}, {"stock_genai", ""}}
	for _, c := range cases {
		for _, sig := range []string{"traces", "logs"} {
			b, err := os.ReadFile(filepath.Join("..", "..", "obsdb", "testdata", "contract", c.name, sig+".json"))
			if os.IsNotExist(err) && sig == "logs" {
				continue // stock_genai: spans only
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.digit != "" {
				b = renumber(b, c.digit)
			}
			post(t, ctx, base+"/otlp/v1/"+sig, token, b)
		}
	}
	const (
		failedID  = "a1100000000000000000000000000001"
		parkedID  = "a1100002000000000000000000000001"
		interrID  = "a1100003000000000000000000000001"
		toolsID   = "a1100004000000000000000000000001"
		sessionID = "01K00000000000000000000001"
		turnID    = sessionID + "-t1"
		derivedID = "otel-5c0a3e1f9b8d4c2e8a7f6b5d4c3e2f10-1a2b3c4d5e6f7081"
	)
	c := rest.New(base, token)
	for id, want := range map[string]string{failedID: `"status":"failed"`, parkedID: `"pending":1`,
		interrID: `"status":"interrupted"`, toolsID: `"status":"succeeded"`, turnID: `"status":"succeeded"`,
		derivedID: `"status":"failed"`} {
		for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			b, err := c.Get(ctx, rest.RunPath(id), nil)
			if err == nil && bytes.Contains(b, []byte(want)) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("run %s never read %s: %s %v", id, want, b, err)
			}
		}
	}
	say("seeded failed, parked, interrupted, tool_calls, session_turns, stock_genai (obsdb/testdata/contract) through /otlp/v1/traces and /otlp/v1/logs")

	// 4. `weftbase mcp --url …`, the token from WEFTBASE_TOKEN (the
	// recommended form: an argument shows in ps, an env value does not),
	// over stdio, driven by the SDK client.
	cs, mcpErr := spawnMCP(t, ctx, bin, home, append(env, "WEFTBASE_DISCOVERY=off", "WEFTBASE_TOKEN="+token), "--url", base)
	defer func() { _ = cs.Close() }()
	if !strings.Contains(cs.InitializeResult().Instructions, "READ-ONLY") {
		t.Errorf("instructions %q, want READ-ONLY", cs.InitializeResult().Instructions)
	}
	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	say("weftbase mcp --url %s (token from WEFTBASE_TOKEN): initialize ok, %d tools listed", base, len(lt.Tools))

	call := func(cs *sdk.ClientSession, tool string, args map[string]any, want ...string) []string {
		t.Helper()
		res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		got := texts(t, res)
		a, _ := json.Marshal(args)
		if args == nil {
			a = []byte("{}")
		}
		fmt.Printf("\n> %s %s\n%s\n", tool, a, got[0])
		if len(got) > 1 {
			fmt.Printf("%s\n", head(got[1], 240))
		}
		if n := got[len(got)-1]; len(got) > 2 && strings.HasPrefix(n, "[truncated: ") {
			fmt.Printf("%s\n", n)
		}
		if res.IsError {
			t.Fatalf("%s: error %q", tool, got)
		}
		all := strings.Join(got, "\n")
		for _, w := range want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: answer lacks %q:\n%.600s", tool, w, all)
			}
		}
		return got
	}
	explained := func(got []string) explanation {
		t.Helper()
		var ex explanation
		if err := json.Unmarshal([]byte(got[1]), &ex); err != nil {
			t.Fatalf("explain_failure: %v\n%s", err, got[1])
		}
		return ex
	}

	call(cs, "meta", nil, `"capabilities"`)
	call(cs, "runs", map[string]any{"limit": 10}, "runs: 8 runs of 8", failedID, parkedID, interrID, toolsID, derivedID)
	ex := explained(call(cs, "explain_failure", map[string]any{"id": failedID},
		"run "+failedID+" (support): failed before step 1 started (step 0, its last recorded step, finished): ",
		"maximum number of steps", base+"/runs/"+failedID))
	if ex.Status != "failed" || ex.Step == nil || *ex.Step != 0 || len(ex.Next) == 0 {
		t.Errorf("explain_failure %s: %+v", failedID, ex)
	}
	fmt.Printf("next: %s\n", strings.Join(ex.Next, " | "))
	call(cs, "run", map[string]any{"id": failedID}, "run "+failedID+" (support): failed, 1 step: weft: run failed at step 1")
	call(cs, "step", map[string]any{"id": failedID, "n": 0}, "run "+failedID+" step 0: ok (tool_calls); tool calls: lookup_order ok")
	call(cs, "request", map[string]any{"id": failedID, "step": 0, "transcript": true},
		"run "+failedID+" requests: 1 request; then the transcript of step 0", `"messages"`)
	call(cs, "events", map[string]any{"id": failedID, "limit": 3}, "run "+failedID+" events: 3 events (pos 0–2); more: after=3; the run is terminal",
		`"run_terminal":true`)
	call(cs, "tools", map[string]any{"id": failedID}, "run "+failedID+": tool catalogs", "lookup_order")
	call(cs, "search", map[string]any{"q": "exceeded"}, "search: 1 run of 1", failedID)
	call(cs, "search", map[string]any{"tool": "lookup_order", "status": "failed"}, "search: 1 run of 1", failedID)
	// U33.1: the error_class facet names the failures' classes; a class
	// narrows search and runs; stats answers the groups, compact.
	got := call(cs, "search", map[string]any{"status": "failed", "all": true}, "; error classes: ", `"error_class":[{"class":"`)
	var facets struct {
		Facets struct {
			ErrorClass []struct {
				Class string `json:"class"`
				Count int    `json:"count"`
			} `json:"error_class"`
		} `json:"facets"`
	}
	if err := json.Unmarshal([]byte(got[1]), &facets); err != nil || len(facets.Facets.ErrorClass) == 0 {
		t.Fatalf("search's error_class facet: %v %s", err, got[1])
	}
	top := facets.Facets.ErrorClass[0]
	want := count(top.Count, "run", "runs")
	call(cs, "search", map[string]any{"error_class": top.Class, "all": true}, "search: "+want+" of "+strconv.Itoa(top.Count))
	call(cs, "runs", map[string]any{"error_class": top.Class, "all": true}, "runs: "+want)
	call(cs, "stats", map[string]any{"by": "agent", "since": "2000-01-01T00:00:00Z", "until": "2100-01-01T00:00:00Z"}, "stats by agent, ", `"key":"support"`, `"runs_query":"`)
	call(cs, "stats", map[string]any{"by": "tool", "since": "2000-01-01T00:00:00Z", "until": "2100-01-01T00:00:00Z"}, "stats by tool, ", `"key":"lookup_order"`)
	call(cs, "diff", map[string]any{"a": failedID, "b": toolsID},
		"diff "+failedID+" → "+toolsID+": 1 changed step, first at step 1", `"summary"`)
	call(cs, "export", map[string]any{"id": failedID, "format": "jsonl", "max_bytes": 2048},
		"run "+failedID+" exported as jsonl", "[truncated: showing 2048 of ")
	ex = explained(call(cs, "explain_failure", map[string]any{"id": parkedID}, "; parked: 1 call awaiting a decision (refund_order)"))
	if ex.Pending != 1 {
		t.Errorf("explain_failure %s: pending %d, want 1", parkedID, ex.Pending)
	}
	ex = explained(call(cs, "explain_failure", map[string]any{"id": interrID}, "run "+interrID+" (support): interrupted at step 0", "holes: interrupted"))
	if ex.Status != "interrupted" {
		t.Errorf("explain_failure %s: status %q, want interrupted", interrID, ex.Status)
	}
	ex = explained(call(cs, "explain_failure", map[string]any{"id": derivedID}, "failed at step 0", "holes: derived"))
	if !strings.Contains(string(ex.RunHoles), "derived") {
		t.Errorf("explain_failure %s: holes %s, want derived", derivedID, ex.RunHoles)
	}
	call(cs, "sessions", nil, "sessions: 2 sessions of 2", sessionID)
	call(cs, "session", map[string]any{"id": sessionID}, "session "+sessionID, turnID)

	// 4b. The investigation shapes (I5): explain_failure walks every
	// step and the children, reads this run's own text, and reports a
	// decided park as decided; a nine-turn session answers compact.
	seedInvestigations(t, ctx, base, token)
	child := chkSubFailed + "/0/q1"
	ex = explained(call(cs, "explain_failure", map[string]any{"id": chkSubFailed},
		"run "+chkSubFailed+" (checkout-assistant): succeeded, last step 1",
		"first error: step 0 shipping_quote → child "+child+" (shipping-quoter) step 0: ", "carrier API outage: DHL rates unavailable",
		`last assistant text "Express quotes are down`))
	if ex.FirstError == nil || ex.FirstError.Run != child || !strings.Contains(ex.FirstError.Error, "carrier API outage") {
		t.Errorf("explain_failure %s: first_error %+v, want the child's error", chkSubFailed, ex.FirstError)
	}
	fe, _ := json.Marshal(ex.FirstError)
	fmt.Printf("first_error: %s\ndigest:\n  %s\n", fe, strings.Join(ex.Digest, "\n  "))
	ex = explained(call(cs, "explain_failure", map[string]any{"id": chkChaos},
		"run "+chkChaos+" (deal-lead): succeeded", "first error: step 0 review_ip → child ",
		"SUBAGENT_FAILED", "provider maintenance window", // the IP reviewer's provider is down
		"did not answer within 150ms",                            // the tax reviewer ran past its Timeout (weft 0.23: OUTCOME_UNKNOWN)
		"SUBAGENT_PENDING", "parked: 1 call awaiting a decision", // the NDA signer parked inside the delegation
		"ocr engine: nil page")) // the litigation reviewer's tool panicked, the reviewer recovered
	fmt.Printf("digest:\n  %s\n", strings.Join(ex.Digest, "\n  "))
	t1, t2 := chkOpsSess+"-t1", chkOpsSess+"-t2"
	ex = explained(call(cs, "explain_failure", map[string]any{"id": t1},
		"; parked, decided: deploy denied by alice: change freeze until Monday → resumed as "+t2,
		"tool calls: deploy denied by alice", "no assistant text this run"))
	if ex.LastAssistantText != nil || strings.Contains(strings.Join(ex.Digest, " "), "awaiting") {
		t.Errorf("explain_failure %s: %+v, want no text and no awaiting", t1, ex)
	}
	call(cs, "explain_failure", map[string]any{"id": t2},
		"resumed "+t1+": deploy denied by alice", `last assistant text "Deploy to prod cancelled`)
	ex = explained(call(cs, "explain_failure", map[string]any{"id": chkChatSess + "-t5"},
		`last assistant text "Turn 5: order A-1005`, "lookup_order ok"))
	if ex.LastAssistantText == nil || !strings.HasPrefix(*ex.LastAssistantText, "Turn 5:") {
		t.Errorf("explain_failure t5: last text %v, want turn 5's own", ex.LastAssistantText)
	}
	call(cs, "explain_failure", map[string]any{"id": chkChatSess + "-t2"}, `last assistant text "Turn 2: order A-1002`)
	call(cs, "explain_failure", map[string]any{"id": chkChatSess + "-t3"},
		"first error: step 0 lookup_order: order service timed out after 5s looking up A-1003", `last assistant text "Turn 3: I could not reach`)
	got = call(cs, "session", map[string]any{"id": chkChatSess}, "session "+chkChatSess+" (support-desk): succeeded, 9 turns",
		`"cost_reasons":[`, `"turn_defaults":{`, `"tool_errors":1`)
	size := len(strings.Join(got, "\n"))
	full := call(cs, "session", map[string]any{"id": chkChatSess, "full": true}, "9 turns")
	fmt.Printf("session %s: %d bytes compact (%d as served)\n", chkChatSess, size, len(strings.Join(full, "\n")))
	if size >= 4096 {
		t.Errorf("a nine-turn session answers %d bytes, want under 4 KB", size)
	}
	call(cs, "spans", map[string]any{"id": chkSubFailed, "tree": true}, "run "+chkSubFailed+" spans (tree): ", `"run_id":"`+child+`"`)
	call(cs, "logs", map[string]any{"id": chkSubFailed}, "run "+chkSubFailed+" logs: ")
	call(cs, "transcript", map[string]any{"id": t2}, "run "+t2+" transcript: ")
	call(cs, "live", nil, "live: running now: 0 runs")
	_ = cs.Close()
	if strings.Contains(mcpErr.String(), token) || !strings.Contains(mcpErr.String(), "token from WEFTBASE_TOKEN") {
		t.Errorf("weftbase mcp stderr %q: want the start line naming WEFTBASE_TOKEN, never the token", mcpErr.String())
	}

	// 5. `weftbase mcp` with no flag and no token: the path `claude mcp add
	// weft -- weftbase mcp` takes. WEFTBASE_DISCOVERY unset, so it reads the
	// discovery file the scratch server wrote under the scratch
	// XDG_RUNTIME_DIR, and takes that server's URL and token from it.
	cs2, mcpErr2 := spawnMCP(t, ctx, bin, home, env)
	defer func() { _ = cs2.Close() }()
	say("weftbase mcp (no flags; discovery file): initialize ok, server %s", cs2.InitializeResult().ServerInfo.Name)
	call(cs2, "meta", nil, "Server at "+base+": capabilities ")
	_ = cs2.Close()
	if s := mcpErr2.String(); strings.Contains(s, token) || !strings.Contains(s, "for the weftbase server at "+base+" (token from the discovery file)") {
		t.Errorf("no-flag weftbase mcp stderr %q: want the server %s with the token from the discovery file, never the token", s, base)
	}

	stop()
	if _, err := os.Stat(filepath.Join(run, "weftbase", "dev.json")); !os.IsNotExist(err) {
		t.Errorf("the stopped server left its discovery file: %v", err)
	}
	if t.Failed() {
		return
	}
	fmt.Println("\nweftbase stopped; its discovery file removed; the developer's discovery files unchanged")
	fmt.Println("MCP CHECK PASS")
}

// spawnMCP starts `weftbase mcp args…` and connects the SDK client to it
// over stdio; stderr is collected.
func spawnMCP(t *testing.T, ctx context.Context, bin, dir string, env []string, args ...string) (*sdk.ClientSession, *syncBuffer) {
	t.Helper()
	cmd := exec.Command(bin, append([]string{"mcp"}, args...)...)
	cmd.Dir, cmd.Env = dir, env
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "mcp-check", Version: "0"}, nil).
		Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect weftbase mcp %q: %v (stderr %q)", args, err, stderr.String())
	}
	return cs, stderr
}

// mcpCheckEnv is the environment every process runs with: the
// caller's, minus anything that could name another server or reach the
// developer's discovery file, the discovery locations inside scratch.
// WEFTBASE_DISCOVERY is removed; only the --url mcp spawn adds "off" (the
// Server writes the scratch discovery file, the no-flag spawn reads it).
func mcpCheckEnv(scratch, home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "WEFTBASE_URL", "WEFTBASE_TOKEN", "WEFTBASE_DISCOVERY", "WEFTBASE_DB", "WEFTBASE_DATA", "WEFT_MANIFEST",
			"WEFTBASE_ADDR", "WEFT_RETAIN", "WEFT_PRICING", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "HOME":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "WEFT_MODEL_REQUESTS=deny", "HOME="+home,
		"XDG_RUNTIME_DIR="+filepath.Join(scratch, "run"), "XDG_CACHE_HOME="+filepath.Join(scratch, "cache"))
}

// userDiscovery is the developer's own discovery files (the default
// locations from this process's environment and working directory),
// each with its size and modification time, to compare before and
// after.
func userDiscovery() string {
	var b strings.Builder
	for _, d := range discovery.DefaultDirs() {
		p := filepath.Join(d, "dev.json")
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&b, "%s %d %s; ", p, fi.Size(), fi.ModTime().Format(time.RFC3339Nano))
		} else {
			fmt.Fprintf(&b, "%s absent; ", p)
		}
	}
	return b.String()
}

// renumber gives a contract case's ids a case digit: run ids a110…,
// trace ids 7ace…, span ids 5ba0… — lengths and hex kept.
func renumber(b []byte, digit string) []byte {
	for _, p := range []string{"a110000", "7ace000", "5ba0000"} {
		b = bytes.ReplaceAll(b, []byte(p+"0"), []byte(p+digit))
	}
	return b
}

// freeAddr is a loopback address no one listens on (now).
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// waitServer waits for the bootstrap key file and an authorised /v1/meta,
// failing at once with the server's output when the process exits.
func waitServer(t *testing.T, ctx context.Context, base, tokenFile string, exited <-chan struct{}, out *syncBuffer) string {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		select {
		case <-exited:
			t.Fatalf("weftbase dev exited before it answered %s/v1/meta:\n%s", base, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no server answered %s/v1/meta within 30s", base)
		}
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			continue
		}
		tok := strings.TrimSpace(string(b))
		if _, err := rest.New(base, tok).Get(ctx, "/v1/meta", nil); err == nil {
			return tok
		}
	}
}

// post sends one OTLP/JSON body to the ingest.
func post(t *testing.T, ctx context.Context, url, token string, body []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: %s", url, resp.Status)
	}
}

// head is s cut to n bytes on a rune boundary, "…" marking the cut.
func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// say is one progress line of the check.
func say(format string, args ...any) { fmt.Printf("mcp-check: "+format+"\n", args...) }
