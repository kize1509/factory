package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"factory/internal/checks"
	"factory/internal/handoff"
	"factory/internal/prompts"
	"factory/internal/runtime"
	"factory/internal/skills"
	"factory/internal/workspace"
)

// The Symposium (PRD §7.3): for complex work, try several approaches at
// once on separate branches, grade them, and keep the best by a fixed
// formula.

const (
	planner = "aristotle"
	grader  = "zeno"
)

// builders for experiments, in the order hypotheses take them
var experimentBuilders = []string{"archimedes", "euclid", "hero", "pythagoras"}

// Experiment is one hypothesis to try.
type Experiment struct {
	Name          string `json:"name"`
	Hypothesis    string `json:"hypothesis"`
	Approach      string `json:"approach"`
	SuccessSignal string `json:"success_signal"`
}

// Proposal is a set of experiments an agent asked for, and why.
type Proposal struct {
	From        string       `json:"from"`
	Why         string       `json:"why"`
	Experiments []Experiment `json:"experiments"`
}

// RoleSpec is the runtime, model and skills one role plays with.
type RoleSpec struct {
	Runtime string   `json:"runtime"`
	Model   string   `json:"model"`
	Skills  []string `json:"skills,omitempty"`
}

// complexSignal finds you saying a task is complex or wants experiments.
var complexSignal = regexp.MustCompile(`(?i)\b(hypothes\w*|experiment\w*|several approaches|multiple approaches|explore (the )?options|try (a few|several|different) (ways|approaches|options)|this (one )?is (really |very )?complex|it'?s complex|not sure (which|how)|unclear how)\b`)

// complexReason says why this job counts as complex, or "" when it doesn't.
func (j *Job) complexReason() string {
	if j.Complexity == "complex" {
		return "you marked it complex"
	}
	if m := complexSignal.FindString(j.Notes); m != "" {
		return fmt.Sprintf("your notes say %q", m)
	}
	if m := complexSignal.FindString(j.Prompt); m != "" {
		return fmt.Sprintf("the ticket says %q", m)
	}
	return ""
}

// ProposeExperiments is how a planner (or a builder who finds the task is
// harder than it looked) asks for a Symposium.
func (j *Job) ProposeExperiments(from string, p Proposal) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.inExperiment[from] {
		return fmt.Errorf("you are already testing one hypothesis; build that approach only")
	}
	if j.symposiumDone {
		return fmt.Errorf("this job already had its Symposium; carry on with the winning approach")
	}
	if len(p.Experiments) < 2 || len(p.Experiments) > len(experimentBuilders) {
		return fmt.Errorf("propose between 2 and %d experiments", len(experimentBuilders))
	}
	for i, e := range p.Experiments {
		if strings.TrimSpace(e.Name) == "" || strings.TrimSpace(e.Hypothesis) == "" {
			return fmt.Errorf("experiment %d needs a name and a hypothesis", i+1)
		}
	}
	p.From = from
	j.proposal = &p
	return nil
}

// Grade records the grader's score for one experiment, known to it only by
// its label (A, B, C…), so it can't favour a builder.
func (j *Job) Grade(from, label string, score int, reason string) error {
	if from != grader {
		return fmt.Errorf("only the grader grades experiments")
	}
	if score < 1 || score > 5 {
		return fmt.Errorf("score from 1 to 5")
	}
	label = strings.ToUpper(strings.TrimSpace(label))
	j.mu.Lock()
	if _, ok := j.labels[label]; !ok {
		j.mu.Unlock()
		return fmt.Errorf("no experiment %s", label)
	}
	j.grades[label] = score
	j.mu.Unlock()
	j.emit(from, "experiment.graded", map[string]any{"label": label, "score": score, "reason": reason})
	return nil
}

func (j *Job) takeProposal() *Proposal {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := j.proposal
	j.proposal = nil
	return p
}

