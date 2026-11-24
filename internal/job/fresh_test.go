package job

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/runtime"
	"factory/internal/workspace"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// withMainOrigin makes a repo whose origin has main, plus another clone that
// stands for other people merging work.
func withMainOrigin(t *testing.T) (repo, other string) {
	t.Helper()
	repo, remote := t.TempDir(), t.TempDir()
	gitT(t, remote, "init", "-q", "--bare", "-b", "main")
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "config", "user.email", "t@t")
	gitT(t, repo, "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".factory/\n"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-qm", "init")
	gitT(t, repo, "remote", "add", "origin", remote)
	gitT(t, repo, "push", "-q", "-u", "origin", "main")
	other = filepath.Join(t.TempDir(), "other")
	gitT(t, repo, "clone", "-q", remote, other)
	return
}

func mergeOnMain(t *testing.T, other, file, text string) {
	t.Helper()
	gitT(t, other, "pull", "-q", "--rebase")
	os.WriteFile(filepath.Join(other, file), []byte(text), 0o644)
	gitT(t, other, "add", ".")
	gitT(t, other, "commit", "-qm", "merged "+file)
	gitT(t, other, "push", "-q", "origin", "main")
}

// mainMovesRT is a builder whose work is done while someone else merges
// into main: during its turn, onTurn runs.
type mainMovesRT struct {
	job    *Job
	onTurn func(cwd string)
}

func (f *mainMovesRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &mainMovesAgent{rt: f, cwd: s.Cwd, id: s.Agent}, nil
}

type mainMovesAgent struct {
	rt      *mainMovesRT
	cwd, id string
}

func (a *mainMovesAgent) Prompt(_ context.Context, text string) (string, error) {
	if a.id == builder && strings.HasPrefix(text, "Ticket") {
		a.rt.onTurn(a.cwd)
		a.rt.job.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "did it", Next: "review", Files: []string{"a.txt"}})
	}
	return "ok", nil
}
func (a *mainMovesAgent) Steer(string) error { return nil }
func (a *mainMovesAgent) Stop() error        { return nil }

