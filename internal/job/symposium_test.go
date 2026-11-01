package job

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/runtime"
)

// symRT plays every role in a Symposium: the planner proposes two
// approaches; experiment 1 writes the right answer, experiment 2 a wrong one.
type symRT struct{ job *Job }

func (f *symRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &symAgent{rt: f, spec: s}, nil
}

type symAgent struct {
	rt   *symRT
	spec runtime.AgentSpec
}

func (a *symAgent) Prompt(_ context.Context, text string) (string, error) {
	j := a.rt.job
	switch {
	case a.spec.Agent == planner:
		return "", j.ProposeExperiments(planner, Proposal{Why: "two ways", Experiments: []Experiment{
			{Name: "write it directly", Hypothesis: "simplest works", Approach: "write done"},
			{Name: "write it backwards", Hypothesis: "maybe", Approach: "write enod"}}})
	case a.spec.Agent == grader:
		j.Grade(grader, "A", 5, "solves it")
		return "", nil
	case strings.Contains(text, "experiment 1 of a Symposium"):
		os.WriteFile(filepath.Join(a.spec.Cwd, "app.txt"), []byte("done\n"), 0o644)
	case strings.Contains(text, "experiment 2 of a Symposium"):
		os.WriteFile(filepath.Join(a.spec.Cwd, "app.txt"), []byte("enod\n"), 0o644)
	case strings.HasPrefix(text, "Your handoff is missing"):
		j.SaveHandoff(a.spec.Agent, &handoff.Note{From: a.spec.Agent, To: "reviewer", Did: "done", Next: "review", Files: []string{"app.txt"}})
	}
	return "ok", nil
}
func (a *symAgent) Steer(string) error { return nil }
func (a *symAgent) Stop() error        { return nil }

func TestSymposiumPicksTheWinner(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	bus := events.NewBus(t.TempDir())
	rt := &symRT{}
	j := New(Spec{ID: "S1", Title: "make app", Repo: repo, Prompt: "write done into app.txt",
		Notes: "This one is complex, use hypothesizing.", Checks: []string{"grep -q '^done$' app.txt"}}, bus, rt)
	j.SetReviewer(nil)
	rt.job = j
	j.Run(context.Background())
	if j.State != "done" {
		t.Fatalf("state %s, error %s", j.State, j.Error)
	}
	got, _ := os.ReadFile(filepath.Join(j.Worktree, "app.txt"))
	if string(got) != "done\n" {
		t.Fatalf("job branch has %q, want the winning experiment's work", got)
	}
	log, _ := bus.Replay(j.ID)
	seen := map[string]int{}
	var scored map[string]any
	for _, e := range log {
		seen[e.Type]++
		if e.Type == "symposium.scored" {
			scored = e.Data
		}
	}
	for _, want := range []string{"complexity.signal", "symposium.opened", "experiment.started", "experiment.finished", "symposium.scored", "symposium.winner", "symposium.merged", "job.committed"} {
		if seen[want] == 0 {
			t.Errorf("missing %s", want)
		}
	}
	if fmt.Sprint(scored["winner"]) != "1" {
		t.Fatalf("winner %v, want experiment 1", scored["winner"])
	}
	if _, err := os.Stat(filepath.Join(j.Dir(), "symposium", "exp-2-write-it-backwards", "diff.patch")); err != nil {
		t.Fatal("the losing experiment should be archived")
	}
}
