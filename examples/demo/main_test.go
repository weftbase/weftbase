package main

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

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

func num(v any, path ...any) float64 {
	f, _ := at(v, path...).(float64)
	return f
}

func str(v any, path ...any) string {
	s, _ := at(v, path...).(string)
	return s
}

// find is the first element of rows that is.
func find(rows []any, is func(any) bool) any {
	for _, r := range rows {
		if is(r) {
			return r
		}
	}
	return nil
}

// The demo boots, plays its first batch with no scripted latency, and
// /v1 then shows everything the Console is built against: each subtest
// is one thing a screen needs, read from the real API over real data.
func TestDemo(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	ready := make(chan client, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, config{addr: "127.0.0.1:0", dir: t.TempDir(), speed: 0, out: io.Discard},
			func(url, key string) { ready <- client{base: url, key: key} })
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
	case c = <-ready:
	case err := <-done:
		done <- err
		t.Fatalf("the demo ended early: %v", err)
	case <-time.After(3 * time.Minute):
		t.Fatal("the first batch did not finish")
	}

	// The runs reach the trace store over OTLP in batches (records
	// first, spans up to a second later): wait until nothing runs and
	// every run has its spans.
	var all []any
	deadline := time.Now().Add(40 * time.Second)
	for {
		all = list(getAny(t, c, "/v1/runs?parent=*&limit=300"), "runs")
		unsettled := 0
		for _, r := range all {
			switch {
			case at(r, "display_status") == "running", at(r, "display_status") == "queued", at(r, "settling") == true:
				unsettled++
			case len(list(getAny(t, c, "/v1/runs/"+url.PathEscape(str(r, "id"))+"/spans"), "spans")) == 0:
				unsettled++
			}
		}
		// 43 or 44 rows: 14 support and children, 11 refunds, 5 extras and
		// their resume, 13 desk, 1 derived (see the subtests).
		if unsettled == 0 && len(all) >= 43 {
			break
		}
		if time.Now().After(deadline) {
			count := map[any]int{}
			for _, r := range all {
				count[at(r, "agent")]++
			}
			t.Fatalf("the batch's traces did not settle: %d rows, %d unsettled: %v", len(all), unsettled, count)
		}
		time.Sleep(50 * time.Millisecond)
	}
	chains := list(getAny(t, c, "/v1/runs?chains=1&limit=300"), "runs")
	byAgent := func(rows []any, agent string) []any {
		var out []any
		for _, r := range rows {
			if at(r, "agent") == agent {
				out = append(out, r)
			}
		}
		return out
	}

	t.Run("placements and versions", func(t *testing.T) {
		via := map[any]bool{}
		for _, r := range chains {
			if at(r, "agent_version") == nil {
				continue // an app's own run (desk), a derived one
			}
			via[at(r, "resolved_via")] = true
			if num(r, "agent_version", "number") < 1 || !strings.HasPrefix(str(r, "meta", "weftbase.agent_version"), "agv_") {
				t.Errorf("run %v: version %v", at(r, "id"), at(r, "agent_version"))
			}
		}
		if !via["label:prod"] || !via["label:staging"] || !via["latest"] {
			t.Errorf("resolved_via seen: %v", via)
		}
		// The local build reports with no wiring of its own: the
		// supervisor hands it the node's URL and an ingest key.
		refunds := byAgent(chains, "refunds")
		if len(refunds) == 0 || at(refunds[0], "service") != "refunds-build" || at(refunds[0], "placement") != "local" {
			t.Fatalf("the local build's runs = %v", refunds)
		}
		if s := byAgent(chains, "support"); len(s) == 0 || at(s[0], "placement") != "embedded" {
			t.Errorf("support's placement = %v", at(s, 0, "placement"))
		}
	})

	t.Run("statuses", func(t *testing.T) {
		states := map[any]int{}
		for _, r := range chains {
			states[at(r, "display_status")]++
			if at(r, "resumed_from") != nil {
				t.Errorf("a resume is a row of its own: %v", at(r, "id"))
			}
		}
		// waiting: the refund left pending. cancelled: the export, and
		// the two desk turns the next message ended. failed: the address
		// change and the provider outage.
		if states["waiting"] != 1 || states["cancelled"] != 3 || states["failed"] != 2 || states["succeeded"] < 20 || states["running"]+states["queued"] != 0 {
			t.Errorf("display statuses = %v", states)
		}
		failed := list(getAny(t, c, "/v1/runs?chains=1&display_status=failed"), "runs")
		kinds := []string{str(failed, 0, "error_type"), str(failed, 1, "error_type")}
		slices.Sort(kinds)
		if len(failed) != 2 || !slices.Equal(kinds, []string{"max_steps", "model_error"}) {
			t.Errorf("display_status=failed = %v", kinds)
		}
		// The search honours chains and display_status as the list does.
		found := list(getAny(t, c, "/v1/runs?q=refund&chains=1&display_status=succeeded"), "runs")
		chained := 0
		for _, r := range found {
			if at(r, "resumed_from") != nil || at(r, "display_status") != "succeeded" || at(r, "resolved_via") == nil {
				t.Errorf("search row %v: %v via %v", at(r, "id"), at(r, "display_status"), at(r, "resolved_via"))
			}
			if len(list(r, "chain", "runs")) > 1 {
				chained++
			}
		}
		if chained < 4 {
			t.Errorf("the search folded %d chains of %d rows, want the 4 decided refunds", chained, len(found))
		}
		if rows := list(getAny(t, c, "/v1/runs?sort=duration&chains=1&display_status=failed"), "runs"); len(rows) != 2 {
			t.Errorf("sort=duration&display_status=failed: %d rows", len(rows))
		}
		for _, r := range list(getAny(t, c, "/v1/runs?sort=tokens&chains=1&display_status=succeeded&limit=300"), "runs") {
			if at(r, "display_status") != "succeeded" {
				t.Errorf("a %v run under display_status=succeeded", at(r, "display_status"))
			}
		}
	})

	t.Run("approvals", func(t *testing.T) {
		inbox := getAny(t, c, "/v1/approvals")
		// 7: four refunds decided (one of them denied), the account
		// closure, the payout that expired, and the refund left pending.
		if at(inbox, "pending") != 1.0 || at(inbox, "total") != 7.0 || at(inbox, "approvals", 0, "status") != "pending" {
			t.Fatalf("approvals = %v", inbox)
		}
		pending := at(inbox, "approvals", 0)
		if num(pending, "agent_version", "number") != 2 || at(pending, "agent_version", "labels", 0) != "staging" {
			t.Errorf("the pending approval's version = %v", at(pending, "agent_version"))
		}
		states := map[any]int{}
		for _, a := range list(inbox, "approvals")[1:] {
			states[at(a, "status")]++
			if at(a, "actor", "kind") == nil || at(a, "resumed_by") == nil || at(a, "decided_at") == nil {
				t.Errorf("decided approval = %v", a)
			}
		}
		if states["approved"] != 4 || states["denied"] != 1 || states["expired"] != 1 {
			t.Errorf("decided approvals = %v", states)
		}
		expired := find(list(inbox, "approvals"), func(a any) bool { return at(a, "status") == "expired" })
		if at(expired, "agent") != "payouts" || at(expired, "actor", "kind") != "system" || at(expired, "outcome") != "deny" {
			t.Errorf("the expired approval = %v", expired)
		}
		path := "/v1/approvals/" + str(pending, "id")
		if err := c.post(ctx, path+"/decision", map[string]any{"decision": "approve", "args_sha256": "0000"}, nil); err == nil || !strings.Contains(err.Error(), "args_changed") {
			t.Errorf("a decision on other arguments: %v, want 409 args_changed", err)
		}
		if got := getAny(t, c, path); at(got, "status") != "pending" || at(got, "args", "order_id") != "1077" {
			t.Errorf("the pending approval = %v", got)
		}
		if w := list(getAny(t, c, "/v1/runs?display_status=waiting"), "runs"); len(w) != 1 || at(w[0], "id") != at(pending, "run_id") {
			t.Errorf("display_status=waiting = %v", w)
		}
	})

	t.Run("chains", func(t *testing.T) {
		// The account closure waited twice: three runs, two waits.
		twice := find(chains, func(r any) bool { return len(list(r, "chain", "runs")) == 3 })
		if twice == nil || len(list(twice, "chain", "waits")) != 2 || at(twice, "chain", "display_status") != "succeeded" ||
			at(twice, "chain", "waits", 0, "approvals", 0, "tool") != "issue_refund" || at(twice, "chain", "waits", 1, "approvals", 0, "tool") != "close_account" ||
			at(twice, "chain", "waits", 1, "decided_by", 0, "kind") != "key" || at(twice, "chain", "waits", 1, "to") == nil {
			t.Fatalf("the twice-parked chain = %v", at(twice, "chain"))
		}
		spans := getAny(t, c, "/v1/runs/"+str(twice, "id")+"/spans?tree=1&chain=1")
		runsSeen := map[any]bool{}
		for _, s := range list(spans, "spans") {
			runsSeen[at(s, "run_id")] = true
		}
		if len(list(spans, "chain")) != 3 || len(list(spans, "waits")) != 2 || len(runsSeen) != 3 {
			t.Errorf("chain spans: chain %v, %d waits, spans of %d runs", at(spans, "chain"), len(list(spans, "waits")), len(runsSeen))
		}
		detail := getAny(t, c, "/v1/runs/"+str(twice, "id"))
		if at(detail, "decided", "actor_kind") != "key" || len(list(detail, "approvals")) != 1 || at(detail, "chain", "display_status") != "succeeded" ||
			num(detail, "chain", "usage", "input_tokens") <= num(detail, "usage", "input_tokens") {
			t.Errorf("run detail: decided %v, approvals %v, chain usage %v", at(detail, "decided"), at(detail, "approvals"), at(detail, "chain", "usage"))
		}
		// The expired payout is a chain too: decided by the system.
		paid := find(chains, func(r any) bool { return at(r, "agent") == "payouts" })
		if at(paid, "chain", "waits", 0, "decided_by", 0, "kind") != "system" || at(paid, "chain", "waits", 0, "approvals", 0, "status") != "expired" ||
			at(paid, "display_status") != "succeeded" {
			t.Errorf("the payout's chain = %v", at(paid, "chain"))
		}
	})

	t.Run("queued", func(t *testing.T) {
		// reports takes one run at a time: of its two, started together,
		// one waited in the queue until the other was done.
		var waits []time.Duration
		for _, r := range byAgent(chains, "reports") {
			var st struct {
				CreatedAt time.Time `json:"created_at"`
				StartedAt time.Time `json:"started_at"`
			}
			if err := c.get(ctx, "/v1/runs/"+str(r, "id")+"/status", &st); err != nil {
				t.Fatal(err)
			}
			waits = append(waits, st.StartedAt.Sub(st.CreatedAt))
		}
		slices.Sort(waits)
		if len(waits) != 2 || waits[0] > 500*time.Millisecond || waits[1] < time.Second {
			t.Errorf("the reports runs waited %v in the queue, want one at once and one for over a second", waits)
		}
	})

	t.Run("a run with everything", func(t *testing.T) {
		gift := find(chains, func(r any) bool { return at(r, "agent") == "concierge" })
		if gift == nil || at(gift, "public_id") != "pub_gift_4471" || at(gift, "meta", "weft.page_url") != "https://shop.example/gifts/coffee" ||
			num(gift, "attempts_retried") < 1 || num(gift, "fallbacks") != 1 || num(gift, "repaired") != 1 || num(gift, "truncated") != 1 ||
			at(gift, "stopped_by") != "output_submitted" || at(gift, "display_status") != "succeeded" {
			t.Fatalf("the concierge run = %v", gift)
		}
		id := str(gift, "id")
		if doc := getAny(t, c, "/v1/runs/"+id); num(doc, "log_warn") < 1 || num(doc, "log_error") < 1 {
			t.Errorf("log counts: warn %v, error %v", at(doc, "log_warn"), at(doc, "log_error"))
		}
		// The first model call was retried; the last one fell back to
		// another model; the reply carries reasoning.
		step := getAny(t, c, "/v1/runs/"+id+"/steps/0")
		if len(list(step, "attempts")) != 2 || at(step, "tool_calls", 0, "name") != "inventory_search" || at(step, "tool_calls", 0, "args", "query") != "pour-over" {
			t.Errorf("step 0: %d attempts, tool call %v", len(list(step, "attempts")), at(step, "tool_calls", 0))
		}
		last := getAny(t, c, "/v1/runs/"+id+"/steps/2")
		if at(last, "model", "requested") != "gpt-4o-mini" || at(last, "model", "answered") != "gpt-4o" {
			t.Errorf("step 2's model = %v", at(last, "model"))
		}
		var transcript, tools json.RawMessage
		if err := c.get(ctx, "/v1/runs/"+id+"/transcript", &transcript); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"type":"reasoning"`, `"name":"submit_output"`, `"sku":"DRP-02"`} {
			if !strings.Contains(string(transcript), want) {
				t.Errorf("the transcript lacks %s", want)
			}
		}
		// The MCP tool says where it comes from; the reviews result was
		// cut at the tool's cap.
		if err := c.get(ctx, "/v1/runs/"+id+"/tools", &tools); err != nil || !strings.Contains(string(tools), `"inventory_search"`) || !strings.Contains(string(tools), `mcp`) {
			t.Errorf("the tool catalog does not show the MCP tool: %v %.300s", err, tools)
		}
		if res := at(getAny(t, c, "/v1/runs/"+id+"/steps/1"), "tool_calls", 0, "result"); at(res, "truncated") != true {
			t.Errorf("the reviews result = %v, want truncated", res)
		}
	})

	t.Run("children", func(t *testing.T) {
		// A failed child under a succeeded parent.
		parent := find(chains, func(r any) bool { return num(r, "children_failed") == 1 })
		if parent == nil || at(parent, "display_status") != "succeeded" || at(parent, "agent") != "support" {
			t.Fatalf("no succeeded run with a failed child: %v", parent)
		}
		child := find(all, func(r any) bool { return at(r, "parent_run_id") == at(parent, "id") })
		if at(child, "agent") != "billing" || at(child, "display_status") != "failed" || at(child, "cost_known") != false {
			t.Errorf("the failed child = %v", child)
		}
		if ok := find(all, func(r any) bool { return at(r, "agent") == "billing" && at(r, "display_status") == "succeeded" }); ok == nil {
			t.Error("no succeeded billing child")
		}
	})

	t.Run("stock traces", func(t *testing.T) {
		derived := find(all, func(r any) bool { return at(r, "derived") == true })
		if at(derived, "agent") != "recommender" || !strings.HasPrefix(str(derived, "id"), "otel-") || num(derived, "usage", "input_tokens") != 1960 {
			t.Errorf("the derived run = %v", derived)
		}
		notRun := getAny(t, c, "/v1/traces?not_run=1")
		if at(notRun, "total") != 1.0 || at(notRun, "traces", 0, "service") != "review-digest-job" {
			t.Errorf("traces?not_run=1 = %v", notRun)
		}
		web := find(list(getAny(t, c, "/v1/traces?service=checkout-web"), "traces"), func(any) bool { return true })
		if num(web, "span_count") != 3 || len(list(web, "run_ids")) != 0 {
			t.Errorf("the web trace = %v", web)
		}
	})

	t.Run("inventory", func(t *testing.T) {
		agents := getAny(t, c, "/v1/agents?include=summary")
		// Five registered; desk, billing and recommender only seen in runs.
		if at(agents, "totals", "agents") != 5.0 || len(list(agents, "totals", "placements")) != 2 || len(list(agents, "agents")) != 8 {
			t.Errorf("agents summary totals = %v (%d rows)", at(agents, "totals"), len(list(agents, "agents")))
		}
		for _, a := range list(agents, "agents") {
			switch at(a, "name") {
			case "billing", "desk", "recommender":
				if at(a, "registered") != false {
					t.Errorf("%v = %v", at(a, "name"), a)
				}
			case "support":
				if at(a, "registered") != true || at(a, "summary", "subagents") != 1.0 || at(a, "summary", "placement") != "embedded" ||
					at(a, "summary", "model", "name") != "gpt-4o-mini" || num(a, "summary", "stats", "runs") < 8 ||
					at(a, "label_versions", "prod", "number") != 1.0 || at(a, "label_versions", "staging", "number") != 2.0 ||
					at(a, "summary", "stats", "failed") != 2.0 || at(a, "summary", "stats", "cancelled") != 1.0 {
					t.Errorf("support = %v", a)
				}
			}
		}
		agent := getAny(t, c, "/v1/agents/refunds")
		if at(agent, "placement") != "local" || at(agent, "version_list", 0, "changes", "tools_added", 0) != "offer_store_credit" ||
			at(agent, "by_label", "prod", "number") != 1.0 || at(agent, "newest", "manifest", "tools", 1, "needs_approval") != true ||
			at(agent, "version_list", 0, "manifest_drift", "drift") != false {
			t.Errorf("agent refunds = %v", agent)
		}
		// payouts registered another manifest than the one its runs report.
		drift := at(getAny(t, c, "/v1/agents/payouts"), "version_list", 0, "manifest_drift")
		if at(drift, "drift") != true || num(drift, "runs_drifted") != 2 || at(drift, "since") == nil {
			t.Errorf("payouts' manifest drift = %v", drift)
		}
		if paid := find(chains, func(r any) bool { return at(r, "agent") == "payouts" }); at(paid, "manifest_drift") != true {
			t.Errorf("the payout run's manifest_drift = %v", at(paid, "manifest_drift"))
		}
		if d := getAny(t, c, "/v1/agents/support/versions/1/diff/2"); at(d, "tools_added", 0) != "handoff_to_human" || at(d, "instructions_changed") != false {
			t.Errorf("support 1 → 2 = %v", d)
		}
		tools := list(getAny(t, c, "/v1/tools"), "tools")
		tool := func(name string) any { return find(tools, func(x any) bool { return at(x, "name") == name }) }
		if tl := tool("lookup_order"); len(list(tl, "agents")) != 2 || num(tl, "stats", "calls") < 5 || at(tl, "stats", "duration_ms") == nil {
			t.Errorf("lookup_order = %v", tl)
		}
		// A call that parks is not a call that ran: issue_refund ran
		// three times (the approved ones), though it was asked for five.
		if tl := tool("issue_refund"); at(tl, "needs_approval") != true || at(tl, "agents", 0, "agent") != "refunds" || at(tl, "stats", "calls") != 3.0 {
			t.Errorf("issue_refund = %v", tl)
		}
		if tl := tool("send_payout"); at(tl, "stats") != nil {
			t.Errorf("send_payout never ran, its stats = %v", at(tl, "stats"))
		}
		if tl := tool("update_address"); at(tl, "stats", "failure_rate") != 1.0 {
			t.Errorf("update_address = %v", tl)
		}
		if tl := tool("inventory_search"); at(tl, "agents", 0, "agent") != "concierge" {
			t.Errorf("inventory_search = %v", tl)
		}
		off := getAny(t, c, "/v1/meta")
		if at(off, "capabilities_off") == nil || len(list(off, "capabilities_off")) != 0 || at(off, "capabilities", 0) != "ingest" {
			t.Errorf("meta capabilities = %v off %v", at(off, "capabilities"), at(off, "capabilities_off"))
		}
	})

	t.Run("stats", func(t *testing.T) {
		total := getAny(t, c, "/v1/stats?by=total&since=1h&buckets=12")
		if len(list(total, "series")) != 12 || len(list(total, "bucket_starts")) != 12 || at(total, "bucket_ms") != 300000.0 {
			t.Fatalf("total series = %v", total)
		}
		tot := at(total, "total")
		if at(tot, "failed") != 2.0 || at(tot, "cancelled") != 3.0 || at(tot, "waiting") != 1.0 || at(tot, "runs") != float64(len(chains)) ||
			num(tot, "cost_usd") <= 0 || at(tot, "duration_ms", "p95") == nil || num(tot, "failure_rate") >= 0.5 {
			t.Errorf("total = %v (rows with chains: %d)", tot, len(chains))
		}
		if at(total, "series", 11, "runs") != at(tot, "runs") {
			t.Errorf("the last bucket = %v", at(total, "series", 11))
		}
		for _, g := range list(getAny(t, c, "/v1/stats?by=agent&since=1h"), "groups") {
			if at(g, "key") == "billing" {
				t.Error("by=agent counts a subagent's child run by default")
			}
			if at(g, "key") == "support" && (at(g, "outcomes", "cancelled") != 1.0 || at(g, "outcomes", "failed") != 2.0 || len(list(g, "outcome_series")) != 24) {
				t.Errorf("support's outcomes = %v", at(g, "outcomes"))
			}
		}
	})

	t.Run("sessions", func(t *testing.T) {
		sessions := list(getAny(t, c, "/v1/sessions"), "sessions")
		// turns are logical: a resume continues its turn.
		turns := map[string][]float64{}
		for _, s := range sessions {
			turns[str(s, "agent")] = append(turns[str(s, "agent")], num(s, "turns"))
			slices.Sort(turns[str(s, "agent")])
		}
		if !slices.Equal(turns["support"], []float64{2, 3}) || !slices.Equal(turns["refunds"], []float64{2}) || !slices.Equal(turns["desk"], []float64{1, 9}) {
			t.Errorf("session turns = %v", turns)
		}
		for _, s := range sessions {
			if at(s, "agent") == "refunds" && at(s, "runs") != 3.0 {
				t.Errorf("the refunds session: %v runs, want 3 (two turns and a resume)", at(s, "runs"))
			}
		}
		// A ledger session: a park and its resume are one turn.
		ref := find(sessions, func(s any) bool { return at(s, "agent") == "refunds" })
		conv := getAny(t, c, "/v1/sessions/"+str(ref, "id")+"/conversation")
		first := at(conv, "turns", 0)
		if at(conv, "total_turns") != 2.0 || len(list(first, "runs")) != 2 || at(first, "display_status") != "succeeded" ||
			at(first, "waits", 0, "approvals", 0, "status") != "approved" || at(first, "tool_calls") != 3.0 ||
			!strings.HasPrefix(str(first, "reply"), "Done: $64.00") || at(first, "input") != "Refund order #1042, the kettle arrived cracked." ||
			at(conv, "turns", 1, "reply") != "You're welcome." || at(conv, "session", "turns") != 2.0 || num(conv, "session", "cost_usd") <= 0 {
			t.Errorf("the refund conversation = %v", conv)
		}
		// Search by public id and by id prefix; the cancelled filter.
		desk := find(sessions, func(s any) bool { return at(s, "agent") == "desk" && num(s, "turns") == 9 })
		for _, q := range []string{str(desk, "public_id"), str(desk, "id")[:14]} {
			if got := list(getAny(t, c, "/v1/sessions?q="+url.QueryEscape(q)), "sessions"); len(got) == 0 || find(got, func(s any) bool { return at(s, "id") == at(desk, "id") }) == nil {
				t.Errorf("sessions?q=%s does not find the desk session", q)
			}
		}
		if got := list(getAny(t, c, "/v1/sessions?cancelled=1"), "sessions"); len(got) != 0 {
			// The desk's ended turns are thread's "ended", not a
			// caller's cancel: no session here has a cancelled turn.
			t.Errorf("sessions?cancelled=1 = %d sessions", len(got))
		}
	})

	t.Run("a thread session", func(t *testing.T) {
		sessions := list(getAny(t, c, "/v1/sessions?agent=desk"), "sessions")
		desk := find(sessions, func(s any) bool { return num(s, "turns") == 9 })
		fork := find(sessions, func(s any) bool { return num(s, "turns") == 1 })
		if desk == nil || fork == nil || !strings.HasPrefix(str(fork, "forked_from_session"), str(desk, "id")+"#") || !strings.HasPrefix(str(desk, "public_id"), "pub_desk_") ||
			at(desk, "title") != "Gift wrap, this month's orders" {
			t.Fatalf("desk sessions = %v", sessions)
		}
		conv := getAny(t, c, "/v1/sessions/"+str(desk, "id")+"/conversation?tail=1&limit=4")
		if at(conv, "total_turns") != 9.0 || at(conv, "offset") != 5.0 || len(list(conv, "turns")) != 4 || at(conv, "next_offset") != nil ||
			at(conv, "session", "public_id") != at(desk, "public_id") || at(conv, "session", "first_seen") == nil {
			t.Fatalf("the newest page = offset %v, %d turns of %v", at(conv, "offset"), len(list(conv, "turns")), at(conv, "total_turns"))
		}
		turns := list(getAny(t, c, "/v1/sessions/"+str(desk, "id")+"/conversation?limit=50"), "turns")
		marks := map[string]any{}
		for _, turn := range turns {
			for _, m := range list(turn, "marks") {
				key := str(m, "kind")
				if p := str(m, "fields", "policy"); p != "" {
					key += ":" + p
				}
				if _, seen := marks[key]; !seen {
					marks[key] = m
				}
			}
		}
		for _, kind := range []string{"approval.request", "approval.decision", "grant.mint", "grant.use", "receipt", "compaction", "interrupt:interrupt", "interrupt:rollback", "fork"} {
			if marks[kind] == nil {
				t.Errorf("no %s mark in the conversation: %v", kind, slices.Sorted(func(yield func(string) bool) {
					for k := range marks {
						if !yield(k) {
							return
						}
					}
				}))
			}
		}
		if m := marks["approval.decision"]; at(m, "actor") != "op_maria" || at(m, "fields", "outcome") != "approve" {
			t.Errorf("the approval decision mark = %v", m)
		}
		if m := marks["compaction"]; num(m, "fields", "tokens_before") <= num(m, "fields", "tokens_after") || num(m, "fields", "tokens_after") <= 0 {
			t.Errorf("the compaction mark = %v", at(m, "fields"))
		}
		// The approved turn is one turn of two runs; the steered turn
		// answers the steer; the interrupted and the rolled-back turns
		// say what ended them.
		byInput := func(text string) any { return find(turns, func(x any) bool { return at(x, "input") == text }) }
		if x := byInput(deskWrap); len(list(x, "runs")) != 2 || at(x, "display_status") != "succeeded" || !strings.HasPrefix(str(x, "reply"), "Gift wrap is added") {
			t.Errorf("the approved turn = %v", x)
		}
		if x := byInput(deskSummary); !strings.HasPrefix(str(x, "reply"), "This month:") {
			t.Errorf("the steered turn's reply = %v", at(x, "reply"))
		}
		if x := byInput(deskStock); at(x, "ended_kind") != "interrupt" || at(x, "off_path") != nil || at(x, "reply") != nil || at(x, "display_status") != "cancelled" {
			t.Errorf("the interrupted turn = %v", x)
		}
		if x := byInput(deskShip); at(x, "ended_kind") != "rollback" || at(x, "off_path") != true || num(x, "ended_by_turn") == 0 {
			t.Errorf("the rolled-back turn = %v", x)
		}
		if got := getAny(t, c, "/v1/sessions/"+str(fork, "id")+"/conversation"); !strings.HasPrefix(str(got, "session", "forked_from_session"), str(desk, "id")) ||
			!strings.HasPrefix(str(got, "turns", 0, "reply"), "Returns are free") {
			t.Errorf("the fork's conversation = %v", got)
		}
	})

	t.Run("logs", func(t *testing.T) {
		failed := find(chains, func(r any) bool { return at(r, "error_type") == "max_steps" })
		logs := getAny(t, c, "/v1/runs/"+str(failed, "id")+"/logs?severity=error")
		if num(failed, "tool_errors") != 4 || at(logs, "logs", 0, "body") != "address service unavailable" {
			t.Errorf("the failed run: %v tool errors, logs %v", at(failed, "tool_errors"), at(logs, "logs"))
		}
	})

	t.Run("sign-in links and the event tail", func(t *testing.T) {
		var link struct {
			Link string `json:"link"`
		}
		if err := c.post(ctx, "/v1/auth/links", map[string]string{"origin": "http://localhost:3000"}, &link); err != nil || !strings.HasPrefix(link.Link, "http://localhost:3000/#signin=") {
			t.Errorf("a fresh sign-in link: %q %v", link.Link, err)
		}
		// The audit trail has the link; a run's delete goes there too.
	})

	t.Run("delete", func(t *testing.T) {
		// A waiting run is refused; a finished chain goes whole.
		waiting := find(chains, func(r any) bool { return at(r, "display_status") == "waiting" })
		if err := c.do(ctx, "DELETE", "/v1/runs/"+str(waiting, "id"), nil, nil); err == nil || !strings.Contains(err.Error(), "run_active") {
			t.Errorf("deleting a waiting run: %v, want 409 run_active", err)
		}
		twice := find(chains, func(r any) bool { return len(list(r, "chain", "runs")) == 3 })
		var del map[string]any
		if err := c.do(ctx, "DELETE", "/v1/runs/"+str(twice, "chain", "runs", 2, "id"), nil, &del); err != nil ||
			len(list(del, "run_ids")) != 3 || at(del, "ledger_runs") != 3.0 || at(del, "approvals") != 2.0 {
			t.Fatalf("deleting the chain by its last run: %v %v", del, err)
		}
		for _, r := range list(getAny(t, c, "/v1/runs?parent=*&limit=300"), "runs") {
			if slices.Contains(list(del, "run_ids"), at(r, "id")) {
				t.Errorf("run %v is still listed after the delete", at(r, "id"))
			}
		}
		if at(getAny(t, c, "/v1/approvals"), "total") != 5.0 {
			t.Error("the deleted chain's approvals are still listed")
		}
		// A session with its runs.
		ref := find(list(getAny(t, c, "/v1/sessions?agent=refunds"), "sessions"), func(any) bool { return true })
		if err := c.do(ctx, "DELETE", "/v1/sessions/"+str(ref, "id"), nil, &del); err != nil || at(del, "ledger_runs") != 3.0 || at(del, "approvals") != 1.0 {
			t.Errorf("deleting the refunds session: %v %v", del, err)
		}
		if at(getAny(t, c, "/v1/approvals"), "total") != 4.0 {
			t.Error("the deleted session's approval is still listed")
		}
	})
}
