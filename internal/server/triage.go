package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"factory/internal/config"
	"factory/internal/job"
	"factory/internal/triage"
)

// Triage runs: the product agent (Pericles) goes over one repo's board,
// asks owners, fixes tickets and starts jobs. The engine starts the agent,
// holds the limits and starts the jobs it asks for (D2).

func (s *Server) triageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/triage", s.startTriage)
	mux.HandleFunc("GET /api/triage", s.listTriage)
	mux.HandleFunc("GET /api/triage/{run}", s.getTriage)
	mux.HandleFunc("POST /api/triage/{run}/stop", s.stopTriage)
}

// triageOf finds a run from the "job" id its agent's tool URL carries
// ("triage-<id>").
func (s *Server) triageOf(scope string) *triage.Run {
	id, ok := strings.CutPrefix(scope, "triage-")
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.triage[id]
}

// triageTool runs a tool for a triage run's agent: the tracker tools go
// through the operations service, the rest are the run's own.
func (s *Server) triageTool(run *triage.Run, agent, tool string, args []byte) (string, error) {
	if text, handled, err := s.opsTool(run.Scope(), agent, tool, args); handled {
		return text, err
	}
	if triage.Tools[tool] {
		return run.Tool(agent, tool, args)
	}
	if tool == "set_status" {
		var b struct{ Step string }
		json.Unmarshal(args, &b)
		s.Bus.Emit(run.Scope(), agent, "agent.status", map[string]any{"step": b.Step})
		return "ok", nil
	}
	return "", fmt.Errorf("the product agent has no tool %q", tool)
}

// liveJobs lists the jobs in repo that are still going.
func (s *Server) liveJobs(repo string) []triage.LiveJob {
	s.mu.Lock()
	list := make([]*job.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if j.Repo == repo {
			list = append(list, j)
		}
	}
	s.mu.Unlock()
	var out []triage.LiveJob
	for _, j := range list {
		state, _, _, _ := j.Shipped()
		switch state {
		case "done", "cancelled", "failed":
			continue
		}
		out = append(out, triage.LiveJob{ID: j.ID, Issue: j.Issue, State: state})
	}
	return out
}

