// Package patrol runs a pass: map the project's features, split feature ×
// technique cells among patrol agents, let each agent run one harness check
// and say what it makes of it, and record the result in the coverage ledger.
//
// The ledger is only an index. Every row points at a harness run folder
// (.factory/runs/<id>/ with result.json, logs and a replay_cmd), so evidence,
// replay and metrics are the harness's, and a failing Result is already the
// finding. Per D2 the engine starts agents and owns the budget and the kill
// switch; agents only call `factory test check` and record_check.
package patrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"factory/internal/harness"
	"factory/internal/store"
)

// Verdicts an agent may record.
const (
	OK      = "ok"
	Finding = "finding"
	Blocked = "blocked"
)

// Pass states.
const (
	Running = "running"
	Done    = "done"
	Stopped = "stopped" // the kill switch
	OverRun = "budget"  // a budget ran out
)

// Budget limits a pass. Zero means no limit for that field, except Agents,
// which defaults to 1.
type Budget struct {
	USD     float64       `json:"usd"`
	Time    time.Duration `json:"time"`
	Agents  int           `json:"agents"`
	Checks  int           `json:"checks"` // most checks in one pass
	Timeout time.Duration `json:"-"`      // per check command; default harness.CheckTimeout
	Poll    time.Duration `json:"-"`      // how often live agent spend is read; default 2s
}

// Feature is one entry of the feature map.
type Feature struct {
	ID    string `json:"id"`
	Paths string `json:"paths,omitempty"`
}

// Task is one cell of the pass handed to an agent.
type Task struct {
	Pass      string
	Agent     string
	Feature   Feature
	Technique string
	Repo      string
	Root      string
	Commit    string

	pass *Pass
}

// Check runs the harness check for this task. It is what `factory test
// check` does; an in-process agent calls it, a runtime agent calls the shell
// command. It refuses once the pass is stopped or out of budget.
func (t *Task) Check(ctx context.Context) (*harness.Result, error) {
	if err := t.pass.allow(ctx); err != nil {
		return nil, err
	}
	return harness.Check(ctx, t.Root, t.Repo, t.Feature.ID, t.Technique, harness.CheckOptions{Commit: t.Commit, Timeout: t.pass.Budget.Timeout})
}

// Record is record_check: the agent says which run it made and what it
// concludes. The verdict must fit the result (a failed run is not ok).
func (t *Task) Record(runID, verdict, note string) error {
	return t.pass.record(t, runID, verdict, note)
}

// Agent is one patrol agent. It returns the USD it spent. It must call
// Record before returning; a missing record becomes a blocked row.
type Agent interface {
	Patrol(ctx context.Context, t *Task) (cost float64, err error)
}

// Meter is an Agent that can say what a running task has cost so far, so the
// pass can stop at the USD budget while agents are still working.
type Meter interface {
	Spent(t *Task) float64
}

// AgentFunc adapts a function to Agent.
type AgentFunc func(ctx context.Context, t *Task) (float64, error)

func (f AgentFunc) Patrol(ctx context.Context, t *Task) (float64, error) { return f(ctx, t) }

// Summary is how a pass ended.
type Summary struct {
	State    string   `json:"state"`
	Features int      `json:"features"`
	Checks   int      `json:"checks"`
	OK       int      `json:"ok"`
	Findings int      `json:"findings"`
	Blocked  int      `json:"blocked"`
	WallMS   float64  `json:"wall_ms"`
	CostUSD  float64  `json:"cost_usd"`
	Runs     []string `json:"runs"`
	Next     []string `json:"next,omitempty"` // cells not reached: look at these next time
}

// Pass is one run of the swarm over a project.
type Pass struct {
	ID         string
	Project    string
	Repo       string // the project's git repo
	Root       string // where .factory/runs lives (usually Repo)
	Features   []Feature
	Techniques []string
	Budget     Budget
	Agent      Agent
	Store      *store.Store

	mu       sync.Mutex
	state    string
	spent    float64
	started  time.Time
	deadline time.Time
	cancel   context.CancelFunc
	recorded map[*Task]*pending
	active   map[string]*Task // agent name → its current task
	sum      Summary
}

type pending struct{ run, verdict, note string }

// ErrStopped and ErrBudget tell an agent why a check was refused.
var (
	ErrStopped = errors.New("pass stopped")
	ErrBudget  = errors.New("pass budget used up")
)

// State is the pass's current state.
func (p *Pass) State() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == "" {
		return Running
	}
	return p.state
}

// Stop is the kill switch: every running agent's context is cancelled.
func (p *Pass) Stop() {
	p.mu.Lock()
	if p.state == Running || p.state == "" {
		p.state = Stopped
	}
	c := p.cancel
	p.mu.Unlock()
	if c != nil {
		c()
	}
}

