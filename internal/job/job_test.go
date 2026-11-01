package job

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/runtime"
)

// fakeRT plays a builder: the first turn breaks the check and touches a
// test file, the fix turn makes the check pass, and it only writes its
// handoff after the engine reminds it.
type fakeRT struct{ job *Job }

func (f *fakeRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &fakeAgent{rt: f, cwd: s.Cwd}, nil
}

type fakeAgent struct {
	rt      *fakeRT
	cwd     string
	prompts []string
}

func (a *fakeAgent) Prompt(_ context.Context, text string) (string, error) {
	a.prompts = append(a.prompts, text)
	switch {
	case strings.HasPrefix(text, "Ticket"):
		os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("half done\n"), 0o644)
		os.WriteFile(filepath.Join(a.cwd, "app_test.go"), []byte("package x\n"), 0o644)
	case strings.HasPrefix(text, "The checks failed"):
		os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
	case strings.HasPrefix(text, "Your handoff is missing"):
		a.rt.job.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "finished app.txt", Next: "review", Files: []string{"app.txt"}})
	}
	return "ok", nil
}
func (a *fakeAgent) Steer(string) error { return nil }
func (a *fakeAgent) Stop() error        { return nil }

func TestPipelineHealsAndEnforcesHandoff(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	bus := events.NewBus(t.TempDir())
	rt := &fakeRT{}
	j := New(Spec{ID: "T1", Title: "make app", Repo: repo, Prompt: "write done into app.txt", Checks: []string{"grep -q '^done$' app.txt"}}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	j.Run(context.Background())

	if j.State != "done" || j.Commit == "" {
		t.Fatalf("state %s commit %q error %q", j.State, j.Commit, j.Error)
	}
	if _, err := os.Stat(filepath.Join(j.Worktree, "app_test.go")); !os.IsNotExist(err) {
		t.Fatal("the new test file should have been removed")
	}
	log, _ := bus.Replay(j.ID)
	seen := map[string]bool{}
	for _, e := range log {
		seen[e.Type] = true
	}
	for _, want := range []string{"workspace.blocked_write", "checks.failed", "checks.passed", "handoff.missing", "handoff.written", "job.committed", "job.done"} {
		if !seen[want] {
			t.Errorf("missing event %s", want)
		}
	}
}

// askRT writes a handoff with an open question on its first turn.
type askRT struct{ job *Job }

func (f *askRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &askAgent{rt: f, cwd: s.Cwd}, nil
}

type askAgent struct {
	rt  *askRT
	cwd string
}

func (a *askAgent) Prompt(_ context.Context, text string) (string, error) {
	os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
	a.rt.job.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "done", Next: "review",
		Files: []string{"app.txt"}, OpenQuestions: []string{"Should archived invoices be included?"}})
	return "ok", nil
}
func (a *askAgent) Steer(string) error { return nil }
func (a *askAgent) Stop() error        { return nil }

func TestOpenQuestionWaitsForAnswer(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	bus := events.NewBus(t.TempDir())
	rt := &askRT{}
	j := New(Spec{ID: "Q1", Title: "ask", Repo: repo, Prompt: "x"}, bus, rt)
	rt.job = j
	done := make(chan struct{})
	go func() { j.Run(context.Background()); close(done) }()
	for i := 0; i < 200 && !j.Waiting(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !j.Waiting() || j.Question == "" {
		t.Fatalf("job should wait on the question, state %s", j.State)
	}
	if !j.Answer("yes, include them") {
		t.Fatal("answer not delivered")
	}
	<-done
	if j.State != "done" || j.handoffs[builder].Findings[0] != "Answer from you: yes, include them" {
		t.Fatalf("state %s, findings %v", j.State, j.handoffs[builder].Findings)
	}
}

// reviewRT plays builder and reviewer: the reviewer grills the builder once,
// blocks round one, and approves round two.
type reviewRT struct {
	job    *Job
	rounds int
	asked  string
	answer string
}

func (f *reviewRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &reviewAgent{rt: f, role: s.Role, cwd: s.Cwd}, nil
}

type reviewAgent struct {
	rt   *reviewRT
	role string
	cwd  string
}

func (a *reviewAgent) Prompt(_ context.Context, text string) (string, error) {
	j := a.rt.job
	if a.role == "reviewer" {
		a.rt.rounds++
		ans, err := j.AskBuilder(reviewer, "Why is the value hardcoded?")
		if err != nil {
			return "", err
		}
		a.rt.answer = ans
		if a.rt.rounds == 1 {
			return "", j.SetVerdict(reviewer, Verdict{Findings: []string{"app.txt: value is hardcoded, read it from config"}})
		}
		return "", j.SetVerdict(reviewer, Verdict{Approved: true, Summary: "fine now"})
	}
	if strings.HasPrefix(text, "Socrates, the reviewer, asks") {
		a.rt.asked = text
		return "Because the ticket said so.", nil
	}
	os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
	j.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewer", Did: "done", Next: "review", Files: []string{"app.txt"}})
	return "ok", nil
}
func (a *reviewAgent) Steer(string) error { return nil }
func (a *reviewAgent) Stop() error        { return nil }

func TestReviewerGrillsBuilder(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	bus := events.NewBus(t.TempDir())
	rt := &reviewRT{}
	j := New(Spec{ID: "R1", Title: "review me", Repo: repo, Prompt: "x", Checks: []string{"test -f app.txt"}}, bus, rt)
	j.SetReviewer(rt)
	rt.job = j
	j.Run(context.Background())
	if j.State != "done" {
		t.Fatalf("state %s, error %s", j.State, j.Error)
	}
	if rt.rounds != 2 || !strings.Contains(rt.asked, "Why is the value hardcoded?") || rt.answer != "Because the ticket said so." {
		t.Fatalf("rounds %d asked %q answer %q", rt.rounds, rt.asked, rt.answer)
	}
	log, _ := bus.Replay(j.ID)
	seen := map[string]int{}
	for _, e := range log {
		seen[e.Type]++
	}
	if seen["review.question"] != 2 || seen["review.answer"] != 2 || seen["review.verdict"] != 2 || seen["checks.passed"] != 2 {
		t.Fatalf("events %v", seen)
	}
}
