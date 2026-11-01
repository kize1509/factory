package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"factory/internal/events"
	"factory/internal/skills"
)

// Codex runs each prompt as one `codex exec --json` run in the worktree and
// resumes the same session for the next prompt. Codex can't take a message
// in the middle of a run, so Steer stops the run and resumes the session
// with your message (PRD §10.3a). The model provider and the factory's MCP
// tools are set with -c for these runs only; your Codex config is untouched.
type Codex struct {
	Engine string
	Bus    *events.Bus
}

func (c *Codex) Start(_ context.Context, s AgentSpec) (Agent, error) {
	if !strings.HasPrefix(s.Model, "openai/") {
		return nil, fmt.Errorf("Codex runs GPT models only, not %s", s.Model)
	}
	c.Bus.Emit(s.Job, s.Agent, "agent.started", map[string]any{"runtime": "codex", "model": s.Model})
	return &codexAgent{c: c, spec: s}, nil
}

type codexAgent struct {
	c      *Codex
	spec   AgentSpec
	mu     sync.Mutex
	thread string // Codex session id, set by the first run
	cmd    *exec.Cmd
	steer  string // a message waiting to be delivered by resuming
	turns  int
}

func (a *codexAgent) emit(typ string, data map[string]any) {
	a.c.Bus.Emit(a.spec.Job, a.spec.Agent, typ, data)
}

func (a *codexAgent) args(text string) []string {
	_, model, _ := strings.Cut(a.spec.Model, "/")
	gw := fmt.Sprintf("%s/llm/%s/%s/codex/openai/v1", a.c.Engine, a.spec.Job, a.spec.Agent)
	args := []string{"exec"}
	if a.thread != "" {
		args = append(args, "resume", a.thread)
	}
	args = append(args, "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-m", model,
		"-c", "model_provider=factory",
		"-c", "model_providers.factory.name=factory",
		"-c", "model_providers.factory.base_url="+gw,
		"-c", "model_providers.factory.wire_api=responses",
		"-c", "mcp_servers.factory.url="+fmt.Sprintf("%s/mcp/%s/%s", a.c.Engine, a.spec.Job, a.spec.Agent))
	if a.thread == "" {
		args = append(args, "-C", a.spec.Cwd)
	}
	return append(args, text)
}

func (a *codexAgent) Prompt(ctx context.Context, text string) (string, error) {
	a.mu.Lock()
	if a.turns == 0 {
		// Codex has no flag to add to its system prompt or load a skill
		// list per run, so both go in front of the first prompt
		if list := skills.Instructions(a.spec.Skills); list != "" {
			text = list + "\n" + text
		}
		if a.spec.System != "" {
			text = a.spec.System + "\n\n" + text
		}
	}
	a.turns++
	a.mu.Unlock()
	for {
		reply, err := a.run(ctx, text)
		a.mu.Lock()
		next := a.steer
		a.steer = ""
		a.mu.Unlock()
		if next == "" {
			return reply, err
		}
		text = next // the run was stopped to deliver your message: resume with it
	}
}

func (a *codexAgent) run(ctx context.Context, text string) (string, error) {
	cmd := exec.CommandContext(ctx, "codex", a.args(text)...)
	cmd.Dir = a.spec.Cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start codex: %w", err)
	}
	a.mu.Lock()
	a.cmd = cmd
	a.mu.Unlock()
	reply, failure := a.read(out)
	werr := cmd.Wait()
	a.mu.Lock()
	a.cmd = nil
	steered := a.steer != ""
	a.mu.Unlock()
	if steered {
		return reply, nil
	}
	if failure != "" {
		return reply, errors.New(failure)
	}
	if werr != nil {
		return reply, fmt.Errorf("codex: %w", werr)
	}
	return reply, nil
}

// read turns codex exec's JSON lines into factory events and returns the
// agent's last message.
func (a *codexAgent) read(out io.Reader) (reply, failure string) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var m struct {
			Type     string `json:"type"`
			ThreadID string `json:"thread_id"`
			Message  string `json:"message"`
			Error    struct {
				Message string `json:"message"`
			} `json:"error"`
			Item struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Command  string `json:"command"`
				ExitCode *int   `json:"exit_code"`
				Status   string `json:"status"`
				Tool     string `json:"tool"`
				Server   string `json:"server"`
				Changes  []struct {
					Path string `json:"path"`
				} `json:"changes"`
			} `json:"item"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		it := m.Item
		switch m.Type {
		case "thread.started":
			a.mu.Lock()
			a.thread = m.ThreadID
			a.mu.Unlock()
		case "item.started":
			switch it.Type {
			case "command_execution":
				a.emit("agent.tool", map[string]any{"tool": "bash", "args": map[string]any{"command": it.Command}})
			case "mcp_tool_call":
				a.emit("agent.tool", map[string]any{"tool": it.Tool})
			}
		case "item.completed":
			switch it.Type {
			case "reasoning":
				a.emit("agent.thinking", map[string]any{"text": it.Text})
			case "agent_message":
				reply = it.Text
				a.emit("agent.text", map[string]any{"text": it.Text})
			case "command_execution":
				a.emit("agent.tool.done", map[string]any{"tool": "bash", "error": it.ExitCode != nil && *it.ExitCode != 0})
			case "file_change":
				for _, c := range it.Changes {
					a.emit("agent.tool", map[string]any{"tool": "edit", "args": map[string]any{"path": c.Path}})
				}
			case "mcp_tool_call":
				a.emit("agent.tool.done", map[string]any{"tool": it.Tool, "error": it.Status == "failed"})
			}
		case "turn.failed":
			failure = m.Error.Message
		case "error":
			failure = m.Message
		}
	}
	return reply, failure
}

// Steer stops the current run and resumes the session with your message.
func (a *codexAgent) Steer(text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd == nil || a.cmd.Process == nil {
		return errors.New("the Codex agent is between runs; your message goes with its next prompt")
	}
	a.steer = text
	return syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
}

func (a *codexAgent) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cmd != nil && a.cmd.Process != nil {
		syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
	}
	return nil
}
