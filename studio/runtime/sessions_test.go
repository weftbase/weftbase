package runtime

import (
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSessionCommand pins the Studio side of plan F4's session frame:
// SessionsRuntime picks a connected runtime of the run's service that
// registered sessions (never one of another service, one without the
// option, or a run with no service); EnqueueSession writes an
// `event: session` frame carrying the SessionCommand verbatim, its row
// scoped to the public id; the acks that name the app's run never make
// it a runtime-started run (no steer, approval or cancel routes through
// it); and the enqueue's errors are Enqueue's.
func TestSessionCommand(t *testing.T) {
	rs := fastServer()
	rs.AckDeadline = time.Minute // the rows below are acked by hand
	ts := httptest.NewServer(mux(rs))
	defer ts.Close()

	plain := regBody("rt_plain")
	sess := regBody("rt_sess")
	sess.Sessions = true
	other := regBody("rt_other")
	other.Sessions, other.Service = true, "other-api"
	for _, reg := range []Registration{plain, sess, other} {
		register(t, mux(rs), reg)
	}
	if rs.AnySessions() {
		t.Error("AnySessions with no stream open")
	}
	if _, ok := rs.SessionsRuntime("acme-api", "", ""); ok {
		t.Error("SessionsRuntime picked a runtime holding no stream")
	}
	_, closePlain := subscribe(t, rs, ts.URL, "rt_plain", "")
	defer closePlain()
	r, closeSess := subscribe(t, rs, ts.URL, "rt_sess", "")
	defer closeSess()
	_, closeOther := subscribe(t, rs, ts.URL, "rt_other", "")
	defer closeOther()

	if !rs.AnySessions() {
		t.Error("AnySessions = false with rt_sess connected")
	}
	if id, ok := rs.SessionsRuntime("acme-api", "", ""); !ok || id != "rt_sess" {
		t.Errorf("SessionsRuntime(acme-api) = %q %v, want rt_sess", id, ok)
	}
	for _, service := range []string{"", "nope"} {
		if id, ok := rs.SessionsRuntime(service, "", ""); ok {
			t.Errorf("SessionsRuntime(%q) = %q, want none", service, id)
		}
	}
	for _, v := range rs.Snapshot() {
		if v.Sessions != (v.ID != "rt_plain") {
			t.Errorf("view %s sessions = %v", v.ID, v.Sessions)
		}
	}

	sc, err := rs.EnqueueSession("rt_sess", SessionCommand{Verb: VerbDecide, PublicID: "pub_7Hk2",
		SessionID: "s_1", RunID: "s_1-t1", CallID: "call_1", Decision: "approve", Actor: "devtools"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sc.CommandID, "cmd_") {
		t.Errorf("minted id = %q", sc.CommandID)
	}
	id, event, data := func() (string, string, []byte) {
		for {
			id, event, data := nextEvent(t, r)
			if !idle(event, data) {
				return id, event, data
			}
		}
	}()
	want := `{"command_id":"` + sc.CommandID + `","verb":"decide","public_id":"pub_7Hk2","session_id":"s_1","run_id":"s_1-t1",` +
		`"call_id":"call_1","decision":"approve","actor":"devtools"}`
	if id != sc.CommandID || event != "session" || string(data) != want {
		t.Errorf("frame = id %q event %q data %s\nwant  %s", id, event, data, want)
	}
	if cmd, ok := rs.CommandOf(sc.CommandID); !ok || cmd.PublicID != "pub_7Hk2" {
		t.Errorf("the row's scope = %+v", cmd)
	}
	ack(t, rs, Ack{CommandID: sc.CommandID, State: "accepted", RunID: "s_1-t1"})
	if _, started := rs.RuntimeOf("s_1-t1"); started {
		t.Error("the accepted ack made the app's run a runtime-started one")
	}
	if _, err := rs.CancelRun("s_1-t1"); !errors.Is(err, ErrUnknownRun) {
		t.Errorf("CancelRun on the app's run = %v, want ErrUnknownRun", err)
	}
	ack(t, rs, Ack{CommandID: sc.CommandID, State: "finished", RunID: "s_1-t2", Status: "succeeded"})
	if st := waitState(t, rs, sc.CommandID, StateFinished); st.RunID != "s_1-t2" || st.Status != "succeeded" {
		t.Errorf("finished row = %+v", st)
	}
	if _, started := rs.RuntimeOf("s_1-t2"); started {
		t.Error("the finished ack made the resumed app turn a runtime-started run")
	}

	for _, c := range []struct {
		runtime, id string
		want        error
	}{
		{"rt_nope", "", ErrUnknownRuntime},
		{"rt_sess", "bad id\nevent: run", ErrInvalidCommandID},
		{"rt_sess", sc.CommandID, ErrDuplicateCommand},
	} {
		if _, err := rs.EnqueueSession(c.runtime, SessionCommand{CommandID: c.id, Verb: VerbCancel}); !errors.Is(err, c.want) {
			t.Errorf("EnqueueSession(%s, %q) = %v, want %v", c.runtime, c.id, err, c.want)
		}
	}
}

// TestSessionsRuntimePrefersTheRunsVersion: a run parked on one version
// of the service is decided through a runtime of that version when one
// is connected — its agent registered under the run's manifest hash —
// even while a newer process of the service (a rolling deploy's next
// version) was seen later. Only when no connected runtime registers the
// run's version does the newest of the service take it, as before (the
// redeploy that left no old process).
func TestSessionsRuntimePrefersTheRunsVersion(t *testing.T) {
	// A clock that moves only when the test moves it: rt_v2 is seen
	// after rt_v1, and no keep-alive ping reorders the two.
	var (
		clockMu sync.Mutex
		clock   = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	)
	rs := New(func(rs *RuntimeServer) {
		rs.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	})
	tick := func() { clockMu.Lock(); clock = clock.Add(time.Second); clockMu.Unlock() }
	ts := httptest.NewServer(mux(rs))
	defer ts.Close()

	v1 := regBody("rt_v1")
	v1.Sessions = true
	v1.Agents[0].ManifestHash = "hash_v1"
	v2 := regBody("rt_v2")
	v2.Sessions = true
	v2.Agents[0].ManifestHash = "hash_v2"
	register(t, mux(rs), v1)
	_, close1 := subscribe(t, rs, ts.URL, "rt_v1", "")
	defer close1()
	tick() // rt_v2 is the newest seen
	register(t, mux(rs), v2)
	_, close2 := subscribe(t, rs, ts.URL, "rt_v2", "")
	defer close2()

	for _, c := range []struct{ agent, hash, want string }{
		{"acme-support", "hash_v1", "rt_v1"},
		{"acme-support", "hash_v2", "rt_v2"},
		{"acme-support", "hash_gone", "rt_v2"}, // no runtime of that version: the newest
		{"other-agent", "hash_v1", "rt_v2"},    // another agent's hash pins nothing
		{"acme-support", "", "rt_v2"},          // a run with no manifest hash recorded
	} {
		if id, ok := rs.SessionsRuntime("acme-api", c.agent, c.hash); !ok || id != c.want {
			t.Errorf("SessionsRuntime(acme-api, %s, %s) = %q %v, want %s", c.agent, c.hash, id, ok, c.want)
		}
	}
}
