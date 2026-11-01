// Package runtime drives coding agents. M0 has the Pi runtime, which talks
// to the pi-host sidecar; Claude Code and Codex come later behind the same
// interface.
package runtime

import "context"

type AgentSpec struct {
	Job      string
	Agent    string   // e.g. "archimedes"
	Cwd      string   // the job's worktree
	Model    string   // provider/id, e.g. anthropic/claude-sonnet-5-5
	System   string   // role prompt, added to the runtime's own system prompt
	Tools    []string // built-in tools to enable
	Thinking string   // off, low, medium, high
	Role     string   // e.g. builder
	Skills   []string // skill folders (each with a SKILL.md) this agent gets
}

type Agent interface {
	// Prompt runs one turn and waits until the agent is idle. It returns
	// the agent's last reply.
	Prompt(ctx context.Context, text string) (string, error)
	// Steer delivers a message while the agent works (your UFO).
	Steer(text string) error
	Stop() error
}

type Runtime interface {
	Start(ctx context.Context, spec AgentSpec) (Agent, error)
}
