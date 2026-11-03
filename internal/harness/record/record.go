// Package record writes cassettes: one file per agent turn, taken at the
// runtime.Agent seam. A turn holds the prompt that went in, the reply that
// came out, the runtime events it emitted, the factory tool calls the agent
// made through the Job, and the change it left in the worktree. Because it
// only wraps the runtime.Runtime interface, it works for Pi, Claude, Codex
// and scripted agents alike.
package record

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"factory/internal/events"
	"factory/internal/job"
	"factory/internal/runtime"
)

// Call is one factory tool call an agent made during a turn.
type Call struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

// Turn is one Prompt call.
type Turn struct {
	Agent       string         `json:"agent"`
	N           int            `json:"n"` // 1-based, per agent, across restarts of that agent
	Prompt      string         `json:"prompt"`
	Reply       string         `json:"reply"`
	Error       string         `json:"error,omitempty"`
	StartEvents []events.Event `json:"start_events,omitempty"` // bus events during the Start that began the agent this turn is the first for
	Events      []events.Event `json:"events,omitempty"`       // everything on the bus during the turn
	Calls       []Call         `json:"calls,omitempty"`
	Cwd         string         `json:"cwd,omitempty"` // where the agent worked when recorded; evidence only
	TreeBefore  string         `json:"tree_before,omitempty"`
	TreeAfter   string         `json:"tree_after,omitempty"`
	Diff        string         `json:"diff,omitempty"` // git diff --binary TreeBefore TreeAfter
}

const repoToken = "{{repo}}"

// Root is the folder a cassette's paths are relative to: the project repo
// when cwd is a job worktree (<repo>/.factory/worktrees/<name>), else cwd.
func Root(cwd string) string {
	if i := strings.Index(cwd, "/.factory/worktrees/"); i > 0 {
		return cwd[:i]
	}
	return cwd
}

// Normalize swaps the machine-specific repo path in text for a token, so a
// cassette recorded in one temp folder replays in another.
func Normalize(text, cwd string) string {
	if r := Root(cwd); r != "" {
		return strings.ReplaceAll(text, r, repoToken)
	}
	return text
}

// Denormalize puts the current repo path back.
func Denormalize(text, cwd string) string {
	return strings.ReplaceAll(text, repoToken, Root(cwd))
}

// Dir is where an agent's turns live under a job's cassette root.
func Dir(root, agent string) string { return filepath.Join(root, "cassette", agent) }

// Load reads an agent's turns in order. A missing folder gives no turns.
func Load(root, agent string) ([]Turn, error) {
	files, _ := filepath.Glob(filepath.Join(Dir(root, agent), "*.json"))
	var turns []Turn
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var t Turn
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		turns = append(turns, t)
	}
	sort.Slice(turns, func(i, k int) bool { return turns[i].N < turns[k].N })
	return turns, nil
}

// IsRuntimeEvent says whether a bus event came from a runtime, not from the
// engine: replay puts these back on the bus, the engine makes the rest again.
func IsRuntimeEvent(e events.Event) bool {
	if e.Type == "agent.retry" {
		return false
	}
	for _, p := range []string{"agent.", "tool.", "message.", "pi.", "claude.", "codex."} {
		if strings.HasPrefix(e.Type, p) {
			return true
		}
	}
	return false
}

// Runtime wraps another runtime and records every turn of every agent it
// starts under Root/cassette/<agent>/<n>.json.
type Runtime struct {
	Inner runtime.Runtime
	Bus   *events.Bus
	Root  string // the job's folder, usually job.Dir()

	mu      sync.Mutex
	counts  map[string]int
	current map[string]*[]Call        // agent -> calls of the turn it is in
	cwd     map[string]string         // agent -> where it works
	started map[string][]events.Event // agent -> events its Start emitted, until its next turn is written
}

// New wraps inner. Call Attach once the job exists.
func New(inner runtime.Runtime, bus *events.Bus, root string) *Runtime {
	return &Runtime{Inner: inner, Bus: bus, Root: root, counts: map[string]int{}, current: map[string]*[]Call{}, started: map[string][]events.Event{}, cwd: map[string]string{}}
}

