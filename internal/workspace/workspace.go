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
	From   string // what the branch started from, like origin/main; set by Create
}

// Create adds a worktree on a new branch. With an origin it fetches first
// and starts from origin's default branch, so a job never builds on a stale
// local checkout or on whatever branch the main checkout has out (that is
// how PRs ended up stacked on each other). Without an origin it starts from
// the repo's HEAD.
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
	git(repo, "worktree", "prune")
	if _, err := git(repo, "rev-parse", "-q", "--verify", "refs/heads/"+w.Branch); err == nil {
		// the branch is still here from an earlier run whose worktree went
		if _, err := git(repo, "worktree", "add", w.Path, w.Branch); err != nil {
			return nil, err
		}
		w.From = w.Branch
		return w, nil
	}
	w.From = "HEAD"
	if b := FreshBase(repo); b != "" {
		w.From = b
	}
	if _, err := git(repo, "worktree", "add", "--no-track", "-b", w.Branch, w.Path, w.From); err != nil {
		return nil, err
	}
	return w, nil
}

// DefaultBranch is origin's default branch (main, master, ...), "" when
// the repo has no origin or origin has no branches yet.
func DefaultBranch(repo string) string {
	if out, err := git(repo, "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b, ok := strings.CutPrefix(strings.TrimSpace(out), "origin/"); ok && b != "" {
			return b
		}
	}
	for _, b := range []string{"main", "master", "trunk", "develop"} {
		if _, err := git(repo, "rev-parse", "-q", "--verify", "refs/remotes/origin/"+b); err == nil {
			return b
		}
	}
	return ""
}

// FreshBase fetches origin and returns its default branch as a ref, like
// origin/main, or "" when there is no origin to start from. A failed fetch
// (offline) still returns the last copy fetched.
func FreshBase(repo string) string {
	if !HasOrigin(repo) {
		return ""
	}
	git(repo, "fetch", "-q", "--prune", "origin")
	if _, err := git(repo, "rev-parse", "-q", "--verify", "refs/remotes/origin/HEAD"); err != nil {
		git(repo, "remote", "set-head", "origin", "--auto") // learn the default branch once
	}
	if b := DefaultBranch(repo); b != "" {
		return "origin/" + b
	}
	return ""
}

// FreshBaseName is origin's default branch as a ref, like origin/main,
// without fetching; "" when there is none.
func FreshBaseName(repo string) string {
	if b := DefaultBranch(repo); b != "" {
		return "origin/" + b
	}
	return ""
}

// ErrConflict is an update onto origin's default branch that conflicts.
type ErrConflict struct {
	Onto  string
	Files []string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("the work conflicts with %s in %s", e.Onto, strings.Join(e.Files, ", "))
}

// CatchUp brings the job's work onto the latest origin default branch.
// base is the commit the work started from; everything since (committed or
// not) is the work. It returns the new base, or base when origin hasn't
// moved. The work comes back uncommitted on top of the new base.
//
// On a conflict the rebase is left in progress with conflict markers in
// the files, so an agent can resolve them; call Continue after, or Abort.
func (w *Worktree) CatchUp(base string) (string, error) {
	onto := FreshBase(w.Repo)
	if onto == "" {
		return base, nil
	}
	target, err := w.rev(onto)
	if err != nil {
		return base, err
	}
	if _, err := git(w.Path, "merge-base", "--is-ancestor", target, base); err == nil {
		return base, nil // origin hasn't moved past where the work started
	}
	w.QuitInProgress()
	// fold the work into one commit, so a rebase stops at most once
	if _, err := git(w.Path, "reset", "--soft", "-q", base); err != nil {
		return base, err
	}
	git(w.Path, "add", "-A")
	if out, _ := git(w.Path, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		// no work yet: just move to the new base
		if _, err := git(w.Path, "reset", "--hard", "-q", target); err != nil {
			return base, err
		}
		return target, nil
	}
	if _, err := git(w.Path, "-c", "user.name=factory", "-c", "user.email=factory@localhost", "commit", "-q", "--no-verify", "-m", "factory: work in progress"); err != nil {
		return base, err
	}
	if _, err := git(w.Path, "-c", "user.name=factory", "-c", "user.email=factory@localhost", "rebase", "-q", target); err != nil {
		files := w.Conflicted()
		if len(files) == 0 {
			git(w.Path, "rebase", "--abort")
			git(w.Path, "reset", "--soft", "-q", base)
			return base, err
		}
		return target, &ErrConflict{Onto: onto, Files: files}
	}
	_, err = git(w.Path, "reset", "--soft", "-q", target)
	return target, err
}

// Conflicted lists files with unresolved conflicts.
func (w *Worktree) Conflicted() []string {
	out, _ := git(w.Path, "diff", "--name-only", "--diff-filter=U")
	return strings.Fields(out)
}

// Continue finishes a CatchUp that stopped on a conflict, once the
// conflicts are resolved in the files, and leaves the work uncommitted on
// top of target.
func (w *Worktree) Continue(target string) error {
	if files := w.withMarkers(); len(files) > 0 {
		return &ErrConflict{Onto: FreshBaseName(w.Repo), Files: files}
	}
	git(w.Path, "add", "-A")
	cmd := exec.Command("git", "-c", "user.name=factory", "-c", "user.email=factory@localhost", "rebase", "--continue")
	cmd.Dir, cmd.Env = w.Path, append(os.Environ(), "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil && w.inProgress("rebase-merge") {
		return fmt.Errorf("git rebase --continue: %v: %s", err, strings.TrimSpace(string(out)))
	}
	_, err := git(w.Path, "reset", "--soft", "-q", target)
	return err
}

