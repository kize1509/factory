package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"factory/internal/prompts"
	"factory/internal/runtime"
	"factory/internal/skills"
	"factory/internal/workspace"
)

// A stuck job needs a way out that works, not only a box for guidance. Three
// things give it one:
//
//   - Actions: one-click fixes the engine carries out itself (try again, push
//     to a new branch, skip the PR, ...), offered for the step it stopped at.
//   - The repair turn: the commit and the push are the engine's own steps, so
//     guidance there goes to an agent with git in the job's worktree, and the
//     engine ships whatever it leaves.
//   - Repeats: a failure that comes back the same way after a resume is
//     counted, "try again" stops being offered, and the agent and you are told
//     the last attempt didn't help.

// Action is a one-click way out of a stuck job, offered next to your guidance.
type Action struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Confirm string `json:"confirm,omitempty"` // asked before it runs, for actions you can't take back
}

func shipPhase(p string) bool { return p == "committing" || p == "shipping" }

// actions lists what you can do for a job stuck at phase, besides guiding it.
func (j *Job) actions(phase string, repeats int) []Action {
	var out []Action
	retry := Action{ID: "retry", Label: "Try again", Hint: "Runs the step again as it is. Pushing fetches origin and rebases onto it first."}
	switch {
	case shipPhase(phase):
		if repeats == 0 {
			out = append(out, retry)
		}
		if phase == "shipping" {
			branch := j.Branch
			if j.pushTo != "" {
				branch = j.pushTo
			}
			if j.Ship == "pr" {
				out = append(out, Action{ID: "skip_pr", Label: "Push without a PR", Hint: "Pushes the branch and finishes the job. You open the PR yourself."})
			}
			out = append(out,
				Action{ID: "new_branch", Label: "Push to a new branch", Hint: fmt.Sprintf("Leaves origin's %s alone and ships to the next free name, like %s-2.", branch, j.Branch)},
				Action{ID: "force_push", Label: "Overwrite origin's branch", Hint: fmt.Sprintf("Force-pushes the job's commit over origin's %s.", branch),
					Confirm: fmt.Sprintf("This replaces origin's %s with the job's commit. Commits that are only on origin are lost. Go on?", branch)},
				Action{ID: "commit_only", Label: "Keep the commit, ship nothing", Hint: "Finishes the job with the local commit on " + j.Branch + ". You push it yourself."})
		}
	case phase == "building" || phase == "checking" || phase == "handoff" || phase == "reviewing":
		out = append(out, Action{ID: "commit_anyway", Label: "Commit it as it is", Hint: "Skips the checks and review that are left and commits the work as it stands, then ships it.",
			Confirm: "The checks or the review haven't passed. Commit the work as it is anyway?"})
	}
	return out
}

func (j *Job) hasAction(id string) bool {
	for _, a := range j.Actions {
		if a.ID == id {
			return true
		}
	}
	return false
}

var noise = regexp.MustCompile(`\b[0-9a-f]{7,40}\b|\d+`)

// sameFailure reports whether two errors say the same thing, apart from
// commit hashes, numbers and spacing.
func sameFailure(a, b string) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(noise.ReplaceAllString(s, "#")), " ") }
	return a != "" && norm(a) == norm(b)
}

// repeats counts how many resumes in a row ended in the failure err.
func (j *Job) repeats(phase string, err error) int {
	j.mu.Lock()
	prev := j.prev
	j.mu.Unlock()
	if prev != nil && prev.Phase == phase && sameFailure(prev.Error, err.Error()) {
		return prev.Repeats + 1
	}
	return 0
}

// workFor is the worktree a stuck job's work is in: the job's own, or the
// winning experiment's when that one is still there.
func (j *Job) workFor(wt *workspace.Worktree, r *Resume) *workspace.Worktree {
	if r.Work != "" && r.Work != wt.Path {
		if _, err := os.Stat(r.Work); err == nil {
			return &workspace.Worktree{Repo: j.Repo, Path: r.Work, Branch: r.WorkBranch}
		}
	}
	return wt
}

// act carries out the action you picked for a stuck job.
func (j *Job) act(ctx context.Context, wt *workspace.Worktree, r *Resume) (*workspace.Worktree, error) {
	work := j.workFor(wt, r)
	if r.Base != "" && j.base == "" {
		j.base = r.Base
	}
	j.emit("", "job.resumed", map[string]any{"action": r.Action, "phase": r.Phase, "lead": j.lead})
	j.set(r.Phase) // a failure from here is stuck at the same step again
	a := j.Agent(j.lead)
	switch r.Action {
	case "retry":
	case "skip_pr":
		j.Ship = "push"
	case "commit_only":
		j.Ship = "commit"
	case "new_branch":
		name, err := wt.FreeBranch()
		if err != nil {
			return wt, err
		}
		j.pushTo, wt.Remote = name, name
		j.emit("", "ship.new_branch", map[string]any{"branch": name})
	case "force_push":
		j.force = true
	case "commit_anyway":
		j.emit("", "ship.forced", map[string]any{"phase": r.Phase})
		if a != nil && j.cwd[j.lead] != work.Path {
			a = nil
		}
		return work, j.commit(wt, work, a)
	default:
		return wt, fmt.Errorf("unknown action %q", r.Action)
	}
	return work, j.shipFrom(wt, work, a)
}

