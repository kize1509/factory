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
	Tracker Tracker `yaml:"tracker"`
}

// Tracker maps the factory project to a YouTrack project and says how far
// the operations agent may go (PRD §13). The URL and token stay in settings.
type Tracker struct {
	Kind    string `yaml:"kind"`    // youtrack; empty means no tracker
	Project string `yaml:"project"` // YouTrack project short name, e.g. HF
	Level   string `yaml:"level"`   // observe | propose | file | fix; default fix
	States  struct {
		Working string `yaml:"working"` // an empty name means: don't move
		Blocked string `yaml:"blocked"`
		Review  string `yaml:"review"`
		Done    string `yaml:"done"`
	} `yaml:"states"`
	MaxCreatedPerJob  int `yaml:"max_created_per_job"`
	MaxCreatedPerPass int `yaml:"max_created_per_pass"`
}

// Control levels, from least to most the operations agent may do.
var Levels = []string{"observe", "propose", "file", "fix"}

// ValidLevel returns the level in lower case, "" when it isn't one.
func ValidLevel(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	for _, ok := range Levels {
		if l == ok {
			return l
		}
	}
	return ""
}

// Normalised fills in the defaults: level fix, five issues per job, ten per pass.
func (t Tracker) Normalised() Tracker {
	if t.Level = ValidLevel(t.Level); t.Level == "" {
		t.Level = "fix"
	}
	if t.MaxCreatedPerJob <= 0 {
		t.MaxCreatedPerJob = 5
	}
	if t.MaxCreatedPerPass <= 0 {
		t.MaxCreatedPerPass = 10
	}
	return t
}

const Template = `# Factory settings for this repo. See docs/PRD.md §13.
ship: commit              # commit | push | pr; a ticket's Ship mode field wins
checks:
  saved_plan: []          # e.g. ["go test ./..."]; empty means the job asks
limits:
  build_loops: 4
# tracker:                # lets the operations agent (Hermes) run the board
#   kind: youtrack        # the URL and token are in Admin → Connections
#   project: ABC          # the YouTrack project for this repo
#   level: fix            # observe | propose | file | fix
#   states: {working: "In Progress", blocked: "", review: "In Review", done: "Done"}  # "" means don't move
#   max_created_per_job: 5
#   max_created_per_pass: 10
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
	Wire  string          `json:"wire,omitempty"`  // Central wire prefix; found automatically when empty
	Model string          `json:"model,omitempty"` // fallback model for roles without one
	Roles map[string]Role `json:"roles,omitempty"`
	// Projects overrides roles for one repo (by path), e.g. a cheaper
	// operations model for a small project.
	Projects map[string]ProjectRoles `json:"projects,omitempty"`
	Skills   map[string][]string     `json:"skills,omitempty"` // role -> skill names
	YouTrack YouTrack                `json:"youtrack"`
	Limits   Limits                  `json:"limits"`
}

// ProjectRoles are the role settings one project overrides.
type ProjectRoles struct {
	Roles map[string]Role `json:"roles,omitempty"`
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
var RoleNames = []string{"scheduler", "ops", "researcher", "planner", "builder", "executor", "reviewer", "humanizer", "finalizer"}

// DefaultRoles follows D29 and D31: Pi everywhere, the Claude model of
// each role's tier.
func DefaultRoles() map[string]Role {
	think, code, execute := "anthropic/claude-opus-5-5", "anthropic/claude-sonnet-5-5", "anthropic/claude-haiku-5-5"
	return map[string]Role{
		"scheduler": {"pi", think}, "planner": {"pi", think}, "reviewer": {"pi", think},
		"researcher": {"pi", code}, "builder": {"pi", code}, "humanizer": {"pi", code},
		"executor": {"pi", execute}, "finalizer": {"pi", execute},
		"ops": {"pi", "openai/gpt-6.1-terra"}, // Hermes, the operations agent (D2, D29)
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

// DefaultSkills picks model-invoked skills that fit each role without
// breaking the factory's rules (no test writing, its own handoff). The
// design skills (impeccable, and journey, fortify, include and articulate
// from Intent) only trigger on UI and UX work, so other tasks don't pay
// for them beyond a line each in the skill list.
func DefaultSkills() map[string][]string {
	design := []string{"impeccable", "journey", "fortify", "include", "articulate"}
	return map[string][]string{
		"researcher": {"research"},
		"planner":    append([]string{"domain-modeling", "codebase-design"}, design...),
		"builder":    append([]string{"diagnosing-bugs", "codebase-design"}, design...),
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

// RoleForProject returns the role for one repo: its override if it has
// one, else the engine-wide setting.
func (e Engine) RoleForProject(repo, name string) Role {
	if r, ok := e.Projects[repo].Roles[name]; ok && r.Model != "" {
		return r
	}
	return e.RoleFor(name)
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
