package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"factory/internal/config"
	"factory/internal/job"
	"factory/internal/triage"
	"factory/internal/workspace"
	"factory/internal/youtrack"
)

// Triage runs: the product agent (Pericles) goes over one repo's board,
// asks owners, fixes tickets and starts jobs. The engine starts the agent,
// holds the limits and starts the jobs it asks for (D2).

func (s *Server) triageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/triage", s.startTriage)
	mux.HandleFunc("GET /api/triage", s.listTriage)
	mux.HandleFunc("GET /api/triage/boards", s.boards)
	mux.HandleFunc("PUT /api/triage/auto", s.putAuto)
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

// pastJobs lists the jobs that ran in repo, newest first, with what
// became of their pull requests.
func (s *Server) pastJobs(repo string) []triage.PastJob {
	s.mu.Lock()
	list := make([]*job.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if j.Repo == repo {
			list = append(list, j)
		}
	}
	s.mu.Unlock()
	sort.Slice(list, func(a, b int) bool { return list[a].Started.After(list[b].Started) })
	if len(list) > 25 {
		list = list[:25]
	}
	out := make([]triage.PastJob, len(list))
	var wg sync.WaitGroup
	for i, j := range list {
		state, pr, _, _ := j.Shipped()
		out[i] = triage.PastJob{ID: j.ID, Issue: j.Issue, State: state, PR: pr}
		if pr != "" {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				out[i].PRState = workspace.PRState(repo, out[i].PR)
			}(i)
		}
	}
	wg.Wait()
	return out
}

// triageOpts is what a run is started with, from the Board panel or the
// automatic schedule.
type triageOpts struct {
	Repo    string  `json:"repo"`
	Project string  `json:"project"`  // the YouTrack project, when factory.yml has no tracker block
	Level   string  `json:"level"`    // observe, propose, file or fix; default the tracker's level
	MaxJobs *int    `json:"max_jobs"` // jobs it may start; default 2, 0 only recommends
	Minutes float64 `json:"minutes"`  // how long it may take; default 30
	Focus   string  `json:"focus"`    // what to look at first, in your words
	auto    bool
	why     string
}

func (s *Server) startTriage(w http.ResponseWriter, r *http.Request) {
	var b triageOpts
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Repo == "" {
		http.Error(w, "pick a repository", http.StatusBadRequest)
		return
	}
	b.why = "you started it"
	run, code, err := s.beginTriage(b)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]any{"id": run.ID, "project": run.Project, "level": run.Level, "max_jobs": run.MaxJobs})
}

// boardProject is the YouTrack project for repo: factory.yml's tracker
// block, else the one given. It says why when there is none.
func boardProject(repo, given string) (config.Tracker, string, error) {
	proj, err := config.LoadProject(repo)
	if err != nil {
		return config.Tracker{}, "", fmt.Errorf("factory.yml: %w", err)
	}
	tr := proj.Tracker.Normalised()
	project := strings.ToUpper(strings.TrimSpace(given))
	if strings.EqualFold(tr.Kind, "youtrack") && tr.Project != "" {
		if project != "" && !strings.EqualFold(project, tr.Project) {
			return tr, "", fmt.Errorf("factory.yml maps this repo to %s, not %s", tr.Project, project)
		}
		project = tr.Project
	}
	if project == "" {
		return tr, "", fmt.Errorf("which YouTrack project is this repo's board? Give its short name, like HF")
	}
	return tr, project, nil
}

// beginTriage checks the options and starts a run in the background.
func (s *Server) beginTriage(b triageOpts) (*triage.Run, int, error) {
	if s.Ops == nil {
		return nil, http.StatusServiceUnavailable, fmt.Errorf("the operations service is not running")
	}
	repo, err := repoRoot(b.Repo)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	_, project, err := boardProject(repo, b.Project)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	// he runs the board: comments, new tickets, fields and states. The API
	// still takes a lower level for anyone who wants one.
	level := "fix"
	if b.Level != "" {
		if level = config.ValidLevel(b.Level); level == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("level is observe, propose, file or fix")
		}
	}
	maxJobs := 2
	if b.MaxJobs != nil {
		maxJobs = *b.MaxJobs
	}
	if maxJobs < 0 || maxJobs > 10 {
		return nil, http.StatusBadRequest, fmt.Errorf("it may start 0 to 10 jobs")
	}
	settings := config.LoadEngine()
	if !(youtrack.Client{URL: settings.YouTrack.URL, Token: settings.YouTrack.Token}).Configured() {
		return nil, http.StatusBadRequest, fmt.Errorf("YouTrack is not set up yet (Admin → Connections)")
	}
	role := settings.RoleForProject(repo, "product")
	if p := config.CheckRole(role); p != "" {
		return nil, http.StatusBadRequest, fmt.Errorf("product role: %s", p)
	}
	rt := s.Runtimes[role.Runtime]
	if rt == nil {
		return nil, http.StatusBadRequest, fmt.Errorf("no runtime %s", role.Runtime)
	}

	id := fmt.Sprintf("T%d", time.Now().UnixMilli())
	run := &triage.Run{ID: id, Repo: repo, Project: project, Level: level, MaxJobs: maxJobs, Focus: strings.TrimSpace(b.Focus), Auto: b.auto, Why: b.why,
		Time: time.Duration(b.Minutes * float64(time.Minute)), RT: rt, Model: role.Model, Ops: s.Ops, Bus: s.Bus,
		Dir:    filepath.Join(config.Home(), "triage", id),
		Live:   func() []triage.LiveJob { return s.liveJobs(repo) },
		Recent: func() []triage.PastJob { return s.pastJobs(repo) },
		Board: func(ctx context.Context) ([]youtrack.Card, bool, error) {
			return youtrack.Client{URL: settings.YouTrack.URL, Token: settings.YouTrack.Token}.Board(ctx, project, 100)
		},
		Start: func(issue, notes string, testsAllowed bool) (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			jobID, _, err := s.launch(ctx, job.Spec{Issue: issue, Repo: repo, TestsAllowed: testsAllowed,
				Notes: "Picked by the product agent (Pericles) in triage run " + id + ".\n" + notes})
			return jobID, err
		}}
	// he reads the code as it is merged now, not the main checkout
	run.Cwd, run.Base = workspace.BoardCheckout(repo)
	s.mu.Lock()
	if s.triage == nil {
		s.triage = map[string]*triage.Run{}
	}
	for _, other := range s.triage {
		if other.Repo == repo && other.StateNow() == triage.Running {
			s.mu.Unlock()
			return nil, http.StatusConflict, fmt.Errorf("Pericles is already going over this board (run %s)", other.ID)
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
	return run, http.StatusOK, nil
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

// getTriage shows one run, live while it runs.
func (s *Server) getTriage(w http.ResponseWriter, r *http.Request) {
	if run := s.triageRun(w, r.PathValue("run")); run != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write(run.Snapshot())
	}
}

// listTriage lists the runs, newest first; ?repo= keeps one repo's.
func (s *Server) listTriage(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo != "" {
		if root, err := repoRoot(repo); err == nil {
			repo = root
		}
	}
	list := []json.RawMessage{}
	for _, run := range s.runsFor(repo) {
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
