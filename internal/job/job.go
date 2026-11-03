// Package job runs one ticket through the M0 pipeline: worktree, builder,
// checks and healing loop, enforced handoff, commit.
package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"factory/internal/checks"
	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/prompts"
	"factory/internal/runtime"
	"factory/internal/skills"
	"factory/internal/workspace"
)

type Spec struct {
	ID           string   `json:"id"`
	Issue        string   `json:"issue,omitempty"`      // tracker ticket, e.g. ABC-123
	Notes        string   `json:"notes,omitempty"`      // your extra notes on top of the ticket
	TestNotes    string   `json:"test_notes,omitempty"` // how to test, in your words; the agent turns it into commands
	Title        string   `json:"title"`
	Repo         string   `json:"repo"`
	Prompt       string   `json:"prompt"`
	Checks       []string `json:"checks"`
	Model        string   `json:"model"`
	Runtime      string   `json:"runtime"` // pi, claude-code or codex
	MaxLoops     int      `json:"max_loops"`
	TestsAllowed bool     `json:"tests_allowed"`
	Skills       []string `json:"skills,omitempty"` // the builder's skill names, from Admin → Skills
	// the reviewer who grills the builder once the checks pass (Admin → Roles, Skills)
	ReviewerRuntime string   `json:"reviewer_runtime,omitempty"`
	ReviewerModel   string   `json:"reviewer_model,omitempty"`
	ReviewerSkills  []string `json:"reviewer_skills,omitempty"`
	MaxReviews      int      `json:"max_reviews,omitempty"`
	// simple, medium or complex; complex work (or you saying so in the notes)
	// gets a planner and, when there are several approaches, a Symposium
	Complexity string              `json:"complexity,omitempty"`
	Roles      map[string]RoleSpec `json:"roles,omitempty"`   // runtime, model and skills per role
	Ship       string              `json:"ship,omitempty"`    // commit (the default), push or pr
	Project    string              `json:"project,omitempty"` // the project's readable name, given once by the planner
}