// allow is the check before each Check call.
func (p *Pass) allow(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return ErrStopped
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == Stopped {
		return ErrStopped
	}
	if p.Budget.USD > 0 && p.spent >= p.Budget.USD {
		p.over()
		return ErrBudget
	}
	if !p.deadline.IsZero() && !time.Now().Before(p.deadline) {
		p.over()
		return ErrBudget
	}
	return nil
}

// live is the USD of tasks still running, read from the agents' meter.
func (p *Pass) live() float64 {
	m, ok := p.Agent.(Meter)
	if !ok {
		return 0
	}
	p.mu.Lock()
	tasks := make([]*Task, 0, len(p.active))
	for _, t := range p.active {
		tasks = append(tasks, t)
	}
	p.mu.Unlock()
	var usd float64
	for _, t := range tasks {
		usd += m.Spent(t)
	}
	return usd
}

// watchSpend ends the pass as soon as finished plus running spend reaches the
// USD budget. It is what keeps a budget from being overshot by agents that
// are still mid-task.
func (p *Pass) watchSpend(ctx context.Context) {
	if p.Budget.USD <= 0 {
		return
	}
	every := p.Budget.Poll
	if every <= 0 {
		every = 2 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			// live first, then spent: a task finishing in between is then
			// counted twice rather than not at all
			l := p.live()
			p.mu.Lock()
			if p.spent+l >= p.Budget.USD {
				p.over()
			}
			p.mu.Unlock()
		}
	}
}

// over marks the budget as used up and cancels the rest. Caller holds mu.
func (p *Pass) over() {
	if p.state == Running {
		p.state = OverRun
	}
	if p.cancel != nil {
		p.cancel()
	}
}

// Map builds the feature map from the repo's code: see MapFeatures.
func (p *Pass) Map() []Feature {
	if len(p.Features) == 0 {
		p.Features = MapFeatures(p.Repo)
	}
	return p.Features
}

// Run does the whole pass and returns its summary. A stopped or over-budget
// pass still returns a summary of what it covered, with a nil error.
func (p *Pass) Run(ctx context.Context) (*Summary, error) {
	if p.Agent == nil || p.Store == nil {
		return nil, errors.New("pass needs an agent and a store")
	}
	repo, err := CanonicalRepo(p.Repo)
	if err != nil {
		return nil, err
	}
	if p.Root == "" || p.Root == p.Repo {
		p.Root = repo
	}
	p.Repo = repo
	if len(p.Techniques) == 0 {
		p.Techniques = harness.Techniques
	}
	workers := p.Budget.Agents
	if workers < 1 {
		workers = 1
	}
	commit, err := headCommit(p.Repo)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p.mu.Lock()
	p.state, p.cancel, p.started = Running, cancel, time.Now()
	p.recorded = map[*Task]*pending{}
	p.active = map[string]*Task{}
	if p.Budget.Time > 0 {
		p.deadline = p.started.Add(p.Budget.Time)
		var c2 context.CancelFunc
		ctx, c2 = context.WithDeadline(ctx, p.deadline)
		defer c2()
	}
	p.mu.Unlock()
	save := func(end bool) {
		p.mu.Lock()
		sum := p.sum
		sum.State = p.state
		p.mu.Unlock()
		b, _ := json.Marshal(sum)
		row := store.PassRow{ID: p.ID, Project: p.Project, State: sum.State, Summary: b, Started: p.started.Format(time.RFC3339)}
		if end {
			row.Ended = time.Now().Format(time.RFC3339)
		}
		p.Store.SavePass(row)
	}
	save(false)

	features := p.Map() // map
	p.sum.Features = len(features)
	cells := p.split(features) // split
	if p.Budget.Checks > 0 && len(cells) > p.Budget.Checks {
		cells = cells[:p.Budget.Checks]
	}

	go p.watchSpend(ctx)

	// patrol: agents take cells in order; the pass stops handing out work
	// once it is stopped or the budget is gone
	queue := make(chan int)
	var wg sync.WaitGroup
	var next sync.Mutex
	reached := make([]bool, len(cells))
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range queue {
				if p.allow(ctx) != nil {
					continue
				}
				next.Lock()
				reached[i] = true
				next.Unlock()
				t := &Task{Pass: p.ID, Agent: fmt.Sprintf("argus-%d", w+1), Feature: cells[i].f, Technique: cells[i].t,
					Repo: p.Repo, Root: p.Root, Commit: commit, pass: p}
				p.patrolOne(ctx, t)
			}
		}(w)
	}
