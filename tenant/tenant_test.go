package tenant

import (
	"errors"
	"testing"
)

// Within narrows a project-level scope to any tenant of its project and
// env, and any scope to its own tenant; nothing else.
func TestWithin(t *testing.T) {
	project := Default()
	acme, err := project.Within("ten_acme")
	if err != nil || acme.Tenant() != "ten_acme" || acme.Project() != project.Project() || acme.Env() != project.Env() || acme.ProjectLevel() {
		t.Fatalf("project.Within(ten_acme) = %v, %v", acme, err)
	}
	if own, err := acme.Within("ten_acme"); err != nil || own != acme {
		t.Errorf("acme.Within(its own) = %v, %v", own, err)
	}
	if back, err := project.Within(ProjectTenant); err != nil || back != project {
		t.Errorf("project.Within(its own) = %v, %v", back, err)
	}
	for _, c := range []struct {
		s      Scope
		tenant string
	}{{acme, "ten_other"}, {acme, ProjectTenant}, {project, ""}, {Scope{}, "ten_acme"}} {
		if got, err := c.s.Within(c.tenant); !errors.Is(err, ErrOutsideScope) || !got.IsZero() {
			t.Errorf("%v.Within(%q) = %v, %v; want ErrOutsideScope", c.s, c.tenant, got, err)
		}
	}
}
