// Package triage runs the product agent (Pericles, role "product") over
// one repo's board. It reads the open tickets and the code behind them,
// fixes what is missing or wrong on the tickets, asks the owner when a
// ticket can't be worked on as written, and asks the engine to start jobs
// on the tickets that are ready.
//
// Per D2 the agent decides and the engine executes: tracker writes go
// through the operations service's guard (project, cap, control level,
// drafts), and the engine starts the jobs, within the run's limits.
package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"factory/internal/events"
	"factory/internal/ops"
	"factory/internal/prompts"
	"factory/internal/runtime"
)

// Pericles is the product agent's id on the map and in events.
const Pericles = "pericles"

// Run states.
const (
	Running = "running"
	Done    = "done"
	Stopped = "stopped"
	Failed  = "failed"
)

// LiveJob is a job the engine is running in the repo.
type LiveJob struct {
	ID    string `json:"id"`
	Issue string `json:"issue"`
	State string `json:"state"`
}

// Question is one question the product agent put to the ticket's owner.
type Question struct {
	Issue    string    `json:"issue"`
	Question string    `json:"question"`
	Posted   string    `json:"posted"` // what happened to the comment on the ticket
	At       time.Time `json:"at"`
	OnTicket bool      `json:"on_ticket"` // the comment went out
}

// Started is one job the product agent asked for.
type Started struct {
	Issue string `json:"issue"`
	Why   string `json:"why"`
	Job   string `json:"job,omitempty"` // empty when it was only recommended
	Note  string `json:"note,omitempty"`
}

// Report is what the product agent says when it ends the run.
type Report struct {
	Summary string   `json:"summary"`
	Ready   []string `json:"ready,omitempty"`   // tickets ready for a job that it didn't start
	Blocked []string `json:"blocked,omitempty"` // tickets waiting on the owner, with why
}

// Run is one triage run over a repo's board.
type Run struct {
	ID      string        `json:"id"`
	Repo    string        `json:"repo"`
	Project string        `json:"project"`
	Level   string        `json:"level"`
	MaxJobs int           `json:"max_jobs"` // 0: start_job only recommends
	Time    time.Duration `json:"-"`        // how long the run may take; default 30 minutes
	Focus   string        `json:"focus,omitempty"`
	Auto    bool          `json:"auto,omitempty"` // the engine started it on its own
	Why     string        `json:"why,omitempty"`  // what made it start

	RT    runtime.Runtime                           `json:"-"`
	Model string                                    `json:"-"`
	Ops   *ops.Service                              `json:"-"`
	Bus   *events.Bus                               `json:"-"`
	Dir   string                                    `json:"-"` // where run.json goes
	Live  func() []LiveJob                          `json:"-"` // the jobs running in the repo now
	Start func(issue, notes string) (string, error) `json:"-"` // starts a job on a ticket

	mu        sync.Mutex
	State     string     `json:"state"`
	Started   time.Time  `json:"started"`
	Ended     time.Time  `json:"ended,omitempty"`
	Error     string     `json:"error,omitempty"`
	Questions []Question `json:"questions"`
	Jobs      []Started  `json:"jobs"`
	Report    *Report    `json:"report,omitempty"`
	cancel    context.CancelFunc
}

// Scope is the id the run's agent and events use ("triage-<id>").
func (r *Run) Scope() string { return "triage-" + r.ID }

func (r *Run) emit(typ string, data map[string]any) {
	if r.Bus != nil {
		r.Bus.Emit(r.Scope(), Pericles, typ, data)
	}
}

// Snapshot is the run as JSON, safe to read while it runs.
func (r *Run) Snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, _ := json.MarshalIndent(r, "", "  ")
	return data
}

func (r *Run) save() {
	if r.Dir == "" {
		return
	}
	os.MkdirAll(r.Dir, 0o755)
	os.WriteFile(filepath.Join(r.Dir, "run.json"), r.Snapshot(), 0o644)
}

// StateNow is the run's state.
func (r *Run) StateNow() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.State
}

// Load reads a run back from its run.json. A run that was still going
// when the engine stopped is marked stopped.
func Load(dir string) (*Run, error) {
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	r.Dir = dir
	if r.State == Running {
		r.State, r.Error = Stopped, "the engine stopped while the run was going"
		if r.Ended.IsZero() {
			r.Ended = r.Started
		}
	}
	return &r, nil
}

// Stop ends the run and its agent.
func (r *Run) Stop() {
	r.mu.Lock()
	c := r.cancel
	if r.State == Running {
		r.State = Stopped
	}
	r.mu.Unlock()
	if c != nil {
		c()
	}
}

