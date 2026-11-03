// Package wire holds the worker protocol's types (P§6): what a worker
// registers, the commands it is sent, the acks it answers with, and the
// frames both travel in. The server (worker/hub, worker/executor) and
// agent builds (client/worker) import it; it imports the standard
// library and client/ulid only, so the JSON is the contract and the goldens in
// testdata pin it.
//
// One protocol, several transports: the embedded placement passes
// these values through Go calls, the local placement as framed JSON
// over a socket.
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/weftbase/weftbase/client/ulid"
)

// Placements: where a build's workers run (P§5.7).
const (
	PlacementEmbedded = "embedded"
	PlacementLocal    = "local"
	PlacementRemote   = "remote"
)

// Build kinds (P§5.2).
const (
	BuildEmbedded = "embedded"
	BuildBinary   = "binary"
	BuildExternal = "external"
)

// Registration is what a worker announces on connect and after every
// reconnect (P§6.2): who it is, the build it runs and the agents that
// build serves.
type Registration struct {
	// WorkerID is the id the server gave on a previous connect; empty
	// on the first.
	WorkerID  string `json:"worker_id,omitempty"`
	Instance  string `json:"instance,omitempty"`
	Host      string `json:"host,omitempty"`
	PID       int    `json:"pid,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Service   string `json:"service,omitempty"`
	Placement string `json:"placement"`
	Build     Build  `json:"build"`
	// Agents are the agents the build serves, each with its one-agent
	// manifest.
	Agents []AgentRegistration `json:"agents"`
	// Capabilities are the command kinds the worker takes (run, resume,
	// cancel); the server sends nothing else.
	Capabilities []string `json:"capabilities"`
	// HeartbeatMS is how often the worker re-acks a command it holds.
	HeartbeatMS int64 `json:"heartbeat_ms"`
}

// Build is the build identity a worker announces.
type Build struct {
	// Digest is sha256 of the executable ("sha256:<hex>"); required.
	Digest      string `json:"digest"`
	Kind        string `json:"kind,omitempty"`
	VCS         *VCS   `json:"vcs,omitempty"`
	WeftVersion string `json:"weft_version,omitempty"`
}

// VCS is the build's source revision, when known.
type VCS struct {
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
}

// AgentRegistration is one agent a build serves.
type AgentRegistration struct {
	Name string `json:"name"`
	// Manifest is the one-agent core.Manifest document, verbatim.
	Manifest json.RawMessage `json:"manifest"`
	// ManifestHash is sha256 (lowercase hex) of Manifest's bytes:
	// weft.manifest.hash on the agent's runs.
	ManifestHash string `json:"manifest_hash"`
	// Concurrency is how many runs of the agent the worker takes at
	// once (0: no bound of its own).
	Concurrency int `json:"concurrency,omitempty"`
}

// Validate reports what is missing from a registration.
func (r Registration) Validate() error {
	if !strings.HasPrefix(r.Build.Digest, "sha256:") || len(r.Build.Digest) <= len("sha256:") {
		return errors.New("wire: a registration needs build.digest sha256:<hex>")
	}
	if len(r.Agents) == 0 {
		return errors.New("wire: a registration names no agent")
	}
	seen := map[string]bool{}
	for _, a := range r.Agents {
		if a.Name == "" || a.ManifestHash == "" || len(a.Manifest) == 0 {
			return fmt.Errorf("wire: agent %q needs a name, a manifest and its hash", a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("wire: agent %q registered twice", a.Name)
		}
		seen[a.Name] = true
	}
	return nil
}

// Command kinds.
const (
	KindStart  = "start"
	KindResume = "resume"
)

// Command is one run command (P§6.3): start a run, or resume a parked
// one. It always names the AgentVersion; a worker refuses a version it
// does not serve.
type Command struct {
	CommandID    string            `json:"command_id"`
	Kind         string            `json:"kind"`
	RunID        string            `json:"run_id"`
	Agent        string            `json:"agent"`
	AgentVersion string            `json:"agent_version"`
	BuildDigest  string            `json:"build_digest"`
	ManifestHash string            `json:"manifest_hash"`
	TenantID     string            `json:"tenant_id"`
	Input        *Input            `json:"input,omitempty"`
	SessionID    string            `json:"session_id,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Resume       *Resume           `json:"resume,omitempty"`
	Deadline     *time.Time        `json:"deadline,omitempty"`
	Trace        *Trace            `json:"trace,omitempty"`
}

