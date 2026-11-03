package prompts

import (
	"strings"
	"testing"
)

// The team rules are part of every role's system prompt.
func TestSystemAddsTheTeamRules(t *testing.T) {
	for _, role := range []string{"planner", "builder", "reviewer"} {
		s := System(role)
		if !strings.HasPrefix(s, strings.TrimRight(Default(role), "\n")) || !strings.Contains(s, "ask_agent") || !strings.Contains(s, "handover") {
			t.Errorf("%s: system prompt lacks the role text or the team rules", role)
		}
	}
}
