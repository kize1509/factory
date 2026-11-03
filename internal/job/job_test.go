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
	"factory/internal/workspace"
)

// fakeRT plays a builder: the first turn breaks the check, adds a test
// file and edits an existing one, the fix turn makes the check pass, and it only writes its
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
		os.WriteFile(filepath.Join(a.cwd, "old_test.go"), []byte("package x // weakened\n"), 0o644)
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
	os.WriteFile(filepath.Join(repo, "old_test.go"), []byte("package x\n"), 0o644)
	for _, args := range [][]string{{"add", "old_test.go"}, {"commit", "-q", "-m", "old test"}} {
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
	if _, err := os.Stat(filepath.Join(j.Worktree, "app_test.go")); err != nil {
		t.Fatal("the new test file should have stayed")
	}
	if b, _ := os.ReadFile(filepath.Join(j.Worktree, "old_test.go")); string(b) != "package x\n" {
		t.Fatalf("the existing test file should have been put back, got %q", b)
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

// Open questions left in a handoff don't stop the job: real blockers are
// asked with ask_human during the work, and the rest goes on to the reviewer.
func TestOpenQuestionsGoOnAsFindings(t *testing.T) {
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
	j.Run(context.Background())
	n := j.handoffs[builder]
	if j.State != "done" || len(n.OpenQuestions) != 0 || len(n.Findings) == 0 || !strings.HasPrefix(n.Findings[len(n.Findings)-1], "Open question: ") {
		t.Fatalf("state %s, findings %v, open questions %v", j.State, n.Findings, n.OpenQuestions)
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

// committingRT plays a builder that commits its own work, which a ticket
// without ship permission must not keep.
type committingRT struct{ job *Job }

func (f *committingRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &committingAgent{rt: f, cwd: s.Cwd}, nil
}

type committingAgent struct {
	rt  *committingRT
	cwd string
}

func (a *committingAgent) Prompt(_ context.Context, text string) (string, error) {
	os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "agent commit"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.cwd
		cmd.Run()
	}
	a.rt.job.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "finished app.txt", Next: "review", Files: []string{"app.txt"}})
	return "ok", nil
}
func (a *committingAgent) Steer(string) error { return nil }
func (a *committingAgent) Stop() error        { return nil }

func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	return repo
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDefaultShipCommitsOnceAfterAgentCommit(t *testing.T) {
	repo := gitRepo(t)
	base := gitOut(t, repo, "rev-parse", "HEAD")
	bus := events.NewBus(t.TempDir())
	rt := &committingRT{}
	j := New(Spec{ID: "N1", Title: "make app", Repo: repo, Prompt: "write done into app.txt"}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	j.Run(context.Background())

	if j.State != "done" || j.Commit == "" || j.PR != "" {
		t.Fatalf("state %s commit %q pr %q error %q", j.State, j.Commit, j.PR, j.Error)
	}
	if parent := gitOut(t, j.Worktree, "rev-parse", "HEAD~1"); parent != base {
		t.Fatalf("want one engine commit on top of %s, parent is %s", base, parent)
	}
	if msg := gitOut(t, j.Worktree, "log", "-1", "--format=%s"); msg != "N1: make app" {
		t.Fatalf("commit message %q", msg)
	}
	log, _ := bus.Replay(j.ID)
	seen := map[string]bool{}
	for _, e := range log {
		seen[e.Type] = true
	}
	if !seen["ship.uncommitted"] || !seen["job.committed"] || seen["job.pushed"] {
		t.Fatalf("events %v", seen)
	}
}

func TestShipPushSendsBranchToOrigin(t *testing.T) {
	repo := gitRepo(t)
	remote := t.TempDir()
	gitOut(t, remote, "init", "-q", "--bare")
	gitOut(t, repo, "remote", "add", "origin", remote)
	bus := events.NewBus(t.TempDir())
	rt := &committingRT{}
	j := New(Spec{Ship: "push", ID: "P1", Title: "make app", Repo: repo, Prompt: "write done into app.txt"}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	j.Run(context.Background())

	if j.State != "done" || j.Commit == "" {
		t.Fatalf("state %s commit %q error %q", j.State, j.Commit, j.Error)
	}
	if got := gitOut(t, remote, "rev-parse", "--short", "factory/P1"); !strings.HasPrefix(j.Commit, got[:7]) {
		t.Fatalf("origin has %s, job committed %s", got, j.Commit)
	}
}

// A job stuck after its push (here the PR can't open) and carried on ships
// the commit it already pushed, rather than making it again with a new SHA
// that origin would refuse.
func TestResumeAtShippingKeepsPushedCommit(t *testing.T) {
	bin := t.TempDir() // a gh that always fails, so the PR step gets stuck
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho no such repo >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := gitRepo(t)
	remote := t.TempDir()
	gitOut(t, remote, "init", "-q", "--bare")
	gitOut(t, repo, "remote", "add", "origin", remote)
	bus := events.NewBus(t.TempDir())
	rt := &committingRT{}
	spec := Spec{Ship: "pr", ID: "P2", Title: "make app", Repo: repo, Prompt: "write done into app.txt"}
	j := New(spec, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()
	for i := 0; i < 300 && j.stateNow() != "stuck"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if j.stateNow() != "stuck" {
		t.Fatalf("state %s, want stuck on the PR; error %q", j.stateNow(), j.Error)
	}
	stop()
	<-done
	pushed := gitOut(t, remote, "rev-parse", "factory/P2")

	// a new engine carries it on; the PR gets opened by hand, so just push
	spec.Ship = "push"
	rt2 := &committingRT{}
	j2 := New(spec, bus, rt2)
	j2.State = "stuck"
	rt2.job = j2
	if start, err := j2.Continue("try again"); err != nil || !start {
		t.Fatalf("continue: %v %v", start, err)
	}
	j2.Run(context.Background())
	if j2.State != "done" {
		t.Fatalf("state %s error %q", j2.State, j2.Error)
	}
	if got := gitOut(t, remote, "rev-parse", "factory/P2"); got != pushed {
		t.Fatalf("origin moved from %s to %s", pushed, got)
	}
	if !strings.HasPrefix(pushed, j2.Commit) {
		t.Fatalf("job commit %s, origin %s", j2.Commit, pushed)
	}
}

// stuckRT plays a builder who can't get the check to pass on its own; it
// gets there once you tell it how.
type stuckRT struct {
	job    *Job
	starts int
}

func (f *stuckRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	if s.Agent == builder {
		f.starts++
	}
	return &stuckAgent{rt: f, cwd: s.Cwd, id: s.Agent}, nil
}

type stuckAgent struct {
	rt  *stuckRT
	cwd string
	id  string
}

func (a *stuckAgent) Prompt(_ context.Context, text string) (string, error) {
	switch {
	case a.id != builder: // the planner only plans
	case strings.Contains(text, "write exactly done"):
		os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
		a.rt.job.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "wrote done", Next: "review", Files: []string{"app.txt"}})
	default:
		os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("nearly\n"), 0o644)
	}
	return "ok", nil
}
func (a *stuckAgent) Steer(string) error { return nil }
func (a *stuckAgent) Stop() error        { return nil }

func TestStuckJobCarriesOnWithGuidance(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	bus := events.NewBus(t.TempDir())
	rt := &stuckRT{}
	j := New(Spec{ID: "S1", Title: "make app", Repo: repo, Prompt: "write done into app.txt", Checks: []string{"grep -q '^done$' app.txt"}, MaxLoops: 2}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	done := make(chan struct{})
	go func() { j.Run(context.Background()); close(done) }()
	waitState := func(want string) {
		for i := 0; i < 300 && !strings.EqualFold(j.stateNow(), want); i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if got := j.stateNow(); got != want {
			t.Fatalf("state %s, want %s (error %q)", got, want, j.Error)
		}
	}
	waitState("stuck")
	brief := j.Brief()
	for _, want := range []string{"Where S1 stopped", "checks keep failing", "app.txt", "grep -q"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief should mention %q:\n%s", want, brief)
		}
	}
	if start, err := j.Continue("The check wants the line done: write exactly done into app.txt."); err != nil || start {
		t.Fatalf("a live stuck job should take guidance at once: start %v err %v", start, err)
	}
	<-done
	if j.State != "done" || j.Commit == "" {
		t.Fatalf("state %s commit %q error %q", j.State, j.Commit, j.Error)
	}
	if rt.starts != 1 {
		t.Errorf("the same builder should carry on; builders started: %d", rt.starts)
	}
}

func (j *Job) stateNow() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.State
}

