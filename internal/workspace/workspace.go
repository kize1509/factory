// Package workspace gives each job its own git worktree and branch, inside
// the project's .factory folder.
package workspace

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Worktree struct {
	Repo   string // the project's main checkout
	Path   string // .factory/worktrees/<job>
	Branch string // factory/<job>
	Remote string // the branch on origin it ships to, when not Branch
}

// Create adds a worktree on a new branch from the repo's current HEAD.
func Create(repo, job string) (*Worktree, error) {
	w := &Worktree{
		Repo:   repo,
		Path:   filepath.Join(repo, ".factory", "worktrees", job),
		Branch: "factory/" + job,
	}
	if _, err := os.Stat(w.Path); err == nil {
		return w, nil // reuse after a restart
	}
	if err := ensureIgnored(repo); err != nil {
		return nil, err
	}
	if _, err := git(repo, "worktree", "add", "-b", w.Branch, w.Path, "HEAD"); err != nil {
		return nil, err
	}
	return w, nil
}

// Changed lists files that differ from the branch point, including new ones.
func (w *Worktree) Changed() ([]string, error) {
	out, err := git(w.Path, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 3 {
			files = append(files, strings.TrimSpace(line[3:]))
		}
	}
	return files, nil
}

// Diff returns the working changes as a patch.
func (w *Worktree) Diff() (string, error) {
	git(w.Path, "add", "-A", "--intent-to-add")
	return git(w.Path, "diff")
}

// InHead reports whether path exists in the branch's last commit.
func (w *Worktree) InHead(path string) bool {
	_, err := git(w.Path, "cat-file", "-e", "HEAD:"+path)
	return err == nil
}

// Revert puts paths back to how they are at HEAD (used to undo edits to
// protected test files).
func (w *Worktree) Revert(paths ...string) error {
	_, err := git(w.Path, append([]string{"checkout", "HEAD", "--"}, paths...)...)
	return err
}

