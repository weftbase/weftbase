package weftbase

import (
	"fmt"
	"slices"
	"strings"
)

// Role is one job a node runs (plan §5.1). One binary runs any set of
// them; the default is all four.
type Role string

const (
	// RoleAPI serves /v1 (REST and SSE), the Console, /healthz,
	// /readyz and /metrics.
	RoleAPI Role = "api"
	// RoleIngest serves OTLP at /otlp/v1/{traces,logs}.
	RoleIngest Role = "ingest"
	// RoleScheduler runs the background work: the retention sweeper
	// now; queue claims, timers and relays as they land.
	RoleScheduler Role = "scheduler"
	// RoleSupervisor runs local agent builds (Phase 3; nothing yet).
	RoleSupervisor Role = "supervisor"
)

// AllRoles is every role, the default.
var AllRoles = []Role{RoleAPI, RoleIngest, RoleScheduler, RoleSupervisor}

// ParseRoles reads a comma-separated role list ("api,ingest").
func ParseRoles(s string) ([]Role, error) {
	var out []Role
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		r := Role(f)
		if !slices.Contains(AllRoles, r) {
			return nil, fmt.Errorf("unknown role %q (the roles: api, ingest, scheduler, supervisor)", f)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no role named (the roles: api, ingest, scheduler, supervisor)")
	}
	return out, nil
}

// roleSet is a node's roles.
type roleSet []Role

func (rs roleSet) has(r Role) bool { return slices.Contains(rs, r) }
