package server

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// The New task form picks its repository by browsing folders on this
// machine, the way a file dialog does. The engine listens on 127.0.0.1 only,
// so this shows your own disk to you.

type folder struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Repo bool   `json:"repo"` // a git repository the factory can work in
}

type recentRepo struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// browse lists the folders inside ?path= (your home folder when empty), with
// the repositories earlier jobs used.
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	dir := expandHome(strings.TrimSpace(r.URL.Query().Get("path")), home)
	if dir == "" {
		dir = home
	}
	if !filepath.IsAbs(dir) {
		http.Error(w, "give a full path, starting with / or ~", http.StatusBadRequest)
		return
	}
	dir = filepath.Clean(dir)
	list, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	folders := []folder{}
	for _, e := range list {
		if !hidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 { // follow links to folders
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				continue
			}
		} else if !e.IsDir() {
			continue
		}
		folders = append(folders, folder{Name: e.Name(), Path: p, Repo: isRepo(p)})
	}
	sort.Slice(folders, func(a, b int) bool { return strings.ToLower(folders[a].Name) < strings.ToLower(folders[b].Name) })
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	names, _ := s.Store.Projects()
	writeJSON(w, map[string]any{"path": dir, "parent": parent, "home": home, "repo": isRepo(dir),
		"name": names[dir], "folders": folders, "recent": s.recentRepos(names)})
}

// recentRepos are the repositories earlier jobs worked in, newest first,
// that still exist.
func (s *Server) recentRepos(names map[string]string) []recentRepo {
	out := []recentRepo{}
	seen := map[string]bool{}
	rows, _ := s.Store.Jobs()
	for i := len(rows) - 1; i >= 0; i-- {
		p := rows[i].Repo
		if p == "" || seen[p] || !isRepo(p) {
			continue
		}
		seen[p] = true
		name := names[p]
		if name == "" {
			name = filepath.Base(p)
		}
		out = append(out, recentRepo{p, name})
	}
	return out
}

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// repoRoot turns what you picked into the repository's top folder, so a job
// started from a subfolder still works on the whole repo.
func repoRoot(p string) (string, error) {
	home, _ := os.UserHomeDir()
	p = expandHome(strings.TrimSpace(p), home)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%q isn't a full path; pick the repository in the folder browser", p)
	}
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return "", fmt.Errorf("there's no folder at %s", p)
	}
	out, err := exec.Command("git", "-C", p, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s isn't a git repository; the factory works in git repos", p)
	}
	return strings.TrimSpace(string(out)), nil
}
