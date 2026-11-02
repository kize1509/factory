package job

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factory/internal/handoff"
	"factory/internal/prompts"
	"factory/internal/runtime"
	"factory/internal/skills"
	"factory/internal/workspace"
)

// A ticket you give the factory gets solved. When the engine can't get any
// further on its own, the job doesn't end: it gets stuck. It writes down
// where it stopped, tells you, keeps its agents and worktree, and waits for
// your guidance. With it, the same agent carries on where it left off (or,
// after a restart, a new one with everything written down), as many times as
// it takes.

// Resume is what a stuck job wrote down about where it stopped. It lives in
// .factory/jobs/<job>/resume.json, next to the readable resume.md.
type Resume struct {
	Phase      string    `json:"phase"` // the state the job was in when it got stuck
	Error      string    `json:"error"`
	Ask        string    `json:"ask,omitempty"` // the agent, to you: what it needs to carry on
	Lead       string    `json:"lead"`          // the builder whose work goes forward
	Work       string    `json:"work"`          // that builder's worktree
	WorkBranch string    `json:"work_branch"`   // and its branch
	Base       string    `json:"base"`          // the commit the job started from
	Checks     []string  `json:"checks,omitempty"`
	Guidance   []string  `json:"guidance,omitempty"` // everything you said so far, oldest first
	Stuck      time.Time `json:"stuck"`
}

// stuck records where the job stopped and why, and tells you.
func (j *Job) stuck(phase string, err error, work *workspace.Worktree, ask string) {
	j.mu.Lock()
	r := Resume{Phase: phase, Error: err.Error(), Ask: ask, Lead: j.lead, Base: j.base, Checks: j.Checks, Guidance: j.guidance, Stuck: time.Now()}
	if work != nil {
		r.Work, r.WorkBranch = work.Path, work.Branch
	}
	j.Error, j.Phase, j.Ask = r.Error, phase, ask
	j.mu.Unlock()
	os.MkdirAll(j.Dir(), 0o755)
	os.WriteFile(filepath.Join(j.Dir(), "resume.json"), mustJSON(r), 0o644)
	brief := j.brief(&r, work)
	os.WriteFile(filepath.Join(j.Dir(), "resume.md"), []byte(brief), 0o644)
	j.set("stuck")
	j.emit(j.lead, "job.stuck", map[string]any{"error": r.Error, "phase": phase, "ask": ask, "context": filepath.Join(j.Dir(), "resume.md")})
}

