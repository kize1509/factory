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
	Roles      map[string]RoleSpec `json:"roles,omitempty"` // runtime, model and skills per role
}

type Job struct {
	Spec
	State    string    `json:"state"` // starting, building, checking, handoff, committing, done, failed
	Started  time.Time `json:"started"`
	Branch   string    `json:"branch,omitempty"`
	Commit   string    `json:"commit,omitempty"`
	Error    string    `json:"error,omitempty"`
	Worktree string    `json:"worktree,omitempty"`

	bus      *events.Bus
	rt       runtime.Runtime
	mu       sync.Mutex
	agents   map[string]runtime.Agent
	handoffs map[string]*handoff.Note
	cancel   context.CancelFunc
	answers  chan string // your replies while the job waits on you
	reviewRT runtime.Runtime
	verdict  *Verdict
	asked    int // questions the reviewer asked this round
	ctx      context.Context
	Question string `json:"question,omitempty"`

	rts           map[string]runtime.Runtime // every runtime, to pick one per role
	lead          string                     // the builder whose work goes forward
	proposal      *Proposal
	symposiumDone bool
	inExperiment  map[string]bool
	labels        map[string]int // grading label -> experiment
	grades        map[string]int
	cwd           map[string]string // agent -> the worktree it works in
	base          string            // the commit the job started from
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
	return &Job{Spec: spec, State: "starting", Started: time.Now(), bus: bus, rt: rt,
		agents: map[string]runtime.Agent{}, handoffs: map[string]*handoff.Note{}, answers: make(chan string, 1),
		lead: builder, inExperiment: map[string]bool{}, labels: map[string]int{}, grades: map[string]int{}, cwd: map[string]string{}}
}

func (j *Job) Dir() string { return filepath.Join(j.Repo, ".factory", "jobs", j.ID) }

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

func (j *Job) Stop() {
	if j.cancel != nil {
		j.cancel()
	}
}

// Run takes the job from start to a commit, or to a failure.
func (j *Job) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	j.cancel = cancel
	j.mu.Lock()
	j.ctx = ctx
	j.mu.Unlock()
	defer cancel()
	j.emit("", "job.started", map[string]any{"title": j.Title, "issue": j.Issue, "repo": j.Repo, "checks": j.Checks, "model": j.Model, "runtime": j.Runtime})
	if err := j.run(ctx); err != nil {
		j.mu.Lock()
		j.Error = err.Error()
		j.mu.Unlock()
		j.set("failed")
		j.emit("", "job.failed", map[string]any{"error": err.Error()})
	}
	j.mu.Lock()
	for id, a := range j.agents {
		a.Stop()
		delete(j.agents, id)
	}
	j.mu.Unlock()
}

