package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/weftbase/weft/core"

	"github.com/weftbase/weftbase/client/wire"
)

// SeenSize bounds the set of command ids a Runner remembers: a command
// id it has seen is never run twice (at-most-once). The server never
// re-sends a command, so the window only has to outlive a replayed
// frame.
const SeenSize = 4096

// DefaultHeartbeat is how often a running command is re-acked.
const DefaultHeartbeat = 60 * time.Second

// Metadata keys a Runner puts on every run it starts, so the run's
// spans and records name the AgentVersion and build it ran on.
//
// TODO(weft#13): weft gains an agent_version key on its session
// entries (prompt entry, TurnEntry, ApprovalRequestEntry,
// PoolReceiptEntry, the pool child header). Pass the AgentVersion
// through that option once it lands; the metadata stays for the
// trace store.
// TODO(weft#14): weft puts the build identity (weft.build.digest) on
// the OTel resource; drop MetaBuildDigest from the run metadata then.
const (
	MetaAgentVersion = "weftbase.agent_version"
	MetaBuildDigest  = "weftbase.build_digest"
	MetaTenant       = "weftbase.tenant_id"
)

// Runner runs one build's agents on wire commands: the at-most-once
// gate (the seen set), a goroutine per accepted command, per-agent
// capacity (busy), cancel and steer by run id, heartbeats, the finished
// ack, and drain. Every placement drives one: Serve over a socket (the
// local placement), and the server's embedded placement over Go calls.
//
// Acks after the first one (run_started, heartbeat, finished) are sent
// on Acks; whoever drives the Runner forwards them.
type Runner struct {
	build     wire.Build
	agents    map[string]*entry
	order     []string
	heartbeat time.Duration
	acks      chan wire.Ack

	base context.Context
	stop context.CancelCauseFunc

	mu       sync.Mutex
	seen     map[string]bool
	seenFIFO []string
	runs     map[string]*running
	draining bool
	wg       sync.WaitGroup
}

type entry struct {
	agent       *core.Agent
	manifest    []byte
	hash        string
	concurrency int
	inflight    int
}

type running struct {
	cmdID  string
	agent  *entry
	cancel context.CancelCauseFunc
	steer  []core.Message
}

// AgentOption configures one agent a build serves.
type AgentOption func(*AgentSpec)

// Concurrency bounds how many runs of the agent the worker takes at
// once; one past it is answered busy (requeued, not a retry). 0 (the
// default) sets no bound of its own.
func Concurrency(n int) AgentOption { return func(s *AgentSpec) { s.Concurrency = max(n, 0) } }

// AgentSpec is an agent and its options.
type AgentSpec struct {
	Agent       *core.Agent
	Concurrency int
}

// Spec builds an AgentSpec.
func Spec(a *core.Agent, opts ...AgentOption) AgentSpec {
	s := AgentSpec{Agent: a}
	for _, o := range opts {
		o(&s)
	}
	return s
}

// RunnerOption configures NewRunner.
type RunnerOption func(*Runner)

// WithHeartbeat sets how often a running command is re-acked (default
// 60 s).
func WithHeartbeat(d time.Duration) RunnerOption {
	return func(r *Runner) {
		if d > 0 {
			r.heartbeat = d
		}
	}
}

// NewRunner serves agents as the build b (b.Digest "sha256:<hex>";
// ExecutableDigest gives the running binary's). Every agent needs a
// name, unique in the build.
func NewRunner(b wire.Build, agents []AgentSpec, opts ...RunnerOption) (*Runner, error) {
	if !strings.HasPrefix(b.Digest, "sha256:") || len(b.Digest) <= len("sha256:") {
		return nil, fmt.Errorf("worker: build digest %q is not sha256:<hex>", b.Digest)
	}
	if b.WeftVersion == "" {
		b.WeftVersion = WeftVersion()
	}
	r := &Runner{
		build: b, agents: map[string]*entry{}, heartbeat: DefaultHeartbeat,
		acks: make(chan wire.Ack, 1024), seen: map[string]bool{}, runs: map[string]*running{},
	}
	for _, o := range opts {
		o(r)
	}
	if len(agents) == 0 {
		return nil, errors.New("worker: a build needs at least one agent")
	}
	for _, s := range agents {
		a := s.Agent
		if a == nil || a.Name() == "" {
			return nil, errors.New("worker: an agent needs a name (weft.Name)")
		}
		if _, dup := r.agents[a.Name()]; dup {
			return nil, fmt.Errorf("worker: agent %q given twice", a.Name())
		}
		m, err := core.Manifest(a)
		if err != nil {
			return nil, fmt.Errorf("worker: manifest of %s: %w", a.Name(), err)
		}
		sum := sha256.Sum256(m)
		r.agents[a.Name()] = &entry{agent: a, manifest: m, hash: hex.EncodeToString(sum[:]), concurrency: s.Concurrency}
		r.order = append(r.order, a.Name())
	}
	r.base, r.stop = context.WithCancelCause(context.Background())
	return r, nil
}

