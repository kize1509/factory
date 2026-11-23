package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH puts a gh on PATH with two accounts (alice active, bob), whose
// "repo create" makes a bare repository under base.
func fakeGH(t *testing.T, base string) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$1 $2" in
"auth status") echo '{"hosts":{"github.com":[{"state":"success","active":true,"login":"alice"},{"state":"success","active":false,"login":"bob"}]}}' ;;
"auth token") [ "$4" = alice ] || [ "$4" = bob ] && echo "tok-$4" || exit 1 ;;
"repo create") [ -n "$GH_TOKEN" ] || exit 3; mkdir -p "` + base + `/$3.git" && git init -q --bare "` + base + `/$3.git" ;;
*) exit 2 ;;
esac
`
	os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInitRepoCommitsWhatIsThere(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	dir := filepath.Join(t.TempDir(), "new-project")
	if err := InitRepo(dir); err != nil { // a folder that doesn't exist yet
		t.Fatal(err)
	}
	if gitT(t, dir, "branch", "--show-current") != "main" || gitT(t, dir, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("want one commit on main")
	}
	if !strings.Contains(gitT(t, dir, "show", "HEAD:.gitignore"), ".factory/") {
		t.Fatal(".factory/ should be ignored")
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "notes.md"), []byte("hi\n"), 0o644)
	if err := InitRepo(other); err != nil {
		t.Fatal(err)
	}
	if gitT(t, other, "show", "HEAD:notes.md") != "hi" {
		t.Fatal("files already there go in the first commit")
	}
	if err := InitRepo(filepath.Join(other, "sub")); err == nil {
		t.Fatal("a folder inside a repository must be refused")
	}
}

func TestCreateGitHubRepoPushesAsTheAccount(t *testing.T) {
	base := t.TempDir()
	fakeGH(t, base)
	old := GitHubBase
	GitHubBase = "file://" + base
	t.Cleanup(func() { GitHubBase = old })

	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	if accts, err := GitHubAccounts(); err != nil || len(accts) != 2 || !accts[0].Active || accts[0].Login != "alice" {
		t.Fatalf("%v %v", accts, err)
	}
	n := NewRemote{As: "bob", Name: "my-tool"}
	url, err := CreateGitHubRepo(repo, n)
	if err != nil {
		t.Fatal(err)
	}
	if url != GitHubBase+"/bob/my-tool" || Origin(repo) != url+".git" {
		t.Fatalf("url %s origin %s", url, Origin(repo))
	}
	if gitT(t, repo, "config", "factory.ghuser") != "bob" {
		t.Fatal("the repo should remember the account")
	}
	if got := gitT(t, base+"/bob/my-tool.git", "rev-parse", "main"); got != gitT(t, repo, "rev-parse", "HEAD") {
		t.Fatal("main should be pushed, so a PR has a base")
	}
	if _, err := CreateGitHubRepo(repo, n); err == nil {
		t.Fatal("a repo with a remote gets no second one")
	}
	if err := (&NewRemote{As: "bob", Name: "bad name"}).Check(repo); err == nil {
		t.Fatal("spaces aren't allowed in a repository name")
	}
}