// Input is a run's input: a prompt, or messages (weft Message JSON).
type Input struct {
	Prompt   string            `json:"prompt,omitempty"`
	Messages []json.RawMessage `json:"messages,omitempty"`
}

// Resume continues a parked run as a new run linked to it.
type Resume struct {
	// ResumeOf is the parked run.
	ResumeOf string `json:"resume_of"`
	// Checkpoint is what the parked run's finished ack carried: opaque
	// to the server, read back by the worker that wrote it.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	Decisions  []Decision      `json:"decisions,omitempty"`
}

// Decision is one decision on a parked call.
type Decision struct {
	CallID string `json:"call_id"`
	// Decision is approve, deny or resolve.
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
	Content  string `json:"content,omitempty"`
}

// Trace carries the W3C trace context.
type Trace struct {
	Traceparent string `json:"traceparent,omitempty"`
}

// Validate reports a malformed command.
func (c Command) Validate() error {
	switch {
	case !ValidCommandID(c.CommandID):
		return fmt.Errorf("wire: command id %q is not cmd_<ulid>", c.CommandID)
	case c.Kind != KindStart && c.Kind != KindResume:
		return fmt.Errorf("wire: unknown command kind %q", c.Kind)
	case c.RunID == "" || c.Agent == "" || c.AgentVersion == "":
		return errors.New("wire: a command needs a run id, an agent and its agent_version")
	case c.Kind == KindResume && (c.Resume == nil || c.Resume.ResumeOf == ""):
		return errors.New("wire: a resume command needs resume.resume_of")
	case c.Kind == KindStart && c.Resume != nil:
		return errors.New("wire: a start command carries no resume")
	}
	return nil
}

// Steer delivers user messages into a running run at its next drain
// point (P§7.5): after a step's tool batch, or at a final step, which
// then takes one more step. It is run-scoped (it goes to the worker
// holding the run) and answered accepted or rejected.
type Steer struct {
	CommandID string `json:"command_id"`
	RunID     string `json:"run_id"`
	// Messages are weft Message JSON, every one role user.
	Messages []json.RawMessage `json:"messages"`
}

// Validate reports a malformed steer.
func (s Steer) Validate() error {
	switch {
	case !ValidCommandID(s.CommandID):
		return fmt.Errorf("wire: command id %q is not cmd_<ulid>", s.CommandID)
	case s.RunID == "":
		return errors.New("wire: a steer needs a run id")
	case len(s.Messages) == 0:
		return errors.New("wire: a steer carries no message")
	}
	for i, m := range s.Messages {
		var probe struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(m, &probe); err != nil {
			return fmt.Errorf("wire: steer message %d: %w", i, err)
		}
		if probe.Role != "user" {
			return fmt.Errorf("wire: steer message %d has role %q; only user messages steer", i, probe.Role)
		}
	}
	return nil
}

// Cancel causes (P§6.3).
const (
	CauseUser           = "user"
	CauseBudget         = "budget"
	CauseTimeout        = "timeout"
	CauseInterrupt      = "interrupt"
	CauseShutdown       = "shutdown"
	CauseParent         = "parent"
	CauseVersionRetired = "version_retired"
)

// Cancel asks a worker to cancel a run it holds. It has its own id, so
// it survives a reconnect and is acked once.
type Cancel struct {
	CommandID string `json:"command_id"`
	RunID     string `json:"run_id"`
	Cause     string `json:"cause"`
}

// AckState is an ack's state. accepted and rejected answer a command
// before anything runs (at-most-once); run_started names the run;
// heartbeat re-confirms a held command; finished ends it; busy hands
// it back without counting as a retry.
type AckState string

const (
	AckAccepted   AckState = "accepted"
	AckRejected   AckState = "rejected"
	AckBusy       AckState = "busy"
	AckRunStarted AckState = "run_started"
	AckHeartbeat  AckState = "heartbeat"
	AckFinished   AckState = "finished"
)

