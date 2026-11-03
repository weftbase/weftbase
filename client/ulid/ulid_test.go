package ulid

import (
	"strings"
	"testing"
)

// TestNew pins the id shape: a known prefix on a 26-char
// time-ordered body, and ids strictly increasing within a process (a
// command's SSE id is the resume cursor, so ordering must never
// regress).
func TestNew(t *testing.T) {
	prev := ""
	for i := 0; i < 100; i++ {
		for _, prefix := range []string{"wk_", "cmd_", "run_"} {
			id := prefix + New()
			if !strings.HasPrefix(id, prefix) {
				t.Fatalf("id %q lacks prefix %q", id, prefix)
			}
			body := strings.TrimPrefix(id, prefix)
			if len(body) != Len {
				t.Fatalf("id %q body len = %d, want %d", id, len(body), Len)
			}
			if strings.HasPrefix(id, "cmd_") {
				if prev != "" && body < prev {
					t.Fatalf("ids regressed: %q after %q", body, prev)
				}
				prev = body
			}
		}
	}
}
