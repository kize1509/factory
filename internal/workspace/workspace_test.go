package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
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