// Execute runs the product agent over the board until it calls
// finish_triage, the time runs out, or you stop it.
func (r *Run) Execute(parent context.Context) error {
	if r.Time <= 0 {
		r.Time = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, r.Time)
	defer cancel()
	r.mu.Lock()
	r.State, r.Started, r.cancel = Running, time.Now(), cancel
	if r.Questions == nil {
		r.Questions, r.Jobs = []Question{}, []Started{}
	}
	r.mu.Unlock()
	r.emit("triage.started", map[string]any{"repo": r.Repo, "project": r.Project, "level": r.Level, "max_jobs": r.MaxJobs, "auto": r.Auto, "why": r.Why})
	r.save()

	err := r.converse(ctx)

	r.mu.Lock()
	switch {
	case r.State == Stopped:
	case err != nil:
		r.State, r.Error = Failed, err.Error()
	default:
		r.State = Done
	}
	r.Ended = time.Now()
	state, report := r.State, r.Report
	r.mu.Unlock()
	r.save()
	data := map[string]any{"state": state}
	if report != nil {
		data["summary"] = report.Summary
	}
	if err != nil {
		data["error"] = err.Error()
	}
	r.emit("triage.ended", data)
	return err
}

func (r *Run) converse(ctx context.Context) error {
	a, err := r.RT.Start(ctx, runtime.AgentSpec{Job: r.Scope(), Agent: Pericles, Cwd: r.Repo, Model: r.Model,
		System: prompts.For("product"), Role: "product", Tools: []string{"read", "grep", "find", "ls"}})
	if err != nil {
		return fmt.Errorf("start the product agent: %w", err)
	}
	defer a.Stop()
	go func() { <-ctx.Done(); a.Stop() }()
	if _, err := a.Prompt(ctx, r.brief()); err != nil && !r.finished() {
		if ctx.Err() != nil {
			return fmt.Errorf("the run ran out of time or was stopped before the agent finished")
		}
		return err
	}
	for try := 0; try < 2 && !r.finished(); try++ {
		if ctx.Err() != nil {
			break
		}
		if _, err := a.Prompt(ctx, "You haven't ended the run. Carry on if there are tickets left, then call finish_triage with a summary, the ready tickets you didn't start and the ones waiting on the owner."); err != nil && !r.finished() {
			return err
		}
	}
	if !r.finished() {
		return fmt.Errorf("the product agent never called finish_triage")
	}
	return nil
}

func (r *Run) finished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Report != nil
}

// brief is the run's first message: the board, the limits and what is
// running now.
func (r *Run) brief() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Triage run %s for the repo %s. Its board is the YouTrack project %s.\n\n", r.ID, r.Repo, r.Project)
	fmt.Fprintf(&b, "Control level: %s. %s\n\n", r.Level, levelMeaning(r.Level))
	if r.MaxJobs > 0 {
		fmt.Fprintf(&b, "You may start up to %d jobs in this run with start_job.\n\n", r.MaxJobs)
	} else {
		b.WriteString("You may not start jobs in this run: start_job records your pick as a recommendation for the person running the factory.\n\n")
	}
	if live := r.live(); len(live) > 0 {
		b.WriteString("Jobs the factory is running in this repo now (don't start another on these tickets):\n")
		for _, j := range live {
			fmt.Fprintf(&b, "- %s on %s: %s\n", j.ID, orNone(j.Issue), j.State)
		}
		b.WriteString("\n")
	} else {
		b.WriteString("No jobs are running in this repo now.\n\n")
	}
	if r.Focus != "" {
		fmt.Fprintf(&b, "The person who started this run asks you to focus on:\n%s\n\n", r.Focus)
	}
	fmt.Fprintf(&b, "Start with tracker_search for the open tickets, for example \"#Unresolved sort by: updated desc\". Work through them the way your instructions say, then call finish_triage.")
	return b.String()
}

func levelMeaning(level string) string {
	switch level {
	case "observe":
		return "You can read the board but not change it. Say in finish_triage what you would change."
	case "propose":
		return "Every change you make goes to the person as a draft to approve. That is expected, not an error."
	case "file":
		return "You can create tickets and comment. Field and state changes go to the person as drafts."
	}
	return "You can create, comment, set fields and move any ticket in the project."
}

func orNone(s string) string {
	if s == "" {
		return "no ticket"
	}
	return s
}

func (r *Run) live() []LiveJob {
	if r.Live == nil {
		return nil
	}
	list := r.Live()
	sort.Slice(list, func(a, b int) bool { return list[a].ID < list[b].ID })
	return list
}

// ---- the product agent's own tools ----

// Tools are the tools only the product agent has. The tracker tools come
// from the operations service.
var Tools = map[string]bool{"ask_owner": true, "start_job": true, "finish_triage": true}

