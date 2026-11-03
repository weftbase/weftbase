package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weftbase/weftbase/events"
	"github.com/weftbase/weftbase/tenant"
	"github.com/weftbase/weftbase/tenant/keys"
)

// sseEvent is one frame of a stream.
type sseEvent struct {
	id, event, data string
}

// openStream opens GET path with the key and Last-Event-ID and returns
// a channel of its frames (pings dropped); the stream closes with the
// test.
func openStream(t *testing.T, s *stack, path, key, lastID string) <-chan sseEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	hr, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.ts.URL+path, nil)
	hr.Header.Set("Authorization", "Bearer "+key)
	if lastID != "" {
		hr.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream %s: %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 64)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.event != "" && cur.event != "ping" {
					out <- cur
				}
				cur = sseEvent{}
			case strings.HasPrefix(line, "id: "):
				cur.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[6:]
			}
		}
	}()
	return out
}

func next(t *testing.T, c <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case e, ok := <-c:
		if !ok {
			t.Fatal("the stream ended")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5s")
	}
	return sseEvent{}
}

// TestEventsStreamReplay: GET /v1/events replays the stored events
// after Last-Event-ID (id = seq), then streams new ones live, each
// once; events of another scope never reach the subscriber; a
// malformed cursor is a 400.
func TestEventsStreamReplay(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	// The stack minted its key: seq 1. Two more in the default scope.
	for _, name := range []string{"a", "b"} {
		if _, _, err := s.keys.Mint(ctx, tenant.Default(), keys.Secret, name, events.System); err != nil {
			t.Fatal(err)
		}
	}
	// One in another project, which the subscriber must never see.
	other, err := tenant.NewScope("prj_other", "test", tenant.ProjectTenant)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.keys.Mint(ctx, other, keys.Secret, "other", events.System); err != nil {
		t.Fatal(err)
	}

	c := openStream(t, s, "/v1/events", s.key, "1")
	for _, want := range []string{"2", "3"} {
		e := next(t, c)
		if e.id != want || e.event != events.KeyMinted {
			t.Fatalf("replayed %+v, want id %s key.minted", e, want)
		}
		var ev events.Event
		if err := json.Unmarshal([]byte(e.data), &ev); err != nil || strconv.FormatInt(ev.Seq, 10) != e.id || ev.ProjectID != tenant.DefaultProject {
			t.Fatalf("frame data %s (%v)", e.data, err)
		}
	}
	// Live: a revoke in this scope arrives; the other project's mint
	// (seq 4) was skipped.
	ks, err := s.keys.List(ctx, tenant.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.keys.Revoke(ctx, tenant.Default(), ks[0].ID, events.System); err != nil {
		t.Fatal(err)
	}
	if e := next(t, c); e.id != "5" || e.event != events.KeyRevoked {
		t.Fatalf("live frame %+v, want id 5 key.revoked", e)
	}

	// A type filter, from the start.
	c2 := openStream(t, s, "/v1/events?type=key.revoked", s.key, "")
	if e := next(t, c2); e.id != "5" {
		t.Fatalf("filtered replay %+v, want only id 5", e)
	}

	if resp, body := s.do(t, req{path: "/v1/events", key: s.key, header: map[string]string{"Last-Event-ID": "x"}}); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad cursor: %d %s", resp.StatusCode, body)
	}
}
