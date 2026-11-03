// Package replay is a runtime.Runtime that plays a cassette back (see
// package record). Each Prompt checks the prompt against the recording,
// applies the recorded change to the worktree with git apply, puts the
// runtime's events back on the bus and re-issues the agent's tool calls
// through the Job. No model, no network.
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/harness/record"
	"factory/internal/job"
	"factory/internal/runtime"
)

// Hooks is what replay calls to re-issue tool calls; *job.Job satisfies it.
type Hooks interface {
	SaveHandoff(agent string, n *handoff.Note) []string
	SetChecks(agent string, commands []string, why string) error
	SetVerdict(from string, v job.Verdict) error
	Name(agent, title, project string) error
	AskBuilder(from, question string) (string, error)
	ProposeExperiments(from string, p job.Proposal) error
	Grade(from, label string, score int, reason string) error
}

// Drift is the error a replay gives when the engine asks something other
// than what was recorded.
type Drift struct {
	Agent string
	N     int
	Diff  string
}

func (d *Drift) Error() string {
	return fmt.Sprintf("replay drift: %s turn %d: the prompt differs from the recording (- recorded, + now):\n%s", d.Agent, d.N, d.Diff)
}

// Runtime replays the cassette under Root (a job's folder).
type Runtime struct {
	Root  string
	Bus   *events.Bus // optional: where runtime events go back to
	Hooks Hooks       // set with Bind before the job runs

	mu   sync.Mutex
	next map[string]int // agent -> turns played
	all  map[string][]record.Turn
}

func New(root string, bus *events.Bus) *Runtime {
	return &Runtime{Root: root, Bus: bus, next: map[string]int{}, all: map[string][]record.Turn{}}
}

// Bind sets the job whose hooks the recorded tool calls go through.
func (r *Runtime) Bind(h Hooks) { r.Hooks = h }

func (r *Runtime) turns(agent string) ([]record.Turn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.all[agent]; ok {
		return t, nil
	}
	t, err := record.Load(r.Root, agent)
	if err != nil {
		return nil, err
	}
	r.all[agent] = t
	return t, nil
}

func (r *Runtime) Start(_ context.Context, spec runtime.AgentSpec) (runtime.Agent, error) {
	if _, err := r.turns(spec.Agent); err != nil {
		return nil, err
	}
	return &agent{r: r, spec: spec}, nil
}

// Unplayed lists recorded turns the replay never reached, "agent#n". An
// engine that does less than it did when recorded leaves some.
func (r *Runtime) Unplayed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for name, ts := range r.all {
		for _, t := range ts[min(r.next[name], len(ts)):] {
			out = append(out, fmt.Sprintf("%s#%d", name, t.N))
		}
	}
	return out
}

type agent struct {
	r    *Runtime
	spec runtime.AgentSpec
}

func (a *agent) Steer(string) error { return nil }
func (a *agent) Stop() error        { return nil }

func (a *agent) Prompt(ctx context.Context, text string) (string, error) {
	r, name := a.r, a.spec.Agent
	ts, _ := r.turns(name)
	r.mu.Lock()
	i := r.next[name]
	r.next[name]++
	r.mu.Unlock()
	if i >= len(ts) {
		return "", fmt.Errorf("replay: %s asked for turn %d but the cassette has %d", name, i+1, len(ts))
	}
	t := ts[i]
	if norm := record.Normalize(text, a.spec.Cwd); t.Prompt != norm {
		return "", &Drift{Agent: name, N: t.N, Diff: LineDiff(t.Prompt, norm)}
	}
	if r.Bus != nil {
		for _, e := range t.Events {
			if e.Agent == name && record.IsRuntimeEvent(e) {
				r.Bus.Emit(a.spec.Job, name, e.Type, e.Data)
			}
		}
	}
	if strings.TrimSpace(t.Diff) != "" {
		if err := apply(a.spec.Cwd, t.Diff); err != nil {
			return "", fmt.Errorf("replay: %s turn %d: %w", name, t.N, err)
		}
	}
	for _, c := range t.Calls {
		if err := r.call(name, a.spec.Cwd, c); err != nil {
			return "", fmt.Errorf("replay: %s turn %d: %w", name, t.N, err)
		}
	}
	if t.Error != "" {
		return record.Denormalize(t.Reply, a.spec.Cwd), errors.New(t.Error)
	}
	return record.Denormalize(t.Reply, a.spec.Cwd), nil
}

// call re-issues one recorded tool call. What the job answers (a rejected
// handoff, say) is the job's own business: it behaves as it did.
func (r *Runtime) call(agent, cwd string, c record.Call) error {
	c.Args = json.RawMessage(record.Denormalize(string(c.Args), cwd))
	h := r.Hooks
	if h == nil {
		return errors.New("no job bound: call Bind first")
	}
	switch c.Tool {
	case "handoff":
		var n handoff.Note
		if err := json.Unmarshal(c.Args, &n); err != nil {
			return err
		}
		h.SaveHandoff(agent, &n)
	case "set_checks":
		var a struct {
			Commands []string `json:"commands"`
			Why      string   `json:"why"`
		}
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return err
		}
		h.SetChecks(agent, a.Commands, a.Why)
	case "verdict":
		var v job.Verdict
		if err := json.Unmarshal(c.Args, &v); err != nil {
			return err
		}
		h.SetVerdict(agent, v)
	case "name_job":
		var a struct{ Title, Project string }
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return err
		}
		h.Name(agent, a.Title, a.Project)
	case "ask_builder":
		var a struct{ Question string }
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return err
		}
		h.AskBuilder(agent, a.Question)
	case "propose_experiments":
		var p job.Proposal
		if err := json.Unmarshal(c.Args, &p); err != nil {
			return err
		}
		h.ProposeExperiments(agent, p)
	case "grade":
		var a struct {
			Label  string
			Score  int
			Reason string
		}
		if err := json.Unmarshal(c.Args, &a); err != nil {
			return err
		}
		h.Grade(agent, a.Label, a.Score, a.Reason)
	default:
		return fmt.Errorf("unknown recorded tool %q", c.Tool)
	}
	return nil
}

func apply(dir, patch string) error {
	cmd := exec.Command("git", "apply", "--binary", "--whitespace=nowarn", "-")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("apply the recorded change in %s: %v: %s", dir, err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// LineDiff shows how b differs from a, line by line: "- " for lines only in
// a, "+ " for lines only in b, and two spaces for shared ones.
func LineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	// longest common subsequence table
	l := make([][]int, len(x)+1)
	for i := range l {
		l[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				l[i][j] = l[i+1][j+1] + 1
			} else {
				l[i][j] = max(l[i+1][j], l[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out = append(out, "  "+x[i])
			i++
			j++
		case j < len(y) && (i == len(x) || l[i][j+1] >= l[i+1][j]):
			out = append(out, "+ "+y[j])
			j++
		default:
			out = append(out, "- "+x[i])
			i++
		}
	}
	// keep it readable: shared lines far from a change collapse to "…"
	changed := make([]bool, len(out))
	for k, line := range out {
		if !strings.HasPrefix(line, "  ") {
			for d := -2; d <= 2; d++ {
				if k+d >= 0 && k+d < len(out) {
					changed[k+d] = true
				}
			}
		}
	}
	var res []string
	skipped := false
	for k, line := range out {
		if changed[k] {
			res = append(res, line)
			skipped = false
		} else if !skipped {
			res = append(res, "  …")
			skipped = true
		}
	}
	return strings.Join(res, "\n")
}