func (s *Server) startTriage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repo    string  `json:"repo"`
		Project string  `json:"project"`  // the YouTrack project, when factory.yml has no tracker block
		Level   string  `json:"level"`    // observe, propose, file or fix; default the tracker's level
		MaxJobs *int    `json:"max_jobs"` // jobs it may start; default 2, 0 only recommends
		Minutes float64 `json:"minutes"`  // how long it may take; default 30
		Focus   string  `json:"focus"`    // what to look at first, in your words
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Repo == "" {
		http.Error(w, "need a repo", http.StatusBadRequest)
		return
	}
	if s.Ops == nil {
		http.Error(w, "the operations service is not running", http.StatusServiceUnavailable)
		return
	}
	repo, err := repoRoot(b.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	proj, err := config.LoadProject(repo)
	if err != nil {
		http.Error(w, "factory.yml: "+err.Error(), http.StatusBadRequest)
		return
	}
	tr := proj.Tracker.Normalised()
	project := strings.ToUpper(strings.TrimSpace(b.Project))
	if strings.EqualFold(tr.Kind, "youtrack") && tr.Project != "" {
		if project != "" && !strings.EqualFold(project, tr.Project) {
			http.Error(w, fmt.Sprintf("factory.yml maps this repo to %s, not %s", tr.Project, project), http.StatusBadRequest)
			return
		}
		project = tr.Project
	}
	if project == "" {
		http.Error(w, "which YouTrack project is this repo's board? Pass project, or add a tracker block to factory.yml", http.StatusBadRequest)
		return
	}
	level := tr.Level
	if b.Level != "" {
		if level = config.ValidLevel(b.Level); level == "" {
			http.Error(w, "level is observe, propose, file or fix", http.StatusBadRequest)
			return
		}
	}
	maxJobs := 2
	if b.MaxJobs != nil {
		maxJobs = *b.MaxJobs
	}
	if maxJobs < 0 || maxJobs > 10 {
		http.Error(w, "max_jobs is 0 to 10", http.StatusBadRequest)
		return
	}
	settings := config.LoadEngine()
	role := settings.RoleForProject(repo, "product")
	if p := config.CheckRole(role); p != "" {
		http.Error(w, "product role: "+p, http.StatusBadRequest)
		return
	}
	rt := s.Runtimes[role.Runtime]
	if rt == nil {
		http.Error(w, "no runtime "+role.Runtime, http.StatusBadRequest)
		return
	}

	id := fmt.Sprintf("T%d", time.Now().UnixMilli())
	run := &triage.Run{ID: id, Repo: repo, Project: project, Level: level, MaxJobs: maxJobs, Focus: strings.TrimSpace(b.Focus),
		Time: time.Duration(b.Minutes * float64(time.Minute)), RT: rt, Model: role.Model, Ops: s.Ops, Bus: s.Bus,
		Dir:  filepath.Join(config.Home(), "triage", id),
		Live: func() []triage.LiveJob { return s.liveJobs(repo) },
		Start: func(issue, notes string) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			jobID, _, err := s.launch(ctx, job.Spec{Issue: issue, Repo: repo,
				Notes: "Picked by the product agent (Pericles) in triage run " + id + ".\n" + notes})
			return jobID, err
		}}
	s.mu.Lock()
	if s.triage == nil {
		s.triage = map[string]*triage.Run{}
	}
	for _, other := range s.triage {
		if other.Repo == repo && other.StateNow() == triage.Running {
			s.mu.Unlock()
			http.Error(w, "triage run "+other.ID+" is already going over this board", http.StatusConflict)
			return
		}
	}
	s.triage[id] = run
	s.mu.Unlock()
	s.Bus.Place(run.Scope(), run.Dir)
	s.Ops.RegisterBoard(run.Scope(), repo, project, level, triage.Pericles)
	go func() {
		defer s.Ops.Close(run.Scope())
		run.Execute(context.Background())
	}()
	writeJSON(w, map[string]any{"id": id, "project": project, "level": level, "max_jobs": maxJobs})
}

func (s *Server) triageRun(w http.ResponseWriter, id string) *triage.Run {
	s.mu.Lock()
	run := s.triage[id]
	s.mu.Unlock()
	if run == nil {
		http.Error(w, "no triage run "+id, http.StatusNotFound)
	}
	return run
}

// getTriage shows a run: live while it runs, from its run.json after a restart.
func (s *Server) getTriage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("run")
	s.mu.Lock()
	run := s.triage[id]
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if run != nil {
		w.Write(run.Snapshot())
		return
	}
	if strings.ContainsAny(id, `/\.`) {
		http.Error(w, "no triage run "+id, http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(filepath.Join(config.Home(), "triage", id, "run.json"))
	if err != nil {
		http.Error(w, "no triage run "+id, http.StatusNotFound)
		return
	}
	w.Write(data)
}

// listTriage lists the runs this server has started, newest first.
func (s *Server) listTriage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	list := make([]json.RawMessage, 0, len(s.triage))
	ids := make([]string, 0, len(s.triage))
	for id := range s.triage {
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	runs := make([]*triage.Run, 0, len(ids))
	for _, id := range ids {
		runs = append(runs, s.triage[id])
	}
	s.mu.Unlock()
	for _, run := range runs {
		list = append(list, run.Snapshot())
	}
	writeJSON(w, map[string]any{"runs": list})
}

func (s *Server) stopTriage(w http.ResponseWriter, r *http.Request) {
	if run := s.triageRun(w, r.PathValue("run")); run != nil {
		run.Stop()
		writeJSON(w, map[string]any{"stopped": run.ID})
	}
}