func TestStuckJobCarriesOnAfterRestart(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	spec := Spec{ID: "S2", Title: "make app", Repo: repo, Prompt: "write done into app.txt", Checks: []string{"grep -q '^done$' app.txt"}, MaxLoops: 2}
	bus := events.NewBus(t.TempDir())
	rt := &stuckRT{}
	j := New(spec, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { j.Run(ctx); close(done) }()
	for i := 0; i < 300 && j.stateNow() != "stuck"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	stop() // the engine goes away while the job is stuck
	<-done

	// a new engine loads the job and you guide it on
	rt2 := &stuckRT{}
	j2 := New(spec, bus, rt2)
	j2.State = "stuck"
	rt2.job = j2
	start, err := j2.Continue("write exactly done into app.txt")
	if err != nil || !start {
		t.Fatalf("a stuck job from before a restart should start again: start %v err %v", start, err)
	}
	j2.Run(context.Background())
	if j2.State != "done" || j2.Commit == "" {
		t.Fatalf("state %s commit %q error %q", j2.State, j2.Commit, j2.Error)
	}
	if rt2.starts != 1 {
		t.Errorf("one new builder should pick it up; started %d", rt2.starts)
	}
}

// busyRT plays an agent that works until it's told to stop.
type busyRT struct{ working, stopped chan struct{} }

func (f *busyRT) Start(context.Context, runtime.AgentSpec) (runtime.Agent, error) {
	return &busyAgent{f}, nil
}

type busyAgent struct{ rt *busyRT }

func (a *busyAgent) Prompt(ctx context.Context, _ string) (string, error) {
	select {
	case a.rt.working <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return "", ctx.Err()
}
func (a *busyAgent) Steer(string) error { return nil }
func (a *busyAgent) Stop() error {
	select {
	case a.rt.stopped <- struct{}{}:
	default:
	}
	return nil
}

func TestCancelStopsARunningJob(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	bus := events.NewBus(t.TempDir())
	rt := &busyRT{working: make(chan struct{}, 1), stopped: make(chan struct{}, 1)}
	j := New(Spec{ID: "C1", Title: "big job", Repo: repo, Prompt: "work forever", MaxLoops: 2}, bus, rt)
	bus.Place(j.ID, j.Dir())
	done := make(chan struct{})
	go func() { j.Run(context.Background()); close(done) }()
	<-rt.working
	if err := j.Cancel(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job kept running after Cancel")
	}
	if j.State != "cancelled" {
		t.Fatalf("state %s, want cancelled", j.State)
	}
	select {
	case <-rt.stopped:
	default:
		t.Error("the agent wasn't stopped")
	}
	if _, err := j.Continue("go on"); err == nil {
		t.Error("a cancelled job shouldn't take guidance")
	}
}

// helpRT plays a builder who needs a key only you have, and asks for it at once.
type helpRT struct {
	job   *Job
	asked int
}

func (f *helpRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &helpAgent{rt: f, cwd: s.Cwd, id: s.Agent}, nil
}

type helpAgent struct {
	rt  *helpRT
	cwd string
	id  string
}

func (a *helpAgent) Prompt(_ context.Context, text string) (string, error) {
	switch {
	case a.id != builder:
	case strings.Contains(text, "sk-test-42"):
		os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
		a.rt.job.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "used the key", Next: "review", Files: []string{"app.txt"}})
	default:
		a.rt.asked++
		a.rt.job.AskHuman(builder, Ask{Need: "I need the payment API key", Why: "The client won't start without it."})
	}
	return "ok", nil
}
func (a *helpAgent) Steer(string) error { return nil }
func (a *helpAgent) Stop() error        { return nil }