// runPlanner has the planner name the job, explore, and either propose
// experiments or leave a plan. Every job gets one; reason says why a job
// counts as complex, or is "" when it doesn't. It returns the proposal, if
// any, and the planner's note.
func (j *Job) runPlanner(ctx context.Context, wt *workspace.Worktree, reason string) (*Proposal, *handoff.Note, error) {
	j.set("planning")
	role := j.role("planner")
	a, err := j.rtFor("planner").Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: planner, Cwd: wt.Path, Model: role.Model,
		System: prompts.For("planner"), Role: "planner", Skills: skills.Dirs(role.Skills)})
	if err != nil {
		return nil, nil, fmt.Errorf("start planner: %w", err)
	}
	j.mu.Lock()
	j.agents[planner] = a
	j.mu.Unlock()
	task := fmt.Sprintf("Ticket %s: %s\n\n%s\n\n", j.ID, j.Title, j.Prompt)
	j.mu.Lock()
	named := j.named
	j.mu.Unlock()
	if named { // a second call, after the checks kept failing: the name stands
	} else if j.Project == "" {
		task += fmt.Sprintf("First call name_job with a short, readable title for this task (at most 60 characters, plain words, no file paths) "+
			"and a name for the project in %s: the name it gives itself (the README's title, else the module or package name), not the folder's name; e.g. \"Invoice Service\".\n\n", j.Repo)
	} else {
		task += fmt.Sprintf("First call name_job with a short, readable title for this task (at most 60 characters, plain words, no file paths). The project is %s.\n\n", j.Project)
	}
	if reason != "" {
		task += fmt.Sprintf("This job counts as complex because %s.\n\n", reason)
	}
	if j.TestNotes != "" && len(j.Checks) == 0 {
		task += "How to test it, from the person who started the job:\n" + j.TestNotes + "\nCall set_checks with the exact commands, so every approach is checked the same way.\n\n"
	}
	// you asking for it (marking the task complex, or saying so) means:
	// try several approaches, even when one looks fine
	asked := j.Complexity == "complex" || complexSignal.MatchString(j.Notes) || complexSignal.MatchString(j.Prompt)
	if asked {
		task += "Explore the code (don't change it). The person who started this job asked for several approaches to be tried, so call propose_experiments with 2 to 4 really different ones: " +
			"a short name, the hypothesis, the approach and the signal that would show it works. Then write your handoff."
	} else if reason != "" {
		task += "Explore the code (don't change it). If two or more approaches are workable and none is clearly better, call propose_experiments with 2 to 4 of them: " +
			"a short name, the hypothesis, the approach and the signal that would show it works. Otherwise write the plan in your handoff for the builder."
	} else {
		task += "Explore the code around the task (don't change it) and write a short plan in your handoff for the builder: the approach, the files, the order and the risks. " +
			"Keep it brief when the task is small. Only if the task turns out to have two or more workable approaches with no clear best, call propose_experiments instead."
	}
	if _, err := j.ask(ctx, a, task); err != nil {
		return nil, nil, fmt.Errorf("planner: %w", err)
	}
	p := j.takeProposal()
	if p == nil && asked {
		j.emit(planner, "experiments.missing", nil)
		j.ask(ctx, a, "You haven't proposed experiments. The person asked for several approaches to be tried: call propose_experiments now with 2 to 4 really different approaches.")
		p = j.takeProposal()
	}
	j.mu.Lock()
	note := j.handoffs[planner]
	j.mu.Unlock()
	return p, note, nil
}

type expResult struct {
	Label     string  `json:"label"`
	Index     int     `json:"index"`
	Name      string  `json:"name"`
	Agent     string  `json:"agent"`
	Branch    string  `json:"branch"`
	PassRate  float64 `json:"pass_rate"`
	Grade     int     `json:"grade"`
	Score     float64 `json:"score"`
	Lines     int     `json:"lines"`
	Files     int     `json:"files"`
	Out       bool    `json:"disqualified"`
	Why       string  `json:"why,omitempty"`
	Finished  time.Time
	wt        *workspace.Worktree
	agent     runtime.Agent
	diff      string
	checkText string
}