// Tool runs one of the product agent's tools.
func (r *Run) Tool(agent, tool string, args []byte) (string, error) {
	if agent != Pericles {
		return "", fmt.Errorf("only the product agent has the tool %q", tool)
	}
	switch tool {
	case "ask_owner":
		var b struct{ Issue, Question string }
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		return r.askOwner(strings.TrimSpace(b.Issue), strings.TrimSpace(b.Question))
	case "start_job":
		var b struct{ Issue, Why, Notes string }
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		return r.startJob(strings.ToUpper(strings.TrimSpace(b.Issue)), strings.TrimSpace(b.Why), strings.TrimSpace(b.Notes))
	case "finish_triage":
		var rep Report
		if err := json.Unmarshal(args, &rep); err != nil {
			return "", err
		}
		if strings.TrimSpace(rep.Summary) == "" {
			return "", fmt.Errorf("finish_triage needs a summary")
		}
		r.mu.Lock()
		r.Report = &rep
		r.mu.Unlock()
		r.save()
		r.emit("triage.report", map[string]any{"summary": rep.Summary, "ready": rep.Ready, "blocked": rep.Blocked})
		return "Run finished. End your turn.", nil
	}
	return "", fmt.Errorf("unknown tool %s", tool)
}

// askOwner posts a question on the ticket, where the owner answers it. The
// next triage run reads the answer in the ticket's comments.
func (r *Run) askOwner(issue, question string) (string, error) {
	if issue == "" || question == "" {
		return "", fmt.Errorf("ask_owner needs the ticket and the question")
	}
	posted, sent := "not posted", false
	reply := ""
	if r.Ops != nil {
		if sc := r.Ops.Scope(r.Scope()); sc != nil {
			msg, err := r.Ops.Write(sc, Pericles, ops.Action{Kind: "comment", Issue: issue, Text: "Question for the owner: " + question})
			if err != nil {
				posted, reply = "refused: "+err.Error(), "The comment didn't go out ("+err.Error()+"). The question is kept in the run's report for the person running the factory."
			} else {
				posted, sent, reply = msg, !strings.Contains(msg, "draft"), msg+". The owner answers on the ticket; a later run reads the answer."
			}
		}
	}
	if reply == "" {
		reply = "Recorded for the person running the factory."
	}
	r.mu.Lock()
	r.Questions = append(r.Questions, Question{Issue: issue, Question: question, Posted: posted, At: time.Now(), OnTicket: sent})
	r.mu.Unlock()
	r.save()
	r.emit("triage.question", map[string]any{"issue": issue, "question": question, "posted": posted})
	return reply, nil
}

// startJob asks the engine for a job on a ready ticket, within the run's
// limits.
func (r *Run) startJob(issue, why, notes string) (string, error) {
	if issue == "" {
		return "", fmt.Errorf("start_job needs the ticket")
	}
	if p, _, _ := strings.Cut(issue, "-"); !strings.EqualFold(p, r.Project) {
		return "", fmt.Errorf("%s is outside this board's project %s", issue, r.Project)
	}
	for _, j := range r.live() {
		if strings.EqualFold(j.Issue, issue) {
			return "", fmt.Errorf("job %s is already working on %s (%s)", j.ID, issue, j.State)
		}
	}
	r.mu.Lock()
	for _, s := range r.Jobs {
		if s.Issue == issue {
			r.mu.Unlock()
			return "", fmt.Errorf("you already picked %s in this run", issue)
		}
	}
	started := 0
	for _, s := range r.Jobs {
		if s.Job != "" {
			started++
		}
	}
	r.mu.Unlock()

	rec := Started{Issue: issue, Why: why, Note: notes}
	var reply string
	switch {
	case r.MaxJobs <= 0:
		reply = "Recorded as a recommendation: this run may not start jobs. The person running the factory sees it in the report."
	case started >= r.MaxJobs:
		return "", fmt.Errorf("the limit of %d jobs for this run is reached; list the other ready tickets in finish_triage", r.MaxJobs)
	case r.Start == nil:
		return "", fmt.Errorf("the engine can't start jobs from this run")
	default:
		id, err := r.Start(issue, notes)
		if err != nil {
			return "", fmt.Errorf("the engine couldn't start a job on %s: %w", issue, err)
		}
		rec.Job = id
		reply = fmt.Sprintf("Started job %s on %s. It moves the ticket on the board itself.", id, issue)
	}
	r.mu.Lock()
	r.Jobs = append(r.Jobs, rec)
	r.mu.Unlock()
	r.save()
	r.emit("triage.job", map[string]any{"issue": issue, "why": why, "job": rec.Job})
	return reply, nil
}
