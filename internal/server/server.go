// Package server is the engine's HTTP face: the job API, the event stream,
// the factory tools agents call, the metering gateway and the web files.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/gateway"
	"factory/internal/handoff"
	"factory/internal/job"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/youtrack"
)

type Server struct {
	Bus      *events.Bus
	Gateway  *gateway.Gateway
	Runtimes map[string]runtime.Runtime // pi, claude-code, codex
	Web      string                     // folder served at /
	Model    string                     // default model
	Root     string                     // repo root of the factory itself (for pi-host)
	Started  time.Time
	Store    *store.Store

	mu   sync.Mutex
	jobs map[string]*job.Job
	n    int
}

func (s *Server) Handler() http.Handler {
	if s.jobs == nil {
		s.jobs = map[string]*job.Job{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/jobs", s.startJob)
	mux.HandleFunc("GET /api/jobs", s.listJobs)
	mux.HandleFunc("POST /api/jobs/{job}/continue", s.continueJob)
	mux.HandleFunc("POST /api/jobs/{job}/cancel", s.cancelJob)
	mux.HandleFunc("GET /api/jobs/{job}/context", s.jobContext)
	mux.HandleFunc("GET /api/projects", s.projects)
	mux.HandleFunc("GET /api/fs", s.browse)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/agents/{job}/{agent}/message", s.message)
	mux.HandleFunc("POST /api/agents/{job}/{agent}/stop", s.stopAgent)
	mux.HandleFunc("POST /api/agents/{job}/{agent}/pause", s.pause)
	mux.HandleFunc("POST /api/agents/{job}/{agent}/resume", s.resume)
	mux.HandleFunc("POST /api/tools/{job}/{agent}/{tool}", s.tool)
	s.adminRoutes(mux)
	mux.HandleFunc("POST /mcp/{job}/{agent}", s.mcp)
	mux.Handle("/llm/", s.Gateway)
	mux.HandleFunc("GET /app", s.app)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(s.Web, "assets", "icon", "owl.idle.png"))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/app", http.StatusFound) })
	mux.Handle("/", http.FileServer(http.Dir(s.Web)))
	return mux
}

func (s *Server) job(w http.ResponseWriter, id string) *job.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		http.Error(w, "no job "+id, http.StatusNotFound)
	}
	return j
}

