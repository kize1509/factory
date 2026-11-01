// Package config reads a repo's factory.yml and the engine's own settings
// in ~/.factory.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is a repo's factory.yml. Only what M0 uses is here so far.
type Project struct {
	Ship   string `yaml:"ship"` // commit | push | pr
	Checks struct {
		SavedPlan []string `yaml:"saved_plan"`
	} `yaml:"checks"`
	Limits struct {
		BuildLoops int `yaml:"build_loops"`
	} `yaml:"limits"`
}

const Template = `# Factory settings for this repo. See docs/PRD.md §13.
ship: commit              # commit | push | pr; a ticket's Ship mode field wins
checks:
  saved_plan: []          # e.g. ["go test ./..."]; empty means the job asks
limits:
  build_loops: 4
`

// ShipModes are the ways a job's work can leave the factory.
var ShipModes = []string{"commit", "push", "pr"}

// ShipMode picks how a job ships: the ticket's Ship mode first, then the
// repo's factory.yml, then commit. It returns "" for a value that isn't
// a ship mode.
func ShipMode(ticket, project string) string {
	for _, m := range []string{ticket, project} {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" {
			for _, ok := range ShipModes {
				if m == ok {
					return m
				}
			}
			return ""
		}
	}
	return "commit"
}

func LoadProject(repo string) (Project, error) {
	var p Project
	data, err := os.ReadFile(filepath.Join(repo, "factory.yml"))
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, yaml.Unmarshal(data, &p)
}

// Engine is ~/.factory/settings.json, edited in the admin panel.
type Engine struct {
	Wire     string              `json:"wire,omitempty"`  // Central wire prefix; found automatically when empty
	Model    string              `json:"model,omitempty"` // fallback model for roles without one
	Roles    map[string]Role     `json:"roles,omitempty"`
	Skills   map[string][]string `json:"skills,omitempty"` // role -> skill names
	YouTrack YouTrack            `json:"youtrack"`
	Limits   Limits              `json:"limits"`
}

// Role says which runtime and model play a role (PRD §12.2).
type Role struct {
	Runtime string `json:"runtime"` // pi, claude-code or codex
	Model   string `json:"model"`   // provider/id, e.g. anthropic/claude-sonnet-5-5
}

type YouTrack struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// Limits are off by default so real usage can be measured first (D34).
type Limits struct {
	Enabled  bool    `json:"enabled"`
	JobUSD   float64 `json:"job_usd,omitempty"`
	AgentUSD float64 `json:"agent_usd,omitempty"`
}

// RoleNames lists the roles in pipeline order.
var RoleNames = []string{"scheduler", "researcher", "planner", "builder", "executor", "reviewer", "humanizer", "finalizer"}

// DefaultRoles follows D29 and D31: Pi everywhere, the Claude model of
// each role's tier.
func DefaultRoles() map[string]Role {
	think, code, execute := "anthropic/claude-opus-5-5", "anthropic/claude-sonnet-5-5", "anthropic/claude-haiku-5-5"
	return map[string]Role{
		"scheduler": {"pi", think}, "planner": {"pi", think}, "reviewer": {"pi", think},
		"researcher": {"pi", code}, "builder": {"pi", code}, "humanizer": {"pi", code},
		"executor": {"pi", execute}, "finalizer": {"pi", execute},
	}
}

// CheckRole says why a runtime can't run a model, or "" when it can.
func CheckRole(r Role) string {
	provider, _, _ := strings.Cut(r.Model, "/")
	switch r.Runtime {
	case "pi":
		if provider != "anthropic" && provider != "openai" {
			return "Pi runs anthropic/… or openai/… models"
		}
	case "claude-code":
		if provider != "anthropic" {
			return "Claude Code runs Claude models only"
		}
	case "codex":
		if provider != "openai" {
			return "Codex runs GPT models only"
		}
	default:
		return "unknown runtime " + r.Runtime
	}
	return ""
}

// DefaultSkills picks model-invoked skills from mattpocock/skills that fit
// each role without breaking the factory's rules (no test writing, its own
// handoff).
func DefaultSkills() map[string][]string {
	return map[string][]string{
		"researcher": {"research"},
		"planner":    {"domain-modeling", "codebase-design"},
		"builder":    {"diagnosing-bugs", "codebase-design"},
		"reviewer":   {"code-review", "grilling"},
		"humanizer":  {"writing-for-agents"},
		"finalizer":  {"pr"},
	}
}

// SkillsFor returns the role's skills, falling back to the defaults.
func (e Engine) SkillsFor(role string) []string {
	if s, ok := e.Skills[role]; ok {
		return s
	}
	return DefaultSkills()[role]
}

// RoleFor returns the role's setting, falling back to the defaults.
func (e Engine) RoleFor(name string) Role {
	if r, ok := e.Roles[name]; ok && r.Model != "" {
		return r
	}
	return DefaultRoles()[name]
}

func Home() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".factory")
}

func SaveEngine(e Engine) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(e, "", "  ")
	return os.WriteFile(filepath.Join(Home(), "settings.json"), data, 0o600)
}

func LoadEngine() Engine {
	var e Engine
	data, err := os.ReadFile(filepath.Join(Home(), "settings.json"))
	if err == nil {
		json.Unmarshal(data, &e)
	}
	return e
}
