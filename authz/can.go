package authz

import "github.com/weftbase/weftbase/tenant/keys"

// Action is one thing a principal may do. Every /v1 operation names
// exactly one (api/http's route table; a test checks it is complete).
type Action string

const (
	// Public needs no credential: liveness, readiness, metrics and the
	// Console's sign-in. Can is never asked about it.
	Public Action = "public"
	// AuthSession is any authenticated principal acting on its own
	// session (sign-out).
	AuthSession   Action = "auth.session"
	MetaRead      Action = "meta.read"
	RunsRead      Action = "runs.read"
	RunsContent   Action = "runs.content" // prompts, tool arguments, transcripts
	RunsDelete    Action = "runs.delete"
	RunsCancel    Action = "runs.cancel"
	RunsSteer     Action = "runs.steer"
	RunsCreate    Action = "runs.create"
	SessionsRead  Action = "sessions.read"
	SessionsWrite Action = "sessions.write"
	TracesRead    Action = "traces.read"
	EventsRead    Action = "events.read"
	AgentsRead    Action = "agents.read"
	AgentsWrite   Action = "agents.write"
	SettingsRead  Action = "settings.read"
	KeysManage    Action = "keys.manage"
	IngestWrite   Action = "ingest.write"
	WorkerConnect Action = "worker.connect"
)

// Actions lists every action, for the route-completeness test.
var Actions = []Action{
	Public, AuthSession, MetaRead, RunsRead, RunsContent, RunsDelete, RunsCancel, RunsSteer, RunsCreate,
	SessionsRead, SessionsWrite, TracesRead, EventsRead, AgentsRead, AgentsWrite,
	SettingsRead, KeysManage, IngestWrite, WorkerConnect,
}

// Decision is Can's answer.
type Decision struct {
	Allow  bool
	Reason string
}

// Can decides whether p may perform a. Until roles and restricted-key
// scopes land (Phase 2): secret keys, operators and the Local principal
// may do everything; a worker key may connect a worker and ingest; a
// publishable key may read meta only; a restricted key nothing yet.
func Can(p Principal, a Action) Decision {
	if a == Public {
		return Decision{Allow: true}
	}
	switch p.Kind {
	case Operator, Local:
		return Decision{Allow: true}
	case Key:
		if a == AuthSession {
			return Decision{Allow: true}
		}
		switch p.KeyKind {
		case keys.Secret:
			return Decision{Allow: true}
		case keys.Worker:
			if a == WorkerConnect || a == IngestWrite || a == MetaRead {
				return Decision{Allow: true}
			}
			return Decision{Reason: "a worker key connects workers and sends telemetry only"}
		case keys.Publishable:
			if a == MetaRead {
				return Decision{Allow: true}
			}
			return Decision{Reason: "a publishable key reads meta only"}
		case keys.Restricted:
			return Decision{Reason: "restricted keys have no scopes yet"}
		}
	}
	return Decision{Reason: "unknown principal"}
}