// symposium tries each experiment on its own branch and returns the winner.
func (j *Job) symposium(ctx context.Context, base *workspace.Worktree, p *Proposal, reason string) (*expResult, error) {
	j.mu.Lock()
	j.symposiumDone = true
	j.mu.Unlock()
	if err := base.Clean(); err != nil {
		return nil, err
	}
	baseSHA, err := base.Head()
	if err != nil {
		return nil, err
	}
	j.set("symposium")
	j.emit(p.From, "symposium.opened", map[string]any{"reason": reason, "why": p.Why, "experiments": p.Experiments})

	role := j.role("builder")
	results := make([]*expResult, len(p.Experiments))
	var wg sync.WaitGroup
	for i, e := range p.Experiments {
		r := &expResult{Index: i + 1, Label: string(rune('A' + i)), Name: e.Name, Agent: experimentBuilders[i],
			Branch: fmt.Sprintf("exp/%s/%d", j.ID, i+1)}
		results[i] = r
		wt, err := workspace.CreateFrom(j.Repo, fmt.Sprintf("%s-exp-%d", j.ID, i+1), r.Branch, baseSHA)
		if err != nil {
			return nil, fmt.Errorf("experiment %d: %w", i+1, err)
		}
		r.wt = wt
		a, err := j.rt.Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: r.Agent, Cwd: wt.Path, Model: role.Model,
			System: prompts.For("builder"), Role: "builder", Skills: skills.Dirs(role.Skills)})
		if err != nil {
			return nil, fmt.Errorf("start %s: %w", r.Agent, err)
		}
		r.agent = a
		j.mu.Lock()
		j.agents[r.Agent] = a
		j.inExperiment[r.Agent] = true
		j.cwd[r.Agent] = wt.Path
		j.mu.Unlock()
		j.emit(r.Agent, "experiment.started", map[string]any{"index": r.Index, "name": e.Name, "hypothesis": e.Hypothesis, "branch": r.Branch})
		wg.Add(1)
		go func(r *expResult, e Experiment) {
			defer wg.Done()
			j.runExperiment(ctx, r, e, baseSHA)
		}(r, e)
	}
	wg.Wait()

	j.gradeExperiments(ctx, base, results)
	winner := score(results)
	list := make([]map[string]any, len(results))
	for i, r := range results {
		list[i] = map[string]any{"label": r.Label, "index": r.Index, "name": r.Name, "agent": r.Agent, "score": r.Score, "grade": r.Grade,
			"pass_rate": r.PassRate, "lines": r.Lines, "files": r.Files, "disqualified": r.Out, "why": r.Why}
	}
	data := map[string]any{"results": list}
	if winner != nil {
		data["winner"] = winner.Index
	}
	j.emit("", "symposium.scored", data)
	j.archive(results, winner)

	for _, r := range results { // the losers go; their work is archived
		if r != winner {
			r.agent.Stop()
			r.wt.Remove()
			j.mu.Lock()
			delete(j.agents, r.Agent)
			j.mu.Unlock()
		}
	}
	if winner == nil {
		return nil, fmt.Errorf("no experiment passed: %s", whyAll(results))
	}
	j.mu.Lock()
	j.inExperiment[winner.Agent] = false
	j.lead = winner.Agent
	j.mu.Unlock()
	j.emit(winner.Agent, "symposium.winner", map[string]any{"index": winner.Index, "name": winner.Name, "score": winner.Score, "branch": winner.Branch})
	return winner, nil
}

func (j *Job) runExperiment(ctx context.Context, r *expResult, e Experiment, baseSHA string) {
	task := fmt.Sprintf("Ticket %s: %s\n\n%s\n\nYou are testing one hypothesis in experiment %d of a Symposium: %s.\nHypothesis: %s\nApproach: %s\nSignal that it works: %s\n\n"+
		"Build only this approach, as well as you can. Other builders try the other approaches on their own branches; the engine compares the results.\n"+
		"Scratch folder for logs: %s\nThe engine will check your work with: %s",
		j.ID, j.Title, j.Prompt, r.Index, e.Name, e.Hypothesis, e.Approach, e.SuccessSignal,
		filepath.Join(j.Dir(), "scratch"), strings.Join(quoted(j.Checks), ", "))
	if j.TestsAllowed {
		task += "\nYou may change existing test files in this task, not only add new ones."
	} else {
		task += "\nDon't change existing test files. You can add new ones. The engine puts changed test files back, and an experiment that changes existing tests is out."
	}
	if _, err := j.ask(ctx, r.agent, task); err != nil {
		r.Out, r.Why = true, "the builder failed: "+err.Error()
		return
	}
	blocked := false
	for loop := 1; ; loop++ {
		if j.protectTests(ctx, r.wt, r.agent, r.Agent) {
			blocked = true
		}
		if len(j.Checks) == 0 {
			r.PassRate = 1
			break
		}
		res := checks.Run(ctx, j.bus, j.ID, r.Agent, r.wt.Path, j.Checks, 20*time.Minute)
		passed := 0
		for _, c := range res {
			if c.Passed {
				passed++
			}
		}
		r.PassRate = float64(passed) / float64(len(res))
		r.checkText = fmt.Sprintf("%d of %d checks passed", passed, len(res))
		if checks.AllPassed(res) || loop >= 2 {
			break
		}
		j.ask(ctx, r.agent, "The checks failed. Fix the cause, not the test, still within your approach.\n\n"+checks.Failures(res))
	}
	r.Lines, r.Files = r.wt.DiffSize(baseSHA)
	r.diff = r.wt.DiffFrom(baseSHA)
	if _, err := r.wt.Commit(fmt.Sprintf("%s experiment %d: %s", j.ID, r.Index, r.Name)); err != nil && r.Lines == 0 {
		r.Out, r.Why = true, "no changes"
	}
	switch {
	case blocked:
		r.Out, r.Why = true, "changed the project's existing tests"
	case len(j.Checks) > 0 && r.PassRate < 1:
		r.Out, r.Why = true, r.checkText
	}
	r.Finished = time.Now()
	j.emit(r.Agent, "experiment.finished", map[string]any{"index": r.Index, "name": r.Name, "pass_rate": r.PassRate,
		"lines": r.Lines, "files": r.Files, "disqualified": r.Out, "why": r.Why})
}

