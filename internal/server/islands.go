package server

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"factory/internal/aegean"
	"factory/internal/job"
	"factory/internal/workspace"
)

// Every job owns one cell of the Aegean map's lattice, chosen when it starts
// and stored with it (see package aegean). The caller holds s.mu.

// placed lists the jobs that have a cell, oldest first.
func (s *Server) placed(skip *job.Job) ([]*job.Job, []aegean.Placed) {
	var list []*job.Job
	for _, j := range s.jobs {
		if j != skip && j.Island >= 0 {
			list = append(list, j)
		}
	}
	sort.Slice(list, func(a, b int) bool {
		if !list[a].Started.Equal(list[b].Started) {
			return list[a].Started.Before(list[b].Started)
		}
		return list[a].ID < list[b].ID
	})
	out := make([]aegean.Placed, len(list))
	for i, j := range list {
		out[i] = aegean.Placed{Project: j.Repo, Cell: j.Island}
	}
	return list, out
}

// claimIsland gives a new job its cell and stores it.
func (s *Server) claimIsland(j *job.Job) {
	_, placed := s.placed(j)
	j.Island = aegean.Claim(placed, j.Repo)
	s.Store.SetIsland(j.ID, j.Island)
}

// placeIslands gives every job that has none a cell, in the order the jobs
// started: old jobs from before the lattice, once. Cells already stored stay.
func (s *Server) placeIslands() {
	var todo []*job.Job
	for _, j := range s.jobs {
		if j.Island < 0 || j.Island >= aegean.Cells {
			j.Island = -1
			todo = append(todo, j)
		}
	}
	sort.Slice(todo, func(a, b int) bool {
		if !todo[a].Started.Equal(todo[b].Started) {
			return todo[a].Started.Before(todo[b].Started)
		}
		return todo[a].ID < todo[b].ID
	})
	for _, j := range todo {
		// a job that started before the others needs its place among the older jobs only
		s.claimIsland(j)
	}
}

// deletable says which jobs may be deleted: the ones that aren't running.
func deletable(state string) bool {
	switch state {
	case "done", "failed", "interrupted", "cancelled":
		return true
	}
	return false
}

// deleteJob removes a finished, failed, interrupted or stopped job: its row,
// events (and with them its messages and costs), log folder, and the
// worktree and branch it left behind. Work that shipped stays: a branch with
// a pull request is kept, and one with commits only goes if it is merged.
// The rest of its country then closes up around the gap.
func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	s.mu.Lock()
	state, pr, commit, branch := j.Shipped()
	if !deletable(state) {
		s.mu.Unlock()
		http.Error(w, j.ID+" is "+state+"; stop it first, then delete it", http.StatusConflict)
		return
	}
	if err := s.Store.DeleteJob(j.ID); err != nil {
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	delete(s.jobs, j.ID)
	moved := s.closeUp(j)
	s.mu.Unlock()

	s.Bus.Forget(j.ID)
	os.RemoveAll(j.Dir())
	if branch == "" {
		branch = "factory/" + j.ID
	}
	wt := &workspace.Worktree{Repo: j.Repo, Path: filepath.Join(j.Repo, ".factory", "worktrees", j.ID), Branch: branch}
	wt.Discard(pr != "", commit != "")
	if matches, _ := filepath.Glob(filepath.Join(j.Repo, ".factory", "worktrees", j.ID+"-exp-*")); len(matches) > 0 {
		for _, m := range matches { // the Symposium's experiment worktrees
			(&workspace.Worktree{Repo: j.Repo, Path: m}).Discard(true, false)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "moved": moved})
}

// closeUp lets the deleted job's country close the gap and stores the move.
// It returns the id of the job whose island moved, if any.
func (s *Server) closeUp(gone *job.Job) string {
	list, placed := s.placed(nil)
	i, cell := aegean.CloseUp(placed, gone.Repo, gone.Island)
	if i < 0 {
		return ""
	}
	list[i].Island = cell
	s.Store.SetIsland(list[i].ID, cell)
	return list[i].ID
}