func (j *Job) run(ctx context.Context) error {
	wt, err := workspace.Create(j.Repo, j.ID)
	if err != nil {
		return err
	}
	j.Worktree, j.Branch = wt.Path, wt.Branch
	j.base, _ = wt.Head()
	j.emit("", "workspace.ready", map[string]any{"path": wt.Path, "branch": wt.Branch})
	scratch := filepath.Join(j.Dir(), "scratch")
	os.MkdirAll(scratch, 0o755)

	// the work that goes forward: the job's own worktree, or the winning
	// experiment's after a Symposium
	work, a := wt, runtime.Agent(nil)
	var plan *handoff.Note

	// complex work starts with a planner, who may open a Symposium
	if reason := j.complexReason(); reason != "" {
		j.emit("", "complexity.signal", map[string]any{"reason": reason})
		p, note, err := j.runPlanner(ctx, wt, reason)
		if err != nil {
			return err
		}
		plan = note
		if p != nil {
			win, err := j.symposium(ctx, wt, p, reason)
			if err != nil {
				return err
			}
			work, a = win.wt, win.agent
		}
	}

	if a == nil {
		j.set("building")
		a, err = j.rt.Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: builder, Cwd: wt.Path, Model: j.Model, System: prompts.For("builder"),
			Role: "builder", Skills: skills.Dirs(j.Skills)})
		if err != nil {
			return fmt.Errorf("start builder: %w", err)
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
				"Use the project's existing tests and tools; don't write new tests. The engine runs these commands after you finish and decides whether the work passes."
		default:
			task += "nothing (no checks were picked, so be extra careful)."
		}
		if plan != nil {
			task += "\n\nThe planner's handoff for you:\n" + string(mustJSON(plan))
		}
		if !j.symposiumDone {
			task += "\n\nIf the task turns out harder than it looks (several workable approaches and no clear best, or a vague ticket), call propose_experiments instead of guessing: the engine then tries the approaches side by side."
		}
		if _, err := j.ask(ctx, a, task); err != nil {
			return fmt.Errorf("builder: %w", err)
		}

		// the builder found the task harder than it looked: Symposium
		if p := j.takeProposal(); p != nil {
			a.Stop()
			win, err := j.symposium(ctx, wt, p, "the builder found several approaches")
			if err != nil {
				return err
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
					return perr
				}
				if p == nil {
					return fmt.Errorf("checks keep failing the same way and the planner saw no other approach")
				}
				a.Stop()
				win, serr := j.symposium(ctx, wt, p, "the same check failed the same way twice")
				if serr != nil {
					return serr
				}
				work, a = win.wt, win.agent
			} else if err != nil {
				return err
			}
		}
	}

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

	j.set("committing")
	if _, err := work.Commit(fmt.Sprintf("%s: %s", j.ID, j.Title)); err != nil && work == wt {
		return err
	}
	if work != wt { // the winning experiment's branch becomes the job branch
		if err := wt.FastForward(work.Branch); err != nil {
			return fmt.Errorf("merge the winning experiment: %w", err)
		}
		j.emit("", "symposium.merged", map[string]any{"from": work.Branch, "into": wt.Branch})
		a.Stop()
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
	j.set("done")
	j.emit("", "job.done", nil)
	return nil
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
			j.emit("oracle", "checks.passed", map[string]any{"loop": loop})
			break
		}
		j.emit("oracle", "checks.failed", map[string]any{"loop": loop})
		failure := checks.Failures(results)
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
				// open questions stop the job until you answer them (D38)
				q := strings.Join(n.OpenQuestions, " ")
				j.mu.Lock()
				j.Question = q
				j.mu.Unlock()
				j.emit(agent, "handoff.blocked", map[string]any{"open_questions": n.OpenQuestions})
				j.set("waiting")
				var answer string
				select {
				case answer = <-j.answers:
				case <-ctx.Done():
					return ctx.Err()
				}
				j.mu.Lock()
				j.Question = ""
				n.Findings = append(n.Findings, "Answer from you: "+answer)
				n.OpenQuestions = nil
				j.mu.Unlock()
				j.emit(agent, "handoff.answered", map[string]any{"answer": answer})
				j.set("handoff")
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

// protectTests undoes edits to test files unless the task is about tests.
// It reports whether an existing test file was changed (tampering); a new
// test file is only removed.
func (j *Job) protectTests(ctx context.Context, wt *workspace.Worktree, a runtime.Agent, agent string) bool {
	if j.TestsAllowed {
		return false
	}
	changed, _ := wt.Changed()
	var blocked []string
	for _, f := range changed {
		if isTestFile(f) {
			blocked = append(blocked, f)
		}
	}
	if len(blocked) == 0 {
		return false
	}
	existing := false
	for _, f := range blocked {
		if wt.Revert(f) == nil {
			existing = true
		} else {
			os.Remove(filepath.Join(wt.Path, f)) // a new test file has nothing to go back to
		}
	}
	j.emit(agent, "workspace.blocked_write", map[string]any{"files": blocked, "existing": existing})
	j.ask(ctx, a, "You added or changed test files, which is not allowed in this task. The engine undid it: "+strings.Join(blocked, ", ")+". Use the project's existing tests and fix the code instead.")
	return existing
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
func (j *Job) ask(ctx context.Context, a runtime.Agent, text string) (string, error) {
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