type Job struct {
	Spec
	State    string    `json:"state"`           // starting, planning, building, checking, handoff, reviewing, committing, shipping, done; stuck waits for your guidance
	Phase    string    `json:"phase,omitempty"` // the step a stuck job stopped at
	Started  time.Time `json:"started"`
	Branch   string    `json:"branch,omitempty"`
	Commit   string    `json:"commit,omitempty"`
	PR       string    `json:"pr,omitempty"` // the pull request's URL
	Error    string    `json:"error,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Island   int       `json:"island"` // the job's cell on the Aegean map, set once when it starts; -1 for none

	// Tap, when set, sees every agent tool call (handoff, set_checks, verdict,
	// name_job, ask_builder, propose_experiments, grade) as it arrives, before
	// the job checks it. The test harness records cassettes with it.
	Tap func(agent, tool string, args any) `json:"-"`

	bus       *events.Bus
	rt        runtime.Runtime
	mu        sync.Mutex
	agents    map[string]runtime.Agent
	handoffs  map[string]*handoff.Note
	cancel    context.CancelFunc
	cancelled bool        // you cancelled it: it ends for good
	answers   chan string // your replies while the job waits on you
	reviewRT  runtime.Runtime
	verdict   *Verdict
	asked     int // questions the reviewer asked this round
	ctx       context.Context
	Question  string          `json:"question,omitempty"`
	Ask       string          `json:"ask,omitempty"` // a stuck job's agent, in its own words: what it needs from you
	asks      map[string]*Ask // requests agents made with ask_human, by agent

	rts           map[string]runtime.Runtime // every runtime, to pick one per role
	lead          string                     // the builder whose work goes forward
	proposal      *Proposal
	symposiumDone bool
	inExperiment  map[string]bool
	labels        map[string]int // grading label -> experiment
	grades        map[string]int
	cwd           map[string]string // agent -> the worktree it works in
	base          string            // the commit the job started from

	alive       bool        // Run is going: a stuck job takes guidance at once
	guide       chan string // your guidance for a stuck job
	guidance    []string    // all of it so far
	resume      *Resume     // set when the next run carries a stuck job on
	lastFailure string      // the last failed check output
	named       bool        // the planner gave the job its title
}

const (
	builder  = "archimedes"
	reviewer = "socrates"
	maxAsks  = 4 // questions a reviewer may ask the builder per round
)

// Verdict is the reviewer's decision on one round of review.
type Verdict struct {
	Approved bool     `json:"approved"`
	Findings []string `json:"findings"`
	Summary  string   `json:"summary"`
}

// SetReviewer gives the job the runtime its reviewer runs on; without one
// there is no review step.
func (j *Job) SetReviewer(rt runtime.Runtime) { j.reviewRT = rt }

// SetRuntimes gives the job every runtime, so each role can run on the one
// Admin → Roles picked for it.
func (j *Job) SetRuntimes(rts map[string]runtime.Runtime) { j.rts = rts }

// role returns a role's runtime, model and skills, falling back to the
// builder's runtime and model.
func (j *Job) role(name string) RoleSpec {
	r := j.Roles[name]
	if name == "builder" {
		r = RoleSpec{Runtime: j.Runtime, Model: j.Model, Skills: j.Skills}
	}
	if name == "reviewer" && r.Model == "" && j.ReviewerModel != "" {
		r = RoleSpec{Runtime: j.ReviewerRuntime, Model: j.ReviewerModel, Skills: j.ReviewerSkills}
	}
	if r.Model == "" {
		r.Runtime, r.Model = j.Runtime, j.Model
	}
	return r
}

// rtFor finds the runtime a role runs on; nil means the role is skipped.
func (j *Job) rtFor(name string) runtime.Runtime {
	if j.rts != nil {
		if rt := j.rts[j.role(name).Runtime]; rt != nil {
			return rt
		}
	}
	if name == "reviewer" {
		return j.reviewRT
	}
	return j.rt
}

// AskBuilder passes a reviewer's question to the builder and returns its
// answer: the reviewer grills the builder on its decisions.
func (j *Job) AskBuilder(from, question string) (string, error) {
	j.tap(from, "ask_builder", map[string]any{"question": question})
	j.mu.Lock()
	b, ctx := j.agents[j.lead], j.ctx
	if from != reviewer {
		j.mu.Unlock()
		return "", fmt.Errorf("only the reviewer asks the builder questions")
	}
	if j.asked >= maxAsks {
		j.mu.Unlock()
		return "", fmt.Errorf("you have asked %d questions this round; decide with what you know and call verdict", maxAsks)
	}
	j.asked++
	j.mu.Unlock()
	if b == nil {
		return "", fmt.Errorf("the builder is not available")
	}
	j.emit(from, "review.question", map[string]any{"to": j.lead, "question": question})
	answer, err := j.ask(ctx, b, "Socrates, the reviewer, asks about your work:\n\n"+question+
		"\n\nAnswer him plainly and briefly, with file paths where it helps. Explain the reason behind your decision. Don't change any code now.")
	if err != nil {
		return "", err
	}
	j.emit(j.lead, "review.answer", map[string]any{"to": from, "answer": answer})
	return answer, nil
}

// SetVerdict records the reviewer's decision for this round.
func (j *Job) SetVerdict(from string, v Verdict) error {
	j.tap(from, "verdict", v)
	if from != reviewer {
		return fmt.Errorf("only the reviewer gives a verdict")
	}
	if !v.Approved && len(v.Findings) == 0 {
		return fmt.Errorf("a blocking verdict needs at least one finding")
	}
	j.mu.Lock()
	j.verdict = &v
	j.mu.Unlock()
	j.emit(from, "review.verdict", map[string]any{"approved": v.Approved, "findings": v.Findings, "summary": v.Summary})
	return nil
}

func New(spec Spec, bus *events.Bus, rt runtime.Runtime) *Job {
	if spec.MaxLoops == 0 {
		spec.MaxLoops = 4
	}
	if spec.Model == "" {
		spec.Model = "anthropic/claude-sonnet-5-5"
	}
	if spec.MaxReviews == 0 {
		spec.MaxReviews = 2
	}
	return &Job{Spec: spec, State: "starting", Island: -1, Started: time.Now(), bus: bus, rt: rt,
		agents: map[string]runtime.Agent{}, handoffs: map[string]*handoff.Note{}, answers: make(chan string, 1), guide: make(chan string, 1),
		lead: builder, inExperiment: map[string]bool{}, labels: map[string]int{}, grades: map[string]int{}, cwd: map[string]string{}}
}

// Shipped reports the job's state and what it left behind: its pull request,
// its commit and its branch.
func (j *Job) Shipped() (state, pr, commit, branch string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.State, j.PR, j.Commit, j.Branch
}

func (j *Job) Dir() string { return filepath.Join(j.Repo, ".factory", "jobs", j.ID) }

// tap reports a tool call to Tap, if one is set.
func (j *Job) tap(agent, tool string, args any) {
	if j.Tap != nil {
		j.Tap(agent, tool, args)
	}
}

func (j *Job) emit(agent, typ string, data map[string]any) { j.bus.Emit(j.ID, agent, typ, data) }

func (j *Job) set(state string) {
	j.mu.Lock()
	j.State = state
	j.mu.Unlock()
	j.emit("", "job.state", map[string]any{"state": state})
}

// Agent returns a running agent of this job, for steering and stopping.
func (j *Job) Agent(id string) runtime.Agent {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.agents[id]
}

// SaveHandoff stores a note written through the handoff tool and returns
// what is wrong with it, if anything.
func (j *Job) SaveHandoff(agent string, n *handoff.Note) []string {
	j.tap(agent, "handoff", n)
	j.mu.Lock()
	dir := j.cwd[agent]
	j.mu.Unlock()
	if dir == "" {
		dir = j.Worktree
	}
	problems := n.Problems(dir, j.Dir()) // paths are checked where this agent works
	if len(problems) > 0 {
		j.emit(agent, "handoff.rejected", map[string]any{"problems": problems})
		return problems
	}
	j.mu.Lock()
	j.handoffs[agent] = n
	j.mu.Unlock()
	path := filepath.Join(j.Dir(), "handoff", agent+".json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, mustJSON(n), 0o644)
	j.emit(agent, "handoff.written", map[string]any{"note": n})
	return nil
}

// SetChecks records the check commands an agent worked out from your test
// notes. The engine runs them; the agent doesn't decide when it passes.
func (j *Job) SetChecks(agent string, commands []string, why string) error {
	j.tap(agent, "set_checks", map[string]any{"commands": commands, "why": why})
	var clean []string
	for _, c := range commands {
		if c = strings.TrimSpace(c); c != "" {
			clean = append(clean, c)
		}
	}
	if len(clean) == 0 {
		return fmt.Errorf("give at least one command")
	}
	j.mu.Lock()
	j.Checks = clean
	j.mu.Unlock()
	j.emit(agent, "checks.planned", map[string]any{"commands": clean, "why": why})
	return nil
}

// Waiting reports whether the job is waiting for your answer.
func (j *Job) Waiting() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.State == "waiting"
}

// Answer delivers your reply to a job that is waiting on you.
func (j *Job) Answer(text string) bool {
	select {
	case j.answers <- text:
		return true
	default:
		return false
	}
}

// Cancel ends the job for good, in whatever state it is: its agents stop,
// a running check is killed, and it can't be guided on any more. The
// worktree and branch stay, so the work done so far isn't thrown away.
func (j *Job) Cancel() error {
	j.mu.Lock()
	if j.State == "done" || j.State == "cancelled" {
		defer j.mu.Unlock()
		return fmt.Errorf("%s is already %s", j.ID, j.State)
	}
	j.cancelled = true
	alive, cancel := j.alive, j.cancel
	j.mu.Unlock()
	if alive && cancel != nil {
		cancel() // Run ends and marks it cancelled
		return nil
	}
	j.markCancelled() // stuck from before a restart, failed or interrupted: no Run to end
	return nil
}

func (j *Job) markCancelled() {
	j.mu.Lock()
	j.Error, j.Phase = "", ""
	j.mu.Unlock()
	j.set("cancelled")
	j.emit("", "job.cancelled", map[string]any{"worktree": j.Worktree, "branch": j.Branch})
}

// Run takes the job to a commit. It never gives up on its own: when it
// can't get further it gets stuck, tells you, and waits with its agents for
// your guidance, then carries on. Only you cancelling it ends Run early.
func (j *Job) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	j.mu.Lock()
	j.cancel, j.ctx, j.alive, j.Error = cancel, ctx, true, ""
	first := j.resume == nil
	j.mu.Unlock()
	defer cancel()
	if first {
		j.emit("", "job.started", map[string]any{"title": j.Title, "issue": j.Issue, "repo": j.Repo, "project": j.Project, "checks": j.Checks, "model": j.Model, "runtime": j.Runtime})
	}
	for {
		work, err := j.run(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			break // cancelled
		}
		j.mu.Lock()
		phase := j.State
		j.mu.Unlock()
		j.stuck(phase, err, work, j.plea(ctx, err))
		select {
		case g := <-j.guide:
			r := j.loadResume()
			j.mu.Lock()
			j.guidance = append(j.guidance, g)
			r.Guidance = j.guidance
			j.resume, j.Error, j.Phase, j.Ask = r, "", "", ""
			j.mu.Unlock()
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	j.mu.Lock()
	j.alive = false
	cancelled := j.cancelled && j.State != "done"
	for id, a := range j.agents {
		a.Stop()
		delete(j.agents, id)
	}
	j.mu.Unlock()
	if cancelled {
		j.markCancelled()
	}
}

func (j *Job) takeResume() *Resume {
	j.mu.Lock()
	defer j.mu.Unlock()
	r := j.resume
	j.resume = nil
	return r
}

// run is one go at the job: from the start, or, for a stuck job you
// guided, from where it stopped. It returns the worktree the work is in.
func (j *Job) run(ctx context.Context) (*workspace.Worktree, error) {
	wt, err := workspace.Create(j.Repo, j.ID)
	if err != nil {
		return nil, err
	}
	j.Worktree, j.Branch = wt.Path, wt.Branch
	r := j.takeResume()
	if j.base == "" && r != nil {
		j.base = r.Base
	}
	if j.base == "" {
		j.base, _ = wt.Head()
	}
	j.emit("", "workspace.ready", map[string]any{"path": wt.Path, "branch": wt.Branch})
	scratch := filepath.Join(j.Dir(), "scratch")
	os.MkdirAll(scratch, 0o755)

	if r != nil { // carry a stuck job on with your guidance
		work, a, commitOnly, err := j.carryOn(ctx, wt, r)
		if err != nil {
			return orWT(work, wt), err
		}
		if commitOnly {
			return work, j.commit(wt, work, a)
		}
		if err := j.checkLoop(ctx, work, a, j.lead, false); err != nil {
			return work, err
		}
		return work, j.finish(ctx, wt, work, a)
	}

	// the work that goes forward: the job's own worktree, or the winning
	// experiment's after a Symposium
	work, a := wt, runtime.Agent(nil)
	var plan *handoff.Note

	// every job starts with the planner: it names the job, reads the code and
	// plans; complex work may get a Symposium
	reason := j.complexReason()
	if reason != "" {
		j.emit("", "complexity.signal", map[string]any{"reason": reason})
	}
	{
		p, note, err := j.runPlanner(ctx, wt, reason)
		if err != nil {
			return wt, err
		}
		plan = note
		if p != nil {
			win, err := j.symposium(ctx, wt, p, reason)
			if err != nil {
				return wt, err
			}
			work, a = win.wt, win.agent
		}
	}

	if a == nil {
		j.set("building")
		a, err = j.rt.Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: builder, Cwd: wt.Path, Model: j.Model, System: prompts.For("builder"),
			Role: "builder", Skills: skills.Dirs(j.Skills)})
		if err != nil {
			return wt, fmt.Errorf("start builder: %w", err)
		}
		j.mu.Lock()
		j.agents[builder] = a
		j.mu.Unlock()

		task := fmt.Sprintf("Ticket %s: %s\n\n%s\n\nScratch folder for logs, notes and evidence: %s\nPut those files there, never in the repo. Evidence paths in your handoff can point into it.\n\nThe engine will check your work with: %s",
			j.ID, j.Title, j.Prompt, scratch, strings.Join(quoted(j.Checks), ", "))
		switch {
		case len(j.Checks) > 0:
		case j.TestNotes != "":
			task = strings.TrimSuffix(task, "\n\nThe engine will check your work with: ")
			task += "\n\nHow to test this, from the person who started the job:\n" + j.TestNotes +
				"\n\nBefore you change any code, call set_checks with the exact shell commands that test this work, based on those notes. " +
				"Use the project's existing tests and tools, plus any tests this ticket asks you to write. The engine runs these commands after you finish and decides whether the work passes."
		default:
			task += "nothing (no checks were picked, so be extra careful)."
		}
		if j.TestsAllowed {
			task += "\n\nYou may change existing test files in this task, not only add new ones."
		}
		if plan != nil {
			task += "\n\nThe planner's handoff for you:\n" + string(mustJSON(plan))
		}
		if !j.symposiumDone {
			task += "\n\nIf the task turns out harder than it looks (several workable approaches and no clear best, or a vague ticket), call propose_experiments instead of guessing: the engine then tries the approaches side by side."
		}
		if _, err := j.ask(ctx, a, task); err != nil {
			return wt, fmt.Errorf("builder: %w", err)
		}

		// the builder found the task harder than it looked: Symposium
		if p := j.takeProposal(); p != nil {
			a.Stop()
			win, err := j.symposium(ctx, wt, p, "the builder found several approaches")
			if err != nil {
				return wt, err
			}
			work, a = win.wt, win.agent
		} else {
			if len(j.Checks) == 0 && j.TestNotes != "" { // asked for checks but got none: remind once
				j.emit(builder, "checks.missing", nil)
				j.ask(ctx, a, "You haven't called set_checks yet. Call it now with the shell commands that test this work, based on the test notes in your task.")
				if len(j.Checks) == 0 {
					j.emit("", "checks.none", map[string]any{"reason": "the agent did not name any check commands"})
				}
			}
			err := j.checkLoop(ctx, wt, a, builder, !j.symposiumDone)
			if err == errSameFailure { // the same failure twice: ask the planner for approaches
				p, _, perr := j.runPlanner(ctx, wt, "the same check failed the same way twice")
				if perr != nil {
					return wt, perr
				}
				if p == nil {
					j.set("checking") // stuck at the checks, not at the planning
					return wt, fmt.Errorf("checks keep failing the same way and the planner saw no other approach")
				}
				a.Stop()
				win, serr := j.symposium(ctx, wt, p, "the same check failed the same way twice")
				if serr != nil {
					return wt, serr
				}
				work, a = win.wt, win.agent
			} else if err != nil {
				return wt, err
			}
		}
	}

	return work, j.finish(ctx, wt, work, a)
}

func orWT(work, wt *workspace.Worktree) *workspace.Worktree {
	if work != nil {
		return work
	}
	return wt
}

// finish takes checked work through the handoff and review to the commit.
func (j *Job) finish(ctx context.Context, wt, work *workspace.Worktree, a runtime.Agent) error {
	lead := j.lead
	j.set("handoff")
	if err := j.requireHandoff(ctx, lead, a); err != nil {
		return err
	}

	// the reviewer grills the builder; blocking findings go back to the Workshop
	for round := 1; j.rtFor("reviewer") != nil && (j.rts != nil || j.reviewRT != nil); round++ {
		v, err := j.review(ctx, work, round)
		if err != nil {
			return err
		}
		if v.Approved {
			break
		}
		if round >= j.MaxReviews {
			return fmt.Errorf("review still blocking after %d rounds: %s", round, strings.Join(v.Findings, "; "))
		}
		j.set("building")
		if _, err := j.ask(ctx, a, "The reviewer found blocking problems. Fix them, then update your handoff with the handoff tool:\n- "+strings.Join(v.Findings, "\n- ")); err != nil {
			return fmt.Errorf("builder: %w", err)
		}
		if err := j.checkLoop(ctx, work, a, lead, false); err != nil {
			return err
		}
	}

	return j.commit(wt, work, a)
}

// commit makes the job's one commit, merges a winning experiment and ships.
func (j *Job) commit(wt, work *workspace.Worktree, a runtime.Agent) error {
	// only the engine commits: undo any commit an agent made on the job
	// branch and keep its changes
	if work == wt && j.base != "" {
		if head, _ := wt.Head(); head != j.base {
			if err := wt.Uncommit(j.base); err != nil {
				return fmt.Errorf("undo the agent's commits: %w", err)
			}
			j.emit("", "ship.uncommitted", map[string]any{"from": head, "to": j.base})
		}
	}
	j.set("committing")
	if _, err := work.Commit(fmt.Sprintf("%s: %s", j.ID, j.Title)); err != nil && work == wt {
		return err
	}
	if work != wt { // the winning experiment's branch becomes the job branch
		if err := wt.FastForward(work.Branch); err != nil {
			return fmt.Errorf("merge the winning experiment: %w", err)
		}
		j.emit("", "symposium.merged", map[string]any{"from": work.Branch, "into": wt.Branch})
		if a != nil {
			a.Stop()
		}
		work.Remove()
	}
	sha, err := wt.Head()
	if err != nil {
		return err
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	j.Commit = sha
	j.emit("", "job.committed", map[string]any{"sha": sha, "branch": wt.Branch})
	if err := j.ship(wt); err != nil {
		return err
	}
	j.set("done")
	j.emit("", "job.done", nil)
	return nil
}

// ship pushes the job branch and opens a PR, as the ship mode says.
func (j *Job) ship(wt *workspace.Worktree) error {
	mode := j.Ship
	if mode == "" || mode == "commit" {
		return nil
	}
	j.set("shipping")
	if err := wt.Push(); err != nil {
		return fmt.Errorf("push: %w", err)
	}
	j.emit("", "job.pushed", map[string]any{"branch": wt.Branch})
	if mode != "pr" {
		return nil
	}
	url, err := wt.OpenPR(fmt.Sprintf("%s: %s", j.ID, j.Title), j.prBody())
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.PR = url
	j.mu.Unlock()
	j.emit("", "job.pr_opened", map[string]any{"url": url, "branch": wt.Branch})
	return nil
}

// prBody is the pull request description, built from the builder's handoff
// and the reviewer's verdict.
func (j *Job) prBody() string {
	j.mu.Lock()
	note, v := j.handoffs[j.lead], j.verdict
	j.mu.Unlock()
	var b strings.Builder
	if j.Issue != "" {
		fmt.Fprintf(&b, "Ticket: %s\n\n", j.Issue)
	}
	if note != nil {
		fmt.Fprintf(&b, "## What changed\n\n%s\n", note.Did)
		for _, d := range note.Decisions {
			fmt.Fprintf(&b, "- %s\n", d)
		}
		b.WriteString("\n")
	}
	if len(j.Checks) > 0 {
		b.WriteString("## Checks\n\n")
		for _, c := range j.Checks {
			fmt.Fprintf(&b, "- `%s` passed\n", c)
		}
		b.WriteString("\n")
	}
	if v != nil && v.Summary != "" {
		fmt.Fprintf(&b, "## Review\n\n%s\n", v.Summary)
	}
	return strings.TrimSpace(b.String())
}

var errSameFailure = fmt.Errorf("the same check failed the same way twice")

// checkLoop runs the checks and sends failures back to the builder until
// they pass or the loops run out. With watch on, the same failure twice in
// a row stops it with errSameFailure, so a Symposium can take over.
func (j *Job) checkLoop(ctx context.Context, wt *workspace.Worktree, a runtime.Agent, agent string, watch bool) error {
	last := ""
	for loop := 1; ; loop++ {
		j.protectTests(ctx, wt, a, agent)
		if len(j.Checks) == 0 {
			break
		}
		j.set("checking")
		results := checks.Run(ctx, j.bus, j.ID, "oracle", wt.Path, j.Checks, 20*time.Minute)
		if checks.AllPassed(results) {
			j.mu.Lock()
			j.lastFailure = ""
			j.mu.Unlock()
			j.emit("oracle", "checks.passed", map[string]any{"loop": loop})
			break
		}
		j.emit("oracle", "checks.failed", map[string]any{"loop": loop})
		failure := checks.Failures(results)
		j.mu.Lock()
		j.lastFailure = failure
		j.mu.Unlock()
		if watch && failure == last {
			j.emit("oracle", "checks.same_failure", map[string]any{"loop": loop})
			return errSameFailure
		}
		last = failure
		if loop >= j.MaxLoops {
			return fmt.Errorf("checks still failing after %d loops", loop)
		}
		j.set("building")
		if _, err := j.ask(ctx, a, "The checks failed. Fix the cause, not the test.\n\n"+failure); err != nil {
			return fmt.Errorf("builder: %w", err)
		}
	}
	return nil
}

// review runs one round: the reviewer reads the work, grills the builder
// with ask_builder, and gives a verdict.
func (j *Job) review(ctx context.Context, wt *workspace.Worktree, round int) (*Verdict, error) {
	j.set("reviewing")
	j.mu.Lock()
	r := j.agents[reviewer]
	j.verdict, j.asked = nil, 0
	note := j.handoffs[j.lead]
	j.mu.Unlock()
	if r == nil {
		var err error
		role := j.role("reviewer")
		r, err = j.rtFor("reviewer").Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: reviewer, Cwd: wt.Path, Model: role.Model,
			System: prompts.For("reviewer"), Role: "reviewer", Skills: skills.Dirs(role.Skills)})
		if err != nil {
			return nil, fmt.Errorf("start reviewer: %w", err)
		}
		j.mu.Lock()
		j.agents[reviewer] = r
		j.mu.Unlock()
	}
	// From the start commit, so a Symposium winner (already committed) shows too.
	diff := wt.DiffFrom(j.base)
	if len(diff) > 60000 {
		diff = diff[:60000] + "\n… (diff cut at 60 000 characters; read the files for the rest)"
	}
	var task string
	if round == 1 {
		task = fmt.Sprintf("Review round %d for ticket %s: %s\n\nThe task:\n%s\n\nChecks that passed: %s\n\nThe builder's handoff note:\n%s\n\nThe diff:\n```diff\n%s\n```\n\n"+
			"Grill the builder on the decisions that matter with the ask_builder tool, one question at a time (at most %d), the way the grilling skill describes, then call verdict.",
			round, j.ID, j.Title, j.Prompt, strings.Join(j.Checks, ", "), mustJSON(note), diff, maxAsks)
	} else {
		task = fmt.Sprintf("Review round %d. The builder says it fixed your findings and the checks pass again. The diff now:\n```diff\n%s\n```\n\nAsk what you still need with ask_builder (at most %d), then call verdict.", round, diff, maxAsks)
	}
	if _, err := j.ask(ctx, r, task); err != nil {
		return nil, fmt.Errorf("reviewer: %w", err)
	}
	for try := 0; try < 2; try++ {
		j.mu.Lock()
		v := j.verdict
		j.mu.Unlock()
		if v != nil {
			return v, nil
		}
		j.emit(reviewer, "review.no_verdict", map[string]any{"try": try + 1})
		if _, err := j.ask(ctx, r, "You haven't given a verdict. Call the verdict tool now: approved, or blocking findings with file, line and fix."); err != nil {
			return nil, err
		}
	}
	j.mu.Lock()
	v := j.verdict
	j.mu.Unlock()
	if v == nil {
		return nil, fmt.Errorf("the reviewer did not give a verdict after 2 reminders")
	}
	return v, nil
}

// requireHandoff makes sure the agent left a valid note, sending it back to
// write one up to twice (PRD §7.6).
func (j *Job) requireHandoff(ctx context.Context, agent string, a runtime.Agent) error {
	for try := 0; ; try++ {
		j.mu.Lock()
		n := j.handoffs[agent]
		j.mu.Unlock()
		if n != nil {
			if len(n.OpenQuestions) > 0 {
				// real blockers are asked with ask_human while the agent works; what's
				// left at the end goes on to the reviewer instead of stopping the job
				j.mu.Lock()
				for _, q := range n.OpenQuestions {
					n.Findings = append(n.Findings, "Open question: "+q)
				}
				n.OpenQuestions = nil
				j.mu.Unlock()
			}
			return nil
		}
		if try == 2 {
			return fmt.Errorf("%s did not write a valid handoff after 2 reminders", agent)
		}
		j.emit(agent, "handoff.missing", map[string]any{"try": try + 1})
		if _, err := j.ask(ctx, a, "Your handoff is missing or was rejected. Write it now with the handoff tool. Fill every field and list only files that exist."); err != nil {
			return err
		}
	}
}

// protectTests puts back test files that exist at HEAD, unless the job
// allows test changes. New test files stay: adding tests is fine, rewriting
// the ones the checks rely on is not. It reports whether it undid anything.
func (j *Job) protectTests(ctx context.Context, wt *workspace.Worktree, a runtime.Agent, agent string) bool {
	if j.TestsAllowed {
		return false
	}
	changed, _ := wt.Changed()
	var blocked []string
	for _, f := range changed {
		if isTestFile(f) && wt.InHead(f) {
			blocked = append(blocked, f)
		}
	}
	if len(blocked) == 0 || wt.Revert(blocked...) != nil {
		return false
	}
	j.emit(agent, "workspace.blocked_write", map[string]any{"files": blocked, "existing": true})
	j.ask(ctx, a, "You changed existing test files, which this task doesn't allow. The engine put them back: "+strings.Join(blocked, ", ")+". New test files are fine. Fix the code, not the tests the checks rely on.")
	return true
}

func isTestFile(path string) bool {
	p := "/" + filepath.ToSlash(path)
	for _, s := range []string{"_test.go", ".test.", ".spec.", "/test/", "/tests/", "/__tests__/", "/e2e/", "/fixtures/", "/__snapshots__/"} {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

func quoted(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "`" + x + "`"
	}
	return out
}

// ask sends one turn to an agent and retries, twice at most, when the
// connection to the model breaks off: a passing glitch shouldn't end a job.
// ask gives an agent a turn. When the agent asked you for help during it,
// the job waits for your answer and hands it over as the next turn.
func (j *Job) ask(ctx context.Context, a runtime.Agent, text string) (string, error) {
	for {
		out, err := j.prompt(ctx, a, text)
		if err != nil {
			return out, err
		}
		agent, q := j.takeAsk(a)
		if q == nil {
			return out, nil
		}
		answer, err := j.waitForYou(ctx, agent, q)
		if err != nil {
			return "", err
		}
		text = "You asked the person running the factory: " + q.Need + "\n\nThey answer:\n\n" + answer + "\n\nCarry on with your task."
	}
}

// prompt runs one turn, retrying when the connection to the model breaks.
func (j *Job) prompt(ctx context.Context, a runtime.Agent, text string) (string, error) {
	for try := 0; ; try++ {
		out, err := a.Prompt(ctx, text)
		if err == nil || try >= 2 || ctx.Err() != nil || !transient(err) {
			return out, err
		}
		j.emit("", "agent.retry", map[string]any{"error": err.Error(), "try": try + 1})
		select {
		case <-time.After(time.Duration(try+1) * 3 * time.Second):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		text = "The connection to the model broke off in the middle of your answer. Carry on where you left off."
	}
}

func transient(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"sse", "unexpected end of json", "overloaded", "529", "502", "503", "504", "econnreset", "socket hang up", "connection reset", "timeout", "eof", "pi-host exited"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