// WeftVersion is the weft core module's version this binary was built
// with ("" when unknown, as in a workspace build).
func WeftVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/weftbase/weft/core" {
				return d.Version
			}
		}
	}
	return ""
}

var (
	exeOnce   sync.Once
	exeDigest string
	exeErr    error
)

// ExecutableDigest is sha256 of the running executable as a build
// digest ("sha256:<hex>"), computed once.
func ExecutableDigest() (string, error) {
	exeOnce.Do(func() {
		path, err := os.Executable()
		if err != nil {
			exeErr = err
			return
		}
		exeDigest, exeErr = FileDigest(path)
	})
	return exeDigest, exeErr
}

// FileDigest is sha256 of the file at path as a build digest.
func FileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Registration is the build's registration for placement.
func (r *Runner) Registration(placement string) wire.Registration {
	reg := wire.Registration{
		Placement: placement, Build: r.build, PID: os.Getpid(),
		Capabilities: []string{wire.EventRun, wire.EventCancel, wire.EventSteer}, HeartbeatMS: r.heartbeat.Milliseconds(),
	}
	if h, err := os.Hostname(); err == nil {
		reg.Host = h
		reg.Instance = fmt.Sprintf("%s/%d", h, reg.PID)
	}
	if wd, err := os.Getwd(); err == nil {
		reg.Cwd = wd
	}
	for _, n := range r.order {
		a := r.agents[n]
		reg.Agents = append(reg.Agents, wire.AgentRegistration{Name: n, Manifest: a.manifest, ManifestHash: a.hash, Concurrency: a.concurrency})
	}
	return reg
}

// Manifest is the build's manifest: every agent's one-agent manifest
// by name (what `<build> --manifest` prints).
func (r *Runner) Manifest() map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(r.order))
	for _, n := range r.order {
		out[n] = r.agents[n].manifest
	}
	return out
}

// Acks are the acks the Runner sends after a command was accepted:
// run_started, heartbeat, finished.
func (r *Runner) Acks() <-chan wire.Ack { return r.acks }

// Done is closed by Close.
func (r *Runner) Done() <-chan struct{} { return r.base.Done() }

// InFlight is the number of runs the Runner holds.
func (r *Runner) InFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runs)
}

// markSeen records id; false when it was seen before.
func (r *Runner) markSeen(id string) bool {
	if r.seen[id] {
		return false
	}
	r.seen[id] = true
	r.seenFIFO = append(r.seenFIFO, id)
	if len(r.seenFIFO) > SeenSize {
		delete(r.seen, r.seenFIFO[0])
		r.seenFIFO = r.seenFIFO[1:]
	}
	return true
}

// Admit is the at-most-once gate. It answers cmd — accepted, rejected
// or busy — before anything runs. When accepted, the caller delivers
// the ack and then calls start: start(true) runs the command;
// start(false) (the ack could not be delivered) runs nothing and
// settles it with a finished ack, failed "not run", so a side that
// recorded the accepted ack is not left waiting. start is nil unless
// the ack is accepted, and must be called exactly once.
//
// A command id seen before is rejected; so is a command for a version
// this build does not serve, and a run id already running here. A
// draining Runner, or an agent at its concurrency, answers busy.
func (r *Runner) Admit(cmd wire.Command) (wire.Ack, func(confirmed bool)) {
	reject := func(why string) (wire.Ack, func(bool)) {
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckRejected, Error: why}, nil
	}
	if err := cmd.Validate(); err != nil {
		return reject(err.Error())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.draining || r.base.Err() != nil {
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckBusy}, nil
	}
	if !r.markSeen(cmd.CommandID) {
		return reject("command " + cmd.CommandID + " was seen before")
	}
	a, ok := r.agents[cmd.Agent]
	if !ok || cmd.BuildDigest != r.build.Digest || cmd.ManifestHash != a.hash {
		return reject(fmt.Sprintf("agent_version %s (%s, build %s, manifest %s) is not served here", cmd.AgentVersion, cmd.Agent, cmd.BuildDigest, cmd.ManifestHash))
	}
	if _, dup := r.runs[cmd.RunID]; dup {
		return reject("run " + cmd.RunID + " is already running here")
	}
	if a.concurrency > 0 && a.inflight >= a.concurrency {
		return wire.Ack{CommandID: cmd.CommandID, State: wire.AckBusy}, nil
	}
	ctx, cancel := context.WithCancelCause(r.base)
	run := &running{cmdID: cmd.CommandID, agent: a, cancel: cancel}
	r.runs[cmd.RunID] = run
	a.inflight++
	r.wg.Add(1)
	var once sync.Once
	start := func(confirmed bool) {
		once.Do(func() {
			if confirmed {
				go r.run(ctx, run, cmd)
				return
			}
			r.finish(cmd.RunID, run)
			r.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: cmd.RunID, Status: wire.StatusFailed,
				Error: "not run: the accepted ack was not delivered"})
			r.wg.Done()
		})
	}
	return wire.Ack{CommandID: cmd.CommandID, State: wire.AckAccepted, RunID: cmd.RunID}, start
}