func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var spec job.Spec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec.Issue, spec.Notes = strings.TrimSpace(spec.Issue), strings.TrimSpace(spec.Notes)
	ticketShip := "" // the ticket's Ship mode, read fresh for every job
	if spec.Issue != "" && spec.Prompt == "" {
		// a ticket ID: load it from YouTrack, add your notes
		yt := config.LoadEngine().YouTrack
		client := youtrack.Client{URL: yt.URL, Token: yt.Token}
		switch {
		case client.Configured():
			is, err := client.Issue(r.Context(), spec.Issue)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			spec.Title = is.Summary
			ticketShip = is.ShipMode()
			spec.Prompt = is.Summary + "\n\n" + strings.TrimSpace(is.Description)
			if spec.Notes != "" {
				spec.Prompt += "\n\nNotes from the person who started this job:\n" + spec.Notes
			}
		case spec.Notes != "":
			spec.Prompt = spec.Notes // no YouTrack yet: the notes are the task
		default:
			http.Error(w, "YouTrack is not set up yet (Admin → Connections). Until it is, describe the task in the notes.", http.StatusBadRequest)
			return
		}
		if spec.Title == "" {
			spec.Title = firstLine(spec.Notes, 70)
		}
		if spec.ID == "" {
			spec.ID = spec.Issue
		}
	}
	if spec.Repo == "" || spec.Prompt == "" {
		http.Error(w, "a repository and an issue ID (or a task description) are required", http.StatusBadRequest)
		return
	}
	root, err := repoRoot(spec.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec.Repo = root
	proj, err := config.LoadProject(spec.Repo)
	if err != nil {
		http.Error(w, "factory.yml: "+err.Error(), http.StatusBadRequest)
		return
	}
	if spec.Ship = config.ShipMode(ticketShip, proj.Ship); spec.Ship == "" {
		http.Error(w, fmt.Sprintf("unknown ship mode (ticket %q, factory.yml %q); use commit, push or pr", ticketShip, proj.Ship), http.StatusBadRequest)
		return
	}
	if len(spec.Checks) == 0 && strings.TrimSpace(spec.TestNotes) == "" {
		spec.Checks = proj.Checks.SavedPlan // D23: use the saved plan when there is one
	}
	if spec.MaxLoops == 0 {
		spec.MaxLoops = proj.Limits.BuildLoops
	}
	// the admin panel's choice for builders, unless the request says otherwise
	role := config.LoadEngine().RoleFor("builder")
	if spec.Runtime == "" {
		spec.Runtime = role.Runtime
	}
	if spec.Model == "" {
		spec.Model = role.Model
	}
	if p := config.CheckRole(config.Role{Runtime: spec.Runtime, Model: spec.Model}); p != "" {
		http.Error(w, p, http.StatusBadRequest)
		return
	}
	rt := s.Runtimes[spec.Runtime]
	settings := config.LoadEngine()
	if spec.Skills == nil {
		spec.Skills = settings.SkillsFor("builder")
	}
	rev := settings.RoleFor("reviewer") // the reviewer grills the builder after the checks pass
	if spec.ReviewerRuntime == "" {
		spec.ReviewerRuntime, spec.ReviewerModel = rev.Runtime, rev.Model
	}
	if spec.ReviewerSkills == nil {
		spec.ReviewerSkills = settings.SkillsFor("reviewer")
	}
	if spec.Roles == nil { // the other roles a job may need, from Admin → Roles and Skills
		spec.Roles = map[string]job.RoleSpec{}
		for _, name := range []string{"planner", "reviewer"} {
			r := settings.RoleFor(name)
			spec.Roles[name] = job.RoleSpec{Runtime: r.Runtime, Model: r.Model, Skills: settings.SkillsFor(name)}
		}
		spec.Roles["reviewer"] = job.RoleSpec{Runtime: spec.ReviewerRuntime, Model: spec.ReviewerModel, Skills: spec.ReviewerSkills}
	}
	s.mu.Lock()
	s.n++
	if spec.ID == "" {
		spec.ID = fmt.Sprintf("F-%s-%d", time.Now().Format("0102"), s.n)
	}
	if s.jobs[spec.ID] != nil { // the same ticket again: number the new run
		base := spec.ID
		for n := 2; s.jobs[spec.ID] != nil; n++ {
			spec.ID = fmt.Sprintf("%s-run%d", base, n)
		}
	}
	if names, _ := s.Store.Projects(); names[spec.Repo] != "" {
		spec.Project = names[spec.Repo] // named on an earlier job; the planner names it otherwise
	}
	j := job.New(spec, s.Bus, rt)
	j.SetRuntimes(s.Runtimes)
	s.jobs[spec.ID] = j
	s.mu.Unlock()
	s.Bus.Place(j.ID, j.Dir())
	s.Store.SaveJob(j.ID, spec.Issue, spec.Title, spec.Repo, spec, j.Started)
	go j.Run(context.Background())
	writeJSON(w, map[string]any{"id": j.ID})
}

// continueJob gives a stuck job your guidance. A job still alive in the
// engine takes it at once; one stuck before a restart starts again from what
// it wrote down.
func (s *Server) continueJob(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	s.guide(w, j, body.Text)
}

