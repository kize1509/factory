package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShipMode(t *testing.T) {
	for _, c := range []struct{ ticket, project, want string }{
		{"", "", "commit"}, {"", "commit", "commit"}, {"Push", "commit", "push"}, {"PR", "", "pr"}, {"deploy", "pr", ""}, {"", "yolo", ""},
	} {
		if got := ShipMode(c.ticket, c.project); got != c.want {
			t.Errorf("ShipMode(%q, %q) = %q, want %q", c.ticket, c.project, got, c.want)
		}
	}
}

func TestOpsRoleDefaultsToTerraAndOverridesPerProject(t *testing.T) {
	var e Engine
	if r := e.RoleFor("ops"); r.Model != "openai/gpt-6.1-terra" || r.Runtime != "pi" || CheckRole(r) != "" {
		t.Fatalf("default ops role %+v", r)
	}
	e.Projects = map[string]ProjectRoles{"/repo/a": {Roles: map[string]Role{"ops": {"pi", "anthropic/claude-haiku-5-5"}}}}
	if e.RoleForProject("/repo/a", "ops").Model != "anthropic/claude-haiku-5-5" {
		t.Fatal("the project override should win")
	}
	if e.RoleForProject("/repo/b", "ops").Model != "openai/gpt-6.1-terra" {
		t.Fatal("other projects keep the default")
	}
	if e.RoleForProject("/repo/a", "builder") != e.RoleFor("builder") {
		t.Fatal("roles without an override fall back")
	}
}

func TestTrackerDefaultsAndLevels(t *testing.T) {
	tr := Tracker{}.Normalised()
	if tr.Level != "fix" || tr.MaxCreatedPerJob != 5 || tr.MaxCreatedPerPass != 10 {
		t.Fatalf("%+v", tr)
	}
	if ValidLevel(" Propose ") != "propose" || ValidLevel("root") != "" {
		t.Fatal("ValidLevel")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "factory.yml"), []byte("tracker:\n  kind: youtrack\n  project: HF\n  states: {working: \"In Progress\", blocked: \"\"}\n"), 0o644)
	p, err := LoadProject(dir)
	if err != nil || p.Tracker.Project != "HF" || p.Tracker.States.Working != "In Progress" || p.Tracker.States.Blocked != "" {
		t.Fatalf("%v %+v", err, p.Tracker)
	}
}
