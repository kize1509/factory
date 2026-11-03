package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/harness/fixture"
)

func TestCheckSeededFailureFillsRunFolder(t *testing.T) {
	repo := fixture.New(t, "seeded")
	root := t.TempDir()
	r, err := Check(context.Background(), root, repo, "cart.total", "tests", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Pass || r.Failure == nil || r.Failure.Step != "go test ./..." || !strings.Contains(r.Failure.Actual, "TestTotal") {
		t.Fatalf("want a failure naming TestTotal: %+v", r)
	}
	if r.Kind != KindTests || r.Feature != "cart.total" || r.Commit == "" || r.Metrics["wall_ms"] <= 0 {
		t.Fatalf("%+v", r)
	}
	dir := RunDir(root, r.Run)
	for _, f := range append([]string{"result.json"}, r.Evidence.Logs...) {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}
	// results reads a patrol run like any other
	got, err := Results(root, r.Run)
	if err != nil || got.Run != r.Run || got.Pass {
		t.Fatalf("%v %+v", err, got)
	}
	if !strings.Contains(r.ReplayCmd, "--commit "+r.Commit) {
		t.Fatal(r.ReplayCmd)
	}
	// the repo was left alone: no worktree registered, no files added
	if out := gitOut(repo, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Fatalf("worktree left behind: %s", out)
	}
}

func TestCheckStaticPassesAndCommit(t *testing.T) {
	repo := fixture.New(t, "seeded")
	r, err := Check(context.Background(), t.TempDir(), repo, "root", "static", CheckOptions{})
	if err != nil || !r.Pass || r.Failure != nil {
		t.Fatalf("%v %+v", err, r)
	}
	if _, err := Check(context.Background(), t.TempDir(), repo, "root", "nope", CheckOptions{}); err == nil {
		t.Fatal("unknown technique must error")
	}
	if _, err := Check(context.Background(), t.TempDir(), repo, "root", "static", CheckOptions{Commit: "deadbeef"}); err == nil {
		t.Fatal("unknown commit must error")
	}
}
