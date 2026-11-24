package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// originSetup makes a repo whose origin has main, and a second clone
// ("other") that stands for everyone else merging work.
func originSetup(t *testing.T) (repo, other string, run func(dir string, args ...string) string) {
	t.Helper()
	run = func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo, remote := t.TempDir(), t.TempDir()
	run(remote, "init", "-q", "--bare", "-b", "main")
	run(repo, "init", "-q", "-b", "main")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	run(repo, "add", ".")
	run(repo, "commit", "-qm", "init")
	run(repo, "remote", "add", "origin", remote)
	run(repo, "push", "-q", "-u", "origin", "main")
	other = filepath.Join(t.TempDir(), "other")
	run(repo, "clone", "-q", remote, other)
	return
}

// merge stands for someone merging a PR: a commit lands on origin's main.
func merge(t *testing.T, run func(string, ...string) string, other, file, text string) string {
	t.Helper()
	run(other, "pull", "-q", "--rebase")
	os.WriteFile(filepath.Join(other, file), []byte(text), 0o644)
	run(other, "add", ".")
	run(other, "commit", "-qm", "merged "+file)
	run(other, "push", "-q", "origin", "main")
	return run(other, "rev-parse", "HEAD")
}

// The main checkout is behind and on another branch: the job still starts
// from origin's main, not from it.
func TestCreateStartsFromOriginNotStaleCheckout(t *testing.T) {
	repo, other, run := originSetup(t)
	merged := merge(t, run, other, "b.txt", "b\n")
	run(repo, "checkout", "-q", "-b", "someone-elses-branch")
	run(repo, "commit", "-q", "--allow-empty", "-m", "unmerged work")

	w, err := Create(repo, "J1")
	if err != nil {
		t.Fatal(err)
	}
	if head, _ := w.Head(); head != merged {
		t.Fatalf("job starts at %s, want origin/main %s", head, merged)
	}
	if w.From != "origin/main" {
		t.Fatalf("From = %q", w.From)
	}
}

// Without a remote the job starts from HEAD, as before.
func TestCreateWithoutOriginUsesHead(t *testing.T) {
	repo := t.TempDir()
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	w, err := Create(repo, "J1")
	if err != nil || w.From != "HEAD" {
		t.Fatalf("err %v, From %q", err, w.From)
	}
}

// main moved on while the job worked: the work comes back uncommitted on
// top of the new main.
func TestCatchUpPutsWorkOnNewMain(t *testing.T) {
	repo, other, run := originSetup(t)
	w, _ := Create(repo, "J1")
	base, _ := w.Head()
	os.WriteFile(filepath.Join(w.Path, "c.txt"), []byte("ours\n"), 0o644)
	merged := merge(t, run, other, "b.txt", "theirs\n")

	nb, err := w.CatchUp(base)
	if err != nil {
		t.Fatal(err)
	}
	if nb != merged {
		t.Fatalf("new base %s, want %s", nb, merged)
	}
	if head, _ := w.Head(); head != merged {
		t.Fatalf("HEAD %s, want %s", head, merged)
	}
	if b, _ := os.ReadFile(filepath.Join(w.Path, "b.txt")); string(b) != "theirs\n" {
		t.Fatal("main's change is missing")
	}
	if c, _ := os.ReadFile(filepath.Join(w.Path, "c.txt")); string(c) != "ours\n" {
		t.Fatal("the job's work is missing")
	}
	if w.IsClean() {
		t.Fatal("the work should stay uncommitted for the engine's own commit")
	}
	// nothing moved since: no-op
	if again, err := w.CatchUp(nb); err != nil || again != nb {
		t.Fatalf("second catch up: %s %v", again, err)
	}
}

// A conflict stops with the files named; once resolved, Continue finishes.
func TestCatchUpConflictResolvedThenContinue(t *testing.T) {
	repo, other, run := originSetup(t)
	w, _ := Create(repo, "J1")
	base, _ := w.Head()
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("ours\n"), 0o644)
	merged := merge(t, run, other, "a.txt", "theirs\n")

	nb, err := w.CatchUp(base)
	var c *ErrConflict
	if !errors.As(err, &c) || len(c.Files) != 1 || c.Files[0] != "a.txt" {
		t.Fatalf("err = %v", err)
	}
	if err := w.Continue(nb); err == nil {
		t.Fatal("continue with markers still in the file should fail")
	}
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("theirs and ours\n"), 0o644)
	if err := w.Continue(nb); err != nil {
		t.Fatal(err)
	}
	if head, _ := w.Head(); head != merged {
		t.Fatalf("HEAD %s, want %s", head, merged)
	}
	if b, _ := os.ReadFile(filepath.Join(w.Path, "a.txt")); string(b) != "theirs and ours\n" {
		t.Fatalf("a.txt = %q", b)
	}
}

