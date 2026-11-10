package main

import (
	"context"
	"io"
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
	if !slices.Equal(turns, []int{2, 3}) {
		t.Errorf("session turns = %v, want a session of 2 and one of 3", turns)
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