// main moved while the job worked: the job's commit sits on the new main,
// so its PR won't carry stale history or conflict with what was merged.
func TestJobCatchesUpWithMain(t *testing.T) {
	repo, other := withMainOrigin(t)
	bus := events.NewBus(t.TempDir())
	rt := &mainMovesRT{}
	rt.onTurn = func(cwd string) {
		os.WriteFile(filepath.Join(cwd, "c.txt"), []byte("ours\n"), 0o644)
		mergeOnMain(t, other, "b.txt", "theirs\n")
	}
	j := New(Spec{ID: "J1", Title: "add c", Repo: repo, Prompt: "add c", Checks: []string{"test -f c.txt"}}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	j.Run(context.Background())
	if j.State != "done" || j.Commit == "" {
		t.Fatalf("state %s commit %q error %q", j.State, j.Commit, j.Error)
	}
	main := gitT(t, other, "rev-parse", "HEAD")
	if parent := gitT(t, j.Worktree, "rev-parse", "HEAD^"); parent != main {
		t.Fatalf("job commit sits on %s, want the new main %s", parent, main)
	}
	if b, _ := os.ReadFile(filepath.Join(j.Worktree, "b.txt")); string(b) != "theirs\n" {
		t.Fatal("main's change is missing from the job's branch")
	}
	seen := map[string]bool{}
	log, _ := bus.Replay(j.ID)
	for _, e := range log {
		seen[e.Type] = true
	}
	if !seen["workspace.caught_up"] {
		t.Error("no workspace.caught_up event")
	}
}

// The same change was merged on main while the job worked: nothing is left
// to ship, and the job ends done instead of stuck at "no commits between".
func TestJobAlreadyOnMainEndsDone(t *testing.T) {
	repo, other := withMainOrigin(t)
	bus := events.NewBus(t.TempDir())
	rt := &mainMovesRT{}
	rt.onTurn = func(cwd string) {
		os.WriteFile(filepath.Join(cwd, "b.txt"), []byte("same\n"), 0o644)
		mergeOnMain(t, other, "b.txt", "same\n")
	}
	j := New(Spec{ID: "J2", Title: "add b", Repo: repo, Prompt: "add b", Ship: "pr"}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	j.Run(context.Background())
	if j.State != "done" {
		t.Fatalf("state %s error %q", j.State, j.Error)
	}
	if j.PR != "" {
		t.Fatalf("opened PR %s", j.PR)
	}
}

// With a reviewer, a builder's edit to an existing test stays and the
// reviewer is told to judge it; without one it's put back.
func TestTestEditsGoToTheReviewer(t *testing.T) {
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "old_test.go"), []byte("package x\n"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-qm", "init")
	wt, err := workspace.Create(repo, "T10")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt.Path, "old_test.go"), []byte("package x // count is 5 now\n"), 0o644)
	bus := events.NewBus(t.TempDir())
	j := New(Spec{ID: "T10", Repo: repo}, bus, &fakeRT{})
	j.SetReviewer(&fakeRT{})
	bus.Place(j.ID, j.Dir())
	a := &fakeAgent{rt: &fakeRT{job: j}, cwd: wt.Path}

	if j.protectTests(context.Background(), wt, a, builder) {
		t.Fatal("with a reviewer the edit should stay")
	}
	if b, _ := os.ReadFile(filepath.Join(wt.Path, "old_test.go")); string(b) != "package x // count is 5 now\n" {
		t.Fatalf("edit undone: %q", b)
	}
	if got := j.testEditsForReview(); !strings.Contains(got, "old_test.go") {
		t.Fatalf("reviewer isn't told: %q", got)
	}
	if !strings.Contains(j.testsRule(), "reviewer reads every change") {
		t.Fatalf("builder rule: %q", j.testsRule())
	}
	if len(a.prompts) != 0 {
		t.Fatalf("the builder was nagged: %v", a.prompts)
	}
}

// Agents don't get to ask you about what the engine decides for them.
func TestAskHumanTurnsAwayEngineQuestions(t *testing.T) {
	bus := events.NewBus(t.TempDir())
	j := New(Spec{ID: "T11", Title: "migrations"}, bus, &fakeRT{})
	j.SetReviewer(&fakeRT{})
	bus.Place(j.ID, j.Dir())
	for _, need := range []string{
		"I need your OK to edit three existing tests: tests/test_db.py",
		"I need permission for my edits to tests/test_engine.py to stick",
		"Can you approve that I rebase onto main?",
	} {
		if err := j.AskHuman(builder, Ask{Need: need}); err == nil {
			t.Errorf("%q went through", need)
		}
	}
	for _, need := range []string{
		"I need you to approve a Stripe test API key for the sandbox.",
		"Can I get read access to the git submodule's private repo?",
		"Should the login test use a fixed clock or a longer timeout?",
	} {
		if err := j.AskHuman(builder, Ask{Need: need}); err != nil {
			t.Errorf("%q was turned away: %v", need, err)
		}
	}
	if err := j.AskHuman(builder, Ask{Need: strings.Repeat("word ", 80)}); err == nil {
		t.Error("an essay went through as need")
	}
	if err := j.AskHuman(builder, Ask{Need: "I need a Product Hunt API token.", Recommend: "Set PRODUCT_HUNT_TOKEN and I'll record fixtures."}); err != nil {
		t.Fatal(err)
	}
	q := j.asks[builder]
	md := q.Markdown()
	for _, want := range []string{"Archimedes, working on T11 (migrations), asks:", "**I need a Product Hunt API token.**", "What I'd do: Set PRODUCT_HUNT_TOKEN"} {
		if !strings.Contains(md, want) {
			t.Errorf("question lacks %q:\n%s", want, md)
		}
	}
}