// Run statuses a finished ack reports.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
	StatusParked    = "parked"
)

// Ack is a worker's answer about one command.
type Ack struct {
	CommandID string   `json:"command_id"`
	State     AckState `json:"state"`
	// RunID names the run (run_started, finished).
	RunID string `json:"run_id,omitempty"`
	// Status is the run's end (finished only).
	Status string `json:"status,omitempty"`
	// Error says why: a rejection's reason, a failed run's error.
	Error string `json:"error,omitempty"`
	// Cause is a canceled run's cancel cause.
	Cause string `json:"cause,omitempty"`
	// Pending are a parked run's calls awaiting a decision.
	Pending []PendingCall `json:"pending,omitempty"`
	// Checkpoint is a parked run's state, sent back on its resume.
	Checkpoint json.RawMessage `json:"checkpoint,omitempty"`
	// Usage is the run's token usage (finished only).
	Usage *Usage `json:"usage,omitempty"`
}

// PendingCall is one parked call.
type PendingCall struct {
	CallID string          `json:"call_id"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// Usage is a run's token usage.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Final reports whether the ack ends its command.
func (a Ack) Final() bool {
	return a.State == AckFinished || a.State == AckRejected || a.State == AckBusy
}

// Frame events: what travels on a worker's stream, down (welcome,
// ping, run, cancel, steer, drain) and up (register, ack). disconnected
// never crosses a socket: an executor yields it from Events when its
// worker's link ended.
const (
	EventPing         = "ping"
	EventRun          = "run"
	EventCancel       = "cancel"
	EventSteer        = "steer"
	EventDrain        = "drain"
	EventRegister     = "register"
	EventWelcome      = "welcome"
	EventAck          = "ack"
	EventDisconnected = "disconnected"
)

// Hello is a worker's register frame on a socket: the per-child token
// its supervisor handed it (WEFTBASE_TOKEN) and its registration.
type Hello struct {
	Token        string       `json:"token,omitempty"`
	Registration Registration `json:"registration"`
}

// Welcome is the server's answer to a register frame: the worker's id
// and how often the server pings. A non-empty Error refuses the
// registration; the worker stops.
type Welcome struct {
	WorkerID string `json:"worker_id,omitempty"`
	PingMS   int64  `json:"ping_ms,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Disconnected is what an executor reports when its worker's link
// ended. Lost is true when the runs in the worker's hands ended with it
// (the process exited): they will never report.
type Disconnected struct {
	Reason string `json:"reason"`
	Lost   bool   `json:"lost"`
}

// MaxFrameBytes is the largest frame either side accepts.
const MaxFrameBytes = 8 << 20

// Frame is one message on a worker's stream.
type Frame struct {
	Event string `json:"event"`
	// ID is set on frames that need an ack (run): the command id.
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewFrame encodes v as a frame's data.
func NewFrame(event, id string, v any) (Frame, error) {
	f := Frame{Event: event, ID: id}
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return Frame{}, fmt.Errorf("wire: frame %s: %w", event, err)
		}
		f.Data = b
	}
	return f, nil
}

// AckFrame is an ack as a frame.
func AckFrame(a Ack) Frame {
	f, _ := NewFrame(EventAck, "", a) // an Ack always marshals
	return f
}

// Decode reads the frame's data into v.
func (f Frame) Decode(v any) error {
	if len(f.Data) == 0 {
		return fmt.Errorf("wire: frame %s has no data", f.Event)
	}
	return json.Unmarshal(f.Data, v)
}

// Drain asks a worker to finish what it holds and take nothing more.
type Drain struct {
	DeadlineMS int64 `json:"deadline_ms"`
}

// NewCommandID returns a fresh cmd_<ulid>.
func NewCommandID() string { return "cmd_" + ulid.New() }

// ValidCommandID reports whether id is cmd_ followed by a 26-character
// ULID (Crockford base32).
func ValidCommandID(id string) bool {
	rest, ok := strings.CutPrefix(id, "cmd_")
	if !ok || len(rest) != 26 {
		return false
	}
	for _, c := range rest {
		if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", c) {
			return false
		}
	}
	return true
}