// Dispatch admits cmd and, when accepted, runs it: for a transport
// that delivers the ack by returning it (the embedded placement).
func (r *Runner) Dispatch(cmd wire.Command) wire.Ack {
	ack, start := r.Admit(cmd)
	if start != nil {
		start(true)
	}
	return ack
}

// finish forgets a run (its slot and its steering queue).
func (r *Runner) finish(runID string, run *running) {
	r.mu.Lock()
	if r.runs[runID] == run {
		delete(r.runs, runID)
	}
	run.agent.inflight--
	r.mu.Unlock()
	run.cancel(nil)
}

// send hands an ack to whoever drives the Runner; it gives up when the
// Runner is closed.
func (r *Runner) send(a wire.Ack) {
	select {
	case r.acks <- a:
	case <-r.base.Done():
	}
}

// checkpoint is a parked run's state: opaque to the server, sent back
// on the run's resume to a worker of the same AgentVersion.
type checkpoint struct {
	Messages []core.Message `json:"messages"`
	Park     *core.ParkRule `json:"park,omitempty"`
}

func (r *Runner) run(ctx context.Context, run *running, cmd wire.Command) {
	defer r.wg.Done()
	defer r.finish(cmd.RunID, run)
	ack := wire.Ack{CommandID: cmd.CommandID, State: wire.AckFinished, RunID: cmd.RunID}
	func() {
		defer func() {
			if p := recover(); p != nil {
				ack.Status, ack.Error = wire.StatusFailed, fmt.Sprintf("panic: %v", p)
			}
		}()
		opts, err := r.runOptions(cmd)
		if err != nil {
			ack.Status, ack.Error = wire.StatusFailed, err.Error()
			return
		}
		r.send(wire.Ack{CommandID: cmd.CommandID, State: wire.AckRunStarted, RunID: cmd.RunID})
		beat := time.NewTicker(r.heartbeat)
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-beat.C:
					// A beat that finds the queue full is dropped: the
					// next one, or the finished ack, says the same.
					select {
					case r.acks <- wire.Ack{CommandID: cmd.CommandID, State: wire.AckHeartbeat, RunID: cmd.RunID}:
					default:
					}
				}
			}
		}()
		res, err := run.agent.agent.Generate(ctx, opts...)
		beat.Stop()
		close(done)
		if res != nil {
			ack.Usage = &wire.Usage{InputTokens: res.Usage.InputTokens, OutputTokens: res.Usage.OutputTokens}
		}
		switch {
		case err != nil && ctx.Err() != nil:
			ack.Status, ack.Cause = wire.StatusCanceled, causeOf(ctx)
		case err != nil:
			ack.Status, ack.Error = wire.StatusFailed, err.Error()
		case len(res.Pending) > 0:
			ack.Status = wire.StatusParked
			for _, p := range res.Pending {
				ack.Pending = append(ack.Pending, wire.PendingCall{CallID: p.ID, Tool: p.Name, Args: p.Args})
			}
			ack.Checkpoint, _ = json.Marshal(checkpoint{Messages: res.Messages, Park: res.Park})
		default:
			ack.Status = wire.StatusSucceeded
		}
	}()
	r.send(ack)
}

func causeOf(ctx context.Context) string {
	var c core.CancelCause
	if errors.As(context.Cause(ctx), &c) {
		return string(c)
	}
	return wire.CauseInterrupt
}

