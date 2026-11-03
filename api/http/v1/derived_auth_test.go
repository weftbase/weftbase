package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/weftbase/weftbase/obsdb/sqlite"
	pricing "github.com/weftbase/weftbase/observe/cost"
)

// A derived run's session id is its gen_ai.conversation.id, which any
// stock app chooses: a stock trace that collides with a weft session's
// id joins that session's GROUP BY — the session lists and sums the
// colliding derived runs.
func TestDerivedRunSessionCollision(t *testing.T) {
	const serverTok, ingestTok = "srv-token", "ingest-token"
	db, err := sqlite.Open(t.TempDir() + "/collide.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := New(DB(db),
		Pricing(pricing.Table{Dated: "2026-10-01", Models: []pricing.ModelPrice{{Provider: "*", ModelGlob: "*", Input: 1000, Output: 1000}}}))
	t.Cleanup(func() { _ = srv.Close() })
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// The weft turn of pub_a's session conv_7f3a (usage 1/1)…
	seedRun(t, db, "r_mine", "pub_a", "conv_7f3a", "", nil)
	// …and another app's stock trace with the same conversation id.
	b, err := os.ReadFile("../../../obsdb/testdata/contract/stock_genai/traces.json")
	if err != nil {
		t.Fatal(err)
	}
	if code, out := b4Do(t, http.MethodPost, ts.URL+"/otlp/v1/traces", ingestTok, string(b)); code != http.StatusOK {
		t.Fatalf("ingest: %d %s", code, out)
	}
	var dev struct {
		Turns   int      `json:"turns"`
		CostUSD *float64 `json:"cost_usd"`
		Runs    []struct {
			ID string `json:"id"`
		} `json:"runs"`
	}
	code, out := b4Do(t, http.MethodGet, ts.URL+"/v1/sessions/conv_7f3a", serverTok, "")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/sessions/conv_7f3a = %d %s", code, out)
	}
	if err := json.Unmarshal([]byte(out), &dev); err != nil {
		t.Fatal(err)
	}
	// r_mine's own cost: 1 input + 1 output token at $1000 per 1M.
	const mine = 0.002
	if dev.Turns != 3 || len(dev.Runs) != 3 || !strings.Contains(out, "otel-") {
		t.Errorf("server GET /v1/sessions/conv_7f3a = %s, want the weft turn and both derived runs", out)
	}
	if dev.CostUSD == nil || *dev.CostUSD <= mine {
		t.Errorf("server GET /v1/sessions/conv_7f3a = %s, want a cost_usd above r_mine's own (the derived runs summed)", out)
	}
}