// gradeExperiments has the grader score every remaining experiment, blind.
func (j *Job) gradeExperiments(ctx context.Context, base *workspace.Worktree, results []*expResult) {
	var in []*expResult
	for _, r := range results {
		if !r.Out {
			in = append(in, r)
		}
	}
	if len(in) < 2 { // nothing to compare
		return
	}
	role := j.role("reviewer")
	rt := j.rtFor("reviewer")
	if rt == nil {
		return
	}
	j.set("grading")
	a, err := rt.Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: grader, Cwd: base.Path, Model: role.Model,
		System: prompts.For("reviewer"), Role: "reviewer", Skills: skills.Dirs(role.Skills)})
	if err != nil {
		j.emit(grader, "grading.failed", map[string]any{"error": err.Error()})
		return
	}
	defer a.Stop()
	j.mu.Lock()
	j.labels, j.grades = map[string]int{}, map[string]int{}
	for _, r := range in {
		j.labels[r.Label] = r.Index
	}
	j.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "Grade %d experiments for ticket %s: %s\n\nThe task:\n%s\n\n", len(in), j.ID, j.Title, j.Prompt)
	for _, r := range in {
		d := r.diff
		if len(d) > 20000 {
			d = d[:20000] + "\n… (cut)"
		}
		fmt.Fprintf(&b, "Experiment %s (%s; %d lines in %d files):\n```diff\n%s\n```\n\n", r.Label, r.checkText, r.Lines, r.Files, d)
	}
	b.WriteString("Call the grade tool once for each experiment with a score from 1 to 5 and a short reason. Judge whether it really solves the task, how simple and clear it is, and the risk it adds. " +
		"You don't know who built which, on purpose. The engine combines your grades with the check results.")
	j.ask(ctx, a, b.String())
	j.mu.Lock()
	missing := len(j.grades) < len(in)
	j.mu.Unlock()
	if missing {
		j.ask(ctx, a, "Some experiments have no grade yet. Grade each of them with the grade tool.")
	}
	j.mu.Lock()
	for _, r := range in {
		r.Grade = j.grades[r.Label]
	}
	j.mu.Unlock()
}

// score applies the fixed formula (D17): out if a check failed, a test file
// was edited or nothing changed; else 0.6 × checks passed + 0.4 × grade;
// ties go to the smaller diff, then fewer files, then whoever finished first.
func score(results []*expResult) *expResult {
	var in []*expResult
	for _, r := range results {
		if r.Out {
			continue
		}
		g := r.Grade
		if g == 0 {
			g = 3 // no grade: neither helps nor hurts
		}
		r.Score = 0.6*r.PassRate + 0.4*float64(g-1)/4
		in = append(in, r)
	}
	if len(in) == 0 {
		return nil
	}
	sort.SliceStable(in, func(a, b int) bool {
		x, y := in[a], in[b]
		if x.Score != y.Score {
			return x.Score > y.Score
		}
		if x.Lines != y.Lines {
			return x.Lines < y.Lines
		}
		if x.Files != y.Files {
			return x.Files < y.Files
		}
		return x.Finished.Before(y.Finished)
	})
	return in[0]
}

func whyAll(results []*expResult) string {
	var parts []string
	for _, r := range results {
		parts = append(parts, fmt.Sprintf("%d %s: %s", r.Index, r.Name, r.Why))
	}
	return strings.Join(parts, "; ")
}

// archive keeps every experiment's diff and result in the job folder, so
// later runs can see what was tried.
func (j *Job) archive(results []*expResult, winner *expResult) {
	for _, r := range results {
		slug := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(r.Name), "-"), "-")
		dir := filepath.Join(j.Dir(), "symposium", fmt.Sprintf("exp-%d-%s", r.Index, slug))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "diff.patch"), []byte(r.diff), 0o644)
		summary := map[string]any{"result": r, "winner": r == winner}
		os.WriteFile(filepath.Join(dir, "summary.json"), mustJSON(summary), 0o644)
	}
}
