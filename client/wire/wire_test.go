package wire

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the goldens")

func golden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name+".json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from its golden:\n got: %s\nwant: %s", name, got, want)
	}
}

const cmdID = "cmd_01JB0000000000000000000000"

func TestGoldens(t *testing.T) {
	deadline := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	golden(t, "registration", Registration{
		Instance: "host-a/1234", Host: "host-a", PID: 1234, Placement: PlacementLocal,
		Build:        Build{Digest: "sha256:abc", Kind: BuildBinary, WeftVersion: "0.21.0", VCS: &VCS{Revision: "deadbeef"}},
		Agents:       []AgentRegistration{{Name: "support", Manifest: json.RawMessage(`{"weft":1,"agents":[{"name":"support"}]}`), ManifestHash: "f00d", Concurrency: 4}},
		Capabilities: []string{EventRun, EventCancel}, HeartbeatMS: 60000,
	})
	golden(t, "command_start", Command{
		CommandID: cmdID, Kind: KindStart, RunID: "run_1", Agent: "support", AgentVersion: "agv_1",
		BuildDigest: "sha256:abc", ManifestHash: "f00d", TenantID: "ten_project",
		Input: &Input{Prompt: "where is order 42?"}, Deadline: &deadline,
		Metadata: map[string]string{"ticket": "T-1"}, Trace: &Trace{Traceparent: "00-1-2-01"},
	})
	golden(t, "command_resume", Command{
		CommandID: cmdID, Kind: KindResume, RunID: "run_2", Agent: "support", AgentVersion: "agv_1",
		BuildDigest: "sha256:abc", ManifestHash: "f00d", TenantID: "ten_project",
		Resume: &Resume{ResumeOf: "run_1", Checkpoint: json.RawMessage(`{"messages":[]}`),
			Decisions: []Decision{{CallID: "call_1", Decision: "approve"}, {CallID: "call_2", Decision: "deny", Reason: "no"}}},
	})
	golden(t, "cancel", Cancel{CommandID: cmdID, RunID: "run_1", Cause: CauseUser})
	golden(t, "acks", []Ack{
		{CommandID: cmdID, State: AckAccepted},
		{CommandID: cmdID, State: AckRejected, Error: "agent_version agv_9 is not served here"},
		{CommandID: cmdID, State: AckBusy},
		{CommandID: cmdID, State: AckRunStarted, RunID: "run_1"},
		{CommandID: cmdID, State: AckHeartbeat, RunID: "run_1"},
		{CommandID: cmdID, State: AckFinished, RunID: "run_1", Status: StatusSucceeded, Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
		{CommandID: cmdID, State: AckFinished, RunID: "run_1", Status: StatusCanceled, Cause: CauseUser},
		{CommandID: cmdID, State: AckFinished, RunID: "run_1", Status: StatusParked,
			Pending:    []PendingCall{{CallID: "call_1", Tool: "refund", Args: json.RawMessage(`{"order":42}`)}},
			Checkpoint: json.RawMessage(`{"messages":[]}`)},
	})
	f, err := NewFrame(EventRun, cmdID, Cancel{CommandID: cmdID, RunID: "run_1", Cause: CauseUser})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "frames", []Frame{f, AckFrame(Ack{CommandID: cmdID, State: AckAccepted}), {Event: EventPing}})
	golden(t, "steer", Steer{CommandID: cmdID, RunID: "run_1",
		Messages: []json.RawMessage{json.RawMessage(`{"role":"user","content":[{"type":"text","text":"only EU orders"}]}`)}})
	golden(t, "handshake", []any{
		Hello{Token: "wbt_secret", Registration: Registration{Placement: PlacementLocal, Build: Build{Digest: "sha256:abc", Kind: BuildBinary},
			Agents:       []AgentRegistration{{Name: "support", Manifest: json.RawMessage(`{}`), ManifestHash: "f00d"}},
			Capabilities: []string{EventRun, EventCancel, EventSteer}, HeartbeatMS: 60000}},
		Welcome{WorkerID: "wk_1", PingMS: 15000},
		Welcome{Error: "token refused"},
		Disconnected{Reason: "the child exited: exit status 2", Lost: true},
	})
}

