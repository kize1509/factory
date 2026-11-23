package triage

import (
	"fmt"
	"strings"
	"time"

	"factory/internal/config"
)

// Signals are what happened on the board since the last run.
type Signals struct {
	Changed  []string // tickets a person created or changed
	Answered []string // tickets where the owner answered a question
}

// Due says why an automatic run should start now, or "" when it shouldn't.
// last is the repo's latest run (manual or automatic), autoToday how many
// automatic runs started today, live how many jobs are going in the repo.
func Due(a config.TriageAuto, now time.Time, last *Run, autoToday, live int, sig Signals) string {
	a = a.Normalised()
	if !a.On {
		return ""
	}
	if last != nil && last.StateNow() == Running {
		return ""
	}
	if autoToday >= a.PerDay {
		return ""
	}
	if last == nil {
		return "the first run since it was turned on"
	}
	if end := last.EndedAt(); now.Sub(end) < time.Duration(a.Gap)*time.Minute {
		return ""
	}
	if a.OnAnswers && len(sig.Answered) > 0 {
		return "the owner answered on " + list(sig.Answered)
	}
	if a.OnBoard && len(sig.Changed) > 0 {
		return "someone changed " + list(sig.Changed)
	}
	if a.OnReady && a.MaxJobs > 0 && live == 0 && last.hitLimit() {
		return "the jobs it started are done and it left " + list(last.readyLeft()) + " ready"
	}
	return ""
}

func list(xs []string) string {
	if len(xs) > 4 {
		return strings.Join(xs[:4], ", ") + fmt.Sprintf(" and %d more", len(xs)-4)
	}
	return strings.Join(xs, ", ")
}

// EndedAt is when the run ended, or started while it hasn't.
func (r *Run) EndedAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Ended.IsZero() {
		return r.Ended
	}
	return r.Started
}

// hitLimit says whether the run started as many jobs as it could and still
// left tickets ready: a later run has work to start once those jobs end.
func (r *Run) hitLimit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	started := 0
	for _, j := range r.Jobs {
		if j.Job != "" {
			started++
		}
	}
	return r.MaxJobs > 0 && started >= r.MaxJobs && r.Report != nil && len(r.Report.Ready) > 0
}

func (r *Run) readyLeft() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Report == nil {
		return nil
	}
	return r.Report.Ready
}

// Pending lists the questions this run put on tickets, for the answer check.
func (r *Run) Pending() []Question {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Question
	for _, q := range r.Questions {
		if q.OnTicket {
			out = append(out, q)
		}
	}
	return out
}

// Since is when the run started: changes after it are new to the agent.
func (r *Run) Since() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Started
}
