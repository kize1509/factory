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
	"factory/internal/runtime"
)

// failingGH puts a gh on PATH that always fails, so a PR can't open.
func failingGH(t *testing.T) {
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho no such repo >&2\nexit 1\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// withOrigin makes a repo with a bare origin.
func withOrigin(t *testing.T) (repo, remote string) {
	repo, remote = gitRepo(t), t.TempDir()
	gitOut(t, remote, "init", "-q", "--bare")
	gitOut(t, repo, "remote", "add", "origin", remote)
	return
}

// theirs puts a commit on origin's branch that clashes with the job's
// app.txt, as if someone else had pushed there first.
func theirs(t *testing.T, repo, remote, branch string) {
	other := filepath.Join(t.TempDir(), "other")
	gitOut(t, repo, "clone", "-q", remote, other)
	gitOut(t, other, "checkout", "-q", "-b", branch, "origin/main")
	os.WriteFile(filepath.Join(other, "app.txt"), []byte("theirs\n"), 0o644)
	gitOut(t, other, "add", "app.txt")
	gitOut(t, other, "-c", "user.email=o@o", "-c", "user.name=o", "commit", "-qm", "theirs")
	gitOut(t, other, "push", "-q", "origin", branch)
}

// live runs a job in the background and lets a test wait for it to settle.
type live struct {
	t    *testing.T
	j    *Job
	bus  *events.Bus
	stop context.CancelFunc
	done chan struct{}
}

func start(t *testing.T, spec Spec, rt interface {
	runtime.Runtime
	setJob(*Job)
}) *live {
	bus := events.NewBus(t.TempDir())
	j := New(spec, bus, rt)
	rt.setJob(j)
	bus.Place(j.ID, j.Dir())
	ctx, stop := context.WithCancel(context.Background())
	l := &live{t: t, j: j, bus: bus, stop: stop, done: make(chan struct{})}
	go func() { j.Run(ctx); close(l.done) }()
	t.Cleanup(func() { stop(); <-l.done })
	return l
}

func (l *live) stucks() int {
	n := 0
	log, _ := l.bus.ReplayFile(l.j.ID)
	for _, e := range log {
		if e.Type == "job.stuck" {
			n++
		}
	}
	return n
}

// settle waits until the job is done, or stuck for the nth time.
func (l *live) settle(n int) string {
	l.t.Helper()
	for i := 0; i < 1500; i++ {
		select {
		case <-l.done:
			return l.j.stateNow()
		case <-time.After(20 * time.Millisecond):
		}
		if l.j.stateNow() == "stuck" && l.stucks() >= n {
			return "stuck"
		}
	}
	l.t.Fatalf("job never settled: state %s", l.j.stateNow())
	return ""
}

func (l *live) nudge(text, action string) {
	l.t.Helper()
	if _, err := l.j.ContinueWith(text, action); err != nil {
		l.t.Fatalf("continue %q %q: %v", text, action, err)
	}
}

func (f *committingRT) setJob(j *Job) { f.job = j }

func actionIDs(acts []Action) string {
	var ids []string
	for _, a := range acts {
		ids = append(ids, a.ID)
	}
	return strings.Join(ids, ",")
}

// Guidance at the shipping step goes to a repair turn; when the same
// failure comes back it's counted, "try again" goes away, and a way out
// still finishes the job.
func TestShipFailureRepeatsAndWayOut(t *testing.T) {
	failingGH(t)
	repo, remote := withOrigin(t)
	l := start(t, Spec{Ship: "pr", ID: "U1", Title: "make app", Repo: repo, Prompt: "write done"}, &committingRT{})
	if st := l.settle(1); st != "stuck" {
		t.Fatalf("state %s, want stuck on the PR", st)
	}
	if got := actionIDs(l.j.Actions); got != "retry,skip_pr,new_branch,force_push,commit_only" {
		t.Fatalf("actions %s", got)
	}
	pushed := gitOut(t, remote, "rev-parse", "factory/U1")

	l.nudge("I fixed the login, try again", "")
	if st := l.settle(2); st != "stuck" {
		t.Fatalf("state %s, want stuck again", st)
	}
	if l.j.Repeats != 1 || strings.Contains(actionIDs(l.j.Actions), "retry") {
		t.Fatalf("repeats %d actions %s", l.j.Repeats, actionIDs(l.j.Actions))
	}
	if !strings.Contains(l.j.Brief(), "same failure 2 times in a row") {
		t.Errorf("the brief should say the failure repeats:\n%s", l.j.Brief())
	}
	if _, err := l.j.ContinueWith("", "retry"); err == nil {
		t.Fatal("try again shouldn't be offered after a repeat")
	}

	l.nudge("", "commit_only")
	if st := l.settle(3); st != "done" {
		t.Fatalf("state %s error %q", st, l.j.Error)
	}
	if got := gitOut(t, remote, "rev-parse", "factory/U1"); got != pushed {
		t.Fatalf("origin moved from %s to %s", pushed, got)
	}
}

// Someone pushed clashing work to the job's branch: the push can't rebase,
// so the job ships to a new branch, or overwrites origin's when you say so.
func TestShipToNewBranchOrForce(t *testing.T) {
	for _, tc := range []struct{ action, branch string }{{"new_branch", "factory/U2-2"}, {"force_push", "factory/U2"}} {
		t.Run(tc.action, func(t *testing.T) {
			repo, remote := withOrigin(t)
			gitOut(t, repo, "push", "-q", "origin", "main")
			theirs(t, repo, remote, "factory/U2")
			l := start(t, Spec{Ship: "push", ID: "U2", Title: "make app", Repo: repo, Prompt: "write done"}, &committingRT{})
			if st := l.settle(1); st != "stuck" || !strings.Contains(l.j.Error, "conflict") {
				t.Fatalf("state %s error %q", st, l.j.Error)
			}
			l.nudge("", tc.action)
			if st := l.settle(2); st != "done" {
				t.Fatalf("state %s error %q", st, l.j.Error)
			}
			head := gitOut(t, l.j.Worktree, "rev-parse", "HEAD")
			if got := gitOut(t, remote, "rev-parse", tc.branch); got != head {
				t.Fatalf("origin %s is %s, job is %s", tc.branch, got, head)
			}
			if tc.action == "new_branch" {
				if msg := gitOut(t, remote, "log", "-1", "--format=%s", "factory/U2"); msg != "theirs" {
					t.Fatalf("origin's factory/U2 should be left alone, has %q", msg)
				}
			}
		})
	}
}

// repairRT plays a builder that, given a repair turn, merges origin's copy
// of the branch the way the person said to.
type repairRT struct {
	committingRT
	repairs int
}

func (f *repairRT) Start(ctx context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	a, _ := f.committingRT.Start(ctx, s)
	return &repairAgent{committingAgent: a.(*committingAgent), rt: f}, nil
}

type repairAgent struct {
	*committingAgent
	rt *repairRT
}

func (a *repairAgent) Prompt(ctx context.Context, text string) (string, error) {
	if !strings.Contains(text, "engine's own step") {
		return a.committingAgent.Prompt(ctx, text)
	}
	a.rt.repairs++
	for _, args := range [][]string{{"fetch", "-q", "origin"}, {"merge", "-q", "-X", "ours", "-m", "merge origin", "origin/factory/U3"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = a.cwd
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", &exec.ExitError{Stderr: out}
		}
	}
	return "Merged origin's branch, keeping our app.txt.", nil
}

// Guidance at the push goes to an agent with git; the engine ships what it
// leaves and keeps the merge it made.
func TestRepairTurnShipsTheAgentsFix(t *testing.T) {
	repo, remote := withOrigin(t)
	gitOut(t, repo, "push", "-q", "origin", "main")
	theirs(t, repo, remote, "factory/U3")
	rt := &repairRT{}
	l := start(t, Spec{Ship: "push", ID: "U3", Title: "make app", Repo: repo, Prompt: "write done"}, rt)
	if st := l.settle(1); st != "stuck" {
		t.Fatalf("state %s", st)
	}
	l.nudge("merge their branch in, keep our app.txt", "")
	if st := l.settle(2); st != "done" {
		t.Fatalf("state %s error %q", st, l.j.Error)
	}
	if rt.repairs != 1 {
		t.Fatalf("repair turns: %d", rt.repairs)
	}
	if log := gitOut(t, remote, "log", "--format=%s", "factory/U3"); !strings.Contains(log, "merge origin") || !strings.Contains(log, "theirs") {
		t.Fatalf("origin log:\n%s", log)
	}
	if got := gitOut(t, remote, "show", "factory/U3:app.txt"); got != "done" {
		t.Fatalf("app.txt on origin: %q", got)
	}
}

// Checks that won't pass needn't end the work: commit it as it is.
func TestCommitAnyway(t *testing.T) {
	repo := gitRepo(t)
	rt := &stuckRT{}
	bus := events.NewBus(t.TempDir())
	j := New(Spec{ID: "U4", Title: "make app", Repo: repo, Prompt: "write done", Checks: []string{"grep -q '^done$' app.txt"}, MaxLoops: 1}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	ctx, stop := context.WithCancel(context.Background())
	l := &live{t: t, j: j, bus: bus, stop: stop, done: make(chan struct{})}
	go func() { j.Run(ctx); close(l.done) }()
	t.Cleanup(func() { stop(); <-l.done })
	if st := l.settle(1); st != "stuck" || j.Phase != "checking" {
		t.Fatalf("state %s phase %s", st, j.Phase)
	}
	l.nudge("", "commit_anyway")
	if st := l.settle(2); st != "done" || j.Commit == "" {
		t.Fatalf("state %s commit %q error %q", st, j.Commit, j.Error)
	}
	if got := gitOut(t, repo, "show", "factory/U4:app.txt"); got != "nearly" {
		t.Fatalf("committed app.txt %q", got)
	}
}

func TestSameFailureIgnoresHashes(t *testing.T) {
	if !sameFailure("push 364862f rejected after 3 tries", "push aa4dd65 rejected after 4 tries") {
		t.Fatal("hashes and numbers should not make it a different failure")
	}
	if sameFailure("push rejected", "gh pr create failed") {
		t.Fatal("different failures")
	}
}
