package studio

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/obsdb/sqlite"
)

// usageLimitRuns is the U24.2 fixture: seven researcher runs that hit
// the usage limit at different steps over different token counts — one
// failure class — beside a loop detection and a success.
func usageLimitRuns() []statsRun {
	out := []statsRun{}
	for i := range 7 {
		step := i % 3
		steps := make([]statsStep, step+1)
		for s := range steps {
			steps[s] = statsStep{in: int64(100 + 10*i), out: int64(40 + i), ttft: 300, dur: 400 * time.Millisecond}
		}
		out = append(out, statsRun{
			id: fmt.Sprintf("ul_%d", i), agent: "researcher", provider: "zai", model: "glm-5.3-flash",
			at: time.Duration(i+1) * time.Hour, dur: 2 * time.Second, errType: "usage_limit",
			err:   fmt.Sprintf("weft: run failed at step %d: weft: run exceeded its usage limit (input %d/250, output %d/0)", step, 260+i, 40+i),
			steps: steps,
		})
	}
	out = append(out,
		statsRun{id: "ul_loop", agent: "researcher", provider: "zai", model: "glm-5.3-flash", at: 9 * time.Hour, dur: time.Second,
			errType: "loop_detected", err: arenaLoop, steps: []statsStep{{in: 5, out: 2, dur: time.Second}}},
		statsRun{id: "ul_ok", agent: "researcher", provider: "zai", model: "glm-5.3-flash", at: 10 * time.Hour, dur: time.Second,
			steps: []statsStep{{in: 5, out: 2, dur: time.Second}}},
	)
	return out
}

// TestUsageLimitOneFacetOneMatrix is U24.2's Done line through the real
// routes: the seven usage-limit failures are one error_class facet of 7,
// and the class filters /api/search to exactly them. The facet page is
// the golden the runs list's facet test reads.
func TestUsageLimitOneFacetOneMatrix(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, r := range usageLimitRuns() {
		if err := db.Write(context.Background(), r.batch()); err != nil {
			t.Fatal(err)
		}
	}
	h := Handler(DB(db))
	code, _, body := get(t, h, "/studio/api/search?status=failed&agent=researcher")
	if code != http.StatusOK {
		t.Fatalf("search: %d %s", code, body)
	}
	if !strings.Contains(body, `"facets":{"error_class":[{"class":"`+usageClass+`","count":7},`) {
		t.Errorf("facet: want the usage-limit class first with 7 runs:\n%s", body)
	}
	golden(t, "search-usage-limit.golden.json", body)

	ids, total, _ := searchIDs(t, h, "/studio/api/search?status=failed&agent=researcher&error_class="+url.QueryEscape(usageClass))
	if total != 7 || len(ids) != 7 {
		t.Fatalf("search?error_class=<usage limit> = %v (total %d), want the 7 runs", ids, total)
	}
}