// shipFrom finishes a job whose work is done: it ships the commit already
// on the branch, or makes it first.
func (j *Job) shipFrom(wt, work *workspace.Worktree, a runtime.Agent) error {
	if work == wt && wt.IsClean() {
		if head, _ := wt.Head(); head != "" && head != j.base {
			// committing again would give the commit a new SHA that clashes
			// with the one origin may already have
			return j.shipCommitted(wt, head)
		}
	}
	return j.commit(wt, work, a)
}

// repair hands your guidance for a stuck commit or push to an agent that
// can use git in the job's worktree, then ships what it leaves. The engine
// doesn't undo the agent's commits here: a rebase or merge it made is the
// fix.
func (j *Job) repair(ctx context.Context, wt *workspace.Worktree, r *Resume) (*workspace.Worktree, error) {
	if r.Base != "" && j.base == "" {
		j.base = r.Base
	}
	work := j.workFor(wt, r)
	guidance := r.Guidance[len(r.Guidance)-1]
	j.emit("", "job.resumed", map[string]any{"guidance": guidance, "phase": r.Phase, "lead": j.lead, "repair": true})
	j.set("repairing")

	lead := j.lead
	j.mu.Lock()
	a := j.agents[lead]
	if a != nil && j.cwd[lead] != wt.Path {
		a = nil // that session works somewhere else
	}
	j.mu.Unlock()
	if a == nil {
		role := j.role("builder")
		var err error
		a, err = j.rtFor("builder").Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: lead, Cwd: wt.Path, Model: role.Model,
			System: prompts.For("builder"), Role: "builder", Skills: skills.Dirs(role.Skills)})
		if err != nil {
			j.set(r.Phase)
			return wt, fmt.Errorf("start the repair agent: %w", err)
		}
		j.mu.Lock()
		j.agents[lead], j.cwd[lead] = a, wt.Path
		j.mu.Unlock()
	}
	if _, err := j.ask(ctx, a, j.repairBrief(wt, work, r, guidance)); err != nil {
		j.set(r.Phase)
		return wt, fmt.Errorf("repair agent: %w", err)
	}
	j.set(r.Phase)
	if work != wt { // the experiment's work still has to come over
		return work, j.commit(wt, work, a)
	}
	if !wt.IsClean() { // on top of whatever the agent did, so its history stays
		if _, err := wt.Commit(fmt.Sprintf("%s: %s", j.ID, j.Title)); err != nil {
			return wt, err
		}
	}
	head, err := wt.Head()
	if err != nil {
		return wt, err
	}
	if head == j.base {
		return wt, fmt.Errorf("nothing to ship: %s has no commit past the job's base", wt.Branch)
	}
	return wt, j.shipCommitted(wt, head)
}

func (j *Job) repairBrief(wt, work *workspace.Worktree, r *Resume, guidance string) string {
	mode := map[string]string{"": "nothing more, the commit is all", "commit": "nothing more, the commit is all", "push": "a push to origin", "pr": "a push to origin and a pull request"}[j.Ship]
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket %s: %s\n\nThe work is done and checked. What failed is the engine's own step, %s:\n\n%s\n\n", j.ID, j.Title, phaseName(r.Phase), r.Error)
	fmt.Fprintf(&b, "The person running the factory says:\n\n%s\n\n", guidance)
	if r.Repeats > 0 {
		fmt.Fprintf(&b, "This failure has come back %d times in a row, so what was tried before didn't help. Do something different.\n\n", r.Repeats+1)
	}
	if len(r.Guidance) > 1 {
		b.WriteString("What they said before, oldest first:\n")
		for _, g := range r.Guidance[:len(r.Guidance)-1] {
			fmt.Fprintf(&b, "- %s\n", g)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Do what they say, in %s, with git and gh from the shell. The job branch is %s; it ships to origin's %s.", wt.Path, wt.Branch, wt.RemoteBranch())
	if work != wt {
		fmt.Fprintf(&b, " The finished work is on %s in %s; the engine merges it into the job branch.", work.Branch, work.Path)
	}
	b.WriteString(`

Rules:
- Stay on the job branch. Don't switch to another branch or delete one.
- Don't force-push, delete a branch on origin or rewrite commits already on origin, unless the person's words above plainly tell you to.
- Leave the job's code as it is, apart from what resolving a conflict needs. Don't commit new changes of your own; the engine commits whatever you leave uncommitted.
- If you start a rebase or merge, finish it so the branch is clean, or abort it.
- If the person's words need something you can't do yourself (a login, access, a decision), call ask_human.

`)
	fmt.Fprintf(&b, "When you're done, say in one or two sentences what you did. The engine then ships the branch: %s. Scratch folder for notes: %s", mode, filepath.Join(j.Dir(), "scratch"))
	return b.String()
}

// optionsLine names the actions for the plea and the brief.
func optionsLine(acts []Action) string {
	var labels []string
	for _, a := range acts {
		labels = append(labels, a.Label)
	}
	return strings.Join(labels, ", ")
}
