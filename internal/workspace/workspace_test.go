package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateEditCommit(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	run("add", ".")
	run("commit", "-qm", "init")

	w, err := Create(repo, "J1")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("b\n"), 0o644)
	changed, _ := w.Changed()
	if len(changed) != 1 || changed[0] != "b.txt" {
		t.Fatalf("changed = %v", changed)
	}
	if _, err := w.Commit("add b"); err != nil {
		t.Fatal(err)
	}
	ignore, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(ignore) != ".factory/\n" {
		t.Fatalf(".gitignore = %q", ignore)
	}
}

// pushSetup makes a repo with a bare origin and a job worktree on J1.
func pushSetup(t *testing.T) (repo, remote string, w *Worktree, run func(dir string, args ...string) string) {
	t.Helper()
	run = func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo, remote = t.TempDir(), t.TempDir()
	run(remote, "init", "-q", "--bare")
	run(repo, "init", "-q", "-b", "main")
	run(repo, "config", "user.email", "t@t")
	run(repo, "config", "user.name", "t")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644)
	run(repo, "add", ".")
	run(repo, "commit", "-qm", "init")
	run(repo, "remote", "add", "origin", remote)
	w, err := Create(repo, "J1")
	if err != nil {
		t.Fatal(err)
	}
	return
}

// The engine re-made its own pushed commit (same change, new SHA): the
// push rebases, the duplicate drops out, and origin's commit stays.
func TestPushAfterRecommitOfSameChange(t *testing.T) {
	_, remote, w, run := pushSetup(t)
	base, _ := w.Head()
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("b\n"), 0o644)
	w.Commit("J1: add b")
	if err := w.Push(); err != nil {
		t.Fatal(err)
	}
	pushed := run(remote, "rev-parse", "factory/J1")
	w.Uncommit(strings.TrimSpace(base))
	run(w.Path, "commit", "-qm", "J1: add b", "--date", "2000-01-01T00:00:00")
	if err := w.Push(); err != nil {
		t.Fatalf("push after recommit: %v", err)
	}
	if got := run(remote, "rev-parse", "factory/J1"); got != pushed {
		t.Fatalf("origin moved to %s, want %s", got, pushed)
	}
}

// Someone else pushed to the job branch: the job's commit goes on top.
func TestPushRebasesOntoOthersCommits(t *testing.T) {
	repo, remote, w, run := pushSetup(t)
	os.WriteFile(filepath.Join(w.Path, "b.txt"), []byte("b\n"), 0o644)
	w.Commit("J1: add b")
	w.Push()
	other := filepath.Join(t.TempDir(), "other")
	run(repo, "clone", "-q", "-b", "factory/J1", remote, other)
	run(other, "-c", "user.email=o@o", "-c", "user.name=o", "commit", "-q", "--allow-empty", "-m", "theirs")
	run(other, "push", "-q", "origin", "factory/J1")
	os.WriteFile(filepath.Join(w.Path, "c.txt"), []byte("c\n"), 0o644)
	w.Commit("J1: add c")
	if err := w.Push(); err != nil {
		t.Fatal(err)
	}
	if log := run(remote, "log", "--format=%s", "factory/J1"); log != "J1: add c\ntheirs\nJ1: add b\ninit" {
		t.Fatalf("origin log:\n%s", log)
	}
}

// A conflicting remote commit stops the push and leaves the branch alone.
func TestPushConflictAborts(t *testing.T) {
	repo, remote, w, run := pushSetup(t)
	run(repo, "push", "-q", "origin", "main:factory/J1")
	other := filepath.Join(t.TempDir(), "other")
	run(repo, "clone", "-q", "-b", "factory/J1", remote, other)
	os.WriteFile(filepath.Join(other, "a.txt"), []byte("theirs\n"), 0o644)
	run(other, "-c", "user.email=o@o", "-c", "user.name=o", "commit", "-qam", "theirs")
	run(other, "push", "-q", "origin", "factory/J1")
	os.WriteFile(filepath.Join(w.Path, "a.txt"), []byte("ours\n"), 0o644)
	w.Commit("J1: ours")
	head, _ := w.Head()
	if err := w.Push(); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("err = %v", err)
	}
	if now, _ := w.Head(); now != head || !w.IsClean() {
		t.Fatalf("branch moved or dirty after aborted rebase")
	}
}