func TestAgentAsksYouAndCarriesOn(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Run()
	}
	bus := events.NewBus(t.TempDir())
	rt := &helpRT{}
	j := New(Spec{ID: "A1", Title: "pay", Repo: repo, Prompt: "wire up payments", Checks: []string{"grep -q '^done$' app.txt"}, MaxLoops: 2}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	done := make(chan struct{})
	go func() { j.Run(context.Background()); close(done) }()
	for i := 0; i < 300 && !j.Waiting(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !j.Waiting() {
		t.Fatalf("the job should wait for you; state %s", j.stateNow())
	}
	j.mu.Lock()
	q := j.Question
	j.mu.Unlock()
	if q != "**I need the payment API key**\n\nThe client won't start without it." {
		t.Errorf("question %q", q)
	}
	if !j.Answer("use sk-test-42") {
		t.Fatal("the answer wasn't taken")
	}
	<-done
	if j.State != "done" {
		t.Fatalf("state %s error %q", j.State, j.Error)
	}
	if rt.asked != 1 {
		t.Errorf("asked %d times, want once", rt.asked)
	}
}

// New test files always stay; existing ones go back unless the job allows
// test changes (the New task checkbox).
func TestProtectTestsKeepsNewTests(t *testing.T) {
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "old_test.go"), []byte("package x\n"), 0o644)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	wt, err := workspace.Create(repo, "T9")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(wt.Path, "e2e"), 0o755)
	os.WriteFile(filepath.Join(wt.Path, "e2e", "flow.go"), []byte("package e2e\n"), 0o644)
	os.WriteFile(filepath.Join(wt.Path, "old_test.go"), []byte("package x // edited\n"), 0o644)
	bus := events.NewBus(t.TempDir())
	j := New(Spec{ID: "T9", Repo: repo, TestsAllowed: true}, bus, &fakeRT{})
	bus.Place(j.ID, j.Dir())
	a := &fakeAgent{rt: &fakeRT{job: j}, cwd: wt.Path}

	if j.protectTests(context.Background(), wt, a, builder) {
		t.Fatal("with test changes allowed nothing should be undone")
	}
	if b, _ := os.ReadFile(filepath.Join(wt.Path, "old_test.go")); string(b) != "package x // edited\n" {
		t.Fatalf("allowed edit was undone: %q", b)
	}

	j.TestsAllowed = false
	if !j.protectTests(context.Background(), wt, a, builder) {
		t.Fatal("the edit to an existing test should be reported")
	}
	if b, _ := os.ReadFile(filepath.Join(wt.Path, "old_test.go")); string(b) != "package x\n" {
		t.Fatalf("existing test not put back: %q", b)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "e2e", "flow.go")); err != nil {
		t.Fatal("the new e2e file should have stayed")
	}
}
