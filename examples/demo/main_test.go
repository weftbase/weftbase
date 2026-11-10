package main

import (
	"context"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// row is the part of a run row (GET /v1/runs) the test reads.
type row struct {
	ID          string            `json:"id"`
	Agent       string            `json:"agent"`
	ParentRunID string            `json:"parent_run_id"`
	Status      string            `json:"status"`
	StoppedBy   string            `json:"stopped_by"`
	ErrorType   string            `json:"error_type"`
	ResumedFrom string            `json:"resumed_from"`
	ResumedBy   string            `json:"resumed_by"`
	SessionID   string            `json:"session_id"`
	Turn        int               `json:"turn"`
	Steps       int               `json:"steps"`
	Pending     int               `json:"pending"`
	ToolErrors  int               `json:"tool_errors"`
	CostUSD     *float64          `json:"cost_usd"`
	CostKnown   bool              `json:"cost_known"`
	Meta        map[string]string `json:"meta"`
}

// The demo boots, plays its first batch with no scripted latency, and
// /v1 then shows the variety the Console is built against.
func TestDemoFeedsTheAPI(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	type node struct{ url, key string }
	ready := make(chan node, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{addr: "127.0.0.1:0", dir: t.TempDir(), speed: 0, out: io.Discard},
			func(url, key string) { ready <- node{url, key} })
	}()
	t.Cleanup(func() {
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the demo did not stop")
		}
	})
	var c client
	select {
	case n := <-ready:
		c = client{base: n.url, key: n.key}
	case err := <-done:
		done <- err
		t.Fatalf("the demo ended early: %v", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("the first batch did not finish")
	}

	// The registry: two agents, two versions each, prod and staging.
	var agents struct {
		Agents []struct {
			Name     string            `json:"name"`
			Versions int               `json:"versions"`
			Labels   map[string]string `json:"labels"`
		} `json:"agents"`
	}
	if err := c.get(ctx, "/v1/agents", &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents.Agents) != 2 {
		t.Fatalf("agents = %+v", agents.Agents)
	}
	for _, a := range agents.Agents {
		if a.Versions != 2 || a.Labels["prod"] == "" || a.Labels["staging"] == "" || a.Labels["prod"] == a.Labels["staging"] {
			t.Errorf("agent %s: %d versions, labels %v", a.Name, a.Versions, a.Labels)
		}
	}

	// The runs reach the trace store over OTLP in batches: poll until
	// every kind is there.
	want := map[string]func(row) bool{
		"an embedded run with several steps": func(r row) bool { return r.Agent == "support" && r.Status == "succeeded" && r.Steps >= 3 },
		"a local run":                        func(r row) bool { return r.Agent == "refunds" && r.ParentRunID == "" },
		"a subagent's child run, unpriced":   func(r row) bool { return r.Agent == "billing" && r.ParentRunID != "" && !r.CostKnown },
		"a priced run":                       func(r row) bool { return r.CostKnown && r.CostUSD != nil && *r.CostUSD > 0 },
		"a run failed on a tool":             func(r row) bool { return r.Status == "failed" && r.ErrorType == "max_steps" && r.ToolErrors > 0 },
		"a run failed on the model":          func(r row) bool { return r.Status == "failed" && r.ErrorType == "model_error" },
		"a cancelled run":                    func(r row) bool { return r.ErrorType == "cancelled" },
		"a run waiting for a person":         func(r row) bool { return r.StoppedBy == "parked" && r.Pending == 1 && r.ResumedBy == "" },
		"a decided run and its resume":       func(r row) bool { return r.ResumedFrom != "" && r.Status == "succeeded" },
		"a session's third turn":             func(r row) bool { return strings.HasPrefix(r.SessionID, "ses_") && r.Turn == 3 },
	}
	var runs []row
	deadline := time.Now().Add(30 * time.Second)
	for {
		var page struct {
			Runs []row `json:"runs"`
		}
		if err := c.get(ctx, "/v1/runs?parent=*&limit=200", &page); err != nil {
			t.Fatal(err)
		}
		runs = page.Runs
		var missing []string
		for name, is := range want {
			if !slices.ContainsFunc(runs, is) {
				missing = append(missing, name)
			}
		}
		if len(missing) == 0 {
			break
		}
		if time.Now().After(deadline) {
			slices.Sort(missing)
			t.Fatalf("GET /v1/runs lacks: %s\nrows: %+v", strings.Join(missing, "; "), runs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Every run went through the ledger: it names its AgentVersion, and
	// its status says how the version was chosen.
	via := map[string]bool{}
	for _, r := range runs {
		if r.ParentRunID != "" {
			continue
		}
		if !strings.HasPrefix(r.Meta["weftbase.agent_version"], "agv_") {
			t.Errorf("run %s (%s) names no AgentVersion: %v", r.ID, r.Agent, r.Meta)
		}
		var st struct {
			ResolvedVia string `json:"resolved_via"`
		}
		if err := c.get(ctx, "/v1/runs/"+r.ID+"/status", &st); err != nil {
			t.Fatal(err)
		}
		via[st.ResolvedVia] = true
	}
	for _, v := range []string{"label:prod", "label:staging", "pin"} {
		if !via[v] {
			t.Errorf("no run resolved via %s: %v", v, via)
		}
	}

	// Exactly one approval is left to decide, with its arguments.
	var parked struct {
		Runs []row `json:"runs"`
	}
	if err := c.get(ctx, "/v1/runs?status=parked", &parked); err != nil || len(parked.Runs) != 1 {
		t.Fatalf("open approvals = %+v, %v", parked.Runs, err)
	}
	var st struct {
		Status  string `json:"status"`
		Pending []struct {
			Tool string `json:"tool"`
		} `json:"pending"`
	}
	if err := c.get(ctx, "/v1/runs/"+parked.Runs[0].ID+"/status", &st); err != nil || st.Status != "parked" || len(st.Pending) != 1 || st.Pending[0].Tool != "issue_refund" {
		t.Errorf("the pending run = %+v, %v", st, err)
	}

	// Sessions of several turns, and app logs on a run.
	var sessions struct {
		Sessions []struct {
			Turns int `json:"turns"`
		} `json:"sessions"`
	}
	if err := c.get(ctx, "/v1/sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	var turns []int
	for _, s := range sessions.Sessions {
		turns = append(turns, s.Turns)
	}
	slices.Sort(turns)
	if !slices.Equal(turns, []int{2, 3, 3}) {
		t.Errorf("session turns = %v, want sessions of 2, 3 and 3 runs", turns)
	}
	failed := runs[slices.IndexFunc(runs, want["a run failed on a tool"])]
	var logs struct {
		Logs []struct {
			Severity, Body string
		} `json:"logs"`
	}
	if err := c.get(ctx, "/v1/runs/"+failed.ID+"/logs?severity=error", &logs); err != nil || len(logs.Logs) == 0 || logs.Logs[0].Body != "address service unavailable" {
		t.Errorf("app logs of the failed run = %+v, %v", logs.Logs, err)
	}
}

// getAny reads path into a generic JSON value.
func getAny(t *testing.T, c client, path string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := c.get(context.Background(), path, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// at walks a decoded JSON value: keys for objects, ints for arrays.
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func list(v any, path ...any) []any {
	a, _ := at(v, path...).([]any)
	return a
}

// The control surface the Console is built on, over the demo's data:
// approvals as a resource, rows that say their state and version,
// chains, the inventory, the stats series and the conversation.
func TestDemoControlSurface(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	ready := make(chan client, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{addr: "127.0.0.1:0", dir: t.TempDir(), speed: 0, out: io.Discard},
			func(url, key string) { ready <- client{base: url, key: key} })
	}()
	t.Cleanup(func() { stop(); <-done })
	var c client
	select {
	case c = <-ready:
	case err := <-done:
		done <- err
		t.Fatalf("the demo ended early: %v", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("the first batch did not finish")
	}
	// Wait for the traces of the batch's last runs (OTLP batches).
	deadline := time.Now().Add(30 * time.Second)
	for {
		rows := list(getAny(t, c, "/v1/runs?parent=*&limit=200"), "runs")
		running, resumes, billing := 0, 0, 0
		for _, r := range rows {
			if at(r, "display_status") == "running" || at(r, "ledger_only") == true {
				running++
			}
			if at(r, "resumed_from") != "" && at(r, "resumed_from") != nil {
				resumes++
			}
			if at(r, "agent") == "billing" {
				billing++
			}
		}
		if running == 0 && resumes == 3 && billing == 1 && len(rows) >= 20 {
			// Rows come from records, which arrive before the spans do:
			// wait for every run's spans too.
			spanless := 0
			for _, r := range rows {
				if len(list(getAny(t, c, "/v1/runs/"+url.PathEscape(at(r, "id").(string))+"/spans"), "spans")) == 0 {
					spanless++
				}
			}
			if spanless == 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the batch's traces did not settle: %d rows, %d running, %d resumes", len(rows), running, resumes)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A. The local build's traces arrived with no wiring in the demo:
	// its exporter read what the supervisor handed it.
	refunds := list(getAny(t, c, "/v1/runs?agent=refunds"), "runs")
	if len(refunds) == 0 || at(refunds[0], "service") != "refunds-build" || at(refunds[0], "placement") != "local" {
		t.Fatalf("the local build's runs = %v", refunds)
	}

	// B. Approvals: one pending, the others decided with their decider
	// and the run that continued; a decision is bound to the arguments.
	inbox := getAny(t, c, "/v1/approvals")
	if at(inbox, "pending") != 1.0 || at(inbox, "total") != 4.0 || at(inbox, "approvals", 0, "status") != "pending" {
		t.Fatalf("approvals = %v", inbox)
	}
	pending := at(inbox, "approvals", 0)
	for _, a := range list(inbox, "approvals")[1:] {
		if at(a, "actor", "kind") != "key" || at(a, "resumed_by") == nil || at(a, "decided_at") == nil || at(a, "tool") != "issue_refund" {
			t.Errorf("decided approval = %v", a)
		}
	}
	path := "/v1/approvals/" + at(pending, "id").(string)
	if err := c.post(ctx, path+"/decision", map[string]any{"decision": "approve", "args_sha256": "0000"}, nil); err == nil || !strings.Contains(err.Error(), "args_changed") {
		t.Errorf("a decision on other arguments: %v, want 409 args_changed", err)
	}
	if got := getAny(t, c, path); at(got, "status") != "pending" || at(got, "args", "order_id") != "1077" {
		t.Errorf("the pending approval = %v", got)
	}

	// C and D. One row per logical run, each with its state, version
	// and placement; a decided refund is one chain with its wait.
	chains := list(getAny(t, c, "/v1/runs?chains=1&limit=200"), "runs")
	states, placements := map[any]int{}, map[any]bool{}
	var chain any
	for _, r := range chains {
		states[at(r, "display_status")]++
		placements[at(r, "placement")] = true
		if at(r, "resumed_from") != nil {
			t.Errorf("a resume is a row of its own: %v", at(r, "id"))
		}
		if n, _ := at(r, "agent_version", "number").(float64); n < 1 || at(r, "resolved_via") == nil {
			t.Errorf("row %v: version %v via %v", at(r, "id"), at(r, "agent_version"), at(r, "resolved_via"))
		}
		if len(list(r, "chain", "runs")) == 2 && at(r, "session_id") == "" {
			chain = r
		}
	}
	if states["waiting"] != 1 || states["cancelled"] != 1 || states["failed"] != 2 || states["succeeded"] < 8 || !placements["embedded"] || !placements["local"] {
		t.Errorf("display statuses = %v, placements = %v", states, placements)
	}
	if chain == nil || at(chain, "chain", "waits", 0, "decided_by", 0, "kind") != "key" || at(chain, "chain", "waits", 0, "to") == nil {
		t.Fatalf("no decided chain among the rows: %v", chain)
	}
	if w := list(getAny(t, c, "/v1/runs?display_status=waiting"), "runs"); len(w) != 1 || at(w[0], "id") != at(pending, "run_id") {
		t.Errorf("display_status=waiting = %v", w)
	}
	spans := getAny(t, c, "/v1/runs/"+at(chain, "id").(string)+"/spans?tree=1&chain=1")
	runsSeen := map[any]bool{}
	for _, s := range list(spans, "spans") {
		runsSeen[at(s, "run_id")] = true
	}
	if len(list(spans, "chain")) != 2 || len(list(spans, "waits")) != 1 || len(runsSeen) != 2 {
		t.Errorf("chain spans: chain %v, %d waits, spans of %d runs: %v", at(spans, "chain"), len(list(spans, "waits")), len(runsSeen), spans)
	}
	detail := getAny(t, c, "/v1/runs/"+at(chain, "id").(string))
	if at(detail, "decided", "actor_kind") != "key" || len(list(detail, "approvals")) != 1 || at(detail, "chain", "display_status") != at(chain, "display_status") {
		t.Errorf("run detail: decided %v, approvals %v", at(detail, "decided"), at(detail, "approvals"))
	}

	// E. The inventory.
	agents := getAny(t, c, "/v1/agents?include=summary")
	if at(agents, "totals", "agents") != 2.0 || len(list(agents, "totals", "placements")) != 2 || len(list(agents, "agents")) != 3 {
		t.Errorf("agents summary totals = %v (%d rows)", at(agents, "totals"), len(list(agents, "agents")))
	}
	for _, a := range list(agents, "agents") {
		switch at(a, "name") {
		case "billing":
			if at(a, "registered") != false {
				t.Errorf("billing = %v", a)
			}
		case "support":
			if at(a, "registered") != true || at(a, "summary", "subagents") != 1.0 || at(a, "summary", "placement") != "embedded" ||
				at(a, "summary", "model", "name") != "gpt-4o-mini" || at(a, "summary", "stats", "runs").(float64) < 8 {
				t.Errorf("support = %v", a)
			}
		}
	}
	agent := getAny(t, c, "/v1/agents/refunds")
	if at(agent, "placement") != "local" || at(agent, "version_list", 0, "changes", "tools_added", 0) != "offer_store_credit" ||
		at(agent, "by_label", "prod", "number") != 1.0 || at(agent, "newest", "manifest", "tools", 1, "needs_approval") != true {
		t.Errorf("agent refunds = %v", agent)
	}
	if d := getAny(t, c, "/v1/agents/support/versions/1/diff/2"); at(d, "tools_added", 0) != "handoff_to_human" || at(d, "instructions_changed") != false {
		t.Errorf("support 1 → 2 = %v", d)
	}
	tools := getAny(t, c, "/v1/tools")
	byName := map[any]any{}
	for _, tl := range list(tools, "tools") {
		byName[at(tl, "name")] = tl
	}
	if tl := byName["lookup_order"]; len(list(tl, "agents")) != 2 || at(tl, "stats", "calls").(float64) < 5 || at(tl, "stats", "duration_ms") == nil {
		t.Errorf("lookup_order = %v", tl)
	}
	if tl := byName["issue_refund"]; at(tl, "needs_approval") != true || at(tl, "agents", 0, "agent") != "refunds" {
		t.Errorf("issue_refund = %v", tl)
	}
	if tl := byName["update_address"]; at(tl, "stats", "failure_rate") != 1.0 {
		t.Errorf("update_address = %v", tl)
	}

	// F. The stats: one series by outcome, with timestamps and cost.
	total := getAny(t, c, "/v1/stats?by=total&since=1h&buckets=12")
	if len(list(total, "series")) != 12 || len(list(total, "bucket_starts")) != 12 || at(total, "bucket_ms") != 300000.0 {
		t.Fatalf("total series = %v", total)
	}
	tot := at(total, "total")
	if at(tot, "failed") != 2.0 || at(tot, "cancelled") != 1.0 || at(tot, "waiting") != 1.0 || at(tot, "runs") != float64(len(chains)) ||
		at(tot, "cost_usd").(float64) <= 0 || at(tot, "duration_ms", "p95") == nil || at(tot, "failure_rate").(float64) >= 0.5 {
		t.Errorf("total = %v (rows with chains: %d)", tot, len(chains))
	}
	if at(total, "series", 11, "runs") != at(tot, "runs") {
		t.Errorf("the last bucket = %v", at(total, "series", 11))
	}
	byAgent := getAny(t, c, "/v1/stats?by=agent&since=1h")
	for _, g := range list(byAgent, "groups") {
		if at(g, "key") == "billing" {
			t.Error("by=agent counts a subagent's child run by default")
		}
		if at(g, "key") == "support" && (at(g, "outcomes", "cancelled") != 1.0 || at(g, "outcomes", "failed") != 2.0 || len(list(g, "outcome_series")) != 24) {
			t.Errorf("support's outcomes = %v", at(g, "outcomes"))
		}
	}

	// G. A session as a conversation: a park and its resume are one
	// turn, with the wait and who decided.
	for _, s := range list(getAny(t, c, "/v1/sessions"), "sessions") {
		conv := getAny(t, c, "/v1/sessions/"+at(s, "id").(string)+"/conversation")
		turns := list(conv, "turns")
		if at(conv, "agent") != "refunds" {
			if at(turns[0], "input_known") != true || at(turns[len(turns)-1], "reply") == nil || at(turns[0], "model_calls").(float64) < 2 {
				t.Errorf("conversation %v = %v", at(s, "id"), conv)
			}
			continue
		}
		first := turns[0]
		if len(turns) != 2 || at(conv, "total_turns") != 2.0 || len(list(first, "runs")) != 2 || at(first, "display_status") != "succeeded" ||
			at(first, "waits", 0, "approvals", 0, "status") != "approved" || at(first, "tool_calls") != 3.0 ||
			!strings.HasPrefix(at(first, "reply").(string), "Done: $64.00") || at(first, "input") != "Refund order #1042, the kettle arrived cracked." ||
			at(turns[1], "reply") != "You're welcome." {
			t.Errorf("the refund conversation = %v", conv)
		}
	}
}
