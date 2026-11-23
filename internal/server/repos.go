package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"factory/internal/config"
	"factory/internal/workspace"
)

// Picking the repository for a ticket: no repository is carried over from
// an earlier job. The engine suggests the ones the ticket's project ran in,
// checks the pick against the project, makes a git repository in a folder
// that has none, and lists the GitHub accounts a new remote can be made
// with.

func (s *Server) repoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/repos/suggest", s.suggestRepos)
	mux.HandleFunc("POST /api/repos/init", s.initRepo)
	mux.HandleFunc("GET /api/repos/info", s.repoInfo)
	mux.HandleFunc("GET /api/github/accounts", s.githubAccounts)
}

// ticketProject is the project part of an issue ID: MR for MR-3.
func ticketProject(issue string) string {
	p, _, ok := strings.Cut(strings.TrimSpace(issue), "-")
	if !ok {
		return ""
	}
	return strings.ToUpper(p)
}

// projectRepos counts, per repository, the jobs that ran tickets of each
// project.
func (s *Server) projectRepos() map[string]map[string]int {
	out := map[string]map[string]int{} // project -> repo -> jobs
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		p := ticketProject(j.Issue)
		if p == "" || j.Repo == "" {
			continue
		}
		if out[p] == nil {
			out[p] = map[string]int{}
		}
		out[p][j.Repo]++
	}
	return out
}

func (s *Server) repoLabel(repo string) string {
	if names, _ := s.Store.Projects(); names[repo] != "" {
		return names[repo]
	}
	return filepath.Base(repo)
}

// repoMismatch says why repo looks wrong for the ticket, "" when it looks
// right. hard means factory.yml rules it out; otherwise you may confirm it.
func (s *Server) repoMismatch(issue, repo string, proj config.Project) (msg string, hard bool) {
	p := ticketProject(issue)
	if p == "" {
		return "", false
	}
	tr := proj.Tracker.Normalised()
	if strings.EqualFold(tr.Kind, "youtrack") && tr.Project != "" {
		if !strings.EqualFold(tr.Project, p) {
			return fmt.Sprintf("%s is a %s ticket, but %s's factory.yml maps it to the %s project. Pick the repository %s tickets belong in.", issue, p, s.repoLabel(repo), tr.Project, p), true
		}
		return "", false
	}
	all := s.projectRepos()
	if all[p][repo] > 0 {
		return "", false // this project ran here before
	}
	var elsewhere []string
	for r := range all[p] {
		elsewhere = append(elsewhere, s.repoLabel(r))
	}
	sort.Strings(elsewhere)
	if len(elsewhere) > 0 {
		return fmt.Sprintf("%s tickets ran in %s before, never in %s. Start %s in %s anyway?", p, strings.Join(elsewhere, ", "), s.repoLabel(repo), issue, s.repoLabel(repo)), false
	}
	var here []string
	for q, repos := range all {
		if repos[repo] > 0 {
			here = append(here, q)
		}
	}
	sort.Strings(here)
	if len(here) > 0 {
		return fmt.Sprintf("%s has run %s tickets, never %s ones. Is it the right repository for %s?", s.repoLabel(repo), strings.Join(here, ", "), p, issue), false
	}
	return "", false
}

// suggestRepos lists the repositories a ticket's project ran in, most jobs
// first. They are suggestions: the form never picks one for you.
func (s *Server) suggestRepos(w http.ResponseWriter, r *http.Request) {
	p := ticketProject(r.URL.Query().Get("issue"))
	type suggestion struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Jobs int    `json:"jobs"`
	}
	out := []suggestion{}
	for repo, n := range s.projectRepos()[p] {
		if isRepo(repo) {
			out = append(out, suggestion{repo, s.repoLabel(repo), n})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].Jobs > out[b].Jobs || out[a].Jobs == out[b].Jobs && out[a].Name < out[b].Name
	})
	writeJSON(w, map[string]any{"project": p, "repos": out})
}

// repoInfo says what the form needs about a folder: whether it is a
// repository, and whether it has a remote to ship to.
func (s *Server) repoInfo(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	p := filepath.Clean(expandHome(strings.TrimSpace(r.URL.Query().Get("path")), home))
	info := map[string]any{"path": p, "exists": false, "repo": false}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		info["exists"] = true
		if root, err := repoRoot(p); err == nil {
			info["repo"], info["root"], info["name"], info["origin"] = true, root, s.repoLabel(root), workspace.Origin(root)
		}
	}
	writeJSON(w, info)
}

// initRepo makes the folder a git repository, creating it when needed.
func (s *Server) initRepo(w http.ResponseWriter, r *http.Request) {
	var b struct{ Path string }
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || strings.TrimSpace(b.Path) == "" {
		http.Error(w, "give the folder", http.StatusBadRequest)
		return
	}
	home, _ := os.UserHomeDir()
	p := expandHome(strings.TrimSpace(b.Path), home)
	if !filepath.IsAbs(p) {
		http.Error(w, "give a full path, starting with / or ~", http.StatusBadRequest)
		return
	}
	p = filepath.Clean(p)
	if p == home || p == "/" {
		http.Error(w, "pick a project folder, not "+p, http.StatusBadRequest)
		return
	}
	if err := workspace.InitRepo(p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"path": p, "repo": true})
}

func (s *Server) githubAccounts(w http.ResponseWriter, r *http.Request) {
	list, err := workspace.GitHubAccounts()
	if err != nil {
		writeJSON(w, map[string]any{"accounts": []workspace.Account{}, "error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"accounts": list})
}

func hasAccount(login string) bool {
	list, _ := workspace.GitHubAccounts()
	for _, a := range list {
		if strings.EqualFold(a.Login, login) {
			return true
		}
	}
	return false
}