func (s *Server) guide(w http.ResponseWriter, j *job.Job, text string) {
	start, err := j.Continue(text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.Bus.Emit(j.ID, "", "human.guidance", map[string]any{"text": text})
	if start {
		go j.Run(context.Background())
	}
	writeJSON(w, map[string]any{"ok": true, "continued": true})
}

// jobContext is what a stuck job wrote down about where it stopped.
func (s *Server) jobContext(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	writeJSON(w, map[string]any{"id": j.ID, "state": j.State, "phase": j.Phase, "error": j.Error, "brief": j.Brief()})
}

func (s *Server) projects(w http.ResponseWriter, _ *http.Request) {
	names, err := s.Store.Projects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, names)
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	list := make([]*job.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j)
	}
	s.mu.Unlock()
	sort.Slice(list, func(a, b int) bool { return list[a].Started.Before(list[b].Started) })
	writeJSON(w, list)
}

// events streams events as server-sent events, starting with everything
// already logged: one job's with ?job=<id>, or every job's with ?all=1. The
// app uses one stream for all jobs, because browsers allow only six open
// connections per server and a stream per job would starve its images.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("job")
	all := r.URL.Query().Get("all") == "1"
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, stop := s.Bus.Subscribe(id)
	defer stop()
	var last int64
	replay := []string{}
	if id != "" {
		replay = append(replay, id)
	} else if all {
		s.mu.Lock()
		list := make([]*job.Job, 0, len(s.jobs))
		for _, j := range s.jobs {
			list = append(list, j)
		}
		s.mu.Unlock()
		sort.Slice(list, func(a, b int) bool { return list[a].Started.Before(list[b].Started) })
		for _, j := range list {
			replay = append(replay, j.ID)
		}
	}
	for _, jid := range replay {
		past, _ := s.Bus.Replay(jid)
		for _, e := range past {
			sse(w, e)
			if e.Seq > last {
				last = e.Seq
			}
		}
	}
	fmt.Fprint(w, "event: replayed\ndata: {}\n\n") // the page knows history is in
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case e := <-ch:
			if e.Seq > last {
				sse(w, e)
				fl.Flush()
			}
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func sse(w http.ResponseWriter, e events.Event) {
	b, _ := json.Marshal(e)
	fmt.Fprintf(w, "data: %s\n\n", b)
}

