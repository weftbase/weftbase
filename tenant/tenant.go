// Package tenant holds the Scope every tenant-scoped read and write
// runs under, and the reserved identifiers of the default project.
//
// Every tenant-scoped row in the three stores carries
// (project_id, env, tenant_id). A Scope is built only by authz (from a
// verified credential) and by the tenant packages themselves; the
// layering check refuses a NewScope call anywhere else, so a handler
// cannot invent a tenant. The package imports the standard library
// only; the services (keys, later tenants and end users) live in its
// subpackages.
package tenant

import (
	"errors"
	"strings"
)

// The defaults until projects and tenants exist as objects (Phase 2):
// one project, the test environment of the bootstrap key, and the
// reserved project-level tenant.
const (
	// DefaultProject is the single project of a fresh install.
	DefaultProject = "prj_default"
	// DefaultEnv is the environment of the bootstrap key (wb_sk_test_…).
	DefaultEnv = "test"
	// ProjectTenant is the reserved tenant of agents that serve no
	// particular tenant.
	ProjectTenant = "ten_project"
)

// Envs are the environments a key belongs to.
var Envs = []string{"test", "live"}

// Scope is the (project, env, tenant) a request runs under. Its fields
// are unexported: a Scope comes from authz, never from a request body
// or a metadata attribute.
type Scope struct{ project, env, tenant string }

// NewScope builds a Scope. Only authz and the tenant packages call it
// (scripts/layering enforces it; tests are exempt).
func NewScope(project, env, tenant string) (Scope, error) {
	if project == "" || env == "" || tenant == "" {
		return Scope{}, errors.New("tenant: a scope needs a project, an env and a tenant")
	}
	if !ValidEnv(env) {
		return Scope{}, errors.New("tenant: unknown env " + env)
	}
	return Scope{project, env, tenant}, nil
}

// Default is the default project's test environment, project-level
// tenant: what a fresh single-node install runs under.
func Default() Scope { return Scope{DefaultProject, DefaultEnv, ProjectTenant} }

// ValidEnv reports whether env is a known environment.
func ValidEnv(env string) bool {
	for _, e := range Envs {
		if e == env {
			return true
		}
	}
	return false
}

func (s Scope) Project() string { return s.project }
func (s Scope) Env() string     { return s.env }
func (s Scope) Tenant() string  { return s.tenant }

// IsZero reports whether s is the zero Scope (no request ever runs
// under it; a repository refuses it).
func (s Scope) IsZero() bool { return s == Scope{} }

// ProjectLevel reports whether s is the project-level tenant.
func (s Scope) ProjectLevel() bool { return s.tenant == ProjectTenant }

// ErrOutsideScope: Within was asked for a tenant the scope cannot act
// for.
var ErrOutsideScope = errors.New("tenant: the scope cannot act for that tenant")

// Within is s narrowed to tenantID, in s's project and env: what a
// project-level principal acts under on one tenant's row (the row's own
// tenant, so its events reach that tenant's subscribers). Only a
// project-level s may narrow to another tenant; any s may name its own.
// Anything else is ErrOutsideScope.
func (s Scope) Within(tenantID string) (Scope, error) {
	switch {
	case s.IsZero() || tenantID == "":
		return Scope{}, ErrOutsideScope
	case tenantID == s.tenant:
		return s, nil
	case s.ProjectLevel():
		return Scope{s.project, s.env, tenantID}, nil
	}
	return Scope{}, ErrOutsideScope
}

// String is "project/env/tenant", for logs.
func (s Scope) String() string {
	return strings.Join([]string{s.project, s.env, s.tenant}, "/")
}

// ErrZeroScope is what a repository returns for the zero Scope.
var ErrZeroScope = errors.New("tenant: zero scope")