// Brief is the written context of a stuck job: what you read before you
// guide it, and what a new agent reads to pick up the work.
func (j *Job) Brief() string {
	data, err := os.ReadFile(filepath.Join(j.Dir(), "resume.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

func (j *Job) loadResume() *Resume {
	var r Resume
	data, err := os.ReadFile(filepath.Join(j.Dir(), "resume.json"))
	if err != nil || json.Unmarshal(data, &r) != nil {
		// stuck before resume files existed (an old failed or interrupted job)
		return &Resume{Phase: j.State, Error: j.Error, Lead: builder}
	}
	return &r
}

// brief writes everything a builder needs to carry on: the task, where it
// stopped, the checks, the review, every handoff so far and the files
// changed.
func (j *Job) brief(r *Resume, work *workspace.Worktree) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Where %s stopped\n\nTicket %s: %s\n\n## The task\n\n%s\n\n", j.ID, j.ID, j.Title, strings.TrimSpace(j.Prompt))
	fmt.Fprintf(&b, "## Where it stopped\n\nStep: %s\n\nWhy: %s\n\n", phaseName(r.Phase), r.Error)
	if r.Ask != "" {
		fmt.Fprintf(&b, "The agent asked: %s\n\n", r.Ask)
	}
	if len(r.Checks) > 0 {
		fmt.Fprintf(&b, "## Checks\n\nThe engine checks the work with: %s\n\n", strings.Join(quoted(r.Checks), ", "))
	}
	j.mu.Lock()
	failure, v := j.lastFailure, j.verdict
	j.mu.Unlock()
	if failure != "" {
		if len(failure) > 6000 {
			failure = "…" + failure[len(failure)-6000:]
		}
		fmt.Fprintf(&b, "The last check run failed like this:\n\n%s\n\n", strings.TrimSpace(failure))
	}
	if v != nil && !v.Approved {
		fmt.Fprintf(&b, "## Review\n\nSocrates' blocking findings:\n- %s\n\n", strings.Join(v.Findings, "\n- "))
	}
	if notes := j.savedHandoffs(); len(notes) > 0 {
		b.WriteString("## Handoff notes so far\n\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "**%s** did: %s\n", n.agent, n.note.Did)
			for _, d := range n.note.Decisions {
				fmt.Fprintf(&b, "- decision: %s\n", d)
			}
			for _, f := range n.note.Findings {
				fmt.Fprintf(&b, "- finding: %s\n", f)
			}
			if n.note.Next != "" {
				fmt.Fprintf(&b, "- next: %s\n", n.note.Next)
			}
			b.WriteString("\n")
		}
	}
	if work != nil && r.Base != "" {
		if stat := work.Stat(r.Base); stat != "" {
			fmt.Fprintf(&b, "## Files changed so far (in %s)\n\n```\n%s\n```\n\n", work.Path, stat)
		} else {
			b.WriteString("## Files changed so far\n\nNone yet.\n\n")
		}
	}
	if len(r.Guidance) > 0 {
		b.WriteString("## Your guidance so far\n\n")
		for i, g := range r.Guidance {
			fmt.Fprintf(&b, "%d. %s\n", i+1, g)
		}
		b.WriteString("\n")
	}
	return b.String()
}

type savedNote struct {
	agent string
	note  handoff.Note
}

// savedHandoffs reads every handoff note the job wrote, from disk, so they
// survive a restart.
func (j *Job) savedHandoffs() []savedNote {
	files, _ := filepath.Glob(filepath.Join(j.Dir(), "handoff", "*.json"))
	sort.Strings(files)
	var out []savedNote
	for _, f := range files {
		var n handoff.Note
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &n) == nil {
			out = append(out, savedNote{strings.TrimSuffix(filepath.Base(f), ".json"), n})
		}
	}
	return out
}

func phaseName(p string) string {
	if s, ok := map[string]string{"starting": "starting", "planning": "planning", "symposium": "the Symposium", "grading": "grading the experiments",
		"building": "building", "checking": "the checks", "handoff": "the handoff", "reviewing": "review", "committing": "the commit", "shipping": "shipping"}[p]; ok {
		return s
	}
	if p == "" {
		return "unknown"
	}
	return p
}

// Restore brings back what a job stuck before a restart said: the step and
// the agent's request.
func (j *Job) Restore() {
	r := j.loadResume()
	j.mu.Lock()
	j.Phase, j.Ask = r.Phase, r.Ask
	j.mu.Unlock()
}

// Resumable reports whether you can guide this job on.
func (j *Job) Resumable() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.State == "stuck" || j.State == "failed" || j.State == "interrupted"
}

// Continue hands your guidance to a stuck job. A job still alive in this
// engine takes it at once; otherwise it reports true and the caller starts
// Run again, which picks the work up from what was written down.
func (j *Job) Continue(text string) (start bool, err error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return false, fmt.Errorf("write some guidance for the agent")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.State != "stuck" && j.State != "failed" && j.State != "interrupted" {
		return false, fmt.Errorf("%s is %s, not stuck", j.ID, j.State)
	}
	if j.alive {
		select {
		case j.guide <- text:
			return false, nil
		default:
			return false, fmt.Errorf("%s is already carrying on", j.ID)
		}
	}
	r := j.loadResume()
	r.Guidance = append(r.Guidance, text)
	j.resume, j.guidance, j.Phase, j.Ask = r, r.Guidance, "", ""
	return true, nil
}

