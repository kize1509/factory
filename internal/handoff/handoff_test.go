package handoff

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProblems(t *testing.T) {
	wt := t.TempDir()
	os.WriteFile(filepath.Join(wt, "main.go"), nil, 0o644)
	ok := Note{From: "archimedes", To: "reviewers", Did: "fixed it", Next: "review", Files: []string{"main.go"}}
	if p := ok.Problems(wt, t.TempDir()); len(p) != 0 {
		t.Fatalf("unexpected %v", p)
	}
	bad := Note{From: "archimedes", Files: []string{"nope.go"}}
	if p := bad.Problems(wt, t.TempDir()); len(p) != 4 {
		t.Fatalf("want 4 problems, got %v", p)
	}
}
