// Package fixture makes a throwaway git repo from testdata/fixtures/<name>/.
package fixture

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Root finds the module root (the folder with go.mod), starting from the
// working directory.
func Root() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		if up := filepath.Dir(dir); up != dir {
			dir = up
			continue
		}
		return "", fmt.Errorf("no go.mod above the working directory")
	}
}

// Make copies testdata/fixtures/<name> into a new folder under parent,
// initialises a git repo on main there and commits everything. The commit
// date and author are fixed, so the same fixture gives the same commit.
func Make(parent, name string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	src := filepath.Join(root, "testdata", "fixtures", name)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("fixture %q: %w", name, err)
	}
	dst := filepath.Join(parent, name)
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(target, b, info.Mode().Perm())
	})
	if err != nil {
		return "", err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "fixture@example.com"}, {"config", "user.name", "fixture"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture " + name}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
			"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %v: %v: %s", args, err, out)
		}
	}
	return dst, nil
}

// New is Make in a fresh temp folder that the test cleans up.
func New(t testing.TB, name string) string {
	t.Helper()
	dir, err := Make(t.TempDir(), name)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