// carryOn picks a stuck job up again: the same builder (or a new one with
// the written brief) gets your guidance, then the checks, handoff, review
// and commit follow as usual.
func (j *Job) carryOn(ctx context.Context, wt *workspace.Worktree, r *Resume) (*workspace.Worktree, runtime.Agent, bool, error) {
	if r.Base != "" && j.base == "" {
		j.base = r.Base
	}
	if len(j.Checks) == 0 && len(r.Checks) > 0 {
		j.Checks = r.Checks
	}
	work := wt
	if r.Work != "" && r.Work != wt.Path {
		if _, err := os.Stat(r.Work); err == nil { // the winning experiment's worktree
			work = &workspace.Worktree{Repo: j.Repo, Path: r.Work, Branch: r.WorkBranch}
		}
	}
	lead := r.Lead
	if lead == "" || (work == wt && lead != builder) {
		lead = builder // an experiment whose worktree is gone: the main builder takes over
	}
	guidance := r.Guidance[len(r.Guidance)-1]
	j.emit("", "job.resumed", map[string]any{"guidance": guidance, "phase": r.Phase, "lead": lead})
	if r.Phase == "committing" || r.Phase == "shipping" {
		return work, j.Agent(lead), true, nil // the work was done; only the commit or push is left
	}

	j.mu.Lock()
	j.lead = lead
	a := j.agents[lead]
	if a != nil && j.cwd[lead] != "" && j.cwd[lead] != work.Path {
		a = nil // that session works somewhere else
	}
	delete(j.handoffs, lead) // a fresh handoff for the work that follows
	j.cwd[lead] = work.Path
	j.mu.Unlock()

	j.set("building")
	var msg string
	if a != nil { // the same session: it remembers its work
		msg = fmt.Sprintf("The job got stuck at %s: %s\n\nThe person running the factory read where you stopped and says:\n\n%s\n\n"+
			"Carry on from where you left off, following that. When you're done, write your handoff again with the handoff tool.", phaseName(r.Phase), r.Error, guidance)
	} else {
		role := j.role("builder")
		var err error
		a, err = j.rtFor("builder").Start(ctx, runtime.AgentSpec{Job: j.ID, Agent: lead, Cwd: work.Path, Model: role.Model,
			System: prompts.For("builder"), Role: "builder", Skills: skills.Dirs(role.Skills)})
		if err != nil {
			return nil, nil, false, fmt.Errorf("start builder: %w", err)
		}
		j.mu.Lock()
		j.agents[lead] = a
		j.mu.Unlock()
		msg = j.brief(r, work) + fmt.Sprintf("## Carry on\n\nYou pick this job up where it stopped. The work so far is in your working folder (%s). "+
			"Read the files changed so far before you change anything.\n\nThe person running the factory says, most recently:\n\n%s\n\n"+
			"Follow that and finish the task. Scratch folder for logs and notes: %s\nWhen you're done, write your handoff with the handoff tool.",
			work.Path, guidance, filepath.Join(j.Dir(), "scratch"))
	}
	if _, err := j.ask(ctx, a, msg); err != nil {
		return work, a, false, fmt.Errorf("builder: %w", err)
	}
	return work, a, false, nil
}

// Name records a readable title for the job (and, the first time, for the
// project), given by the planner.
func (j *Job) Name(agent, title, project string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("give the job a title")
	}
	if r := []rune(title); len(r) > 60 {
		title = string(r[:59]) + "…"
	}
	project = strings.TrimSpace(project)
	j.mu.Lock()
	j.Title, j.named = title, true
	if j.Project == "" && project != "" {
		j.Project = project
	} else {
		project = j.Project
	}
	j.mu.Unlock()
	j.emit(agent, "job.named", map[string]any{"title": title, "project": project, "repo": j.Repo})
	return nil
}