// runOptions turns a command into weft run options.
func (r *Runner) runOptions(cmd wire.Command) ([]core.RunOption, error) {
	md := map[string]string{}
	for k, v := range cmd.Metadata {
		md[k] = v
	}
	md[MetaAgentVersion], md[MetaBuildDigest] = cmd.AgentVersion, cmd.BuildDigest
	if cmd.TenantID != "" {
		md[MetaTenant] = cmd.TenantID
	}
	opts := []core.RunOption{core.RunID(cmd.RunID), core.Metadata(md), core.Steering(r.steering(cmd.RunID))}
	switch cmd.Kind {
	case wire.KindResume:
		var cp checkpoint
		if len(cmd.Resume.Checkpoint) == 0 {
			return nil, errors.New("worker: a resume command carries no checkpoint")
		}
		if err := json.Unmarshal(cmd.Resume.Checkpoint, &cp); err != nil {
			return nil, fmt.Errorf("worker: checkpoint: %w", err)
		}
		opts = append(opts, core.Messages(cp.Messages...), core.ResumeOf(cmd.Resume.ResumeOf))
		opts = append(opts, cp.Park.Options()...)
		for _, d := range cmd.Resume.Decisions {
			switch d.Decision {
			case "approve":
				opts = append(opts, core.Approve(d.CallID))
			case "deny":
				opts = append(opts, core.Deny(d.CallID, d.Reason))
			case "resolve":
				opts = append(opts, core.Resolve(d.CallID, d.Content))
			default:
				return nil, fmt.Errorf("worker: unknown decision %q", d.Decision)
			}
		}
	default:
		if cmd.Input == nil {
			return nil, errors.New("worker: a start command carries no input")
		}
		if len(cmd.Input.Messages) > 0 {
			msgs := make([]core.Message, len(cmd.Input.Messages))
			for i, raw := range cmd.Input.Messages {
				if err := json.Unmarshal(raw, &msgs[i]); err != nil {
					return nil, fmt.Errorf("worker: input message %d: %w", i, err)
				}
			}
			opts = append(opts, core.Messages(msgs...))
		}
		if cmd.Input.Prompt != "" {
			opts = append(opts, core.Prompt(cmd.Input.Prompt))
		}
	}
	return opts, nil
}

// steering is a run's steering source: it drains the messages Steer
// queued for the run, never waiting.
func (r *Runner) steering(runID string) core.SteerFunc {
	return func(context.Context, core.SteerPoint) []core.Message {
		r.mu.Lock()
		defer r.mu.Unlock()
		run := r.runs[runID]
		if run == nil || len(run.steer) == 0 {
			return nil
		}
		out := run.steer
		run.steer = nil
		return out
	}
}

// Cancel cancels a run the Runner holds with c's cause (default user);
// a run it does not hold is ignored (it ended, or never ran here).
func (r *Runner) Cancel(c wire.Cancel) {
	r.mu.Lock()
	run := r.runs[c.RunID]
	r.mu.Unlock()
	if run == nil {
		return
	}
	cause := c.Cause
	if cause == "" {
		cause = wire.CauseUser
	}
	run.cancel(core.CancelCause(cause))
}

// Steer queues s's messages for a run the Runner holds: the run drains
// them at its next drain point (after a step's tool batch, or at a
// final step). It answers accepted, or rejected for a malformed steer
// or a run not running here. A steer that arrives after the run's last
// drain point is not delivered (weft: a park or a StopWhen end stays an
// end).
func (r *Runner) Steer(s wire.Steer) wire.Ack {
	reject := func(why string) wire.Ack {
		return wire.Ack{CommandID: s.CommandID, State: wire.AckRejected, RunID: s.RunID, Error: why}
	}
	if err := s.Validate(); err != nil {
		return reject(err.Error())
	}
	msgs := make([]core.Message, len(s.Messages))
	for i, raw := range s.Messages {
		if err := json.Unmarshal(raw, &msgs[i]); err != nil {
			return reject(fmt.Sprintf("steer message %d: %v", i, err))
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.markSeen(s.CommandID) {
		return reject("command " + s.CommandID + " was seen before")
	}
	run := r.runs[s.RunID]
	if run == nil {
		return reject("run " + s.RunID + " is not running here")
	}
	run.steer = append(run.steer, msgs...)
	return wire.Ack{CommandID: s.CommandID, State: wire.AckAccepted, RunID: s.RunID}
}

// Drain takes no more commands (they are answered busy), lets the
// running ones finish until deadline, then cancels the rest (cause
// shutdown) and waits for them, bounded by ctx. Their finished acks are
// still sent on Acks.
func (r *Runner) Drain(ctx context.Context, deadline time.Time) error {
	r.mu.Lock()
	r.draining = true
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	for _, run := range r.runs {
		run.cancel(core.CancelCause(wire.CauseShutdown))
	}
	r.mu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close cancels every run (cause shutdown) and stops the Runner: acks
// not yet taken from Acks are dropped. Call it after Drain.
func (r *Runner) Close() {
	r.mu.Lock()
	r.draining = true
	for _, run := range r.runs {
		run.cancel(core.CancelCause(wire.CauseShutdown))
	}
	r.mu.Unlock()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	r.stop(errors.New("worker: closed"))
}

// vcs is the binary's source revision, when the toolchain stamped one.
func vcs() *wire.VCS {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	var v wire.VCS
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			v.Revision = s.Value
		case "vcs.modified":
			v.Modified = s.Value == "true"
		}
	}
	if v.Revision == "" {
		return nil
	}
	return &v
}