feed:
	for i := range cells {
		select {
		case queue <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(queue)
	wg.Wait()

	p.mu.Lock()
	for i, c := range cells {
		if !reached[i] {
			p.sum.Next = append(p.sum.Next, c.f.ID+" × "+c.t)
		}
	}
	if p.state == Running {
		p.state = Done
	}
	p.sum.WallMS = float64(time.Since(p.started).Milliseconds())
	p.sum.CostUSD = p.spent
	sum := p.sum
	p.sum.State = p.state
	sum.State = p.state
	p.mu.Unlock()
	save(true)
	return &sum, nil
}

type cell struct {
	f Feature
	t string
}

// split orders feature × technique cells: never checked first, then least
// recently checked, then by name.
func (p *Pass) split(features []Feature) []cell {
	var cells []cell
	for _, f := range features {
		for _, t := range p.Techniques {
			cells = append(cells, cell{f, t})
		}
	}
	last, _ := p.Store.LastChecked(p.Repo)
	sort.SliceStable(cells, func(i, j int) bool {
		a, b := last[cells[i].f.ID+"\x00"+cells[i].t], last[cells[j].f.ID+"\x00"+cells[j].t]
		if a != b {
			return a < b // "" (never) sorts first; RFC3339 sorts by time
		}
		return false
	})
	return cells
}

// patrolOne runs one agent on one cell and then writes the ledger row. The
// row is written even when the agent failed or the pass was stopped.
func (p *Pass) patrolOne(ctx context.Context, t *Task) {
	p.mu.Lock()
	p.active[t.Agent] = t
	p.mu.Unlock()
	cost, err := p.Agent.Patrol(ctx, t)
	p.mu.Lock()
	delete(p.active, t.Agent)
	rec := p.recorded[t]
	delete(p.recorded, t)
	p.spent += cost
	p.mu.Unlock()
	if rec == nil {
		note := "agent recorded no check"
		if err != nil {
			note += ": " + err.Error()
		}
		p.mu.Lock()
		p.sum.Checks++
		p.sum.Blocked++
		p.mu.Unlock()
		p.Store.AddLedger(store.LedgerRow{Pass: p.ID, Repo: p.Repo, Feature: t.Feature.ID, Technique: t.Technique, Agent: t.Agent, Commit: t.Commit, Run: "", Verdict: Blocked, Note: note})
		return
	}
	res, err := harness.Results(p.Root, rec.run) // checked in record
	if err != nil {
		res = &harness.Result{}
	}
	run := store.Run{ID: rec.run, Repo: p.Repo, Setup: "patrol", Commit: t.Commit, Kind: res.Kind, Feature: t.Feature.ID, Technique: t.Technique,
		Pass: res.Pass, WallMS: res.Metrics["wall_ms"], CostUSD: res.Metrics["cost_usd"] + cost, Dir: harness.RunDir(p.Root, rec.run)}
	p.Store.AddRun(run)
	p.Store.AddLedger(store.LedgerRow{Pass: p.ID, Repo: p.Repo, Feature: t.Feature.ID, Technique: t.Technique, Agent: t.Agent, Commit: t.Commit, Run: rec.run, Verdict: rec.verdict, Note: rec.note})
	p.mu.Lock()
	p.sum.Checks++
	p.sum.Runs = append(p.sum.Runs, rec.run)
	switch rec.verdict {
	case OK:
		p.sum.OK++
	case Finding:
		p.sum.Findings++
	default:
		p.sum.Blocked++
	}
	p.mu.Unlock()
}

func (p *Pass) record(t *Task, runID, verdict, note string) error {
	switch verdict {
	case OK, Finding, Blocked:
	default:
		return fmt.Errorf("verdict must be ok, finding or blocked, not %q", verdict)
	}
	res, err := harness.Results(p.Root, runID)
	if err != nil {
		return fmt.Errorf("run %q: %w (run `factory test check` first and pass its run id)", runID, err)
	}
	if res.Feature != t.Feature.ID || res.Technique != t.Technique || (res.Commit != t.Commit) {
		return fmt.Errorf("run %s is for %s × %s at %s, but your task is %s × %s at %s", runID, res.Feature, res.Technique, res.Commit, t.Feature.ID, t.Technique, t.Commit)
	}
	if verdict == OK && !res.Pass {
		return fmt.Errorf("run %s failed (%s): that is a finding, not ok", runID, res.Failure.Step)
	}
	if verdict == Finding && res.Pass && strings.TrimSpace(note) == "" {
		return errors.New("a finding on a passing run needs a note saying what is wrong")
	}
	p.mu.Lock()
	p.recorded[t] = &pending{runID, verdict, note}
	p.mu.Unlock()
	return nil
}

// Ledger is what GET /api/passes/{id}/ledger returns.
func (p *Pass) Ledger() ([]store.LedgerRow, error) { return p.Store.Ledger(p.ID) }

// RecordFor is record_check as the engine's tool endpoint calls it: the
// agent is named by the URL, its task is the one it is working on.
func (p *Pass) RecordFor(agent, runID, verdict, note string) error {
	p.mu.Lock()
	t := p.active[agent]
	p.mu.Unlock()
	if t == nil {
		return fmt.Errorf("agent %s has no task in pass %s", agent, p.ID)
	}
	return t.Record(runID, verdict, note)
}
