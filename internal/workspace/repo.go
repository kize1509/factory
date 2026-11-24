package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Repositories the factory makes: a git repository in a folder that has
// none, and a private GitHub repository for one that has no remote.

// HasOrigin reports whether the repository has a remote named origin.
func HasOrigin(repo string) bool {
	_, err := git(repo, "remote", "get-url", "origin")
	return err == nil
}

// Origin is origin's URL, "" when there is none.
func Origin(repo string) string {
	out, err := git(repo, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// InitRepo makes path a git repository on main, creating the folder when it
// doesn't exist. What is in the folder already becomes the first commit, so
// jobs have a commit to branch from. A folder inside another repository is
// refused: the factory would work in the outer one.
func InitRepo(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	if _, err := git(path, "rev-parse", "--show-toplevel"); err == nil {
		return fmt.Errorf("%s is already inside a git repository", path)
	}
	if _, err := git(path, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := ensureIgnored(path); err != nil {
		return err
	}
	if _, err := git(path, "add", "-A"); err != nil {
		return err
	}
	if _, err := git(path, "commit", "-q", "--allow-empty", "-m", "Initial commit"); err != nil {
		os.RemoveAll(filepath.Join(path, ".git")) // leave the folder as it was
		return fmt.Errorf("the first commit failed (is git's user.name and user.email set?): %w", err)
	}
	return nil
}

// Account is one GitHub login the gh CLI has.
type Account struct {
	Login  string `json:"login"`
	Active bool   `json:"active"`
}

// GitHubAccounts lists the github.com accounts gh is logged in with.
func GitHubAccounts() ([]Account, error) {
	out, err := exec.Command("gh", "auth", "status", "--json", "hosts").Output()
	if err != nil && len(out) == 0 {
		return nil, fmt.Errorf("gh isn't logged in to GitHub (gh auth login)")
	}
	var st struct {
		Hosts map[string][]struct {
			Login  string `json:"login"`
			Active bool   `json:"active"`
			State  string `json:"state"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, fmt.Errorf("gh auth status: %w", err)
	}
	var list []Account
	for _, a := range st.Hosts["github.com"] {
		if a.State == "success" && a.Login != "" {
			list = append(list, Account{Login: a.Login, Active: a.Active})
		}
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("gh has no working GitHub login (gh auth login)")
	}
	return list, nil
}

// NewRemote is the GitHub repository to create for a repo that has none.
type NewRemote struct {
	As    string `json:"as"`              // the gh account that creates it and pushes to it
	Owner string `json:"owner,omitempty"` // a user or an organisation; default As
	Name  string `json:"name"`            // default the folder's name
}

// GitHubBase is where new repositories live; tests point it at a folder.
var GitHubBase = "https://github.com"

var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// Check fills in the defaults for repo and says what's wrong.
func (n *NewRemote) Check(repo string) error {
	n.As, n.Owner, n.Name = strings.TrimSpace(n.As), strings.TrimSpace(n.Owner), strings.TrimSpace(n.Name)
	if n.As == "" {
		return fmt.Errorf("pick the GitHub account that creates the repository")
	}
	if n.Owner == "" {
		n.Owner = n.As
	}
	if n.Name == "" {
		n.Name = filepath.Base(repo)
	}
	if !repoName.MatchString(n.Name) || n.Name == "." || n.Name == ".." {
		return fmt.Errorf("%q can't be a GitHub repository name: use letters, digits, dots, dashes and underscores", n.Name)
	}
	if !repoName.MatchString(n.Owner) {
		return fmt.Errorf("%q isn't a GitHub account or organisation", n.Owner)
	}
	return nil
}

// tokenFor is the gh token of one account, without switching gh's active one.
func tokenFor(login string) (string, error) {
	out, err := exec.Command("gh", "auth", "token", "--user", login).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("gh has no token for %s (gh auth login)", login)
	}
	return strings.TrimSpace(string(out)), nil
}

// CreateGitHubRepo creates a private repository for repo on GitHub as n.As,
// makes it origin and pushes the current branch there, so a pull request
// has a base. Pushes and pull requests for this repo then go as n.As: its
// HTTPS remote asks gh for that account's token, whichever account gh has
// active. It returns the repository's URL.
func CreateGitHubRepo(repo string, n NewRemote) (string, error) {
	if err := n.Check(repo); err != nil {
		return "", err
	}
	if HasOrigin(repo) {
		return "", fmt.Errorf("%s already has a remote: %s", repo, Origin(repo))
	}
	token, err := tokenFor(n.As)
	if err != nil {
		return "", err
	}
	full := n.Owner + "/" + n.Name
	cmd := exec.Command("gh", "repo", "create", full, "--private")
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GH_TOKEN="+token)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gh repo create %s: %v: %s", full, err, strings.TrimSpace(errb.String()))
	}
	url := GitHubBase + "/" + full
	for _, args := range [][]string{
		{"remote", "add", "origin", url + ".git"},
		{"config", "factory.ghuser", n.As},
		// only this repo's GitHub remote: the account's token from gh
		{"config", "credential." + url + ".git.helper", ""},
		{"config", "--add", "credential." + url + ".git.helper", "!f() { test \"$1\" = get && echo username=" + n.As + " && echo password=$(gh auth token --user " + n.As + "); }; f"},
	} {
		if _, err := git(repo, args...); err != nil {
			return url, err
		}
	}
	if _, err := git(repo, "push", "-q", "-u", "origin", "HEAD"); err != nil {
		return url, fmt.Errorf("created %s, but the first push failed: %w", url, err)
	}
	return url, nil
}

// ghEnv is the environment for gh in a repository: the token of the account
// the factory created its remote with, when it did.
func ghEnv(dir string) []string {
	env := os.Environ()
	out, err := git(dir, "config", "--get", "factory.ghuser")
	if err != nil || strings.TrimSpace(out) == "" {
		return env
	}
	if t, err := tokenFor(strings.TrimSpace(out)); err == nil {
		env = append(env, "GH_TOKEN="+t)
	}
	return env
}

// BoardCheckout is a read-only checkout of origin's default branch, fresh
// from a fetch, for agents that read the code without changing it (the
// product agent). It lives in .factory/board and is reset every time. With
// no origin it returns the repo itself. It also returns what it checked out.
func BoardCheckout(repo string) (path, from string) {
	base := FreshBase(repo)
	if base == "" {
		return repo, ""
	}
	path = filepath.Join(repo, ".factory", "board")
	if err := ensureIgnored(repo); err != nil {
		return repo, ""
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		if _, err := git(path, "checkout", "-q", "--detach", "--force", base); err == nil {
			git(path, "clean", "-fdq")
			return path, base
		}
	}
	os.RemoveAll(path)
	git(repo, "worktree", "prune")
	if _, err := git(repo, "worktree", "add", "-q", "--detach", path, base); err != nil {
		return repo, ""
	}
	return path, base
}

// PRState asks GitHub what became of a pull request: MERGED, CLOSED, or
// OPEN with whether it can merge ("OPEN, conflicts with the base"). It is
// "" when gh can't tell.
func PRState(repo, url string) string {
	cmd := exec.Command("gh", "pr", "view", url, "--json", "state,mergeable")
	cmd.Dir, cmd.Env = repo, ghEnv(repo)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var st struct{ State, Mergeable string }
	if json.Unmarshal(out, &st) != nil {
		return ""
	}
	switch {
	case st.State != "OPEN":
		return st.State
	case st.Mergeable == "CONFLICTING":
		return "OPEN, conflicts with the base branch"
	case st.Mergeable == "MERGEABLE":
		return "OPEN, can merge"
	}
	return "OPEN"
}