// Abort gives up a CatchUp that stopped on a conflict: the work is back,
// uncommitted, on the base it started from.
func (w *Worktree) Abort(base string) {
	git(w.Path, "rebase", "--abort")
	git(w.Path, "reset", "--soft", "-q", base)
}

// withMarkers lists conflicted or changed files that still hold conflict
// markers.
func (w *Worktree) withMarkers() []string {
	all, _ := git(w.Path, "diff", "--name-only", "HEAD")
	var files []string
	for _, f := range dedupe(append(w.Conflicted(), strings.Fields(all)...)) {
		data, err := os.ReadFile(filepath.Join(w.Path, f))
		data = append([]byte("\n"), data...)
		if err == nil && bytes.Contains(data, []byte("\n<<<<<<< ")) && bytes.Contains(data, []byte("\n>>>>>>> ")) {
			files = append(files, f)
		}
	}
	return files
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (w *Worktree) rev(ref string) (string, error) {
	out, err := git(w.Path, "rev-parse", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

// gitDir is the worktree's own git folder (.git/worktrees/<job>).
func (w *Worktree) gitDir() string {
	out, err := git(w.Path, "rev-parse", "--git-dir")
	if err != nil {
		return ""
	}
	d := strings.TrimSpace(out)
	if !filepath.IsAbs(d) {
		d = filepath.Join(w.Path, d)
	}
	return d
}

func (w *Worktree) inProgress(name string) bool {
	d := w.gitDir()
	if d == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(d, name))
	return err == nil
}

// QuitInProgress ends a merge, cherry-pick or revert an agent left half
// done, keeping what it changed in the files, and aborts a rebase. The
// engine can't reset or commit in the middle of one of those. It returns
// what it ended, "" for nothing.
func (w *Worktree) QuitInProgress() string {
	switch {
	case w.inProgress("rebase-merge") || w.inProgress("rebase-apply"):
		git(w.Path, "rebase", "--abort")
		return "rebase"
	case w.inProgress("MERGE_HEAD"):
		git(w.Path, "merge", "--quit")
		return "merge"
	case w.inProgress("CHERRY_PICK_HEAD"):
		git(w.Path, "cherry-pick", "--quit")
		return "cherry-pick"
	case w.inProgress("REVERT_HEAD"):
		git(w.Path, "revert", "--quit")
		return "revert"
	}
	return ""
}

// Ahead counts the commits HEAD has that origin's default branch lacks,
// by content: a commit whose change is already there doesn't count. It is
// -1 when there is no default branch to compare with.
func (w *Worktree) Ahead() int {
	b := DefaultBranch(w.Repo)
	if b == "" {
		return -1
	}
	out, err := git(w.Path, "rev-list", "--count", "--cherry-pick", "--right-only", "--no-merges", "origin/"+b+"...HEAD")
	if err != nil {
		return -1
	}
	n := 0
	fmt.Sscan(strings.TrimSpace(out), &n)
	if n > 0 {
		// commits that are new but change nothing against the branch
		if d, err := git(w.Path, "diff", "--quiet", "origin/"+b, "HEAD"); err == nil && d == "" {
			return 0
		}
	}
	return n
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
//
// Then it rebases onto origin's default branch when that moved on, so the
// PR is built on what is merged now and not on a base that has gone. If
// that rewrote commits origin's copy has, the push replaces origin's copy,
// but only if it is still the copy the engine fetched and built on.
func (w *Worktree) Push() error {
	if err := w.Sync(); err != nil {
		return err
	}
	if err := w.RebaseOnDefault(); err != nil {
		return err
	}
	remote := w.RemoteBranch()
	args := []string{"push", "-u", "origin", w.Branch + ":refs/heads/" + remote}
	if sha, err := w.rev("refs/remotes/origin/" + remote); err == nil && sha != "" {
		if _, err := git(w.Path, "merge-base", "--is-ancestor", sha, "HEAD"); err != nil {
			args = []string{"push", "-u", "--force-with-lease=refs/heads/" + remote + ":" + sha, "origin", w.Branch + ":refs/heads/" + remote}
		}
	}
	_, err := git(w.Path, args...)
	return err
}

// RebaseOnDefault rebases the job's commits onto origin's default branch
// when it has moved on. Commits whose change is already there drop out. A
// conflict is aborted and leaves the branch as it was.
func (w *Worktree) RebaseOnDefault() error {
	onto := FreshBase(w.Repo)
	if onto == "" {
		return nil
	}
	if _, err := git(w.Path, "merge-base", "--is-ancestor", onto, "HEAD"); err == nil {
		return nil
	}
	w.QuitInProgress()
	if _, err := git(w.Path, "rebase", "-q", onto); err != nil {
		files := w.Conflicted()
		git(w.Path, "rebase", "--abort")
		if len(files) > 0 {
			return &ErrConflict{Onto: onto, Files: files}
		}
		return fmt.Errorf("rebase onto %s: %w", onto, err)
	}
	return nil
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
	args := []string{"pr", "create", "--head", w.RemoteBranch(), "--title", title, "--body", body}
	if b := DefaultBranch(w.Repo); b != "" {
		args = append(args, "--base", b) // never on top of another branch
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir, cmd.Env = w.Path, ghEnv(w.Path)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "No commits between") {
			return "", ErrNothingToShip
		}
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

// ErrNothingToShip is a branch with no change that the default branch
// doesn't already have: there is no pull request to open.
var ErrNothingToShip = fmt.Errorf("the branch has no changes that origin's default branch doesn't already have")

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