func (s *Server) message(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if j.Resumable() { // a message to a stuck job is guidance to carry it on
		s.guide(w, j, body.Text)
		return
	}
	if strings.TrimSpace(body.Text) != "" && j.Waiting() && j.Answer(body.Text) { // the job was waiting on you: this is the answer
		s.Bus.Emit(j.ID, r.PathValue("agent"), "human.message", map[string]any{"text": body.Text})
		writeJSON(w, map[string]any{"ok": true, "answered": true})
		return
	}
	a := j.Agent(r.PathValue("agent"))
	if a == nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "no such agent, or empty message", http.StatusBadRequest)
		return
	}
	s.Bus.Emit(j.ID, r.PathValue("agent"), "human.message", map[string]any{"text": body.Text})
	if err := a.Steer(body.Text); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) stopAgent(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	s.Bus.Emit(j.ID, r.PathValue("agent"), "human.stop", nil)
	if err := j.Cancel(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// cancelJob ends a job for good: running, stuck or interrupted.
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	s.Bus.Emit(j.ID, "", "human.cancel", nil)
	if err := j.Cancel(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	s.Gateway.Pause(r.PathValue("job"), r.PathValue("agent"))
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.Gateway.Resume(r.PathValue("job"), r.PathValue("agent"))
	writeJSON(w, map[string]any{"ok": true})
}

// tool handles the factory tools Pi agents call over HTTP.
func (s *Server) tool(w http.ResponseWriter, r *http.Request) {
	j := s.job(w, r.PathValue("job"))
	if j == nil {
		return
	}
	body, _ := io.ReadAll(r.Body)
	text, err := s.callTool(j, r.PathValue("agent"), r.PathValue("tool"), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	fmt.Fprint(w, text)
}

// callTool runs one factory tool for an agent. The engine decides and logs
// (D2); the reply text goes back to the agent.
func (s *Server) callTool(j *job.Job, agent, tool string, args []byte) (string, error) {
	switch tool {
	case "handoff":
		var n handoff.Note
		if err := json.Unmarshal(args, &n); err != nil {
			return "", err
		}
		if p := j.SaveHandoff(agent, &n); len(p) > 0 {
			return "", fmt.Errorf("Handoff rejected: %s. Fix it and call handoff again.", strings.Join(p, "; "))
		}
		return "Handoff saved.", nil
	case "set_checks":
		var body struct {
			Commands []string `json:"commands"`
			Why      string   `json:"why"`
		}
		if err := json.Unmarshal(args, &body); err != nil {
			return "", err
		}
		if err := j.SetChecks(agent, body.Commands, body.Why); err != nil {
			return "", err
		}
		return "Checks saved. The engine runs them when you finish: " + strings.Join(body.Commands, "; "), nil
	case "ask_human":
		var q job.Ask
		if err := json.Unmarshal(args, &q); err != nil {
			return "", err
		}
		if err := j.AskHuman(agent, q); err != nil {
			return "", err
		}
		return "Your request went to the person running the factory. End your turn now, without more tool calls; their answer comes as your next message.", nil
	case "ask_builder":
		var body struct {
			Question string `json:"question"`
		}
		json.Unmarshal(args, &body)
		if strings.TrimSpace(body.Question) == "" {
			return "", fmt.Errorf("ask a question")
		}
		return j.AskBuilder(agent, body.Question)
	case "propose_experiments":
		var p job.Proposal
		if err := json.Unmarshal(args, &p); err != nil {
			return "", err
		}
		if err := j.ProposeExperiments(agent, p); err != nil {
			return "", err
		}
		return fmt.Sprintf("Proposal saved: %d experiments. Stop here; when you finish this turn the engine starts a builder on each, on its own branch.", len(p.Experiments)), nil
	case "grade":
		var g struct {
			Experiment string `json:"experiment"`
			Score      int    `json:"score"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal(args, &g); err != nil {
			return "", err
		}
		if err := j.Grade(agent, g.Experiment, g.Score, g.Reason); err != nil {
			return "", err
		}
		return "Grade recorded.", nil
	case "verdict":
		var v job.Verdict
		if err := json.Unmarshal(args, &v); err != nil {
			return "", err
		}
		if err := j.SetVerdict(agent, v); err != nil {
			return "", err
		}
		return "Verdict recorded.", nil
	case "name_job":
		var body struct {
			Title   string `json:"title"`
			Project string `json:"project"`
		}
		if err := json.Unmarshal(args, &body); err != nil {
			return "", err
		}
		named := j.Project != ""
		if err := j.Name(agent, body.Title, body.Project); err != nil {
			return "", err
		}
		if !named && j.Project != "" {
			s.Store.SetProject(j.Repo, j.Project)
		}
		s.Store.SaveJob(j.ID, j.Issue, j.Title, j.Repo, j.Spec, j.Started)
		return fmt.Sprintf("Named: %s (project %s).", j.Title, j.Project), nil
	case "set_status":
		var body struct {
			Step string `json:"step"`
		}
		json.Unmarshal(args, &body)
		s.Bus.Emit(j.ID, agent, "agent.status", map[string]any{"step": body.Step})
		return "ok", nil
	}
	return "", fmt.Errorf("unknown tool %s", tool)
}

// app serves the map as the factory's real interface: live mode only, no
// demo. The file is shared with the art preview, which keeps the demo.
func (s *Server) app(w http.ResponseWriter, _ *http.Request) {
	page, err := os.ReadFile(filepath.Join(s.Web, "assets", "preview.html"))
	if err != nil {
		http.Error(w, "web/assets missing: run go run ./cmd/artbuild", http.StatusInternalServerError)
		return
	}
	html := strings.Replace(string(page), "<head>", `<head><base href="/assets/"><script>window.FACTORY_LIVE = true</script>`, 1)
	html = strings.Replace(html, "<title>Factory art preview</title>", "<title>Factory</title>", 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, html)
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > n {
		s = s[:n-1] + "…"
	}
	return s
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