// Abort puts the work back on the old base.
func TestCatchUpAbort(t *testing.T) {
	repo, other, run := originSetup(t)
	w, _ := Create(repo, "J1")
	base, _ := w.Head()
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("ours\n"), 0o644)
	merge(t, run, other, "a.txt", "theirs\n")
	w.CatchUp(base)
	w.Abort(base)
	if head, _ := w.Head(); head != base {
		t.Fatalf("HEAD %s, want %s", head, base)
	}
	if b, _ := os.ReadFile(filepath.Join(w.Path, "a.txt")); string(b) != "ours\n" {
		t.Fatalf("a.txt = %q", b)
	}
}

// Push rebases the job's commit onto main when main moved, so the PR is
// against what's merged now.
func TestPushRebasesOntoNewMain(t *testing.T) {
	repo, other, run := originSetup(t)
	w, _ := Create(repo, "J1")
	os.WriteFile(filepath.Join(w.Path, "c.txt"), []byte("c\n"), 0o644)
	w.Commit("J1: add c")
	merged := merge(t, run, other, "b.txt", "b\n")
	if err := w.Push(); err != nil {
		t.Fatal(err)
	}
	run(other, "fetch", "-q", "origin")
	if parent := run(other, "rev-parse", "origin/factory/J1^"); parent != merged {
		t.Fatalf("pushed branch sits on %s, want main %s", parent, merged)
	}
	// main moves again after the branch was pushed: the next push rewrites
	// the engine's own branch with a lease
	merged = merge(t, run, other, "d.txt", "d\n")
	os.WriteFile(filepath.Join(w.Path, "e.txt"), []byte("e\n"), 0o644)
	w.Commit("J1: add e")
	if err := w.Push(); err != nil {
		t.Fatal(err)
	}
	run(other, "fetch", "-q", "origin")
	if base := run(other, "merge-base", "origin/factory/J1", "origin/main"); base != merged {
		t.Fatalf("branch is based on %s, want %s", base, merged)
	}
}

// Work that is already on main leaves nothing to ship.
func TestAheadIsZeroWhenMainHasTheChange(t *testing.T) {
	repo, other, run := originSetup(t)
	w, _ := Create(repo, "J1")
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("b\n"), 0o644)
	w.Commit("J1: add b")
	if n := w.Ahead(); n != 1 {
		t.Fatalf("ahead = %d, want 1", n)
	}
	merge(t, run, other, "b.txt", "b\n") // merged elsewhere, as another commit
	if err := w.RebaseOnDefault(); err != nil {
		t.Fatal(err)
	}
	if n := w.Ahead(); n != 0 {
		t.Fatalf("ahead = %d, want 0", n)
	}
}

// An agent left a merge half done: QuitInProgress ends it and keeps the
// files, so the engine can reset and commit.
func TestQuitInProgressMerge(t *testing.T) {
	repo, other, run := originSetup(t)
	w, _ := Create(repo, "J1")
	base, _ := w.Head()
	os.WriteFile(filepath.Join(w.Path, "c.txt"), []byte("c\n"), 0o644)
	w.Commit("J1: c")
	merge(t, run, other, "b.txt", "b\n")
	run(w.Path, "fetch", "-q", "origin")
	run(w.Path, "merge", "--no-commit", "--no-ff", "origin/main")
	if err := w.Uncommit(base); err == nil {
		t.Fatal("expected git to refuse a reset mid-merge")
	}
	if got := w.QuitInProgress(); got != "merge" {
		t.Fatalf("ended %q", got)
	}
	if err := w.Uncommit(base); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(w.Path, f)); err != nil {
			t.Fatalf("%s lost", f)
		}
	}
}

// The product agent's checkout follows origin's main and is reset each run.
func TestBoardCheckoutIsFresh(t *testing.T) {
	repo, other, run := originSetup(t)
	path, from := BoardCheckout(repo)
	if from != "origin/main" || path == repo {
		t.Fatalf("path %s from %q", path, from)
	}
	os.WriteFile(filepath.Join(path, "junk.txt"), []byte("x"), 0o644)
	merged := merge(t, run, other, "b.txt", "b\n")
	path2, _ := BoardCheckout(repo)
	if path2 != path {
		t.Fatalf("moved to %s", path2)
	}
	if head := run(path, "rev-parse", "HEAD"); head != merged {
		t.Fatalf("board at %s, want %s", head, merged)
	}
	if _, err := os.Stat(filepath.Join(path, "junk.txt")); err == nil {
		t.Fatal("stray file survived the reset")
	}
}