// Commit stages everything and commits it on the job branch.
func (w *Worktree) Commit(msg string) (string, error) {
	if _, err := git(w.Path, "add", "-A"); err != nil {
		return "", err
	}
	if out, _ := git(w.Path, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("nothing to commit")
	}
	if _, err := git(w.Path, "commit", "-m", msg); err != nil {
		return "", err
	}
	sha, err := git(w.Path, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(sha), err
}

// Uncommit moves the branch back to base and keeps every change in the
// working tree, so commits an agent made on its own don't count.
func (w *Worktree) Uncommit(base string) error {
	_, err := git(w.Path, "reset", "--soft", "-q", base)
	return err
}

// Push sends the job branch to origin. If origin's copy of the branch has
// commits this one lacks, it rebases onto them first (commits whose change
// is already there drop out), so the push is a fast-forward. A rebase that
// conflicts is aborted and leaves the branch as it was.
func (w *Worktree) Push() error {
	if err := w.Sync(); err != nil {
		return err
	}
	_, err := git(w.Path, "push", "-u", "origin", w.Branch+":refs/heads/"+w.RemoteBranch())
	return err
}

// ForcePush replaces origin's copy of the branch with this one. It still
// refuses if origin moved since the last fetch, so it only drops commits
// the engine has seen.
func (w *Worktree) ForcePush() error {
	remote := w.RemoteBranch()
	has, err := w.onOrigin(remote)
	if err != nil {
		return err
	}
	if !has { // nothing there to overwrite
		_, err := git(w.Path, "push", "-u", "origin", w.Branch+":refs/heads/"+remote)
		return err
	}
	if _, err := git(w.Path, "fetch", "-q", "origin", "+refs/heads/"+remote+":refs/remotes/origin/"+remote); err != nil {
		return err
	}
	_, err = git(w.Path, "push", "-u", "--force-with-lease=refs/heads/"+remote, "origin", w.Branch+":refs/heads/"+remote)
	return err
}

// RemoteBranch is the branch on origin the job ships to: Remote when set,
// otherwise the job branch's own name.
func (w *Worktree) RemoteBranch() string {
	if w.Remote != "" {
		return w.Remote
	}
	return w.Branch
}

// FreeBranch finds a name next to the job branch (factory/X-2, -3, ...)
// that origin doesn't have yet.
func (w *Worktree) FreeBranch() (string, error) {
	for n := 2; n < 100; n++ {
		name := fmt.Sprintf("%s-%d", w.Branch, n)
		has, err := w.onOrigin(name)
		if err != nil {
			return "", err
		}
		if !has {
			return name, nil
		}
	}
	return "", fmt.Errorf("origin already has %s-2 to %s-99", w.Branch, w.Branch)
}

// onOrigin reports whether origin has the branch.
func (w *Worktree) onOrigin(branch string) (bool, error) {
	cmd := exec.Command("git", "ls-remote", "--exit-code", "--heads", "origin", branch)
	cmd.Dir = w.Path
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 2 {
		return false, nil // origin answered: no such branch
	}
	return false, fmt.Errorf("git ls-remote origin %s: %v: %s", branch, err, strings.TrimSpace(errb.String()))
}

// Sync fetches origin's copy of the job branch and, when it has moved on,
// rebases the job branch onto it. No copy on origin is fine.
func (w *Worktree) Sync() error {
	branch := w.RemoteBranch()
	has, err := w.onOrigin(branch)
	if err != nil || !has {
		return err
	}
	remote := "refs/remotes/origin/" + branch
	if _, err := git(w.Path, "fetch", "-q", "origin", "+refs/heads/"+branch+":"+remote); err != nil {
		return err
	}
	if _, err := git(w.Path, "merge-base", "--is-ancestor", remote, "HEAD"); err == nil {
		return nil // origin is behind or level: a plain push will do
	}
	if _, err := git(w.Path, "rebase", "-q", remote); err != nil {
		git(w.Path, "rebase", "--abort")
		return fmt.Errorf("origin/%s has commits that conflict with the job's; rebase them by hand: %w", branch, err)
	}
	return nil
}

// IsClean reports whether the worktree has no uncommitted changes.
func (w *Worktree) IsClean() bool {
	out, err := git(w.Path, "status", "--porcelain")
	return err == nil && strings.TrimSpace(out) == ""
}

// OpenPR opens a pull request for the job branch with the gh CLI and
// returns its URL.
func (w *Worktree) OpenPR(title, body string) (string, error) {
	cmd := exec.Command("gh", "pr", "create", "--head", w.RemoteBranch(), "--title", title, "--body", body)
	cmd.Dir = w.Path
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "already exists") { // opened earlier, by hand or by a repair turn
			for _, f := range strings.Fields(msg) {
				if strings.HasPrefix(f, "https://") {
					return f, nil
				}
			}
		}
		return "", fmt.Errorf("gh pr create: %v: %s", err, msg)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// ensureIgnored makes sure .factory/ is in the repo's .gitignore.
func ensureIgnored(repo string) error {
	path := filepath.Join(repo, ".gitignore")
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == ".factory/" || strings.TrimSpace(line) == ".factory" {
			return nil
		}
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	return os.WriteFile(path, append(data, []byte(".factory/\n")...), 0o644)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// CreateFrom adds a worktree on a new branch that starts at base (a commit
// or branch), for an experiment next to the job's own worktree.
func CreateFrom(repo, name, branch, base string) (*Worktree, error) {
	w := &Worktree{Repo: repo, Path: filepath.Join(repo, ".factory", "worktrees", name), Branch: branch}
	os.RemoveAll(w.Path)
	git(repo, "worktree", "prune")
	git(repo, "branch", "-D", branch)
	if _, err := git(repo, "worktree", "add", "-b", branch, w.Path, base); err != nil {
		return nil, err
	}
	return w, nil
}

// Head is the worktree's current commit.
func (w *Worktree) Head() (string, error) {
	out, err := git(w.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Clean throws away uncommitted changes, back to the last commit.
func (w *Worktree) Clean() error {
	if _, err := git(w.Path, "reset", "--hard", "-q"); err != nil {
		return err
	}
	_, err := git(w.Path, "clean", "-fdq")
	return err
}

// FastForward moves this worktree's branch to another branch that is ahead
// of it (the winning experiment).
func (w *Worktree) FastForward(branch string) error {
	_, err := git(w.Path, "merge", "--ff-only", "-q", branch)
	return err
}

// DiffSize counts changed lines and files compared with base, committed or not.
func (w *Worktree) DiffSize(base string) (lines, files int) {
	git(w.Path, "add", "-A", "--intent-to-add")
	out, _ := git(w.Path, "diff", "--numstat", base)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		var a, d int
		fmt.Sscan(f[0], &a)
		fmt.Sscan(f[1], &d)
		lines += a + d
		files++
	}
	return
}

// DiffFrom is the patch against base, committed or not.
func (w *Worktree) DiffFrom(base string) string {
	git(w.Path, "add", "-A", "--intent-to-add")
	out, _ := git(w.Path, "diff", base)
	return out
}

// Stat lists the files changed since base, with lines added and removed.
func (w *Worktree) Stat(base string) string {
	git(w.Path, "add", "-A", "--intent-to-add")
	out, _ := git(w.Path, "diff", "--stat", base)
	return strings.TrimSpace(out)
}

// Remove deletes the worktree and its branch.
func (w *Worktree) Remove() {
	git(w.Repo, "worktree", "remove", "--force", w.Path)
	git(w.Repo, "branch", "-D", w.Branch)
}

// Discard removes the worktree and, unless it holds shipped work, the branch.
// With keepUnmerged the branch only goes if it is merged into the repo's
// HEAD (git's safe delete); with keep it stays.
func (w *Worktree) Discard(keep, keepUnmerged bool) {
	git(w.Repo, "worktree", "remove", "--force", w.Path)
	os.RemoveAll(w.Path)
	git(w.Repo, "worktree", "prune")
	switch {
	case keep || w.Branch == "":
	case keepUnmerged:
		git(w.Repo, "branch", "-d", w.Branch)
	default:
		git(w.Repo, "branch", "-D", w.Branch)
	}
}