func TestRoundTrip(t *testing.T) {
	in := Command{CommandID: cmdID, Kind: KindResume, RunID: "r", Agent: "a", AgentVersion: "agv_1",
		Resume: &Resume{ResumeOf: "r0", Decisions: []Decision{{CallID: "c", Decision: "approve"}}}}
	f, err := NewFrame(EventRun, in.CommandID, in)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f)
	var back Frame
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	var out Command
	if err := back.Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Resume == nil || out.Resume.ResumeOf != "r0" || out.Validate() != nil {
		t.Errorf("round trip = %+v (%v)", out, out.Validate())
	}
	if err := (Frame{Event: EventPing}).Decode(&out); err == nil {
		t.Error("Decode of an empty frame succeeded")
	}
}

func TestValidCommandID(t *testing.T) {
	for id, want := range map[string]bool{
		cmdID:                            true,
		NewCommandID():                   true,
		"cmd_01jb0000000000000000000000": false, // lower case
		"cmd_01JB000000000000000000000":  false, // 25
		"cmd_01JB000000000000000000000I": false, // I is not Crockford
		"run_01JB0000000000000000000000": false,
		"":                               false,
	} {
		if got := ValidCommandID(id); got != want {
			t.Errorf("ValidCommandID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Command{CommandID: cmdID, Kind: KindStart, RunID: "r", Agent: "a", AgentVersion: "agv"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []Command{
		{Kind: KindStart, RunID: "r", Agent: "a", AgentVersion: "agv"},
		{CommandID: cmdID, Kind: "replay", RunID: "r", Agent: "a", AgentVersion: "agv"},
		{CommandID: cmdID, Kind: KindStart, RunID: "r", Agent: "a"},
		{CommandID: cmdID, Kind: KindResume, RunID: "r", Agent: "a", AgentVersion: "agv"},
		{CommandID: cmdID, Kind: KindStart, RunID: "r", Agent: "a", AgentVersion: "agv", Resume: &Resume{ResumeOf: "x"}},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("bad[%d] validated", i)
		}
	}
	reg := Registration{Build: Build{Digest: "sha256:x"}, Agents: []AgentRegistration{{Name: "a", Manifest: json.RawMessage(`{}`), ManifestHash: "h"}}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	reg.Agents = append(reg.Agents, reg.Agents[0])
	if reg.Validate() == nil {
		t.Error("a duplicate agent validated")
	}
	if (Registration{Build: Build{Digest: "md5:x"}}).Validate() == nil {
		t.Error("a non-sha256 digest validated")
	}
	user := json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi"}]}`)
	if err := (Steer{CommandID: cmdID, RunID: "r", Messages: []json.RawMessage{user}}).Validate(); err != nil {
		t.Errorf("a user steer: %v", err)
	}
	for i, st := range []Steer{
		{RunID: "r", Messages: []json.RawMessage{user}},
		{CommandID: cmdID, Messages: []json.RawMessage{user}},
		{CommandID: cmdID, RunID: "r"},
		{CommandID: cmdID, RunID: "r", Messages: []json.RawMessage{json.RawMessage(`{"role":"assistant"}`)}},
		{CommandID: cmdID, RunID: "r", Messages: []json.RawMessage{json.RawMessage(`[`)}},
	} {
		if st.Validate() == nil {
			t.Errorf("bad steer %d validated", i)
		}
	}
	if !(Ack{State: AckFinished}).Final() || (Ack{State: AckHeartbeat}).Final() {
		t.Error("Final")
	}
}

func TestRestoreManifest(t *testing.T) {
	indented := []byte("{\n  \"weft\": 1,\n  \"agents\": [\n    {\n      \"name\": \"a\\u003cb\"\n    }\n  ]\n}\n")
	hash := sha256Hex(indented)
	reg := Registration{Agents: []AgentRegistration{{Name: "a", Manifest: indented, ManifestHash: hash}}}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	var back Registration
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if sha256Hex(back.Agents[0].Manifest) == hash {
		t.Fatal("the transport kept the indentation: this test no longer pins anything")
	}
	if got := RestoreManifest(back.Agents[0].Manifest, hash); !bytes.Equal(got, indented) {
		t.Errorf("restored = %q", got)
	}
	if got := RestoreManifest(indented, hash); !bytes.Equal(got, indented) {
		t.Error("bytes that match were changed")
	}
	if got := RestoreManifest(json.RawMessage(`{"x":1}`), hash); string(got) != `{"x":1}` {
		t.Error("a manifest that matches in no form was changed")
	}
}