// Attach makes the job report its tool calls to the recorder.
func (r *Runtime) Attach(j *job.Job) { j.Tap = r.Tap }

// Tap records a tool call in the turn the agent is in.
func (r *Runtime) Tap(agent, tool string, args any) {
	b, _ := json.Marshal(args)
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.current[agent]; c != nil {
		*c = append(*c, Call{Tool: tool, Args: json.RawMessage(Normalize(string(b), r.cwd[agent]))})
	}
}

func (r *Runtime) busLen(job string) int {
	evs, _ := r.Bus.Replay(job)
	return len(evs)
}

func (r *Runtime) Start(ctx context.Context, spec runtime.AgentSpec) (runtime.Agent, error) {
	at := r.busLen(spec.Job)
	a, err := r.Inner.Start(ctx, spec)
	if err != nil {
		return nil, err
	}
	var startEvs []events.Event
	if evs, _ := r.Bus.Replay(spec.Job); at <= len(evs) {
		startEvs = evs[at:]
	}
	r.mu.Lock()
	r.cwd[spec.Agent] = spec.Cwd
	r.started[spec.Agent] = startEvs
	r.mu.Unlock()
	return &agent{r: r, in: a, spec: spec}, nil
}

type agent struct {
	r    *Runtime
	in   runtime.Agent
	spec runtime.AgentSpec
}

func (a *agent) Steer(text string) error { return a.in.Steer(text) }
func (a *agent) Stop() error             { return a.in.Stop() }

func (a *agent) Prompt(ctx context.Context, text string) (string, error) {
	r, name := a.r, a.spec.Agent
	before, _ := Snapshot(a.spec.Cwd)
	r.mu.Lock()
	startEvs := r.started[name]
	delete(r.started, name)
	calls := &[]Call{}
	prev := r.current[name]
	r.current[name] = calls
	r.counts[name]++
	n := r.counts[name]
	r.mu.Unlock()
	from := r.busLen(a.spec.Job)

	reply, err := a.in.Prompt(ctx, text)

	t := Turn{StartEvents: startEvs, Agent: name, N: n, Prompt: Normalize(text, a.spec.Cwd), Reply: Normalize(reply, a.spec.Cwd), Cwd: a.spec.Cwd, TreeBefore: before}
	if err != nil {
		t.Error = err.Error()
	}
	if evs, _ := r.Bus.Replay(a.spec.Job); from <= len(evs) {
		t.Events = evs[from:]
	}
	r.mu.Lock()
	r.current[name] = prev
	t.Calls = *calls
	r.mu.Unlock()
	if after, aerr := Snapshot(a.spec.Cwd); aerr == nil {
		t.TreeAfter = after
		if before != after {
			t.Diff, _ = TreeDiff(a.spec.Cwd, before, after)
		}
	}
	if werr := write(r.Root, t); werr != nil && err == nil {
		return reply, fmt.Errorf("record turn: %w", werr)
	}
	return reply, err
}

func write(root string, t Turn) error {
	dir := Dir(root, t.Agent)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(t, "", "  ")
	return os.WriteFile(filepath.Join(dir, strconv.Itoa(t.N)+".json"), b, 0o644)
}

func git(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Snapshot returns the tree hash of everything in dir (tracked and new, not
// ignored files), without touching the repo's own index.
func Snapshot(dir string) (string, error) {
	f, err := os.CreateTemp("", "factory-index-*")
	if err != nil {
		return "", err
	}
	f.Close()
	os.Remove(f.Name()) // git wants to create it
	defer os.Remove(f.Name())
	env := []string{"GIT_INDEX_FILE=" + f.Name()}
	if _, err := git(dir, env, "add", "-A"); err != nil {
		return "", err
	}
	out, err := git(dir, env, "write-tree")
	return strings.TrimSpace(out), err
}

// TreeDiff is the binary patch that turns tree a into tree b.
func TreeDiff(dir, a, b string) (string, error) {
	return git(dir, nil, "diff", "--binary", "--no-color", "--no-ext-diff", a, b)
}
