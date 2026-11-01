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

// Claude runs each agent as a headless Claude Code process that stays up
// for the whole job: prompts and your messages go in as stream-json on
// stdin, and its events come out as stream-json on stdout. Model traffic is
// sent to the engine's gateway through --settings for this run only, so
// your own Claude Code config is never changed.
type Claude struct {
	Engine string // engine base URL
	Bus    *events.Bus
}

func (c *Claude) Start(ctx context.Context, s AgentSpec) (Agent, error) {
	_, model, _ := strings.Cut(s.Model, "/")
	if !strings.HasPrefix(s.Model, "anthropic/") {
		return nil, fmt.Errorf("Claude Code runs Claude models only, not %s", s.Model)
	}
	settings, _ := json.Marshal(map[string]any{"env": map[string]string{
		"ANTHROPIC_BASE_URL": fmt.Sprintf("%s/llm/%s/%s/claude-code/anthropic", c.Engine, s.Job, s.Agent),
	}})
	mcp, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"factory": map[string]any{"type": "http", "url": fmt.Sprintf("%s/mcp/%s/%s", c.Engine, s.Job, s.Agent)},
	}})
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "--model", model, "--settings", string(settings),
		"--mcp-config", string(mcp), "--strict-mcp-config"}
	if s.System != "" {
		args = append(args, "--append-system-prompt", s.System)
	}
	if len(s.Skills) > 0 { // only this role's skills, as a small plugin
		dir, err := skills.PluginDir(s.Role, s.Skills)
		if err != nil {
			return nil, fmt.Errorf("skills: %w", err)
		}
		args = append(args, "--plugin-dir", dir)
	}
	cmd := exec.Command("claude", args...)
	cmd.Dir = s.Cwd
	cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+fmt.Sprintf("%s/llm/%s/%s/claude-code/anthropic", c.Engine, s.Job, s.Agent))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}
	a := &claudeAgent{c: c, spec: s, cmd: cmd, in: in, results: make(chan claudeResult, 4)}
	go a.read(out)
	c.Bus.Emit(s.Job, s.Agent, "agent.started", map[string]any{"runtime": "claude-code", "model": s.Model, "pid": cmd.Process.Pid})
	return a, nil
}

type claudeResult struct {
	text string
	err  error
}

type claudeAgent struct {
	c       *Claude
	spec    AgentSpec
	cmd     *exec.Cmd
	in      io.WriteCloser
	mu      sync.Mutex
	results chan claudeResult
	tools   map[string]string // tool_use id -> tool name
	waiting bool              // a Prompt is waiting for its answer
}

func (a *claudeAgent) emit(typ string, data map[string]any) {
	a.c.Bus.Emit(a.spec.Job, a.spec.Agent, typ, data)
}

func (a *claudeAgent) send(text string) error {
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{
		"role": "user", "content": []map[string]any{{"type": "text", "text": text}}}})
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err := a.in.Write(append(line, '\n'))
	return err
}

// read turns Claude Code's stream into factory events. A "result" line ends
// one turn.
func (a *claudeAgent) read(out io.Reader) {
	a.tools = map[string]string{}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var m struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
			Message struct {
				Content []struct {
					Type     string          `json:"type"`
					Text     string          `json:"text"`
					Thinking string          `json:"thinking"`
					ID       string          `json:"id"`
					Name     string          `json:"name"`
					Input    json.RawMessage `json:"input"`
					ToolUse  string          `json:"tool_use_id"`
					IsError  bool            `json:"is_error"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case "assistant":
			for _, c := range m.Message.Content {
				switch c.Type {
				case "thinking":
					if strings.TrimSpace(c.Thinking) != "" {
						a.emit("agent.thinking", map[string]any{"text": c.Thinking})
					}
				case "text":
					if strings.TrimSpace(c.Text) != "" {
						a.emit("agent.text", map[string]any{"text": c.Text})
					}
				case "tool_use":
					name := toolName(c.Name)
					a.tools[c.ID] = name
					var args map[string]any
					json.Unmarshal(c.Input, &args)
					a.emit("agent.tool", map[string]any{"tool": name, "args": normalizeArgs(name, args)})
				}
			}
		case "user":
			for _, c := range m.Message.Content {
				if c.Type == "tool_result" {
					a.emit("agent.tool.done", map[string]any{"tool": a.tools[c.ToolUse], "error": c.IsError})
				}
			}
		case "result":
			// a message sent with Steer is usually folded into the running
			// turn, but it may start a turn of its own; only an answer
			// someone is waiting for is passed on
			var err error
			if m.IsError {
				err = errors.New(m.Result)
			}
			a.mu.Lock()
			if a.waiting {
				a.waiting = false
				a.results <- claudeResult{text: m.Result, err: err}
			}
			a.mu.Unlock()
		}
	}
	close(a.results)
}

// toolName maps Claude Code's tool names onto the factory's (bash, read,
// edit, write, grep, find) so the map treats every runtime the same way.
func toolName(n string) string {
	if strings.HasPrefix(n, "mcp__factory__") {
		return strings.TrimPrefix(n, "mcp__factory__")
	}
	switch n {
	case "Bash":
		return "bash"
	case "Read":
		return "read"
	case "Edit", "MultiEdit", "NotebookEdit":
		return "edit"
	case "Write":
		return "write"
	case "Grep":
		return "grep"
	case "Glob", "LS":
		return "find"
	}
	return strings.ToLower(n)
}

func normalizeArgs(tool string, args map[string]any) map[string]any {
	if p, ok := args["file_path"]; ok {
		args["path"] = p
	}
	return args
}

// Prompt sends a turn and waits for its answer.
func (a *claudeAgent) Prompt(ctx context.Context, text string) (string, error) {
	a.mu.Lock()
	a.waiting = true
	a.mu.Unlock()
	if err := a.send(text); err != nil {
		return "", err
	}
	select {
	case r, ok := <-a.results:
		if !ok {
			return "", errors.New("claude exited")
		}
		return r.text, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Steer sends your message while Claude Code works; it reads it before its
// next step.
func (a *claudeAgent) Steer(text string) error { return a.send(text) }

func (a *claudeAgent) Stop() error {
	a.in.Close()
	if a.cmd.Process != nil {
		syscall.Kill(-a.cmd.Process.Pid, syscall.SIGTERM)
	}
	return nil
}
